package main

import (
	"strings"
	"testing"
)

// The two halves of a client context group are built by the same helper call
// with both pairs swapped, but that is a convention a later edit can break in
// one place and not the other. A swapped pair still produces a table that
// compiles, still passes every behaviour test, and is simply wrong: the reverse
// looks for a spelling the forward pass never emitted, so the client gets a
// .gemini path back. This asserts the sets are exact inverses.
func TestContextGroupsAreExactInverses(t *testing.T) {
	for _, group := range []struct {
		client        string
		forward, back []rewriteMapping
	}{
		{"claude_code", claudeContextMappings, claudeReverseContextMappings},
		{"codex", codexContextMappings, codexReverseContextMappings},
	} {
		// Keyed by what the forward pass emits, which is what the reverse pass
		// has to match, and valued with what the client must get back.
		emitted := make(map[string]string, len(group.forward))
		for _, m := range group.forward {
			emitted[strings.ToLower(m.Replacement)] = m.Match
		}
		if len(emitted) != len(group.forward) {
			t.Errorf("%s: forward group emits two identical spellings, so the reverse cannot tell them apart", group.client)
		}
		matched := make(map[string]string, len(group.back))
		for _, m := range group.back {
			matched[strings.ToLower(m.Match)] = m.Replacement
		}
		if len(emitted) != len(matched) {
			t.Errorf("%s: group sizes differ, forward %d reverse %d", group.client, len(emitted), len(matched))
		}
		for emittedSpelling, original := range emitted {
			restored, ok := matched[emittedSpelling]
			if !ok {
				t.Errorf("%s: forward emits %q but no reverse rule matches it", group.client, emittedSpelling)
				continue
			}
			if restored != original {
				t.Errorf("%s: reverse of %q is not exact\n  forward: %q -> %q\n  reverse: %q -> %q",
					group.client, emittedSpelling, original, emittedSpelling, emittedSpelling, restored)
			}
		}
	}
}

// Each rule inverted is itself: applying the client's whole reverse table to
// what the forward rule produced has to give back the original spelling. This is
// the property the round trip depends on, checked on the bytes through the table
// the response path actually uses rather than on the pairing, so deleting or
// mis-scoping a reverse table fails here.
func TestContextGroupRulesInvertOnBytes(t *testing.T) {
	for _, group := range []struct {
		client string
		rules  []rewriteMapping
	}{
		{"claude_code", claudeContextMappings},
		{"codex", codexContextMappings},
	} {
		for _, m := range group.rules {
			back := applyReverseTable(m.Replacement, group.client)
			if back != m.Match {
				t.Errorf("%s: %q -> %q restored to %q, want %q", group.client, m.Match, m.Replacement, back, m.Match)
			}
		}
	}
}
