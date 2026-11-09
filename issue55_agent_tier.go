package main

// Issue #55: two evidenced Claude Code identity fingerprints, per the spec.
//
// Contract A — known system news URL. The exact captured news sentence (native
// "Claude" spelling and the default-cloaked "Antigravity" spelling) carries the
// Anthropic news link. Only the URL inside a recognized sentence is replaced
// with https://antigravity.google. The rule is request-only, one-way, gated on
// use_default_keywords, and applied after the existing prose rewrites in the
// supported carrier positions only.
//
// Contract B — reversible Agent model tiers. The native Claude Code Agent
// declaration's model enum (sonnet/opus/haiku/fable) is translated to the
// upstream tier aliases before CPA derives provider help, and the translation
// is made reversible: response tool calls, streamed argument fragments, and the
// next request's tool-call history all map back and forth through the
// request-scoped alias plan authority. The client-wide argument string-reversal
// tables stay untouched (they are not JSON-path-aware); the tier work uses a
// dedicated structured scanner that only ever touches the decoded top-level
// string property "model" of an argument object owned by the eligible call.

import (
	"encoding/json"
	"strings"
)

// ─────────────────────────── Contract A ───────────────────────────

// claudeNewsURL is the exact source link from the captured system sentence.
const claudeNewsURL = "https://www.anthropic.com/news/claude-fable-5-mythos-5"

// claudeNewsURLSanitized is the neutral root the recognized sentence carries upstream.
const claudeNewsURLSanitized = "https://antigravity.google"

// claudeNewsSentencePrefix forms the two recognized sentences together with the
// exact URL. Case-sensitive, exact: a different sentence, URL suffix, host or
// casing never matches. Form 0 is the native capture; form 1 is what the
// existing default brand rewrite turns it into.
const claudeNewsSentencePrefix = "If the person asks about the differences between the two, "

var claudeNewsSentenceForms = [2]string{
	claudeNewsSentencePrefix + "Claude can direct them to " + claudeNewsURL + " for more information.",
	claudeNewsSentencePrefix + "Antigravity can direct them to " + claudeNewsURL + " for more information.",
}

// sanitizeClaudeNewsSentences replaces only the URL inside a recognized exact
// sentence; every other byte, including any bare copy of the URL elsewhere,
// passes through unchanged. Idempotent: the replacement sentence no longer
// contains the source URL, so a second pass matches nothing.
func sanitizeClaudeNewsSentences(text string) (string, bool) {
	changed := false
	for _, form := range claudeNewsSentenceForms {
		if !strings.Contains(text, form) {
			continue
		}
		replacement := strings.Replace(form, claudeNewsURL, claudeNewsURLSanitized, 1)
		text = strings.ReplaceAll(text, form, replacement)
		changed = true
	}
	return text, changed
}

// sanitizeSystemNewsURL applies Contract A to the supported carrier positions
// only: Anthropic top-level system (string or type:"text" blocks), OpenAI
// messages with role system/developer (string content or type:"text" blocks).
// Nothing else is walked: no user/assistant/tool messages, no nested "system"
// keys, no tool payloads, no reminder spans.
func sanitizeSystemNewsURL(root map[string]any, format string) bool {
	switch format {
	case "anthropic":
		switch sys := root["system"].(type) {
		case string:
			if next, changed := sanitizeClaudeNewsSentences(sys); changed {
				root["system"] = next
				return true
			}
		case []any:
			changed := false
			for _, raw := range sys {
				block, ok := raw.(map[string]any)
				if !ok || block["type"] != "text" {
					continue
				}
				txt, ok := block["text"].(string)
				if !ok {
					continue
				}
				if next, one := sanitizeClaudeNewsSentences(txt); one {
					block["text"] = next
					changed = true
				}
			}
			return changed
		}
	case "openai":
		changed := false
		for _, msg := range sliceOfMaps(root["messages"]) {
			role, _ := msg["role"].(string)
			if role != "system" && role != "developer" {
				continue
			}
			switch content := msg["content"].(type) {
			case string:
				if next, one := sanitizeClaudeNewsSentences(content); one {
					msg["content"] = next
					changed = true
				}
			case []any:
				for _, partRaw := range content {
					part, ok := partRaw.(map[string]any)
					if !ok || part["type"] != "text" {
						continue
					}
					txt, ok := part["text"].(string)
					if !ok {
						continue
					}
					if next, one := sanitizeClaudeNewsSentences(txt); one {
						part["text"] = next
						changed = true
					}
				}
			}
		}
		return changed
	}
	return false
}

// ─────────────────────────── Contract B ───────────────────────────

// claudeAgentToolSourceName is the native Claude Code subagent tool. The tier
// feature is bounded to this one declaration; the upstream identity it is
// translated to is never hardcoded, it is resolved through the admitted plan.
const claudeAgentToolSourceName = "Agent"

// claudeAgentTierAliases is the native tier -> upstream alias vocabulary from
// the spec. These are schema/argument aliases, not gateway model IDs: the
// body's routing model and provider selection are untouched.
var claudeAgentTierAliases = map[string]string{
	"opus":   "gemini-pro",
	"sonnet": "gemini-flash",
	"haiku":  "gemini-flash-lite",
	"fable":  "gemini-argon",
}

// claudeAgentTierNatives inverts the alias map so response-side reverse
// translation never guesses: an alias maps back to exactly its native tier.
var claudeAgentTierNatives = func() map[string]string {
	out := make(map[string]string, len(claudeAgentTierAliases))
	for native, alias := range claudeAgentTierAliases {
		out[alias] = native
	}
	return out
}()

// claudeAgentModelDescriptionPrefix is the recognizable help signature of the
// captured declaration; an altered or unrecognized signature leaves the tier
// feature inactive instead of guessing.
const claudeAgentModelDescriptionPrefix = "Optional model override for this agent."

// requestTierAuthority is pinned inside the admitting requestAliasPlan for the
// request that advertised the translatable enum. No cross-request memory and no
// second lifecycle manager: it dies with the plan at request.complete.
type requestTierAuthority struct {
	sourceName   string // native Claude Code declaration name, exactly "Agent"
	upstreamName string // the effective upstream identity from the plan's forward map
}

// ownsToolName reports whether a wire tool identity belongs to the pinned
// declaration: either its native source name (after response-side restoration,
// or before request-side cloaking) or its admitted upstream identity (a raw
// upstream name only counts when the plan itself admitted that identity).
func (t *requestTierAuthority) ownsToolName(name string) bool {
	if t == nil {
		return false
	}
	return name == t.sourceName || (t.upstreamName != "" && name == t.upstreamName)
}

func (t *requestTierAuthority) translateForward(value string) (string, bool) {
	if t == nil {
		return "", false
	}
	alias, ok := claudeAgentTierAliases[value]
	return alias, ok
}

func (t *requestTierAuthority) translateReverse(value string) (string, bool) {
	if t == nil {
		return "", false
	}
	native, ok := claudeAgentTierNatives[value]
	return native, ok
}

// toolDeclarationParts reads one declaration's owner object, source name and
// schema in the carrier's shape. The owner carries the description; the schema
// holds the model property leaf.
func toolDeclarationParts(tool map[string]any, sourceFormat string) (owner map[string]any, name string, schema any, ok bool) {
	if sourceFormat == "openai" {
		fn, hasFn := tool["function"].(map[string]any)
		if !hasFn {
			return nil, "", nil, false
		}
		name, _ = fn["name"].(string)
		return fn, name, fn["parameters"], name != ""
	}
	name, _ = tool["name"].(string)
	return tool, name, tool["input_schema"], name != ""
}

// agentTierRootSchemaKeys is the captured root-shape allowlist. Anything else -
// references, composition, conditional or dependent constraints - leaves the
// tier feature inactive rather than being interpreted.
var agentTierRootSchemaKeys = map[string]bool{
	"$schema":              true,
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"description":          true,
	"title":                true,
}

// agentTierModelLeafKeys is the captured model-leaf allowlist. A constraint the
// spec does not track (pattern, format, minLength, ...) deactivates the feature.
var agentTierModelLeafKeys = map[string]bool{
	"type":        true,
	"enum":        true,
	"description": true,
	"title":       true,
}

func agentTierKeysAllowlisted(schema map[string]any, allowed map[string]bool) bool {
	for key := range schema {
		if !allowed[key] {
			return false
		}
	}
	return true
}

// agentTierSchemaEligible decides whether a declaration's schema matches the
// captured contract exactly: ordinary object root, direct optional string
// `model` property with the recognizable description prefix and an enum of
// exactly the four distinct native tiers in any order. `model` must be absent
// from `required`.
func agentTierSchemaEligible(schemaValue any) bool {
	schema, ok := schemaValue.(map[string]any)
	if !ok {
		return false
	}
	if !agentTierKeysAllowlisted(schema, agentTierRootSchemaKeys) {
		return false
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		return false
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	modelRaw, exists := props["model"]
	if !exists {
		return false
	}
	model, ok := modelRaw.(map[string]any)
	if !ok {
		return false
	}
	if !agentTierKeysAllowlisted(model, agentTierModelLeafKeys) {
		return false
	}
	if typ, _ := model["type"].(string); typ != "string" {
		return false
	}
	descr, _ := model["description"].(string)
	if !strings.HasPrefix(descr, claudeAgentModelDescriptionPrefix) {
		return false
	}
	enumRaw, ok := model["enum"].([]any)
	if !ok || len(enumRaw) != len(claudeAgentTierAliases) {
		return false
	}
	seen := make(map[string]bool, len(enumRaw))
	for _, member := range enumRaw {
		value, ok := member.(string)
		if !ok {
			return false
		}
		if _, native := claudeAgentTierAliases[value]; !native {
			return false
		}
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	if required, ok := schema["required"].([]any); ok {
		for _, member := range required {
			if name, ok := member.(string); ok && name == "model" {
				return false
			}
		}
	}
	return true
}

// detectAgentTierAuthority scans the RAW (pre-cloak) declarations of an
// admitted request for the eligible native Agent declaration, resolving its
// effective upstream identity through the plan's forward map. It mutates
// nothing; the returned authority (nil when unsupported) is pinned by the
// caller in the plan, which is the certificate every later half consumes.
func detectAgentTierAuthority(requestBody []byte, sourceFormat string, plan *requestAliasPlan) *requestTierAuthority {
	if plan == nil || plan.client != "claude_code" || (sourceFormat != "anthropic" && sourceFormat != "openai") {
		// The tier vocabulary is Claude Code ownership; another client's plan
		// acquires no authority even when it declares a tool with the same name.
		return nil
	}
	root, ok := decodeStrictProtectedJSON(requestBody)
	if !ok {
		return nil
	}
	tools, _ := root["tools"].([]any)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		_, name, schema, ok := toolDeclarationParts(tool, sourceFormat)
		if !ok || name != claudeAgentToolSourceName {
			continue
		}
		target, hasTarget := plan.forward[name]
		if !hasTarget || target == "" {
			return nil
		}
		if !agentTierSchemaEligible(schema) {
			return nil
		}
		debugLog("agent tier: eligible declaration pinned source=%q upstream=%q", name, target)
		return &requestTierAuthority{sourceName: name, upstreamName: target}
	}
	return nil
}

// translateAgentTierEnum translates ONLY the eligible declaration's model enum
// members, preserving declared order, before CPA's provider conversion. Every
// other literal in the schema keeps the #50/#51 behavior (the generic walker is
// untouched). The eligibility check runs again against the live schema so a
// drift between the detection snapshot and the body being mutated cannot
// translate an unsupported shape.
func translateAgentTierEnum(root map[string]any, tier *requestTierAuthority, sourceFormat string) bool {
	if tier == nil {
		return false
	}
	changed := false
	tools, _ := root["tools"].([]any)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		_, name, schema, ok := toolDeclarationParts(tool, sourceFormat)
		if !ok || name != tier.sourceName || !agentTierSchemaEligible(schema) {
			continue
		}
		props, _ := schema.(map[string]any)["properties"].(map[string]any)
		model, _ := props["model"].(map[string]any)
		enumRaw, ok := model["enum"].([]any)
		if !ok {
			continue
		}
		next := make([]any, len(enumRaw))
		copy(next, enumRaw)
		for i, member := range next {
			if value, ok := member.(string); ok {
				if alias, native := claudeAgentTierAliases[value]; native {
					next[i] = alias
				}
			}
		}
		model["enum"] = next
		changed = true
		debugLog("agent tier: enum translated source=%q", tier.sourceName)
	}
	return changed
}

// applyTierArgsForwardHistory re-encodes the current request's assistant
// tool-call history: the decoded top-level string property `model` of an
// argument object owned by the eligible declaration goes native tier ->
// upstream alias, exact whole-value matches only. A history-only name without a
// currently eligible declaration grants no authority.
func applyTierArgsForwardHistory(root map[string]any, tier *requestTierAuthority, sourceFormat string) bool {
	if tier == nil {
		return false
	}
	changed := false
	msgs, _ := root["messages"].([]any)
	for _, mRaw := range msgs {
		msg, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		switch sourceFormat {
		case "anthropic":
			contents, _ := msg["content"].([]any)
			for _, cRaw := range contents {
				block, ok := cRaw.(map[string]any)
				if !ok || block["type"] != "tool_use" || !tier.ownsToolName(stringOf(block["name"])) {
					continue
				}
				input, ok := block["input"].(map[string]any)
				if !ok {
					continue
				}
				if alterTopLevelModelScalar(input, tier.translateForward) {
					changed = true
				}
			}
		case "openai":
			if role, _ := msg["role"].(string); role != "assistant" {
				continue
			}
			for _, callRaw := range sliceOfMaps(msg["tool_calls"]) {
				fn, _ := callRaw["function"].(map[string]any)
				if fn == nil || !tier.ownsToolName(stringOf(fn["name"])) {
					continue
				}
				args, ok := fn["arguments"].(string)
				if !ok {
					continue
				}
				next := patchJSONTopLevelModel(args, tier.translateForward)
				if next != args {
					fn["arguments"] = next
					changed = true
				}
			}
		}
	}
	return changed
}

// applyTierToResponseBody reverses the tier vocabulary on the non-stream
// response body: Anthropic tool_use input maps and OpenAI decoded
// function.arguments strings map upstream alias -> native tier, only for calls
// owned by the pinned declaration and only at the top-level `model` scalar.
func applyTierToResponseBody(node any, tier *requestTierAuthority) (any, bool) {
	if tier == nil {
		return node, false
	}
	changed := false
	switch typed := node.(type) {
	case map[string]any:
		if t, hasType := typed["type"].(string); hasType && t == "tool_use" && tier.ownsToolName(stringOf(typed["name"])) {
			if input, ok := typed["input"].(map[string]any); ok {
				if alterTopLevelModelScalar(input, tier.translateReverse) {
					changed = true
				}
			}
		}
		for _, callRaw := range sliceOfMaps(typed["tool_calls"]) {
			fn, _ := callRaw["function"].(map[string]any)
			if fn == nil || !tier.ownsToolName(stringOf(fn["name"])) {
				continue
			}
			args, ok := fn["arguments"].(string)
			if !ok {
				continue
			}
			next := patchJSONTopLevelModel(args, tier.translateReverse)
			if next != args {
				fn["arguments"] = next
				changed = true
			}
		}
		for key, child := range typed {
			if next, one := applyTierToResponseBody(child, tier); one {
				typed[key] = next
				changed = true
			}
		}
	case []any:
		for i, child := range typed {
			if next, one := applyTierToResponseBody(child, tier); one {
				typed[i] = next
				changed = true
			}
		}
	}
	return node, changed
}

// applyTierResponseBodyBytes decodes a response body, applies the tier patch,
// and re-encodes when it changed. It is the handler-side entry for the
// non-stream response carrier.
func applyTierResponseBodyBytes(body []byte, tier *requestTierAuthority) ([]byte, bool) {
	if tier == nil || len(body) == 0 {
		return body, false
	}
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return body, false
	}
	next, changed := applyTierToResponseBody(root, tier)
	if !changed {
		return body, false
	}
	raw, err := safeMarshal(next)
	if err != nil {
		return body, false
	}
	return raw, true
}

// alterTopLevelModelScalar patches the decoded top-level string `model` of an
// argument object in place through whole-value table matches only.
func alterTopLevelModelScalar(args map[string]any, translate func(string) (string, bool)) bool {
	value, ok := args["model"].(string)
	if !ok {
		return false
	}
	next, ok := translate(value)
	if !ok {
		return false
	}
	args["model"] = next
	return true
}

func stringOf(raw any) string {
	value, _ := raw.(string)
	return value
}

// ─────────────── the structured argument scanner ───────────────

// tierJSONStringForScalar encodes a replacement native tier value as a JSON
// string token, so it can be spliced into a partial_json fragment stream.
// Native tier values are plain ASCII, so the marshal cannot fail.
func tierJSONStringForScalar(value string) string {
	raw, _ := safeMarshal(value)
	return string(raw)
}

type tierScanState int

const (
	tierScanKey tierScanState = iota // expecting a key (an array holds no keys)
	tierScanColon
	tierScanValue
	tierScanDelim // a value completed; expects , } ] or EOF
	tierScanDone  // the argument object completed; further bytes pass raw
)

// tierArgsScanner is the structured, JSON-aware scanner for argument-object
// fragments. It translates ONLY the decoded top-level string property "model"
// of the argument object, matching a complete decoded scalar before replacing
// it, and never rewriting anything nested. Anything it cannot validate is
// passed through raw (malformed input is released, never repaired or guessed).
type tierArgsScanner struct {
	translate func(string) (string, bool)

	state     tierScanState
	stack     []bool // per open container: true = object, false = array
	inLiteral bool   // inside a non-string value token (number/true/false/null)
	broken    bool

	inString   bool
	escaped    bool
	stringRole tierScanState // state when the current string opened (Key or Value)

	keyRaw      []byte // raw bytes of the current top-level key token
	topLevelKey bool
	keyWasModel bool // the last completed top-level key decoded exactly to "model"

	holdingModel bool
	held         []byte // raw bytes held from the value's opening quote, quote included
}

func newTierArgsScanner(translate func(string) (string, bool)) *tierArgsScanner {
	return &tierArgsScanner{translate: translate, state: tierScanValue}
}

// feed consumes the next fragment and returns the bytes that are safe to emit
// now (their replacement decision is known). Bytes of a potentially matching
// scalar are withheld until the complete decoded value is known; every other
// consumed byte - structure, keys, non-matching values - passes through
// unchanged in its source order.
func (s *tierArgsScanner) feed(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if s.broken {
			// Bytes already emitted stay emitted; the rest passes raw.
			out.WriteString(text[i:])
			break
		}
		c := text[i]
		if s.holdingModel {
			s.held = append(s.held, c)
			if len(s.held) > 65536 {
				// Bound the hold; past it the scan cannot be trusted to
				// resolve a tier decision, so everything goes raw.
				s.breakScan(&out)
				continue
			}
			if s.escaped {
				s.escaped = false
				continue
			}
			if c == '\\' {
				s.escaped = true
				continue
			}
			if c == '"' {
				s.decideHeld(&out)
			}
			continue
		}
		// Hold-first: the opening quote of a top-level model value must go into
		// the hold buffer, not the passthrough stream. Decide that BEFORE the
		// default byte emission below.
		if !s.inString && c == '"' && s.state == tierScanValue && len(s.stack) == 1 && s.keyWasModel {
			s.inString = true
			s.escaped = false
			s.stringRole = tierScanValue
			s.keyWasModel = false
			s.holdingModel = true
			s.held = append(s.held[:0], c)
			continue
		}
		// Default passthrough: every consumed byte outside a held scalar is
		// handed back in the order it arrived.
		out.WriteByte(c)
		if s.inString {
			if s.escaped {
				s.escaped = false
				continue
			}
			if c == '\\' {
				s.escaped = true
				continue
			}
			if c != '"' {
				if s.topLevelKey && s.stringRole == tierScanKey {
					s.keyRaw = append(s.keyRaw, c)
				}
				continue
			}
			// Closing quote.
			if s.stringRole == tierScanKey {
				s.closeKeyToken()
				s.inString = false
				s.state = tierScanColon
			} else {
				s.inString = false
				s.state = tierScanDelim
			}
			continue
		}
		if s.inLiteral {
			switch c {
			case ' ', '\t', '\r', '\n', ',', ':', '}', ']':
				s.inLiteral = false
				// fall through to structural handling below
			default:
				continue
			}
		}
		switch c {
		case '"':
			switch s.state {
			case tierScanKey:
				s.inString = true
				s.escaped = false
				s.stringRole = tierScanKey
				s.topLevelKey = len(s.stack) == 1
				if s.topLevelKey {
					s.keyRaw = s.keyRaw[:0]
				}
			case tierScanValue:
				s.inString = true
				s.escaped = false
				s.stringRole = tierScanValue
				s.keyWasModel = false
			default:
				s.breakScan(&out)
			}
		case '{', '[':
			if s.state == tierScanDone && c == '{' {
				// Sequential complete calls can share one lane; a new object
				// restarts the scan for the next argument payload.
				s.reset()
			}
			if s.state != tierScanValue {
				s.breakScan(&out)
				continue
			}
			s.stack = append(s.stack, c == '{')
			s.keyWasModel = false
			if c == '{' {
				s.state = tierScanKey
			} else {
				s.state = tierScanValue
			}
		case '}':
			if (s.state != tierScanDelim && s.state != tierScanKey) || len(s.stack) == 0 || !s.stack[len(s.stack)-1] {
				s.breakScan(&out)
				continue
			}
			s.stack = s.stack[:len(s.stack)-1]
			if len(s.stack) == 0 {
				s.state = tierScanDone
			} else {
				s.state = tierScanDelim
			}
		case ']':
			if (s.state != tierScanDelim && s.state != tierScanValue) || len(s.stack) == 0 || s.stack[len(s.stack)-1] {
				s.breakScan(&out)
				continue
			}
			s.stack = s.stack[:len(s.stack)-1]
			if len(s.stack) == 0 {
				s.state = tierScanDone
			} else {
				s.state = tierScanDelim
			}
		case ',':
			if len(s.stack) == 0 || s.state != tierScanDelim {
				s.breakScan(&out)
				continue
			}
			if s.stack[len(s.stack)-1] {
				s.state = tierScanKey
			} else {
				s.state = tierScanValue
			}
		case ':':
			if s.state != tierScanColon {
				s.breakScan(&out)
				continue
			}
			s.state = tierScanValue
		case ' ', '\t', '\r', '\n':
			// Whitespace between tokens.
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
			if s.state != tierScanValue {
				s.breakScan(&out)
				continue
			}
			s.keyWasModel = false
			s.inLiteral = true
			s.state = tierScanDelim
		default:
			// Begin-or-continue non-string literal bytes are handled by
			// inLiteral; any other byte outside a string is malformed.
			s.breakScan(&out)
		}
	}
	if s.broken {
		s.releaseHold(&out)
	}
	return out.String()
}

// releaseHold returns any bytes still held, raw and unchanged. It is the
// truncated/malformed terminal fallback: no guessing, no JSON repair.
func (s *tierArgsScanner) releaseHold(out *strings.Builder) {
	if !s.holdingModel {
		return
	}
	s.holdingModel = false
	out.Write(s.held)
	s.held = nil
}

// breakScan marks the scanner broken: bytes already emitted stay emitted, the
// rest of any further input passes raw, and pending held bytes go out raw in
// their source order (before the remainder of the current fragment).
func (s *tierArgsScanner) breakScan(out *strings.Builder) {
	s.broken = true
	s.releaseHold(out)
}

// closeKeyToken validates the completed top-level key token and compares its
// DECODED value to "model", so escapes cannot fake or break the signature. A
// key that cannot be decoded leaves the scan raw forever.
func (s *tierArgsScanner) closeKeyToken() {
	if !s.topLevelKey {
		s.keyWasModel = false
		return
	}
	var decoded string
	// s.keyRaw carries the literal BODY (between the quotes), so decode it as a
	// JSON string by wrapping it back into one.
	literal := make([]byte, 0, len(s.keyRaw)+2)
	literal = append(literal, '"')
	literal = append(literal, s.keyRaw...)
	literal = append(literal, '"')
	if err := json.Unmarshal(literal, &decoded); err != nil {
		s.broken = true
		s.keyWasModel = false
		return
	}
	s.keyWasModel = decoded == "model"
}

// reset returns the scanner to its startup state for a fresh argument payload.
func (s *tierArgsScanner) reset() {
	s.state = tierScanValue
	s.stack = s.stack[:0]
	s.inLiteral = false
	s.broken = false
	s.inString = false
	s.escaped = false
	s.stringRole = tierScanValue
	s.keyRaw = s.keyRaw[:0]
	s.topLevelKey = false
	s.keyWasModel = false
	s.holdingModel = false
	s.held = nil
}

// decideHeld resolves a completed model-value token: decode the held JSON
// string literal, translate exactly when the decoded WHOLE value matches, and
// emit the decision. A non-matching value is released unchanged.
func (s *tierArgsScanner) decideHeld(out *strings.Builder) {
	held := s.held
	s.holdingModel = false
	s.held = nil
	s.inString = false
	var decoded string
	if err := json.Unmarshal(held, &decoded); err != nil {
		out.Write(held)
		s.state = tierScanDelim
		return
	}
	if replacement, ok := s.translate(decoded); ok {
		out.WriteString(tierJSONStringForScalar(replacement))
	} else {
		out.Write(held)
	}
	s.state = tierScanDelim
}

// hasPendingHold reports whether the scanner sits inside a potentially matching
// scalar whose decision is not known yet.
func (s *tierArgsScanner) hasPendingHold() bool {
	return s.holdingModel
}

// ─────────────── stream lane plumbing ───────────────

// streamTierLane is one argument lane's tier scanner state, keyed exactly like
// the brand argument lane so interleaved eligible and unrelated calls stay
// isolated per request and per call.
type streamTierLane struct {
	scan *tierArgsScanner
}

func (sess *streamSession) armTierLane(laneKey string) {
	if sess.tier == nil {
		return
	}
	if sess.tierLanes == nil {
		sess.tierLanes = make(map[string]*streamTierLane)
	}
	if _, exists := sess.tierLanes[laneKey]; !exists {
		sess.tierLanes[laneKey] = &streamTierLane{scan: newTierArgsScanner(sess.tier.translateReverse)}
		debugLog("agent tier lane armed key=%s", laneKey)
	}
}

// armTierToolCall arms the lane for an OpenAI tool call whose named owner is
// the pinned declaration, before the call's argument fragments are applied.
func (sess *streamSession) armTierToolCall(laneKey, fnName string) {
	if sess.tier != nil && sess.tier.ownsToolName(fnName) {
		sess.armTierLane(laneKey)
	}
}

// armTierContentBlock arms the lane for an Anthropic content block whose type
// and name mark the pinned call; a completed input object is patched directly.
func (sess *streamSession) armTierContentBlock(laneKey string, block map[string]any) {
	if sess.tier == nil {
		return
	}
	typ, _ := block["type"].(string)
	if typ != "tool_use" || !sess.tier.ownsToolName(stringOf(block["name"])) {
		return
	}
	sess.armTierLane(laneKey)
	if input, ok := block["input"].(map[string]any); ok {
		alterTopLevelModelScalar(input, sess.tier.translateReverse)
	}
}

// tierLaneArmed reports whether this args lane must run the tier pass.
func (sess *streamSession) tierLaneArmed(laneKey string) bool {
	return sess.tier != nil && sess.tierLanes != nil && sess.tierLanes[laneKey] != nil
}

// applyTierLane runs the tier pass over an args lane's text. terminal marks a
// flush/finalize: any still-incomplete scalar is released raw unchanged.
// The changed flag is byte-level: withholding fragments for a decision is a
// change the caller must deliver.
func applyTierLane(sess *streamSession, laneKey, text string, terminal bool) (string, bool) {
	tierLane := sess.tierLanes[laneKey]
	if tierLane == nil || tierLane.scan == nil {
		return text, false
	}
	out := tierLane.scan.feed(text)
	if terminal {
		var extra strings.Builder
		tierLane.scan.releaseHold(&extra)
		out += extra.String()
	}
	if out == text {
		return text, false
	}
	debugLog("agent tier lane %s terminal=%t -> %q", laneKey, terminal, out)
	return out, true
}

// resolveTierFlushText is the ONE flushed-text resolver for argument lanes: the
// resolved brand carry still passes the tier scanner, so a scalar completed at
// flush time translates and an incomplete one is released raw in its proper
// argument carrier.
func resolveTierFlushText(sess *streamSession, laneKey, text string) string {
	if sess.tier == nil {
		return text
	}
	if _, _, isArgs := toolArgsLaneParts(laneKey); !isArgs || !sess.tierLaneArmed(laneKey) {
		return text
	}
	out, _ := applyTierLane(sess, laneKey, text, true)
	return out
}

// resolveLaneFlushText resolves one lane's flushed text end to end: the brand
// carry through its lane table, then the tier pass. Both flush sites (the
// filtered-event generator and the per-block lane flush) route through here, so
// their hold/resolution decisions cannot drift apart.
func resolveLaneFlushText(sess *streamSession, laneKey, carry string, lastIsWord bool) string {
	resolved, _ := replaceInsensitiveSetWithPrev(carry, lastIsWord, laneReverseTable(sess, laneKey))
	return resolveTierFlushText(sess, laneKey, resolved)
}

// hasPendingTierHold reports whether any tier lane still withholds bytes that
// a later fragment could complete; the session must not be freed before those
// bytes are resolved or released.
func hasPendingTierHold(sess *streamSession) bool {
	if sess == nil || sess.tierLanes == nil {
		return false
	}
	for _, lane := range sess.tierLanes {
		if lane != nil && lane.scan != nil && lane.scan.hasPendingHold() {
			return true
		}
	}
	return false
}

// patchJSONTopLevelModel re-encodes one COMPLETE argument string (history,
// non-stream responses): only the decoded top-level string `model` scalar is
// translated; everything else keeps its exact bytes. A truncated suffix is
// released byte-for-byte instead of guessed.
func patchJSONTopLevelModel(args string, translate func(string) (string, bool)) string {
	if args == "" {
		return args
	}
	scan := newTierArgsScanner(translate)
	out := scan.feed(args)
	var extra strings.Builder
	scan.releaseHold(&extra)
	out += extra.String()
	if out == args {
		return args
	}
	return out
}
