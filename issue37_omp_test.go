package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestOMP_CanonicalNinePlusSharedAliases(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-canonical-shared"
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"write"}},
			{"type":"function","function":{"name":"edit"}},
			{"type":"function","function":{"name":"bash"}},
			{"type":"function","function":{"name":"grep"}},
			{"type":"function","function":{"name":"glob"}},
			{"type":"function","function":{"name":"task"}},
			{"type":"function","function":{"name":"ask"}},
			{"type":"function","function":{"name":"web_search"}},
			{"type":"function","function":{"name":"todo"}},
			{"type":"function","function":{"name":"hub"}},
			{"type":"function","function":{"name":"eval"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	expected := []string{
		`"name":"view_file"`, `"name":"write_to_file"`, `"name":"replace_file_content"`,
		`"name":"run_command"`, `"name":"grep_search"`, `"name":"find_by_name"`,
		`"name":"invoke_subagent"`, `"name":"ask_question"`, `"name":"search_web"`,
		`"name":"wp_todo"`, `"name":"wp_hub"`, `"name":"wp_eval"`,
	}
	for _, want := range expected {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s: %s", want, admitted.Body)
		}
	}

	notExpected := []string{`"name":"todo"`, `"name":"hub"`, `"name":"eval"`}
	for _, notWant := range notExpected {
		if bytes.Contains(admitted.Body, []byte(notWant)) {
			t.Fatalf("cloaked request should not contain %s: %s", notWant, admitted.Body)
		}
	}
}

func TestOMP_UnknownDeclarationsGetFallbackAliases(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-unknown-fallback"
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"_foo"}},
			{"type":"function","function":{"name":"__read"}},
			{"type":"function","function":{"name":"custom_unknown_tool"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	fbFoo := fallbackAliasForSource("_foo")
	fbRead := fallbackAliasForSource("__read")
	fbCustom := fallbackAliasForSource("custom_unknown_tool")

	expected := []string{
		`"name":"view_file"`,
		`"name":"` + fbFoo + `"`,
		`"name":"` + fbRead + `"`,
		`"name":"` + fbCustom + `"`,
	}
	for _, want := range expected {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s: %s", want, admitted.Body)
		}
	}
	// Verify raw names did NOT survive cloaking
	for _, raw := range []string{`"name":"_foo"`, `"name":"__read"`, `"name":"custom_unknown_tool"`} {
		if bytes.Contains(admitted.Body, []byte(raw)) {
			t.Fatalf("raw unknown tool name %s survived cloaking in %s", raw, admitted.Body)
		}
	}

	// Verify response uncloak restores the exact source spellings
	respBody := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [
					{"id": "c1", "type": "function", "function": {"name": "view_file", "arguments": "{}"}},
					{"id": "c2", "type": "function", "function": {"name": "` + fbFoo + `", "arguments": "{}"}},
					{"id": "c3", "type": "function", "function": {"name": "` + fbRead + `", "arguments": "{}"}},
					{"id": "c4", "type": "function", "function": {"name": "` + fbCustom + `", "arguments": "{}"}}
				]
			}
		}]
	}`)
	var uncloaked pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID:    requestID,
		SourceFormat: "openai",
		Model:        "agy/measurement",
		Body:         respBody,
	}, &uncloaked)
	for _, wantOrig := range []string{`"name":"read"`, `"name":"_foo"`, `"name":"__read"`, `"name":"custom_unknown_tool"`} {
		if !bytes.Contains(uncloaked.Body, []byte(wantOrig)) {
			t.Fatalf("response uncloak missing original spelling %s in %s", wantOrig, uncloaked.Body)
		}
	}
}

func TestOMP_VibeModeToolsCloaked(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-vibe-tools"
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"vibe_spawn"}},
			{"type":"function","function":{"name":"vibe_send"}},
			{"type":"function","function":{"name":"vibe_wait"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	expected := []string{
		`"name":"view_file"`,
		`"name":"wp_vibe_spawn"`,
		`"name":"wp_vibe_send"`,
		`"name":"wp_vibe_wait"`,
	}
	for _, want := range expected {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s: %s", want, admitted.Body)
		}
	}

	notExpected := []string{`"name":"vibe_spawn"`, `"name":"vibe_send"`, `"name":"vibe_wait"`}
	for _, notWant := range notExpected {
		if bytes.Contains(admitted.Body, []byte(notWant)) {
			t.Fatalf("cloaked request should not contain %s: %s", notWant, admitted.Body)
		}
	}
}

func TestOMP_CanonicalCollisionStillRejects(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-collision"
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"_read"}}
		]
	}`)

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &result)
	if !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("collision must reject with 503: terminate=%t status=%d body=%s", result.Terminate, result.StatusCode, result.ResponseBody)
	}
}

func TestOMP_ExtendedTableReversal(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-reversal"

	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"todo"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	var nonstream pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/measurement",
		Body: []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}},{"function":{"name":"wp_todo","arguments":"{}"}}]}}]}`),
	}, &nonstream)

	if !bytes.Contains(nonstream.Body, []byte(`"name":"read"`)) ||
		!bytes.Contains(nonstream.Body, []byte(`"name":"todo"`)) {
		t.Fatalf("non-stream response did not restore exact source spellings: %s", nonstream.Body)
	}

	if bytes.Contains(nonstream.Body, []byte(`"name":"view_file"`)) ||
		bytes.Contains(nonstream.Body, []byte(`"name":"wp_todo"`)) {
		t.Fatalf("non-stream response leaked cloaked spellings: %s", nonstream.Body)
	}
}

func TestOMP_StreamReversalWithExtendedTable(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-stream-reversal"

	admitOMPMeasurement(t, requestID, "openai", []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"todo"}}
		]
	}`))

	// Fragmented stream chunk test
	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"wp_todo\",\"arguments\":\"{}\"}}]}}]}\n\n")
	cut := bytes.Index(frame, []byte("wp_todo")) + len("wp_")
	parts := [][]byte{frame[:cut], frame[cut:]}

	var wire []byte
	for i, part := range parts {
		var result pluginapi.StreamChunkInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
			RequestID: requestID, SourceFormat: "openai", Model: "agy/measurement", ChunkIndex: i, Body: part,
		}, &result)
		wire = appendOMPMeasurementWire(wire, part, result)
	}

	if !bytes.Contains(wire, []byte(`"name":"todo"`)) {
		t.Fatalf("fragmented stream did not restore original tool name: %s", wire)
	}
	if bytes.Contains(wire, []byte("wp_todo")) {
		t.Fatalf("fragmented stream leaked cloaked target: %s", wire)
	}
}

func TestOMP_EscapedSharedAliasesCloakAndReverseExact(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-escaped-shared-exact"

	body := []byte(`{
		"tools": [
			{"type": "function", "function": {"name": "read"}},
			{"type": "function", "function": {"name": "_todo"}},
			{"type": "function", "function": {"name": "_hub"}},
			{"type": "function", "function": {"name": "_eval"}}
		],
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "_todo", "arguments": "{}"}},
					{"id": "call_2", "type": "function", "function": {"name": "_hub", "arguments": "{}"}}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"function": {"name": "_eval"}
		}
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	// Verify cloaked targets: _todo -> wp_todo, _hub -> wp_hub, _eval -> wp_eval
	for _, want := range []string{`"name":"view_file"`, `"name":"wp_todo"`, `"name":"wp_hub"`, `"name":"wp_eval"`} {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s: %s", want, admitted.Body)
		}
	}
	// Verify raw names did not leak
	for _, raw := range []string{`"name":"_todo"`, `"name":"_hub"`, `"name":"_eval"`} {
		if bytes.Contains(admitted.Body, []byte(raw)) {
			t.Fatalf("raw escaped name %s leaked in request: %s", raw, admitted.Body)
		}
	}

	// Response reversal: upstream sends wp_todo, wp_hub, wp_eval
	respBody := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [
					{"id": "c1", "type": "function", "function": {"name": "wp_todo", "arguments": "{}"}},
					{"id": "c2", "type": "function", "function": {"name": "wp_hub", "arguments": "{}"}},
					{"id": "c3", "type": "function", "function": {"name": "wp_eval", "arguments": "{}"}}
				]
			}
		}]
	}`)
	var uncloaked pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID:    requestID,
		SourceFormat: "openai",
		Model:        "agy/measurement",
		Body:         respBody,
	}, &uncloaked)

	// Verify exact restoration of escaped source spelling (_todo, _hub, _eval)
	for _, wantOrig := range []string{`"name":"_todo"`, `"name":"_hub"`, `"name":"_eval"`} {
		if !bytes.Contains(uncloaked.Body, []byte(wantOrig)) {
			t.Fatalf("response uncloak missing exact escaped spelling %s in %s", wantOrig, uncloaked.Body)
		}
	}
	// Verify bare names were NOT erroneously substituted
	for _, bare := range []string{`"name":"todo"`, `"name":"hub"`, `"name":"eval"`} {
		if bytes.Contains(uncloaked.Body, []byte(bare)) {
			t.Fatalf("response uncloak leaked bare name instead of exact escaped %s in %s", bare, uncloaked.Body)
		}
	}
}

func TestOMP_EscapedSharedCollisionRejects(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-escaped-shared-collision"

	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"todo"}},
			{"type":"function","function":{"name":"_todo"}}
		]
	}`)

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &result)
	if !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("collision of todo and _todo must reject with 503: terminate=%t status=%d body=%s", result.Terminate, result.StatusCode, result.ResponseBody)
	}
}

func TestOMP_UndeclaredHistoryAndToolChoiceCloaked(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-undeclared-hist-tc"

	// tools[] only declares read.
	// messages[] has tool_calls with bash and custom_history_tool, and tool-role with name todo.
	// tool_choice specifies ask.
	body := []byte(`{
		"tools":[{"type":"function","function":{"name":"read"}}],
		"messages":[
			{"role":"assistant","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"bash","arguments":"{}"}},
				{"id":"c2","type":"function","function":{"name":"custom_history_tool","arguments":"{}"}}
			]},
			{"role":"tool","name":"todo","content":"done"}
		],
		"tool_choice":{"type":"function","function":{"name":"ask"}}
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	fbCustom := fallbackAliasForSource("custom_history_tool")
	expected := []string{
		`"name":"view_file"`,
		`"name":"run_command"`,
		`"name":"` + fbCustom + `"`,
		`"name":"wp_todo"`,
		`"name":"ask_question"`,
	}
	for _, want := range expected {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s in %s", want, admitted.Body)
		}
	}

	// Raw source names must not leak
	for _, raw := range []string{`"bash"`, `"custom_history_tool"`, `"todo"`, `"ask"`} {
		if bytes.Contains(admitted.Body, []byte(raw)) {
			t.Fatalf("raw undeclared identity %s leaked in %s", raw, admitted.Body)
		}
	}

	// Verify response uncloak restores exact spelling for fallback
	respBody := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [
					{"id": "c1", "type": "function", "function": {"name": "` + fbCustom + `", "arguments": "{}"}}
				]
			}
		}]
	}`)
	var uncloaked pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID:    requestID,
		SourceFormat: "openai",
		Model:        "agy/measurement",
		Body:         respBody,
	}, &uncloaked)
	if !bytes.Contains(uncloaked.Body, []byte(`"name":"custom_history_tool"`)) {
		t.Fatalf("response uncloak failed to restore custom_history_tool: %s", uncloaked.Body)
	}
}

func TestOMP_UnknownFunctionsAndDefaultApiPrefixCollisionFailsClosed(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-unknown-prefix-collision"

	// Regression: unknown functions:foo + default_api:foo resolve to same final base and must fail closed.
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"functions:foo"}},
			{"type":"function","function":{"name":"default_api:foo"}}
		]
	}`)

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &result)
	if !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("collision of functions:foo and default_api:foo on final base must reject with 503: terminate=%t status=%d body=%s",
			result.Terminate, result.StatusCode, result.ResponseBody)
	}
	if !bytes.Contains(result.ResponseBody, []byte("omp_cloak_required")) {
		t.Fatalf("expected omp_cloak_required error, got: %s", result.ResponseBody)
	}
}

func TestOMP_ConfigExtraNoncanonicalMappingsHonored(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-config-extra-noncanonical"

	// Configure oh_my_pi with 10 mappings (canonical 9 + extra noncanonical custom_extra -> wp_custom)
	cfg := defaultFilterConfig()
	cfg.ToolMappings["oh_my_pi"]["custom_extra"] = "wp_custom"
	applyFilterConfig(cfg)

	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"custom_extra"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	if !bytes.Contains(admitted.Body, []byte(`"name":"wp_custom"`)) {
		t.Fatalf("custom_extra was not cloaked to configured target wp_custom: %s", admitted.Body)
	}
	if bytes.Contains(admitted.Body, []byte(`"name":"custom_extra"`)) {
		t.Fatalf("raw custom_extra leaked: %s", admitted.Body)
	}

	// Verify reverse uncloaking
	respBody := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [
					{"id": "c1", "type": "function", "function": {"name": "wp_custom", "arguments": "{}"}}
				]
			}
		}]
	}`)
	var uncloaked pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID:    requestID,
		SourceFormat: "openai",
		Model:        "agy/measurement",
		Body:         respBody,
	}, &uncloaked)
	if !bytes.Contains(uncloaked.Body, []byte(`"name":"custom_extra"`)) {
		t.Fatalf("response uncloak failed to restore custom_extra: %s", uncloaked.Body)
	}
}

func TestOMP_ConfigSharedKeyOverrideHonored(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-config-shared-override"

	// Configure oh_my_pi overriding shared alias "todo" to "my_custom_todo" instead of default "wp_todo"
	cfg := defaultFilterConfig()
	cfg.ToolMappings["oh_my_pi"]["todo"] = "my_custom_todo"
	applyFilterConfig(cfg)

	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"todo"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	if !bytes.Contains(admitted.Body, []byte(`"name":"my_custom_todo"`)) {
		t.Fatalf("todo was not cloaked to configured override target my_custom_todo: %s", admitted.Body)
	}
	if bytes.Contains(admitted.Body, []byte(`"name":"wp_todo"`)) {
		t.Fatalf("default shared alias wp_todo shadowed operator config: %s", admitted.Body)
	}
}

func TestOMP_MutatedCanonicalConfigRejects(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-mutated-canonical"

	// Mutate canonical read from view_file to custom_read
	cfg := defaultFilterConfig()
	cfg.ToolMappings["oh_my_pi"]["read"] = "custom_read"
	applyFilterConfig(cfg)

	body := []byte(`{
		"messages":[],
		"tools":[{"type":"function","function":{"name":"read"}}]
	}`)

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &result)
	if !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("mutated canonical config must reject with 503: terminate=%t status=%d body=%s",
			result.Terminate, result.StatusCode, result.ResponseBody)
	}
}

func TestOMP_RemovedCanonicalConfigRejects(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-removed-canonical"

	// Remove canonical read so count is 8 (< 9)
	cfg := defaultFilterConfig()
	delete(cfg.ToolMappings["oh_my_pi"], "read")
	applyFilterConfig(cfg)

	body := []byte(`{
		"messages":[],
		"tools":[{"type":"function","function":{"name":"write"}}]
	}`)

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &result)
	if !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("removed canonical config must reject with 503: terminate=%t status=%d body=%s",
			result.Terminate, result.StatusCode, result.ResponseBody)
	}
}

func TestOMP_VirtualDeviceXDProtocolPreserved(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-xd-protocol-preserved"

	// Request with virtual device and MCP tool calls dispatched through read/write via xd:// URIs.
	body := []byte(`{
		"tools": [
			{"type": "function", "function": {"name": "read"}},
			{"type": "function", "function": {"name": "write"}}
		],
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "c1",
						"type": "function",
						"function": {
							"name": "read",
							"arguments": "{\"path\":\"xd://mcp__github_list_issues?repo=test\"}"
						}
					},
					{
						"id": "c2",
						"type": "function",
						"function": {
							"name": "write",
							"arguments": "{\"path\":\"xd://ast_edit\",\"content\":\"function foo() {}\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"name": "read",
				"content": "output from xd://mcp__github_list_issues"
			}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	gotReq := string(admitted.Body)
	// Tool names read -> view_file, write -> write_to_file
	if !strings.Contains(gotReq, `"name":"view_file"`) || !strings.Contains(gotReq, `"name":"write_to_file"`) {
		t.Fatalf("tools were not cloaked: %s", gotReq)
	}
	// xd:// URIs and namespaces MUST be byte-for-byte preserved and not converted to call_mcp_tool
	xdURIs := []string{
		`xd://mcp__github_list_issues?repo=test`,
		`xd://ast_edit`,
		`output from xd://mcp__github_list_issues`,
	}
	for _, uri := range xdURIs {
		if !strings.Contains(gotReq, uri) {
			t.Errorf("xd:// URI was not preserved byte-for-byte in request: %q not in %s", uri, gotReq)
		}
	}
	if strings.Contains(gotReq, "call_mcp_tool") {
		t.Errorf("xd:// call was incorrectly rewritten to call_mcp_tool: %s", gotReq)
	}

	// Verify streamed response uncloaks view_file -> read while preserving xd:// arguments byte-for-byte
	chunkBody := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"view_file\",\"arguments\":\"{\\\"path\\\":\\\"xd://mcp__github_list_issues\\\"}\"}}]}}]}\n\n"
	var chunkResp pluginapi.StreamChunkInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
		RequestID:    requestID,
		SourceFormat: "openai",
		Model:        "agy/measurement",
		ChunkIndex:   0,
		Body:         []byte(chunkBody),
	}, &chunkResp)

	gotChunk := string(chunkResp.Body)
	if !strings.Contains(gotChunk, `"name":"read"`) {
		t.Errorf("expected view_file to uncloak to read in stream: %s", gotChunk)
	}
	if !strings.Contains(gotChunk, `xd://mcp__github_list_issues`) {
		t.Errorf("xd:// URI in tool arguments was corrupted in stream: %s", gotChunk)
	}
}

func TestOMP_AutoresearchAndMemorySharedAliases(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-issue37-autoresearch-memory"
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"bash"}},
			{"type":"function","function":{"name":"init_experiment"}},
			{"type":"function","function":{"name":"run_experiment"}},
			{"type":"function","function":{"name":"log_experiment"}},
			{"type":"function","function":{"name":"update_notes"}},
			{"type":"function","function":{"name":"learn"}},
			{"type":"function","function":{"name":"manage_skill"}},
			{"type":"function","function":{"name":"find"}}
		]
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}

	expected := []string{
		`"name":"view_file"`,
		`"name":"run_command"`,
		`"name":"wp_init_experiment"`,
		`"name":"wp_run_experiment"`,
		`"name":"wp_log_experiment"`,
		`"name":"wp_update_notes"`,
		`"name":"wp_learn"`,
		`"name":"wp_manage_skill"`,
		`"name":"wp_find"`,
	}
	for _, want := range expected {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s: %s", want, admitted.Body)
		}
	}

	// Verify raw source names do NOT leak in the cloaked request
	rawSources := []string{
		`"name":"init_experiment"`,
		`"name":"run_experiment"`,
		`"name":"log_experiment"`,
		`"name":"update_notes"`,
		`"name":"learn"`,
		`"name":"manage_skill"`,
		`"name":"find"`,
	}
	for _, raw := range rawSources {
		if bytes.Contains(admitted.Body, []byte(raw)) {
			t.Fatalf("raw tool name %s survived cloaking in %s", raw, admitted.Body)
		}
	}

	// Verify response uncloak restores exact original source names
	respBody := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [
					{"id": "c1", "type": "function", "function": {"name": "wp_init_experiment", "arguments": "{}"}},
					{"id": "c2", "type": "function", "function": {"name": "wp_learn", "arguments": "{}"}},
					{"id": "c3", "type": "function", "function": {"name": "wp_manage_skill", "arguments": "{}"}},
					{"id": "c4", "type": "function", "function": {"name": "wp_find", "arguments": "{}"}}
				]
			}
		}]
	}`)
	var uncloaked pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID:    requestID,
		SourceFormat: "openai",
		Model:        "agy/measurement",
		Body:         respBody,
	}, &uncloaked)

	for _, wantOrig := range []string{`"name":"init_experiment"`, `"name":"learn"`, `"name":"manage_skill"`, `"name":"find"`} {
		if !bytes.Contains(uncloaked.Body, []byte(wantOrig)) {
			t.Fatalf("response uncloak missing original spelling %s in %s", wantOrig, uncloaked.Body)
		}
	}
	for _, alias := range []string{`"name":"wp_init_experiment"`, `"name":"wp_learn"`, `"name":"wp_manage_skill"`, `"name":"wp_find"`} {
		if bytes.Contains(uncloaked.Body, []byte(alias)) {
			t.Fatalf("alias name %s leaked in response: %s", alias, uncloaked.Body)
		}
	}

	var completed struct{}
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: requestID}, &completed)
}
