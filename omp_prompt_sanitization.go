package main

// Issue #59 (extending ADR 0003): inbound-only Oh My Pi system/developer prompt
// sanitization for Google Cloud Code / Antigravity (`agy/*`) routes.
//
// Rewrites exact `<system-conventions>` / `</system-conventions>` tags and
// case-insensitive Oh My Pi RFC 2119 normative header signatures, merged with
// live-reconfigurable `omp_prompt_sanitizations` entries precomputed on the
// atomic `filterConfig` snapshot.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
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

const ompRFC2119KeywordsReplacement = "Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."

var defaultOMPSanitizationPresets = []ompSanitizationRule{
	{
		match:         "<system-conventions>",
		lowerMatch:    "<system-conventions>",
		replacement:   "<conventions>",
		caseSensitive: true,
		asciiOnly:     true,
	},
	{
		match:         "</system-conventions>",
		lowerMatch:    "</system-conventions>",
		replacement:   "</conventions>",
		caseSensitive: true,
		asciiOnly:     true,
	},
	{
		match:         "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.",
		lowerMatch:    "rfc 2119 keywords: must, required, should, recommended, may, optional.",
		replacement:   ompRFC2119KeywordsReplacement,
		caseSensitive: false,
		asciiOnly:     true,
	},
	{
		match:         "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.",
		lowerMatch:    "rfc 2119: must, required, should, recommended, may, optional.",
		replacement:   ompRFC2119KeywordsReplacement,
		caseSensitive: false,
		asciiOnly:     true,
	},
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
			compiled, err := regexp.Compile("(?i)" + regexp.QuoteMeta(entry.Match))
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
	var entries []ompSanitizationEntry
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for k := range typed {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		entries = make([]ompSanitizationEntry, 0, len(typed))
		for _, match := range keys {
			if strings.TrimSpace(match) == "" {
				return nil, fmt.Errorf("omp_prompt_sanitizations match key must not be empty")
			}
			text, ok := typed[match].(string)
			if !ok {
				return nil, fmt.Errorf("omp_prompt_sanitizations values must be strings")
			}
			entries = append(entries, ompSanitizationEntry{Match: match, Replacement: text})
		}
	case string:
		var err error
		entries, err = parseOMPSanitizationString(typed)
		if err != nil {
			return nil, err
		}
	case []any:
		entries = make([]ompSanitizationEntry, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("omp_prompt_sanitizations entries must be strings")
			}
			parsed, err := parseOMPSanitizationString(text)
			if err != nil {
				return nil, err
			}
			if len(parsed) == 0 {
				return nil, fmt.Errorf("omp_prompt_sanitizations entries must use match: replacement")
			}
			entries = append(entries, parsed...)
		}
	default:
		return nil, fmt.Errorf("omp_prompt_sanitizations must be an object, array, or string")
	}
	for _, entry := range entries {
		if !utf8.ValidString(entry.Match) || !utf8.ValidString(entry.Replacement) {
			return nil, fmt.Errorf("omp_prompt_sanitizations entries must be valid UTF-8")
		}
	}
	return entries, nil
}

func parseOMPSanitizationString(value string) ([]ompSanitizationEntry, error) {
	rawEntries := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	entries := make([]ompSanitizationEntry, 0, len(rawEntries))
	for _, entry := range rawEntries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		match, replacement, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("omp_prompt_sanitizations entries must use match: replacement")
		}
		trimmedMatch := strings.TrimSpace(match)
		if trimmedMatch == "" {
			return nil, fmt.Errorf("omp_prompt_sanitizations match key must not be empty")
		}
		entries = append(entries, ompSanitizationEntry{
			Match:       trimmedMatch,
			Replacement: strings.TrimSpace(replacement),
		})
	}
	return entries, nil
}

// sanitizeOMPPrompt applies the precomputed OMP prompt sanitization rules to
// top-level `system` and `system`/`developer` message text blocks.
func sanitizeOMPPrompt(rootMap map[string]any) bool {
	rules := activeFilterConfig().ompSanitizationRules
	changed := false
	if system, ok := rootMap["system"]; ok {
		if next, one := sanitizeOMPPromptContent(system, rules); one {
			rootMap["system"] = next
			changed = true
		}
	}
	if messages, ok := rootMap["messages"].([]any); ok {
		for _, raw := range messages {
			msg, ok := raw.(map[string]any)
			if !ok || (msg["role"] != "system" && msg["role"] != "developer") {
				continue
			}
			if content, ok := msg["content"]; ok {
				if next, one := sanitizeOMPPromptContent(content, rules); one {
					msg["content"] = next
					changed = true
				}
			}
		}
	}
	return changed
}

func sanitizeOMPPromptContent(value any, rules []ompSanitizationRule) (any, bool) {
	switch content := value.(type) {
	case string:
		if next, changed := sanitizeOMPPromptText(content, rules); changed {
			return next, true
		}
		return value, false
	case []any:
		changed := false
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok || (block["type"] != "text" && block["type"] != "input_text") {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if next, one := sanitizeOMPPromptText(text, rules); one {
					block["text"] = next
					changed = true
				}
			}
		}
		return value, changed
	}
	return value, false
}

func sanitizeOMPPromptText(text string, rules []ompSanitizationRule) (string, bool) {
	if text == "" || len(rules) == 0 {
		return text, false
	}
	changed := false
	for i := range rules {
		r := &rules[i]
		if r.caseSensitive {
			if !strings.Contains(text, r.match) {
				continue
			}
			text = strings.ReplaceAll(text, r.match, r.replacement)
			changed = true
			continue
		}
		if r.asciiOnly {
			if next, ok := replaceAllASCIIFold(text, r.lowerMatch, r.replacement); ok {
				text = next
				changed = true
			}
			continue
		}
		if r.re != nil && r.re.MatchString(text) {
			text = r.re.ReplaceAllLiteralString(text, r.replacement)
			changed = true
		}
	}
	return text, changed
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func indexASCIIFold(s, lowerNeedle string) int {
	n := len(lowerNeedle)
	if n == 0 || len(s) < n {
		return -1
	}
	first := lowerNeedle[0]
	firstUpper := first
	if first >= 'a' && first <= 'z' {
		firstUpper = first - ('a' - 'A')
	}
	limit := len(s) - n
	for i := 0; i <= limit; i++ {
		c := s[i]
		if c != first && c != firstUpper {
			continue
		}
		matched := true
		for j := 1; j < n; j++ {
			sc := s[i+j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 'a' - 'A'
			}
			if sc != lowerNeedle[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func replaceAllASCIIFold(s, lowerNeedle, replacement string) (string, bool) {
	idx := indexASCIIFold(s, lowerNeedle)
	if idx < 0 {
		return s, false
	}
	n := len(lowerNeedle)
	var b strings.Builder
	b.Grow(len(s) - n + len(replacement))
	start := 0
	for {
		b.WriteString(s[start:idx])
		b.WriteString(replacement)
		start = idx + n
		rel := indexASCIIFold(s[start:], lowerNeedle)
		if rel < 0 {
			break
		}
		idx = start + rel
	}
	b.WriteString(s[start:])
	return b.String(), true
}
