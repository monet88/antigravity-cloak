package cloak

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCC_ProvenToolsCloakViaAliasPlan(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_proven"
	reqBody := []byte(`{
		"messages": [{"role":"user","content":"do something"}],
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""},
			{"name":"Grep","description":""},
			{"name":"Glob","description":""},
			{"name":"Agent","description":""},
			{"name":"AskUserQuestion","description":""},
			{"name":"WebSearch","description":""},
			{"name":"WebFetch","description":""},
			{"name":"ListAgents","description":""},
			{"name":"SendMessage","description":""},
			{"name":"TaskStop","description":""},
			{"name":"ListMcpResourcesTool","description":""},
			{"name":"ReadMcpResourceTool","description":""}
		]
	}`)

	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/claude-test", reqBody)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	// Tier-1: proven AGY role targets (from static table).
	tier1Targets := []string{
		"run_command", "read_file", "write_file", "edit_file",
		"grep_search", "find_by_name", "invoke_subagent", "ask_question",
		"search_web",
	}
	for _, target := range tier1Targets {
		if !strings.Contains(string(bodyDecoded), `"`+target+`"`) {
			t.Errorf("missing Tier-1 target tool %q in cloaked request", target)
		}
	}
	if strings.Contains(string(bodyDecoded), `"read_url_content"`) {
		t.Errorf("removed AGY tool read_url_content must not be emitted in cloaked request")
	}
	// Tier-2: wp_ aliases from shared aliases (parent #32 vocabulary).
	tier2Targets := []string{
		"wp_fetch_url",
		"wp_list_workers", "wp_send_message", "wp_cancel_task",
		"wp_list_resources", "wp_read_resource",
	}
	for _, target := range tier2Targets {
		if !strings.Contains(string(bodyDecoded), `"`+target+`"`) {
			t.Errorf("missing Tier-2 target tool %q in cloaked request", target)
		}
	}
	for _, src := range []string{"Bash", "Read", "Write", "Edit", "WebFetch"} {
		if strings.Contains(string(bodyDecoded), `"`+src+`"`) {
			t.Errorf("found source tool %q in cloaked request (not cloaked)", src)
		}
	}

	// Verify WebFetch round-trips cleanly via wp_fetch_url -> WebFetch.
	respBody := []byte(`{"id":"msg_wf","type":"message","role":"assistant","content":[{"type":"tool_use","id":"tu_wf","name":"wp_fetch_url","input":{"url":"https://example.com"}}]}`)
	respRaw := HandleResponseIntercept(makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-test", respBody))
	respDecoded := decodeEnvelopeBody(t, respRaw)
	if !strings.Contains(string(respDecoded), `"name":"WebFetch"`) || strings.Contains(string(respDecoded), `"wp_fetch_url"`) {
		t.Fatalf("expected wp_fetch_url to uncloak to WebFetch, got: %s", respDecoded)
	}
}

func TestCC_SharedAliasesCloakViaAliasPlan(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_shared"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"ToolSearch","description":""},
			{"name":"Skill","description":""},
			{"name":"Workflow","description":""}
		]
	}`)

	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	targets := []string{
		"run_command", "wp_find_tools", "wp_invoke_skill", "wp_run_workflow",
	}
	for _, target := range targets {
		if !strings.Contains(string(bodyDecoded), `"`+target+`"`) {
			t.Errorf("missing target tool %q", target)
		}
	}
}

func TestCC_UnknownMcpDeclarationGetsFallback(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_mcp"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""},
			{"name":"Grep","description":""},
			{"name":"Glob","description":""},
			{"name":"mcp__context7__resolve_library_id","description":""}
		]
	}`)

	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/claude-test", reqBody)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	if !strings.Contains(string(bodyDecoded), `"wp_ext_`) {
		t.Errorf("missing wp_ext_ fallback for unknown MCP tool")
	}
	if strings.Contains(string(bodyDecoded), `"mcp__context7__resolve_library_id"`) {
		t.Errorf("mcp tool name leaked")
	}
}

func TestCC_ThinMarkerRequest(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_thin"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""}
		]
	}`)

	headers := make(http.Header)
	headers.Set("X-Cloak-Client", "claude_code")

	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	if !strings.Contains(string(bodyDecoded), `"run_command"`) {
		t.Errorf("thin request not cloaked")
	}
}

func TestCC_ResponseReversalViaAliasPlan(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_resp"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Workflow","description":""}
		]
	}`)
	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers)
	HandleRequestInterceptBefore(reqPayload)

	respBody := []byte(`{
		"content": [
			{
				"type": "tool_use",
				"id": "toolu_123",
				"name": "run_command",
				"input": {}
			},
			{
				"type": "tool_use",
				"id": "toolu_456",
				"name": "wp_run_workflow",
				"input": {}
			}
		]
	}`)

	respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-test", respBody)
	rawEnvelope := HandleResponseIntercept(respPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	if !strings.Contains(string(bodyDecoded), `"Bash"`) {
		t.Errorf("missing reversed Bash, got: %s", bodyDecoded)
	}
	if !strings.Contains(string(bodyDecoded), `"Workflow"`) {
		t.Errorf("missing reversed Workflow, got: %s", bodyDecoded)
	}
}

func TestCC_StreamReversalViaAliasPlan(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_stream"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""}
		]
	}`)
	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/claude-test", reqBody)
	HandleRequestInterceptBefore(reqPayload)

	chunkBody := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"run_command","input":{}}}` + "\n\n")

	chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/claude-test", 0, chunkBody, nil)
	rawEnvelope := HandleStreamChunkIntercept(chunkPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	if !strings.Contains(string(bodyDecoded), `"name":"Bash"`) {
		t.Errorf("missing stream reversed Bash, got: %s", bodyDecoded)
	}
}

func TestCC_HistoryConsistency(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_history"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""}
		],
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_01",
						"name": "Bash",
						"input": {}
					}
				]
			}
		]
	}`)

	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/claude-test", reqBody)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	if !strings.Contains(string(bodyDecoded), `"name":"run_command"`) {
		t.Errorf("history not cloaked, got: %s", bodyDecoded)
	}
}

func TestCC_ToolChoiceConsistency(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_tool_choice"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""},
			{"name":"Grep","description":""}
		],
		"tool_choice": {"type":"tool","name":"Bash"}
	}`)

	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/claude-test", reqBody)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)

	if !strings.Contains(string(bodyDecoded), `"name":"run_command"`) {
		t.Errorf("tool_choice not cloaked, got: %s", bodyDecoded)
	}
}

func TestCC_AllTier2Vocabulary(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_all_tier2"
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"ToolSearch","description":""},
			{"name":"Skill","description":""},
			{"name":"Workflow","description":""},
			{"name":"ListAgents","description":""},
			{"name":"SendMessage","description":""},
			{"name":"TaskStop","description":""},
			{"name":"ScheduleWakeup","description":""},
			{"name":"CronCreate","description":""},
			{"name":"CronDelete","description":""},
			{"name":"CronList","description":""},
			{"name":"EnterPlanMode","description":""},
			{"name":"ExitPlanMode","description":""},
			{"name":"EnterWorktree","description":""},
			{"name":"ExitWorktree","description":""},
			{"name":"NotebookEdit","description":""},
			{"name":"ReportFindings","description":""},
			{"name":"DeferredToolPlaceholder","description":""},
			{"name":"WaitForMcpServers","description":""},
			{"name":"ListMcpResourcesTool","description":""},
			{"name":"ReadMcpResourceTool","description":""},
			{"name":"ReadMcpResourceDirTool","description":""}
		]
	}`)

	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := decodeEnvelopeBody(t, rawEnvelope)
	got := string(bodyDecoded)

	expectedAliases := map[string]string{
		"ToolSearch":              "wp_find_tools",
		"Skill":                   "wp_invoke_skill",
		"Workflow":                "wp_run_workflow",
		"ListAgents":              "wp_list_workers",
		"SendMessage":             "wp_send_message",
		"TaskStop":                "wp_cancel_task",
		"ScheduleWakeup":          "wp_set_wakeup",
		"CronCreate":              "wp_create_schedule",
		"CronDelete":              "wp_delete_schedule",
		"CronList":                "wp_list_schedules",
		"EnterPlanMode":           "wp_begin_planning",
		"ExitPlanMode":            "wp_finish_planning",
		"EnterWorktree":           "wp_open_worktree",
		"ExitWorktree":            "wp_close_worktree",
		"NotebookEdit":            "wp_edit_notebook",
		"ReportFindings":          "wp_submit_report",
		"DeferredToolPlaceholder": "wp_resolve_tool",
		"WaitForMcpServers":       "wp_wait_integrations",
		"ListMcpResourcesTool":    "wp_list_resources",
		"ReadMcpResourceTool":     "wp_read_resource",
		"ReadMcpResourceDirTool":  "wp_list_resource_dir",
	}

	for src, want := range expectedAliases {
		if !strings.Contains(got, `"`+want+`"`) {
			t.Errorf("missing Tier-2 alias %q for %s in %s", want, src, got)
		}
		if strings.Contains(got, `"`+src+`"`) {
			t.Errorf("source name %s leaked in cloaked body", src)
		}
	}
}

func TestCC_MissingCorrelation503(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""}
		]
	}`)

	// Missing RequestID ("") on eligible Claude Code request -> must fail closed with 503 tool_cloak_required
	reqPayload := makeIntegrationRequestInterceptPayload(t, "", "anthropic", "agy/claude-test", reqBody)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)

	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Terminate    bool   `json:"Terminate"`
			StatusCode   int    `json:"StatusCode"`
			ResponseBody string `json:"ResponseBody"`
		} `json:"result"`
	}
	mustUnmarshalJSON(t, rawEnvelope, &envelope)

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

func TestCC_NamespaceVariantCollisionFailsClosed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	cases := []struct {
		name  string
		tools []string
	}{
		{"unknown namespace variants", []string{"functions:foo", "default_api:foo"}},
		{"mapped namespace variants", []string{"functions:Read", "default_api:Read"}},
		{"mapped and native target", []string{"functions:Read", "default_api:read_file"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var toolDecls []string
			for _, tool := range tc.tools {
				toolDecls = append(toolDecls, `{"name":"`+tool+`","description":""}`)
			}
			reqBody := []byte(`{"tools":[` + strings.Join(toolDecls, ",") + `]}`)
			headers := make(http.Header)
			headers.Set("X-Cloak-Client", "claude_code")
			reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, "cc-collision-"+tc.name, "anthropic", "agy/claude-test", reqBody, headers)
			rawEnvelope := HandleRequestInterceptBefore(reqPayload)
			var envelope struct {
				OK     bool `json:"ok"`
				Result struct {
					Terminate    bool   `json:"Terminate"`
					StatusCode   int    `json:"StatusCode"`
					ResponseBody string `json:"ResponseBody"`
				} `json:"result"`
			}
			mustUnmarshalJSON(t, rawEnvelope, &envelope)
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

func TestCC_DeferredToolPlaceholderProtocolPositions(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_deferred_positions"
	// Test DeferredToolPlaceholder across the three standard protocol positions:
	// 1. tools[] declaration
	// 2. messages[] history (tool_use block)
	// 3. tool_choice specification
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":""},
			{"name":"DeferredToolPlaceholder","description":""}
		],
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "tu_def_01",
						"name": "DeferredToolPlaceholder",
						"input": {"name":"target_tool"}
					}
				]
			}
		],
		"tool_choice": {"type":"tool","name":"DeferredToolPlaceholder"}
	}`)

	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := string(decodeEnvelopeBody(t, rawEnvelope))

	// All protocol positions must be cloaked to wp_resolve_tool
	if !strings.Contains(bodyDecoded, `"wp_resolve_tool"`) {
		t.Fatalf("missing wp_resolve_tool in cloaked body: %s", bodyDecoded)
	}
	if strings.Contains(bodyDecoded, `"DeferredToolPlaceholder"`) {
		t.Fatalf("DeferredToolPlaceholder leaked in protocol positions: %s", bodyDecoded)
	}

	// Response uncloaking must restore exact source name
	respBody := []byte(`{
		"content": [
			{
				"type": "tool_use",
				"id": "tu_resp_01",
				"name": "wp_resolve_tool",
				"input": {}
			}
		]
	}`)
	respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-test", respBody)
	rawRespEnvelope := HandleResponseIntercept(respPayload)
	respDecoded := string(decodeEnvelopeBody(t, rawRespEnvelope))
	if !strings.Contains(respDecoded, `"DeferredToolPlaceholder"`) {
		t.Fatalf("response uncloak failed to restore DeferredToolPlaceholder: %s", respDecoded)
	}
	if strings.Contains(respDecoded, `"wp_resolve_tool"`) {
		t.Fatalf("wp_resolve_tool leaked in response: %s", respDecoded)
	}
}

func TestCC_ArbitraryDataFieldsUntouched(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_arbitrary_data"
	// Payload with tool names inside arbitrary data/metadata fields, outside protocol tool-identity positions
	reqBody := []byte(`{
		"tools": [
			{"name":"Bash","description":"Execute command"},
			{"name":"Read","description":"Read file"}
		],
		"metadata": {
			"author_name": "Read",
			"tool_info": {"name": "Bash", "type": "config"}
		},
		"messages": [
			{
				"role": "user",
				"content": "Please inspect user Read with profile name Bash."
			}
		]
	}`)

	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/claude-test", reqBody)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := string(decodeEnvelopeBody(t, rawEnvelope))

	// Protocol tool declarations must be cloaked
	if !strings.Contains(bodyDecoded, `"run_command"`) || !strings.Contains(bodyDecoded, `"read_file"`) {
		t.Fatalf("tool declarations were not cloaked: %s", bodyDecoded)
	}
	// Metadata and user content fields must remain completely untouched
	if !strings.Contains(bodyDecoded, `"author_name":"Read"`) {
		t.Fatalf("metadata.author_name was mutated: %s", bodyDecoded)
	}
	if !strings.Contains(bodyDecoded, `"name":"Bash"`) {
		t.Fatalf("metadata.tool_info.name was mutated: %s", bodyDecoded)
	}
	if !strings.Contains(bodyDecoded, "Please inspect user Read with profile name Bash.") {
		t.Fatalf("user message content was mutated: %s", bodyDecoded)
	}
}

func TestDetectCloakedClient_AtFullCoverage_UnequalTableSizes(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// Two qualifying clients with equal ratio (4/4 = 1.0) and equal hits (4), but unequal table sizes:
	// codex has 4 targets (100% full coverage), claude_code has 9 targets (44% partial coverage).
	// The fully covered client must win over the partial-coverage superset client.
	observed := []string{"run_command", "search_web", "ask_question", "invoke_subagent"}
	got := detectCloakedClient(observed)
	if got != "codex" {
		t.Fatalf("detectCloakedClient(%v) = %q, want \"codex\" (full-coverage tie break over partial)", observed, got)
	}
}

func TestCC_AlreadyCloakedBody_WithoutAuthority_PassesThrough(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	// An executed/already-cloaked request body where only run_command (and search_web,
	// ask_question) are present, so cloak-TARGET detection would guess claude_code.
	// There is no RequestID, no alias plan, no marker and no UA evidence, so the client
	// cannot be attributed. Executed targets are not pinned reverse authority (Issue #39):
	// the response must pass through unmutated instead of being statically reversed.
	cloakedReqBody := []byte(`{
		"tools": [
			{"name": "run_command", "description": ""},
			{"name": "search_web", "description": ""},
			{"name": "ask_question", "description": ""}
		]
	}`)

	// Upstream response returns tool_use for both run_command and read_file.
	respBody := []byte(`{
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "run_command", "input": {"command": "ls"}},
			{"type": "tool_use", "id": "tu_2", "name": "read_file", "input": {"path": "main.go"}}
		]
	}`)

	// Uncorrelated response passing the already-cloaked request body as RequestBody
	var req pluginapi.ResponseInterceptRequest
	req.Model = "agy/claude-test"
	req.SourceFormat = "anthropic"
	req.RequestBody = cloakedReqBody
	req.Body = respBody

	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	rawResp := HandleResponseIntercept(payload)
	decoded := decodeEnvelopeBody(t, rawResp)
	if decoded != nil && !bytes.Equal(decoded, respBody) {
		t.Fatalf("executed body without pinned authority must pass through unmutated: %s", string(decoded))
	}
}

func TestCC_ResponseLeavesNativeTargetAlone(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)), nil)

	reqID := "req_cc_native_target_neg"
	// Claude Code request declares ONLY Bash (which maps to run_command).
	// Read (which maps to read_file) is deliberately omitted.
	reqBody := []byte(`{
		"messages": [{"role":"user","content":"run command"}],
		"tools": [
			{"name":"Bash","description":"run a shell command"}
		]
	}`)
	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers)
	rawEnvelope := HandleRequestInterceptBefore(reqPayload)
	bodyDecoded := string(decodeEnvelopeBody(t, rawEnvelope))
	if !strings.Contains(bodyDecoded, `"run_command"`) {
		t.Fatalf("Bash was not cloaked to run_command: %s", bodyDecoded)
	}
	if strings.Contains(bodyDecoded, `"Bash"`) {
		t.Fatalf("Bash leaked in request body: %s", bodyDecoded)
	}

	// Upstream response contains both run_command (declared) and read_file
	// (native AGY target for undeclared Read).
	respBody := []byte(`{
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "run_command", "input": {"command": "ls"}},
			{"type": "tool_use", "id": "tu_2", "name": "read_file", "input": {"path": "main.go"}}
		]
	}`)

	// 1. Correlated response path:
	// Plan must reverse run_command -> Bash, but MUST leave read_file untouched.
	respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-test", respBody)
	rawRespEnvelope := HandleResponseIntercept(respPayload)
	respDecoded := string(decodeEnvelopeBody(t, rawRespEnvelope))
	if !strings.Contains(respDecoded, `"Bash"`) {
		t.Fatalf("run_command was not uncloaked to Bash: %s", respDecoded)
	}
	if !strings.Contains(respDecoded, `"read_file"`) {
		t.Fatalf("native target read_file was improperly modified in response: %s", respDecoded)
	}
	if strings.Contains(respDecoded, `"Read"`) {
		t.Fatalf("undeclared tool Read was improperly introduced into response: %s", respDecoded)
	}

	// 2. Correlated stream path:
	// A stream chunk carrying read_file must NOT be rewritten to Read.
	streamChunk := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_2","name":"read_file","input":{}}}` + "\n\n")
	chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/claude-test", 0, streamChunk, nil)
	rawChunkEnvelope := HandleStreamChunkIntercept(chunkPayload)
	chunkDecoded := decodeEnvelopeBody(t, rawChunkEnvelope)
	chunkStr := string(chunkDecoded)
	if chunkDecoded != nil && strings.Contains(chunkStr, `"Read"`) {
		t.Fatalf("stream chunk improperly reversed read_file to Read: %s", chunkStr)
	}
	if chunkDecoded != nil && !strings.Contains(chunkStr, `"read_file"`) {
		t.Fatalf("stream chunk improperly altered read_file: %s", chunkStr)
	}

	// 3. Uncorrelated response path:
	// An uncorrelated response with no matching request / without declared Read authority
	// must leave read_file untouched and never invent Read.
	uncorrelatedRespPayload := makeIntegrationResponseInterceptPayload(t, "req_cc_uncorrelated_neg", "anthropic", "agy/claude-test", respBody)
	rawUncorrelatedEnvelope := HandleResponseIntercept(uncorrelatedRespPayload)
	uncorrelatedBody := decodeEnvelopeBody(t, rawUncorrelatedEnvelope)
	if uncorrelatedBody != nil {
		uncorrelatedStr := string(uncorrelatedBody)
		if strings.Contains(uncorrelatedStr, `"Read"`) {
			t.Fatalf("uncorrelated response improperly reversed read_file to Read: %s", uncorrelatedStr)
		}
		if !strings.Contains(uncorrelatedStr, `"read_file"`) {
			t.Fatalf("native target read_file was dropped or altered in uncorrelated response: %s", uncorrelatedStr)
		}
	}
}
