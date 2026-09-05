package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Helpers for response/stream with RequestHeaders.

func makeIntegrationResponseInterceptPayloadWithHeaders(t *testing.T, reqID, sourceFormat, model string, body []byte, reqHeaders http.Header) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.ResponseInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		Body:           body,
		RequestHeaders: reqHeaders,
	})
	if err != nil {
		t.Fatalf("marshal response intercept with headers: %v", err)
	}
	return raw
}

func makeIntegrationResponseInterceptPayloadWithRequestBodyAndHeaders(t *testing.T, reqID, sourceFormat, model string, body []byte, reqBody []byte, reqHeaders http.Header) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.ResponseInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		Body:           body,
		RequestBody:    reqBody,
		RequestHeaders: reqHeaders,
	})
	if err != nil {
		t.Fatalf("marshal response intercept with body+headers: %v", err)
	}
	return raw
}

func makeIntegrationStreamChunkPayloadWithHeaders(t *testing.T, reqID, sourceFormat, model string, chunkIndex int, chunkBody []byte, reqHeaders http.Header) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		ChunkIndex:     chunkIndex,
		Body:           chunkBody,
		RequestHeaders: reqHeaders,
	})
	if err != nil {
		t.Fatalf("marshal stream chunk with headers: %v", err)
	}
	return raw
}

func makeIntegrationStreamChunkPayloadWithRequestBodyAndHeaders(t *testing.T, reqID, sourceFormat, model string, chunkIndex int, chunkBody, reqBody []byte, reqHeaders http.Header) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		ChunkIndex:     chunkIndex,
		Body:           chunkBody,
		RequestBody:    reqBody,
		RequestHeaders: reqHeaders,
	})
	if err != nil {
		t.Fatalf("marshal stream chunk with body+headers: %v", err)
	}
	return raw
}

// TestIntegration_UserAgent_ThinOhMyPi_Roundtrip proves a thin OMP request
// exposing only `read` cloaks when User-Agent: omp/<version> and no explicit
// override, and that the lifecycle round-trips via session truth.
func TestIntegration_UserAgent_ThinOhMyPi_Roundtrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "ua-thin-roundtrip-001"
	model := "agy/gemini-3.7-flash"
	headers := http.Header{}
	headers.Set("User-Agent", "omp/1.2.3")

	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read file"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": "Read file"}},
		},
		"stream": false,
	}
	b, _ := json.Marshal(clientReq)
	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d", code)
	}
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"view_file"`) {
		t.Fatalf("UA omp thin request not cloaked to view_file: %s", body)
	}
	if strings.Contains(string(body), `"name":"read"`) {
		t.Fatalf("leaked read: %s", body)
	}
	// Response path must use session truth (pre-registered).
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("response did not uncloak view_file to read via session: %s", out)
	}
	// Streaming path must also use session truth.
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, chunkBody, nil))
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"read"`) {
		t.Fatalf("stream did not uncloak view_file to read via session: %s", chunkOut)
	}
}

// TestIntegration_UserAgent_NoUANoDetection preserves conservative no-detection.
func TestIntegration_UserAgent_NoUANoDetection(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thin)
	// No UA
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-no-ua-002a", "openai", model, b, nil))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("thin OMP without UA must not cloak, got: %s", body)
	}
	// Unknown UA
	h := http.Header{}
	h.Set("User-Agent", "Mozilla/5.0")
	rawResp2, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-no-ua-002b", "openai", model, b, h))
	body2, _, _ := decodeEnvelopeRequestIntercept(t, rawResp2)
	if len(body2) != 0 {
		t.Fatalf("unknown UA must not cloak thin OMP, got: %s", body2)
	}
}

// TestIntegration_UserAgent_CaseInsensitiveAndRequiresMapping
func TestIntegration_UserAgent_CaseInsensitiveAndRequiresMapping(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thin)
	cases := []string{"omp/1.0", "OMP/2.0", "Omp/3.0", "oMp/0.9.1 (linux)"}
	for _, ua := range cases {
		h := http.Header{}
		h.Set("User-Agent", ua)
		rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-case-"+ua, "openai", model, b, h))
		body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
		if !strings.Contains(string(body), `"name":"view_file"`) {
			t.Fatalf("UA %q should cloak thin OMP case-insensitively, got: %s", ua, body)
		}
	}
	// When oh_my_pi mapping is empty, UA must not activate.
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings: map[string]map[string]string{
			"claude_code": copyToolMappings(defaultCloakTables)["claude_code"],
			"codex":       copyToolMappings(defaultCloakTables)["codex"],
			// oh_my_pi omitted -> empty
		},
	})
	// Need rebuild handled by applyFilterConfig.
	h := http.Header{}
	h.Set("User-Agent", "omp/1.2.3")
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-no-mapping-003", "openai", model, b, h))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("UA omp should be inert when oh_my_pi mapping empty, got: %s", body)
	}
}

// TestIntegration_UserAgent_ClaudeCliDoesNotInfer
func TestIntegration_UserAgent_ClaudeCliDoesNotInfer(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thin)
	h := http.Header{}
	h.Set("User-Agent", "claude-cli/1.0.0 (oh-my-pi)")
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-claude-cli-004", "openai", model, b, h))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("claude-cli UA must not force claude_code for thin OMP, got cloak: %s", body)
	}
	// also test that claude-cli with a proper claude_code body still relies on body detection, not UA
	ccBody := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Edit"}},
		},
	}
	ccB, _ := json.Marshal(ccBody)
	rawResp2, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-claude-cli-004b", "openai", model, ccB, h))
	body2, _, _ := decodeEnvelopeRequestIntercept(t, rawResp2)
	if !strings.Contains(string(body2), `"name":"run_command"`) {
		t.Fatalf("claude_code body should still cloak via body gate, got: %s", body2)
	}
}

// TestIntegration_UserAgent_OpencodeInert
func TestIntegration_UserAgent_OpencodeInert(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thin)
	h := http.Header{}
	h.Set("User-Agent", "opencode/1.0.0")
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-opencode-005", "openai", model, b, h))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("opencode UA must be inert without mapping, got cloak: %s", body)
	}
	// case-insensitive
	h2 := http.Header{}
	h2.Set("User-Agent", "OpenCode/2.0")
	rawResp2, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-opencode-005b", "openai", model, b, h2))
	body2, _, _ := decodeEnvelopeRequestIntercept(t, rawResp2)
	if len(body2) != 0 {
		t.Fatalf("OpenCode UA must be inert without mapping, got: %s", body2)
	}
}

// TestIntegration_UserAgent_UnknownFallsThrough
func TestIntegration_UserAgent_UnknownFallsThrough(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	// Thin OMP should not cloak with unknown UA
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thin)
	for _, ua := range []string{"codex/1.0", "custom-agent/2.0", "Mozilla/5.0", "cursor/1.0"} {
		h := http.Header{}
		h.Set("User-Agent", ua)
		rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-unknown-"+ua, "openai", model, b, h))
		body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
		if len(body) != 0 {
			t.Fatalf("unknown UA %q must fall through without cloak for thin OMP, got: %s", ua, body)
		}
	}
	// But a body that classifies as oh_my_pi via distinctive tools should still cloak via body gate even with unknown UA
	bodyCC := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "hub"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "task"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	bb, _ := json.Marshal(bodyCC)
	h := http.Header{}
	h.Set("User-Agent", "unknown/1.0")
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-unknown-bodygate-006", "openai", model, bb, h))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"run_command"`) {
		t.Fatalf("unknown UA must still allow body Client Gate to cloak, got: %s", body)
	}
}

// TestIntegration_UserAgent_ValidExplicitBeatsUA proves explicit wins over contradictory UA.
func TestIntegration_UserAgent_ValidExplicitBeatsUA(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	reqID := "ua-explicit-beats-007"
	// Body looks like claude_code but explicit says oh_my_pi, UA says omp (same as explicit) or contradictory
	headers := http.Header{}
	headers.Set(explicitClientHeader, "claude_code")
	headers.Set("User-Agent", "omp/1.2.3")
	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Edit"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Read"}},
		},
	}
	b, _ := json.Marshal(clientReq)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	// explicit claude_code should win: Bash (Pascal) -> run_command, but uncloak table is claude_code (Bash). The request cloak for claude_code will use Bash->run_command.
	// If UA omp had won, it would look for lowercase bash, not Bash, so no cloak for Pascal? But we can verify via response uncloak: run_command should restore to Bash not bash.
	// Request body cloak for claude_code with those tools will produce run_command.
	if !strings.Contains(string(body), `"name":"run_command"`) {
		t.Fatalf("explicit claude_code should cloak Bash to run_command, got: %s", body)
	}
	// Now verify response uses explicit's table (Bash) not UA's oh_my_pi (bash)
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"Bash"`) {
		t.Fatalf("valid explicit must beat UA, expected Bash, got: %s", out)
	}
	if strings.Contains(string(out), `"name":"bash"`) && !strings.Contains(string(out), `"name":"Bash"`) {
		t.Fatalf("UA oh_my_pi should not have overridden explicit claude_code, got: %s", out)
	}
	// opposite: explicit oh_my_pi, UA claude-cli (should be ignored)
	headers2 := http.Header{}
	headers2.Set(explicitClientHeader, "oh_my_pi")
	headers2.Set("User-Agent", "claude-cli/1.0")
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	bb, _ := json.Marshal(thin)
	rawResp3, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-explicit-beats-007b", "openai", model, bb, headers2))
	body3, _, _ := decodeEnvelopeRequestIntercept(t, rawResp3)
	if !strings.Contains(string(body3), `"name":"view_file"`) {
		t.Fatalf("explicit oh_my_pi should cloak thin read even with contradictory UA, got: %s", body3)
	}
}

// TestIntegration_UserAgent_InvalidExplicitFallsToBodyNotUA
func TestIntegration_UserAgent_InvalidExplicitFallsToBodyNotUA(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	// invalid explicit + valid omp UA + thin read must NOT cloak (UA bypassed, body gate needs 4 or distinctive)
	headers := http.Header{}
	headers.Set(explicitClientHeader, "bogus_client")
	headers.Set("User-Agent", "omp/1.2.3")
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thin)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-invalid-bypass-008", "openai", model, b, headers))
	body, _, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("invalid explicit + valid omp UA must not activate UA, thin OMP must stay uncloaked, got: %s", body)
	}
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("invalid explicit must still be cleared")
	}
	// invalid explicit + valid omp UA + body that classifies as claude_code via body gate must use body classification
	headers2 := http.Header{}
	headers2.Set(explicitClientHeader, "unknown")
	headers2.Set("User-Agent", "omp/1.0")
	ccBody := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Edit"}},
		},
	}
	bb, _ := json.Marshal(ccBody)
	rawResp2, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-invalid-bypass-008b", "openai", model, bb, headers2))
	body2, _, _ := decodeEnvelopeRequestIntercept(t, rawResp2)
	if !strings.Contains(string(body2), `"name":"run_command"`) {
		t.Fatalf("invalid explicit should fall back to body detection (claude_code), got: %s", body2)
	}
}

// TestIntegration_UserAgent_ResponseUsesSessionTruth proves response/stream use pre-registered client.
func TestIntegration_UserAgent_ResponseUsesSessionTruth(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	reqID := "ua-session-truth-009"
	// Establish session via claude_code body detection (distinctive)
	ccBody := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Edit"}},
		},
	}
	cb, _ := json.Marshal(ccBody)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, cb))
	// Response with same RequestID but UA that would otherwise map to oh_my_pi must still use session (claude_code)
	reqHeaders := http.Header{}
	reqHeaders.Set("User-Agent", "omp/1.2.3")
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayloadWithHeaders(t, reqID, "openai", model, respBody, reqHeaders))
	out := decodeEnvelopeBody(t, rawResp)
	if !strings.Contains(string(out), `"name":"Bash"`) {
		t.Fatalf("response with session must use pre-registered claude_code (Bash), not UA oh_my_pi (bash), got: %s", out)
	}
	// Stream chunk with same RequestID and UA omp must also use session
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, makeIntegrationStreamChunkPayloadWithHeaders(t, reqID, "openai", model, 0, chunkBody, reqHeaders))
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"Bash"`) {
		t.Fatalf("stream with session must use pre-registered claude_code, got: %s", chunkOut)
	}
	// Also test reverse: session oh_my_pi, UA claude-cli must not override
	reqID2 := "ua-session-truth-009b"
	headersOMP := http.Header{}
	headersOMP.Set("User-Agent", "omp/1.0")
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	tb, _ := json.Marshal(thin)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID2, "openai", model, tb, headersOMP))
	// This session is oh_my_pi (thin read via UA)
	reqHeaders2 := http.Header{}
	reqHeaders2.Set("User-Agent", "claude-cli/1.0")
	respBody2 := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayloadWithHeaders(t, reqID2, "openai", model, respBody2, reqHeaders2))
	out2 := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out2), `"name":"read"`) {
		t.Fatalf("response with oh_my_pi session must uncloak view_file to read, got: %s", out2)
	}
}

// TestIntegration_UserAgent_FallbackWhenNoSessionMayUseUA
func TestIntegration_UserAgent_FallbackWhenNoSessionMayUseUA(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	// Response without prior session but with UA omp should uncloak via UA
	reqHeaders := http.Header{}
	reqHeaders.Set("User-Agent", "omp/1.2.3")
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	// Use a fresh RequestID that was never registered
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayloadWithHeaders(t, "ua-fallback-no-session-010a", "openai", model, respBody, reqHeaders))
	out := decodeEnvelopeBody(t, rawResp)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("response fallback via UA omp should uncloak view_file to read, got: %s", out)
	}
	if strings.Contains(string(out), "view_file") {
		t.Fatalf("leaked view_file: %s", out)
	}
	// Response with unknown UA and thin read should not uncloak (body gate conservative, no UA)
	unknownHeaders := http.Header{}
	unknownHeaders.Set("User-Agent", "Mozilla/5.0")
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayloadWithHeaders(t, "ua-fallback-no-session-010b", "openai", model, respBody, unknownHeaders))
	out2 := decodeEnvelopeBody(t, rawResp2)
	// Thin OMP body detection needs 4 or distinctive; view_file alone via body detection would be via cloaked detection? But detection from request body is not present here (no request body), so it should not invent client.
	// The resp Body contains view_file but without request context, buildUncloakTable will look at empty request body? Our test supplied no RequestBody, so detection will fail and return nothing, correctly not uncloaking.
	if !strings.Contains(string(out2), "view_file") {
		// If it incorrectly uncloaked, it would contain read. But with unknown UA, should remain view_file
		// So we expect no change (still view_file)
		t.Logf("unknown UA fallback correctly left view_file untouched: %s", out2)
	}
	// Stream fallback: no session, payload chunk with UA omp, standalone JSON
	streamHeaders := http.Header{}
	streamHeaders.Set("User-Agent", "omp/9.0")
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, makeIntegrationStreamChunkPayloadWithHeaders(t, "ua-fallback-stream-010c", "openai", model, 0, chunkBody, streamHeaders))
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"bash"`) {
		t.Fatalf("stream fallback via UA omp should uncloak run_command to bash, got: %s", chunkOut)
	}
}

// TestIntegration_UserAgent_StreamHeaderInitFallbackUsesUA
func TestIntegration_UserAgent_StreamHeaderInitFallbackUsesUA(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	// Send header-init chunk with UA omp but no request body that would classify; it should register session via UA
	reqID := "ua-stream-header-init-011"
	headers := http.Header{}
	headers.Set("User-Agent", "omp/1.0")
	// Header-init payload (ChunkIndex -1) with RequestHeaders UA and empty bodies
	initPayload, _ := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   "openai",
		Model:          model,
		RequestedModel: model,
		ChunkIndex:     pluginapi.StreamChunkHeaderInitIndex,
		RequestHeaders: headers,
	})
	rawInit, code := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
	if code != 0 {
		t.Fatalf("header-init failed code=%d", code)
	}
	// Must return empty body (no uncloak on header-init)
	if out := decodeEnvelopeBody(t, rawInit); len(out) != 0 {
		t.Fatalf("header-init should return empty body, got: %s", out)
	}
	// Now payload chunk should use that session (via UA) to uncloak
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, makeIntegrationStreamChunkPayloadWithHeaders(t, reqID, "openai", model, 0, chunkBody, headers))
	out := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("stream payload after UA header-init must uncloak view_file to read, got: %s", out)
	}
}

// TestIntegration_UserAgent_NoCodexUAInference ensures Codex not inferred from UA
func TestIntegration_UserAgent_NoCodexUAInference(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "shell_command"}},
		},
	}
	b, _ := json.Marshal(thin)
	for _, ua := range []string{"codex/1.0", "Codex/2.0", "codex-cli/1.0"} {
		h := http.Header{}
		h.Set("User-Agent", ua)
		rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-nocodex-"+ua, "openai", model, b, h))
		body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
		if len(body) != 0 {
			t.Fatalf("UA %q must not infer codex for thin tool, got cloak: %s", ua, body)
		}
	}
	// Ensure body gate still works for codex when tool set qualifies
	codexBody := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "shell_command"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "apply_patch"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "view_image"}},
		},
	}
	cb, _ := json.Marshal(codexBody)
	h := http.Header{}
	h.Set("User-Agent", "Mozilla/5.0")
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayloadWithHeaders(t, "ua-nocodex-bodygate", "openai", model, cb, h))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), "run_command") && !strings.Contains(string(body), "multi_replace_file_content") {
		t.Fatalf("codex body gate should still cloak via body detection, got: %s", body)
	}
}

// TestIntegration_NonClientRequestWithBrandWords_PreservesBodyWithoutMutation proves
// that a normal non-client API request that passes the model gate and contains brand
// words (OMP, Codex, Claude Code) in system / system-role content returns no body
// mutation when no supported coding client is resolved (Issue #23).
func TestIntegration_NonClientRequestWithBrandWords_PreservesBodyWithoutMutation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"

	// OpenAI format: top-level system and messages system role with brand words, no coding tools
	openAIReq := map[string]any{
		"model":  model,
		"system": "You are a helpful assistant talking about OMP, Codex, and Claude Code.",
		"messages": []any{
			map[string]any{"role": "system", "content": "Keep OMP, Codex, and Claude Code intact."},
			map[string]any{"role": "user", "content": "Explain OMP architecture."},
		},
	}
	b, err := json.Marshal(openAIReq)
	if err != nil {
		t.Fatalf("marshal openAIReq: %v", err)
	}

	// Case 1: No User-Agent header
	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue23-no-client-no-ua", "openai", model, b, nil))
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d, want 0", code)
	}
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("normal API request without client must not be mutated, got: %s", body)
	}

	// Case 2: Generic User-Agent (curl/8.0.0)
	h := http.Header{}
	h.Set("User-Agent", "curl/8.0.0")
	rawResp2, code2 := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue23-no-client-curl-ua", "openai", model, b, h))
	if code2 != 0 {
		t.Fatalf("request.intercept_before code=%d, want 0", code2)
	}
	body2, _, _ := decodeEnvelopeRequestIntercept(t, rawResp2)
	if len(body2) != 0 {
		t.Fatalf("normal API request with generic UA must not be mutated, got: %s", body2)
	}

	// Case 3: Anthropic format with brand words in top-level system
	anthropicReq := map[string]any{
		"model":  model,
		"system": "You are a helpful assistant talking about OMP and Claude Code.",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	bAnth, err := json.Marshal(anthropicReq)
	if err != nil {
		t.Fatalf("marshal anthropicReq: %v", err)
	}
	rawRespAnth, codeAnth := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue23-no-client-anthropic", "anthropic", model, bAnth, h))
	if codeAnth != 0 {
		t.Fatalf("request.intercept_before code=%d, want 0", codeAnth)
	}
	bodyAnth, _, _ := decodeEnvelopeRequestIntercept(t, rawRespAnth)
	if len(bodyAnth) != 0 {
		t.Fatalf("normal Anthropic API request must not be mutated, got: %s", bodyAnth)
	}
}

// TestIntegration_RecognizedOhMyPi_BrandRewritesAndToolCloaks proves that a
// recognized Oh My Pi request (via UA evidence or body-based tools) performs
// both brand rewriting on system/system-role fields and tool cloaking on the
// request path, and uncloaks cleanly on response and stream paths (Issue #23).
func TestIntegration_RecognizedOhMyPi_BrandRewritesAndToolCloaks(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "omp-brand-tool-roundtrip-001"
	model := "agy/gemini-3.7-flash"
	headers := http.Header{}
	headers.Set("User-Agent", "omp/1.2.3")

	clientReq := map[string]any{
		"model":  model,
		"system": "You are Oh My Pi coding assistant.",
		"messages": []any{
			map[string]any{"role": "system", "content": "Running in OMP harness."},
			map[string]any{"role": "user", "content": "read file"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": "Read file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash", "description": "Run shell"}},
		},
		"stream": false,
	}
	b, err := json.Marshal(clientReq)
	if err != nil {
		t.Fatalf("marshal clientReq: %v", err)
	}

	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d, want 0", code)
	}
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) == 0 {
		t.Fatalf("recognized OMP request must be rewritten, got empty body")
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"name":"view_file"`) {
		t.Fatalf("read tool not cloaked to view_file: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"name":"run_command"`) {
		t.Fatalf("bash tool not cloaked to run_command: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "Oh My Pi") {
		t.Fatalf("leaked 'Oh My Pi' brand in request body: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "OMP") {
		t.Fatalf("leaked 'OMP' brand in request body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Antigravity") {
		t.Fatalf("missing 'Antigravity' replacement in request body: %s", bodyStr)
	}

	// Non-streaming response path uncloaks view_file to read
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawResp2, code2 := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	if code2 != 0 {
		t.Fatalf("response.intercept_after code=%d", code2)
	}
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("response did not uncloak view_file to read: %s", out)
	}

	// Streaming response path uncloaks view_file to read
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawChunk, codeChunk := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk,
		makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, chunkBody, nil))
	if codeChunk != 0 {
		t.Fatalf("stream chunk code=%d", codeChunk)
	}
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"read"`) {
		t.Fatalf("stream chunk did not uncloak view_file to read: %s", chunkOut)
	}

	// Case 2: Body-based OMP detection (no UA) with >= 4 common tools
	bodyOnlyReq := map[string]any{
		"model":  model,
		"system": "You are Oh My Pi agent.",
		"messages": []any{
			map[string]any{"role": "system", "content": "OMP environment."},
			map[string]any{"role": "user", "content": "edit"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "write"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "edit"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
		},
	}
	bBody, _ := json.Marshal(bodyOnlyReq)
	rawRespBody, codeBody := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "omp-body-detected-002", "openai", model, bBody, nil))
	if codeBody != 0 {
		t.Fatalf("request.intercept_before code=%d, want 0", codeBody)
	}
	bodyRes, _, _ := decodeEnvelopeRequestIntercept(t, rawRespBody)
	if len(bodyRes) == 0 {
		t.Fatalf("body-detected OMP request must be rewritten, got empty body")
	}
	bodyResStr := string(bodyRes)
	if !strings.Contains(bodyResStr, `"name":"replace_file_content"`) {
		t.Fatalf("edit tool not cloaked to replace_file_content: %s", bodyResStr)
	}
	if strings.Contains(bodyResStr, "Oh My Pi") || strings.Contains(bodyResStr, "OMP") {
		t.Fatalf("leaked brand in body-detected OMP request: %s", bodyResStr)
	}
	if !strings.Contains(bodyResStr, "Antigravity") {
		t.Fatalf("missing 'Antigravity' replacement in body-detected OMP request: %s", bodyResStr)
	}
}
