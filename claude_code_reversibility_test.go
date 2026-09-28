package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Claude Code sends brand text that the forward rewrite renames onto Antigravity
// wording. Tool NAMES and brand TEXT are then on two different contracts:
//
//   - a tool name must come back exactly as the client spelled it, or the
//     client cannot dispatch a call it was told about;
//   - brand text is renamed for the model's benefit and is never mapped back.
//     Mapping it back would hand the client "CLAUDE.md" for a file the model
//     was legitimately told is called AGENTS.md.
//
// This pins both halves: the asymmetry is deliberate and easy to break by
// someone adding a symmetric reverse rule.
func TestClaudeCodeToolNamesReverseButBrandTextDoesNot(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	reqID := "req_cc_reversibility"
	reqBody := []byte(`{
		"system": "You are Claude Code. Update CLAUDE.md, built on the Claude Agent SDK, and drive the Workflow tool.",
		"messages": [{"role":"user","content":"go"}],
		"tools": [
			{"name":"Bash","description":"Edit CLAUDE.md"},
			{"name":"Read","description":""},
			{"name":"Write","description":""},
			{"name":"Edit","description":""},
			{"name":"Grep","description":""},
			{"name":"Glob","description":""},
			{"name":"Agent","description":""},
			{"name":"AskUserQuestion","description":""},
			{"name":"WebSearch","description":""},
			{"name":"WebFetch","description":""},
			{"name":"Workflow","description":"Run a workflow"}
		]
	}`)

	headers := http.Header{}
	headers.Set("X-Cloak-Client", "claude_code")
	rawEnvelope, code := handlePluginCall("request.intercept_before", makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, headers))
	if code != 0 {
		t.Fatalf("code=%d, envelope=%s", code, rawEnvelope)
	}
	forward := string(decodeEnvelopeBody(t, rawEnvelope))
	if forward == "" {
		t.Fatalf("forward rewrite produced an empty body, envelope=%s", rawEnvelope)
	}
	for _, leak := range []string{"CLAUDE.md", "Claude Agent SDK"} {
		if strings.Contains(forward, leak) {
			t.Fatalf("forward request still leaks %q: %s", leak, forward)
		}
	}
	if !strings.Contains(forward, "AGENTS.md") || !strings.Contains(forward, "teamwork_preview_layer") {
		t.Fatalf("forward request missing the Antigravity spellings: %s", forward)
	}

	// The model answers naming the cloaked tool and echoing the cloaked text.
	upstream := `{"content":[{"type":"tool_use","name":"wp_run_workflow","input":{}}],
		"text":"I updated AGENTS.md via the Antigravity SDK and will use teamwork_preview_layer."}`
	payload := responseInterceptRequestJSON(t, string(reqBody), upstream, "anthropic")
	var mm map[string]any
	if err := json.Unmarshal(payload, &mm); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}
	mm["RequestID"] = reqID
	mm["Model"] = "agy/claude-test"
	payload, _ = json.Marshal(mm)

	raw, _ := handlePluginCall("response.intercept_after", payload)
	back := string(decodeEnvelopeBody(t, raw))
	if back == "" {
		t.Fatal("response rewrite produced an empty body")
	}

	// Tool name: restored to the exact spelling the client declared.
	if !strings.Contains(back, `"name":"Workflow"`) {
		t.Fatalf("tool name not restored to Workflow: %s", back)
	}
	// Brand text: forward-only. Restoring it would make the client look for a
	// CLAUDE.md that no longer exists in the conversation the model was given.
	if strings.Contains(back, "CLAUDE.md") {
		t.Fatalf("brand text was reversed to CLAUDE.md: %s", back)
	}
	if !strings.Contains(back, "AGENTS.md") {
		t.Fatalf("AGENTS.md should survive the response unchanged: %s", back)
	}
}
