package cloak

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	rawResp := HandleRequestInterceptBefore(payload)
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("ClearHeaders=%v, want to include %q", clearHeaders, explicitClientHeader)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}
	if !strings.Contains(string(body), `"name":"read_file"`) {
		t.Fatalf("request not cloaked to read_file: %s", body)
	}
	if strings.Contains(string(body), `"name":"read"`) {
		t.Fatalf("request leaked read: %s", body)
	}

	// Non-streaming response path must uncloak using req-time session truth.
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":"{}"}}]}}]}`)
	rawResp2 := HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	respOut := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(respOut), `"name":"read"`) {
		t.Fatalf("response did not uncloak read_file to read: %s", respOut)
	}
	if strings.Contains(string(respOut), "read_file") {
		t.Fatalf("response leaked read_file: %s", respOut)
	}

	// Streaming response path must uncloak the same way.
	chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"read_file","arguments":"{}"}}]}}]}`)
	rawChunk := HandleStreamChunkIntercept(makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, chunkBody, nil))
	chunkOut := decodeEnvelopeBody(t, rawChunk)
	if !strings.Contains(string(chunkOut), `"name":"read"`) {
		t.Fatalf("stream did not uncloak read_file to read: %s", chunkOut)
	}
	if strings.Contains(string(chunkOut), "read_file") {
		t.Fatalf("stream leaked read_file: %s", chunkOut)
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
			rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
			body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
			if !strings.Contains(string(body), `"name":"read_file"`) {
				t.Fatalf("alias %q did not cloak read to read_file: %s", alias, body)
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
		"system":   "You are Claude Code, Anthropic's official CLI.",
		"messages": []any{map[string]any{"role": "user", "content": "run"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, clientReqBytes, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("ClearHeaders=%v, want to include %q", clearHeaders, explicitClientHeader)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}
	if !strings.Contains(string(body), `"name":"run_command"`) {
		t.Fatalf("expected request cloak bash -> run_command, got: %s", body)
	}

	// The response must be uncloaked via the oh_my_pi table (bash, lowercase),
	// not the claude_code table (Bash), proving the override won.
	respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
	rawResp2 := HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
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
	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, "omp-explicit-invalid-004a", "openai", model, b, headers))
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
	rawResp2 := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, "omp-explicit-invalid-004b", "openai", model, b2, headers2))
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
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")), nil)
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
	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
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

			rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
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
	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", model, b, headers))
	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"read_file"`) {
		t.Fatalf("anthropic request not cloaked to read_file: %s", body)
	}

	respBody := []byte(`{"content":[{"type":"tool_use","id":"tu1","name":"read_file","input":{}}]}`)
	rawResp2 := HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", model, respBody))
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("anthropic response did not uncloak read_file to read: %s", out)
	}
}

// TestIntegration_ExplicitClient_EmptyPrefixesAllowsAllModels proves the
// default empty model_prefixes allows all models on the ProtectedAGY route,
// cloaking tools on request and uncloaking them on response.
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
	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if len(body) == 0 {
		t.Fatal("empty model_prefixes must cloak explicit OMP request on any model")
	}
	var gotReq map[string]any
	mustUnmarshalJSON(t, body, &gotReq)
	toolFn := gotReq["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"]
	if toolFn != "read_file" {
		t.Fatalf("cloaked tool = %v, want read_file", toolFn)
	}
	if !containsStr(clearHeaders, explicitClientHeader) {
		t.Fatalf("ClearHeaders=%v, want to include %q", clearHeaders, explicitClientHeader)
	}
	if headerContainsFold(respHeaders, explicitClientHeader) {
		t.Fatalf("X-Cloak-Client still present in resp.Headers: %v", respHeaders)
	}

	respBody := []byte(`{"choices":[{"message":{"content":"Hello Antigravity world","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}}]}`)
	rawResp2 := HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody))
	out := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out), `"name":"read"`) || !strings.Contains(string(out), "Hello omp world") {
		t.Fatalf("correlated response did not restore read/omp, got: %s", out)
	}
}

// TestIntegration_ExplicitClient_CustomModelPrefixGatesProtectedRoute proves
// ProtectedAGY, User-Agent OMP sanitization, and ExplicitOMPNonAGYBypass follow
// configured model_prefixes (e.g. ["monet/"] or ["monet"]) without any
// hardcoded "agy/" assumption.
func TestIntegration_ExplicitClient_CustomModelPrefixGatesProtectedRoute(t *testing.T) {
	for _, prefix := range []string{"monet/", "monet"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			HandlePluginLifecycle(lifecycleRequestJSON(t, []byte("use_default_keywords: true\nmodel_prefixes:\n  - "+prefix+"\n")), nil)

			// 1. monet/* with explicit X-Cloak-Client: oh_my_pi -> ProtectedAGY + sanitization + reverse
			reqID1 := "custom-prefix-protected-" + prefix
			h1 := http.Header{}
			h1.Set(explicitClientHeader, "oh_my_pi")
			body1, _ := json.Marshal(map[string]any{
				"model": "monet/gemini-4-hydrogen",
				"messages": []any{
					map[string]any{"role": "system", "content": "<system-conventions>You use Oh My Pi.</system-conventions>"},
					map[string]any{"role": "user", "content": "read"},
				},
				"tools": []any{
					map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
				},
			})
			raw1 := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID1, "openai", "monet/gemini-4-hydrogen", body1, h1))
			out1, _, clear1 := decodeEnvelopeRequestIntercept(t, raw1)
			if !containsStr(clear1, explicitClientHeader) {
				t.Fatalf("expected %s cleared, got %v", explicitClientHeader, clear1)
			}
			s1 := string(out1)
			if !strings.Contains(s1, "<conventions>") || strings.Contains(s1, "<system-conventions>") || !strings.Contains(s1, `"name":"read_file"`) {
				t.Fatalf("monet/* protected request not cloaked/sanitized: %s", s1)
			}
			resp1 := []byte(`{"choices":[{"message":{"content":"Hello Antigravity","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}}]}`)
			rev1 := decodeEnvelopeBody(t, HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID1, "openai", "monet/gemini-4-hydrogen", resp1)))
			if !strings.Contains(string(rev1), `"name":"read"`) || !strings.Contains(string(rev1), "Hello omp") {
				t.Fatalf("monet/* response not restored: %s", rev1)
			}

			// 2. monet/* with User-Agent: omp/... -> sanitized + cloaked
			reqID2 := "custom-prefix-ua-" + prefix
			h2 := http.Header{}
			h2.Set("User-Agent", "omp/19.2.1")
			raw2 := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID2, "openai", "monet/gemini-4-hydrogen", body1, h2))
			out2, _, _ := decodeEnvelopeRequestIntercept(t, raw2)
			s2 := string(out2)
			if !strings.Contains(s2, "<conventions>") || strings.Contains(s2, "<system-conventions>") || !strings.Contains(s2, `"name":"read_file"`) {
				t.Fatalf("monet/* UA request not cloaked/sanitized: %s", s2)
			}

			// 3. agy/* outside model_prefixes with X-Cloak-Client: oh_my_pi -> durable bypass (zero mutation)
			reqID3 := "custom-prefix-agy-bypass-" + prefix
			body3, _ := json.Marshal(map[string]any{
				"model": "agy/gemini-4-hydrogen",
				"messages": []any{
					map[string]any{"role": "system", "content": "<system-conventions>You use Oh My Pi.</system-conventions>"},
				},
				"tools": []any{
					map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
				},
			})
			raw3 := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID3, "openai", "agy/gemini-4-hydrogen", body3, h1))
			out3, _, clear3 := decodeEnvelopeRequestIntercept(t, raw3)
			if len(out3) != 0 {
				t.Fatalf("agy/* outside model_prefixes must bypass with zero mutation, got: %s", out3)
			}
			if !containsStr(clear3, explicitClientHeader) {
				t.Fatalf("bypass must still clear %s, got %v", explicitClientHeader, clear3)
			}
			resp3 := []byte(`{"choices":[{"message":{"content":"Hello Antigravity"}}]}`)
			if rev3 := decodeEnvelopeBody(t, HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID3, "openai", "agy/gemini-4-hydrogen", resp3))); len(rev3) != 0 {
				t.Fatalf("agy/* bypass response must not mutate, got: %s", rev3)
			}

			// 4. agy/* outside model_prefixes with User-Agent: omp/... -> gate skip (zero mutation)
			raw4 := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, "custom-prefix-agy-ua-"+prefix, "openai", "agy/gemini-4-hydrogen", body3, h2))
			if out4, _, _ := decodeEnvelopeRequestIntercept(t, raw4); len(out4) != 0 {
				t.Fatalf("agy/* UA outside model_prefixes must skip, got: %s", out4)
			}

			// 5. SSE stream on monet/* restores read_file -> read and Antigravity -> omp
			streamChunk := []byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello Antigravity\",\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			streamOut := decodeEnvelopeBody(t, HandleStreamChunkIntercept(makeIntegrationStreamChunkPayload(t, reqID1, "openai", "monet/gemini-4-hydrogen", 0, streamChunk, nil)))
			if !strings.Contains(string(streamOut), `"name":"read"`) || !strings.Contains(string(streamOut), "Hello omp") {
				t.Fatalf("monet/* SSE stream not restored: %s", streamOut)
			}

			// 6. Conflicting marker containing OMP: 503 on monet/* (inside model_prefixes), bypass on agy/* (outside model_prefixes)
			hConflict := http.Header{}
			hConflict.Set(explicitClientHeader, "oh_my_pi, claude_code")
			rawConflictIn := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, "custom-prefix-conflict-in-"+prefix, "openai", "monet/gemini-4-hydrogen", body1, hConflict))
			var envConflictIn struct {
				Result pluginapi.RequestInterceptResponse `json:"result"`
			}
			mustUnmarshalJSON(t, rawConflictIn, &envConflictIn)
			if !envConflictIn.Result.Terminate || envConflictIn.Result.StatusCode != 503 || !strings.Contains(string(envConflictIn.Result.ResponseBody), "omp_cloak_required") {
				t.Fatalf("monet/* conflicting marker must fail closed 503, got: %+v", envConflictIn.Result)
			}
			if !containsStr(envConflictIn.Result.ClearHeaders, explicitClientHeader) {
				t.Fatalf("monet/* conflicting 503 must clear %s, got %v", explicitClientHeader, envConflictIn.Result.ClearHeaders)
			}

			reqIDConflictOut := "custom-prefix-conflict-out-" + prefix
			rawConflictOut := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqIDConflictOut, "openai", "agy/gemini-4-hydrogen", body3, hConflict))
			outConflictOut, _, clearConflictOut := decodeEnvelopeRequestIntercept(t, rawConflictOut)
			if len(outConflictOut) != 0 {
				t.Fatalf("agy/* conflicting marker outside model_prefixes must bypass with zero mutation, got: %s", outConflictOut)
			}
			if !containsStr(clearConflictOut, explicitClientHeader) {
				t.Fatalf("agy/* conflicting bypass must clear %s, got %v", explicitClientHeader, clearConflictOut)
			}
			if revConflictOut := decodeEnvelopeBody(t, HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqIDConflictOut, "openai", "agy/gemini-4-hydrogen", resp3))); len(revConflictOut) != 0 {
				t.Fatalf("agy/* conflicting bypass response must not mutate, got: %s", revConflictOut)
			}
		})
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
	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"read_file"`) {
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
	rawResp := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, b, headers))
	body, respHeaders, clearHeaders := decodeEnvelopeRequestIntercept(t, rawResp)
	if !strings.Contains(string(body), `"name":"read_file"`) {
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
