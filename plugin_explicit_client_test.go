package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// makeIntegrationRequestInterceptPayloadWithHeaders builds a
// request.intercept_before payload carrying outbound request headers, so tests
// can exercise the plugin-owned control header consumption contract.
func makeIntegrationRequestInterceptPayloadWithHeaders(t *testing.T, reqID, sourceFormat, model string, body []byte, headers http.Header) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		Body:           body,
		Headers:        headers,
	})
	if err != nil {
		t.Fatalf("marshal request intercept: %v", err)
	}
	return raw
}

// makeIntegrationResponseInterceptPayload builds a response.intercept_after
// payload correlated to a request id.
func makeIntegrationResponseInterceptPayload(t *testing.T, reqID, sourceFormat, model string, body []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.ResponseInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		Body:           body,
	})
	if err != nil {
		t.Fatalf("marshal response intercept: %v", err)
	}
	return raw
}

// decodeEnvelopeRequestIntercept decodes a request-interceptor envelope,
// returning the (possibly empty) modified body, the replacement header set
// (resp.Headers, nil when the plugin made no header change), and the
// ClearHeaders directive.
func decodeEnvelopeRequestIntercept(t *testing.T, rawEnvelope []byte) ([]byte, http.Header, []string) {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body         string              `json:"Body"`
			Headers      map[string][]string `json:"Headers"`
			ClearHeaders []string            `json:"ClearHeaders"`
		} `json:"result"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw: %s)", err, string(rawEnvelope))
	}
	if !env.OK {
		if env.Error != nil {
			t.Fatalf("envelope returned error: %s - %s", env.Error.Code, env.Error.Message)
		}
		t.Fatalf("envelope returned ok=false")
	}
	var body []byte
	if env.Result.Body != "" {
		decoded, err := base64.StdEncoding.DecodeString(env.Result.Body)
		if err != nil {
			t.Fatalf("base64 decode envelope body: %v", err)
		}
		body = decoded
	}
	return body, http.Header(env.Result.Headers), env.Result.ClearHeaders
}

// headerContainsFold reports whether headers has any key equal-fold to name,
// which http.Header.Get cannot detect when the stored key is non-canonical.
func headerContainsFold(headers http.Header, name string) bool {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// TestIntegration_ExplicitClient_ThinOhMyPi_Roundtrip proves a thin Oh My Pi
// request exposing only `read` cloaks when X-Cloak-Client: oh_my_pi is present,
// and that request-time session pre-registration keeps the non-streaming and
// streaming response paths symmetric after the header is consumed.
func TestIntegration_ExplicitClient_ThinOhMyPi_Roundtrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "omp-explicit-thin-001"
	model := "agy/gemini-3.7-flash"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "oh_my_pi")

	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read file"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": "Read file"}},
		},
		"stream": false,
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	// Request intercept: thin body + valid explicit client must cloak.
	payload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, clientReqBytes, headers)
	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, payload)
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d", code)
	}
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("ClearHeaders=%v, want to include %q", clearHeaders, explicitClientHeader)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}
	if !strings.Contains(string(body), `"name":"view_file"`) {
		t.Fatalf("request not cloaked to view_file: %s", body)
	}
	if strings.Contains(string(body), `"name":"read"`) {
		t.Fatalf("request leaked read: %s", body)
	}

	// Non-streaming response path must uncloak using req-time session truth.
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawResp2, code := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	if code != 0 {
		t.Fatalf("response.intercept_after code=%d", code)
	}
	respOut := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(respOut), `"name":"read"`) {
		t.Fatalf("response did not uncloak view_file to read: %s", respOut)
	}
	if strings.Contains(string(respOut), "view_file") {
		t.Fatalf("response leaked view_file: %s", respOut)
	}

	// Streaming response path must uncloak the same way.
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, chunkBody, nil))
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"read"`) {
		t.Fatalf("stream did not uncloak view_file to read: %s", chunkOut)
	}
	if strings.Contains(string(chunkOut), "view_file") {
		t.Fatalf("stream leaked view_file: %s", chunkOut)
	}
}

// TestIntegration_ExplicitClient_AliasNormalization proves aliases such as
// `omp` and `oh-my-pi` normalize to the canonical `oh_my_pi` table key.
func TestIntegration_ExplicitClient_AliasNormalization(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"
	aliases := []string{"oh_my_pi", "omp", "oh-my-pi", "OH_MY_PI", "  OMP  "}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			reqID := "omp-explicit-alias-002-" + strings.ReplaceAll(strings.TrimSpace(alias), " ", "")
			headers := http.Header{}
			headers.Set(explicitClientHeader, alias)
			clientReq := map[string]any{
				"model":    model,
				"messages": []any{map[string]any{"role": "user", "content": "read"}},
				"tools": []any{
					map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
				},
			}
			b, _ := json.Marshal(clientReq)
			rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
			body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
			if !strings.Contains(string(body), `"name":"view_file"`) {
				t.Fatalf("alias %q did not cloak read to view_file: %s", alias, body)
			}
		})
	}
}

// TestIntegration_ExplicitClient_BeatsBodyClassification proves a valid
// explicit override wins over a body whose tool set would otherwise classify
// as claude_code, choosing the oh_my_pi table for both cloak and uncloak.
func TestIntegration_ExplicitClient_BeatsBodyClassification(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "omp-explicit-beat-003"
	model := "agy/gemini-3.7-flash"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "oh_my_pi")

	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Edit"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Read"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, clientReqBytes, headers))
	_, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("ClearHeaders=%v, want to include %q", clearHeaders, explicitClientHeader)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}

	// The response must be uncloaked via the oh_my_pi table (bash, lowercase),
	// not the claude_code table (Bash), proving the override won.
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"bash"`) {
		t.Fatalf("expected OMP uncloak to lowercase bash, got: %s", out)
	}
	if strings.Contains(string(out), `"name":"Bash"`) {
		t.Fatalf("claude_code uncloak should have been overridden, got Bash: %s", out)
	}
}

// TestIntegration_ExplicitClient_InvalidFallsBackToBody proves an invalid or
// unknown explicit value does not activate an unusable mapping and falls back
// to the existing body Client Gate, while still consuming the header.
func TestIntegration_ExplicitClient_InvalidFallsBackToBody(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"

	// Invalid value (no tool table) + thin OMP body -> no cloak, header consumed.
	headers := http.Header{}
	headers.Set(explicitClientHeader, "opencode")
	thinBody := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(thinBody)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "omp-explicit-invalid-004a", "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("invalid explicit value must not cloak thin OMP body, got: %s", body)
	}
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("invalid value must still consume header, ClearHeaders=%v", clearHeaders)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}

	// Invalid value + body that classifies as claude_code -> body detection runs.
	headers2 := http.Header{}
	headers2.Set(explicitClientHeader, "bogus_client")
	ccBody := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "Edit"}},
		},
	}
	b2, _ := json.Marshal(ccBody)
	rawResp2, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "omp-explicit-invalid-004b", "openai", model, b2, headers2))
	body2, respHeaders2, clearHeaders2 := decodeEnvelopeRequestIntercept(t, rawResp2)
	if !strings.Contains(string(body2), `"name":"run_command"`) {
		t.Fatalf("invalid explicit value must fall back to body Client Gate and cloak claude_code, got: %s", body2)
	}
	if !containsStr(clearHeaders2, explicitClientHeader) {
		t.Fatalf("invalid value must still consume header, ClearHeaders=%v", clearHeaders2)
	}
	if headerContainsFold(respHeaders2, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders2)
	}
}

// TestIntegration_ExplicitClient_ModelGateSkipsCloakButClearsHeader proves the
// Model Gate stays authoritative: a non-matching model is not cloaked even with
// a valid override, yet the plugin-owned control header still must not leak.
func TestIntegration_ExplicitClient_ModelGateSkipsCloakButClearsHeader(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")))
	reqID := "omp-explicit-modelgate-005"
	model := "non-agy/gpt"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "oh_my_pi")
	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(clientReq)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) != 0 {
		t.Fatalf("model gate must not cloak non-matching model, got body: %s", body)
	}
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("model gate must still clear the control header, ClearHeaders=%v", clearHeaders)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}
}

// TestIntegration_ExplicitClient_HeaderConsumedBeforeUpstream proves the owned
// header never reaches upstream for both valid and invalid values while
// authorization and unrelated headers survive unchanged.
func TestIntegration_ExplicitClient_HeaderConsumedBeforeUpstream(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	cases := []struct {
		name string
		val  string
	}{
		{"valid", "oh_my_pi"},
		{"invalid", "opencode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqID := "omp-explicit-upstream-" + tc.name
			model := "agy/gemini-3.7-flash"
			headers := http.Header{}
			headers.Set(explicitClientHeader, tc.val)
			headers.Set("Authorization", "Bearer secret-token")
			headers.Set("X-Custom", "kept")

			clientReq := map[string]any{
				"model":    model,
				"messages": []any{map[string]any{"role": "user", "content": "read"}},
				"tools": []any{
					map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
				},
			}
			b, _ := json.Marshal(clientReq)

			received := make(chan http.Header, 1)
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- r.Header.Clone()
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer mock.Close()

			rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
			_, respHeaders, _ := decodeEnvelopeRequestIntercept(t, rawResp)

			// Model the before-auth host contract: resp.Headers, when set, is the
			// final request header set (replacing the original); otherwise the
			// original request headers are kept.
			upHeaders := headers.Clone()
			if respHeaders != nil && len(respHeaders) > 0 {
				upHeaders = respHeaders.Clone()
			}
			req, err := http.NewRequest(http.MethodPost, mock.URL, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("build upstream request: %v", err)
			}
			req.Header = upHeaders
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post to mock upstream: %v", err)
			}
			resp.Body.Close()

			got := <-received
			if got.Get(explicitClientHeader) != "" {
				t.Fatalf("X-Cloak-Client leaked upstream: %v", got)
			}
			if got.Get("Authorization") != "Bearer secret-token" {
				t.Fatalf("Authorization not preserved: %v", got)
			}
			if got.Get("X-Custom") != "kept" {
				t.Fatalf("unrelated header not preserved: %v", got)
			}
		})
	}
}

// TestIntegration_ExplicitClient_AnthropicFormat proves explicit client
// selection works through the Anthropic-shaped request walker too.
func TestIntegration_ExplicitClient_AnthropicFormat(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "omp-explicit-anthropic-007"
	model := "agy/gemini-3.7-flash"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "oh_my_pi")

	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"name": "read", "description": "Read"},
		},
	}
	b, _ := json.Marshal(clientReq)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", model, b, headers))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"view_file"`) {
		t.Fatalf("anthropic request not cloaked to view_file: %s", body)
	}

	respBody := []byte(`{"content":[{"type":"tool_use","id":"tu1","name":"view_file","input":{}}]}`)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", model, respBody))
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("anthropic response did not uncloak view_file to read: %s", out)
	}
}

// TestIntegration_ExplicitClient_EmptyPrefixesAllowsAllModels proves the
// default empty model_prefixes preserves all-model eligibility with a valid
// explicit override.
func TestIntegration_ExplicitClient_EmptyPrefixesAllowsAllModels(t *testing.T) {
	defer restoreDefaultFilterConfig(t) // empty model_prefixes
	reqID := "omp-explicit-empty-prefix-008"
	model := "some/unrelated-model"

	headers := http.Header{}
	headers.Set(explicitClientHeader, "oh_my_pi")
	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(clientReq)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"view_file"`) {
		t.Fatalf("empty model_prefixes must allow cloaking for any model, got: %s", body)
	}
}

// TestIntegration_ExplicitClient_CaseInsensitiveHeaderKey proves the header is
// captured case-insensitively even when the JSON transport preserved a
// non-canonical key spelling, and that the matched key (not the assumed
// canonical spelling) is what the host is told to clear.
func TestIntegration_ExplicitClient_CaseInsensitiveHeaderKey(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "omp-explicit-lowercase-key-009"
	model := "agy/gemini-3.7-flash"

	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(clientReq)

	// Supply the header key in non-canonical casing, as a raw transport could.
	headers := http.Header(map[string][]string{"x-cloak-client": {"oh_my_pi"}})
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"view_file"`) {
		t.Fatalf("non-canonical header key must still cloak: %s", body)
	}
	if len(clearHeaders) != 1 || !strings.EqualFold(clearHeaders[0], explicitClientHeader) {
		t.Fatalf("ClearHeaders=%v, want one case-insensitive match for %q", clearHeaders, explicitClientHeader)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("non-canonical X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}
}

// TestIntegration_ExplicitClient_DuplicateCasingKeysCleared proves that when the
// ABI payload carries multiple differently-cased spellings of the owned header,
// every variant is cleared and selection is deterministic.
func TestIntegration_ExplicitClient_DuplicateCasingKeysCleared(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "omp-explicit-duplicate-case-010"
	model := "agy/gemini-3.7-flash"

	clientReq := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "read"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	b, _ := json.Marshal(clientReq)

	headers := http.Header(map[string][]string{
		"X-Cloak-Client": {"oh_my_pi"},
		"x-cloak-client": {"oh_my_pi"},
	})
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"view_file"`) {
		t.Fatalf("duplicate-case header must still cloak: %s", body)
	}
	if len(clearHeaders) != 2 {
		t.Fatalf("ClearHeaders=%v, want both duplicate casings cleared", clearHeaders)
	}
	for _, h := range clearHeaders {
		if !strings.EqualFold(h, explicitClientHeader) {
			t.Fatalf("ClearHeaders=%v contains non-owned key %q", clearHeaders, h)
		}
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("duplicate-case X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}
}
