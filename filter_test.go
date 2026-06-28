package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRewriteRequestReplacesDefaultSystemKeywords(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "string system mentions opencode",
			body: `{"system":"You are OpenCode, an AI coding tool."}`,
			want: "You are Antigravity, an AI coding tool.",
		},
		{
			name: "array system mentions claude code",
			body: `{"system":[{"type":"text","text":"Run as Claude Code."}]}`,
			want: "Run as Antigravity.",
		},
		{
			name: "case insensitive codex",
			body: `{"system":"route this CODEX session"}`,
			want: "route this Antigravity session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rewritten := rewriteRequestBody([]byte(tt.body), "openai")
			if !rewritten {
				t.Fatalf("rewritten = false, want true")
			}
			if !containsSystemText(t, got, tt.want) {
				t.Fatalf("rewritten body = %s, want system text %q", got, tt.want)
			}
		})
	}
}

func TestRewriteRequestIgnoresKeywordsOutsideSystem(t *testing.T) {
	// UPDATED: This test now verifies brand replace does NOT touch
	// user/assistant message content. System role messages ARE replaced (new behavior).
	body := []byte(`{
		"messages":[
			{"role":"user","content":"please compare OpenCode and Codex"},
			{"role":"assistant","content":"Claude Code is a tool"}
		],
		"input":"Claude Code is mentioned by the user"
	}`)
	got, rewritten := rewriteRequestBody(body, "openai")
	if rewritten {
		t.Fatalf("rewritten = true, want false; body=%s", got)
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
			{"type":"function","function":{"name":"shell_command","description":"Execute Codex shell"}},
			{"type":"function","function":{"name":"apply_patch","description":"Apply patches"}}
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
	if name := t1["name"].(string); name != "multi_replace_file_content" {
		t.Errorf("tools[1] name = %q, want multi_replace_file_content", name)
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
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
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
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
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
	if content := m1["content"].(string); !strings.Contains(content, "Claude Code") {
		t.Errorf("user message content = %q, want unchanged", content)
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
	// Codex
	if defaultUncloakTables["codex"]["run_command"] != "shell_command" {
		t.Fatal("expected shell_command")
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
		{"codex by shell_command", []string{"shell_command", "apply_patch"}, "codex"},
		{"codex by apply_patch only", []string{"apply_patch", "request_user_input"}, "codex"},
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

func TestDetectCloakedClient(t *testing.T) {
	tests := []struct {
		name       string
		toolNames  []string
		wantClient string
	}{
		// All Claude Code cloak TARGETS present → detected as claude_code
		{"cloaked claude code", []string{"run_command", "replace_file_content", "view_file", "write_to_file", "grep_search", "list_dir", "invoke_subagent", "ask_question", "search_web", "call_mcp_tool", "schedule"}, "claude_code"},
		// All Codex cloak TARGETS present → detected as codex
		{"cloaked codex", []string{"run_command", "multi_replace_file_content", "ask_question", "generate_image", "manage_task", "search_web", "schedule", "send_message", "define_subagent", "list_resources", "list_permissions", "read_resource"}, "codex"},
		// Both clients' targets present (native Antigravity) → returns ""
		{"native antigravity superset", []string{"run_command", "replace_file_content", "view_file", "write_to_file", "grep_search", "list_dir", "invoke_subagent", "ask_question", "search_web", "call_mcp_tool", "schedule", "multi_replace_file_content", "generate_image", "manage_task", "send_message", "define_subagent", "list_resources", "list_permissions", "read_resource", "ask_permission"}, ""},
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
	// Regression test for Bug #1: when request body has already been cloaked,
	// buildUncloakTable should still find the correct uncloak table via
	// detectCloakedClient instead of misidentifying as Antigravity.
	cloakedReqBody := `{
		"tools":[
			{"type":"function","function":{"name":"run_command"}},
			{"type":"function","function":{"name":"replace_file_content"}},
			{"type":"function","function":{"name":"view_file"}},
			{"type":"function","function":{"name":"write_to_file"}},
			{"type":"function","function":{"name":"grep_search"}},
			{"type":"function","function":{"name":"list_dir"}},
			{"type":"function","function":{"name":"invoke_subagent"}},
			{"type":"function","function":{"name":"ask_question"}},
			{"type":"function","function":{"name":"search_web"}},
			{"type":"function","function":{"name":"call_mcp_tool"}},
			{"type":"function","function":{"name":"schedule"}}
		],
		"messages":[]
	}`
	uncloakTable, client := buildUncloakTable([]byte(cloakedReqBody), "openai")
	if uncloakTable == nil {
		t.Fatal("expected non-nil uncloak table for cloaked Claude Code request")
	}
	if client == "" {
		t.Fatal("expected non-empty client name")
	}
	if uncloakTable["run_command"] != "Bash" {
		t.Fatalf("expected run_command → Bash, got %q", uncloakTable["run_command"])
	}
	if uncloakTable["invoke_subagent"] != "Agent" {
		t.Fatalf("expected invoke_subagent → Agent, got %q", uncloakTable["invoke_subagent"])
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
	result, rewritten := rewriteRequestBody([]byte(body), "openai")
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
			complete, incomplete := splitSSEEvents([]byte(tt.input))
			if string(complete) != tt.wantComplete {
				t.Fatalf("complete = %q, want %q", string(complete), tt.wantComplete)
			}
			if string(incomplete) != tt.wantIncomplete {
				t.Fatalf("incomplete = %q, want %q", string(incomplete), tt.wantIncomplete)
			}
		})
	}
}

func TestSSESplitStringChunk(t *testing.T) {
	// THE critical test: tool name "run_command" is split across two TCP chunks.
	// Without event reassembly, regex misses the match on both chunks.
	// With reassembly, the incomplete event from chunk 1 is buffered and
	// combined with chunk 2 to form a complete event before regex runs.
	cached := buildTestUncloakPattern(map[string]string{"run_command": "bash"})

	// Chunk 1: incomplete event — tool name cut at "run_c"
	chunk1 := []byte(`data: {"type": "tool_use", "id": "123", "name": "run_c`)

	// Simulate new stream
	const streamKey = uint64(1)
	resetStreamBuffer(streamKey)

	complete1, incomplete1 := splitSSEEvents(chunk1)
	if len(complete1) != 0 {
		t.Fatal("chunk1 should have no complete events")
	}
	if string(incomplete1) != string(chunk1) {
		t.Fatal("chunk1 should be entirely buffered")
	}
	pushStreamBuffer(streamKey, incomplete1)

	// Chunk 2: completes the event
	chunk2 := []byte("ommand\", \"input\": {}}\n\n")

	buffered := popStreamBuffer(streamKey)
	if buffered == nil {
		t.Fatal("expected buffered data from chunk 1")
	}

	// Combine buffered + chunk2
	combined := make([]byte, len(buffered)+len(chunk2))
	copy(combined, buffered)
	copy(combined[len(buffered):], chunk2)

	complete2, incomplete2 := splitSSEEvents(combined)
	if len(incomplete2) != 0 {
		t.Fatalf("expected no incomplete tail, got %q", string(incomplete2))
	}

	// Regex on complete event
	result, changed := uncloakStreamChunk(complete2, cached)
	if !changed {
		t.Fatal("expected uncloakStreamChunk to match the reassembled tool name")
	}

	resultStr := string(result)
	if !strings.Contains(resultStr, `"name": "bash"`) && !strings.Contains(resultStr, `"name":"bash"`) {
		t.Fatalf("tool name not uncloaked after reassembly: %s", resultStr)
	}
	if strings.Contains(resultStr, "run_command") {
		t.Fatalf("cloaked name 'run_command' leaked through: %s", resultStr)
	}
}

func TestStreamBufferResetOnNewStream(t *testing.T) {
	// Verify that resetStreamBuffer clears leftover data from a previous stream,
	// preventing cross-stream pollution.
	const streamKey = uint64(1)
	pushStreamBuffer(streamKey, []byte("leftover from stream 1"))

	// Simulate new stream (ChunkIndex == 0)
	resetStreamBuffer(streamKey)

	data := popStreamBuffer(streamKey)
	if data != nil {
		t.Fatalf("expected nil after reset, got %q", string(data))
	}
}

func TestStreamBufferPushPop(t *testing.T) {
	// Verify basic push/pop semantics.
	const streamKey = uint64(1)
	resetStreamBuffer(streamKey)

	data := popStreamBuffer(streamKey)
	if data != nil {
		t.Fatal("expected nil from empty buffer")
	}

	pushStreamBuffer(streamKey, []byte("test data"))
	data = popStreamBuffer(streamKey)
	if string(data) != "test data" {
		t.Fatalf("expected 'test data', got %q", string(data))
	}

	// Second pop should be nil
	data = popStreamBuffer(streamKey)
	if data != nil {
		t.Fatal("expected nil after pop")
	}
}

func TestStreamBufferIsolatesConcurrentStreams(t *testing.T) {
	// Two interleaved streams must not corrupt each other's buffered tail.
	const keyA = uint64(0xA)
	const keyB = uint64(0xB)
	resetStreamBuffer(keyA)
	resetStreamBuffer(keyB)

	pushStreamBuffer(keyA, []byte("stream-A-tail"))
	pushStreamBuffer(keyB, []byte("stream-B-tail"))

	if got := string(popStreamBuffer(keyA)); got != "stream-A-tail" {
		t.Fatalf("stream A buffer = %q, want %q", got, "stream-A-tail")
	}
	// Popping A must not disturb B.
	if got := string(popStreamBuffer(keyB)); got != "stream-B-tail" {
		t.Fatalf("stream B buffer = %q, want %q", got, "stream-B-tail")
	}
}

func TestStreamBufferKeyDiffersPerRequest(t *testing.T) {
	// Distinct request bodies must map to distinct buffer keys so concurrent
	// streams stay isolated; identical bodies share a key across chunks.
	reqA := &pluginapi.StreamChunkInterceptRequest{OriginalRequest: []byte(`{"a":1}`)}
	reqB := &pluginapi.StreamChunkInterceptRequest{OriginalRequest: []byte(`{"b":2}`)}
	if streamBufferKey(reqA) == streamBufferKey(reqB) {
		t.Fatal("expected different keys for different request bodies")
	}
	if streamBufferKey(reqA) != streamBufferKey(reqA) {
		t.Fatal("expected stable key for identical request body")
	}
	// Falls back to RequestBody when OriginalRequest is empty.
	reqC := &pluginapi.StreamChunkInterceptRequest{RequestBody: []byte(`{"a":1}`)}
	if streamBufferKey(reqA) != streamBufferKey(reqC) {
		t.Fatal("expected OriginalRequest and matching RequestBody fallback to share a key")
	}
}

// buildTestCloakPatterns creates a cachedCloakPatterns for testing,
// mirroring the logic in rebuildCachedRegexes.
func buildTestCloakPatterns(cloakTable map[string]string) *cachedCloakPatterns {
	cp := &cachedCloakPatterns{
		cloakTable: cloakTable,
		safeLookup: make(map[string]string),
	}
	var safeParts []string
	for orig, target := range cloakTable {
		if isUnambiguousToolName(orig) {
			safeParts = append(safeParts, regexp.QuoteMeta(orig))
			cp.safeLookup[orig] = target
		} else {
			qOrig := regexp.QuoteMeta(orig)
			var patterns []*regexp.Regexp
			for _, p := range []string{
				`(?i)(the\s+)` + qOrig + `(\s+(?:tool|function|command)\b)`,
				`(?i)((?:use|call|run|invoke|with)\s+)` + qOrig + `(\b)`,
			} {
				if re, err := regexp.Compile(p); err == nil {
					patterns = append(patterns, re)
				}
			}
			cp.ambiguousRules = append(cp.ambiguousRules, cachedAmbiguousRule{
				patterns: patterns,
				target:   target,
			})
		}
	}
	if len(safeParts) > 0 {
		pattern := `\b(` + strings.Join(safeParts, "|") + `)\b`
		cp.safeRe, _ = regexp.Compile(pattern)
	}
	return cp
}

// buildTestUncloakPattern creates a cachedUncloakPattern for testing.
func buildTestUncloakPattern(uncloakTable map[string]string) *cachedUncloakPattern {
	targets := make([]string, 0, len(uncloakTable))
	for target := range uncloakTable {
		targets = append(targets, regexp.QuoteMeta(target))
	}
	pattern := `"name"\s*:\s*"(` + strings.Join(targets, "|") + `)"`
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
