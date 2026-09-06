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
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"regexp"
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
	pluginVersion    = "0.4.3"
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
	case pluginabi.MethodResponseInterceptAfter:
		return handleResponseIntercept(request), 0
	case pluginabi.MethodResponseInterceptStreamChunk:
		return handleStreamChunkIntercept(request), 0
	default:
		return mustErrorEnvelope("unknown_method", fmt.Sprintf("unknown method %q", method)), 0
	}
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
			ResponseInterceptor    bool `json:"response_interceptor"`
			StreamChunkInterceptor bool `json:"response_stream_interceptor"`
		}{
			RequestInterceptor:     true,
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

func handleRequestInterceptBefore(request []byte) []byte {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return mustErrorEnvelope("invalid_request", fmt.Sprintf("decode request.intercept_before request: %v", err))
	}

	resp := pluginapi.RequestInterceptResponse{}
	explicitClient, matchedKeys, explicitPresent, explicitValid := resolveExplicitClient(req.Headers)
	if explicitPresent {
		// Consume the plugin-owned control header on both host header contracts:
		// ClearHeaders covers a merging host; the before-auth interceptor applies
		// resp.Headers as the final set, so the filtered clone provides every
		// non-owned inbound header verbatim. Inbound request headers are canonical
		// MIME keys (Go net/http), so the host can always drop the owned key; a
		// non-canonically-spelled stored key is a host-contract limitation outside
		// this plugin's control and cannot be produced by the real HTTP stack.
		resp.ClearHeaders = matchedKeys
		resp.Headers = filteredHeaders(req.Headers, matchedKeys)
	}

	format := normalizeSourceFormat(req.SourceFormat)
	debugLog("handleRequestInterceptBefore: SourceFormat=%s (normalized=%s) ToFormat=%q Model=%q RequestedModel=%q explicitClient=%q valid=%t Body=%s", req.SourceFormat, format, req.ToFormat, req.Model, req.RequestedModel, explicitClient, explicitValid, string(req.Body))
	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleRequestInterceptBefore: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return mustEnvelope(resp)
	}

	// Precedence: valid explicit owned header > verified positive UA evidence
	// > existing body Client Gate. An invalid explicit value bypasses UA and
	// falls directly to body classification so a weaker signal cannot hide
	// operator misconfiguration.
	forcedClient := ""
	if explicitPresent {
		if explicitValid {
			forcedClient = explicitClient
		} else {
			debugLog("handleRequestInterceptBefore: invalid explicit client %q, falling back to body detection", explicitClient)
		}
	} else if uaClient, ok := resolveUserAgentClient(req.Headers); ok {
		forcedClient = uaClient
		debugLog("handleRequestInterceptBefore: UA evidence client=%q", uaClient)
	}
	body, rewritten, client := rewriteRequestBodyWithClient(req.Body, format, forcedClient)
	debugLog("handleRequestInterceptBefore: rewritten=%t client=%s Body=%s", rewritten, client, string(body))
	if req.RequestID != "" {
		if client != "" {
			cached := activeFilterConfig().uncloakRegexCache[client]
			if cached != nil && cached.re != nil {
				globalStreamManager.resetSession("req:"+req.RequestID, client, cached, requestChoiceCount(req.Body))
			}
		} else if explicitPresent && !explicitValid {
			// Precedence ran to completion with UA suppressed and the body
			// classifying nothing: record the authoritative negative so the
			// response/stream paths cannot re-infer a weaker client from the
			// surviving User-Agent after this interceptor consumed the header.
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
	debugLog("handleResponseIntercept: SourceFormat=%s (normalized=%s) RequestBody=%s Body=%s", req.SourceFormat, format, string(req.RequestBody), string(req.Body))
	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleResponseIntercept: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	var client string
	var uncloakTable map[string]string
	correlated := false
	if req.RequestID != "" {
		if c := globalStreamManager.getClient("req:" + req.RequestID); c != "" {
			correlated = true
			if c != negativeClientResolution {
				client = c
				uncloakTable = effectiveUncloakTable(c)
			}
		}
	}
	// Weaker-evidence recovery runs only when request-time correlation is
	// genuinely unavailable; a recorded negative resolution suppresses it.
	if !correlated && uncloakTable == nil {
		if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
			uncloakTable = effectiveUncloakTable(uaClient)
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
		if rev, c := reverseBrandInResponseBody(modified, format); c {
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
	debugLog("handleStreamChunkIntercept: SourceFormat=%s (normalized=%s) ChunkIndex=%d Body=%s", req.SourceFormat, format, req.ChunkIndex, string(req.Body))
	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleStreamChunkIntercept: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
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
	if client != "" {
		return effectiveUncloakTable(client), client
	}

	// Request body may already be cloaked — detect from cloak targets
	cloakedClient := detectCloakedClient(toolNames)
	debugLog("buildUncloakTable: cloakedClient=%s", cloakedClient)
	if cloakedClient != "" {
		return effectiveUncloakTable(cloakedClient), cloakedClient
	}

	return nil, ""
}

// detectionRequestBody returns the body used for client detection. The host
// runs this plugin's request.intercept_before first, so RequestBody is already
// cloaked by the time response/stream interceptors fire. OriginalRequest holds
// the raw client body with original tool names, which the reliable detectClient
// path keys on; fall back to RequestBody when OriginalRequest is unavailable.
func detectionRequestBody(originalRequest, requestBody []byte) []byte {
	if len(originalRequest) > 0 {
		return originalRequest
	}
	return requestBody
}

// splitToolNamespace separates an optional namespace prefix (e.g. "functions:", "default_api:")
// from the base tool name. It returns (prefix, baseName). If no prefix is present, it returns ("", name).
func splitToolNamespace(name string) (string, string) {
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		return name[:idx+1], name[idx+1:]
	}
	return "", name
}

// lookupCloak maps a tool name (with or without namespace prefix) to its cloaked equivalent.
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

func reverseBrandInResponseBody(body []byte, format string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}
	if !reverseAssistantBrandInJSON(root, format) {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func reverseAssistantBrandInJSON(root any, format string) bool {
	changed := false
	switch format {
	case "openai":
		m, ok := root.(map[string]any)
		if !ok {
			return false
		}
		choices, ok := m["choices"].([]any)
		if !ok {
			return false
		}
		for _, chRaw := range choices {
			ch, ok := chRaw.(map[string]any)
			if !ok {
				continue
			}
			if msg, ok := ch["message"].(map[string]any); ok {
				if content, exists := msg["content"]; exists {
					if next, c := reverseBrandInOpenAIContent(content); c {
						msg["content"] = next
						changed = true
					}
				}
			}
			if delta, ok := ch["delta"].(map[string]any); ok {
				if content, exists := delta["content"]; exists {
					if next, c := reverseBrandInOpenAIContent(content); c {
						delta["content"] = next
						changed = true
					}
				}
			}
		}
	case "anthropic":
		m, ok := root.(map[string]any)
		if !ok {
			return false
		}
		contentArr, ok := m["content"].([]any)
		if !ok {
			return false
		}
		for _, blockRaw := range contentArr {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] != "text" {
				continue
			}
			if txt, ok := block["text"].(string); ok {
				if next, c := replaceInsensitive(txt, reverseBrandMatch, reverseBrandReplacement); c {
					block["text"] = next
					changed = true
				}
			}
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

func reverseBrandInOpenAIContent(content any) (any, bool) {
	switch v := content.(type) {
	case string:
		return replaceInsensitive(v, reverseBrandMatch, reverseBrandReplacement)
	case []any:
		changed := false
		for _, partRaw := range v {
			if part, ok := partRaw.(map[string]any); ok {
				if typ, _ := part["type"].(string); !isAssistantTextPartType(typ) {
					continue
				}
				if txt, ok := part["text"].(string); ok {
					if next, c := replaceInsensitive(txt, reverseBrandMatch, reverseBrandReplacement); c {
						part["text"] = next
						changed = true
					}
				}
			} else if s, ok := partRaw.(string); ok {
				if next, c := replaceInsensitive(s, reverseBrandMatch, reverseBrandReplacement); c {
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
			if next, c := replaceInsensitive(txt, reverseBrandMatch, reverseBrandReplacement); c {
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
		if orig, ok := lookupUncloak(toolName, cached.lookup); ok {
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

func (m *streamSessionManager) reverseBrandSSE(sess *streamSession, sseBytes []byte, format string) ([]byte, bool) {
	if sess == nil || sess.client != "oh_my_pi" {
		return nil, false
	}
	isDone := bytes.Contains(sseBytes, []byte("data: [DONE]")) || bytes.Contains(sseBytes, []byte("data:[DONE]"))
	events := splitSSEEventsForBrand(sseBytes)
	var out bytes.Buffer
	changedOverall := false
	for _, ev := range events {
		trimmed := bytes.TrimSpace(ev)
		isDoneEvent := bytes.HasPrefix(trimmed, []byte("data: [DONE]")) || bytes.HasPrefix(trimmed, []byte("data:[DONE]")) || (bytes.Contains(trimmed, []byte("[DONE]")) && bytes.HasPrefix(trimmed, []byte("data:")))
		if isDoneEvent {
			if isDone {
				flushEvents := m.generateBrandFlushEvents(sess, format)
				for _, fe := range flushEvents {
					out.Write(fe)
					changedOverall = true
				}
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
				var flushEvents [][]byte
				if kind == "content_block_stop" {
					flushEvents = m.generateBrandFlushEventsFiltered(sess, format, []string{laneKey})
				} else { // message_stop
					flushEvents = m.generateBrandFlushEvents(sess, format)
				}
				for _, fe := range flushEvents {
					out.Write(fe)
					changedOverall = true
				}
			}
		}
		modifiedEv, changed := m.reverseBrandSingleSSEEvent(sess, ev, format, false)
		if changed {
			out.Write(modifiedEv)
			changedOverall = true
		} else {
			out.Write(ev)
		}
	}
	return out.Bytes(), changedOverall
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

func (m *streamSessionManager) reverseBrandSingleSSEEvent(sess *streamSession, ev []byte, format string, isFinal bool) ([]byte, bool) {
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
		didChange := false
		if format == "openai" {
			didChange = m.reverseBrandOpenAIStreamingMap(dataMap, sess, isFinal)
		} else if format == "anthropic" {
			didChange = m.reverseBrandAnthropicStreamingMap(dataMap, sess, isFinal)
		}
		if didChange {
			newPayload, err := safeMarshal(dataMap)
			if err == nil {
				lines[i] = "data: " + string(newPayload)
				changed = true
			}
		}
	}
	if !changed {
		return ev, false
	}
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
	return []byte(rebuilt), true
}

func (m *streamSessionManager) reverseBrandOpenAIStreamingMap(data map[string]any, sess *streamSession, isFinal bool) bool {
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
		lane := getBrandLane(sess, laneKey)
		var delta map[string]any
		if d, ok := ch["delta"].(map[string]any); ok {
			delta = d
		} else if d, ok := ch["message"].(map[string]any); ok {
			delta = d
		}
		if delta == nil {
			continue
		}
		if content, exists := delta["content"]; exists {
			switch v := content.(type) {
			case string:
				newStr, _ := applyBrandLane(v, lane, isFinal)
				if newStr != v {
					delta["content"] = newStr
					changed = true
				}
			case []any:
				c2 := false
				for _, partRaw := range v {
					if part, ok := partRaw.(map[string]any); ok {
						if typ, _ := part["type"].(string); !isAssistantTextPartType(typ) {
							continue
						}
						if txt, ok := part["text"].(string); ok {
							newTxt, _ := applyBrandLane(txt, lane, isFinal)
							if newTxt != txt {
								part["text"] = newTxt
								c2 = true
							}
						}
					} else if s, ok := partRaw.(string); ok {
						newStr, _ := applyBrandLane(s, lane, isFinal)
						if newStr != s {
							for i, elem := range v {
								if elem == partRaw {
									v[i] = newStr
									c2 = true
									break
								}
							}
						}
					}
				}
				if c2 {
					changed = true
				}
			case map[string]any:
				if typ, _ := v["type"].(string); isAssistantTextPartType(typ) {
					if txt, ok := v["text"].(string); ok {
						newTxt, _ := applyBrandLane(txt, lane, isFinal)
						if newTxt != txt {
							v["text"] = newTxt
							changed = true
						}
					}
				}
			}
		}
	}
	return changed
}

func (m *streamSessionManager) reverseBrandAnthropicStreamingMap(data map[string]any, sess *streamSession, isFinal bool) bool {
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
	lane := getBrandLane(sess, laneKey)
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
			newTxt, _ := applyBrandLane(txt, lane, isFinal)
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
		if delta["type"] != "text_delta" {
			return false
		}
		if txt, ok := delta["text"].(string); ok {
			newTxt, _ := applyBrandLane(txt, lane, isFinal)
			if newTxt != txt {
				delta["text"] = newTxt
				return true
			}
		}
	default:
		if delta, ok := data["delta"].(map[string]any); ok {
			if delta["type"] == "text_delta" {
				if txt, ok := delta["text"].(string); ok {
					newTxt, _ := applyBrandLane(txt, lane, isFinal)
					if newTxt != txt {
						delta["text"] = newTxt
						return true
					}
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

// laneIndexNum parses the numeric lane index from a "format:N" carry key.
func laneIndexNum(key string) (int, bool) {
	i := strings.LastIndexByte(key, ':')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// orderedBrandFlushes resolves the final text of every held carry in
// deterministic lane order: numeric lane indices ascending, non-numeric lanes
// last, tie-broken by key. Go map iteration would otherwise randomise the
// cross-lane flush sequence; Issue #18 requires stable event/lane ordering
// while keeping distinct indexed lanes isolated. This does not mutate lane
// state — callers drain only once the flushed text is safely delivered.
func orderedBrandFlushes(sess *streamSession) []brandFlush {
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
		return keys[i] < keys[j]
	})
	flushes := make([]brandFlush, 0, len(keys))
	for _, k := range keys {
		lane := sess.brandCarries[k]
		finalOut, _ := replaceInsensitiveWithPrev(lane.carry, lane.lastIsWord, reverseBrandMatch, reverseBrandReplacement)
		if finalOut == "" {
			finalOut = lane.carry
		}
		if finalOut == "" {
			continue
		}
		flushes = append(flushes, brandFlush{key: k, text: finalOut})
	}
	return flushes
}

func drainBrandFlushes(sess *streamSession, flushes []brandFlush) {
	for _, f := range flushes {
		lane := sess.brandCarries[f.key]
		if lane == nil {
			continue
		}
		lane.carry = ""
		lane.lastIsWord = isWordByte(f.text[len(f.text)-1])
	}
}

func (m *streamSessionManager) generateBrandFlushEvents(sess *streamSession, format string) [][]byte {
	return m.generateBrandFlushEventsFiltered(sess, format, nil)
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
	s := string(ev)
	// Split into lines handling both \n and \r\n.
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := safeUnmarshal([]byte(payload), &m); err != nil {
			continue
		}
		if k, lk := anthropicTerminalKindFromMap(m); k != "" {
			return k, lk
		}
	}
	return "", ""
}

func sseContainsAnthropicMessageStop(sse []byte) bool {
	for _, ev := range splitSSEEventsForBrand(sse) {
		if k, _ := sseAnthropicTerminalKind(ev); k == "message_stop" {
			return true
		}
	}
	return false
}

// generateBrandFlushEventsFiltered emits pending carries filtered to only the
// given lane keys (nil means all), draining each flushed lane. Preserves the
// deterministic lane ordering of orderedBrandFlushes.
func (m *streamSessionManager) generateBrandFlushEventsFiltered(sess *streamSession, format string, only []string) [][]byte {
	flushes := orderedBrandFlushes(sess)
	if only != nil {
		filtered := flushes[:0]
		for _, f := range flushes {
			for _, k := range only {
				if f.key == k {
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
		idx, _ := laneIndexNum(f.key)
		var ev []byte
		if format == "openai" {
			dataMap := map[string]any{
				"choices": []any{
					map[string]any{
						"index": idx,
						"delta": map[string]any{
							"content": f.text,
						},
					},
				},
			}
			jb, _ := safeMarshal(dataMap)
			ev = []byte("data: " + string(jb) + "\n\n")
		} else if format == "anthropic" {
			dataMap := map[string]any{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]any{
					"type": "text_delta",
					"text": f.text,
				},
			}
			jb, _ := safeMarshal(dataMap)
			ev = []byte("data: " + string(jb) + "\n\n")
		} else {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// markStandaloneFinishes records the choices a standalone (non-SSE) chunk
// finished: a non-null finish_reason ends ONLY its own choice lane, never the
// whole stream (with n > 1 other choices keep streaming). It reports the lanes
// finished by this chunk and whether the stream as a whole is done: a global
// terminal payload (bare [DONE] or Anthropic message_stop), or finish_reasons
// covering every expected choice lane.
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
			finished = append(finished, laneKey)
		}
	}
	if len(finished) == 0 || len(sess.brandCarries) < sess.expected {
		return finished, false
	}
	for _, lane := range sess.brandCarries {
		if !lane.finished {
			return finished, false
		}
	}
	return finished, true
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

func (m *streamSessionManager) flushBrandStandalone(sess *streamSession, body []byte, format string, only []string) ([]byte, bool) {
	if sess == nil || sess.client != "oh_my_pi" || len(sess.brandCarries) == 0 {
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
				if k == f.key {
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
					for i, f := range flushes {
						idx, _ := laneIndexNum(f.key)
						dataMap := map[string]any{
							"type":  "content_block_delta",
							"index": idx,
							"delta": map[string]any{
								"type": "text_delta",
								"text": f.text,
							},
						}
						jb, _ := safeMarshal(dataMap)
						if i > 0 {
							buf.WriteString("\n\n")
						}
						buf.WriteString("data: ")
						buf.Write(jb)
					}
					buf.WriteString("\n\ndata: ")
					buf.Write(trimmed)
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
		idx, ok := laneIndexNum(f.key)
		if !ok {
			idx = 0
		}
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
		if s, ok := delta["content"].(string); ok {
			delta["content"] = s + f.text
		} else {
			delta["content"] = f.text
		}
	}
	root["choices"] = choices
	return true
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
	txt, ok := delta["text"].(string)
	if !ok {
		return false
	}
	delta["text"] = txt + flushes[0].text
	return true
}

func (m *streamSessionManager) reverseBrandStandalone(sess *streamSession, body []byte, format string) ([]byte, bool) {
	if sess == nil {
		return nil, false
	}
	if sess.client != "oh_my_pi" {
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
	changed := false
	if format == "openai" {
		changed = m.reverseBrandOpenAIStreamingMap(root, sess, false)
	} else if format == "anthropic" {
		if _, ok := root["delta"]; ok || root["type"] == "content_block_delta" || root["type"] == "content_block_start" {
			changed = m.reverseBrandAnthropicStreamingMap(root, sess, false)
		} else {
			changed = m.reverseBrandOpenAIStreamingMap(root, sess, false)
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
	// expected is the request's OpenAI "n" (choices per completion),
	// minimum 1; gates standalone stream-end detection.
	expected int
}

const (
	reverseBrandMatch       = "Antigravity"
	reverseBrandReplacement = "omp"
)

type streamSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*streamSession
}

var globalStreamManager = newStreamSessionManager()

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

func (m *streamSessionManager) getClient(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[key]; sess != nil {
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
		if s.updatedAt.Before(cutoff) {
			delete(m.sessions, k)
		}
	}
}

func (m *streamSessionManager) processChunk(req *pluginapi.StreamChunkInterceptRequest, format string) pluginapi.StreamChunkInterceptResponse {
	key := m.sessionKey(req)

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
		detectSrc := detectionRequestBody(req.OriginalRequest, req.RequestBody)
		n := requestChoiceCount(detectSrc)
		if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
			debugLog("StreamSessionManager: header-init UA evidence key=%s client=%s", key, uaClient)
			if cached := activeFilterConfig().uncloakRegexCache[uaClient]; cached != nil && cached.re != nil {
				m.resetSession(key, uaClient, cached, n)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		_, client := buildUncloakTable(detectSrc, format)
		debugLog("StreamSessionManager: header-init key=%s client=%s", key, client)
		if client != "" {
			cached := activeFilterConfig().uncloakRegexCache[client]
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
		sess = m.ensureFallbackSession(req, format, key)
	}
	if sess == nil || sess.cached == nil || sess.cached.re == nil {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	cached := sess.cached

	// Reset buffer on first payload chunk (ChunkIndex == 0)
	m.mu.Lock()
	if req.ChunkIndex == 0 {
		sess.tail = nil
	}

	var combined []byte
	if len(sess.tail) > 0 {
		combined = make([]byte, len(sess.tail)+len(req.Body))
		copy(combined, sess.tail)
		copy(combined[len(sess.tail):], req.Body)
	} else {
		combined = req.Body
	}

	// Check if this is an SSE-formatted stream vs individual JSON chunk payload
	trimmed := bytes.TrimSpace(combined)
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.Contains(combined, []byte("\n\n")) || bytes.Contains(combined, []byte("\r\n\r\n")) {
		completeEvents, incompleteTail := splitSSEEvents(combined)
		sess.tail = incompleteTail
		sess.updatedAt = time.Now()
		m.mu.Unlock()

		if len(completeEvents) == 0 {
			debugLog("StreamSessionManager: no complete events, dropping chunk len=%d", len(incompleteTail))
			return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
		}

		modified, changed := uncloakStreamChunk(completeEvents, cached)
		if !changed {
			modified = completeEvents
		}
		brandChanged := false
		if sess.client == "oh_my_pi" {
			if bm, bc := m.reverseBrandSSE(sess, modified, format); bc {
				modified = bm
				brandChanged = true
			} else if bm != nil && !bytes.Equal(bm, modified) {
				modified = bm
				brandChanged = true
			}
		}
		overallChanged := changed || brandChanged
		isDone := bytes.Contains(completeEvents, []byte("data: [DONE]")) || bytes.Contains(modified, []byte("data: [DONE]"))
		isAnthropicEnd := format == "anthropic" && (sseContainsAnthropicMessageStop(completeEvents) || sseContainsAnthropicMessageStop(modified))
		if isDone || isAnthropicEnd {
			if isDone && sess.client == "oh_my_pi" {
				// Safety net for [DONE] embedded in a multi-line event that
				// reverseBrandSSE's per-event check misses. generateBrandFlushEvents
				// drains each lane as it emits, so the common case (flush events
				// already written before the [DONE] frame) is a no-op here — no
				// double emission.
				if flush := m.generateBrandFlushEvents(sess, format); len(flush) > 0 {
					doneIdx := bytes.Index(modified, []byte("data: [DONE]"))
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
			}
		}
		if !overallChanged {
			return pluginapi.StreamChunkInterceptResponse{}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: modified}
	}

	// Standalone JSON chunk payload (e.g. CLIProxyAPI OpenAI protocol chunk)
	sess.tail = nil
	sess.updatedAt = time.Now()
	m.mu.Unlock()

	modified, changed := uncloakStreamChunk(req.Body, cached)
	if !changed {
		modified = req.Body
	}
	if sess.client == "oh_my_pi" {
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
		if cached := activeFilterConfig().uncloakRegexCache[uaClient]; cached != nil && cached.re != nil {
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
	cached := activeFilterConfig().uncloakRegexCache[client]
	if cached == nil || cached.re == nil {
		return nil
	}
	return m.ensureSession(key, client, cached, n)
}

// splitSSEEvents splits combined bytes into complete SSE events and an
// incomplete trailing tail. Complete events are those terminated by "\n\n".
// The returned completeEvents includes the terminating "\n\n" sequences.
func splitSSEEvents(data []byte) (completeEvents []byte, incompleteTail []byte) {
	// Find the last event boundary (\n\n)
	// Also check \r\n\r\n for Windows-style line endings
	lastBoundary := -1
	boundaryLen := 0

	if idx := bytes.LastIndex(data, []byte("\n\n")); idx >= 0 {
		lastBoundary = idx
		boundaryLen = 2
	}
	if idx := bytes.LastIndex(data, []byte("\r\n\r\n")); idx >= 0 {
		// Use whichever boundary is LATER (further into the data)
		if idx > lastBoundary {
			lastBoundary = idx
			boundaryLen = 4
		}
	}

	if lastBoundary < 0 {
		// No complete event boundary found — everything is incomplete
		return nil, data
	}

	splitAt := lastBoundary + boundaryLen
	return data[:splitAt], data[splitAt:]
}

func uncloakJSONNode(node any, uncloakTable map[string]string, sourceFormat string) bool {
	changed := false

	switch typed := node.(type) {
	case map[string]any:
		if sourceFormat == "openai" {
			if msg, ok := typed["message"].(map[string]any); ok {
				if toolCalls, ok := msg["tool_calls"].([]any); ok {
					for _, tcRaw := range toolCalls {
						if tc, ok := tcRaw.(map[string]any); ok {
							if fn, ok := tc["function"].(map[string]any); ok {
								if name, ok := fn["name"].(string); ok {
									if orig, exists := lookupUncloak(name, uncloakTable); exists {
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
									if orig, exists := lookupUncloak(name, uncloakTable); exists {
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
					if orig, exists := lookupUncloak(name, uncloakTable); exists {
						typed["name"] = orig
						changed = true
					}
				}
			}
		}

		for _, v := range typed {
			if childChanged := uncloakJSONNode(v, uncloakTable, sourceFormat); childChanged {
				changed = true
			}
		}

	case []any:
		for _, v := range typed {
			if childChanged := uncloakJSONNode(v, uncloakTable, sourceFormat); childChanged {
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

var defaultRewriteMappings = []rewriteMapping{
	// Major AI code editors, assistants, and terminal coding agents.
	{Match: "Claude Code", Replacement: "Antigravity"},
	{Match: "OpenAI Codex", Replacement: "Antigravity"},
	{Match: "Codex CLI", Replacement: "Antigravity"},
	{Match: "Codex", Replacement: "Antigravity"},
	{Match: "OpenCode", Replacement: "Antigravity"},
	{Match: "GitHub Copilot CLI", Replacement: "Antigravity"},
	{Match: "GitHub Copilot", Replacement: "Antigravity"},
	{Match: "Gemini Code Assist", Replacement: "Antigravity"},
	{Match: "Gemini CLI", Replacement: "Antigravity"},
	{Match: "Cursor", Replacement: "Antigravity"},
	{Match: "Windsurf", Replacement: "Antigravity"},
	{Match: "Codeium", Replacement: "Antigravity"},
	{Match: "Cline", Replacement: "Antigravity"},
	{Match: "Roo Code", Replacement: "Antigravity"},
	{Match: "Kilo Code", Replacement: "Antigravity"},
	{Match: "Aider", Replacement: "Antigravity"},
	{Match: "Continue.dev", Replacement: "Antigravity"},
	{Match: "Amazon Q Developer", Replacement: "Antigravity"},
	{Match: "Amazon CodeWhisperer", Replacement: "Antigravity"},
	{Match: "JetBrains AI Assistant", Replacement: "Antigravity"},
	{Match: "JetBrains Junie", Replacement: "Antigravity"},
	{Match: "Kiro", Replacement: "Antigravity"},
	{Match: "Qoder CLI", Replacement: "Antigravity"},
	{Match: "Qoder", Replacement: "Antigravity"},
	{Match: "Qwen Code", Replacement: "Antigravity"},
	{Match: "Trae", Replacement: "Antigravity"},
	{Match: "Tabnine", Replacement: "Antigravity"},
	{Match: "Sourcegraph Cody", Replacement: "Antigravity"},
	{Match: "Augment Code", Replacement: "Antigravity"},
	{Match: "Replit Agent", Replacement: "Antigravity"},
	{Match: "Replit Ghostwriter", Replacement: "Antigravity"},
	{Match: "Devin", Replacement: "Antigravity"},
	{Match: "OpenHands", Replacement: "Antigravity"},
	{Match: "SWE-agent", Replacement: "Antigravity"},
	{Match: "Goose", Replacement: "Antigravity"},
	{Match: "Zed AI", Replacement: "Antigravity"},
	{Match: "Void Editor", Replacement: "Antigravity"},
	{Match: "PearAI", Replacement: "Antigravity"},
	{Match: "Refact.ai", Replacement: "Antigravity"},
	{Match: "Tabby", Replacement: "Antigravity"},
	{Match: "GitLab Duo", Replacement: "Antigravity"},
	{Match: "Visual Studio IntelliCode", Replacement: "Antigravity"},
	{Match: "CodeBuddy", Replacement: "Antigravity"},
	{Match: "Blackbox AI", Replacement: "Antigravity"},
	{Match: "Pieces for Developers", Replacement: "Antigravity"},
	{Match: "Qodo", Replacement: "Antigravity"},
	{Match: "CodiumAI", Replacement: "Antigravity"},
	{Match: "Rovo Dev CLI", Replacement: "Antigravity"},
	{Match: "Factory Droid", Replacement: "Antigravity"},

	// Oh My Pi coding agent & harness.
	{Match: "Oh My Pi", Replacement: "Antigravity"},
	{Match: "oh-my-pi", Replacement: "Antigravity"},
	{Match: "omp", Replacement: "Antigravity"},

	// General-purpose local agents that can generate and modify code.
	{Match: "OpenClaw", Replacement: "Antigravity"},
	{Match: "Clawdbot", Replacement: "Antigravity"},
	{Match: "Moltbot", Replacement: "Antigravity"},
	{Match: "Hermes Agent", Replacement: "Antigravity"},
	{Match: "Hermes", Replacement: "Antigravity"},
	{Match: "WorkBuddy", Replacement: "Antigravity"},
}

type rewriteMapping struct {
	Match       string
	Replacement string
}

var defaultCloakTables = map[string]map[string]string{
	"claude_code": {
		"Bash": "run_command", "Edit": "replace_file_content", "Read": "view_file",
		"Write": "write_to_file", "Grep": "grep_search", "Glob": "list_dir",
		"Agent": "invoke_subagent", "AskUserQuestion": "ask_question",
		"ToolSearch": "search_web", "Skill": "call_mcp_tool", "Workflow": "schedule",
	},
	"codex": {
		"shell_command": "run_command", "apply_patch": "multi_replace_file_content",
		"request_user_input": "ask_question", "view_image": "generate_image",
		"update_plan": "manage_task", "tool_search": "search_web",
		"get_goal":                    "schedule",
		"create_goal":                 "send_message",
		"update_goal":                 "define_subagent",
		"list_mcp_resources":          "list_resources",
		"list_mcp_resource_templates": "list_permissions",
		"read_mcp_resource":           "read_resource",
	},
	"oh_my_pi": {
		"read":            "view_file",
		"write":           "write_to_file",
		"edit":            "replace_file_content",
		"bash":            "run_command",
		"grep":            "grep_search",
		"glob":            "list_dir",
		"task":            "invoke_subagent",
		"ask":             "ask_question",
		"todo":            "manage_task",
		"hub":             "send_message",
		"web_search":      "search_web",
		"eval":            "execute_code",
		"vibe_spawn":      "define_subagent",
		"vibe_send":       "schedule",
		"vibe_wait":       "wait",
		"vibe_kill":       "cancel",
		"vibe_list":       "list",
		"init_experiment": "create_goal",
		"run_experiment":  "call_mcp_tool",
		"log_experiment":  "update_plan",
		"update_notes":    "update_goal",
	},
}

// clientDistinctiveTools lists harness-specific source tool names whose
// presence alone identifies a client. Clients whose source names are mostly
// common words ("read", "bash") collide with arbitrary user-defined tools,
// so those names require several simultaneous matches instead.
//
// MUST stay in sync with the matching keys of defaultCloakTables; update both
// together when a table changes.
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
	re     *regexp.Regexp
	lookup map[string]string // matched target → original name
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

// resolveExplicitClient reads the plugin-owned X-Cloak-Client header from the
// inbound request headers. It returns the normalized client key, every stored
// header key spelling that matches the owned header case-insensitively, whether
// the header was present at all, and whether the value resolves to a currently
// usable (non-empty) ToolMappings entry. All matching key spellings are returned
// so the interceptor can clear every variant; the client value is derived from
// the lexicographically first spelling for deterministic selection.
func resolveExplicitClient(headers http.Header) (client string, matchedKeys []string, present, valid bool) {
	if headers == nil {
		return "", nil, false, false
	}
	for k := range headers {
		if strings.EqualFold(k, explicitClientHeader) {
			matchedKeys = append(matchedKeys, k)
		}
	}
	if len(matchedKeys) == 0 {
		return "", nil, false, false
	}
	sort.Strings(matchedKeys)
	value := ""
	if vs := headers[matchedKeys[0]]; len(vs) > 0 {
		value = vs[0]
	}
	client = normalizeClientKey(value)
	if client == "" {
		return client, matchedKeys, true, false
	}
	if table := activeFilterConfig().ToolMappings[client]; len(table) == 0 {
		return client, matchedKeys, true, false
	}
	return client, matchedKeys, true, true
}

// resolveUserAgentClient returns a client derived from conservative
// User-Agent evidence. Only prefixes listed in userAgentEvidence may match,
// comparison is case-insensitive, and the match requires a usable active
// ToolMappings entry so entries like opencode/ stay inert until a table
// exists. The UA value is taken from the lexicographically first
// User-Agent key spelling for determinism, mirroring resolveExplicitClient.
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
			result[normalized] = clientMap
			continue
		}
		for orig, target := range clientMap {
			result[normalized][orig] = target
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

func effectiveMappings(cfg *filterConfig) []rewriteMapping {
	if cfg == nil {
		return defaultRewriteMappings
	}
	mappings := make([]rewriteMapping, 0, len(defaultRewriteMappings)+len(cfg.CustomMappings))
	if cfg.UseDefaultKeywords {
		mappings = append(mappings, defaultRewriteMappings...)
	}
	if len(cfg.CustomMappings) > 0 {
		mappings = append(mappings, cfg.CustomMappings...)
	}
	return normalizeMappings(mappings)
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
		if _, exists := seen[match]; exists {
			continue
		}
		seen[match] = struct{}{}
		reversed = append(reversed, rewriteMapping{Match: match, Replacement: replacement})
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

	mappings := effectiveMappings(cfg)
	rewritten, sysChanged := rewriteSystemFields(rootMap, mappings)
	rootMap = rewritten.(map[string]any)
	changed = changed || sysChanged

	descChanged := rewriteToolDescriptions(rootMap, mappings, cachedCloak, sourceFormat)
	changed = changed || descChanged

	sysMsgChanged := rewriteSystemMessages(rootMap, mappings, cachedCloak)
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
		if sourceFormat == "openai" {
			fn, ok := tMap["function"].(map[string]any)
			if !ok {
				continue
			}
			if rewriteDescriptionField(fn, "description", mappings, cached) {
				changed = true
			}
		} else if sourceFormat == "anthropic" {
			if rewriteDescriptionField(tMap, "description", mappings, cached) {
				changed = true
			}
		}
	}
	return changed
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
		next, replaced = replaceBrandKeyword(next, mapping.Match, mapping.Replacement)
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

func rewriteSystemMessages(root map[string]any, mappings []rewriteMapping, cached *cachedCloakPatterns) bool {
	changed := false
	msgsRaw, ok := root["messages"].([]any)
	if !ok {
		return false
	}
	for _, mRaw := range msgsRaw {
		msg, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		if role, ok := msg["role"].(string); ok && role == "system" {
			if content, exists := msg["content"]; exists {
				next, contentChanged := rewriteSystemValue(content, mappings)
				if contentChanged {
					msg["content"] = next
					changed = true
				}
				if cached != nil {
					current := msg["content"]
					toolNext, toolChanged := replaceToolNamesInValue(current, cached)
					if toolChanged {
						msg["content"] = toolNext
						changed = true
					}
				}
			}
		}
	}
	return changed
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
			next, replaced = replaceBrandKeyword(next, mapping.Match, mapping.Replacement)
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

func replaceInsensitive(value, match, replacement string) (string, bool) {
	return replaceInsensitiveOpt(value, match, replacement, false)
}

// replaceInsensitiveOpt is the shared case-insensitive word-boundary matcher.
// When skipPath is true, matches that are literal ".omp" path segments (preceded by
// '.') are left untouched.
func replaceInsensitiveOpt(value, match, replacement string, skipPath bool) (string, bool) {
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
		hasRightBoundary := !lastIsWord || matchEnd == len(value) || !isWordByte(value[matchEnd])
		pathSegment := false
		if skipPath && lowerMatch == "omp" && index > 0 && value[index-1] == '.' {
			dotIndex := index - 1
			leftSegmentBoundary := dotIndex == 0 || value[dotIndex-1] == '/' || value[dotIndex-1] == '\\'
			rightSegmentBoundary := matchEnd == len(value) || value[matchEnd] == '/' || value[matchEnd] == '\\'
			pathSegment = leftSegmentBoundary && rightSegmentBoundary
		}

		if hasLeftBoundary && hasRightBoundary && !pathSegment {
			builder.WriteString(value[start:index])
			builder.WriteString(replacement)
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

// replaceBrandKeyword is the forward brand rewrite, skipping ".omp" path segments so
// real resource paths (e.g. ".omp" in C:\Users\monet\.omp\agent) are never
// masked into tool arguments the reverse brand path does not restore.
func replaceBrandKeyword(value, match, replacement string) (string, bool) {
	return replaceInsensitiveOpt(value, match, replacement, true)
}

func replaceInsensitiveWithPrev(value string, prevIsWord bool, match, replacement string) (string, bool) {
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
		hasLeftBoundary := !firstIsWord
		if firstIsWord {
			if index == 0 {
				hasLeftBoundary = !prevIsWord
			} else {
				hasLeftBoundary = !isWordByte(value[index-1])
			}
		}
		hasRightBoundary := !lastIsWord || matchEnd == len(value) || !isWordByte(value[matchEnd])
		if hasLeftBoundary && hasRightBoundary {
			builder.WriteString(value[start:index])
			builder.WriteString(replacement)
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

func findHoldLenWithBoundary(combined, brand string, prevIsWord bool) int {
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
		if leftOK {
			return k
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

func applyBrandLane(text string, lane *brandLane, isFinal bool) (string, bool) {
	combined := lane.carry + text
	if combined == "" {
		return "", false
	}
	if isFinal {
		out, changed := replaceInsensitiveWithPrev(combined, lane.lastIsWord, reverseBrandMatch, reverseBrandReplacement)
		lane.carry = ""
		if out != "" {
			lane.lastIsWord = isWordByte(out[len(out)-1])
		}
		return out, changed || out != combined
	}
	holdLen := findHoldLenWithBoundary(combined, reverseBrandMatch, lane.lastIsWord)
	if holdLen == 0 {
		out, changed := replaceInsensitiveWithPrev(combined, lane.lastIsWord, reverseBrandMatch, reverseBrandReplacement)
		lane.carry = ""
		if out != "" {
			lane.lastIsWord = isWordByte(out[len(out)-1])
		}
		return out, changed || out != combined
	}
	emitPart := combined[:len(combined)-holdLen]
	newCarry := combined[len(combined)-holdLen:]
	outEmit, changedEmit := replaceInsensitiveWithPrev(emitPart, lane.lastIsWord, reverseBrandMatch, reverseBrandReplacement)
	lane.carry = newCarry
	if outEmit != "" {
		lane.lastIsWord = isWordByte(outEmit[len(outEmit)-1])
	}
	return outEmit, changedEmit || outEmit != emitPart
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
		matches := cached.identRe.FindAllStringIndex(text, -1)
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

func walkJSON(value any, visit func(path []string, value any) bool) {
	var walk func(path []string, current any) bool
	walk = func(path []string, current any) bool {
		if !visit(path, current) {
			return false
		}
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if !walk(appendPath(path, key), child) {
					return false
				}
			}
		case []any:
			for index, child := range typed {
				if !walk(appendPath(path, fmt.Sprintf("%d", index)), child) {
					return false
				}
			}
		}
		return true
	}
	walk(nil, value)
}

func appendPath(path []string, item string) []string {
	next := make([]string, len(path), len(path)+1)
	copy(next, path)
	return append(next, item)
}

func collectText(value any) string {
	var parts []string
	var collect func(any)
	collect = func(current any) {
		switch typed := current.(type) {
		case string:
			parts = append(parts, typed)
		case map[string]any:
			for _, child := range typed {
				collect(child)
			}
		case []any:
			for _, child := range typed {
				collect(child)
			}
		}
	}
	collect(value)
	return strings.Join(parts, "\n")
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

// detectClient identifies the client from ORIGINAL (uncloaked) tool names.
// It is data-driven: it checks cloak table keys (source tool names) against
// the provided tool name list. The client with the most key matches wins.
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
	for client, cloakTable := range cfg.ToolMappings {
		count := 0
		for orig := range cloakTable {
			if nameSet[orig] {
				count++
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
// an alias. A candidate qualifies when at least 3 tools match AND it covers
// at least 80% of the observed unique identities (hits/observed). The static
// table length is not an alternative qualification path.
// When multiple distinct tables reach full coverage (hits == tableSize),
// native Antigravity traffic serving every tool table is indistinguishable,
// so cloaking is skipped.
func detectCloakedClient(toolNames []string) string {
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
	for client, cloakTable := range cfg.ToolMappings {
		if len(cloakTable) == 0 {
			continue
		}
		hits := 0
		for _, target := range cloakTable {
			if observedSet[target] {
				hits++
			}
		}
		if hits >= 3 && hits*minCloakTargetHitDen >= totalObserved*minCloakTargetHitNum {
			matches = append(matches, cloakTargetMatch{
				client:    client,
				hits:      hits,
				observed:  totalObserved,
				tableSize: len(cloakTable),
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
	// ties deterministically by absolute hit count and finally by client id,
	// so repeated detections against identical input agree.
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.higherRatioThan(b) || b.higherRatioThan(a) {
			return a.higherRatioThan(b)
		}
		if a.hits != b.hits {
			return a.hits > b.hits
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
	// Full-coverage tie
	if top.atFullCoverage() && runnerUp.atFullCoverage() {
		return ""
	}
	return top.client
}
