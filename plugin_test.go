package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
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
	if capabilities["response_stream_interceptor"] != true {
		t.Fatalf("response_stream_interceptor = %#v, want true", capabilities["response_stream_interceptor"])
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
	if !hasConfigField(fields, "omp_prompt_sanitizations", "object") {
		t.Fatalf("ConfigFields = %#v, want object omp_prompt_sanitizations", fields)
	}
}

func TestHandlePluginCallLifecycleInvalidConfigReturnsErrorEnvelope(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, method := range []string{"plugin.register", "plugin.reconfigure"} {
		raw, code := handlePluginCall(method, lifecycleRequestJSON(t, []byte("custom_mappings:\n  - nested: invalid\n")))
		if code != 0 {
			t.Fatalf("%s code = %d, want 0; body=%s", method, code, raw)
		}
		var envelope struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		mustUnmarshalJSON(t, raw, &envelope)
		if envelope.OK || envelope.Error.Code != "invalid_config" {
			t.Fatalf("%s got ok=%v code=%q, want ok=false code=invalid_config", method, envelope.OK, envelope.Error.Code)
		}
	}
}

func TestHandlePluginCallRequestInterceptAfterAndCompleteRoutes(t *testing.T) {
	rawAfter, codeAfter := handlePluginCall("request.intercept_after", nil)
	if codeAfter != 0 {
		t.Fatalf("request.intercept_after code = %d, want 0; body=%s", codeAfter, rawAfter)
	}
	var envAfter struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, rawAfter, &envAfter)
	if !envAfter.OK {
		t.Fatalf("request.intercept_after ok = false; body=%s", rawAfter)
	}

	compReq, err := json.Marshal(map[string]any{"RequestID": "root-complete-check", "Outcome": "succeeded"})
	if err != nil {
		t.Fatalf("marshal completion: %v", err)
	}
	rawComp, codeComp := handlePluginCall("request.complete", compReq)
	if codeComp != 0 {
		t.Fatalf("request.complete code = %d, want 0; body=%s", codeComp, rawComp)
	}
	var envComp struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, rawComp, &envComp)
	if !envComp.OK {
		t.Fatalf("request.complete ok = false; body=%s", rawComp)
	}
}

func TestHandlePluginCallRequestInterceptBeforeRewritesCodingSignals(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "coding-signals-rewrite"
	body := `{"system":"You are Codex.","messages":[],"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"request_user_input"}}]}`
	interceptRaw, _ := json.Marshal(map[string]any{
		"RequestID":      reqID,
		"SourceFormat":   "openai",
		"Model":          "antigravity/test",
		"RequestedModel": "antigravity/test",
		"Body":           []byte(body),
	})

	raw, code := handlePluginCall("request.intercept_before", interceptRaw)
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
		t.Fatalf("ok = false, want true; raw=%s", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(string(decoded), "You are Antigravity.") {
		t.Fatalf("body = %s, want rewritten system", decoded)
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

func TestHandlePluginCallRegisterVersionMatchesRegistry(t *testing.T) {
	semverRe := regexp.MustCompile(`^\d+\.\d+\.\d+$`)

	raw, code := handlePluginCall("plugin.register", nil)
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Metadata struct {
				Version string `json:"Version"`
			} `json:"metadata"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if !envelope.OK {
		t.Fatalf("ok = false, want true; raw=%s", raw)
	}

	registryRaw, err := os.ReadFile("registry.json")
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	var registry struct {
		Plugins []struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	mustUnmarshalJSON(t, registryRaw, &registry)
	if len(registry.Plugins) == 0 {
		t.Fatalf("registry.json plugins is empty")
	}

	gotMeta := envelope.Result.Metadata.Version
	gotReg := registry.Plugins[0].Version
	if !semverRe.MatchString(gotMeta) || gotMeta == "0.6.0" {
		t.Errorf("Metadata.Version = %q, want bumped semver", gotMeta)
	}
	if gotMeta != pluginVersion || gotReg != gotMeta {
		t.Errorf("version mismatch: pluginVersion=%q Metadata.Version=%q registry.json=%q", pluginVersion, gotMeta, gotReg)
	}
}

func TestResponseInterceptReversesCodexCloakForCorrelatedRequest(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "codex-correlated-non-stream"
	interceptRaw, err := json.Marshal(map[string]any{
		"RequestID":      reqID,
		"SourceFormat":   "openai",
		"Model":          "antigravity/test",
		"RequestedModel": "antigravity/test",
		"Body":           []byte(`{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"collaboration__list_agents"}}],"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("marshal request intercept request: %v", err)
	}
	if raw, code := handlePluginCall("request.intercept_before", interceptRaw); code != 0 {
		t.Fatalf("request intercept code = %d; body=%s", code, raw)
	}

	cloakedReq := `{"tools":[{"type":"function","function":{"name":"run_command"}},{"type":"function","function":{"name":"wp_list_workers"}}],"messages":[]}`
	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}},{"function":{"name":"wp_list_workers","arguments":"{}"}}]}}]}`
	request, err := json.Marshal(map[string]any{
		"RequestID":       reqID,
		"SourceFormat":    "openai",
		"OriginalRequest": []byte(cloakedReq),
		"RequestBody":     []byte(cloakedReq),
		"Body":            []byte(respBody),
	})
	if err != nil {
		t.Fatalf("marshal response intercept request: %v", err)
	}

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
	if envelope.Result.Body == "" {
		t.Fatalf("correlated codex response was not reversed: %s", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	body := string(decoded)
	if !strings.Contains(body, `"name":"exec"`) || !strings.Contains(body, `"name":"collaboration__list_agents"`) {
		t.Fatalf("declared codex sources were not restored: %s", body)
	}
	if strings.Contains(body, "run_command") || strings.Contains(body, "wp_list_workers") {
		t.Fatalf("cloaked target leaked downstream: %s", body)
	}
}

func TestResponseInterceptLeavesNativeTargetAloneForCorrelatedRequest(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "codex-native-target-correlated"
	interceptRaw, err := json.Marshal(map[string]any{
		"RequestID":      reqID,
		"SourceFormat":   "openai",
		"Model":          "antigravity/test",
		"RequestedModel": "antigravity/test",
		"Body":           []byte(`{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"request_user_input"}},{"type":"function","function":{"name":"search_web"}}],"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("marshal request intercept request: %v", err)
	}
	if raw, code := handlePluginCall("request.intercept_before", interceptRaw); code != 0 {
		t.Fatalf("request intercept code = %d; body=%s", code, raw)
	}

	cloakedReq := `{"tools":[{"type":"function","function":{"name":"run_command"}},{"type":"function","function":{"name":"ask_question"}},{"type":"function","function":{"name":"search_web"}}],"messages":[]}`
	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}},{"function":{"name":"search_web","arguments":"{}"}}]}}]}`
	request, err := json.Marshal(map[string]any{
		"RequestID":       reqID,
		"SourceFormat":    "openai",
		"OriginalRequest": []byte(cloakedReq),
		"RequestBody":     []byte(cloakedReq),
		"Body":            []byte(respBody),
	})
	if err != nil {
		t.Fatalf("marshal response intercept request: %v", err)
	}

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
	body := respBody
	if envelope.Result.Body != "" {
		decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
		if err != nil {
			t.Fatalf("decode base64: %v", err)
		}
		body = string(decoded)
	}

	var resp map[string]any
	mustUnmarshalJSON(t, []byte(body), &resp)
	choices := resp["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	names := map[string]bool{}
	for _, tcRaw := range message["tool_calls"].([]any) {
		fn := tcRaw.(map[string]any)["function"].(map[string]any)
		names[fn["name"].(string)] = true
	}
	if !names["exec"] {
		t.Fatalf("declared codex source was not restored: %s", body)
	}
	if !names["search_web"] || len(names) != 2 {
		t.Fatalf("native AGY target was rewritten to a name the client never declared: %s", body)
	}
}

func TestResponseInterceptDoesNotCorruptProse(t *testing.T) {
	reqBody := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}},{"type":"function","function":{"name":"Edit"}}],"messages":[]}`
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

	if envelope.Result.Body != "" {
		decoded, err := base64.StdEncoding.DecodeString(envelope.Result.Body)
		if err != nil {
			t.Fatalf("decode base64: %v", err)
		}
		t.Fatalf("expected no changes (empty Body), but got: %s", string(decoded))
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
	raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, nil))
	if code != 0 {
		t.Fatalf("restore default config code = %d; body=%s", code, raw)
	}
}

func mustUnmarshalJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
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
