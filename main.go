package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxy_plugin_call(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxy_plugin_free(void*, size_t);
extern void cliproxy_plugin_shutdown(void);
*/
import "C"

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// Debug logging is opt-in. It writes full request/response bodies (which can
// contain user prompts and tool outputs) so it must never run for normal
// production traffic. Set CPA_FILTER_DEBUG to any non-empty value to enable it.
// The log file is opened lazily once and reused under a mutex to avoid the
// per-call open/close cost on hot streaming paths.
var (
	debugLogOnce    sync.Once
	debugLogMu      sync.Mutex
	debugLogFile    *os.File
	debugLogEnabled bool
)

func debugLog(format string, args ...any) {
	debugLogOnce.Do(func() {
		if os.Getenv("CPA_FILTER_DEBUG") == "" {
			return
		}
		f, err := os.OpenFile("logs/cpa-filter-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			f, err = os.OpenFile("cpa-filter-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		}
		if err == nil {
			debugLogFile = f
			debugLogEnabled = true
		}
	})
	if !debugLogEnabled || debugLogFile == nil {
		return
	}
	debugLogMu.Lock()
	fmt.Fprintf(debugLogFile, "[DEBUG] "+format+"\n", args...)
	debugLogMu.Unlock()
}

const abiVersion = 1

const (
	pluginName       = "antigravity-cloak"
	pluginVersion    = "0.6.0"
	pluginRepository = "https://github.com/monet88/antigravity-cloak"
)

// explicitClientHeader is the plugin-owned control header carrying the
// operator-declared client identity. It is consumed on the request path and
// never forwarded upstream, whether its value is valid or not.
const explicitClientHeader = "X-Cloak-Client"

// negativeClientResolution is a sentinel client id recorded under a request's
// correlation key when request-time precedence ran to completion but resolved
// no client (an invalid explicit X-Cloak-Client suppressed User-Agent evidence
// and body classification found nothing). Its presence is authoritative:
// response and stream paths must not reclassify the request from a weaker
// signal that survived after the control header was consumed. It is distinct
// from a missing session, which leaves conservative recovery available.
const negativeClientResolution = "__none__"

// userAgentEvidence lists conservative User-Agent prefixes that may resolve a
// client. Each entry is verified against active ToolMappings; an entry without
// a usable mapping (e.g. opencode before any table exists) stays inert and
// never invents a client.
var userAgentEvidence = []struct {
	prefix string
	client string
}{
	{"omp/", "oh_my_pi"},
	{"opencode/", "opencode"},
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = abiVersion
	plugin.call = (C.cliproxy_plugin_call_fn)(C.cliproxy_plugin_call)
	plugin.free_buffer = (C.cliproxy_plugin_free_fn)(C.cliproxy_plugin_free)
	plugin.shutdown = (C.cliproxy_plugin_shutdown_fn)(C.cliproxy_plugin_shutdown)
	return 0
}

//export cliproxy_plugin_call
func cliproxy_plugin_call(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeCResponse(response, mustErrorEnvelope("invalid_method", "method is required"))
		return 1
	}

	methodName := C.GoString(method)
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, code := handlePluginCall(methodName, requestBytes)
	writeCResponse(response, raw)
	return C.int(code)
}

//export cliproxy_plugin_free
func cliproxy_plugin_free(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxy_plugin_shutdown
func cliproxy_plugin_shutdown() {}

func writeCResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.malloc(C.size_t(len(raw)))
	if ptr == nil {
		return
	}
	C.memcpy(ptr, unsafe.Pointer(&raw[0]), C.size_t(len(raw)))
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func handlePluginCall(method string, request []byte) ([]byte, int) {
	switch method {
	case pluginabi.MethodPluginRegister:
		return handlePluginLifecycle(request), 0
	case pluginabi.MethodPluginReconfigure:
		return handlePluginLifecycle(request), 0
	case pluginabi.MethodRequestInterceptBefore:
		return handleRequestInterceptBefore(request), 0
	case pluginabi.MethodRequestInterceptAfter:
		return mustEnvelope(pluginapi.RequestInterceptResponse{}), 0
	case pluginabi.MethodRequestComplete:
		return handleRequestComplete(request), 0
	case pluginabi.MethodResponseInterceptAfter:
		return handleResponseIntercept(request), 0
	case pluginabi.MethodResponseInterceptStreamChunk:
		return handleStreamChunkIntercept(request), 0
	default:
		return mustErrorEnvelope("unknown_method", fmt.Sprintf("unknown method %q", method)), 0
	}
}

func handleRequestComplete(request []byte) []byte {
	var comp pluginapi.RequestCompletion
	if err := json.Unmarshal(request, &comp); err != nil {
		debugLog("handleRequestComplete: decode error %v", err)
		return mustErrorEnvelope("invalid_request", err.Error())
	}
	debugLog("handleRequestComplete: RequestID=%q Outcome=%s", comp.RequestID, comp.Outcome)
	if comp.RequestID != "" {
		globalLifecycleManager.deleteRoute(comp.RequestID)
		globalAliasPlanManager.delete(comp.RequestID)
		globalStreamManager.deleteSession("req:" + comp.RequestID)
	}
	return mustEnvelope(struct{}{})
}

func handlePluginLifecycle(request []byte) []byte {
	if len(request) > 0 {
		cfg, err := filterConfigFromLifecycleRequest(request)
		if err != nil {
			return mustErrorEnvelope("invalid_config", err.Error())
		}
		applyFilterConfig(cfg)
	}
	return mustEnvelope(registrationResponse())
}

func registrationResponse() any {
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  struct {
			ModelRouter            bool `json:"model_router"`
			Executor               bool `json:"executor"`
			RequestInterceptor     bool `json:"request_interceptor"`
			RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
			ResponseInterceptor    bool `json:"response_interceptor"`
			StreamChunkInterceptor bool `json:"response_stream_interceptor"`
		} `json:"capabilities"`
	}{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "local",
			GitHubRepository: pluginRepository,
			Logo:             "",
			ConfigFields:     configFields(),
		},
		Capabilities: struct {
			ModelRouter            bool `json:"model_router"`
			Executor               bool `json:"executor"`
			RequestInterceptor     bool `json:"request_interceptor"`
			RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
			ResponseInterceptor    bool `json:"response_interceptor"`
			StreamChunkInterceptor bool `json:"response_stream_interceptor"`
		}{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
			ResponseInterceptor:    true,
			StreamChunkInterceptor: true,
		},
	}
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{
			Name:        "use_default_keywords",
			Type:        pluginapi.ConfigFieldTypeBoolean,
			Description: "Enable the built-in coding software and agent keyword preset.",
		},
		{
			Name:        "custom_mappings",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Additional case-insensitive system-field rewrite mappings, for example Cursor: Antigravity.",
		},
		{
			Name:        "tool_mappings",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Custom tool name mappings per client. Keys: client name (claude_code, codex, oh_my_pi). Values: map of original_tool_name → antigravity_target_name. Overrides defaults for matching keys.",
		},
		{
			Name:        "model_prefixes",
			Type:        pluginapi.ConfigFieldTypeArray,
			Description: "Only cloak when the upstream/requested model starts with one of these prefixes, for example agy/. Leave empty to cloak for every model (matches all providers).",
		},
	}
}

// normalizeSourceFormat maps the SDK's source-format identifiers onto the two
// branch families the interceptors understand. The proxy emits "claude" and
// "antigravity" (both Anthropic-shaped) and "codex"/"openai-response" (both
// OpenAI-shaped); collapse them so extractToolNames/cloak/uncloak hit the
// correct branch instead of silently no-oping on an unrecognized string.
func normalizeSourceFormat(sourceFormat string) string {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "anthropic", "claude", "antigravity":
		return "anthropic"
	case "openai", "openai-response", "codex":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(sourceFormat))
	}
}

// modelAllowsCloak reports whether cloaking should run for the current model.
// When ModelPrefixes is empty (the default), cloaking runs for every model so
// behavior matches the upstream "cloak by client" design. When non-empty,
// cloaking runs only if the upstream Model or the client RequestedModel starts
// with one of the configured prefixes (e.g. "agy/"). Both handlers and the
// stream path share this gate so request/response/stream stay consistent.
func modelAllowsCloak(model, requestedModel string) bool {
	prefixes := activeFilterConfig().ModelPrefixes
	if len(prefixes) == 0 {
		return true
	}
	candidates := [...]string{strings.TrimSpace(model), strings.TrimSpace(requestedModel)}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.HasPrefix(candidate, prefix) {
				return true
			}
		}
	}
	return false
}
func isAGYRoute(model, requestedModel string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), "agy/") ||
		strings.HasPrefix(strings.TrimSpace(requestedModel), "agy/")
}

func protected503Response(resp pluginapi.RequestInterceptResponse) []byte {
	resp.Terminate = true
	resp.StatusCode = http.StatusServiceUnavailable // 503
	resp.ResponseHeaders = http.Header{
		"Content-Type": []string{"application/json"},
	}
	resp.ResponseBody = []byte(`{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}`)
	return mustEnvelope(resp)
}

func toolCloak503Response(resp pluginapi.RequestInterceptResponse) []byte {
	resp.Terminate = true
	resp.StatusCode = http.StatusServiceUnavailable
	resp.ResponseHeaders = http.Header{
		"Content-Type": []string{"application/json"},
	}
	resp.ResponseBody = []byte("{\"error\":{\"code\":\"tool_cloak_required\",\"message\":\"Request could not be safely cloaked.\"}}")
	return mustEnvelope(resp)
}

func decodeStrictProtectedJSON(data []byte) (map[string]any, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, false
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return rootMap, true
}

type protectedDeclInfo struct {
	originalFullName string
	prefix           string
	base             string
	finalBase        string
	transformed      bool
}

func collectRequestSourceIdentitiesWithDeclared(root map[string]any, format string) ([]string, []string, error) {
	var sources []string
	var declared []string

	// 1. Declarations in tools[]
	if toolsValue, exists := root["tools"]; exists && toolsValue != nil {
		tools, ok := toolsValue.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("unsupported tools representation")
		}
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("unsupported tool declaration")
			}
			var name string
			if format == "openai" {
				if typ, hasType := tool["type"].(string); hasType && typ != "" && typ != "function" {
					return nil, nil, fmt.Errorf("unsupported openai tool type %q", typ)
				}
				fn, ok := tool["function"].(map[string]any)
				if !ok {
					return nil, nil, fmt.Errorf("unsupported openai tool declaration")
				}
				name, _ = fn["name"].(string)
			} else {
				name, _ = tool["name"].(string)
			}
			if name == "" {
				return nil, nil, fmt.Errorf("tool declaration is missing a name")
			}
			sources = append(sources, name)
			declared = append(declared, name)
		}
	}

	// 2. History in messages[]
	if msgsValue, exists := root["messages"]; exists && msgsValue != nil {
		if msgs, ok := msgsValue.([]any); ok {
			for _, m := range msgs {
				msg, ok := m.(map[string]any)
				if !ok {
					continue
				}
				if format == "openai" {
					if calls, ok := msg["tool_calls"].([]any); ok {
						for _, c := range calls {
							if call, ok := c.(map[string]any); ok {
								if fn, ok := call["function"].(map[string]any); ok {
									if name, ok := fn["name"].(string); ok && name != "" {
										sources = append(sources, name)
									}
								}
							}
						}
					}
					if role, ok := msg["role"].(string); ok && role == "tool" {
						if name, ok := msg["name"].(string); ok && name != "" {
							sources = append(sources, name)
						}
					}
				} else if format == "anthropic" {
					if contents, ok := msg["content"].([]any); ok {
						for _, cnt := range contents {
							if block, ok := cnt.(map[string]any); ok {
								if t, ok := block["type"].(string); ok && t == "tool_use" {
									if name, ok := block["name"].(string); ok && name != "" {
										sources = append(sources, name)
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// 3. Named tool_choice
	if tcValue, exists := root["tool_choice"]; exists && tcValue != nil {
		if tc, ok := tcValue.(map[string]any); ok {
			if format == "openai" {
				if fn, ok := tc["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok && name != "" {
						sources = append(sources, name)
					}
				}
			} else if format == "anthropic" {
				if name, ok := tc["name"].(string); ok && name != "" {
					sources = append(sources, name)
				}
			}
		}
	}

	return sources, declared, nil
}

func collectRequestSourceIdentities(root map[string]any, format string) ([]string, error) {
	sources, _, err := collectRequestSourceIdentitiesWithDeclared(root, format)
	return sources, err
}

func inspectAndValidateProtectedTools(rootMap map[string]any, format string, mergedCloak map[string]string) ([]protectedDeclInfo, error) {
	names, err := collectRequestSourceIdentities(rootMap, format)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}

	seenNames := make(map[string]bool, len(names))
	var uniqueNames []string
	for _, name := range names {
		if !seenNames[name] {
			seenNames[name] = true
			uniqueNames = append(uniqueNames, name)
		}
	}

	var decls []protectedDeclInfo
	for _, name := range uniqueNames {
		prefix, base, resolvedBase := resolveOMPSourceIdentity(name)
		targetBase, isMapped := mergedCloak[resolvedBase]
		var finalBase string
		if isMapped {
			finalBase = targetBase
		} else {
			finalBase = fallbackAliasForSource(base)
		}
		decls = append(decls, protectedDeclInfo{
			originalFullName: name,
			prefix:           prefix,
			base:             base,
			finalBase:        finalBase,
			transformed:      true,
		})
	}

	byFinalBase := make(map[string][]protectedDeclInfo)
	for _, d := range decls {
		byFinalBase[d.finalBase] = append(byFinalBase[d.finalBase], d)
	}
	for fb, group := range byFinalBase {
		if len(group) > 1 {
			return nil, fmt.Errorf("declaration collision for final base identity %q", fb)
		}
	}
	return decls, nil
}

func cloakProtectedToolNames(rootMap map[string]any, cloakTable map[string]string, sourceFormat string) bool {
	changed := false

	if toolsRaw, ok := rootMap["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			tMap, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if sourceFormat == "openai" {
				if fn, ok := tMap["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok {
						prefix, base := splitToolNamespace(name)
						if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
							fn["name"] = prefix + target
							changed = true
						}
					}
				}
			} else if sourceFormat == "anthropic" {
				if name, ok := tMap["name"].(string); ok {
					prefix, base := splitToolNamespace(name)
					if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
						tMap["name"] = prefix + target
						changed = true
					}
				}
			}
		}
	}

	if msgsRaw, ok := rootMap["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			msg, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}
			if sourceFormat == "openai" {
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, cRaw := range calls {
						call, ok := cRaw.(map[string]any)
						if !ok {
							continue
						}
						fn, ok := call["function"].(map[string]any)
						if !ok {
							continue
						}
						if name, ok := fn["name"].(string); ok {
							prefix, base := splitToolNamespace(name)
							if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
								fn["name"] = prefix + target
								changed = true
							}
						}
					}
				}
				if msg["role"] == "tool" {
					if name, ok := msg["name"].(string); ok {
						prefix, base := splitToolNamespace(name)
						if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
							msg["name"] = prefix + target
							changed = true
						}
					}
				}
			} else if sourceFormat == "anthropic" {
				if contents, ok := msg["content"].([]any); ok {
					for _, cntRaw := range contents {
						cnt, ok := cntRaw.(map[string]any)
						if !ok {
							continue
						}
						if cnt["type"] == "tool_use" {
							if name, ok := cnt["name"].(string); ok {
								prefix, base := splitToolNamespace(name)
								if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
									cnt["name"] = prefix + target
									changed = true
								}
							}
						}
					}
				}
			}
		}
	}

	if tc, ok := rootMap["tool_choice"].(map[string]any); ok {
		if sourceFormat == "openai" {
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					prefix, base := splitToolNamespace(name)
					if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
						fn["name"] = prefix + target
						changed = true
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			if name, ok := tc["name"].(string); ok {
				prefix, base := splitToolNamespace(name)
				if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
					tc["name"] = prefix + target
					changed = true
				}
			}
		}
	}

	return changed
}

func validateProtectedPostTransform(rootMap map[string]any, sourceFormat string, uncloakedSources map[string]bool) bool {
	hasUncloakedSource := func(name string) bool {
		if uncloakedSources[name] {
			return true
		}
		_, base, resolvedBase := resolveOMPSourceIdentity(name)
		return uncloakedSources[base] || uncloakedSources[resolvedBase]
	}

	if toolsRaw, ok := rootMap["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if fn, ok := tMap["function"].(map[string]any); ok {
						if name, ok := fn["name"].(string); ok {
							if hasUncloakedSource(name) {
								return false
							}
						}
					}
				} else if sourceFormat == "anthropic" {
					if name, ok := tMap["name"].(string); ok {
						if hasUncloakedSource(name) {
							return false
						}
					}
				}
			}
		}
	}

	if msgsRaw, ok := rootMap["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			if msg, ok := mRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if calls, ok := msg["tool_calls"].([]any); ok {
						for _, cRaw := range calls {
							if call, ok := cRaw.(map[string]any); ok {
								if fn, ok := call["function"].(map[string]any); ok {
									if name, ok := fn["name"].(string); ok {
										if hasUncloakedSource(name) {
											return false
										}
									}
								}
							}
						}
					}
					if msg["role"] == "tool" {
						if name, ok := msg["name"].(string); ok {
							if hasUncloakedSource(name) {
								return false
							}
						}
					}
				} else if sourceFormat == "anthropic" {
					if contents, ok := msg["content"].([]any); ok {
						for _, cntRaw := range contents {
							if cnt, ok := cntRaw.(map[string]any); ok {
								if cnt["type"] == "tool_use" {
									if name, ok := cnt["name"].(string); ok {
										if hasUncloakedSource(name) {
											return false
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	if tc, ok := rootMap["tool_choice"].(map[string]any); ok {
		if sourceFormat == "openai" {
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					if hasUncloakedSource(name) {
						return false
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			if name, ok := tc["name"].(string); ok {
				if hasUncloakedSource(name) {
					return false
				}
			}
		}
	}

	return true
}

const protectedBrandSentinel = "\x00__ANTIGRAVITY_PROTECTED_OMP_BRAND__\x00"

var mandatoryProtectedOMPAliases = []string{
	"Oh My Pi",
	"oh-my-pi",
	"omp",
}

// ompSchemePreserve is the client's own virtual-device scheme, an operational
// address rather than brand prose. A rule that masks the bare alias carries it
// as an exclusion, so "omp://xd/mcp__server_tool" reaches the model
// byte-for-byte while the same word in prose is still masked.
const ompSchemePreserve = "://"

// ompIdentityLines rewrite Oh My Pi's opening sentence whole, rather than
// substituting the bare alias inside it. The harness ships the sentence
// verbatim ("You are omp's trusted coding assistant."), so the bare-alias pass
// would otherwise turn it into "You are Antigravity's trusted coding
// assistant." and leave a sentence that names no product and no vendor. These
// must run BEFORE mandatoryProtectedOMPAliases: that pass swaps the "omp" for
// the sentinel, after which the sentence no longer exists to match.
var ompIdentityLines = []rewriteMapping{
	{Match: "You are omp's trusted coding assistant.", Replacement: antigravityIdentity},
}

func isOMPAlias(match string) bool {
	m := strings.ToLower(strings.TrimSpace(match))
	return m == "omp" || m == "oh-my-pi" || m == "oh my pi"
}

func rewriteProtectedBrandText(text string, cfg *filterConfig, client string) (string, bool) {
	if text == "" {
		return text, false
	}
	current := text
	lowerCurrent := strings.ToLower(current)
	changed := false
	// Whole-sentence first: the sentinel pass below swaps the "omp" inside
	// these lines for the sentinel, after which the sentence cannot match.
	if client == "oh_my_pi" {
		for _, m := range ompIdentityLines {
			if next, rep := replaceInsensitive(current, m.Match, m.Replacement); rep {
				current = next
				lowerCurrent = strings.ToLower(current)
				changed = true
			}
		}
	}
	for _, alias := range mandatoryProtectedOMPAliases {
		if !strings.Contains(lowerCurrent, strings.ToLower(alias)) {
			continue
		}
		// The exclusion keeps a real "omp://" internal URI byte-for-byte: the
		// scheme is the client's own virtual-device address, so masking it
		// would hand the model a URI that resolves nowhere.
		aliasRule := rewriteMapping{
			Match:         alias,
			Replacement:   protectedBrandSentinel,
			NotFollowedBy: ompSchemePreserve,
		}
		if next, rep := replaceInsensitiveRule(current, aliasRule, true); rep {
			current = next
			lowerCurrent = strings.ToLower(current)
			changed = true
		}
	}

	// Client scoping is applied here too: this walker predates applicableMappings
	// and used to ignore rewriteMapping.Client, which would let a claude_code or
	// codex rule fire on a protected Oh My Pi request.
	inScope := func(m rewriteMapping) bool {
		return (m.Client == "" || m.Client == client) && !isOMPAlias(m.Match)
	}
	var nonOMPMappings []rewriteMapping
	if cfg.UseDefaultKeywords {
		for _, m := range brandMappingsFor(client) {
			if inScope(m) {
				nonOMPMappings = append(nonOMPMappings, m)
			}
		}
	}
	for _, m := range cfg.CustomMappings {
		if inScope(m) {
			nonOMPMappings = append(nonOMPMappings, m)
		}
	}
	normalized := normalizeMappings(nonOMPMappings)
	for _, m := range normalized {
		if !strings.Contains(lowerCurrent, m.Match) {
			continue
		}
		if next, rep := replaceInsensitiveRule(current, m, true); rep {
			current = next
			lowerCurrent = strings.ToLower(current)
			changed = true
		}
	}

	if strings.Contains(current, protectedBrandSentinel) {
		current = strings.ReplaceAll(current, protectedBrandSentinel, "Antigravity")
	}

	return current, changed
}

func rewriteProtectedBrandValue(value any, cfg *filterConfig, client string) any {
	switch typed := value.(type) {
	case string:
		next, _ := rewriteProtectedBrandText(typed, cfg, client)
		return next
	case map[string]any:
		for k, v := range typed {
			typed[k] = rewriteProtectedBrandValue(v, cfg, client)
		}
		return typed
	case []any:
		for i, v := range typed {
			typed[i] = rewriteProtectedBrandValue(v, cfg, client)
		}
		return typed
	default:
		return value
	}
}

func rewriteProtectedBrand(rootMap map[string]any, sourceFormat, client string) {
	cfg := activeFilterConfig()

	if sysVal, ok := rootMap["system"]; ok {
		rootMap["system"] = rewriteProtectedBrandValue(sysVal, cfg, client)
	}

	if msgsRaw, ok := rootMap["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			if msg, ok := mRaw.(map[string]any); ok {
				if role, ok := msg["role"].(string); ok && role == "system" {
					if content, exists := msg["content"]; exists {
						msg["content"] = rewriteProtectedBrandValue(content, cfg, client)
					}
				}
			}
		}
	}

	if toolsRaw, ok := rootMap["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if fn, ok := tMap["function"].(map[string]any); ok {
						if desc, ok := fn["description"].(string); ok {
							if next, c := rewriteProtectedBrandText(desc, cfg, client); c {
								fn["description"] = next
							}
						}
					}
				} else if sourceFormat == "anthropic" {
					if desc, ok := tMap["description"].(string); ok {
						if next, c := rewriteProtectedBrandText(desc, cfg, client); c {
							tMap["description"] = next
						}
					}
				}
			}
		}
	}

	if cachedCloak := cfg.cloakRegexCache["oh_my_pi"]; cachedCloak != nil {
		if sysVal, ok := rootMap["system"]; ok {
			next, sysToolChanged := replaceToolNamesInValue(sysVal, cachedCloak)
			if sysToolChanged {
				rootMap["system"] = next
			}
		}
		if msgsRaw, ok := rootMap["messages"].([]any); ok {
			for _, mRaw := range msgsRaw {
				if msg, ok := mRaw.(map[string]any); ok {
					if role, ok := msg["role"].(string); ok && role == "system" {
						if content, exists := msg["content"]; exists {
							toolNext, toolChanged := replaceToolNamesInValue(content, cachedCloak)
							if toolChanged {
								msg["content"] = toolNext
							}
						}
					}
				}
			}
		}
	}
}

// sanitizeSystemConventions replaces only the fixed OMP wrapper tags.
func sanitizeSystemConventions(text string) (string, bool) {
	next := strings.ReplaceAll(text, "<system-conventions>", "<conventions>")
	next = strings.ReplaceAll(next, "</system-conventions>", "</conventions>")
	return next, next != text
}

// sanitizeSystemConventionsContent visits prompt text, not block metadata or
// arbitrary nested JSON such as tool arguments and image sources.
func sanitizeSystemConventionsContent(value any) any {
	switch content := value.(type) {
	case string:
		next, _ := sanitizeSystemConventions(content)
		return next
	case []any:
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok || (block["type"] != "text" && block["type"] != "input_text") {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if next, changed := sanitizeSystemConventions(text); changed {
					block["text"] = next
				}
			}
		}
	}
	return value
}

func sanitizeProtectedSystemConventions(rootMap map[string]any) {
	if system, ok := rootMap["system"]; ok {
		rootMap["system"] = sanitizeSystemConventionsContent(system)
	}
	if messages, ok := rootMap["messages"].([]any); ok {
		for _, raw := range messages {
			msg, ok := raw.(map[string]any)
			if !ok || (msg["role"] != "system" && msg["role"] != "developer") {
				continue
			}
			if content, ok := msg["content"]; ok {
				msg["content"] = sanitizeSystemConventionsContent(content)
			}
		}
	}
}

func handleProtectedAGY(req *pluginapi.RequestInterceptRequest, resp pluginapi.RequestInterceptResponse, format string) []byte {
	if req.RequestID == "" {
		debugLog("handleProtectedAGY: missing RequestID")
		return protected503Response(resp)
	}

	if format != "openai" && format != "anthropic" {
		debugLog("handleProtectedAGY: unsupported source format %q", format)
		return protected503Response(resp)
	}

	rootMap, ok := decodeStrictProtectedJSON(req.Body)
	if !ok {
		debugLog("handleProtectedAGY: strict JSON decode failed")
		return protected503Response(resp)
	}
	expectedChoiceCount := 1
	if n, valid := jsonIndexValue(rootMap["n"]); valid && n > 1 {
		expectedChoiceCount = n
	}

	effective := activeFilterConfig().ToolMappings["oh_my_pi"]
	if len(effective) < len(canonicalOMPSafeMappingSet) {
		debugLog("handleProtectedAGY: effective mapping count below canonical minimum: %d < %d", len(effective), len(canonicalOMPSafeMappingSet))
		return protected503Response(resp)
	}
	for orig, target := range canonicalOMPSafeMappingSet {
		if effective[orig] != target {
			debugLog("handleProtectedAGY: canonical mapping mismatch for %q: got %q, want %q", orig, effective[orig], target)
			return protected503Response(resp)
		}
	}

	// Build the merged cloak table: start from shared aliases, overlay operator config,
	// and ensure canonical nine are immutable.
	mergedCloak := make(map[string]string, len(canonicalOMPSafeMappingSet)+len(ompSharedAliases)+len(effective))
	for k, v := range ompSharedAliases {
		mergedCloak[k] = v
	}
	for k, v := range effective {
		if v == "" {
			debugLog("handleProtectedAGY: empty mapping target for %q", k)
			return protected503Response(resp)
		}
		mergedCloak[k] = v
	}
	for k, v := range canonicalOMPSafeMappingSet {
		mergedCloak[k] = v
	}

	decls, collisionErr := inspectAndValidateProtectedTools(rootMap, format, mergedCloak)
	if collisionErr != nil {
		debugLog("handleProtectedAGY: declaration collision: %v", collisionErr)
		return protected503Response(resp)
	}

	// Build the extended cloak table: start from merged (canonical + shared + config),
	// then add deterministic fallback aliases for unknown declarations.
	extendedCloak := make(map[string]string, len(mergedCloak)+len(decls))
	for k, v := range mergedCloak {
		extendedCloak[k] = v
	}
	for _, d := range decls {
		extendedCloak[d.base] = d.finalBase
		if strings.HasPrefix(d.base, "_") {
			candidate := strings.TrimPrefix(d.base, "_")
			if _, exists := extendedCloak[candidate]; !exists {
				extendedCloak[candidate] = d.finalBase
			}
		}
	}

	activeReverse := make(map[string]string, len(decls))
	for _, d := range decls {
		target := d.finalBase
		transformedFullName := d.prefix + target
		activeReverse[transformedFullName] = d.originalFullName
	}

	var protectedCachedUncloak *cachedUncloakPattern
	if len(activeReverse) > 0 {
		targets := make([]string, 0, len(activeReverse))
		lookup := make(map[string]string, len(activeReverse))
		for targetFull, origFull := range activeReverse {
			targets = append(targets, regexp.QuoteMeta(targetFull))
			lookup[targetFull] = origFull
		}
		pattern := `"name"\s*:\s*"(` + strings.Join(targets, "|") + `)"`
		if re, err := regexp.Compile(pattern); err == nil {
			protectedCachedUncloak = &cachedUncloakPattern{
				re:        re,
				lookup:    lookup,
				exactOnly: true,
			}
		}
	}

	cloakProtectedToolNames(rootMap, extendedCloak, format)
	rewriteProtectedBrand(rootMap, format, "oh_my_pi")
	sanitizeProtectedSystemConventions(rootMap)

	// Machine-generated prose has to name the request-scoped aliases, not the
	// client's own tool names. extendedCloak IS the effective declaration map
	// for this request: canonical, shared (wp_*) and the deterministic
	// wp_ext_<hash> fallbacks, so a tool description that says "hub" reaches
	// the model as its alias and the OMP source identity never appears in a
	// system/developer/tool-description surface (Issue #51). Brand mappings
	// are omitted: that pass already ran, and the alias-plan clients run it
	// the same way.
	proseCloak := &cachedCloakPatterns{
		cloakTable: extendedCloak,
		identRe:    buildCloakIdentRe(extendedCloak),
		ambigRe:    buildCloakAmbiguousRe(extendedCloak),
	}
	rewriteToolDescriptions(rootMap, nil, proseCloak, format)
	rewriteConversationContent(rootMap, nil, proseCloak)
	if sysVal, ok := rootMap["system"]; ok {
		if next, c := replaceToolNamesInValue(sysVal, proseCloak); c {
			rootMap["system"] = next
		}
	}

	uncloakedSources := make(map[string]bool)
	for k := range canonicalOMPSafeMappingSet {
		uncloakedSources[k] = true
	}
	for k := range ompSharedAliases {
		uncloakedSources[k] = true
	}
	for k := range effective {
		uncloakedSources[k] = true
	}
	for _, d := range decls {
		uncloakedSources[d.originalFullName] = true
		uncloakedSources[d.base] = true
		_, _, res := resolveOMPSourceIdentity(d.base)
		uncloakedSources[res] = true
	}

	if !validateProtectedPostTransform(rootMap, format, uncloakedSources) {
		debugLog("handleProtectedAGY: post-transform validation failed")
		return protected503Response(resp)
	}

	canonicalBytes, err := safeMarshal(rootMap)
	if err != nil {
		debugLog("handleProtectedAGY: safeMarshal failed: %v", err)
		return protected503Response(resp)
	}
	resp.Body = canonicalBytes

	globalLifecycleManager.setRoute(req.RequestID, &explicitOMPRouteState{
		routeKind:               routeKindProtectedAGY,
		client:                  "oh_my_pi",
		activeReverse:           activeReverse,
		cachedUncloak:           protectedCachedUncloak,
		brandRestorationEnabled: true,
		expected:                expectedChoiceCount,
	})
	globalStreamManager.resetSession("req:"+req.RequestID, "oh_my_pi", protectedCachedUncloak, expectedChoiceCount)
	return mustEnvelope(resp)
}

func handleRequestInterceptBefore(request []byte) []byte {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return mustErrorEnvelope("invalid_request", fmt.Sprintf("decode request.intercept_before request: %v", err))
	}

	resp := pluginapi.RequestInterceptResponse{}
	marker := parseExplicitClientMarker(req.Headers)
	if marker.present {
		resp.ClearHeaders = marker.matchedKeys
		resp.Headers = filteredHeaders(req.Headers, marker.matchedKeys)
	}

	format := normalizeSourceFormat(req.SourceFormat)
	isAGY := isAGYRoute(req.Model, req.RequestedModel)
	debugLog("handleRequestInterceptBefore: SourceFormat=%s (normalized=%s) ToFormat=%q Model=%q RequestedModel=%q isAGY=%t marker=%+v",
		req.SourceFormat, format, req.ToFormat, req.Model, req.RequestedModel, isAGY, marker)

	if marker.isConflict && marker.conflictContainsOMP {
		if isAGY {
			debugLog("handleRequestInterceptBefore: conflicting marker containing OMP on AGY route -> exact 503")
			return protected503Response(resp)
		}
		debugLog("handleRequestInterceptBefore: conflicting marker containing OMP on non-AGY route -> durable bypass")
		if req.RequestID != "" {
			globalLifecycleManager.setRoute(req.RequestID, &explicitOMPRouteState{
				routeKind: routeKindExplicitOMPNonAGYBypass,
				client:    "oh_my_pi",
			})
		}
		return mustEnvelope(resp)
	}

	if !marker.isConflict && marker.isOMP {
		if isAGY {
			debugLog("handleRequestInterceptBefore: explicit OMP on AGY route -> ProtectedAGY admission")
			return handleProtectedAGY(&req, resp, format)
		}
		debugLog("handleRequestInterceptBefore: explicit OMP on non-AGY route -> durable bypass")
		if req.RequestID != "" {
			globalLifecycleManager.setRoute(req.RequestID, &explicitOMPRouteState{
				routeKind: routeKindExplicitOMPNonAGYBypass,
				client:    "oh_my_pi",
			})
		}
		return mustEnvelope(resp)
	}

	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleRequestInterceptBefore: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return mustEnvelope(resp)
	}

	forcedClient := ""
	if marker.present {
		if marker.valid {
			forcedClient = marker.client
		} else {
			debugLog("handleRequestInterceptBefore: invalid explicit client %q, falling back to body detection", marker.client)
		}
	} else if uaClient, ok := resolveUserAgentClient(req.Headers); ok {
		forcedClient = uaClient
		debugLog("handleRequestInterceptBefore: UA evidence client=%q", uaClient)
	}

	// Resolve the effective client. When forcedClient is set and has a table,
	// use it directly; otherwise fall back to body-based detection.
	effectiveClient := forcedClient
	if effectiveClient != "" && len(effectiveCloakTable(effectiveClient)) == 0 {
		effectiveClient = ""
	}
	if effectiveClient == "" {
		var reqRootAny any
		if err := safeUnmarshal(req.Body, &reqRootAny); err == nil {
			if rootMap, ok := reqRootAny.(map[string]any); ok {
				toolNames := extractToolNames(rootMap, format)
				effectiveClient = detectClient(toolNames)
			}
		}
	}

	// Alias-plan path: clients that declare a variable tool surface route
	// through the request-scoped alias plan machinery. This gives them
	// fail-closed admission, deterministic fallback aliases for unknown/MCP
	// declarations, and exact per-request reversal. Missing correlation is
	// rejected (parent #32 fail-closed requirement) rather than falling back
	// to the legacy partial-cloak path.
	if effectiveClient != "" && clientUsesAliasPlan(effectiveClient) {
		if req.RequestID == "" {
			debugLog("handleRequestInterceptBefore: alias plan client=%s with missing RequestID -> 503", effectiveClient)
			return toolCloak503Response(resp)
		}
		preferred := sharedAliasesFor(effectiveClient)
		plan, rejection := admitRequestAliasPlanOrReject(&req, resp, effectiveClient, preferred)
		if rejection != nil {
			debugLog("handleRequestInterceptBefore: alias plan rejected client=%s", effectiveClient)
			return rejection
		}
		if plan != nil {
			// The plan's forward map is the sole cloak authority for this
			// request: it contains static + shared + fallback mappings for
			// every declared tool. Parse once, cloak tools, rewrite brand,
			// marshal once.
			var planRoot any
			if err := safeUnmarshal(req.Body, &planRoot); err != nil {
				debugLog("handleRequestInterceptBefore: alias plan body parse failed: %v", err)
				return toolCloak503Response(resp)
			}
			rootMap, ok := planRoot.(map[string]any)
			if !ok {
				debugLog("handleRequestInterceptBefore: alias plan body is not an object")
				return toolCloak503Response(resp)
			}

			changed := false
			if len(plan.forward) > 0 {
				toolCloaked := cloakToolNames(rootMap, plan.forward, format)
				changed = changed || toolCloaked
			}

			// Brand rewriting (same steps as rewriteRequestBodyWithClient).
			cfg := activeFilterConfig()
			mappings := effectiveMappings(cfg, effectiveClient)
			rewritten, sysChanged := rewriteSystemFields(rootMap, mappings)
			rootMap = rewritten.(map[string]any)
			changed = changed || sysChanged

			// Build a temporary cachedCloakPatterns from the plan's declared forward
			// table for text-level tool-name replacement in descriptions and
			// system messages. Only tools declared in the current tools[] surface
			// have replacement authority in prose/system fields.
			planCloakCache := &cachedCloakPatterns{
				cloakTable: plan.declaredForward,
				identRe:    buildCloakIdentRe(plan.declaredForward),
				ambigRe:    buildCloakAmbiguousRe(plan.declaredForward),
			}

			descChanged := rewriteToolDescriptions(rootMap, mappings, planCloakCache, format)
			changed = changed || descChanged

			sysMsgChanged := rewriteConversationContent(rootMap, mappings, planCloakCache)
			changed = changed || sysMsgChanged

			if sysVal, ok := rootMap["system"]; ok {
				next, sysToolChanged := replaceToolNamesInValue(sysVal, planCloakCache)
				if sysToolChanged {
					rootMap["system"] = next
					changed = true
				}
			}

			debugLog("handleRequestInterceptBefore: alias plan rewritten=%t client=%s", changed, effectiveClient)
			if !changed {
				return mustEnvelope(resp)
			}
			raw, err := safeMarshal(rootMap)
			if err != nil {
				debugLog("handleRequestInterceptBefore: alias plan marshal failed: %v", err)
				return toolCloak503Response(resp)
			}
			resp.Body = raw
			return mustEnvelope(resp)
		}
	}

	// Legacy path: clients without alias-plan support.
	body, rewritten, client := rewriteRequestBodyWithClient(req.Body, format, forcedClient)
	debugLog("handleRequestInterceptBefore: rewritten=%t client=%s Body=%s", rewritten, client, string(body))
	if req.RequestID != "" {
		if client != "" {
			cached := requestScopedUncloakPattern(client, req.Body, format)
			if cached != nil && cached.re != nil {
				globalStreamManager.resetSession("req:"+req.RequestID, client, cached, requestChoiceCount(req.Body))
			}
		} else if marker.present && !marker.valid {
			debugLog("handleRequestInterceptBefore: negative client resolution recorded for RequestID=%s", req.RequestID)
			globalStreamManager.resetSession("req:"+req.RequestID, negativeClientResolution, nil, requestChoiceCount(req.Body))
		}
	}
	if !rewritten {
		return mustEnvelope(resp)
	}
	resp.Body = body
	return mustEnvelope(resp)
}

func handleResponseIntercept(request []byte) []byte {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return mustErrorEnvelope("invalid_request", err.Error())
	}

	format := normalizeSourceFormat(req.SourceFormat)
	debugLog("handleResponseIntercept: SourceFormat=%s (normalized=%s) RequestBody=%s Body=%s RequestID=%s",
		req.SourceFormat, format, string(req.RequestBody), string(req.Body), req.RequestID)

	if req.RequestID != "" {
		if route := globalLifecycleManager.getRoute(req.RequestID); route != nil {
			if route.routeKind == routeKindExplicitOMPNonAGYBypass {
				debugLog("handleResponseIntercept: correlated ExplicitOMPNonAGYBypass -> zero mutation")
				return mustEnvelope(pluginapi.ResponseInterceptResponse{})
			}
			if route.routeKind == routeKindProtectedAGY {
				if route.malformed || route.client != "oh_my_pi" {
					debugLog("invariant violation: malformed ProtectedAGY route state for RequestID=%s", req.RequestID)
					return mustEnvelope(pluginapi.ResponseInterceptResponse{})
				}
				modified := req.Body
				changed := false
				if len(route.activeReverse) > 0 {
					if m, c := uncloakResponseBodyExact(req.Body, route.activeReverse, format); c {
						modified = m
						changed = true
					}
				}
				if route.brandRestorationEnabled {
					if rev, c := reverseBrandInResponseBody(modified, format, route.client); c {
						modified = rev
						changed = true
					}
				}
				if !changed {
					return mustEnvelope(pluginapi.ResponseInterceptResponse{})
				}
				return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
			}
		}
		if plan := globalAliasPlanManager.get(req.RequestID); plan != nil {
			// A pinned plan is the sole reverse authority for tool names. If
			// it has no match, do not fall through to global/static inversion:
			// a target untouched by this request must remain untouched.
			modified, uncloaked := uncloakResponseBodyExact(req.Body, plan.reverse, plan.sourceFormat)
			if !uncloaked {
				modified = req.Body
			}
			// Brand text introduced by the request path is restored here
			// independently of the tool-name plan: none of those tokens can
			// appear in plan.reverse, so the two authorities never collide.
			if rev, c := reverseCloakedBrandBody(modified, plan.client); c {
				return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: rev})
			}
			if uncloaked {
				return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
			}
			return mustEnvelope(pluginapi.ResponseInterceptResponse{})
		}
		// RequestID is present, but neither route nor alias plan exists.
		// For alias-plan clients (Claude Code, Codex), full-declaration restoration
		// requires pinned request authority. If authority is missing (e.g. after
		// request.complete cleanup or missing plan), it MUST pass through rather
		// than guess or reverse static AGY targets (Issue #39).
		if sess := globalStreamManager.getSession("req:" + req.RequestID); sess != nil && clientUsesAliasPlan(sess.client) {
			debugLog("handleResponseIntercept: alias-plan client=%q with missing authority RequestID=%s -> pass-through", sess.client, req.RequestID)
			return mustEnvelope(pluginapi.ResponseInterceptResponse{})
		}
	}

	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleResponseIntercept: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}

	if marker := parseExplicitClientMarker(req.RequestHeaders); marker.present && clientUsesAliasPlan(marker.client) {
		debugLog("handleResponseIntercept: alias-plan explicit marker=%q without pinned authority -> pass-through", marker.client)
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	var client string
	var uncloakTable map[string]string
	correlated := false
	if req.RequestID != "" {
		if sess := globalStreamManager.getSession("req:" + req.RequestID); sess != nil && sess.client != "" {
			correlated = true
			if sess.client == negativeClientResolution {
				debugLog("handleResponseIntercept: negative client resolution authoritative for RequestID=%s", req.RequestID)
				return mustEnvelope(pluginapi.ResponseInterceptResponse{})
			}
			client = sess.client
			// Reuse the reverse the request interceptor derived from the RAW
			// client body. The executed body below no longer carries source
			// names, so re-deriving the table from it cannot tell an AGY
			// target the client declared natively from one the cloak created.
			if sess.cached != nil && len(sess.cached.lookup) > 0 {
				uncloakTable = sess.cached.lookup
			} else {
				uncloakTable = requestScopedUncloakTable(sess.client, detectionRequestBody(req.OriginalRequest, req.RequestBody), format)
			}
		}
	}
	if !correlated && uncloakTable == nil {
		if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
			uncloakTable = requestScopedUncloakTable(uaClient, detectionRequestBody(req.OriginalRequest, req.RequestBody), format)
			if client == "" {
				client = uaClient
			}
			debugLog("handleResponseIntercept: UA evidence client=%q", uaClient)
		}
	}
	if !correlated && uncloakTable == nil {
		var detectedClient string
		uncloakTable, detectedClient = buildUncloakTable(detectionRequestBody(req.OriginalRequest, req.RequestBody), format)
		if client == "" {
			client = detectedClient
		}
	}
	debugLog("handleResponseIntercept: client=%s uncloakTable=%v", client, uncloakTable)
	if uncloakTable == nil && client != "oh_my_pi" {
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}

	modified := req.Body
	changed := false
	if uncloakTable != nil {
		if m, c := uncloakResponseBody(req.Body, uncloakTable, format); c {
			modified = m
			changed = true
		}
	}
	if client == "oh_my_pi" {
		if rev, c := reverseBrandInResponseBody(modified, format, client); c {
			modified = rev
			changed = true
		}
	}
	if client != "oh_my_pi" {
		if rev, c := reverseCloakedBrandBody(modified, client); c {
			modified = rev
			changed = true
		}
	}
	debugLog("handleResponseIntercept: changed=%t Body=%s", changed, string(modified))
	if !changed {
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
}

func handleStreamChunkIntercept(request []byte) []byte {
	var req pluginapi.StreamChunkInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return mustErrorEnvelope("invalid_request", err.Error())
	}

	format := normalizeSourceFormat(req.SourceFormat)
	debugLog("handleStreamChunkIntercept: SourceFormat=%s (normalized=%s) ChunkIndex=%d Body=%s RequestID=%s",
		req.SourceFormat, format, req.ChunkIndex, string(req.Body), req.RequestID)

	if req.RequestID != "" {
		if route := globalLifecycleManager.getRoute(req.RequestID); route != nil {
			if route.routeKind == routeKindExplicitOMPNonAGYBypass {
				debugLog("handleStreamChunkIntercept: correlated ExplicitOMPNonAGYBypass -> zero mutation")
				return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
			}
			if route.routeKind == routeKindProtectedAGY {
				if route.malformed || route.client != "oh_my_pi" {
					debugLog("invariant violation: malformed ProtectedAGY route state for RequestID=%s", req.RequestID)
					return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
				}
				resp := globalStreamManager.processChunk(&req, format)
				return mustEnvelope(resp)
			}
		}
		if plan := globalAliasPlanManager.get(req.RequestID); plan != nil {
			resp := globalStreamManager.processChunk(&req, plan.sourceFormat)
			return mustEnvelope(resp)
		}
		// RequestID is present, but neither route nor alias plan exists.
		// For alias-plan clients (Claude Code, Codex), full-declaration restoration
		// requires pinned request authority. If authority is missing (e.g. after
		// request.complete cleanup or missing plan), it MUST pass through rather
		// than guess or reverse static AGY targets (Issue #39).
		if sess := globalStreamManager.getSession("req:" + req.RequestID); sess != nil && clientUsesAliasPlan(sess.client) {
			debugLog("handleStreamChunkIntercept: alias-plan client=%q with missing authority RequestID=%s -> pass-through", sess.client, req.RequestID)
			return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
		}
	}

	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleStreamChunkIntercept: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}

	if marker := parseExplicitClientMarker(req.RequestHeaders); marker.present && clientUsesAliasPlan(marker.client) {
		debugLog("handleStreamChunkIntercept: alias-plan explicit marker=%q without pinned authority -> pass-through", marker.client)
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	resp := globalStreamManager.processChunk(&req, format)
	return mustEnvelope(resp)
}

func buildUncloakTable(requestBody []byte, sourceFormat string) (map[string]string, string) {
	var reqRoot map[string]any
	if err := safeUnmarshal(requestBody, &reqRoot); err != nil {
		debugLog("buildUncloakTable: unmarshal err=%v", err)
		return nil, ""
	}
	toolNames := extractToolNames(reqRoot, sourceFormat)
	// Try detecting from original (uncloaked) tool names first
	client := detectClient(toolNames)
	debugLog("buildUncloakTable: toolNames=%v client=%s", toolNames, client)
	if client == "" {
		// Request body may already be cloaked — detect from cloak targets
		client = detectCloakedClient(toolNames)
		debugLog("buildUncloakTable: cloakedClient=%s", client)
	}
	if client == "" {
		return nil, ""
	}
	if clientUsesAliasPlan(client) {
		// Claude Code and Codex cloak through a request-scoped alias plan: their
		// surface is mode-dependent and the exact forward mapping is minted per
		// request. A body alone — whether it still carries source names or only
		// cloak TARGETS — cannot prove that mapping, so it is NOT reverse
		// authority here (Issue #39). Without pinned request authority the caller
		// must pass through instead of guessing a static reverse.
		debugLog("buildUncloakTable: alias-plan client=%q withheld without pinned authority", client)
		return nil, ""
	}
	return scopeUncloakTableToDeclaredNames(effectiveUncloakTable(client), client, toolNames), client
}

// requestsRequestScopedReverse reports whether a client's reverse table must be
// narrowed to the names the current request actually declared. Codex and
// Claude Code require this: their tool surfaces include mode-dependent
// declarations or shared/fallback aliases (wp_*), so a request that declared
// one tool would otherwise receive an unearned target back in place of an
// upstream target -- a tool name that client never declared.
func requestsRequestScopedReverse(client string) bool {
	return client == "codex" || client == "claude_code"
}

// scopeUncloakTableToDeclaredNames drops every reverse pair whose names the
// request never declared, keeping the table when narrowing is not required or no
// names could be read.
//
// Which side of a pair the declared names land on depends on when the body was
// read. Request interception sees the raw client body, so its names are the
// sources the cloak would rename; the response and stream interceptors are handed
// the executed body, which the host only republishes after the rewrite, so its
// names are the targets the rewrite produced.
//
// The source side decides whenever it matches anything: a source name in the
// declared set proves the body is pre-cloak. Only a body carrying no source name
// at all is read as executed, where the targets are the only evidence left of
// which pairs the rewrite applied.
//
// An executed body cannot separate a target the client declared natively from one
// the cloak produced, so a caller holding the raw body must scope from that
// instead of re-deriving from the executed one: request interception scopes from
// the raw body and stores the result on the stream session, which the response
// path and the payload chunks reuse. The uncorrelated stream fallback has only
// the executed body and accepts that ambiguity.
func scopeUncloakTableToDeclaredNames(table map[string]string, client string, declared []string) map[string]string {
	if !requestsRequestScopedReverse(client) || len(table) == 0 || len(declared) == 0 {
		return table
	}
	declaredSet := make(map[string]bool, len(declared)*2)
	for _, name := range declared {
		declaredSet[name] = true
		if _, base := splitToolNamespace(name); base != name {
			declaredSet[base] = true
		}
	}
	declaredIsSource := false
	for _, src := range table {
		if declaredSet[src] {
			declaredIsSource = true
			break
		}
	}
	scoped := make(map[string]string, len(table))
	for target, src := range table {
		if strings.HasPrefix(target, "wp_") {
			// Dynamic identities are NEVER reconstructed by legacy static reverse fallbacks
			continue
		}
		if declaredSet[src] || (!declaredIsSource && declaredSet[target]) {
			scoped[target] = src
		}
	}
	if len(scoped) == 0 {
		return nil
	}
	return scoped
}

// declaredToolNames reads the tool names a request declared. An unreadable
// body yields no names, which scopeUncloakTableToDeclaredNames treats as "do not
// narrow" rather than "declared nothing".
func declaredToolNames(requestBody []byte, sourceFormat string) []string {
	var reqRoot map[string]any
	if err := safeUnmarshal(requestBody, &reqRoot); err != nil {
		return nil
	}
	return extractToolNames(reqRoot, sourceFormat)
}

// requestScopedUncloakPattern returns the client's precompiled stream pattern,
// narrowed to the names the request declared when narrowing applies. The regex
// is shared; only the lookup map is restricted, and a lookup miss leaves the
// matched text untouched, so an undeclared target passes through unchanged.
func requestScopedUncloakPattern(client string, requestBody []byte, sourceFormat string) *cachedUncloakPattern {
	cached := activeFilterConfig().uncloakRegexCache[client]
	if cached == nil || cached.re == nil {
		return nil
	}
	scoped := scopeUncloakTableToDeclaredNames(cached.lookup, client, declaredToolNames(requestBody, sourceFormat))
	if len(scoped) == 0 {
		return nil
	}
	if sameLookup(scoped, cached.lookup) {
		return cached
	}
	return &cachedUncloakPattern{re: cached.re, lookup: scoped, exactOnly: cached.exactOnly}
}

// requestScopedUncloakTable resolves a client's reverse table for one request,
// narrowing it to the declared names when the client requires it.
func requestScopedUncloakTable(client string, requestBody []byte, sourceFormat string) map[string]string {
	return scopeUncloakTableToDeclaredNames(effectiveUncloakTable(client), client, declaredToolNames(requestBody, sourceFormat))
}

// sameLookup reports whether two reverse lookups hold the same pairs, letting the
// scoped path reuse the shared precompiled pattern instead of allocating.
func sameLookup(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}

// detectionRequestBody returns the body used for client detection. The host runs
// this plugin's request.intercept_before first, and then republishes whatever
// that returned: when the request was rewritten, both OriginalRequest and
// RequestBody hold the executed (cloaked) body, so detection here reads cloak
// TARGETS; when it was not, both hold the raw client body. Fall back to
// RequestBody when OriginalRequest is unavailable.
func detectionRequestBody(originalRequest, requestBody []byte) []byte {
	if len(originalRequest) > 0 {
		return originalRequest
	}
	return requestBody
}

// splitToolNamespace separates an optional namespace prefix (e.g. "functions:", "default_api:")
// from the base tool name. It returns (prefix, baseName). If no prefix is present, it returns ("", name).
func splitToolNamespace(name string) (string, string) {
	if idx := strings.LastIndex(name, ":"); idx > 0 {
		return name[:idx+1], name[idx+1:]
	}
	return "", name
}

// resolveOMPSourceIdentity recognizes one OMP wire-escape underscore on the
// base identity only when the unescaped spelling is a verified OMP built-in.
// It preserves the exact namespace and original base for downstream reversal.
func resolveOMPSourceIdentity(name string) (prefix, originalBase, resolvedBase string) {
	prefix, originalBase = splitToolNamespace(name)
	resolvedBase = originalBase
	if strings.HasPrefix(originalBase, "_") {
		candidate := strings.TrimPrefix(originalBase, "_")
		if ompSourceIdentityInventory[candidate] {
			resolvedBase = candidate
		}
	}
	return prefix, originalBase, resolvedBase
}

func lookupProtectedOMPTarget(base string, cloakTable map[string]string) (string, bool) {
	_, _, resolvedBase := resolveOMPSourceIdentity(base)
	target, ok := cloakTable[resolvedBase]
	return target, ok
}

// lookupCloak maps a bare or namespaced tool name to its cloaked target.
func lookupCloak(name string, cloakTable map[string]string) (string, bool) {
	if target, exists := cloakTable[name]; exists {
		return target, true
	}
	if prefix, base := splitToolNamespace(name); prefix != "" {
		if target, exists := cloakTable[base]; exists {
			return prefix + target, true
		}
	}
	return "", false
}

// lookupUncloak maps a cloaked tool name (with or without namespace prefix) back to its original name.
func lookupUncloak(name string, uncloakTable map[string]string) (string, bool) {
	if orig, exists := uncloakTable[name]; exists {
		return orig, true
	}
	if prefix, base := splitToolNamespace(name); prefix != "" {
		if orig, exists := uncloakTable[base]; exists {
			return prefix + orig, true
		}
	}
	return "", false
}

func effectiveUncloakTable(client string) map[string]string {
	cloakTable := activeFilterConfig().ToolMappings[client]
	if cloakTable == nil {
		return nil
	}
	uncloak := make(map[string]string, len(cloakTable))
	for orig, target := range cloakTable {
		uncloak[target] = orig
	}
	return uncloak
}
func uncloakResponseBody(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}

	changed := uncloakJSONNode(root, uncloakTable, sourceFormat)
	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func uncloakResponseBodyExact(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}

	changed := uncloakJSONNodeExact(root, uncloakTable, sourceFormat)
	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func reverseBrandInResponseBody(body []byte, format, client string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}
	if !reverseAssistantBrandInJSON(root, format, client) {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// reverseAssistantBrandInJSON restores the resolved client's own reverse table
// over the CARRIERS a response actually carries: assistant prose, and the values
// inside tool arguments. It never rewrites a tool name, an id, a type or any
// other identity field - those belong to the exact alias-plan authority, which
// has already run by this point. A recursive all-string walk used to reach
// tool_use.name and rewrite a tool the client had declared under a spelling
// that happens to look like a cloaked token.
//
// Both carriers are processed whenever the body has them; the format argument
// is only a hint.
func reverseAssistantBrandInJSON(root any, format, client string) bool {
	m, ok := root.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	if contentArr, ok := m["content"].([]any); ok {
		for _, blockRaw := range contentArr {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if txt, ok := block["text"].(string); ok {
					if next, c := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client)); c {
						block["text"] = next
						changed = true
					}
				}
			case "tool_use":
				// The non-stream twin of the streamed input_json_delta: a model
				// that writes a cloaked path into a file hands the client a path
				// that does not exist. Only the VALUES are touched - the block's
				// name and id are identity fields and stay exactly as the exact
				// uncloak pass left them.
				if input, ok := block["input"]; ok {
					// The argument-safe subset only: an argument is data the
					// client executes, so a bare prose brand word the model may
					// have copied from the user's own prompt is not reversed here.
					if next, c := rewriteSystemValue(input, argumentReverseTableFor(client)); c {
						block["input"] = next
						changed = true
					}
				}
			}
		}
	}
	if choices, ok := m["choices"].([]any); ok {
		for _, chRaw := range choices {
			ch, ok := chRaw.(map[string]any)
			if !ok {
				continue
			}
			holders := make([]map[string]any, 0, 2)
			if msg, ok := ch["message"].(map[string]any); ok {
				holders = append(holders, msg)
			}
			if d, ok := ch["delta"].(map[string]any); ok {
				holders = append(holders, d)
			}
			for _, holder := range holders {
				if content, exists := holder["content"]; exists {
					if next, c := reverseBrandInOpenAIContent(content, client); c {
						holder["content"] = next
						changed = true
					}
				}
				if reverseBrandInToolCallArguments(holder, client) {
					changed = true
				}
			}
		}
	}
	return changed
}

// reverseBrandInToolCallArguments restores operational identifiers inside the
// arguments a tool call carries, on the non-stream path, treating an argument
// string as a complete fragment rather than a mid-stream hold.
func reverseBrandInToolCallArguments(holder map[string]any, client string) bool {
	changed := false
	for _, callRaw := range sliceOfMaps(holder["tool_calls"]) {
		fn, _ := callRaw["function"].(map[string]any)
		if fn == nil {
			continue
		}
		args, ok := fn["arguments"].(string)
		if !ok {
			continue
		}
		if next, c := replaceInsensitiveSetWithPrev(args, false, argumentReverseTableFor(client)); c {
			fn["arguments"] = next
			changed = true
		}
	}
	return changed
}

// isAssistantTextPartType reports whether an OpenAI content part type is
// assistant-visible text. The allowlist is text, output_text, and untyped
// (empty) parts; data/control/tool/reasoning/refusal parts keep literal
// Antigravity. All reverse-brand content paths share this predicate (Issue #21).
func isAssistantTextPartType(typ string) bool {
	return typ == "text" || typ == "output_text" || typ == ""
}

func reverseBrandInOpenAIContent(content any, client string) (any, bool) {
	switch v := content.(type) {
	case string:
		return replaceInsensitiveSetWithPrev(v, false, brandReverseTableFor(client))
	case []any:
		changed := false
		for _, partRaw := range v {
			if part, ok := partRaw.(map[string]any); ok {
				if typ, _ := part["type"].(string); !isAssistantTextPartType(typ) {
					continue
				}
				if txt, ok := part["text"].(string); ok {
					if next, c := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client)); c {
						part["text"] = next
						changed = true
					}
				}
			} else if s, ok := partRaw.(string); ok {
				if next, c := replaceInsensitiveSetWithPrev(s, false, brandReverseTableFor(client)); c {
					for i, elem := range v {
						if elem == partRaw {
							v[i] = next
							changed = true
							break
						}
					}
				}
			}
		}
		return v, changed
	case map[string]any:
		if typ, _ := v["type"].(string); !isAssistantTextPartType(typ) {
			return content, false
		}
		if txt, ok := v["text"].(string); ok {
			if next, c := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client)); c {
				v["text"] = next
				return v, true
			}
		}
	}
	return content, false
}

// uncloakStreamChunk uses pre-compiled regex to replace tool names directly in
// raw SSE bytes. Callers MUST pass complete SSE events (assembled by the event
// reassembly buffer) to guarantee that tool names are never split across calls.
func uncloakStreamChunk(body []byte, cached *cachedUncloakPattern) ([]byte, bool) {
	if cached == nil || cached.re == nil {
		return nil, false
	}
	if cached.exactOnly {
		return uncloakStreamChunkExact(body, cached.lookup)
	}

	// Find all matches of "name":"<target_tool_name>" and replace with originals.
	// Uses FindAllSubmatchIndex to extract the tool name capture group and look
	// it up in the uncloak table for precise replacement.
	bodyStr := string(body)
	matches := cached.re.FindAllStringSubmatchIndex(bodyStr, -1)
	if len(matches) == 0 {
		return nil, false
	}

	var buf strings.Builder
	buf.Grow(len(bodyStr))
	lastEnd := 0
	changed := false

	for _, loc := range matches {
		// FindAllStringSubmatchIndex returns 2*(1+groups) ints; guard the
		// capture-group slice indices before slicing to avoid a panic on any
		// unexpected match shape (e.g. an optional group that did not match).
		if len(loc) < 4 || loc[2] < 0 || loc[3] < 0 {
			continue
		}
		// loc[2]:loc[3] is capture group 1 (the tool name)
		toolName := bodyStr[loc[2]:loc[3]]
		orig, ok := lookupUncloak(toolName, cached.lookup)
		if ok {
			buf.WriteString(bodyStr[lastEnd:loc[2]])
			buf.WriteString(orig)
			lastEnd = loc[3]
			changed = true
		}
	}

	if !changed {
		return nil, false
	}

	buf.WriteString(bodyStr[lastEnd:])
	return []byte(buf.String()), true
}

func uncloakStreamChunkExact(body []byte, uncloakTable map[string]string) ([]byte, bool) {
	rewriteJSON := func(payload []byte) ([]byte, bool) {
		if modified, changed := uncloakResponseBodyExact(payload, uncloakTable, "openai"); changed {
			return modified, true
		}
		return uncloakResponseBodyExact(payload, uncloakTable, "anthropic")
	}

	if modified, changed := rewriteJSON(bytes.TrimSpace(body)); changed {
		return modified, true
	}

	events := splitSSEEventsForBrand(body)
	var out bytes.Buffer
	changedOverall := false
	for _, ev := range events {
		lines := strings.Split(string(ev), "\n")
		changedEvent := false
		for i, line := range lines {
			hasCR := strings.HasSuffix(line, "\r")
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			modified, changed := rewriteJSON([]byte(payload))
			if !changed {
				continue
			}
			lines[i] = "data: " + string(modified)
			if hasCR {
				lines[i] += "\r"
			}
			changedEvent = true
		}
		if changedEvent {
			out.WriteString(strings.Join(lines, "\n"))
			changedOverall = true
		} else {
			out.Write(ev)
		}
	}
	if !changedOverall {
		return nil, false
	}
	return out.Bytes(), true
}

// reverseBrandSSE maps model output back onto the client's own spelling. Both
// reverse authorities share this loop - event splitting, the terminal flush
// discipline, and the per-event rewrite; only the applier and the lane flusher
// differ, and brandReverseFuncs picks those once per session.
//
// Oh My Pi runs the protected-brand policy (Antigravity -> omp) on a single
// lane; every other client runs its own reverse table, one lane per token, so a
// match split across two SSE events is held until it completes.
func (m *streamSessionManager) reverseBrandSSE(sess *streamSession, sseBytes []byte, format string) ([]byte, bool) {
	if sess == nil {
		return nil, false
	}
	applyEvent, flushLanes := m.brandReverseFuncs(sess, format)

	events := splitSSEEventsForBrand(sseBytes)
	var out bytes.Buffer
	changedOverall := false
	for _, ev := range events {
		if sseContainsOpenAIDone(ev) {
			// The stream ends here, so any token still held in a lane is never
			// going to complete. Flush it rather than dropping it: losing the
			// tail of a sentence is far worse than emitting it unreversed.
			if fe := flushLanes("", true, nil); fe != nil {
				out.Write(fe)
				changedOverall = true
			}
			out.Write(ev)
			continue
		}
		// Anthropic native termination: flush pending carry before the
		// terminal control event using a protocol-valid assistant text delta
		// that preserves lane identity. content_block_stop flushes its block
		// lane; message_stop flushes all remaining lanes. The session must
		// not be deleted before this carry is delivered (Issue #21).
		if format == "anthropic" {
			if kind, laneKey := sseAnthropicTerminalKind(ev); kind != "" {
				// The lane a held token belongs to is complete by now, so it
				// must be emitted before the stop event.
				if fe := flushLanes(laneKey, kind == "message_stop", nil); fe != nil {
					out.Write(fe)
					changedOverall = true
				}
			}
		}
		// OpenAI termination: a non-null finish_reason ends ONLY its own choice.
		// That choice's prose and tool-argument lanes are flushed into the
		// stream BEFORE the event carrying the finish_reason - a client may
		// finalize the choice the moment it reads it, so a carry delivered after
		// can be dropped and the argument arrives truncated - and the choice is
		// marked terminal for this event (see streamSession.terminalChoices), so
		// a partial token its OWN delta ends on resolves here rather than being
		// carried past the finish_reason to the end of the stream. Choices still
		// streaming are left alone; [DONE] flushes what remains.
		//
		// The lanes that pre-flush must skip are the ones THIS event appends
		// bytes to. Flushing one of those first delivers the partial carry raw
		// and drains the lane, so the remainder arriving in this event can no
		// longer complete the token: a model that streams "Ant" and then
		// "igravity" with the finish_reason would hand the client the cloaked
		// word instead of the client's own. Those lanes resolve inline instead,
		// in the field their text arrived in, because the mark below also makes
		// them terminal.
		markedTerminal := false
		if format == "openai" && openAIMayCarryFinishReason(ev) {
			for _, data := range sseEventDataMaps(ev) {
				for _, ch := range sliceOfMaps(data["choices"]) {
					if fr, has := ch["finish_reason"]; !has || fr == nil {
						continue
					}
					laneKey := openAIChoiceLaneKey(ch)
					continued := openAIChoiceContinuedLanes(ch)
					if fe := flushLanes(laneKey, false, continued); fe != nil {
						out.Write(fe)
						changedOverall = true
					}
					if sess.terminalChoices == nil {
						sess.terminalChoices = map[string]bool{}
					}
					sess.terminalChoices[laneKey] = true
					markedTerminal = true
				}
			}
		}
		modifiedEv, changed := rewriteSSEEventData(ev, applyEvent)
		if markedTerminal {
			sess.terminalChoices = nil
		}
		if changed {
			changedOverall = true
		}
		out.Write(modifiedEv)
	}
	return out.Bytes(), changedOverall
}

// brandReverseFuncs returns the per-event applier and the lane flusher for the
// session's reverse authority. flushLanes(laneKey, allBlocks, skip) emits
// whatever the selected lanes still hold, or nil when nothing is pending. A key
// in skip is left untouched: it belongs to a lane the event being processed is
// already appending to, so its carry still has a chance to complete there.
func (m *streamSessionManager) brandReverseFuncs(sess *streamSession, format string) (func(map[string]any) bool, func(string, bool, map[string]bool) []byte) {
	if sess.client != "oh_my_pi" {
		return func(data map[string]any) bool {
				return m.reverseCloakedBrandStreamingMap(data, sess, format)
			}, func(laneKey string, allBlocks bool, skip map[string]bool) []byte {
				return m.reverseFlushCloakedBrandLanes(sess, format, laneKey, allBlocks, skip)
			}
	}
	applyEvent := func(data map[string]any) bool {
		switch format {
		case "openai":
			return m.reverseBrandOpenAIStreamingMap(data, sess)
		case "anthropic":
			return m.reverseBrandAnthropicStreamingMap(data, sess)
		}
		return false
	}
	flushLanes := func(laneKey string, allBlocks bool, skip map[string]bool) []byte {
		only := []string{laneKey}
		if allBlocks {
			only = nil
		}
		var out []byte
		for _, fe := range m.generateBrandFlushEventsFiltered(sess, format, only, skip) {
			out = append(out, fe...)
		}
		return out
	}
	return applyEvent, flushLanes
}

// reverseCloakedBrandStreamingMap applies the reverse mappings to every text
// field the protocol exposes in a single streaming event.
func (m *streamSessionManager) reverseCloakedBrandStreamingMap(data map[string]any, sess *streamSession, format string) bool {
	changed := false
	// Every field is rewritten against its own lane. The lane key has to match
	// what the terminal flush derives from the same event (sseAnthropicTerminalKind
	// for a content block, openAIChoiceLaneKey for a choice), or the flush looks
	// for lanes that do not exist and a held token is dropped.
	apply := func(m map[string]any, key, laneKey string) {
		txt, ok := m[key].(string)
		if !ok || txt == "" {
			return
		}
		// next != txt is the trigger, not the changed flag: when a lane holds a
		// trailing partial token the remainder is stripped from the text, and
		// that edit has to reach the client even though nothing was replaced
		// yet. Keeping the original here would send the held token inline and
		// then send it a second time from the flush.
		if next, _ := applySemanticBrandLane(sess, laneKey, txt); next != txt {
			m[key] = next
			changed = true
		}
	}
	if format == "anthropic" {
		laneKey := laneKeyWithIndex("anthropic", data)
		apply(data, "text", laneKey)
		if cb, ok := data["content_block"].(map[string]any); ok {
			apply(cb, "text", laneKey)
		}
		if delta, ok := data["delta"].(map[string]any); ok {
			apply(delta, "text", laneKey)
			// Tool call arguments stream as raw JSON fragments. They carry the
			// same cloaked tokens as the prose, and anything the model writes to
			// disk arrives through here, so leaving them untouched persisted
			// cloaked text into the user's files. The fragments are treated as
			// plain strings, the same way uncloakStreamChunkExact already treats
			// tool names inside them — JSON-escaped, which is why the path rules
			// in the reverse tables also carry their escaped spelling.
			// Its own lane, not the block's: prose and arguments are different
			// carriers, so a carry held in one must never flush through the other.
			apply(delta, "partial_json", toolArgsLaneKey(laneKey))
		}
		return changed
	}
	// OpenAI: the content index lives on each choice, never on the event root.
	// Reading the root alone put every choice in the "openai:0" lane, so a
	// token held back for choice 0 was emitted through choice 1 and the two
	// choices' text interleaved (choice 0 "A" + choice 1 "hello " came out as
	// "" + "Ahello ").
	rootLane := laneKeyWithIndex("openai", data)
	apply(data, "text", rootLane)
	choices, _ := data["choices"].([]any)
	for _, cRaw := range choices {
		choice, ok := cRaw.(map[string]any)
		if !ok {
			continue
		}
		laneKey := openAIChoiceLaneKey(choice)
		if delta, ok := choice["delta"].(map[string]any); ok {
			if content, exists := delta["content"]; exists {
				if next, c := applyOpenAIProseContent(sess, laneKey, content); c {
					delta["content"] = next
					changed = true
				}
			}
			// Chat Completions streams tool-call arguments as
			// choices[].delta.tool_calls[].function.arguments, the carrier
			// Anthropic calls partial_json. They carry the same text, so
			// reversing one and not the other left a .gemini path or an
			// antigravity.google URL inside the arguments of every
			// OpenAI-compatible client. Each call gets its own lane, because
			// one choice streams several calls' fragments side by side and a
			// shared lane would interleave them.
			for _, tRaw := range sliceOfMaps(delta["tool_calls"]) {
				fn, _ := tRaw["function"].(map[string]any)
				if fn == nil {
					continue
				}
				apply(fn, "arguments", openAIToolCallLaneKey(choice, tRaw))
			}
		}
		// The final standalone chunk of some providers carries the whole turn as
		// a message instead of a delta, the same shape the Oh My Pi applier
		// already accepts.
		if msg, ok := choice["message"].(map[string]any); ok {
			if content, exists := msg["content"]; exists {
				if next, c := applyOpenAIProseContent(sess, laneKey, content); c {
					msg["content"] = next
					changed = true
				}
			}
			for _, tRaw := range sliceOfMaps(msg["tool_calls"]) {
				fn, _ := tRaw["function"].(map[string]any)
				if fn == nil {
					continue
				}
				apply(fn, "arguments", openAIToolCallLaneKey(choice, tRaw))
			}
		}
	}
	return changed
}

// toolArgsLaneParts splits a tool-argument lane key back into the content block
// or choice it belongs to and the argument index within it. It reports false
// for an ordinary prose lane.
//
// This is also THE rule for delivering a recovered carry: every flush and every
// standalone merge routes through it, so a held argument fragment always goes
// back as an input_json_delta or a tool_call argument, never as assistant
// text. Sending it through the text carrier would print the recovered bytes in
// the message and leave the call truncated.
func toolArgsLaneParts(laneKey string) (blockLaneKey string, argIndex int, ok bool) {
	i := strings.Index(laneKey, reverseBrandLaneSuffix)
	if i < 0 {
		return laneKey, 0, false
	}
	blockLaneKey = laneKey[:i]
	tail := laneKey[i+len(reverseBrandLaneSuffix):]
	// The third part of the key is the per-mapping lane the reverse table runs
	// in; only the marker just behind the suffix names the carrier.
	if j := strings.Index(tail, reverseBrandLaneSuffix); j >= 0 {
		tail = tail[:j]
	}
	switch {
	case tail == toolArgsLaneMarker:
		return blockLaneKey, 0, true
	case strings.HasPrefix(tail, "tool"):
		n, err := strconv.Atoi(strings.TrimPrefix(tail, "tool"))
		if err != nil {
			return blockLaneKey, 0, false
		}
		return blockLaneKey, n, true
	}
	return blockLaneKey, 0, false
}

// sliceOfMaps returns the []any members of raw that are objects, so a
// malformed or absent field is skipped instead of panicking.
func sliceOfMaps(raw any) []map[string]any {
	items, _ := raw.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// toolArgsLaneMarker names the lane that carries a content block's streamed
// tool-call ARGUMENTS, as opposed to its prose. The two are different fields of
// the same event, so they need different lanes and different flush carriers.
const toolArgsLaneMarker = "args"

// toolArgsLaneKey is the arguments lane of one content block.
func toolArgsLaneKey(blockLaneKey string) string {
	return blockLaneKey + reverseBrandLaneSuffix + toolArgsLaneMarker
}

// openAIToolCallLaneKey derives the brand lane key for one streamed tool call:
// the choice it belongs to plus the call's own index after the lane suffix, so
// two calls of the same choice never share a carry and the flush event still
// reports the choice index the client dispatches on.
func openAIToolCallLaneKey(choice, toolCall map[string]any) string {
	idx := 0
	if n, ok := jsonIndexValue(toolCall["index"]); ok {
		idx = n
	}
	return openAIChoiceLaneKey(choice) + reverseBrandLaneSuffix + "tool" + strconv.Itoa(idx)
}

// openAIContentAppendsProse reports whether the applier's content path would
// append at least one non-empty text fragment from this OpenAI `content` value
// into the choice's prose lane. It is the ONE shape test for that path: the
// applier gates its content branch on it and openAIChoiceContinuedLanes uses it
// to decide which lanes a terminal event still feeds, so a shape the applier can
// append to can never be missing from the pre-flush skip set - and a shape it
// cannot append to can never keep a carry inside the lane past its own
// finish_reason, waiting for a [DONE] that the client has already passed.
//
// `content` is a string in the Chat Completions form, and an array of text
// parts (or a single text-part object) in the content-parts form; the applier
// handles all three, so this must too.
func openAIContentAppendsProse(content any) bool {
	switch v := content.(type) {
	case string:
		return v != ""
	case []any:
		for _, partRaw := range v {
			switch part := partRaw.(type) {
			case string:
				if part != "" {
					return true
				}
			case map[string]any:
				if typ, _ := part["type"].(string); !isAssistantTextPartType(typ) {
					continue
				}
				if txt, ok := part["text"].(string); ok && txt != "" {
					return true
				}
			}
		}
	case map[string]any:
		if typ, _ := v["type"].(string); !isAssistantTextPartType(typ) {
			return false
		}
		txt, ok := v["text"].(string)
		return ok && txt != ""
	}
	return false
}

// applyOpenAIProseContent reverses a choice's `content` value in place, in
// every shape the lane accepts, and reports whether it changed. It is the ONE
// content mutator: both appliers (the Oh My Pi one and the shared one) route
// their content through it, and openAIContentAppendsProse gates it, so the lane
// a terminal event is judged to continue is exactly the lane this appends to.
func applyOpenAIProseContent(sess *streamSession, laneKey string, content any) (any, bool) {
	if !openAIContentAppendsProse(content) {
		return content, false
	}
	switch v := content.(type) {
	case string:
		next, _ := applySemanticBrandLane(sess, laneKey, v)
		if next == v {
			return content, false
		}
		return next, true
	case []any:
		changed := false
		for _, partRaw := range v {
			switch part := partRaw.(type) {
			case map[string]any:
				if typ, _ := part["type"].(string); !isAssistantTextPartType(typ) {
					continue
				}
				if txt, ok := part["text"].(string); ok {
					if next, _ := applySemanticBrandLane(sess, laneKey, txt); next != txt {
						part["text"] = next
						changed = true
					}
				}
			case string:
				if next, _ := applySemanticBrandLane(sess, laneKey, part); next != part {
					for i, elem := range v {
						if elem == partRaw {
							v[i] = next
							changed = true
							break
						}
					}
				}
			}
		}
		return content, changed
	case map[string]any:
		if typ, _ := v["type"].(string); !isAssistantTextPartType(typ) {
			return content, false
		}
		txt, ok := v["text"].(string)
		if !ok {
			return content, false
		}
		if next, _ := applySemanticBrandLane(sess, laneKey, txt); next != txt {
			v["text"] = next
			return content, true
		}
	}
	return content, false
}

// openAIChoiceContinuedLanes returns the lane keys a choice's own payload in the
// event being processed will append bytes to: the choice's prose lane and the
// argument lane of every tool call whose arguments arrive non-empty. These are
// exactly the lanes reverseCloakedBrandStreamingMap applies - both the prose
// check and the applier's content branch go through openAIContentAppendsProse -
// so a lane listed here must not be pre-flushed at that choice's finish_reason:
// its carry has to stay in the lane and combine with the fragment that is
// already on its way. Choice payloads arrive as a delta or, on a final chunk, as
// a whole message, so both are inspected, the same way the applier inspects both.
func openAIChoiceContinuedLanes(choice map[string]any) map[string]bool {
	var out map[string]bool
	mark := func(key string) {
		if out == nil {
			out = map[string]bool{}
		}
		out[key] = true
	}
	for _, field := range [2]string{"delta", "message"} {
		body, _ := choice[field].(map[string]any)
		if body == nil {
			continue
		}
		if openAIContentAppendsProse(body["content"]) {
			mark(openAIChoiceLaneKey(choice))
		}
		for _, call := range sliceOfMaps(body["tool_calls"]) {
			fn, _ := call["function"].(map[string]any)
			if fn == nil {
				continue
			}
			if args, _ := fn["arguments"].(string); args != "" {
				mark(openAIToolCallLaneKey(choice, call))
			}
		}
	}
	return out
}

// reverseFlushCloakedBrandLanes emits whatever a content block's lanes still
// hold once the block is known to be complete. Each lane flushes as its own
// event so the index survives. A lane in skip is held back: the event being
// processed is appending to it, so its carry may still complete there.
func (m *streamSessionManager) reverseFlushCloakedBrandLanes(sess *streamSession, format, laneKey string, allBlocks bool, skip map[string]bool) []byte {
	// The block's own lane is keyed exactly, and its tool-argument lane is that
	// key plus the carrier marker, so both have to be selected. Draining every
	// block matches on the bare prefix.
	suffix := laneKey + reverseBrandLaneSuffix
	prefix := suffix
	if allBlocks {
		prefix = ""
	}
	var out []byte
	for _, key := range sortedCarryKeys(sess) {
		if skip[key] {
			continue
		}
		if key != laneKey && !strings.HasPrefix(key, prefix) {
			continue
		}
		lane := sess.brandCarries[key]
		if lane == nil || lane.carry == "" {
			continue
		}
		text, _ := replaceInsensitiveSetWithPrev(lane.carry, lane.lastIsWord, laneReverseTable(sess, key))
		lane.setCarry("")
		lane.lastIsWord = false
		if text == "" {
			continue
		}
		out = append(out, buildBrandFlushEvent(format, key, text)...)
	}
	return out
}

// buildBrandFlushEvent encodes one flushed lane as the protocol's terminal text
// delta, carrying the lane index so the client attaches the recovered text to
// the content block (Anthropic) or choice (OpenAI) the held bytes came from.
// An Anthropic flush is a real SSE event frame, `event:` plus `data:`, because
// that is how the Anthropic Messages stream itself frames every event: a
// client that dispatches on the SSE event name and never parses the payload
// would otherwise never deliver the recovered text. Returns nil for a format
// with no text-delta encoding.
func buildBrandFlushEvent(format, laneKey, text string) []byte {
	var frame string
	switch format {
	case "anthropic":
		payload, err := safeMarshal(map[string]any{
			"type":  "content_block_delta",
			"index": laneIndexNumOrZero(laneKey),
			"delta": anthropicFlushDelta(laneKey, text),
		})
		if err != nil {
			return nil
		}
		frame = "event: content_block_delta\ndata: " + string(payload) + "\n\n"
	case "openai":
		payload, err := safeMarshal(map[string]any{
			"choices": []any{map[string]any{"index": laneIndexNumOrZero(laneKey), "delta": openAIChoiceDelta(laneKey, text)}},
		})
		if err != nil {
			return nil
		}
		frame = "data: " + string(payload) + "\n\n"
	default:
		return nil
	}
	return []byte(frame)
}

// anthropicFlushDelta picks the carrier toolArgsLaneParts identified.
func anthropicFlushDelta(laneKey, text string) map[string]any {
	if _, _, isArgs := toolArgsLaneParts(laneKey); isArgs {
		return map[string]any{"type": "input_json_delta", "partial_json": text}
	}
	return map[string]any{"type": "text_delta", "text": text}
}

// openAIChoiceDelta picks the carrier toolArgsLaneParts identified.
func openAIChoiceDelta(laneKey, text string) map[string]any {
	_, argIndex, isArgs := toolArgsLaneParts(laneKey)
	if !isArgs {
		return map[string]any{"content": text}
	}
	return map[string]any{"tool_calls": []any{map[string]any{
		"index":    argIndex,
		"function": map[string]any{"arguments": text},
	}}}
}

// laneIndexNumOrZero is laneIndexNum with the unparsable-key case pinned to
// lane 0, so a flush never silently drops a block's recovered text.
func laneIndexNumOrZero(laneKey string) int {
	idx, _ := laneIndexNum(laneKey)
	return idx
}

func splitSSEEventsForBrand(data []byte) [][]byte {
	var events [][]byte
	start := 0
	for i := 0; i < len(data); {
		idx1 := bytes.Index(data[i:], []byte("\n\n"))
		idx2 := bytes.Index(data[i:], []byte("\r\n\r\n"))
		var idx int
		var blen int
		if idx1 >= 0 && idx2 >= 0 {
			if idx1 < idx2 {
				idx = idx1
				blen = 2
			} else {
				idx = idx2
				blen = 4
			}
		} else if idx1 >= 0 {
			idx = idx1
			blen = 2
		} else if idx2 >= 0 {
			idx = idx2
			blen = 4
		} else {
			events = append(events, data[start:])
			break
		}
		end := i + idx + blen
		events = append(events, data[start:end])
		start = end
		i = end
	}
	return events
}

// rewriteSSEEventData rewrites one SSE event by applying apply to every data
// payload in it, rebuilding the frame only when something changed.
func rewriteSSEEventData(ev []byte, apply func(map[string]any) bool) ([]byte, bool) {
	evStr := string(ev)
	lines := strings.Split(strings.ReplaceAll(evStr, "\r\n", "\n"), "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var dataMap map[string]any
		if err := safeUnmarshal([]byte(payload), &dataMap); err != nil {
			continue
		}
		if !apply(dataMap) {
			continue
		}
		newPayload, err := safeMarshal(dataMap)
		if err == nil {
			lines[i] = "data: " + string(newPayload)
			changed = true
		}
	}
	if !changed {
		return ev, false
	}
	return rebuildSSEEvent(ev, lines, evStr), true
}

// rebuildSSEEvent rejoins patched data lines into an SSE frame, preserving
// whichever line terminator the upstream event used.
func rebuildSSEEvent(ev []byte, lines []string, evStr string) []byte {
	rebuilt := strings.Join(lines, "\n")
	if !strings.HasSuffix(rebuilt, "\n\n") {
		if strings.HasSuffix(evStr, "\r\n\r\n") {
			rebuilt += "\r\n"
		}
		if !strings.HasSuffix(rebuilt, "\n\n") {
			if strings.HasSuffix(rebuilt, "\n") {
				rebuilt += "\n"
			} else {
				rebuilt += "\n\n"
			}
		}
	}
	return []byte(rebuilt)
}

func (m *streamSessionManager) reverseBrandOpenAIStreamingMap(data map[string]any, sess *streamSession) bool {
	choices, ok := data["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, chRaw := range choices {
		ch, ok := chRaw.(map[string]any)
		if !ok {
			continue
		}
		laneKey := openAIChoiceLaneKey(ch)
		var delta map[string]any
		if d, ok := ch["delta"].(map[string]any); ok {
			delta = d
		} else if d, ok := ch["message"].(map[string]any); ok {
			delta = d
		}
		if delta == nil {
			continue
		}
		// Streamed tool-call arguments: the Chat Completions twin of Anthropic's
		// input_json_delta. Each call gets its own lane, so two calls of one
		// choice never interleave, and the terminal flush sends the recovered
		// bytes back as an argument fragment rather than assistant text.
		for _, call := range sliceOfMaps(delta["tool_calls"]) {
			if fn, ok := call["function"].(map[string]any); ok {
				if args, ok := fn["arguments"].(string); ok {
					callLane := openAIToolCallLaneKey(ch, call)
					if newArgs, _ := applySemanticBrandLane(sess, callLane, args); newArgs != args {
						fn["arguments"] = newArgs
						changed = true
					}
				}
			}
		}
		if content, exists := delta["content"]; exists {
			if next, c := applyOpenAIProseContent(sess, laneKey, content); c {
				delta["content"] = next
				changed = true
			}
		}
	}
	return changed
}

func (m *streamSessionManager) reverseBrandAnthropicStreamingMap(data map[string]any, sess *streamSession) bool {
	idxVal, hasIdx := data["index"]
	laneKey := "anthropic:0"
	if hasIdx {
		switch v := idxVal.(type) {
		case json.Number:
			if i, err := v.Int64(); err == nil {
				laneKey = fmt.Sprintf("anthropic:%d", i)
			}
		case float64:
			laneKey = fmt.Sprintf("anthropic:%d", int(v))
		case int:
			laneKey = fmt.Sprintf("anthropic:%d", v)
		default:
			laneKey = fmt.Sprintf("anthropic:%v", v)
		}
	}
	typ, _ := data["type"].(string)
	switch typ {
	case "content_block_start":
		cb, ok := data["content_block"].(map[string]any)
		if !ok {
			return false
		}
		if cb["type"] != "text" {
			return false
		}
		if txt, ok := cb["text"].(string); ok {
			newTxt, _ := applySemanticBrandLane(sess, laneKey, txt)
			if newTxt != txt {
				cb["text"] = newTxt
				return true
			}
		}
	case "content_block_delta":
		delta, ok := data["delta"].(map[string]any)
		if !ok {
			return false
		}
		switch delta["type"] {
		case "text_delta":
			if txt, ok := delta["text"].(string); ok {
				newTxt, _ := applySemanticBrandLane(sess, laneKey, txt)
				if newTxt != txt {
					delta["text"] = newTxt
					return true
				}
			}
		case "input_json_delta":
			// Tool-call arguments carry the same operational identifiers as the
			// prose, and the model writes them to disk, so they are restored in
			// their own lane: a hold here must never flush as assistant text.
			if txt, ok := delta["partial_json"].(string); ok {
				newTxt, _ := applySemanticBrandLane(sess, toolArgsLaneKey(laneKey), txt)
				if newTxt != txt {
					delta["partial_json"] = newTxt
					return true
				}
			}
		}
	}
	return false
}

type brandFlush struct {
	key  string
	text string
}

// laneIndexNum parses the numeric lane index from a carry key. A child carrier
// lane appends its own marker after the separator ("openai:0\x00tool0", an
// Anthropic block's "anthropic:0\x00args"), so only the part before the
// separator is the lane index. Parsing the whole tail made every such lane
// report index 0 and sent the flush events out under the wrong content block.
func laneIndexNum(key string) (int, bool) {
	i := strings.LastIndexByte(key, ':')
	if i < 0 {
		return 0, false
	}
	num := key[i+1:]
	if j := strings.IndexByte(num, reverseBrandLaneSuffix[0]); j >= 0 {
		num = num[:j]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, false
	}
	return n, true
}

// sortedCarryKeys returns the key of every lane holding a non-empty carry, in
// deterministic lane order: numeric lane indices ascending, then the order the
// lanes were opened. Go map iteration would otherwise randomise the cross-lane
// flush sequence; Issue #18 requires stable event/lane ordering while keeping
// distinct indexed lanes isolated.
//
// Lanes of the SAME content block are ordered by brandLane.seq, the order their
// text arrived in. Tie-breaking those by key string instead made the flush
// order follow reverse-table declaration order, so two held tokens from one
// block could reach the client transposed (Issue #48).
func sortedCarryKeys(sess *streamSession) []string {
	keys := make([]string, 0, len(sess.brandCarries))
	for k, lane := range sess.brandCarries {
		if lane == nil || lane.carry == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ni, okI := laneIndexNum(keys[i])
		nj, okJ := laneIndexNum(keys[j])
		if okI != okJ {
			return okI
		}
		if ni != nj {
			return ni < nj
		}
		si, sj := sess.brandCarries[keys[i]].seq, sess.brandCarries[keys[j]].seq
		if si != sj {
			return si < sj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// orderedBrandFlushes resolves the final text of every held carry in
// deterministic lane order (sortedCarryKeys). This does not mutate lane state
// — callers drain only once the flushed text is safely delivered.
func orderedBrandFlushes(sess *streamSession) []brandFlush {
	keys := sortedCarryKeys(sess)
	flushes := make([]brandFlush, 0, len(keys))
	for _, k := range keys {
		lane := sess.brandCarries[k]
		text, _ := replaceInsensitiveSetWithPrev(lane.carry, lane.lastIsWord, laneReverseTable(sess, k))
		if text == "" {
			continue
		}
		flushes = append(flushes, brandFlush{key: k, text: text})
	}
	return flushes
}

func drainBrandFlushes(sess *streamSession, flushes []brandFlush) {
	for _, f := range flushes {
		lane := sess.brandCarries[f.key]
		if lane == nil {
			continue
		}
		lane.setCarry("")
		lane.lastIsWord = isWordByte(f.text[len(f.text)-1])
	}
}

func (m *streamSessionManager) generateBrandFlushEvents(sess *streamSession, format string) [][]byte {
	return m.generateBrandFlushEventsFiltered(sess, format, nil, nil)
}
func hasPendingBrandCarry(sess *streamSession) bool {
	if sess == nil {
		return false
	}
	for _, lane := range sess.brandCarries {
		if lane != nil && lane.carry != "" {
			return true
		}
	}
	return false
}

// anthropicTerminalKindFromMap reports whether an Anthropic data map is part
// of the native terminal lifecycle. Returns kind "content_block_stop" (with
// lane key) or "message_stop", or "" if not terminal.
func anthropicTerminalKindFromMap(m map[string]any) (kind, laneKey string) {
	t, _ := m["type"].(string)
	switch t {
	case "content_block_stop":
		idx := 0
		if v, ok := m["index"]; ok {
			if n, ok := jsonIndexValue(v); ok {
				idx = n
			} else if f, ok := v.(float64); ok {
				idx = int(f)
			} else if i, ok := v.(int); ok {
				idx = i
			}
		}
		return "content_block_stop", fmt.Sprintf("anthropic:%d", idx)
	case "message_stop":
		return "message_stop", ""
	default:
		return "", ""
	}
}

// sseAnthropicTerminalKind scans raw SSE bytes for an Anthropic terminal
// control event. Returns kind and laneKey (only for content_block_stop).
func sseAnthropicTerminalKind(ev []byte) (kind, laneKey string) {
	// Without a literal terminal marker, only a JSON escape could decode to one.
	if !bytes.Contains(ev, []byte("content_block_stop")) && !bytes.Contains(ev, []byte("message_stop")) && bytes.IndexByte(ev, '\\') < 0 {
		return "", ""
	}
	for _, m := range sseEventDataMaps(ev) {
		if k, lk := anthropicTerminalKindFromMap(m); k != "" {
			return k, lk
		}
	}
	return "", ""
}

func sseContainsAnthropicMessageStop(sse []byte) bool {
	// Keep escaped terminal types on the exact parser path above.
	if !bytes.Contains(sse, []byte("message_stop")) && bytes.IndexByte(sse, '\\') < 0 {
		return false
	}
	for _, ev := range splitSSEEventsForBrand(sse) {
		if k, _ := sseAnthropicTerminalKind(ev); k == "message_stop" {
			return true
		}
	}
	return false
}

// sseOpenAIDoneIndex returns the byte offset of the start of the `data: [DONE]`
// line in an SSE stream chunk, or -1 if no structural [DONE] frame is present.
// It verifies that [DONE] is the exact payload of an actual SSE data field,
// rather than a substring inside JSON content or tool-call arguments.
func sseOpenAIDoneIndex(sse []byte) int {
	if !bytes.Contains(sse, []byte("[DONE]")) {
		return -1
	}
	data := sse
	offset := 0
	for offset < len(sse) {
		lineStart := offset
		idx := bytes.IndexByte(data, '\n')
		var line []byte
		if idx >= 0 {
			line = data[:idx]
			data = data[idx+1:]
			offset += idx + 1
		} else {
			line = data
			offset += len(data)
			data = nil
		}
		lineNoCR := bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(lineNoCR)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			payload := bytes.TrimSpace(trimmed[5:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				return lineStart
			}
		}
	}
	return -1
}

// sseContainsOpenAIDone reports whether the SSE chunk contains an exact
// OpenAI `data: [DONE]` frame.
func sseContainsOpenAIDone(sse []byte) bool {
	return sseOpenAIDoneIndex(sse) >= 0
}

// generateBrandFlushEventsFiltered emits pending carries filtered to only the
// given lane keys (nil means all) and excluding skip, draining each flushed
// lane. Preserves the deterministic lane ordering of orderedBrandFlushes.
//
// A key selects its own lane and the lanes keyed beneath it: a content block
// or a choice owns the argument and tool-call lanes that stream under it, so
// flushing the block without them left a held argument token behind until the
// end-of-stream flush - after the block it belonged to was already closed. The
// match is the same one flushBrandStandalone makes for the standalone path.
// A key in skip is held back for the opposite reason: the event being
// processed is appending to that lane, so its carry can still complete there.
func (m *streamSessionManager) generateBrandFlushEventsFiltered(sess *streamSession, format string, only []string, skip map[string]bool) [][]byte {
	flushes := orderedBrandFlushes(sess)
	if only != nil || skip != nil {
		filtered := flushes[:0]
		for _, f := range flushes {
			if skip[f.key] {
				continue
			}
			if only == nil {
				filtered = append(filtered, f)
				continue
			}
			for _, k := range only {
				if f.key == k || strings.HasPrefix(f.key, k+reverseBrandLaneSuffix) {
					filtered = append(filtered, f)
					break
				}
			}
		}
		flushes = filtered
	}
	if len(flushes) == 0 {
		return nil
	}
	drainBrandFlushes(sess, flushes)
	var out [][]byte
	for _, f := range flushes {
		if ev := buildBrandFlushEvent(format, f.key, f.text); ev != nil {
			out = append(out, ev)
		}
	}
	return out
}

// markStandaloneFinishes records the choices a standalone (non-SSE) chunk
// finished: a non-null finish_reason ends ONLY its own choice lane, never the
// whole stream (with n > 1 other choices keep streaming). It reports the lanes
// finished by this chunk and whether the stream as a whole is done: a global
// terminal payload (bare [DONE] or Anthropic message_stop), or finish_reasons
// covering every expected ROOT choice.
//
// Done is counted over root choice lanes, never over every lane in the session.
// A choice that streams tool-call arguments opens CHILD lanes under its own key
// ("openai:0\x00tool0"), so counting lanes let choice 0 alone satisfy n = 2 and
// free the session before choice 1 had streamed anything - losing its text.
func markStandaloneFinishes(sess *streamSession, body []byte) (finished []string, done bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, false
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, true
	}
	var root map[string]any
	if err := safeUnmarshal(trimmed, &root); err != nil || root == nil {
		return nil, false
	}
	if t, _ := root["type"].(string); t == "message_stop" {
		return nil, true
	}
	if kind, laneKey := anthropicTerminalKindFromMap(root); kind == "content_block_stop" {
		return []string{laneKey}, false
	}
	choices, _ := root["choices"].([]any)
	for _, chRaw := range choices {
		ch, ok := chRaw.(map[string]any)
		if !ok {
			continue
		}
		if fr, has := ch["finish_reason"]; has && fr != nil {
			laneKey := openAIChoiceLaneKey(ch)
			getBrandLane(sess, laneKey).finished = true
			// The finishing choice's own child lanes are finished with it, so
			// their carries flush into this chunk instead of waiting for a
			// completion signal that will never come.
			prefix := laneKey + reverseBrandLaneSuffix
			for k, lane := range sess.brandCarries {
				if strings.HasPrefix(k, prefix) {
					lane.finished = true
				}
			}
			finished = append(finished, laneKey)
		}
	}
	if len(finished) == 0 {
		return finished, false
	}
	// Two counts decide completion, and both are over ROOT lanes only. Every
	// root lane the stream has actually opened must be finished, or a choice
	// the upstream is still streaming would have the session freed under it;
	// and the finished count must cover n, the number of choices the client
	// asked for, or a choice that has not appeared yet would end the stream
	// early. Child lanes (a choice's tool-call arguments) count for neither: an
	// argument lane under choice 0 belongs to that choice, so it can no longer
	// make n = 2 look satisfied on its own.
	rootTotal, rootFinished := countRootLanes(sess)
	if rootFinished < sess.expected || rootFinished < rootTotal {
		return finished, false
	}
	return finished, true
}

// countRootLanes counts a session's root choice lanes and how many of them are
// finished. Root lanes are the per-choice lanes; the argument and tool-call
// lanes opened beneath them carry a lane-key marker and are not choices.
func countRootLanes(sess *streamSession) (total, finished int) {
	for key, lane := range sess.brandCarries {
		if strings.Contains(key, reverseBrandLaneSuffix) {
			continue
		}
		total++
		if lane.finished {
			finished++
		}
	}
	return total, finished
}

// openAIChoiceLaneKey derives the brand lane key for a choice map, matching
// the keys produced by reverseBrandOpenAIStreamingMap.
func openAIChoiceLaneKey(ch map[string]any) string {
	if idxVal, ok := ch["index"]; ok {
		if n, ok := jsonIndexValue(idxVal); ok {
			return fmt.Sprintf("openai:%d", n)
		}
		return fmt.Sprintf("openai:%v", idxVal)
	}
	return "openai:0"
}

// sseEventDataMaps decodes the objects an SSE event's `data:` payloads carry,
// in order. Malformed, empty and terminal ([DONE]) payloads are skipped, the
// same way the event rewrite and the Anthropic terminal scanner treat them.
func sseEventDataMaps(ev []byte) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.ReplaceAll(string(ev), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := safeUnmarshal([]byte(payload), &m); err != nil || m == nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// openAIMayCarryFinishReason reports whether an OpenAI SSE event may carry a
// choice's finish_reason. Detection itself is always on the key the JSON decodes
// to, never on the raw bytes; this only decides whether the payloads are worth
// scanning. A backslash anywhere may be a JSON escape INSIDE the key itself
// ("finish\u005freason" decodes to finish_reason without containing its bytes),
// so those events are scanned too - the same backstop sseAnthropicTerminalKind
// keeps for its markers.
func openAIMayCarryFinishReason(ev []byte) bool {
	return bytes.Contains(ev, []byte("finish_reason")) || bytes.IndexByte(ev, '\\') >= 0
}

// laneKeyWithIndex derives a lane key from an event's own `index` field,
// defaulting to lane 0 for the events that carry none.
func laneKeyWithIndex(format string, data map[string]any) string {
	if n, ok := jsonIndexValue(data["index"]); ok {
		return fmt.Sprintf("%s:%d", format, n)
	}
	return format + ":0"
}

// requestChoiceCount reads the OpenAI "n" (choices per completion) from a
// request body; absent/invalid means 1.
func requestChoiceCount(body []byte) int {
	var root map[string]any
	if err := safeUnmarshal(body, &root); err != nil || root == nil {
		return 1
	}
	if n, ok := jsonIndexValue(root["n"]); ok && n > 1 {
		return n
	}
	return 1
}

// flushBrandStandalone merges a session's held carries into a standalone
// (non-SSE) chunk and drains them. It is the terminal half of the standalone
// path: the applier already ran over this chunk and left any partial token in
// its lane, so the flush resolves that token and hands it back inside the same
// chunk shape the protocol expects.
func (m *streamSessionManager) flushBrandStandalone(sess *streamSession, body []byte, format string, only []string) ([]byte, bool) {
	if sess == nil || len(sess.brandCarries) == 0 {
		return nil, false
	}
	if sess.client == "" || sess.client == negativeClientResolution {
		return nil, false
	}
	var root map[string]any
	trimmed := bytes.TrimSpace(body)
	bareDone := bytes.Equal(trimmed, []byte("[DONE]"))
	if err := safeUnmarshal(body, &root); err != nil || root == nil {
		if !bareDone {
			return nil, false
		}
		root = map[string]any{}
	}
	flushes := orderedBrandFlushes(sess)
	if only != nil {
		selected := flushes[:0:0]
		for _, f := range flushes {
			for _, k := range only {
				// A cloaked-brand session keeps one lane per carrier under the
				// choice's key - the choice's prose lane plus a lane per streamed
				// tool call - so a finished choice ("openai:0") flushes the lanes
				// it opened ("openai:0\x00tool0"), not only its own key.
				if f.key == k || strings.HasPrefix(f.key, k+reverseBrandLaneSuffix) {
					selected = append(selected, f)
					break
				}
			}
		}
		flushes = selected
	}
	if len(flushes) == 0 {
		return nil, false
	}
	var merged bool
	switch format {
	case "openai":
		merged = mergeOpenAIStandaloneFlush(root, flushes)
	case "anthropic":
		merged = mergeAnthropicStandaloneFlush(root, flushes)
	}
	if !merged {
		if format == "anthropic" {
			if t, _ := root["type"].(string); t == "message_stop" || t == "content_block_stop" {
				if len(flushes) > 0 {
					var buf bytes.Buffer
					for _, f := range flushes {
						ev := buildBrandFlushEvent("anthropic", f.key, f.text)
						if ev == nil {
							continue
						}
						buf.Write(ev)
					}
					buf.WriteString("data: ")
					buf.Write(trimmed)
					buf.WriteString("\n\n")
					drainBrandFlushes(sess, flushes)
					return buf.Bytes(), true
				}
			}
		}
		return nil, false
	}
	drainBrandFlushes(sess, flushes)
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	if bareDone {
		// The host frames the whole payload as one SSE data line, so keep
		// the terminal marker as its own event after the flush content.
		return append(raw, []byte("\n\ndata: [DONE]")...), true
	}
	return raw, true
}

func mergeOpenAIStandaloneFlush(root map[string]any, flushes []brandFlush) bool {
	choices, _ := root["choices"].([]any)
	for _, f := range flushes {
		idx := laneIndexNumOrZero(f.key)
		target := openAIChoiceAt(choices, idx)
		if target == nil {
			target = map[string]any{"index": idx}
			choices = append(choices, target)
		}
		delta, _ := target["delta"].(map[string]any)
		if delta == nil {
			if msg, ok := target["message"].(map[string]any); ok {
				delta = msg
			} else {
				delta = map[string]any{}
				target["delta"] = delta
			}
		}
		appendBrandFlushToChoice(delta, f)
	}
	root["choices"] = choices
	return true
}

// appendBrandFlushToChoice merges one recovered carry into a choice's delta,
// through the carrier toolArgsLaneParts identified.
func appendBrandFlushToChoice(delta map[string]any, f brandFlush) {
	_, argIndex, isArgs := toolArgsLaneParts(f.key)
	if !isArgs {
		if s, ok := delta["content"].(string); ok {
			delta["content"] = s + f.text
		} else {
			delta["content"] = f.text
		}
		return
	}
	calls, fn := openAIToolCallAt(delta["tool_calls"], argIndex)
	delta["tool_calls"] = calls
	if s, ok := fn["arguments"].(string); ok {
		fn["arguments"] = s + f.text
	} else {
		fn["arguments"] = f.text
	}
}

// openAIToolCallAt returns the streamed tool call at idx as it now is in raw,
// creating it when the chunk has not declared it yet. The possibly grown
// slice is returned so the caller can write it back.
func openAIToolCallAt(raw any, idx int) ([]any, map[string]any) {
	calls, _ := raw.([]any)
	for _, cRaw := range calls {
		call, ok := cRaw.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := jsonIndexValue(call["index"]); ok && n == idx {
			fn, ok := call["function"].(map[string]any)
			if !ok {
				fn = map[string]any{}
				call["function"] = fn
			}
			return calls, fn
		}
	}
	fn := map[string]any{}
	return append(calls, map[string]any{"index": idx, "function": fn}), fn
}

func openAIChoiceAt(choices []any, idx int) map[string]any {
	for _, chRaw := range choices {
		ch, ok := chRaw.(map[string]any)
		if !ok {
			continue
		}
		ci := 0
		if v, has := ch["index"]; has {
			if n, ok := jsonIndexValue(v); ok {
				ci = n
			}
		}
		if ci == idx {
			return ch
		}
	}
	return nil
}

// jsonIndexValue reads an integer lane index from a safeUnmarshal-produced
// value (UseNumber guarantees json.Number for every JSON number).
func jsonIndexValue(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

func mergeAnthropicStandaloneFlush(root map[string]any, flushes []brandFlush) bool {
	if len(flushes) != 1 {
		return false
	}
	delta, ok := root["delta"].(map[string]any)
	if !ok {
		return false
	}
	if _, _, isArgs := toolArgsLaneParts(flushes[0].key); isArgs {
		if s, ok := delta["partial_json"].(string); ok {
			delta["partial_json"] = s + flushes[0].text
		} else {
			delta["partial_json"] = flushes[0].text
		}
		return true
	}
	txt, ok := delta["text"].(string)
	if !ok {
		return false
	}
	delta["text"] = txt + flushes[0].text
	return true
}

// reverseBrandStandalone reverses the brand tokens of one standalone (non-SSE)
// chunk: the shape CLIProxyAPI hands the plugin for the OpenAI-protocol exits,
// where the host adds the `data:` framing itself after interception. Oh My Pi
// keeps its protected appliers; every other resolved client runs the same
// per-lane applier the SSE path uses.
func (m *streamSessionManager) reverseBrandStandalone(sess *streamSession, body []byte, format string) ([]byte, bool) {
	if sess == nil || sess.client == "" || sess.client == negativeClientResolution {
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, false
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, false
	}
	var root map[string]any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}
	var changed bool
	switch {
	case sess.client != "oh_my_pi":
		changed = m.reverseCloakedBrandStreamingMap(root, sess, format)
	case format == "openai":
		changed = m.reverseBrandOpenAIStreamingMap(root, sess)
	case format == "anthropic":
		if _, ok := root["delta"]; ok || root["type"] == "content_block_delta" || root["type"] == "content_block_start" {
			changed = m.reverseBrandAnthropicStreamingMap(root, sess)
		} else {
			changed = m.reverseBrandOpenAIStreamingMap(root, sess)
		}
	}
	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// ── Stream Session Manager & SSE Event Reassembly ────────────────────────
//
// Handles the "Split-String Chunk" attack: TCP can split a network chunk
// at any byte boundary, including inside a tool name:
//   Chunk 1: data: {"name": "run_c
//   Chunk 2: ommand", "input": {}}\n\n
//
// Without buffering, regex on each chunk misses the match entirely.
//
// Solution: SSE events are delimited by "\n\n". We buffer incomplete events
// (those without a \n\n terminator) and only process/forward complete events
// where all JSON content — including tool names — is guaranteed intact.
//
// In CLIProxyAPI schema_version >= 3, OriginalRequest and RequestBody are
// delivered only on the header-init chunk (ChunkIndex == StreamChunkHeaderInitIndex).
// StreamSessionManager caches the client uncloak pattern under a correlation
// key (RequestID, metadata/headers ids, or for legacy schema < 3 chunks where
// every chunk repeats the request body, an FNV hash of that body), isolating
// concurrent streams. Payload chunks with NO correlation key cannot be
// attributed to any stream: sharing one slot between them would let one
// stream's state overwrite another's (cross-stream corruption), so such
// chunks pass through unmolested instead of being buffered.

type brandLane struct {
	carry      string
	lastIsWord bool
	// seq is the order in which this lane was first opened, i.e. the order its
	// text appeared in the stream. Two lanes of the same choice or content block
	// can hold text at once (its prose lane and an argument lane), and their
	// flushes have to reach the client in the order the text arrived, not in
	// mapping-name order.
	seq int
	// finished marks the lane's choice as ended by a non-null
	// finish_reason on the standalone path; the whole session is freed
	// only once every expected lane is finished.
	finished bool
}

type streamSession struct {
	client       string
	cached       *cachedUncloakPattern
	tail         []byte
	updatedAt    time.Time
	brandCarries map[string]*brandLane
	// laneSeq hands every new brand lane its arrival order, so flushes of two
	// lanes of the same block can be emitted in source order.
	laneSeq int
	// expected is the request's OpenAI "n" (choices per completion),
	// minimum 1; gates standalone stream-end detection.
	expected       int
	laneProgress   map[int]bool
	payloadStarted bool
	// terminalChoices holds the root lane keys ("openai:0") whose choice ends in
	// the event currently being applied. While a choice is marked, none of its
	// lanes may hold a trailing partial token: no further bytes can arrive for
	// it, so a token its own terminal delta ends on is resolved into that event
	// instead of being carried past the finish_reason to the end of the stream.
	// Set and cleared per event, so nothing leaks into the next one.
	terminalChoices map[string]bool
}

const (
	reverseBrandMatch       = "Antigravity"
	reverseBrandReplacement = "omp"
)

// ompProtectedReverseTable is Oh My Pi's entire reverse authority: the one
// protected pair its lane owns, plus the operational home directory the
// forward path-segment remap produced. Packaged as a table so the flush path
// can treat every client the same way without allocating per chunk, and
// applied to the block's single lane as one set, so the path rules and the
// protected pair share one carry instead of deadlocking each other.
var ompProtectedReverseTable = []rewriteMapping{
	// The protected pair owns the bare brand word. Its exclusion is what keeps
	// the real Antigravity host intact: the dot after "Antigravity" is a
	// non-word byte, so the rule's right boundary passes inside
	// "antigravity.google" and it would otherwise hand the client the dead host
	// "omp.google". An exclusion is also chunk-safe, which the repair rule this
	// replaces was not: with "omp.google" as the only guard, a stream that
	// split the host as "antigravity.oo" + "gle" emitted "omp.oo" and left the
	// second half unrecoverable.
	{Match: reverseBrandMatch, Replacement: reverseBrandReplacement, NotFollowedBy: antigravityHostPreserve},
	// Must precede the .gemini rules below, which would otherwise consume the
	// directory first and leave "AGENTS.md" attached to the wrong root. The
	// "/AGENTS.md" right after ".gemini/" is what makes this reversible: no
	// .omp path carries that shape, because OMP's own is ".omp/agent/AGENTS.md".
	// Only the file is remapped this way, never the .claude directory itself -
	// a blanket .claude -> .gemini would land on the same target as .omp and
	// leave the reverse unable to tell the two apart.
	//
	// The JSON-escaped spelling is its own rule: inside a tool call's arguments
	// the path arrives as JSON text, where each backslash is written twice, and
	// the plain directory rule below would claim the directory alone and hand
	// the client ".omp\\AGENTS.md" instead of ".claude\\CLAUDE.md".
	{Match: ".gemini/AGENTS.md", Replacement: ".claude/CLAUDE.md", WholeSegment: true, ArgumentSafe: true},
	{Match: `.gemini\AGENTS.md`, Replacement: `.claude\CLAUDE.md`, WholeSegment: true, ArgumentSafe: true},
	{Match: `.gemini\\AGENTS.md`, Replacement: `.claude\\CLAUDE.md`, WholeSegment: true, ArgumentSafe: true},
	{Match: ".gemini/", Replacement: ".omp/", WholeSegment: true, ArgumentSafe: true},
	{Match: `.gemini\`, Replacement: `.omp\`, WholeSegment: true, ArgumentSafe: true},
	// The bare form, matching the forward remap's end-of-string case. It is the
	// one rule that needs SegmentEnd: its match ends in a word byte, so the
	// generic right boundary is happy with any non-word byte after it, and
	// ".gemini-backup" or ".gemini.foo" would come back as ".omp-backup" and
	// ".omp.foo" - spellings the forward pass deliberately preserves and never
	// produces. The rule only fires where the forward dot-segment remap does:
	// at the end of the path.
	{Match: ".gemini", Replacement: ".omp", WholeSegment: true, SegmentEnd: true, ArgumentSafe: true},
}

// filterArgumentReverseTable keeps the rules a tool-argument carrier may run:
// the operational identifiers (paths, domains, SDK and skill names) the model
// can legitimately echo from the cloaked prompt. Prose rules stay out of
// arguments - see rewriteMapping.ArgumentSafe.
func filterArgumentReverseTable(table []rewriteMapping) []rewriteMapping {
	out := make([]rewriteMapping, 0, len(table))
	for _, m := range table {
		if m.ArgumentSafe {
			out = append(out, m)
		}
	}
	return out
}

// brandReverseTableFor returns the table used to resolve and flush a session's
// carried tokens: the protected pair for Oh My Pi, that client's own reverse
// table for every other resolved client.
func brandReverseTableFor(client string) []rewriteMapping {
	if client == "oh_my_pi" {
		return ompProtectedReverseTable
	}
	return reverseBrandMappingsFor(client)
}

// argumentReverseTablesByClient holds brandReverseTableFor(client) narrowed to
// the rules marked ArgumentSafe. Built once in init(), so the per-chunk stream
// path never allocates or filters a table.
var argumentReverseTablesByClient = map[string][]rewriteMapping{}

// argumentReverseTableFor returns the rules that may run over a tool-argument
// carrier for one client. See rewriteMapping.ArgumentSafe for why the prose
// rules are excluded: an argument is data the client executes.
func argumentReverseTableFor(client string) []rewriteMapping {
	return argumentReverseTablesByClient[client]
}

// laneReverseTable picks the table a stream lane's bytes are resolved against:
// the argument-safe subset inside a tool-argument lane, the client's whole
// table in a prose lane. Resolving and flushing both go through here, so a
// lane's hold decision and its flush decision can never use different tables.
func laneReverseTable(sess *streamSession, laneKey string) []rewriteMapping {
	if _, _, isArgs := toolArgsLaneParts(laneKey); isArgs {
		return argumentReverseTableFor(sess.client)
	}
	return brandReverseTableFor(sess.client)
}

type streamSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*streamSession
}

var globalStreamManager = newStreamSessionManager()

type explicitOMPRouteKind int

const (
	routeKindNone explicitOMPRouteKind = iota
	routeKindProtectedAGY
	routeKindExplicitOMPNonAGYBypass
)

type streamDisposition int32

const (
	streamDispositionNone streamDisposition = iota
	streamDispositionPayloadActive
	streamDispositionCleanTerminal
)

type requestAliasPair struct {
	SourceIdentity   string
	UpstreamIdentity string
	Changed          bool
}

type requestAliasPlan struct {
	client          string
	sourceFormat    string
	pairs           []requestAliasPair
	forward         map[string]string
	declaredForward map[string]string
	reverse         map[string]string
	cachedUncloak   *cachedUncloakPattern
	expected        int
	disposition     atomic.Int32
}

func (p *requestAliasPlan) getDisposition() streamDisposition {
	if p == nil {
		return streamDispositionNone
	}
	return streamDisposition(p.disposition.Load())
}

func (p *requestAliasPlan) setDisposition(target streamDisposition) {
	if p == nil {
		return
	}
	for {
		cur := p.disposition.Load()
		if streamDisposition(cur) >= target {
			return
		}
		if p.disposition.CompareAndSwap(cur, int32(target)) {
			return
		}
	}
}

func fallbackAliasForSource(source string) string {
	sum := sha256.Sum256([]byte("request-alias-v1\x00" + source))
	return fmt.Sprintf("wp_ext_%x", sum[:16])
}

func buildRequestAliasUncloakPattern(reverse map[string]string) (*cachedUncloakPattern, error) {
	if len(reverse) == 0 {
		return nil, nil
	}
	targets := make([]string, 0, len(reverse))
	lookup := make(map[string]string, len(reverse))
	for target, source := range reverse {
		targets = append(targets, target)
		lookup[target] = source
	}
	sort.Strings(targets)
	escaped := make([]string, 0, len(targets))
	for _, target := range targets {
		escaped = append(escaped, regexp.QuoteMeta(target))
	}
	pattern := "\"name\"[[:space:]]*:[[:space:]]*\"(" + strings.Join(escaped, "|") + ")\""
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &cachedUncloakPattern{re: re, lookup: lookup, exactOnly: true}, nil
}

func buildRequestAliasPlan(client string, sourceIdentities []string, preferred map[string]string, declared ...[]string) (*requestAliasPlan, error) {
	client = normalizeClientKey(client)
	if client == "" {
		return nil, fmt.Errorf("client identity is required")
	}

	unique := make(map[string]struct{}, len(sourceIdentities))
	for _, source := range sourceIdentities {
		if source == "" {
			return nil, fmt.Errorf("source identity is required")
		}
		unique[source] = struct{}{}
	}
	sources := make([]string, 0, len(unique))
	for source := range unique {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	declaredSet := make(map[string]bool)
	if len(declared) > 0 {
		for _, d := range declared[0] {
			declaredSet[d] = true
			_, dBase := splitToolNamespace(d)
			declaredSet[dBase] = true
		}
	}

	plan := &requestAliasPlan{
		client:          client,
		pairs:           make([]requestAliasPair, 0, len(sources)),
		forward:         make(map[string]string, len(sources)),
		declaredForward: make(map[string]string, len(declaredSet)),
		reverse:         make(map[string]string, len(sources)),
	}
	ownerByTarget := make(map[string]string, len(sources))
	ownerByFinalBase := make(map[string]string, len(sources))
	knownTargets := make(map[string]bool, len(preferred)*2)
	for _, t := range preferred {
		_, tBase := splitToolNamespace(t)
		knownTargets[tBase] = true
		knownTargets[t] = true
	}

	for _, source := range sources {
		prefix, base := splitToolNamespace(source)
		target, overridden := preferred[source]
		if !overridden {
			if targetBase, baseOverridden := preferred[base]; baseOverridden {
				if prefix != "" {
					target = prefix + targetBase
				} else {
					target = targetBase
				}
				overridden = true
			} else if knownTargets[base] {
				// Source declares a native target identity (e.g. view_file or default_api:view_file).
				if prefix != "" {
					target = prefix + base
				} else {
					target = base
				}
				overridden = true
			}
		}
		if overridden {
			if target == "" {
				return nil, fmt.Errorf("empty alias target for %q", source)
			}
		} else {
			target = fallbackAliasForSource(base)
		}

		_, finalBase := splitToolNamespace(target)
		ownerTarget, targetConflict := ownerByTarget[target]
		ownerBase, baseConflict := ownerByFinalBase[finalBase]
		hasCollision := (targetConflict && ownerTarget != source) || (baseConflict && ownerBase != source)

		if hasCollision {
			if targetConflict && ownerTarget != source {
				return nil, fmt.Errorf("alias collision: %q and %q resolve to %q", ownerTarget, source, target)
			}
			return nil, fmt.Errorf("alias collision: %q and %q resolve to same final base %q", ownerBase, source, finalBase)
		}
		ownerByTarget[target] = source
		ownerByFinalBase[finalBase] = source

		changed := source != target
		plan.pairs = append(plan.pairs, requestAliasPair{
			SourceIdentity: source, UpstreamIdentity: target, Changed: changed,
		})
		plan.forward[source] = target
		if declaredSet[source] || declaredSet[base] {
			plan.declaredForward[source] = target
		}
		if changed {
			plan.reverse[target] = source
		}
	}
	cached, err := buildRequestAliasUncloakPattern(plan.reverse)
	if err != nil {
		return nil, fmt.Errorf("compile alias reverse pattern: %w", err)
	}
	plan.cachedUncloak = cached
	return plan, nil
}

type requestAliasPlanManager struct {
	mu    sync.Mutex
	plans map[string]*requestAliasPlan
}

func newRequestAliasPlanManager() *requestAliasPlanManager {
	return &requestAliasPlanManager{plans: make(map[string]*requestAliasPlan)}
}

var globalAliasPlanManager = newRequestAliasPlanManager()

func (m *requestAliasPlanManager) set(requestID string, plan *requestAliasPlan) error {
	if requestID == "" || plan == nil {
		return fmt.Errorf("request alias plan requires correlation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.plans[requestID]; exists {
		return fmt.Errorf("request alias plan already exists for %q", requestID)
	}
	m.plans[requestID] = plan
	return nil
}

func (m *requestAliasPlanManager) get(requestID string) *requestAliasPlan {
	if requestID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plans[requestID]
}

func (m *requestAliasPlanManager) delete(requestID string) {
	if requestID == "" {
		return
	}
	m.mu.Lock()
	delete(m.plans, requestID)
	m.mu.Unlock()
}

func requestAliasSourceIdentities(body []byte, sourceFormat string) ([]string, []string, error) {
	format := normalizeSourceFormat(sourceFormat)
	if format != "openai" && format != "anthropic" {
		return nil, nil, fmt.Errorf("unsupported source format %q", sourceFormat)
	}
	root, ok := decodeStrictProtectedJSON(body)
	if !ok {
		return nil, nil, fmt.Errorf("request body must be one strict JSON object")
	}
	return collectRequestSourceIdentitiesWithDeclared(root, format)
}

func admitRequestAliasPlan(req *pluginapi.RequestInterceptRequest, client string, preferred map[string]string) (*requestAliasPlan, error) {
	if req == nil || req.RequestID == "" {
		return nil, fmt.Errorf("request correlation is required")
	}
	sources, declared, err := requestAliasSourceIdentities(req.Body, req.SourceFormat)
	if err != nil {
		return nil, err
	}
	plan, err := buildRequestAliasPlan(client, sources, preferred, declared)
	if err != nil {
		return nil, err
	}
	plan.sourceFormat = normalizeSourceFormat(req.SourceFormat)
	plan.expected = requestChoiceCount(req.Body)
	if err := globalAliasPlanManager.set(req.RequestID, plan); err != nil {
		return nil, err
	}
	globalStreamManager.resetSession("req:"+req.RequestID, plan.client, plan.cachedUncloak, plan.expected)
	return plan, nil
}

func admitRequestAliasPlanOrReject(req *pluginapi.RequestInterceptRequest, resp pluginapi.RequestInterceptResponse, client string, preferred map[string]string) (*requestAliasPlan, []byte) {
	plan, err := admitRequestAliasPlan(req, client, preferred)
	if err == nil {
		return plan, nil
	}
	requestID := ""
	if req != nil {
		requestID = req.RequestID
	}
	debugLog("request alias admission rejected RequestID=%q client=%q: %v", requestID, client, err)
	return nil, toolCloak503Response(resp)
}

// sharedAliasesFor returns the merged preferred alias map for a client: the
// static cloak table targets plus the client's shared/neutral aliases. The
// result is used by admitRequestAliasPlan as the "preferred" map, so declared
// tools that have a static table entry get their proven target, and those in
// the shared alias map get their wp_ alias. Undeclared tools fall through to
// fallbackAliasForSource (deterministic wp_ext_<hash>).
func sharedAliasesFor(client string) map[string]string {
	cfg := activeFilterConfig()
	cloakTable := cfg.ToolMappings[client]
	var shared map[string]string
	switch client {
	case "claude_code":
		shared = claudeCodeSharedAliases
	case "codex":
		shared = codexSharedAliases
	case "oh_my_pi":
		shared = ompSharedAliases
	default:
		if cloakTable == nil {
			return nil
		}
		return cloakTable
	}
	merged := make(map[string]string, len(cloakTable)+len(shared))
	for k, v := range shared {
		merged[k] = v
	}
	for k, v := range cloakTable {
		merged[k] = v
	}
	if client == "oh_my_pi" {
		for k, v := range canonicalOMPSafeMappingSet {
			merged[k] = v
		}
	}
	return merged
}

// clientUsesAliasPlan reports whether a client should use the request-scoped
// alias plan machinery for full declaration cloaking. Clients on this list
// route through admitRequestAliasPlanOrReject instead of the legacy
// rewriteRequestBodyWithClient path, giving them fail-closed admission,
// per-request reversal, and deterministic fallback aliases for unknown
// declarations.
func clientUsesAliasPlan(client string) bool {
	return client == "claude_code" || client == "codex"
}

type explicitOMPRouteState struct {
	routeKind               explicitOMPRouteKind
	client                  string
	activeReverse           map[string]string
	cachedUncloak           *cachedUncloakPattern
	brandRestorationEnabled bool
	expected                int
	disposition             atomic.Int32
	malformed               bool
}

func (s *explicitOMPRouteState) getDisposition() streamDisposition {
	if s == nil {
		return streamDispositionNone
	}
	return streamDisposition(s.disposition.Load())
}

func (s *explicitOMPRouteState) setDisposition(target streamDisposition) {
	if s == nil {
		return
	}
	for {
		cur := s.disposition.Load()
		if streamDisposition(cur) >= target {
			return
		}
		if s.disposition.CompareAndSwap(cur, int32(target)) {
			return
		}
	}
}

type explicitOMPLifecycleManager struct {
	mu     sync.Mutex
	routes map[string]*explicitOMPRouteState
}

var globalLifecycleManager = newExplicitOMPLifecycleManager()

func newExplicitOMPLifecycleManager() *explicitOMPLifecycleManager {
	return &explicitOMPLifecycleManager{
		routes: make(map[string]*explicitOMPRouteState),
	}
}

func (m *explicitOMPLifecycleManager) setRoute(requestID string, state *explicitOMPRouteState) {
	if requestID == "" || state == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[requestID] = state
}

func (m *explicitOMPLifecycleManager) getRoute(requestID string) *explicitOMPRouteState {
	if requestID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routes[requestID]
}

func (m *explicitOMPLifecycleManager) deleteRoute(requestID string) {
	if requestID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.routes, requestID)
}

func newStreamSessionManager() *streamSessionManager {
	return &streamSessionManager{
		sessions: make(map[string]*streamSession),
	}
}

func (m *streamSessionManager) sessionKey(req *pluginapi.StreamChunkInterceptRequest) string {
	if req.RequestID != "" {
		return "req:" + req.RequestID
	}
	if req.Metadata != nil {
		for _, k := range []string{"request_id", "stream_id", "session_id", "trace_id", "id"} {
			if v, ok := req.Metadata[k].(string); ok && v != "" {
				return "meta:" + k + ":" + v
			}
		}
	}
	if req.RequestHeaders != nil {
		for _, h := range []string{"X-Request-Id", "X-Correlation-Id", "X-Amzn-Trace-Id"} {
			if v := req.RequestHeaders.Get(h); v != "" {
				return "req_hdr:" + h + ":" + v
			}
		}
	}
	if req.ResponseHeaders != nil {
		for _, h := range []string{"X-Request-Id", "X-Correlation-Id"} {
			if v := req.ResponseHeaders.Get(h); v != "" {
				return "resp_hdr:" + h + ":" + v
			}
		}
	}
	// For legacy chunks (schema < 3, ChunkIndex >= 0) where OriginalRequest/RequestBody
	// is populated on every chunk, use the FNV hash of the request body as session key.
	if req.ChunkIndex >= 0 {
		src := req.OriginalRequest
		if len(src) == 0 {
			src = req.RequestBody
		}
		if len(src) > 0 {
			h := fnv.New64a()
			_, _ = h.Write(src)
			return fmt.Sprintf("fnv:%x", h.Sum64())
		}
	}
	return ""
}

// resetSession (re)initializes the session for a fresh stream start, clearing
// any stale tail left by an aborted previous incarnation of the same key.
// expected is the request's choice count ("n"); omitted means 1.
func (m *streamSessionManager) resetSession(key, client string, cached *cachedUncloakPattern, expected ...int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStaleLocked()
	m.sessions[key] = newStreamSession(client, cached, expected...)
}

func newStreamSession(client string, cached *cachedUncloakPattern, expected ...int) *streamSession {
	n := 1
	if len(expected) > 0 && expected[0] > 1 {
		n = expected[0]
	}
	return &streamSession{
		client:       client,
		cached:       cached,
		updatedAt:    time.Now(),
		brandCarries: make(map[string]*brandLane),
		expected:     n,
	}
}

// ensureSession registers a session only when none lives under key yet.
// Losing a race to an already-registered session returns the incumbent;
// both racers derive (client, cached) from the same request body, so either
// outcome is correct.
func (m *streamSessionManager) ensureSession(key, client string, cached *cachedUncloakPattern, expected ...int) *streamSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[key]; sess != nil {
		return sess
	}
	sess := newStreamSession(client, cached, expected...)
	m.sessions[key] = sess
	return sess
}

// getSession returns the live session under key, or nil. Callers must treat the
// returned session as read-only: client and cached are set before publication and
// never rewritten, but the buffered tail and brand carries are mutated in place
// by the stream path.
func (m *streamSessionManager) getSession(key string) *streamSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[key]
}

func (m *streamSessionManager) getClient(key string) string {
	if sess := m.getSession(key); sess != nil {
		return sess.client
	}
	return ""
}

func (m *streamSessionManager) deleteSession(key string) {
	if key == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
}

func (m *streamSessionManager) cleanupStaleLocked() {
	cutoff := time.Now().Add(-5 * time.Minute)
	for k, s := range m.sessions {
		if !s.updatedAt.Before(cutoff) {
			continue
		}
		if strings.HasPrefix(k, "req:") {
			reqID := strings.TrimPrefix(k, "req:")
			if plan := globalAliasPlanManager.get(reqID); plan != nil {
				if s.payloadStarted || plan.getDisposition() == streamDispositionPayloadActive || len(s.tail) > 0 || len(s.laneProgress) > 0 {
					continue
				}
			}
			if route := globalLifecycleManager.getRoute(reqID); route != nil && route.routeKind == routeKindProtectedAGY {
				if s.payloadStarted || route.getDisposition() == streamDispositionPayloadActive || len(s.tail) > 0 || hasPendingBrandCarry(s) || len(s.laneProgress) > 0 {
					continue
				}
			}
		}
		delete(m.sessions, k)
	}
}

func (m *streamSessionManager) processChunk(req *pluginapi.StreamChunkInterceptRequest, format string) pluginapi.StreamChunkInterceptResponse {
	key := m.sessionKey(req)

	var protectedRoute *explicitOMPRouteState
	var aliasPlan *requestAliasPlan
	if req.RequestID != "" {
		protectedRoute = globalLifecycleManager.getRoute(req.RequestID)
		aliasPlan = globalAliasPlanManager.get(req.RequestID)
	}
	if aliasPlan != nil && aliasPlan.getDisposition() == streamDispositionCleanTerminal {
		debugLog("StreamSessionManager: alias-plan late chunk after clean terminal key=%s", key)
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
		if protectedRoute.malformed || protectedRoute.client != "oh_my_pi" {
			debugLog("StreamSessionManager: malformed ProtectedAGY route state key=%s", key)
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if protectedRoute.getDisposition() == streamDispositionCleanTerminal {
			debugLog("StreamSessionManager: late chunk after clean terminal key=%s", key)
			return pluginapi.StreamChunkInterceptResponse{}
		}
	}

	// Header-init chunk: schema_version >= 3 delivers OriginalRequest/RequestBody
	// here. Nothing to uncloak on this chunk; register the stream's session so
	// payload chunks resolve their client from cache.
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex || req.ChunkIndex < 0 {
		// Without a correlation key the future payload chunks cannot be
		// attributed back to this session — detection would be wasted work.
		if key == "" {
			return pluginapi.StreamChunkInterceptResponse{}
		}
		// If session was already pre-registered by request intercept, keep it.
		if sessClient := m.getClient(key); sessClient != "" {
			debugLog("StreamSessionManager: header-init using pre-registered session key=%s client=%s", key, sessClient)
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if aliasPlan != nil {
			if aliasPlan.getDisposition() == streamDispositionNone {
				m.ensureSession(key, aliasPlan.client, aliasPlan.cachedUncloak, aliasPlan.expected)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
			if protectedRoute.getDisposition() == streamDispositionNone {
				m.ensureSession(key, "oh_my_pi", protectedRoute.cachedUncloak, protectedRoute.expected)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		detectSrc := detectionRequestBody(req.OriginalRequest, req.RequestBody)
		n := requestChoiceCount(detectSrc)
		if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
			debugLog("StreamSessionManager: header-init UA evidence key=%s client=%s", key, uaClient)
			if cached := requestScopedUncloakPattern(uaClient, detectSrc, format); cached != nil && cached.re != nil {
				m.resetSession(key, uaClient, cached, n)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		_, client := buildUncloakTable(detectSrc, format)
		debugLog("StreamSessionManager: header-init key=%s client=%s", key, client)
		if client != "" {
			cached := requestScopedUncloakPattern(client, detectSrc, format)
			if cached != nil && cached.re != nil {
				m.resetSession(key, client, cached, n)
			}
		}
		return pluginapi.StreamChunkInterceptResponse{}
	}

	// Payload chunks carrying no correlation key cannot be tied to a stream.
	// They pass through unmolested rather than compete for shared state that
	// would corrupt concurrent streams' buffered tails.
	if key == "" {
		return pluginapi.StreamChunkInterceptResponse{}
	}

	// Opportunistic cleanup runs on every payload chunk, matching the
	// original behavior: a stream that dies between chunks must still be
	// pruned by later traffic.
	m.mu.Lock()
	m.cleanupStaleLocked()
	sess := m.sessions[key]
	m.mu.Unlock()
	if sess == nil {
		if aliasPlan != nil {
			if aliasPlan.getDisposition() == streamDispositionPayloadActive {
				debugLog("StreamSessionManager: alias-plan disposable session lost after payload started key=%s", key)
				return pluginapi.StreamChunkInterceptResponse{}
			}
			if aliasPlan.getDisposition() == streamDispositionNone {
				sess = m.ensureSession(key, aliasPlan.client, aliasPlan.cachedUncloak, aliasPlan.expected)
			}
		} else if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
			if protectedRoute.getDisposition() == streamDispositionPayloadActive {
				debugLog("StreamSessionManager: disposable session lost after payload started key=%s", key)
				return pluginapi.StreamChunkInterceptResponse{}
			}
			if protectedRoute.getDisposition() == streamDispositionNone {
				sess = m.ensureSession(key, "oh_my_pi", protectedRoute.cachedUncloak, protectedRoute.expected)
			}
		} else {
			sess = m.ensureFallbackSession(req, format, key)
		}
	}
	if sess == nil {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	// A cached pattern is what the tool-name uncloak pass needs, but not what
	// the brand reverse needs: that runs off the session's resolved client
	// alone. An alias-plan client pins its tool-name authority in the plan
	// rather than in a pattern, so it reaches here with cached == nil, and
	// bailing on that skipped the brand reverse for every such client: the
	// request went out cloaked and the response came back cloaked. Only bail
	// when no client was resolved. The SSE branch below already handles a nil
	// pattern (modified = completeEvents, no uncloak).
	if sess.cached == nil && (sess.client == "" || sess.client == negativeClientResolution) {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	m.mu.Lock()
	if aliasPlan != nil {
		aliasPlan.setDisposition(streamDispositionPayloadActive)
		sess.payloadStarted = true
	}
	if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
		protectedRoute.setDisposition(streamDispositionPayloadActive)
		sess.payloadStarted = true
	}
	cached := sess.cached

	// Reset buffer on first payload chunk (ChunkIndex == 0)
	if req.ChunkIndex == 0 {
		sess.tail = nil
	}

	var combined []byte
	if len(sess.tail) > 0 {
		combined = append(sess.tail, req.Body...)
	} else {
		combined = req.Body
	}

	// Check if this is an SSE-formatted stream vs individual JSON chunk payload
	isSSE := len(sess.tail) > 0
	if !isSSE {
		trimmed := bytes.TrimLeft(combined, " \t\r\n")
		isSSE = bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.Contains(combined, []byte("\n\n")) || bytes.Contains(combined, []byte("\r\n\r\n"))
	}
	if isSSE {
		completeEvents, incompleteTail := splitSSEEventsWithNewBytes(combined, len(req.Body))
		if len(completeEvents) == 0 {
			if len(sess.tail) == 0 {
				sess.tail = append([]byte(nil), req.Body...)
			} else {
				sess.tail = combined
			}
			sess.updatedAt = time.Now()
			m.mu.Unlock()

			debugLog("StreamSessionManager: no complete events, dropping chunk len=%d", len(incompleteTail))
			return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
		}

		if len(incompleteTail) > 0 {
			sess.tail = append([]byte(nil), incompleteTail...)
		} else {
			sess.tail = nil
		}
		sess.updatedAt = time.Now()
		m.mu.Unlock()

		var modified []byte
		changed := false
		if cached != nil && cached.re != nil {
			if mod, ch := uncloakStreamChunk(completeEvents, cached); ch {
				modified = mod
				changed = true
			} else {
				modified = completeEvents
			}
		} else {
			modified = completeEvents
		}
		// Every resolved client runs a brand reverse, not just Oh My Pi.
		// reverseBrandSSE dispatches: oh_my_pi takes the protected-brand lane,
		// everyone else takes its own client's reverse set. A session
		// with no resolved client has nothing to restore against, so it is
		// left alone rather than guessed at.
		brandChanged := false
		if sess.client != "" && sess.client != negativeClientResolution {
			if bm, bc := m.reverseBrandSSE(sess, modified, format); bc {
				modified = bm
				brandChanged = true
			}
		}
		overallChanged := changed || brandChanged
		isDone := sseContainsOpenAIDone(completeEvents) || sseContainsOpenAIDone(modified)
		isAnthropicEnd := format == "anthropic" && (sseContainsAnthropicMessageStop(completeEvents) || sseContainsAnthropicMessageStop(modified))
		if isDone || isAnthropicEnd {
			if isDone && sess.client == "oh_my_pi" {
				// Safety net for [DONE] embedded in a multi-line event that
				// reverseBrandSSE's per-event check misses. generateBrandFlushEvents
				// drains each lane as it emits, so the common case (flush events
				// already written before the [DONE] frame) is a no-op here — no
				// double emission.
				if flush := m.generateBrandFlushEvents(sess, format); len(flush) > 0 {
					doneIdx := sseOpenAIDoneIndex(modified)
					var tmp bytes.Buffer
					if doneIdx >= 0 {
						tmp.Write(modified[:doneIdx])
						for _, fe := range flush {
							tmp.Write(fe)
						}
						tmp.Write(modified[doneIdx:])
					} else {
						for _, fe := range flush {
							tmp.Write(fe)
						}
						tmp.Write(modified)
					}
					modified = tmp.Bytes()
					overallChanged = true
				}
			}
			if !hasPendingBrandCarry(sess) {
				m.deleteSession(key)
				if aliasPlan != nil {
					aliasPlan.setDisposition(streamDispositionCleanTerminal)
				}
				if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
					protectedRoute.setDisposition(streamDispositionCleanTerminal)
				}
			}
		}
		// Empty Body lets the host forward only the current input chunk. After
		// reassembly or withholding a partial next event, emit the complete bytes.
		if !overallChanged && bytes.Equal(modified, req.Body) {
			return pluginapi.StreamChunkInterceptResponse{}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: modified}
	}

	// Standalone JSON chunk payload (e.g. CLIProxyAPI OpenAI protocol chunk)
	sess.tail = nil
	sess.updatedAt = time.Now()
	m.mu.Unlock()

	var modified []byte
	changed := false
	if cached != nil && cached.re != nil {
		if mod, ch := uncloakStreamChunk(req.Body, cached); ch {
			modified = mod
			changed = true
		} else {
			modified = req.Body
		}
	} else {
		modified = req.Body
	}
	// Every resolved client runs the brand reverse here, not just Oh My Pi: the
	// OpenAI-protocol exits hand the plugin unframed JSON chunks (the host adds
	// `data: ` itself after interception), so this is the path a codex or
	// claude_code session actually takes on those routes.
	if sess.client != "" && sess.client != negativeClientResolution {
		if bm, bc := m.reverseBrandStandalone(sess, modified, format); bc {
			modified = bm
			changed = true
		}
		// A non-null finish_reason ends ONLY its own choice lane: flush that
		// lane's held carry into this chunk. The session is freed — and every
		// remaining carry flushed — only at true stream completion (bare
		// [DONE], message_stop, or finish_reasons covering every expected
		// lane); unfinished lanes keep streaming.
		finished, done := markStandaloneFinishes(sess, modified)
		if done || len(finished) > 0 {
			only := finished
			if done {
				only = nil
			}
			if bm, fc := m.flushBrandStandalone(sess, modified, format, only); fc {
				modified = bm
				changed = true
			}
		}
		if done && !hasPendingBrandCarry(sess) {
			m.deleteSession(key)
			if aliasPlan != nil {
				aliasPlan.setDisposition(streamDispositionCleanTerminal)
			}
			if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
				protectedRoute.setDisposition(streamDispositionCleanTerminal)
			}
		}
	}
	if !changed {
		if bytes.Equal(modified, req.Body) {
			return pluginapi.StreamChunkInterceptResponse{}
		}
	}
	return pluginapi.StreamChunkInterceptResponse{Body: modified}
}

// ensureFallbackSession handles schema_version < 3 streams, where every payload
// chunk repeats OriginalRequest/RequestBody, plus the lazy case where a keyed
// header-init carried no detectable client but later chunks might. Detection
// deliberately runs WITHOUT holding the manager lock: parsing a large request
// body under the lock serialized every concurrent stream's chunk processing.
func (m *streamSessionManager) ensureFallbackSession(req *pluginapi.StreamChunkInterceptRequest, format, key string) *streamSession {
	if key == "" {
		return nil
	}
	src := detectionRequestBody(req.OriginalRequest, req.RequestBody)
	n := requestChoiceCount(src)
	if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
		if cached := requestScopedUncloakPattern(uaClient, src, format); cached != nil && cached.re != nil {
			debugLog("StreamSessionManager: fallback UA evidence key=%s client=%s", key, uaClient)
			return m.ensureSession(key, uaClient, cached, n)
		}
	}
	if len(src) == 0 {
		return nil
	}
	_, client := buildUncloakTable(src, format)
	debugLog("StreamSessionManager: fallback detect key=%s client=%s", key, client)
	if client == "" {
		return nil
	}
	cached := requestScopedUncloakPattern(client, src, format)
	if cached == nil || cached.re == nil {
		return nil
	}
	return m.ensureSession(key, client, cached, n)
}

// splitSSEEventsWithNewBytes splits combined bytes into complete SSE events
// and an incomplete trailing tail. Complete events are those terminated by
// "\n\n" (or "\r\n\r\n") and include the terminating delimiter; only the newly
// arrived newLen bytes plus up to 3 preceding bytes need rescanning.
func splitSSEEventsWithNewBytes(data []byte, newLen int) (completeEvents []byte, incompleteTail []byte) {
	n := len(data)
	if n == 0 {
		return nil, data
	}
	// Only the newly arrived bytes plus at most 3 preceding bytes can form or extend a delimiter.
	scanStart := n - newLen - 3
	if scanStart < 0 {
		scanStart = 0
	}
	window := data[scanStart:]
	for i := len(window) - 1; i >= 1; i-- {
		if window[i] != '\n' {
			continue
		}
		if window[i-1] == '\n' || (i >= 3 && window[i-3] == '\r' && window[i-2] == '\n' && window[i-1] == '\r') {
			splitAt := scanStart + i + 1
			return data[:splitAt], data[splitAt:]
		}
	}
	return nil, data
}

func uncloakJSONNode(node any, uncloakTable map[string]string, sourceFormat string) bool {
	return uncloakJSONNodeOpt(node, uncloakTable, sourceFormat, false)
}

func uncloakJSONNodeExact(node any, uncloakTable map[string]string, sourceFormat string) bool {
	return uncloakJSONNodeOpt(node, uncloakTable, sourceFormat, true)
}

func uncloakJSONNodeOpt(node any, uncloakTable map[string]string, sourceFormat string, exactOnly bool) bool {
	changed := false

	lookupFn := lookupUncloak
	if exactOnly {
		lookupFn = func(name string, table map[string]string) (string, bool) {
			orig, exists := table[name]
			return orig, exists
		}
	}

	switch typed := node.(type) {
	case map[string]any:
		if sourceFormat == "openai" {
			if msg, ok := typed["message"].(map[string]any); ok {
				if toolCalls, ok := msg["tool_calls"].([]any); ok {
					for _, tcRaw := range toolCalls {
						if tc, ok := tcRaw.(map[string]any); ok {
							if fn, ok := tc["function"].(map[string]any); ok {
								if name, ok := fn["name"].(string); ok {
									if orig, exists := lookupFn(name, uncloakTable); exists {
										fn["name"] = orig
										changed = true
									}
								}
							}
						}
					}
				}
			}
			if delta, ok := typed["delta"].(map[string]any); ok {
				if toolCalls, ok := delta["tool_calls"].([]any); ok {
					for _, tcRaw := range toolCalls {
						if tc, ok := tcRaw.(map[string]any); ok {
							if fn, ok := tc["function"].(map[string]any); ok {
								if name, ok := fn["name"].(string); ok {
									if orig, exists := lookupFn(name, uncloakTable); exists {
										fn["name"] = orig
										changed = true
									}
								}
							}
						}
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			if typeVal, ok := typed["type"].(string); ok && typeVal == "tool_use" {
				if name, ok := typed["name"].(string); ok {
					if orig, exists := lookupFn(name, uncloakTable); exists {
						typed["name"] = orig
						changed = true
					}
				}
			}
		}

		for _, v := range typed {
			if childChanged := uncloakJSONNodeOpt(v, uncloakTable, sourceFormat, exactOnly); childChanged {
				changed = true
			}
		}

	case []any:
		for _, v := range typed {
			if childChanged := uncloakJSONNodeOpt(v, uncloakTable, sourceFormat, exactOnly); childChanged {
				changed = true
			}
		}
	}

	return changed
}

func mustEnvelope(result any) []byte {
	raw, err := json.Marshal(pluginabi.Envelope{OK: true, Result: mustRawMessage(result)})
	if err != nil {
		return mustErrorEnvelope("marshal_error", err.Error())
	}
	return raw
}

func mustErrorEnvelope(code, message string) []byte {
	raw, err := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"marshal_error","message":"failed to encode plugin response"}}`)
	}
	return raw
}

func mustRawMessage(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// safeUnmarshal decodes JSON preserving number precision by using json.Number
// instead of float64 for all numeric values.
func safeUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// safeMarshal encodes JSON without escaping HTML characters (<, >, &) to their
// unicode equivalents (\u003c, \u003e, \u0026), preserving raw text fidelity.
func safeMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder.Encode appends a trailing newline; strip it.
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// antigravityIdentity is the opening <identity> line the real Antigravity CLI
// sends, captured verbatim from a native session's system prompt. The main
// session and its subagents share it.
const antigravityIdentity = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding."

// antigravityHostPreserve is the native Antigravity host, a real operational
// domain rather than brand prose. The bare "Antigravity" reverse rule carries
// it as an exclusion: the dot after the word is a non-word byte, so the rule's
// right boundary passes inside "antigravity.google" and would otherwise hand
// the client a host that does not exist ("omp.google", "Codex.google"). Naming
// the exclusion is also what makes the reverse chunk-safe - a lane holds the
// word while this suffix is still arriving instead of resolving it early.
const antigravityHostPreserve = ".google"

// Forward brand tables, one per client. Exactly one is consulted per request:
// the resolved client — an explicit X-Cloak-Client header first, then verified
// User-Agent evidence, then body detection — selects its own table and no other.
// Adding a client (opencode, cursor, ...) is one table here plus one line in
// brandMappingsByClient; nothing else in the rewrite path changes.
//
// A client's forward and reverse table are declared together, because they are
// two halves of one identity: whatever that client's own words are rewritten to
// must be rewritten back to that same client's words, and nothing else.
var claudeCodeBrandMappings = slices.Concat(
	[]rewriteMapping{
		// Opening identity lines. Each names the vendor twice, so token substitution
		// alone yields nonsense ("Antigravity, Google's official CLI for
		// Antigravity"). The replacements are the verbatim <identity> lines used by
		// the real Antigravity CLI, for both its main session and its subagents, so a
		// cloaked request is indistinguishable from native traffic.
		// Must precede every other "Claude"/"Anthropic" rule.
		{Match: "You are Claude Code, Anthropic's official CLI for Claude.", Replacement: antigravityIdentity},
		{Match: "You are a Claude agent, built on Anthropic's Claude Agent SDK.", Replacement: antigravityIdentity},
		{Match: "Claude Code", Replacement: "Antigravity"},
		// The context group Claude Code injects into the system context, global
		// and project-local alike, per its documented memory hierarchy:
		//
		//   ~/.claude/CLAUDE.md        user's private global instructions
		//   ~/.claude/rules/*.md      user rules
		//   ~/.claude/projects/<slug>/memory/   per-project auto memory
		//   ./.claude/CLAUDE.md        project instructions
		//   ./.claude/rules/*.md       project rules
		//
		// Built by claudeContextMappings, which matches the home directory as a
		// whole segment at any position because that is the only spelling Claude
		// Code actually emits on Windows. See pathRules.
	},
	claudeContextMappings,
	[]rewriteMapping{
		// Only claude_code needs this: its table also carries the bare "Claude"
		// rule, which would otherwise consume the vendor prefix inside CLAUDE.md
		// and produce Antigravity.md - which the reverse can only restore to
		// Claude.md, corrupting the file name's case. Codex and Oh My Pi have no
		// bare-Claude rule, so their CLAUDE.md passes through untouched already.
		{Match: "CLAUDE.md", Replacement: "AGENTS.md"},
		// Official product/URL names, taken from antigravity.google and its docs.
		// The product is "Antigravity SDK" (pip install google-antigravity); the
		// platform lives on antigravity.google.
		//
		// These two forward rules share one target deliberately: the single
		// reverse rule ("Antigravity SDK" -> "Anthropic SDK") is the approved
		// resolution, so a client that wrote "Claude Agent SDK" reads "Anthropic
		// SDK" back. Do not add a second reverse target to tell them apart; no
		// evidence-backed one exists.
		{Match: "Anthropic SDK", Replacement: "Antigravity SDK"},
		{Match: "Claude Agent SDK", Replacement: "Antigravity SDK"},
		{Match: "claude.ai", Replacement: "antigravity.google"},
		// Deliberately no client model IDs here. All four rows that used to sit
		// here - claude-fable-5-1 and claude-opus-5-5 (both mapped onto
		// gemini-3.1-pro-low, two sources with one target), claude-sonnet-5
		// (gemini-3.8-flash) and claude-haiku-4-5-20251001
		// (gemini-3.5-flash-lite) - were one-way: none of them can be inverted
		// from a route the gateway serves, and keeping only the two that look
		// one-to-one would hand the client back an AGY model name it never
		// wrote. A one-way rewrite is the failure this table exists to avoid:
		// the request looks clean while the client is handed a model name it
		// cannot resolve. Brand-masking a prose model ID is not worth that.
		// Claude Code's multi-agent workflow tool. Antigravity ships the same
		// capability as teamwork_preview_layer, so a cloaked request names the tool
		// Antigravity traffic would actually use. It is a client-specific surface,
		// not a brand token.
		{Match: "Workflow", Replacement: "teamwork_preview_layer"},
		// Plural of the tool name above. Matching is word-bounded, so "Workflows" in
		// prose is a distinct token and would otherwise survive. Grammar is
		// deliberately not repaired, same policy as the bare-brand rules below.
		{Match: "Workflows", Replacement: "teamwork_preview_layer"},
		// Bare vendor name. Mapped to "Google Deepmind" rather than a plain "Google"
		// so the reverse pass has a token specific enough to match safely: a bare
		// "Google" would fire on ordinary response prose, while "Google Deepmind" is
		// exactly the vendor's own name and nothing else.
		{Match: "Anthropic", Replacement: "Google Deepmind"},
		// Catch-all. Must stay after every longer "Claude" form above.
		{Match: "Claude", Replacement: "Antigravity"},
	},
)

// codexIdentityLine is the opening sentence the Codex client really ships,
// verified verbatim in
// .ref/CLIProxyAPI/internal/registry/models/codex_client_models.json:
// "You are Codex, an agent based on GPT-6. You and the user share one
// workspace, ...". It is replaced whole, the way the Claude Code and Oh My Pi
// identities are, because token substitution alone leaves a sentence that names
// neither this product nor its vendor. Longer Codex identity prose after it
// (including the OpenAI spelling some builds ship) is handled by the residual
// rules below, so this stays a sentence rule and not a whole-prompt rule.
const codexIdentityLine = "You are Codex, an agent based on GPT-6."

// codexGeneralIdentityLine is the second opening sentence the Codex client
// ships, supplied from a real session:
//
//	You are Codex, an OpenAI general-purpose agentic assistant that helps the
//	user complete tasks across coding, browsing, apps, documents, research, and
//	other digital workflows.
//
// It is replaced whole for the same reason as the line above, and it has to be
// a whole-sentence rule rather than residual substitution: "OpenAI" alone would
// turn it into "an Google Deepmind general-purpose agentic assistant", which is
// both grammatically wrong and a sentence no client ever wrote.
const codexGeneralIdentityLine = "You are Codex, an OpenAI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."

var codexBrandMappings = slices.Concat(

	// Codex keeps its file names; only the home directory is rewritten. See
	// pathRules for why the match is not anchored to a ~/ or ./ spelling.
	codexContextMappings,
	[]rewriteMapping{
		// Whole identity first, before any shorter token. Both shipped opening
		// sentences are terminal rewrites: the native identity line replaces
		// them whole, so the vendor word inside them never reaches the residual
		// rules and cannot come out as "an Google Deepmind ..." prose.
		{Match: codexIdentityLine, Replacement: antigravityIdentity},
		{Match: codexGeneralIdentityLine, Replacement: antigravityIdentity},
		// Residual client-scoped prose. Codex's own prompts carry the client
		// name and the model family it is built on ("based on GPT-6"); some
		// builds' longer identity prose names the vendor as OpenAI. Each pair
		// is inverted exactly by codexReverseBrandMappings, in this same order.
		{Match: "OpenAI", Replacement: "Google Deepmind"},
		{Match: "GPT-6", Replacement: "Gemini 3"},
		// Bare client name, after every longer form above. Its casing follows
		// the text it matched (Codex -> Antigravity, codex -> antigravity,
		// CODEX -> ANTIGRAVITY) via isProseBrandRule, so one rule covers all
		// three spellings and none can shadow another.
		{Match: "Codex", Replacement: "Antigravity"},
	},
)

var ompBrandMappings = []rewriteMapping{
	// Same whole-sentence rewrite as the protected route, for an OMP marker
	// that did not take the protected branch. See ompIdentityLines.
	ompIdentityLines[0],
	// Oh My Pi reads the user's GLOBAL Claude memory, ~/.claude/CLAUDE.md
	// (discovery/claude.ts:65-69,163-188), while its own root context file is a
	// neutral AGENTS.md (discovery/agents-md.ts:21). That global file exists on
	// disk, so its path is a real operational identifier on this route. Only
	// this one file is remapped, in both spellings plus the JSON-escaped one,
	// and as a whole path element: the .claude directory itself stays verbatim,
	// because a blanket .claude -> .gemini would collide with .omp -> .gemini
	// and make the reverse ambiguous. There is no project-local
	// .claude/CLAUDE.md in play, so nothing else is claimed.
	// ompProtectedReverseTable inverts this pair.
	{Match: ".claude/CLAUDE.md", Replacement: ".gemini/AGENTS.md", WholeSegment: true},
	{Match: `.claude\CLAUDE.md`, Replacement: `.gemini\AGENTS.md`, WholeSegment: true},
	{Match: `.claude\\CLAUDE.md`, Replacement: `.gemini\\AGENTS.md`, WholeSegment: true},
	// Oh My Pi coding agent & harness. On the protected route these go through
	// the sentinel path instead; these entries cover an OMP marker that did not
	// take the protected branch. The bare alias carries the scheme exclusion so
	// a real "omp://" internal URI stays byte-for-byte.
	{Match: "Oh My Pi", Replacement: "Antigravity"},
	{Match: "oh-my-pi", Replacement: "Antigravity"},
	{Match: "omp", Replacement: "Antigravity", NotFollowedBy: ompSchemePreserve},
}

// brandMappingsByClient is the whole forward surface. A resolved client with no
// entry has no forward brand table, so its request body is left untouched.
var brandMappingsByClient = map[string][]rewriteMapping{
	"claude_code": claudeCodeBrandMappings,
	"codex":       codexBrandMappings,
	"oh_my_pi":    ompBrandMappings,
}

// Reverse tables, same shape and the same selection rule by resolved client. A
// reverse entry's Match is what that client's forward table produced, so the two
// are read together: claude_code rewrites Claude -> Antigravity and reads
// Antigravity -> Claude back.
//
// Oh My Pi has no entry here: it runs the protected brand lane, whose
// Antigravity -> omp pair is its own reverse authority.
var claudeCodeReverseBrandMappings = slices.Concat(
	// The context group, including the JSON-escaped spelling of its Windows
	// file rule that arrives inside streamed tool-call arguments. Generated by
	// pathRules with the pairs swapped, so the reverse of every forward rule is
	// its exact mirror and the escaped twin cannot drift out of the group.
	claudeReverseContextMappings,
	[]rewriteMapping{
		// Deliberately no bare "GEMINI.md" rule: the forward pass always
		// rewrites the directory with it, so matching the file name alone would
		// invert a path the forward pass never produced. That drift is what
		// collapsed a README edit into a no-op during live acceptance.
		// Inverse of the forward domain rule. It has to precede the bare brand
		// word below, or "Antigravity" would match inside "antigravity.google"
		// and the client would receive "Claude.google".
		{Match: "antigravity.google", Replacement: "claude.ai", ArgumentSafe: true},
		// Inverse of the bare vendor rule. Safe to reverse precisely because the
		// replacement is the two-word vendor name and not the bare "Google", which
		// would collide with ordinary prose in model output.
		{Match: "Google Deepmind", Replacement: "Anthropic"},
		// A client-declared identifier, like a tool name: the skill slug lives in
		// the client's own registry, so the response has to hand back a slug the
		// client can actually resolve. Without this the model calls
		// Skill("Antigravity-api") and the client answers "Unknown skill".
		{Match: "Antigravity-api", Replacement: "claude-api", ArgumentSafe: true},
		{Match: "Antigravity SDK", Replacement: "Anthropic SDK", ArgumentSafe: true},
		// Inverse of the prose rule that names this client's multi-agent
		// workflow tool. Antigravity ships the capability as
		// teamwork_preview_layer, so the model reads - and may echo - that name
		// in its prose; the client only ever declared "Workflow". A mention in
		// prose is not a declaration, so the exact alias-plan authority cannot
		// restore it and this table has to. The plural forward rule maps back
		// onto the same singular name: grammar is not repaired in either
		// direction.
		//
		// Prose only - deliberately not ArgumentSafe. The forward pass writes
		// this literal into prompts, never into a tool argument, so a
		// teamwork_preview_layer literal inside an argument is the client's own
		// text (a command the user typed, a file the model was told to write)
		// and rewriting it corrupts data the client executes. The tool NAME in
		// an argument or a declaration is a different surface: it round-trips
		// through the alias plan's own exact authority.
		{Match: "teamwork_preview_layer", Replacement: "Workflow"},
		// The bare brand word, listed last so the two longer "Antigravity" tokens
		// above win the prefix. This client's forward pass produced that word only
		// from its own name, so inverting it here is unambiguous.
		{Match: "Antigravity", Replacement: "Claude"},
	},
)

// pathRules builds one direction of a client context group. The home directory
// is matched as a whole segment at ANY position, not only in a ~/ or ./
// spelling. That is not a preference: live acceptance measured 114 absolute
// paths per request going to the model uncloaked, because Claude Code on
// Windows puts C:\Users\<name>\.claude\transcripts and friends in its system
// context and never uses the tilde form at all. A home-prefixed match misses
// every one of them.
//
// Both pairs are passed in MATCH-then-REPLACE order, so a reverse table is this
// same call with both pairs swapped, and TestContextGroupsAreExactInverses is
// what holds that true.
//
// Every rule is WholeSegment, so the directory has to begin a whole path
// element: "foo.claude/settings.json" and "foo.codex/config.toml" are not
// matches and are handed through byte-for-byte. The dot itself is not a word
// byte, so the generic word-boundary test cannot see the element a ".claude" is
// glued to; this flag is that test.
//
// Each separator spelling gets two rules, and the order is load-bearing three
// deep: the file rule must precede its own directory rule so its longer match
// wins, and the JSON-ESCAPED file rule must precede the plain "\" directory
// rule. A path inside a tool call's arguments arrives as JSON text, not as
// decoded prose - the model escapes every backslash when it writes the path
// into OpenAI function.arguments or Anthropic partial_json - so the plain
// directory rule would consume the directory, leave the file name behind, and
// hand the client ".claude\\GEMINI.md": half its file name, half ours. The
// escaped directory rule is not needed, because the plain directory rule
// already leaves the second backslash of the pair in place.
func pathRules(fromDir, toDir, fromFile, toFile string) []rewriteMapping {
	fileRule := func(sep string) rewriteMapping {
		return rewriteMapping{
			Match:        fromDir + sep + strings.ReplaceAll(fromFile, "/", sep),
			Replacement:  toDir + sep + strings.ReplaceAll(toFile, "/", sep),
			WholeSegment: true,
			// A path is what the model actually writes into a command or a
			// file, so every rule of this group runs over tool arguments too.
			ArgumentSafe: true,
		}
	}
	dirRule := func(sep string) rewriteMapping {
		return rewriteMapping{Match: fromDir + sep, Replacement: toDir + sep, WholeSegment: true, ArgumentSafe: true}
	}
	return []rewriteMapping{
		fileRule("/"),
		dirRule("/"),
		fileRule(`\`),
		// The escaped spelling of fileRule(`\`): JSON writes each backslash
		// twice, and JSON never escapes a forward slash, so the "/" rules above
		// need no twin.
		fileRule(`\\`),
		dirRule(`\`),
	}
}

// Claude Code keeps its file name too, because CLAUDE.md is brand-bearing
// prose the model would otherwise quote straight back.
var claudeContextMappings = pathRules(".claude", ".gemini", "CLAUDE.md", "GEMINI.md")

var claudeReverseContextMappings = pathRules(".gemini", ".claude", "GEMINI.md", "CLAUDE.md")

// Codex keeps its file names, per codex-rs/core/src/agents_md.rs: AGENTS.md is
// already the neutral name Codex reads, so there is no brand in the file name to
// hide and none is invented. Only the directory is rewritten.
var codexContextMappings = pathRules(".codex", ".gemini", "AGENTS.md", "AGENTS.md")

var codexReverseContextMappings = pathRules(".gemini", ".codex", "AGENTS.md", "AGENTS.md")

var codexReverseBrandMappings = slices.Concat(
	// The exact inverse of the forward context group. Not a blanket ".gemini/"
	// -> ".codex/": the forward pass never introduced one, so a blanket here
	// would rewrite a ".gemini" the user typed.
	codexReverseContextMappings,
	[]rewriteMapping{
		// Exact inverses of the residual identity prose, in forward order.
		{Match: "Google Deepmind", Replacement: "OpenAI"},
		{Match: "Gemini 3", Replacement: "GPT-6"},
		// The bare brand word, last so the longer tokens above win the prefix.
		// The exclusion is what keeps the real Antigravity host intact: the dot
		// after "Antigravity" is a non-word byte, so its right boundary passes
		// and this rule would otherwise hand the client "Codex.google", a host
		// that does not exist. Codex's forward pass never introduces that
		// domain, so nothing there has to be restored, and an exclusion is the
		// right shape where a repair rule would have to invent an "omp.google"
		// or "Codex.google" source domain that the contract does not have.
		{Match: "Antigravity", Replacement: "Codex", NotFollowedBy: antigravityHostPreserve},
	},
)

var reverseBrandMappingsByClient = map[string][]rewriteMapping{
	"claude_code": claudeCodeReverseBrandMappings,
	"codex":       codexReverseBrandMappings,
}

// brandMappingsFor returns the forward table for one client, or nil when the
// client has none. Order within a table is load-bearing: a longer token that
// contains a shorter one must run first.
func brandMappingsFor(client string) []rewriteMapping {
	return brandMappingsByClient[client]
}

// reverseBrandMappingsFor returns the reverse table for one client, or nil when
// the client has none. Order is load-bearing here too, for the bare
// "Antigravity" token in particular.
func reverseBrandMappingsFor(client string) []rewriteMapping {
	return reverseBrandMappingsByClient[client]
}

type rewriteMapping struct {
	Match       string
	Replacement string
	// Client scopes the rule to one resolved client id (e.g. "claude_code").
	// Empty applies the rule to every client.
	Client string
	// WholeSegment requires the match to begin a whole path element: the byte
	// before it may not be a word byte, so ".claude/" is not a match inside
	// "foo.claude/settings.json". Set on the path groups (pathRules and the
	// client path pairs), where the match begins with a dot and the generic
	// word-boundary test cannot see the element it is glued to. Left off prose
	// rules, which are matched wherever their own word boundary allows.
	WholeSegment bool
	// SegmentEnd requires the match to END a whole path element: the byte after
	// it must be end of string or a path separator. It is the right-hand half of
	// WholeSegment, and the reason the bare ".gemini" reverse rule cannot hand
	// the client ".omp-backup": the generic word boundary accepts a dot or a
	// hyphen after the match, so ".gemini-backup" and ".gemini.foo" would be
	// rewritten into paths the forward pass deliberately never produced. Only
	// the bare form needs it - a rule that ends in "/" or "\" already forces its
	// own right boundary.
	SegmentEnd bool
	// NotFollowedBy is an exclusion suffix: the rule does not apply when the
	// bytes immediately after the match begin with this string. It is how a
	// rule names a spelling that must survive it untouched - the OMP scheme
	// "omp://" on the forward pass, and the real Antigravity host
	// "antigravity.google" on the reverse pass, where the bare brand rule would
	// otherwise hand the client "omp.google". Chunk-safe: the stream lanes hold
	// a complete match whose exclusion suffix is still arriving rather than
	// resolving it early.
	NotFollowedBy string
	// ArgumentSafe marks a rule the reverse pass may also run over a
	// tool-argument carrier (`input_json_delta.partial_json`,
	// `tool_use.input`, `tool_calls[].function.arguments`).
	//
	// An argument is DATA the client executes or writes to disk, not text it
	// displays, so only an operational identifier the model can legitimately
	// echo from the cloaked prompt belongs there: a path, an SDK or domain name,
	// a client-declared skill or tool identifier. A bare prose brand word does
	// not: the forward pass never writes one into an argument (it rewrites
	// prompts, never tool arguments), so a blind reverse only corrupts literals
	// the model copied from the user - `cd antigravity-cloak && go test ./...`
	// came back as `cd Claude-cloak` and the client ran a directory that does not
	// exist. Prose rules stay prose: they reverse assistant text, and this flag
	// keeps them out of the argument carriers.
	ArgumentSafe bool
}

// reverseCloakedBrandBody maps the tokens this plugin introduced back onto the
// client's own spelling, so a cloaked conversation reads to the client exactly
// as it did before. It runs after tool-name uncloaking and uses the same
// recursive walk as the forward pass.
//
// This is separate from reverseBrandInResponseBody, which is the Oh My Pi
// protected-brand policy (Antigravity -> omp) and has its own chunk-safe
// streaming lane. For claude_code and codex the two tables do share the bare
// "Antigravity" token: those clients run this pass and invert it to their own
// name, while Oh My Pi runs the protected lane and inverts it to omp. No client
// runs both, so the token is still inverted exactly once.
func reverseCloakedBrandBody(body []byte, client string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}
	// Carrier-aware, never a recursive all-string walk. The walk used to reach
	// tool_use.name, so a client-declared tool that happens to be spelled like
	// a cloaked token (Claude Code declares "Antigravity-api") was rewritten
	// AFTER exact uncloak had already restored it, and the client got a tool it
	// never declared. Tool identity, ids, types and metadata belong to the
	// exact alias-plan authority alone; brand reverse owns assistant prose and
	// the VALUES inside tool arguments.
	if !reverseAssistantBrandInJSON(root, "", client) {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

var defaultCloakTables = map[string]map[string]string{
	"claude_code": {
		// Tier-1: Core file/shell tools — proven one-to-one AGY role names.
		"Bash": "run_command", "Edit": "replace_file_content", "Read": "view_file",
		"Write": "write_to_file", "Grep": "grep_search",
		"Glob": "find_by_name", // Issue #36: was list_dir — AGY list_dir lists one dir, Glob is a pattern matcher.
		// Tier-1: Subagent/interaction — proven AGY role names.
		"Agent": "invoke_subagent", "AskUserQuestion": "ask_question",
		// Tier-1: Web tools — proven AGY role names (one-to-one, no schema conflict).
		"WebSearch": "search_web", "WebFetch": "read_url_content",
		// Tier-2 tools (ListAgents, SendMessage, TaskStop, ListMcpResourcesTool,
		// ReadMcpResourceTool, etc.) are in claudeCodeSharedAliases with approved
		// parent #32 vocabulary — they have no proven one-to-one AGY role.
	},
	"codex": {
		// Code mode is the default for every routed provider here, so this table
		// keys on what a code-mode session actually declares. The list below was
		// read off this plugin's own debug log for a live cpa/agy session over
		// opencodex's openai-chat adapter (2026-09-12), which delivers Chat
		// Completions:
		//
		//   exec, wait, request_user_input, request_user_input_async,
		//   clock__sleep, collaboration__followup_task,
		//   collaboration__interrupt_agent, collaboration__list_agents,
		//   collaboration__send_message, collaboration__spawn_agent,
		//   collaboration__wait_agent, web_search
		//
		// Namespaced children are keyed flattened, the way opencodex's
		// namespacedToolName lowers them ("<namespace>__<child>") so they survive
		// the chat-completions function-tool format. That exact spelling is what
		// the request-scoped reverse matches a declared name against, so these
		// pairs survive narrowing; a bare "spawn_agent" key would not.
		//
		// Only names that occupy a tool-name position are listed. The helpers that
		// exist solely as prose inside the "exec" description -- apply_patch,
		// exec_command, write_stdin, view_image, tool_search, the goal and
		// MCP-resource tools -- stay pass-through deliberately: the reverse path
		// restores a name only where it appears as a tool name, so cloaking prose
		// would hand the client a helper it never declared.
		//
		// "exec" is the code-mode entry point and owns run_command in this static
		// table. A shell-mode session declares exec_command instead, which maps to
		// run_command via codexSharedAliases; it is kept out of defaultCloakTables
		// so defaultUncloakTables stays injective at init(), and is admitted per-request
		// through the alias plan machinery without collision.
		"exec":                       "run_command",
		"web_search":                 "search_web",
		"request_user_input":         "ask_question",
		"collaboration__spawn_agent": "invoke_subagent",
	},
	"oh_my_pi": {
		"read":       "view_file",
		"write":      "write_to_file",
		"edit":       "replace_file_content",
		"bash":       "run_command",
		"grep":       "grep_search",
		"glob":       "find_by_name",
		"task":       "invoke_subagent",
		"ask":        "ask_question",
		"web_search": "search_web",
	},
}

// canonicalOMPSafeMappingSet is the canonical nine-tool Safe Mapping Set for Oh My Pi.
var canonicalOMPSafeMappingSet = map[string]string{
	"read":       "view_file",
	"write":      "write_to_file",
	"edit":       "replace_file_content",
	"bash":       "run_command",
	"grep":       "grep_search",
	"glob":       "find_by_name",
	"task":       "invoke_subagent",
	"ask":        "ask_question",
	"web_search": "search_web",
}

// ompSourceIdentityInventory contains the static OMP source tool names used
// for request body detection in detectClient. It contains the nine canonical
// Safe Mapping Set source names plus former OMP-specific pass-through names:
// todo, hub, eval, the finite 5 vibe_* tools, and 4 autoresearch tools.
// Arbitrary operator tool_mappings must not extend this inventory.
var ompSourceIdentityInventory = map[string]bool{
	"read":            true,
	"write":           true,
	"edit":            true,
	"bash":            true,
	"grep":            true,
	"glob":            true,
	"task":            true,
	"ask":             true,
	"web_search":      true,
	"todo":            true,
	"hub":             true,
	"eval":            true,
	"vibe_spawn":      true,
	"vibe_send":       true,
	"vibe_wait":       true,
	"vibe_kill":       true,
	"vibe_list":       true,
	"init_experiment": true,
	"run_experiment":  true,
	"log_experiment":  true,
	"update_notes":    true,
}

// codexSourceIdentityInventory contains the static Codex source tool names used
// for request body detection in detectClient. It spans BOTH tool modes so that
// detection survives the mode switch described on defaultCloakTables["codex"]:
// shell mode contributes exec_command, write_stdin, apply_patch and view_image,
// code mode contributes the freeform exec, web_search and the collaboration
// children.
//
// The collaboration entries are listed BOTH bare and in opencodex's flattened
// "<namespace>__<child>" spelling, because that flattened form is what actually
// reaches this plugin over the openai-chat adapter; a bare-only inventory would
// score a real code-mode request one hit short of minToolNameHits whenever it
// declares exec plus namespace children and nothing else.
//
// Names generic enough to belong to any harness ("wait", "clock__sleep",
// "send_message") are deliberately absent from this inventory: client detection
// must stay independent of cloakability. Even though tools like "wait" and
// "clock__sleep" are cloaked via codexSharedAliases when a request is attributed
// to Codex, their presence in an unattributed request must never serve as
// evidence that the client is Codex.
var codexSourceIdentityInventory = map[string]bool{
	"exec":                           true,
	"exec_command":                   true,
	"write_stdin":                    true,
	"apply_patch":                    true,
	"view_image":                     true,
	"web_search":                     true,
	"request_user_input":             true,
	"request_user_input_async":       true,
	"spawn_agent":                    true,
	"followup_task":                  true,
	"list_agents":                    true,
	"wait_agent":                     true,
	"interrupt_agent":                true,
	"collaboration__spawn_agent":     true,
	"collaboration__followup_task":   true,
	"collaboration__list_agents":     true,
	"collaboration__wait_agent":      true,
	"collaboration__interrupt_agent": true,
	"collaboration__send_message":    true,
}

// claudeCodeSharedAliases is the preferred alias map for Claude Code tools that
// have no proven one-to-one AGY role (Tier-2 per parent #32). These wp_ aliases
// are used by admitRequestAliasPlan when CC is admitted through the alias-plan
// path, giving each CC-specific tool a stable upstream identity without claiming
// a real AGY tool name. Names are the exact parent #32 vocabulary.
var claudeCodeSharedAliases = map[string]string{
	// Discovery / workflow.
	"ToolSearch": "wp_find_tools",
	"Skill":      "wp_invoke_skill",
	"Workflow":   "wp_run_workflow",
	// Subagent control — unproven semantic match to AGY targets.
	"ListAgents":  "wp_list_workers",
	"SendMessage": "wp_send_message",
	"TaskStop":    "wp_cancel_task",
	// Scheduling / planning / worktree.
	"ScheduleWakeup": "wp_set_wakeup",
	"CronCreate":     "wp_create_schedule",
	"CronDelete":     "wp_delete_schedule",
	"CronList":       "wp_list_schedules",
	"EnterPlanMode":  "wp_begin_planning",
	"ExitPlanMode":   "wp_finish_planning",
	"EnterWorktree":  "wp_open_worktree",
	"ExitWorktree":   "wp_close_worktree",
	// Notebook / reporting.
	"NotebookEdit":   "wp_edit_notebook",
	"ReportFindings": "wp_submit_report",
	// Deferred / MCP resource tools — Tier-2 (no proven one-to-one AGY role).
	"DeferredToolPlaceholder": "wp_resolve_tool",
	"WaitForMcpServers":       "wp_wait_integrations",
	"ListMcpResourcesTool":    "wp_list_resources",
	"ReadMcpResourceTool":     "wp_read_resource",
	"ReadMcpResourceDirTool":  "wp_list_resource_dir",
}

// codexSharedAliases is the preferred alias map for Codex tools that have no
// proven one-to-one AGY role, or whose previous mappings were unproven semantic
// matches. Issue #38 moves collaboration__followup_task and
// collaboration__list_agents here (previously mapped to manage_task and
// manage_subagents respectively — those AGY tools have different semantics).
var codexSharedAliases = map[string]string{
	// Previously intentional pass-through, now cloaked to shared aliases.
	"wait":                           "wp_wait",
	"request_user_input_async":       "wp_request_user_input_async",
	"clock__sleep":                   "wp_clock_sleep",
	"collaboration__wait_agent":      "wp_collaboration_wait_agent",
	"collaboration__interrupt_agent": "wp_collaboration_interrupt_agent",
	"collaboration__send_message":    "wp_send_message",
	// Moved from unproven semantic mappings in defaultCloakTables.
	"collaboration__followup_task": "wp_collaboration_followup_task",
	"collaboration__list_agents":   "wp_list_workers",
	// Shell-mode tools that need cloaking. exec_command is the shell-mode
	// entry point (proven semantic match to run_command, same as exec in
	// code mode). Kept here rather than in defaultCloakTables to avoid a
	// non-injective inverse map at init(); each request must declare or use
	// only one entry point; distinct sources sharing run_command in a single
	// request reject admission.
	"exec_command": "run_command",
	"apply_patch":  "wp_apply_patch",
	"write_stdin":  "wp_write_stdin",
	"view_image":   "wp_view_image",
}

// ompSharedAliases is the preferred alias map for OMP tools beyond the
// canonical nine. Issue #37: the effective table can exceed nine; these tools
// were formerly intentional pass-through but now get cloaked to shared aliases
// on ProtectedAGY routes through the alias plan machinery. Unknown declarations
// (e.g. custom operator tools) fall through to fallbackAliasForSource.
var ompSharedAliases = map[string]string{
	// Standard pass-through tools.
	"todo": "wp_todo", "hub": "wp_hub", "eval": "wp_eval",
	// Vibe Mode.
	"vibe_spawn": "wp_vibe_spawn", "vibe_send": "wp_vibe_send",
	"vibe_wait": "wp_vibe_wait", "vibe_kill": "wp_vibe_kill", "vibe_list": "wp_vibe_list",
	// Autoresearch Mode.
	"init_experiment": "wp_init_experiment", "run_experiment": "wp_run_experiment",
	"log_experiment": "wp_log_experiment", "update_notes": "wp_update_notes",
	// Memory & skill (no AGY equivalent).
	"learn": "wp_learn", "manage_skill": "wp_manage_skill",
	// Semantic search (cannot map to grep_search: declaration collision).
	"find": "wp_find",
	// Goal and loop runtime. These are declared only while /goal, /guided-goal
	// or /loop is active, so they are invisible in a default session and were
	// silently taking the deterministic wp_ext_<hash> fallback. Every family
	// above is named rather than hashed, and codex has mapped its own "wait" to
	// wp_wait all along, so leaving the Oh My Pi equivalents out was an omission
	// rather than a decision. The fallback stays: any tool a future Oh My Pi
	// release declares still resolves through it.
	"goal": "wp_goal", "yield": "wp_yield", "wait": "wp_wait",
}

// ompCloakedTargetIdentityInventory contains the nine canonical AGY-facing
// target tool names from the Safe Mapping Set.
// These target identities are corroboration/static evidence only and
// MUST NEVER be used as standalone attribution without an independent OMP signal.
var ompCloakedTargetIdentityInventory = map[string]bool{
	"view_file":            true,
	"write_to_file":        true,
	"replace_file_content": true,
	"run_command":          true,
	"grep_search":          true,
	"find_by_name":         true,
	"invoke_subagent":      true,
	"ask_question":         true,
	"search_web":           true,
}

// clientDistinctiveTools lists harness-specific source tool names whose
// presence alone identifies a client. Clients whose source names are mostly
// common words ("read", "bash") collide with arbitrary user-defined tools,
// so those names require several simultaneous matches instead.
//
// For Oh My Pi, this matches the distinctive subset of ompSourceIdentityInventory;
// for other clients, it stays in sync with defaultCloakTables.
var clientDistinctiveTools = map[string]map[string]bool{
	"oh_my_pi": {
		"hub": true, "task": true, "todo": true, "eval": true, "web_search": true,
		"vibe_spawn": true, "vibe_send": true, "vibe_wait": true, "vibe_kill": true, "vibe_list": true,
		"init_experiment": true, "run_experiment": true, "log_experiment": true, "update_notes": true,
	},
}

const (
	// minToolNameHits is the minimum number of tool-name hits required before
	// any client is detected from original tool names at all.
	minToolNameHits = 2

	// minCollidingToolMatches is how many tool-name hits a client needs when
	// none of its distinctive harness tools are present.
	minCollidingToolMatches = 4

	// minCloakTargetHitNum / minCloakTargetHitDen define the target-coverage
	// threshold for cloaked-client detection: hits/total >= 4/5 (80%).
	minCloakTargetHitNum = 4
	minCloakTargetHitDen = 5
)

var defaultUncloakTables map[string]map[string]string

func init() {
	for client, table := range reverseBrandMappingsByClient {
		argumentReverseTablesByClient[client] = filterArgumentReverseTable(table)
	}
	argumentReverseTablesByClient["oh_my_pi"] = filterArgumentReverseTable(ompProtectedReverseTable)
	defaultUncloakTables = make(map[string]map[string]string)
	for client, cloaks := range defaultCloakTables {
		uncloaks := make(map[string]string)
		for orig, mapped := range cloaks {
			uncloaks[mapped] = orig
		}
		defaultUncloakTables[client] = uncloaks
	}
	cfg := defaultFilterConfig()
	globalFilterConfig.Store(&cfg)
}

func copyToolMappings(m map[string]map[string]string) map[string]map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]map[string]string, len(m))
	for k, v := range m {
		if v == nil {
			res[k] = nil
			continue
		}
		inner := make(map[string]string, len(v))
		for ik, iv := range v {
			inner[ik] = iv
		}
		res[k] = inner
	}
	return res
}

type filterConfig struct {
	UseDefaultKeywords bool
	CustomMappings     []rewriteMapping
	ToolMappings       map[string]map[string]string // client → {orig_tool: antigravity_tool}
	// ModelPrefixes gates cloaking by model name. When non-empty, cloaking runs
	// only if the request/response model starts with one of these prefixes.
	// Empty means cloak for every model (legacy behavior, all providers).
	ModelPrefixes []string

	// Pre-compiled regex patterns, rebuilt on config change.
	cloakRegexCache   map[string]*cachedCloakPatterns  // client → compiled cloak patterns
	uncloakRegexCache map[string]*cachedUncloakPattern // client → compiled uncloak pattern
}

// cachedCloakPatterns holds pre-compiled regexes for tool name replacement
// in descriptions and system messages (request cloaking path).
type cachedCloakPatterns struct {
	cloakTable map[string]string // orig → target (for identity replacement lookup)
	identRe    *regexp.Regexp    // single-pass identity replacement (quoted, namespaced, unambiguous)
	ambigRe    *regexp.Regexp    // single-pass contextual replacement for short/ambiguous words
}

// cachedUncloakPattern holds a pre-compiled regex for stream chunk uncloaking.
// Pattern matches: "name"\s*:\s*"(target1|target2|...)" in raw bytes.
type cachedUncloakPattern struct {
	re        *regexp.Regexp
	lookup    map[string]string // matched target → original name
	exactOnly bool              // when true, do not fall back to namespace base stripping
}

var (
	globalFilterConfig atomic.Pointer[filterConfig]
)

func defaultFilterConfig() filterConfig {
	cfg := filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	}
	rebuildCachedRegexes(&cfg)
	return cfg
}

func applyFilterConfig(cfg filterConfig) {
	newCfg := filterConfig{
		UseDefaultKeywords: cfg.UseDefaultKeywords,
		CustomMappings:     append([]rewriteMapping(nil), normalizeMappings(cfg.CustomMappings)...),
		ToolMappings:       copyToolMappings(cfg.ToolMappings),
		ModelPrefixes:      append([]string(nil), cfg.ModelPrefixes...),
	}
	rebuildCachedRegexes(&newCfg)
	globalFilterConfig.Store(&newCfg)
}

// rebuildCachedRegexes pre-compiles all regex patterns from the current
// ToolMappings. Called once on config change, not on every request.
func rebuildCachedRegexes(cfg *filterConfig) {
	cfg.cloakRegexCache = make(map[string]*cachedCloakPatterns, len(cfg.ToolMappings))
	cfg.uncloakRegexCache = make(map[string]*cachedUncloakPattern, len(cfg.ToolMappings))

	for client, cloakTable := range cfg.ToolMappings {
		// Build cloak patterns (for request path: tool name replacement in text)
		cp := &cachedCloakPatterns{
			cloakTable: cloakTable,
			identRe:    buildCloakIdentRe(cloakTable),
		}
		cp.ambigRe = buildCloakAmbiguousRe(cloakTable)
		cfg.cloakRegexCache[client] = cp

		// Build uncloak pattern (for stream path: regex-based tool name restore)
		targets := make([]string, 0, len(cloakTable))
		lookup := make(map[string]string, len(cloakTable))
		for orig, target := range cloakTable {
			targets = append(targets, regexp.QuoteMeta(target))
			lookup[target] = orig
		}
		if len(targets) > 0 {
			// Match "name" : "(?:[a-zA-Z0-9_-]+:)?<target>" with flexible whitespace
			pattern := `"name"\s*:\s*"((?:[a-zA-Z0-9_-]+:)?(?:` + strings.Join(targets, "|") + `))"`
			if re, err := regexp.Compile(pattern); err == nil {
				cfg.uncloakRegexCache[client] = &cachedUncloakPattern{
					re:     re,
					lookup: lookup,
				}
			}
		}
	}
}

func activeFilterConfig() *filterConfig {
	cfg := globalFilterConfig.Load()
	if cfg == nil {
		d := defaultFilterConfig()
		globalFilterConfig.CompareAndSwap(nil, &d)
		return globalFilterConfig.Load()
	}
	return cfg
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

func filterConfigFromLifecycleRequest(request []byte) (filterConfig, error) {
	var req lifecycleRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return filterConfig{}, fmt.Errorf("decode lifecycle request: %w", err)
	}
	return parseFilterConfigYAML(req.ConfigYAML)
}

func parseFilterConfigYAML(raw []byte) (filterConfig, error) {
	cfg := defaultFilterConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}

	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return filterConfig{}, fmt.Errorf("decode config yaml: %w", err)
	}
	if value, exists := values["use_default_keywords"]; exists {
		boolValue, ok := value.(bool)
		if !ok {
			return filterConfig{}, fmt.Errorf("use_default_keywords must be a boolean")
		}
		cfg.UseDefaultKeywords = boolValue
	}
	if value, exists := values["custom_mappings"]; exists {
		mappings, err := parseCustomMappings(value)
		if err != nil {
			return filterConfig{}, err
		}
		cfg.CustomMappings = mappings
	}
	if value, exists := values["tool_mappings"]; exists {
		parsedMappings, err := parseToolMappings(value)
		if err != nil {
			return filterConfig{}, err
		}
		if cfg.ToolMappings == nil {
			cfg.ToolMappings = make(map[string]map[string]string)
		}
		for client, mappings := range parsedMappings {
			if cfg.ToolMappings[client] == nil {
				cfg.ToolMappings[client] = make(map[string]string)
			}
			for orig, target := range mappings {
				cfg.ToolMappings[client][orig] = target
			}
		}
		for client, mappings := range parsedMappings {
			if client == "oh_my_pi" {
				if err := validateOMPConfigMappings(cfg.ToolMappings[client]); err != nil {
					return filterConfig{}, fmt.Errorf("tool_mappings oh_my_pi: %w", err)
				}
			} else if clientUsesAliasPlan(client) {
				if err := validateAliasPlanConfigMappings(client, mappings); err != nil {
					return filterConfig{}, fmt.Errorf("tool_mappings %s: %w", client, err)
				}
			}
		}
	}
	if value, exists := values["model_prefixes"]; exists {
		prefixes, err := parseModelPrefixes(value)
		if err != nil {
			return filterConfig{}, err
		}
		cfg.ModelPrefixes = prefixes
	}
	return cfg, nil
}

// parseModelPrefixes accepts an array of strings, a comma/newline-separated
// string, or a single string, and returns the trimmed, non-empty prefixes.
func parseModelPrefixes(value any) ([]string, error) {
	appendPrefix := func(out []string, raw string) []string {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		}) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	var prefixes []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		prefixes = appendPrefix(prefixes, typed)
	case []any:
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("model_prefixes entries must be strings")
			}
			prefixes = appendPrefix(prefixes, str)
		}
	default:
		return nil, fmt.Errorf("model_prefixes must be an array or string")
	}
	return prefixes, nil
}

// normalizeClientKey canonicalizes a configured client id to the key space
// used by defaultCloakTables and the detection logic: lowercase, with known
// aliases folded onto their canonical table key.
func normalizeClientKey(client string) string {
	key := strings.ToLower(strings.TrimSpace(client))
	switch key {
	case "omp", "oh-my-pi":
		return "oh_my_pi"
	default:
		return key
	}
}

// filteredHeaders returns a copy of headers with every key that matches any of
// remove case-insensitively dropped. It builds a fresh map rather than relying
// on http.Header.Del, which canonicalizes its argument and therefore cannot
// delete a stored key whose spelling is non-canonical. The result carries every
// non-owned inbound header verbatim, which is exactly the set a replacement
// host (the before-auth interceptor) keeps.
func filteredHeaders(headers http.Header, remove []string) http.Header {
	if headers == nil {
		return http.Header{}
	}
	out := make(http.Header, len(headers))
	for k, vs := range headers {
		drop := false
		for _, r := range remove {
			if strings.EqualFold(k, r) {
				drop = true
				break
			}
		}
		if !drop {
			out[k] = append([]string(nil), vs...)
		}
	}
	return out
}

type explicitMarkerResult struct {
	matchedKeys         []string
	present             bool
	uniqueClients       []string
	isOMP               bool
	isConflict          bool
	conflictContainsOMP bool
	client              string
	valid               bool
}

func parseExplicitClientMarker(headers http.Header) explicitMarkerResult {
	var res explicitMarkerResult
	if headers == nil {
		return res
	}
	var matchedKeys []string
	var rawValues []string
	for k, vs := range headers {
		if strings.EqualFold(k, explicitClientHeader) {
			matchedKeys = append(matchedKeys, k)
			rawValues = append(rawValues, vs...)
		}
	}
	if len(matchedKeys) == 0 {
		return res
	}
	sort.Strings(matchedKeys)
	res.matchedKeys = matchedKeys
	res.present = true

	clientSet := make(map[string]struct{})
	for _, raw := range rawValues {
		parts := strings.Split(raw, ",")
		for _, part := range parts {
			token := strings.TrimSpace(part)
			if token == "" {
				continue
			}
			norm := normalizeExplicitClientToken(token)
			if norm != "" {
				clientSet[norm] = struct{}{}
			}
		}
	}

	for c := range clientSet {
		res.uniqueClients = append(res.uniqueClients, c)
	}
	sort.Strings(res.uniqueClients)

	if len(res.uniqueClients) == 1 {
		res.client = res.uniqueClients[0]
		if res.client == "oh_my_pi" {
			res.isOMP = true
			res.valid = true
		} else {
			table := activeFilterConfig().ToolMappings[res.client]
			res.valid = len(table) > 0
		}
	} else if len(res.uniqueClients) > 1 {
		res.isConflict = true
		for _, c := range res.uniqueClients {
			if c == "oh_my_pi" {
				res.conflictContainsOMP = true
				break
			}
		}
	}
	return res
}

func normalizeExplicitClientToken(token string) string {
	lower := strings.ToLower(strings.TrimSpace(token))
	switch lower {
	case "oh_my_pi", "omp", "oh-my-pi":
		return "oh_my_pi"
	default:
		return normalizeClientKey(token)
	}
}

// resolveUserAgentClient returns a client derived from conservative
// User-Agent evidence. Only prefixes listed in userAgentEvidence may match,
// comparison is case-insensitive, and the match requires a usable active
// ToolMappings entry so entries like opencode/ stay inert until a table
// exists. The UA value is taken from the lexicographically first
// User-Agent key spelling for determinism, mirroring the explicit client
// marker resolution.
func resolveUserAgentClient(headers http.Header) (string, bool) {
	if headers == nil {
		return "", false
	}
	var matchedKeys []string
	for k := range headers {
		if strings.EqualFold(k, "User-Agent") {
			matchedKeys = append(matchedKeys, k)
		}
	}
	if len(matchedKeys) == 0 {
		return "", false
	}
	sort.Strings(matchedKeys)
	ua := ""
	if vs := headers[matchedKeys[0]]; len(vs) > 0 {
		ua = strings.TrimSpace(vs[0])
	}
	if ua == "" {
		return "", false
	}
	lowerUA := strings.ToLower(ua)
	for _, e := range userAgentEvidence {
		if strings.HasPrefix(lowerUA, strings.ToLower(e.prefix)) {
			if table := activeFilterConfig().ToolMappings[e.client]; len(table) > 0 {
				return e.client, true
			}
			return "", false
		}
	}
	return "", false
}

// validateOMPConfigMappings validates Oh My Pi tool mappings at configuration time.
// It enforces that canonical nine mappings are immutable, valid noncanonical custom
// mappings are honored, and invalid, non-injective, or forbidden target names fail
// visibly at configuration/reconfigure time.
func validateOMPConfigMappings(mappings map[string]string) error {
	ownerByTarget := make(map[string]string, len(mappings)+len(canonicalOMPSafeMappingSet)+len(ompSharedAliases))
	ownerByFinalBase := make(map[string]string, len(mappings)+len(canonicalOMPSafeMappingSet)+len(ompSharedAliases))

	// Pre-populate canonical nine targets
	for orig, target := range canonicalOMPSafeMappingSet {
		ownerByTarget[target] = orig
		_, base := splitToolNamespace(target)
		ownerByFinalBase[base] = orig
	}

	// Pre-populate shared alias targets (ompSharedAliases)
	for orig, target := range ompSharedAliases {
		ownerByTarget[target] = orig
		_, base := splitToolNamespace(target)
		ownerByFinalBase[base] = orig
	}

	for orig, target := range mappings {
		origTrimmed := strings.TrimSpace(orig)
		targetTrimmed := strings.TrimSpace(target)
		if origTrimmed == "" {
			return fmt.Errorf("empty mapping source")
		}
		if targetTrimmed == "" {
			return fmt.Errorf("empty mapping target for %q", orig)
		}
		if strings.ContainsAny(target, " \t\r\n") {
			return fmt.Errorf("forbidden target naming %q for %q: contains whitespace", target, orig)
		}
		if strings.HasPrefix(target, "_") || strings.HasPrefix(target, ":") {
			return fmt.Errorf("forbidden target naming %q for %q: leading underscore or colon is reserved", target, orig)
		}

		// Canonical nine immutability check
		if canonicalTarget, isCanonical := canonicalOMPSafeMappingSet[orig]; isCanonical {
			if target != canonicalTarget {
				return fmt.Errorf("canonical OMP mapping %q is immutable (cannot remap to %q)", orig, target)
			}
			continue
		}

		// Non-canonical mapping: cannot map to a canonical target or collide with another mapping
		if canonicalOwner, exists := ownerByTarget[target]; exists && canonicalOwner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to already-assigned target %q (owned by %q)", orig, target, canonicalOwner)
		}
		_, finalBase := splitToolNamespace(target)
		if canonicalOwner, exists := ownerByFinalBase[finalBase]; exists && canonicalOwner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to target %q with base %q (owned by %q)", orig, target, finalBase, canonicalOwner)
		}
		ownerByTarget[target] = orig
		ownerByFinalBase[finalBase] = orig
	}
	return nil
}

// validateAliasPlanConfigMappings validates tool mappings for alias-plan clients (claude_code, codex)
// at configuration time, enforcing identical target naming rules (whitespace, empty, leading underscore/colon)
// and injectivity against the client's reserved tables and within custom mappings.
func validateAliasPlanConfigMappings(client string, mappings map[string]string) error {
	client = normalizeClientKey(client)
	ownerByTarget := make(map[string]string)
	ownerByFinalBase := make(map[string]string)

	baseline := make(map[string]string)
	if staticTier1, ok := defaultCloakTables[client]; ok {
		for orig, target := range staticTier1 {
			baseline[orig] = target
		}
	}
	var shared map[string]string
	switch client {
	case "claude_code":
		shared = claudeCodeSharedAliases
	case "codex":
		shared = codexSharedAliases
	}
	if shared != nil {
		for orig, target := range shared {
			if client == "codex" && orig == "exec_command" && target == "run_command" {
				continue
			}
			baseline[orig] = target
		}
	}

	for bOrig, bTarget := range baseline {
		if _, remapped := mappings[bOrig]; remapped {
			continue
		}
		ownerByTarget[bTarget] = bOrig
		_, base := splitToolNamespace(bTarget)
		ownerByFinalBase[base] = bOrig
	}

	keys := make([]string, 0, len(mappings))
	for k := range mappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, orig := range keys {
		target := mappings[orig]
		origTrimmed := strings.TrimSpace(orig)
		targetTrimmed := strings.TrimSpace(target)
		if origTrimmed == "" {
			return fmt.Errorf("empty mapping source")
		}
		if targetTrimmed == "" {
			return fmt.Errorf("empty mapping target for %q", orig)
		}
		if strings.ContainsAny(target, " \t\r\n") {
			return fmt.Errorf("forbidden target naming %q for %q: contains whitespace", target, orig)
		}
		if strings.HasPrefix(target, "_") || strings.HasPrefix(target, ":") {
			return fmt.Errorf("forbidden target naming %q for %q: leading underscore or colon is reserved", target, orig)
		}

		if owner, exists := ownerByTarget[target]; exists && owner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to already-assigned target %q (owned by %q)", orig, target, owner)
		}
		_, finalBase := splitToolNamespace(target)
		if owner, exists := ownerByFinalBase[finalBase]; exists && owner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to target %q with base %q (owned by %q)", orig, target, finalBase, owner)
		}
		ownerByTarget[target] = orig
		ownerByFinalBase[finalBase] = orig
	}
	return nil
}

func parseToolMappings(value any) (map[string]map[string]string, error) {
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool_mappings must be an object")
	}
	result := make(map[string]map[string]string)
	for client, clientVal := range typed {
		clientMap, err := parseStringMap(clientVal)
		if err != nil {
			return nil, fmt.Errorf("client %q: %w", client, err)
		}
		// Merge rather than overwrite so multiple case variants of the same
		// client (e.g. "Claude_Code" and "claude_code") combine deterministically.
		normalized := normalizeClientKey(client)
		if result[normalized] == nil {
			result[normalized] = make(map[string]string, len(clientMap))
		}
		for orig, target := range clientMap {
			result[normalized][orig] = target
		}
		if normalized == "oh_my_pi" {
			if err := validateOMPConfigMappings(result[normalized]); err != nil {
				return nil, fmt.Errorf("client %q: %w", client, err)
			}
		} else if clientUsesAliasPlan(normalized) {
			if err := validateAliasPlanConfigMappings(normalized, result[normalized]); err != nil {
				return nil, fmt.Errorf("client %q: %w", client, err)
			}
		}
	}
	return result, nil
}

func parseStringMap(value any) (map[string]string, error) {
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	res := make(map[string]string)
	for k, v := range typed {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("value for key %q must be a string", k)
		}
		res[k] = s
	}
	return res, nil
}

func parseCustomMappings(value any) ([]rewriteMapping, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return parseMappingString(typed)
	case map[string]any:
		mappings := make([]rewriteMapping, 0, len(typed))
		for match, replacement := range typed {
			text, ok := replacement.(string)
			if !ok {
				return nil, fmt.Errorf("custom_mappings values must be strings")
			}
			mappings = append(mappings, rewriteMapping{Match: match, Replacement: text})
		}
		return mappings, nil
	case []any:
		mappings := make([]rewriteMapping, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("custom_mappings entries must be strings")
			}
			parsed, err := parseMappingString(text)
			if err != nil {
				return nil, err
			}
			mappings = append(mappings, parsed...)
		}
		return mappings, nil
	default:
		return nil, fmt.Errorf("custom_mappings must be an object, array, or string")
	}
}

func parseMappingString(value string) ([]rewriteMapping, error) {
	entries := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	mappings := make([]rewriteMapping, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		match, replacement, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("custom_mappings entries must use match: replacement")
		}
		mappings = append(mappings, rewriteMapping{Match: match, Replacement: replacement})
	}
	return mappings, nil
}

func effectiveMappings(cfg *filterConfig, client string) []rewriteMapping {
	var mappings []rewriteMapping
	if cfg == nil || cfg.UseDefaultKeywords {
		mappings = append(mappings, brandMappingsFor(client)...)
	}
	if cfg != nil && len(cfg.CustomMappings) > 0 {
		mappings = append(mappings, cfg.CustomMappings...)
	}
	// Client-scoped rules are dropped here so every downstream consumer
	// (system fields, tool descriptions, system messages) sees one flat list.
	scoped := make([]rewriteMapping, 0, len(mappings))
	for _, m := range mappings {
		if m.Client != "" && m.Client != client {
			continue
		}
		scoped = append(scoped, m)
	}
	return normalizeMappings(scoped)
}

func normalizeMappings(mappings []rewriteMapping) []rewriteMapping {
	seen := make(map[string]struct{}, len(mappings))
	reversed := make([]rewriteMapping, 0, len(mappings))
	for i := len(mappings) - 1; i >= 0; i-- {
		match := strings.ToLower(strings.TrimSpace(mappings[i].Match))
		replacement := strings.TrimSpace(mappings[i].Replacement)
		if match == "" || replacement == "" {
			continue
		}
		// Dedup key includes the client scope: the same match may legitimately
		// map differently depending on which client sent the request.
		key := match + "\x00" + mappings[i].Client
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		reversed = append(reversed, rewriteMapping{
			Match:         match,
			Replacement:   replacement,
			Client:        mappings[i].Client,
			WholeSegment:  mappings[i].WholeSegment,
			SegmentEnd:    mappings[i].SegmentEnd,
			NotFollowedBy: mappings[i].NotFollowedBy,
		})
	}
	out := make([]rewriteMapping, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		out = append(out, reversed[i])
	}
	return out
}

// rewriteRequestBody is a helper for callers and tests that rewrite a request body
// directly. Body mutation only occurs after a supported coding client is resolved
// from the request body. When no supported client is resolved, zero request-body
// mutation is performed.
func rewriteRequestBody(body []byte, sourceFormat string) ([]byte, bool) {
	raw, changed, _ := rewriteRequestBodyWithClient(body, sourceFormat, "")
	return raw, changed
}

// rewriteRequestBodyWithClient rewrites brand text and cloaks tool names. When
// forcedClient is non-empty (a validated explicit X-Cloak-Client override or
// verified User-Agent evidence), that client's mapping table is used
// deterministically instead of body-based detection. An empty forcedClient
// falls back to body-based client detection. Both brand rewriting and tool
// cloaking only run when a supported client has been resolved; if no supported
// client is resolved, the request body is returned unmutated.
func rewriteRequestBodyWithClient(body []byte, sourceFormat string, forcedClient string) ([]byte, bool, string) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false, ""
	}

	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false, ""
	}

	client := forcedClient
	if client != "" && len(effectiveCloakTable(client)) == 0 {
		client = ""
	}
	if client == "" {
		toolNames := extractToolNames(rootMap, sourceFormat)
		client = detectClient(toolNames)
	}
	if client == "" || len(effectiveCloakTable(client)) == 0 {
		return nil, false, ""
	}

	changed := false
	cfg := activeFilterConfig()
	cloakTable := effectiveCloakTable(client)
	var cachedCloak *cachedCloakPatterns
	if len(cloakTable) > 0 {
		toolCloaked := cloakToolNames(rootMap, cloakTable, sourceFormat)
		changed = changed || toolCloaked
		cachedCloak = cfg.cloakRegexCache[client]
	}

	mappings := effectiveMappings(cfg, client)
	rewritten, sysChanged := rewriteSystemFields(rootMap, mappings)
	rootMap = rewritten.(map[string]any)
	changed = changed || sysChanged

	descChanged := rewriteToolDescriptions(rootMap, mappings, cachedCloak, sourceFormat)
	changed = changed || descChanged

	sysMsgChanged := rewriteConversationContent(rootMap, mappings, cachedCloak)
	changed = changed || sysMsgChanged

	if cachedCloak != nil {
		if sysVal, ok := rootMap["system"]; ok {
			next, sysToolChanged := replaceToolNamesInValue(sysVal, cachedCloak)
			if sysToolChanged {
				rootMap["system"] = next
				changed = true
			}
		}
	}
	if !changed {
		return nil, false, client
	}
	raw, err := safeMarshal(rootMap)
	if err != nil {
		return nil, false, client
	}
	return raw, true, client
}

func effectiveCloakTable(client string) map[string]string {
	return activeFilterConfig().ToolMappings[client]
}

func cloakToolNames(body map[string]any, cloakTable map[string]string, sourceFormat string) bool {
	changed := false

	// Cloak tools[] array
	if toolsRaw, ok := body["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			tMap, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if sourceFormat == "openai" {
				fn, ok := tMap["function"].(map[string]any)
				if !ok {
					continue
				}
				if name, ok := fn["name"].(string); ok {
					if target, exists := lookupCloak(name, cloakTable); exists {
						fn["name"] = target
						changed = true
					}
				}
			} else if sourceFormat == "anthropic" {
				if name, ok := tMap["name"].(string); ok {
					if target, exists := lookupCloak(name, cloakTable); exists {
						tMap["name"] = target
						changed = true
					}
				}
			}
		}
	}

	// Cloak tool refs in messages[]
	if msgsRaw, ok := body["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			msg, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}

			if sourceFormat == "openai" {
				// tool_calls[].function.name
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, cRaw := range calls {
						call, ok := cRaw.(map[string]any)
						if !ok {
							continue
						}
						fn, ok := call["function"].(map[string]any)
						if !ok {
							continue
						}
						if name, ok := fn["name"].(string); ok {
							if target, exists := lookupCloak(name, cloakTable); exists {
								fn["name"] = target
								changed = true
							}
						}
					}
				}
				// tool result message: msg["name"]
				if msg["role"] == "tool" {
					if name, ok := msg["name"].(string); ok {
						if target, exists := lookupCloak(name, cloakTable); exists {
							msg["name"] = target
							changed = true
						}
					}
				}
			} else if sourceFormat == "anthropic" {
				// content blocks with type == "tool_use"
				if contents, ok := msg["content"].([]any); ok {
					for _, cntRaw := range contents {
						cnt, ok := cntRaw.(map[string]any)
						if !ok {
							continue
						}
						if cnt["type"] == "tool_use" {
							if name, ok := cnt["name"].(string); ok {
								if target, exists := lookupCloak(name, cloakTable); exists {
									cnt["name"] = target
									changed = true
								}
							}
						}
					}
				}
			}
		}
	}

	// Cloak tool_choice (handle both string and object shapes)
	if tc, ok := body["tool_choice"].(map[string]any); ok {
		if sourceFormat == "openai" {
			// {type: "function", function: {name: "..."}}
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					if target, exists := lookupCloak(name, cloakTable); exists {
						fn["name"] = target
						changed = true
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			// {type: "tool", name: "..."}
			if name, ok := tc["name"].(string); ok {
				if target, exists := lookupCloak(name, cloakTable); exists {
					tc["name"] = target
					changed = true
				}
			}
		}
	}

	return changed
}

func rewriteToolDescriptions(root map[string]any, mappings []rewriteMapping, cached *cachedCloakPatterns, sourceFormat string) bool {
	changed := false
	toolsRaw, ok := root["tools"].([]any)
	if !ok {
		return false
	}
	for _, tRaw := range toolsRaw {
		tMap, ok := tRaw.(map[string]any)
		if !ok {
			continue
		}
		schemaOwner := tMap
		if sourceFormat == "openai" {
			fn, ok := tMap["function"].(map[string]any)
			if !ok {
				continue
			}
			schemaOwner = fn
		}
		if rewriteDescriptionField(schemaOwner, "description", mappings, cached) {
			changed = true
		}
		// Parameter help lives inside the JSON Schema, one or two levels below
		// the tool. Claude Code ships tools whose parameter description names
		// the client home directory (~/.claude/scheduled_tasks.json), so a
		// top-level-only walk left that sitting in the payload.
		if rewriteSchemaDescriptions(schemaOwner, mappings) {
			changed = true
		}
	}
	return changed
}

// isSchemaTextField reports whether a JSON Schema keyword holds human-readable
// help rather than structure. Every other string in a schema is structure: a
// property name, a `required` member, an `enum`/`const` value, a `pattern`.
func isSchemaTextField(key string) bool {
	return key == "description" || key == "title"
}

// rewriteSchemaDescriptions applies the brand mapping to the help text inside a
// tool's JSON Schema. The schema is walked rather than re-serialized, so key
// order and nesting survive untouched, and only descriptive fields are
// rewritten: a schema is the contract the model's arguments have to satisfy, so
// rewriting a "required" entry, an "enum"/"const" member or a property name —
// while the matching property name or validated value keeps the client's
// spelling — leaves a schema no arguments can satisfy
// (required:["Antigravity"] over a property still called "Claude").
func rewriteSchemaDescriptions(tool map[string]any, mappings []rewriteMapping) bool {
	changed := false
	for _, key := range []string{"input_schema", "parameters"} {
		schema, ok := tool[key]
		if !ok {
			continue
		}
		// Maps and slices are mutated in place, so the schema is already
		// rewritten at tool[key] whenever this reports a change.
		if _, c := rewriteSchemaText(schema, mappings); c {
			changed = true
		}
	}
	return changed
}

// schemaLiteralKeys are the keywords whose value is DATA the caller validates
// against, not a schema. Rewriting anything below one of them changes a
// constant while the property carrying it keeps the client's spelling, so the
// schema can no longer be satisfied: enum:["Antigravity"] over a property
// still called "Claude" rejects every argument the client can send. A nested
// schema is only ever found under a schema keyword, so skipping these subtrees
// costs no real traversal.
var schemaLiteralKeys = map[string]bool{
	"const":    true,
	"enum":     true,
	"default":  true,
	"example":  true,
	"examples": true,
}

// rewriteSchemaText rewrites the descriptive strings of a JSON Schema value,
// recursing through properties, items, $defs and the composition keywords to
// reach nested schemas, and leaving every structural string exactly as it was.
// The subtree under a literal-bearing keyword is never descended into.
func rewriteSchemaText(value any, mappings []rewriteMapping) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			if s, isString := child.(string); isString {
				if !isSchemaTextField(key) {
					continue
				}
				next := s
				for _, mapping := range mappings {
					next, _ = replaceInsensitiveRule(next, mapping, true)
				}
				if next != s {
					typed[key] = next
					changed = true
				}
				continue
			}
			// Only containers are walked, and never under a literal-bearing
			// keyword: a nested string there is a validated constant, not help
			// text, and rewriting it would break the contract the model's
			// arguments have to satisfy.
			if schemaLiteralKeys[key] {
				continue
			}
			switch child.(type) {
			case map[string]any, []any:
				if next, c := rewriteSchemaText(child, mappings); c {
					typed[key] = next
					changed = true
				}
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			switch child.(type) {
			case map[string]any, []any:
				if next, c := rewriteSchemaText(child, mappings); c {
					typed[i] = next
					changed = true
				}
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

// rewriteDescriptionField applies brand replacements and tool-name cloaking to
// the string description stored at obj[key], writing back only when something
// changed. It reports whether the field was modified.
func rewriteDescriptionField(obj map[string]any, key string, mappings []rewriteMapping, cached *cachedCloakPatterns) bool {
	descVal, ok := obj[key].(string)
	if !ok {
		return false
	}
	next := descVal
	descChanged := false
	for _, mapping := range mappings {
		var replaced bool
		next, replaced = replaceInsensitiveRule(next, mapping, true)
		descChanged = descChanged || replaced
	}
	if cached != nil {
		var toolReplaced bool
		next, toolReplaced = replaceToolNamesInText(next, cached)
		descChanged = descChanged || toolReplaced
	}
	if descChanged {
		obj[key] = next
	}
	return descChanged
}

// rewriteConversationContent applies the brand mapping and the tool-name cloak
// to every turn of the conversation, not just the system ones. Claude Code
// inlines its own private global instructions and its whole skill catalogue as
// a <system-reminder> block inside a user message, so a system-only walk left
// the client fingerprint sitting in the payload. tool_result payloads go
// through the same pass: the model is shown the cloaked text and the response
// path maps it back, so the client still reads exactly what the tool returned.
func rewriteConversationContent(root map[string]any, mappings []rewriteMapping, cached *cachedCloakPatterns) bool {
	changed := false
	msgsRaw, ok := root["messages"].([]any)
	if !ok {
		return changed
	}
	for _, mRaw := range msgsRaw {
		msg, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		content, exists := msg["content"]
		if !exists {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "user" {
			// The words the user typed are theirs, byte for byte. Only the
			// client's own machine-generated blocks inside the turn (its
			// <system-reminder> spans and its tool_result payloads) are
			// rewritten, for brands AND for tool names. A blanket
			// replaceToolNamesInValue over the whole turn used to corrupt an
			// ordered typed user string - a file the user really wrote in
			// out.txt was silently persisted as the declaration's wp_find_tools
			// literal instead.
			if next, c := rewriteUserTurnContent(content, mappings, cached); c {
				msg["content"] = next
				changed = true
			}
			continue
		}
		next, contentChanged := rewriteSystemValue(content, mappings)
		if contentChanged {
			msg["content"] = next
			changed = true
		}
		if cached != nil {
			toolNext, toolChanged := replaceToolNamesInValue(msg["content"], cached)
			if toolChanged {
				msg["content"] = toolNext
				changed = true
			}
		}
	}
	return changed
}

// rewriteMachineSpan runs both cloaks over one client-generated span: the brand
// mappings, then the request-scoped tool aliases.
func rewriteMachineSpan(block string, mappings []rewriteMapping, cached *cachedCloakPatterns) (string, bool) {
	next, c := rewriteSystemValue(block, mappings)
	out, _ := next.(string)
	if cached != nil {
		withTools, tc := replaceToolNamesInText(out, cached)
		out = withTools
		c = c || tc
	}
	return out, c
}

// rewriteUserTurnContent applies the brand mapping to a user turn, but only to
// the parts the client generated. A user turn is not one blob: the typed
// message, the <system-reminder> block carrying the client's private
// instructions and skill catalogue, and tool_result payloads all arrive
// interleaved under the same role, and they must be treated differently.
//
// The typed text is left alone on purpose. It is the user's own words, and
// anything the model then writes to disk is persisted in whatever spelling it
// saw, because a file leaves the process and never flows back through the
// response path that reverses the other direction. Cloaking the
// <system-reminder> and tool_result blocks costs nothing in correctness: that
// text is machine-produced context, not the user's.
func rewriteUserTurnContent(content any, mappings []rewriteMapping, cached *cachedCloakPatterns) (any, bool) {
	changed := false
	switch typed := content.(type) {
	case string:
		// A bare string turn: the typed message, possibly with the client's
		// own <system-reminder> block spliced into it. Only the span is
		// cloaked; the words the user typed around it are theirs.
		next, c := rewriteSystemReminderSpans(typed, mappings, cached)
		return next, c
	case []any:
		for _, blockRaw := range typed {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "tool_result":
				// A tool result is machine-generated output echoing the alias
				// names the model was given, so it is cloaked on both surfaces -
				// but only on its content, never on the block's identity fields.
				if next, c := rewriteSystemValue(block["content"], mappings); c {
					block["content"] = next
					changed = true
				}
				if cached != nil {
					if next, c := replaceToolNamesInValue(block["content"], cached); c {
						block["content"] = next
						changed = true
					}
				}
			case "text":
				txt, ok := block["text"].(string)
				if !ok {
					continue
				}
				// Same span-aware rule as the bare-string turn: ordinary text is
				// untouched, <system-reminder> spans get brands + tool aliases.
				if next, c := rewriteSystemReminderSpans(txt, mappings, cached); c {
					block["text"] = next
					changed = true
				}
			}
		}
	}
	return content, changed
}

// rewriteSystemReminderSpans rewrites only the text between <system-reminder>
// markers, leaving the surrounding typed message byte-identical.
// rewriteSystemReminderSpans applies the client's own cloaks to the bytes
// INSIDE its <system-reminder> spans and to nothing else.
//
// The span body gets both surfaces: the brand mappings and, when cached is
// non-nil, the request-scoped tool aliases. A tool named in a reminder is text
// the client generated from the same conversation state the model saw, so it
// carries aliases upstream just like the prose does.
//
// Everything outside a span is written through verbatim. The user typed those
// bytes. Running either surface across the whole turn rewrote real content -
// "Write the literal ToolSearch into out.txt" was silently persisted as
// "Write the literal wp_find_tools into out.txt", and the model was told to
// create a file whose name the user never asked for.
func rewriteSystemReminderSpans(text string, mappings []rewriteMapping, cached *cachedCloakPatterns) (string, bool) {
	const openTag, closeTag = "<system-reminder>", "</system-reminder>"
	var b strings.Builder
	rest := text
	changed := false
	for {
		start := strings.Index(rest, openTag)
		if start < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		rest = rest[start:]
		end := strings.Index(rest, closeTag)
		if end < 0 {
			// Unterminated marker: the client truncated the turn. Treat the
			// remainder as context rather than leaving half a block unclaked.
			next, c := rewriteMachineSpan(rest, mappings, cached)
			b.WriteString(next)
			changed = changed || c
			break
		}
		block := rest[:end+len(closeTag)]
		next, c := rewriteMachineSpan(block, mappings, cached)
		b.WriteString(next)
		changed = changed || c
		rest = rest[end+len(closeTag):]
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

func rewriteSystemFields(value any, mappings []rewriteMapping) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			if key == "system" {
				next, childChanged := rewriteSystemValue(child, mappings)
				if childChanged {
					typed[key] = next
					changed = true
				}
				continue
			}
			next, childChanged := rewriteSystemFields(child, mappings)
			if childChanged {
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			next, childChanged := rewriteSystemFields(child, mappings)
			if childChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func rewriteSystemValue(value any, mappings []rewriteMapping) (any, bool) {
	switch typed := value.(type) {
	case string:
		next := typed
		changed := false
		for _, mapping := range mappings {
			var replaced bool
			next, replaced = replaceInsensitiveRule(next, mapping, true)
			changed = changed || replaced
		}
		return next, changed
	case map[string]any:
		changed := false
		for key, child := range typed {
			next, childChanged := rewriteSystemValue(child, mappings)
			if childChanged {
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			next, childChanged := rewriteSystemValue(child, mappings)
			if childChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// isBareVendorToken reports whether a mapping is a single bare vendor word such
// as "omp" or "claude", as opposed to a composed entry like "claude.ai",
// "Antigravity SDK" or ".gemini/CLAUDE.md". Only bare words are subject to the
// URL/path rule below; a composed entry carries its own deliberate meaning and
// is rewritten wherever it appears.
func isBareVendorToken(match string) bool {
	for i := range len(match) {
		switch match[i] {
		case '.', '-', '_', ' ', '/', '\\':
			return false
		}
	}
	return true
}

// inURLPathContext reports whether a match at [index, matchEnd) sits inside a
// URL or filesystem path rather than in prose. A path delimiter immediately
// before the match is the strong signal - "/omp/", "C:\omp\" - and a URL
// scheme covers the rest. A bare word in ordinary prose is left alone even
// when a sentence elsewhere contains a slash, so "Anthropic/Google" is still
// masked as before.
func inURLPathContext(value string, index, matchEnd int) bool {
	if index == 0 {
		return false
	}
	prev := value[index-1]
	if prev == '/' || prev == '\\' {
		return true
	}
	// Inside a real path element that starts with a dot: ".omp-backup/agent".
	if prev == '.' && (index == 1 || !isWordByte(value[index-2])) {
		return true
	}
	// A URL scheme counts only when it sits in the SAME whitespace-free token as
	// the match. Testing the whole value for "://" suppressed every brand token
	// in a 200KB system prompt that mentions a URL anywhere - which is every
	// Claude Code system prompt, so the forward rewrite silently became a no-op
	// on the largest surface the plugin has. Live acceptance caught it: Claude
	// Code's own system prompt reached upstream with 63 "Claude" and 8
	// "Anthropic" still in it.
	for i := index - 1; i >= 0; i-- {
		switch c := value[i]; c {
		case ' ', '\t', '\n', '\r', '"', '\'', '<', '>', '(', ')', ',', ';', '|', '{', '}', '[':
			return false
		case ':':
			// A scheme's slashes come AFTER the colon ("https://"), so the
			// scheme is recognised by looking forward. Testing value[i-1] and
			// value[i-2] for slashes could only ever fire on a path segment
			// that merely ended in a colon, and left every host-like token
			// whose match is not glued to a slash ("https://www.omp.ai"),
			// classified as prose and rewritten.
			return i+2 < len(value) && value[i+1] == '/' && value[i+2] == '/'
		}
	}
	return false
}

// isDotPrefixedPathSegment reports whether a bare vendor word begins a literal
// dot-prefixed path segment, the only form whose rewrite is a real directory
// remap: ".omp/agent", "C:\Users\u\.claude\settings.json". A dot-directory can
// start right after prose punctuation, because tool-parameter help reads
// "persist to .claude/scheduled_tasks.json on disk", so any non-word byte
// before the dot begins the segment. "foo.omp" is excluded because the byte
// before its dot is a word byte.
func isDotPrefixedPathSegment(value string, index, matchEnd int) bool {
	if index == 0 || value[index-1] != '.' {
		return false
	}
	dot := index - 1
	if dot != 0 && isWordByte(value[dot-1]) {
		return false
	}
	return matchEnd == len(value) || value[matchEnd] == '/' || value[matchEnd] == '\\'
}

// segmentEndsAt reports whether a match ending at matchEnd also ENDS a whole
// path element: end of string, or a separator. It is the right-hand half of
// isDotPrefixedPathSegment, and it is deliberately the same predicate: the
// reverse pass may only rewrite a spelling the forward pass could have
// produced, and the forward dot-segment remap accepts exactly these endings.
func segmentEndsAt(value string, matchEnd int) bool {
	return matchEnd == len(value) || value[matchEnd] == '/' || value[matchEnd] == '\\'
}

func replaceInsensitive(value, match, replacement string) (string, bool) {
	return replaceInsensitiveRule(value, rewriteMapping{Match: match, Replacement: replacement}, false)
}

// Dot-prefixed directory names get their own treatment in the forward brand
// rewrite, because rewriting them as prose would hand the model a path that
// does not exist on disk.

// pathSegmentReplacements map a client home directory onto the Antigravity
// equivalent, so ~/.omp/agent becomes ~/.gemini/agent rather than a dead
// .Antigravity path, inverted by ompProtectedReverseTable.
//
// Only Oh My Pi is listed. Claude Code and Codex go through pathRules instead,
// which can also rewrite the file name (CLAUDE.md -> GEMINI.md) that this map
// has no way to express. Rewriting a bare vendor token in a path also rewrote a
// user's own text during live acceptance: Claude Code wrote ".gemini" over
// ".claude" in a README edit where the remap had collapsed the two sides into
// identical bytes, so the edit silently became a no-op. The path tables avoid
// that because the reverse half is their exact mirror.
var pathSegmentReplacements = map[string]string{
	"omp": "gemini",
}

// replaceInsensitiveRule is the shared case-insensitive word-boundary matcher.
// When skipPath is true, a match that names a literal dot-prefixed path segment
// is remapped to the Antigravity home directory instead of the brand word, so
// a client's own config path becomes one that exists upstream.
func replaceInsensitiveRule(value string, m rewriteMapping, skipPath bool) (string, bool) {
	match, replacement := m.Match, m.Replacement
	if match == "" {
		return value, false
	}
	lowerValue := strings.ToLower(value)
	lowerMatch := strings.ToLower(match)

	firstIsWord := isWordByte(match[0])
	lastIsWord := isWordByte(match[len(match)-1])

	var builder strings.Builder
	start := 0
	changed := false
	for {
		index := strings.Index(lowerValue[start:], lowerMatch)
		if index < 0 {
			break
		}
		index += start
		matchEnd := index + len(match)

		hasLeftBoundary := !firstIsWord || index == 0 || !isWordByte(value[index-1])
		if m.WholeSegment && index > 0 && isWordByte(value[index-1]) {
			hasLeftBoundary = false
		}
		hasRightBoundary := !lastIsWord || matchEnd == len(value) || !isWordByte(value[matchEnd])
		if m.SegmentEnd && !segmentEndsAt(value, matchEnd) {
			hasRightBoundary = false
		}
		effectiveReplacement := replacement
		// Exclusion: a spelling this rule must let through, e.g. the OMP
		// scheme "omp://" or the native host "antigravity.google".
		if m.NotFollowedBy != "" && hasPrefixFold(value[matchEnd:], m.NotFollowedBy) {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		// URL/path rule: inside a URL or filesystem path, a bare vendor word is
		// rewritten only as a literal dot-prefixed directory segment, remapped
		// onto the client's neutral equivalent. Every other position in a path
		// is left byte-for-byte alone - "/omp/", "\omp\", "antigravity-cloak" and
		// "omp.ai" are ordinary names there, and rewriting them yields a path
		// that exists neither upstream nor on the way back. In prose nothing
		// changes and the brand is still masked.
		skip := false
		pathRemapped := false
		if skipPath && isBareVendorToken(match) && inURLPathContext(value, index, matchEnd) {
			if isDotPrefixedPathSegment(value, index, matchEnd) {
				if remapped, ok := pathSegmentReplacements[lowerMatch]; ok {
					effectiveReplacement = remapped
					pathRemapped = true
				} else {
					skip = true
				}
			} else {
				skip = true
			}
		}
		if skip {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		// A prose brand word follows the casing it was written in, so the
		// client's own sentence stays coherent: Codex -> Antigravity,
		// codex -> antigravity, CODEX -> ANTIGRAVITY. Doing it here is what
		// keeps that a property of the mechanism instead of three
		// case-insensitive rules that shadow each other.
		if !pathRemapped && isProseBrandRule(m) {
			effectiveReplacement = mirrorBrandCase(value[index:matchEnd], replacement)
		}

		if hasLeftBoundary && hasRightBoundary {
			builder.WriteString(value[start:index])
			builder.WriteString(effectiveReplacement)
			start = matchEnd
			changed = true
		} else {
			builder.WriteString(value[start : index+1])
			start = index + 1
		}
	}
	if !changed {
		return value, false
	}
	builder.WriteString(value[start:])
	return builder.String(), true
}

// isProseBrandRule reports whether a rule rewrites a spoken brand word rather
// than an operational identifier, which is the only case where the matched
// casing is allowed to travel into the replacement. A brand word is a single
// word ("Codex", "Antigravity", "omp"); a replacement may be one or two words
// ("Google Deepmind"). Tool names, SDK names, paths, model IDs and the OMP
// sentinel all contain a separator or an underscore, so they stay exactly as
// declared and are never re-cased by the text they matched.
func isProseBrandRule(m rewriteMapping) bool {
	if !isBareVendorToken(m.Match) || m.Replacement == "" {
		return false
	}
	for i := range len(m.Replacement) {
		switch c := m.Replacement[i]; {
		case c == ' ':
			continue
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			continue
		default:
			return false
		}
	}
	return true
}

// mirrorBrandCase copies the casing of the text a rule matched onto its
// replacement, so a lowercase spelling stays lowercase and an uppercase one
// stays uppercase. A mixed spelling keeps the replacement as declared.
func mirrorBrandCase(matched, replacement string) string {
	var hasLower, hasUpper bool
	for i := range len(matched) {
		switch c := matched[i]; {
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		}
	}
	switch {
	case hasLower && !hasUpper:
		return strings.ToLower(replacement)
	case hasUpper && !hasLower:
		return strings.ToUpper(replacement)
	default:
		return replacement
	}
}

// hasPrefixFold reports whether s starts with prefix, case-insensitively.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func replaceInsensitiveWithPrev(value string, prevIsWord bool, match, replacement string) (string, bool) {
	return replaceMappingWithPrev(value, prevIsWord, rewriteMapping{Match: match, Replacement: replacement})
}

func replaceMappingWithPrev(value string, prevIsWord bool, m rewriteMapping) (string, bool) {
	match, replacement := m.Match, m.Replacement
	if match == "" {
		return value, false
	}
	lowerValue := strings.ToLower(value)
	lowerMatch := strings.ToLower(match)
	firstIsWord := isWordByte(match[0])
	lastIsWord := isWordByte(match[len(match)-1])
	proseBrand := isProseBrandRule(m)
	var builder strings.Builder
	start := 0
	changed := false
	for {
		index := strings.Index(lowerValue[start:], lowerMatch)
		if index < 0 {
			break
		}
		index += start
		matchEnd := index + len(match)
		hasLeftBoundary := !firstIsWord
		if firstIsWord {
			if index == 0 {
				hasLeftBoundary = !prevIsWord
			} else {
				hasLeftBoundary = !isWordByte(value[index-1])
			}
		}
		if m.WholeSegment && index > 0 && isWordByte(value[index-1]) {
			hasLeftBoundary = false
		}
		hasRightBoundary := !lastIsWord || matchEnd == len(value) || !isWordByte(value[matchEnd])
		if m.SegmentEnd && !segmentEndsAt(value, matchEnd) {
			hasRightBoundary = false
		}
		// Exclusion: a spelling the forward pass never introduced and that this
		// rule must hand back untouched, such as the native host
		// "antigravity.google" inside the bare-brand lane.
		if m.NotFollowedBy != "" && hasPrefixFold(value[matchEnd:], m.NotFollowedBy) {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		// Mirror of the forward URL/path rule, and the reason the round trip has
		// to be symmetric: the model echoes back whatever it was shown, including
		// a directory name the forward pass deliberately left alone because it
		// sat in the user's own text. A blind case-insensitive reverse walk turns
		// "F:/CodeBase/antigravity-cloak/" into "F:/CodeBase/omp-cloak/", and the
		// client then reads a path that does not exist. Composed entries such as
		// ".gemini/GEMINI.md" are exempt; only bare vendor words in a URL or path
		// are held back. In prose the reverse runs as before.
		if isBareVendorToken(match) && inURLPathContext(value, index, matchEnd) && !isDotPrefixedPathSegment(value, index, matchEnd) {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		emitted := replacement
		if proseBrand {
			emitted = mirrorBrandCase(value[index:matchEnd], m.Replacement)
		}
		if hasLeftBoundary && hasRightBoundary {
			builder.WriteString(value[start:index])
			builder.WriteString(emitted)
			start = matchEnd
			changed = true
		} else {
			builder.WriteString(value[start : index+1])
			start = index + 1
		}
	}
	if !changed {
		return value, false
	}
	builder.WriteString(value[start:])
	return builder.String(), true
}

// holdIsLive reports whether a held candidate of length k can still become a
// boundary-valid match of brand, and so is worth delaying the client's text
// for. A candidate shorter than the match can always still be completed, so it
// is live. A candidate equal to the whole match is live only while its RIGHT
// boundary is still unproven: the matcher has yet to see whether a word byte
// follows, which would rule the match out. A match whose last byte is not a
// word byte is already boundary-valid at end of text, so holding it defers
// output the client could have had immediately and proves nothing.
func holdIsLive(brand string, k int) bool {
	if k <= 0 || k > len(brand) {
		return false
	}
	return k < len(brand) || isWordByte(brand[len(brand)-1])
}

// findHoldLenWithBoundary returns the longest suffix of combined that is still
// a live, possibly-incomplete match of m.Match: the bytes already prove its left
// boundary, and holdIsLive proves the match can still grow into a
// boundary-valid one. A suffix that can no longer become one is never retained
// (Issue #48).
//
// A rule with an exclusion also holds a COMPLETE match while the exclusion
// suffix is still arriving. Resolving "antigravity" as soon as its boundary is
// provable would emit "omp" and then ".google" behind it, and the client would
// read "omp.google" for bytes the forward pass never produced. Holding until
// the exclusion is decided (complete, or ruled out by the next byte) is what
// makes the exclusion chunk-safe.
func findHoldLenWithBoundary(combined string, m rewriteMapping, prevIsWord bool) int {
	brand := m.Match
	max := len(brand)
	if len(combined) < max {
		max = len(combined)
	}
	for k := max; k >= 1; k-- {
		suffix := combined[len(combined)-k:]
		if !strings.EqualFold(suffix, brand[:k]) {
			continue
		}
		pos := len(combined) - k
		var leftOK bool
		if pos == 0 {
			leftOK = !prevIsWord
		} else {
			leftOK = !isWordByte(combined[pos-1])
		}
		if leftOK && holdIsLive(brand, k) {
			return k
		}
	}
	// Exclusion hold: the match itself is complete and boundary-valid, and the
	// bytes after it are a non-empty proper prefix of the exclusion, so whether
	// this match may be rewritten is not yet known.
	if excl := m.NotFollowedBy; excl != "" {
		limit := len(excl) - 1
		if tail := len(combined) - len(brand); tail < limit {
			limit = tail
		}
		for rest := limit; rest >= 1; rest-- {
			pos := len(combined) - len(brand) - rest
			if pos < 0 || !strings.EqualFold(combined[pos:pos+len(brand)], brand) {
				continue
			}
			var leftOK bool
			if pos == 0 {
				leftOK = !prevIsWord
			} else {
				leftOK = !isWordByte(combined[pos-1])
			}
			if leftOK && strings.EqualFold(combined[pos+len(brand):], excl[:rest]) {
				return len(brand) + rest
			}
		}
	}
	return 0
}

func getBrandLane(sess *streamSession, key string) *brandLane {
	if sess.brandCarries == nil {
		sess.brandCarries = make(map[string]*brandLane)
	}
	if lane, ok := sess.brandCarries[key]; ok {
		return lane
	}
	lane := &brandLane{}
	sess.brandCarries[key] = lane
	return lane
}

// findHoldLenSet is findHoldLenWithBoundary over a whole table: the longest
// suffix of combined that is still a live prefix of ANY of the table's matches.
// One shared search keeps several rules in a single lane from each holding a
// different partial token of the same text, which is what deadlocks them.
func findHoldLenSet(combined string, table []rewriteMapping, prevIsWord bool) int {
	best := 0
	for _, m := range table {
		if k := findHoldLenWithBoundary(combined, m, prevIsWord); k > best {
			best = k
		}
	}
	return best
}

// replaceInsensitiveSetWithPrev runs every rule of a reverse table over one
// emitted fragment, in declaration order, so the longer tokens win the prefix.
func replaceInsensitiveSetWithPrev(value string, prevIsWord bool, table []rewriteMapping) (string, bool) {
	out, changed := value, false
	for _, m := range table {
		next, c := replaceMappingWithPrev(out, prevIsWord, m)
		out = next
		changed = changed || c
	}
	return out, changed
}

// applyBrandLaneSet is applyBrandLane over a whole table in ONE lane: the
// longest live partial token across all rules is held, and the fragment that
// remains is resolved against the same table. Used by the Oh My Pi protected
// lane, which owns several rules in one carry.
//
// noHold skips the hold for a lane whose choice is finalized by the event being
// applied (see streamSession.terminalChoices). The hold exists to wait for the
// byte that proves a token's right boundary; at a terminal event that byte never
// arrives and end-of-text is itself a valid boundary, so waiting would carry the
// token past the finish_reason that closed the choice. Every other call site
// passes false, so the chunk/boundary semantics of Issue #48 are unchanged.
func applyBrandLaneSet(text string, lane *brandLane, table []rewriteMapping, noHold bool) (string, bool) {
	combined := lane.carry + text
	if combined == "" {
		return "", false
	}
	holdLen := findHoldLenSet(combined, table, lane.lastIsWord)
	if noHold {
		holdLen = 0
	}
	emitPart, newCarry := combined, ""
	if holdLen > 0 {
		emitPart, newCarry = combined[:len(combined)-holdLen], combined[len(combined)-holdLen:]
	}
	out, changed := replaceInsensitiveSetWithPrev(emitPart, lane.lastIsWord, table)
	lane.setCarry(newCarry)
	if out != "" {
		lane.lastIsWord = isWordByte(out[len(out)-1])
	}
	return out, changed || out != emitPart
}

// reverseBrandLaneSuffix separates a content block's lane key from the brand
// token being matched inside it, so one block can carry one lane per mapping.
const reverseBrandLaneSuffix = "\x00"

// setCarry installs a lane's carry and keeps the arrival stamp consistent with
// it: a lane that is not holding has no arrival order, so its stamp is cleared
// and the next hold is stamped afresh. Without this a lane that held, drained
// and held again kept its FIRST stamp, and sortedCarryKeys put its new text
// ahead of a lane that genuinely began holding later.
func (l *brandLane) setCarry(carry string) {
	l.carry = carry
	if carry == "" {
		l.seq = 0
	}
}

// stampLaneArrival records the order in which a lane took its CURRENT carry,
// which is the order those bytes appeared in the stream. It has to happen at the
// first HOLD, not at lane creation: every lane of a content block is created on
// the block's first chunk, in reverse-table order, so a creation-order stamp
// would just re-encode the table order sortedCarryKeys is meant to stop
// depending on.
func stampLaneArrival(sess *streamSession, lane *brandLane) {
	if lane == nil || lane.seq != 0 || lane.carry == "" {
		return
	}
	sess.laneSeq++
	lane.seq = sess.laneSeq
}

// applySemanticBrandLane runs the resolved client's whole reverse table over
// ONE semantic carrier - a content block's prose, that block's tool arguments,
// one OpenAI choice's text - in a SINGLE lane.
//
// One lane per reverse rule gave every rule its own carry INSIDE one carrier,
// and the carries then flushed in the order the rules ran, not the order the
// bytes arrived: a chunk ending "Antigravity .gemini/GEMINI.md" held "Antigravity "
// in the brand lane and ".gemini/GEMINI.md" in the path lane, and the terminal
// flush delivered them the wrong way round. One lane feeding the whole ordered
// table keeps one carrier in one piece, and the separate carriers that really
// are separate keep their own lanes (tool args, and each choice / tool call).
func applySemanticBrandLane(sess *streamSession, laneKey, text string) (string, bool) {
	if sess == nil || text == "" {
		return text, false
	}
	// noHold: the lane belongs to a choice that is finishing in the event being
	// applied (see streamSession.terminalChoices), so nothing more will arrive
	// for it and a trailing partial token is resolved rather than held. The lane
	// is the choice's own ("openai:0") or one beneath it, so compare roots - the
	// key up to the first lane separator.
	noHold := false
	if len(sess.terminalChoices) > 0 {
		root := laneKey
		if i := strings.Index(laneKey, reverseBrandLaneSuffix); i >= 0 {
			root = laneKey[:i]
		}
		noHold = sess.terminalChoices[root]
	}
	lane := getBrandLane(sess, laneKey)
	next, changed := applyBrandLaneSet(text, lane, laneReverseTable(sess, laneKey), noHold)
	stampLaneArrival(sess, lane)
	return next, changed
}

type textSpanReplacement struct {
	start int
	end   int
	repl  string
}

// replaceToolNamesInText uses pre-compiled regex patterns from cachedCloakPatterns.
// Patterns are compiled once on config change (rebuildCachedRegexes), not per call.
// It executes a single-pass reconstruction from non-overlapping match spans
// evaluated against the original text, ensuring that a target which is also a
// source key is translated exactly once per token and never cascades across tiers.
func replaceToolNamesInText(text string, cached *cachedCloakPatterns) (string, bool) {
	if cached == nil || len(cached.cloakTable) == 0 {
		return text, false
	}

	var replacements []textSpanReplacement

	// Tier 1: single-pass identity replacement. A single regex covers quoted
	// references, namespaced (qualified) identifiers, and unambiguous names.
	if cached.identRe != nil {
		identText := text
		if len(text) >= 8<<10 {
			// Every Tier 1 match contains an exact source tool name. On long prompt
			// text, stop the regex just past the last possible source occurrence;
			// one lookahead byte preserves \b. Short descriptions use the direct
			// regex path to avoid paying for the pre-scan.
			identEnd := 0
			for orig := range cached.cloakTable {
				if index := strings.LastIndex(text, orig); index >= 0 {
					identEnd = max(identEnd, index+len(orig))
				}
			}
			if identEnd == 0 {
				identText = ""
			} else {
				identText = text[:min(len(text), identEnd+1)]
			}
		}
		matches := cached.identRe.FindAllStringIndex(identText, -1)
		for _, m := range matches {
			sub := text[m[0]:m[1]]
			repl := replaceToolIdentity(sub, cached)
			if repl != sub {
				replacements = append(replacements, textSpanReplacement{
					start: m[0],
					end:   m[1],
					repl:  repl,
				})
			}
		}
	}

	// Tier 3: Pattern-based replacement for ambiguous names in tool-reference
	// contexts ("the bash tool", "use read"). Evaluated against original text
	// and skipped if overlapping with Tier 1.
	if cached.ambigRe != nil {
		matches := cached.ambigRe.FindAllStringSubmatchIndex(text, -1)
		for _, sub := range matches {
			if len(sub) < 14 {
				continue
			}
			fullStart, fullEnd := sub[0], sub[1]
			var lead, tool, trail string
			if sub[2] >= 0 && sub[3] >= 0 {
				lead = text[sub[2]:sub[3]]
				tool = text[sub[4]:sub[5]]
				trail = text[sub[6]:sub[7]]
			} else if sub[8] >= 0 && sub[9] >= 0 {
				lead = text[sub[8]:sub[9]]
				tool = text[sub[10]:sub[11]]
				trail = text[sub[12]:sub[13]]
			}
			if tool == "" {
				continue
			}
			prefix, base := splitToolNamespace(tool)
			if prefix != "" && isToolSourceName(strings.TrimSuffix(prefix, ":"), cached.cloakTable) {
				// Access-mode / compound token like "read:write", do not cloak.
				continue
			}
			if target, ok := lookupCloakFold(base, cached.cloakTable); ok {
				newStr := lead + prefix + target + trail
				if newStr != text[fullStart:fullEnd] {
					overlaps := false
					for _, r := range replacements {
						if fullStart < r.end && fullEnd > r.start {
							overlaps = true
							break
						}
					}
					if !overlaps {
						replacements = append(replacements, textSpanReplacement{
							start: fullStart,
							end:   fullEnd,
							repl:  newStr,
						})
					}
				}
			}
		}
	}

	if len(replacements) == 0 {
		return text, false
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})

	var builder strings.Builder
	builder.Grow(len(text))
	last := 0
	for _, r := range replacements {
		if r.start < last {
			continue
		}
		builder.WriteString(text[last:r.start])
		builder.WriteString(r.repl)
		last = r.end
	}
	builder.WriteString(text[last:])

	return builder.String(), true
}

// isToolSourceName reports whether name is one of the cloak table's original
// (source) tool names. Used to avoid mistaking an access-mode or compound
// token such as "read:write" for a qualified tool reference.
func isToolSourceName(name string, cloakTable map[string]string) bool {
	if _, ok := cloakTable[name]; ok {
		return true
	}
	for orig := range cloakTable {
		if strings.EqualFold(orig, name) {
			return true
		}
	}
	return false
}

// lookupCloakFold resolves a possibly different-cased tool base name to its
// cloaked target. The ambiguous context patterns match case-insensitively
// ("use Bash" matches the source key "bash"), so the base must be resolved
// against the table with case folding.
func lookupCloakFold(base string, cloakTable map[string]string) (string, bool) {
	if target, ok := cloakTable[base]; ok {
		return target, true
	}
	for orig, target := range cloakTable {
		if strings.EqualFold(orig, base) {
			return target, true
		}
	}
	return "", false
}

// lookupCloakText maps a bare or qualified tool identity to its cloaked form,
// preserving any namespace prefix. A namespaced reference whose prefix is
// itself a source tool name (e.g. "read:write") is an access-mode / compound
// token, not a tool reference, so it is left unchanged.
func lookupCloakText(ident string, cloakTable map[string]string) (string, bool) {
	if prefix, base := splitToolNamespace(ident); prefix != "" {
		if isToolSourceName(strings.TrimSuffix(prefix, ":"), cloakTable) {
			return "", false
		}
		if _, ok := cloakTable[base]; !ok {
			return "", false
		}
	}
	return lookupCloak(ident, cloakTable)
}

// replaceToolIdentity rewrites a single matched tool identity: it strips any
// surrounding quote, maps the identity through the cloak table, and rebuilds
// the identical quoted form so quoted and unquoted references stay consistent.
func replaceToolIdentity(m string, cached *cachedCloakPatterns) string {
	q := byte(0)
	if len(m) >= 2 {
		first, last := m[0], m[len(m)-1]
		if (first == '`' || first == '"') && first == last {
			q = first
			m = m[1 : len(m)-1]
		}
	}
	repl, ok := lookupCloakText(m, cached.cloakTable)
	if !ok {
		if q != 0 {
			return string(q) + m + string(q)
		}
		return m
	}
	if q != 0 {
		return string(q) + repl + string(q)
	}
	return repl
}

// buildCloakIdentRe compiles a single regex that matches any tool identity in
// prose: quoted names, namespaced (qualified) identifiers, and unambiguous
// names. The alternation order matters — quoted first, then namespaced, then
// unambiguous — so the most specific form wins and bare ambiguous names are
// never rewritten here (they are handled by the contextual Tier 3 rules).
func buildCloakIdentRe(cloakTable map[string]string) *regexp.Regexp {
	all := make([]string, 0, len(cloakTable))
	unambig := make([]string, 0, len(cloakTable))
	for orig := range cloakTable {
		all = append(all, regexp.QuoteMeta(orig))
		if isUnambiguousToolName(orig) {
			unambig = append(unambig, regexp.QuoteMeta(orig))
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Strings(all)
	sort.Strings(unambig)
	allRe := strings.Join(all, "|")
	pattern := "[`\"](?:[a-zA-Z0-9_-]+:)?(?:" + allRe + ")[`\"]|\\b(?:[a-zA-Z0-9_-]+:)(?:" + allRe + ")\\b"
	if len(unambig) > 0 {
		pattern += "|\\b(?:[a-zA-Z0-9_-]+:)?(?:" + strings.Join(unambig, "|") + ")\\b"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// buildCloakAmbiguousRe compiles a single regex that matches ambiguous tool
// names only inside explicit tool-reference contexts ("the bash tool",
// "use read", "call edit"). A single pass guarantees that a target which is
// also a source key is never re-translated within one rewrite.
func buildCloakAmbiguousRe(cloakTable map[string]string) *regexp.Regexp {
	var ambig []string
	for orig := range cloakTable {
		if !isUnambiguousToolName(orig) {
			ambig = append(ambig, regexp.QuoteMeta(orig))
		}
	}
	if len(ambig) == 0 {
		return nil
	}
	sort.Strings(ambig)
	ambigRe := strings.Join(ambig, "|")
	toolRe := `(?:[a-zA-Z0-9_-]+:)?(?:` + ambigRe + `)`
	// "the X tool" requires a trailing tool/function/command word; the verb
	// forms ("use X", "call X") only require a word boundary. Keep the capture
	// groups consistent: 1=lead,2=tool,3=trail for the "the" branch and
	// 4=verb,5=tool,6=trail for the verb branch.
	pattern := `(?i)\b(?:(the\s+)(` + toolRe + `)(\s+(?:tool|function|command)\b)|((?:use|call|run|invoke|with)\s+)(` + toolRe + `)(\b))`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// isUnambiguousToolName returns true if a tool name is specific enough for
// safe word-boundary replacement. Names with underscores or camelCase are
// identifiers, not common English words.
func isUnambiguousToolName(name string) bool {
	if strings.Contains(name, "_") {
		return true
	}
	// camelCase / multi-word names (an uppercase rune AFTER the first one) are
	// distinctive enough to replace anywhere in prose, e.g. "AskUserQuestion",
	// "ToolSearch". Single capitalized words like "Read", "Edit", "Bash" collide
	// with ordinary English prose, so treat them as ambiguous and only replace
	// them inside explicit tool-reference contexts (e.g. "use Bash", "the Read tool").
	for i, r := range name {
		if i == 0 {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func replaceToolNamesInValue(value any, cached *cachedCloakPatterns) (any, bool) {
	switch typed := value.(type) {
	case string:
		return replaceToolNamesInText(typed, cached)
	case map[string]any:
		changed := false
		for key, child := range typed {
			next, childChanged := replaceToolNamesInValue(child, cached)
			if childChanged {
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			next, childChanged := replaceToolNamesInValue(child, cached)
			if childChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}
func extractToolNames(body map[string]any, sourceFormat string) []string {
	var names []string

	// Check tools array first
	if toolsRaw, ok := body["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if fn, ok := tMap["function"].(map[string]any); ok {
						if name, ok := fn["name"].(string); ok {
							names = append(names, name)
						}
					}
				} else if sourceFormat == "anthropic" {
					if name, ok := tMap["name"].(string); ok {
						names = append(names, name)
					}
				}
			}
		}
	}

	// Fallback to history when tools[] is empty or absent
	if len(names) == 0 {
		if msgsRaw, ok := body["messages"].([]any); ok {
			for _, mRaw := range msgsRaw {
				if msg, ok := mRaw.(map[string]any); ok {
					if sourceFormat == "openai" {
						if calls, ok := msg["tool_calls"].([]any); ok {
							for _, cRaw := range calls {
								if call, ok := cRaw.(map[string]any); ok {
									if fn, ok := call["function"].(map[string]any); ok {
										if name, ok := fn["name"].(string); ok {
											names = append(names, name)
										}
									}
								}
							}
						}
					} else if sourceFormat == "anthropic" {
						if contents, ok := msg["content"].([]any); ok {
							for _, cntRaw := range contents {
								if cnt, ok := cntRaw.(map[string]any); ok {
									if typeVal, ok := cnt["type"].(string); ok && typeVal == "tool_use" {
										if name, ok := cnt["name"].(string); ok {
											names = append(names, name)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return names
}

// sourceIdentityInventoryFor returns the static source identity inventory for a
// client, or nil when the client is detected from its runtime rename table.
func sourceIdentityInventoryFor(client string) map[string]bool {
	switch client {
	case "oh_my_pi":
		return ompSourceIdentityInventory
	case "codex":
		return codexSourceIdentityInventory
	default:
		return nil
	}
}

// detectClient identifies the client from ORIGINAL (uncloaked) tool names.
// It checks candidate source tool names against the provided tool name list.
// For oh_my_pi, detection keys off the static ompSourceIdentityInventory and
// clientDistinctiveTools rather than arbitrary runtime cfg.ToolMappings["oh_my_pi"].
// The client with the most key matches wins.
func detectClient(toolNames []string) string {
	cfg := activeFilterConfig()
	nameSet := make(map[string]bool, len(toolNames)*2)
	for _, n := range toolNames {
		nameSet[n] = true
		if _, base := splitToolNamespace(n); base != n {
			nameSet[base] = true
		}
	}
	bestClient := ""
	bestCount := 0

	// Candidates to evaluate: clients from cfg.ToolMappings plus "oh_my_pi"
	clients := make([]string, 0, len(cfg.ToolMappings)+1)
	for c := range cfg.ToolMappings {
		if c != "oh_my_pi" {
			clients = append(clients, c)
		}
	}
	clients = append(clients, "oh_my_pi")
	sort.Strings(clients)

	for _, client := range clients {
		count := 0
		// Clients with a static source identity inventory count against it
		// rather than against their runtime rename table: the two differ, and
		// Codex declares one surface or the other depending on the model's
		// tool mode, so a table-only count would drop whichever mode the
		// table does not key on.
		if inventory := sourceIdentityInventoryFor(client); inventory != nil {
			for orig := range inventory {
				if nameSet[orig] {
					count++
				}
			}
			// An operator can add source names to a client's rename table
			// through tool_mappings, and a static inventory cannot know them,
			// so those keys are counted here as well. OMP is exempt: its
			// attribution must stay on the canonical inventory and never take
			// arbitrary configured names.
			if client != "oh_my_pi" {
				for orig := range cfg.ToolMappings[client] {
					if !inventory[orig] && nameSet[orig] {
						count++
					}
				}
			}
		} else {
			cloakTable := cfg.ToolMappings[client]
			for orig := range cloakTable {
				if nameSet[orig] {
					count++
				}
			}
		}

		// Validation rules per client: clients whose source names are mostly
		// common words need either a distinctive harness tool or several
		// simultaneous matches, per clientDistinctiveTools.
		if distinctives := clientDistinctiveTools[client]; len(distinctives) > 0 {
			hasDistinctive := false
			for name := range distinctives {
				if nameSet[name] {
					hasDistinctive = true
					break
				}
			}
			if !hasDistinctive && count < minCollidingToolMatches {
				continue
			}
		}

		if count > bestCount {
			bestCount = count
			bestClient = client
		}
	}

	if bestCount >= minToolNameHits {
		return bestClient
	}
	return ""
}

// cloakTargetMatch records how many of one client's cloak targets appear in
// an observed tool-name set.
type cloakTargetMatch struct {
	client    string
	hits      int
	observed  int
	tableSize int
}

// higherRatioThan orders two matches by hit ratio over observed tools (m > o), using exact integer
// arithmetic. Equal ratios must be broken by the caller.
func (m cloakTargetMatch) higherRatioThan(o cloakTargetMatch) bool {
	return m.hits*o.observed > o.hits*m.observed
}

// atFullCoverage reports whether every one of the client's table targets matched.
func (m cloakTargetMatch) atFullCoverage() bool {
	return m.hits == m.tableSize
}

// detectCloakedClient identifies which client's cloaking was applied by
// checking cloak TARGET names against the observed tool identities.
// Namespace prefixes are normalised away so each declared tool contributes a
// single observed identity and the observed denominator is never inflated by
// an alias.
//
// Target names alone are NEVER sufficient to attribute traffic to oh_my_pi
// because all canonical OMP targets are native Antigravity tool names.
// Non-OMP clients continue to be detected according to their target tables.
func detectCloakedClient(toolNames []string) string {
	return detectCloakedClientWithSignal(toolNames, false)
}

// detectCloakedClientWithSignal extends detectCloakedClient with an explicit
// attribution signal parameter. When ompAttributed is false, oh_my_pi is excluded
// from standalone target-based attribution. When ompAttributed is true (e.g. from
// correlated request state, UA evidence, or source body evidence), the static
// canonical target inventory is used for corroboration rather than runtime tool_mappings.
func detectCloakedClientWithSignal(toolNames []string, ompAttributed bool) string {
	if len(toolNames) < 3 {
		return ""
	}
	cfg := activeFilterConfig()
	observedSet := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		_, base := splitToolNamespace(n)
		observedSet[base] = true
	}

	totalObserved := len(observedSet)
	var matches []cloakTargetMatch

	var clients []string
	for client := range cfg.ToolMappings {
		if client != "oh_my_pi" {
			clients = append(clients, client)
		}
	}
	if ompAttributed {
		clients = append(clients, "oh_my_pi")
	}
	sort.Strings(clients)

	for _, client := range clients {
		hits := 0
		tableSize := 0
		if client == "oh_my_pi" {
			tableSize = len(ompCloakedTargetIdentityInventory)
			for target := range ompCloakedTargetIdentityInventory {
				if observedSet[target] {
					hits++
				}
			}
		} else {
			cloakTable := cfg.ToolMappings[client]
			tableSize = len(cloakTable)
			if tableSize == 0 {
				continue
			}
			for _, target := range cloakTable {
				if observedSet[target] {
					hits++
				}
			}
		}

		if hits >= 3 && hits*minCloakTargetHitDen >= totalObserved*minCloakTargetHitNum {
			matches = append(matches, cloakTargetMatch{
				client:    client,
				hits:      hits,
				observed:  totalObserved,
				tableSize: tableSize,
			})
		}
	}

	if len(matches) == 0 {
		return ""
	}

	// Native Antigravity superset check: if multiple distinct clients see 100% of their table
	// matched, this is native traffic serving every tool table, so cloaking is skipped.
	fullCoverageCount := 0
	for _, m := range matches {
		if m.atFullCoverage() {
			fullCoverageCount++
		}
	}
	if fullCoverageCount >= 2 {
		return ""
	}

	if len(matches) == 1 {
		return matches[0].client
	}

	// Multiple qualifying clients: rank by target-hit ratio over observed, breaking exact
	// ties deterministically by absolute hit count, full-table coverage, and finally by client id,
	// so repeated detections against identical input agree.
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.higherRatioThan(b) || b.higherRatioThan(a) {
			return a.higherRatioThan(b)
		}
		if a.hits != b.hits {
			return a.hits > b.hits
		}
		if a.atFullCoverage() != b.atFullCoverage() {
			return a.atFullCoverage()
		}
		return a.client < b.client
	})

	top, runnerUp := matches[0], matches[1]
	if top.higherRatioThan(runnerUp) {
		return top.client
	}
	if top.hits > runnerUp.hits {
		return top.client
	}
	if top.atFullCoverage() != runnerUp.atFullCoverage() {
		return top.client
	}
	// Full-coverage tie: both at full coverage → native Antigravity superset.
	if top.atFullCoverage() && runnerUp.atFullCoverage() {
		return ""
	}
	return top.client
}
