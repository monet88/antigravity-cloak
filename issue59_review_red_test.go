package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// uaOMPRequest builds a legacy UA-resolved OMP agy/* request without the
// explicit X-Cloak-Client marker, so the route is the UA legacy path.
func uaOMPRequest(tb testing.TB, id, format string, root map[string]any) pluginapi.RequestInterceptRequest {
	tb.Helper()
	return pluginapi.RequestInterceptRequest{
		RequestID:      id,
		SourceFormat:   format,
		Model:          "agy/gemini-3.8-flash",
		RequestedModel: "agy/gemini-3.8-flash",
		Body:           ompMeasurementJSON(tb, root),
		Headers:        map[string][]string{"User-Agent": {"omp/18.18.8"}},
	}
}

// TestUAOMPToolRoleMessageKeepsBrandProse reproduces the AGY review Finding 1:
// on the UA OMP agy/* OpenAI carrier, a role:"tool" message is machine-generated
// result prose that echoed the alias names, so the pre-#59 walker
// (rewriteConversationContent) applied BOTH the brand mappings and the alias
// pass to its content. rewriteOMPConversationProse's catch-all branch applies
// only the alias fn, so an OMP brand word survives in the tool-result prose
// while tool names alias correctly - an uncloaked client fingerprint the model
// can echo back.
func TestUAOMPToolRoleMessageKeepsBrandProse(t *testing.T) {
	isolateOMPMeasurement(t)
	raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: true
`)))
	if code != 0 {
		t.Fatalf("reconfigure code = %d, body = %s", code, raw)
	}
	defer restoreDefaultFilterConfig(t)

	root := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "assistant",
				"content": "Oh My Pi ran the tool",
			},
			map[string]any{
				"role":    "tool",
				"name":    "omp",
				"content": "Oh My Pi tool result for omp",
			},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "bash", "parameters": map[string]any{"type": "object"}}}},
	}

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, uaOMPRequest(t, "ua-tool-role-brand", "openai", root), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	if len(result.Body) == 0 {
		t.Fatal("UA OMP route produced no rewritten body")
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}

	msgs := got["messages"].([]any)
	if asst := msgs[0].(map[string]any)["content"]; asst != "Antigravity ran the tool" {
		t.Fatalf("assistant prose was not branded: %q", asst)
	}
	toolMsg := msgs[1].(map[string]any)
	// The message name is a structural field owned by the alias pass, so the
	// brand word must survive there byte-for-byte (the alias table keys on the
	// client's own tool names, never on brand words).
	if name := toolMsg["name"]; name != "omp" {
		t.Fatalf("tool message name was brand-rewritten: %q", name)
	}
	// The content is machine-generated prose: it must carry both surfaces.
	if content := toolMsg["content"]; content != "Antigravity tool result for antigravity" {
		t.Fatalf("tool-role prose was not branded (Finding 1):\ngot  %q\nwant %q", content, "Antigravity tool result for antigravity")
	}
}

// The Anthropic carrier carries the same machine-generated result prose under
// role:"user" tool_result blocks; the sanitize lane must brand it there too.
func TestUAOMPToolResultProseBrandedAnthropic(t *testing.T) {
	isolateOMPMeasurement(t)
	raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: true
`)))
	if code != 0 {
		t.Fatalf("reconfigure code = %d, body = %s", code, raw)
	}
	defer restoreDefaultFilterConfig(t)

	root := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content": []any{
							map[string]any{"type": "text", "text": "Oh My Pi tool result for omp"},
						},
					},
					map[string]any{"type": "text", "text": "Oh My Pi typed here"},
				},
			},
		},
		"tools": []any{map[string]any{"name": "bash", "input_schema": map[string]any{"type": "object"}}},
	}

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, uaOMPRequest(t, "ua-tool-result-brand-anthropic", "anthropic", root), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}
	msg := got["messages"].([]any)[0].(map[string]any)
	content := msg["content"].([]any)
	resultBlock := content[0].(map[string]any)
	resultText := resultBlock["content"].([]any)[0].(map[string]any)["text"]
	if resultText != "Antigravity tool result for antigravity" {
		t.Fatalf("tool_result prose was not branded on the Anthropic carrier:\ngot  %q\nwant %q", resultText, "Antigravity tool result for antigravity")
	}
	// Typed user prose stays byte-for-byte.
	if typed := content[1].(map[string]any)["text"]; typed != "Oh My Pi typed here" {
		t.Fatalf("typed user prose was rewritten: %q", typed)
	}
}

// TestProtectedAGYCustomMappingsReachProse asserts the Protected OMP route
// honours operator custom mappings in the prose surfaces its brand pass owns
// (system, developer) and that the alias-prose pass still rewrites declared
// tool names inside assistant/tool prose, while every structural field stays
// byte-for-byte - the coverage gap the review flagged for Protected custom
// mappings. The assistant/tool custom word stays verbatim on this route by
// contract: rewriteProtectedBrand walks system/developer only, and the alias
// pass keys on declared tool names, not brand words (verified identical on
// main before this branch).
func TestProtectedAGYCustomMappingsReachProse(t *testing.T) {
	isolateOMPMeasurement(t)
	raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: false
custom_mappings:
  "operator-call": "operator-brand"
`)))
	if code != 0 {
		t.Fatalf("reconfigure code = %d, body = %s", code, raw)
	}
	defer restoreDefaultFilterConfig(t)

	root := map[string]any{
		"system": []any{map[string]any{
			"type":          "text",
			"text":          "Use operator-call now.",
			"cache_control": map[string]any{"type": "ephemeral", "note": "operator-call"},
		}},
		"messages": []any{
			map[string]any{
				"role": "developer",
				"content": []any{
					map[string]any{"type": "text", "text": "Use operator-call and the read tool."},
					map[string]any{
						"type": "tool_use", "id": "call-1", "name": "read",
						"input": map[string]any{"command": "operator-call"},
					},
				},
			},
			map[string]any{"role": "assistant", "content": "use read for the next step, operator-call ran"},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "typed operator-call"},
					map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": "use read to verify, operator-call ran"},
				},
			},
		},
		"tools": []any{map[string]any{"name": "read", "input_schema": map[string]any{"type": "object"}}},
	}

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore,
		ompMeasurementRequest("protected-custom-prose", "anthropic", ompMeasurementJSON(t, root)), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}

	sys := got["system"].([]any)[0].(map[string]any)
	if sys["text"] != "Use operator-brand now." {
		t.Fatalf("Protected system prose missed the custom mapping: %q", sys["text"])
	}
	if note := sys["cache_control"].(map[string]any)["note"]; note != "operator-call" {
		t.Fatalf("Protected cache_control.note was rewritten: %q", note)
	}

	msgs := got["messages"].([]any)
	dev := msgs[0].(map[string]any)["content"].([]any)
	if dev[0].(map[string]any)["text"] != "Use operator-brand and the view_file tool." {
		t.Fatalf("Protected developer prose missed the custom mapping/alias: %q", dev[0].(map[string]any)["text"])
	}
	devCall := dev[1].(map[string]any)
	if devCall["name"] != "view_file" {
		t.Fatalf("Protected declared tool_use name was not cloaked: %q", devCall["name"])
	}
	if !reflect.DeepEqual(devCall["input"], map[string]any{"command": "operator-call"}) {
		t.Fatalf("Protected tool_use.input was rewritten: %#v", devCall["input"])
	}

	if asst := msgs[1].(map[string]any)["content"]; asst != "use view_file for the next step, operator-call ran" {
		t.Fatalf("Protected assistant prose alias contract wrong: %q", asst)
	}

	user := msgs[2].(map[string]any)["content"].([]any)
	if typed := user[0].(map[string]any)["text"]; typed != "typed operator-call" {
		t.Fatalf("Protected typed user text was rewritten: %q", typed)
	}
	if resultBlock := user[1].(map[string]any)["content"]; resultBlock != "use view_file to verify, operator-call ran" {
		t.Fatalf("Protected tool_result payload alias contract wrong: %q", resultBlock)
	}
}

// TestUAOMPToolResultNestedMetadataStaysIntact pins the tool_result shape the
// alias/brand passes must own: a content ARRAY whose text blocks carry the
// prose and whose non-text blocks (thinking signatures, image sources) and
// metadata fields are structural. The brand+alias pass reaches the text blocks
// only; a nested image source URL or signature must survive byte-for-byte.
func TestUAOMPToolResultNestedMetadataStaysIntact(t *testing.T) {
	isolateOMPMeasurement(t)
	raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: true
`)))
	if code != 0 {
		t.Fatalf("reconfigure code = %d, body = %s", code, raw)
	}
	defer restoreDefaultFilterConfig(t)

	root := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "typed omp"},
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "omp-call",
						"content": []any{
							map[string]any{
								"type":          "text",
								"text":          "Oh My Pi omp result",
								"cache_control": map[string]any{"type": "ephemeral", "note": "omp metadata"},
							},
							map[string]any{
								"type": "image",
								"source": map[string]any{
									"type": "base64",
									"data": "omp-looks-like-a-brand-word",
								},
							},
						},
					},
				},
			},
		},
		"tools": []any{map[string]any{"name": "bash", "input_schema": map[string]any{"type": "object"}}},
	}

	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, uaOMPRequest(t, "ua-tool-result-nested", "anthropic", root), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	if len(result.Body) == 0 {
		t.Fatal("UA OMP route produced no rewritten body")
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}

	user := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if typed := user[0].(map[string]any)["text"]; typed != "typed omp" {
		t.Fatalf("typed user text was rewritten: %q", typed)
	}
	resultBlock := user[1].(map[string]any)
	resultContent := resultBlock["content"].([]any)
	textBlock := resultContent[0].(map[string]any)
	if textBlock["text"] != "Antigravity antigravity result" {
		t.Fatalf("tool_result text block was not branded: %q", textBlock["text"])
	}
	if note := textBlock["cache_control"].(map[string]any)["note"]; note != "omp metadata" {
		t.Fatalf("nested tool_result metadata was rewritten: %q", note)
	}
	imageBlock := resultContent[1].(map[string]any)
	source := imageBlock["source"].(map[string]any)
	if source["data"] != "omp-looks-like-a-brand-word" {
		t.Fatalf("nested non-text block payload was rewritten: %q", source["data"])
	}
}
