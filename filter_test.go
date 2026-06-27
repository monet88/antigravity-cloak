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
		{"antigravity by ask_permission", []string{"ask_permission", "run_command"}, "antigravity"},
		{"antigravity by invoke_subagent", []string{"invoke_subagent", "view_file"}, "antigravity"},
		{"unknown tools", []string{"custom_tool", "another_tool"}, ""},
		{"empty list", []string{}, ""},
		// Edge case: Antigravity tools mixed with Claude-like names → Antigravity wins
		{"antigravity mixed with claude-like", []string{"bash", "ask_permission"}, "antigravity"},
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


