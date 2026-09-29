package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRewriteRequestReplacesDefaultSystemKeywords(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		// client is the resolved client the case is driven with.
		client string
		// wantUnchanged marks a body no client owns, so nothing may touch it.
		wantUnchanged bool
	}{
		{
			// A competitor name belongs to no table: under the per-client model
			// only a client's own identity is rewritten, so this survives.
			name:          "competitor name is owned by nobody",
			body:          `{"system":"You are OpenCode, an AI coding tool."}`,
			client:        "claude_code",
			wantUnchanged: true,
		},
		{
			name:   "array system mentions claude code",
			body:   `{"system":[{"type":"text","text":"Run as Claude Code."}]}`,
			want:   "Run as Antigravity.",
			client: "claude_code",
		},
		{
			name:   "case insensitive codex",
			body:   `{"system":"route this CODEX session"}`,
			want:   "route this Antigravity session",
			client: "codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rewritten, _ := rewriteRequestBodyWithClient([]byte(tt.body), "openai", tt.client)
			if tt.wantUnchanged {
				if rewritten {
					t.Fatalf("body owned by no client was rewritten: %s", got)
				}
				return
			}
			if !rewritten {
				t.Fatalf("rewritten = false, want true")
			}
			if !containsSystemText(t, got, tt.want) {
				t.Fatalf("rewritten body = %s, want system text %q", got, tt.want)
			}
		})
	}
}

func TestRewriteRequestCloaksClientContextButNotTypedUserText(t *testing.T) {
	// The typed message is the user's own words and is left byte-identical: if
	// it were cloaked, anything the model then writes to disk would be
	// persisted in the cloaked spelling with no way back, because a file never
	// flows through the response path that reverses the other direction. The
	// assistant turn and the client's own <system-reminder> block are
	// machine-generated context and are still cloaked.
	body := []byte(`{
		"messages":[
			{"role":"user","content":"compare Claude Code and Codex please"},
			{"role":"assistant","content":"Claude Code is a tool"},
			{"role":"user","content":"<system-reminder>the catalogue mentions Anthropic</system-reminder>"}
		],
		"input":"Claude Code is mentioned by the user"
	}`)
	got, rewritten, _ := rewriteRequestBodyWithClient(body, "openai", "claude_code")
	if !rewritten {
		t.Fatalf("client context must still be rewritten; body=%s", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	msgs := doc["messages"].([]any)
	if typed := msgs[0].(map[string]any)["content"].(string); typed != "compare Claude Code and Codex please" {
		t.Errorf("typed user text was altered: %q", typed)
	}
	if assistant := msgs[1].(map[string]any)["content"].(string); strings.Contains(assistant, "Claude") {
		t.Errorf("assistant text must be cloaked, got %q", assistant)
	}
	if reminder := msgs[2].(map[string]any)["content"].(string); !strings.Contains(reminder, "Google Deepmind") {
		t.Errorf("<system-reminder> client context must be cloaked, got %q", reminder)
	}
	if s, _ := doc["input"].(string); s != "Claude Code is mentioned by the user" {
		t.Errorf("top-level input is the typed turn and must be untouched, got %q", s)
	}
}

func TestRewriteRequestAllowsCleanInvalidAndStructuralBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "clean json",
			body: `{"system":"You are Antigravity.","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "invalid json",
			body: `{`,
		},
		{
			name: "empty body",
			body: ``,
		},
		{
			name: "prompt cache key",
			body: `{"prompt_cache_key":"session-cache","system":"plain"}`,
		},
		{
			name: "metadata user id",
			body: `{"metadata":{"user_id":"user-123"},"system":"plain"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rewritten := rewriteRequestBody([]byte(tt.body), "openai")
			if rewritten {
				t.Fatalf("rewritten = true, want false; body=%s", tt.body)
			}
		})
	}
}

func TestRewriteRequestBodyCloaksClaudeCodeTools(t *testing.T) {
	// OpenAI format with Claude Code tools
	body := `{
		"system":"You are Claude Code.",
		"tools":[
			{"type":"function","function":{"name":"Bash","description":"Run Claude Code shell commands"}},
			{"type":"function","function":{"name":"Read","description":"Read files"}},
			{"type":"function","function":{"name":"Edit","description":"Edit files"}}
		],
		"messages":[],
		"tool_choice":{"type":"function","function":{"name":"Bash"}}
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("want rewritten")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	// Assert tools[0].function.name == "run_command"
	toolsRaw := parsed["tools"].([]any)
	t0 := toolsRaw[0].(map[string]any)["function"].(map[string]any)
	if name := t0["name"].(string); name != "run_command" {
		t.Errorf("tools[0] name = %q, want run_command", name)
	}
	// Assert description brand replace: "Claude Code" → "Antigravity" in description
	if desc := t0["description"].(string); desc != "Run Antigravity shell commands" {
		t.Errorf("tools[0] description = %q, want 'Run Antigravity shell commands'", desc)
	}

	// Assert tools[1].function.name == "view_file"
	t1 := toolsRaw[1].(map[string]any)["function"].(map[string]any)
	if name := t1["name"].(string); name != "view_file" {
		t.Errorf("tools[1] name = %q, want view_file", name)
	}

	// Assert tool_choice.function.name == "run_command"
	tc := parsed["tool_choice"].(map[string]any)["function"].(map[string]any)
	if tcName := tc["name"].(string); tcName != "run_command" {
		t.Errorf("tool_choice name = %q, want run_command", tcName)
	}

	// Assert system field brand replace
	if sys := parsed["system"].(string); sys != "You are Antigravity." {
		t.Errorf("system field = %q, want 'You are Antigravity.'", sys)
	}
}

func TestRewriteRequestBodyCloaksCodexTools(t *testing.T) {
	body := `{
		"system":"You are Codex.",
		"tools":[
			{"type":"function","function":{"name":"exec","description":"Execute Codex shell"}},
			{"type":"function","function":{"name":"request_user_input","description":"Ask the user"}}
		],
		"messages":[]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("want rewritten")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	toolsRaw := parsed["tools"].([]any)
	t0 := toolsRaw[0].(map[string]any)["function"].(map[string]any)
	if name := t0["name"].(string); name != "run_command" {
		t.Errorf("tools[0] name = %q, want run_command", name)
	}
	if desc := t0["description"].(string); desc != "Execute Antigravity shell" {
		t.Errorf("tools[0] description = %q, want 'Execute Antigravity shell'", desc)
	}

	t1 := toolsRaw[1].(map[string]any)["function"].(map[string]any)
	if name := t1["name"].(string); name != "ask_question" {
		t.Errorf("tools[1] name = %q, want ask_question", name)
	}
}

func TestRewriteRequestBodyCloaksToolRefsInMessageHistory(t *testing.T) {
	body := `{
		"tools":[
			{"type":"function","function":{"name":"Bash"}},
			{"type":"function","function":{"name":"Edit"}},
			{"type":"function","function":{"name":"Read"}}
		],
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"tc1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"tc1","name":"Bash","content":"output"}
		]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("want rewritten")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	msgs := parsed["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	m0calls := m0["tool_calls"].([]any)
	t0call := m0calls[0].(map[string]any)["function"].(map[string]any)
	if name := t0call["name"].(string); name != "run_command" {
		t.Errorf("tool_call function name = %q, want run_command", name)
	}

	m1 := msgs[1].(map[string]any)
	if name := m1["name"].(string); name != "run_command" {
		t.Errorf("message[1] name = %q, want run_command", name)
	}
}

func TestRewriteRequestBodyHandlesToolChoiceShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "tool_choice as string (skip safely)",
			body: `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Edit"}},{"type":"function","function":{"name":"Read"}}],"tool_choice":"auto","messages":[]}`,
		},
		{
			name: "tool_choice as object with function.name",
			body: `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Edit"}},{"type":"function","function":{"name":"Read"}}],"tool_choice":{"type":"function","function":{"name":"Bash"}},"messages":[]}`,
		},
		{
			name: "anthropic tool_choice as object with name",
			body: `{"tools":[{"name":"Bash"},{"name":"Edit"},{"name":"Read"}],"tool_choice":{"type":"tool","name":"Bash"},"messages":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceFormat := "openai"
			if strings.Contains(tt.name, "anthropic") {
				sourceFormat = "anthropic"
			}
			got, rewritten := rewriteRequestBody([]byte(tt.body), sourceFormat)
			if !rewritten {
				t.Fatal("want rewritten")
			}
			var parsed map[string]any
			json.Unmarshal(got, &parsed)

			if strings.Contains(tt.name, "string") {
				if tc := parsed["tool_choice"].(string); tc != "auto" {
					t.Errorf("expected tool_choice auto, got %v", tc)
				}
			} else if strings.Contains(tt.name, "anthropic") {
				tc := parsed["tool_choice"].(map[string]any)
				if name := tc["name"].(string); name != "run_command" {
					t.Errorf("anthropic tool_choice name = %q, want run_command", name)
				}
			} else {
				tc := parsed["tool_choice"].(map[string]any)["function"].(map[string]any)
				if name := tc["name"].(string); name != "run_command" {
					t.Errorf("openai tool_choice name = %q, want run_command", name)
				}
			}
		})
	}
}

func TestRewriteRequestBodySkipsAntigravityTools(t *testing.T) {
	body := `{
		"tools":[{"type":"function","function":{"name":"ask_permission"}},{"type":"function","function":{"name":"run_command"}}],
		"messages":[]
	}`
	_, rewritten := rewriteRequestBody([]byte(body), "openai")
	if rewritten {
		t.Fatal("expected no rewritten body as all tools are already antigravity tools and no system/desc brand replace is triggered")
	}
}

func TestRewriteRequestBodySkipsUnknownTools(t *testing.T) {
	body := `{
		"tools":[{"type":"function","function":{"name":"custom_tool"}},{"type":"function","function":{"name":"another_tool"}}],
		"messages":[]
	}`
	_, rewritten := rewriteRequestBody([]byte(body), "openai")
	if rewritten {
		t.Fatal("want no rewrite for unknown tools")
	}
}

func TestRewriteRequestBodyAppliesBrandReplaceToToolDescription(t *testing.T) {
	body := `{
		"tools":[{"type":"function","function":{"name":"bash","description":"Claude Code shell tool"}}],
		"messages":[]
	}`
	got, rewritten, _ := rewriteRequestBodyWithClient([]byte(body), "openai", "claude_code")
	if !rewritten {
		t.Fatal("want rewritten")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	toolsRaw := parsed["tools"].([]any)
	t0 := toolsRaw[0].(map[string]any)["function"].(map[string]any)
	if desc := t0["description"].(string); !strings.Contains(desc, "Antigravity") || strings.Contains(desc, "Claude Code") {
		t.Errorf("description = %q, want Claude Code replaced with Antigravity", desc)
	}
}

func TestRewriteRequestBodyAppliesBrandReplaceToSystemMessages(t *testing.T) {
	body := `{
		"tools":[{"type":"function","function":{"name":"bash"}}],
		"messages":[
			{"role":"system","content":"You are Claude Code assistant."},
			{"role":"user","content":"hello Claude Code"}
		]
	}`
	got, rewritten, _ := rewriteRequestBodyWithClient([]byte(body), "openai", "claude_code")
	if !rewritten {
		t.Fatal("want rewritten")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	msgs := parsed["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if content := m0["content"].(string); !strings.Contains(content, "Antigravity") || strings.Contains(content, "Claude Code") {
		t.Errorf("system message content = %q, want brand replaced", content)
	}

	m1 := msgs[1].(map[string]any)
	if content := m1["content"].(string); content != "hello Claude Code" {
		t.Errorf("typed user message = %q, want it left byte-identical", content)
	}
}

func TestRewriteRequestBodyCloaksAnthropicFormat(t *testing.T) {
	body := `{
		"system":"You are Claude Code.",
		"tools":[
			{"name":"Bash","description":"Run Claude Code shell"},
			{"name":"Read","description":"Read files"},
			{"name":"Edit","description":"Edit files"}
		],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]}
		]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "anthropic")
	if !rewritten {
		t.Fatal("want rewritten")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	toolsRaw := parsed["tools"].([]any)
	t0 := toolsRaw[0].(map[string]any)
	if name := t0["name"].(string); name != "run_command" {
		t.Errorf("tools[0] name = %q, want run_command", name)
	}
	if desc := t0["description"].(string); desc != "Run Antigravity shell" {
		t.Errorf("tools[0] description = %q, want 'Run Antigravity shell'", desc)
	}

	t1 := toolsRaw[1].(map[string]any)
	if name := t1["name"].(string); name != "view_file" {
		t.Errorf("tools[1] name = %q, want view_file", name)
	}

	msgs := parsed["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	m0content := m0["content"].([]any)
	c0 := m0content[0].(map[string]any)
	if name := c0["name"].(string); name != "run_command" {
		t.Errorf("content name = %q, want run_command", name)
	}
}

func containsSystemText(t *testing.T, body []byte, want string) bool {
	t.Helper()

	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode rewritten body: %v", err)
	}

	found := false
	walkJSON(root, func(path []string, value any) bool {
		if len(path) == 0 || path[len(path)-1] != "system" {
			return true
		}
		found = strings.Contains(collectText(value), want)
		return !found
	})
	return found
}

func TestUncloakTablesInitialization(t *testing.T) {
	if len(defaultUncloakTables) == 0 {
		t.Fatal("uncloak tables not initialized")
	}
	// Claude Code
	if defaultUncloakTables["claude_code"]["run_command"] != "Bash" {
		t.Fatal("expected Bash")
	}
	// Codex keys on the code-mode surface: "exec" owns run_command, and the
	// collaboration children are keyed in opencodex's flattened "__" spelling
	// (see defaultCloakTables).
	if defaultUncloakTables["codex"]["run_command"] != "exec" {
		t.Fatal("expected exec")
	}
	if defaultUncloakTables["codex"]["invoke_subagent"] != "collaboration__spawn_agent" {
		t.Fatal("expected collaboration__spawn_agent")
	}
	if defaultUncloakTables["codex"]["search_web"] != "web_search" {
		t.Fatal("expected web_search")
	}
	// Verify no key collision within a client's cloak table
	for client, cloaks := range defaultCloakTables {
		seen := make(map[string]bool)
		for _, target := range cloaks {
			if seen[target] {
				t.Fatalf("client %s has duplicate target %s", client, target)
			}
			seen[target] = true
		}
	}
}

func TestDetectClient(t *testing.T) {
	tests := []struct {
		name       string
		toolNames  []string
		wantClient string
	}{
		{"claude code by askUserQuestion", []string{"Bash", "AskUserQuestion", "Read"}, "claude_code"},
		{"claude code by signature trio", []string{"Bash", "Edit", "Read", "Write"}, "claude_code"},
		{"codex by exec and request_user_input", []string{"exec", "request_user_input"}, "codex"},
		{"codex by exec and web_search", []string{"exec", "web_search"}, "codex"},
		{"codex by subagent control tools", []string{"spawn_agent", "list_agents"}, "codex"},
		// Shell-mode Codex declares exec_command/view_image instead of exec.
		{"codex shell mode by exec_command and view_image", []string{"exec_command", "view_image"}, "codex"},
		{"codex shell mode by exec_command and request_user_input", []string{"exec_command", "request_user_input"}, "codex"},
		// minToolNameHits still floors every client at two source-name hits.
		{"single codex signature below threshold", []string{"exec_command", "custom_tool"}, ""},
		// detectClient only matches original (cloak table key) names; Antigravity
		// native tools are NOT keys, so detectClient returns "" for them.
		{"antigravity tools return empty", []string{"ask_permission", "run_command"}, ""},
		{"antigravity-like invoke_subagent", []string{"invoke_subagent", "view_file"}, ""},
		{"unknown tools", []string{"custom_tool", "another_tool"}, ""},
		{"empty list", []string{}, ""},
		// Only 1 original key match → below threshold of 2
		{"single original match below threshold", []string{"Bash", "ask_permission"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectClient(tt.toolNames)
			if got != tt.wantClient {
				t.Fatalf("detectClient(%v) = %q, want %q", tt.toolNames, got, tt.wantClient)
			}
		})
	}
}

func TestDetectClientCountsConfiguredCodexSourceKeys(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	raw, code := handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`
tool_mappings:
  codex:
    custom_wire_a: wp_custom_wire_a
    custom_wire_b: wp_custom_wire_b
`)))
	if code != 0 {
		t.Fatalf("code = %d, want 0; body=%s", code, raw)
	}
	// Codex counts against its static inventory, which cannot know an
	// operator-added source name, so the configured rename table has to be
	// counted as well or the custom mapping is undetectable.
	if got := detectClient([]string{"custom_wire_a", "custom_wire_b"}); got != "codex" {
		t.Fatalf("detectClient() = %q, want codex for configured codex sources", got)
	}
}

func TestDetectCloakedClient(t *testing.T) {
	tests := []struct {
		name       string
		toolNames  []string
		wantClient string
	}{
		// All Claude Code static cloak TARGETS present → detected as claude_code.
		// Only Tier-1 AGY roles are in the static table; Tier-2 (ListAgents,
		// SendMessage, TaskStop, MCP resources) are in shared aliases, whose
		// wp_ targets are not in the static uncloak table.
		{"cloaked claude code", []string{"run_command", "replace_file_content", "view_file", "write_to_file", "grep_search", "find_by_name", "invoke_subagent", "ask_question", "search_web", "read_url_content"}, "claude_code"},
		// Codex cloak TARGETS present → detected as codex.
		// The static codex table now has fewer targets (exec->run_command,
		// web_search->search_web, request_user_input->ask_question,
		// collaboration__spawn_agent->invoke_subagent), and several overlap
		// with the expanded CC table. A pure-codex stream is detected from
		// request context (buildUncloakTable), not cloaked-target detection.
		{"cloaked codex", []string{"run_command", "search_web", "ask_question", "invoke_subagent"}, "codex"},
		// A realistic cloaked Codex body mixes four cloak targets with pass-through
		// names that no client table owns, so target-coverage detection cannot reach
		// its 80% threshold. That is expected: response/stream uncloaking resolves
		// the client from OriginalRequest tool names via buildUncloakTable, not from
		// the target-coverage fallback.
		{"cloaked codex with pass-throughs stays undetected", []string{"run_command", "wait", "request_user_input_async", "sleep", "send_message", "wait_agent", "interrupt_agent", "ask_question", "invoke_subagent"}, ""},
		// Both clients' targets present (native Antigravity) → returns ""
		{"native antigravity superset", []string{"run_command", "replace_file_content", "view_file", "write_to_file", "grep_search", "find_by_name", "invoke_subagent", "ask_question", "search_web", "read_url_content", "multi_replace_file_content", "generate_image", "define_subagent", "list_permissions", "ask_permission"}, ""},
		// Too few targets → no match
		{"too few matches", []string{"run_command", "ask_question"}, ""},
		// Unknown tools → no match
		{"unknown tools", []string{"custom_tool", "another_tool"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectCloakedClient(tt.toolNames)
			if got != tt.wantClient {
				t.Fatalf("detectCloakedClient(%v) = %q, want %q", tt.toolNames, got, tt.wantClient)
			}
		})
	}
}

func TestExtractToolNames(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		sourceFormat string
		wantLen      int // minimum expected tool names
	}{
		{
			name:         "openai tools array",
			body:         `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"read"}}]}`,
			sourceFormat: "openai",
			wantLen:      2,
		},
		{
			name:         "anthropic tools array",
			body:         `{"tools":[{"name":"bash","description":"run shell"},{"name":"read","description":"read file"}]}`,
			sourceFormat: "anthropic",
			wantLen:      2,
		},
		{
			name:         "openai fallback to message history tool_calls",
			body:         `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"bash"}}]}]}`,
			sourceFormat: "openai",
			wantLen:      1,
		},
		{
			name:         "anthropic fallback to message history tool_use",
			body:         `{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"bash"}]}]}`,
			sourceFormat: "anthropic",
			wantLen:      1,
		},
		{
			name:         "empty tools and no history",
			body:         `{"messages":[{"role":"user","content":"hello"}]}`,
			sourceFormat: "openai",
			wantLen:      0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(tt.body), &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			names := extractToolNames(parsed, tt.sourceFormat)
			if len(names) < tt.wantLen {
				t.Fatalf("extractToolNames got %d names %v, want >= %d", len(names), names, tt.wantLen)
			}
		})
	}
}

func TestReplaceToolNamesInText(t *testing.T) {
	cloakTable := map[string]string{
		"bash":            "run_command",
		"read":            "view_file",
		"edit":            "replace_file_content",
		"write":           "write_to_file",
		"agent":           "invoke_subagent",
		"skill":           "call_mcp_tool",
		"workflow":        "schedule",
		"askUserQuestion": "ask_question",
		"shell_command":   "run_command",
	}
	cached := buildTestCloakPatterns(cloakTable)
	tests := []struct {
		name    string
		input   string
		want    string
		changed bool
	}{
		// Tier 1: Quoted context — all names, including ambiguous ones
		{"backtick-quoted name", "Use `bash` to run", "Use `run_command` to run", true},
		{"backtick ambiguous name", "call `read` first", "call `view_file` first", true},
		{"double-quoted name", `Use "edit" tool`, `Use "replace_file_content" tool`, true},
		{"double-quoted ambiguous", `the "agent" handles`, `the "invoke_subagent" handles`, true},

		// Tier 2: Word-boundary for unambiguous names (underscore, camelCase)
		{"camelCase word boundary", "call askUserQuestion for input", "call ask_question for input", true},
		{"underscore word boundary", "run shell_command here", "run run_command here", true},

		// Tier 3: Pattern-based for ambiguous names
		{"the X tool pattern", "the bash tool runs", "the run_command tool runs", true},
		{"the X function pattern", "the edit function", "the replace_file_content function", true},
		{"the X command pattern", "the read command", "the view_file command", true},
		{"use X pattern", "use read to view", "use view_file to view", true},
		{"call X pattern", "call edit on file", "call replace_file_content on file", true},
		{"invoke X pattern", "invoke agent now", "invoke invoke_subagent now", true},
		{"with X pattern", "with write to save", "with write_to_file to save", true},
		{"case insensitive pattern", "Use Bash to run", "Use run_command to run", true},
		{"the X tool case insensitive", "The Read Tool", "The view_file Tool", true},

		// False-positive protection — ambiguous names NOT replaced in plain prose
		{"plain prose read", "read the file contents", "read the file contents", false},
		{"plain prose edit", "edit your configuration", "edit your configuration", false},
		{"plain prose write", "write better code", "write better code", false},
		{"plain prose agent", "the agent assists you", "the agent assists you", false},
		{"partial word bashing", "bashing around", "bashing around", false},
		{"partial word reading", "reading files now", "reading files now", false},
		{"partial word writing", "writing tests", "writing tests", false},
		{"no match at all", "no tool refs here", "no tool refs here", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := replaceToolNamesInText(tt.input, cached)
			if changed != tt.changed {
				t.Fatalf("changed = %v, want %v (got %q)", changed, tt.changed, got)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolDescriptionReplacesToolNames(t *testing.T) {
	// When a Claude Code request is cloaked, tool descriptions should also have
	// tool name references replaced (not just brand text).
	body := `{
		"tools":[
			{"type":"function","function":{"name":"Bash","description":"Use the Bash tool to run commands"}},
			{"type":"function","function":{"name":"Read","description":"Use Read to view files"}}
		],
		"messages":[]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("expected rewritten = true")
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	tools := parsed["tools"].([]any)
	t0 := tools[0].(map[string]any)["function"].(map[string]any)
	desc0 := t0["description"].(string)
	if !strings.Contains(desc0, "run_command") {
		t.Fatalf("expected description to contain 'run_command', got %q", desc0)
	}
	if strings.Contains(desc0, "Bash") {
		t.Fatalf("expected 'Bash' to be replaced in description, got %q", desc0)
	}

	t1 := tools[1].(map[string]any)["function"].(map[string]any)
	desc1 := t1["description"].(string)
	if !strings.Contains(desc1, "view_file") {
		t.Fatalf("expected description to contain 'view_file', got %q", desc1)
	}
}

func TestSystemMessageReplacesToolNames(t *testing.T) {
	body := `{
		"tools":[
			{"type":"function","function":{"name":"Bash","description":"run shell"}},
			{"type":"function","function":{"name":"Read","description":"read file"}}
		],
		"messages":[
			{"role":"system","content":"Use Bash to execute commands. Call Read to view files. The Edit tool modifies content."}
		]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("expected rewritten = true")
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs := parsed["messages"].([]any)
	sysMsg := msgs[0].(map[string]any)
	content := sysMsg["content"].(string)
	if !strings.Contains(content, "run_command") {
		t.Fatalf("expected system message to contain 'run_command', got %q", content)
	}
	if !strings.Contains(content, "view_file") {
		t.Fatalf("expected system message to contain 'view_file', got %q", content)
	}
	// Tool-reference contexts ("Use Bash", "Call Read", "The Edit tool") are replaced.
	if strings.Contains(content, "Use Bash") {
		t.Fatalf("expected 'Bash' replaced in 'Use Bash' context, got %q", content)
	}
	// Plain prose tool names outside a tool pattern remain untouched.
}

func TestBuildUncloakTableWithCloakedRequest(t *testing.T) {
	// An already-cloaked body proves only which cloak TARGETS the upstream saw; it cannot
	// prove which client declared them. Full-declaration clients (Claude Code, Codex) get
	// their reverse exclusively from pinned request authority, so the executed-target guess
	// is withheld (Issue #39) rather than misidentifying the traffic as Antigravity.
	cloakedReqBody := `{
		"tools":[
			{"type":"function","function":{"name":"run_command"}},
			{"type":"function","function":{"name":"replace_file_content"}},
			{"type":"function","function":{"name":"view_file"}},
			{"type":"function","function":{"name":"write_to_file"}},
			{"type":"function","function":{"name":"grep_search"}},
			{"type":"function","function":{"name":"find_by_name"}},
			{"type":"function","function":{"name":"invoke_subagent"}},
			{"type":"function","function":{"name":"ask_question"}},
			{"type":"function","function":{"name":"search_web"}},
			{"type":"function","function":{"name":"read_url_content"}}
		],
		"messages":[]
	}`
	uncloakTable, client := buildUncloakTable([]byte(cloakedReqBody), "openai")
	if uncloakTable != nil {
		t.Fatalf("executed-target inference must be withheld without pinned authority, got %v", uncloakTable)
	}
	if client != "" {
		t.Fatalf("executed-target inference must not attribute a client, got %q", client)
	}
}

func TestCodexCodeModeCloakRewriteAndWithheldReverse(t *testing.T) {
	// A code-mode Codex session declares one freeform "exec" entry point plus
	// the collaboration children, which opencodex flattens to "<ns>__<child>"
	// for the chat-completions function-tool format. The request side renames
	// them to AGY names; the reverse is minted per request by the alias plan and
	// is deliberately withheld here because this body alone is not authority.
	body := []byte(`{
			"tools":[
				{"type":"function","function":{"name":"exec"}},
				{"type":"function","function":{"name":"web_search"}},
				{"type":"function","function":{"name":"request_user_input"}},
				{"type":"function","function":{"name":"collaboration__spawn_agent"}},
				{"type":"function","function":{"name":"collaboration__followup_task"}},
				{"type":"function","function":{"name":"collaboration__list_agents"}}
			],
			"messages":[]
		}`)
	rewritten, changed, client := rewriteRequestBodyWithClient(body, "openai", "codex")
	if !changed || client != "codex" {
		t.Fatalf("rewrite = changed:%v client:%q, want true/codex", changed, client)
	}
	got := string(rewritten)
	// The static table maps exec, web_search, request_user_input,
	// collaboration__spawn_agent. The collaboration__followup_task and
	// collaboration__list_agents were moved to codexSharedAliases and
	// are only cloaked by the alias-plan path, not the legacy path.
	for _, want := range []string{"run_command", "search_web", "ask_question", "invoke_subagent"} {
		if !strings.Contains(got, `"`+want+`"`) {
			t.Fatalf("expected cloaked target %q in %s", want, got)
		}
	}
	for _, unwanted := range []string{"web_search", "request_user_input", "collaboration__spawn_agent"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("source name %q survived cloaking: %s", unwanted, got)
		}
	}

	// buildUncloakTable is NOT reverse authority for an alias-plan client: the body
	// cannot prove the per-request forward mapping, so the reverse is withheld and
	// callers without pinned authority pass through (Issue #39). The pinned reverse
	// is covered by the correlated round-trip tests.
	uncloakTable, uncloakClient := buildUncloakTable(body, "openai")
	if uncloakClient != "" || uncloakTable != nil {
		t.Fatalf("buildUncloakTable must not guess codex reverse without pinned authority, got client=%q table=%v", uncloakClient, uncloakTable)
	}
}

func TestUncloakPreservesLargeIntegers(t *testing.T) {
	// Regression: json.Unmarshal into any converts numbers to float64,
	// causing large integers to lose precision or become scientific notation.
	// safeUnmarshal with UseNumber() must preserve them exactly.
	uncloakTable := map[string]string{"run_command": "bash"}
	body := []byte(`{"content":[{"type":"tool_use","name":"run_command","input":{"id":1234567890123456789}}]}`)

	result, changed := uncloakResponseBody(body, uncloakTable, "anthropic")
	if !changed {
		t.Fatal("expected changed = true")
	}

	resultStr := string(result)
	// Must contain the exact integer, not scientific notation
	if !strings.Contains(resultStr, "1234567890123456789") {
		t.Fatalf("large integer corrupted, got: %s", resultStr)
	}
	if strings.Contains(resultStr, "e+") || strings.Contains(resultStr, "E+") {
		t.Fatalf("integer converted to scientific notation: %s", resultStr)
	}
}

func TestUncloakPreservesHTMLCharacters(t *testing.T) {
	// Regression: json.Marshal escapes <, >, & to \u003c, \u003e, \u0026.
	// safeMarshal with SetEscapeHTML(false) must preserve them literally.
	uncloakTable := map[string]string{"run_command": "bash"}
	body := []byte(`{"content":[{"type":"tool_use","name":"run_command","input":{"html":"<div>Hello & World</div>"}}]}`)

	result, changed := uncloakResponseBody(body, uncloakTable, "anthropic")
	if !changed {
		t.Fatal("expected changed = true")
	}

	resultStr := string(result)
	if strings.Contains(resultStr, `\u003c`) || strings.Contains(resultStr, `\u003e`) || strings.Contains(resultStr, `\u0026`) {
		t.Fatalf("HTML characters were escaped to unicode: %s", resultStr)
	}
	if !strings.Contains(resultStr, "<div>") {
		t.Fatalf("expected raw <div> preserved, got: %s", resultStr)
	}
	if !strings.Contains(resultStr, "& World") {
		t.Fatalf("expected raw & preserved, got: %s", resultStr)
	}
}

func TestRewriteRequestPreservesLargeIntegers(t *testing.T) {
	// Same regression test but for the request path (rewriteRequestBody).
	body := `{
		"system":"You are Claude Code, an AI tool.",
		"tools":[{"type":"function","function":{"name":"bash","description":"runs stuff"}}],
		"messages":[{"role":"user","content":"id is 9007199254740993"}],
		"max_tokens": 9007199254740993
	}`
	result, rewritten, _ := rewriteRequestBodyWithClient([]byte(body), "openai", "claude_code")
	if !rewritten {
		t.Fatal("expected rewritten = true")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, "9007199254740993") {
		t.Fatalf("large integer corrupted in request body: %s", resultStr)
	}
	if strings.Contains(resultStr, "e+") || strings.Contains(resultStr, "E+") {
		t.Fatalf("integer converted to scientific notation: %s", resultStr)
	}
}

func TestStreamChunkPreservesDataFidelity(t *testing.T) {
	cached := buildTestUncloakPattern(map[string]string{"run_command": "bash"})
	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"code\\\":\\\"<h1>Test</h1>\\\",\\\"id\\\":1234567890123456789}\"}}]}}]}\n\n"

	result, changed := uncloakStreamChunk([]byte(chunk), cached)
	if !changed {
		t.Fatal("expected changed = true")
	}

	resultStr := string(result)
	// Verify tool name was uncloaked
	if !strings.Contains(resultStr, `"bash"`) {
		t.Fatalf("tool name not uncloaked: %s", resultStr)
	}
	// Verify no HTML escaping (regex doesn't touch non-name content)
	if strings.Contains(resultStr, `\u003c`) {
		t.Fatalf("HTML chars were escaped: %s", resultStr)
	}
	// Verify number preserved (regex doesn't touch non-name content)
	if !strings.Contains(resultStr, "1234567890123456789") {
		t.Fatalf("large integer corrupted: %s", resultStr)
	}
}

func TestSSEFragmentedChunk(t *testing.T) {
	// Legacy test: regex still works on incomplete JSON within a complete SSE event.
	// This verifies the regex layer, not the reassembly buffer.
	cached := buildTestUncloakPattern(map[string]string{"run_command": "bash"})

	// A complete SSE event (has \n\n) but with incomplete JSON (no closing brackets)
	fragment := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"\n\n")

	result, changed := uncloakStreamChunk(fragment, cached)
	if !changed {
		t.Fatal("expected regex to match even in fragmented JSON")
	}
	resultStr := string(result)
	if !strings.Contains(resultStr, `"name":"bash"`) {
		t.Fatalf("tool name not uncloaked in fragment: %s", resultStr)
	}
	if !strings.HasPrefix(resultStr, `data: {"choices"`) {
		t.Fatalf("fragment prefix corrupted: %s", resultStr)
	}
}

func TestSplitSSEEvents(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantComplete   string
		wantIncomplete string
	}{
		{
			name:           "single complete event",
			input:          "data: {\"name\":\"bash\"}\n\n",
			wantComplete:   "data: {\"name\":\"bash\"}\n\n",
			wantIncomplete: "",
		},
		{
			name:           "complete + incomplete",
			input:          "data: {\"id\":1}\n\ndata: {\"name\": \"run_c",
			wantComplete:   "data: {\"id\":1}\n\n",
			wantIncomplete: "data: {\"name\": \"run_c",
		},
		{
			name:           "no boundary - all incomplete",
			input:          "data: {\"name\": \"run_c",
			wantComplete:   "",
			wantIncomplete: "data: {\"name\": \"run_c",
		},
		{
			name:           "multiple complete events",
			input:          "data: {\"a\":1}\n\ndata: {\"b\":2}\n\n",
			wantComplete:   "data: {\"a\":1}\n\ndata: {\"b\":2}\n\n",
			wantIncomplete: "",
		},
		{
			name:           "windows line endings",
			input:          "data: {\"a\":1}\r\n\r\ndata: {\"name\": \"run_c",
			wantComplete:   "data: {\"a\":1}\r\n\r\n",
			wantIncomplete: "data: {\"name\": \"run_c",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			complete, incomplete := splitSSEEventsWithNewBytes([]byte(tt.input), len(tt.input))
			if string(complete) != tt.wantComplete {
				t.Fatalf("complete = %q, want %q", string(complete), tt.wantComplete)
			}
			if string(incomplete) != tt.wantIncomplete {
				t.Fatalf("incomplete = %q, want %q", string(incomplete), tt.wantIncomplete)
			}
		})
	}
}

func TestSplitSSEEventsWithNewBytes(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		newLen         int
		wantComplete   string
		wantIncomplete string
	}{
		{
			name:           "boundary formed across chunk split LF",
			input:          "data: {\"a\":1}\n\n",
			newLen:         1, // previous chunk had "data: {\"a\":1}\n", new chunk has "\n"
			wantComplete:   "data: {\"a\":1}\n\n",
			wantIncomplete: "",
		},
		{
			name:           "boundary formed across chunk split CRLF",
			input:          "data: {\"a\":1}\r\n\r\n",
			newLen:         2, // previous chunk had "...\r\n", new chunk has "\r\n"
			wantComplete:   "data: {\"a\":1}\r\n\r\n",
			wantIncomplete: "",
		},
		{
			name:           "boundary formed with only final LF of CRLF",
			input:          "data: {\"a\":1}\r\n\r\n",
			newLen:         1, // previous chunk had "...\r\n\r", new chunk has "\n"
			wantComplete:   "data: {\"a\":1}\r\n\r\n",
			wantIncomplete: "",
		},
		{
			name:           "partial boundary with trailing fragment",
			input:          "data: {\"a\":1}\n\ndata: {\"b\":",
			newLen:         1 + len("data: {\"b\":"), // new chunk brought final "\n" + "data: {\"b\":" (14 bytes)
			wantComplete:   "data: {\"a\":1}\n\n",
			wantIncomplete: "data: {\"b\":",
		},
		{
			name:           "mixed boundaries choose latest",
			input:          "data: {\"a\":1}\r\n\r\ndata: {\"b\":2}\n\ntrailing",
			newLen:         len("data: {\"a\":1}\r\n\r\ndata: {\"b\":2}\n\ntrailing"),
			wantComplete:   "data: {\"a\":1}\r\n\r\ndata: {\"b\":2}\n\n",
			wantIncomplete: "trailing",
		},
		{
			name:           "no boundary in new bytes",
			input:          "data: incomplete chunk",
			newLen:         5,
			wantComplete:   "",
			wantIncomplete: "data: incomplete chunk",
		},
		{
			name:           "empty input",
			input:          "",
			newLen:         0,
			wantComplete:   "",
			wantIncomplete: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			complete, incomplete := splitSSEEventsWithNewBytes([]byte(tt.input), tt.newLen)
			if string(complete) != tt.wantComplete {
				t.Fatalf("complete = %q, want %q", string(complete), tt.wantComplete)
			}
			if string(incomplete) != tt.wantIncomplete {
				t.Fatalf("incomplete = %q, want %q", string(incomplete), tt.wantIncomplete)
			}
		})
	}
}

func TestStreamChunkReassemblesSplitToolName(t *testing.T) {
	// THE critical test: tool name "run_command" is split across two TCP chunks.
	// Without event reassembly, regex misses the match on both chunks. With
	// reassembly via the session tail buffer, chunk 1 is held back, combined
	// with chunk 2 into a complete SSE event, then uncloaked.
	mgr := newStreamSessionManager()
	const reqID = "split-chunk-reassembly"
	reqBody := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}],"messages":[]}`
	// The reverse must come from pinned request authority: the request-side alias
	// plan pattern is cached on the stream session (a body alone cannot prove the
	// per-request mapping, Issue #39).
	mgr.resetSession("req:"+reqID, "claude_code", requestScopedUncloakPattern("claude_code", []byte(reqBody), "openai"), 1)

	// Chunk 1: incomplete event — tool name cut at "run_c"
	resp1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		SourceFormat: "openai",
		ChunkIndex:   0,
		Body:         []byte(`data: {"type": "tool_use", "id": "123", "name": "run_c`),
	}, "openai")
	if !resp1.DropChunk {
		t.Fatal("chunk 1 should have been buffered as an incomplete event")
	}

	// Chunk 2 completes the event
	resp2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		SourceFormat: "openai",
		ChunkIndex:   1,
		Body:         []byte("ommand\", \"input\": {}}\n\n"),
	}, "openai")

	resultStr := string(resp2.Body)
	if !strings.Contains(resultStr, `"name": "Bash"`) && !strings.Contains(resultStr, `"name":"Bash"`) {
		t.Fatalf("tool name not uncloaked after reassembly: %s", resultStr)
	}
	if strings.Contains(resultStr, "run_command") {
		t.Fatalf("cloaked name 'run_command' leaked through: %s", resultStr)
	}
}

func TestSessionKeyFallsBackToBodyHash(t *testing.T) {
	// Legacy schema (< 3) streams repeat the request body on every payload
	// chunk, so its FNV hash identifies the stream even without RequestID or
	// metadata. Schema >= 3 payload chunks carry neither, yielding no key.
	m := newStreamSessionManager()
	reqA := &pluginapi.StreamChunkInterceptRequest{ChunkIndex: 0, OriginalRequest: []byte(`{"a":1}`)}
	reqB := &pluginapi.StreamChunkInterceptRequest{ChunkIndex: 0, OriginalRequest: []byte(`{"b":2}`)}
	keyA, keyB := m.sessionKey(reqA), m.sessionKey(reqB)
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Fatalf("expected distinct non-empty body-hash keys, got %q vs %q", keyA, keyB)
	}
	if m.sessionKey(reqA) != keyA {
		t.Fatal("expected a stable hash key for identical request bodies")
	}
	bodyless := &pluginapi.StreamChunkInterceptRequest{ChunkIndex: 0}
	if key := m.sessionKey(bodyless); key != "" {
		t.Fatalf("expected no key for a schema >= 3 payload chunk without identifiers, got %q", key)
	}
}

// cloakedCodexRequest is the executed (post-cloak) form of a code-mode Codex
// request: every declared Codex source name already replaced by its AGY target.
// That is the body the host republishes downstream, because it only records the
// executed payload after request.intercept_before rewrote it.
const cloakedCodexRequest = `{"tools":[{"type":"function","function":{"name":"run_command"}},{"type":"function","function":{"name":"search_web"}},{"type":"function","function":{"name":"ask_question"}},{"type":"function","function":{"name":"invoke_subagent"}}],"messages":[]}`

func TestStreamFallbackDoesNotGuessFromAlreadyCloakedCodexStream(t *testing.T) {
	// Schema < 3 repeats the request body on every payload chunk, and that body is
	// already cloaked by the time the stream interceptor sees it. There is no
	// RequestID, no marker and no UA evidence, so the declared names are unknowable:
	// cloak TARGETS are not pinned reverse authority (Issue #39) and the chunk must
	// pass through unmutated instead of being statically reversed.
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	chunkBody := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"

	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		SourceFormat:    "openai",
		OriginalRequest: []byte(cloakedCodexRequest),
		RequestBody:     []byte(cloakedCodexRequest),
		ChunkIndex:      0,
		Body:            []byte(chunkBody),
	}, "openai")

	if len(resp.Body) != 0 {
		t.Fatalf("executed stream body without pinned authority must pass through unmutated, got %q", string(resp.Body))
	}
}

// buildTestCloakPatterns creates a cachedCloakPatterns for testing,
// mirroring the logic in rebuildCachedRegexes.
func buildTestCloakPatterns(cloakTable map[string]string) *cachedCloakPatterns {
	cp := &cachedCloakPatterns{
		cloakTable: cloakTable,
		identRe:    buildCloakIdentRe(cloakTable),
		ambigRe:    buildCloakAmbiguousRe(cloakTable),
	}
	return cp
}

// buildTestUncloakPattern creates a cachedUncloakPattern for testing.
func buildTestUncloakPattern(uncloakTable map[string]string) *cachedUncloakPattern {
	targets := make([]string, 0, len(uncloakTable))
	for target := range uncloakTable {
		targets = append(targets, regexp.QuoteMeta(target))
	}
	pattern := `"name"\s*:\s*"((?:[a-zA-Z0-9_-]+:)?(?:` + strings.Join(targets, "|") + `))"`
	re := regexp.MustCompile(pattern)
	return &cachedUncloakPattern{re: re, lookup: uncloakTable}
}

func TestModelAllowsCloakEmptyPrefixesAllowsAll(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Default config has no model prefixes → cloak runs for every model.
	if !modelAllowsCloak("grok-build-0.1", "grok-build-0.1") {
		t.Fatal("empty prefixes should allow all models")
	}
	if !modelAllowsCloak("", "") {
		t.Fatal("empty prefixes should allow even empty model names")
	}
}

func TestModelAllowsCloakWithPrefixes(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	cfg := defaultFilterConfig()
	cfg.ModelPrefixes = []string{"agy/"}
	applyFilterConfig(cfg)

	tests := []struct {
		name           string
		model          string
		requestedModel string
		want           bool
	}{
		{"upstream model matches", "agy/gemini-3-flash-agent", "", true},
		{"requested model matches", "", "agy/gemini-3-flash", true},
		{"either side matches", "gemini-3-flash", "agy/gemini-3-flash", true},
		{"non-antigravity model", "grok-build-0.1", "grok-build-0.1", false},
		{"both empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelAllowsCloak(tt.model, tt.requestedModel); got != tt.want {
				t.Fatalf("modelAllowsCloak(%q,%q) = %t, want %t", tt.model, tt.requestedModel, got, tt.want)
			}
		})
	}
}

func TestParseModelPrefixes(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		want    []string
		wantErr bool
	}{
		{"array of strings", []any{"agy/", "antigravity/"}, []string{"agy/", "antigravity/"}, false},
		{"comma separated string", "agy/, antigravity/", []string{"agy/", "antigravity/"}, false},
		{"newline separated string", "agy/\nantigravity/", []string{"agy/", "antigravity/"}, false},
		{"nil", nil, nil, false},
		{"non-string entry", []any{"agy/", 5}, nil, true},
		{"wrong type", 42, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseModelPrefixes(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result %v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestParseFilterConfigYAMLModelPrefixes(t *testing.T) {
	cfg, err := parseFilterConfigYAML([]byte("model_prefixes:\n  - agy/\n  - antigravity/\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.ModelPrefixes) != 2 || cfg.ModelPrefixes[0] != "agy/" || cfg.ModelPrefixes[1] != "antigravity/" {
		t.Fatalf("ModelPrefixes = %v, want [agy/ antigravity/]", cfg.ModelPrefixes)
	}
}

func TestBuiltInBrandTablesCoverEachClientsOwnIdentity(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	applyFilterConfig(defaultFilterConfig())

	// One table per client, driven with that client as the resolved client: a
	// rule belongs to the client whose identity it is, and rewriting a keyword
	// under a different client must leave it alone (asserted separately).
	for client, mappings := range brandMappingsByClient {
		for _, mapping := range mappings {
			keyword := mapping.Match
			t.Run(client+"/"+keyword, func(t *testing.T) {
				// strconv.Quote, not raw concatenation: a keyword containing a
				// backslash would otherwise emit an invalid JSON escape and the body
				// would fail to parse, silently passing a rule that never fired.
				body := `{"system":` + strconv.Quote("You are running with "+keyword+" in this environment.") + `}`
				got, rewritten, _ := rewriteRequestBodyWithClient([]byte(body), "openai", client)
				if !rewritten {
					t.Fatalf("keyword %q was not rewritten for client %q", keyword, client)
				}
				// Assert the configured replacement landed verbatim rather than only
				// that "Antigravity" appears: some keywords deliberately map onto a
				// different surface (a vendor phrase, or a model id that must name a
				// route the gateway really serves).
				want := `{"system":` + strconv.Quote("You are running with "+mapping.Replacement+" in this environment.") + `}`
				if string(got) != want {
					t.Fatalf("keyword %q rewrote to\n  got  %s\n  want %s", keyword, got, want)
				}
			})
		}
	}
}

func TestBrandTableIsScopedToTheResolvedClient(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	applyFilterConfig(defaultFilterConfig())

	// Each pair is a word that belongs to one client and a client that does not
	// own it. The body must come back untouched: a target may only be produced
	// by the client that owns it, or the reverse pass cannot invert it.
	for _, tc := range []struct{ keyword, foreignClient string }{
		{"Claude Code", "codex"},
		{"Anthropic SDK", "codex"},
		{"Claude Code", "oh_my_pi"},
		{"OpenAI Codex", "claude_code"},
		{"Codex", "claude_code"},
		{"Oh My Pi", "claude_code"},
		{"omp", "codex"},
	} {
		t.Run(tc.keyword+"-under-"+tc.foreignClient, func(t *testing.T) {
			body := `{"system":` + strconv.Quote("You are running with "+tc.keyword+" in this environment.") + `}`
			got, rewritten, _ := rewriteRequestBodyWithClient([]byte(body), "openai", tc.foreignClient)
			if rewritten {
				t.Fatalf("keyword %q was rewritten under client %q:\n  %s", tc.keyword, tc.foreignClient, got)
			}
			// The rewrite helper returns a nil body when nothing changed, so an
			// untouched body arrives as nil rather than as the original bytes.
			if got != nil && string(got) != body {
				t.Fatalf("body mutated under client %q:\n  got  %s\n  want %s", tc.foreignClient, got, body)
			}
		})
	}
}

func TestRewriteRequestBodyCloaksOhMyPiTools(t *testing.T) {
	// OpenAI format with Oh My Pi tools
	body := `{
		"system":"Helpful, trusted assistant for load-bearing changes in Oh My Pi coding harness.",
		"tools":[
			{"type":"function","function":{"name":"read","description":"Read files, directories, and web URLs"}},
			{"type":"function","function":{"name":"write","description":"Creates or overwrites file at specified path"}},
			{"type":"function","function":{"name":"edit","description":"Line-anchored patch language"}},
			{"type":"function","function":{"name":"bash","description":"Runs commands in persistent shell"}},
			{"type":"function","function":{"name":"grep","description":"Searches files with regex"}},
			{"type":"function","function":{"name":"glob","description":"Globs files and directories"}},
			{"type":"function","function":{"name":"task","description":"Delegate work to subagents"}},
			{"type":"function","function":{"name":"ask","description":"Ask user for clarification"}},
			{"type":"function","function":{"name":"todo","description":"Manage tasks"}},
			{"type":"function","function":{"name":"hub","description":"Agent coordination and messaging"}},
			{"type":"function","function":{"name":"eval","description":"Run code in persistent kernel"}},
			{"type":"function","function":{"name":"web_search","description":"Web search beyond knowledge cutoff"}}
		],
		"messages":[
			{"role":"system","content":"Use bash for short pipelines and read for files."},
			{"role":"user","content":"inspect the repo"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"main.go\"}"}}]},
			{"role":"tool","name":"read","content":"package main\n"}
		],
		"tool_choice":{"type":"function","function":{"name":"bash"}}
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("want rewritten = true for Oh My Pi request")
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Assert system prompt rewritten
	sys := parsed["system"].(string)
	if !strings.Contains(sys, "Antigravity") || strings.Contains(sys, "Oh My Pi") {
		t.Errorf("system prompt = %q, want 'Oh My Pi' replaced with 'Antigravity'", sys)
	}

	// Assert tools cloaked according to the canonical Safe Mapping Set
	toolsRaw := parsed["tools"].([]any)
	expectedMap := map[string]string{
		"read":       "view_file",
		"write":      "write_to_file",
		"edit":       "replace_file_content",
		"bash":       "run_command",
		"grep":       "grep_search",
		"glob":       "find_by_name",
		"task":       "invoke_subagent",
		"ask":        "ask_question",
		"web_search": "search_web",
	}
	cloakedNames := make(map[string]bool, len(toolsRaw))
	for _, tr := range toolsRaw {
		fn := tr.(map[string]any)["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			cloakedNames[name] = true
		}
	}
	for orig, want := range expectedMap {
		if !cloakedNames[want] {
			t.Errorf("expected cloaked tool %q (from %q) in tools array", want, orig)
		}
	}
	// Assert removed static mappings pass through unchanged
	passThroughExpected := []string{"todo", "hub", "eval"}
	for _, pt := range passThroughExpected {
		if !cloakedNames[pt] {
			t.Errorf("expected intentional pass-through tool %q in tools array", pt)
		}
	}
	staleTargets := []string{"manage_task", "send_message", "execute_code", "list_dir"}
	for _, st := range staleTargets {
		if cloakedNames[st] {
			t.Errorf("stale target %q should not be present in tools array", st)
		}
	}
	// Assert tool_choice cloaked
	tc := parsed["tool_choice"].(map[string]any)["function"].(map[string]any)
	if tc["name"] != "run_command" {
		t.Errorf("tool_choice name = %q, want run_command", tc["name"])
	}

	// Assert messages tool_calls and tool role cloaked
	msgs := parsed["messages"].([]any)
	// Assistant message tool_calls
	asstMsg := msgs[2].(map[string]any)
	tcs := asstMsg["tool_calls"].([]any)
	tc0 := tcs[0].(map[string]any)["function"].(map[string]any)
	if tc0["name"] != "view_file" {
		t.Errorf("messages[2].tool_calls[0].name = %q, want view_file", tc0["name"])
	}
	// Tool response message name
	toolMsg := msgs[3].(map[string]any)
	if toolMsg["name"] != "view_file" {
		t.Errorf("messages[3].name = %q, want view_file", toolMsg["name"])
	}

	// Assert system message prose cloaking: "Use bash" -> "Use run_command"
	sysMsg := msgs[0].(map[string]any)
	if sysContent := sysMsg["content"].(string); !strings.Contains(sysContent, "run_command") {
		t.Errorf("messages[0].content = %q, want 'run_command'", sysContent)
	}
}

func TestRewriteRequestBodyCloaksOhMyPiToolsAnthropicFormat(t *testing.T) {
	body := `{
		"system":"You are Oh My Pi.",
		"tools":[
			{"name":"read","description":"Read files","input_schema":{"type":"object"}},
			{"name":"bash","description":"Run shell","input_schema":{"type":"object"}},
			{"name":"task","description":"Spawn subagent","input_schema":{"type":"object"}}
		],
		"messages":[
			{
				"role":"assistant",
				"content":[{"type":"tool_use","id":"tool_1","name":"read","input":{"path":"a.go"}}]
			}
		]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "anthropic")
	if !rewritten {
		t.Fatal("want rewritten = true")
	}
	var parsed map[string]any
	json.Unmarshal(got, &parsed)

	toolsRaw := parsed["tools"].([]any)
	t0 := toolsRaw[0].(map[string]any)
	if t0["name"] != "view_file" {
		t.Errorf("tools[0].name = %q, want view_file", t0["name"])
	}
	t1 := toolsRaw[1].(map[string]any)
	if t1["name"] != "run_command" {
		t.Errorf("tools[1].name = %q, want run_command", t1["name"])
	}
	t2 := toolsRaw[2].(map[string]any)
	if t2["name"] != "invoke_subagent" {
		t.Errorf("tools[2].name = %q, want invoke_subagent", t2["name"])
	}

	msgs := parsed["messages"].([]any)
	cnt := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if cnt["name"] != "view_file" {
		t.Errorf("content[0].name = %q, want view_file", cnt["name"])
	}
}

func TestUncloakResponseBodyOhMyPi(t *testing.T) {
	uncloakTable := defaultUncloakTables["oh_my_pi"]

	// OpenAI shape
	respOpenAI := []byte(`{
		"choices":[{
			"message":{
				"role":"assistant",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_command","arguments":"{}"}}]
			}
		}]
	}`)
	out, changed := uncloakResponseBody(respOpenAI, uncloakTable, "openai")
	if !changed {
		t.Fatal("want changed = true")
	}
	if !strings.Contains(string(out), `"name":"bash"`) {
		t.Fatalf("output does not contain uncloaked name 'bash': %s", out)
	}

	// Anthropic shape
	respAnthropic := []byte(`{
		"content":[{"type":"tool_use","id":"tool_1","name":"view_file","input":{}}]
	}`)
	outAnth, changedAnth := uncloakResponseBody(respAnthropic, uncloakTable, "anthropic")
	if !changedAnth {
		t.Fatal("want changedAnth = true")
	}
	if !strings.Contains(string(outAnth), `"name":"read"`) {
		t.Fatalf("output does not contain uncloaked name 'read': %s", outAnth)
	}
}

func TestUncloakStreamChunkOhMyPi(t *testing.T) {
	cfg := defaultFilterConfig()
	cached := cfg.uncloakRegexCache["oh_my_pi"]
	if cached == nil {
		t.Fatal("cached uncloak pattern for oh_my_pi is nil")
	}

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"run_command\"}}]}}]}\n\n")
	out, changed := uncloakStreamChunk(chunk, cached)
	if !changed {
		t.Fatal("want changed = true for stream chunk")
	}
	if !strings.Contains(string(out), `"name":"bash"`) {
		t.Fatalf("uncloaked chunk missing 'bash': %s", out)
	}
}

func TestDetectClientOhMyPi(t *testing.T) {
	toolNames := []string{"read", "bash", "edit", "write", "grep", "glob", "task", "ask", "todo", "hub", "eval", "web_search"}
	client := detectClient(toolNames)
	if client != "oh_my_pi" {
		t.Fatalf("detectClient(%v) = %q, want 'oh_my_pi'", toolNames, client)
	}

	// Cloaked tool names detection: target names alone are never sufficient
	// to attribute to oh_my_pi without an independent signal.
	cloakedTargets := []string{"view_file", "run_command", "replace_file_content", "write_to_file", "grep_search", "find_by_name", "invoke_subagent", "ask_question", "search_web"}
	cloakedClient := detectCloakedClient(cloakedTargets)
	if cloakedClient == "oh_my_pi" {
		t.Fatalf("detectCloakedClient(%v) = %q, want non-oh_my_pi for standalone targets", cloakedTargets, cloakedClient)
	}
	// When independent attribution is present, corroboration succeeds.
	if got := detectCloakedClientWithSignal(cloakedTargets, true); got != "oh_my_pi" {
		t.Fatalf("detectCloakedClientWithSignal(%v, true) = %q, want 'oh_my_pi'", cloakedTargets, got)
	}
}

func TestParseToolMappingsOhMyPiAliases(t *testing.T) {
	raw := map[string]any{
		"omp": map[string]any{
			"custom_tool": "custom_target",
		},
	}
	parsed, err := parseToolMappings(raw)
	if err != nil {
		t.Fatalf("parseToolMappings err: %v", err)
	}
	if parsed["oh_my_pi"]["custom_tool"] != "custom_target" {
		t.Fatalf("parsed mapping = %v, want oh_my_pi.custom_tool = custom_target", parsed)
	}
}

func TestProtectedOMP_ConfigValidation(t *testing.T) {
	// 1. Valid noncanonical custom mapping: accepted and honored
	validYAML := []byte(`
tool_mappings:
  omp:
    custom_tool: "custom_target"
    my_extra: "wp_extra"
`)
	cfg, err := parseFilterConfigYAML(validYAML)
	if err != nil {
		t.Fatalf("expected valid noncanonical mapping to be accepted, got error: %v", err)
	}
	if cfg.ToolMappings["oh_my_pi"]["custom_tool"] != "custom_target" || cfg.ToolMappings["oh_my_pi"]["my_extra"] != "wp_extra" {
		t.Fatalf("valid noncanonical mappings not stored in cfg: %v", cfg.ToolMappings["oh_my_pi"])
	}

	// 2. Reject mutating canonical nine mapping
	mutatingYAML := []byte(`
tool_mappings:
  omp:
    read: "custom_read"
`)
	if _, err := parseFilterConfigYAML(mutatingYAML); err == nil {
		t.Fatal("expected mutating canonical mapping 'read' -> 'custom_read' to be rejected")
	}

	// 3. Reject non-injective mapping: custom tool mapping to canonical target
	nonInjectiveCanonicalYAML := []byte(`
tool_mappings:
  omp:
    my_read: "view_file"
`)
	if _, err := parseFilterConfigYAML(nonInjectiveCanonicalYAML); err == nil {
		t.Fatal("expected custom tool mapping to canonical target 'view_file' to be rejected")
	}

	// 4. Reject non-injective mapping: duplicate custom targets
	duplicateTargetYAML := []byte(`
tool_mappings:
  omp:
    custom_a: "wp_tool"
    custom_b: "wp_tool"
`)
	if _, err := parseFilterConfigYAML(duplicateTargetYAML); err == nil {
		t.Fatal("expected duplicate custom mapping targets to be rejected")
	}

	// 4b. Reject non-injective mapping: custom tool mapping to shared alias target (e.g. wp_todo)
	sharedCollisionYAML := []byte(`
tool_mappings:
  omp:
    custom_tool: "wp_todo"
`)
	if _, err := parseFilterConfigYAML(sharedCollisionYAML); err == nil {
		t.Fatal("expected custom tool mapping to shared alias target 'wp_todo' to be rejected at config time")
	} else if !strings.Contains(err.Error(), `owned by "todo"`) {
		t.Fatalf("expected error mentioning owned by \"todo\", got: %v", err)
	}

	// 4c. Re-declaring the same shared mapping (todo -> wp_todo) is accepted
	sharedIdentityYAML := []byte(`
tool_mappings:
  omp:
    todo: "wp_todo"
`)
	if _, err := parseFilterConfigYAML(sharedIdentityYAML); err != nil {
		t.Fatalf("expected identity shared alias mapping to be accepted, got error: %v", err)
	}

	// 5. Reject forbidden target naming: leading underscore
	forbiddenUnderscoreYAML := []byte(`
tool_mappings:
  omp:
    custom_tool: "_reserved_target"
`)
	if _, err := parseFilterConfigYAML(forbiddenUnderscoreYAML); err == nil {
		t.Fatal("expected target with leading underscore to be rejected")
	}

	// 6. Reject forbidden target naming: whitespace (internal, trailing, leading, tab)
	whitespaceTargets := []struct {
		name   string
		target string
	}{
		{"internal_space", "has space"},
		{"trailing_space", "view_file "},
		{"leading_space", " view_file"},
		{"tab", "view\tfile"},
		{"trailing_tab", "custom_target\t"},
	}
	for _, tc := range whitespaceTargets {
		yaml := []byte(fmt.Sprintf("\ntool_mappings:\n  omp:\n    custom_tool: %q\n", tc.target))
		if _, err := parseFilterConfigYAML(yaml); err == nil {
			t.Fatalf("expected target with %s %q to be rejected at config parse", tc.name, tc.target)
		} else if !strings.Contains(err.Error(), "contains whitespace") {
			t.Fatalf("expected error mentioning whitespace for %s, got: %v", tc.name, err)
		}

		rawResp, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, yaml))
		if code != 0 {
			t.Fatalf("reconfigure code = %d for %s", code, tc.name)
		}
		var env struct {
			OK    bool `json:"ok"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		mustUnmarshalJSON(t, rawResp, &env)
		if env.OK || env.Error.Code != "invalid_config" || !strings.Contains(env.Error.Message, "whitespace") {
			t.Fatalf("expected visible invalid_config whitespace envelope at reconfigure time for %s, got: %s", tc.name, rawResp)
		}
	}

	// 7. Reject empty target
	emptyTargetYAML := []byte(`
tool_mappings:
  omp:
    custom_tool: ""
`)
	if _, err := parseFilterConfigYAML(emptyTargetYAML); err == nil {
		t.Fatal("expected empty target to be rejected")
	}

	// 8. Reconfigure lifecycle rejects invalid config visibly
	rawResp, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, mutatingYAML))
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	mustUnmarshalJSON(t, rawResp, &env)
	if env.OK || env.Error.Code != "invalid_config" || !strings.Contains(env.Error.Message, "immutable") {
		t.Fatalf("expected visible invalid_config envelope at reconfigure time, got: %s", rawResp)
	}
}

func TestNormalizeClientKeyAliases(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"omp", "oh_my_pi"},
		{"oh-my-pi", "oh_my_pi"},
		{"OH-MY-PI", "oh_my_pi"},
		{"  Claude_Code  ", "claude_code"},
		{"codex", "codex"},
	}
	for _, tt := range tests {
		if got := normalizeClientKey(tt.in); got != tt.want {
			t.Fatalf("normalizeClientKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDetectCloakedClientTieRules(t *testing.T) {
	// Spec: candidates tying at FULL target coverage are indistinguishable
	// (native Antigravity serving every table) and cloaking is skipped; ties
	// below full confidence resolve deterministically instead.
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings: map[string]map[string]string{
			"c1": {"A": "tA", "B": "tB", "C": "tC", "D": "tD", "E": "tE"},
			"c2": {"A": "tA", "B": "tB", "C": "tC", "D": "tD", "F": "tF"},
		},
	})
	defer restoreDefaultFilterConfig(t)

	// Partial tie: both clients hit 4/5 targets ({tA..tD}) — neither reaches
	// full confidence, so detection resolves rather than skips.
	partialNames := []string{"tA", "tB", "tC", "tD"}
	first := detectCloakedClient(partialNames)
	if first != "c1" && first != "c2" {
		t.Fatalf("partial tie should resolve to a ranked winner, got %q", first)
	}
	for range 10 {
		if again := detectCloakedClient(partialNames); again != first {
			t.Fatalf("detection not deterministic: %q then %q", first, again)
		}
	}

	// Full-coverage tie: every cloak target of both clients is present.
	fullNames := []string{"tA", "tB", "tC", "tD", "tE", "tF"}
	if got := detectCloakedClient(fullNames); got != "" {
		t.Fatalf("full-coverage tie should skip cloaking, got %q", got)
	}
}

func TestMCPPassthroughBothDirections(t *testing.T) {
	// Spec story 29: MCP tools (mcp__*) must pass through unmolested in both
	// directions while surrounding client tools are cloaked/restored.
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	})
	defer restoreDefaultFilterConfig(t)

	// Request side (OpenAI shape): omp detection needs a distinctive harness
	// tool ("hub"), mcp__github__create_issue must survive verbatim.
	body, changed := rewriteRequestBody([]byte(`{"system":"This is omp coding.","tools":[`+
		`{"type":"function","function":{"name":"bash","description":"run shell"}},`+
		`{"type":"function","function":{"name":"hub","description":"send message"}},`+
		`{"type":"function","function":{"name":"mcp__github__create_issue","description":"create issue"}}],"messages":[]}`), "openai")
	if !changed {
		t.Fatal("expected OpenAI request body to be rewritten")
	}
	out := string(body)
	if strings.Contains(out, `"name":"bash"`) {
		t.Fatalf("expected bash cloaked to run_command: %s", out)
	}
	if !strings.Contains(out, "run_command") {
		t.Fatalf("expected cloaked run_command in body: %s", out)
	}
	if !strings.Contains(out, `"name":"mcp__github__create_issue"`) {
		t.Fatalf("MCP tool name was molested on the way up: %s", out)
	}

	// Request side (Anthropic shape).
	anthBody, anthChanged := rewriteRequestBody([]byte(`{"system":"Oh My Pi agent","tools":[`+
		`{"name":"write","description":"write file"},`+
		`{"name":"mcp__jira__search","description":"search jira"},`+
		`{"name":"hub","description":"send message"}],"messages":[]}`), "anthropic")
	if !anthChanged {
		t.Fatal("expected Anthropic request body to be rewritten")
	}
	anthOut := string(anthBody)
	if !strings.Contains(anthOut, `"name":"write_to_file"`) {
		t.Fatalf("expected omp write cloaked in Anthropic body: %s", anthOut)
	}
	if !strings.Contains(anthOut, `"name":"mcp__jira__search"`) {
		t.Fatalf("MCP tool name was molested in Anthropic body: %s", anthOut)
	}

	// Response side (Anthropic shape): MCP tool_use round-trips untouched
	// while native Antigravity names restore.
	modified, respChanged := uncloakResponseBody(
		[]byte(`{"content":[{"type":"tool_use","id":"t1","name":"view_file","input":{}},`+
			`{"type":"tool_use","id":"t2","name":"mcp__github__list_issues","input":{}}]}`),
		defaultUncloakTables["oh_my_pi"], "anthropic")
	if !respChanged {
		t.Fatal("expected Anthropic response body to be uncloaked")
	}
	if respOut := string(modified); !strings.Contains(respOut, "mcp__github__list_issues") {
		t.Fatalf("MCP tool name was molested on the way back: %s", respOut)
	}

	// Stream side (OpenAI shape): MCP tool name survives uncloak in SSE chunks.
	cached := defaultFilterConfig().uncloakRegexCache["oh_my_pi"]
	if cached == nil {
		t.Fatal("cached uncloak pattern for oh_my_pi is nil")
	}
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"function\":{\"name\":\"run_command\"}}," +
		"{\"index\":1,\"function\":{\"name\":\"mcp__fs__read_file\"}}]}}]}\n\n")
	streamOut, changedStream := uncloakStreamChunk(chunk, cached)
	if !changedStream {
		t.Fatal("expected stream chunk to be uncloaked")
	}
	streamStr := string(streamOut)
	if !strings.Contains(streamStr, `"name":"bash"`) {
		t.Fatalf("expected run_command restored to bash: %s", streamStr)
	}
	if !strings.Contains(streamStr, `"name":"mcp__fs__read_file"`) {
		t.Fatalf("MCP tool name was molested in stream chunk: %s", streamStr)
	}
}

func TestStreamUncloakAnthropicSSE(t *testing.T) {
	// Anthropic-format streaming: content_block_start carries the tool_use
	// name; here it is split mid-name across TCP chunks to exercise Anthropic
	// shaping AND event reassembly together, plus CRLF event boundaries.
	mgr := newStreamSessionManager()
	const reqID = "anthropic-sse-cloak"
	reqBody := `{"tools":[{"name":"write","description":"w"},{"name":"read","description":"r"},{"name":"task","description":"t"}],"messages":[]}`
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    "anthropic",
		OriginalRequest: []byte(reqBody),
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}, "anthropic")

	// Chunk 1 ends mid-tool-name inside an incomplete CRLF-delimited event.
	resp1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		SourceFormat: "anthropic",
		ChunkIndex:   0,
		Body: []byte("event: content_block_start\r\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"view_f`),
	}, "anthropic")
	if !resp1.DropChunk {
		t.Fatal("first Anthropic chunk should be buffered as an incomplete event")
	}

	// Chunk 2 completes the event plus more events, ending the stream.
	resp2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		SourceFormat: "anthropic",
		ChunkIndex:   1,
		Body: []byte("ile\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"file_path\\\":\\\"/tmp/x\\\"}\"}}\n\n" +
			"data: [DONE]\n\n"),
	}, "anthropic")

	out := string(resp2.Body)
	if strings.Contains(out, "view_file") {
		t.Fatalf("cloaked name 'view_file' leaked through: %s", out)
	}
	if !strings.Contains(out, `"name":"read"`) && !strings.Contains(out, `"name": "read"`) {
		t.Fatalf("expected view_file restored to read in Anthropic stream: %s", out)
	}

	mgr.mu.Lock()
	_, alive := mgr.sessions["req:"+reqID]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session should be deleted once the stream sends [DONE]")
	}
}

func TestReplaceInsensitiveWordBoundaries(t *testing.T) {
	// "omp" alias should only match standalone word, not inside "prompt", "complete", "computer"
	input := "Please complete the prompt using computer and omp."
	got, changed := replaceInsensitive(input, "omp", "Antigravity")
	if !changed {
		t.Fatal("expected changed = true for standalone 'omp'")
	}
	want := "Please complete the prompt using computer and Antigravity."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Pure substring matches without word boundary must not change
	corruptInput := "This is a prompt with complete components."
	noChangeGot, noChanged := replaceInsensitive(corruptInput, "omp", "Antigravity")
	if noChanged {
		t.Fatalf("corrupted prose: got %q", noChangeGot)
	}
}

func TestReplaceBrandKeywordRemapsPathSegments(t *testing.T) {
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	})
	defer restoreDefaultFilterConfig(t)

	// Every supported client's home directory is an operational identifier, so
	// it is remapped onto the Antigravity equivalent instead of the brand word:
	// a dead .Antigravity path is worse than a path that exists upstream.
	for _, tc := range []struct {
		name   string
		client string
		in     string
		want   string
	}{
		{"claude unix", "claude_code", `{"system":"at /home/u/.claude/scheduled_tasks.json"}`, "/home/u/.gemini/scheduled_tasks.json"},
		{"claude windows", "claude_code", `{"system":"at C:\\Users\\u\\.claude\\settings.json"}`, `C:\\Users\\u\\.gemini\\settings.json`},
		{"codex unix", "codex", `{"system":"at /home/u/.codex/config.toml"}`, "/home/u/.gemini/config.toml"},
		{"codex windows", "codex", `{"system":"at C:\\Users\\u\\.codex\\config.toml"}`, `C:\\Users\\u\\.gemini\\config.toml`},
		{"omp unix", "oh_my_pi", `{"system":"at /home/u/.omp/agent"}`, "/home/u/.gemini/agent"},
		{"omp windows", "oh_my_pi", `{"system":"at C:\\Users\\u\\.omp\\agent"}`, `C:\\Users\\u\\.gemini\\agent`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, _ := rewriteRequestBodyWithClient([]byte(tc.in), "openai", tc.client)
			if !changed {
				t.Fatalf("path was not rewritten: %s", got)
			}
			if !strings.Contains(string(got), tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}

	// systemAfter runs a request through the real forward pass and returns the
	// system text the upstream would receive, whether or not anything changed.
	systemAfter := func(t *testing.T, client, in string) string {
		t.Helper()
		got, changed, _ := rewriteRequestBodyWithClient([]byte(`{"system":"`+in+`"}`), "openai", client)
		if !changed {
			return in
		}
		return string(got)
	}

	// Bare brand mentions are still masked.
	b, bc, _ := rewriteRequestBodyWithClient([]byte(`{"system":"You are omp."}`), "openai", "oh_my_pi")
	if !bc || !strings.Contains(string(b), "Antigravity.") {
		t.Fatalf("bare omp brand must still be masked: changed=%v body=%s", bc, b)
	}

	// A URL or path is only ever rewritten for a literal dot-prefixed directory
	// segment. "/omp/" and "\omp\" are ordinary path names with nothing to do
	// with any coding agent, so they are left byte-for-byte alone - masking them
	// produced a path that existed neither upstream nor on the way back.
	if got := systemAfter(t, "oh_my_pi", "binary at /omp/agent"); got != "binary at /omp/agent" {
		t.Fatalf("/omp/ must never be rewritten: %q", got)
	}
	if got := systemAfter(t, "oh_my_pi", `binary at C:\\omp\\agent`); got != `binary at C:\\omp\\agent` {
		t.Fatalf(`\omp\ must never be rewritten: %q`, got)
	}

	// The same rule holds for every other coding agent's name, not just Oh My Pi.
	for _, tc := range []struct{ client, in, keep string }{
		{"claude_code", `{"system":"srv at /claude/agent"}`, "/claude/agent"},
		{"codex", `{"system":"srv at /codex/agent"}`, "/codex/agent"},
	} {
		if got := systemAfter(t, tc.client, strings.TrimSuffix(strings.TrimPrefix(tc.in, `{"system":"`), `"}`)); got != strings.TrimSuffix(strings.TrimPrefix(tc.in, `{"system":"`), `"}`) {
			t.Fatalf("%s: %q must never be rewritten: %q", tc.client, tc.keep, got)
		}
	}

	// A composed mapping keeps its own deliberate meaning wherever it appears,
	// so the dot-prefixed "oh-my-pi" is still masked even though the bare vendor
	// words in a path are not.
	bOther, bcOther, _ := rewriteRequestBodyWithClient([]byte(`{"system":"config at /home/user/.oh-my-pi"}`), "openai", "oh_my_pi")
	if !bcOther || !strings.Contains(string(bOther), "/home/user/.Antigravity") {
		t.Fatalf(".oh-my-pi must still be masked: changed=%v body=%s", bcOther, bOther)
	}

	// A dot-element whose brand is glued to a suffix by a hyphen is a different
	// path element, not the configuration directory.
	if got := systemAfter(t, "oh_my_pi", "config at /home/user/.omp-backup/agent"); got != "config at /home/user/.omp-backup/agent" {
		t.Fatalf(".omp-backup must be left alone: %q", got)
	}
	// A brand glued onto a leading file name is prose, not a segment, and is
	// still masked as before.
	bExtension, bcExtension, _ := rewriteRequestBodyWithClient([]byte(`{"system":"config at /home/user/profile.omp/agent"}`), "openai", "oh_my_pi")
	if !bcExtension || !strings.Contains(string(bExtension), "/home/user/profile.Antigravity/agent") {
		t.Fatalf("profile.omp must be masked: changed=%v body=%s", bcExtension, bExtension)
	}

	// A directory name that merely contains the brand word is an ordinary path.
	// Rewriting it round-tripped "antigravity-cloak" into "omp-cloak" and the
	// client then read a path that did not exist.
	if got := systemAfter(t, "oh_my_pi", "cwd F:/CodeBase/antigravity-cloak/main.go"); got != "cwd F:/CodeBase/antigravity-cloak/main.go" {
		t.Fatalf("antigravity-cloak must be left alone: %q", got)
	}

	// A URL host is prose about a product, not a coding-agent directory, and the
	// composed domain mappings keep working: only bare words are held back.
	bURL, bcURL, _ := rewriteRequestBodyWithClient([]byte(`{"system":"docs at https://claude.ai/docs"}`), "openai", "claude_code")
	if !bcURL || !strings.Contains(string(bURL), "antigravity.google") {
		t.Fatalf("claude.ai must still cloaks: changed=%v body=%s", bcURL, bURL)
	}
	if got := systemAfter(t, "oh_my_pi", "see https://omp.ai/pricing"); got != "see https://omp.ai/pricing" {
		t.Fatalf("bare omp in a URL must be left alone: %q", got)
	}
}

func TestRewriteMasksBareClaudeAndRemapsClaudeHomePath(t *testing.T) {
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	})
	defer restoreDefaultFilterConfig(t)

	// Real strings lifted from a live Claude Code system instruction: the
	// multi-word form must be consumed first, and the bare brand must mask.
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		// Both the main session and the subagent open with an identity line that
		// names the vendor twice; each maps onto the real Antigravity wording.
		{"main identity sentence", `You are Claude Code, Anthropic's official CLI for Claude.`, antigravityIdentity, true},
		{"subagent identity sentence", `You are a Claude agent, built on Anthropic's Claude Agent SDK.`, antigravityIdentity, true},
		{"claude code longer form first", `You are an agent for Claude Code.`, `You are an agent for Antigravity.`, true},
		// Literal substitution does not repair the article: "a Claude" becomes
		// "a Antigravity". Grammar repair is deliberately out of scope.
		{"bare claude prose keeps article", `You are a Claude agent.`, `You are a Antigravity agent.`, true},
		{"claude agent sdk uses official product name", `built on Claude Agent SDK`, `built on Antigravity SDK`, true},
		{"vendor phrase then bare claude", `Anthropic's official CLI for Claude.`, `Google's official CLI for Antigravity.`, true},
		// Model IDs must land on Antigravity routes the gateway really serves, so
		// the bare "Claude" rule cannot consume the vendor prefix first.
		{"opus model id", `Opus 5.5: 'claude-opus-5-5'`, `Opus 5.5: 'gemini-3.1-pro-low'`, true},
		{"fable model id", `Fable 5.1: 'claude-fable-5-1'`, `Fable 5.1: 'gemini-3.1-pro-low'`, true},
		{"sonnet model id", `Sonnet 5: 'claude-sonnet-5'`, `Sonnet 5: 'gemini-3.8-flash'`, true},
		{"haiku model id", `Haiku 4.5: 'claude-haiku-4-5-20251001'`, `Haiku 4.5: 'gemini-3.5-flash-lite'`, true},
		// A model ID the table does not know still loses its vendor prefix.
		{"unknown model id falls through", `'claude-9-9'`, `'Antigravity-9-9'`, true},
		{"url uses official domain", `web app (claude.ai/code)`, `web app (antigravity.google/code)`, true},
		// The client home directory maps onto the Antigravity equivalent so the
		// model is not handed a dead .Antigravity path.
		{"claude home remapped to gemini", `memory at C:\\Users\\monet\\.claude\\projects\\slug\\memory`, `memory at C:\\Users\\monet\\.gemini\\projects\\slug\\memory`, true},
		{"unix claude home remapped", `memory at /home/user/.claude/projects/slug`, `memory at /home/user/.gemini/projects/slug`, true},
		// A path element that merely starts with a dot is still not the
		// configuration directory, so it is left byte-for-byte alone.
		{"claude-backup is not a path segment", `/home/user/.claude-backup/x`, `/home/user/.claude-backup/x`, false},
		// A non-dot path element has nothing to do with the coding agent either.
		{"slash-claude is never rewritten", `/opt/claude/bin`, `/opt/claude/bin`, false},
		{"backslash-claude is never rewritten", `C:\\claude\\bin`, `C:\\claude\\bin`, false},
		// The repo directory name contains the brand word; rewriting it made the
		// model read a path that did not exist.
		{"brand inside a directory name", `cwd F:/CodeBase/antigravity-cloak/main.go`, `cwd F:/CodeBase/antigravity-cloak/main.go`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, changed, _ := rewriteRequestBodyWithClient([]byte(`{"system":`+strconv.Quote(tc.in)+`}`), "anthropic", "claude_code")
			// An unchanged pass returns no body at all, which is exactly the
			// upstream bytes: the original.
			got := tc.in
			if changed {
				got = systemText(t, body)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v (body=%s)", changed, tc.changed, body)
			}
			if got != tc.want {
				t.Fatalf("system = %q, want %q", got, tc.want)
			}
		})
	}

	// A system prompt with no Claude token is left byte-identical.
	if _, changed, _ := rewriteRequestBodyWithClient([]byte(`{"system":"nothing to mask here"}`), "anthropic", "claude_code"); changed {
		t.Fatalf("unrelated system text must not be rewritten")
	}
}

func TestWorkflowRuleIsScopedToClaudeCode(t *testing.T) {
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	})
	defer restoreDefaultFilterConfig(t)

	// The workflow tool name is a client surface, not a brand token: only
	// Claude Code traffic gets it renamed onto Antigravity's own tool. The
	// plural is renamed too, because a word-bounded matcher treats it as a
	// different token and leaving it would keep the client name in the
	// payload. Grammar is not repaired, same policy as the bare-brand rules.
	const in = `Use the Workflow tool. Workflows run in the background.`
	const want = `Use the teamwork_preview_layer tool. teamwork_preview_layer run in the background.`

	body, changed, client := rewriteRequestBodyWithClient([]byte(`{"system":`+strconv.Quote(in)+`}`), "anthropic", "claude_code")
	if client != "claude_code" {
		t.Fatalf("resolved client = %q, want claude_code", client)
	}
	if !changed || systemText(t, body) != want {
		t.Fatalf("claude_code system = %q, want %q", systemText(t, body), want)
	}

	// Another client declaring the same tool name must keep it: renaming here
	// would rewrite a tool name the client actually called.
	other, otherChanged, _ := rewriteRequestBodyWithClient([]byte(`{"system":`+strconv.Quote(in)+`}`), "anthropic", "codex")
	if otherChanged {
		t.Fatalf("codex system must be untouched, got %q", systemText(t, other))
	}
}

func TestClaudeMdInstructionFileIsRemappedInToolDescriptions(t *testing.T) {
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	})
	defer restoreDefaultFilterConfig(t)

	// Both strings were observed leaking upstream on a request that returned
	// 200: the client home directory inside a skill description, and the
	// uppercase instruction file name the case-sensitive catch-all misses.
	body, changed, _ := rewriteRequestBodyWithClient([]byte(`{"system":"x","tools":[{"name":"Bash","description":"Edit ~/CLAUDE.md, rekey ~/.claude/keybindings.json and project .claude/settings.json","input_schema":{"type":"object"}}]}`), "anthropic", "claude_code")
	if !changed {
		t.Fatalf("tool description must be rewritten (body=%s)", body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	desc, _ := doc["tools"].([]any)[0].(map[string]any)["description"].(string)
	for _, leak := range []string{"CLAUDE.md", ".claude/"} {
		if strings.Contains(desc, leak) {
			t.Fatalf("tool description still leaks %q: %s", leak, desc)
		}
	}
	if !strings.Contains(desc, "AGENTS.md") || !strings.Contains(desc, ".gemini/") {
		t.Fatalf("tool description not remapped to Antigravity equivalents: %s", desc)
	}
}

// systemText extracts the system field of a rewritten request body for assertions.
func systemText(t *testing.T, body []byte) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("rewritten body is not JSON: %v (body=%s)", err, body)
	}
	sys, ok := doc["system"].(string)
	if !ok {
		t.Fatalf("system field missing or not a string: %s", body)
	}
	return sys
}

func TestDetectClientOhMyPiRequiresSignatureOrThreshold(t *testing.T) {
	// Generic tools (read, write) alone should NOT trigger Oh My Pi
	if client := detectClient([]string{"read", "write"}); client != "" {
		t.Fatalf("detectClient([read, write]) = %q, want empty string", client)
	}
	if client := detectClient([]string{"read", "write", "edit"}); client != "" {
		t.Fatalf("detectClient([read, write, edit]) = %q, want empty string", client)
	}

	// 4 generic tools should trigger Oh My Pi
	if client := detectClient([]string{"read", "write", "edit", "bash"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([read, write, edit, bash]) = %q, want 'oh_my_pi'", client)
	}

	// 2 tools including a distinctive harness tool should trigger Oh My Pi
	if client := detectClient([]string{"read", "hub"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([read, hub]) = %q, want 'oh_my_pi'", client)
	}
	if client := detectClient([]string{"bash", "task"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([bash, task]) = %q, want 'oh_my_pi'", client)
	}
	if client := detectClient([]string{"read", "vibe_spawn"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([read, vibe_spawn]) = %q, want 'oh_my_pi'", client)
	}
	if client := detectClient([]string{"vibe_spawn", "vibe_send"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([vibe_spawn, vibe_send]) = %q, want 'oh_my_pi'", client)
	}
	if client := detectClient([]string{"read", "init_experiment"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([read, init_experiment]) = %q, want 'oh_my_pi'", client)
	}
	if client := detectClient([]string{"init_experiment", "run_experiment", "log_experiment", "update_notes"}); client != "oh_my_pi" {
		t.Fatalf("detectClient([autoresearch tools]) = %q, want 'oh_my_pi'", client)
	}
}

func TestStreamSessionManagerHeaderInitSchemaV4(t *testing.T) {
	mgr := newStreamSessionManager()
	// In schema_version >= 3 (e.g. v4), OriginalRequest and RequestBody are
	// only provided at ChunkIndex == StreamChunkHeaderInitIndex (-1).
	// Subsequent payload chunks (ChunkIndex >= 0) have OriginalRequest/RequestBody = nil.
	reqID := "stream-session-test-v4"
	reqBodyText := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}},{"type":"function","function":{"name":"Edit"}}],"messages":[]}`
	reqBody := []byte(reqBodyText)
	// Pinned request authority: the alias plan minted at request intercept is the
	// only source of the reverse, so header-init must register the session from it
	// (a body alone cannot prove the per-request mapping, Issue #39).
	ccHeaders := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/claude-model", reqBody, ccHeaders))

	// Step 1: Header-init chunk (ChunkIndex == -1)
	initReq := &pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    "openai",
		OriginalRequest: reqBody,
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}
	resp := mgr.processChunk(initReq, "openai")
	if resp.DropChunk || len(resp.Body) > 0 {
		t.Fatalf("header-init should return empty no-op response, got resp=%v", resp)
	}

	// Step 2: Payload chunk 0 (ChunkIndex == 0, OriginalRequest = nil, RequestBody = nil)
	payloadChunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"
	chunkReq := &pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		SourceFormat: "openai",
		ChunkIndex:   0,
		Body:         []byte(payloadChunk),
	}
	chunkResp := mgr.processChunk(chunkReq, "openai")
	if len(chunkResp.Body) == 0 {
		t.Fatal("payload chunk was not uncloaked via cached session from header-init")
	}
	if !strings.Contains(string(chunkResp.Body), `"name":"Bash"`) && !strings.Contains(string(chunkResp.Body), `"name": "Bash"`) {
		t.Fatalf("expected tool name 'Bash' in uncloaked body, got: %s", string(chunkResp.Body))
	}
}

func TestStreamSessionManagerIsolatesByRequestID(t *testing.T) {
	mgr := newStreamSessionManager()
	reqBody := []byte(`{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}],"messages":[]}`)
	reqIDA := "stream-session-iso-A"
	reqIDB := "stream-session-iso-B"

	// Pinned request authority for both streams: each alias plan carries its own
	// reverse, and header-init must register the session from it (Issue #39).
	ccHeaders := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	for _, id := range []string{reqIDA, reqIDB} {
		handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeIntegrationRequestInterceptPayloadWithHeaders(t, id, "openai", "agy/claude-model", reqBody, ccHeaders))
	}

	// Init session A
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqIDA,
		SourceFormat:    "openai",
		OriginalRequest: reqBody,
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}, "openai")

	// Init session B
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqIDB,
		SourceFormat:    "openai",
		OriginalRequest: reqBody,
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}, "openai")

	// Send split chunk to Stream A: "data: {\"name\": \"run_c" (incomplete)
	respA1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqIDA,
		SourceFormat: "openai",
		ChunkIndex:   0,
		Body:         []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_c"),
	}, "openai")

	if !respA1.DropChunk {
		t.Fatalf("Stream A chunk 1 should be buffered and dropped, got drop=%t", respA1.DropChunk)
	}

	// Send complete chunk to Stream B — should NOT be affected by Stream A's buffered tail
	completeB := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"
	respB := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqIDB,
		SourceFormat: "openai",
		ChunkIndex:   0,
		Body:         []byte(completeB),
	}, "openai")

	if respB.DropChunk {
		t.Fatal("Stream B chunk should be processed immediately")
	}
	if !strings.Contains(string(respB.Body), "Bash") {
		t.Fatalf("Stream B should be uncloaked to Bash, got: %s", string(respB.Body))
	}

	// Complete Stream A with second chunk
	respA2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqIDA,
		SourceFormat: "openai",
		ChunkIndex:   1,
		Body:         []byte("ommand\"}}]}}]}\n\n"),
	}, "openai")

	if !strings.Contains(string(respA2.Body), "Bash") {
		t.Fatalf("Stream A should be uncloaked to Bash after reassembly, got: %s", string(respA2.Body))
	}
}

func TestAtomicFilterConfigConcurrentSafety(t *testing.T) {
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// 8 readers
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cfg := activeFilterConfig()
					if cfg == nil {
						t.Errorf("activeFilterConfig returned nil")
						return
					}
					_ = cfg.ToolMappings
					_ = cfg.ModelPrefixes
					_ = cfg.UseDefaultKeywords
				}
			}
		}()
	}

	// 1 writer
	for i := range 50 {
		applyFilterConfig(filterConfig{
			UseDefaultKeywords: i%2 == 0,
			ToolMappings:       copyToolMappings(defaultCloakTables),
			ModelPrefixes:      []string{"agy/", "antigravity/"},
		})
	}

	close(stop)
	wg.Wait()
	restoreDefaultFilterConfig(t)
}

func TestEffectiveMappingsCustomOverride(t *testing.T) {
	cfg := filterConfig{
		UseDefaultKeywords: true,
		CustomMappings: []rewriteMapping{
			{Match: "Codex", Replacement: "MyBrand"},
		},
		ToolMappings: copyToolMappings(defaultCloakTables),
	}
	applyFilterConfig(cfg)
	defer restoreDefaultFilterConfig(t)

	body := []byte(`{"system":"This is Codex testing"}`)
	rewritten, changed, _ := rewriteRequestBodyWithClient(body, "openai", "codex")
	if !changed {
		t.Fatalf("expected changed = true")
	}
	if strings.Contains(string(rewritten), "Antigravity") {
		t.Fatalf("expected custom mapping MyBrand to override Antigravity, got: %s", string(rewritten))
	}
	if !strings.Contains(string(rewritten), "MyBrand") {
		t.Fatalf("expected MyBrand in rewritten body, got: %s", string(rewritten))
	}
}

func TestStreamChunksWithoutCorrelationKeyPassThrough(t *testing.T) {
	mgr := newStreamSessionManager()
	// Schema_version >= 3 payload chunks carry neither RequestID nor the
	// original request body. Such chunks cannot be attributed to a stream;
	// sharing one slot between them would corrupt concurrent output, so they
	// pass through unmolested and never create manager state.
	initNoKey := &pluginapi.StreamChunkInterceptRequest{
		RequestID:       "",
		SourceFormat:    "openai",
		OriginalRequest: []byte(`{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}],"messages":[]}`),
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}
	resp := mgr.processChunk(initNoKey, "openai")
	if len(resp.Body) > 0 || resp.DropChunk {
		t.Fatalf("header-init should stay a no-op, got %+v", resp)
	}

	payloadChunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"
	chunkReq := &pluginapi.StreamChunkInterceptRequest{
		RequestID:    "",
		SourceFormat: "openai",
		ChunkIndex:   0,
		Body:         []byte(payloadChunk),
	}
	countSessions := func() int {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.sessions)
	}
	sessionsBefore := countSessions()

	resp = mgr.processChunk(chunkReq, "openai")
	if len(resp.Body) > 0 || resp.DropChunk {
		t.Fatalf("expected pass-through of uncorrelated payload chunk, got body=%q drop=%t", string(resp.Body), resp.DropChunk)
	}
	if after := countSessions(); after != sessionsBefore {
		t.Fatalf("expected no session created for correlation-less stream, before=%d after=%d", sessionsBefore, after)
	}
}

func TestStreamSessionManagerCleanupStaleSessions(t *testing.T) {
	mgr := newStreamSessionManager()
	// Add an abandoned session with a stale timestamp (>5 mins ago)
	staleTime := time.Now().Add(-10 * time.Minute)
	mgr.mu.Lock()
	mgr.sessions["req:stale-stream"] = &streamSession{
		client:    "claude_code",
		updatedAt: staleTime,
	}
	mgr.mu.Unlock()

	// Trigger opportunistic cleanup via processChunk on any chunk
	dummyReq := &pluginapi.StreamChunkInterceptRequest{
		RequestID:    "req:active-stream",
		SourceFormat: "openai",
		ChunkIndex:   0,
		Body:         []byte("data: {}\n\n"),
	}
	mgr.processChunk(dummyReq, "openai")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if _, exists := mgr.sessions["req:stale-stream"]; exists {
		t.Fatalf("expected stale session to be pruned by processChunk")
	}
}

func TestStreamSessionManagerCleanupStalePreservesProtectedActiveSession(t *testing.T) {
	const requestID = "cleanup-protected-active"
	globalLifecycleManager.setRoute(requestID, &explicitOMPRouteState{
		routeKind: routeKindProtectedAGY,
		client:    "oh_my_pi",
	})
	defer globalLifecycleManager.deleteRoute(requestID)

	mgr := newStreamSessionManager()
	mgr.sessions["req:"+requestID] = &streamSession{
		client:         "oh_my_pi",
		updatedAt:      time.Now().Add(-10 * time.Minute),
		payloadStarted: true,
	}

	mgr.mu.Lock()
	mgr.cleanupStaleLocked()
	_, exists := mgr.sessions["req:"+requestID]
	mgr.mu.Unlock()
	if !exists {
		t.Fatal("expected stale ProtectedAGY session with active payload state to survive cleanup")
	}
}

func TestDetectCloakedClientOhMyPiStandardNineTools(t *testing.T) {
	// Canonical 9 tools sent by Oh My Pi after cloaking (using find_by_name, not list_dir)
	ompCloakedTools := []string{
		"view_file", "write_to_file", "replace_file_content",
		"run_command", "grep_search", "find_by_name",
		"invoke_subagent", "ask_question", "search_web",
	}
	// Target inventory is corroboration-only, never standalone OMP attribution.
	// For no-marker / uncorrelated traffic, a set containing only canonical AGY
	// target identities MUST NOT resolve to oh_my_pi solely from target names.
	got := detectCloakedClient(ompCloakedTools)
	if got == "oh_my_pi" {
		t.Fatalf("detectCloakedClient(ompCloakedTools) = %q, want non-oh_my_pi for standalone target names", got)
	}
	// When independent OMP attribution is present, corroboration succeeds.
	if gotCorroborated := detectCloakedClientWithSignal(ompCloakedTools, true); gotCorroborated != "oh_my_pi" {
		t.Fatalf("detectCloakedClientWithSignal(ompCloakedTools, true) = %q, want 'oh_my_pi'", gotCorroborated)
	}
}

func TestDetectCloakedClientNamespaceNormalized(t *testing.T) {
	// A namespaced (qualified) cloaked set must contribute one observed
	// identity per declared tool and never inflate the denominator with aliases.
	qualifiedNine := []string{
		"functions:view_file", "functions:write_to_file", "functions:replace_file_content",
		"functions:run_command", "functions:grep_search", "functions:find_by_name",
		"functions:invoke_subagent", "functions:ask_question", "functions:search_web",
	}
	// Target-only without attribution must not resolve to oh_my_pi
	if got := detectCloakedClient(qualifiedNine); got == "oh_my_pi" {
		t.Fatalf("qualified nine standalone => %q, want non-oh_my_pi", got)
	}
	if got := detectCloakedClientWithSignal(qualifiedNine, true); got != "oh_my_pi" {
		t.Fatalf("qualified nine with signal => %q, want oh_my_pi", got)
	}

	// Mixed qualified/unqualified forms of the same tool de-duplicate to one identity.
	mixed := []string{
		"view_file", "functions:write_to_file", "default_api:replace_file_content",
		"run_command", "grep_search", "find_by_name",
		"invoke_subagent", "ask_question", "search_web",
	}
	if got := detectCloakedClient(mixed); got == "oh_my_pi" {
		t.Fatalf("mixed nine standalone => %q, want non-oh_my_pi", got)
	}
	if got := detectCloakedClientWithSignal(mixed, true); got != "oh_my_pi" {
		t.Fatalf("mixed nine with signal => %q, want oh_my_pi", got)
	}

	// A set of observed identities that is mostly non-targets (below the 80%
	// observed-coverage threshold) must not qualify as any client.
	belowThreshold := []string{
		"functions:view_file", "functions:write_to_file", "functions:replace_file_content",
		"functions:run_command", "functions:custom_a", "functions:custom_b",
		"functions:custom_c", "functions:custom_d", "functions:custom_e",
	}
	if got := detectCloakedClient(belowThreshold); got != "" {
		t.Fatalf("below-threshold set should not qualify, got %q", got)
	}
	if got := detectCloakedClientWithSignal(belowThreshold, true); got != "" {
		t.Fatalf("below-threshold set with signal should not qualify, got %q", got)
	}
}

func TestBuildUncloakTableFallbackQualified(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Request body is already cloaked with qualified names, and there is no
	// RequestID pre-registration or independent OMP attribution.
	// Target names alone MUST NOT authorize OMP reverse mutation.
	body := `{
		"tools":[
			{"type":"function","function":{"name":"functions:view_file"}},
			{"type":"function","function":{"name":"functions:write_to_file"}},
			{"type":"function","function":{"name":"functions:replace_file_content"}},
			{"type":"function","function":{"name":"functions:run_command"}},
			{"type":"function","function":{"name":"functions:grep_search"}},
			{"type":"function","function":{"name":"functions:find_by_name"}},
			{"type":"function","function":{"name":"functions:invoke_subagent"}},
			{"type":"function","function":{"name":"functions:ask_question"}},
			{"type":"function","function":{"name":"functions:search_web"}}
		]
	}`
	uncloakTable, client := buildUncloakTable([]byte(body), "openai")
	if client == "oh_my_pi" {
		t.Fatalf("target-only body must not resolve to oh_my_pi, got client=%q", client)
	}
	if uncloakTable != nil && uncloakTable["view_file"] == "read" {
		t.Fatalf("target-only body must not authorize OMP reverse mutation, got %v", uncloakTable)
	}

	// When independent source-side evidence exists in the original request,
	// buildUncloakTable successfully identifies oh_my_pi and builds the table.
	origBody := `{
		"tools":[
			{"type":"function","function":{"name":"functions:read"}},
			{"type":"function","function":{"name":"functions:write"}},
			{"type":"function","function":{"name":"functions:edit"}},
			{"type":"function","function":{"name":"functions:bash"}},
			{"type":"function","function":{"name":"functions:grep"}},
			{"type":"function","function":{"name":"functions:glob"}}
		]
	}`
	origTable, origClient := buildUncloakTable([]byte(origBody), "openai")
	if origClient != "oh_my_pi" {
		t.Fatalf("origBody client = %q, want 'oh_my_pi'", origClient)
	}
	if origTable == nil || origTable["view_file"] != "read" || origTable["find_by_name"] != "glob" {
		t.Fatalf("expected OMP uncloak table with view_file->read and find_by_name->glob, got %v", origTable)
	}
}

func TestDetectClientWithNamespacePrefix(t *testing.T) {
	tests := []struct {
		name       string
		toolNames  []string
		wantClient string
	}{
		{
			name:       "oh_my_pi with functions prefix",
			toolNames:  []string{"functions:read", "functions:write", "functions:bash", "functions:todo"},
			wantClient: "oh_my_pi",
		},
		{
			name:       "oh_my_pi with default_api prefix",
			toolNames:  []string{"default_api:read", "default_api:write", "default_api:bash", "default_api:todo"},
			wantClient: "oh_my_pi",
		},
		{
			name:       "claude_code with functions prefix",
			toolNames:  []string{"functions:Bash", "functions:Edit", "functions:Read", "functions:Write"},
			wantClient: "claude_code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectClient(tt.toolNames)
			if got != tt.wantClient {
				t.Fatalf("detectClient(%v) = %q, want %q", tt.toolNames, got, tt.wantClient)
			}
		})
	}
}

func TestRewriteRequestBodyWithNamespacePrefix(t *testing.T) {
	body := `{
		"system": "You have access to functions:read and functions:task.",
		"tools": [
			{"type": "function", "function": {"name": "functions:read", "description": "Read file"}},
			{"type": "function", "function": {"name": "functions:task", "description": "Delegate tasks"}},
			{"type": "function", "function": {"name": "default_api:bash", "description": "Execute command"}}
		],
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "functions:read", "arguments": "{\"path\":\"file.txt\"}"}}
				]
			},
			{
				"role": "tool",
				"name": "functions:read",
				"content": "file contents"
			}
		]
	}`

	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten {
		t.Fatal("expected rewritten = true")
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify tools were cloaked with namespace preserved
	tools := parsed["tools"].([]any)
	t0 := tools[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	t1 := tools[1].(map[string]any)["function"].(map[string]any)["name"].(string)
	t2 := tools[2].(map[string]any)["function"].(map[string]any)["name"].(string)

	if t0 != "functions:view_file" {
		t.Errorf("t0 name = %q, want functions:view_file", t0)
	}
	if t1 != "functions:invoke_subagent" {
		t.Errorf("t1 name = %q, want functions:invoke_subagent", t1)
	}
	if t2 != "default_api:run_command" {
		t.Errorf("t2 name = %q, want default_api:run_command", t2)
	}

	// Verify messages tool calls and tool result
	msgs := parsed["messages"].([]any)
	tc := msgs[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	if tc != "functions:view_file" {
		t.Errorf("msg[0] tool_call name = %q, want functions:view_file", tc)
	}
	trName := msgs[1].(map[string]any)["name"].(string)
	if trName != "functions:view_file" {
		t.Errorf("msg[1] tool result name = %q, want functions:view_file", trName)
	}

	// Verify system prompt tool replacement
	sys := parsed["system"].(string)
	if strings.Contains(sys, "functions:read") || strings.Contains(sys, "functions:task") {
		t.Errorf("system prompt leaked original names: %s", sys)
	}
}

func TestUncloakResponseBodyWithNamespacePrefix(t *testing.T) {
	uncloakTable := map[string]string{
		"view_file":   "read",
		"manage_task": "todo",
		"run_command": "bash",
	}

	body := `{
		"choices": [
			{
				"message": {
					"role": "assistant",
					"tool_calls": [
						{"id": "call_1", "type": "function", "function": {"name": "functions:view_file", "arguments": "{\"path\":\"a.txt\"}"}},
						{"id": "call_2", "type": "function", "function": {"name": "default_api:manage_task", "arguments": "{\"op\":\"view\"}"}}
					]
				}
			}
		]
	}`

	got, changed := uncloakResponseBody([]byte(body), uncloakTable, "openai")
	if !changed {
		t.Fatal("expected changed = true")
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	calls := parsed["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)
	c0 := calls[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	c1 := calls[1].(map[string]any)["function"].(map[string]any)["name"].(string)

	if c0 != "functions:read" {
		t.Errorf("c0 name = %q, want functions:read", c0)
	}
	if c1 != "default_api:todo" {
		t.Errorf("c1 name = %q, want default_api:todo", c1)
	}
}

func TestUncloakStreamChunkWithNamespacePrefix(t *testing.T) {
	uncloakTable := map[string]string{
		"view_file":   "read",
		"manage_task": "todo",
	}
	cached := buildTestUncloakPattern(uncloakTable)

	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"functions:view_file\",\"arguments\":\"{}\"}}]}}]}\n\n"
	got, changed := uncloakStreamChunk([]byte(chunk), cached)
	if !changed {
		t.Fatal("expected changed = true")
	}
	if !strings.Contains(string(got), `"name":"functions:read"`) {
		t.Errorf("expected functions:read in stream chunk, got: %s", string(got))
	}
}
func TestReplaceToolNamesInTextNamespaceSafety(t *testing.T) {
	cloakTable := map[string]string{
		"read":  "view_file",
		"write": "write_to_file",
		"todo":  "manage_task",
		"bash":  "run_command",
	}
	cached := buildTestCloakPatterns(cloakTable)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"qualified in prose", "use functions:read here", "use functions:view_file here"},
		{"qualified todo", "manage functions:todo now", "manage functions:manage_task now"},
		{"qualified bash", "call default_api:bash", "call default_api:run_command"},
		{"access mode read:write", "the mode read:write", "the mode read:write"},
		{"access mode in explicit tool context", "the read:write tool", "the read:write tool"},
		{"access mode write:read", "the mode write:read", "the mode write:read"},
		{"bare ambiguous untouched", "read the file", "read the file"},
		{"bare bash untouched", "bash around", "bash around"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := replaceToolNamesInText(tt.input, cached)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplaceToolNamesInTextExactlyOnceCustomMapping(t *testing.T) {
	// A custom mapping whose target is also a source key must NOT cascade:
	// read -> write -> edit must stop at write for a single "read" identity.
	// foo_bar -> read must stop at read and not cascade to write in "use foo_bar".
	cloakTable := map[string]string{
		"read":    "write",
		"write":   "edit",
		"foo_bar": "read",
	}
	cached := buildTestCloakPatterns(cloakTable)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"quoted read", "use `read`", "use `write`"},
		{"quoted write", "use `write`", "use `edit`"},
		{"context read", "use read to go", "use write to go"},
		{"unambiguous to ambiguous no cascade", "use foo_bar", "use read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := replaceToolNamesInText(tt.input, cached)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplaceToolNamesInTextQuotedUnquotedConsistent(t *testing.T) {
	cloakTable := map[string]string{
		"read":  "view_file",
		"write": "write_to_file",
	}
	cached := buildTestCloakPatterns(cloakTable)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"quoted namespace", "`functions:read`", "`functions:view_file`"},
		{"unquoted namespace", "functions:read", "functions:view_file"},
		{"quoted base", "`read`", "`view_file`"},
		{"unquoted bare ambiguous untouched", "read", "read"},
		{"double quoted namespace", `"functions:write"`, `"functions:write_to_file"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := replaceToolNamesInText(tt.input, cached)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplaceToolNamesInTextLongTierOneBounds(t *testing.T) {
	cached := buildTestCloakPatterns(map[string]string{
		"read":    "view_file",
		"foo_bar": "mapped",
	})
	padding := strings.Repeat("x", 9<<10)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"quoted namespace", "use `functions:read` " + padding, "use `functions:view_file` " + padding},
		{"unambiguous boundary", "foo_bar " + padding, "mapped " + padding},
		{"unambiguous partial word", "foo_barX " + padding, "foo_barX " + padding},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := replaceToolNamesInText(tt.input, cached)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelGateBeforeAlias503_EmptyRequestID(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy/]`)))

	cases := []struct {
		name         string
		client       string
		sourceFormat string
		reqBody      string
	}{
		{
			name:         "claude_code",
			client:       "claude_code",
			sourceFormat: "anthropic",
			reqBody:      `{"tools":[{"name":"Bash","description":"run commands"}],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:         "codex",
			client:       "codex",
			sourceFormat: "openai",
			reqBody:      `{"tools":[{"type":"function","function":{"name":"exec"}}],"messages":[{"role":"user","content":"hi"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 1. Model outside the gate ("other/model") with EMPTY RequestID ("")
			// Because modelAllowsCloak runs before alias plan correlation, the request
			// must be gate-skipped (passthrough) with Terminate=false and NO 503 tool_cloak_required.
			headers := http.Header{"X-Cloak-Client": {tc.client}}
			rawPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, "", tc.sourceFormat, "other/model", []byte(tc.reqBody), headers)

			rawResp, code := handlePluginCall("request.intercept_before", rawPayload)
			if code != 0 {
				t.Fatalf("code = %d", code)
			}

			var envelope struct {
				OK     bool `json:"ok"`
				Result struct {
					Terminate       bool                `json:"Terminate"`
					StatusCode      int                 `json:"StatusCode"`
					ResponseBody    string              `json:"ResponseBody"`
					ResponseHeaders map[string][]string `json:"ResponseHeaders"`
				} `json:"result"`
			}
			mustUnmarshalJSON(t, rawResp, &envelope)

			if envelope.Result.Terminate {
				t.Fatalf("expected request outside model gate to NOT terminate, got Terminate=true with status=%d", envelope.Result.StatusCode)
			}
			if envelope.Result.StatusCode != 0 {
				t.Fatalf("expected status 0 (no HTTP error), got %d", envelope.Result.StatusCode)
			}
			if strings.Contains(envelope.Result.ResponseBody, "tool_cloak_required") {
				t.Fatalf("unexpected tool_cloak_required in gate-skipped response: %s", envelope.Result.ResponseBody)
			}

			// 2. Control check: on an eligible model ("agy/model"), the same empty RequestID MUST fail closed with 503 tool_cloak_required
			rawEligible := makeIntegrationRequestInterceptPayloadWithHeaders(t, "", tc.sourceFormat, "agy/model", []byte(tc.reqBody), headers)
			rawRespEligible, codeEligible := handlePluginCall("request.intercept_before", rawEligible)
			if codeEligible != 0 {
				t.Fatalf("code = %d", codeEligible)
			}
			var envelopeEligible struct {
				OK     bool `json:"ok"`
				Result struct {
					Terminate    bool   `json:"Terminate"`
					StatusCode   int    `json:"StatusCode"`
					ResponseBody string `json:"ResponseBody"`
				} `json:"result"`
			}
			mustUnmarshalJSON(t, rawRespEligible, &envelopeEligible)
			if !envelopeEligible.Result.Terminate || envelopeEligible.Result.StatusCode != 503 {
				t.Fatalf("expected eligible model with empty RequestID to fail closed with 503, got terminate=%t status=%d",
					envelopeEligible.Result.Terminate, envelopeEligible.Result.StatusCode)
			}
		})
	}
}

func TestAliasPlan_ConfigValidation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	clients := []string{"claude_code", "codex"}
	for _, client := range clients {
		t.Run(client, func(t *testing.T) {
			cases := []struct {
				name        string
				yaml        string
				wantErr     bool
				errContains string
			}{
				{
					name: "valid custom mapping",
					yaml: "tool_mappings:\n  " + client + ":\n    my_tool: wp_custom_tool\n",
				},
				{
					name:        "internal whitespace target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: wp custom tool\n",
					wantErr:     true,
					errContains: "contains whitespace",
				},
				{
					name:        "trailing space target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: \"wp_custom \"\n",
					wantErr:     true,
					errContains: "contains whitespace",
				},
				{
					name:        "leading space target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: \" wp_custom\"\n",
					wantErr:     true,
					errContains: "contains whitespace",
				},
				{
					name:        "tab in target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: \"wp\tcustom\"\n",
					wantErr:     true,
					errContains: "contains whitespace",
				},
				{
					name:        "empty target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: \"\"\n",
					wantErr:     true,
					errContains: "empty mapping target",
				},
				{
					name:        "leading underscore target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: _custom_target\n",
					wantErr:     true,
					errContains: "leading underscore or colon is reserved",
				},
				{
					name:        "leading colon target",
					yaml:        "tool_mappings:\n  " + client + ":\n    my_tool: \":custom_target\"\n",
					wantErr:     true,
					errContains: "leading underscore or colon is reserved",
				},
				{
					name:        "collision within custom mappings",
					yaml:        "tool_mappings:\n  " + client + ":\n    tool1: wp_shared\n    tool2: wp_shared\n",
					wantErr:     true,
					errContains: "non-injective target naming",
				},
				{
					name:        "collision with namespace variants",
					yaml:        "tool_mappings:\n  " + client + ":\n    tool1: functions:wp_shared\n    tool2: default_api:wp_shared\n",
					wantErr:     true,
					errContains: "non-injective target naming",
				},
				{
					name:        "collision with reserved shared alias target",
					yaml:        "tool_mappings:\n  " + client + ":\n    unrelated: wp_list_workers\n",
					wantErr:     true,
					errContains: "non-injective target naming",
				},
				{
					name:        "collision with static tier-1 target",
					yaml:        "tool_mappings:\n  " + client + ":\n    unrelated: run_command\n",
					wantErr:     true,
					errContains: "non-injective target naming",
				},
				{
					name:        "remap of an unrelated tier-1 owner does not free the target",
					yaml:        "tool_mappings:\n  " + client + ":\n    unrelated: run_command\n    Read: wp_read_alias\n    web_search: wp_web_search_alias\n",
					wantErr:     true,
					errContains: "non-injective target naming",
				},
				{
					name:        "case-variant of tier-1 owner does not free target",
					yaml:        "tool_mappings:\n  " + client + ":\n    unrelated: run_command\n    bash: wp_bash_alias\n    Exec: wp_exec_alias\n",
					wantErr:     true,
					errContains: "non-injective target naming",
				},
				{
					// Effective-state validation, not a target blacklist: remapping the
					// tier-1 owner away (Bash for Claude Code, exec for Codex) releases
					// run_command for a custom source in the same delta.
					name: "remap-away of the tier-1 owner frees its target",
					yaml: "tool_mappings:\n  " + client + ":\n    unrelated: run_command\n    Bash: wp_bash_alias\n    exec: wp_exec_alias\n",
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					_, err := parseFilterConfigYAML([]byte(tc.yaml))
					if tc.wantErr {
						if err == nil {
							t.Fatalf("expected error containing %q, got nil", tc.errContains)
						}
						if !strings.Contains(err.Error(), tc.errContains) {
							t.Fatalf("error %q does not contain %q", err.Error(), tc.errContains)
						}

						// Also verify plugin.reconfigure fails visibly with invalid_config error envelope
						rawResp, _ := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(tc.yaml)))
						var env struct {
							OK    bool `json:"ok"`
							Error struct {
								Code    string `json:"code"`
								Message string `json:"message"`
							} `json:"error"`
						}
						mustUnmarshalJSON(t, rawResp, &env)
						if env.OK || env.Error.Code != "invalid_config" {
							t.Fatalf("expected visible invalid_config envelope at reconfigure time for %s, got: %s", tc.name, rawResp)
						}
					} else {
						if err != nil {
							t.Fatalf("unexpected error: %v", err)
						}
						rawResp, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(tc.yaml)))
						if code != 0 {
							t.Fatalf("reconfigure failed with code %d", code)
						}
						var env struct {
							OK bool `json:"ok"`
						}
						mustUnmarshalJSON(t, rawResp, &env)
						if !env.OK {
							t.Fatalf("reconfigure expected ok=true, got: %s", rawResp)
						}
					}
				})
			}
		})
	}
}

// Only the help text of a tool schema may be rewritten. The walk used to touch
// every string in the schema, so a `required` member diverged from the property
// name it referred to: required:["Claude"] became required:["Antigravity"] while
// the property stayed "Claude", leaving a schema no arguments could satisfy.
func TestToolSchemaStructureSurvivesBrandRewrite(t *testing.T) {
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	})
	defer restoreDefaultFilterConfig(t)

	body, changed, _ := rewriteRequestBodyWithClient([]byte(`{"system":"x","tools":[{"name":"Bash","description":"d","input_schema":{"type":"object","description":"see .claude/CLAUDE.md","properties":{"Claude":{"type":"string","description":"the Claude name"}},"required":["Claude"],"additionalProperties":false}}]}`), "anthropic", "claude_code")
	if !changed {
		t.Fatalf("schema help text must still be rewritten (body=%s)", body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	schema, _ := doc["tools"].([]any)[0].(map[string]any)["input_schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("input_schema lost: %s", body)
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "Claude" {
		t.Fatalf("required member rewritten away from the property name: %v", schema["required"])
	}
	props, _ := schema["properties"].(map[string]any)
	inner, _ := props["Claude"].(map[string]any)
	if inner == nil {
		t.Fatalf("property name rewritten; required no longer resolves: %v", props)
	}
	if _, ok := schema["additionalProperties"].(bool); !ok {
		t.Fatalf("additionalProperties keyword lost: %v", schema["additionalProperties"])
	}
	if got, _ := schema["description"].(string); !strings.Contains(got, ".gemini/GEMINI.md") {
		t.Fatalf("schema description not remapped: %q", got)
	}
	if got, _ := inner["description"].(string); strings.Contains(got, "Claude") {
		t.Fatalf("nested property description not remapped: %q", got)
	}
}
