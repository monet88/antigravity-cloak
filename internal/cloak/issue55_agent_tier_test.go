package cloak

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
	raw := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, format, "agy/probe", body, h))
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
		raw := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/probe", issue55FixtureBody("anthropic"), h))
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
	raw := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue55-url-custom", "anthropic", "agy/probe", issue55FixtureBody("anthropic"), h))
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
	var assistant map[string]any
	if format == "anthropic" {
		history = []any{
			map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{"model": "opus"}},
		}
		assistant = map[string]any{"role": "assistant", "content": history}
	} else {
		history = []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "Agent", "arguments": `{"model":"opus"}`}},
		}
		// OpenAI carries tool calls in their own field; putting them under
		// content left the real history gate untested.
		assistant = map[string]any{"role": "assistant", "tool_calls": history}
	}
	root := map[string]any{
		"model": "agy/gemini-3.8-flash",
		"tools": []any{tool},
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			assistant,
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
		func(m map[string]any) { // required present but not an array
			m["required"] = "model"
		},
		func(m map[string]any) { // required array with a non-string member
			m["required"] = []any{"description", "prompt", "model", 7}
		},
		func(m map[string]any) { // required present with the wrong container type
			m["required"] = map[string]any{"model": true}
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
		RequestID:       reqID,
		SourceFormat:    format,
		Model:           "agy/probe",
		RequestedModel:  "agy/probe",
		OriginalRequest: nil,
		RequestBody:     issue55FixtureBody(format),
		Body:            []byte(upstream),
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	raw := HandleResponseIntercept(respRaw)
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
	out := HandleRequestInterceptBefore(callRaw)
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
	raw := HandleRequestInterceptBefore(makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue55-responses-defer", "openai-response", "agy/probe", body, h))
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

// ─────────────── authority boundaries (Issue #55 gates) ───────────────

// TestIssue55FilteredFlushKeepsUnselectedTierHold pins the standalone flush
// ordering: only the lanes being delivered may be resolved. Resolving a tier
// lane releases its withheld scalar, so resolving every lane first and
// filtering afterwards dropped the bytes of a call that was still streaming.
func TestIssue55FilteredFlushKeepsUnselectedTierHold(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-filtered-tier-hold"
	root := issue55Decode(t, issue55FixtureBody("openai"))
	root["n"] = 2 // two choices: finishing one must not disturb the other
	raw, err := safeMarshal(root)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	issue55Admit(t, reqID, "openai", raw)

	call := func(id, arguments string) string {
		return `{"index":0,"id":"` + id + `","type":"function","function":{"name":"invoke_subagent","arguments":` +
			string(mustMarshalJSON(t, arguments)) + `}}`
	}
	chunks := []string{
		`{"id":"c","object":"chat.completion.chunk","choices":[` +
			`{"index":0,"delta":{"tool_calls":[` + call("c1", `{"model":"gem`) + `]},"finish_reason":null},` +
			`{"index":1,"delta":{"tool_calls":[` + call("c2", `{"model":"gem`) + `]},"finish_reason":null}]}`,
		// Choice 0 finishes; choice 1 is still streaming.
		`{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c","object":"chat.completion.chunk","choices":[{"index":1,"delta":{"tool_calls":[` +
			`{"index":0,"function":{"arguments":` + string(mustMarshalJSON(t, `ini-flash","prompt":"p"}`)) + `}}]},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","choices":[{"index":1,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	args := map[int]string{}
	for i, chunk := range chunks {
		resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			SourceFormat: "openai",
			Model:        "agy/probe",
			ChunkIndex:   i,
			Body:         []byte(chunk),
		}, "openai")
		if resp.DropChunk {
			t.Fatalf("chunk %d was dropped", i)
		}
		delivered := resp.Body
		if len(delivered) == 0 {
			delivered = []byte(chunk)
		}
		for index, fragment := range issue55OpenAIChoiceArguments(t, delivered) {
			args[index] += fragment
		}
		if i == 1 {
			sess := globalStreamManager.getSession("req:" + reqID)
			if sess == nil {
				t.Fatal("session lost after choice 0 finished")
			}
			pending := 0
			for key, lane := range sess.tierLanes {
				if strings.HasPrefix(key, "openai:1") && lane.scan.hasPendingHold() {
					pending++
				}
			}
			if pending == 0 {
				t.Fatal("finishing choice 0 released choice 1's withheld scalar")
			}
		}
	}
	if got, want := args[1], `{"model":"sonnet","prompt":"p"}`; got != want {
		t.Fatalf("choice 1 args = %q, want %q", got, want)
	}
}

// issue55OpenAIChoiceArguments joins the tool-call argument fragments a chunk
// carries, keyed by choice index.
func issue55OpenAIChoiceArguments(t *testing.T, body []byte) map[int]string {
	t.Helper()
	var root map[string]any
	if safeUnmarshal(body, &root) != nil {
		return nil
	}
	out := map[int]string{}
	for _, cRaw := range sliceOfAny(root["choices"]) {
		choice, _ := cRaw.(map[string]any)
		index, ok := jsonIndexValue(choice["index"])
		if !ok {
			index = 0
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			delta, _ = choice["message"].(map[string]any)
		}
		for _, callRaw := range sliceOfAny(delta["tool_calls"]) {
			call, _ := callRaw.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			if fragment, ok := fn["arguments"].(string); ok {
				out[index] += fragment
			}
		}
	}
	return out
}

// TestIssue55TierScannerDecodesEscapedModelKey proves the scanner reads the
// key as the JSON literal it is: an escaped spelling of "model" is still the
// model key, so its value keeps the tier decision and gets translated.
func TestIssue55TierScannerDecodesEscapedModelKey(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-escaped-model-key"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	nameFrame := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n"
	got := issue55DeliverStream(t, reqID, "openai", []string{
		nameFrame,
		issue55ArgFrame("openai", 0, `{"\u006dodel":"gemini-pro","prompt":"p"}`),
	})
	if !strings.Contains(got, `"\u006dodel":"opus"`) {
		t.Fatalf("escaped model key was not translated: %q", got)
	}
}

// TestIssue55TierScannerKeepsEscapedLookalikeKeyLiteral is the corrupting half:
// "m\nodel" decodes to a key that is NOT "model", so its value must pass raw.
func TestIssue55TierScannerKeepsEscapedLookalikeKeyLiteral(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-escaped-lookalike-key"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	nameFrame := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n"
	got := issue55DeliverStream(t, reqID, "openai", []string{
		nameFrame,
		issue55ArgFrame("openai", 0, `{"m\nodel":"gemini-pro"}`),
	})
	if want := `{"m\nodel":"gemini-pro"}`; got != want {
		t.Fatalf("lookalike key was corrupted: %q, want %q", got, want)
	}
}

// TestIssue55TierStaysActiveWhenACustomMappingOnlyTouchesAWordInterior proves
// the signature guard replays the real rewrite instead of a substring scan: an
// operator rule for "id" never matches inside "override", the prefix survives,
// and Contract B stays active.
func TestIssue55TierStaysActiveWhenACustomMappingOnlyTouchesAWordInterior(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	cfg.CustomMappings = []rewriteMapping{{Match: "id", Replacement: "zz"}}
	applyFilterConfig(cfg)
	body := mustMarshalJSON(t, map[string]any{
		"model":    "agy/probe",
		"tools":    []any{issue55AgentTool("anthropic")},
		"system":   []any{map[string]any{"type": "text", "text": "x"}},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	})
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-guard-word-interior", SourceFormat: "anthropic",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := HandleRequestInterceptBefore(callRaw)
	forward := string(decodeEnvelopeBody(t, raw))
	if !strings.Contains(forward, claudeAgentTierAliases["sonnet"]) {
		t.Fatalf("tier enum stayed native although the prefix survived: %s", forward)
	}
	if plan := globalAliasPlanManager.get("issue55-guard-word-interior"); plan == nil || plan.tier == nil {
		t.Fatal("tier authority was dropped for an untouched signature")
	}
}

// issue55AssistantToolUses returns the assistant history tool_use inputs keyed
// by their wire name.
func issue55AssistantToolUses(root map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, mRaw := range sliceOfAny(root["messages"]) {
		msg, _ := mRaw.(map[string]any)
		for _, cRaw := range sliceOfAny(msg["content"]) {
			block, _ := cRaw.(map[string]any)
			if block["type"] != "tool_use" {
				continue
			}
			input, _ := block["input"].(map[string]any)
			out[stringOf(block["name"])] = input
		}
	}
	return out
}

// TestIssue55TierAuthorityIgnoresAnotherToolsAgentAlias proves the native
// spelling is not a fallback: when another admitted declaration is cloaked TO
// "Agent", only the pinned upstream identity grants tier authority.
func TestIssue55TierAuthorityIgnoresAnotherToolsAgentAlias(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	mappings := copyToolMappings(cfg.ToolMappings)
	mappings["claude_code"]["OtherTool"] = "Agent"
	cfg.ToolMappings = mappings
	applyFilterConfig(cfg)

	otherTool := map[string]any{
		"name": "OtherTool",
		"input_schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"model": map[string]any{"type": "string"}},
		},
	}
	root := map[string]any{
		"model":  "agy/probe",
		"tools":  []any{issue55AgentTool("anthropic"), otherTool},
		"system": []any{map[string]any{"type": "text", "text": "x"}},
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{"model": "opus"}},
				map[string]any{"type": "tool_use", "id": "tu2", "name": "OtherTool", "input": map[string]any{"model": "opus"}},
			}},
		},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-conflicting-agent-alias", SourceFormat: "anthropic",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := HandleRequestInterceptBefore(callRaw)
	forward := issue55Decode(t, decodeEnvelopeBody(t, raw))
	uses := issue55AssistantToolUses(forward)
	if got := uses["invoke_subagent"]["model"]; got != claudeAgentTierAliases["opus"] {
		t.Fatalf("owned history call not translated: %v", uses)
	}
	if got := uses["Agent"]["model"]; got != "opus" {
		t.Fatalf("another tool's Agent alias gained tier authority: %v", uses)
	}
}

// TestIssue55TierNamespacedAgentDeclaration resolves the native identity
// through the existing namespace policy while pinning the exact spelling.
func TestIssue55TierNamespacedAgentDeclaration(t *testing.T) {
	isolateRequestAliasPlan(t)
	agent := issue55AgentTool("anthropic")
	agent["name"] = "functions:Agent"
	root := map[string]any{
		"model":  "agy/probe",
		"tools":  []any{agent},
		"system": []any{map[string]any{"type": "text", "text": "x"}},
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "functions:Agent", "input": map[string]any{"model": "opus"}},
			}},
		},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	forward := issue55Decode(t, issue55Admit(t, "issue55-namespaced-agent", "anthropic", body))
	decl, _ := sliceOfAny(forward["tools"])[0].(map[string]any)
	if stringOf(decl["name"]) != "functions:invoke_subagent" {
		t.Fatalf("namespaced declaration not cloaked: %v", decl["name"])
	}
	model, _ := decl["input_schema"].(map[string]any)["properties"].(map[string]any)["model"].(map[string]any)
	enum, _ := model["enum"].([]any)
	want := []any{"gemini-flash", "gemini-pro", "gemini-flash-lite", "gemini-argon"}
	if fmt.Sprint(enum) != fmt.Sprint(want) {
		t.Fatalf("enum = %v, want %v", enum, want)
	}
	if got := issue55AssistantToolUses(forward)["functions:invoke_subagent"]["model"]; got != "gemini-pro" {
		t.Fatalf("namespaced history not translated: %v", got)
	}
}

// TestIssue55TierAuthoritySurvivesSessionCleanup proves the tier pinned in the
// alias plan is re-attached when a disposable stream session is rebuilt.
func TestIssue55TierAuthoritySurvivesSessionCleanup(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-cleanup"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))
	globalStreamManager.deleteSession("req:" + reqID)

	frames := []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{}}}` + "\n\n",
		issue55ArgFrame("anthropic", 1, `{"model":"gemini-pro","prompt":"p"}`),
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "anthropic", frames)
	if want := `{"model":"opus","prompt":"p"}`; got != want {
		t.Fatalf("streamed args after cleanup = %q, want %q", got, want)
	}
}

// TestIssue55TierStreamAnthropicSplitKey splits the top-level key itself across
// events: the scanner must not lose the key/colon boundary.
func TestIssue55TierStreamAnthropicSplitKey(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-anthropic-key"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))

	frames := []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{}}}` + "\n\n",
		issue55ArgFrame("anthropic", 1, `{"mo`),
		issue55ArgFrame("anthropic", 1, `del":"gemini-flash-lite","prompt":"p"}`),
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "anthropic", frames)
	if want := `{"model":"haiku","prompt":"p"}`; got != want {
		t.Fatalf("split-key args = %q, want %q", got, want)
	}
}

// TestIssue55TierFullCycle runs forward enum/history -> response reversal ->
// next request's assistant history, all through the real dispatcher.
func TestIssue55TierFullCycle(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	forward := issue55Decode(t, issue55Admit(t, "issue55-cycle-1", "anthropic", issue55FixtureBody("anthropic")))
	if !strings.Contains(string(mustMarshalJSON(t, forward)), claudeAgentTierAliases["fable"]) {
		t.Fatalf("forward pass did not advertise the alias vocabulary")
	}

	upstream := `{"id":"m","type":"message","role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"invoke_subagent","input":{"model":"gemini-argon","prompt":"p"}}]}`
	out := issue55ReverseTierResponse(t, upstream, "issue55-cycle-2", "anthropic")
	if !strings.Contains(out, `"model":"fable"`) {
		t.Fatalf("response did not restore the native tier: %s", out)
	}

	root := map[string]any{
		"model":  "agy/probe",
		"tools":  []any{issue55AgentTool("anthropic")},
		"system": []any{map[string]any{"type": "text", "text": "x"}},
		"messages": []any{
			map[string]any{"role": "user", "content": "go"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{"model": "fable", "prompt": "p"}},
			}},
		},
	}
	next := issue55Decode(t, issue55Admit(t, "issue55-cycle-3", "anthropic", mustMarshalJSON(t, root)))
	if got := issue55AssistantToolUses(next)["invoke_subagent"]["model"]; got != "gemini-argon" {
		t.Fatalf("continuation history not re-encoded: %v", got)
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestIssue55TierStreamOpenAIIncompleteScalarAtFinishReleasedInline proves an
// incomplete scalar arriving in the finish_reason event itself is released raw
// in that event, not stranded past the terminal (there is no [DONE] here).
func TestIssue55TierStreamOpenAIIncompleteScalarAtFinishReleasedInline(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-openai-finish-hold"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))

	finish, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"finish_reason": "stop",
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"arguments": `{"model":"gem`},
			}}},
		}},
	})
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		"data: " + string(finish) + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "openai", frames)
	if want := `{"model":"gem`; got != want {
		t.Fatalf("terminal release = %q, want raw release %q", got, want)
	}
}

// TestIssue55TierContentBlockStartInputReversed covers a populated tool_use
// input arriving in content_block_start: the patch must reach the client.
func TestIssue55TierContentBlockStartInputReversed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-start-input"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))
	frame := "event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{"model":"gemini-pro"}}}` + "\n\n"
	resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: reqID, SourceFormat: "anthropic", Model: "agy/probe", ChunkIndex: 0, Body: []byte(frame),
	}, "anthropic")
	out := string(resp.Body)
	if !strings.Contains(out, `"model":"opus"`) || strings.Contains(out, "gemini-pro") {
		t.Fatalf("content_block_start input not reversed: %s", out)
	}
}

// TestIssue55NewsURLIsClaudeCodeOnly proves codex traffic keeps the sentence
// untouched, even though it is admitted through an alias plan.
func TestIssue55NewsURLIsClaudeCodeOnly(t *testing.T) {
	isolateRequestAliasPlan(t)
	body := mustMarshalJSON(t, map[string]any{
		"model": "agy/probe",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "exec", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		}}},
		"messages": []any{
			map[string]any{"role": "system", "content": issue55NewsNative},
			map[string]any{"role": "user", "content": "go"},
		},
	})
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-codex-news", SourceFormat: "openai",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := HandleRequestInterceptBefore(callRaw)
	forward := string(decodeEnvelopeBody(t, raw))
	if !strings.Contains(forward, claudeNewsURL) {
		t.Fatalf("codex system sentence lost its URL: %s", forward)
	}
}

// TestIssue55TierInactiveWhenProseMappingsAlterSignature: the forwarded help
// string is what the spec recognizes, so a prose mapping that rewrites it must
// leave the tier feature inactive.
func TestIssue55TierInactiveWhenProseMappingsAlterSignature(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	cfg.CustomMappings = []rewriteMapping{{
		Match:       claudeAgentModelDescriptionPrefix,
		Replacement: "Rewritten by an operator rule.",
	}}
	applyFilterConfig(cfg)
	body := mustMarshalJSON(t, map[string]any{
		"model":    "agy/probe",
		"tools":    []any{issue55AgentTool("anthropic")},
		"system":   []any{map[string]any{"type": "text", "text": "x"}},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	})
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-prose-altered", SourceFormat: "anthropic",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := HandleRequestInterceptBefore(callRaw)
	forward := string(decodeEnvelopeBody(t, raw))
	if strings.Contains(forward, claudeAgentTierAliases["sonnet"]) {
		t.Fatalf("tier enum translated despite an altered help signature: %s", forward)
	}
	if plan := globalAliasPlanManager.get("issue55-prose-altered"); plan == nil || plan.tier != nil {
		t.Fatalf("tier authority pinned despite an altered help signature")
	}
}

// TestIssue55TierResponseIgnoresNestedCallShape: only the spec's top-level
// carriers gain authority; a nested object shaped like a call is untouched.
func TestIssue55TierResponseIgnoresNestedCallShape(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	resp := `{"content":[{"type":"tool_use","name":"invoke_subagent","input":{"model":"gemini-pro","nested":{"type":"tool_use","name":"invoke_subagent","input":{"model":"gemini-pro"}}}}]}`
	out := issue55ReverseTierResponse(t, resp, "issue55-nested-call-shape", "anthropic")
	root := issue55Decode(t, []byte(out))
	block, _ := sliceOfAny(root["content"])[0].(map[string]any)
	input, _ := block["input"].(map[string]any)
	if input["model"] != "opus" {
		t.Fatalf("top-level call not reversed: %s", out)
	}
	nested, _ := input["nested"].(map[string]any)
	nestedInput, _ := nested["input"].(map[string]any)
	if nestedInput["model"] != "gemini-pro" {
		t.Fatalf("nested call shape was mutated: %s", out)
	}
}

// ───────── pre-uncloak wire identity (Issue #55 Contract B) ─────────

// A raw upstream name that merely equals the client's NATIVE spelling is not
// the admitted upstream identity, so it must not borrow the pinned
// declaration's tier authority. Only the admitted upstream identity reverses.

func TestIssue55TierIgnoresRawNativeNameInAnthropicResponse(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	resp := `{"content":[{"type":"tool_use","id":"tu","name":"Agent","input":{"model":"gemini-pro","prompt":"p"}}]}`
	out := issue55ReverseTierResponse(t, resp, "issue55-raw-agent-anthropic", "anthropic")
	if out == "" {
		// An empty envelope body means pass-through: the host forwards the
		// upstream body unchanged.
		out = resp
	}
	if strings.Contains(out, `"model":"opus"`) {
		t.Fatalf("raw native wire name gained tier authority: %s", out)
	}
	if !strings.Contains(out, `"model":"gemini-pro"`) {
		t.Fatalf("unowned wire argument was mutated: %s", out)
	}
}

func TestIssue55TierIgnoresRawNativeNameInOpenAIResponse(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	resp := `{"choices":[{"message":{"tool_calls":[{"index":0,"type":"function","function":{"name":"Agent","arguments":"{\"model\":\"gemini-pro\",\"prompt\":\"p\"}"}}]}}]}`
	out := issue55ReverseTierResponse(t, resp, "issue55-raw-agent-openai", "openai")
	if out == "" {
		out = resp
	}
	if strings.Contains(out, `opus`) {
		t.Fatalf("raw native wire name gained tier authority: %s", out)
	}
	if !strings.Contains(out, `gemini-pro`) {
		t.Fatalf("unowned wire argument was mutated: %s", out)
	}
}

func TestIssue55TierIgnoresRawNativeNameInAnthropicStream(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-raw-agent-stream-anthropic"
	issue55Admit(t, reqID, "anthropic", issue55FixtureBody("anthropic"))
	frames := []string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"Agent","input":{}}}` + "\n\n",
		issue55ArgFrame("anthropic", 1, `{"model":"gemini-pro","prompt":"p"}`),
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
	}
	got := issue55DeliverStream(t, reqID, "anthropic", frames)
	want := `{"model":"gemini-pro","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want unowned passthrough %q", got, want)
	}
}

func TestIssue55TierIgnoresRawNativeNameInOpenAIStream(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-raw-agent-stream-openai"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"Agent"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `{"model":"gemini-pro","prompt":"p"}`),
	}
	got := issue55DeliverStream(t, reqID, "openai", frames)
	want := `{"model":"gemini-pro","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want unowned passthrough %q", got, want)
	}
}

// A wire name split across chunks is still the admitted identity once
// completed, so the lane must arm from the accumulated fragments.
func TestIssue55TierStreamSplitUpstreamNameStillReverses(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-stream-split-name"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_"}}]}}]}` + "\n\n",
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"subagent"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `{"model":"gemini-pro","prompt":"p"}`),
	}
	got := issue55DeliverStream(t, reqID, "openai", frames)
	want := `{"model":"opus","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want admitted-identity reversal %q", got, want)
	}
}

// Two declarations that share the native base identity are both admitted when
// their source spelling is identical, so first-match selection would make tier
// authority depend on declaration order. Either order must acquire none.
func TestIssue55TierDuplicateAgentDeclarationsAcquireNoAuthority(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	ineligible := issue55AgentSchema()
	props, _ := ineligible["properties"].(map[string]any)
	props["model"].(map[string]any)["enum"] = []any{"sonnet", "opus", "haiku"}
	for _, order := range []string{"eligible-first", "ineligible-first"} {
		tools := []any{
			issue55AgentTool("anthropic"),
			map[string]any{"name": "Agent", "input_schema": ineligible},
		}
		if order == "ineligible-first" {
			tools[0], tools[1] = tools[1], tools[0]
		}
		root := map[string]any{
			"model":  "agy/probe",
			"tools":  tools,
			"system": []any{map[string]any{"type": "text", "text": "x"}},
			"messages": []any{
				map[string]any{"role": "user", "content": "go"},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "id": "tu1", "name": "Agent", "input": map[string]any{"model": "opus", "prompt": "x"}},
				}},
			},
		}
		body, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		forward := issue55Admit(t, "issue55-dup-agent-"+order, "anthropic", body)
		uses := issue55AssistantToolUses(issue55Decode(t, forward))
		for name, input := range uses {
			if input["model"] != "opus" {
				t.Fatalf("%s: duplicate declaration %q gained tier authority: %v", order, name, uses)
			}
		}
	}
}

// A standalone (unframed) chunk decodes its tool-call indexes through the
// number-preserving decoder, so ownership stays on the call the upstream named
// and never crosses to another index.
func TestIssue55TierStandaloneArmingKeepsToolCallIndexes(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-standalone-index"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	chunk := `{"choices":[{"index":0,"delta":{"tool_calls":[` +
		`{"index":0,"id":"c1","type":"function","function":{"name":"run_command","arguments":"{\"model\":\"gemini-pro\"}"}},` +
		`{"index":1,"id":"c2","type":"function","function":{"name":"invoke_subagent","arguments":"{\"model\":\"gemini-pro\"}"}}]}}]}`
	resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: reqID, SourceFormat: "openai", Model: "agy/probe", ChunkIndex: 0, Body: []byte(chunk),
	}, "openai")
	out := string(resp.Body)
	unowned := strings.Index(out, `\"model\":\"gemini-pro\"`)
	owned := strings.Index(out, `\"model\":\"opus\"`)
	if unowned < 0 || owned < 0 || owned < unowned {
		t.Fatalf("standalone lane ownership crossed calls: %s", out)
	}
}

// A wire name that only starts with the admitted identity is someone else's
// call: once the next fragment diverges, the lane must lose tier authority.
func TestIssue55TierStreamDivergingNameRevokesAuthority(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	mappings := copyToolMappings(cfg.ToolMappings)
	mappings["claude_code"]["OtherTool"] = "invoke_subagent_extra"
	cfg.ToolMappings = mappings
	applyFilterConfig(cfg)
	root := map[string]any{
		"model": "agy/probe",
		"tools": []any{
			issue55AgentTool("openai"),
			map[string]any{"type": "function", "function": map[string]any{
				"name": "OtherTool", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			}},
		},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-diverging-name", SourceFormat: "openai",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	HandleRequestInterceptBefore(callRaw)
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"_extra"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `{"model":"gemini-pro","prompt":"p"}`),
	}
	got := issue55DeliverStream(t, "issue55-diverging-name", "openai", frames)
	want := `{"model":"gemini-pro","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want unowned passthrough %q", got, want)
	}
}

// A fragment that equals the admitted identity is only a repeated complete
// name when the lane has no partial prefix pending; after "invoke_" it is the
// continuation of a longer identity owned by another declared tool.
func TestIssue55TierStreamResetFragmentDoesNotArm(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	mappings := copyToolMappings(cfg.ToolMappings)
	mappings["claude_code"]["OtherTool"] = "invoke_invoke_subagent"
	cfg.ToolMappings = mappings
	applyFilterConfig(cfg)
	root := map[string]any{
		"model": "agy/probe",
		"tools": []any{
			issue55AgentTool("openai"),
			map[string]any{"type": "function", "function": map[string]any{
				"name": "OtherTool", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			}},
		},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-reset-fragment", SourceFormat: "openai",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	HandleRequestInterceptBefore(callRaw)
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_"}}]}}]}` + "\n\n",
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `{"model":"gemini-pro","prompt":"p"}`),
	}
	got := issue55DeliverStream(t, "issue55-reset-fragment", "openai", frames)
	want := `{"model":"gemini-pro","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want unowned passthrough %q", got, want)
	}
}

// Two fragments that each equal the admitted identity may be one longer name
// (`invoke_subagentinvoke_subagent`); with no argument bytes in between the
// lane must treat the second as a continuation, not a repetition.
func TestIssue55TierStreamDoubledFullNameFragmentsDoNotArm(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	mappings := copyToolMappings(cfg.ToolMappings)
	mappings["claude_code"]["OtherTool"] = "invoke_subagentinvoke_subagent"
	cfg.ToolMappings = mappings
	applyFilterConfig(cfg)
	root := map[string]any{
		"model": "agy/probe",
		"tools": []any{
			issue55AgentTool("openai"),
			map[string]any{"type": "function", "function": map[string]any{
				"name": "OtherTool", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			}},
		},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-doubled-name", SourceFormat: "openai",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	HandleRequestInterceptBefore(callRaw)
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `{"model":"gemini-pro","prompt":"p"}`),
	}
	got := issue55DeliverStream(t, "issue55-doubled-name", "openai", frames)
	want := `{"model":"gemini-pro","prompt":"p"}`
	if got != want {
		t.Fatalf("streamed args = %q, want unowned passthrough %q", got, want)
	}
}

// A provider that repeats the complete name after argument bytes have already
// arrived is re-announcing the same call, so the lane keeps its authority.
func TestIssue55TierStreamRepeatedNameAfterArgumentsKeepsAuthority(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-repeat-name-after-args"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	frames := []string{
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `{"model":"gem`),
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"invoke_subagent"}}]}}]}` + "\n\n",
		issue55ArgFrame("openai", 0, `ini-pro"}`),
	}
	got := issue55DeliverStream(t, reqID, "openai", frames)
	want := `{"model":"opus"}`
	if got != want {
		t.Fatalf("streamed args = %q, want translated %q", got, want)
	}
}

// The same repeat-after-arguments stream can arrive as one coalesced chunk:
// the arming pass must count argument bytes that precede the repeated name
// inside that chunk, not only bytes from earlier chunks.
func TestIssue55TierStreamCoalescedRepeatedNameKeepsAuthority(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-coalesced-repeat"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	name := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"invoke_subagent"}}]}}]}` + "\n\n"
	argsOpen := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"model\":\"gem"}}]}}]}` + "\n\n"
	nameAgain := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"invoke_subagent"}}]}}]}` + "\n\n"
	argsClose := "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ini-pro\"}"}}]}}]}` + "\n\n"
	resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: reqID, SourceFormat: "openai", Model: "agy/probe", ChunkIndex: 0,
		Body: []byte(name + argsOpen + nameAgain + argsClose),
	}, "openai")
	got := streamedToolArgumentText(t, "openai", resp.Body)
	if got != `{"model":"opus"}` {
		t.Fatalf("coalesced streamed args = %q, want translated", got)
	}
}

// A revoked Anthropic lane must not translate a later block-start input: the
// lane keeps its scanner so bytes already held still drain, but its translator
// is neutralised.
func TestIssue55TierStreamRevokedBlockStartInputUntouched(t *testing.T) {
	isolateRequestAliasPlan(t)
	cfg := defaultFilterConfig()
	mappings := copyToolMappings(cfg.ToolMappings)
	mappings["claude_code"]["OtherTool"] = "invoke_subagent_extra"
	cfg.ToolMappings = mappings
	applyFilterConfig(cfg)
	root := map[string]any{
		"model": "agy/probe",
		"tools": []any{
			issue55AgentTool("anthropic"),
			map[string]any{"name": "OtherTool", "input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"model": map[string]any{"type": "string"}},
			}},
		},
		"system":   []any{map[string]any{"type": "text", "text": "x"}},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	callRaw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID: "issue55-revoked-block-start", SourceFormat: "anthropic",
		Model: "agy/probe", RequestedModel: "agy/probe", Body: body,
		Headers: map[string][]string{"X-Cloak-Client": {"claude_code"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	HandleRequestInterceptBefore(callRaw)
	first := "event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent","input":{}}}` + "\n\n"
	second := "event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu","name":"invoke_subagent_extra","input":{"model":"gemini-pro"}}}` + "\n\n"
	resp := globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue55-revoked-block-start", SourceFormat: "anthropic", Model: "agy/probe",
		ChunkIndex: 0, Body: []byte(first + second),
	}, "anthropic")
	out := string(resp.Body)
	if strings.Contains(out, `"model":"opus"`) {
		t.Fatalf("revoked lane translated its block-start input: %s", out)
	}
	if !strings.Contains(out, `"model":"gemini-pro"`) {
		t.Fatalf("unowned input was lost or mutated: %s", out)
	}
}

func sse(data string) string { return "data: " + data + "\n\n" }

// A lane can withhold bytes for the tier scanner without ever holding brand
// text, so stampLaneArrival never fires for it. Without an arrival stamp of its
// own its seq stays 0, which sortedCarryKeys reads as "arrived first" and
// flushes ahead of a sibling carry that really did arrive earlier.
func TestIssue55TierOnlyHeldLaneKeepsArrivalOrder(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-tier-hold-order"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	chunks := []string{
		// 1: the tool call starts, arming the lane with nothing held yet.
		sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"invoke_subagent"}}]}}]}`),
		// 2: the same choice's prose holds a partial brand token.
		sse(`{"choices":[{"index":0,"delta":{"content":"made by Antigrav"}}]}`),
		// 3: the argument lane starts withholding a tier scalar.
		sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"model\": \"gem"}}]}}]}`),
	}
	sess := globalStreamManager.getSession("req:" + reqID)
	if sess == nil {
		t.Fatal("session was not registered")
	}
	for i, chunk := range chunks {
		globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: "openai", Model: "agy/probe", ChunkIndex: i, Body: []byte(chunk),
		}, "openai")
	}
	flushes := orderedBrandFlushes(sess)
	if len(flushes) != 2 {
		t.Fatalf("want two held lanes, got %d: %+v", len(flushes), flushes)
	}
	proseKey := openAIChoiceLaneKey(map[string]any{"index": 0})
	argsKey := openAIChoiceLaneKey(map[string]any{"index": 0}) + reverseBrandLaneSuffix + "tool0"
	if flushes[0].key != proseKey || flushes[1].key != argsKey {
		t.Fatalf("flush order = %q then %q, want the earlier brand carry %q before %q",
			flushes[0].key, flushes[1].key, proseKey, argsKey)
	}
}

// The same ordering has to survive a hold that keeps growing: a lane's arrival
// is when it FIRST withheld bytes, so a further fragment of the same held
// scalar must not re-stamp it behind text that arrived in between.
func TestIssue55TierHoldKeepsFirstArrivalAcrossFragments(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const reqID = "issue55-tier-hold-growth"
	issue55Admit(t, reqID, "openai", issue55FixtureBody("openai"))
	sess := globalStreamManager.getSession("req:" + reqID)
	if sess == nil {
		t.Fatal("session was not registered")
	}
	chunks := []string{
		// 1: arm the lane, then start withholding a tier scalar.
		sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"invoke_subagent"}}]}}]}`),
		sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"model\": \"gem"}}]}}]}`),
		// 2: prose arrives while the scalar is still held.
		sse(`{"choices":[{"index":0,"delta":{"content":"made by Antigrav"}}]}`),
		// 3: the held scalar grows, but the lane's arrival is unchanged.
		sse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ini"}}]}}]}`),
	}
	for i, chunk := range chunks {
		globalStreamManager.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: "openai", Model: "agy/probe", ChunkIndex: i, Body: []byte(chunk),
		}, "openai")
	}
	flushes := orderedBrandFlushes(sess)
	if len(flushes) != 2 {
		t.Fatalf("want two held lanes, got %d: %+v", len(flushes), flushes)
	}
	proseKey := openAIChoiceLaneKey(map[string]any{"index": 0})
	argsKey := openAIChoiceLaneKey(map[string]any{"index": 0}) + reverseBrandLaneSuffix + "tool0"
	if flushes[0].key != argsKey || flushes[1].key != proseKey {
		t.Fatalf("flush order = %q then %q, want the lane that began holding first %q before %q",
			flushes[0].key, flushes[1].key, argsKey, proseKey)
	}
}
