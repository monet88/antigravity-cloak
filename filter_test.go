package main

import (
	"encoding/json"
	"strings"
	"testing"
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
			{"type":"function","function":{"name":"bash","description":"Run Claude Code shell commands"}},
			{"type":"function","function":{"name":"read","description":"Read files"}},
			{"type":"function","function":{"name":"edit","description":"Edit files"}}
		],
		"messages":[],
		"tool_choice":{"type":"function","function":{"name":"bash"}}
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten { t.Fatal("want rewritten") }
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
	if !rewritten { t.Fatal("want rewritten") }
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
			{"type":"function","function":{"name":"bash"}},
			{"type":"function","function":{"name":"edit"}},
			{"type":"function","function":{"name":"read"}}
		],
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"tc1","type":"function","function":{"name":"bash","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"tc1","name":"bash","content":"output"}
		]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten { t.Fatal("want rewritten") }
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
	tests := []struct{
		name string
		body string
	}{
		{
			name: "tool_choice as string (skip safely)",
			body: `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"edit"}},{"type":"function","function":{"name":"read"}}],"tool_choice":"auto","messages":[]}`,
		},
		{
			name: "tool_choice as object with function.name",
			body: `{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"edit"}},{"type":"function","function":{"name":"read"}}],"tool_choice":{"type":"function","function":{"name":"bash"}},"messages":[]}`,
		},
		{
			name: "anthropic tool_choice as object with name",
			body: `{"tools":[{"name":"bash"},{"name":"edit"},{"name":"read"}],"tool_choice":{"type":"tool","name":"bash"},"messages":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceFormat := "openai"
			if strings.Contains(tt.name, "anthropic") { sourceFormat = "anthropic" }
			got, rewritten := rewriteRequestBody([]byte(tt.body), sourceFormat)
			if !rewritten { t.Fatal("want rewritten") }
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
	if rewritten { t.Fatal("want no rewrite for unknown tools") }
}

func TestRewriteRequestBodyAppliesBrandReplaceToToolDescription(t *testing.T) {
	body := `{
		"tools":[{"type":"function","function":{"name":"bash","description":"Claude Code shell tool"}}],
		"messages":[]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "openai")
	if !rewritten { t.Fatal("want rewritten") }
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
	if !rewritten { t.Fatal("want rewritten") }
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
			{"name":"bash","description":"Run Claude Code shell"},
			{"name":"read","description":"Read files"},
			{"name":"edit","description":"Edit files"}
		],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"bash","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"output"}]}
		]
	}`
	got, rewritten := rewriteRequestBody([]byte(body), "anthropic")
	if !rewritten { t.Fatal("want rewritten") }
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
	if defaultUncloakTables["claude_code"]["run_command"] != "bash" {
		t.Fatal("expected bash")
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
		{"claude code by askUserQuestion", []string{"bash", "askUserQuestion", "read"}, "claude_code"},
		{"claude code by signature trio", []string{"bash", "edit", "read", "write"}, "claude_code"},
		{"codex by shell_command", []string{"shell_command", "apply_patch"}, "codex"},
		{"codex by apply_patch only", []string{"apply_patch", "request_user_input"}, "codex"},
		// detectClient only matches original (cloak table key) names; Antigravity
		// native tools are NOT keys, so detectClient returns "" for them.
		{"antigravity tools return empty", []string{"ask_permission", "run_command"}, ""},
		{"antigravity-like invoke_subagent", []string{"invoke_subagent", "view_file"}, ""},
		{"unknown tools", []string{"custom_tool", "another_tool"}, ""},
		{"empty list", []string{}, ""},
		// Only 1 original key match → below threshold of 2
		{"single original match below threshold", []string{"bash", "ask_permission"}, ""},
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
			got, changed := replaceToolNamesInText(tt.input, cloakTable)
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
			{"type":"function","function":{"name":"bash","description":"Use the bash tool to run commands"}},
			{"type":"function","function":{"name":"read","description":"Use read to view files"}}
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
	if strings.Contains(desc0, "bash") {
		t.Fatalf("expected 'bash' to be replaced in description, got %q", desc0)
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
			{"type":"function","function":{"name":"bash","description":"run shell"}},
			{"type":"function","function":{"name":"read","description":"read file"}}
		],
		"messages":[
			{"role":"system","content":"Use bash to execute commands. Call read to view files. The edit tool modifies content."}
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
	// "read" in plain prose should NOT be present — only the tool-reference pattern "Call read" matched
	if strings.Contains(content, "Use bash") {
		t.Fatalf("expected 'bash' replaced in 'Use bash' context, got %q", content)
	}
	// Plain prose "read" outside a tool pattern should remain untouched
	// (here all occurrences of "read" were in "Call read" context, so all replaced)
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
	uncloakTable := buildUncloakTable([]byte(cloakedReqBody), "openai")
	if uncloakTable == nil {
		t.Fatal("expected non-nil uncloak table for cloaked Claude Code request")
	}
	if uncloakTable["run_command"] != "bash" {
		t.Fatalf("expected run_command → bash, got %q", uncloakTable["run_command"])
	}
	if uncloakTable["invoke_subagent"] != "agent" {
		t.Fatalf("expected invoke_subagent → agent, got %q", uncloakTable["invoke_subagent"])
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
	uncloakTable := map[string]string{"run_command": "bash"}
	chunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"code\\\":\\\"<h1>Test</h1>\\\",\\\"id\\\":1234567890123456789}\"}}]}}]}\n\n"

	result, changed := uncloakStreamChunk([]byte(chunk), uncloakTable, "openai")
	if !changed {
		t.Fatal("expected changed = true")
	}

	resultStr := string(result)
	// Verify tool name was uncloaked
	if !strings.Contains(resultStr, `"bash"`) {
		t.Fatalf("tool name not uncloaked: %s", resultStr)
	}
	// Verify no HTML escaping
	if strings.Contains(resultStr, `\u003c`) {
		t.Fatalf("HTML chars were escaped: %s", resultStr)
	}
	// Verify number preserved
	if !strings.Contains(resultStr, "1234567890123456789") {
		t.Fatalf("large integer corrupted: %s", resultStr)
	}
}
