package main

// Issue #59 (extending ADR 0003): inbound-only Oh My Pi system/developer prompt
// sanitization for Google Cloud Code / Antigravity (`agy/*`) routes.
//
// Rewrites exact `<system-conventions>` / `</system-conventions>` tags,
// case-insensitive Oh My Pi RFC 2119 normative header signatures, and Oh My Pi's
// injected Claude Code OAuth system instruction, merged with
// live-reconfigurable `omp_prompt_sanitizations` entries precomputed on the
// atomic `filterConfig` snapshot.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/monet88/antigravity-cloak/internal/spec"
)

type ompSanitizationEntry struct {
	Match       string
	Replacement string
}

type ompSanitizationRule struct {
	match         string
	lowerMatch    string
	replacement   string
	caseSensitive bool
	asciiOnly     bool
	re            *regexp.Regexp
}

// ompSanitizationPreset builds a built-in preset, deriving lowerMatch and the
// ASCII fast-scan flag from the match text instead of restating them by hand.
// lowerMatch doubles as the case-insensitive override key in
// buildOMPSanitizationRules even for case-sensitive presets.
func ompSanitizationPreset(match, replacement string, caseSensitive bool) ompSanitizationRule {
	lower := strings.ToLower(match)
	return ompSanitizationRule{
		match:         match,
		lowerMatch:    lower,
		replacement:   replacement,
		caseSensitive: caseSensitive,
		asciiOnly:     isASCIIString(match) && isASCIIString(lower) && len(lower) == len(match),
	}
}

var defaultOMPSanitizationPresets []ompSanitizationRule

func setDefaultOMPSanitizationPresets(presets []spec.PromptSanitizationPreset) {
	rules := make([]ompSanitizationRule, 0, len(presets))
	for _, p := range presets {
		rules = append(rules, ompSanitizationPreset(p.Match, p.Replacement, p.CaseSensitive))
	}
	defaultOMPSanitizationPresets = rules
}

func buildOMPSanitizationRules(entries []ompSanitizationEntry) []ompSanitizationRule {
	byLower := make(map[string]ompSanitizationRule, len(defaultOMPSanitizationPresets)+len(entries))
	for _, preset := range defaultOMPSanitizationPresets {
		byLower[preset.lowerMatch] = preset
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Match) == "" {
			continue
		}
		lower := strings.ToLower(entry.Match)
		ascii := isASCIIString(entry.Match) && isASCIIString(lower) && len(lower) == len(entry.Match)
		var re *regexp.Regexp
		if !ascii {
			compiled, err := regexp.Compile("(?i)^" + regexp.QuoteMeta(entry.Match))
			if err != nil {
				continue
			}
			re = compiled
		}
		byLower[lower] = ompSanitizationRule{
			match:         entry.Match,
			lowerMatch:    lower,
			replacement:   entry.Replacement,
			caseSensitive: false,
			asciiOnly:     ascii,
			re:            re,
		}
	}
	rules := make([]ompSanitizationRule, 0, len(byLower))
	for _, rule := range byLower {
		rules = append(rules, rule)
	}
	slices.SortFunc(rules, func(a, b ompSanitizationRule) int {
		if len(a.match) != len(b.match) {
			return len(b.match) - len(a.match)
		}
		return strings.Compare(a.lowerMatch, b.lowerMatch)
	})
	return rules
}

func parseOMPPromptSanitizations(value any) ([]ompSanitizationEntry, error) {
	pairs, err := parseConfigKeyValues("omp_prompt_sanitizations", value, true, true)
	if err != nil {
		return nil, err
	}
	if pairs == nil {
		return nil, nil
	}
	entries := make([]ompSanitizationEntry, 0, len(pairs))
	for _, pair := range pairs {
		if strings.TrimSpace(pair.Key) == "" {
			return nil, fmt.Errorf("omp_prompt_sanitizations match key must not be empty")
		}
		if !utf8.ValidString(pair.Key) || !utf8.ValidString(pair.Value) {
			return nil, fmt.Errorf("omp_prompt_sanitizations entries must be valid UTF-8")
		}
		entries = append(entries, ompSanitizationEntry{Match: pair.Key, Replacement: pair.Value})
	}
	return entries, nil
}

// sanitizeOMPPrompt applies the precomputed OMP prompt sanitization rules to
// top-level `system` and `system`/`developer` message text blocks.
func sanitizeOMPPrompt(rootMap map[string]any) bool {
	rules := activeFilterConfig().ompSanitizationRules
	fn := func(s string) (string, bool) { return sanitizeOMPPromptText(s, rules) }
	changed := false
	if system, ok := rootMap["system"]; ok {
		if next, one := rewritePromptTextContent(system, true, fn); one {
			rootMap["system"] = next
			changed = true
		}
	}
	if rewriteSystemDeveloperMessages(rootMap, true, fn) {
		changed = true
	}
	return changed
}

func rewritePromptTextContent(value any, allowInputText bool, fn func(string) (string, bool)) (any, bool) {
	switch content := value.(type) {
	case string:
		if next, changed := fn(content); changed {
			return next, true
		}
		return value, false
	case []any:
		changed := false
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			btype, _ := block["type"].(string)
			if btype != "text" && (!allowInputText || btype != "input_text") {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if next, one := fn(text); one {
					block["text"] = next
					changed = true
				}
			}
		}
		return value, changed
	}
	return value, false
}

func rewriteSystemDeveloperMessages(rootMap map[string]any, allowInputText bool, fn func(string) (string, bool)) bool {
	messages, ok := rootMap["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok || (msg["role"] != "system" && msg["role"] != "developer") {
			continue
		}
		if content, ok := msg["content"]; ok {
			if next, one := rewritePromptTextContent(content, allowInputText, fn); one {
				msg["content"] = next
				changed = true
			}
		}
	}
	return changed
}

// sanitizeOMPPromptText applies the rules in one left-to-right pass, longest
// match first at each position. Replacement text is emitted verbatim and is
// never rescanned, so a shorter rule can neither cascade into its own output
// nor rewrite the output of a longer override.
func sanitizeOMPPromptText(text string, rules []ompSanitizationRule) (string, bool) {
	if text == "" || len(rules) == 0 {
		return text, false
	}
	var b strings.Builder
	built := false
	for i := 0; i < len(text); {
		rule, n := ompMatchingRuleAt(text[i:], rules)
		if rule != nil {
			if !built {
				b.Grow(len(text))
				b.WriteString(text[:i])
				built = true
			}
			b.WriteString(rule.replacement)
			i += n
			continue
		}
		// Step one full rune, never mid-rune: a slice starting inside a
		// multi-byte character decodes its continuation bytes as U+FFFD, so a
		// byte-wise step would let a U+FFFD-prefixed rule false-match valid
		// UTF-8 and split characters into invalid output.
		_, width := utf8.DecodeRuneInString(text[i:])
		if width <= 0 {
			width = 1
		}
		if built {
			b.WriteString(text[i : i+width])
		}
		i += width
	}
	if !built {
		return text, false
	}
	return b.String(), true
}

// ompMatchingRuleAt returns the longest rule matching at s[0]. Ties keep the
// buildOMPSanitizationRules order, so the choice is deterministic.
func ompMatchingRuleAt(s string, rules []ompSanitizationRule) (*ompSanitizationRule, int) {
	var best *ompSanitizationRule
	bestLen := 0
	for i := range rules {
		if n := ompRuleMatchLength(s, &rules[i]); n > bestLen {
			best, bestLen = &rules[i], n
		}
	}
	return best, bestLen
}

func ompRuleMatchLength(s string, rule *ompSanitizationRule) int {
	switch {
	case rule.caseSensitive:
		if strings.HasPrefix(s, rule.match) {
			return len(rule.match)
		}
	case rule.asciiOnly:
		if hasPrefixASCIIFold(s, rule.lowerMatch) {
			return len(rule.lowerMatch)
		}
	default:
		if rule.re != nil {
			if loc := rule.re.FindStringIndex(s); loc != nil && loc[0] == 0 {
				return loc[1]
			}
		}
	}
	return 0
}

func hasPrefixASCIIFold(s, lowerNeedle string) bool {
	if len(s) < len(lowerNeedle) {
		return false
	}
	for i := range len(lowerNeedle) {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lowerNeedle[i] {
			return false
		}
	}
	return true
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
