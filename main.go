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
	"os"
	"regexp"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func debugLog(format string, args ...any) {
	f, err := os.OpenFile("logs/cpa-filter-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		f, err = os.OpenFile("cpa-filter-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	}
	if err == nil {
		defer f.Close()
		fmt.Fprintf(f, "[DEBUG] "+format+"\n", args...)
	}
}

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

	debugLog("handleRequestInterceptBefore: SourceFormat=%s Body=%s", req.SourceFormat, string(req.Body))
	body, rewritten := rewriteRequestBody(req.Body, req.SourceFormat)
	debugLog("handleRequestInterceptBefore: rewritten=%t Body=%s", rewritten, string(body))
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

	debugLog("handleResponseIntercept: SourceFormat=%s RequestBody=%s Body=%s", req.SourceFormat, string(req.RequestBody), string(req.Body))
	uncloakTable, _ := buildUncloakTable(req.RequestBody, req.SourceFormat)
	debugLog("handleResponseIntercept: uncloakTable=%v", uncloakTable)
	if uncloakTable == nil {
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}

	modified, changed := uncloakResponseBody(req.Body, uncloakTable, req.SourceFormat)
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

	debugLog("handleStreamChunkIntercept: SourceFormat=%s ChunkIndex=%d Body=%s", req.SourceFormat, req.ChunkIndex, string(req.Body))
	_, client := buildUncloakTable(req.RequestBody, req.SourceFormat)
	debugLog("handleStreamChunkIntercept: client=%s", client)
	if client == "" {
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}

	cfg := activeFilterConfig()
	cached := cfg.uncloakRegexCache[client]
	if cached == nil || cached.re == nil {
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}

	// ── SSE Event-Level Reassembly Buffer ──────────────────────────────
	// TCP can split a network chunk at ANY byte boundary, including inside
	// a tool name like "run_command" → Chunk1: "run_c", Chunk2: "ommand".
	// Regex on each chunk alone would miss the match entirely.
	//
	// Solution: Buffer at the SSE EVENT level. SSE events are delimited by
	// "\n\n". Incomplete events (no \n\n terminator) are buffered until the
	// next chunk completes them. Regex only runs on complete events where
	// tool names are guaranteed to be unfragmented.
	//
	// Stream identity: ChunkIndex (provided by the SDK, monotonic per-stream).
	// ChunkIndex == 0 means new stream → reset buffer. No content-based hashing.

	// Reset buffer on new stream
	if req.ChunkIndex == 0 {
		resetStreamBuffer()
	}

	// Retrieve and clear buffered tail from previous chunk
	buffered := popStreamBuffer()

	// Combine buffered tail + current chunk
	var combined []byte
	if len(buffered) > 0 {
		combined = make([]byte, len(buffered)+len(req.Body))
		copy(combined, buffered)
		copy(combined[len(buffered):], req.Body)
	} else {
		combined = req.Body
	}

	// Split into complete SSE events and incomplete tail
	completeEvents, incompleteTail := splitSSEEvents(combined)

	// Buffer the incomplete tail for next chunk
	if len(incompleteTail) > 0 {
		pushStreamBuffer(incompleteTail)
		debugLog("handleStreamChunkIntercept: buffered %d bytes (incomplete event)", len(incompleteTail))
	}

	if len(completeEvents) == 0 {
		// No complete events — entire chunk is buffered.
		// Use DropChunk to suppress this chunk entirely. The buffered bytes
		// will be prepended to the next chunk and delivered then.
		debugLog("handleStreamChunkIntercept: no complete events, dropping chunk")
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{DropChunk: true})
	}

	// Run regex replacement on complete events only
	modified, changed := uncloakStreamChunk(completeEvents, cached)
	debugLog("handleStreamChunkIntercept: changed=%t Body=%s", changed, string(modified))
	if !changed {
		modified = completeEvents
	}

	// If the result equals the original chunk, report no changes
	if bytes.Equal(modified, req.Body) {
		return mustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	return mustEnvelope(pluginapi.StreamChunkInterceptResponse{Body: modified})
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
		// loc[2]:loc[3] is capture group 1 (the tool name)
		toolName := bodyStr[loc[2]:loc[3]]
		if orig, ok := cached.lookup[toolName]; ok {
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

// ── SSE Event Reassembly Buffer ────────────────────────────────────────
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
// Stream identity uses ChunkIndex from the SDK (monotonic per-stream).
// ChunkIndex == 0 signals a new stream → buffer is reset. This avoids the
// architectural anti-pattern of using content hashing as a stream identifier
// (which would cause cross-stream pollution with identical requests).
//
// A single buffer slot is sufficient because the framework dispatches
// stream chunks to plugins sequentially within each stream.

var (
	streamBufMu sync.Mutex
	streamBuf   []byte // single-slot buffer for the active stream's incomplete tail
)

func resetStreamBuffer() {
	streamBufMu.Lock()
	defer streamBufMu.Unlock()
	streamBuf = nil
}

func popStreamBuffer() []byte {
	streamBufMu.Lock()
	defer streamBufMu.Unlock()
	data := streamBuf
	streamBuf = nil
	return data
}

func pushStreamBuffer(data []byte) {
	streamBufMu.Lock()
	defer streamBufMu.Unlock()
	// Copy to avoid retaining references to large chunk slices
	buf := make([]byte, len(data))
	copy(buf, data)
	streamBuf = buf
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
	println("cpa-plugin-antigravity-coding-filter: init start")
	defaultUncloakTables = make(map[string]map[string]string)
	for client, cloaks := range defaultCloakTables {
		uncloaks := make(map[string]string)
		for orig, mapped := range cloaks {
			uncloaks[mapped] = orig
		}
		defaultUncloakTables[client] = uncloaks
	}
	println("cpa-plugin-antigravity-coding-filter: init end")
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

	// Pre-compiled regex patterns, rebuilt on config change.
	cloakRegexCache   map[string]*cachedCloakPatterns  // client → compiled cloak patterns
	uncloakRegexCache map[string]*cachedUncloakPattern // client → compiled uncloak pattern
}

// cachedCloakPatterns holds pre-compiled regexes for tool name replacement
// in descriptions and system messages (request cloaking path).
type cachedCloakPatterns struct {
	cloakTable     map[string]string     // orig → target (for Tier 1 quoted replacement)
	safeRe         *regexp.Regexp        // Tier 2: word-boundary for unambiguous names
	safeLookup     map[string]string     // match → replacement for safe names
	ambiguousRules []cachedAmbiguousRule // Tier 3: pattern-based for short words
}

type cachedAmbiguousRule struct {
	patterns []*regexp.Regexp
	target   string
}

// cachedUncloakPattern holds a pre-compiled regex for stream chunk uncloaking.
// Pattern matches: "name"\s*:\s*"(target1|target2|...)" in raw bytes.
type cachedUncloakPattern struct {
	re     *regexp.Regexp
	lookup map[string]string // matched target → original name
}

var (
	filterConfigMu      sync.RWMutex
	currentFilterConfig = defaultFilterConfig()
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
	filterConfigMu.Lock()
	defer filterConfigMu.Unlock()

	newCfg := filterConfig{
		UseDefaultKeywords: cfg.UseDefaultKeywords,
		CustomMappings:     append([]rewriteMapping(nil), normalizeMappings(cfg.CustomMappings)...),
		ToolMappings:       copyToolMappings(cfg.ToolMappings),
	}
	rebuildCachedRegexes(&newCfg)
	currentFilterConfig = newCfg
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
			safeLookup: make(map[string]string),
		}
		var safeParts []string
		for orig, target := range cloakTable {
			if isUnambiguousToolName(orig) {
				safeParts = append(safeParts, regexp.QuoteMeta(orig))
				cp.safeLookup[orig] = target
			} else {
				qOrig := regexp.QuoteMeta(orig)
				var patterns []*regexp.Regexp
				for _, p := range []string{
					`(?i)(the\s+)` + qOrig + `(\s+(?:tool|function|command)\b)`,
					`(?i)((?:use|call|run|invoke|with)\s+)` + qOrig + `(\b)`,
				} {
					if re, err := regexp.Compile(p); err == nil {
						patterns = append(patterns, re)
					}
				}
				cp.ambiguousRules = append(cp.ambiguousRules, cachedAmbiguousRule{
					patterns: patterns,
					target:   target,
				})
			}
		}
		if len(safeParts) > 0 {
			pattern := `\b(` + strings.Join(safeParts, "|") + `)\b`
			cp.safeRe, _ = regexp.Compile(pattern)
		}
		cfg.cloakRegexCache[client] = cp

		// Build uncloak pattern (for stream path: regex-based tool name restore)
		targets := make([]string, 0, len(cloakTable))
		lookup := make(map[string]string, len(cloakTable))
		for orig, target := range cloakTable {
			targets = append(targets, regexp.QuoteMeta(target))
			lookup[target] = orig
		}
		if len(targets) > 0 {
			// Match "name" : "<target>" with flexible whitespace
			pattern := `"name"\s*:\s*"(` + strings.Join(targets, "|") + `)"`
			if re, err := regexp.Compile(pattern); err == nil {
				cfg.uncloakRegexCache[client] = &cachedUncloakPattern{
					re:     re,
					lookup: lookup,
				}
			}
		}
	}
}

func activeFilterConfig() filterConfig {
	filterConfigMu.RLock()
	defer filterConfigMu.RUnlock()

	return filterConfig{
		UseDefaultKeywords: currentFilterConfig.UseDefaultKeywords,
		CustomMappings:     append([]rewriteMapping(nil), currentFilterConfig.CustomMappings...),
		ToolMappings:       copyToolMappings(currentFilterConfig.ToolMappings),
		cloakRegexCache:    currentFilterConfig.cloakRegexCache,
		uncloakRegexCache:  currentFilterConfig.uncloakRegexCache,
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
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}

	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false
	}

	changed := false

	// 1. Existing brand text replace on "system" key
	cfg := activeFilterConfig()
	mappings := effectiveMappings(cfg)
	rewritten, sysChanged := rewriteSystemFields(rootMap, mappings)
	rootMap = rewritten.(map[string]any)
	changed = changed || sysChanged

	// 2. Tool cloaking
	toolNames := extractToolNames(rootMap, sourceFormat)
	client := detectClient(toolNames)
	var cachedCloak *cachedCloakPatterns
	if client != "" {
		cloakTable := effectiveCloakTable(client)
		if len(cloakTable) > 0 {
			toolCloaked := cloakToolNames(rootMap, cloakTable, sourceFormat)
			changed = changed || toolCloaked
		}
		cachedCloak = cfg.cloakRegexCache[client]
	}

	// 3. Brand replace + tool name replace in tools[].description
	descChanged := rewriteToolDescriptions(rootMap, mappings, cachedCloak, sourceFormat)
	changed = changed || descChanged

	// 4. Brand replace + tool name replace in messages[].content where role == "system"
	sysMsgChanged := rewriteSystemMessages(rootMap, mappings, cachedCloak)
	changed = changed || sysMsgChanged

	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(rootMap)
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
			if descVal, ok := fn["description"].(string); ok {
				next := descVal
				descChanged := false
				for _, mapping := range mappings {
					var replaced bool
					next, replaced = replaceInsensitive(next, mapping.Match, mapping.Replacement)
					descChanged = descChanged || replaced
				}
				if cached != nil {
					var toolReplaced bool
					next, toolReplaced = replaceToolNamesInText(next, cached)
					descChanged = descChanged || toolReplaced
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
				if cached != nil {
					var toolReplaced bool
					next, toolReplaced = replaceToolNamesInText(next, cached)
					descChanged = descChanged || toolReplaced
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

// replaceToolNamesInText uses pre-compiled regex patterns from cachedCloakPatterns.
// Patterns are compiled once on config change (rebuildCachedRegexes), not per call.
func replaceToolNamesInText(text string, cached *cachedCloakPatterns) (string, bool) {
	if cached == nil || len(cached.cloakTable) == 0 {
		return text, false
	}

	result := text
	changed := false

	// Tier 1: Quoted replacement for ALL names — backticks and double-quotes
	// are strong signals of a tool name reference regardless of word length.
	for orig, target := range cached.cloakTable {
		for _, q := range []string{"`", `"`} {
			old := q + orig + q
			repl := q + target + q
			if strings.Contains(result, old) {
				result = strings.ReplaceAll(result, old, repl)
				changed = true
			}
		}
	}

	// Tier 2: Word-boundary replacement for unambiguous names (pre-compiled)
	if cached.safeRe != nil {
		newResult := cached.safeRe.ReplaceAllStringFunc(result, func(match string) string {
			if target, ok := cached.safeLookup[match]; ok {
				return target
			}
			return match
		})
		if newResult != result {
			result = newResult
			changed = true
		}
	}

	// Tier 3: Pattern-based replacement for ambiguous names (pre-compiled)
	for _, rule := range cached.ambiguousRules {
		for _, re := range rule.patterns {
			newResult := re.ReplaceAllString(result, "${1}"+rule.target+"${2}")
			if newResult != result {
				result = newResult
				changed = true
			}
		}
	}

	return result, changed
}

// isUnambiguousToolName returns true if a tool name is specific enough for
// safe word-boundary replacement. Names with underscores or camelCase are
// identifiers, not common English words.
func isUnambiguousToolName(name string) bool {
	if strings.Contains(name, "_") {
		return true
	}
	for _, r := range name {
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
	nameSet := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		nameSet[n] = true
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
		if count > bestCount {
			bestCount = count
			bestClient = client
		}
	}

	if bestCount >= 2 {
		return bestClient
	}
	return ""
}

// detectCloakedClient identifies which client's cloaking was applied by
// checking cloak TARGET names against the provided tool names.
// If exactly one client's targets match at >=80%, that client was cloaked.
// If multiple clients match (native Antigravity has all targets), returns "".
func detectCloakedClient(toolNames []string) string {
	cfg := activeFilterConfig()
	nameSet := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		nameSet[n] = true
	}

	type clientMatch struct {
		client string
		hits   int
	}
	var matches []clientMatch
	for client, cloakTable := range cfg.ToolMappings {
		if len(cloakTable) == 0 {
			continue
		}
		hits := 0
		for _, target := range cloakTable {
			if nameSet[target] {
				hits++
			}
		}
		// 80% threshold: most of the client's cloak targets are present
		if hits*5 >= len(cloakTable)*4 {
			matches = append(matches, clientMatch{client, hits})
		}
	}

	// Exactly one client's cloak targets match → that client was cloaked
	// Multiple matches → likely native Antigravity (superset of all cloak targets)
	if len(matches) == 1 {
		return matches[0].client
	}
	return ""
}

