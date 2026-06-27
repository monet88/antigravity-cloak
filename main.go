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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const abiVersion = 1

const (
	pluginName       = "antigravity-coding-filter"
	pluginVersion    = "0.1.0"
	pluginRepository = "https://github.com/jellyfish-p/cpa-plugin-antigravity-coding-filter"
)

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
			StreamChunkInterceptor bool `json:"stream_chunk_interceptor"`
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
			StreamChunkInterceptor bool `json:"stream_chunk_interceptor"`
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
			Description: "Enable the built-in rewrite mapping preset: OpenCode, Codex, Claude Code -> Antigravity.",
		},
		{
			Name:        "custom_mappings",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Additional case-insensitive system-field rewrite mappings, for example Cursor: Antigravity.",
		},
		{
			Name:        "tool_mappings",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Custom tool name mappings per client. Keys: client name (claude_code, codex). Values: map of original_tool_name → antigravity_target_name. Overrides defaults for matching keys.",
		},
	}
}

func handleRequestInterceptBefore(request []byte) []byte {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return mustErrorEnvelope("invalid_request", fmt.Sprintf("decode request.intercept_before request: %v", err))
	}

	body, rewritten := rewriteRequestBody(req.Body, req.SourceFormat)
	if !rewritten {
		return mustEnvelope(pluginapi.RequestInterceptResponse{})
	}
	return mustEnvelope(pluginapi.RequestInterceptResponse{Body: body})
}

func handleResponseIntercept(request []byte) []byte {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return mustErrorEnvelope("invalid_request", err.Error())
	}

	uncloakTable := buildUncloakTable(req.RequestBody, req.SourceFormat)
	if uncloakTable == nil {
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}

	modified, changed := uncloakResponseBody(req.Body, uncloakTable, req.SourceFormat)
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

	uncloakTable := buildUncloakTable(req.RequestBody, req.SourceFormat)
	if uncloakTable == nil {
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}

	modified, changed := uncloakStreamChunk(req.Body, uncloakTable, req.SourceFormat)
	if !changed {
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	return mustEnvelope(pluginapi.StreamChunkInterceptResponse{Body: modified})
}

func buildUncloakTable(requestBody []byte, sourceFormat string) map[string]string {
	var reqRoot map[string]any
	if err := json.Unmarshal(requestBody, &reqRoot); err != nil {
		return nil
	}
	toolNames := extractToolNames(reqRoot, sourceFormat)
	client := detectClient(toolNames)
	if client == "" || client == "antigravity" {
		return nil
	}
	return effectiveUncloakTable(client)
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
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false
	}

	changed := uncloakJSONNode(root, uncloakTable, sourceFormat)
	if !changed {
		return nil, false
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func uncloakStreamChunk(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
	lines := strings.Split(string(body), "\n")
	changed := false
	for i, line := range lines {
		trimmedLine := strings.TrimRight(line, "\r")
		suffix := line[len(trimmedLine):]

		if !strings.HasPrefix(trimmedLine, "data: ") {
			continue
		}

		dataJSON := strings.TrimPrefix(trimmedLine, "data: ")
		var root any
		if err := json.Unmarshal([]byte(dataJSON), &root); err != nil {
			continue
		}

		if uncloakJSONNode(root, uncloakTable, sourceFormat) {
			newJSON, err := json.Marshal(root)
			if err == nil {
				lines[i] = "data: " + string(newJSON) + suffix
				changed = true
			}
		}
	}

	if !changed {
		return nil, false
	}
	return []byte(strings.Join(lines, "\n")), true
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
									if orig, exists := uncloakTable[name]; exists {
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
									if orig, exists := uncloakTable[name]; exists {
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
					if orig, exists := uncloakTable[name]; exists {
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

var defaultRewriteMappings = []rewriteMapping{
	{Match: "opencode", Replacement: "Antigravity"},
	{Match: "codex", Replacement: "Antigravity"},
	{Match: "claude code", Replacement: "Antigravity"},
}

type rewriteMapping struct {
	Match       string
	Replacement string
}

var defaultCloakTables = map[string]map[string]string{
	"claude_code": {
		"bash": "run_command", "edit": "replace_file_content", "read": "view_file",
		"write": "write_to_file", "grep": "grep_search", "glob": "list_dir",
		"agent": "invoke_subagent", "askUserQuestion": "ask_question",
		"toolSearch": "search_web", "skill": "call_mcp_tool", "workflow": "schedule",
	},
	"codex": {
		"shell_command": "run_command", "apply_patch": "multi_replace_file_content",
		"request_user_input": "ask_question", "view_image": "generate_image",
		"update_plan": "manage_task", "tool_search": "search_web",
		"get_goal": "schedule",
		"create_goal": "send_message",
		"update_goal": "define_subagent",
		"list_mcp_resources": "list_resources",
		"list_mcp_resource_templates": "list_permissions",
		"read_mcp_resource": "read_resource",
	},
}

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
}

var (
	filterConfigMu      sync.RWMutex
	currentFilterConfig = defaultFilterConfig()
)

func defaultFilterConfig() filterConfig {
	return filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	}
}

func applyFilterConfig(cfg filterConfig) {
	filterConfigMu.Lock()
	defer filterConfigMu.Unlock()

	currentFilterConfig = filterConfig{
		UseDefaultKeywords: cfg.UseDefaultKeywords,
		CustomMappings:     append([]rewriteMapping(nil), normalizeMappings(cfg.CustomMappings)...),
		ToolMappings:       copyToolMappings(cfg.ToolMappings),
	}
}

func activeFilterConfig() filterConfig {
	filterConfigMu.RLock()
	defer filterConfigMu.RUnlock()

	return filterConfig{
		UseDefaultKeywords: currentFilterConfig.UseDefaultKeywords,
		CustomMappings:     append([]rewriteMapping(nil), currentFilterConfig.CustomMappings...),
		ToolMappings:       copyToolMappings(currentFilterConfig.ToolMappings),
	}
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
	return cfg, nil
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
		result[client] = clientMap
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

func effectiveMappings(cfg filterConfig) []rewriteMapping {
	mappings := make([]rewriteMapping, 0, len(defaultRewriteMappings)+len(cfg.CustomMappings))
	if cfg.UseDefaultKeywords {
		mappings = append(mappings, defaultRewriteMappings...)
	}
	mappings = append(mappings, cfg.CustomMappings...)
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

func rewriteRequestBody(body []byte, sourceFormat string) ([]byte, bool) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false
	}

	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false
	}

	changed := false

	// 1. Existing brand text replace on "system" key
	mappings := effectiveMappings(activeFilterConfig())
	rewritten, sysChanged := rewriteSystemFields(rootMap, mappings)
	rootMap = rewritten.(map[string]any)
	changed = changed || sysChanged

	// 2. Tool cloaking
	toolNames := extractToolNames(rootMap, sourceFormat)
	client := detectClient(toolNames)
	if client != "" && client != "antigravity" {
		cloakTable := effectiveCloakTable(client)
		if len(cloakTable) > 0 {
			toolCloaked := cloakToolNames(rootMap, cloakTable, sourceFormat)
			changed = changed || toolCloaked
		}
	}

	// 3. Brand replace in tools[].description / tools[].function.description
	descChanged := rewriteToolDescriptions(rootMap, mappings, sourceFormat)
	changed = changed || descChanged

	// 4. Brand replace in messages[].content where role == "system"
	sysMsgChanged := rewriteSystemMessages(rootMap, mappings)
	changed = changed || sysMsgChanged

	if !changed {
		return nil, false
	}
	raw, err := json.Marshal(rootMap)
	if err != nil {
		return nil, false
	}
	return raw, true
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
					if target, exists := cloakTable[name]; exists {
						fn["name"] = target
						changed = true
					}
				}
			} else if sourceFormat == "anthropic" {
				if name, ok := tMap["name"].(string); ok {
					if target, exists := cloakTable[name]; exists {
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
							if target, exists := cloakTable[name]; exists {
								fn["name"] = target
								changed = true
							}
						}
					}
				}
				// tool result message: msg["name"]
				if msg["role"] == "tool" {
					if name, ok := msg["name"].(string); ok {
						if target, exists := cloakTable[name]; exists {
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
								if target, exists := cloakTable[name]; exists {
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
					if target, exists := cloakTable[name]; exists {
						fn["name"] = target
						changed = true
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			// {type: "tool", name: "..."}
			if name, ok := tc["name"].(string); ok {
				if target, exists := cloakTable[name]; exists {
					tc["name"] = target
					changed = true
				}
			}
		}
	}

	return changed
}

func rewriteToolDescriptions(root map[string]any, mappings []rewriteMapping, sourceFormat string) bool {
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
			if descVal, ok := fn["description"].(string); ok {
				next := descVal
				descChanged := false
				for _, mapping := range mappings {
					var replaced bool
					next, replaced = replaceInsensitive(next, mapping.Match, mapping.Replacement)
					descChanged = descChanged || replaced
				}
				if descChanged {
					fn["description"] = next
					changed = true
				}
			}
		} else if sourceFormat == "anthropic" {
			if descVal, ok := tMap["description"].(string); ok {
				next := descVal
				descChanged := false
				for _, mapping := range mappings {
					var replaced bool
					next, replaced = replaceInsensitive(next, mapping.Match, mapping.Replacement)
					descChanged = descChanged || replaced
				}
				if descChanged {
					tMap["description"] = next
					changed = true
				}
			}
		}
	}
	return changed
}

func rewriteSystemMessages(root map[string]any, mappings []rewriteMapping) bool {
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
			next, replaced = replaceInsensitive(next, mapping.Match, mapping.Replacement)
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

func replaceInsensitive(value, match, replacement string) (string, bool) {
	if match == "" {
		return value, false
	}
	lowerValue := strings.ToLower(value)
	lowerMatch := strings.ToLower(match)
	var builder strings.Builder
	start := 0
	changed := false
	for {
		index := strings.Index(lowerValue[start:], lowerMatch)
		if index < 0 {
			break
		}
		index += start
		builder.WriteString(value[start:index])
		builder.WriteString(replacement)
		start = index + len(match)
		changed = true
	}
	if !changed {
		return value, false
	}
	builder.WriteString(value[start:])
	return builder.String(), true
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

func detectClient(toolNames []string) string {
	hasClaude := false
	hasCodex := false
	hasAntigravity := false

	claudeSigs := map[string]bool{"bash": true, "edit": true, "read": true}
	seenClaudeSigs := make(map[string]bool)

	for _, n := range toolNames {
		if n == "askUserQuestion" {
			hasClaude = true
		}
		if claudeSigs[n] {
			seenClaudeSigs[n] = true
		}
		if n == "shell_command" || n == "apply_patch" {
			hasCodex = true
		}
		if n == "ask_permission" || n == "invoke_subagent" {
			hasAntigravity = true
		}
	}

	// Antigravity detection takes priority — skip cloaking entirely
	if hasAntigravity {
		return "antigravity"
	}
	if hasClaude || len(seenClaudeSigs) >= 3 {
		return "claude_code"
	}
	if hasCodex {
		return "codex"
	}
	return ""
}

