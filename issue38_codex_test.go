package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Issue #38: Codex code/shell declarations tests

func TestCodex_CodeModeAliasPlan(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-code-alias-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}},{"type":"function","function":{"name":"request_user_input"}},{"type":"function","function":{"name":"collaboration__spawn_agent"}},{"type":"function","function":{"name":"wait"}},{"type":"function","function":{"name":"clock__sleep"}},{"type":"function","function":{"name":"collaboration__followup_task"}},{"type":"function","function":{"name":"collaboration__list_agents"}}],"messages":[]}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	wants := []string{"run_command", "search_web", "wp_wait", "wp_clock_sleep", "wp_collaboration_followup_task", "wp_list_workers"}
	for _, want := range wants {
		if !strings.Contains(got, `"`+want+`"`) {
			t.Errorf("expected cloaked target %q in %s", want, got)
		}
	}
	unwants := []string{"exec", "wait", "clock__sleep", "collaboration__followup_task", "collaboration__list_agents"}
	for _, unwanted := range unwants {
		if strings.Contains(got, `"`+unwanted+`"`) {
			t.Errorf("source name %q survived cloaking: %s", unwanted, got)
		}
	}
}

func TestCodex_ShellModeAliasPlan(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-shell-alias-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec_command"}},{"type":"function","function":{"name":"apply_patch"}},{"type":"function","function":{"name":"write_stdin"}},{"type":"function","function":{"name":"view_image"}},{"type":"function","function":{"name":"request_user_input"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	wants := []string{"run_command", "wp_apply_patch", "wp_write_stdin", "wp_view_image"}
	for _, want := range wants {
		if !strings.Contains(got, `"`+want+`"`) {
			t.Errorf("expected cloaked target %q in %s", want, got)
		}
	}
	unwants := []string{"exec_command", "apply_patch", "write_stdin", "view_image"}
	for _, unwanted := range unwants {
		if strings.Contains(got, `"`+unwanted+`"`) {
			t.Errorf("source name %q survived cloaking: %s", unwanted, got)
		}
	}
}

func TestCodex_ModeIsolation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))
	model := "agy/codex-test"

	// Code mode
	reqIDCode := "codex-iso-code-01"
	reqBodyCode := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`
	payloadCode := makeIntegrationRequestInterceptPayload(t, reqIDCode, "openai", model, []byte(reqBodyCode))
	handlePluginCall("request.intercept_before", payloadCode)

	respBodyCode := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`
	payloadRespCode := makeIntegrationResponseInterceptPayload(t, reqIDCode, "openai", model, []byte(respBodyCode))
	rawRespCode, codeCode := handlePluginCall("response.intercept_after", payloadRespCode)
	if codeCode != 0 {
		t.Fatalf("code = %d", codeCode)
	}
	gotCodeBody := string(decodeEnvelopeBody(t, rawRespCode))
	if !strings.Contains(gotCodeBody, `"name":"exec"`) {
		t.Errorf("code mode missing exec: %s", gotCodeBody)
	}
	if strings.Contains(gotCodeBody, `"name":"exec_command"`) {
		t.Errorf("code mode incorrectly got exec_command: %s", gotCodeBody)
	}

	// Shell mode
	reqIDShell := "codex-iso-shell-01"
	reqBodyShell := `{"tools":[{"type":"function","function":{"name":"exec_command"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`
	payloadShell := makeIntegrationRequestInterceptPayload(t, reqIDShell, "openai", model, []byte(reqBodyShell))
	handlePluginCall("request.intercept_before", payloadShell)

	respBodyShell := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`
	payloadRespShell := makeIntegrationResponseInterceptPayload(t, reqIDShell, "openai", model, []byte(respBodyShell))
	rawRespShell, codeShell := handlePluginCall("response.intercept_after", payloadRespShell)
	if codeShell != 0 {
		t.Fatalf("code = %d", codeShell)
	}
	gotShellBody := string(decodeEnvelopeBody(t, rawRespShell))
	if !strings.Contains(gotShellBody, `"name":"exec_command"`) {
		t.Errorf("shell mode missing exec_command: %s", gotShellBody)
	}
	if strings.Contains(gotShellBody, `"name":"exec"`) {
		t.Errorf("shell mode incorrectly got exec: %s", gotShellBody)
	}
}

func TestCodex_DynamicMcpDeclarationGetsFallback(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-dyn-mcp-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}},{"type":"function","function":{"name":"wait"}},{"type":"function","function":{"name":"mcp__exa__web_search","description":"Search"}},{"type":"function","function":{"name":"mcp__exa__web_search2","description":"Search2"}}],"messages":[]}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	if !strings.Contains(got, `"wp_ext_`) {
		t.Errorf("expected dynamic fallback wp_ext_ for mcp tool, got: %s", got)
	}
	if strings.Contains(got, `"mcp__exa__web_search"`) {
		t.Errorf("mcp tool not cloaked: %s", got)
	}
}

func TestCodex_CodeModeResponseReversal(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-code-resp-rev-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}},{"type":"function","function":{"name":"wait"}}],"messages":[]}`
	payloadReq := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	handlePluginCall("request.intercept_before", payloadReq)

	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}},{"function":{"name":"wp_wait","arguments":"{}"}}]}}]}`
	payloadResp := makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, []byte(respBody))
	rawResp, code := handlePluginCall("response.intercept_after", payloadResp)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	got := string(decodeEnvelopeBody(t, rawResp))
	if !strings.Contains(got, `"name":"exec"`) || !strings.Contains(got, `"name":"wait"`) {
		t.Errorf("expected exec and wait, got: %s", got)
	}
	if strings.Contains(got, `"name":"run_command"`) || strings.Contains(got, `"name":"wp_wait"`) {
		t.Errorf("cloaked targets leaked: %s", got)
	}
}

func TestCodex_ShellModeStreamReversal(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-shell-stream-rev-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec_command"}},{"type":"function","function":{"name":"web_search"}},{"type":"function","function":{"name":"request_user_input"}}],"messages":[]}`
	payloadReq := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	handlePluginCall("request.intercept_before", payloadReq)

	cloakedReq := `{"tools":[{"type":"function","function":{"name":"run_command"}},{"type":"function","function":{"name":"search_web"}},{"type":"function","function":{"name":"ask_question"}}],"messages":[]}`
	chunkBody := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"

	payloadChunk := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(chunkBody), []byte(cloakedReq))
	rawChunk, code := handlePluginCall("response.intercept_stream_chunk", payloadChunk)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	gotChunk := string(decodeEnvelopeBody(t, rawChunk))
	if !strings.Contains(gotChunk, `"name":"exec_command"`) {
		t.Errorf("expected exec_command in stream, got: %s", gotChunk)
	}
	if strings.Contains(gotChunk, `"name":"exec"`) || strings.Contains(gotChunk, `"name":"run_command"`) {
		t.Errorf("unwanted names in stream: %s", gotChunk)
	}
}

func TestCodex_MarkerBasedThinRequest(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-thin-req-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`

	headers := map[string][]string{"X-Cloak-Client": {"codex"}}
	interceptReq := map[string]any{
		"RequestID":      reqID,
		"SourceFormat":   "openai",
		"Model":          model,
		"RequestedModel": model,
		"Body":           []byte(reqBody),
		"RequestHeaders": headers,
	}
	interceptRaw, _ := json.Marshal(interceptReq)

	rawResp, code := handlePluginCall("request.intercept_before", interceptRaw)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)
	if !strings.Contains(got, `"name":"run_command"`) {
		t.Errorf("expected run_command, got: %s", got)
	}
}

func TestCodex_HistoryToolCallsConsistency(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-history-tc-01"
	model := "agy/codex-test"
	reqBody := `{
		"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}},{"type":"function","function":{"name":"wait"}}],
		"messages":[
			{"role":"assistant","tool_calls":[{"function":{"name":"exec","arguments":"{}"}},{"function":{"name":"wait","arguments":"{}"}}]}
		]
	}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	if !strings.Contains(got, `"run_command"`) || !strings.Contains(got, `"wp_wait"`) {
		t.Errorf("expected history to be cloaked to run_command/wp_wait, got: %s", got)
	}
	if strings.Contains(got, `"name":"exec"`) || strings.Contains(got, `"name":"wait"`) {
		t.Errorf("history contained uncloaked sources: %s", got)
	}
}

func TestCodex_MissingCorrelation503(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`

	// Missing RequestID ("") on eligible Codex request -> must fail closed with 503 tool_cloak_required
	payload := makeIntegrationRequestInterceptPayload(t, "", "openai", "agy/codex-test", []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d, raw=%s", code, rawResp)
	}

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawResp, &envelope)

	if !envelope.Result.Terminate || envelope.Result.StatusCode != 503 {
		t.Fatalf("expected 503 fail-closed rejection for missing RequestID, got terminate=%t status=%d",
			envelope.Result.Terminate, envelope.Result.StatusCode)
	}
	respBytes, err := base64.StdEncoding.DecodeString(envelope.Result.ResponseBody)
	if err != nil {
		respBytes = []byte(envelope.Result.ResponseBody)
	}
	if !strings.Contains(string(respBytes), "tool_cloak_required") {
		t.Fatalf("expected tool_cloak_required error code, got %s", string(respBytes))
	}
}

func TestCodex_ModelGateCoverage(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`

	// 1. Non-matching model ("other/codex-test"): model gate skips, body untouched
	payloadNonMatch := makeIntegrationRequestInterceptPayload(t, "codex-gate-skip", "openai", "other/codex-test", []byte(reqBody))
	rawRespNonMatch, codeNonMatch := handlePluginCall("request.intercept_before", payloadNonMatch)
	if codeNonMatch != 0 {
		t.Fatalf("code = %d", codeNonMatch)
	}
	bodyNonMatch, _, _ := decodeEnvelopeRequestIntercept(t, rawRespNonMatch)
	if len(bodyNonMatch) != 0 && string(bodyNonMatch) != reqBody {
		t.Fatalf("model gate non-matching model mutated body: %s", bodyNonMatch)
	}

	// 2. Matching model ("agy/codex-test"): cloaking runs
	payloadMatch := makeIntegrationRequestInterceptPayload(t, "codex-gate-pass", "openai", "agy/codex-test", []byte(reqBody))
	rawRespMatch, codeMatch := handlePluginCall("request.intercept_before", payloadMatch)
	if codeMatch != 0 {
		t.Fatalf("code = %d", codeMatch)
	}
	bodyMatch, _, _ := decodeEnvelopeRequestIntercept(t, rawRespMatch)
	if !strings.Contains(string(bodyMatch), "run_command") {
		t.Fatalf("matching model was not cloaked: %s", bodyMatch)
	}
}

func TestCodex_RequestIDDuplicateRetryContract(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	const reqID = "codex-dup-retry-req-01"
	const model = "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}}],"messages":[]}`

	// 1. First request -> admitted successfully
	payload1 := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp1, code1 := handlePluginCall("request.intercept_before", payload1)
	if code1 != 0 {
		t.Fatalf("code = %d", code1)
	}
	body1, _, _ := decodeEnvelopeRequestIntercept(t, rawResp1)
	if !strings.Contains(string(body1), "run_command") {
		t.Fatalf("expected run_command, got: %s", body1)
	}

	// 2. Duplicate request with SAME RequestID before completion -> rejected with 503 tool_cloak_required
	payload2 := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp2, code2 := handlePluginCall("request.intercept_before", payload2)
	if code2 != 0 {
		t.Fatalf("code = %d", code2)
	}
	var env2 struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawResp2, &env2)
	if !env2.Result.Terminate || env2.Result.StatusCode != 503 {
		t.Fatalf("duplicate RequestID must reject with 503, got terminate=%t status=%d", env2.Result.Terminate, env2.Result.StatusCode)
	}

	// 3. Complete the request lifecycle
	var completed struct{}
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID}, &completed)

	// 4. Retry request with SAME RequestID after completion -> admitted successfully
	payload3 := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp3, code3 := handlePluginCall("request.intercept_before", payload3)
	if code3 != 0 {
		t.Fatalf("code = %d", code3)
	}
	body3, _, _ := decodeEnvelopeRequestIntercept(t, rawResp3)
	if !strings.Contains(string(body3), "run_command") {
		t.Fatalf("retry request after completion failed to cloak: %s", body3)
	}
}

func TestCodex_GenericSharedNamesDoNotDetectCodex(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	// Request declaring only generic shared names ("wait", "clock__sleep")
	// Must NOT be detected as Codex; body remains uncloaked.
	reqBody := `{"tools":[{"type":"function","function":{"name":"wait"}},{"type":"function","function":{"name":"clock__sleep"}}],"messages":[]}`
	payload := makeIntegrationRequestInterceptPayload(t, "generic-tools-no-codex", "openai", "agy/test", []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	// Body should not be cloaked because client detection is independent of cloakability
	if strings.Contains(string(body), "wp_wait") || strings.Contains(string(body), "wp_clock_sleep") {
		t.Fatalf("generic shared names falsely detected Codex and cloaked: %s", body)
	}
}

func TestCodex_UndeclaredHistoryAndToolChoiceCloaked(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-undeclared-hist-01"
	model := "agy/codex-test"
	// tools[] only declares exec and web_search.
	// messages[] contains tool_calls with undeclared "wait" and tool role message with name "clock__sleep".
	// tool_choice specifies undeclared "request_user_input".
	reqBody := `{
		"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"web_search"}}],
		"messages":[
			{"role":"assistant","tool_calls":[{"function":{"name":"wait","arguments":"{}"}}]},
			{"role":"tool","name":"clock__sleep","content":"done"}
		],
		"tool_choice":{"type":"function","function":{"name":"request_user_input"}}
	}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	// All must be cloaked according to the plan
	if !strings.Contains(got, `"run_command"`) {
		t.Errorf("exec was not cloaked to run_command: %s", got)
	}
	if !strings.Contains(got, `"wp_wait"`) {
		t.Errorf("undeclared history wait was not cloaked to wp_wait: %s", got)
	}
	if !strings.Contains(got, `"wp_clock_sleep"`) {
		t.Errorf("undeclared history tool-role clock__sleep was not cloaked to wp_clock_sleep: %s", got)
	}
	if !strings.Contains(got, `"ask_question"`) {
		t.Errorf("undeclared tool_choice request_user_input was not cloaked to ask_question: %s", got)
	}

	// Raw names must not leak
	for _, raw := range []string{`"wait"`, `"clock__sleep"`, `"request_user_input"`} {
		if strings.Contains(got, raw) {
			t.Errorf("raw undeclared identity leaked: %s in %s", raw, got)
		}
	}
}

func TestCodex_NamespaceVariantCollisionFailsClosed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	cases := []struct {
		name  string
		tools []string
	}{
		{"unknown namespace variants", []string{"functions:foo", "default_api:foo"}},
		{"mapped namespace variants", []string{"functions:exec", "default_api:exec"}},
		{"mapped and native target", []string{"functions:exec", "default_api:run_command"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var toolDecls []string
			for _, tool := range tc.tools {
				toolDecls = append(toolDecls, `{"type":"function","function":{"name":"`+tool+`"}}`)
			}
			reqBody := `{"tools":[` + strings.Join(toolDecls, ",") + `],"messages":[]}`
			headers := make(http.Header)
			headers.Set("X-Cloak-Client", "codex")
			payload := makeIntegrationRequestInterceptPayloadWithHeaders(t, "codex-col-"+tc.name, "openai", "agy/codex-test", []byte(reqBody), headers)
			rawResp, code := handlePluginCall("request.intercept_before", payload)
			if code != 0 {
				t.Fatalf("code = %d", code)
			}
			var envelope struct {
				OK     bool `json:"ok"`
				Result struct {
					Terminate    bool   `json:"Terminate"`
					StatusCode   int    `json:"StatusCode"`
					ResponseBody string `json:"ResponseBody"`
				} `json:"result"`
			}
			mustUnmarshalJSON(t, rawResp, &envelope)
			if !envelope.Result.Terminate || envelope.Result.StatusCode != 503 {
				t.Fatalf("expected 503 fail-closed rejection for collision, got terminate=%t status=%d",
					envelope.Result.Terminate, envelope.Result.StatusCode)
			}
			respBytes, err := base64.StdEncoding.DecodeString(envelope.Result.ResponseBody)
			if err != nil {
				respBytes = []byte(envelope.Result.ResponseBody)
			}
			if !strings.Contains(string(respBytes), "tool_cloak_required") {
				t.Fatalf("expected tool_cloak_required error code, got %s", string(respBytes))
			}
		})
	}
}

func TestCodex_ExecDescriptionHelpersPreserved(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-desc-helpers-01"
	model := "agy/codex-test"
	desc := "Run shell command. Inside exec, you can use apply_patch, exec_command, write_stdin, view_image, and tool_search."
	reqBody := `{
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "exec",
					"description": "` + desc + `"
				}
			},
			{
				"type": "function",
				"function": {
					"name": "web_search",
					"description": "Search web"
				}
			}
		],
		"messages": []
	}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	// Tool name must be cloaked to run_command
	if !strings.Contains(got, `"name":"run_command"`) {
		t.Errorf("exec was not cloaked to run_command: %s", got)
	}
	// Prose description helpers MUST remain untouched data/prose and NOT be rewritten
	helpers := []string{"apply_patch", "exec_command", "write_stdin", "view_image", "tool_search"}
	for _, helper := range helpers {
		if !strings.Contains(got, helper) {
			t.Errorf("helper prose name %q was corrupted in exec description: %s", helper, got)
		}
	}
	if !strings.Contains(got, desc) {
		t.Errorf("exec description was corrupted: %s", got)
	}
}

func TestCodex_MixedMode_ExecDescriptionHelpersWithHistoricalShellCalls(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-mixed-desc-helpers-01"
	model := "agy/codex-test"
	desc := "Run shell command. Inside exec, you can use apply_patch, exec_command, write_stdin, view_image, and tool_search."

	// 1. Valid mixed mode: declaration exec (run_command) with non-colliding historical helpers (apply_patch, write_stdin)
	reqBodyValid := `{
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "exec",
					"description": "` + desc + `"
				}
			},
			{
				"type": "function",
				"function": {
					"name": "web_search",
					"description": "Search web"
				}
			}
		],
		"messages": [
			{
				"role": "assistant",
				"content": "Running historical commands",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "write_stdin",
							"arguments": "{\"stdin\":\"hello\"}"
						}
					},
					{
						"id": "call_2",
						"type": "function",
						"function": {
							"name": "apply_patch",
							"arguments": "{\"patch\":\"diff ...\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"name": "write_stdin",
				"content": "hello"
			},
			{
				"role": "tool",
				"tool_call_id": "call_2",
				"name": "apply_patch",
				"content": "success"
			}
		]
	}`

	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBodyValid))
	rawResp, code := handlePluginCall("request.intercept_before", payload)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	body, _, _ := decodeEnvelopeRequestIntercept(t, rawResp)
	got := string(body)

	// Declared tools must be cloaked to AGY targets
	if !strings.Contains(got, `"name":"run_command"`) {
		t.Errorf("exec was not cloaked to run_command: %s", got)
	}
	if !strings.Contains(got, `"name":"search_web"`) {
		t.Errorf("web_search was not cloaked to search_web: %s", got)
	}

	// Historical tool calls must be protocol-cloaked to neutral shared aliases
	if strings.Contains(got, `"name":"write_stdin"`) {
		t.Errorf("historical write_stdin was not protocol-cloaked: %s", got)
	}
	if !strings.Contains(got, `"wp_write_stdin"`) {
		t.Errorf("historical write_stdin was not cloaked to wp_write_stdin: %s", got)
	}
	if strings.Contains(got, `"name":"apply_patch"`) {
		t.Errorf("historical apply_patch was not protocol-cloaked: %s", got)
	}
	if !strings.Contains(got, `"wp_apply_patch"`) {
		t.Errorf("historical apply_patch was not cloaked to wp_apply_patch: %s", got)
	}

	// Prose description helpers MUST remain untouched data/prose and NOT be rewritten
	helpers := []string{"apply_patch", "exec_command", "write_stdin", "view_image", "tool_search"}
	for _, helper := range helpers {
		if !strings.Contains(got, helper) {
			t.Errorf("helper prose name %q was corrupted in exec description: %s", helper, got)
		}
	}
	if !strings.Contains(got, desc) {
		t.Errorf("exec description was corrupted: %s", got)
	}

	// Reverse path: response run_command must reverse to declared exec, and wp_apply_patch to apply_patch
	respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}},{"function":{"name":"wp_apply_patch","arguments":"{}"}}]}}]}`
	payloadResp := makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, []byte(respBody))
	rawRespAfter, codeAfter := handlePluginCall("response.intercept_after", payloadResp)
	if codeAfter != 0 {
		t.Fatalf("response code = %d", codeAfter)
	}
	gotResp := string(decodeEnvelopeBody(t, rawRespAfter))
	if !strings.Contains(gotResp, `"name":"exec"`) {
		t.Errorf("run_command did not reverse to declared exec: %s", gotResp)
	}
	if !strings.Contains(gotResp, `"name":"apply_patch"`) {
		t.Errorf("wp_apply_patch did not reverse to apply_patch: %s", gotResp)
	}

	var completed struct{}
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID}, &completed)

	// 2. Collision case: declaration exec + historical exec_command both resolve to run_command -> must fail closed with 503
	reqIDCollision := "codex-mixed-collision-01"
	reqBodyCollision := `{
		"tools": [
			{"type": "function", "function": {"name": "exec"}},
			{"type": "function", "function": {"name": "web_search"}}
		],
		"messages": [
			{"role": "assistant", "tool_calls": [{"id": "call_1", "function": {"name": "exec_command", "arguments": "{}"}}]}
		]
	}`
	payloadColl := makeIntegrationRequestInterceptPayload(t, reqIDCollision, "openai", model, []byte(reqBodyCollision))
	rawRespColl, codeColl := handlePluginCall("request.intercept_before", payloadColl)
	if codeColl != 0 {
		t.Fatalf("codeColl = %d", codeColl)
	}
	var envColl struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawRespColl, &envColl)
	if !envColl.Result.Terminate || envColl.Result.StatusCode != 503 {
		t.Fatalf("expected 503 fail-closed rejection for mixed exec+exec_command collision, got terminate=%t status=%d",
			envColl.Result.Terminate, envColl.Result.StatusCode)
	}
}

func TestCodex_ModeSwitch_ShellDecl_HistoricalExec(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-mode-switch-shell-decl-01"
	model := "agy/codex-test"

	// Shell-mode request declaring exec_command, with prior code-mode history calling exec.
	// Since both exec_command and exec map to run_command, distinct sources sharing run_command
	// in a single request violates injectivity and must fail closed with 503 tool_cloak_required.
	reqBody := `{
		"tools": [
			{"type": "function", "function": {"name": "exec_command"}},
			{"type": "function", "function": {"name": "web_search"}}
		],
		"messages": [
			{"role": "assistant", "tool_calls": [{"id": "call_1", "function": {"name": "exec", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "name": "exec", "content": "output"}
		]
	}`

	payloadReq := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payloadReq)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawResp, &env)
	if !env.Result.Terminate || env.Result.StatusCode != 503 {
		t.Fatalf("expected 503 fail-closed rejection for exec_command+exec collision, got terminate=%t status=%d",
			env.Result.Terminate, env.Result.StatusCode)
	}
	respBytes, err := base64.StdEncoding.DecodeString(env.Result.ResponseBody)
	if err != nil {
		respBytes = []byte(env.Result.ResponseBody)
	}
	if !strings.Contains(string(respBytes), "tool_cloak_required") {
		t.Fatalf("expected tool_cloak_required error code, got %s", string(respBytes))
	}
}

func TestCodex_ModeSwitch_CodeDecl_HistoricalExecCommand(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-mode-switch-code-decl-01"
	model := "agy/codex-test"

	// Code-mode request declaring exec, with prior shell-mode history calling exec_command.
	// Since both exec and exec_command map to run_command, distinct sources sharing run_command
	// in a single request violates injectivity and must fail closed with 503 tool_cloak_required.
	reqBody := `{
		"tools": [
			{"type": "function", "function": {"name": "exec"}},
			{"type": "function", "function": {"name": "web_search"}}
		],
		"messages": [
			{"role": "assistant", "tool_calls": [{"id": "call_1", "function": {"name": "exec_command", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "name": "exec_command", "content": "output"}
		]
	}`

	payloadReq := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payloadReq)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawResp, &env)
	if !env.Result.Terminate || env.Result.StatusCode != 503 {
		t.Fatalf("expected 503 fail-closed rejection for exec+exec_command collision, got terminate=%t status=%d",
			env.Result.Terminate, env.Result.StatusCode)
	}
	respBytes, err := base64.StdEncoding.DecodeString(env.Result.ResponseBody)
	if err != nil {
		respBytes = []byte(env.Result.ResponseBody)
	}
	if !strings.Contains(string(respBytes), "tool_cloak_required") {
		t.Fatalf("expected tool_cloak_required error code, got %s", string(respBytes))
	}
}

func TestCodex_ModeSwitch_BothDeclaredInTools_FailsClosed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-both-decl-01"
	model := "agy/codex-test"

	// If a client declares BOTH exec and exec_command in tools[] in the same request,
	// that is a true collision for run_command -> must fail closed with 503 tool_cloak_required.
	reqBody := `{
		"tools": [
			{"type": "function", "function": {"name": "exec"}},
			{"type": "function", "function": {"name": "exec_command"}},
			{"type": "function", "function": {"name": "web_search"}}
		],
		"messages": []
	}`

	payloadReq := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawResp, code := handlePluginCall("request.intercept_before", payloadReq)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawResp, &env)
	if !env.Result.Terminate || env.Result.StatusCode != 503 {
		t.Fatalf("expected 503 fail-closed rejection when both are declared in tools, got terminate=%t status=%d",
			env.Result.Terminate, env.Result.StatusCode)
	}
	respBytes, err := base64.StdEncoding.DecodeString(env.Result.ResponseBody)
	if err != nil {
		respBytes = []byte(env.Result.ResponseBody)
	}
	if !strings.Contains(string(respBytes), "tool_cloak_required") {
		t.Fatalf("expected tool_cloak_required error code, got %s", string(respBytes))
	}
}

func TestCrossClient_SameRoleSharedVocabulary(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	// 1. Codex declaring collaboration__send_message and collaboration__list_agents
	reqIDCodex := "codex-cross-client-01"
	model := "agy/codex-test"
	reqBodyCodex := `{
		"tools": [
			{"type": "function", "function": {"name": "collaboration__send_message"}},
			{"type": "function", "function": {"name": "collaboration__list_agents"}},
			{"type": "function", "function": {"name": "exec"}}
		],
		"messages": []
	}`
	payloadCodex := makeIntegrationRequestInterceptPayload(t, reqIDCodex, "openai", model, []byte(reqBodyCodex))
	rawRespCodex, codeCodex := handlePluginCall("request.intercept_before", payloadCodex)
	if codeCodex != 0 {
		t.Fatalf("codeCodex = %d", codeCodex)
	}
	bodyCodex, _, _ := decodeEnvelopeRequestIntercept(t, rawRespCodex)
	gotCodex := string(bodyCodex)

	// Upstream receives shared vocabulary without client namespace
	if !strings.Contains(gotCodex, `"wp_send_message"`) || !strings.Contains(gotCodex, `"wp_list_workers"`) {
		t.Fatalf("expected wp_send_message and wp_list_workers upstream for Codex, got: %s", gotCodex)
	}
	if strings.Contains(gotCodex, "collaboration__") {
		t.Fatalf("client-identifying namespace collaboration__ leaked upstream: %s", gotCodex)
	}

	// Codex response reversal restores exact Codex source names
	respBodyCodex := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"wp_send_message","arguments":"{}"}},{"function":{"name":"wp_list_workers","arguments":"{}"}}]}}]}`
	payloadRespCodex := makeIntegrationResponseInterceptPayload(t, reqIDCodex, "openai", model, []byte(respBodyCodex))
	rawRespInterCodex, codeRespCodex := handlePluginCall("response.intercept_after", payloadRespCodex)
	if codeRespCodex != 0 {
		t.Fatalf("codeRespCodex = %d", codeRespCodex)
	}
	respGotCodex := string(decodeEnvelopeBody(t, rawRespInterCodex))
	if !strings.Contains(respGotCodex, `"collaboration__send_message"`) || !strings.Contains(respGotCodex, `"collaboration__list_agents"`) {
		t.Fatalf("expected exact Codex source names restored, got: %s", respGotCodex)
	}
	if strings.Contains(respGotCodex, `"wp_send_message"`) || strings.Contains(respGotCodex, `"wp_list_workers"`) {
		t.Fatalf("cloaked aliases leaked in Codex response: %s", respGotCodex)
	}

	var completedCodex struct{}
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqIDCodex}, &completedCodex)

	// 2. Claude Code declaring SendMessage and ListAgents
	reqIDCC := "cc-cross-client-01"
	reqBodyCC := []byte(`{
		"tools": [
			{"name": "SendMessage", "description": ""},
			{"name": "ListAgents", "description": ""},
			{"name": "Bash", "description": ""}
		]
	}`)
	headersCC := make(http.Header)
	headersCC.Set("X-Cloak-Client", "claude_code")
	payloadCC := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqIDCC, "anthropic", model, reqBodyCC, headersCC)
	rawRespCC, codeCC := handlePluginCall("request.intercept_before", payloadCC)
	if codeCC != 0 {
		t.Fatalf("codeCC = %d", codeCC)
	}
	gotCC := string(decodeEnvelopeBody(t, rawRespCC))

	// Upstream receives exact same shared vocabulary
	if !strings.Contains(gotCC, `"wp_send_message"`) || !strings.Contains(gotCC, `"wp_list_workers"`) {
		t.Fatalf("expected wp_send_message and wp_list_workers upstream for CC, got: %s", gotCC)
	}

	// CC response reversal restores exact CC source names
	respBodyCC := []byte(`{
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "wp_send_message", "input": {}},
			{"type": "tool_use", "id": "tu_2", "name": "wp_list_workers", "input": {}}
		]
	}`)
	payloadRespCC := makeIntegrationResponseInterceptPayload(t, reqIDCC, "anthropic", model, respBodyCC)
	rawRespInterCC, codeRespCC := handlePluginCall("response.intercept_after", payloadRespCC)
	if codeRespCC != 0 {
		t.Fatalf("codeRespCC = %d", codeRespCC)
	}
	respGotCC := string(decodeEnvelopeBody(t, rawRespInterCC))
	if !strings.Contains(respGotCC, `"SendMessage"`) || !strings.Contains(respGotCC, `"ListAgents"`) {
		t.Fatalf("expected exact CC source names restored, got: %s", respGotCC)
	}
	if strings.Contains(respGotCC, `"wp_send_message"`) || strings.Contains(respGotCC, `"wp_list_workers"`) {
		t.Fatalf("cloaked aliases leaked in CC response: %s", respGotCC)
	}
}

func TestCodex_StreamReversal_FalseDoneInPayloadDoesNotKillSession(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	reqID := "codex-false-done-01"
	model := "agy/codex-test"
	reqBody := `{"tools":[{"type":"function","function":{"name":"exec_command"}},{"type":"function","function":{"name":"apply_patch"}}],"messages":[]}`
	payloadReq := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, []byte(reqBody))
	rawReq, code := handlePluginCall("request.intercept_before", payloadReq)
	if code != 0 {
		t.Fatalf("request.intercept_before code = %d", code)
	}
	cloakedReq := string(decodeEnvelopeBody(t, rawReq))

	// Chunk 1: Content contains literal string "data: [DONE]" inside arguments/text.
	// This must NOT trigger terminal stream cleanup or set disposition CleanTerminal.
	chunk1Body := "data: {\"choices\":[{\"delta\":{\"content\":\"Notice: the data: [DONE] string should not terminate stream\"}}]}\n\n"
	payloadChunk1 := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(chunk1Body), []byte(cloakedReq))
	rawChunk1, code1 := handlePluginCall("response.intercept_stream_chunk", payloadChunk1)
	if code1 != 0 {
		t.Fatalf("chunk1 code = %d", code1)
	}
	_ = rawChunk1

	// Verify session remains active in globalStreamManager
	globalStreamManager.mu.Lock()
	sess := globalStreamManager.sessions["req:"+reqID]
	globalStreamManager.mu.Unlock()
	if sess == nil {
		t.Fatal("session was prematurely deleted by literal 'data: [DONE]' inside JSON payload")
	}

	// Chunk 2: Tool call delta arrives with cloaked target run_command and wp_apply_patch.
	// Since session is still alive, both must be reversed to declared exec_command and apply_patch.
	chunk2Body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"run_command\"}},{\"index\":1,\"function\":{\"name\":\"wp_apply_patch\"}}]}}]}\n\n"
	payloadChunk2 := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 1, []byte(chunk2Body), []byte(cloakedReq))
	rawChunk2, code2 := handlePluginCall("response.intercept_stream_chunk", payloadChunk2)
	if code2 != 0 {
		t.Fatalf("chunk2 code = %d", code2)
	}
	gotChunk2 := string(decodeEnvelopeBody(t, rawChunk2))
	if !strings.Contains(gotChunk2, `"name":"exec_command"`) {
		t.Errorf("expected run_command to reverse to exec_command in stream, got: %s", gotChunk2)
	}
	if !strings.Contains(gotChunk2, `"name":"apply_patch"`) {
		t.Errorf("expected wp_apply_patch to reverse to apply_patch in stream, got: %s", gotChunk2)
	}
	if strings.Contains(gotChunk2, `"name":"run_command"`) || strings.Contains(gotChunk2, `"name":"wp_apply_patch"`) {
		t.Errorf("cloaked targets survived in stream: %s", gotChunk2)
	}

	// Chunk 3: True terminal SSE frame data: [DONE]
	// This must perform terminal cleanup and set CleanTerminal.
	chunk3Body := "data: [DONE]\n\n"
	payloadChunk3 := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 2, []byte(chunk3Body), []byte(cloakedReq))
	_, code3 := handlePluginCall("response.intercept_stream_chunk", payloadChunk3)
	if code3 != 0 {
		t.Fatalf("chunk3 code = %d", code3)
	}

	globalStreamManager.mu.Lock()
	sessAfter := globalStreamManager.sessions["req:"+reqID]
	globalStreamManager.mu.Unlock()
	if sessAfter != nil {
		t.Fatal("session was not cleaned up after true data: [DONE] frame")
	}

	plan := globalAliasPlanManager.get(reqID)
	if plan == nil || plan.getDisposition() != streamDispositionCleanTerminal {
		t.Fatalf("expected plan disposition CleanTerminal, got: %v", plan)
	}
}
