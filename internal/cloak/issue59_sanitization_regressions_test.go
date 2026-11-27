package cloak

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestProtectedBrandLeavesBlockMetadataAndToolArgumentsIntact(t *testing.T) {
	isolateOMPMeasurement(t)
	defer restoreDefaultFilterConfig(t)
	root := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Oh My Pi runs your tasks.",
				"cache_control": map[string]any{
					"type": "ephemeral",
					"note": "omp cache hint for omp",
				},
			},
		},
		"messages": []any{
			map[string]any{
				"role": "developer",
				"content": []any{
					map[string]any{
						"type": "tool_use",
						"id":   "toolu_1",
						"name": "omp",
						"input": map[string]any{
							"name":        "omp collector",
							"platform":    "oh my pi",
							"rename_hint": "Oh My Pi",
						},
					},
				},
			},
		},
	}
	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, HandleRequestInterceptBefore, ompMeasurementRequest("p1-metadata", "anthropic", ompMeasurementJSON(t, root)), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}
	// System block prose is branded; the block's cache_control is data the
	// harness reads back, not prose, so it must survive byte-for-byte.
	sysBlock := got["system"].([]any)[0].(map[string]any)
	if prose := sysBlock["text"]; prose != "Antigravity runs your tasks." {
		t.Fatalf("system prose not branded: %q", prose)
	}
	cc := sysBlock["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" || cc["note"] != "omp cache hint for omp" {
		t.Fatalf("cache_control metadata was rewritten: %#v", cc)
	}
	// tool_use.input is validated tool data, never prose.
	input := got["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["input"].(map[string]any)
	for key, want := range map[string]string{
		"name":        "omp collector",
		"platform":    "oh my pi",
		"rename_hint": "Oh My Pi",
	} {
		if input[key] != want {
			t.Fatalf("tool_use.input[%q] was rewritten: %q (want %q)", key, input[key], want)
		}
	}
	if name := got["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["name"]; name != "wp_ext_08e7f56dd7fdc17a26f5a51affb5d7de" {
		t.Fatalf("unexpected fallback alias for unknown tool name: %q", name)
	}
}

func TestOMPPromptSanitizationsDoNotCascadeIntoOperatorsLongestOverride(t *testing.T) {
	isolateOMPMeasurement(t)
	raw, code := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
omp_prompt_sanitizations:
  "RFC 2119": "Short marker"
  "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.": "Specific full override. Keep RFC 2119 exact."
`)), nil), 0
	if code != 0 {
		t.Fatalf("reconfigure code = %d, body = %s", code, raw)
	}
	defer restoreDefaultFilterConfig(t)
	// The longer override's replacement contains the shorter rule's match as
	// literal content. One left-to-right longest-match-first pass must emit
	// the longer rule's replacement verbatim - the shorter rule never sees
	// that span within the pass.
	in := "prefix RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. suffix"
	out, changed := sanitizeOMPPromptText(in, activeFilterConfig().ompSanitizationRules)
	const want = "prefix Specific full override. Keep RFC 2119 exact. suffix"
	if !changed || out != want {
		t.Fatalf("cascade corrupted the longer override's replacement:\ngot  %q\nwant %q", out, want)
	}
	// The shorter rule still works on its own span.
	out3, changed3 := sanitizeOMPPromptText("plain RFC 2119 text", activeFilterConfig().ompSanitizationRules)
	if !changed3 || out3 != "plain Short marker text" {
		t.Fatalf("shorter rule broken: %q changed=%v", out3, changed3)
	}
}

func TestProtectedAGYProseCloakLeavesStructuralFieldsIntact(t *testing.T) {
	isolateOMPMeasurement(t)
	defer restoreDefaultFilterConfig(t)
	// The structural contract is discriminating under a DEVELOPER message:
	// the pre-fix recursive brand walker rewrote every string inside
	// system/developer content, including a tool_use block's input values and
	// any metadata note; the fixed walker keeps them byte-for-byte while the
	// prose and declared tool names still carry the aliases.
	root := map[string]any{
		"system": []any{map[string]any{
			"type":          "text",
			"text":          "Oh My Pi orchestrates with bash.",
			"cache_control": map[string]any{"type": "ephemeral", "note": "omp hint"},
		}},
		"messages": []any{
			map[string]any{
				"role": "developer",
				"content": []any{
					map[string]any{"type": "text", "text": "Oh My Pi used bash"},
					map[string]any{
						"type":          "tool_use",
						"id":            "toolu_2",
						"name":          "grep",
						"input":         map[string]any{"pattern": "with bash", "note": "omp pattern"},
						"cache_control": map[string]any{"type": "ephemeral", "note": "omp"},
					},
				},
			},
		},
		"tools": []any{
			map[string]any{"name": "bash", "input_schema": map[string]any{"type": "object"}},
			map[string]any{"name": "grep", "input_schema": map[string]any{"type": "object"}},
		},
	}
	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, HandleRequestInterceptBefore, ompMeasurementRequest("p-structural", "anthropic", ompMeasurementJSON(t, root)), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}
	sysBlock := got["system"].([]any)[0].(map[string]any)
	if text := sysBlock["text"]; text != "Antigravity orchestrates with run_command." {
		t.Fatalf("system prose not branded: %q", text)
	}
	if note := sysBlock["cache_control"].(map[string]any)["note"]; note != "omp hint" {
		t.Fatalf("system cache_control.note was rewritten: %q", note)
	}
	blocks := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	// Developer prose is branded (system/developer scope) and the quoted tool
	// name aliases.
	if text := blocks[0].(map[string]any)["text"]; text != "Antigravity used bash" {
		t.Fatalf("developer prose not branded: %q", text)
	}
	// tool_use block: structural fields stay byte-for-byte; the declared
	// tool name aliases.
	tu := blocks[1].(map[string]any)
	if tu["name"] != "grep_search" {
		t.Fatalf("tool_use.name not aliased through the canonical set: %q", tu["name"])
	}
	if tu["id"] != "toolu_2" {
		t.Fatalf("tool_use.id was rewritten: %q", tu["id"])
	}
	input := tu["input"].(map[string]any)
	if input["pattern"] != "with bash" {
		t.Fatalf("tool_use.input was prose-rewritten: %#v", input)
	}
	if input["note"] != "omp pattern" {
		t.Fatalf("tool_use.input note was brand-rewritten: %q", input["note"])
	}
	cc := tu["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" || cc["note"] != "omp" {
		t.Fatalf("tool_use cache_control was rewritten: %#v", cc)
	}
}

func TestOMPPromptSanitizationUnicodeRuleBounded(t *testing.T) {
	isolateOMPMeasurement(t)
	raw, code := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
omp_prompt_sanitizations:
  "règle spéciale": "X"
`)), nil), 0
	if code != 0 {
		t.Fatalf("reconfigure code = %d, body = %s", code, raw)
	}
	defer restoreDefaultFilterConfig(t)
	rules := activeFilterConfig().ompSanitizationRules
	if len(rules) == 0 {
		t.Fatal("unicode rule not admitted")
	}
	// A non-ASCII rule matches case-insensitively through the regex path and
	// is bounded: one pass, no quadratic rescan. The haystack is large
	// (>= 1 MiB, no match after the single real one) so a quadratic rescan
	// or unanchored re-scan behavior shows up as a wall-time blowup under
	// -timeout rather than silently passing.
	in := "une RÈGLE SPÉCIALE ici " + strings.Repeat("remplissage ", 90_000)
	out, changed := sanitizeOMPPromptText(in, rules)
	if !changed || !strings.HasPrefix(out, "une X ici ") {
		t.Fatalf("unicode rule did not match case-insensitively: changed=%v out=%.60q", changed, out)
	}
	if len(out) != len(in)-len("règle spéciale")+1 {
		t.Fatalf("unexpected output length: %d", len(out))
	}
}

func TestUAOMPProseCloakLeavesStructuralFieldsIntact(t *testing.T) {
	isolateOMPMeasurement(t)
	defer restoreDefaultFilterConfig(t)
	// Discriminating surface on the UA lane: the system field and a
	// developer message carry the prose AND structural fields (cache_control,
	// tool_use input values). The pre-fix recursive pass rewrote every string
	// there; the fixed block-prose helper keeps the structural fields
	// byte-for-byte while branding the prose and aliasing the tool name.
	root := map[string]any{
		"system": []any{map[string]any{
			"type":          "text",
			"text":          "Oh My Pi orchestrates with bash.",
			"cache_control": map[string]any{"type": "ephemeral", "note": "omp hint"},
		}},
		"messages": []any{
			map[string]any{
				"role": "developer",
				"content": []any{
					map[string]any{"type": "input_text", "text": "Oh My Pi used bash"},
					map[string]any{
						"type":          "tool_use",
						"id":            "toolu_3",
						"name":          "grep",
						"input":         map[string]any{"pattern": "with bash", "note": "omp pattern"},
						"cache_control": map[string]any{"type": "ephemeral", "note": "omp"},
					},
				},
			},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "bash", "parameters": map[string]any{"type": "object"}}},
			map[string]any{"type": "function", "function": map[string]any{"name": "grep", "parameters": map[string]any{"type": "object"}}},
		},
	}
	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, HandleRequestInterceptBefore, uaOMPRequest(t, "ua-structural", "openai", root), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}
	sysBlock := got["system"].([]any)[0].(map[string]any)
	if text := sysBlock["text"]; text != "Antigravity orchestrates with run_command." {
		t.Fatalf("system prose not branded on UA lane: %q", text)
	}
	if note := sysBlock["cache_control"].(map[string]any)["note"]; note != "omp hint" {
		t.Fatalf("system cache_control.note was rewritten: %q", note)
	}
	blocks := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	// Developer prose on the UA lane is branded; the bare ambiguous "bash"
	// inside prose stays literal.
	if text := blocks[0].(map[string]any)["text"]; text != "Antigravity used bash" {
		t.Fatalf("developer prose not branded on UA lane: %q", text)
	}
	tu := blocks[1].(map[string]any)
	// The tool_use.name field inside a message is structural on the UA lane:
	// on the OpenAI carrier a message-embedded Anthropic-style block keeps
	// its literal name field (the alias pass rewrites prose references and
	// the declared tools[] surface; verified behavior, not a bug pin).
	if tu["name"] != "grep" {
		t.Fatalf("tool_use.name was rewritten: %q", tu["name"])
	}
	if tu["id"] != "toolu_3" {
		t.Fatalf("tool_use.id was rewritten: %q", tu["id"])
	}
	if input := tu["input"].(map[string]any)["pattern"]; input != "with bash" {
		t.Fatalf("tool_use.input was prose-rewritten: %#v", input)
	}
	if note := tu["input"].(map[string]any)["note"]; note != "omp pattern" {
		t.Fatalf("tool_use.input note was brand-rewritten: %q", note)
	}
	cc := tu["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" || cc["note"] != "omp" {
		t.Fatalf("tool_use cache_control was rewritten: %#v", cc)
	}
}

func TestUAOMPBrandProseLeavesStructuralFieldsIntact(t *testing.T) {
	isolateOMPMeasurement(t)
	defer restoreDefaultFilterConfig(t)
	root := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Oh My Pi runs your tasks.",
				"cache_control": map[string]any{
					"type": "ephemeral",
					"note": "omp cache hint",
				},
			},
		},
		"messages": []any{
			map[string]any{
				"role": "developer",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "Oh My Pi rules for omp",
						"extra": map[string]any{
							"owner": "oh my pi",
						},
					},
				},
			},
		},
	}
	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, HandleRequestInterceptBefore, uaOMPRequest(t, "ua-brand-structural", "openai", root), &result)
	if result.Terminate {
		t.Fatalf("unexpected termination: %s", result.ResponseBody)
	}
	var got map[string]any
	if err := json.Unmarshal(result.Body, &got); err != nil {
		t.Fatal(err)
	}
	sysBlock := got["system"].([]any)[0].(map[string]any)
	if prose := sysBlock["text"]; prose != "Antigravity runs your tasks." {
		t.Fatalf("system prose not branded: %q", prose)
	}
	if note := sysBlock["cache_control"].(map[string]any)["note"]; note != "omp cache hint" {
		t.Fatalf("system cache_control.note was rewritten: %q", note)
	}
	devBlock := got["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if prose := devBlock["text"]; prose != "Antigravity rules for antigravity" {
		t.Fatalf("developer prose not branded: %q", prose)
	}
	if owner := devBlock["extra"].(map[string]any)["owner"]; owner != "oh my pi" {
		t.Fatalf("developer non-prose metadata was rewritten: %q", owner)
	}
}

// TestOMPSanitizationRuleDoesNotFalseMatchMidRune pins the rune-width scan of
// sanitizeOMPPromptText: a VALID operator rule whose match begins with U+FFFD
// must never false-match the interior bytes of a valid multi-byte character
// (a slice starting at a continuation byte decodes as U+FFFD), and the output
// must stay valid UTF-8.
func TestOMPSanitizationRuleDoesNotFalseMatchMidRune(t *testing.T) {
	isolateOMPMeasurement(t)
	defer restoreDefaultFilterConfig(t)
	rules := []ompSanitizationEntry{{Match: "\ufffd" + "omp", Replacement: "X"}}
	rs := buildOMPSanitizationRules(rules)
	if len(rs) == 0 {
		t.Fatal("rule fixture rejected at admission")
	}
	hay := "éomp text"
	out, changed := sanitizeOMPPromptText(hay, rs)
	if changed || out != hay {
		t.Fatalf("false match into multi-byte character: changed=%v out=%q", changed, out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
	// The rule still fires on a genuine U+FFFD occurrence.
	out2, changed2 := sanitizeOMPPromptText("\ufffd"+"omp here", rs)
	if !changed2 || out2 != "X here" {
		t.Fatalf("genuine U+FFFD rule no longer matches: %q changed=%v", out2, changed2)
	}
}

// Round-2 review follow-up: a post-match multi-byte rune after the last
// replacement must survive the pass intact (guards a truncation bug at the
// builder boundary).
func TestOMPSanitizationMidRuneTailIntact(t *testing.T) {
	isolateOMPMeasurement(t)
	defer restoreDefaultFilterConfig(t)
	rules := []ompSanitizationEntry{{Match: "règle", Replacement: "X"}}
	rs := buildOMPSanitizationRules(rules)
	hay := "une règle 規約 fin"
	out, changed := sanitizeOMPPromptText(hay, rs)
	if !changed || out != "une X 規約 fin" {
		t.Fatalf("multi-byte tail corrupted: changed=%v out=%q", changed, out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
}
