package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandlePluginCallRegisterDeclaresRequestInterceptor(t *testing.T) {
	raw, code := handlePluginCall("plugin.register", nil)
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}

	var envelope map[string]any
	mustUnmarshalJSON(t, raw, &envelope)
	if envelope["ok"] != true {
		t.Fatalf("ok = %#v, want true", envelope["ok"])
	}
	result := envelope["result"].(map[string]any)
	metadata := result["metadata"].(map[string]any)
	if metadata["GitHubRepository"] != pluginRepository {
		t.Fatalf("GitHubRepository = %#v, want %q", metadata["GitHubRepository"], pluginRepository)
	}
	capabilities := result["capabilities"].(map[string]any)
	if capabilities["model_router"] == true {
		t.Fatalf("model_router = %#v, want false", capabilities["model_router"])
	}
	if capabilities["executor"] == true {
		t.Fatalf("executor = %#v, want false", capabilities["executor"])
	}
	if capabilities["request_interceptor"] != true {
		t.Fatalf("request_interceptor = %#v, want true", capabilities["request_interceptor"])
	}
	if capabilities["response_interceptor"] != true {
		t.Fatalf("response_interceptor = %#v, want true", capabilities["response_interceptor"])
	}
	if capabilities["stream_chunk_interceptor"] != true {
		t.Fatalf("stream_chunk_interceptor = %#v, want true", capabilities["stream_chunk_interceptor"])
	}
	fields := result["metadata"].(map[string]any)["ConfigFields"].([]any)
	if !hasConfigField(fields, "use_default_keywords", "boolean") {
		t.Fatalf("ConfigFields = %#v, want boolean use_default_keywords", fields)
	}
	if !hasConfigField(fields, "custom_mappings", "object") {
		t.Fatalf("ConfigFields = %#v, want object custom_mappings", fields)
	}
	if !hasConfigField(fields, "tool_mappings", "object") {
		t.Fatalf("ConfigFields = %#v, want object tool_mappings", fields)
	}
}

func TestReconfigureWithToolMappingsOverride(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
tool_mappings:
  claude_code:
    bash: run_command
    my_custom_tool: ask_permission
  codex:
    shell_command: run_command
`)))
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}
	cfg := activeFilterConfig()
	// Verify ToolMappings parsed correctly
	if cfg.ToolMappings["claude_code"]["my_custom_tool"] != "ask_permission" {
		t.Fatalf("expected custom tool mapping")
	}
}

func TestHandlePluginCallReconfigureAppliesCustomMappingsAndDefaultToggle(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
enabled: true
priority: 1
use_default_keywords: false
custom_mappings:
  Cursor: Antigravity
  Windsurf: Antigravity
`)))
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}

	if got, rewritten := rewriteRequestBody([]byte(`{"system":"You are Codex."}`), "openai"); rewritten {
		t.Fatalf("Codex rewritten after disabling defaults; body=%s", got)
	}
	got, rewritten := rewriteRequestBody([]byte(`{"system":"route this Cursor session"}`), "openai")
	if !rewritten {
		t.Fatalf("Cursor rewritten = false, want true")
	}
	if !strings.Contains(string(got), "route this Antigravity session") {
		t.Fatalf("body = %s, want Cursor replaced with Antigravity", got)
	}
}

func TestHandlePluginCallReconfigureAcceptsDelimitedCustomMappings(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
custom_mappings: |
  Cursor: Antigravity
  Windsurf: Antigravity
  JetBrains AI: Antigravity
`)))
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}

	tests := []string{"Cursor", "Windsurf", "JetBrains AI"}
	for _, keyword := range tests {
		t.Run(keyword, func(t *testing.T) {
			got, rewritten := rewriteRequestBody([]byte(`{"system":"route this ` + keyword + ` session"}`), "openai")
			if !rewritten {
				t.Fatalf("%s rewritten = false, want true", keyword)
			}
			if !strings.Contains(string(got), "Antigravity") {
				t.Fatalf("body = %s, want replacement", got)
			}
		})
	}
}

func TestHandlePluginCallReconfigureKeepsPreviousConfigOnInvalidInput(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
use_default_keywords: false
custom_mappings:
  Cursor: Antigravity
`)))
	if code != 0 {
		t.Fatalf("initial reconfigure code = %d, want 0; body=%s", code, raw)
	}

	raw, code = handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
custom_mappings:
  - nested: invalid
`)))
	if code != 0 {
		t.Fatalf("invalid reconfigure code = %d, want 0 handled error envelope; body=%s", code, raw)
	}

	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if envelope.OK {
		t.Fatalf("ok = true, want false")
	}
	if envelope.Error.Code != "invalid_config" {
		t.Fatalf("error code = %q, want invalid_config", envelope.Error.Code)
	}

	if _, rewritten := rewriteRequestBody([]byte(`{"system":"route this Cursor session"}`), "openai"); !rewritten {
		t.Fatalf("Cursor rewritten = false after invalid config, want previous config retained")
	}
	if got, rewritten := rewriteRequestBody([]byte(`{"system":"You are Codex."}`), "openai"); rewritten {
		t.Fatalf("Codex rewritten after invalid config, want previous disabled-default state retained; body=%s", got)
	}
}

func TestHandlePluginCallRequestInterceptBeforeRewritesCodingSignals(t *testing.T) {
	request := requestInterceptRequestJSON(t, `{"system":"You are Codex.","messages":[]}`)

	raw, code := handlePluginCall("request.intercept_before", request)
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("ok = false, want true")
	}
	body, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(string(body), "You are Antigravity.") {
		t.Fatalf("body = %s, want rewritten system", body)
	}
}

func TestHandlePluginCallRequestInterceptBeforePassesCleanRequests(t *testing.T) {
	request := requestInterceptRequestJSON(t, `{"system":"You are Antigravity.","messages":[]}`)

	raw, code := handlePluginCall("request.intercept_before", request)
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("ok = false, want true")
	}
	if envelope.Result.Body != "" {
		t.Fatalf("Body = %q, want empty body to keep original request", envelope.Result.Body)
	}
}

func TestHandlePluginCallUnknownMethodReturnsErrorEnvelope(t *testing.T) {
	raw, code := handlePluginCall("unknown.method", nil)
	if code != 0 {
		t.Fatalf("code = %d, want 0 for handled error envelope; body=%s", code, raw)
	}

	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if envelope.OK {
		t.Fatalf("ok = true, want false")
	}
	if envelope.Error.Code != "unknown_method" {
		t.Fatalf("error code = %q, want unknown_method", envelope.Error.Code)
	}
}

func requestInterceptRequestJSON(t *testing.T, body string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"SourceFormat":   "openai",
		"ToFormat":       "",
		"Model":          "antigravity/test",
		"RequestedModel": "antigravity/test",
		"Body":           []byte(body),
	})
	if err != nil {
		t.Fatalf("marshal request intercept request: %v", err)
	}
	return raw
}

func lifecycleRequestJSON(t *testing.T, configYAML []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"config_yaml":    configYAML,
	})
	if err != nil {
		t.Fatalf("marshal lifecycle request: %v", err)
	}
	return raw
}

func hasConfigField(fields []any, name, fieldType string) bool {
	for _, field := range fields {
		object, ok := field.(map[string]any)
		if !ok {
			continue
		}
		if object["Name"] == name && object["Type"] == fieldType {
			return true
		}
	}
	return false
}

func restoreDefaultFilterConfig(t *testing.T) {
	t.Helper()
	applyFilterConfig(defaultFilterConfig())
}

func mustUnmarshalJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}

func TestResponseInterceptReversesClaudeCodeCloak(t *testing.T) {
	// Build a request body with Claude Code tools
	reqBody := `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"edit"}}],"messages":[]}`
	// Build a response body with cloaked tool call
	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`

	request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	raw, code := handlePluginCall("response.intercept_after", request)
	if code != 0 {
		t.Fatalf("code = %d; body=%s", code, raw)
	}

	// Parse response → verify tool_calls[].function.name == "bash" (uncloaked)
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("envelope not OK")
	}

	decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	var resp map[string]any
	mustUnmarshalJSON(t, decoded, &resp)

	choices := resp["choices"].([]any)
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	fn := toolCall["function"].(map[string]any)
	name := fn["name"].(string)

	if name != "bash" {
		t.Fatalf("expected tool call function name to be 'bash', got %q", name)
	}
}

func TestResponseInterceptReversesCodexCloak(t *testing.T) {
	reqBody := `{"tools":[{"type":"function","function":{"name":"shell_command"}},{"type":"function","function":{"name":"apply_patch"}}],"messages":[]}`
	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`

	request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	raw, code := handlePluginCall("response.intercept_after", request)
	if code != 0 {
		t.Fatalf("code = %d; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("envelope not OK")
	}

	decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	var resp map[string]any
	mustUnmarshalJSON(t, decoded, &resp)

	choices := resp["choices"].([]any)
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	fn := toolCall["function"].(map[string]any)
	name := fn["name"].(string)

	if name != "shell_command" {
		t.Fatalf("expected tool call function name to be 'shell_command', got %q", name)
	}
}

func TestResponseInterceptDoesNotCorruptProse(t *testing.T) {
	// Verify that "run_command" appearing in assistant text is NOT replaced
	reqBody := `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"edit"}}],"messages":[]}`
	respBody := `{"choices":[{"message":{"content":"You can use run_command to execute..."}}]}`

	request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	raw, code := handlePluginCall("response.intercept_after", request)
	if code != 0 {
		t.Fatalf("code = %d; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("envelope not OK")
	}

	// Should be no-op because it didn't change anything, so Body should be empty
	if envelope.Result.Body != "" {
		decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
		if err != nil {
			t.Fatalf("decode base64: %v", err)
		}
		t.Fatalf("expected no changes (empty Body), but got: %s", string(decoded))
	}
}

func TestStreamChunkInterceptReversesCloak(t *testing.T) {
	reqBody := `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"edit"}}],"messages":[]}`
	chunkBody := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"

	request := streamChunkInterceptRequestJSON(t, reqBody, chunkBody, "openai")
	raw, code := handlePluginCall("response.intercept_stream_chunk", request)
	if code != 0 {
		t.Fatalf("code = %d; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("envelope not OK")
	}

	decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	chunkStr := string(decoded)
	if !strings.HasPrefix(chunkStr, "data: ") {
		t.Fatalf("expected stream chunk to start with 'data: ', got %q", chunkStr)
	}
	dataJSON := strings.TrimPrefix(chunkStr, "data: ")
	var resp map[string]any
	mustUnmarshalJSON(t, []byte(dataJSON), &resp)

	choices := resp["choices"].([]any)
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	toolCalls := delta["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	fn := toolCall["function"].(map[string]any)
	name := fn["name"].(string)

	if name != "bash" {
		t.Fatalf("expected delta tool call function name to be 'bash', got %q", name)
	}
}

func TestResponseInterceptPassesThroughAntigravity(t *testing.T) {
	reqBody := `{"tools":[{"type":"function","function":{"name":"ask_permission"}}],"messages":[]}`
	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`

	request := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	raw, code := handlePluginCall("response.intercept_after", request)
	if code != 0 {
		t.Fatalf("code = %d; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("envelope not OK")
	}

	if envelope.Result.Body != "" {
		t.Fatalf("expected empty Body for passthrough (Antigravity tools present in request), got: %s", envelope.Result.Body)
	}
}

func TestResponseInterceptAnthropicFormat(t *testing.T) {
	reqBody := `{"tools":[{"name":"bash"},{"name":"read"},{"name":"edit"}],"messages":[]}`
	respBody := `{"content":[{"type":"tool_use","id":"tu1","name":"run_command","input":{}}]}`

	request := responseInterceptRequestJSON(t, reqBody, respBody, "anthropic")
	raw, code := handlePluginCall("response.intercept_after", request)
	if code != 0 {
		t.Fatalf("code = %d; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("envelope not OK")
	}

	decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	var resp map[string]any
	mustUnmarshalJSON(t, decoded, &resp)

	content := resp["content"].([]any)
	block := content[0].(map[string]any)
	name := block["name"].(string)

	if name != "bash" {
		t.Fatalf("expected tool_use name to be 'bash', got %q", name)
	}
}

func responseInterceptRequestJSON(t *testing.T, reqBody, respBody, sourceFormat string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"SourceFormat": sourceFormat,
		"RequestBody":  []byte(reqBody),
		"Body":         []byte(respBody),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func streamChunkInterceptRequestJSON(t *testing.T, reqBody, chunkBody, sourceFormat string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"SourceFormat": sourceFormat,
		"RequestBody":  []byte(reqBody),
		"Body":         []byte(chunkBody),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
