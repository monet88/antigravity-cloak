package spec

import "testing"

func TestPathRulesOrderAndExactInverses(t *testing.T) {
	fwd := PathRules(".claude", ".gemini", "CLAUDE.md", "GEMINI.md")
	back := PathRules(".gemini", ".claude", "GEMINI.md", "CLAUDE.md")

	if len(fwd) != 5 {
		t.Fatalf("len(fwd) = %d, want 5", len(fwd))
	}
	if len(back) != 5 {
		t.Fatalf("len(back) = %d, want 5", len(back))
	}

	wantMatches := []string{
		".claude/CLAUDE.md",
		".claude/",
		`.claude\CLAUDE.md`,
		`.claude\\CLAUDE.md`,
		`.claude\`,
	}
	for i, m := range fwd {
		if m.Match != wantMatches[i] {
			t.Errorf("fwd[%d].Match = %q, want %q", i, m.Match, wantMatches[i])
		}
		if !m.WholeSegment || !m.ArgumentSafe {
			t.Errorf("fwd[%d] WholeSegment=%v ArgumentSafe=%v, want both true", i, m.WholeSegment, m.ArgumentSafe)
		}
		if back[i].Match != m.Replacement || back[i].Replacement != m.Match {
			t.Errorf("back[%d] = (%q -> %q), want inverse of (%q -> %q)", i, back[i].Match, back[i].Replacement, m.Match, m.Replacement)
		}
	}
}
