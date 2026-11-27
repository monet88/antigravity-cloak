package cloak

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

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
	HandleRequestInterceptBefore(interceptRaw)

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

	raw := HandleResponseIntercept(request)

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
	HandleRequestInterceptBefore(interceptRaw)

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

	raw := HandleResponseIntercept(request)
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
	raw := HandleResponseIntercept(request)

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
	raw := HandleResponseIntercept(request)

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
