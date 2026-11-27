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

func TestHandlePluginCallResponseAndStreamRoutes(t *testing.T) {
	respReq, err := json.Marshal(map[string]any{
		"SourceFormat": "openai",
		"Body":         []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
	})
	if err != nil {
		t.Fatalf("marshal response request: %v", err)
	}
	rawResp, codeResp := handlePluginCall("response.intercept_after", respReq)
	if codeResp != 0 {
		t.Fatalf("response.intercept_after code = %d, want 0; body=%s", codeResp, rawResp)
	}
	var envResp struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, rawResp, &envResp)
	if !envResp.OK {
		t.Fatalf("response.intercept_after ok = false; body=%s", rawResp)
	}

	streamReq, err := json.Marshal(map[string]any{
		"SourceFormat": "openai",
		"ChunkIndex":   0,
		"Chunk":        []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"),
	})
	if err != nil {
		t.Fatalf("marshal stream request: %v", err)
	}
	rawStream, codeStream := handlePluginCall("response.intercept_stream_chunk", streamReq)
	if codeStream != 0 {
		t.Fatalf("response.intercept_stream_chunk code = %d, want 0; body=%s", codeStream, rawStream)
	}
	var envStream struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, rawStream, &envStream)
	if !envStream.OK {
		t.Fatalf("response.intercept_stream_chunk ok = false; body=%s", rawStream)
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
