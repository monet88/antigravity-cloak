package main

import "testing"

// TestIssue49_OMPOwnedPathsRoundTrip pins the four path spellings the Oh My Pi
// harness bakes into its own system prompt. All four are absolute or tilde
// forms under .omp, injected on every request, so both directions must hold.
// Measured against oh-my-pi d1932a6ff8: the paths come from
// packages/coding-agent/src/prompts/system/autolearn-guidance.md:4,7,
// prompts/tools/learn.md:6, prompts/tools/manage-skill.md:1, and
// prompts/tools/ida.md:5.
func TestIssue49_OMPOwnedPathsRoundTrip(t *testing.T) {
	cases := []string{
		"~/.omp/agent/managed-skills",
		"~/.omp/agent/skills",
		".omp/skills",
		"~/.omp/agent/idbs/a1b2c3d4e5f60718-mytool/",
		"C:\\Users\\monet\\.omp\\agent\\AGENTS.md",
		"/home/m/.omp/agent/AGENTS.md",
	}
	for _, in := range cases {
		mid, _ := replaceInsensitiveOpt(in, "omp", "Antigravity", true)
		if mid == in {
			t.Errorf("%q: forward pass made no change", in)
			continue
		}
		back := mid
		for _, m := range ompProtectedReverseTable {
			back, _ = replaceInsensitiveOpt(back, m.Match, m.Replacement, true)
			back, _ = replaceInsensitiveOpt(back, m.Match, m.Replacement, false)
		}
		if back != in {
			t.Errorf("%q: round trip = %q", in, back)
		}
	}
}

// TestIssue49_OMPLooksLikeSegmentStillMasks covers the dot-segment rule's
// left boundary. A .omp that is a real path segment rewrites; one that is
// merely a lookalike inside a larger word does not.
func TestIssue49_OMPLooksLikeSegmentStillMasks(t *testing.T) {
	for _, in := range []string{
		"~/.omp/agent/AGENTS.md",
		".omp/commands/review-prs.md",
		"persist to .omp/state",
	} {
		if got, _ := replaceInsensitiveWithPrev(in, false, "omp", "Antigravity"); got == in {
			t.Errorf("%q: expected the dot-segment to be masked, got it verbatim", in)
		}
	}
}

// TestIssue49_CompetitorDirsLeftAlone records a deliberate decision, not a
// capability: .claude, .codex, .cursor, .windsurf, .vscode, .opencode and
// .copilot stay verbatim on every client. The Oh My Pi harness discovers
// context files under all of them
// (packages/coding-agent/src/discovery/{claude,codex,cursor,windsurf,
// opencode,github}.ts) and injects the absolute paths, so these strings do
// reach the model. They are accepted because the operator does not use those
// tools in their own repositories. If that changes, this test is the tripwire.
func TestIssue49_CompetitorDirsLeftAlone(t *testing.T) {
	for _, dir := range []string{
		".claude", ".codex", ".cursor", ".windsurf", ".vscode", ".opencode", ".copilot",
	} {
		in := "C:/Users/monet/" + dir + "/settings.json"
		for _, client := range []string{"claude_code", "codex", "oh_my_pi"} {
			for _, m := range brandMappingsFor(client) {
				if m.Match != "omp" && m.Match != "Oh My Pi" && m.Match != "oh-my-pi" {
					continue
				}
				if got, _ := replaceInsensitiveWithPrev(in, false, m.Match, m.Replacement); got != in {
					t.Errorf("%s rewrote %q: %q", client, in, got)
				}
			}
		}
	}
}

// TestIssue49_OMPClaudeMdPairIsReversible pins the OMP-only pair
// <user home>/.claude/CLAUDE.md -> <user home>/.gemini/AGENTS.md in both
// directions, for POSIX and Windows spellings. The file-level scope is the
// whole point: a blanket .claude -> .gemini would land on the same target as
// the existing .omp -> .gemini and leave the reverse unable to tell them
// apart. Reversibility survives because "/AGENTS.md" directly after
// ".gemini/" is a shape no .omp path produces - OMP's own is
// ".omp/agent/AGENTS.md".
func TestIssue49_OMPClaudeMdPairIsReversible(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"~/.claude/CLAUDE.md", "~/.gemini/AGENTS.md"},
		{"C:/Users/monet/.claude/CLAUDE.md", "C:/Users/monet/.gemini/AGENTS.md"},
		{"C:\\Users\\monet\\.claude\\CLAUDE.md", "C:\\Users\\monet\\.gemini\\AGENTS.md"},
	} {
		got := applyTable(tc.in, ompBrandMappings)
		if got != tc.want {
			t.Errorf("forward %q = %q, want %q", tc.in, got, tc.want)
			continue
		}
		if back := applyTable(got, ompProtectedReverseTable); back != tc.in {
			t.Errorf("reverse %q = %q, want %q", got, back, tc.in)
		}
	}
}

// TestIssue49_OMPRootAGENTSmdUntouched is the negative half: the neutral
// root file must survive both directions. A stray blanket rule here would
// rewrite it, and since AGENTS.md is what the reverse uses to recognise the
// .claude pair, that would also make the pair above ambiguous.
func TestIssue49_OMPRootAGENTSmdUntouched(t *testing.T) {
	for _, in := range []string{
		"./AGENTS.md",
		"AGENTS.md",
		"F:/repo/AGENTS.md",
		"~/.claude/settings.json",
		"C:/Users/monet/.claude/rules/style.md",
	} {
		got := applyTable(in, ompBrandMappings)
		if back := applyTable(got, ompProtectedReverseTable); got != in || back != in {
			t.Errorf("%q: forward %q, reverse %q", in, got, back)
		}
	}
}

// TestIssue49_ClaudeMdRewrittenOnlyByClaudeCode is the case-safety proof for
// the CLAUDE.md rule. claude_code also carries the bare "Claude" rule, so
// without this rule CLAUDE.md becomes Antigravity.md and the reverse can only
// restore it to Claude.md, corrupting the name's case. codex and oh_my_pi have
// no bare-Claude rule, so for them the file name must survive untouched.
func TestIssue49_ClaudeMdRewrittenOnlyByClaudeCode(t *testing.T) {
	const in = "Update CLAUDE.md now"

	fwd := applyTable(in, brandMappingsFor("claude_code"))
	if fwd != "Update AGENTS.md now" {
		t.Errorf("claude_code forward = %q, want the file renamed", fwd)
	}
	// The rename is one-way by design: AGENTS.md is the neutral convention, so
	// the reverse must leave it alone rather than invent a CLAUDE.md the
	// client never asked for.
	if got := applyTable(fwd, reverseBrandMappingsFor("claude_code")); got != fwd {
		t.Errorf("claude_code reverse moved the neutral name: %q", got)
	}
	for _, client := range []string{"codex", "oh_my_pi"} {
		if got := applyTable(in, brandMappingsFor(client)); got != in {
			t.Errorf("%s rewrote the file name: %q", client, got)
		}
	}
}

func applyTable(s string, tables []rewriteMapping) string {
	for _, m := range tables {
		s, _ = replaceInsensitiveWithPrev(s, false, m.Match, m.Replacement)
	}
	return s
}
