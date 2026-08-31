package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestIntegration_Precedence_InvalidExplicitSuppressesUA_Lifecycle proves that a
// present-but-invalid X-Cloak-Client keeps suppressing User-Agent inference
// across the WHOLE response lifecycle, not just the request interceptor. With a
// thin Oh My Pi body (no independent classification), the request must not
// cloak, and the non-streaming response, stream header-init, and lazy stream
// payload fallback must all refuse to reclassify the request from the surviving
// omp/... User-Agent. Proved entirely through the black-box handlePluginCall
// seam: the observable contract is "view_file is never restored to read".
func TestIntegration_Precedence_InvalidExplicitSuppressesUA_Lifecycle(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	reqID := "prec-invalid-explicit-001"

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
	tb, _ := json.Marshal(thin)

	// 1. Request side: no cloak, header consumed.
	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, tb, headers))
	body, _, clearHeaders := decodeEnvelopeRequestIntercept(t, rawReq)
	if len(body) != 0 {
		t.Fatalf("invalid explicit + UA + thin body must not cloak, got: %s", body)
	}
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("invalid explicit must still consume header, ClearHeaders=%v", clearHeaders)
	}

	// 2. Non-streaming response with the same RequestID and a surviving omp UA:
	// the request-time negative resolution must win; view_file stays view_file.
	respHeaders := http.Header{}
	respHeaders.Set("User-Agent", "omp/1.2.3")
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayloadWithHeaders(t, reqID, "openai", model, respBody, respHeaders))
	if out := decodeEnvelopeBody(t, rawResp); len(out) != 0 {
		t.Fatalf("response must not uncloak via UA after request-time negative resolution, got: %s", out)
	}

	// 3. Streaming: header-init must not reset the negative session from UA,
	// and the payload chunk must pass through untouched.
	initPayload, _ := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   "openai",
		Model:          model,
		RequestedModel: model,
		ChunkIndex:     pluginapi.StreamChunkHeaderInitIndex,
		RequestHeaders: respHeaders,
	})
	rawInit, code := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
	if code != 0 {
		t.Fatalf("header-init failed code=%d", code)
	}
	if out := decodeEnvelopeBody(t, rawInit); len(out) != 0 {
		t.Fatalf("header-init should return empty body, got: %s", out)
	}
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk,
		makeIntegrationStreamChunkPayloadWithHeaders(t, reqID, "openai", model, 0, chunkBody, respHeaders))
	if out := decodeEnvelopeBody(t, rawChunk); len(out) != 0 {
		t.Fatalf("stream payload must not uncloak via UA after request-time negative resolution, got: %s", out)
	}
}

// TestIntegration_Precedence_InvalidExplicit_LazyStreamFallbackSuppressed covers
// the lazy lane: a payload chunk arriving with no header-init still must not
// reinterpret the surviving UA evidence when the request interceptor recorded a
// negative resolution for the correlated RequestID.
func TestIntegration_Precedence_InvalidExplicit_LazyStreamFallbackSuppressed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	reqID := "prec-invalid-explicit-002"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "nope")
	headers.Set("User-Agent", "omp/9.9")
	thin := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	tb, _ := json.Marshal(thin)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, tb, headers))

	// Payload chunk directly (no header-init): fallback must see the negative
	// correlation and leave run_command (an omp cloaked name) untouched.
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk,
		makeIntegrationStreamChunkPayloadWithHeaders(t, reqID, "openai", model, 0, chunkBody, headers))
	if out := decodeEnvelopeBody(t, rawChunk); len(out) != 0 {
		t.Fatalf("lazy stream fallback must honor request-time negative resolution, got: %s", out)
	}
}

// TestIntegration_Precedence_InvalidExplicit_BodyResolvedClientRoundTrips proves
// body classification remains the fallback for the invalid explicit override
// itself: when the body independently classifies as claude_code, that client is
// recorded at request time and reused by the response and stream paths even
// though a contradictory omp UA survives.
func TestIntegration_Precedence_InvalidExplicit_BodyResolvedClientRoundTrips(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	reqID := "prec-invalid-explicit-003"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "bogus_client")
	headers.Set("User-Agent", "omp/1.0")
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
	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, cb, headers))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawReq)
	if !strings.Contains(string(body), `"name":"run_command"`) {
		t.Fatalf("invalid explicit must fall back to body detection (claude_code), got: %s", body)
	}

	// Response reuses the body-resolved claude_code client, not the UA's omp.
	respHeaders := http.Header{}
	respHeaders.Set("User-Agent", "omp/1.0")
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayloadWithHeaders(t, reqID, "openai", model, respBody, respHeaders))
	out := decodeEnvelopeBody(t, rawResp)
	if !strings.Contains(string(out), `"name":"Bash"`) {
		t.Fatalf("response must uncloak with body-resolved claude_code (Bash), not omp (bash), got: %s", out)
	}

	// Stream payload chunk follows the same body-resolved session truth.
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk,
		makeIntegrationStreamChunkPayloadWithHeaders(t, reqID, "openai", model, 0, chunkBody, respHeaders))
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"Bash"`) {
		t.Fatalf("stream must uncloak with body-resolved claude_code, got: %s", chunkOut)
	}
}

// TestIntegration_Precedence_MissingCorrelationStillRecoversViaUA pins the
// boundary of the negative-resolution rule: when the request interceptor never
// saw the request (no correlation state at all), the conservative recovery
// order may still use verified surviving User-Agent evidence.
func TestIntegration_Precedence_MissingCorrelationStillRecoversViaUA(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	respHeaders := http.Header{}
	respHeaders.Set("User-Agent", "omp/1.2.3")
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayloadWithHeaders(t, "prec-no-correlation-004", "openai", model, respBody, respHeaders))
	out := decodeEnvelopeBody(t, rawResp)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("genuinely missing correlation must still allow UA recovery, got: %s", out)
	}
}
