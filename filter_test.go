package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestStreamChunkReassemblesSplitToolName(t *testing.T) {
	// THE critical test: tool name "run_command" is split across two TCP chunks.
	// Without event reassembly, regex misses the match on both chunks. With
	// reassembly via the session tail buffer, chunk 1 is held back, combined
	// with chunk 2 into a complete SSE event, then uncloaked.
	mgr := newStreamSessionManager()
	const reqID = "split-chunk-reassembly"
	reqBody := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}],"messages":[]}`
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    "openai",
		OriginalRequest: []byte(reqBody),
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}, "openai")

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

func TestBuiltInKeywordPresetCoversMainstreamCodingToolsAndAgents(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	applyFilterConfig(defaultFilterConfig())

	for _, mapping := range defaultRewriteMappings {
		keyword := mapping.Match
		t.Run(keyword, func(t *testing.T) {
			body := `{"system":"You are running with ` + keyword + ` in this environment."}`
			got, rewritten := rewriteRequestBody([]byte(body), "openai")
			if !rewritten {
				t.Fatalf("keyword %q was not rewritten", keyword)
			}
			if strings.Contains(strings.ToLower(string(got)), strings.ToLower(keyword)) && strings.ToLower(keyword) != "antigravity" {
				t.Fatalf("keyword %q still present in output: %s", keyword, got)
			}
			if !strings.Contains(string(got), "Antigravity") {
				t.Fatalf("replacement Antigravity missing for keyword %q: %s", keyword, got)
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

	// Assert tools cloaked
	toolsRaw := parsed["tools"].([]any)
	expectedMap := map[string]string{
		"read":       "view_file",
		"write":      "write_to_file",
		"edit":       "replace_file_content",
		"bash":       "run_command",
		"grep":       "grep_search",
		"glob":       "list_dir",
		"task":       "invoke_subagent",
		"ask":        "ask_question",
		"todo":       "manage_task",
		"hub":        "send_message",
		"eval":       "execute_code",
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

	// Cloaked tool names detection
	cloakedTargets := []string{"view_file", "run_command", "replace_file_content", "write_to_file", "grep_search", "list_dir", "invoke_subagent", "ask_question", "manage_task", "send_message", "execute_code", "search_web"}
	cloakedClient := detectCloakedClient(cloakedTargets)
	if cloakedClient != "oh_my_pi" {
		t.Fatalf("detectCloakedClient(%v) = %q, want 'oh_my_pi'", cloakedTargets, cloakedClient)
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
		"{\"index\":0,\"function\":{\"name\":\"execute_code\"}}," +
		"{\"index\":1,\"function\":{\"name\":\"mcp__fs__read_file\"}}]}}]}\n\n")
	streamOut, changedStream := uncloakStreamChunk(chunk, cached)
	if !changedStream {
		t.Fatal("expected stream chunk to be uncloaked")
	}
	streamStr := string(streamOut)
	if !strings.Contains(streamStr, `"name":"eval"`) {
		t.Fatalf("expected execute_code restored to eval: %s", streamStr)
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
	reqBody := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}},{"type":"function","function":{"name":"Edit"}}],"messages":[]}`

	// Step 1: Header-init chunk (ChunkIndex == -1)
	initReq := &pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    "openai",
		OriginalRequest: []byte(reqBody),
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
	reqBody := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}],"messages":[]}`
	reqIDA := "stream-session-iso-A"
	reqIDB := "stream-session-iso-B"

	// Init session A
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqIDA,
		SourceFormat:    "openai",
		OriginalRequest: []byte(reqBody),
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
	}, "openai")

	// Init session B
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqIDB,
		SourceFormat:    "openai",
		OriginalRequest: []byte(reqBody),
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
	rewritten, changed := rewriteRequestBody(body, "openai")
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

func TestDetectCloakedClientOhMyPiStandardNineTools(t *testing.T) {
	// Standard default 9 tools sent by Oh My Pi after cloaking
	ompCloakedTools := []string{
		"view_file", "write_to_file", "replace_file_content",
		"run_command", "grep_search", "list_dir",
		"invoke_subagent", "ask_question", "manage_task",
	}
	got := detectCloakedClient(ompCloakedTools)
	if got != "oh_my_pi" {
		t.Fatalf("detectCloakedClient(ompCloakedTools) = %q, want 'oh_my_pi'", got)
	}
}

func TestHandleRequestAndStreamUncloakRoundTripOhMyPi(t *testing.T) {
	const reqID = "omp-roundtrip-test-1"
	reqPayload := `{"model":"agy/gemini-3.7-flash","stream":true,"messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"bash","description":"run bash"}},{"type":"function","function":{"name":"read","description":"read file"}},{"type":"function","function":{"name":"edit","description":"edit file"}},{"type":"function","function":{"name":"write","description":"write file"}},{"type":"function","function":{"name":"grep","description":"search"}},{"type":"function","function":{"name":"glob","description":"find"}},{"type":"function","function":{"name":"task","description":"subtask"}},{"type":"function","function":{"name":"ask","description":"ask"}},{"type":"function","function":{"name":"todo","description":"task"}}],"stream":true}`

	reqJSON, _ := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   "openai",
		Model:          "agy/gemini-3.7-flash",
		RequestedModel: "agy/gemini-3.7-flash",
		Body:           []byte(reqPayload),
	})

	// 1. Request Intercept Before
	respEnv := handleRequestInterceptBefore(reqJSON)
	var reqEnv struct {
		OK     bool `json:"ok"`
		Result struct {
			Body []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respEnv, &reqEnv); err != nil {
		t.Fatalf("unmarshal request resp: %v", err)
	}
	if !strings.Contains(string(reqEnv.Result.Body), "run_command") {
		t.Fatalf("expected request tools cloaked to run_command: %s", string(reqEnv.Result.Body))
	}

	// 2. Stream Header Init (ChunkIndex = -1)
	initJSON, _ := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:       reqID,
		SourceFormat:    "openai",
		Model:           "agy/gemini-3.7-flash",
		RequestedModel:  "agy/gemini-3.7-flash",
		ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
		OriginalRequest: reqEnv.Result.Body,
		RequestBody:     reqEnv.Result.Body,
	})
	handleStreamChunkIntercept(initJSON)

	// 3. Stream Payload Chunk with tool_call "run_command"
	payloadChunk := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"command\\\":\\\"ls\\\"}\"}}]}}]}\n\n"
	chunkJSON, _ := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   "openai",
		Model:          "agy/gemini-3.7-flash",
		RequestedModel: "agy/gemini-3.7-flash",
		ChunkIndex:     0,
		Body:           []byte(payloadChunk),
	})
	streamEnv := handleStreamChunkIntercept(chunkJSON)
	var streamEnvResp struct {
		OK     bool `json:"ok"`
		Result struct {
			Body []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(streamEnv, &streamEnvResp); err != nil {
		t.Fatalf("unmarshal stream chunk resp: %v", err)
	}
	streamOut := string(streamEnvResp.Result.Body)
	if strings.Contains(streamOut, "run_command") {
		t.Fatalf("run_command leaked through stream without uncloaking: %s", streamOut)
	}
	if !strings.Contains(streamOut, `"name":"bash"`) && !strings.Contains(streamOut, `"name": "bash"`) {
		t.Fatalf("expected run_command uncloaked to bash: %s", streamOut)
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
		"system": "You have access to functions:read and functions:todo.",
		"tools": [
			{"type": "function", "function": {"name": "functions:read", "description": "Read file"}},
			{"type": "function", "function": {"name": "functions:todo", "description": "Manage tasks"}},
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
	if t1 != "functions:manage_task" {
		t.Errorf("t1 name = %q, want functions:manage_task", t1)
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
	if strings.Contains(sys, "functions:read") || strings.Contains(sys, "functions:todo") {
		t.Errorf("system prompt leaked original names: %s", sys)
	}
}

func TestUncloakResponseBodyWithNamespacePrefix(t *testing.T) {
	uncloakTable := map[string]string{
		"view_file":    "read",
		"manage_task":  "todo",
		"run_command":  "bash",
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
	cloakTable := map[string]string{
		"read":  "write",
		"write": "edit",
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
