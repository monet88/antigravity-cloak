package cloak

import (
	"strings"
)

const protectedBrandSentinel = "\x00__ANTIGRAVITY_PROTECTED_OMP_BRAND__\x00"

func isOMPAlias(match string) bool {
	trimmed := strings.TrimSpace(match)
	for _, alias := range mandatoryProtectedOMPAliases {
		if strings.EqualFold(trimmed, alias) {
			return true
		}
	}
	return false
}

// protectedNonOMPMappings derives the normalized, client-scoped non-OMP brand
// mappings for protected brand rewriting once per request.
func protectedNonOMPMappings(cfg *filterConfig, client string) []rewriteMapping {
	if cfg == nil {
		return nil
	}
	// Client scoping is applied here too: this walker predates applicableMappings
	// and used to ignore rewriteMapping.Client, which would let a claude_code or
	// codex rule fire on a protected Oh My Pi request.
	inScope := func(m rewriteMapping) bool {
		return (m.Client == "" || m.Client == client) && !isOMPAlias(m.Match)
	}
	var nonOMPMappings []rewriteMapping
	if cfg.UseDefaultKeywords {
		for _, m := range brandMappingsFor(client) {
			if inScope(m) {
				nonOMPMappings = append(nonOMPMappings, m)
			}
		}
	}
	for _, m := range cfg.CustomMappings {
		if inScope(m) {
			nonOMPMappings = append(nonOMPMappings, m)
		}
	}
	return normalizeMappings(nonOMPMappings)
}

func rewriteProtectedBrandText(text string, nonOMPMappings []rewriteMapping, client string) (string, bool) {
	if text == "" {
		return text, false
	}
	current := text
	lowerCurrent := strings.ToLower(current)
	changed := false
	// Whole-sentence first: the sentinel pass below swaps the "omp" inside
	// these lines for the sentinel, after which the sentence cannot match.
	if protectedRouteClients[client] {
		for _, m := range ompIdentityLines {
			if next, rep := replaceInsensitive(current, m.Match, m.Replacement); rep {
				current = next
				lowerCurrent = strings.ToLower(current)
				changed = true
			}
		}
	}
	for _, alias := range mandatoryProtectedOMPAliases {
		if !strings.Contains(lowerCurrent, strings.ToLower(alias)) {
			continue
		}
		// The exclusion keeps a real "omp://" internal URI byte-for-byte: the
		// scheme is the client's own virtual-device address, so masking it
		// would hand the model a URI that resolves nowhere.
		aliasRule := rewriteMapping{
			Match:         alias,
			Replacement:   protectedBrandSentinel,
			NotFollowedBy: ompSchemePreserve,
		}
		if next, rep := replaceInsensitiveRule(current, aliasRule, true); rep {
			current = next
			lowerCurrent = strings.ToLower(current)
			changed = true
		}
	}

	for _, m := range nonOMPMappings {
		if !strings.Contains(lowerCurrent, m.Match) {
			continue
		}
		if next, rep := replaceInsensitiveRule(current, m, true); rep {
			current = next
			lowerCurrent = strings.ToLower(current)
			changed = true
		}
	}

	if strings.Contains(current, protectedBrandSentinel) {
		current = strings.ReplaceAll(current, protectedBrandSentinel, "Antigravity")
	}

	return current, changed
}

func rewriteProtectedBrand(rootMap map[string]any, sourceFormat, client string, cfg *filterConfig) {
	if cfg == nil {
		cfg = activeFilterConfig()
	}
	nonOMPMappings := protectedNonOMPMappings(cfg, client)
	brandText := func(s string) (string, bool) {
		return rewriteProtectedBrandText(s, nonOMPMappings, client)
	}

	// Only prose text is in scope: a block's structural fields carry data the
	// caller validates against, so rewriting cache_control metadata or a
	// tool_use argument would corrupt the request instead of cloaking it.
	if sysVal, ok := rootMap["system"]; ok {
		if next, changed := rewritePromptTextContent(sysVal, true, brandText); changed {
			rootMap["system"] = next
		}
	}
	rewriteSystemDeveloperMessages(rootMap, true, brandText)
	rewriteOMPConversationBrandTurns(rootMap, brandText)

	// Parameter help is machine-generated tool prose one or two levels below
	// the tool, so the same brand contract applies there as to the tool's own
	// description: a nested description or title naming the client or its home
	// directory must not reach the model literal, while structure and
	// literal-bearing subtrees stay byte-for-byte. The request-scoped tool
	// aliases reach these fields through the alias pass that runs after this
	// one.
	rewriteToolsProse(rootMap, sourceFormat, brandText)
}

// reverseAssistantBrandInJSON restores the resolved client's own reverse table
// over the CARRIERS a response actually carries: assistant prose, and the values
// inside tool arguments. It never rewrites a tool name, an id, a type or any
// other identity field - those belong to the exact alias-plan authority, which
// has already run by this point. A recursive all-string walk used to reach
// tool_use.name and rewrite a tool the client had declared under a spelling
// that happens to look like a cloaked token.
//
// Both carriers are processed whenever the body has them.
func reverseAssistantBrandInJSON(root any, client string) bool {
	m, ok := root.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	if contentArr, ok := m["content"].([]any); ok {
		for _, blockRaw := range contentArr {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if txt, ok := block["text"].(string); ok {
					if next, c := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client)); c {
						block["text"] = next
						changed = true
					}
				}
			case "tool_use":
				// The non-stream twin of the streamed input_json_delta: a model
				// that writes a cloaked path into a file hands the client a path
				// that does not exist. Only the VALUES are touched - the block's
				// name and id are identity fields and stay exactly as the exact
				// uncloak pass left them.
				if input, ok := block["input"]; ok {
					// The argument-safe subset only: an argument is data the
					// client executes, so a bare prose brand word the model may
					// have copied from the user's own prompt is not reversed here.
					if next, c := rewriteSystemValue(input, argumentReverseTableFor(client)); c {
						block["input"] = next
						changed = true
					}
				}
			}
		}
	}
	if choices, ok := m["choices"].([]any); ok {
		for _, chRaw := range choices {
			ch, ok := chRaw.(map[string]any)
			if !ok {
				continue
			}
			holders := make([]map[string]any, 0, 2)
			if msg, ok := ch["message"].(map[string]any); ok {
				holders = append(holders, msg)
			}
			if d, ok := ch["delta"].(map[string]any); ok {
				holders = append(holders, d)
			}
			for _, holder := range holders {
				if content, exists := holder["content"]; exists {
					if next, c := reverseBrandInOpenAIContent(content, client); c {
						holder["content"] = next
						changed = true
					}
				}
				if reverseBrandInToolCallArguments(holder, client) {
					changed = true
				}
			}
		}
	}
	return changed
}

// reverseBrandInToolCallArguments restores operational identifiers inside the
// arguments a tool call carries, on the non-stream path, treating an argument
// string as a complete fragment rather than a mid-stream hold.
func reverseBrandInToolCallArguments(holder map[string]any, client string) bool {
	changed := false
	for _, callRaw := range sliceOfMaps(holder["tool_calls"]) {
		fn, _ := callRaw["function"].(map[string]any)
		if fn == nil {
			continue
		}
		args, ok := fn["arguments"].(string)
		if !ok {
			continue
		}
		if next, c := replaceInsensitiveSetWithPrev(args, false, argumentReverseTableFor(client)); c {
			fn["arguments"] = next
			changed = true
		}
	}
	return changed
}

// isAssistantTextPartType reports whether an OpenAI content part type is
// assistant-visible text. The allowlist is text, output_text, and untyped
// (empty) parts; data/control/tool/reasoning/refusal parts keep literal
// Antigravity. All reverse-brand content paths share this predicate (Issue #21).
func isAssistantTextPartType(typ string) bool {
	return typ == "text" || typ == "output_text" || typ == ""
}

// walkOpenAITextParts rewrites every assistant-visible text string in an OpenAI
// `content` value: a plain string, an array whose entries are strings or
// text-part objects, or a single text-part object. Only those three shapes are
// touched - every control, non-text or otherwise unsupported part is left
// byte-for-byte unchanged. Containers are mutated in place; the plain-string
// shape is returned for the caller to reassign. The flag is true only when the
// rewrite changed at least one string.
func walkOpenAITextParts(content any, rewrite func(string) string) (any, bool) {
	switch v := content.(type) {
	case string:
		if next := rewrite(v); next != v {
			return next, true
		}
	case []any:
		changed := false
		for _, partRaw := range v {
			switch part := partRaw.(type) {
			case map[string]any:
				if typ, _ := part["type"].(string); !isAssistantTextPartType(typ) {
					continue
				}
				if txt, ok := part["text"].(string); ok {
					if next := rewrite(txt); next != txt {
						part["text"] = next
						changed = true
					}
				}
			case string:
				if next := rewrite(part); next != part {
					// Replace the first equal element, not the element at this
					// index: the rewrite may be stateful (a streaming lane), so
					// two equal strings need not rewrite identically.
					for i, elem := range v {
						if elem == partRaw {
							v[i] = next
							changed = true
							break
						}
					}
				}
			}
		}
		return content, changed
	case map[string]any:
		if typ, _ := v["type"].(string); !isAssistantTextPartType(typ) {
			return content, false
		}
		if txt, ok := v["text"].(string); ok {
			if next := rewrite(txt); next != txt {
				v["text"] = next
				return content, true
			}
		}
	}
	return content, false
}

func reverseBrandInOpenAIContent(content any, client string) (any, bool) {
	return walkOpenAITextParts(content, func(txt string) string {
		next, _ := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client))
		return next
	})
}

// reverseCloakedBrandBody maps the tokens this plugin introduced back onto the
// client's own spelling, so a cloaked conversation reads to the client exactly
// as it did before. It runs after tool-name uncloaking and uses the same
// recursive walk as the forward pass, with the table selected by the resolved
// client: the protected pair and home directory for oh_my_pi, the client's own
// table for claude_code and codex.
func reverseCloakedBrandBody(body []byte, client string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}
	if !reverseAssistantBrandInJSON(root, client) {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func rewriteSystemFields(value any, mappings []rewriteMapping) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			if key == "system" {
				next, childChanged := rewriteSystemValue(child, mappings)
				if childChanged {
					typed[key] = next
					changed = true
				}
				continue
			}
			next, childChanged := rewriteSystemFields(child, mappings)
			if childChanged {
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			next, childChanged := rewriteSystemFields(child, mappings)
			if childChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func rewriteSystemValue(value any, mappings []rewriteMapping) (any, bool) {
	switch typed := value.(type) {
	case string:
		next := typed
		changed := false
		for _, mapping := range mappings {
			var replaced bool
			next, replaced = replaceInsensitiveRule(next, mapping, true)
			changed = changed || replaced
		}
		return next, changed
	case map[string]any:
		changed := false
		for key, child := range typed {
			next, childChanged := rewriteSystemValue(child, mappings)
			if childChanged {
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			next, childChanged := rewriteSystemValue(child, mappings)
			if childChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// isBareVendorToken reports whether a mapping is a single bare vendor word such
// as "omp" or "claude", as opposed to a composed entry like "claude.ai",
// "Antigravity SDK" or ".gemini/CLAUDE.md". Only bare words are subject to the
// URL/path rule below; a composed entry carries its own deliberate meaning and
// is rewritten wherever it appears.
func isBareVendorToken(match string) bool {
	for i := range len(match) {
		switch match[i] {
		case '.', '-', '_', ' ', '/', '\\':
			return false
		}
	}
	return true
}

// inURLPathContext reports whether a match at [index, matchEnd) sits inside a
// URL or filesystem path rather than in prose. A path delimiter immediately
// before the match is the strong signal - "/omp/", "C:\omp\" - and a URL
// scheme covers the rest. A bare word in ordinary prose is left alone even
// when a sentence elsewhere contains a slash, so "Anthropic/Google" is still
// masked as before.
func inURLPathContext(value string, index, matchEnd int) bool {
	if index == 0 {
		return false
	}
	prev := value[index-1]
	if prev == '/' || prev == '\\' {
		return true
	}
	// Inside a real path element that starts with a dot: ".omp-backup/agent".
	if prev == '.' && (index == 1 || !isWordByte(value[index-2])) {
		return true
	}
	// A URL scheme counts only when it sits in the SAME whitespace-free token as
	// the match. Testing the whole value for "://" suppressed every brand token
	// in a 200KB system prompt that mentions a URL anywhere - which is every
	// Claude Code system prompt, so the forward rewrite silently became a no-op
	// on the largest surface the plugin has. Live acceptance caught it: Claude
	// Code's own system prompt reached upstream with 63 "Claude" and 8
	// "Anthropic" still in it.
	for i := index - 1; i >= 0; i-- {
		switch c := value[i]; c {
		case ' ', '\t', '\n', '\r', '"', '\'', '<', '>', '(', ')', ',', ';', '|', '{', '}', '[':
			return false
		case ':':
			// A scheme's slashes come AFTER the colon ("https://"), so the
			// scheme is recognised by looking forward. Testing value[i-1] and
			// value[i-2] for slashes could only ever fire on a path segment
			// that merely ended in a colon, and left every host-like token
			// whose match is not glued to a slash ("https://www.omp.ai"),
			// classified as prose and rewritten.
			return i+2 < len(value) && value[i+1] == '/' && value[i+2] == '/'
		}
	}
	return false
}

// isDotPrefixedPathSegment reports whether a bare vendor word begins a literal
// dot-prefixed path segment, the only form whose rewrite is a real directory
// remap: ".omp/agent", "C:\Users\u\.claude\settings.json". A dot-directory can
// start right after prose punctuation, because tool-parameter help reads
// "persist to .claude/scheduled_tasks.json on disk", so any non-word byte
// before the dot begins the segment. "foo.omp" is excluded because the byte
// before its dot is a word byte.
func isDotPrefixedPathSegment(value string, index, matchEnd int) bool {
	if index == 0 || value[index-1] != '.' {
		return false
	}
	dot := index - 1
	if dot != 0 && isWordByte(value[dot-1]) {
		return false
	}
	return matchEnd == len(value) || value[matchEnd] == '/' || value[matchEnd] == '\\'
}

// segmentEndsAt reports whether a match ending at matchEnd also ENDS a whole
// path element: end of string, or a separator. It is the right-hand half of
// isDotPrefixedPathSegment, and it is deliberately the same predicate: the
// reverse pass may only rewrite a spelling the forward pass could have
// produced, and the forward dot-segment remap accepts exactly these endings.
func segmentEndsAt(value string, matchEnd int) bool {
	return matchEnd == len(value) || value[matchEnd] == '/' || value[matchEnd] == '\\'
}

func replaceInsensitive(value, match, replacement string) (string, bool) {
	return replaceInsensitiveRule(value, rewriteMapping{Match: match, Replacement: replacement}, false)
}

// Dot-prefixed directory names get their own treatment in the forward brand
// rewrite, because rewriting them as prose would hand the model a path that
// does not exist on disk.

// pathSegmentReplacements map a client home directory onto the Antigravity
// equivalent, so ~/.omp/agent becomes ~/.gemini/agent rather than a dead
// .Antigravity path, inverted by ompProtectedReverseTable.
//
// Only Oh My Pi is listed. Claude Code and Codex go through pathRules instead,
// which can also rewrite the file name (CLAUDE.md -> GEMINI.md) that this map
// has no way to express. Rewriting a bare vendor token in a path also rewrote a
// user's own text during live acceptance: Claude Code wrote ".gemini" over
// ".claude" in a README edit where the remap had collapsed the two sides into
// identical bytes, so the edit silently became a no-op. The path tables avoid
// that because the reverse half is their exact mirror.
var pathSegmentReplacements = map[string]string{
	"omp": "gemini",
}

// replaceInsensitiveRule is the shared case-insensitive word-boundary matcher.
// When skipPath is true, a match that names a literal dot-prefixed path segment
// is remapped to the Antigravity home directory instead of the brand word, so
// a client's own config path becomes one that exists upstream.
func replaceInsensitiveRule(value string, m rewriteMapping, skipPath bool) (string, bool) {
	match, replacement := m.Match, m.Replacement
	if match == "" {
		return value, false
	}
	lowerValue := strings.ToLower(value)
	lowerMatch := strings.ToLower(match)

	firstIsWord := isWordByte(match[0])
	lastIsWord := isWordByte(match[len(match)-1])

	var builder strings.Builder
	start := 0
	changed := false
	for {
		index := strings.Index(lowerValue[start:], lowerMatch)
		if index < 0 {
			break
		}
		index += start
		matchEnd := index + len(match)

		hasLeftBoundary := !firstIsWord || index == 0 || !isWordByte(value[index-1])
		if m.WholeSegment && index > 0 && isWordByte(value[index-1]) {
			hasLeftBoundary = false
		}
		hasRightBoundary := !lastIsWord || matchEnd == len(value) || !isWordByte(value[matchEnd])
		if m.SegmentEnd && !segmentEndsAt(value, matchEnd) {
			hasRightBoundary = false
		}
		effectiveReplacement := replacement
		// Exclusion: a spelling this rule must let through, e.g. the OMP
		// scheme "omp://" or the native host "antigravity.google".
		if m.NotFollowedBy != "" && hasPrefixFold(value[matchEnd:], m.NotFollowedBy) {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		// URL/path rule: inside a URL or filesystem path, a bare vendor word is
		// rewritten only as a literal dot-prefixed directory segment, remapped
		// onto the client's neutral equivalent. Every other position in a path
		// is left byte-for-byte alone - "/omp/", "\omp\", "antigravity-cloak" and
		// "omp.ai" are ordinary names there, and rewriting them yields a path
		// that exists neither upstream nor on the way back. In prose nothing
		// changes and the brand is still masked.
		skip := false
		pathRemapped := false
		if skipPath && isBareVendorToken(match) && inURLPathContext(value, index, matchEnd) {
			if isDotPrefixedPathSegment(value, index, matchEnd) {
				if remapped, ok := pathSegmentReplacements[lowerMatch]; ok {
					effectiveReplacement = remapped
					pathRemapped = true
				} else {
					skip = true
				}
			} else {
				skip = true
			}
		}
		if skip {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		// A prose brand word follows the casing it was written in, so the
		// client's own sentence stays coherent: Codex -> Antigravity,
		// codex -> antigravity, CODEX -> ANTIGRAVITY. Doing it here is what
		// keeps that a property of the mechanism instead of three
		// case-insensitive rules that shadow each other.
		if !pathRemapped && isProseBrandRule(m) {
			effectiveReplacement = mirrorBrandCase(value[index:matchEnd], replacement)
		}

		if hasLeftBoundary && hasRightBoundary {
			builder.WriteString(value[start:index])
			builder.WriteString(effectiveReplacement)
			start = matchEnd
			changed = true
		} else {
			builder.WriteString(value[start : index+1])
			start = index + 1
		}
	}
	if !changed {
		return value, false
	}
	builder.WriteString(value[start:])
	return builder.String(), true
}

// isProseBrandRule reports whether a rule rewrites a spoken brand word rather
// than an operational identifier, which is the only case where the matched
// casing is allowed to travel into the replacement. A brand word is a single
// word ("Codex", "Antigravity", "omp"); a replacement may be one or two words
// ("Google Deepmind"). Tool names, SDK names, paths, model IDs and the OMP
// sentinel all contain a separator or an underscore, so they stay exactly as
// declared and are never re-cased by the text they matched.
func isProseBrandRule(m rewriteMapping) bool {
	if !isBareVendorToken(m.Match) || m.Replacement == "" {
		return false
	}
	for i := range len(m.Replacement) {
		switch c := m.Replacement[i]; {
		case c == ' ':
			continue
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			continue
		default:
			return false
		}
	}
	return true
}

// mirrorBrandCase copies the casing of the text a rule matched onto its
// replacement, so a lowercase spelling stays lowercase and an uppercase one
// stays uppercase. A mixed spelling keeps the replacement as declared.
func mirrorBrandCase(matched, replacement string) string {
	var hasLower, hasUpper bool
	for i := range len(matched) {
		switch c := matched[i]; {
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		}
	}
	switch {
	case hasLower && !hasUpper:
		return strings.ToLower(replacement)
	case hasUpper && !hasLower:
		return strings.ToUpper(replacement)
	default:
		return replacement
	}
}

// hasPrefixFold reports whether s starts with prefix, case-insensitively.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func replaceMappingWithPrev(value string, prevIsWord bool, m rewriteMapping) (string, bool) {
	match, replacement := m.Match, m.Replacement
	if match == "" {
		return value, false
	}
	lowerValue := strings.ToLower(value)
	lowerMatch := strings.ToLower(match)
	firstIsWord := isWordByte(match[0])
	lastIsWord := isWordByte(match[len(match)-1])
	proseBrand := isProseBrandRule(m)
	var builder strings.Builder
	start := 0
	changed := false
	for {
		index := strings.Index(lowerValue[start:], lowerMatch)
		if index < 0 {
			break
		}
		index += start
		matchEnd := index + len(match)
		hasLeftBoundary := !firstIsWord
		if firstIsWord {
			if index == 0 {
				hasLeftBoundary = !prevIsWord
			} else {
				hasLeftBoundary = !isWordByte(value[index-1])
			}
		}
		if m.WholeSegment && index > 0 && isWordByte(value[index-1]) {
			hasLeftBoundary = false
		}
		hasRightBoundary := !lastIsWord || matchEnd == len(value) || !isWordByte(value[matchEnd])
		if m.SegmentEnd && !segmentEndsAt(value, matchEnd) {
			hasRightBoundary = false
		}
		// Exclusion: a spelling the forward pass never introduced and that this
		// rule must hand back untouched, such as the native host
		// "antigravity.google" inside the bare-brand lane.
		if m.NotFollowedBy != "" && hasPrefixFold(value[matchEnd:], m.NotFollowedBy) {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		// Mirror of the forward URL/path rule, and the reason the round trip has
		// to be symmetric: the model echoes back whatever it was shown, including
		// a directory name the forward pass deliberately left alone because it
		// sat in the user's own text. A blind case-insensitive reverse walk turns
		// "F:/CodeBase/antigravity-cloak/" into "F:/CodeBase/omp-cloak/", and the
		// client then reads a path that does not exist. Composed entries such as
		// ".gemini/GEMINI.md" are exempt; only bare vendor words in a URL or path
		// are held back. In prose the reverse runs as before.
		if isBareVendorToken(match) && inURLPathContext(value, index, matchEnd) && !isDotPrefixedPathSegment(value, index, matchEnd) {
			builder.WriteString(value[start : index+1])
			start = index + 1
			continue
		}
		emitted := replacement
		if proseBrand {
			emitted = mirrorBrandCase(value[index:matchEnd], m.Replacement)
		}
		if hasLeftBoundary && hasRightBoundary {
			builder.WriteString(value[start:index])
			builder.WriteString(emitted)
			start = matchEnd
			changed = true
		} else {
			builder.WriteString(value[start : index+1])
			start = index + 1
		}
	}
	if !changed {
		return value, false
	}
	builder.WriteString(value[start:])
	return builder.String(), true
}

// holdIsLive reports whether a held candidate of length k can still become a
// boundary-valid match of brand, and so is worth delaying the client's text
// for. A candidate shorter than the match can always still be completed, so it
// is live. A candidate equal to the whole match is live only while its RIGHT
// boundary is still unproven: the matcher has yet to see whether a word byte
// follows, which would rule the match out. A match whose last byte is not a
// word byte is already boundary-valid at end of text, so holding it defers
// output the client could have had immediately and proves nothing.
func holdIsLive(brand string, k int) bool {
	if k <= 0 || k > len(brand) {
		return false
	}
	return k < len(brand) || isWordByte(brand[len(brand)-1])
}

// findHoldLenWithBoundary returns the longest suffix of combined that is still
// a live, possibly-incomplete match of m.Match: the bytes already prove its left
// boundary, and holdIsLive proves the match can still grow into a
// boundary-valid one. A suffix that can no longer become one is never retained
// (Issue #48).
//
// A rule with an exclusion also holds a COMPLETE match while the exclusion
// suffix is still arriving. Resolving "antigravity" as soon as its boundary is
// provable would emit "omp" and then ".google" behind it, and the client would
// read "omp.google" for bytes the forward pass never produced. Holding until
// the exclusion is decided (complete, or ruled out by the next byte) is what
// makes the exclusion chunk-safe.
func findHoldLenWithBoundary(combined string, m rewriteMapping, prevIsWord bool) int {
	brand := m.Match
	max := len(brand)
	if len(combined) < max {
		max = len(combined)
	}
	for k := max; k >= 1; k-- {
		suffix := combined[len(combined)-k:]
		if !strings.EqualFold(suffix, brand[:k]) {
			continue
		}
		pos := len(combined) - k
		var leftOK bool
		if pos == 0 {
			leftOK = !prevIsWord
		} else {
			leftOK = !isWordByte(combined[pos-1])
		}
		if leftOK && holdIsLive(brand, k) {
			return k
		}
	}
	// Exclusion hold: the match itself is complete and boundary-valid, and the
	// bytes after it are a non-empty proper prefix of the exclusion, so whether
	// this match may be rewritten is not yet known.
	if excl := m.NotFollowedBy; excl != "" {
		limit := len(excl) - 1
		if tail := len(combined) - len(brand); tail < limit {
			limit = tail
		}
		for rest := limit; rest >= 1; rest-- {
			pos := len(combined) - len(brand) - rest
			if pos < 0 || !strings.EqualFold(combined[pos:pos+len(brand)], brand) {
				continue
			}
			var leftOK bool
			if pos == 0 {
				leftOK = !prevIsWord
			} else {
				leftOK = !isWordByte(combined[pos-1])
			}
			if leftOK && strings.EqualFold(combined[pos+len(brand):], excl[:rest]) {
				return len(brand) + rest
			}
		}
	}
	return 0
}

func getBrandLane(sess *streamSession, key string) *brandLane {
	if sess.brandCarries == nil {
		sess.brandCarries = make(map[string]*brandLane)
	}
	if lane, ok := sess.brandCarries[key]; ok {
		return lane
	}
	lane := &brandLane{}
	sess.brandCarries[key] = lane
	return lane
}

// findHoldLenSet is findHoldLenWithBoundary over a whole table: the longest
// suffix of combined that is still a live prefix of ANY of the table's matches.
// One shared search keeps several rules in a single lane from each holding a
// different partial token of the same text, which is what deadlocks them.
func findHoldLenSet(combined string, table []rewriteMapping, prevIsWord bool) int {
	best := 0
	for _, m := range table {
		if k := findHoldLenWithBoundary(combined, m, prevIsWord); k > best {
			best = k
		}
	}
	return best
}

// replaceInsensitiveSetWithPrev runs every rule of a reverse table over one
// emitted fragment, in declaration order, so the longer tokens win the prefix.
func replaceInsensitiveSetWithPrev(value string, prevIsWord bool, table []rewriteMapping) (string, bool) {
	out, changed := value, false
	for _, m := range table {
		next, c := replaceMappingWithPrev(out, prevIsWord, m)
		out = next
		changed = changed || c
	}
	return out, changed
}

// applyBrandLaneSet is applyBrandLane over a whole table in ONE lane: the
// longest live partial token across all rules is held, and the fragment that
// remains is resolved against the same table. Used by the Oh My Pi protected
// lane, which owns several rules in one carry.
//
// noHold skips the hold for a lane whose choice is finalized by the event being
// applied (see streamSession.terminalChoices). The hold exists to wait for the
// byte that proves a token's right boundary; at a terminal event that byte never
// arrives and end-of-text is itself a valid boundary, so waiting would carry the
// token past the finish_reason that closed the choice. Every other call site
// passes false, so the chunk/boundary semantics of Issue #48 are unchanged.
func applyBrandLaneSet(text string, lane *brandLane, table []rewriteMapping, noHold bool) (string, bool) {
	combined := lane.carry + text
	if combined == "" {
		return "", false
	}
	holdLen := findHoldLenSet(combined, table, lane.lastIsWord)
	if noHold {
		holdLen = 0
	}
	emitPart, newCarry := combined, ""
	if holdLen > 0 {
		emitPart, newCarry = combined[:len(combined)-holdLen], combined[len(combined)-holdLen:]
	}
	out, changed := replaceInsensitiveSetWithPrev(emitPart, lane.lastIsWord, table)
	lane.setCarry(newCarry)
	if out != "" {
		lane.lastIsWord = isWordByte(out[len(out)-1])
	}
	return out, changed || out != emitPart
}

// reverseBrandLaneSuffix separates a content block's lane key from the brand
// token being matched inside it, so one block can carry one lane per mapping.
const reverseBrandLaneSuffix = "\x00"

// setCarry installs a lane's carry and keeps the arrival stamp consistent with
// it: a lane that is not holding has no arrival order, so its stamp is cleared
// and the next hold is stamped afresh. Without this a lane that held, drained
// and held again kept its FIRST stamp, and sortedCarryKeys put its new text
// ahead of a lane that genuinely began holding later.
func (l *brandLane) setCarry(carry string) {
	l.carry = carry
	if carry == "" {
		l.seq = 0
	}
}

// stampLaneArrival records the order in which a lane took its CURRENT carry,
// which is the order those bytes appeared in the stream. It has to happen at the
// first HOLD, not at lane creation: every lane of a content block is created on
// the block's first chunk, in reverse-table order, so a creation-order stamp
// would just re-encode the table order sortedCarryKeys is meant to stop
// depending on.
func stampLaneArrival(sess *streamSession, lane *brandLane) {
	if lane == nil || lane.seq != 0 || lane.carry == "" {
		return
	}
	sess.laneSeq++
	lane.seq = sess.laneSeq
}

// applySemanticBrandLane runs the resolved client's whole reverse table over
// ONE semantic carrier - a content block's prose, that block's tool arguments,
// one OpenAI choice's text - in a SINGLE lane.
//
// One lane per reverse rule gave every rule its own carry INSIDE one carrier,
// and the carries then flushed in the order the rules ran, not the order the
// bytes arrived: a chunk ending "Antigravity .gemini/GEMINI.md" held "Antigravity "
// in the brand lane and ".gemini/GEMINI.md" in the path lane, and the terminal
// flush delivered them the wrong way round. One lane feeding the whole ordered
// table keeps one carrier in one piece, and the separate carriers that really
// are separate keep their own lanes (tool args, and each choice / tool call).
func applySemanticBrandLane(sess *streamSession, laneKey, text string) (string, bool) {
	if sess == nil || text == "" {
		return text, false
	}
	// noHold: the lane belongs to a choice that is finishing in the event being
	// applied (see streamSession.terminalChoices), so nothing more will arrive
	// for it and a trailing partial token is resolved rather than held. The lane
	// is the choice's own ("openai:0") or one beneath it, so compare roots - the
	// key up to the first lane separator.
	noHold := false
	if len(sess.terminalChoices) > 0 {
		root := laneKey
		if i := strings.Index(laneKey, reverseBrandLaneSuffix); i >= 0 {
			root = laneKey[:i]
		}
		noHold = sess.terminalChoices[root]
	}
	lane := getBrandLane(sess, laneKey)
	next, changed := applyBrandLaneSet(text, lane, laneReverseTable(sess, laneKey), noHold)
	stampLaneArrival(sess, lane)
	// Issue #55 Contract B: an armed argument lane passes the resolved brand
	// text through the structured tier scanner — a complete decoded top-level
	// "model" scalar translates, a partial one is withheld until its decision
	// is known, and the withheld suffix only rejoins through the flush paths.
	if _, _, isArgs := toolArgsLaneParts(laneKey); isArgs && sess.tierLaneArmed(laneKey) {
		// noHold means this choice is finishing in this event: an incomplete
		// scalar is released raw inline, in its own argument carrier, instead
		// of being stranded past the finish_reason.
		tiered, tierChanged := applyTierLane(sess, laneKey, next, noHold)
		return tiered, tierChanged || changed
	}
	return next, changed
}
