package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Ticket #49: a home directory, a config path and an official domain are
// operational identifiers, not brand prose. Whatever the forward pass turned
// them into has to come back in the SAME client's spelling, or the client is
// handed a path or a URL that does not exist on its machine.

func TestIssue49_ForwardPathSegmentPerClient(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, tc := range []struct {
		client string
		in     string
		want   string
	}{
		// Oh My Pi's whole home directory is remapped, inverted by
		// ompProtectedReverseTable.
		{"oh_my_pi", `{"system":"see /home/u/.omp/agent/AGENTS.md"}`, "/home/u/.gemini/agent/AGENTS.md"},
		{"oh_my_pi", `{"system":"see C:\\Users\\u\\.omp\\agent"}`, `C:\\Users\\u\\.gemini\\agent`},
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(tc.in), "openai", tc.client)
		if !changed || !strings.Contains(string(got), tc.want) {
			t.Errorf("%s: got %s, want it to contain %s", tc.client, got, tc.want)
		}
	}
}

// Only the home INSTRUCTION FILE is remapped for Claude Code and Codex. A
// plain directory is an operational identifier and stays byte-for-byte, in
// both directions. Live acceptance is why: remapping every ".claude" path
// rewrote the user's own text, and Claude Code was editing a README whose old
// and new strings differed only in that directory, so the forward remap
// collapsed them into identical bytes and the edit silently became a no-op.
func TestIssue49_HomeDirectoryIsRemappedEverywhere(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Live acceptance measured 114 absolute client-home paths per request going
	// to the model uncloaked, because the home directory is matched as a whole
	// segment at any position rather than only in a ~/ or ./ spelling. A config
	// file under it is remapped for the same reason: the brand reaches the model
	// either way, and the reverse mirrors this table exactly.
	for _, tc := range []struct {
		client, in, want string
	}{
		{"claude_code", `{"system":"see C:\\Users\\u\\.claude\\settings.json"}`, `C:\\Users\\u\\.gemini\\settings.json`},
		{"claude_code", `{"system":"see C:\\Users\\u\\.claude\\transcripts"}`, `C:\\Users\\u\\.gemini\\transcripts`},
		{"claude_code", `{"system":"see /home/u/.claude/projects/-repo/memory/a.md"}`, `/home/u/.gemini/projects/-repo/memory/a.md`},
		{"codex", `{"system":"see /home/u/.codex/config.toml"}`, "/home/u/.gemini/config.toml"},
		{"codex", `{"system":"see C:\\Users\\u\\.codex\\config.toml"}`, `C:\\Users\\u\\.gemini\\config.toml`},
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(tc.in), "openai", tc.client)
		if !changed {
			t.Errorf("%s: a client home directory was left alone.\n in: %s", tc.client, tc.in)
			continue
		}
		if !strings.Contains(string(got), tc.want) {
			t.Errorf("%s: home directory not remapped\n got: %s\n want substring: %s", tc.client, got, tc.want)
		}
	}
	// A path element that merely starts with the same letters is not the home
	// directory, so it stays byte-for-byte alone. The dot is not a word byte,
	// so only the dot-segment rule keeps "x.codex-backup" out of the remap.
	//
	// A brand glued onto a leading file name ("foo.codex/config.toml") is a
	// different case: it is not a path element at all by this rule's reading,
	// because the byte before its dot is a word byte, so the BARE brand rule
	// masks it as prose and the reverse hands the client its own spelling back.
	// That is the bare-rule contract (filter_test.go). The whole-segment
	// boundary itself is pinned in TestPathRulesAreWholeSegment.
	for _, tc := range []struct{ client, in string }{
		{"claude_code", `{"system":"see /home/u/.claude-backup/x"}`},
		{"codex", `{"system":"see /home/u/.codex-backup/x"}`},
	} {
		if _, changed, _ := rewriteRequestBodyWithClient([]byte(tc.in), "openai", tc.client); changed {
			t.Errorf("%s: a lookalike directory was rewritten: %s", tc.client, tc.in)
		}
	}
}

// TestPathRulesAreWholeSegment is the boundary contract for the path groups
// themselves. The directory they match begins with a dot, which is not a word
// byte, so nothing in the generic word-boundary test stops ".claude/" matching
// the tail of "foo.claude/". Both directions and both spellings are asserted,
// because a rule that over-matches on the way up hands the model a path the
// client does not have, and one that over-matches on the way back hands the
// client a path that does not exist upstream.
func TestPathRulesAreWholeSegment(t *testing.T) {
	const (
		unchanged = ""
	)
	for _, tc := range []struct {
		name  string
		rules []rewriteMapping
		in    string
		want  string
	}{
		// Valid forms still match.
		{"claude forward, home spelling", claudeContextMappings, "read ~/.claude/CLAUDE.md", "read ~/.gemini/GEMINI.md"},
		{"claude forward, project spelling", claudeContextMappings, "read ./repo/.claude/CLAUDE.md", "read ./repo/.gemini/GEMINI.md"},
		{"claude forward, escaped windows", claudeContextMappings, `read C:\\u\\.claude\\CLAUDE.md`, `read C:\\u\\.gemini\\GEMINI.md`},
		{"claude reverse, escaped windows", claudeReverseContextMappings, `read C:\\u\\.gemini\\GEMINI.md`, `read C:\\u\\.claude\\CLAUDE.md`},
		{"codex forward, escaped windows", codexContextMappings, `read C:\\u\\.codex\\AGENTS.md`, `read C:\\u\\.gemini\\AGENTS.md`},
		{"codex reverse, escaped windows", codexReverseContextMappings, `read C:\\u\\.gemini\\AGENTS.md`, `read C:\\u\\.codex\\AGENTS.md`},
		// A larger element that merely ENDS in the directory name is not it.
		{"claude forward, larger element", claudeContextMappings, "see foo.claude/CLAUDE.md", unchanged},
		{"claude forward, larger element escaped", claudeContextMappings, `see foo.claude\\CLAUDE.md`, unchanged},
		{"claude reverse, larger element", claudeReverseContextMappings, "see foo.gemini/GEMINI.md", unchanged},
		{"claude reverse, larger element escaped", claudeReverseContextMappings, `see foo.gemini\\GEMINI.md`, unchanged},
		{"codex forward, larger element", codexContextMappings, "see foo.codex/AGENTS.md", unchanged},
		{"codex reverse, larger element", codexReverseContextMappings, "see foo.gemini/AGENTS.md", unchanged},
		{"codex reverse, larger element escaped", codexReverseContextMappings, `see foo.gemini\\AGENTS.md`, unchanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == unchanged {
				want = tc.in
			}
			got := tc.in
			for _, m := range tc.rules {
				got, _ = replaceMappingWithPrev(got, false, m)
			}
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestIssue49_ReversePathSegmentPerClient(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, tc := range []struct {
		client string
		in     string
		want   string
	}{
		// Only the paths the forward pass actually injected come back, and only
		// in a home spelling. A ".gemini" the user typed is not something the
		// forward pass produced, and an absolute path is out of scope.
		{"claude_code", "~/.gemini/projects/p/memory.md", "~/.claude/projects/p/memory.md"},
		{"claude_code", "~/.gemini/rules/style.md", "~/.claude/rules/style.md"},
		{"claude_code", "~/.gemini/GEMINI.md", "~/.claude/CLAUDE.md"},
		{"claude_code", "/home/u/.gemini/GEMINI.md", "/home/u/.claude/CLAUDE.md"},
		// Escaped (JSON) spelling: this is the form a path takes inside a tool
		// call's arguments, where each backslash is written twice. The file rule
		// for that spelling is what keeps the file name with the directory, so
		// the client gets its own file back instead of a mixed
		// ".claude\\GEMINI.md".
		{"claude_code", `C:\\Users\\dev\\.gemini\\GEMINI.md`, `C:\\Users\\dev\\.claude\\CLAUDE.md`},
		// Whole-segment: a directory that merely STARTS with the remapped name is
		// not that directory, so no rule may claim it.
		{"claude_code", ".gemini-backup/GEMINI.md", ".gemini-backup/GEMINI.md"},
		{"codex", "~/.gemini/AGENTS.md", "~/.codex/AGENTS.md"},
		{"codex", "./.gemini/skills/x/SKILL.md", "./.codex/skills/x/SKILL.md"},
		{"codex", "/home/u/.gemini/AGENTS.md", "/home/u/.codex/AGENTS.md"},
		{"codex", "/home/u/.gemini/config.toml", "/home/u/.codex/config.toml"},
		// The escaped spelling of the same paths, as they arrive inside a tool
		// call's arguments.
		{"codex", `C:\\Users\\u\\.gemini\\AGENTS.md`, `C:\\Users\\u\\.codex\\AGENTS.md`},
		{"oh_my_pi", "/home/u/.gemini/agent/AGENTS.md", "/home/u/.omp/agent/AGENTS.md"},
		{"oh_my_pi", "/home/u/.gemini/config.yml", "/home/u/.omp/config.yml"},
		{"oh_my_pi", `C:\\Users\\dev\\.gemini\\agent`, `C:\\Users\\dev\\.omp\\agent`},
		// Oh My Pi's global Claude memory, escaped: only the file pair below
		// ".gemini/" may claim this, or the client is handed ".omp\\AGENTS.md"
		// for a file it keeps at ".claude\\CLAUDE.md".
		{"oh_my_pi", `C:\\Users\\dev\\.gemini\\AGENTS.md`, `C:\\Users\\dev\\.claude\\CLAUDE.md`},
		// Whole-segment boundary: a larger element that merely ENDS in the
		// directory name is not that directory. The dot is not a word byte, so
		// nothing but this boundary stops the rule matching the suffix of
		// "foo.claude" / "foo.codex" / "foo.gemini".
		{"claude_code", "foo.gemini/GEMINI.md", "foo.gemini/GEMINI.md"},
		{"claude_code", "foo.gemini/settings.json", "foo.gemini/settings.json"},
		{"codex", "foo.gemini/AGENTS.md", "foo.gemini/AGENTS.md"},
		{"codex", "foo.gemini/config.toml", "foo.gemini/config.toml"},
		{"oh_my_pi", "foo.gemini/agent/AGENTS.md", "foo.gemini/agent/AGENTS.md"},
	} {
		got := applyReverseTable(tc.in, tc.client)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.client, got, tc.want)
		}
	}
}

// applyReverseTable runs one client's whole reverse table in declared order,
// through the same helper the non-stream body pass uses, so the table's own
// boundary data (whole-segment matches, exclusions) is honoured exactly as it
// is on the wire.
func applyReverseTable(text, client string) string {
	out, _ := replaceInsensitiveSetWithPrev(text, false, brandReverseTableFor(client))
	return out
}

// TestIssue49_NoCrossClientGuessing is the guard rail: a path is only ever
// restored for the client that was resolved, never guessed from the content.
func TestIssue49_NoCrossClientGuessing(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Each client restores only what its OWN forward pass could have produced.
	// Sharpest form of the rule: same input, three clients, and a path that is
	// only meaningful to one of them.
	// The directory rule is deliberately shared in shape, so what still makes
	// a path client-specific is the file name: only claude_code renames the
	// instruction file, and only Codex owns AGENTS.md.
	claudePath := "/home/u/.gemini/GEMINI.md"
	if got := applyReverseTable(claudePath, "claude_code"); !strings.Contains(got, ".claude/CLAUDE.md") {
		t.Fatalf("claude_code did not restore its own path: %q", got)
	}
	// codex never renames a file name, so a GEMINI.md is not its own and must be
	// left alone rather than guessed.
	if got := applyReverseTable(claudePath, "codex"); strings.Contains(got, "CLAUDE.md") {
		t.Fatalf("codex guessed a claude_code file name: %q", got)
	}
	ompPath := "/home/u/.gemini/agent/AGENTS.md"
	if got := applyReverseTable(ompPath, "oh_my_pi"); !strings.Contains(got, ".omp/") {
		t.Fatalf("oh_my_pi did not restore its own path: %q", got)
	}
}

// TestIssue49_OperationalDomainRoundTrips covers the one URL the forward pass
// cloaks for claude_code. The reverse rule has to precede the bare brand word,
// or "Antigravity" matches inside "antigravity.google" and the client receives
// "Claude.google".
func TestIssue49_OperationalDomainRoundTrips(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	body, changed, _ := rewriteRequestBodyWithClient(
		[]byte(`{"system":"docs at https://claude.ai/new"}`), "openai", "claude_code")
	if !changed || !strings.Contains(string(body), "https://antigravity.google/new") {
		t.Fatalf("forward domain remap failed: %s", body)
	}
	if got := applyReverseTable("read https://antigravity.google/new today", "claude_code"); got != "read https://claude.ai/new today" {
		t.Fatalf("reverse domain remap = %q", got)
	}
	// A client whose forward pass never introduced the domain must hand it back
	// untouched. The bare brand word matches inside "antigravity.google" (the
	// dot is a non-word byte, so the right boundary passes), and without a pin
	// the client received the dead host "Codex.google".
	for _, client := range []string{"codex", "oh_my_pi"} {
		if got := applyReverseTable("https://antigravity.google/new", client); got != "https://antigravity.google/new" {
			t.Errorf("%s rewrote a domain it never introduced: %q", client, got)
		}
	}
}

// TestIssue49_OMPRemovesBlanketPreservePolicy is the regression for the policy
// this ticket supersedes: .omp used to be preserved verbatim in every position,
// which left the client with a path upstream never saw. It is now an ordinary
// operational identifier, remapped forward and restored back, while a
// lookalike segment (.omp-backup, profile.omp) still masks the brand.
func TestIssue49_OMPRemovesBlanketPreservePolicy(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// The exact spelling matters: a remap onto anything other than .gemini, or
	// a truncated rewrite, satisfies a bare "no .omp left" check.
	for _, tc := range []struct{ in, want string }{
		{`{"system":"at /home/user/.omp"}`, `"at /home/user/.gemini"`},
		{`{"system":"at /home/user/.omp/agent"}`, `"at /home/user/.gemini/agent"`},
		{`{"system":"at C:\\Users\\monet\\.omp\\agent"}`, `"at C:\\Users\\monet\\.gemini\\agent"`},
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(tc.in), "openai", "oh_my_pi")
		if !changed {
			t.Errorf("%s was not rewritten: %s", tc.in, got)
			continue
		}
		if !strings.Contains(string(got), tc.want) {
			t.Errorf("%s: got %s, want it to contain %s", tc.in, got, tc.want)
		}
	}
	// Only the literal dot-prefixed directory is a real operational identifier.
	// A dot-element whose brand is glued to a suffix is a different path, and a
	// brand behind a leading file name is prose: both are still masked. The
	// replacement follows the casing it matched, so lowercase "omp" lands as
	// lowercase "antigravity".
	for _, in := range []string{
		`{"system":"at /home/user/profile.omp/agent"}`,
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(in), "openai", "oh_my_pi")
		if !changed || !strings.Contains(string(got), ".antigravity") {
			t.Errorf("%s must still mask the brand: %s", in, got)
		}
	}
	// ".omp-backup" is inside a real path element but is not the configuration
	// directory, so it is left byte-for-byte alone.
	bBackup, changedBackup, _ := rewriteRequestBodyWithClient([]byte(`{"system":"at /home/user/.omp-backup/agent"}`), "openai", "oh_my_pi")
	if changedBackup {
		t.Errorf(".omp-backup must be left alone: %s", bBackup)
	}
}

// TestIssue49_OMPReversePathThroughTheProtectedStreamLane drives the real OMP
// entry point: the protected lane owns the path rules in its single carry, so
// a path split across two chunks comes back as one token.
func TestIssue49_OMPReversePathThroughTheProtectedStreamLane(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue49-omp", "oh_my_pi", ompUncloakCache(t))

	var delivered strings.Builder
	collect := func(resp pluginapi.StreamChunkInterceptResponse) {
		for _, f := range sseFrames(t, resp.Body) {
			if f.data["type"] != "content_block_delta" {
				continue
			}
			delta, _ := f.data["delta"].(map[string]any)
			s, _ := delta["text"].(string)
			delivered.WriteString(s)
		}
	}
	collect(mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue49-omp", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"config at /home/u/.gemini/\"}}\n\n"),
	}, "anthropic"))
	collect(mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue49-omp", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"agent and by Antigravity\"}}\n\n"),
	}, "anthropic"))
	collect(mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue49-omp", SourceFormat: "anthropic", ChunkIndex: 2,
		Body: []byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
	}, "anthropic"))

	if got := delivered.String(); got != "config at /home/u/.omp/agent and by omp" {
		t.Fatalf("OMP protected lane delivered %q", got)
	}
}

// TestIssue49_BareHomeDirectoryRoundTrips covers the form the forward remap
// produces when the home directory IS the whole path: a tool argument that ends
// at the directory name carries no trailing separator, so separator-only
// reverse rules miss it. Every client failed this before the bare rule was
// added, at every chunk split.
func TestIssue49_BareHomeDirectoryRoundTrips(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, tc := range []struct {
		client string
		want   string
	}{
		// Only Oh My Pi: its home directory is remapped in full. Claude Code and
		// Codex remap the home instruction file only, so they never produce this
		// bare form in the first place - see TestIssue49_OnlyTheInstructionFile.
		{"oh_my_pi", "see ~/.omp"},
	} {
		// The forward pass really does produce the bare form.
		fwd, changed, _ := rewriteRequestBodyWithClient(
			[]byte(`{"system":"see ~/`+pathSegmentFor(tc.client)+`"}`), "openai", tc.client)
		if !changed || !strings.Contains(string(fwd), "~/.gemini") {
			t.Errorf("%s: forward pass did not produce the bare .gemini form: %s", tc.client, fwd)
		}
		if got := applyReverseTable("see ~/.gemini", tc.client); got != tc.want {
			t.Errorf("%s: bare home directory reverse = %q, want %q", tc.client, got, tc.want)
		}
	}
}

// pathSegmentFor names the dot-directory each client is remapped from.
func pathSegmentFor(client string) string {
	switch client {
	case "claude_code":
		return ".claude"
	case "codex":
		return ".codex"
	default:
		return ".omp"
	}
}

// TestPathContextIsLocalToTheMatch pins the rule that a URL scheme only makes a
// match "in a URL" when the scheme sits in the same whitespace-free token.
//
// The first implementation answered strings.Contains(value, "://") over the
// WHOLE value. Claude Code sends a ~200KB system prompt that certainly contains
// a URL, so every bare vendor word in it was classified as URL context and left
// alone: live traffic showed 63 "Claude" and 8 "Anthropic" reaching upstream
// untouched. The forward brand rewrite was a silent no-op on the largest
// surface the plugin has, and no unit test caught it because every fixture was
// a short string with the URL next to the match.
func TestPathContextIsLocalToTheMatch(t *testing.T) {
	cases := []struct {
		name, value, match string
		want               bool
	}{
		{"plain prose", "plain prose about Claude here", "Claude", false},
		{
			// The regression: the URL is far earlier in the same string.
			name: "url elsewhere in the same value", value: "see https://www.anthropic.com and also Claude Code rocks",
			match: "Claude", want: false,
		},
		{
			name: "url in the same token", value: "go to https://www.anthropic.com/Claude/docs now",
			match: "Claude", want: true,
		},
		{
			name: "path delimiter immediately before", value: "read /home/dev/Claude/notes.md now",
			match: "Claude", want: true,
		},
		{
			name: "windows separator", value: `read C:\Users\dev\Claude\notes.md now`,
			match: "Claude", want: true,
		},
		{
			name: "dot-directory", value: "persist to .omp-backup/agent today",
			match: "omp", want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := strings.Index(tc.value, tc.match)
			if idx < 0 {
				t.Fatalf("probe %q not present in %q", tc.match, tc.value)
			}
			got := inURLPathContext(tc.value, idx, idx+len(tc.match))
			if got != tc.want {
				t.Errorf("inURLPathContext(%q, %d) = %v, want %v", tc.value, idx, got, tc.want)
			}
		})
	}
}

// TestForwardRewriteSurvivesAPromptContainingAURL is the end-to-end guard for
// the same defect: a large value that carries a URL somewhere must still have
// every ordinary brand token rewritten.
func TestForwardRewriteSurvivesAPromptContainingAURL(t *testing.T) {
	prompt := strings.Repeat("filler words that say nothing interesting. ", 400) +
		"see https://www.anthropic.com for details. You are Claude Code, Anthropic's official CLI for Claude."
	next, changed := replaceInsensitiveRule(prompt, rewriteMapping{Match: "Claude", Replacement: "Antigravity"}, true)
	if !changed {
		t.Fatal("forward rewrite reported no change on a value containing a URL")
	}
	if strings.Contains(next, "You are Claude Code") || strings.Contains(next, "official CLI for Claude") {
		t.Errorf("brand survived a prompt that merely contains a URL: %s", next[len(next)-140:])
	}
	// Only the Claude mapping is in play here, so "Anthropic" is untouched by
	// this call; the real table rewrites it through its own rule.
	if !strings.Contains(next, "You are Antigravity Code, Anthropic's official CLI for Antigravity.") {
		t.Errorf("identity line was not rewritten: %s", next[len(next)-140:])
	}
}

// TestContextGroupRoundTrips is the contract for the injected-context path
// group. Only the paths a client actually injects into its system context are
// remapped, global and project-local alike, and in both directions. A plain
// home directory that is NOT injected context is left byte-for-byte, because a
// forward rule with no matching reverse rule is what collapsed a README edit
// into a no-op during live acceptance.
func TestContextGroupRoundTrips(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, tc := range []struct {
		client, in, want string
	}{
		// claude_code forward: the documented memory hierarchy. Only a home
		// spelling is cloaked; a .claude belonging to another project, the
		// absolute Unix home, and a config file are all left alone.
		{"claude_code", "read ~/.claude/CLAUDE.md", "read ~/.gemini/GEMINI.md"},
		{"claude_code", "read ./.claude/CLAUDE.md", "read ./.gemini/GEMINI.md"},
		{"claude_code", "load ~/.claude/rules/style.md", "load ~/.gemini/rules/style.md"},
		{"claude_code", "load ./.claude/rules/style.md", "load ./.gemini/rules/style.md"},
		{"claude_code", "recall ~/.claude/projects/-repo/memory/notes.md", "recall ~/.gemini/projects/-repo/memory/notes.md"},
		{"claude_code", "rekey ~/.claude/settings.json", "rekey ~/.gemini/settings.json"},
		{"claude_code", "read ./repo/.claude/AGENTS.md", "read ./repo/.gemini/AGENTS.md"},
		{"claude_code", "at /home/u/.claude/projects/p/memory.md", "at /home/u/.gemini/projects/p/memory.md"},
		{"claude_code", "read ~\\.claude\\CLAUDE.md", "read ~\\.gemini\\GEMINI.md"},
		{"claude_code", `read C:\Users\u\.claude\AGENTS.md`, `read C:\Users\u\.gemini\AGENTS.md`},
		// codex: the context group it actually injects, per
		// codex-rs/core/src/agents_md.rs. Its own walk uses a plain AGENTS.md at
		// the repo root, but CODEX_HOME may point at ./.codex for a per-repo
		// profile, so the project-local half is a real case here. The home
		// spelling is part of the match, which is what keeps each forward rule
		// paired with an exact reverse instead of a bare ".gemini".
		{"codex", "read ~/.codex/AGENTS.md", "read ~/.gemini/AGENTS.md"},
		{"codex", "read ~/.codex/AGENTS.override.md", "read ~/.gemini/AGENTS.override.md"},
		{"codex", "skill ~/.codex/skills/pdf/SKILL.md", "skill ~/.gemini/skills/pdf/SKILL.md"},
		{"codex", "read ./.codex/AGENTS.md", "read ./.gemini/AGENTS.md"},
		{"codex", "read ./.codex/AGENTS.override.md", "read ./.gemini/AGENTS.override.md"},
		{"codex", "skill ./.codex/skills/pdf/SKILL.md", "skill ./.gemini/skills/pdf/SKILL.md"},
		// The absolute form is in scope for the same reason: Claude Code and
		// Codex both put it in the system context on Windows, and measured 114
		// such paths per request going to the model uncloaked when it was not.
		{"codex", "read ./repo/.codex/AGENTS.md", "read ./repo/.gemini/AGENTS.md"},
		{"codex", `read C:\Users\u\.codex\AGENTS.md`, `read C:\Users\u\.gemini\AGENTS.md`},
		{"codex", "edit ./.codex/config.toml", "edit ./.gemini/config.toml"},
		// oh_my_pi: the whole home directory is the config directory, so the
		// blanket dot-segment rule already covers it in both directions.
		{"oh_my_pi", "read ~/.omp/agent/AGENTS.md", "read ~/.gemini/agent/AGENTS.md"},
		{"oh_my_pi", "load .omp/AGENTS.md", "load .gemini/AGENTS.md"},
	} {
		got, changed, _ := rewriteRequestBodyWithClient(
			[]byte(`{"system":`+strconv.Quote(tc.in)+`}`), "anthropic", tc.client)
		out := tc.in
		if changed {
			out = systemText(t, got)
		}
		if out != tc.want {
			t.Errorf("%s FORWARD\n  in:   %s\n  got:  %s\n  want: %s", tc.client, tc.in, out, tc.want)
		}
	}
}

// TestContextGroupReverses is the other direction: whatever the forward pass
// injected, the response has to hand the client back its own spelling.
func TestContextGroupReverses(t *testing.T) {
	for _, tc := range []struct {
		client, in, want string
	}{
		{"claude_code", "I read ~/.gemini/GEMINI.md", "I read ~/.claude/CLAUDE.md"},
		{"claude_code", "I read ./.gemini/rules/style.md", "I read ./.claude/rules/style.md"},
		{"claude_code", "recall ~/.gemini/projects/-repo/memory/notes.md", "recall ~/.claude/projects/-repo/memory/notes.md"},
		{"claude_code", "the .gemini cache", "the .gemini cache"},
		{"claude_code", "I read ./.gemini/GEMINI.md", "I read ./.claude/CLAUDE.md"},
		{"claude_code", "I read /home/u/.gemini/GEMINI.md", "I read /home/u/.claude/CLAUDE.md"},
		{"codex", "I read ~/.gemini/AGENTS.md", "I read ~/.codex/AGENTS.md"},
		{"codex", "I read ~/.gemini/AGENTS.override.md", "I read ~/.codex/AGENTS.override.md"},
		{"codex", "skill ~/.gemini/skills/pdf/SKILL.md", "skill ~/.codex/skills/pdf/SKILL.md"},
		{"codex", "read ./.gemini/AGENTS.md", "read ./.codex/AGENTS.md"},
		{"codex", "skill ./.gemini/skills/pdf/SKILL.md", "skill ./.codex/skills/pdf/SKILL.md"},
		{"codex", "the .gemini cache", "the .gemini cache"},
		{"oh_my_pi", "I read ~/.gemini/agent/AGENTS.md", "I read ~/.omp/agent/AGENTS.md"},
		// ".omp/AGENTS.md" is not a shape OMP emits - its own file is
		// ".omp/agent/AGENTS.md", which the row above covers. A bare
		// ".gemini/AGENTS.md" is therefore the target of the .claude pair, and
		// must come back as .claude, not as a fictional .omp path.
		{"oh_my_pi", "I read .gemini/AGENTS.md", "I read .claude/CLAUDE.md"},
		{"oh_my_pi", "I read C:/u/.gemini/AGENTS.md", "I read C:/u/.claude/CLAUDE.md"},
		{"oh_my_pi", "I read C:\\u\\.gemini\\AGENTS.md", "I read C:\\u\\.claude\\CLAUDE.md"},
	} {
		if got := applyReverseTable(tc.in, tc.client); got != tc.want {
			t.Errorf("%s REVERSE\n  in:   %s\n  got:  %s\n  want: %s", tc.client, tc.in, got, tc.want)
		}
	}
}
