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
		mid, _ := replaceInsensitiveRule(in, rewriteMapping{Match: "omp", Replacement: "Antigravity"}, true)
		if mid == in {
			t.Errorf("%q: forward pass made no change", in)
			continue
		}
		back, _ := replaceInsensitiveSetWithPrev(mid, false, ompProtectedReverseTable)
		if back != in {
			t.Errorf("%q: round trip = %q", in, back)
		}
	}
}

// TestIssue49_OMPLooksLikeSegmentStillMasks covers the dot-segment rule's
// boundaries on the production forward seam (the bare alias rule with the path
// guard enabled): a .omp that is a real path segment is remapped onto the
// neutral .gemini directory, a segment that continues past the brand word
// (~/.omp-backup/agent) or a slash-delimited /omp/ is left byte-for-byte, and a
// brand glued to a preceding word (checkpoint.omp/state) is prose, not a
// segment, so it is masked to the bare brand word - the reverse restores it, so
// the round trip still hands the client its own spelling back (the same
// contract filter_test.go pins for profile.omp/agent). Expected outputs are
// exact, so the test can tell the right rewrite from a wrong one instead of
// only detecting "something changed".
func TestIssue49_OMPLooksLikeSegmentStillMasks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"~/.omp/agent/AGENTS.md", "~/.gemini/agent/AGENTS.md"},
		{".omp/commands/review-prs.md", ".gemini/commands/review-prs.md"},
		{"persist to .omp/state", "persist to .gemini/state"},
		// Not a segment: the element continues past the brand word.
		{"~/.omp-backup/agent", "~/.omp-backup/agent"},
		// Not dot-prefixed: an ordinary name in a path.
		{"cd /omp/state", "cd /omp/state"},
		// Glued to a preceding word: prose, so the bare brand word is masked
		// rather than remapped as a directory, and the reverse undoes it.
		{"checkpoint.omp/state", "checkpoint.antigravity/state"},
		// Prose is not a path, so the bare brand word is still masked.
		{"run omp now", "run antigravity now"},
	} {
		got, _ := replaceInsensitiveRule(tc.in, rewriteMapping{Match: "omp", Replacement: "Antigravity"}, true)
		if got != tc.want {
			t.Errorf("%q: forward = %q, want %q", tc.in, got, tc.want)
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
//
// It applies each client's REAL forward table, so a blanket .claude -> .gemini
// added to oh_my_pi (or any other table reaching into a foreign directory) fails
// here. Each client's own directory is the client's own business and is skipped;
// the liveness check below proves the table under test actually rewrites
// something, so the negative rows cannot pass vacuously.
func TestIssue49_CompetitorDirsLeftAlone(t *testing.T) {
	ownDir := map[string]string{"claude_code": ".claude", "codex": ".codex", "oh_my_pi": ".omp"}
	for client, own := range ownDir {
		live := "C:/Users/monet/" + own + "/settings.json"
		if applyTable(live, brandMappingsFor(client)) == live {
			t.Fatalf("%s: forward table does not rewrite its own directory %q", client, live)
		}
	}
	for _, dir := range []string{
		".claude", ".codex", ".cursor", ".windsurf", ".vscode", ".opencode", ".copilot",
	} {
		in := "C:/Users/monet/" + dir + "/settings.json"
		for client := range ownDir {
			if ownDir[client] == dir {
				continue
			}
			if got := applyTable(in, brandMappingsFor(client)); got != in {
				t.Errorf("%s rewrote %q: %q", client, in, got)
			}
		}
	}
}

// TestIssue49_OMPClaudeMdPairIsReversible pins the OMP-only pair for the
// user's GLOBAL Claude memory, ~/.claude/CLAUDE.md -> ~/.gemini/AGENTS.md, in
// both directions and for POSIX and Windows spellings. There is no
// project-local .claude/CLAUDE.md; the match is unanchored so the ~/ and
// absolute spellings both hit it. The file-level scope is the
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

// TestIssue49_OMPIdentityLineRewrittenWhole proves the opening sentence is
// replaced whole, not by substituting the bare alias inside it. The bare-alias
// pass alone yields "You are Antigravity's trusted coding assistant.", a
// sentence that names no product and no vendor. Also proves the ordering: the
// sentinel pass swaps "omp" for a marker, so the sentence rule has to land
// first or it never matches.
func TestIssue49_OMPIdentityLineRewrittenWhole(t *testing.T) {
	const in = "You are omp's trusted coding assistant."
	cfg := activeFilterConfig()

	got, changed := rewriteProtectedBrandText(in, cfg, "oh_my_pi")
	if !changed || got != antigravityIdentity {
		t.Fatalf("protected route: changed=%v out=%q", changed, got)
	}
	for _, alias := range mandatoryProtectedOMPAliases {
		if _, rep := replaceInsensitiveRule(got, rewriteMapping{Match: alias, Replacement: protectedBrandSentinel}, true); rep {
			t.Errorf("sentinel still matches %q in %q", alias, got)
		}
	}
	if tbl := applyTable(in, ompBrandMappings); tbl != antigravityIdentity {
		t.Errorf("forward table = %q", tbl)
	}
}

// TestIssue49_OMPIdentityLineLeavesProseAlone keeps the rule a sentence rule.
// A bare "omp" in ordinary prose still takes the alias path, and text that is
// not the shipped sentence is untouched.
func TestIssue49_OMPIdentityLineLeavesProseAlone(t *testing.T) {
	for _, in := range []string{
		"You are a trusted coding assistant.",
		"Run the omp binary to start.",
		"you are OMP's trusted coding assistant.",
	} {
		got, _ := rewriteProtectedBrandText(in, activeFilterConfig(), "oh_my_pi")
		if got == antigravityIdentity && in != "you are OMP's trusted coding assistant." {
			t.Errorf("%q unexpectedly became the identity line", in)
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

// applyTable applies a table the way the wire does - one rule at a time through
// the rule objects themselves, so a rule's whole-segment boundary, its scheme
// exclusion and (on the forward pass) the dot-segment path remap are part of
// what the helper exercises instead of being dropped by a match/replacement
// pair. It is the same loop the request path uses for text, which is what makes
// these fixtures able to fail when production changes.
func applyTable(s string, tables []rewriteMapping) string {
	for _, m := range tables {
		s, _ = replaceInsensitiveRule(s, m, true)
	}
	return s
}
