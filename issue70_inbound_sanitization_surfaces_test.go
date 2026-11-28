package main

import (
	"bytes"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestIssue70_ToolAndSchemaSanitizationSurfaces(t *testing.T) {
	const (
		rawTag          = "<system-conventions>Rule</system-conventions>"
		sanitizedTag    = "<conventions>Rule</conventions>"
		rawRFC          = "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."
		sanitizedRFC    = "Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."
		customMatch     = "custom-harness-sig"
		customReplaced  = "neutral-harness-sig"
		stripMatch      = " [STRIP-ME]"
		rawCombined     = rawTag + " " + rawRFC + " " + customMatch + stripMatch
		sanitizedResult = sanitizedTag + " " + sanitizedRFC + " " + customReplaced
	)

	makeSchema := func() map[string]any {
		return map[string]any{
			"type":        "object",
			"title":       rawCombined,
			"description": rawCombined,
			"required":    []any{rawTag, customMatch},
			"properties": map[string]any{
				rawTag: map[string]any{
					"type":        "string",
					"title":       rawCombined,
					"description": rawCombined,
					"enum":        []any{rawTag, rawRFC, customMatch},
					"const":       rawCombined,
					"default":     rawCombined,
					"example":     map[string]any{"description": rawCombined, "title": rawCombined},
					"examples": []any{
						rawCombined,
						map[string]any{"description": rawCombined, "title": rawCombined},
					},
				},
				// Property name collides with literal keyword "default" — its subschema's
				// description/title must still be sanitized while its own default stays literal.
				"default": map[string]any{
					"type":        "string",
					"title":       rawCombined,
					"description": rawCombined,
					"default":     rawCombined,
				},
			},
			"$defs": map[string]any{
				"examples": map[string]any{
					"type":        "object",
					"title":       rawCombined,
					"description": rawCombined,
					"properties": map[string]any{
						"nested": map[string]any{
							"type":        "string",
							"description": rawCombined,
							"enum":        []any{rawCombined},
						},
					},
				},
			},
		}
	}

	makeTool := func(format string) map[string]any {
		if format == "openai" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "read",
					"description": rawCombined,
					"parameters":  makeSchema(),
				},
			}
		}
		return map[string]any{
			"name":         "read",
			"description":  rawCombined,
			"input_schema": makeSchema(),
		}
	}

	for _, tc := range []struct {
		name    string
		format  string
		model   string
		cfgYAML string
		headers http.Header
	}{
		{
			name:   "openai-protected-agy-prefix",
			format: "openai",
			model:  "agy/gemini-3.8-flash",
			cfgYAML: `
use_default_keywords: false
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "custom-harness-sig": "neutral-harness-sig"
  " [STRIP-ME]": ""
`,
			headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}},
		},
		{
			name:   "anthropic-protected-agy-prefix",
			format: "anthropic",
			model:  "agy/claude-sonnet-4-6",
			cfgYAML: `
use_default_keywords: false
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "custom-harness-sig": "neutral-harness-sig"
  " [STRIP-ME]": ""
`,
			headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}},
		},
		{
			name:   "openai-ua-omp-prefix",
			format: "openai",
			model:  "agy/gemini-3.8-flash",
			cfgYAML: `
use_default_keywords: false
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "custom-harness-sig": "neutral-harness-sig"
  " [STRIP-ME]": ""
`,
			headers: http.Header{"User-Agent": {"omp/19.2.1"}},
		},
		{
			name:   "anthropic-ua-omp-empty-prefixes",
			format: "anthropic",
			model:  "claude-sonnet-4-6",
			cfgYAML: `
use_default_keywords: false
model_prefixes: []
omp_prompt_sanitizations:
  "custom-harness-sig": "neutral-harness-sig"
  " [STRIP-ME]": ""
`,
			headers: http.Header{"User-Agent": {"omp/19.2.1"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			reconfigurePluginYAML(t, tc.cfgYAML)

			reqID := "issue70-tools-" + tc.name
			defer completePluginRequest(t, reqID)

			root := map[string]any{
				"system": rawCombined,
				"messages": []any{
					map[string]any{"role": "developer", "content": rawCombined},
				},
				"tools": []any{makeTool(tc.format)},
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
			if res.Terminate {
				t.Fatalf("unexpected terminate: %s", res.ResponseBody)
			}
			if len(res.Body) == 0 {
				t.Fatalf("expected rewritten body, got empty")
			}

			var got map[string]any
			mustUnmarshalJSON(t, res.Body, &got)

			if got["system"] != sanitizedResult {
				t.Errorf("system = %q, want %q", got["system"], sanitizedResult)
			}
			dev := got["messages"].([]any)[0].(map[string]any)["content"]
			if dev != sanitizedResult {
				t.Errorf("developer = %q, want %q", dev, sanitizedResult)
			}

			tool := got["tools"].([]any)[0].(map[string]any)
			var owner, schema map[string]any
			if tc.format == "openai" {
				owner = tool["function"].(map[string]any)
				schema = owner["parameters"].(map[string]any)
			} else {
				owner = tool
				schema = owner["input_schema"].(map[string]any)
			}

			if owner["name"] != "read_file" {
				t.Errorf("tool name = %q, want %q", owner["name"], "read_file")
			}
			if owner["description"] != sanitizedResult {
				t.Errorf("tool description = %q, want %q", owner["description"], sanitizedResult)
			}
			if schema["title"] != sanitizedResult {
				t.Errorf("schema title = %q, want %q", schema["title"], sanitizedResult)
			}
			if schema["description"] != sanitizedResult {
				t.Errorf("schema description = %q, want %q", schema["description"], sanitizedResult)
			}

			// Structural required[] and property key names remain byte-for-byte untouched.
			if !reflect.DeepEqual(schema["required"], []any{rawTag, customMatch}) {
				t.Errorf("schema required mutated: %#v", schema["required"])
			}
			props := schema["properties"].(map[string]any)
			prop, ok := props[rawTag].(map[string]any)
			if !ok {
				t.Fatalf("property key %q was mutated; got keys %#v", rawTag, props)
			}
			if prop["title"] != sanitizedResult || prop["description"] != sanitizedResult {
				t.Errorf("property title/description = %q / %q, want %q", prop["title"], prop["description"], sanitizedResult)
			}
			// Literal subtrees inside property stay byte-for-byte untouched.
			if !reflect.DeepEqual(prop["enum"], []any{rawTag, rawRFC, customMatch}) {
				t.Errorf("enum mutated: %#v", prop["enum"])
			}
			if prop["const"] != rawCombined {
				t.Errorf("const mutated: %q", prop["const"])
			}
			if prop["default"] != rawCombined {
				t.Errorf("default mutated: %q", prop["default"])
			}
			wantExample := map[string]any{"description": rawCombined, "title": rawCombined}
			if !reflect.DeepEqual(prop["example"], wantExample) {
				t.Errorf("example mutated: %#v", prop["example"])
			}
			wantExamples := []any{rawCombined, map[string]any{"description": rawCombined, "title": rawCombined}}
			if !reflect.DeepEqual(prop["examples"], wantExamples) {
				t.Errorf("examples mutated: %#v", prop["examples"])
			}

			// Property colliding with literal keyword "default": subschema help sanitized, literal default untouched.
			defProp := props["default"].(map[string]any)
			if defProp["title"] != sanitizedResult || defProp["description"] != sanitizedResult {
				t.Errorf("properties.default title/description = %q / %q, want %q", defProp["title"], defProp["description"], sanitizedResult)
			}
			if defProp["default"] != rawCombined {
				t.Errorf("properties.default.default mutated: %q", defProp["default"])
			}

			// $defs["examples"] colliding with literal keyword "examples": subschema help sanitized, nested enum untouched.
			defEx := schema["$defs"].(map[string]any)["examples"].(map[string]any)
			if defEx["title"] != sanitizedResult || defEx["description"] != sanitizedResult {
				t.Errorf("$defs.examples title/description = %q / %q, want %q", defEx["title"], defEx["description"], sanitizedResult)
			}
			nested := defEx["properties"].(map[string]any)["nested"].(map[string]any)
			if nested["description"] != sanitizedResult {
				t.Errorf("$defs.examples.properties.nested.description = %q, want %q", nested["description"], sanitizedResult)
			}
			if !reflect.DeepEqual(nested["enum"], []any{rawCombined}) {
				t.Errorf("$defs.examples.properties.nested.enum mutated: %#v", nested["enum"])
			}
		})
	}
}

func TestIssue70_SystemReminderSanitizationAndConversationExclusions(t *testing.T) {
	const (
		rawSig       = "<system-conventions>Rule</system-conventions> RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. custom-harness-sig [STRIP-ME]"
		sanitizedSig = "<conventions>Rule</conventions> Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. neutral-harness-sig"
		cfgYAML      = `
use_default_keywords: true
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "custom-harness-sig": "neutral-harness-sig"
  " [STRIP-ME]": ""
`
	)

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
			reconfigurePluginYAML(t, cfgYAML)

			reqID := "issue70-reminder-" + tc.name
			defer completePluginRequest(t, reqID)

			userStrRaw := "User typed " + rawSig + "\n" +
				"<system-reminder>First " + rawSig + "</system-reminder>\n" +
				"Between " + rawSig + "\n" +
				"<system-reminder>Second " + rawSig + "</system-reminder>\n" +
				"Tail before open " + rawSig + "\n" +
				"<system-reminder>Unterminated " + rawSig
			userStrWant := "User typed " + rawSig + "\n" +
				"<system-reminder>First " + sanitizedSig + "</system-reminder>\n" +
				"Between " + rawSig + "\n" +
				"<system-reminder>Second " + sanitizedSig + "</system-reminder>\n" +
				"Tail before open " + rawSig + "\n" +
				"<system-reminder>Unterminated " + sanitizedSig

			userBlockTextRaw := "Typed block " + rawSig + "\n<system-reminder>Block " + rawSig + "</system-reminder>\nAfter " + rawSig
			userBlockTextWant := "Typed block " + rawSig + "\n<system-reminder>Block " + sanitizedSig + "</system-reminder>\nAfter " + rawSig

			// Machine-Generated Conversation Turns get brand rewriting ("Oh My Pi" -> "Antigravity"),
			// NEVER one-way Inbound Sanitization — even when file content echoed in a tool/assistant
			// turn literally contains a `<system-reminder>` tag around sanitized signatures.
			machineTurnRaw := "Oh My Pi file content: <system-reminder>" + rawSig + "</system-reminder>"
			machineTurnWant := "Antigravity file content: <system-reminder>" + rawSig + "</system-reminder>"

			messages := []any{
				map[string]any{"role": "user", "content": userStrRaw},
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type":          "text",
							"text":          userBlockTextRaw,
							"cache_control": map[string]any{"type": "ephemeral", "note": "<system-reminder>" + rawSig + "</system-reminder>"},
						},
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "toolu_str",
							"content":     machineTurnRaw,
						},
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "toolu_arr",
							"content": []any{
								map[string]any{
									"type":          "text",
									"text":          machineTurnRaw,
									"cache_control": map[string]any{"type": "ephemeral", "note": rawSig},
								},
								map[string]any{
									"type":   "image",
									"source": map[string]any{"type": "base64", "data": "<system-reminder>" + rawSig + "</system-reminder>"},
								},
							},
						},
						map[string]any{
							"type": "input_text",
							"text": "<system-reminder>" + rawSig + "</system-reminder>",
						},
						map[string]any{
							"type": "image",
							"text": "<system-reminder>" + rawSig + "</system-reminder>",
						},
					},
				},
				map[string]any{
					"role":    "assistant",
					"content": machineTurnRaw,
					"tool_calls": []any{
						map[string]any{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "read",
								"arguments": `{"note":"<system-reminder>` + rawSig + `</system-reminder>"}`,
							},
						},
					},
				},
				map[string]any{
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "text", "text": machineTurnRaw},
						map[string]any{"type": "thinking", "thinking": machineTurnRaw},
						map[string]any{
							"type":  "tool_use",
							"id":    "toolu_1",
							"name":  "read",
							"input": map[string]any{"note": "<system-reminder>" + rawSig + "</system-reminder>"},
						},
					},
				},
				map[string]any{
					"role":         "tool",
					"name":         "read",
					"tool_call_id": "call_1",
					"content":      machineTurnRaw,
				},
				map[string]any{
					"role":         "tool",
					"name":         "read",
					"tool_call_id": "call_2",
					"content": []any{
						map[string]any{
							"type":          "text",
							"text":          machineTurnRaw,
							"cache_control": map[string]any{"type": "ephemeral", "note": rawSig},
						},
						map[string]any{
							"type":      "image_url",
							"image_url": map[string]any{"url": "data:image/png;base64," + rawSig},
						},
					},
				},
			}

			var tools []any
			if tc.format == "openai" {
				tools = []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}}
			} else {
				tools = []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}}
			}
			root := map[string]any{"messages": messages, "tools": tools}

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
			if res.Terminate {
				t.Fatalf("unexpected terminate: %s", res.ResponseBody)
			}

			var got map[string]any
			mustUnmarshalJSON(t, res.Body, &got)
			msgs := got["messages"].([]any)

			// 0: bare string user message
			if gotUser0 := msgs[0].(map[string]any)["content"]; gotUser0 != userStrWant {
				t.Errorf("user[0] string mismatch:\ngot  %q\nwant %q", gotUser0, userStrWant)
			}

			// 1: array user message
			user1 := msgs[1].(map[string]any)["content"].([]any)
			textBlk := user1[0].(map[string]any)
			if textBlk["text"] != userBlockTextWant {
				t.Errorf("user[1] text block mismatch:\ngot  %q\nwant %q", textBlk["text"], userBlockTextWant)
			}
			if note := textBlk["cache_control"].(map[string]any)["note"]; note != "<system-reminder>"+rawSig+"</system-reminder>" {
				t.Errorf("user[1] text cache_control mutated: %q", note)
			}
			// tool_result string: branded, NEVER sanitized
			if trStr := user1[1].(map[string]any)["content"]; trStr != machineTurnWant {
				t.Errorf("tool_result string mismatch:\ngot  %q\nwant %q", trStr, machineTurnWant)
			}
			// tool_result array: text branded & unsanitized, metadata & image byte-for-byte
			trArr := user1[2].(map[string]any)["content"].([]any)
			if trArr[0].(map[string]any)["text"] != machineTurnWant {
				t.Errorf("tool_result array text mismatch:\ngot  %q\nwant %q", trArr[0].(map[string]any)["text"], machineTurnWant)
			}
			if trArr[0].(map[string]any)["cache_control"].(map[string]any)["note"] != rawSig {
				t.Errorf("tool_result cache_control mutated: %#v", trArr[0].(map[string]any)["cache_control"])
			}
			if trArr[1].(map[string]any)["source"].(map[string]any)["data"] != "<system-reminder>"+rawSig+"</system-reminder>" {
				t.Errorf("tool_result image mutated: %#v", trArr[1])
			}
			// user input_text and image blocks stay untouched
			if user1[3].(map[string]any)["text"] != "<system-reminder>"+rawSig+"</system-reminder>" {
				t.Errorf("user input_text block was mutated: %q", user1[3].(map[string]any)["text"])
			}
			if user1[4].(map[string]any)["text"] != "<system-reminder>"+rawSig+"</system-reminder>" {
				t.Errorf("user image block was mutated: %q", user1[4].(map[string]any)["text"])
			}

			// 2: assistant string + tool_calls arguments
			asst0 := msgs[2].(map[string]any)
			if asst0["content"] != machineTurnWant {
				t.Errorf("assistant string mismatch:\ngot  %q\nwant %q", asst0["content"], machineTurnWant)
			}
			callArgs := asst0["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"]
			wantArgs := `{"note":"<system-reminder>` + rawSig + `</system-reminder>"}`
			if callArgs != wantArgs {
				t.Errorf("assistant tool_calls arguments mutated:\ngot  %q\nwant %q", callArgs, wantArgs)
			}

			// 3: assistant blocks (text, thinking, tool_use.input)
			asst1 := msgs[3].(map[string]any)["content"].([]any)
			if asst1[0].(map[string]any)["text"] != machineTurnWant {
				t.Errorf("assistant text block mismatch:\ngot  %q\nwant %q", asst1[0].(map[string]any)["text"], machineTurnWant)
			}
			if asst1[1].(map[string]any)["thinking"] != machineTurnWant {
				t.Errorf("assistant thinking block mismatch:\ngot  %q\nwant %q", asst1[1].(map[string]any)["thinking"], machineTurnWant)
			}
			if tuNote := asst1[2].(map[string]any)["input"].(map[string]any)["note"]; tuNote != "<system-reminder>"+rawSig+"</system-reminder>" {
				t.Errorf("assistant tool_use.input mutated: %q", tuNote)
			}

			// 4 & 5: role:"tool" string & array
			if tool0 := msgs[4].(map[string]any)["content"]; tool0 != machineTurnWant {
				t.Errorf("role:tool string mismatch:\ngot  %q\nwant %q", tool0, machineTurnWant)
			}
			tool1 := msgs[5].(map[string]any)["content"].([]any)
			if tool1[0].(map[string]any)["text"] != machineTurnWant {
				t.Errorf("role:tool array text mismatch:\ngot  %q\nwant %q", tool1[0].(map[string]any)["text"], machineTurnWant)
			}
			if tool1[0].(map[string]any)["cache_control"].(map[string]any)["note"] != rawSig {
				t.Errorf("role:tool cache_control mutated: %#v", tool1[0].(map[string]any)["cache_control"])
			}
			if tool1[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,"+rawSig {
				t.Errorf("role:tool image_url mutated: %#v", tool1[1])
			}
		})
	}

	// Tool-free UA OMP request whose ONLY change is inside a user <system-reminder> span must serialize a rewritten body.
	t.Run("ua-omp-tool-free-reminder-only-triggers-rewrite", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, "use_default_keywords: false\nmodel_prefixes:\n  - \"agy/\"\n")
		const reqID = "issue70-ua-reminder-only"
		defer completePluginRequest(t, reqID)

		root := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "Typed <system-conventions>\n<system-reminder><system-conventions>x</system-conventions></system-reminder>"},
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
				Headers:        http.Header{"User-Agent": {"omp/19.2.1"}},
				Body:           mustMarshalJSONBytes(t, root),
			},
		)
		if res.Terminate || len(res.Body) == 0 {
			t.Fatalf("expected rewritten body, got terminate=%t len=%d", res.Terminate, len(res.Body))
		}
		var got map[string]any
		mustUnmarshalJSON(t, res.Body, &got)
		gotContent := got["messages"].([]any)[0].(map[string]any)["content"]
		wantContent := "Typed <system-conventions>\n<system-reminder><conventions>x</conventions></system-reminder>"
		if gotContent != wantContent {
			t.Errorf("got %q, want %q", gotContent, wantContent)
		}
	})

	// UA OMP request whose ONLY change is sanitizing an unmapped tool's description must serialize a rewritten body.
	t.Run("ua-omp-unmapped-tool-desc-sanitization-only-triggers-rewrite", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, "use_default_keywords: false\nmodel_prefixes:\n  - \"agy/\"\n")
		const reqID = "issue70-ua-tooldesc-only"
		defer completePluginRequest(t, reqID)

		root := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "plain user prompt"},
			},
			"tools": []any{
				map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "custom_unmapped_tool",
						"description": "<system-conventions>x</system-conventions>",
						"parameters":  map[string]any{"type": "object", "title": "<system-conventions>t</system-conventions>"},
					},
				},
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
				Headers:        http.Header{"User-Agent": {"omp/19.2.1"}},
				Body:           mustMarshalJSONBytes(t, root),
			},
		)
		if res.Terminate || len(res.Body) == 0 {
			t.Fatalf("expected rewritten body, got terminate=%t len=%d", res.Terminate, len(res.Body))
		}
		var got map[string]any
		mustUnmarshalJSON(t, res.Body, &got)
		fn := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
		if fn["description"] != "<conventions>x</conventions>" || fn["parameters"].(map[string]any)["title"] != "<conventions>t</conventions>" {
			t.Errorf("unexpected sanitized unmapped tool: %#v", fn)
		}
	})
}

func TestIssue70_UnifiedPipelineOrderingAllHarnessSurfaces(t *testing.T) {
	// Triple-lock ordering vector:
	//   input:  "Oh My Pi policy: use bash directly"
	//   1 brand:    "Antigravity policy: use bash directly"
	//   2 sanitize: "sanitized-literal-token: Always use bash for shell work."
	//   3 alias:    "sanitized-literal-token: Always use run_command for shell work."
	// - Fails if brand runs after sanitize (match never triggers OR sanitized-literal-token becomes WRONG).
	// - Fails if tool-alias runs before sanitize ("use bash" becomes "use run_command", breaking match).
	// - Fails if tool-alias does not run after sanitize ("use bash" in sanitized output stays uncloaked).
	const (
		inputText     = "Oh My Pi policy: use bash directly"
		wantSanitized = "sanitized-literal-token: Always use run_command for shell work."
		// Machine-Generated Conversation Turns run brand -> tool-alias only (no sanitize):
		wantMachineTurn = "Antigravity policy: use run_command directly"
		cfgYAML         = `
use_default_keywords: true
model_prefixes:
  - "agy/"
custom_mappings:
  "sanitized-literal-token": "WRONG"
omp_prompt_sanitizations:
  "Antigravity policy: use bash directly": "sanitized-literal-token: Always use bash for shell work."
`
	)

	makeTool := func(format string) map[string]any {
		schema := map[string]any{
			"type":        "object",
			"title":       inputText,
			"description": inputText,
			"properties": map[string]any{
				"cmd": map[string]any{
					"type":        "string",
					"title":       inputText,
					"description": inputText,
					"default":     inputText,
				},
			},
		}
		if format == "openai" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "bash",
					"description": inputText,
					"parameters":  schema,
				},
			}
		}
		return map[string]any{
			"name":         "bash",
			"description":  inputText,
			"input_schema": schema,
		}
	}

	for _, format := range []string{"openai", "anthropic"} {
		for _, route := range []struct {
			name    string
			headers http.Header
		}{
			{name: "protected-agy", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}},
			{name: "ua-omp", headers: http.Header{"User-Agent": {"omp/19.2.1"}}},
		} {
			t.Run(format+"/"+route.name, func(t *testing.T) {
				defer restoreDefaultFilterConfig(t)
				reconfigurePluginYAML(t, cfgYAML)

				reqID := "issue70-order-" + format + "-" + route.name
				defer completePluginRequest(t, reqID)

				root := map[string]any{
					"system": inputText,
					"messages": []any{
						map[string]any{"role": "system", "content": inputText},
						map[string]any{"role": "developer", "content": inputText},
						map[string]any{
							"role":    "user",
							"content": "Typed " + inputText + "\n<system-reminder>" + inputText + "</system-reminder>",
						},
						map[string]any{
							"role": "user",
							"content": []any{
								map[string]any{"type": "text", "text": "Block typed " + inputText + "\n<system-reminder>" + inputText + "</system-reminder>"},
								map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": inputText},
							},
						},
						map[string]any{"role": "assistant", "content": inputText},
						map[string]any{"role": "tool", "name": "bash", "tool_call_id": "call_1", "content": inputText},
					},
					"tools": []any{makeTool(format)},
				}

				res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
					t,
					pluginabi.MethodRequestInterceptBefore,
					pluginapi.RequestInterceptRequest{
						RequestID:      reqID,
						SourceFormat:   format,
						Model:          "agy/gemini-3.8-flash",
						RequestedModel: "agy/gemini-3.8-flash",
						Headers:        route.headers,
						Body:           mustMarshalJSONBytes(t, root),
					},
				)
				if res.Terminate {
					t.Fatalf("unexpected terminate: %s", res.ResponseBody)
				}

				var got map[string]any
				mustUnmarshalJSON(t, res.Body, &got)

				if got["system"] != wantSanitized {
					t.Errorf("system = %q, want %q", got["system"], wantSanitized)
				}
				msgs := got["messages"].([]any)
				if gotSys := msgs[0].(map[string]any)["content"]; gotSys != wantSanitized {
					t.Errorf("system msg = %q, want %q", gotSys, wantSanitized)
				}
				if gotDev := msgs[1].(map[string]any)["content"]; gotDev != wantSanitized {
					t.Errorf("developer msg = %q, want %q", gotDev, wantSanitized)
				}
				wantUser0 := "Typed " + inputText + "\n<system-reminder>" + wantSanitized + "</system-reminder>"
				if gotUser0 := msgs[2].(map[string]any)["content"]; gotUser0 != wantUser0 {
					t.Errorf("user string <system-reminder>:\ngot  %q\nwant %q", gotUser0, wantUser0)
				}
				user1 := msgs[3].(map[string]any)["content"].([]any)
				wantUser1Text := "Block typed " + inputText + "\n<system-reminder>" + wantSanitized + "</system-reminder>"
				if gotUser1Text := user1[0].(map[string]any)["text"]; gotUser1Text != wantUser1Text {
					t.Errorf("user array <system-reminder>:\ngot  %q\nwant %q", gotUser1Text, wantUser1Text)
				}
				// Machine-Generated Conversation Turns: brand + tool-alias ONLY (no sanitize).
				if gotTR := user1[1].(map[string]any)["content"]; gotTR != wantMachineTurn {
					t.Errorf("tool_result = %q, want %q", gotTR, wantMachineTurn)
				}
				if gotAsst := msgs[4].(map[string]any)["content"]; gotAsst != wantMachineTurn {
					t.Errorf("assistant = %q, want %q", gotAsst, wantMachineTurn)
				}
				if gotTool := msgs[5].(map[string]any)["content"]; gotTool != wantMachineTurn {
					t.Errorf("role:tool = %q, want %q", gotTool, wantMachineTurn)
				}

				// Tool declaration & nested schema fields.
				tool := got["tools"].([]any)[0].(map[string]any)
				var owner, schema map[string]any
				if format == "openai" {
					owner = tool["function"].(map[string]any)
					schema = owner["parameters"].(map[string]any)
				} else {
					owner = tool
					schema = owner["input_schema"].(map[string]any)
				}
				if owner["name"] != "run_command" {
					t.Errorf("tool name = %q, want %q", owner["name"], "run_command")
				}
				if owner["description"] != wantSanitized {
					t.Errorf("tool description:\ngot  %q\nwant %q", owner["description"], wantSanitized)
				}
				if schema["title"] != wantSanitized || schema["description"] != wantSanitized {
					t.Errorf("schema title/description = %q / %q, want %q", schema["title"], schema["description"], wantSanitized)
				}
				cmdProp := schema["properties"].(map[string]any)["cmd"].(map[string]any)
				if cmdProp["title"] != wantSanitized || cmdProp["description"] != wantSanitized {
					t.Errorf("cmd property title/description = %q / %q, want %q", cmdProp["title"], cmdProp["description"], wantSanitized)
				}
				if cmdProp["default"] != inputText {
					t.Errorf("cmd property default literal mutated: %q", cmdProp["default"])
				}
			})
		}
	}

	// ProtectedAGY also cloaks non-canonical declared tools (shared wp_* and
	// deterministic wp_ext_* fallback aliases) emitted inside sanitized tool
	// descriptions and <system-reminder> spans.
	t.Run("protected-agy-non-canonical-tool-emitted-by-sanitization", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		reconfigurePluginYAML(t, `
use_default_keywords: true
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "Antigravity inspect trigger": "Use hub and custom_probe to inspect workspace state."
`)
		const reqID = "issue70-protected-hub-sanitized"
		defer completePluginRequest(t, reqID)

		const trigger = "Oh My Pi inspect trigger"
		root := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "<system-reminder>" + trigger + "</system-reminder>"},
			},
			"tools": []any{
				map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "hub",
						"description": trigger,
						"parameters":  map[string]any{"type": "object", "description": trigger},
					},
				},
				map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "custom_probe",
						"description": trigger,
						"parameters":  map[string]any{"type": "object", "description": trigger},
					},
				},
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
				Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				Body:           mustMarshalJSONBytes(t, root),
			},
		)
		if res.Terminate {
			t.Fatalf("unexpected terminate: %s", res.ResponseBody)
		}
		var got map[string]any
		mustUnmarshalJSON(t, res.Body, &got)
		tools := got["tools"].([]any)
		hubFn := tools[0].(map[string]any)["function"].(map[string]any)
		probeFn := tools[1].(map[string]any)["function"].(map[string]any)
		hubAlias, _ := hubFn["name"].(string)
		probeAlias, _ := probeFn["name"].(string)
		if hubAlias != "wp_hub" {
			t.Fatalf("expected shared wp_hub alias for hub, got %q", hubAlias)
		}
		if !strings.HasPrefix(probeAlias, "wp_ext_") {
			t.Fatalf("expected deterministic wp_ext_* alias for custom_probe, got %q", probeAlias)
		}
		want := "Use " + hubAlias + " and " + probeAlias + " to inspect workspace state."
		if hubFn["description"] != want || probeFn["description"] != want {
			t.Errorf("tool descriptions = %q / %q, want %q", hubFn["description"], probeFn["description"], want)
		}
		if schemaDesc := hubFn["parameters"].(map[string]any)["description"]; schemaDesc != want {
			t.Errorf("schema description = %q, want %q", schemaDesc, want)
		}
		gotReminder := got["messages"].([]any)[0].(map[string]any)["content"]
		if gotReminder != "<system-reminder>"+want+"</system-reminder>" {
			t.Errorf("reminder = %q, want %q", gotReminder, "<system-reminder>"+want+"</system-reminder>")
		}
	})
}

func TestIssue70_NegativeGatesConfigCompatAndNonReversal(t *testing.T) {
	const (
		rawSig   = "<system-conventions>Rule</system-conventions> RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. custom-harness-sig [STRIP-ME]"
		sanitSig = "<conventions>Rule</conventions> Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. neutral-harness-sig"
		cfgYAML  = `
use_default_keywords: true
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "custom-harness-sig": "neutral-harness-sig"
  " [STRIP-ME]": ""
`
	)

	makeNegativeBody := func(format string, toolNames []string) map[string]any {
		tools := make([]any, 0, len(toolNames))
		for _, name := range toolNames {
			schema := map[string]any{
				"type":        "object",
				"title":       rawSig,
				"description": rawSig,
				"properties": map[string]any{
					"p": map[string]any{"type": "string", "description": rawSig},
				},
			}
			if format == "openai" || format == "openai-response" {
				tools = append(tools, map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        name,
						"description": rawSig,
						"parameters":  schema,
					},
				})
			} else {
				tools = append(tools, map[string]any{
					"name":         name,
					"description":  rawSig,
					"input_schema": schema,
				})
			}
		}
		return map[string]any{
			"system": rawSig,
			"messages": []any{
				map[string]any{"role": "developer", "content": rawSig},
				map[string]any{"role": "user", "content": "<system-reminder>" + rawSig + "</system-reminder>"},
			},
			"tools": tools,
		}
	}

	for _, tc := range []struct {
		name           string
		format         string
		model          string
		headers        http.Header
		tools          []string
		wantZeroMutate bool
		wantClearHdr   bool
	}{
		{
			name:           "explicit-omp-non-agy-bypass",
			format:         "openai",
			model:          "openai/gpt-5",
			headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
			tools:          []string{"read"},
			wantZeroMutate: true,
			wantClearHdr:   true,
		},
		{
			name:           "ua-omp-outside-model-prefixes",
			format:         "openai",
			model:          "openai/gpt-5",
			headers:        http.Header{"User-Agent": {"omp/19.2.1"}},
			tools:          []string{"read"},
			wantZeroMutate: true,
		},
		{
			name:    "unmarked-no-ua-omp-tools",
			format:  "anthropic",
			model:   "agy/gemini-3.8-flash",
			headers: nil,
			tools:   []string{"read", "write", "edit", "bash", "hub"},
		},
		{
			name:         "claude-code-on-agy",
			format:       "anthropic",
			model:        "agy/claude-sonnet-4-6",
			headers:      http.Header{"X-Cloak-Client": {"claude_code"}},
			tools:        []string{"Bash", "Read"},
			wantClearHdr: true,
		},
		{
			name:         "codex-on-agy",
			format:       "openai",
			model:        "agy/gemini-3.8-flash",
			headers:      http.Header{"X-Cloak-Client": {"codex"}},
			tools:        []string{"exec_command"},
			wantClearHdr: true,
		},
		{
			name:         "responses-carrier-protected-agy",
			format:       "openai-response",
			model:        "agy/gemini-3.8-flash",
			headers:      http.Header{"X-Cloak-Client": {"oh_my_pi"}},
			tools:        []string{"read"},
			wantClearHdr: true,
		},
		{
			name:    "responses-carrier-ua-omp",
			format:  "openai-response",
			model:   "agy/gemini-3.8-flash",
			headers: http.Header{"User-Agent": {"omp/19.2.1"}},
			tools:   []string{"read"},
		},
	} {
		t.Run("negative/"+tc.name, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			reconfigurePluginYAML(t, cfgYAML)

			reqID := "issue70-neg-" + tc.name
			defer completePluginRequest(t, reqID)

			body := mustMarshalJSONBytes(t, makeNegativeBody(tc.format, tc.tools))
			res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
				t,
				pluginabi.MethodRequestInterceptBefore,
				pluginapi.RequestInterceptRequest{
					RequestID:      reqID,
					SourceFormat:   tc.format,
					Model:          tc.model,
					RequestedModel: tc.model,
					Headers:        tc.headers,
					Body:           body,
				},
			)
			if res.Terminate {
				t.Fatalf("unexpected terminate: %s", res.ResponseBody)
			}
			if tc.wantClearHdr && !reflect.DeepEqual(res.ClearHeaders, []string{"X-Cloak-Client"}) {
				t.Fatalf("ClearHeaders = %v, want [X-Cloak-Client]", res.ClearHeaders)
			}
			if tc.wantZeroMutate {
				if len(res.Body) != 0 && !bytes.Equal(res.Body, body) {
					t.Fatalf("expected zero body mutation, got %s", res.Body)
				}
				return
			}
			// Route mutated tool names/brands, but MUST NOT apply OMP inbound sanitization anywhere.
			if bytes.Contains(res.Body, []byte("<conventions>")) ||
				bytes.Contains(res.Body, []byte("Normative keywords:")) ||
				bytes.Contains(res.Body, []byte("neutral-harness-sig")) {
				t.Fatalf("negative route unexpectedly applied OMP sanitization: %s", res.Body)
			}
			var got map[string]any
			mustUnmarshalJSON(t, res.Body, &got)
			if got["system"] != rawSig {
				t.Errorf("system mutated by sanitization: %q", got["system"])
			}
			msgs := got["messages"].([]any)
			if msgs[0].(map[string]any)["content"] != rawSig {
				t.Errorf("developer mutated by sanitization: %q", msgs[0].(map[string]any)["content"])
			}
			if msgs[1].(map[string]any)["content"] != "<system-reminder>"+rawSig+"</system-reminder>" {
				t.Errorf("user <system-reminder> mutated by sanitization: %q", msgs[1].(map[string]any)["content"])
			}
			tool := got["tools"].([]any)[0].(map[string]any)
			var owner, schema map[string]any
			if tc.format == "openai" || tc.format == "openai-response" {
				owner = tool["function"].(map[string]any)
				schema = owner["parameters"].(map[string]any)
			} else {
				owner = tool
				schema = owner["input_schema"].(map[string]any)
			}
			if owner["description"] != rawSig || schema["title"] != rawSig || schema["description"] != rawSig {
				t.Errorf("tool/schema prose unexpectedly sanitized: desc=%q title=%q schemaDesc=%q",
					owner["description"], schema["title"], schema["description"])
			}
		})
	}

	// Flat omp_prompt_sanitizations backward compatibility via live plugin.reconfigure (JSON object, string, array)
	// applying overrides and "" strips across tool description + <system-reminder>.
	t.Run("flat-config-reconfigure-compat", func(t *testing.T) {
		defer restoreDefaultFilterConfig(t)
		for _, cfgCase := range []struct {
			name  string
			cfg   string
			input string
			want  string
		}{
			{
				name:  "json-object-override-and-strip",
				cfg:   `{"use_default_keywords":false,"model_prefixes":["agy/"],"omp_prompt_sanitizations":{"<system-conventions>":"<ops-rules>","</system-conventions>":"</ops-rules>","drop-me":""}}`,
				input: "<system-conventions>keep drop-me</system-conventions>",
				want:  "<ops-rules>keep </ops-rules>",
			},
			{
				name:  "yaml-string-form",
				cfg:   "use_default_keywords: false\nmodel_prefixes:\n  - \"agy/\"\nomp_prompt_sanitizations: |\n  sig-a: safe-a\n  sig-drop:\n",
				input: "sig-a[sig-drop]",
				want:  "safe-a[]",
			},
			{
				name:  "yaml-array-form",
				cfg:   "use_default_keywords: false\nmodel_prefixes:\n  - \"agy/\"\nomp_prompt_sanitizations:\n  - \"sig-b: safe-b\"\n  - \"sig-rm:\"\n",
				input: "sig-b[sig-rm]",
				want:  "safe-b[]",
			},
		} {
			t.Run(cfgCase.name, func(t *testing.T) {
				reconfigurePluginYAML(t, cfgCase.cfg)
				reqID := "issue70-cfg-" + cfgCase.name
				defer completePluginRequest(t, reqID)

				root := map[string]any{
					"messages": []any{
						map[string]any{"role": "user", "content": "<system-reminder>" + cfgCase.input + "</system-reminder>"},
					},
					"tools": []any{
						map[string]any{
							"type": "function",
							"function": map[string]any{
								"name":        "read",
								"description": cfgCase.input,
								"parameters":  map[string]any{"type": "object", "title": cfgCase.input},
							},
						},
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
						Headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
						Body:           mustMarshalJSONBytes(t, root),
					},
				)
				if res.Terminate {
					t.Fatalf("unexpected terminate: %s", res.ResponseBody)
				}
				var got map[string]any
				mustUnmarshalJSON(t, res.Body, &got)
				fn := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
				if fn["description"] != cfgCase.want || fn["parameters"].(map[string]any)["title"] != cfgCase.want {
					t.Errorf("tool/schema mismatch: desc=%q title=%q want=%q", fn["description"], fn["parameters"].(map[string]any)["title"], cfgCase.want)
				}
				if gotRem := got["messages"].([]any)[0].(map[string]any)["content"]; gotRem != "<system-reminder>"+cfgCase.want+"</system-reminder>" {
					t.Errorf("reminder = %q, want %q", gotRem, "<system-reminder>"+cfgCase.want+"</system-reminder>")
				}
			})
		}
	})

	// One-way non-reversal on non-stream responses and SSE streams for both ProtectedAGY and UA OMP.
	t.Run("one-way-response-and-sse-non-reversal", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			format  string
			headers http.Header
			wantBr  string // expected restored brand word from "Antigravity"
		}{
			{name: "openai-protected-agy", format: "openai", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}, wantBr: "omp"},
			{name: "anthropic-protected-agy", format: "anthropic", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}, wantBr: "omp"},
			{name: "openai-ua-omp", format: "openai", headers: http.Header{"User-Agent": {"omp/19.2.1"}}, wantBr: "omp"},
			{name: "anthropic-ua-omp", format: "anthropic", headers: http.Header{"User-Agent": {"omp/19.2.1"}}, wantBr: "omp"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				defer restoreDefaultFilterConfig(t)
				reconfigurePluginYAML(t, cfgYAML)

				reqID := "issue70-nonrev-" + tc.name
				defer completePluginRequest(t, reqID)

				reqBody := mustMarshalJSONBytes(t, makeNegativeBody(tc.format, []string{"read"}))
				reqRes := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
					t,
					pluginabi.MethodRequestInterceptBefore,
					pluginapi.RequestInterceptRequest{
						RequestID:      reqID,
						SourceFormat:   tc.format,
						Model:          "agy/gemini-3.8-flash",
						RequestedModel: "agy/gemini-3.8-flash",
						Headers:        tc.headers,
						Body:           reqBody,
					},
				)
				if reqRes.Terminate || !bytes.Contains(reqRes.Body, []byte(sanitSig)) {
					t.Fatalf("request did not sanitize as expected: terminate=%t body=%s", reqRes.Terminate, reqRes.Body)
				}

				upstreamText := "Antigravity sees " + sanitSig
				var respBody []byte
				if tc.format == "openai" {
					respBody = mustMarshalJSONBytes(t, map[string]any{
						"choices": []any{
							map[string]any{
								"message": map[string]any{
									"role":    "assistant",
									"content": upstreamText,
									"tool_calls": []any{
										map[string]any{
											"id":       "call_1",
											"type":     "function",
											"function": map[string]any{"name": "read_file", "arguments": `{}`},
										},
									},
								},
							},
						},
					})
				} else {
					respBody = mustMarshalJSONBytes(t, map[string]any{
						"content": []any{
							map[string]any{"type": "text", "text": upstreamText},
							map[string]any{"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": map[string]any{}},
						},
					})
				}

				// Non-stream response: brand + tool name restore, sanitized tokens stay one-way.
				nonStream := callPluginABI[pluginapi.ResponseInterceptRequest, pluginapi.ResponseInterceptResponse](
					t,
					pluginabi.MethodResponseInterceptAfter,
					pluginapi.ResponseInterceptRequest{
						RequestID:       reqID,
						SourceFormat:    tc.format,
						Model:           "agy/gemini-3.8-flash",
						OriginalRequest: reqBody,
						RequestBody:     reqRes.Body,
						Body:            respBody,
					},
				)
				outStr := string(nonStream.Body)
				if !strings.Contains(outStr, tc.wantBr+" sees "+sanitSig) {
					t.Errorf("non-stream response unexpectedly reversed sanitization or missed brand restore: %s", outStr)
				}
				if strings.Contains(outStr, "<system-conventions>") || strings.Contains(outStr, "RFC 2119:") || strings.Contains(outStr, "custom-harness-sig") {
					t.Errorf("non-stream response reversed one-way sanitization: %s", outStr)
				}
				if !strings.Contains(outStr, `"name":"read"`) {
					t.Errorf("non-stream response did not uncloak tool name read_file -> read: %s", outStr)
				}

				// SSE stream chunks (use fresh request ID so stream session is clean).
				streamReqID := reqID + "-sse"
				defer completePluginRequest(t, streamReqID)
				streamReqRes := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
					t,
					pluginabi.MethodRequestInterceptBefore,
					pluginapi.RequestInterceptRequest{
						RequestID:      streamReqID,
						SourceFormat:   tc.format,
						Model:          "agy/gemini-3.8-flash",
						RequestedModel: "agy/gemini-3.8-flash",
						Headers:        tc.headers,
						Body:           reqBody,
					},
				)
				var sseChunks [][]byte
				if tc.format == "openai" {
					sseChunks = [][]byte{
						[]byte("data: " + string(mustMarshalJSONBytes(t, map[string]any{
							"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Antigravity sees " + sanitSig}}},
						})) + "\n\n"),
						[]byte("data: " + string(mustMarshalJSONBytes(t, map[string]any{
							"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
						)) + "\n\n"),
						[]byte("data: [DONE]\n\n"),
					}
				} else {
					sseChunks = [][]byte{
						[]byte("event: content_block_delta\ndata: " + string(mustMarshalJSONBytes(t, map[string]any{
							"type":  "content_block_delta",
							"index": 0,
							"delta": map[string]any{"type": "text_delta", "text": "Antigravity sees " + sanitSig},
						})) + "\n\n"),
						[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
					}
				}
				var wire []byte
				for idx, ch := range sseChunks {
					sc := callPluginABI[pluginapi.StreamChunkInterceptRequest, pluginapi.StreamChunkInterceptResponse](
						t,
						pluginabi.MethodResponseInterceptStreamChunk,
						pluginapi.StreamChunkInterceptRequest{
							RequestID:       streamReqID,
							SourceFormat:    tc.format,
							Model:           "agy/gemini-3.8-flash",
							OriginalRequest: reqBody,
							RequestBody:     streamReqRes.Body,
							ChunkIndex:      idx,
							Body:            ch,
						},
					)
					wire = appendStreamResult(wire, ch, sc)
				}
				sseOut := string(wire)
				if !strings.Contains(sseOut, tc.wantBr+" sees "+sanitSig) {
					t.Errorf("SSE stream missed brand restore or altered sanitized text: %s", sseOut)
				}
				if strings.Contains(sseOut, "<system-conventions>") || strings.Contains(sseOut, "RFC 2119:") || strings.Contains(sseOut, "custom-harness-sig") {
					t.Errorf("SSE stream reversed one-way sanitization: %s", sseOut)
				}
			})
		}
	})
}

func TestIssue70_SystemReminderFusedSanitizeAndAlias(t *testing.T) {
	makeTool := func(format, name string) map[string]any {
		schema := map[string]any{"type": "object"}
		if format == "openai" || format == "openai-response" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":       name,
					"parameters": schema,
				},
			}
		}
		return map[string]any{
			"name":         name,
			"input_schema": schema,
		}
	}

	// Case 1: rewriting <system-reminder> / </system-reminder> via
	// omp_prompt_sanitizations must STILL cloak declared tools inside the span
	// on both OpenAI and Anthropic carriers and both ProtectedAGY and UA OMP routes.
	t.Run("tag-rewrite-still-cloaks-tools", func(t *testing.T) {
		const cfgYAML = `
use_default_keywords: true
model_prefixes:
  - "agy/"
tool_mappings:
  oh_my_pi:
    custom_alias_tool: wp_custom_alias
omp_prompt_sanitizations:
  "<system-reminder>": "<note>"
  "</system-reminder>": "</note>"
  "custom-harness-sig": "neutral-harness-sig"
`
		for _, format := range []string{"openai", "anthropic"} {
			for _, route := range []struct {
				name        string
				headers     http.Header
				isProtected bool
			}{
				{name: "protected-agy", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}, isProtected: true},
				{name: "ua-omp", headers: http.Header{"User-Agent": {"omp/19.2.1"}}, isProtected: false},
			} {
				t.Run(format+"/"+route.name, func(t *testing.T) {
					defer restoreDefaultFilterConfig(t)
					reconfigurePluginYAML(t, cfgYAML)

					reqID := "issue70-fused-case1-" + format + "-" + route.name
					defer completePluginRequest(t, reqID)

					const spanBody = "Oh My Pi custom-harness-sig: use bash, use hub, custom_alias_tool, custom_probe"
					const machineRaw = "<system-reminder>Oh My Pi custom-harness-sig: use bash</system-reminder>"
					const machineWant = "<system-reminder>Antigravity custom-harness-sig: use run_command</system-reminder>"

					root := map[string]any{
						"messages": []any{
							map[string]any{
								"role":    "user",
								"content": "User typed: use bash and use hub\n<system-reminder>" + spanBody + "</system-reminder>",
							},
							map[string]any{
								"role": "user",
								"content": []any{
									map[string]any{
										"type": "text",
										"text": "Typed block: use bash and use hub\n<system-reminder>" + spanBody + "</system-reminder>",
									},
									map[string]any{
										"type":        "tool_result",
										"tool_use_id": "toolu_1",
										"content":     machineRaw,
									},
								},
							},
							map[string]any{"role": "assistant", "content": machineRaw},
							map[string]any{"role": "tool", "name": "bash", "tool_call_id": "call_1", "content": machineRaw},
						},
						"tools": []any{
							makeTool(format, "bash"),
							makeTool(format, "hub"),
							makeTool(format, "custom_alias_tool"),
							makeTool(format, "custom_probe"),
						},
					}

					res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
						t,
						pluginabi.MethodRequestInterceptBefore,
						pluginapi.RequestInterceptRequest{
							RequestID:      reqID,
							SourceFormat:   format,
							Model:          "agy/gemini-3.8-flash",
							RequestedModel: "agy/gemini-3.8-flash",
							Headers:        route.headers,
							Body:           mustMarshalJSONBytes(t, root),
						},
					)
					if res.Terminate || len(res.Body) == 0 {
						t.Fatalf("unexpected result: terminate=%t len=%d resp=%s", res.Terminate, len(res.Body), res.ResponseBody)
					}

					var got map[string]any
					mustUnmarshalJSON(t, res.Body, &got)

					wantSpan := "<note>Antigravity neutral-harness-sig: use run_command, use hub, wp_custom_alias, custom_probe</note>"
					if route.isProtected {
						tools := got["tools"].([]any)
						var probeAlias string
						if format == "openai" {
							probeAlias, _ = tools[3].(map[string]any)["function"].(map[string]any)["name"].(string)
						} else {
							probeAlias, _ = tools[3].(map[string]any)["name"].(string)
						}
						if !strings.HasPrefix(probeAlias, "wp_ext_") {
							t.Fatalf("expected wp_ext_* fallback alias for custom_probe, got %q", probeAlias)
						}
						wantSpan = "<note>Antigravity neutral-harness-sig: use run_command, use wp_hub, wp_custom_alias, " + probeAlias + "</note>"
					}

					msgs := got["messages"].([]any)
					wantUser0 := "User typed: use bash and use hub\n" + wantSpan
					if gotUser0 := msgs[0].(map[string]any)["content"]; gotUser0 != wantUser0 {
						t.Errorf("user[0] string mismatch:\ngot  %q\nwant %q", gotUser0, wantUser0)
					}
					user1 := msgs[1].(map[string]any)["content"].([]any)
					wantUser1Text := "Typed block: use bash and use hub\n" + wantSpan
					if gotUser1Text := user1[0].(map[string]any)["text"]; gotUser1Text != wantUser1Text {
						t.Errorf("user[1] text block mismatch:\ngot  %q\nwant %q", gotUser1Text, wantUser1Text)
					}
					if gotTR := user1[1].(map[string]any)["content"]; gotTR != machineWant {
						t.Errorf("tool_result must be alias+brand only (never sanitized):\ngot  %q\nwant %q", gotTR, machineWant)
					}
					if gotAsst := msgs[2].(map[string]any)["content"]; gotAsst != machineWant {
						t.Errorf("assistant must be alias+brand only (never sanitized):\ngot  %q\nwant %q", gotAsst, machineWant)
					}
					if gotTool := msgs[3].(map[string]any)["content"]; gotTool != machineWant {
						t.Errorf("role:tool must be alias+brand only (never sanitized):\ngot  %q\nwant %q", gotTool, machineWant)
					}
				})
			}
		}
	})

	// Case 2: stripping </system-reminder> via omp_prompt_sanitizations between
	// two spans must leave "User typed bash" between them byte-for-byte intact
	// while both spans sanitize and cloak declared tools.
	t.Run("strip-close-tag-preserves-user-typed-turn", func(t *testing.T) {
		const cfgYAML = `
use_default_keywords: true
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "</system-reminder>": ""
  "custom-harness-sig": "neutral-harness-sig"
`
		const (
			rawInput  = "<system-reminder>A custom-harness-sig use bash</system-reminder>\nUser typed bash: use bash and `bash`\n<system-reminder>B custom-harness-sig use bash</system-reminder>"
			wantInput = "<system-reminder>A neutral-harness-sig use run_command\nUser typed bash: use bash and `bash`\n<system-reminder>B neutral-harness-sig use run_command"
		)
		for _, format := range []string{"openai", "anthropic"} {
			for _, route := range []struct {
				name    string
				headers http.Header
			}{
				{name: "protected-agy", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}},
				{name: "ua-omp", headers: http.Header{"User-Agent": {"omp/19.2.1"}}},
			} {
				t.Run(format+"/"+route.name, func(t *testing.T) {
					defer restoreDefaultFilterConfig(t)
					reconfigurePluginYAML(t, cfgYAML)

					reqID := "issue70-fused-case2-" + format + "-" + route.name
					defer completePluginRequest(t, reqID)

					root := map[string]any{
						"messages": []any{
							map[string]any{"role": "user", "content": rawInput},
							map[string]any{
								"role": "user",
								"content": []any{
									map[string]any{"type": "text", "text": rawInput},
								},
							},
						},
						"tools": []any{makeTool(format, "bash")},
					}

					res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
						t,
						pluginabi.MethodRequestInterceptBefore,
						pluginapi.RequestInterceptRequest{
							RequestID:      reqID,
							SourceFormat:   format,
							Model:          "agy/gemini-3.8-flash",
							RequestedModel: "agy/gemini-3.8-flash",
							Headers:        route.headers,
							Body:           mustMarshalJSONBytes(t, root),
						},
					)
					if res.Terminate || len(res.Body) == 0 {
						t.Fatalf("unexpected result: terminate=%t len=%d resp=%s", res.Terminate, len(res.Body), res.ResponseBody)
					}

					var got map[string]any
					mustUnmarshalJSON(t, res.Body, &got)
					msgs := got["messages"].([]any)
					if gotUser0 := msgs[0].(map[string]any)["content"]; gotUser0 != wantInput {
						t.Errorf("user[0] string mismatch:\ngot  %q\nwant %q", gotUser0, wantInput)
					}
					gotUser1 := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
					if gotUser1 != wantInput {
						t.Errorf("user[1] text block mismatch:\ngot  %q\nwant %q", gotUser1, wantInput)
					}
				})
			}
		}
	})

	// Zero-tools edge case: requests with NO declared tools on eligible OMP
	// routes must still undergo Inbound Sanitization on <system-reminder> spans
	// while keeping User-Typed Turns, tool_result, and assistant untouched.
	t.Run("zero-tools-still-sanitizes-reminders", func(t *testing.T) {
		const cfgYAML = `
use_default_keywords: false
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "<system-reminder>": "<note>"
  "</system-reminder>": "</note>"
  "custom-harness-sig": "neutral-harness-sig"
`
		for _, format := range []string{"openai", "anthropic"} {
			for _, route := range []struct {
				name    string
				headers http.Header
			}{
				{name: "protected-agy", headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}}},
				{name: "ua-omp", headers: http.Header{"User-Agent": {"omp/19.2.1"}}},
			} {
				t.Run(format+"/"+route.name, func(t *testing.T) {
					defer restoreDefaultFilterConfig(t)
					reconfigurePluginYAML(t, cfgYAML)

					reqID := "issue70-fused-notools-" + format + "-" + route.name
					defer completePluginRequest(t, reqID)

					const (
						rawSpan   = "<system-reminder><system-conventions>R</system-conventions> custom-harness-sig</system-reminder>"
						wantSpan  = "<note><conventions>R</conventions> neutral-harness-sig</note>"
						rawUntch  = "<system-reminder>custom-harness-sig</system-reminder>"
						rawUser0  = "User typed <system-conventions> custom-harness-sig\n" + rawSpan
						wantUser0 = "User typed <system-conventions> custom-harness-sig\n" + wantSpan
						rawUser1  = "Block typed custom-harness-sig\n" + rawSpan
						wantUser1 = "Block typed custom-harness-sig\n" + wantSpan
					)

					root := map[string]any{
						"messages": []any{
							map[string]any{"role": "user", "content": rawUser0},
							map[string]any{
								"role": "user",
								"content": []any{
									map[string]any{"type": "text", "text": rawUser1},
									map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": rawUntch},
								},
							},
							map[string]any{"role": "assistant", "content": rawUntch},
						},
					}

					res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
						t,
						pluginabi.MethodRequestInterceptBefore,
						pluginapi.RequestInterceptRequest{
							RequestID:      reqID,
							SourceFormat:   format,
							Model:          "agy/gemini-3.8-flash",
							RequestedModel: "agy/gemini-3.8-flash",
							Headers:        route.headers,
							Body:           mustMarshalJSONBytes(t, root),
						},
					)
					if res.Terminate || len(res.Body) == 0 {
						t.Fatalf("expected sanitized body without tools, got terminate=%t len=%d resp=%s", res.Terminate, len(res.Body), res.ResponseBody)
					}

					var got map[string]any
					mustUnmarshalJSON(t, res.Body, &got)
					msgs := got["messages"].([]any)
					if gotUser0 := msgs[0].(map[string]any)["content"]; gotUser0 != wantUser0 {
						t.Errorf("user[0] mismatch:\ngot  %q\nwant %q", gotUser0, wantUser0)
					}
					user1 := msgs[1].(map[string]any)["content"].([]any)
					if gotUser1 := user1[0].(map[string]any)["text"]; gotUser1 != wantUser1 {
						t.Errorf("user[1] text block mismatch:\ngot  %q\nwant %q", gotUser1, wantUser1)
					}
					if gotTR := user1[1].(map[string]any)["content"]; gotTR != rawUntch {
						t.Errorf("tool_result unexpectedly sanitized:\ngot  %q\nwant %q", gotTR, rawUntch)
					}
					if gotAsst := msgs[2].(map[string]any)["content"]; gotAsst != rawUntch {
						t.Errorf("assistant unexpectedly sanitized:\ngot  %q\nwant %q", gotAsst, rawUntch)
					}
				})
			}
		}
	})

	// Negative gates preserved: model_prefixes mismatch and /v1/responses
	// (carriesOpenAIResponses == true) must NOT sanitize <system-reminder> spans.
	t.Run("negative-gates-preserve-reminders", func(t *testing.T) {
		const cfgYAML = `
use_default_keywords: true
model_prefixes:
  - "agy/"
omp_prompt_sanitizations:
  "<system-reminder>": "<note>"
  "</system-reminder>": "</note>"
  "custom-harness-sig": "neutral-harness-sig"
`
		for _, tc := range []struct {
			name           string
			format         string
			model          string
			headers        http.Header
			wantZeroMutate bool
			wantClearHdr   bool
		}{
			{
				name:           "protected-agy-model-prefix-mismatch",
				format:         "openai",
				model:          "openai/gpt-5",
				headers:        http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				wantZeroMutate: true,
				wantClearHdr:   true,
			},
			{
				name:           "ua-omp-model-prefix-mismatch",
				format:         "openai",
				model:          "openai/gpt-5",
				headers:        http.Header{"User-Agent": {"omp/19.2.1"}},
				wantZeroMutate: true,
			},
			{
				name:         "protected-agy-responses-carrier",
				format:       "openai-response",
				model:        "agy/gemini-3.8-flash",
				headers:      http.Header{"X-Cloak-Client": {"oh_my_pi"}},
				wantClearHdr: true,
			},
			{
				name:    "ua-omp-responses-carrier",
				format:  "openai-response",
				model:   "agy/gemini-3.8-flash",
				headers: http.Header{"User-Agent": {"omp/19.2.1"}},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				defer restoreDefaultFilterConfig(t)
				reconfigurePluginYAML(t, cfgYAML)

				reqID := "issue70-fused-neg-" + tc.name
				defer completePluginRequest(t, reqID)

				const rawReminder = "<system-reminder>custom-harness-sig use bash</system-reminder>"
				root := map[string]any{
					"messages": []any{
						map[string]any{"role": "user", "content": rawReminder},
					},
					"tools": []any{makeTool(tc.format, "bash")},
				}
				body := mustMarshalJSONBytes(t, root)
				res := callPluginABI[pluginapi.RequestInterceptRequest, pluginapi.RequestInterceptResponse](
					t,
					pluginabi.MethodRequestInterceptBefore,
					pluginapi.RequestInterceptRequest{
						RequestID:      reqID,
						SourceFormat:   tc.format,
						Model:          tc.model,
						RequestedModel: tc.model,
						Headers:        tc.headers,
						Body:           body,
					},
				)
				if res.Terminate {
					t.Fatalf("unexpected terminate: %s", res.ResponseBody)
				}
				if tc.wantClearHdr && !reflect.DeepEqual(res.ClearHeaders, []string{"X-Cloak-Client"}) {
					t.Fatalf("ClearHeaders = %v, want [X-Cloak-Client]", res.ClearHeaders)
				}
				if tc.wantZeroMutate {
					if len(res.Body) != 0 && !bytes.Equal(res.Body, body) {
						t.Fatalf("expected zero body mutation, got %s", res.Body)
					}
					return
				}
				if bytes.Contains(res.Body, []byte("<note>")) ||
					bytes.Contains(res.Body, []byte("</note>")) ||
					bytes.Contains(res.Body, []byte("neutral-harness-sig")) {
					t.Fatalf("negative route unexpectedly sanitized <system-reminder>: %s", res.Body)
				}
				var got map[string]any
				mustUnmarshalJSON(t, res.Body, &got)
				gotUser := got["messages"].([]any)[0].(map[string]any)["content"]
				wantUser := "<system-reminder>custom-harness-sig use run_command</system-reminder>"
				if gotUser != wantUser {
					t.Errorf("responses carrier reminder mismatch:\ngot  %q\nwant %q", gotUser, wantUser)
				}
			})
		}
	})
}
