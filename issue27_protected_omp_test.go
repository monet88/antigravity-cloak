package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

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

// 1. Strict JSON / Canonical serialization
func TestIssue27_StrictJSON_Admission(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// 1a. Trailing data / multiple JSON documents -> 503
	bodyMulti := []byte(`{"model":"agy/gemini-2.5-flash","messages":[]}{"extra":"data"}`)
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-strict-1", "openai", model, bodyMulti, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("multiple JSON documents must return 503 terminate, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}
	if !strings.Contains(string(resp.ResponseBody), "omp_cloak_required") {
		t.Fatalf("expected omp_cloak_required error code, got: %s", string(resp.ResponseBody))
	}

	// 1b. Non-object root -> 503
	bodyArray := []byte(`["item1", "item2"]`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-strict-2", "openai", model, bodyArray, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("non-object JSON root must return 503 terminate, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}

	// 1c. Empty body -> 503
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-strict-3", "openai", model, []byte{}, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("empty body must return 503 terminate, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}

	// 1d. Number preservation as json.Number
	bodyNumber := []byte(`{"model":"agy/gemini-2.5-flash","messages":[],"temperature":0.000000000000000000123456789}`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-strict-4", "openai", model, bodyNumber, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("valid number request should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if !strings.Contains(string(resp.Body), "0.000000000000000000123456789") {
		t.Fatalf("json.Number precision must be preserved, got: %s", string(resp.Body))
	}
}

// 2. Marker parsing, coalescing, and conflict handling
func TestIssue27_MarkerParsingAndConflict(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	nonAGYModel := "openai/gpt-4o"
	validBody := []byte(`{"model":"m","messages":[{"role":"user","content":"test"}]}`)
	headersCoalesced := http.Header(map[string][]string{
		"x-cloak-client": {" , OMP , "},
		"X-CLOAK-CLIENT": {"oh-my-pi, oh_my_pi, "},
	})
	marker := parseExplicitClientMarker(headersCoalesced)
	if marker.isConflict {
		t.Fatalf("expected non-conflicting normalized OMP, got conflict: %+v", marker)
	}
	if !marker.isOMP || marker.client != "oh_my_pi" {
		t.Fatalf("expected isOMP=true, client=oh_my_pi, got: %+v", marker)
	}
	if len(marker.matchedKeys) != 2 {
		t.Fatalf("expected 2 matchedKeys, got: %v", marker.matchedKeys)
	}

	// Admitted on AGY route
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-marker-1", "openai", agyModel, validBody, headersCoalesced))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("coalesced OMP should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if !containsStr(resp.ClearHeaders, "x-cloak-client") || !containsStr(resp.ClearHeaders, "X-CLOAK-CLIENT") {
		t.Fatalf("all matchedKeys must be cleared, got ClearHeaders: %v", resp.ClearHeaders)
	}

	// 2b. Conflicting markers containing OMP on AGY route -> exact 503
	headersConflict := http.Header{}
	headersConflict.Add("X-Cloak-Client", "oh_my_pi, claude_code")
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-marker-conflict-agy", "openai", agyModel, validBody, headersConflict))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("conflicting markers containing OMP on AGY route must return 503, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}

	// Order-independent: claude_code then omp
	headersConflictReverse := http.Header{}
	headersConflictReverse.Add("X-Cloak-Client", "claude_code, omp")
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-marker-conflict-rev", "openai", agyModel, validBody, headersConflictReverse))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("reversed conflicting markers containing OMP on AGY route must return 503, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}

	// 2c. Conflicting markers containing OMP on non-AGY route -> durable bypass
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-marker-conflict-nonagy", "openai", nonAGYModel, validBody, headersConflict))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("conflicting marker on non-AGY route must not terminate, got 503: %s", string(resp.ResponseBody))
	}
	if len(resp.Body) != 0 {
		t.Fatalf("non-AGY bypass must have zero request mutation, got: %s", string(resp.Body))
	}

	// Correlated response for non-AGY bypass
	respBody := []byte(`{"choices":[{"message":{"content":"Hello Antigravity"}}]}`)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, "req-marker-conflict-nonagy", "openai", nonAGYModel, respBody))
	outResp := decodeEnvelopeBody(t, rawResp2)
	if len(outResp) != 0 {
		t.Fatalf("correlated response for non-AGY bypass must have zero mutation, got: %s", string(outResp))
	}

	// Cleanup lifecycle
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, "req-marker-1", "succeeded"))
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, "req-marker-conflict-nonagy", "succeeded"))
}

// 3. Base collision and namespace-exact reverse
func TestIssue27_BaseCollisionAndNamespaceExactReverse(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// 3a. functions:bash + default_api:bash => exact Protected 503 (same base bash)
	bodyCollision1 := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"functions:bash"}},
			{"type":"function","function":{"name":"default_api:bash"}}
		]
	}`)
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-col-1", "openai", agyModel, bodyCollision1, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("functions:bash + default_api:bash must return 503, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}

	// 3b. functions:bash + default_api:run_command => exact Protected 503 (both end up as run_command)
	bodyCollision2 := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"functions:bash"}},
			{"type":"function","function":{"name":"default_api:run_command"}}
		]
	}`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-col-2", "openai", agyModel, bodyCollision2, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("functions:bash + default_api:run_command must return 503, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}

	// 3c. functions:bash + default_api:read => Admitted (distinct bases: run_command vs view_file)
	reqIDAdmitted := "req-col-safe-distinct"
	bodySafe := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"functions:bash"}},
			{"type":"function","function":{"name":"default_api:read"}}
		]
	}`)
	raw, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqIDAdmitted, "openai", agyModel, bodySafe, headers))
	resp, _ = decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("functions:bash + default_api:read should be admitted, got 503: %s", string(resp.ResponseBody))
	}
	if !strings.Contains(string(resp.Body), `"name":"functions:run_command"`) {
		t.Fatalf("expected functions:bash -> functions:run_command, got: %s", string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), `"name":"default_api:view_file"`) {
		t.Fatalf("expected default_api:read -> default_api:view_file, got: %s", string(resp.Body))
	}

	// Response uncloak: exact namespace reverse
	// Model returns functions:run_command and default_api:view_file
	respUpstream := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[
					{"function":{"name":"functions:run_command","arguments":"{}"}},
					{"function":{"name":"default_api:view_file","arguments":"{}"}},
					{"function":{"name":"default_api:run_command","arguments":"{}"}},
					{"function":{"name":"functions:view_file","arguments":"{}"}}
				]
			}
		}]
	}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqIDAdmitted, "openai", agyModel, respUpstream))
	outBody := decodeEnvelopeBody(t, rawResp)

	// functions:run_command reversed to functions:bash
	if !strings.Contains(string(outBody), `"name":"functions:bash"`) {
		t.Fatalf("expected functions:run_command -> functions:bash, got: %s", string(outBody))
	}
	// default_api:view_file reversed to default_api:read
	if !strings.Contains(string(outBody), `"name":"default_api:read"`) {
		t.Fatalf("expected default_api:view_file -> default_api:read, got: %s", string(outBody))
	}
	// Cross-namespace output must NOT be reversed
	if !strings.Contains(string(outBody), `"name":"default_api:run_command"`) {
		t.Fatalf("cross-namespace default_api:run_command must NOT be reversed, got: %s", string(outBody))
	}
	if !strings.Contains(string(outBody), `"name":"functions:view_file"`) {
		t.Fatalf("cross-namespace functions:view_file must NOT be reversed, got: %s", string(outBody))
	}

	// Cleanup lifecycle
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqIDAdmitted, "succeeded"))
}

// 4. Config drift rejection
func TestIssue27_ConfigDriftRejection(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// Mutate activeFilterConfig tool_mappings.oh_my_pi by adding an extra mapping
	cfg := activeFilterConfig()
	cfgCopy := *cfg
	cfgCopy.ToolMappings = copyToolMappings(cfg.ToolMappings)
	cfgCopy.ToolMappings["oh_my_pi"]["extra_tool"] = "extra_target"
	applyFilterConfig(cfgCopy)

	bodyValid := []byte(`{"model":"agy/gemini-2.5-flash","messages":[],"tools":[{"type":"function","function":{"name":"bash"}}]}`)
	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, "req-drift-1", "openai", agyModel, bodyValid, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if !resp.Terminate || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("drifted config must return 503, got terminate=%t status=%d", resp.Terminate, resp.StatusCode)
	}
}

// 5. Terminal brand masking and .omp path preservation
func TestIssue27_BrandMaskingAndPathPreservation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	// Custom mapping attempting to rewrite Antigravity -> FakeBrand
	cfg := activeFilterConfig()
	cfgCopy := *cfg
	cfgCopy.CustomMappings = append(cfgCopy.CustomMappings, rewriteMapping{
		Match:       "Antigravity",
		Replacement: "FakeBrand",
	})
	// Also attempt to override OMP -> OtherBrand via custom mappings
	cfgCopy.CustomMappings = append(cfgCopy.CustomMappings, rewriteMapping{
		Match:       "omp",
		Replacement: "OtherBrand",
	})
	applyFilterConfig(cfgCopy)

	// Body with Oh My Pi, omp, and a path C:\Users\user\.omp\agent
	reqID := "req-brand-path"
	body := []byte(`{
		"model":"agy/gemini-2.5-flash",
		"system":"Running Oh My Pi agent with config in C:\\Users\\user\\.omp\\agent and using omp commands.",
		"messages":[]
	}`)

	raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, body, headers))
	resp, _ := decodeProtectedRequestIntercept(t, raw)
	if resp.Terminate {
		t.Fatalf("brand test should be admitted, got 503: %s", string(resp.ResponseBody))
	}

	bodyStr := string(resp.Body)
	// .omp path must be preserved
	if !strings.Contains(bodyStr, `.omp\agent`) && !strings.Contains(bodyStr, `.omp\\agent`) {
		t.Fatalf("expected .omp path to be preserved, got: %s", bodyStr)
	}
	// "Oh My Pi" and "omp" must become "Antigravity", terminal (not rewritten to FakeBrand)
	if strings.Contains(bodyStr, "FakeBrand") {
		t.Fatalf("terminal Antigravity must NOT be rewritten by later custom mappings to FakeBrand: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "OtherBrand") {
		t.Fatalf("omp must NOT be rewritten to OtherBrand: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Running Antigravity agent") {
		t.Fatalf("expected 'Running Antigravity agent', got: %s", bodyStr)
	}

	// Correlated response uncloak: brandRestorationEnabled is true
	respUpstream := []byte(`{"choices":[{"message":{"content":"Welcome to Antigravity runtime."}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", agyModel, respUpstream))
	outBody := string(decodeEnvelopeBody(t, rawResp))
	if !strings.Contains(outBody, "Welcome to Oh My Pi runtime.") && !strings.Contains(outBody, "Welcome to omp runtime.") {
		t.Fatalf("expected brand restoration in response, got: %s", outBody)
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
}

// 6. Stream lifecycle, rehydration, and invariant loss
func TestIssue27_StreamLifecycleAndRehydration(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	agyModel := "agy/gemini-2.5-flash"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqID := "req-stream-lifecycle"
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

	// 6a. Stream header-init (ChunkIndex == -1)
	headerInitReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   -1,
		SourceFormat: "openai",
		Model:        agyModel,
		Body:         []byte{},
	}
	rawChunk, _ := json.Marshal(headerInitReq)
	res1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk)
	var env1 pluginabi.Envelope
	json.Unmarshal(res1, &env1)
	if env1.Error != nil {
		t.Fatalf("header-init chunk returned error: %v", env1.Error)
	}

	// Verify route state is active
	route := globalLifecycleManager.getRoute(reqID)
	if route == nil || route.disposition != streamDispositionNone {
		t.Fatalf("expected streamDispositionNone before payload chunks, got: %+v", route)
	}

	// 6b. Rehydration before payload processing: evict stream session
	globalStreamManager.deleteSession("req:" + reqID)

	// First payload chunk (ChunkIndex == 0) delivers run_command
	chunk0Req := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        agyModel,
		Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n"),
	}
	rawChunk0, _ := json.Marshal(chunk0Req)
	res0, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk0)
	chunk0Body, _ := decodeEnvelopeStreamChunk(t, res0)

	if !strings.Contains(string(chunk0Body), `"name":"bash"`) {
		t.Fatalf("rehydrated stream state must uncloak run_command -> bash, got: %s", string(chunk0Body))
	}

	// Verify route state is now streamDispositionPayloadActive
	if route.disposition != streamDispositionPayloadActive {
		t.Fatalf("expected streamDispositionPayloadActive, got: %v", route.disposition)
	}

	// 6c. Missing state after payload processing began is an invariant violation -> empty response, no reconstruction
	globalStreamManager.deleteSession("req:" + reqID)
	chunk1Req := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   1,
		SourceFormat: "openai",
		Model:        agyModel,
		Body:         []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{}"}}]}}]}` + "\n\n"),
	}
	rawChunk1, _ := json.Marshal(chunk1Req)
	resLate, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunk1)
	lateBody, _ := decodeEnvelopeStreamChunk(t, resLate)
	if len(lateBody) != 0 {
		t.Fatalf("invariant violation after payload started must return empty response, got: %s", string(lateBody))
	}

	// Clean up route state via request.complete
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "failed"))
	if r := globalLifecycleManager.getRoute(reqID); r != nil {
		t.Fatalf("route state must be deleted after request.complete, got: %+v", r)
	}
}

// 7. Request lifecycle completion idempotence and outcomes
func TestIssue27_RequestCompleteOutcomes(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	outcomes := []string{"succeeded", "failed", "canceled", "rejected"}

	for _, outcome := range outcomes {
		reqID := "req-complete-" + outcome
		globalLifecycleManager.setRoute(reqID, &explicitOMPRouteState{
			routeKind: routeKindProtectedAGY,
			client:    "oh_my_pi",
		})
		globalStreamManager.resetSession("req:"+reqID, "oh_my_pi", nil, 1)

		rawComplete := makeRequestCompletePayload(t, reqID, outcome)
		res1, code1 := handlePluginCall(pluginabi.MethodRequestComplete, rawComplete)
		if code1 != 0 {
			t.Fatalf("request.complete failed for outcome %s: code=%d, res=%s", outcome, code1, string(res1))
		}

		if r := globalLifecycleManager.getRoute(reqID); r != nil {
			t.Fatalf("route must be deleted for outcome %s", outcome)
		}

		// Idempotent second call
		res2, code2 := handlePluginCall(pluginabi.MethodRequestComplete, rawComplete)
		if code2 != 0 {
			t.Fatalf("idempotent request.complete failed for outcome %s: code=%d, res=%s", outcome, code2, string(res2))
		}
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
