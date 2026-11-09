package main

// Issue #55 tests: the two Claude Code fingerprints, driven through the real
// plugin dispatcher (admission, response and stream interception).
//
// Contract A — the exact news sentence (native "Claude" and default-cloaked
// "Antigravity" spellings) is the only carrier of the URL replacement, and only
// in the supported system positions.
//
// Contract B — the eligible Agent declaration's model enum is translated before
// provider conversion, the translation is reversible on responses, streams and
// the next request's history, and every ineligible shape acquires no tier
// authority at all.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ─────────────────────────── fixtures ───────────────────────────

const issue55NewsNative = "If the person asks about the differences between the two, Claude can direct them to https://www.anthropic.com/news/claude-fable-5-mythos-5 for more information."

// issue55AgentSchema is the captured native Agent declaration shape.
func issue55AgentSchema() map[string]any {
	return map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"description", "prompt"},
		"properties": map[string]any{
			"description": map[string]any{"type": "string"},
			"prompt":      map[string]any{"type": "string"},
			"model": map[string]any{
				"type":        "string",
				"enum":        []any{"sonnet", "opus", "haiku", "fable"},
				"description": "Optional model override for this agent. Takes precedence over the agent definition's model frontmatter and the configured default subagent model.",
			},
		},
	}
}

func issue55AgentTool(format string) map[string]any {
	if format == "openai" {
		return map[string]any{"type": "function", "function": map[string]any{
			"name": "Agent", "description": "A general-purpose subagent.", "parameters": issue55AgentSchema(),
		}}
	}
	return map[string]any{"name": "Agent", "description": "A general-purpose subagent.", "input_schema": issue55AgentSchema()}
}

func issue55BashTool(format string) map[string]any {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cmd":   map[string]any{"type": "string"},
			"model": map[string]any{"type": "string", "description": "Optional model override for this agent."},
		},
	}
	if format == "openai" {
		return map[string]any{"type": "function", "function": map[string]any{
			"name": "Bash", "description": "Run a shell command.", "parameters": schema,
		}}
	}
	return map[string]any{"name": "Bash", "description": "Run a shell command.", "input_schema": schema}
}

// issue55FixtureBody carries the system sentence, a user "bare URL" message and
// a tool-call history (owned Agent call plus an unrelated Bash call), every key
// of which must survive or translate exactly per spec.
func issue55FixtureBody(format string) []byte {
	systemText := issue55NewsNative + "\n\n# Session-specific guidance\nRun tools as needed."
	userContent := "Bare copy only: https://www.anthropic.com/news/claude-fable-5-mythos-5"
	root := map[string]any{
		"model": "agy/gemini-3.8-flash",
		"tools": []any{issue55AgentTool(format), issue55BashTool(format)},
	}
	if format == "openai" {
		root["messages"] = []any{
			map[string]any{"role": "system", "content": systemText},
			map[string]any{"role": "user", "content": userContent},
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "Agent", "arguments": `{"model":"opus","nested":{"model":"sonnet"},"prompt":"x"}`}},
				map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": "Bash", "arguments": `{"model":"opus","cmd":"ls"}`}},
			}},
		}
	} else {
		root["system"] = []any{
			map[string]any{"type": "text", "text": systemText},
		}
		root["messages"] = []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": userContent}}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{
					"model": "opus", "nested": map[string]any{"model": "sonnet"}, "prompt": "x",
				}},
				map[string]any{"type": "tool_use", "id": "tu2", "name": "Bash", "input": map[string]any{
					"model": "opus", "cmd": "ls",
				}},
			}},
		}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return raw
}

// issue55Admit drives the real request interceptor (marker header) and returns
// the transformed forward body.
func issue55Admit(t *testing.T, reqID, format string, body []byte) []byte {
	t.Helper()
	isolateRequestAliasPlan(t)
	h := map[string][]string{"X-Cloak-Client": {"claude_code"}}
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, format, "agy/probe", body, h))
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	return decodeEnvelopeBody(t, raw)
}

func issue55Decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode body: %v (%s)", err, body)
	}
	return root
}

// issue55ToolByName reads a forward declaration's schema.
func issue55ToolByName(t *testing.T, root map[string]any, format, name string) map[string]any {
	t.Helper()
	tools, _ := root["tools"].([]any)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		_, toolName, schemaValue, ok := toolDeclarationParts(tool, format)
		if !ok || toolName != name {
			continue
		}
		schema, _ := schemaValue.(map[string]any)
		return schema
	}
	t.Fatalf("forward body has no %q declaration: %s", name, root)
	return nil
}

// ─────────────────────────── Contract A ───────────────────────────

func TestIssue55NewsURLSanitizedBothCarriers(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, format := range []string{"anthropic", "openai"} {
		forward := issue55Admit(t, "issue55-url-"+format, format, issue55FixtureBody(format))
		root := issue55Decode(t, forward)
		var systemText string
		if format == "anthropic" {
			blocks, _ := root["system"].([]any)
			block, _ := blocks[0].(map[string]any)
			systemText, _ = block["text"].(string)
		} else {
			systemText, _ = root["messages"].([]any)[0].(map[string]any)["content"].(string)
		}
		// The URL became the neutral root inside the recognized sentence, and
		// the sentence prose (already brand-cloaked) is retained.
		if !strings.Contains(systemText, "direct them to https://antigravity.google for more information.") {
			t.Fatalf("%s: news URL was not sanitized in the recognized sentence: %s", format, systemText)
		}
		if strings.Contains(systemText, claudeNewsURL) {
			t.Fatalf("%s: the source news URL survived in the recognized sentence: %s", format, systemText)
		}
		// Idempotence: a second run changes nothing.
		if again, changed := sanitizeClaudeNewsSentences(systemText); changed || again != systemText {
			t.Fatalf("%s: sanitation is not idempotent: %q", format, again)
		}
		// The bare copy in the user turn stays; nothing else is walked.
		var userText any
		if format == "anthropic" {
			userText = anyOf(t, root, format, "user")
		} else {
			userText, _ = root["messages"].([]any)[1].(map[string]any)["content"].(string)
		}
		if !strings.Contains(fmt.Sprint(userText), claudeNewsURL) {
			t.Fatalf("%s: user turn lost the bare URL it never owed unsanitized: %q", format, userText)
		}
		// No other URL spelling entered the forward body anywhere.
		if strings.Contains(string(forward), "https://antigravity.google/news") {
			t.Fatalf("%s: unexpected URL variant entered the forward body: %s", format, forward)
		}
	}
}

// anyOf returns the first text string a given message role carries, in
// whichever carrier shape the format uses.
func anyOf(t *testing.T, root map[string]any, format, role string) any {
	t.Helper()
	msgs, _ := root["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if r, _ := msg["role"].(string); r == role {
			content := msg["content"]
			if format == "anthropic" {
				parts, _ := content.([]any)
				part, _ := parts[0].(map[string]any)
				return part["text"]
			}
			return content
		}
	}
	t.Fatalf("no %q message in fixture", role)
	return nil
}

func TestIssue55NewsURLNegativeScope(t *testing.T) {
	negatives := []string{
		// Path suffix: a different article path never matches.
		"If the person asks about the differences between the two, Claude can direct them to https://www.anthropic.com/news/claude-fable-5-mythos-5-2 for more information.",
		// Wrong host.
		"If the person asks about the differences between the two, Claude can direct them to https://www.example.com/news/claude-fable-5-mythos-5 for more information.",
		// Different sentence but same URL: the URL stays.
		"Read https://www.anthropic.com/news/claude-fable-5-mythos-5 at your leisure.",
		// Wrong casing of the sentence form.
		"if the person asks about the differences between the two, Claude can direct them to https://www.anthropic.com/news/claude-fable-5-mythos-5 for more information.",
	}
	for _, sentence := range negatives {
		if _, changed := sanitizeClaudeNewsSentences(sentence); changed {
			t.Fatalf("negative form was sanitized: %s", sentence)
		}
	}
}

func TestIssue55NewsURLGateFollowsUseDefaultKeywords(t *testing.T) {
	isolateRequestAliasPlan(t)
	setConfig := func(enabled bool) {
		cfg := defaultFilterConfig()
		cfg.UseDefaultKeywords = enabled
		applyFilterConfig(cfg)
	}
	h := map[string][]string{"X-Cloak-Client": {"claude_code"}}
	admit := func(reqID string) []byte {
		raw, code := handlePluginCall("request.intercept_before",
			makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/probe", issue55FixtureBody("anthropic"), h))
		if code != 0 {
			t.Fatalf("code=%d envelope=%s", code, raw)
		}
		return decodeEnvelopeBody(t, raw)
	}
	setConfig(false)
	if forward := admit("issue55-url-off"); strings.Contains(string(forward), "https://antigravity.google for more information") {
		t.Fatalf("URL rule ran with use_default_keywords disabled: %s", forward)
	}
	// The keyword gate touches only the URL rule; re-enabling the default set
	// brings exactly that transform back.
	setConfig(true)
	forward := admit("issue55-url-back")
	if !strings.Contains(string(forward), "https://antigravity.google for more information") {
		t.Fatalf("URL rule did not return with use_default_keywords enabled: %s", forward)
	}
}

func TestIssue55NewsURLSkipsWhenCustomMappingOwnsTheURL(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	cfg.CustomMappings = []rewriteMapping{{Match: "anthropic.com/news/claude-fable-5-mythos-5", Replacement: "example.com/custom-news", WholeSegment: true}}
	applyFilterConfig(cfg)
	h := map[string][]string{"X-Cloak-Client": {"claude_code"}}
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue55-url-custom", "anthropic", "agy/probe", issue55FixtureBody("anthropic"), h))
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	forward := decodeEnvelopeBody(t, raw)
	root := issue55Decode(t, forward)
	blocks, _ := root["system"].([]any)
	systemText, _ := blocks[0].(map[string]any)["text"].(string)
	if strings.Contains(systemText, claudeNewsURL) {
		t.Fatalf("custom prose mapping did not own the URL first: %s", systemText)
	}
	if strings.Contains(systemText, "https://antigravity.google for more information") {
		t.Fatalf("the built-in rule ran over a custom-mapped sentence it did not recognize: %s", systemText)
	}
	if !strings.Contains(systemText, "www.example.com/custom-news") {
		t.Fatalf("custom mapping lost: %s", systemText)
	}
}

// ─────────────────────────── Contract B ───────────────────────────

func TestIssue55AgentTypeEnumTranslatedBothCarriers(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, format := range []string{"anthropic", "openai"} {
		forward := issue55Admit(t, "issue55-enum-"+format, format, issue55FixtureBody(format))
		root := issue55Decode(t, forward)
		// The Agent declaration itself kept its own spelling (the tool names
		// cloak; the schema leaf keeps its spelling), so find it by position.
		agentSchema := issue55AgentSchemaFor(t, root, format)
		props, _ := agentSchema["properties"].(map[string]any)
		model, _ := props["model"].(map[string]any)
		enum, _ := model["enum"].([]any)
		want := []any{"gemini-flash", "gemini-pro", "gemini-flash-lite", "gemini-argon"}
		if len(enum) != len(want) {
			t.Fatalf("%s: translated enum has %d members: %v", format, len(enum), enum)
		}
		for i := range want {
			if enum[i] != want[i] {
				t.Fatalf("%s: enum member %d = %v, want %v (declared order preserved)", format, i, enum[i], want[i])
			}
		}
		// model remains optional and its own help text keeps its meaning.
		required, _ := agentSchema["required"].([]any)
		if len(required) != 2 || required[0] != "description" || required[1] != "prompt" {
			t.Fatalf("%s: required changed: %v", format, required)
		}
		if desc, _ := model["description"].(string); !strings.HasPrefix(desc, "Optional model override for this agent.") {
			t.Fatalf("%s: model description lost: %q", format, desc)
		}
		// Bash's unrelated model leaf keeps its native schema.
		bashSchema := issue55BashSchemaFor(t, root, format)
		bashProps, _ := bashSchema["properties"].(map[string]any)
		bashModel, _ := bashProps["model"].(map[string]any)
		if len(bashModel) != 2 {
			t.Fatalf("%s: Bash model leaf picked up tier machinery: %v", format, bashModel)
		}
	}
}

// issue55AgentSchemaFor finds the translated Agent schema by its model enum's
// four alias values (the declaration's name spells its upstream alias by now).
func issue55AgentSchemaFor(t *testing.T, root map[string]any, format string) map[string]any {
	t.Helper()
	tools, _ := root["tools"].([]any)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		_, _, schemaValue, ok := toolDeclarationParts(tool, format)
		if !ok {
			continue
		}
		schema, _ := schemaValue.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		model, ok := props["model"].(map[string]any)
		if !ok {
			continue
		}
		enumRaw, ok := model["enum"].([]any)
		if !ok || len(enumRaw) != 4 {
			continue
		}
		if value, _ := enumRaw[0].(string); value == claudeAgentTierAliases["sonnet"] {
			return schema
		}
	}
	t.Fatalf("forward body has no tier-translated Agent declaration: %s", root)
	return nil
}

func issue55BashSchemaFor(t *testing.T, root map[string]any, format string) map[string]any {
	t.Helper()
	tools, _ := root["tools"].([]any)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		_, name, schemaValue, ok := toolDeclarationParts(tool, format)
		if !ok || name != "run_command" {
			continue
		}
		schema, _ := schemaValue.(map[string]any)
		return schema
	}
	t.Fatalf("forward body has no Bash declaration: %s", root)
	return nil
}

func TestIssue55AgentTierHistoryForwardBothCarriers(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, format := range []string{"anthropic", "openai"} {
		forward := issue55Admit(t, "issue55-hist-"+format, format, issue55FixtureBody(format))
		root := issue55Decode(t, forward)
		msgs := root["messages"].([]any)
		assistant := msgs[len(msgs)-1].(map[string]any)
		if format == "anthropic" {
			for _, b := range assistant["content"].([]any) {
				block := b.(map[string]any)
				input, _ := block["input"].(map[string]any)
				name, _ := block["name"].(string)
				switch name {
				case "invoke_subagent": // the translated Agent identity on this wire
					if got, _ := input["model"].(string); got != "gemini-pro" {
						t.Fatalf("%s: owned history model = %q, want gemini-pro (%v)", format, got, input)
					}
					if nested, _ := input["nested"].(map[string]any); nested["model"] != "sonnet" {
						t.Fatalf("%s: nested model key was touched: %v", format, nested)
					}
				case "run_command":
					if got, _ := input["model"].(string); got != "opus" {
						t.Fatalf("%s: unowned call's model was translated: %v", format, input)
					}
				default:
					t.Fatalf("%s: unexpected history tool name %q", format, name)
				}
			}
		} else {
			for _, cRaw := range assistant["tool_calls"].([]any) {
				fn, _ := cRaw.(map[string]any)["function"].(map[string]any)
				var args map[string]any
				if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
					t.Fatalf("%s: args decode: %v", format, err)
				}
				switch fn["name"] {
				case "invoke_subagent":
					if got, _ := args["model"].(string); got != "gemini-pro" {
						t.Fatalf("%s: owned history model = %q, want gemini-pro (%v)", format, got, args)
					}
					if nested, _ := args["nested"].(map[string]any); nested["model"] != "sonnet" {
						t.Fatalf("%s: nested model key was touched: %v", format, args)
					}
				case "run_command":
					if got, _ := args["model"].(string); got != "opus" {
						t.Fatalf("%s: unowned call's model was translated: %v", format, args)
					}
				default:
					t.Fatalf("%s: unexpected history tool name %q", format, fn["name"])
				}
			}
		}
	}
}

func issue55BodyWithAgentSchema(schema map[string]any, format string) []byte {
	tool := map[string]any{"name": "Agent", "input_schema": schema}
	if format == "openai" {
		tool = map[string]any{"type": "function", "function": map[string]any{"name": "Agent", "parameters": schema}}
	}
	var history any
	if format == "anthropic" {
		history = []any{
			map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{"model": "opus"}},
		}
	} else {
		history = []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "Agent", "arguments": `{"model":"opus"}`}},
		}
	}
	root := map[string]any{
		"model": "agy/gemini-3.8-flash",
		"tools": []any{tool},
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "content": history},
		},
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestIssue55AgentTierMustMatchEligibleSignature(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { // subset enum
			props, _ := m["properties"].(map[string]any)
			props["model"].(map[string]any)["enum"] = []any{"sonnet", "opus", "haiku"}
		},
		func(m map[string]any) { // duplicate member
			props, _ := m["properties"].(map[string]any)
			props["model"].(map[string]any)["enum"] = []any{"sonnet", "sonnet", "haiku", "fable"}
		},
		func(m map[string]any) { // alias-valued enum
			props, _ := m["properties"].(map[string]any)
			props["model"].(map[string]any)["enum"] = []any{"sonnet", "opus", "haiku", "gemini-argon"}
		},
		func(m map[string]any) { // wrong leaf type
			props, _ := m["properties"].(map[string]any)
			props["model"].(map[string]any)["type"] = "integer"
		},
		func(m map[string]any) { // unrecognized help signature
			props, _ := m["properties"].(map[string]any)
			props["model"].(map[string]any)["description"] = "Model picks the subagent engine."
		},
		func(m map[string]any) { // unsupported constraint at the leaf
			props, _ := m["properties"].(map[string]any)
			props["model"].(map[string]any)["minLength"] = 1
		},
		func(m map[string]any) { // conditional composition at the root
			m["oneOf"] = []any{map[string]any{"type": "object"}}
		},
		func(m map[string]any) { // model is required
			m["required"] = []any{"description", "prompt", "model"}
		},
	} {
		schema := issue55AgentSchema()
		mutate(schema)
		for _, format := range []string{"anthropic", "openai"} {
			forward := issue55Admit(t, "issue55-sig", format, issue55BodyWithAgentSchema(schema, format))
			if strings.Contains(string(forward), claudeAgentTierAliases["sonnet"]) {
				t.Fatalf("%s: ineligible delegation translated tiers: %s", format, forward)
			}
			if !strings.Contains(string(forward), `"model":"opus"`) && !strings.Contains(string(forward), `\"model\":\"opus\"`) {
				t.Fatalf("%s: history lost the native tier without authority: %s", format, forward)
			}
		}
	}
}

func TestIssue55AgentTierNeedsACurrentDeclaration(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// A history-only Agent mention gets no tier authority and is not recovered
	// from prior requests.
	root := map[string]any{
		"model": "agy/gemini-3.8-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{"model": "opus"}},
			}},
		},
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	forward := issue55Admit(t, "issue55-history-only", "anthropic", raw)
	if strings.Contains(string(forward), claudeAgentTierAliases["opus"]) {
		t.Fatalf("history-only mention gained tier authority: %s", forward)
	}
	if !strings.Contains(string(forward), `"model":"opus"`) {
		t.Fatalf("history without a declaration was mutated: %s", forward)
	}
}

func TestIssue55TierResponseReversalNonStreamAnthropic(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	resp := `{"content":[{"type":"tool_use","name":"invoke_subagent","input":{"model":"gemini-pro"}},` +
		`{"type":"tool_use","name":"invoke_subagent","input":{"model":"gemini-flash-lite"}},` +
		`{"type":"tool_use","name":"invoke_subagent","input":{"model":"gemini-pro-max"}},` +
		`{"type":"tool_use","name":"invoke_subagent","input":{"model":"opus"}},` +
		`{"type":"tool_use","name":"invoke_subagent","input":{"model":null}}]}`
	out := issue55ReverseTierResponse(t, resp, "issue55-resp-anthropic", "anthropic")
	if !strings.Contains(out, `"model":"opus"`) {
		t.Fatalf("anthropic response did not restore the resolved tier: %s", out)
	}
	if !strings.Contains(out, `"model":"haiku"`) {
		t.Fatalf("anthropic response did not restore the flash-lite tier: %s", out)
	}
	if !strings.Contains(out, `"model":"gemini-pro-max"`) {
		t.Fatalf("alias-like suffix was mutated: %s", out)
	}
	if strings.Count(out, `"model":"opus"`) != 2 {
		t.Fatalf("already-native value was double-translated: %s", out)
	}
	if !strings.Contains(out, `"model":null`) || strings.Contains(out, `"model":""`) {
		t.Fatalf("null model was mutated: %s", out)
	}
}

func issue55ReverseTierResponse(t *testing.T, upstream, reqID, format string) string {
	t.Helper()
	issue55Admit(t, reqID, format, issue55FixtureBody(format))
	respRaw, err := json.Marshal(pluginapi.ResponseInterceptRequest{
		RequestID:         reqID,
		SourceFormat:      format,
		Model:             "agy/probe",
		RequestedModel:    "agy/probe",
		OriginalRequest:   nil,
		RequestBody:       issue55FixtureBody(format),
		Body:              []byte(upstream),
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	raw, code := handlePluginCall("response.intercept_after", respRaw)
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	return string(decodeEnvelopeBody(t, raw))
}

func TestIssue55TierResponseReversalNonStreamOpenAI(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	resp := `{"choices":[{"message":{"tool_calls":[
		{"index":0,"type":"function","function":{"name":"invoke_subagent","arguments":"{\"model\":\"gemini-flash\",\"prompt\":\"p\",\"nested\":{\"model\":\"gemini-pro\"}}"}},
		{"index":1,"type":"function","function":{"name":"run_command","arguments":"{\"model\":\"gemini-pro\",\"cmd\":\"ls\"}"}}
	]}}]}`
	out := issue55ReverseTierResponse(t, resp, "issue55-resp-openai", "openai")
	if !strings.Contains(out, `\"model\":\"sonnet\"`) && !strings.Contains(out, `"model":"sonnet"`) {
		t.Fatalf("openai response did not restore the flash tier: %s", out)
	}
	if !strings.Contains(out, `\"nested\":{\"model\":\"gemini-pro\"}`) && !strings.Contains(out, `"model":"gemini-pro"`) {
		t.Fatalf("nested key mutated: %s", out)
	}
	// The unowned call keeps the alias: the four aliases are reserved only in
	// the owned response position.
	if !strings.Contains(out, `model\":\"gemini-pro\",\"cmd`) && !strings.Contains(out, `"model":"gemini-pro\"`) {
		t.Fatalf("unowned call lost its literal: %s", out)
	}
}

func TestIssue55AgentTierIsClaudeCodeOwnership(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Another client's plan acquires no tier authority even when it declares a
	// tool with the same name and the same schema shape.
	h := map[string][]string{"X-Cloak-Client": {"codex"}}
	raw, _ := json.Marshal(map[string]any{
		"model": "agy/probe",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "Agent", "parameters": issue55AgentSchema(),
		}}},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	})
	isolateRequestAliasPlan(t)
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-codex-agent", SourceFormat: "openai", Model: "agy/probe",
		RequestedModel: "agy/probe", Body: raw, Headers: h,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, code := handlePluginCall("request.intercept_before", callRaw)
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, out)
	}
	forward := decodeEnvelopeBody(t, out)
	if strings.Contains(string(forward), claudeAgentTierAliases["sonnet"]) {
		t.Fatalf("codex request gained claude tier authority: %s", forward)
	}
}

// ─────────────── streaming: split scalars never resolve early ───────────────

func issue55ArgFrame(format string, index int, fragment string) string {
	esc, _ := json.Marshal(fragment)
	if index < 0 {
		index = 1
	}
	if format == "anthropic" {
		return "event: content_block_delta\n" +
			"data: " + `{"type":"content_block_delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"input_json_delta","partial_json":` + string(esc) + `}}` + "\n\n"
	}
	return "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":` + strconv.Itoa(index) + `,"function":{"arguments":` + string(esc) + `}}]}}]}` + "\n\n"
}

// issue55DeliverStream runs the frames through the live session and joins every
// argument fragment the client would see, including torn-out flush deltas.
func issue55DeliverStream(t *testing.T, reqID, format string, frames []string) string {
	t.Helper()
	var delivered strings.Builder
	for i, frame := range frames {
		resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			SourceFormat: format,
			Model:        "agy/probe",
			ChunkIndex:   i,
			Body:         []byte(frame),
		}, format)
		if resp.DropChunk {
			t.Fatalf("chunk %d was dropped", i)
		}
		if len(resp.Body) == 0 {
			// Untouched pass-through: nothing to collect for this chunk.
			delivered.WriteString(issue55ArgFramePassthrough(t, format, frame))
			continue
		}
		delivered.WriteString(streamedToolArgumentText(t, format, resp.Body))
	}
	return delivered.String()
}

// issue55ArgFramePassthrough extracts the argument text of a frame the plugin
// forwarded unchanged (no interception envelope body).
func issue55ArgFramePassthrough(t *testing.T, format, frame string) string {
	t.Helper()
	return streamedToolArgumentText(t, format, []byte(frame))
}

func TestIssue55ResponsesCarrierIsDeferred(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// The OpenAI Responses wire surface is deferred for both contracts: raw
	// source format "openai-response" gets neither the URL sanitation nor the
	// tier enum/history translations, while normal behavior is unchanged.
	isolateRequestAliasPlan(t)
	h := map[string][]string{"X-Cloak-Client": {"claude_code"}}
	root := map[string]any{
		"model": "agy/probe",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "Agent", "parameters": issue55AgentSchema(),
		}}},
		"system": issue55NewsNative + "\n",
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "Agent", "arguments": `{"model":"opus"}`}},
			}},
		},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue55-responses-defer", "openai-response", "agy/probe", body, h))
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	forward := string(decodeEnvelopeBody(t, raw))
	if strings.Contains(forward, "https://antigravity.google for more information") {
		t.Fatalf("URL sanitation ran on the deferred Responses carrier: %s", forward)
	}
	if !strings.Contains(forward, "Optional model override") && !strings.Contains(forward, claudeAgentTierAliases["sonnet"]) {
		t.Fatalf("fixture drift: %s", forward)
	}
	if strings.Contains(forward, claudeAgentTierAliases["sonnet"]) {
		t.Fatalf("tier enum translation ran on the deferred Responses carrier: %s", forward)
	}
	if !strings.Contains(forward, `"model":"opus"`) && !strings.Contains(forward, `\"model\":\"opus\"`) {
		t.Fatalf("history was re-encoded on the deferred Responses carrier: %s", forward)
	}
	if plan := globalAliasPlanManager.get("issue55-responses-defer"); plan == nil || plan.tier != nil {
		t.Fatalf("Responses carrier pinned tier authority: %+v", plan)
	}
}

func TestIssue55TierStreamAnthropicSplitScalars(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-anthropic"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))
	// A config reload after admission must not disturb the pinned authority.
	applyFilterConfig(defaultFilterConfig())

	frames := []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{}}}` + "\n\n",
		// "gemini-flash-lite" split mid-value: the decision must wait (never
		// resolve "sonnet" then be unable to finish "-lite").
		issue55ArgFrame("anthropic", 1, `{"model":"gemini-`),
		issue55ArgFrame("anthropic", 1, `flash`),
		issue55ArgFrame("anthropic", 1, `-lite","prompt":"p"}`),
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "anthropic", frames)
	want := `{"model":"haiku","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want %q", got, want)
	}
	if strings.Contains(got, "gemini") {
		t.Fatalf("upstream alias leaked to the client: %q", got)
	}
	if strings.Contains(got, "sonnet") {
		t.Fatalf("early scalar resolution produced a wrong tier: %q", got)
	}
}

func TestIssue55TierStreamAnthropicInterleavedAndEscaped(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-anthropic-esc"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))

	// Just like the real event shapes: instructions are payload text within the
	// delta stream, complete in one frame; none of the literals should change.
	frames := []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{}}}` + "\n\n",
		issue55ArgFrame("anthropic", 2, `{"command":"say \"gemini-flash\" loudly","model":"gemini\u002dpro"}`),
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":2}` + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "anthropic", frames)
	// The escaped alias decodes to the exact whole value and is translated;
	// the quoted literal inside another string is untouched.
	want := `{"command":"say \"gemini-flash\" loudly","model":"opus"}`
	if got != want {
		t.Fatalf("streamed args = %q, want %q", got, want)
	}
}

func TestIssue55TierStreamAnthropicIncompleteScalarReleasedRaw(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-anthropic-trunc"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))

	frames := []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{}}}` + "\n\n",
		issue55ArgFrame("anthropic", 1, `{"model":"gemini`),
		// Terminal arrives while the scalar is incomplete: the existing
		// malformed policy releases the retained bytes raw, no guessing.
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "anthropic", frames)
	want := `{"model":"gemini`
	if got != want {
		t.Fatalf("truncated scalar handling = %q, want raw release %q", got, want)
	}
}

func TestIssue55TierStreamOpenAISplitScalarsAndLanes(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-openai"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))

	nameFrame := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n"
	frames := []string{
		nameFrame,
		issue55ArgFrame("openai", 0, `{"model":"gem`),
		issue55ArgFrame("openai", 0, `ini-flash`),
		issue55ArgFrame("openai", 0, `","prompt":"p"}`),
	}
	got := issue55DeliverStream(t, reqID, "openai", frames)
	want := `{"model":"sonnet","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want %q", got, want)
	}
}

func TestIssue55TierStreamOpenAIIgnoreUnrelatedCallLane(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-openai-lane"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))

	var frames []string
	addCall := func(index int, id, name, fragment string) {
		namePayload, _ := json.Marshal([]any{map[string]any{
			"index": index, "id": id, "type": "function",
			"function": map[string]any{"name": name},
		}})
		argsPayload, _ := json.Marshal([]any{map[string]any{
			"index":    index,
			"function": map[string]any{"arguments": fragment},
		}})
		frames = append(frames, "data: "+`{"choices":[{"index":0,"delta":{"tool_calls":`+string(namePayload)+`}}]}`+"\n\n")
		frames = append(frames, "data: "+`{"choices":[{"index":0,"delta":{"tool_calls":`+string(argsPayload)+`}}]}`+"\n\n")
	}
	// Two interleaved calls; only the eligible one gains authority.
	addCall(0, "c1", "invoke_subagent", `{"model":"gemini-pro","prompt":"p"}`)
	addCall(1, "c2", "run_command", `{"model":"gemini-pro"}`)
	resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: reqID, SourceFormat: "openai", Model: "agy/probe", ChunkIndex: 0,
		Body: []byte(strings.Join(frames, "")),
	}, "openai")
	got := streamedToolArgumentText(t, "openai", resp.Body)
	if !strings.Contains(got, `"model":"opus","prompt":"p"`) {
		t.Fatalf("owned call did not reverse: %q", got)
	}
	if !strings.Contains(got, `"model":"gemini-pro"`) {
		t.Fatalf("unowned call lost its literal: %q", got)
	}
}


