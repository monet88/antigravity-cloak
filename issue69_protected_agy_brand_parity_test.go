package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/monet88/antigravity-cloak/internal/spec"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var bareOMPWordRe = regexp.MustCompile(`(?i)\bomp\b`)

func callPluginABI[Req any, Resp any](t *testing.T, method string, req Req) Resp {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	raw, code := handlePluginCall(method, payload)
	if code != 0 {
		t.Fatalf("%s code = %d, want 0; body=%s", method, code, raw)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result Resp `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("%s returned ok=false: %s", method, raw)
	}
	return env.Result
}

func reconfigurePluginYAML(t *testing.T, yamlCfg string) {
	t.Helper()
	raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(yamlCfg)))
	if code != 0 {
		t.Fatalf("plugin.reconfigure code = %d, want 0; body=%s", code, raw)
	}
	var env struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("plugin.reconfigure ok=false: %s", raw)
	}
}

func completePluginRequest(t *testing.T, requestID string) {
	t.Helper()
	_ = callPluginABI[pluginapi.RequestCompletion, map[string]any](
		t,
		pluginabi.MethodRequestComplete,
		pluginapi.RequestCompletion{RequestID: requestID, Outcome: "succeeded"},
	)
}

func mustMarshalJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func TestIssue69_ProtectedAGYOpenAIConversationBrandParity(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reconfigurePluginYAML(t, `
use_default_keywords: false
model_prefixes:
  - "agy/"
custom_mappings:
  "operator-call": "operator-brand"
`)

	const reqID = "issue69-protected-openai-brand-parity"
	defer completePluginRequest(t, reqID)

	root := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "User typed omp and operator-call literally.\n<system-reminder>\nNotice from Oh My Pi (oh-my-pi / omp): run operator-call and use read, check ~/.omp/agent/settings.json, or read omp://docs/guide.md.\n</system-reminder>\nTrailing user omp text.",
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":          "text",
						"text":          "Typed omp outside\n<system-reminder>omp reminder with operator-call and omp://xd/mcp__srv_tool</system-reminder>",
						"cache_control": map[string]any{"type": "ephemeral", "note": "omp operator-call"},
					},
				},
			},
			map[string]any{
				"role":    "assistant",
				"content": "Oh My Pi can use read and operator-call; see omp://xd/mcp__srv_tool or ~/.omp/agent/skills.",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_omp_1",
						"type": "function",
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"omp status --via operator-call"}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"name":         "bash",
				"tool_call_id": "call_omp_1",
				"content":      "Hidden frames notice from omp: background-move and secure-input handled by oh-my-pi with operator-call; worktree isolation-error in ~/.omp/agent; URI omp://xd/mcp__srv_tool.",
			},
			map[string]any{
				"role":         "tool",
				"name":         "omp:grep",
				"tool_call_id": "call_omp_2",
				"content": []any{
					map[string]any{
						"type":          "text",
						"text":          "Oh My Pi runtime notice for omp and operator-call (keep omp://docs/api).",
						"cache_control": map[string]any{"type": "ephemeral", "note": "omp metadata"},
					},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "data:image/png;base64,omp-operator-call",
						},
					},
				},
			},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash", "parameters": map[string]any{"type": "object"}}},
		},
	}

	resp := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
		t,
		pluginabi.MethodRequestInterceptBefore,
		pluginapi.RequestInterceptRequest{
			RequestID:      reqID,
			SourceFormat:   "openai",
			Model:          "agy/gemini-3.8-flash",
			RequestedModel: "agy/gemini-3.8-flash",
			Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
			Body:           mustMarshalJSONBytes(t, root),
		},
	)
	if resp.Terminate {
		t.Fatalf("unexpected ProtectedAGY termination: %s", resp.ResponseBody)
	}

	var got map[string]any
	mustUnmarshalJSON(t, resp.Body, &got)
	msgs := got["messages"].([]any)

	// 1. Bare-string user turn: <system-reminder> branded + aliased; typed text verbatim.
	wantUser0 := "User typed omp and operator-call literally.\n<system-reminder>\nNotice from Antigravity (Antigravity / Antigravity): run operator-brand and use read_file, check ~/.gemini/agent/settings.json, or read omp://docs/guide.md.\n</system-reminder>\nTrailing user omp text."
	if gotUser0 := msgs[0].(map[string]any)["content"]; gotUser0 != wantUser0 {
		t.Fatalf("user[0] content mismatch:\ngot  %q\nwant %q", gotUser0, wantUser0)
	}

	// 2. Block-array user turn: <system-reminder> branded, typed text & cache_control verbatim.
	user1Block := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	wantUser1Text := "Typed omp outside\n<system-reminder>Antigravity reminder with operator-brand and omp://xd/mcp__srv_tool</system-reminder>"
	if user1Block["text"] != wantUser1Text {
		t.Fatalf("user[1] text mismatch:\ngot  %q\nwant %q", user1Block["text"], wantUser1Text)
	}
	if note := user1Block["cache_control"].(map[string]any)["note"]; note != "omp operator-call" {
		t.Fatalf("user[1] cache_control mutated: %q", note)
	}

	// 3. Assistant turn: text branded + aliased, tool_calls[].function.name cloaked, arguments verbatim.
	asstMsg := msgs[2].(map[string]any)
	wantAsst := "Antigravity can use read_file and operator-brand; see omp://xd/mcp__srv_tool or ~/.gemini/agent/skills."
	if asstMsg["content"] != wantAsst {
		t.Fatalf("assistant content mismatch:\ngot  %q\nwant %q", asstMsg["content"], wantAsst)
	}
	toolCallFn := asstMsg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if toolCallFn["name"] != "run_command" {
		t.Fatalf("assistant tool_call name = %q, want run_command", toolCallFn["name"])
	}
	if toolCallFn["arguments"] != `{"command":"omp status --via operator-call"}` {
		t.Fatalf("assistant tool_call arguments mutated: %q", toolCallFn["arguments"])
	}

	// 4. OpenAI role:"tool" string content: runtime notices branded, omp:// preserved, name cloaked.
	toolMsg1 := msgs[3].(map[string]any)
	if toolMsg1["name"] != "run_command" {
		t.Fatalf("tool[1] name = %q, want run_command", toolMsg1["name"])
	}
	if toolMsg1["tool_call_id"] != "call_omp_1" {
		t.Fatalf("tool[1] tool_call_id mutated: %q", toolMsg1["tool_call_id"])
	}
	wantTool1 := "Hidden frames notice from Antigravity: background-move and secure-input handled by Antigravity with operator-brand; worktree isolation-error in ~/.gemini/agent; URI omp://xd/mcp__srv_tool."
	if toolMsg1["content"] != wantTool1 {
		t.Fatalf("tool[1] content mismatch:\ngot  %q\nwant %q", toolMsg1["content"], wantTool1)
	}
	if bareOMPWordRe.MatchString(strings.ReplaceAll(toolMsg1["content"].(string), "omp://", "")) {
		t.Fatalf("tool[1] leaked bare omp: %q", toolMsg1["content"])
	}

	// 5. OpenAI role:"tool" block content: text block branded; name is owned by Tool Cloaking (prefix omp: preserved, base cloaked), cache_control/image_url verbatim.
	toolMsg2 := msgs[4].(map[string]any)
	if toolMsg2["name"] != "omp:grep_search" {
		t.Fatalf("tool[2] structural name = %q, want omp:grep_search", toolMsg2["name"])
	}
	tool2Blocks := toolMsg2["content"].([]any)
	tool2Text := tool2Blocks[0].(map[string]any)
	wantTool2Text := "Antigravity runtime notice for Antigravity and operator-brand (keep omp://docs/api)."
	if tool2Text["text"] != wantTool2Text {
		t.Fatalf("tool[2] text block mismatch:\ngot  %q\nwant %q", tool2Text["text"], wantTool2Text)
	}
	if note := tool2Text["cache_control"].(map[string]any)["note"]; note != "omp metadata" {
		t.Fatalf("tool[2] cache_control mutated: %q", note)
	}
	if imgURL := tool2Blocks[1].(map[string]any)["image_url"].(map[string]any)["url"]; imgURL != "data:image/png;base64,omp-operator-call" {
		t.Fatalf("tool[2] image_url mutated: %q", imgURL)
	}
}

func TestIssue69_ProtectedAGYAnthropicConversationBrandParity(t *testing.T) {
	t.Run("use_default_keywords_false_mandatory_aliases_and_custom_mappings", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: false
model_prefixes:
  - "agy/"
custom_mappings:
  "operator-call": "operator-brand"
`)

		const reqID = "issue69-protected-anthropic-default-off"
		defer completePluginRequest(t, reqID)

		root := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "assistant",
					"content": []any{
						map[string]any{
							"type":          "thinking",
							"thinking":      "Oh My Pi checked omp and operator-call; keep omp://docs/spec and <system-conventions> untouched by 1-way sanitization.",
							"signature":     "sig-omp-operator-call",
							"cache_control": map[string]any{"type": "ephemeral", "note": "omp-thinking"},
						},
						map[string]any{
							"type":          "text",
							"text":          "oh-my-pi will use read with operator-call in ~/.omp/agent/skills (see omp://xd/mcp__srv_tool).",
							"cache_control": map[string]any{"type": "ephemeral", "note": "omp-asst"},
						},
						map[string]any{
							"type":  "tool_use",
							"id":    "toolu_omp_1",
							"name":  "read",
							"input": map[string]any{"path": "omp/file.txt", "note": "operator-call"},
						},
					},
				},
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type":          "tool_result",
							"tool_use_id":   "toolu_omp_1",
							"cache_control": map[string]any{"type": "ephemeral", "note": "omp-result-outer"},
							"content":       "browser-hidden-frames & computer-background-move notice from omp: use read, operator-call ran, URI omp://xd/mcp__srv_tool, <system-conventions> verbatim.",
						},
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "toolu_omp_2",
							"content": []any{
								map[string]any{
									"type":          "text",
									"text":          "computer-secure-input & isolation-error from Oh My Pi (omp) with operator-call; ~/.omp/agent and omp://docs/guide.",
									"cache_control": map[string]any{"type": "ephemeral", "note": "omp-result-text"},
								},
								map[string]any{
									"type": "image",
									"source": map[string]any{
										"type":       "base64",
										"media_type": "image/png",
										"data":       "omp-operator-call-image-payload",
									},
								},
							},
						},
						map[string]any{
							"type":          "text",
							"text":          "Typed omp and operator-call.\n<system-reminder>First omp notice for operator-call.</system-reminder>\nMiddle typed omp.\n<system-reminder>Second oh-my-pi notice with omp://docs/ref.</system-reminder>\nFinal typed omp.\n<system-reminder>Unterminated omp tail",
							"cache_control": map[string]any{"type": "ephemeral", "note": "omp-user-text"},
						},
					},
				},
			},
			"tools": []any{
				map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}},
			},
		}

		resp := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
			t,
			pluginabi.MethodRequestInterceptBefore,
			pluginapi.RequestInterceptRequest{
				RequestID:      reqID,
				SourceFormat:   "anthropic",
				Model:          "agy/claude-sonnet-4",
				RequestedModel: "agy/claude-sonnet-4",
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           mustMarshalJSONBytes(t, root),
			},
		)
		if resp.Terminate {
			t.Fatalf("unexpected ProtectedAGY termination: %s", resp.ResponseBody)
		}

		var got map[string]any
		mustUnmarshalJSON(t, resp.Body, &got)
		msgs := got["messages"].([]any)

		// Assistant thinking + text + tool_use
		asstBlocks := msgs[0].(map[string]any)["content"].([]any)
		thinking := asstBlocks[0].(map[string]any)
		wantThinking := "Antigravity checked Antigravity and operator-brand; keep omp://docs/spec and <system-conventions> untouched by 1-way sanitization."
		if thinking["thinking"] != wantThinking {
			t.Fatalf("assistant thinking mismatch:\ngot  %q\nwant %q", thinking["thinking"], wantThinking)
		}
		if thinking["signature"] != "sig-omp-operator-call" {
			t.Fatalf("assistant thinking signature mutated: %q", thinking["signature"])
		}
		if note := thinking["cache_control"].(map[string]any)["note"]; note != "omp-thinking" {
			t.Fatalf("assistant thinking cache_control mutated: %q", note)
		}

		asstText := asstBlocks[1].(map[string]any)
		wantAsstText := "Antigravity will use read_file with operator-brand in ~/.gemini/agent/skills (see omp://xd/mcp__srv_tool)."
		if asstText["text"] != wantAsstText {
			t.Fatalf("assistant text mismatch:\ngot  %q\nwant %q", asstText["text"], wantAsstText)
		}
		if note := asstText["cache_control"].(map[string]any)["note"]; note != "omp-asst" {
			t.Fatalf("assistant text cache_control mutated: %q", note)
		}

		toolUse := asstBlocks[2].(map[string]any)
		if toolUse["name"] != "read_file" {
			t.Fatalf("tool_use name = %q, want read_file", toolUse["name"])
		}
		toolInput := toolUse["input"].(map[string]any)
		if toolInput["path"] != "omp/file.txt" || toolInput["note"] != "operator-call" {
			t.Fatalf("tool_use.input mutated: %#v", toolInput)
		}

		// User tool_result (string + block array) & <system-reminder> spans
		userBlocks := msgs[1].(map[string]any)["content"].([]any)
		res1 := userBlocks[0].(map[string]any)
		if res1["tool_use_id"] != "toolu_omp_1" {
			t.Fatalf("tool_result[0] tool_use_id mutated: %q", res1["tool_use_id"])
		}
		if note := res1["cache_control"].(map[string]any)["note"]; note != "omp-result-outer" {
			t.Fatalf("tool_result[0] cache_control mutated: %q", note)
		}
		wantRes1 := "browser-hidden-frames & computer-background-move notice from Antigravity: use read_file, operator-brand ran, URI omp://xd/mcp__srv_tool, <system-conventions> verbatim."
		if res1["content"] != wantRes1 {
			t.Fatalf("tool_result[0] content mismatch:\ngot  %q\nwant %q", res1["content"], wantRes1)
		}
		if bareOMPWordRe.MatchString(strings.ReplaceAll(res1["content"].(string), "omp://", "")) {
			t.Fatalf("tool_result[0] leaked bare omp: %q", res1["content"])
		}

		res2 := userBlocks[1].(map[string]any)
		if res2["tool_use_id"] != "toolu_omp_2" {
			t.Fatalf("tool_result[1] tool_use_id mutated: %q", res2["tool_use_id"])
		}
		res2Blocks := res2["content"].([]any)
		res2Text := res2Blocks[0].(map[string]any)
		wantRes2Text := "computer-secure-input & isolation-error from Antigravity (Antigravity) with operator-brand; ~/.gemini/agent and omp://docs/guide."
		if res2Text["text"] != wantRes2Text {
			t.Fatalf("tool_result[1] text block mismatch:\ngot  %q\nwant %q", res2Text["text"], wantRes2Text)
		}
		if bareOMPWordRe.MatchString(strings.ReplaceAll(res2Text["text"].(string), "omp://", "")) {
			t.Fatalf("tool_result[1] leaked bare omp: %q", res2Text["text"])
		}
		if note := res2Text["cache_control"].(map[string]any)["note"]; note != "omp-result-text" {
			t.Fatalf("tool_result[1] cache_control mutated: %q", note)
		}
		imgSource := res2Blocks[1].(map[string]any)["source"].(map[string]any)
		if imgSource["data"] != "omp-operator-call-image-payload" {
			t.Fatalf("tool_result[1] image data mutated: %q", imgSource["data"])
		}

		userText := userBlocks[2].(map[string]any)
		wantUserText := "Typed omp and operator-call.\n<system-reminder>First Antigravity notice for operator-brand.</system-reminder>\nMiddle typed omp.\n<system-reminder>Second Antigravity notice with omp://docs/ref.</system-reminder>\nFinal typed omp.\n<system-reminder>Unterminated Antigravity tail"
		if userText["text"] != wantUserText {
			t.Fatalf("user text block mismatch:\ngot  %q\nwant %q", userText["text"], wantUserText)
		}
		if note := userText["cache_control"].(map[string]any)["note"]; note != "omp-user-text" {
			t.Fatalf("user text cache_control mutated: %q", note)
		}
	})

	t.Run("use_default_keywords_true_applies_identity_line_and_claude_memory_path", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)

		const reqID = "issue69-protected-anthropic-default-on"
		defer completePluginRequest(t, reqID)

		root := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "text", "text": "You are omp's trusted coding assistant. Read ~/.claude/CLAUDE.md."},
					},
				},
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "tu_1",
							"content":     "Loaded ~/.claude/CLAUDE.md and ~/.omp/agent/AGENTS.md",
						},
						map[string]any{
							"type": "text",
							"text": "User typed ~/.claude/CLAUDE.md\n<system-reminder>Memory at ~/.claude/CLAUDE.md</system-reminder>",
						},
					},
				},
			},
			"tools": []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}},
		}

		resp := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
			t,
			pluginabi.MethodRequestInterceptBefore,
			pluginapi.RequestInterceptRequest{
				RequestID:      reqID,
				SourceFormat:   "anthropic",
				Model:          "agy/claude-sonnet-4",
				RequestedModel: "agy/claude-sonnet-4",
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           mustMarshalJSONBytes(t, root),
			},
		)
		if resp.Terminate {
			t.Fatalf("unexpected termination: %s", resp.ResponseBody)
		}
		var got map[string]any
		mustUnmarshalJSON(t, resp.Body, &got)
		msgs := got["messages"].([]any)

		asstText := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
		wantAsst := spec.AntigravityIdentity + " Read ~/.gemini/AGENTS.md."
		if asstText != wantAsst {
			t.Fatalf("assistant default brand mismatch:\ngot  %q\nwant %q", asstText, wantAsst)
		}

		userBlocks := msgs[1].(map[string]any)["content"].([]any)
		if gotRes := userBlocks[0].(map[string]any)["content"]; gotRes != "Loaded ~/.gemini/AGENTS.md and ~/.gemini/agent/AGENTS.md" {
			t.Fatalf("tool_result default brand mismatch: %q", gotRes)
		}
		wantUser := "User typed ~/.claude/CLAUDE.md\n<system-reminder>Memory at ~/.gemini/AGENTS.md</system-reminder>"
		if gotUser := userBlocks[1].(map[string]any)["text"]; gotUser != wantUser {
			t.Fatalf("user reminder default brand mismatch:\ngot  %q\nwant %q", gotUser, wantUser)
		}
	})

	t.Run("explicit_omp_non_agy_bypass_leaves_conversation_untouched", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)

		const reqID = "issue69-explicit-bypass"
		defer completePluginRequest(t, reqID)

		rawBody := mustMarshalJSONBytes(t, map[string]any{
			"messages": []any{
				map[string]any{"role": "assistant", "content": "Oh My Pi ran omp"},
				map[string]any{"role": "tool", "name": "bash", "content": "omp isolation-error"},
			},
			"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "bash"}}},
		})

		resp := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
			t,
			pluginabi.MethodRequestInterceptBefore,
			pluginapi.RequestInterceptRequest{
				RequestID:      reqID,
				SourceFormat:   "openai",
				Model:          "other/model",
				RequestedModel: "other/model",
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           rawBody,
			},
		)
		if resp.Terminate {
			t.Fatalf("unexpected termination on bypass: %s", resp.ResponseBody)
		}
		if len(resp.Body) != 0 && !bytes.Equal(resp.Body, rawBody) {
			t.Fatalf("ExplicitOMPNonAGYBypass mutated body: %s", resp.Body)
		}
		if len(resp.ClearHeaders) == 0 {
			t.Fatalf("ExplicitOMPNonAGYBypass did not clear X-Cloak-Client header")
		}
	})
}

func appendStreamResult(dst, input []byte, res pluginapi.StreamChunkInterceptResponse) []byte {
	if res.DropChunk {
		return dst
	}
	if len(res.Body) > 0 {
		return append(dst, res.Body...)
	}
	return append(dst, input...)
}

func TestIssue69_ProtectedAGYResponseAndStreamRoundTrip(t *testing.T) {
	t.Run("openai_response_and_fragmented_stream", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)

		const reqID = "issue69-roundtrip-openai"
		defer completePluginRequest(t, reqID)

		reqBody := mustMarshalJSONBytes(t, map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "check\n<system-reminder>omp uses todo and ~/.omp/agent/skills</system-reminder>"},
				map[string]any{"role": "assistant", "content": "Oh My Pi checked ~/.claude/CLAUDE.md"},
				map[string]any{"role": "tool", "name": "read", "content": "omp notice with omp://docs/a"},
			},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}},
				map[string]any{"type": "function", "function": map[string]any{"name": "todo", "parameters": map[string]any{"type": "object"}}},
			},
		})

		reqResp := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
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
		if reqResp.Terminate || len(reqResp.Body) == 0 {
			t.Fatalf("ProtectedAGY admission failed: terminate=%t body=%s", reqResp.Terminate, reqResp.ResponseBody)
		}

		// Non-stream JSON response restoration
		upstreamJSON := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Antigravity checked ~/.gemini/agent/skills, ~/.gemini/AGENTS.md, and https://antigravity.google/docs","tool_calls":[{"id":"c1","type":"function","function":{"name":"wp_todo","arguments":"{\"path\":\"~/.gemini/agent/skills\"}"}},{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"~/.gemini/AGENTS.md\"}"}}]}}]}`)
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
		wantContent := "omp checked ~/.omp/agent/skills, ~/.claude/CLAUDE.md, and https://antigravity.google/docs"
		if msg["content"] != wantContent {
			t.Fatalf("OpenAI response content mismatch:\ngot  %q\nwant %q", msg["content"], wantContent)
		}
		calls := msg["tool_calls"].([]any)
		call0 := calls[0].(map[string]any)["function"].(map[string]any)
		call1 := calls[1].(map[string]any)["function"].(map[string]any)
		if call0["name"] != "todo" || call0["arguments"] != `{"path":"~/.omp/agent/skills"}` {
			t.Fatalf("OpenAI response tool_call[0] mismatch: %#v", call0)
		}
		if call1["name"] != "read" || call1["arguments"] != `{"path":"~/.claude/CLAUDE.md"}` {
			t.Fatalf("OpenAI response tool_call[1] mismatch: %#v", call1)
		}

		// Fragmented SSE stream restoration (splitting both brand token and cloaked tool name)
		toolFrame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"wp_todo\",\"arguments\":\"{}\"}}]}}]}\n\n")
		cut := bytes.Index(toolFrame, []byte("wp_todo")) + len("wp_")
		chunks := [][]byte{
			[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Run Anti\"}}]}\n\n"),
			[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"gravity from ~/.gem\"}}]}\n\n"),
			[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ini/agent/skills\"}}]}\n\n"),
			toolFrame[:cut],
			toolFrame[cut:],
			[]byte("data: [DONE]\n\n"),
		}
		var wire []byte
		for i, chunk := range chunks {
			streamOut := callPluginABI[pluginapi.StreamChunkInterceptRequest, pluginapi.StreamChunkInterceptResponse](
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
			wire = appendStreamResult(wire, chunk, streamOut)
		}
		wireStr := string(wire)
		if !strings.Contains(wireStr, `"name":"todo"`) || strings.Contains(wireStr, "wp_todo") {
			t.Fatalf("OpenAI stream did not restore wp_todo -> todo: %s", wireStr)
		}
		if strings.Contains(wireStr, "Antigravity") || strings.Contains(wireStr, ".gemini/") {
			t.Fatalf("OpenAI stream leaked cloaked brand/path: %s", wireStr)
		}
		if !strings.Contains(wireStr, "omp") || !strings.Contains(wireStr, ".omp") {
			t.Fatalf("OpenAI stream missing restored omp brand/path: %s", wireStr)
		}
	})

	t.Run("anthropic_response_and_fragmented_stream", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
`)

		const reqID = "issue69-roundtrip-anthropic"
		defer completePluginRequest(t, reqID)

		reqBody := mustMarshalJSONBytes(t, map[string]any{
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "omp notice in ~/.omp/agent"},
						map[string]any{"type": "text", "text": "<system-reminder>omp reminder</system-reminder>"},
					},
				},
			},
			"tools": []any{
				map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}},
				map[string]any{"name": "todo", "input_schema": map[string]any{"type": "object"}},
			},
		})

		reqResp := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
			t,
			pluginabi.MethodRequestInterceptBefore,
			pluginapi.RequestInterceptRequest{
				RequestID:      reqID,
				SourceFormat:   "anthropic",
				Model:          "agy/claude-sonnet-4",
				RequestedModel: "agy/claude-sonnet-4",
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           reqBody,
			},
		)
		if reqResp.Terminate || len(reqResp.Body) == 0 {
			t.Fatalf("ProtectedAGY admission failed: terminate=%t body=%s", reqResp.Terminate, reqResp.ResponseBody)
		}

		// Non-stream Anthropic JSON response restoration
		upstreamJSON := []byte(`{"content":[{"type":"text","text":"Antigravity opened ~/.gemini/agent/skills and https://antigravity.google/docs"},{"type":"tool_use","id":"tu_2","name":"wp_todo","input":{"path":"~/.gemini/AGENTS.md"}}]}`)
		respOut := callPluginABI[pluginapi.ResponseInterceptRequest, pluginapi.ResponseInterceptResponse](
			t,
			pluginabi.MethodResponseInterceptAfter,
			pluginapi.ResponseInterceptRequest{
				RequestID:    reqID,
				SourceFormat: "anthropic",
				Model:        "agy/claude-sonnet-4",
				Body:         upstreamJSON,
			},
		)
		var restored map[string]any
		mustUnmarshalJSON(t, respOut.Body, &restored)
		blocks := restored["content"].([]any)
		wantText := "omp opened ~/.omp/agent/skills and https://antigravity.google/docs"
		if gotText := blocks[0].(map[string]any)["text"]; gotText != wantText {
			t.Fatalf("Anthropic response text mismatch:\ngot  %q\nwant %q", gotText, wantText)
		}
		tu := blocks[1].(map[string]any)
		if tu["name"] != "todo" {
			t.Fatalf("Anthropic response tool_use name = %q, want todo", tu["name"])
		}
		if path := tu["input"].(map[string]any)["path"]; path != "~/.claude/CLAUDE.md" {
			t.Fatalf("Anthropic response tool_use input.path = %q, want ~/.claude/CLAUDE.md", path)
		}

		// Fragmented Anthropic SSE stream restoration
		toolStart := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_3\",\"name\":\"wp_todo\",\"input\":{}}}\n\n")
		cut := bytes.Index(toolStart, []byte("wp_todo")) + len("wp_")
		chunks := [][]byte{
			[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"By Anti\"}}\n\n"),
			[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"gravity in ~/.gemini/agent\"}}\n\n"),
			toolStart[:cut],
			toolStart[cut:],
			[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		}
		var wire []byte
		for i, chunk := range chunks {
			streamOut := callPluginABI[pluginapi.StreamChunkInterceptRequest, pluginapi.StreamChunkInterceptResponse](
				t,
				pluginabi.MethodResponseInterceptStreamChunk,
				pluginapi.StreamChunkInterceptRequest{
					RequestID:    reqID,
					SourceFormat: "anthropic",
					Model:        "agy/claude-sonnet-4",
					ChunkIndex:   i,
					Body:         chunk,
				},
			)
			wire = appendStreamResult(wire, chunk, streamOut)
		}
		wireStr := string(wire)
		if !strings.Contains(wireStr, `"name":"todo"`) || strings.Contains(wireStr, "wp_todo") {
			t.Fatalf("Anthropic stream did not restore wp_todo -> todo: %s", wireStr)
		}
		if strings.Contains(wireStr, "Antigravity") || strings.Contains(wireStr, ".gemini/") {
			t.Fatalf("Anthropic stream leaked cloaked brand/path: %s", wireStr)
		}
		if !strings.Contains(wireStr, "omp") || !strings.Contains(wireStr, ".omp/") {
			t.Fatalf("Anthropic stream missing restored omp brand/path: %s", wireStr)
		}
	})
}
