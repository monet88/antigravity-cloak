package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	issue71ScanCoordinatorCommentOMP = "<!-- Derived from openai/codex-security f22d4a36f26d16287bcdfd707b369116e02a08c3: sdk/typescript/_bundled_plugin/skills/security-scan/SKILL.md and finding-discovery/SKILL.md. Ported to OMP AgentSession/task semantics; Codex workspace, plugin, app-server, and CODEX_HOME instructions intentionally omitted. -->"
	issue71RatchetAttribution        = "Adapted from the claude-api skill's build-eval, eval-audit, and hillclimb guides (Apache-2.0, github.com/anthropics/skills)."
)

func issue71AllNinePresetGroupsPayload() string {
	return strings.Join([]string{
		"- keep-before",
		"- omp://: harness docs, AVOID unless asked.",
		"- keep-between\r",
		"- omp://: harness docs, AVOID unless asked.\r",
		"- keep-after",
		"Bare fallback: omp://: harness docs, AVOID unless asked. Done.",
		"Read project docs from AGENTS.md, `CLAUDE.md`, `.cursorrules` first.",
		"Scan via Codex Security cloud with your ChatGPT OAuth account.",
		issue71RatchetAttribution,
		"Claude usage limit reached; hand off remaining items.",
		"Check project-specific `CLAUDE.md` guidance and `CLAUDE.md` coding norms.",
		issue71ScanCoordinatorCommentOMP,
		"Use task to delegate.",
	}, "\n")
}

func issue71AllNinePresetGroupsExpected(taskCloaked bool) string {
	lastLine := "Use task to delegate."
	if taskCloaked {
		lastLine = "Use invoke_subagent to delegate."
	}
	return strings.Join([]string{
		"- keep-before",
		"- keep-between\r",
		"- keep-after",
		"Bare fallback:  Done.",
		"Read project docs from AGENTS.md first.",
		"Scan via remote security cloud with your remote OAuth account.",
		"",
		"Usage limit reached; hand off remaining items.",
		"Check project-specific `AGENTS.md` guidance and `AGENTS.md` coding norms.",
		"",
		lastLine,
	}, "\n")
}

func TestIssue71_BuiltInV19PresetsApplyAcrossAllHarnessSurfaces(t *testing.T) {
	rawPayload := issue71AllNinePresetGroupsPayload()
	mixedCasePayload := strings.Join([]string{
		"- OMP://: HARNESS DOCS, AVOID UNLESS ASKED.\n- retained",
		"Context: AGENTS.md, `claude.md`, `.CURSORRULES`.",
		"Cloud: CODEX SECURITY CLOUD / chatgpt oauth account.",
		"Notice: CLAUDE USAGE LIMIT REACHED; wrap up.",
		"Architect: PROJECT-SPECIFIC `claude.md` AND `claude.md` CODING.",
	}, "\n")
	wantMixedCase := strings.Join([]string{
		"- retained",
		"Context: AGENTS.md.",
		"Cloud: remote security cloud / remote OAuth account.",
		"Notice: Usage limit reached; wrap up.",
		"Architect: project-specific `AGENTS.md` and `AGENTS.md` coding.",
	}, "\n")

	makeTool := func(format, desc string) map[string]any {
		schema := map[string]any{
			"type":        "object",
			"title":       desc,
			"description": desc,
			"required":    []any{"Codex Security cloud"},
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"title":       mixedCasePayload,
					"description": desc,
					"enum":        []any{"Codex Security cloud", "ChatGPT OAuth account"},
					"const":       "Claude usage limit reached;",
					"default":     ", `CLAUDE.md`, `.cursorrules`",
				},
			},
		}
		if format == "openai" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "task",
					"description": desc,
					"parameters":  schema,
				},
			}
		}
		return map[string]any{
			"name":         "task",
			"description":  desc,
			"input_schema": schema,
		}
	}

	for _, tc := range []struct {
		name               string
		format             string
		model              string
		cfgYAML            string
		headers            http.Header
		expectToolCloaking bool
		expectConvBrand    string // expected replacement of OMP in conversation turns ("", "Antigravity", "ANTIGRAVITY")
	}{
		{
			name:   "openai-protected-agy-prefix-default-keywords-on",
			format: "openai",
			model:  "agy/gemini-3.8-flash",
			cfgYAML: `
use_default_keywords: true
model_prefixes:
  - "agy/"
`,
			headers:            http.Header{"X-Cloak-Client": {"oh_my_pi"}},
			expectToolCloaking: true,
			expectConvBrand:    "Antigravity",
		},
		{
			name:   "anthropic-protected-agy-prefix-default-keywords-on",
			format: "anthropic",
			model:  "agy/claude-sonnet-4-6",
			cfgYAML: `
use_default_keywords: true
model_prefixes:
  - "agy/"
`,
			headers:            http.Header{"X-Cloak-Client": {"oh_my_pi"}},
			expectToolCloaking: true,
			expectConvBrand:    "Antigravity",
		},
		{
			name:   "anthropic-protected-agy-empty-prefixes-default-keywords-off",
			format: "anthropic",
			model:  "claude-sonnet-4-6",
			cfgYAML: `
use_default_keywords: false
model_prefixes: []
`,
			headers:            http.Header{"X-Cloak-Client": {"oh_my_pi"}},
			expectToolCloaking: true,
			expectConvBrand:    "Antigravity",
		},
		{
			name:   "openai-ua-omp-prefix-default-keywords-on",
			format: "openai",
			model:  "agy/gemini-3.8-flash",
			cfgYAML: `
use_default_keywords: true
model_prefixes:
  - "agy/"
`,
			headers:            http.Header{"User-Agent": {"omp/19.2.1"}},
			expectToolCloaking: true,
			expectConvBrand:    "ANTIGRAVITY",
		},
		{
			name:   "anthropic-ua-omp-empty-prefixes-default-keywords-on",
			format: "anthropic",
			model:  "claude-sonnet-4-6",
			cfgYAML: `
use_default_keywords: true
model_prefixes: []
`,
			headers:            http.Header{"User-Agent": {"omp/19.2.1"}},
			expectToolCloaking: true,
			expectConvBrand:    "ANTIGRAVITY",
		},
		{
			name:   "openai-ua-omp-empty-prefixes-default-keywords-off-prebrand-comment",
			format: "openai",
			model:  "gemini-3.8-flash",
			cfgYAML: `
use_default_keywords: false
model_prefixes: []
`,
			headers:            http.Header{"User-Agent": {"omp/19.2.1"}},
			expectToolCloaking: true,
			expectConvBrand:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			reconfigurePluginYAML(t, tc.cfgYAML)

			reqID := "issue71-presets-" + tc.name
			defer completePluginRequest(t, reqID)

			wantHarness := issue71AllNinePresetGroupsExpected(tc.expectToolCloaking)
			userTyped := "User typed verbatim:\n" + rawPayload
			reminderUser := userTyped + "\n<system-reminder>" + rawPayload + "</system-reminder>"

			messages := []any{
				map[string]any{"role": "system", "content": rawPayload},
				map[string]any{"role": "developer", "content": rawPayload},
				map[string]any{"role": "user", "content": reminderUser},
				map[string]any{"role": "assistant", "content": rawPayload},
			}
			if tc.format == "openai" {
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": "call_1",
					"name":         "task",
					"content":      rawPayload,
				})
			} else {
				messages = append(messages, map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "text", "text": userTyped + "\n<system-reminder>" + mixedCasePayload + "</system-reminder>"},
						map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": rawPayload},
					},
				})
			}

			root := map[string]any{
				"system":   rawPayload,
				"messages": messages,
				"tools":    []any{makeTool(tc.format, rawPayload)},
			}

			res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
				t,
				pluginabi.MethodRequestInterceptBefore,
				pluginapi.RequestInterceptRequest{
					RequestID:      reqID,
					SourceFormat:   tc.format,
					Model:          tc.model,
					RequestedModel: tc.model,
					Headers:        tc.headers,
					Body:           mustMarshalJSONBytes(t, root),
				},
			)
			if res.Terminate || len(res.Body) == 0 {
				t.Fatalf("unexpected rejection or unchanged body: terminate=%t body=%s resp=%s", res.Terminate, res.Body, res.ResponseBody)
			}

			var got map[string]any
			mustUnmarshalJSON(t, res.Body, &got)

			// 1. Top-level system (Harness-Authored Surface).
			if got["system"] != wantHarness {
				t.Errorf("system mismatch:\ngot  %q\nwant %q", got["system"], wantHarness)
			}

			// 2. system & developer messages (Harness-Authored Surfaces).
			gotMsgs := got["messages"].([]any)
			for idx, role := range []string{"system", "developer"} {
				if content := gotMsgs[idx].(map[string]any)["content"]; content != wantHarness {
					t.Errorf("%s message mismatch:\ngot  %q\nwant %q", role, content, wantHarness)
				}
			}

			// 3. user turn: typed text untouched, <system-reminder> sanitized + aliased.
			wantUserReminder := userTyped + "\n<system-reminder>" + wantHarness + "</system-reminder>"
			if gotUser := gotMsgs[2].(map[string]any)["content"]; gotUser != wantUserReminder {
				t.Errorf("user <system-reminder> mismatch:\ngot  %q\nwant %q", gotUser, wantUserReminder)
			}

			// 4. tool description + schema description/title sanitized; schema literals preserved.
			gotTool := got["tools"].([]any)[0].(map[string]any)
			var toolDesc string
			var schema map[string]any
			if tc.format == "openai" {
				fn := gotTool["function"].(map[string]any)
				toolDesc = fn["description"].(string)
				schema = fn["parameters"].(map[string]any)
			} else {
				toolDesc = gotTool["description"].(string)
				schema = gotTool["input_schema"].(map[string]any)
			}
			if toolDesc != wantHarness {
				t.Errorf("tool.description mismatch:\ngot  %q\nwant %q", toolDesc, wantHarness)
			}
			if schema["title"] != wantHarness || schema["description"] != wantHarness {
				t.Errorf("schema top-level title/description mismatch:\ntitle=%q\ndesc =%q\nwant =%q", schema["title"], schema["description"], wantHarness)
			}
			modeProp := schema["properties"].(map[string]any)["mode"].(map[string]any)
			if modeProp["title"] != wantMixedCase {
				t.Errorf("schema mode.title mixed-case mismatch:\ngot  %q\nwant %q", modeProp["title"], wantMixedCase)
			}
			if modeProp["description"] != wantHarness {
				t.Errorf("schema mode.description mismatch:\ngot  %q\nwant %q", modeProp["description"], wantHarness)
			}
			if modeProp["const"] != "Claude usage limit reached;" || modeProp["default"] != ", `CLAUDE.md`, `.cursorrules`" {
				t.Errorf("schema literal fields mutated: const=%q default=%q", modeProp["const"], modeProp["default"])
			}
			if gotEnum := modeProp["enum"].([]any); len(gotEnum) != 2 || gotEnum[0] != "Codex Security cloud" || gotEnum[1] != "ChatGPT OAuth account" {
				t.Errorf("schema enum mutated: %#v", gotEnum)
			}
			if gotReq := schema["required"].([]any); len(gotReq) != 1 || gotReq[0] != "Codex Security cloud" {
				t.Errorf("schema required mutated: %#v", gotReq)
			}

			// 5. Machine-Generated Conversation Turns (assistant, role:tool / tool_result)
			// must NEVER run one-way Inbound Sanitization.
			checkNoSanitization := func(label, gotText string) {
				t.Helper()
				for _, kept := range []string{
					"- omp://: harness docs, AVOID unless asked.",
					", `CLAUDE.md`, `.cursorrules`",
					"Codex Security cloud",
					"ChatGPT OAuth account",
					issue71RatchetAttribution,
					"Claude usage limit reached;",
					"project-specific `CLAUDE.md`",
					"and `CLAUDE.md` coding",
					"Derived from openai/codex-security f22d4a36f26d16287bcdfd707b369116e02a08c3",
				} {
					if !strings.Contains(gotText, kept) {
						t.Errorf("%s unexpectedly sanitized %q; got:\n%s", label, kept, gotText)
					}
				}
				if tc.expectConvBrand != "" && !strings.Contains(gotText, "Ported to "+tc.expectConvBrand+" AgentSession/") {
					t.Errorf("%s expected brand parity %q in conversation turn; got:\n%s", label, tc.expectConvBrand, gotText)
				}
			}
			checkNoSanitization("assistant", gotMsgs[3].(map[string]any)["content"].(string))
			if tc.format == "openai" {
				checkNoSanitization("role:tool", gotMsgs[4].(map[string]any)["content"].(string))
			} else {
				blocks := gotMsgs[4].(map[string]any)["content"].([]any)
				wantBlock0 := userTyped + "\n<system-reminder>" + wantMixedCase + "</system-reminder>"
				if gotBlock0 := blocks[0].(map[string]any)["text"]; gotBlock0 != wantBlock0 {
					t.Errorf("anthropic user mixed-case <system-reminder> mismatch:\ngot  %q\nwant %q", gotBlock0, wantBlock0)
				}
				checkNoSanitization("anthropic tool_result", blocks[1].(map[string]any)["content"].(string))
			}
		})
	}
}

func TestIssue71_OperatorOverridesNewPresetsCaseInsensitivelyWithoutCascade(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "codex security cloud": "Operator Codex Security cloud via ChatGPT OAuth account"
  ", `+"`claude.md`"+`, `+"`.cursorrules`"+`": ", `+"`CUSTOM.md`"+`"
  "- omp://: harness docs, avoid unless asked.\n": "- custom harness entry\n"
`)

	for _, tc := range []struct {
		name    string
		headers http.Header
	}{
		{name: "protected-agy", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}},
		{name: "ua-omp", headers: http.Header{"User-Agent": {"omp/19.2.1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqID := "issue71-override-" + tc.name
			defer completePluginRequest(t, reqID)

			in := strings.Join([]string{
				"- omp://: harness docs, AVOID unless asked.",
				"Docs: AGENTS.md, `CLAUDE.md`, `.cursorrules`.",
				"Cloud: Codex Security cloud.",
				"Direct OAuth: ChatGPT OAuth account.",
				"Limit: Claude usage limit reached; stop.",
			}, "\n")
			want := strings.Join([]string{
				"- custom harness entry",
				"Docs: AGENTS.md, `CUSTOM.md`.",
				// Replacement contains both its own match and "ChatGPT OAuth account" —
				// neither may cascade within the pass.
				"Cloud: Operator Codex Security cloud via ChatGPT OAuth account.",
				// Unoverridden built-in presets still fire on their own spans.
				"Direct OAuth: remote OAuth account.",
				"Limit: Usage limit reached; stop.",
			}, "\n")

			root := map[string]any{
				"messages": []any{
					map[string]any{"role": "developer", "content": in},
					map[string]any{"role": "user", "content": "keep\n<system-reminder>" + in + "</system-reminder>"},
				},
			}
			res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
				t,
				pluginabi.MethodRequestInterceptBefore,
				pluginapi.RequestInterceptRequest{
					RequestID:      reqID,
					SourceFormat:   "openai",
					Model:          "agy/gemini-3.8-flash",
					RequestedModel: "agy/gemini-3.8-flash",
					Headers:        tc.headers,
					Body:           mustMarshalJSONBytes(t, root),
				},
			)
			var got map[string]any
			mustUnmarshalJSON(t, res.Body, &got)
			msgs := got["messages"].([]any)
			if gotDev := msgs[0].(map[string]any)["content"]; gotDev != want {
				t.Errorf("developer override mismatch:\ngot  %q\nwant %q", gotDev, want)
			}
			if gotUser := msgs[1].(map[string]any)["content"]; gotUser != "keep\n<system-reminder>"+want+"</system-reminder>" {
				t.Errorf("user reminder override mismatch:\ngot  %q\nwant %q", gotUser, "keep\n<system-reminder>"+want+"</system-reminder>")
			}
		})
	}
}

func TestIssue71_OperationalIdentifiersPreservedVerbatim(t *testing.T) {
	const (
		opBlock = "URIs: omp://docs/architecture.md and omp://xd/mcp__server_tool; " +
			"env: PI_BROWSER_RELAY, PI_BROWSER_WEBKIT; " +
			"dotfiles: .claude/rules/, .codex/, .cursor/."
		combinedWithSignatures = "- omp://: harness docs, AVOID unless asked.\n" +
			"Context files: AGENTS.md, `CLAUDE.md`, `.cursorrules`.\n" +
			opBlock + "\n" +
			"Global memory: ~/.claude/CLAUDE.md."
		wantCombined = "Context files: AGENTS.md.\n" +
			opBlock + "\n" +
			"Global memory: ~/.gemini/AGENTS.md."
	)

	makeTool := func(format, name, desc string) map[string]any {
		schema := map[string]any{
			"type":        "object",
			"description": desc,
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"title":       opBlock,
					"description": desc,
				},
			},
		}
		if format == "openai" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": desc,
					"parameters":  schema,
				},
			}
		}
		return map[string]any{
			"name":         name,
			"description":  desc,
			"input_schema": schema,
		}
	}

	for _, tc := range []struct {
		name    string
		format  string
		headers http.Header
	}{
		{name: "openai-protected-agy", format: "openai", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}},
		{name: "anthropic-protected-agy", format: "anthropic", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}},
		{name: "openai-ua-omp", format: "openai", headers: http.Header{"User-Agent": {"omp/19.2.1"}}},
		{name: "anthropic-ua-omp", format: "anthropic", headers: http.Header{"User-Agent": {"omp/19.2.1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)
			reqID := "issue71-opids-" + tc.name
			defer completePluginRequest(t, reqID)

			root := map[string]any{
				"system": combinedWithSignatures,
				"messages": []any{
					map[string]any{"role": "developer", "content": combinedWithSignatures},
					map[string]any{"role": "user", "content": "typed\n<system-reminder>" + combinedWithSignatures + "</system-reminder>"},
				},
				"tools": []any{
					makeTool(tc.format, "find", combinedWithSignatures),
					makeTool(tc.format, "glob", combinedWithSignatures),
				},
			}

			res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
				t,
				pluginabi.MethodRequestInterceptBefore,
				pluginapi.RequestInterceptRequest{
					RequestID:      reqID,
					SourceFormat:   tc.format,
					Model:          "agy/gemini-3.8-flash",
					RequestedModel: "agy/gemini-3.8-flash",
					Headers:        tc.headers,
					Body:           mustMarshalJSONBytes(t, root),
				},
			)
			if res.Terminate || len(res.Body) == 0 {
				t.Fatalf("unexpected rejection/no-op: terminate=%t resp=%s", res.Terminate, res.ResponseBody)
			}

			var got map[string]any
			mustUnmarshalJSON(t, res.Body, &got)

			if got["system"] != wantCombined {
				t.Errorf("system operational identifiers mismatch:\ngot  %q\nwant %q", got["system"], wantCombined)
			}
			msgs := got["messages"].([]any)
			if gotDev := msgs[0].(map[string]any)["content"]; gotDev != wantCombined {
				t.Errorf("developer operational identifiers mismatch:\ngot  %q\nwant %q", gotDev, wantCombined)
			}
			if gotUser := msgs[1].(map[string]any)["content"]; gotUser != "typed\n<system-reminder>"+wantCombined+"</system-reminder>" {
				t.Errorf("user <system-reminder> operational identifiers mismatch:\ngot  %q\nwant %q", gotUser, "typed\n<system-reminder>"+wantCombined+"</system-reminder>")
			}
			for i, tRaw := range got["tools"].([]any) {
				tMap := tRaw.(map[string]any)
				var desc string
				var schema map[string]any
				if tc.format == "openai" {
					fn := tMap["function"].(map[string]any)
					desc = fn["description"].(string)
					schema = fn["parameters"].(map[string]any)
				} else {
					desc = tMap["description"].(string)
					schema = tMap["input_schema"].(map[string]any)
				}
				if desc != wantCombined {
					t.Errorf("tool[%d].description mismatch:\ngot  %q\nwant %q", i, desc, wantCombined)
				}
				patternProp := schema["properties"].(map[string]any)["pattern"].(map[string]any)
				if patternProp["title"] != opBlock || patternProp["description"] != wantCombined {
					t.Errorf("tool[%d] schema pattern mismatch: title=%q desc=%q", i, patternProp["title"], patternProp["description"])
				}
			}
		})
	}
}

func TestIssue71_NonReversalAndRoundTripPreserved(t *testing.T) {
	const sanitizedProse = "remote security cloud, remote OAuth account, Usage limit reached; project-specific `AGENTS.md` and `AGENTS.md` coding"

	t.Run("openai_non_stream_and_sse", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)
		const reqID = "issue71-roundtrip-openai"
		defer completePluginRequest(t, reqID)

		reqBody := mustMarshalJSONBytes(t, map[string]any{
			"messages": []any{
				map[string]any{"role": "developer", "content": "Oh My Pi uses " + issue71AllNinePresetGroupsPayload() + "\nPaths: ~/.omp/agent/skills and ~/.claude/CLAUDE.md"},
			},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}},
				map[string]any{"type": "function", "function": map[string]any{"name": "bash", "parameters": map[string]any{"type": "object"}}},
			},
		})
		reqRes := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
			t,
			pluginabi.MethodRequestInterceptBefore,
			pluginapi.RequestInterceptRequest{
				RequestID:      reqID,
				SourceFormat:   "openai",
				Model:          "agy/gemini-3.8-flash",
				RequestedModel: "agy/gemini-3.8-flash",
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           reqBody,
			},
		)
		if reqRes.Terminate || len(reqRes.Body) == 0 {
			t.Fatalf("ProtectedAGY admission failed: terminate=%t resp=%s", reqRes.Terminate, reqRes.ResponseBody)
		}

		// Non-stream JSON response: sanitized strings stay verbatim while 2-way brand + tools reverse.
		upstreamJSON := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Antigravity checked ~/.gemini/agent/skills, ~/.gemini/AGENTS.md, and ` + sanitizedProse + `","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"~/.gemini/AGENTS.md\"}"}},{"id":"c2","type":"function","function":{"name":"run_command","arguments":"{\"cmd\":\"ls ~/.gemini/agent/skills\"}"}}]}}]}`)
		respOut := callPluginABI[pluginapi.ResponseInterceptRequest, pluginapi.ResponseInterceptResponse](
			t,
			pluginabi.MethodResponseInterceptAfter,
			pluginapi.ResponseInterceptRequest{
				RequestID:    reqID,
				SourceFormat: "openai",
				Model:        "agy/gemini-3.8-flash",
				Body:         upstreamJSON,
			},
		)
		var restored map[string]any
		mustUnmarshalJSON(t, respOut.Body, &restored)
		msg := restored["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		wantContent := "omp checked ~/.omp/agent/skills, ~/.claude/CLAUDE.md, and " + sanitizedProse
		if msg["content"] != wantContent {
			t.Fatalf("OpenAI non-stream content mismatch:\ngot  %q\nwant %q", msg["content"], wantContent)
		}
		calls := msg["tool_calls"].([]any)
		c0 := calls[0].(map[string]any)["function"].(map[string]any)
		c1 := calls[1].(map[string]any)["function"].(map[string]any)
		if c0["name"] != "read" || c0["arguments"] != `{"path":"~/.claude/CLAUDE.md"}` {
			t.Fatalf("OpenAI non-stream tool_call[0] mismatch: %#v", c0)
		}
		if c1["name"] != "bash" || c1["arguments"] != `{"cmd":"ls ~/.omp/agent/skills"}` {
			t.Fatalf("OpenAI non-stream tool_call[1] mismatch: %#v", c1)
		}

		// SSE stream: sanitized strings never reverse while split brand & tool names restore.
		chunks := [][]byte{
			[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Anti\"}}]}\n\n"),
			[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"gravity: " + sanitizedProse + "\"}}]}\n\n"),
			[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"~/.gemini/AGENTS.md\\\"}\"}}]}}]}\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
		var wire []byte
		for i, chunk := range chunks {
			stRes := callPluginABI[pluginapi.StreamChunkInterceptRequest, pluginapi.StreamChunkInterceptResponse](
				t,
				pluginabi.MethodResponseInterceptStreamChunk,
				pluginapi.StreamChunkInterceptRequest{
					RequestID:    reqID,
					SourceFormat: "openai",
					Model:        "agy/gemini-3.8-flash",
					ChunkIndex:   i,
					Body:         chunk,
				},
			)
			wire = appendStreamResult(wire, chunk, stRes)
		}
		wireStr := string(wire)
		for _, wantSub := range []string{"omp: " + sanitizedProse, `"name":"read"`, `~/.claude/CLAUDE.md`} {
			if !strings.Contains(wireStr, wantSub) {
				t.Errorf("OpenAI SSE missing %q in:\n%s", wantSub, wireStr)
			}
		}
		for _, forbidden := range []string{"Codex Security cloud", "ChatGPT OAuth account", "Claude usage limit reached;", "project-specific `CLAUDE.md`", "and `CLAUDE.md` coding"} {
			if strings.Contains(wireStr, forbidden) {
				t.Errorf("OpenAI SSE reversed one-way sanitized string %q in:\n%s", forbidden, wireStr)
			}
		}
	})

	t.Run("anthropic_non_stream_and_sse", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)
		const reqID = "issue71-roundtrip-anthropic"
		defer completePluginRequest(t, reqID)

		reqBody := mustMarshalJSONBytes(t, map[string]any{
			"system": "Oh My Pi uses " + issue71AllNinePresetGroupsPayload() + "\nPaths: ~/.omp/agent/skills and ~/.claude/CLAUDE.md",
			"messages": []any{
				map[string]any{"role": "user", "content": "proceed"},
			},
			"tools": []any{
				map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}},
				map[string]any{"name": "bash", "input_schema": map[string]any{"type": "object"}},
			},
		})
		reqRes := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
			t,
			pluginabi.MethodRequestInterceptBefore,
			pluginapi.RequestInterceptRequest{
				RequestID:      reqID,
				SourceFormat:   "anthropic",
				Model:          "agy/claude-sonnet-4-6",
				RequestedModel: "agy/claude-sonnet-4-6",
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           reqBody,
			},
		)
		if reqRes.Terminate || len(reqRes.Body) == 0 {
			t.Fatalf("ProtectedAGY anthropic admission failed: terminate=%t resp=%s", reqRes.Terminate, reqRes.ResponseBody)
		}

		// Non-stream Anthropic JSON response.
		upstreamJSON := []byte(`{"content":[{"type":"text","text":"Antigravity checked ~/.gemini/AGENTS.md and ` + sanitizedProse + `"},{"type":"tool_use","id":"tu_1","name":"read_file","input":{"path":"~/.gemini/agent/skills"}}]}`)
		respOut := callPluginABI[pluginapi.ResponseInterceptRequest, pluginapi.ResponseInterceptResponse](
			t,
			pluginabi.MethodResponseInterceptAfter,
			pluginapi.ResponseInterceptRequest{
				RequestID:    reqID,
				SourceFormat: "anthropic",
				Model:        "agy/claude-sonnet-4-6",
				Body:         upstreamJSON,
			},
		)
		var restored map[string]any
		mustUnmarshalJSON(t, respOut.Body, &restored)
		blocks := restored["content"].([]any)
		wantText := "omp checked ~/.claude/CLAUDE.md and " + sanitizedProse
		if blocks[0].(map[string]any)["text"] != wantText {
			t.Fatalf("Anthropic non-stream text mismatch:\ngot  %q\nwant %q", blocks[0].(map[string]any)["text"], wantText)
		}
		tu := blocks[1].(map[string]any)
		if tu["name"] != "read" || tu["input"].(map[string]any)["path"] != "~/.omp/agent/skills" {
			t.Fatalf("Anthropic non-stream tool_use mismatch: %#v", tu)
		}

		// Anthropic SSE stream.
		chunks := [][]byte{
			[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Anti\"}}\n\n"),
			[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"gravity: " + sanitizedProse + "\"}}\n\n"),
			[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"run_command\"}}\n\n"),
			[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"~/.gemini/AGENTS.md\\\"}\"}}\n\n"),
			[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"),
			[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		}
		var wire []byte
		for i, chunk := range chunks {
			stRes := callPluginABI[pluginapi.StreamChunkInterceptRequest, pluginapi.StreamChunkInterceptResponse](
				t,
				pluginabi.MethodResponseInterceptStreamChunk,
				pluginapi.StreamChunkInterceptRequest{
					RequestID:    reqID,
					SourceFormat: "anthropic",
					Model:        "agy/claude-sonnet-4-6",
					ChunkIndex:   i,
					Body:         chunk,
				},
			)
			wire = appendStreamResult(wire, chunk, stRes)
		}
		wireStr := string(wire)
		for _, wantSub := range []string{"omp: " + sanitizedProse, `"name":"bash"`, `~/.claude/CLAUDE.md`} {
			if !strings.Contains(wireStr, wantSub) {
				t.Errorf("Anthropic SSE missing %q in:\n%s", wantSub, wireStr)
			}
		}
		for _, forbidden := range []string{"Codex Security cloud", "ChatGPT OAuth account", "Claude usage limit reached;", "project-specific `CLAUDE.md`", "and `CLAUDE.md` coding"} {
			if strings.Contains(wireStr, forbidden) {
				t.Errorf("Anthropic SSE reversed one-way sanitized string %q in:\n%s", forbidden, wireStr)
			}
		}
	})
}
