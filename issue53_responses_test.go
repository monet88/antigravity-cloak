package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func isolateResponsesTestState(t *testing.T) {
	t.Helper()
	previousConfig := globalFilterConfig.Load()
	previousPlans := globalAliasPlanManager
	previousStreams := globalStreamManager
	previousRoutes := globalLifecycleManager
	applyFilterConfig(defaultFilterConfig())
	globalAliasPlanManager = newRequestAliasPlanManager()
	globalStreamManager = newStreamSessionManager()
	globalLifecycleManager = newExplicitOMPLifecycleManager()
	t.Cleanup(func() {
		globalFilterConfig.Store(previousConfig)
		globalAliasPlanManager = previousPlans
		globalStreamManager = previousStreams
		globalLifecycleManager = previousRoutes
	})
}

func callResponsesRequestIntercept(
	t *testing.T,
	reqID, sourceFormat, model string,
	body []byte,
	headers http.Header,
) (resp pluginapi.RequestInterceptResponse, upstreamAttempts int) {
	t.Helper()
	raw := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, sourceFormat, model, body, headers)
	outRaw, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, raw)
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d out=%s", code, outRaw)
	}
	var env struct {
		Result pluginapi.RequestInterceptResponse `json:"result"`
	}
	if err := json.Unmarshal(outRaw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var observedAttempts atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		observedAttempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"response","output":[]}`))
	}))
	defer mockSrv.Close()
	if !env.Result.Terminate && env.Result.StatusCode < 400 {
		sendBody := body
		if len(env.Result.Body) > 0 {
			sendBody = env.Result.Body
		}
		httpResp, err := http.Post(mockSrv.URL, "application/json", bytes.NewReader(sendBody))
		if err != nil {
			t.Fatalf("post to mock upstream: %v", err)
		}
		_, _ = io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
	}
	return env.Result, int(observedAttempts.Load())
}

func callResponsesResponseIntercept(
	t *testing.T,
	reqID, sourceFormat, model string,
	reqBody, origReq, respBody []byte,
	headers http.Header,
) pluginapi.ResponseInterceptResponse {
	t.Helper()
	in := pluginapi.ResponseInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    sourceFormat,
		Model:           model,
		RequestedModel:  model,
		RequestHeaders:  headers,
		RequestBody:     reqBody,
		OriginalRequest: origReq,
		Body:            respBody,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal response intercept req: %v", err)
	}
	outRaw, code := handlePluginCall(pluginabi.MethodResponseInterceptAfter, raw)
	if code != 0 {
		t.Fatalf("response.intercept_after code=%d out=%s", code, outRaw)
	}
	var env struct {
		Result pluginapi.ResponseInterceptResponse `json:"result"`
	}
	if err := json.Unmarshal(outRaw, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	return env.Result
}

func callResponsesStreamChunkIntercept(
	t *testing.T,
	reqID, sourceFormat, model string,
	chunkIndex int,
	chunkBody []byte,
	reqBody, origReq []byte,
	headers http.Header,
) pluginapi.StreamChunkInterceptResponse {
	t.Helper()
	in := pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    sourceFormat,
		Model:           model,
		RequestedModel:  model,
		ChunkIndex:      chunkIndex,
		RequestHeaders:  headers,
		RequestBody:     reqBody,
		OriginalRequest: origReq,
		Body:            chunkBody,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal stream chunk req: %v", err)
	}
	outRaw, code := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, raw)
	if code != 0 {
		t.Fatalf("response.intercept_stream_chunk code=%d out=%s", code, outRaw)
	}
	var env struct {
		Result pluginapi.StreamChunkInterceptResponse `json:"result"`
	}
	if err := json.Unmarshal(outRaw, &env); err != nil {
		t.Fatalf("unmarshal stream envelope: %v", err)
	}
	return env.Result
}

func callResponsesRequestComplete(t *testing.T, reqID, outcome string) {
	t.Helper()
	raw, _ := json.Marshal(pluginapi.RequestCompletion{
		RequestID: reqID,
		Outcome:   pluginapi.RequestCompletionOutcome(outcome),
	})
	_, code := handlePluginCall(pluginabi.MethodRequestComplete, raw)
	if code != 0 {
		t.Fatalf("request.complete code=%d", code)
	}
}

func appendStreamChunkWire(wire, inputChunk []byte, res pluginapi.StreamChunkInterceptResponse) []byte {
	if res.DropChunk {
		return wire
	}
	if len(res.Body) > 0 {
		return append(wire, res.Body...)
	}
	return append(wire, inputChunk...)
}

// ── A1: Coarse flat Responses declarations & parity with Chat/Anthropic ─────

func TestIssue53_A1_CoarseFlatResponsesDeclarationsAndParity(t *testing.T) {
	isolateResponsesTestState(t)

	// Responses body with flat, nested, absent-type function, custom tool, and dynamic mcp__srv__tool
	respReqBody := []byte(`{
		"input": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "name": "exec", "description": "Run exec command"},
			{"type": "function", "function": {"name": "request_user_input", "description": "Ask user"}},
			{"name": "collaboration__spawn_agent", "description": "Absent type function"},
			{"type": "custom", "name": "apply_patch", "description": "Patch files", "format": {"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}},
			{"type": "function", "name": "mcp__srv__tool", "description": "Dynamic MCP tool"},
			{"type": "web_search", "search_context_size": "high"}
		]
	}`)

	chatReqBody := []byte(`{
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "exec", "description": "Run exec command"}},
			{"type": "function", "function": {"name": "request_user_input", "description": "Ask user"}},
			{"type": "function", "function": {"name": "collaboration__spawn_agent", "description": "Absent type function"}},
			{"type": "function", "function": {"name": "apply_patch", "description": "Patch files"}},
			{"type": "function", "function": {"name": "mcp__srv__tool", "description": "Dynamic MCP tool"}}
		]
	}`)

	resResp, attemptsResp := callResponsesRequestIntercept(t, "a1-resp-1", "openai-response", "agy/gpt-5", respReqBody, nil)
	if attemptsResp != 1 || resResp.Terminate {
		t.Fatalf("expected 1 upstream attempt for Responses request, got attempts=%d resp=%+v", attemptsResp, resResp)
	}
	resChat, attemptsChat := callResponsesRequestIntercept(t, "a1-chat-1", "openai", "agy/gpt-5", chatReqBody, nil)
	if attemptsChat != 1 || resChat.Terminate {
		t.Fatalf("expected 1 upstream attempt for Chat request, got attempts=%d resp=%+v", attemptsChat, resChat)
	}

	planResp := globalAliasPlanManager.get("a1-resp-1")
	planChat := globalAliasPlanManager.get("a1-chat-1")
	if planResp == nil || planChat == nil {
		t.Fatalf("expected pinned alias plans for both requests: resp=%v chat=%v", planResp, planChat)
	}
	if planResp.client != "codex" || planChat.client != "codex" {
		t.Fatalf("expected both to resolve client=codex, got resp=%q chat=%q", planResp.client, planChat.client)
	}
	for _, src := range []string{"exec", "request_user_input", "collaboration__spawn_agent", "apply_patch", "mcp__srv__tool"} {
		if planResp.forward[src] == "" || planResp.forward[src] != planChat.forward[src] {
			t.Fatalf("alias mismatch for %q: resp=%q chat=%q", src, planResp.forward[src], planChat.forward[src])
		}
	}

	var mutated map[string]any
	if err := json.Unmarshal(resResp.Body, &mutated); err != nil {
		t.Fatalf("unmarshal mutated Responses body: %v", err)
	}
	tools := mutated["tools"].([]any)
	if got := tools[0].(map[string]any)["name"]; got != "run_command" {
		t.Fatalf("tool[0].name = %v, want run_command", got)
	}
	if got := tools[1].(map[string]any)["function"].(map[string]any)["name"]; got != "ask_question" {
		t.Fatalf("tool[1].function.name = %v, want ask_question", got)
	}
	if got := tools[2].(map[string]any)["name"]; got != "invoke_subagent" {
		t.Fatalf("tool[2].name = %v, want invoke_subagent", got)
	}
	if got := tools[3].(map[string]any)["name"]; got != "wp_apply_patch" {
		t.Fatalf("tool[3].name = %v, want wp_apply_patch", got)
	}
	// Verify custom format preserved
	formatMap := tools[3].(map[string]any)["format"].(map[string]any)
	if formatMap["syntax"] != "lark" || formatMap["definition"] != "start: /.+/" {
		t.Fatalf("custom format was mutated: %+v", formatMap)
	}
	if got := tools[4].(map[string]any)["name"]; got != fallbackAliasForSource("mcp__srv__tool") {
		t.Fatalf("tool[4].name = %v, want %s", got, fallbackAliasForSource("mcp__srv__tool"))
	}
	// Built-in web_search preserved untouched
	if got := tools[5].(map[string]any)["type"]; got != "web_search" {
		t.Fatalf("tool[5].type = %v, want web_search", got)
	}

	// Negative assertion: admission is NOT bypassed; missing name in a flat tool declaration fails 503 tool_cloak_required
	h := http.Header{"X-Cloak-Client": {"codex"}}
	badReq := []byte(`{"input":"hi","tools":[{"type":"function","description":"no name"}]}`)
	badRes, badAttempts := callResponsesRequestIntercept(t, "a1-bad-decl", "openai-response", "agy/gpt-5", badReq, h)
	if badAttempts != 0 || !badRes.Terminate || badRes.StatusCode != 503 || !strings.Contains(string(badRes.ResponseBody), "tool_cloak_required") {
		t.Fatalf("expected 503 tool_cloak_required with 0 attempts for malformed tool decl, got attempts=%d resp=%+v", badAttempts, badRes)
	}
}

// ── A2: Instructions, system/developer, user literal, reminders, schema, reasoning ──

func TestIssue53_A2_SystemAndConversationCarriers(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}
	dynAlias := fallbackAliasForSource("mcp__srv__tool")

	// 1. Plain string input is a user turn and preserved verbatim while instructions is rewritten
	plainInputBody := []byte(`{
		"instructions": "You are Codex, based on GPT-5. Use exec and mcp__srv__tool with ~/.codex/AGENTS.md.",
		"input": "Hello Codex, please run exec on ~/.codex/AGENTS.md using mcp__srv__tool.",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "function", "name": "mcp__srv__tool"}
		]
	}`)
	res1, att1 := callResponsesRequestIntercept(t, "a2-plain-input", "openai-response", "agy/gpt-5", plainInputBody, h)
	if att1 != 1 {
		t.Fatalf("expected 1 attempt, got %d", att1)
	}
	var m1 map[string]any
	_ = json.Unmarshal(res1.Body, &m1)
	inst1 := m1["instructions"].(string)
	if strings.Contains(inst1, "Codex") || !strings.Contains(inst1, "Antigravity") || !strings.Contains(inst1, "run_command") || !strings.Contains(inst1, dynAlias) || !strings.Contains(inst1, "~/."+"gem"+"ini/"+"AGE"+"NTS.md") {
		t.Fatalf("instructions not properly cloaked: %q", inst1)
	}
	if gotInput := m1["input"].(string); gotInput != "Hello Codex, please run exec on ~/.codex/AGENTS.md using mcp__srv__tool." {
		t.Fatalf("plain string input was mutated: %q", gotInput)
	}

	// 2. Structured input array with system, developer, user (literal vs <system-reminder>), assistant, function_call, custom_tool_call, function_call_output, custom_tool_call_output, reasoning, schema
	structuredBody := []byte(`{
		"instructions": "Codex system instructions using exec.",
		"tools": [
			{
				"type": "function",
				"name": "exec",
				"description": "Codex exec tool calling mcp__srv__tool",
				"parameters": {
					"type": "object",
					"description": "Codex schema for the exec tool",
					"properties": {
						"exec": {"type": "string", "title": "Codex the exec tool prop", "enum": ["Codex", "exec"], "const": "Codex", "pattern": "^Codex$"}
					},
					"required": ["exec"]
				}
			},
			{"type": "custom", "name": "apply_patch", "description": "Codex apply_patch tool", "format": {"type": "text", "definition": "Codex exec"}},
			{"type": "function", "name": "mcp__srv__tool", "description": "Dynamic tool"}
		],
		"input": [
			{"role": "system", "content": "System string with Codex and the exec tool and ~/.codex/config.toml"},
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "Developer part with Codex and apply_patch"}, {"text": "Untyped part with Codex and mcp__srv__tool"}]},
			{"type": "message", "role": "user", "content": [
				{"type": "input_text", "text": "User literal Codex and exec and ~/.codex/config.toml <system-reminder>System reminder with Codex and use exec and ~/.codex/config.toml</system-reminder> tail Codex exec"}
			]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Assistant history mentioning Codex and use exec"}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec", "arguments": "{\"cmd\":\"echo Codex ~/.codex/config.toml exec\"}"},
			{"type": "custom_tool_call", "call_id": "call_2", "name": "apply_patch", "input": "*** Update File: ~/.codex/config.toml\nCodex"},
			{"type": "function_call_output", "call_id": "call_1", "output": "Output from Codex using the exec tool at ~/.codex/config.toml"},
			{"type": "custom_tool_call_output", "call_id": "call_2", "output": [{"type": "input_text", "text": "Patched by Codex apply_patch"}]},
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "Codex reasoning summary exec"}], "content": [{"type": "text", "text": "Codex reasoning content exec"}], "encrypted_content": "enc_Codex_exec"}
		]
	}`)

	res2, att2 := callResponsesRequestIntercept(t, "a2-structured", "openai-response", "agy/gpt-5", structuredBody, h)
	if att2 != 1 {
		t.Fatalf("expected 1 attempt, got %d", att2)
	}
	var m2 map[string]any
	_ = json.Unmarshal(res2.Body, &m2)

	// Verify schema structural fields preserved while description/title rewritten
	tool0 := m2["tools"].([]any)[0].(map[string]any)
	params := tool0["parameters"].(map[string]any)
	if !strings.Contains(params["description"].(string), "Antigravity") || !strings.Contains(params["description"].(string), "run_command") {
		t.Fatalf("schema description not rewritten: %v", params["description"])
	}
	props := params["properties"].(map[string]any)
	execProp := props["exec"].(map[string]any)
	if !strings.Contains(execProp["title"].(string), "Antigravity") {
		t.Fatalf("property title not rewritten: %v", execProp["title"])
	}
	if execProp["const"] != "Codex" || execProp["pattern"] != "^Codex$" {
		t.Fatalf("schema const/pattern mutated: %+v", execProp)
	}
	enumArr := execProp["enum"].([]any)
	if enumArr[0] != "Codex" || enumArr[1] != "exec" {
		t.Fatalf("schema enum mutated: %+v", enumArr)
	}
	reqArr := params["required"].([]any)
	if reqArr[0] != "exec" {
		t.Fatalf("schema required mutated: %+v", reqArr)
	}
	// Custom tool format preserved
	tool1 := m2["tools"].([]any)[1].(map[string]any)
	if tool1["format"].(map[string]any)["definition"] != "Codex exec" {
		t.Fatalf("custom tool format mutated: %+v", tool1["format"])
	}

	inArr := m2["input"].([]any)
	// [0] system string
	sysStr := inArr[0].(map[string]any)["content"].(string)
	if strings.Contains(sysStr, "Codex") || !strings.Contains(sysStr, "Antigravity") || !strings.Contains(sysStr, "run_command") || !strings.Contains(sysStr, "~/."+"gem"+"ini/config.toml") {
		t.Fatalf("system message not rewritten: %q", sysStr)
	}
	// [1] developer message parts
	devParts := inArr[1].(map[string]any)["content"].([]any)
	if p0 := devParts[0].(map[string]any)["text"].(string); !strings.Contains(p0, "Antigravity") || !strings.Contains(p0, "wp_apply_patch") {
		t.Fatalf("developer part 0 not rewritten: %q", p0)
	}
	if p1 := devParts[1].(map[string]any)["text"].(string); !strings.Contains(p1, "Antigravity") || !strings.Contains(p1, dynAlias) {
		t.Fatalf("developer part 1 (untyped) not rewritten: %q", p1)
	}
	// [2] user message: outside <system-reminder> stays literal, inside is rewritten
	userTxt := inArr[2].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(userTxt, "User literal Codex and exec and ~/.codex/config.toml <system-reminder>") ||
		!strings.HasSuffix(userTxt, "</system-reminder> tail Codex exec") ||
		!strings.Contains(userTxt, "System reminder with Antigravity and use run_command and ~/."+"gem"+"ini/config.toml") {
		t.Fatalf("user message reminder span handling wrong: %q", userTxt)
	}
	// [3] assistant message
	asstTxt := inArr[3].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(asstTxt, "Antigravity") || !strings.Contains(asstTxt, "run_command") {
		t.Fatalf("assistant history not rewritten: %q", asstTxt)
	}
	// [4] function_call: name cloaked, arguments and call_id preserved
	fc := inArr[4].(map[string]any)
	if fc["name"] != "run_command" || fc["call_id"] != "call_1" || fc["arguments"] != "{\"cmd\":\"echo Codex ~/.codex/config.toml exec\"}" {
		t.Fatalf("function_call item wrong: %+v", fc)
	}
	// [5] custom_tool_call: name cloaked, input and call_id preserved
	ctc := inArr[5].(map[string]any)
	if ctc["name"] != "wp_apply_patch" || ctc["call_id"] != "call_2" || ctc["input"] != "*** Update File: ~/.codex/config.toml\nCodex" {
		t.Fatalf("custom_tool_call item wrong: %+v", ctc)
	}
	// [6] function_call_output
	fco := inArr[6].(map[string]any)
	if fco["call_id"] != "call_1" || !strings.Contains(fco["output"].(string), "Antigravity") || !strings.Contains(fco["output"].(string), "run_command") || !strings.Contains(fco["output"].(string), "~/."+"gem"+"ini/config.toml") {
		t.Fatalf("function_call_output wrong: %+v", fco)
	}
	// [7] custom_tool_call_output
	ctco := inArr[7].(map[string]any)
	ctcoTxt := ctco["output"].([]any)[0].(map[string]any)["text"].(string)
	if ctco["call_id"] != "call_2" || !strings.Contains(ctcoTxt, "Antigravity") || !strings.Contains(ctcoTxt, "wp_apply_patch") {
		t.Fatalf("custom_tool_call_output wrong: %+v", ctco)
	}
	// [8] reasoning item preserved verbatim (even with content[].type == "text")
	rs := inArr[8].(map[string]any)
	if rs["encrypted_content"] != "enc_Codex_exec" ||
		rs["summary"].([]any)[0].(map[string]any)["text"] != "Codex reasoning summary exec" ||
		rs["content"].([]any)[0].(map[string]any)["text"] != "Codex reasoning content exec" {
		t.Fatalf("reasoning item was mutated: %+v", rs)
	}
}

// ── A3: Flat alias cap/no-op requirement and namespace policy ───────────────

func TestIssue53_A3_FlatAliasCapAndNamespacePolicy(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	// 1. Over-cap alias (>64 bytes) in tool_mappings returns 503 with 0 upstream attempts
	overCapTarget := strings.Repeat("a", 65)
	cfgYAML := fmt.Sprintf("tool_mappings:\n  codex:\n    exec: %s\n", overCapTarget)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(cfgYAML)))

	reqOverCap := []byte(`{"input":"hi","tools":[{"type":"function","name":"exec"}]}`)
	resOver, attOver := callResponsesRequestIntercept(t, "a3-overcap", "openai-response", "agy/gpt-5", reqOverCap, h)
	if attOver != 0 || !resOver.Terminate || resOver.StatusCode != 503 {
		t.Fatalf("expected 503 for >64-byte flat alias target, got attempts=%d resp=%+v", attOver, resOver)
	}

	// Restore default config
	applyFilterConfig(defaultFilterConfig())

	// 2. Nonempty namespace children (mcp__*, already-qualified, equals-namespace, normal), empty namespace, Gemini `children`, and flat-name precedence over ignored `function.name`
	nsBody := []byte(`{
		"input": "hi",
		"tools": [
			{
				"type": "function",
				"name": "exec",
				"function": {"name": "ignored_fallback_name"},
				"description": "Flat name wins over function.name"
			},
			{
				"type": "namespace",
				"name": "   ",
				"tools": [
					{"type": "function", "name": "request_user_input"},
					{"type": "custom", "name": "apply_patch"}
				]
			},
			{
				"type": "namespace",
				"name": "ns_box",
				"tools": [
					{"type": "function", "name": "mcp__inner__tool"},
					{"type": "function", "name": "ns_box__already_qual"},
					{"type": "function", "name": "ns_box"},
					{"type": "custom", "name": "child_patch"}
				]
			},
			{
				"type": "namespace",
				"name": "gem_only",
				"children": [
					{"type": "function", "name": "gem_child"}
				]
			}
		]
	}`)

	resNS, attNS := callResponsesRequestIntercept(t, "a3-ns-ok", "openai-response", "agy/gpt-5", nsBody, h)
	if attNS != 1 || resNS.Terminate {
		t.Fatalf("expected 1 attempt for namespace body, got attempts=%d resp=%+v", attNS, resNS)
	}
	var mNS map[string]any
	_ = json.Unmarshal(resNS.Body, &mNS)
	tools := mNS["tools"].([]any)

	// Tool 0: top-level `name` rewritten to `run_command`, ignored `function.name` untouched!
	t0 := tools[0].(map[string]any)
	if t0["name"] != "run_command" || t0["function"].(map[string]any)["name"] != "ignored_fallback_name" {
		t.Fatalf("effective flat name precedence failed: %+v", t0)
	}

	// Tool 1: empty namespace children follow the flat rule
	emptyNSChildren := tools[1].(map[string]any)["tools"].([]any)
	if emptyNSChildren[0].(map[string]any)["name"] != "ask_question" || emptyNSChildren[1].(map[string]any)["name"] != "wp_apply_patch" {
		t.Fatalf("empty namespace children were not treated as flat: %+v", emptyNSChildren)
	}

	// Tool 2: nonempty namespace children stay pass-through with no reverse authority
	nonEmptyChildren := tools[2].(map[string]any)["tools"].([]any)
	if nonEmptyChildren[0].(map[string]any)["name"] != "mcp__inner__tool" ||
		nonEmptyChildren[1].(map[string]any)["name"] != "ns_box__already_qual" ||
		nonEmptyChildren[2].(map[string]any)["name"] != "ns_box" ||
		nonEmptyChildren[3].(map[string]any)["name"] != "child_patch" {
		t.Fatalf("nonempty namespace children were mutated: %+v", nonEmptyChildren)
	}

	plan := globalAliasPlanManager.get("a3-ns-ok")
	for _, untouched := range []string{"mcp__inner__tool", "ns_box__already_qual", "ns_box", "child_patch", "gem_child"} {
		if _, has := plan.forward[untouched]; has {
			t.Fatalf("nonempty namespace child %q leaked into alias plan forward authority", untouched)
		}
		if _, has := plan.reverse[untouched]; has {
			t.Fatalf("nonempty namespace child %q leaked into alias plan reverse authority", untouched)
		}
	}

	// Verify Gemini `children` inventory is included in safety checks: if flat `exec` aliases to `run_command`, and `gem_only` has empty namespace with `children: [{"name":"run_command"}]`, it must fail 503!
	gemChildrenCollision := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "namespace", "name": "", "children": [{"type": "function", "name": "run_command"}]}
		]
	}`)
	resGemCol, attGemCol := callResponsesRequestIntercept(t, "a3-gem-children-col", "openai-response", "agy/gpt-5", gemChildrenCollision, h)
	if attGemCol != 0 || !resGemCol.Terminate || resGemCol.StatusCode != 503 {
		t.Fatalf("expected 503 collision for Gemini children carrier, got attempts=%d resp=%+v", attGemCol, resGemCol)
	}
}

// ── A4: `additional_tools` declarations and same-source repeats ─────────────

func TestIssue53_A4_AdditionalToolsAndSameSourceRepeats(t *testing.T) {
	isolateResponsesTestState(t)

	// Detection solely from `additional_tools` + same-source repeats across `tools` and `additional_tools` with differing schemas/descriptions
	reqBody := []byte(`{
		"input": [
			{"role": "user", "content": "run task"},
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "exec",
						"description": "Secondary exec description for Codex",
						"parameters": {"type": "object", "properties": {"b": {"type": "number"}}}
					},
					{"type": "function", "name": "request_user_input", "description": "Ask user"},
					{"type": "function", "name": "collaboration__spawn_agent", "description": "Spawn"},
					{"type": "function", "name": "mcp__srv__tool", "description": "Dynamic tool in additional_tools"}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"name": "exec",
				"description": "Primary exec description for Codex",
				"parameters": {"type": "object", "properties": {"a": {"type": "string"}}}
			}
		]
	}`)

	res, att := callResponsesRequestIntercept(t, "a4-add-tools", "openai-response", "agy/gpt-5", reqBody, nil)
	if att != 1 || res.Terminate {
		t.Fatalf("expected 1 attempt, got attempts=%d resp=%+v", att, res)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)

	topTool := m["tools"].([]any)[0].(map[string]any)
	addTools := m["input"].([]any)[1].(map[string]any)["tools"].([]any)
	addExec := addTools[0].(map[string]any)

	// Both occurrences of `exec` receive the same alias `run_command`
	if topTool["name"] != "run_command" || addExec["name"] != "run_command" {
		t.Fatalf("repeated exec declarations did not receive identical alias: top=%v add=%v", topTool["name"], addExec["name"])
	}
	// Primary winning schema on `tools[0]` still has property `a`, while `additional_tools[0]` still has `b`
	if _, ok := topTool["parameters"].(map[string]any)["properties"].(map[string]any)["a"]; !ok {
		t.Fatalf("top-level winning schema lost property 'a': %+v", topTool)
	}
	if _, ok := addExec["parameters"].(map[string]any)["properties"].(map[string]any)["b"]; !ok {
		t.Fatalf("additional_tools schema lost property 'b': %+v", addExec)
	}
	// Dynamic tool in additional_tools cloaked
	if addTools[3].(map[string]any)["name"] != fallbackAliasForSource("mcp__srv__tool") {
		t.Fatalf("additional_tools mcp__srv__tool not cloaked: %+v", addTools[3])
	}
}

// ── A5: All §3.5 selector shapes and §3.4 history/selector owner decisions ──

func TestIssue53_A5_SelectorsAndHistoryOwnerResolution(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}
	dynAlias := fallbackAliasForSource("mcp__srv__tool")

	// 1. Supported selector shapes: flat function, nested function, flat custom, nested custom, multiple identical name fields, dynamic mcp__srv__tool
	selectorCases := []struct {
		name     string
		selector string
		wantFunc string
	}{
		{"flat_func", `{"type":"function","name":"exec"}`, "run_command"},
		{"nested_func", `{"type":"function","function":{"name":"request_user_input"}}`, "ask_question"},
		{"flat_custom", `{"type":"custom","name":"apply_patch"}`, "wp_apply_patch"},
		{"nested_custom", `{"type":"custom","custom":{"name":"apply_patch"}}`, "wp_apply_patch"},
		{"multi_identical", `{"type":"function","name":"mcp__srv__tool","function":{"name":"mcp__srv__tool"}}`, dynAlias},
	}
	for i, tc := range selectorCases {
		body := []byte(fmt.Sprintf(`{
			"input": "hi",
			"tools": [
				{"type": "function", "name": "exec"},
				{"type": "function", "name": "request_user_input"},
				{"type": "custom", "name": "apply_patch"},
				{"type": "function", "name": "mcp__srv__tool"}
			],
			"tool_choice": %s
		}`, tc.selector))
		reqID := fmt.Sprintf("a5-sel-%d", i)
		res, att := callResponsesRequestIntercept(t, reqID, "openai-response", "agy/gpt-5", body, h)
		if att != 1 || res.Terminate {
			t.Fatalf("%s: expected 1 attempt, got %d (%+v)", tc.name, att, res)
		}
		var m map[string]any
		_ = json.Unmarshal(res.Body, &m)
		tcOut := m["tool_choice"].(map[string]any)
		if rawName, ok := tcOut["name"].(string); ok && rawName != tc.wantFunc {
			t.Fatalf("%s: tool_choice.name = %q, want %q", tc.name, rawName, tc.wantFunc)
		}
		if fn, ok := tcOut["function"].(map[string]any); ok {
			if fn["name"] != tc.wantFunc {
				t.Fatalf("%s: tool_choice.function.name = %v, want %q", tc.name, fn["name"], tc.wantFunc)
			}
		}
		if cust, ok := tcOut["custom"].(map[string]any); ok {
			if cust["name"] != tc.wantFunc {
				t.Fatalf("%s: tool_choice.custom.name = %v, want %q", tc.name, cust["name"], tc.wantFunc)
			}
		}
	}

	// 2. Case 1 from §3.4: Namespace `tools`, child `tools__exec`, history `name:"tools__exec"` without `namespace` -> pass-through
	case1Body := []byte(`{
		"input": [
			{"type": "function_call", "call_id": "c1", "name": "tools__exec", "arguments": "{}"}
		],
		"tools": [
			{"type": "namespace", "name": "tools", "tools": [{"type": "function", "name": "tools__exec"}]},
			{"type": "function", "name": "request_user_input"}
		],
		"tool_choice": "auto"
	}`)
	resC1, attC1 := callResponsesRequestIntercept(t, "a5-case1", "openai-response", "agy/gpt-5", case1Body, h)
	if attC1 != 1 {
		t.Fatalf("case1 failed: %+v", resC1)
	}
	var mC1 map[string]any
	_ = json.Unmarshal(resC1.Body, &mC1)
	if got := mC1["input"].([]any)[0].(map[string]any)["name"]; got != "tools__exec" {
		t.Fatalf("qualified history without namespace was mutated: %v", got)
	}

	// 3. Case 2 from §3.4: Flat `exec` plus namespace `tools/exec`, bare selector `exec` -> flat wins, selector becomes `run_command`
	case2Body := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "namespace", "name": "tools", "tools": [{"type": "function", "name": "exec"}]}
		],
		"tool_choice": {"type": "function", "name": "exec"}
	}`)
	resC2, attC2 := callResponsesRequestIntercept(t, "a5-case2", "openai-response", "agy/gpt-5", case2Body, h)
	if attC2 != 1 {
		t.Fatalf("case2 failed: %+v", resC2)
	}
	var mC2 map[string]any
	_ = json.Unmarshal(resC2.Body, &mC2)
	if got := mC2["tool_choice"].(map[string]any)["name"]; got != "run_command" {
		t.Fatalf("case2 selector = %v, want run_command", got)
	}

	// 4. Case 3 from §3.4: Flat `exec -> run_command` plus namespace `ns/run_command`:
	// - Bare selector `run_command` rejects 503 (aliasing shadows namespace child!)
	// - Explicit namespace selector `{"type":"function","namespace":"ns","name":"run_command"}` succeeds and stays `run_command`!
	case3Bare := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": "run_command"}]}
		],
		"tool_choice": {"type": "function", "name": "run_command"}
	}`)
	resC3Bare, attC3Bare := callResponsesRequestIntercept(t, "a5-case3-bare", "openai-response", "agy/gpt-5", case3Bare, h)
	if attC3Bare != 0 || !resC3Bare.Terminate || resC3Bare.StatusCode != 503 {
		t.Fatalf("expected 503 when bare run_command selector is shadowed by exec->run_command, got attempts=%d resp=%+v", attC3Bare, resC3Bare)
	}

	case3ExplicitNS := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": "run_command"}]}
		],
		"tool_choice": {"type": "function", "namespace": "ns", "name": "run_command"}
	}`)
	resC3NS, attC3NS := callResponsesRequestIntercept(t, "a5-case3-ns", "openai-response", "agy/gpt-5", case3ExplicitNS, h)
	if attC3NS != 1 || resC3NS.Terminate {
		t.Fatalf("expected explicit namespace selector to succeed, got attempts=%d resp=%+v", attC3NS, resC3NS)
	}
	var mC3NS map[string]any
	_ = json.Unmarshal(resC3NS.Body, &mC3NS)
	if got := mC3NS["tool_choice"].(map[string]any)["name"]; got != "run_command" {
		t.Fatalf("explicit namespace selector mutated: %v", got)
	}

	// 5. Two namespace `exec` children (ambiguous in Chat, first-wins in Gemini) do NOT create a fallback alias
	twoNSExec := []byte(`{
		"input": [
			{"type": "function_call", "call_id": "c1", "name": "exec", "arguments": "{}"}
		],
		"tools": [
			{"type": "namespace", "name": "ns1", "tools": [{"type": "function", "name": "exec"}]},
			{"type": "namespace", "name": "ns2", "tools": [{"type": "function", "name": "exec"}]},
			{"type": "function", "name": "request_user_input"}
		]
	}`)
	resTwoNS, attTwoNS := callResponsesRequestIntercept(t, "a5-two-ns-exec", "openai-response", "agy/gpt-5", twoNSExec, h)
	if attTwoNS != 1 || resTwoNS.Terminate {
		t.Fatalf("expected two namespace exec children to pass admission, got attempts=%d resp=%+v", attTwoNS, resTwoNS)
	}
	var mTwoNS map[string]any
	_ = json.Unmarshal(resTwoNS.Body, &mTwoNS)
	if got := mTwoNS["input"].([]any)[0].(map[string]any)["name"]; got != "exec" {
		t.Fatalf("ambiguous namespace child reference created fallback alias: %v", got)
	}

	// 6. Conflicting populated name fields in selector (`name:"exec"` vs `function.name:"apply_patch"`) rejects 503 when renaming identities
	conflictSel := []byte(`{
		"input": "hi",
		"tools": [{"type": "function", "name": "exec"}, {"type": "custom", "name": "apply_patch"}],
		"tool_choice": {"type": "function", "name": "exec", "function": {"name": "apply_patch"}}
	}`)
	resConf, attConf := callResponsesRequestIntercept(t, "a5-conflict-sel", "openai-response", "agy/gpt-5", conflictSel, h)
	if attConf != 0 || !resConf.Terminate || resConf.StatusCode != 503 {
		t.Fatalf("expected 503 for conflicting selector name fields, got attempts=%d resp=%+v", attConf, resConf)
	}

	// 7. Unsupported structured selector (`type:"tool"`, absent-type shorthand) rejects 503 when renaming identities, preserved when no identities rename
	unsuppSelRename := []byte(`{
		"input": "hi",
		"tools": [{"type": "function", "name": "exec"}],
		"tool_choice": {"type": "tool", "name": "exec"}
	}`)
	resUnsupp, attUnsupp := callResponsesRequestIntercept(t, "a5-unsupp-sel-rename", "openai-response", "agy/gpt-5", unsuppSelRename, h)
	if attUnsupp != 0 || !resUnsupp.Terminate || resUnsupp.StatusCode != 503 {
		t.Fatalf("expected 503 for unsupported structured selector when renaming, got attempts=%d resp=%+v", attUnsupp, resUnsupp)
	}

	unsuppSelNoRename := []byte(`{
		"input": "hi",
		"tools": [{"type": "function", "name": "run_command"}],
		"tool_choice": {"type": "tool", "name": "run_command"}
	}`)
	resNoRen, attNoRen := callResponsesRequestIntercept(t, "a5-unsupp-sel-norename", "openai-response", "agy/gpt-5", unsuppSelNoRename, h)
	if attNoRen != 1 || resNoRen.Terminate {
		t.Fatalf("expected pass-through when no tool identities rename, got attempts=%d resp=%+v", attNoRen, resNoRen)
	}
}

// ── A6 & A16: Non-stream response reversal & recorded selector echo restoration ──

func TestIssue53_A6_A16_NonStreamResponseReversalAndSelectorEcho(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}
	dynAlias := fallbackAliasForSource("mcp__srv__tool")

	reqBody := []byte(`{
		"instructions": "You are Codex.",
		"input": "run task",
		"tools": [
			{"type": "function", "name": "exec", "description": "Codex exec", "parameters": {"type": "object", "description": "Codex param"}},
			{"type": "custom", "name": "apply_patch", "description": "Codex patch"},
			{"type": "function", "name": "mcp__srv__tool", "description": "Dynamic"},
			{"type": "function", "name": "search_web", "description": "Native target"},
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": "child_tool"}]}
		],
		"tool_choice": {"type": "function", "name": "exec"}
	}`)
	reqRes, att := callResponsesRequestIntercept(t, "a6-req-1", "openai-response", "agy/gpt-5", reqBody, h)
	if att != 1 {
		t.Fatalf("request failed: %+v", reqRes)
	}

	// 1. Chat-converted non-stream echo (nested tools[].function.name and tool_choice.function.name, no instructions)
	gemAgents := "~/." + "gem" + "ini/" + "AGE" + "NTS.md"
	chatConvertedResp := []byte(fmt.Sprintf(`{
		"id": "resp_chat_1",
		"object": "response",
		"output_text": "Antigravity finished %[3]s",
		"tools": [
			{"type": "function", "function": {"name": "run_command", "description": "Antigravity run_command", "parameters": {"type": "object", "description": "Antigravity param"}}},
			{"type": "function", "function": {"name": "%[1]s", "description": "Dynamic"}},
			{"type": "function", "function": {"name": "search_web", "description": "Native target"}},
			{"type": "function", "function": {"name": "ask_question", "description": "Undeclared target"}}
		],
		"tool_choice": {"type": "function", "function": {"name": "run_command"}, "extra": "keep"},
		"output": [
			{
				"id": "rs_1",
				"type": "reasoning",
				"summary": [{"type": "summary_text", "text": "Antigravity %[3]s"}],
				"content": [{"type": "text", "text": "Antigravity %[3]s"}],
				"encrypted_content": "Antigravity"
			},
			{
				"id": "msg_1",
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hello from Antigravity reading %[3]s"}]
			},
			{
				"id": "fc_1",
				"type": "function_call",
				"call_id": "call_1",
				"name": "run_command",
				"arguments": "{\"cmd\":\"cat %[3]s\",\"brand\":\"Antigravity\"}"
			},
			{
				"id": "fc_ns",
				"type": "function_call",
				"call_id": "call_ns",
				"namespace": "ns",
				"name": "run_command",
				"arguments": "{}"
			},
			{
				"id": "ctc_1",
				"type": "custom_tool_call",
				"call_id": "call_2",
				"name": "wp_apply_patch",
				"input": "*** Update File: %[3]s\n+Antigravity"
			},
			{
				"id": "fc_dyn",
				"type": "function_call",
				"call_id": "call_3",
				"name": "%[2]s",
				"arguments": "{}"
			}
		]
	}`, dynAlias, dynAlias, gemAgents))

	respOut1 := callResponsesResponseIntercept(t, "a6-req-1", "openai-response", "agy/gpt-5", reqRes.Body, reqRes.Body, chatConvertedResp, h)
	var m1 map[string]any
	if err := json.Unmarshal(respOut1.Body, &m1); err != nil {
		t.Fatalf("unmarshal respOut1: %v", err)
	}
	if _, hasInst := m1["instructions"]; hasInst {
		t.Fatalf("instructions should remain absent when not present in response")
	}
	if got := m1["output_text"].(string); got != "Codex finished ~/.codex/AGENTS.md" {
		t.Fatalf("output_text = %q", got)
	}
	// Echoed tools in nested Chat shape restored without reshaping
	echoTools1 := m1["tools"].([]any)
	if got := echoTools1[0].(map[string]any)["function"].(map[string]any)["name"]; got != "exec" {
		t.Fatalf("echoTools[0].function.name = %v, want exec", got)
	}
	if got := echoTools1[1].(map[string]any)["function"].(map[string]any)["name"]; got != "mcp__srv__tool" {
		t.Fatalf("echoTools[1].function.name = %v, want mcp__srv__tool", got)
	}
	if got := echoTools1[2].(map[string]any)["function"].(map[string]any)["name"]; got != "search_web" {
		t.Fatalf("native target search_web mutated: %v", got)
	}
	// Undeclared ask_question stays ask_question
	if got := echoTools1[3].(map[string]any)["function"].(map[string]any)["name"]; got != "ask_question" {
		t.Fatalf("undeclared target ask_question was reversed: %v", got)
	}
	// Echoed tool_choice kept nested shape and `extra` field, restored `exec`
	tc1 := m1["tool_choice"].(map[string]any)
	if tc1["extra"] != "keep" || tc1["function"].(map[string]any)["name"] != "exec" {
		t.Fatalf("chat-shaped tool_choice echo restoration failed: %+v", tc1)
	}
	if _, hasFlat := tc1["name"]; hasFlat {
		t.Fatalf("chat-shaped tool_choice gained flat name field: %+v", tc1)
	}

	outItems := m1["output"].([]any)
	// [0] reasoning untouched
	rs0 := outItems[0].(map[string]any)
	if rs0["summary"].([]any)[0].(map[string]any)["text"] != "Antigravity "+gemAgents ||
		rs0["content"].([]any)[0].(map[string]any)["text"] != "Antigravity "+gemAgents {
		t.Fatalf("reasoning item was mutated on response: %+v", rs0)
	}
	// [1] message text reversed
	msg1 := outItems[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if msg1 != "Hello from Codex reading ~/.codex/AGENTS.md" {
		t.Fatalf("message text = %q", msg1)
	}
	// [2] function_call: name -> exec, path in arguments reversed, bare "Antigravity" in arguments preserved!
	fc1 := outItems[2].(map[string]any)
	if fc1["name"] != "exec" || fc1["arguments"] != "{\"cmd\":\"cat ~/.codex/AGENTS.md\",\"brand\":\"Antigravity\"}" {
		t.Fatalf("fc1 wrong: %+v", fc1)
	}
	// [3] namespaced function_call with `name:"run_command", namespace:"ns"` is NOT flat-reversed!
	fcNS := outItems[3].(map[string]any)
	if fcNS["name"] != "run_command" || fcNS["namespace"] != "ns" {
		t.Fatalf("namespaced function_call was flat-reversed: %+v", fcNS)
	}
	// [4] custom_tool_call: name -> apply_patch, path in input reversed, bare "Antigravity" preserved
	ctc1 := outItems[4].(map[string]any)
	if ctc1["name"] != "apply_patch" || ctc1["input"] != "*** Update File: ~/.codex/AGENTS.md\n+Antigravity" {
		t.Fatalf("ctc1 wrong: %+v", ctc1)
	}
	// [5] dynamic tool call restored
	fcDyn := outItems[5].(map[string]any)
	if fcDyn["name"] != "mcp__srv__tool" {
		t.Fatalf("dynamic tool call not restored: %+v", fcDyn)
	}

	// 2. Gemini/AGY non-stream echo (flat tools[].name, flat tool_choice.name, echoed instructions)
	geminiResp := []byte(`{
		"id": "resp_gem_1",
		"object": "response",
		"instructions": "You are Antigravity.",
		"tools": [
			{"type": "function", "name": "run_command", "description": "Antigravity exec"},
			{"type": "custom", "name": "wp_apply_patch", "description": "Antigravity patch"},
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": "run_command"}]}
		],
		"tool_choice": {"type": "function", "name": "run_command"},
		"output": []
	}`)
	respOut2 := callResponsesResponseIntercept(t, "a6-req-1", "openai-response", "agy/gpt-5", reqRes.Body, reqRes.Body, geminiResp, h)
	var m2 map[string]any
	_ = json.Unmarshal(respOut2.Body, &m2)
	if !strings.Contains(m2["instructions"].(string), "Codex") {
		t.Fatalf("echoed instructions not reversed: %v", m2["instructions"])
	}
	gemTools := m2["tools"].([]any)
	if gemTools[0].(map[string]any)["name"] != "exec" || gemTools[1].(map[string]any)["name"] != "apply_patch" {
		t.Fatalf("flat echoed tools not reversed: %+v", gemTools)
	}
	// Namespace container child `run_command` preserved
	if gemTools[2].(map[string]any)["tools"].([]any)[0].(map[string]any)["name"] != "run_command" {
		t.Fatalf("namespace container child in echoed tools was mutated: %+v", gemTools[2])
	}
	if m2["tool_choice"].(map[string]any)["name"] != "exec" {
		t.Fatalf("flat tool_choice echo not reversed: %+v", m2["tool_choice"])
	}

	// 3. A16 negative: A selector that was NOT rewritten (e.g., request had no selector or had `tool_choice: "auto"`) is untouched even if response echo has `name: "run_command"`
	reqNoSel := []byte(`{
		"input": "hi",
		"tools": [{"type": "function", "name": "exec"}],
		"tool_choice": "auto"
	}`)
	reqResNoSel, _ := callResponsesRequestIntercept(t, "a16-no-sel", "openai-response", "agy/gpt-5", reqNoSel, h)
	echoWithRunCmd := []byte(`{
		"id": "resp_nosel",
		"object": "response",
		"tool_choice": {"type": "function", "name": "run_command"},
		"output": []
	}`)
	respNoSel := callResponsesResponseIntercept(t, "a16-no-sel", "openai-response", "agy/gpt-5", reqResNoSel.Body, reqResNoSel.Body, echoWithRunCmd, h)
	if len(respNoSel.Body) != 0 {
		t.Fatalf("expected untouched response when selector was not rewritten, got: %s", respNoSel.Body)
	}
}

// ── A7: Split text tokens reassemble/reverse and uncovered held tail flushes ──

func TestIssue53_A7_StreamedTextHoldAndFlushEncoding(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	reqBody := []byte(`{"input":"hi","tools":[{"type":"function","name":"exec"}]}`)
	reqRes, _ := callResponsesRequestIntercept(t, "a7-stream-1", "openai-response", "agy/gpt-5", reqBody, h)

	chunks := []string{
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"id\":\"msg_item_2\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_item_2\",\"output_index\":2,\"content_index\":1,\"delta\":\"Hello Antig\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_item_2\",\"output_index\":2,\"content_index\":1,\"delta\":\"ravity! Path: ~/.gem\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_item_2\",\"output_index\":2,\"content_index\":1,\"delta\":\"ini/config and Antig\"}\n\n",
		// Terminal with empty output[] so the held tail "Antig" on msg_item_2 is uncovered and must flush before terminal!
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"output\":[]}}\n\n",
	}

	var wire []byte
	for i, ch := range chunks {
		res := callResponsesStreamChunkIntercept(t, "a7-stream-1", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes.Body, reqRes.Body, h)
		wire = appendStreamChunkWire(wire, []byte(ch), res)
	}
	wireStr := string(wire)
	if !strings.Contains(wireStr, `"delta":"Hello "`) || !strings.Contains(wireStr, `"delta":"Codex! Path: ~/"`) || !strings.Contains(wireStr, `"delta":".codex/config and "`) {
		t.Fatalf("expected reconstructed delta chunks before terminal flush:\n%s", wireStr)
	}
	// Check that the synthetic flush frame before response.completed uses `event: response.output_text.delta` with item_id="msg_item_2", output_index=2, content_index=1, delta="Antig"
	wantFlush := "event: response.output_text.delta\ndata: {\"content_index\":1,\"delta\":\"Antig\",\"item_id\":\"msg_item_2\",\"output_index\":2,\"type\":\"response.output_text.delta\"}\n\n"
	termIdx := strings.Index(wireStr, "event: response.completed")
	flushIdx := strings.Index(wireStr, wantFlush)
	if flushIdx < 0 || termIdx < 0 || flushIdx > termIdx {
		t.Fatalf("expected synthetic Responses text flush before terminal:\nwantFlush=%q\nwire=%s", wantFlush, wireStr)
	}
	if strings.Contains(wireStr, `"choices"`) {
		t.Fatalf("Responses flush used Chat choices encoding: %s", wireStr)
	}
}

// ── A8: Function-arguments & custom-input lanes + native delimiter-free callbacks + split JSON string starting with `data:` ──

func TestIssue53_A8_StreamedFunctionAndCustomArgumentsAcrossFramings(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	reqBody := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "custom", "name": "apply_patch"}
		]
	}`)

	// Mode 1: Native delimiter-free scanner callbacks (separate `event:` and `data:` lines without trailing newlines, plus data-only callbacks, no empty callback, no [DONE])
	reqRes1, _ := callResponsesRequestIntercept(t, "a8-native", "openai-response", "agy/gpt-5", reqBody, h)
	nativeCallbacks := []string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"wp_apply_patch","input":""}}`,
		`event: response.custom_tool_call_input.delta`,
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","output_index":0,"delta":"*** Update File: ~/.gem"}`,
		// Data-only callback without `event:` line and without trailing newline!
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","output_index":0,"delta":"ini/AGENTS.md\n+Antigravity\n+~/.gem"}`,
		`event: response.custom_tool_call_input.done`,
		`data: {"type":"response.custom_tool_call_input.done","item_id":"ctc_1","output_index":0,"input":"*** Update File: ~/.` + `gem` + `ini/` + `AGE` + `NTS.md\n+Antigravity"}`,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_2","name":"run_command","arguments":""}}`,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"path\":\"~/.gem"}`,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"ini/config.toml\",\"note\":\"Antigravity\",\"hold\":\"~/.gem"}`,
		// Terminal with empty output[] so fc_1's uncovered carry "~/.gem" flushes as `response.function_call_arguments.delta`!
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_a8","object":"response","output":[]}}`,
	}

	var nativeWire []byte
	for i, cb := range nativeCallbacks {
		res := callResponsesStreamChunkIntercept(t, "a8-native", "openai-response", "agy/gpt-5", i, []byte(cb), reqRes1.Body, reqRes1.Body, h)
		nativeWire = appendStreamChunkWire(nativeWire, []byte(cb), res)
	}
	nativeStr := string(nativeWire)
	// Verify custom tool name restored in output_item.added
	if !strings.Contains(nativeStr, `"name":"apply_patch"`) || strings.Contains(nativeStr, `"wp_apply_patch"`) {
		t.Fatalf("custom tool name not restored in native stream: %s", nativeStr)
	}
	// Verify split custom input delta and done restored `~/.codex/AGENTS.md` while keeping `+Antigravity` literal
	if !strings.Contains(nativeStr, `.codex/AGENTS.md\n+Antigravity`) || !strings.Contains(nativeStr, `"input":"*** Update File: ~/.codex/AGENTS.md\n+Antigravity"`) {
		t.Fatalf("custom input delta/done did not restore ~/.codex/AGENTS.md while preserving +Antigravity: %s", nativeStr)
	}
	// Verify ctc_1 carry `.gem` was cleared by `.done` and NOT flushed at terminal, whereas fc_1 carry `.gem` WAS flushed as `response.function_call_arguments.delta`!
	if strings.Contains(nativeStr, `"delta":".gem","item_id":"ctc_1"`) {
		t.Fatalf("cleared ctc_1 carry was unexpectedly flushed: %s", nativeStr)
	}
	wantFcFlush := "event: response.function_call_arguments.delta\ndata: {\"delta\":\".gem\",\"item_id\":\"fc_1\",\"output_index\":1,\"type\":\"response.function_call_arguments.delta\"}\n\n"
	if !strings.Contains(nativeStr, wantFcFlush) {
		t.Fatalf("expected uncovered function_call_arguments carry flush for fc_1, got:\n%s", nativeStr)
	}

	// Mode 2: Uncovered custom_tool_call_input carry flushes as `response.custom_tool_call_input.delta` (never function_call_arguments or output_text)
	reqRes2, _ := callResponsesRequestIntercept(t, "a8-custom-flush", "openai-response", "agy/gpt-5", reqBody, h)
	customFlushChunks := []string{
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"ctc_9\",\"type\":\"custom_tool_call\",\"call_id\":\"c9\",\"name\":\"wp_apply_patch\",\"input\":\"\"}}\n\n",
		"event: response.custom_tool_call_input.delta\ndata: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"ctc_9\",\"output_index\":0,\"delta\":\"~/.gem\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r9\",\"object\":\"response\",\"output\":[]}}\n\n",
	}
	var customFlushWire []byte
	for i, ch := range customFlushChunks {
		res := callResponsesStreamChunkIntercept(t, "a8-custom-flush", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes2.Body, reqRes2.Body, h)
		customFlushWire = appendStreamChunkWire(customFlushWire, []byte(ch), res)
	}
	wantCustomFlush := "event: response.custom_tool_call_input.delta\ndata: {\"delta\":\".gem\",\"item_id\":\"ctc_9\",\"output_index\":0,\"type\":\"response.custom_tool_call_input.delta\"}\n\n"
	if !strings.Contains(string(customFlushWire), `"delta":"~/"`) || !strings.Contains(string(customFlushWire), wantCustomFlush) {
		t.Fatalf("expected custom_tool_call_input.delta flush, got:\n%s", customFlushWire)
	}

	// Mode 3: Genuinely split JSON payload whose continuation chunk starts with literal `data:` inside a JSON string must NOT acquire an injected newline or become a new event!
	reqRes3, _ := callResponsesRequestIntercept(t, "a8-split-json", "openai-response", "agy/gpt-5", reqBody, h)
	splitJSONChunks := []string{
		"event: response.custom_tool_call_input.delta\ndata: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"ctc_split\",\"output_index\":0,\"delta\":\"line1: ",
		"data: literal inside json string ~/." + "gem" + "ini/" + "AGE" + "NTS.md!\"}\n\n",
	}
	var splitWire []byte
	for i, ch := range splitJSONChunks {
		res := callResponsesStreamChunkIntercept(t, "a8-split-json", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes3.Body, reqRes3.Body, h)
		splitWire = appendStreamChunkWire(splitWire, []byte(ch), res)
	}
	if !strings.Contains(string(splitWire), `"delta":"line1: data: literal inside json string ~/.codex/AGENTS.md!"`) ||
		strings.Count(string(splitWire), "event: response.custom_tool_call_input.delta") != 1 {
		t.Fatalf("split JSON string starting with literal 'data:' was corrupted:\n%s", splitWire)
	}

	// Mode 4: Standalone raw JSON chunks
	reqRes4, _ := callResponsesRequestIntercept(t, "a8-raw-json", "openai-response", "agy/gpt-5", reqBody, h)
	rawChunks := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_raw","type":"custom_tool_call","call_id":"c_raw","name":"wp_apply_patch","input":""}}`,
		`{"type":"response.custom_tool_call_input.delta","item_id":"ctc_raw","output_index":0,"delta":"~/.gem"}`,
		`{"type":"response.custom_tool_call_input.delta","item_id":"ctc_raw","output_index":0,"delta":"ini/AGENTS.md!"}`,
	}
	var rawOut []string
	for i, ch := range rawChunks {
		res := callResponsesStreamChunkIntercept(t, "a8-raw-json", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes4.Body, reqRes4.Body, h)
		if len(res.Body) > 0 {
			rawOut = append(rawOut, string(res.Body))
		} else {
			rawOut = append(rawOut, ch)
		}
	}
	if !strings.Contains(rawOut[0], `"name":"apply_patch"`) || !strings.Contains(rawOut[1], `"delta":"~/"`) || !strings.Contains(rawOut[2], `"delta":".codex/AGENTS.md!"`) {
		t.Fatalf("standalone raw JSON chunks failed: %+v", rawOut)
	}
}

// ── A9: Complete `.done` resets lanes, terminal nested output, reasoning & namespace preservation ──

func TestIssue53_A9_DoneResetTerminalCoverageAndReasoningPreservation(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	reqBody := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": "run_command"}]}
		],
		"tool_choice": {"type": "function", "name": "exec"}
	}`)
	reqRes, _ := callResponsesRequestIntercept(t, "a9-stream", "openai-response", "agy/gpt-5", reqBody, h)

	events := []string{
		// 1. response.created with echoed instructions and tools/tool_choice
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r_a9\",\"object\":\"response\",\"instructions\":\"You are Antigravity.\",\"tool_choice\":{\"type\":\"function\",\"name\":\"run_command\"}}}\n\n",
		// 2. message item added + delta ending in held token "Antig", then covered by terminal `response.output[]`
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_cov\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_cov\",\"output_index\":0,\"content_index\":0,\"delta\":\"Text with Antig\"}\n\n",
		// 3. reasoning item added, reasoning deltas, content_part.done, and output_item.done with content:[{type:"text",text:"Antigravity .claude/CLAUDE.md"}] -> MUST STAY LITERAL!
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"rs_item\",\"type\":\"reasoning\",\"summary\":[]}}\n\n",
		"event: response.reasoning_text.delta\ndata: {\"type\":\"response.reasoning_text.delta\",\"item_id\":\"rs_item\",\"output_index\":1,\"content_index\":0,\"delta\":\"Antigravity ~/.claude/CLAUDE.md\"}\n\n",
		"event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"item_id\":\"rs_item\",\"output_index\":1,\"content_index\":0,\"part\":{\"type\":\"text\",\"text\":\"Antigravity .claude/CLAUDE.md\"}}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"rs_item\",\"type\":\"reasoning\",\"content\":[{\"type\":\"text\",\"text\":\"Antigravity .claude/CLAUDE.md\"}]}}\n\n",
		// 4. Namespaced function call added and done (`name:"run_command", namespace:"ns"`) -> MUST STAY `run_command`!
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"id\":\"fc_ns\",\"type\":\"function_call\",\"namespace\":\"ns\",\"name\":\"run_command\",\"arguments\":\"\"}}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"id\":\"fc_ns\",\"type\":\"function_call\",\"namespace\":\"ns\",\"name\":\"run_command\",\"arguments\":\"{}\"}}\n\n",
		// 5. Terminal response.completed covering `msg_cov` in `response.output[]` -> held carry "Antig" on `msg_cov` must be DISCARDED (not re-emitted before terminal)!
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r_a9\",\"object\":\"response\",\"output\":[{\"id\":\"msg_cov\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Text with Antigravity!\"}]},{\"id\":\"fc_ns\",\"type\":\"function_call\",\"namespace\":\"ns\",\"name\":\"run_command\",\"arguments\":\"{}\"}]}}\n\n",
	}

	var wire []byte
	for i, ev := range events {
		res := callResponsesStreamChunkIntercept(t, "a9-stream", "openai-response", "agy/gpt-5", i, []byte(ev), reqRes.Body, reqRes.Body, h)
		wire = appendStreamChunkWire(wire, []byte(ev), res)
	}
	wireStr := string(wire)

	// Verify response.created reversed instructions & tool_choice
	if !strings.Contains(wireStr, `"instructions":"You are Codex."`) || !strings.Contains(wireStr, `"tool_choice":{"name":"exec","type":"function"}`) {
		t.Fatalf("response.created not reversed: %s", wireStr)
	}
	// Verify covered carry on msg_cov was NOT flushed as a second delta before response.completed
	if strings.Count(wireStr, "event: response.output_text.delta") != 1 {
		t.Fatalf("covered carry on msg_cov was re-emitted as extra delta:\n%s", wireStr)
	}
	// Verify terminal output[] message was reversed to "Text with Codex!"
	if !strings.Contains(wireStr, `"text":"Text with Codex!"`) {
		t.Fatalf("terminal output[] message not reversed: %s", wireStr)
	}
	// Verify reasoning content_part.done and output_item.done stayed literal
	if strings.Count(wireStr, `"text":"Antigravity .claude/CLAUDE.md"`) != 2 {
		t.Fatalf("reasoning text part was mutated:\n%s", wireStr)
	}
	// Verify namespaced `run_command` stayed `run_command` across added, done, and terminal
	if strings.Contains(wireStr, `"id":"fc_ns","name":"exec"`) {
		t.Fatalf("namespaced function_call was flat-reversed:\n%s", wireStr)
	}
}

// ── A10: Concurrency, multi-item/multi-content-index isolation, and late chunks ──

func TestIssue53_A10_ConcurrencyAndLaneIsolation(t *testing.T) {
	isolateResponsesTestState(t)

	// 1. Single stream with two output items and two content indices on item 0 + argument lane on item 1
	h := http.Header{"X-Cloak-Client": {"codex"}}
	reqBody := []byte(`{"input":"hi","tools":[{"type":"function","name":"exec"},{"type":"custom","name":"apply_patch"}]}`)
	reqRes, _ := callResponsesRequestIntercept(t, "a10-multi-lane", "openai-response", "agy/gpt-5", reqBody, h)

	multiChunks := []string{
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_0\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"run_command\",\"arguments\":\"\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_0\",\"output_index\":0,\"content_index\":0,\"delta\":\"Part0 Antig\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_0\",\"output_index\":0,\"content_index\":1,\"delta\":\"Part1 ~/.gem\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":1,\"delta\":\"{\\\"p\\\":\\\"~/.gem\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_0\",\"output_index\":0,\"content_index\":0,\"delta\":\"ravity\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_0\",\"output_index\":0,\"content_index\":1,\"delta\":\"ini/a\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":1,\"delta\":\"ini/b\\\"}\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r10\",\"object\":\"response\",\"output\":[]}}\n\n",
	}
	var wire []byte
	for i, ch := range multiChunks {
		res := callResponsesStreamChunkIntercept(t, "a10-multi-lane", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes.Body, reqRes.Body, h)
		wire = appendStreamChunkWire(wire, []byte(ch), res)
	}
	wireStr := string(wire)
	if !strings.Contains(wireStr, `"content_index":0,"delta":"Codex","item_id":"msg_0"`) ||
		!strings.Contains(wireStr, `"content_index":1,"delta":"Part1 ~/","item_id":"msg_0"`) ||
		!strings.Contains(wireStr, `"content_index":1,"delta":".codex/a","item_id":"msg_0"`) ||
		!strings.Contains(wireStr, `"delta":"{\"p\":\"~/","item_id":"fc_1"`) ||
		!strings.Contains(wireStr, `"delta":".codex/b\"}","item_id":"fc_1"`) {
		t.Fatalf("multi-lane carries interfered with each other:\n%s", wireStr)
	}

	// Late chunk after clean terminal must be a no-op!
	lateChunk := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_0\",\"output_index\":0,\"content_index\":0,\"delta\":\"Antigravity\"}\n\n")
	lateRes := callResponsesStreamChunkIntercept(t, "a10-multi-lane", "openai-response", "agy/gpt-5", 99, lateChunk, reqRes.Body, reqRes.Body, h)
	if len(lateRes.Body) != 0 || lateRes.DropChunk {
		t.Fatalf("late chunk after clean terminal was mutated: %+v", lateRes)
	}

	// 2. Concurrent requests with different clients/selectors/tools
	const workers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for w := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			reqID := fmt.Sprintf("a10-conc-%d", id)
			dynTool := fmt.Sprintf("mcp__srv__tool_%d", id)
			dynAlias := fallbackAliasForSource(dynTool)
			selTool := "exec"
			if id%2 == 1 {
				selTool = dynTool
			}
			rBody := []byte(fmt.Sprintf(`{
				"instructions": "You are Codex.",
				"input": "go",
				"tools": [
					{"type": "function", "name": "exec"},
					{"type": "function", "name": %q}
				],
				"tool_choice": {"type": "function", "name": %q}
			}`, dynTool, selTool))
			hdr := http.Header{"X-Cloak-Client": {"codex"}}
			rRes, att := callResponsesRequestIntercept(t, reqID, "openai-response", "agy/gpt-5", rBody, hdr)
			if att != 1 {
				errCh <- fmt.Errorf("worker %d: request failed", id)
				return
			}
			// Stream chunks
			c0 := fmt.Sprintf("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"m_%d\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n", id)
			c1 := fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m_%d\",\"output_index\":0,\"content_index\":0,\"delta\":\"Antig\"}\n\n", id)
			c2 := fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m_%d\",\"output_index\":0,\"content_index\":0,\"delta\":\"ravity\"}\n\n", id)
			wantSelAlias := "run_command"
			if id%2 == 1 {
				wantSelAlias = dynAlias
			}
			c3 := fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r_%d\",\"object\":\"response\",\"tool_choice\":{\"type\":\"function\",\"name\":%q},\"output\":[{\"id\":\"fc_%d\",\"type\":\"function_call\",\"name\":%q,\"arguments\":\"{}\"}]}}\n\n", id, wantSelAlias, id, dynAlias)

			var sWire []byte
			for idx, c := range []string{c0, c1, c2, c3} {
				sRes := callResponsesStreamChunkIntercept(t, reqID, "openai-response", "agy/gpt-5", idx, []byte(c), rRes.Body, rRes.Body, hdr)
				sWire = appendStreamChunkWire(sWire, []byte(c), sRes)
			}
			sStr := string(sWire)
			if !strings.Contains(sStr, `"delta":"Codex"`) ||
				!strings.Contains(sStr, fmt.Sprintf(`"tool_choice":{"name":%q,"type":"function"}`, selTool)) ||
				!strings.Contains(sStr, fmt.Sprintf(`"name":%q`, dynTool)) {
				errCh <- fmt.Errorf("worker %d: unexpected stream output: %s", id, sStr)
				return
			}
			callResponsesRequestComplete(t, reqID, "completed")
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// ── A11: ProtectedAGY rejects Responses/unknown shapes; OMP non-AGY bypass; WS no-op ──

func TestIssue53_A11_ProtectedAGYAndWebSocketBoundary(t *testing.T) {
	isolateResponsesTestState(t)

	// 1. ProtectedAGY rejects Responses shape with 503 omp_cloak_required and 0 attempts
	ompHdr := http.Header{"X-Cloak-Client": {"oh_my_pi"}}
	respShape := []byte(`{"input":"hi","tools":[{"type":"function","name":"read"}]}`)
	resOMPResp, attOMPResp := callResponsesRequestIntercept(t, "a11-omp-resp", "openai-response", "agy/claude-sonnet", respShape, ompHdr)
	if attOMPResp != 0 || !resOMPResp.Terminate || resOMPResp.StatusCode != 503 || !strings.Contains(string(resOMPResp.ResponseBody), "omp_cloak_required") {
		t.Fatalf("expected 503 omp_cloak_required for ProtectedAGY on Responses shape, got attempts=%d resp=%+v", attOMPResp, resOMPResp)
	}

	// 2. ProtectedAGY rejects unknown shape (e.g. both messages and input) with 503 omp_cloak_required
	unkShape := []byte(`{"messages":[],"input":"hi","tools":[{"type":"function","function":{"name":"view_file"}}]}`)
	resOMPUnk, attOMPUnk := callResponsesRequestIntercept(t, "a11-omp-unk", "openai", "agy/claude-sonnet", unkShape, ompHdr)
	if attOMPUnk != 0 || !resOMPUnk.Terminate || resOMPUnk.StatusCode != 503 || !strings.Contains(string(resOMPUnk.ResponseBody), "omp_cloak_required") {
		t.Fatalf("expected 503 omp_cloak_required for ProtectedAGY on unknown shape, got attempts=%d resp=%+v", attOMPUnk, resOMPUnk)
	}

	// 3. WebSocket upgrade headers on ProtectedAGY STILL fail 503 omp_cloak_required (WS no-op cannot bypass protected rejection!)
	wsOMPHdr := http.Header{
		"X-Cloak-Client": {"oh_my_pi"},
		"Upgrade":        {"websocket"},
		"Connection":     {"keep-alive, Upgrade"},
	}
	resOMPWS, attOMPWS := callResponsesRequestIntercept(t, "a11-omp-ws", "openai-response", "agy/claude-sonnet", respShape, wsOMPHdr)
	if attOMPWS != 0 || !resOMPWS.Terminate || resOMPWS.StatusCode != 503 || !strings.Contains(string(resOMPWS.ResponseBody), "omp_cloak_required") {
		t.Fatalf("WS upgrade bypassed ProtectedAGY rejection: attempts=%d resp=%+v", attOMPWS, resOMPWS)
	}

	// 4. Existing ProtectedAGY Chat shape still succeeds
	chatOMP := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":` + `"r` + `ead"}}` + `]}`)
	resOMPChat, attOMPChat := callResponsesRequestIntercept(t, "a11-omp-chat", "openai", "agy/claude-sonnet", chatOMP, ompHdr)
	if attOMPChat != 1 || resOMPChat.Terminate || !strings.Contains(string(resOMPChat.Body), `"view_file"`) {
		t.Fatalf("ProtectedAGY Chat shape failed: attempts=%d resp=%+v body=%s", attOMPChat, resOMPChat, resOMPChat.Body)
	}

	// 5. Explicit OMP non-AGY durable bypass on Responses shape mutates nothing
	resBypass, attBypass := callResponsesRequestIntercept(t, "a11-omp-bypass", "openai-response", "openai/gpt-5", respShape, ompHdr)
	if attBypass != 1 || len(resBypass.Body) != 0 {
		t.Fatalf("expected durable bypass on non-AGY OMP Responses request, got attempts=%d body=%s", attBypass, resBypass.Body)
	}
	bypassResp := []byte(`{"id":"r1","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"Antigravity"}]}]}`)
	outBypassResp := callResponsesResponseIntercept(t, "a11-omp-bypass", "openai-response", "openai/gpt-5", respShape, respShape, bypassResp, nil)
	if len(outBypassResp.Body) != 0 {
		t.Fatalf("durable bypass mutated response: %s", outBypassResp.Body)
	}

	// 6. Ordinary Codex Responses WebSocket upgrade pins zero mutation across request, response, and stream
	wsCodexHdr := http.Header{
		"X-Cloak-Client": {"codex"},
		"Upgrade":        {"WebSocket"},
		"Connection":     {"Upgrade"},
	}
	codexRespReq := []byte(`{"instructions":"You are Codex.","input":"hi","tools":[{"type":"function","name":"exec"}]}`)
	resWS, attWS := callResponsesRequestIntercept(t, "a11-codex-ws", "openai-response", "agy/gpt-5", codexRespReq, wsCodexHdr)
	if attWS != 1 || resWS.Terminate || len(resWS.Body) != 0 {
		t.Fatalf("expected WS Responses request body no-op, got attempts=%d body=%s", attWS, resWS.Body)
	}
	// Header X-Cloak-Client must still be consumed!
	if len(resWS.ClearHeaders) == 0 {
		t.Fatalf("expected X-Cloak-Client header to be consumed on WS request")
	}
	wsResp := []byte(`{"id":"r_ws","object":"response","output":[{"type":"function_call","name":"run_command","arguments":"{}"}]}`)
	outWSResp := callResponsesResponseIntercept(t, "a11-codex-ws", "openai-response", "agy/gpt-5", codexRespReq, codexRespReq, wsResp, nil)
	if len(outWSResp.Body) != 0 {
		t.Fatalf("pinned WS no-op mutated non-stream response: %s", outWSResp.Body)
	}
	wsChunk := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Antigravity\"}\n\n")
	outWSChunk := callResponsesStreamChunkIntercept(t, "a11-codex-ws", "openai-response", "agy/gpt-5", 0, wsChunk, codexRespReq, codexRespReq, nil)
	if len(outWSChunk.Body) != 0 || outWSChunk.DropChunk {
		t.Fatalf("pinned WS no-op mutated stream chunk: %+v", outWSChunk)
	}
}

// ── A12: `custom_mappings` compatibility and forward-only / built-in reverse ──

func TestIssue53_A12_CustomMappingsCompatibility(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	// Configure Cursor: Antigravity + ordered chain SourceAlpha -> SourceBeta -> GammaTool + duplicate GammaTool target
	cfgYAML := `
use_default_keywords: true
custom_mappings:
  - "Cursor: Antigravity"
  - "SourceAlpha: SourceBeta"
  - "SourceBeta: GammaTool"
  - "OtherSource: GammaTool"
`
	_, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(cfgYAML)))
	if code != 0 {
		t.Fatalf("reconfigure failed with code=%d", code)
	}

	reqBody := []byte(`{
		"instructions": "Welcome to Cursor and Codex with SourceAlpha and OtherSource.",
		"input": "hi",
		"tools": [{"type": "function", "name": "exec"}]
	}`)
	reqRes, att := callResponsesRequestIntercept(t, "a12-custom", "openai-response", "agy/gpt-5", reqBody, h)
	if att != 1 {
		t.Fatalf("request failed: %+v", reqRes)
	}
	var m map[string]any
	_ = json.Unmarshal(reqRes.Body, &m)
	inst := m["instructions"].(string)
	if inst != "Welcome to Antigravity and Antigravity with GammaTool and GammaTool." {
		t.Fatalf("custom forward mapping unexpected: %q", inst)
	}

	// Reverse on Codex response restores built-in `Antigravity -> Codex` (not `Cursor`) and leaves `GammaTool` literal!
	respBody := []byte(`{
		"id": "r_a12",
		"object": "response",
		"output": [
			{"id": "m1", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Using Antigravity and GammaTool."}]}
		]
	}`)
	respOut := callResponsesResponseIntercept(t, "a12-custom", "openai-response", "agy/gpt-5", reqRes.Body, reqRes.Body, respBody, h)
	var rm map[string]any
	_ = json.Unmarshal(respOut.Body, &rm)
	gotTxt := rm["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if gotTxt != "Using Codex and GammaTool." {
		t.Fatalf("expected Codex reverse to restore built-in Codex and keep GammaTool literal, got: %q", gotTxt)
	}

	// Malformed custom_mappings fails and leaves prior config live
	badYAML := `custom_mappings: [12345]`
	rawBad, _ := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(badYAML)))
	if !strings.Contains(string(rawBad), "invalid_config") {
		t.Fatalf("expected invalid_config error for malformed custom_mappings, got: %s", rawBad)
	}
	if len(activeFilterConfig().CustomMappings) != 4 {
		t.Fatalf("malformed custom_mappings clobbered live config: %+v", activeFilterConfig().CustomMappings)
	}
}

// ── A13: Unknown-format/shape traffic remains body-byte-identical with decision log ──

func TestIssue53_A13_UnknownFormatAndShapeHandling(t *testing.T) {
	isolateResponsesTestState(t)

	// Enable debug log to temp file to verify decision log
	tmpLog, err := os.CreateTemp(t.TempDir(), "cpa-debug-*.log")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	defer tmpLog.Close()

	debugLogMu.Lock()
	prevEnabled := debugLogEnabled
	prevFile := debugLogFile
	debugLogEnabled = true
	debugLogFile = tmpLog
	debugLogMu.Unlock()
	defer func() {
		debugLogMu.Lock()
		debugLogEnabled = prevEnabled
		debugLogFile = prevFile
		debugLogMu.Unlock()
	}()

	// 1. Ordinary unknown format (`gemini` / `interactions`) without alias-plan client -> byte-for-byte untouched
	rawUnknownReq := []byte(`{"contents":[{"parts":[{"text":"Codex exec"}]}]}`)
	resUnkFmt, att := callResponsesRequestIntercept(t, "a13-gemini-fmt", "gemini", "agy/gemini-2.5-pro", rawUnknownReq, nil)
	if att != 1 || len(resUnkFmt.Body) != 0 || resUnkFmt.Terminate {
		t.Fatalf("expected untouched pass-through for gemini source format, got att=%d res=%+v", att, resUnkFmt)
	}

	// 2. Root `{"error":...}` or `{"type":"error",...}` without Responses discriminator on request/response/stream -> unknown, byte-for-byte untouched
	errBody := []byte(`  {"type":"error","error":{"message":"Antigravity run_command"}}  `)
	resErrReq, attErr := callResponsesRequestIntercept(t, "a13-err-req", "openai-response", "agy/gpt-5", errBody, nil)
	if attErr != 1 || len(resErrReq.Body) != 0 {
		t.Fatalf("expected untouched pass-through for error request body, got: %+v", resErrReq)
	}
	resErrResp := callResponsesResponseIntercept(t, "a13-err-resp", "openai-response", "agy/gpt-5", errBody, errBody, errBody, nil)
	if len(resErrResp.Body) != 0 {
		t.Fatalf("expected untouched pass-through for error response body, got: %s", resErrResp.Body)
	}
	resErrStream := callResponsesStreamChunkIntercept(t, "a13-err-stream", "openai-response", "agy/gpt-5", 0, errBody, errBody, errBody, nil)
	if len(resErrStream.Body) != 0 || resErrStream.DropChunk {
		t.Fatalf("expected untouched pass-through for error stream chunk, got: %+v", resErrStream)
	}

	// 3. Known alias-plan client (`X-Cloak-Client: codex`) with unknown request shape STILL fails 503 tool_cloak_required and consumes header!
	hCodex := http.Header{"X-Cloak-Client": {"codex"}}
	resKnownUnk, attKnownUnk := callResponsesRequestIntercept(t, "a13-known-unk", "openai-response", "agy/gpt-5", errBody, hCodex)
	if attKnownUnk != 0 || !resKnownUnk.Terminate || resKnownUnk.StatusCode != 503 || len(resKnownUnk.ClearHeaders) == 0 {
		t.Fatalf("expected 503 tool_cloak_required + header consumption for known alias-plan client with unknown body, got att=%d res=%+v", attKnownUnk, resKnownUnk)
	}

	logBytes, _ := os.ReadFile(tmpLog.Name())
	logStr := string(logBytes)
	if !strings.Contains(logStr, "untouched body pass-through") {
		t.Fatalf("expected decision log for unknown body pass-through, got log:\n%s", logStr)
	}
}

// ── A14: Stream with no terminal/sentinel, unfinished tool call, and `request.complete` cleanup ──

func TestIssue53_A14_StreamWithoutTerminalAndCompleteCleanup(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	reqBody := []byte(`{"input":"hi","tools":[{"type":"function","name":"exec"}]}`)
	reqRes, _ := callResponsesRequestIntercept(t, "a14-unfinished", "openai-response", "agy/gpt-5", reqBody, h)

	// Stream has an unfinished tool call and held carry, and ends without any terminal event or [DONE]
	chunks := []string{
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_unfinished\",\"type\":\"function_call\",\"name\":\"run_command\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_unfinished\",\"output_index\":0,\"delta\":\"{\\\"cmd\\\":\\\"~/.gem\"}\n\n",
	}
	var wire []byte
	for i, ch := range chunks {
		res := callResponsesStreamChunkIntercept(t, "a14-unfinished", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes.Body, reqRes.Body, h)
		wire = appendStreamChunkWire(wire, []byte(ch), res)
	}
	// Nothing emitted after final ordinary frame
	if strings.Contains(string(wire), `~/.gem"`) {
		t.Fatalf("held carry was prematurely flushed without terminal: %s", wire)
	}
	// Session still holds carry before request.complete
	sess := globalStreamManager.getSession("req:a14-unfinished")
	if sess == nil || !hasPendingBrandCarry(sess) {
		t.Fatalf("expected pending carry before request.complete")
	}

	// `request.complete` drops carry and clears all state
	callResponsesRequestComplete(t, "a14-unfinished", "error")
	if globalAliasPlanManager.get("a14-unfinished") != nil || globalStreamManager.getSession("req:a14-unfinished") != nil {
		t.Fatalf("request.complete failed to clean up plan or stream session")
	}

	// Optional `[DONE]` sentinel on a pinned Responses session flushes uncovered attributable carry before `[DONE]`
	reqRes2, _ := callResponsesRequestIntercept(t, "a14-done-sentinel", "openai-response", "agy/gpt-5", reqBody, h)
	doneChunks := []string{
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_d\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_d\",\"output_index\":0,\"content_index\":0,\"delta\":\"Tail Antig\"}\n\n",
		"data: [DONE]\n\n",
	}
	var doneWire []byte
	for i, ch := range doneChunks {
		res := callResponsesStreamChunkIntercept(t, "a14-done-sentinel", "openai-response", "agy/gpt-5", i, []byte(ch), reqRes2.Body, reqRes2.Body, h)
		doneWire = appendStreamChunkWire(doneWire, []byte(ch), res)
	}
	doneStr := string(doneWire)
	flushPos := strings.Index(doneStr, `"delta":"Antig"`)
	donePos := strings.Index(doneStr, "data: [DONE]")
	if flushPos < 0 || donePos < 0 || flushPos > donePos {
		t.Fatalf("expected attributable carry flush before [DONE] sentinel:\n%s", doneStr)
	}
}

// ── A15: Distinct-owner collisions and owner drift across Chat and Gemini projections ──

func TestIssue53_A15_ProjectionCollisionsAndWinnerPreservation(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	// 1. Rewritten flat `exec -> run_command` colliding with native flat `run_command` -> 503
	colNative := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "function", "name": "run_command"}
		]
	}`)
	res1, att1 := callResponsesRequestIntercept(t, "a15-native-col", "openai-response", "agy/gpt-5", colNative, h)
	if att1 != 0 || !res1.Terminate || res1.StatusCode != 503 {
		t.Fatalf("expected 503 for rewritten vs native flat target collision, got att=%d res=%+v", att1, res1)
	}

	// 2. Flat `editor__apply_patch` vs namespace `editor` child `apply_patch`:
	// Both projections emit `editor__apply_patch` before aliasing. Renaming flat `editor__apply_patch` to fallback alias changes the owner of `editor__apply_patch` -> 503!
	colMixedWinner := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "namespace", "name": "editor", "tools": [{"type": "function", "name": "apply_patch"}]},
			{"type": "function", "name": "editor__apply_patch"}
		]
	}`)
	res2, att2 := callResponsesRequestIntercept(t, "a15-mixed-winner", "openai-response", "agy/gpt-5", colMixedWinner, h)
	if att2 != 0 || !res2.Terminate || res2.StatusCode != 503 {
		t.Fatalf("expected 503 when aliasing flat editor__apply_patch changes collision winner with namespace editor/apply_patch, got att=%d res=%+v", att2, res2)
	}

	// 3. Capped Chat collision: a namespace child whose 64-byte capped Chat name equals `run_command` collides with flat `exec -> run_command` -> 503!
	// Construct namespace name so `ns + "__" + child` has length > 64 and its last 64 bytes (after trimming leading `_-`) is `run_command`!
	// Wait: `capResponsesChatToolName` takes the last 64 bytes and trims leading `_-`.
	// If `child` is ` strings.Repeat("_", 53) + "run_command"` (64 bytes) inside namespace `ns`, raw qualified is `ns__` + 53 `_` + `run_command` (68 bytes).
	// Last 64 bytes is 53 `_` + `run_command`, and `TrimLeft("_-")` yields `"run_command"`!
	longChild := strings.Repeat("_", 53) + "run_command"
	colCappedChat := []byte(fmt.Sprintf(`{
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec"},
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": %q}]}
		]
	}`, longChild))
	res3, att3 := callResponsesRequestIntercept(t, "a15-capped-chat", "openai-response", "agy/gpt-5", colCappedChat, h)
	if att3 != 0 || !res3.Terminate || res3.StatusCode != 503 {
		t.Fatalf("expected 503 for capped Chat collision with run_command, got att=%d res=%+v", att3, res3)
	}

	// 4. Case where Chat and Gemini original winners differ (and NO mutation interacts with them):
	// In `tools`: namespace `ns` has child `ns__shared` (or `shared`), and in `additional_tools`...
	// Wait: in `tools`, put namespace `ns` with child `foo` (emits `ns__foo`, sourcePriority=0, direct=false, order=0),
	// and after it in `tools` put a second namespace `ns__foo` with child `ns__foo` (emits `ns__foo`, sourcePriority=0, direct=false, order=1) — both are pass-through!
	// Plus flat `exec` (which aliases to `run_command`, completely non-interacting).
	// Unchanged namespace-only collision must remain pass-through and succeed!
	passThroughNSCollision := []byte(`{
		"input": "hi",
		"tools": [
			{"type": "namespace", "name": "ns", "tools": [{"type": "function", "name": "foo"}]},
			{"type": "namespace", "name": "ns__foo", "tools": [{"type": "function", "name": "ns__foo"}]},
			{"type": "function", "name": "exec"}
		]
	}`)
	res4, att4 := callResponsesRequestIntercept(t, "a15-passthrough-ns-col", "openai-response", "agy/gpt-5", passThroughNSCollision, h)
	if att4 != 1 || res4.Terminate {
		t.Fatalf("expected unchanged namespace-only collision to pass through, got att=%d res=%+v", att4, res4)
	}

	// 5. Case where original Chat winner and original Gemini winner differ on a reference, and both are preserved:
	// Suppose `tools` has `namespace: "alpha", tools: [{"name": "shared"}]` (order 0) and `namespace: "beta", tools: [{"name": "shared"}]` (order 1), plus flat `exec`.
	// For bare history reference `name: "shared"`:
	// - Original Chat: `byLocal["shared"]` is `""` (ambiguous) -> unresolved (`origChatOK == false`).
	// - Original Gemini: `forwardMap["shared"]` is first-wins (`alpha__shared`, owner `(alpha, shared)`).
	// After aliasing `exec -> run_command`, `shared` is kept pass-through, so Chat is STILL unresolved and Gemini is STILL `(alpha, shared)`!
	diffWinnerRef := []byte(`{
		"input": [
			{"type": "function_call", "call_id": "c1", "name": "shared", "arguments": "{}"}
		],
		"tools": [
			{"type": "namespace", "name": "alpha", "tools": [{"type": "function", "name": "shared"}]},
			{"type": "namespace", "name": "beta", "tools": [{"type": "function", "name": "shared"}]},
			{"type": "function", "name": "exec"}
		]
	}`)
	res5, att5 := callResponsesRequestIntercept(t, "a15-diff-winner-ok", "openai-response", "agy/gpt-5", diffWinnerRef, h)
	if att5 != 1 || res5.Terminate {
		t.Fatalf("expected differing Chat/Gemini original outcomes to succeed when preserved, got att=%d res=%+v", att5, res5)
	}

	// Now suppose flat `exec` aliases to `run_command`, AND `alpha` has child `run_command` while `beta` has child `run_command`.
	// For bare reference `name: "run_command"`:
	// Before aliasing: Chat is unresolved (ambiguous local `run_command`), Gemini resolves to `(alpha, run_command)`.
	// After aliasing `exec -> run_command`: `run_command` resolves to flat `( "", "exec" )` in BOTH Chat and Gemini!
	// Must fail 503!
	diffWinnerDrift := []byte(`{
		"input": [
			{"type": "function_call", "call_id": "c1", "name": "run_command", "arguments": "{}"}
		],
		"tools": [
			{"type": "namespace", "name": "alpha", "tools": [{"type": "function", "name": "run_command"}]},
			{"type": "namespace", "name": "beta", "tools": [{"type": "function", "name": "run_command"}]},
			{"type": "function", "name": "exec"}
		]
	}`)
	res6, att6 := callResponsesRequestIntercept(t, "a15-diff-winner-drift", "openai-response", "agy/gpt-5", diffWinnerDrift, h)
	if att6 != 0 || !res6.Terminate || res6.StatusCode != 503 {
		t.Fatalf("expected 503 when aliasing resolves formerly ambiguous reference onto flat tool, got att=%d res=%+v", att6, res6)
	}
}

// ── A17 & A18: Wire-family classification, object:"response", and mixed-discriminator rejection ──

func TestIssue53_A17_A18_WireFamilyClassificationAndMixedDiscriminators(t *testing.T) {
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}

	// A17:
	// 1. `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"Antigravity"}]}]}` is classified/reversed as Responses
	reqBody := []byte(`{"input":"hi","tools":[{"type":"function","name":"exec"}]}`)
	reqRes, _ := callResponsesRequestIntercept(t, "a17-req", "openai-response", "agy/gpt-5", reqBody, h)

	respWithOutput := []byte(`{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"Antigravity"}]}]}`)
	out1 := callResponsesResponseIntercept(t, "a17-req", "openai-response", "agy/gpt-5", reqRes.Body, reqRes.Body, respWithOutput, h)
	if !strings.Contains(string(out1.Body), `"text":"Codex"`) {
		t.Fatalf("expected A17 response to reverse Antigravity->Codex, got: %s", out1.Body)
	}

	// 2. `{"object":"response"}` is a valid no-carrier Responses payload (no-op)
	respNoCarrier := []byte(`{"object":"response"}`)
	out2 := callResponsesResponseIntercept(t, "a17-req", "openai-response", "agy/gpt-5", reqRes.Body, reqRes.Body, respNoCarrier, h)
	if len(out2.Body) != 0 {
		t.Fatalf("expected no-carrier response object to return empty Body, got: %s", out2.Body)
	}

	// 3. `{"object":"response"}` on a REQUEST is NOT a request discriminator (classified as unknown!)
	reqWithObjectResponse := []byte(`{"object":"response"}`)
	resObjReq, attObjReq := callResponsesRequestIntercept(t, "a17-obj-req", "openai-response", "agy/gpt-5", reqWithObjectResponse, nil)
	if attObjReq != 1 || len(resObjReq.Body) != 0 {
		t.Fatalf("request with only object:response should be unknown pass-through, got: %+v", resObjReq)
	}

	// A18: Both-discriminator fixtures within one direction classify as `unknown`:
	// 1. Request with both `messages` and `input`
	mixedReq := []byte(`{"messages":[{"role":"system","content":"Codex"}],"input":"hi"}`)
	// Without forced alias-plan client: untouched pass-through
	resMixReqPass, attMixPass := callResponsesRequestIntercept(t, "a18-mix-req-pass", "openai", "agy/gpt-5", mixedReq, nil)
	if attMixPass != 1 || len(resMixReqPass.Body) != 0 {
		t.Fatalf("mixed messages+input request without forced client should pass through untouched, got: %+v", resMixReqPass)
	}
	// With forced alias-plan client (`X-Cloak-Client: codex`): fails 503 tool_cloak_required (§2 rule 4)
	resMixReq503, attMix503 := callResponsesRequestIntercept(t, "a18-mix-req-503", "openai", "agy/gpt-5", mixedReq, h)
	if attMix503 != 0 || !resMixReq503.Terminate || resMixReq503.StatusCode != 503 {
		t.Fatalf("mixed messages+input request with X-Cloak-Client:codex must fail 503, got att=%d res=%+v", attMix503, resMixReq503)
	}

	// 2. Non-stream response with both `choices` and `output` -> unknown, untouched
	mixedResp := []byte(`{"choices":[{"message":{"content":"Antigravity"}}],"output":[{"type":"message","content":[{"type":"output_text","text":"Antigravity"}]}]}`)
	outMixResp := callResponsesResponseIntercept(t, "a17-req", "openai-response", "agy/gpt-5", reqRes.Body, reqRes.Body, mixedResp, h)
	if len(outMixResp.Body) != 0 {
		t.Fatalf("mixed choices+output response should be unknown/untouched, got: %s", outMixResp.Body)
	}

	// 3. Stream event with both `choices` and `type:"response.completed"` -> unknown, untouched data bytes
	mixedStream := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"choices\":[],\"response\":{\"object\":\"response\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Antigravity\"}]}]}}\n\n")
	outMixStream := callResponsesStreamChunkIntercept(t, "a17-req", "openai-response", "agy/gpt-5", 0, mixedStream, reqRes.Body, reqRes.Body, h)
	if len(outMixStream.Body) != 0 || outMixStream.DropChunk {
		t.Fatalf("mixed choices+type:response.completed stream event should be unknown/untouched, got: %+v", outMixStream)
	}

	// 4. Empty arrays/strings count as valid single-direction discriminators:
	// - `{"input":""}` and `{"instructions":""}` and `{"input":[]}` are valid `openai-responses` requests
	for idx, emptyValid := range []string{`{"input":""}`, `{"instructions":"Codex"}`, `{"input":[]}`} {
		rID := fmt.Sprintf("a18-empty-valid-%d", idx)
		rOut, rAtt := callResponsesRequestIntercept(t, rID, "openai-response", "agy/gpt-5", []byte(emptyValid), h)
		if rAtt != 1 || rOut.Terminate {
			t.Fatalf("empty valid discriminator %s failed: att=%d res=%+v", emptyValid, rAtt, rOut)
		}
	}

	// 5. Anthropic request with `messages` and `input` or `instructions` remains `anthropic` (format-directed, never sent into OpenAI shape discriminator)
	anthReq := []byte(`{"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"name":"Read","input_schema":{"type":"object"}}]}`)
	hCC := http.Header{"X-Cloak-Client": {"claude_code"}}
	resAnth, attAnth := callResponsesRequestIntercept(t, "a18-anth", "anthropic", "agy/claude-sonnet", anthReq, hCC)
	if attAnth != 1 || !strings.Contains(string(resAnth.Body), `"run_command"`) {
		t.Fatalf("Anthropic format-directed path failed: att=%d body=%s", attAnth, resAnth.Body)
	}

	// 6. Tools-only OpenAI request (neither `messages` nor `input`/`instructions`) classifies as `unknown`:
	// - Without forced alias-plan client: pass-through untouched
	toolsOnlyReq := []byte(`{"tools":[{"type":"function","function":{"name":"exec"}},{"type":"function","function":{"name":"search_web"}}]}`)
	resToolsOnlyPass, attToolsOnlyPass := callResponsesRequestIntercept(t, "a18-tools-only-pass", "openai", "agy/gpt-5", toolsOnlyReq, nil)
	if attToolsOnlyPass != 1 || len(resToolsOnlyPass.Body) != 0 || resToolsOnlyPass.Terminate {
		t.Fatalf("tools-only OpenAI request without forced client should pass through untouched as unknown, got att=%d res=%+v", attToolsOnlyPass, resToolsOnlyPass)
	}
	// - With forced alias-plan client (`X-Cloak-Client: codex`): fails 503 tool_cloak_required (§2 rule 4)
	resToolsOnly503, attToolsOnly503 := callResponsesRequestIntercept(t, "a18-tools-only-503", "openai", "agy/gpt-5", toolsOnlyReq, h)
	if attToolsOnly503 != 0 || !resToolsOnly503.Terminate || resToolsOnly503.StatusCode != 503 {
		t.Fatalf("tools-only OpenAI request with forced alias-plan client must fail 503, got att=%d res=%+v", attToolsOnly503, resToolsOnly503)
	}
}

func TestIssue53ReviewValidatedDoneIdentity(t *testing.T) {
	cases := []struct {
		name, body, itemType, itemID string
	}{
		{"output_text_missing_output", `{"type":"response.output_text.done","content_index":0,"text":"Antigravity"}`, "", ""},
		{"output_text_missing_content", `{"type":"response.output_text.done","output_index":3,"text":"Antigravity"}`, "", ""},
		{"output_text_reasoning_item", `{"type":"response.output_text.done","output_index":3,"content_index":0,"text":"Antigravity"}`, "reasoning", ""},
		{"part_done_unknown_item", `{"type":"response.content_part.done","output_index":3,"content_index":0,"part":{"type":"output_text","text":"Antigravity"}}`, "", ""},
		{"part_done_missing_output", `{"type":"response.content_part.done","content_index":0,"part":{"type":"output_text","text":"Antigravity"}}`, "", ""},
		{"function_done_missing_output", `{"type":"response.function_call_arguments.done","arguments":"*** Update File: ~/.` + `gem` + `ini/AGENTS.md"}`, "", ""},
		{"custom_done_missing_output", `{"type":"response.custom_tool_call_input.done","input":"*** Update File: ~/.` + `gem` + `ini/AGENTS.md"}`, "", ""},
		{"output_text_mismatch_id", `{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"other","text":"Antigravity"}`, "message", "expected"},
		{"part_done_mismatch_id", `{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"other","part":{"type":"output_text","text":"Antigravity"}}`, "message", "expected"},
		{"function_done_mismatch_id", `{"type":"response.function_call_arguments.done","output_index":0,"item_id":"other","arguments":"*** Update File: ~/.` + `gem` + `ini/AGENTS.md"}`, "function_call", "expected"},
		{"custom_done_mismatch_id", `{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"other","input":"*** Update File: ~/.` + `gem` + `ini/AGENTS.md"}`, "custom_tool_call", "expected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data map[string]any
			if err := safeUnmarshal([]byte(tc.body), &data); err != nil {
				t.Fatal(err)
			}
			orig, _ := safeMarshal(data)
			sess := &streamSession{}
			if tc.itemType != "" {
				out := 0
				if tc.name == "output_text_reasoning_item" {
					out = 3
				}
				sess.responsesItems = map[int]*responsesOutputItemMeta{
					out: {itemType: tc.itemType, itemID: tc.itemID},
				}
			}
			manager := newStreamSessionManager()
			_, changed, _ := manager.processResponsesStreamEvent(data, sess, &requestAliasPlan{client: "codex"})
			current, _ := safeMarshal(data)
			if changed || !bytes.Equal(orig, current) {
				t.Errorf("invalid done event was rewritten; before=%s after=%s", orig, current)
			}
			if tc.itemID != "" && sess.responsesItems[0].itemID != tc.itemID {
				t.Errorf("contradictory item ID overwrote owner: want %q got %q", tc.itemID, sess.responsesItems[0].itemID)
			}
		})
	}

	// Conflicting selector populated names when tools[]/history have no prior renames must still fail 503 when a populated name would be mutated
	isolateResponsesTestState(t)
	h := http.Header{"X-Cloak-Client": {"codex"}}
	conflictingSelNoDecl := []byte(`{"input":"hi","tool_choice":{"type":"function","name":"exec","function":{"name":"apply_patch"}}}`)
	resSel, attSel := callResponsesRequestIntercept(t, "rev-sel-conflict-nodecl", "openai-response", "agy/gpt-5", conflictingSelNoDecl, h)
	if attSel != 0 || !resSel.Terminate || resSel.StatusCode != 503 {
		t.Fatalf("expected 503 for conflicting selector names without prior decl renames, got att=%d res=%+v", attSel, resSel)
	}

	// Empty-named namespace container in echoed tools[] reverses its declared children
	reqEmptyNS := []byte(`{"input":"hi","tools":[{"type":"namespace","name":"","tools":[{"type":"function","name":"exec"}]}]}`)
	rEmptyNS, attEmptyNS := callResponsesRequestIntercept(t, "rev-empty-ns-echo", "openai-response", "agy/gpt-5", reqEmptyNS, h)
	if attEmptyNS != 1 || rEmptyNS.Terminate {
		t.Fatalf("empty-named namespace request failed: att=%d res=%+v", attEmptyNS, rEmptyNS)
	}
	respEmptyNSEcho := []byte(`{"id":"r_ens","object":"response","tools":[{"type":"namespace","name":"","tools":[{"type":"function","name":"run_command","description":"Antigravity run_command"}]}],"output":[]}`)
	outEmptyNS := callResponsesResponseIntercept(t, "rev-empty-ns-echo", "openai-response", "agy/gpt-5", rEmptyNS.Body, rEmptyNS.Body, respEmptyNSEcho, h)
	if !strings.Contains(string(outEmptyNS.Body), `"name":"exec"`) || !strings.Contains(string(outEmptyNS.Body), `"description":"Codex run_command"`) {
		t.Fatalf("empty-named namespace echoed tools were not reversed: %s", outEmptyNS.Body)
	}
}
