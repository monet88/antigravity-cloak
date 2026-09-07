package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Helper to make integration request payloads with arbitrary headers.
func makeProtectedIntegrationRequest(t *testing.T, reqID, format, model string, body []byte, headers http.Header) []byte {
	t.Helper()
	req := pluginapi.RequestInterceptRequest{
		RequestID:    reqID,
		SourceFormat: format,
		Model:        model,
		Body:         body,
		Headers:      headers,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return b
}

func decodeProtectedRequestIntercept(t *testing.T, raw []byte) (pluginapi.RequestInterceptResponse, error) {
	t.Helper()
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Error != nil {
		return pluginapi.RequestInterceptResponse{}, fmt.Errorf("envelope error: %s - %s", env.Error.Code, env.Error.Message)
	}
	var resp pluginapi.RequestInterceptResponse
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &resp); err != nil {
			t.Fatalf("unmarshal result to RequestInterceptResponse: %v", err)
		}
	}
	return resp, nil
}

func assertExact503Rejection(t *testing.T, resp pluginapi.RequestInterceptResponse, caseName string) {
	t.Helper()
	if !resp.Terminate {
		t.Fatalf("[%s] expected Terminate=true, got false", caseName)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("[%s] expected StatusCode=503, got %d", caseName, resp.StatusCode)
	}
	if ct := resp.ResponseHeaders.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("[%s] expected Content-Type application/json, got %q", caseName, ct)
	}
	expectedBody := `{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}`
	if string(resp.ResponseBody) != expectedBody {
		t.Fatalf("[%s] expected exact ResponseBody %s, got: %s", caseName, expectedBody, string(resp.ResponseBody))
	}
}

// 1. Protected routing must still engage when model_prefixes would otherwise skip cloaking.
func TestIssue27_ProtectedRoutingBypassesModelPrefixesSkip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Restrict model_prefixes to non-agy models only
	cfg := activeFilterConfig()
	cfgCopy := *cfg
	cfgCopy.ModelPrefixes = []string{"openai/", "other-provider/"}
	applyFilterConfig(cfgCopy)

	agyModel := "agy/gemini-2.5-flash"
	reqID := "req-prefix-bypass-1"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqBody := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"messages":[{"role":"user","content":"run bash"}],
		"tools":[{"type":"function","function":{"name":"bash"}}]
	}`)

	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, reqBody, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("protected request must be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if !strings.Contains(string(resp.Body), `"name":"run_command"`) {
		t.Fatalf("protected request must be cloaked even when model_prefixes does not match, got: %s", string(resp.Body))
	}

	// Correlated response uncloak
	upstreamResp := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", agyModel, upstreamResp))
	outBody := string(decodeEnvelopeBody(t, rawResp))
	if !strings.Contains(outBody, `"name":"bash"`) {
		t.Fatalf("correlated response must be uncloaked regardless of model_prefixes, got: %s", outBody)
	}

	// Correlated stream uncloak
	chunkReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        agyModel,
		Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n"),
	}
	rawChunk, _ := json.Marshal(chunkReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
	chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	if !strings.Contains(string(chunkBody), `"name":"bash"`) {
		t.Fatalf("correlated stream chunk must be uncloaked regardless of model_prefixes, got: %s", string(chunkBody))
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
}

// 2. Exact Protected rejection assertions: 503, Content-Type, exact body, covering all strict cases.
func TestIssue27_Exact503Rejections(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	cases := []struct {
		name      string
		reqID     string
		format    string
		body      []byte
		headers   http.Header
	}{
		{
			name:   "truncated JSON",
			reqID:  "req-rej-trunc",
			format: "openai",
			body:   []byte(`{"model":"agy/gemini-2.5-flash","messages":`),
		},
		{
			name:   "trailing non-whitespace garbage",
			reqID:  "req-rej-garbage",
			format: "openai",
			body:   []byte(`{"model":"agy/gemini-2.5-flash","messages":[]} trailing_garbage`),
		},
		{
			name:   "multiple JSON documents",
			reqID:  "req-rej-multi",
			format: "openai",
			body:   []byte(`{"model":"agy/gemini-2.5-flash","messages":[]}{"extra":"document"}`),
		},
		{
			name:   "non-object JSON root",
			reqID:  "req-rej-array",
			format: "openai",
			body:   []byte(`["item1", "item2"]`),
		},
		{
			name:   "empty body",
			reqID:  "req-rej-empty",
			format: "openai",
			body:   []byte{},
		},
		{
			name:   "unsupported normalized SourceFormat",
			reqID:  "req-rej-format",
			format: "grpc-proto-unsupported",
			body:   []byte(`{"model":"agy/gemini-2.5-flash","messages":[]}`),
		},
		{
			name:   "missing RequestID",
			reqID:  "",
			format: "openai",
			body:   []byte(`{"model":"agy/gemini-2.5-flash","messages":[]}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeProtectedIntegrationRequest(t, tc.reqID, tc.format, model, tc.body, headers))
			resp, _ := decodeProtectedRequestIntercept(t, raw)
			assertExact503Rejection(t, resp, tc.name)
		})
	}
}

// 3. Canonical serialization for semantic no-op / pass-through-only Protected requests,
// plus duplicate-key parser-divergence regressions for tools, messages, and tool_choice.
func TestIssue27_CanonicalSerializationAndDuplicateKeys(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// 3a. Semantic no-op / pass-through-only request with non-canonical raw formatting
	rawNoOp := []byte("{\n  \"model\": \"agy/gemini-2.5-flash\",\n  \"tools\": [\n    {\n      \"type\": \"function\",\n      \"function\": {\n        \"name\": \"custom_passthrough\"\n      }\n    }\n  ],\n  \"messages\": [\n    {\"role\": \"user\", \"content\": \"hello\"}\n  ]\n}")
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-noop-1", "openai", model, rawNoOp, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("pass-through request should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if len(resp.Body) == 0 {
		t.Fatalf("admitted Protected request must have non-empty serialized body")
	}
	if bytes.Equal(resp.Body, rawNoOp) {
		t.Fatalf("must forward canonical serialization, not original raw bytes")
	}
	var parsedNoOp map[string]any
	if err := json.Unmarshal(resp.Body, &parsedNoOp); err != nil {
		t.Fatalf("canonical body must be valid JSON: %v", err)
	}

	// 3b. Duplicate top-level tools keys
	rawDupTools := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"tools":[{"type":"function","function":{"name":"first_tool"}}],
		"tools":[{"type":"function","function":{"name":"second_tool"}}],
		"messages":[]
	}`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-dup-tools", "openai", model, rawDupTools, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("duplicate tools request should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if bytes.Equal(resp.Body, rawDupTools) {
		t.Fatalf("must forward collapsed canonical serialization, not duplicate raw bytes")
	}
	if strings.Contains(string(resp.Body), "first_tool") {
		t.Fatalf("decoder collapsed duplicate tools key, first_tool must not appear: %s", string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "second_tool") {
		t.Fatalf("second_tool must appear in canonical body: %s", string(resp.Body))
	}

	// 3c. Duplicate top-level messages keys
	rawDupMessages := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"messages":[{"role":"user","content":"first_msg"}],
		"messages":[{"role":"user","content":"second_msg"}]
	}`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-dup-msgs", "openai", model, rawDupMessages, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("duplicate messages request should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if strings.Contains(string(resp.Body), "first_msg") {
		t.Fatalf("decoder collapsed duplicate messages key, first_msg must not appear: %s", string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "second_msg") {
		t.Fatalf("second_msg must appear in canonical body: %s", string(resp.Body))
	}

	// 3d. Duplicate top-level tool_choice keys
	rawDupChoice := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"tool_choice":"auto",
		"tool_choice":"none",
		"messages":[]
	}`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-dup-choice", "openai", model, rawDupChoice, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("duplicate tool_choice request should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if strings.Contains(string(resp.Body), `"auto"`) {
		t.Fatalf("decoder collapsed duplicate tool_choice key, auto must not appear: %s", string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), `"none"`) {
		t.Fatalf("none must appear in canonical body: %s", string(resp.Body))
	}

	// 3e. Duplicate top-level n keys (choice count): canonicalization and pinned route alignment
	rawDupN := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"n":1,
		"n":3,
		"messages":[]
	}`)
	reqIDDupN := "req-dup-n"
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqIDDupN, "openai", model, rawDupN, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("duplicate n request should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if bytes.Equal(resp.Body, rawDupN) {
		t.Fatalf("must forward collapsed canonical serialization, not duplicate raw bytes")
	}
	var parsedDupN map[string]any
	if err := safeUnmarshal(resp.Body, &parsedDupN); err != nil {
		t.Fatalf("unmarshal canonical body: %v", err)
	}
	canonicalN, ok := jsonIndexValue(parsedDupN["n"])
	if !ok || canonicalN != 3 {
		t.Fatalf("expected canonical body to have n=3, got %v", parsedDupN["n"])
	}
	route := globalLifecycleManager.getRoute(reqIDDupN)
	if route == nil {
		t.Fatalf("expected pinned route for %s, got nil", reqIDDupN)
	}
	if route.expected != 3 {
		t.Fatalf("expected route.expected == 3 matching canonical object, got %d", route.expected)
	}
	if route.expected != requestChoiceCount(resp.Body) {
		t.Fatalf("route.expected (%d) does not match requestChoiceCount(resp.Body) (%d)", route.expected, requestChoiceCount(resp.Body))
	}
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqIDDupN, "succeeded"))
}

// 4. Config drift rejection covering add, remove, and override of canonical OMP mapping table.
func TestIssue27_ConfigDriftRejection_AddRemoveOverride(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")
	validBody := []byte(`{"model":"agy/gemini-2.5-flash","messages":[],"tools":[{"type":"function","function":{"name":"bash"}}]}`)

	// 4a. Add
	t.Run("add mapping", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		cfg := activeFilterConfig()
		cfgCopy := *cfg
		cfgCopy.ToolMappings = copyToolMappings(cfg.ToolMappings)
		cfgCopy.ToolMappings["oh_my_pi"]["added_tool"] = "added_target"
		applyFilterConfig(cfgCopy)

		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, "req-drift-add", "openai", agyModel, validBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, raw)
		assertExact503Rejection(t, resp, "add mapping drift")
	})

	// 4b. Remove
	t.Run("remove mapping", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		cfg := activeFilterConfig()
		cfgCopy := *cfg
		cfgCopy.ToolMappings = copyToolMappings(cfg.ToolMappings)
		delete(cfgCopy.ToolMappings["oh_my_pi"], "bash")
		applyFilterConfig(cfgCopy)

		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, "req-drift-remove", "openai", agyModel, validBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, raw)
		assertExact503Rejection(t, resp, "remove mapping drift")
	})

	// 4c. Override
	t.Run("override mapping", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		cfg := activeFilterConfig()
		cfgCopy := *cfg
		cfgCopy.ToolMappings = copyToolMappings(cfg.ToolMappings)
		cfgCopy.ToolMappings["oh_my_pi"]["bash"] = "custom_override_target"
		applyFilterConfig(cfgCopy)

		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, "req-drift-override", "openai", agyModel, validBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, raw)
		assertExact503Rejection(t, resp, "override mapping drift")
	})
}

// 5. Conflict containing no OMP retains existing invalid-explicit/authoritative-negative behavior.
func TestIssue27_ConflictContainingNoOMP(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	reqID := "req-no-omp-conflict-1"
	headers := http.Header{}
	// Conflict between two non-OMP clients
	headers.Set("X-Cloak-Client", "claude_code, codex")
	// Add UA evidence that would otherwise match if not suppressed by authoritative-negative
	headers.Set("User-Agent", "Claude-Code/1.0")

	body := []byte(`{"model":"agy/gemini-2.5-flash","messages":[{"role":"user","content":"test"}]}`)
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, body, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)

	// Must NOT return 503 (it is not a Protected OMP conflict)
	if resp.Terminate {
		t.Fatalf("non-OMP conflict must not return 503 terminate")
	}

	// Must NOT create Protected or bypass state
	if r := globalLifecycleManager.getRoute(reqID); r != nil {
		t.Fatalf("non-OMP conflict must NOT create lifecycle route, got: %+v", r)
	}

	// Must record authoritative negative client resolution
	if c := globalStreamManager.getClient("req:" + reqID); c != negativeClientResolution {
		t.Fatalf("expected negative client resolution %q, got: %q", negativeClientResolution, c)
	}

	// Correlated response must not cloak/uncloak or restore brand
	respBody := []byte(`{"choices":[{"message":{"content":"Welcome to Antigravity runtime."}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", agyModel, respBody))
	outBody := decodeEnvelopeBody(t, rawResp)
	if len(outBody) != 0 {
		t.Fatalf("correlated response for authoritative negative must perform zero mutation, got: %s", string(outBody))
	}
}

// 6. Non-AGY bypass: non-empty RequestID pins bypass sentinel, response and stream zero mutation,
// brand restoration disabled; empty RequestID does not invent 503.
func TestIssue27_NonAGYBypassDetailed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	nonAGYModel := "openai/gpt-4o"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqID := "req-bypass-detailed-1"
	body := []byte(`{
		"model":"openai/gpt-4o",
		"system":"Running Oh My Pi agent",
		"messages":[{"role":"user","content":"run bash"}],
		"tools":[{"type":"function","function":{"name":"bash"}}]
	}`)

	// 6a. Non-empty RequestID pins bypass sentinel
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", nonAGYModel, body, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("non-AGY request must not terminate, got 503: %s", string(resp.ResponseBody))
	}
	if len(resp.Body) != 0 {
		t.Fatalf("non-AGY bypass request must have zero mutation, got: %s", string(resp.Body))
	}

	route := globalLifecycleManager.getRoute(reqID)
	if route == nil || route.routeKind != routeKindExplicitOMPNonAGYBypass {
		t.Fatalf("expected routeKindExplicitOMPNonAGYBypass, got: %+v", route)
	}
	if route.brandRestorationEnabled {
		t.Fatalf("brandRestorationEnabled must be false for non-AGY bypass")
	}

	// 6b. Response intercept has zero mutation and brand restoration is disabled
	upstreamResp := []byte(`{"choices":[{"message":{"content":"Welcome to Antigravity runtime.","tool_calls":[{"function":{"name":"run_command"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", nonAGYModel, upstreamResp))
	outBody := decodeEnvelopeBody(t, rawResp)
	if len(outBody) != 0 {
		t.Fatalf("correlated response for non-AGY bypass must be zero mutation, got: %s", string(outBody))
	}

	// 6c. Stream intercept has zero mutation and brand restoration is disabled
	streamReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        nonAGYModel,
		Body:         []byte("data: " + `{"choices":[{"delta":{"content":"Antigravity"}}]}` + "\n\n"),
	}
	rawChunk, _ := json.Marshal(streamReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
	chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	if len(chunkBody) != 0 {
		t.Fatalf("correlated stream chunk for non-AGY bypass must be zero mutation, got: %s", string(chunkBody))
	}

	// 6d. Empty RequestID forwards with zero mutation, does NOT invent 503
	rawEmptyID, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "", "openai", nonAGYModel, body, headers))
	respEmptyID, _ := decodeProtectedRequestIntercept(t, rawEmptyID)
	if respEmptyID.Terminate {
		t.Fatalf("empty-ID non-AGY request must not terminate with 503, got: %s", string(respEmptyID.ResponseBody))
	}
	if len(respEmptyID.Body) != 0 {
		t.Fatalf("empty-ID non-AGY request must have zero mutation, got: %s", string(respEmptyID.Body))
	}
}

// 7. Protected active reverse: unqualified active pair does not authorize qualified output;
// same-namespace, cross-namespace, and collision-safe two-active-namespace coverage.
func TestIssue27_ActiveReverse_UnqualifiedDoesNotAuthorizeQualified(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// 7a. Request has unqualified tool bash -> transformed to run_command
	reqID := "req-unqual-reverse-1"
	reqBody := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"messages":[],
		"tools":[{"type":"function","function":{"name":"bash"}}]
	}`)
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, reqBody, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("admission failed: %s", string(resp.ResponseBody))
	}

	// Upstream returns unqualified run_command, plus qualified functions:run_command and default_api:run_command
	upstreamResp := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[
					{"function":{"name":"run_command","arguments":"{}"}},
					{"function":{"name":"functions:run_command","arguments":"{}"}},
					{"function":{"name":"default_api:run_command","arguments":"{}"}}
				]
			}
		}]
	}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", agyModel, upstreamResp))
	outBody := string(decodeEnvelopeBody(t, rawResp))

	// Unqualified run_command MUST be reversed to bash
	if !strings.Contains(outBody, `"name":"bash"`) {
		t.Fatalf("expected unqualified run_command -> bash, got: %s", outBody)
	}
	// Qualified variants MUST NOT be reversed
	if !strings.Contains(outBody, `"name":"functions:run_command"`) {
		t.Fatalf("functions:run_command must NOT be reversed by unqualified pair, got: %s", outBody)
	}
	if !strings.Contains(outBody, `"name":"default_api:run_command"`) {
		t.Fatalf("default_api:run_command must NOT be reversed by unqualified pair, got: %s", outBody)
	}

	// 7b. Same assertion for stream chunks
	chunkReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        agyModel,
		Body: []byte("data: " + `{
			"choices":[{
				"delta":{
					"tool_calls":[
						{"function":{"name":"run_command"}},
						{"function":{"name":"functions:run_command"}},
						{"function":{"name":"default_api:run_command"}}
					]
				}
			}]
		}` + "\n\n"),
	}
	rawChunk, _ := json.Marshal(chunkReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
	chunkBodyBytes, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	chunkBody := string(chunkBodyBytes)

	if !strings.Contains(chunkBody, `"name":"bash"`) {
		t.Fatalf("stream: expected unqualified run_command -> bash, got: %s", chunkBody)
	}
	if !strings.Contains(chunkBody, `"name":"functions:run_command"`) {
		t.Fatalf("stream: functions:run_command must NOT be reversed by unqualified pair, got: %s", chunkBody)
	}
	if !strings.Contains(chunkBody, `"name":"default_api:run_command"`) {
		t.Fatalf("stream: default_api:run_command must NOT be reversed by unqualified pair, got: %s", chunkBody)
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
}

// 8. Protected brand restoration when request-side alias masking was a semantic no-op / pass-through-only,
// in both non-stream and stream paths.
func TestIssue27_BrandRestorationOnSemanticNoOpRequest(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// Request has NO brand keywords to mask (no Oh My Pi / omp), only normal text
	reqID := "req-brand-noop-1"
	body := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"system":"You are a coding assistant.",
		"messages":[{"role":"user","content":"Hello world"}]
	}`)

	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, body, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("admission failed: %s", string(resp.ResponseBody))
	}

	// 8a. Non-stream response contains "Antigravity"
	upstreamResp := []byte(`{"choices":[{"message":{"content":"Welcome to Antigravity runtime."}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", agyModel, upstreamResp))
	outBody := string(decodeEnvelopeBody(t, rawResp))
	if !strings.Contains(outBody, "Welcome to omp runtime.") && !strings.Contains(outBody, "Welcome to Oh My Pi runtime.") {
		t.Fatalf("brand restoration must run on response for semantic no-op request, got: %s", outBody)
	}

	// 8b. Stream response contains "Antigravity"
	streamReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        agyModel,
		Body:         []byte("data: " + `{"choices":[{"delta":{"content":"Welcome to Antigravity runtime."}}]}` + "\n\n"),
	}
	rawChunk, _ := json.Marshal(streamReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
	chunkBodyBytes, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	chunkBody := string(chunkBodyBytes)
	if !strings.Contains(chunkBody, "Welcome to omp runtime.") && !strings.Contains(chunkBody, "Welcome to Oh My Pi runtime.") {
		t.Fatalf("brand restoration must run on stream for semantic no-op request, got: %s", chunkBody)
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
}

// 9. Stream lifecycle hardening:
//  1) delete pre-payload session, change Config A -> B, prove rehydration uses pinned Config-A authority;
//  2) make Protected session stale AFTER payload starts, prove generic TTL cleanup does not evict it;
//  3) inject loss with incomplete SSE tail / brand carry, prove invariant handling with zero fallback;
//  4) complete stream cleanly, prove durable route remains while disposable state gone, late payload rejected;
//  5) request.complete cleans both durable route and residual disposable session, idempotently.
func TestIssue27_StreamLifecycle_HardenedRequirements(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// 9.1: Delete pre-payload disposable session, change Config A -> Config B, prove rehydration uses pinned Config A
	t.Run("rehydration uses pinned Config A authority despite config drift", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reqID := "req-life-cfg-drift-1"
		reqBody := []byte(`{
			"model":"agy/gemini-2.5-flash",
			"messages":[],
			"tools":[{"type":"function","function":{"name":"bash"}}]
		}`)

		// Admitted under Config A
		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, raw)
		if resp.Terminate {
			t.Fatalf("admission failed: %s", string(resp.ResponseBody))
		}

		// Delete disposable stream session
		globalStreamManager.deleteSession("req:" + reqID)

		// Change live config to Config B (e.g. mutate oh_my_pi mapping table and model_prefixes)
		cfg := activeFilterConfig()
		cfgCopy := *cfg
		cfgCopy.ToolMappings = copyToolMappings(cfg.ToolMappings)
		cfgCopy.ToolMappings["oh_my_pi"]["bash"] = "drifted_target"
		cfgCopy.ModelPrefixes = []string{"other-model/"}
		applyFilterConfig(cfgCopy)

		// Send payload chunk 0
		chunkReq := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n"),
		}
		rawChunk, _ := json.Marshal(chunkReq)
		rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
		chunkBody, _ := decodeEnvelopeStreamChunk(t, rawResp)

		// Must be uncloaked using pinned Config A authority
		if !strings.Contains(string(chunkBody), `"name":"bash"`) {
			t.Fatalf("rehydrated chunk must use pinned Config A authority, got: %s", string(chunkBody))
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	})

	// 9.2: Make Protected session stale AFTER payload starts, prove generic TTL cleanup does not evict it
	t.Run("stale session after payload started is protected from TTL cleanup", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reqID := "req-life-ttl-protect-1"
		reqBody := []byte(`{
			"model":"agy/gemini-2.5-flash",
			"messages":[],
			"tools":[{"type":"function","function":{"name":"bash"}}]
		}`)

		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, reqBody, headers))
		decodeProtectedRequestIntercept(t, raw)

		// Send chunk 0 to transition disposition to streamDispositionPayloadActive
		chunkReq := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n"),
		}
		rawChunk, _ := json.Marshal(chunkReq)
		handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)

		// Make the session stale (older than 5 minutes)
		globalStreamManager.mu.Lock()
		sess := globalStreamManager.sessions["req:"+reqID]
		if sess == nil {
			globalStreamManager.mu.Unlock()
			t.Fatalf("expected stream session to exist")
		}
		sess.updatedAt = time.Now().Add(-10 * time.Minute)
		// Run cleanupStaleLocked while under lock
		globalStreamManager.cleanupStaleLocked()
		survivingSess := globalStreamManager.sessions["req:"+reqID]
		globalStreamManager.mu.Unlock()

		if survivingSess == nil {
			t.Fatalf("Protected session with active payload must NOT be evicted by cleanupStaleLocked")
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	})

	// 9.3: Inject loss with incomplete SSE tail and brand carry, prove invariant handling with zero fallback
	t.Run("invariant loss handling with zero fallback", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reqID := "req-life-loss-1"
		reqBody := []byte(`{
			"model":"agy/gemini-2.5-flash",
			"messages":[],
			"tools":[{"type":"function","function":{"name":"bash"}}]
		}`)

		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, reqBody, headers))
		decodeProtectedRequestIntercept(t, raw)

		// Chunk 0: starts payload
		chunk0 := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n"),
		}
		rawChunk0, _ := json.Marshal(chunk0)
		handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk0)

		// Evict session while payload is active
		globalStreamManager.deleteSession("req:" + reqID)

		// Next chunk arrives: missing mutable state must be invariant violation -> empty response, zero fallback
		chunk1 := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   1,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{}"}}]}}]}` + "\n\n"),
		}
		rawChunk1, _ := json.Marshal(chunk1)
		rawResp1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk1)
		body1, _ := decodeEnvelopeStreamChunk(t, rawResp1)
		if len(body1) != 0 {
			t.Fatalf("missing session after payload started must produce zero-mutation empty response, got: %s", string(body1))
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "failed"))
	})

	// 9.4: Clean stream completion leaves durable route, removes disposable session; late payload rejected
	t.Run("clean stream completion and late payload rejection", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reqID := "req-life-clean-term-1"
		reqBody := []byte(`{
			"model":"agy/gemini-2.5-flash",
			"messages":[],
			"tools":[{"type":"function","function":{"name":"bash"}}]
		}`)

		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, reqBody, headers))
		decodeProtectedRequestIntercept(t, raw)

		// Chunk 0: payload
		chunk0 := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: " + `{"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"),
		}
		rawChunk0, _ := json.Marshal(chunk0)
		handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk0)

		// Chunk 1: terminal [DONE]
		chunkDone := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   1,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: [DONE]\n\n"),
		}
		rawChunkDone, _ := json.Marshal(chunkDone)
		handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkDone)

		// Assert: disposable session is deleted
		globalStreamManager.mu.Lock()
		sess := globalStreamManager.sessions["req:"+reqID]
		globalStreamManager.mu.Unlock()
		if sess != nil {
			t.Fatalf("disposable stream session must be deleted after [DONE]")
		}

		// Assert: durable route remains
		route := globalLifecycleManager.getRoute(reqID)
		if route == nil {
			t.Fatalf("durable route must survive [DONE]")
		}
		if route.getDisposition() != streamDispositionCleanTerminal {
			t.Fatalf("expected streamDispositionCleanTerminal, got: %v", route.getDisposition())
		}

		// Send late payload chunk
		lateChunk := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   2,
			SourceFormat: "openai",
			Model:        agyModel,
			Body:         []byte("data: " + `{"choices":[{"delta":{"content":"late"}}]}` + "\n\n"),
		}
		rawLate, _ := json.Marshal(lateChunk)
		rawLateResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawLate)
		lateBody, _ := decodeEnvelopeStreamChunk(t, rawLateResp)
		if len(lateBody) != 0 {
			t.Fatalf("late chunk after clean terminal must produce empty response, got: %s", string(lateBody))
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	})

	// 9.5: request.complete cleans both durable route and residual disposable session, idempotently
	t.Run("request.complete cleans both durable route and disposable session idempotently", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		outcomes := []string{"succeeded", "failed", "canceled", "rejected"}
		for _, outcome := range outcomes {
			reqID := "req-life-complete-" + outcome
			globalLifecycleManager.setRoute(reqID, &explicitOMPRouteState{
				routeKind: routeKindProtectedAGY,
				client:    "oh_my_pi",
			})
			globalStreamManager.resetSession("req:"+reqID, "oh_my_pi", nil, 1)

			payload := makeRequestCompletePayload(t, reqID, outcome)
			res1, code1 := handlePluginCall(pluginabi.MethodRequestComplete, payload)
			if code1 != 0 {
				t.Fatalf("request.complete failed: %s", string(res1))
			}

			if r := globalLifecycleManager.getRoute(reqID); r != nil {
				t.Fatalf("durable route must be deleted by request.complete")
			}
			globalStreamManager.mu.Lock()
			s := globalStreamManager.sessions["req:"+reqID]
			globalStreamManager.mu.Unlock()
			if s != nil {
				t.Fatalf("residual stream session must be deleted by request.complete")
			}

			// Idempotent second call
			res2, code2 := handlePluginCall(pluginabi.MethodRequestComplete, payload)
			if code2 != 0 {
				t.Fatalf("second idempotent request.complete call failed: %s", string(res2))
			}
		}
	})
}

// 10. Malformed/corrupted Protected post-upstream route state must remain a no-weaker-fallback
// invariant path; do not claim or synthesize the pre-upstream exact 503 there.
func TestIssue27_MalformedPostUpstreamRouteState_NoWeakerFallback(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	reqID := "req-malformed-route-1"

	// Inject malformed ProtectedAGY route
	globalLifecycleManager.setRoute(reqID, &explicitOMPRouteState{
		routeKind: routeKindProtectedAGY,
		client:    "oh_my_pi",
		malformed: true,
	})

	// 10a. Non-stream response intercept
	respBody := []byte(`{"choices":[{"message":{"content":"Welcome to Antigravity runtime.","tool_calls":[{"function":{"name":"run_command"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", agyModel, respBody))
	outBody := decodeEnvelopeBody(t, rawResp)
	// Must return empty body (zero mutation, no weaker fallback, no 503 synthesized)
	if len(outBody) != 0 {
		t.Fatalf("malformed post-upstream state must not uncloak or restore brand, got: %s", string(outBody))
	}

	// 10b. Stream chunk intercept
	streamReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        agyModel,
		Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n"),
	}
	rawChunk, _ := json.Marshal(streamReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
	chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	if len(chunkBody) != 0 {
		t.Fatalf("stream: malformed post-upstream state must produce empty response, got: %s", string(chunkBody))
	}

	// 10c. Corrupted client identifier in route
	reqID2 := "req-corrupted-client-1"
	globalLifecycleManager.setRoute(reqID2, &explicitOMPRouteState{
		routeKind: routeKindProtectedAGY,
		client:    "corrupted_non_omp_client",
	})
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID2, "openai", agyModel, respBody))
	outBody2 := decodeEnvelopeBody(t, rawResp2)
	if len(outBody2) != 0 {
		t.Fatalf("corrupted client identifier must produce empty response, got: %s", string(outBody2))
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "failed"))
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID2, "failed"))
}

// Concurrency race check: execute concurrent calls on streamDisposition and lifecycle methods
func TestIssue27_ConcurrencySafeDisposition(t *testing.T) {
	state := &explicitOMPRouteState{
		routeKind: routeKindProtectedAGY,
		client:    "oh_my_pi",
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = state.getDisposition()
				if j%2 == 0 {
					state.setDisposition(streamDispositionPayloadActive)
				} else {
					state.setDisposition(streamDispositionCleanTerminal)
				}
			}
		}(i)
	}
	wg.Wait()

	if state.getDisposition() != streamDispositionCleanTerminal {
		t.Fatalf("final disposition must be streamDispositionCleanTerminal, got: %v", state.getDisposition())
	}
}

func makeRequestCompletePayload(t *testing.T, reqID, outcome string) []byte {
	t.Helper()
	c := pluginapi.RequestCompletion{
		RequestID: reqID,
		Outcome:   pluginapi.RequestCompletionOutcome(outcome),
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal completion: %v", err)
	}
	return b
}
