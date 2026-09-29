package main

import (
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
		{"claude_code", `{"system":"see /home/u/.claude/projects/p/memory.md"}`, "/home/u/.gemini/projects/p/memory.md"},
		{"claude_code", `{"system":"see C:\\Users\\u\\.claude\\settings.json"}`, `C:\\Users\\u\\.gemini\\settings.json`},
		{"codex", `{"system":"see /home/u/.codex/config.toml"}`, "/home/u/.gemini/config.toml"},
		{"codex", `{"system":"see C:\\Users\\u\\.codex\\config.toml"}`, `C:\\Users\\u\\.gemini\\config.toml`},
		{"oh_my_pi", `{"system":"see /home/u/.omp/agent/AGENTS.md"}`, "/home/u/.gemini/agent/AGENTS.md"},
		{"oh_my_pi", `{"system":"see C:\\Users\\u\\.omp\\agent"}`, `C:\\Users\\u\\.gemini\\agent`},
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(tc.in), "openai", tc.client)
		if !changed || !strings.Contains(string(got), tc.want) {
			t.Errorf("%s: got %s, want it to contain %s", tc.client, got, tc.want)
		}
	}
}

// TestIssue49_ReversePathSegmentPerClient covers the way back, including the
// Windows separator and the JSON-escaped spelling a streamed tool call
// argument actually arrives in.
func TestIssue49_ReversePathSegmentPerClient(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, tc := range []struct {
		client string
		in     string
		want   string
	}{
		{"claude_code", "/home/u/.gemini/projects/p/memory.md", "/home/u/.claude/projects/p/memory.md"},
		{"claude_code", `C:\\Users\\dev\\.gemini\\settings.json`, `C:\\Users\\dev\\.claude\\settings.json`},
		{"codex", "/home/u/.gemini/config.toml", "/home/u/.codex/config.toml"},
		{"codex", `C:\\Users\\dev\\.gemini\\config.toml`, `C:\\Users\\dev\\.codex\\config.toml`},
		{"oh_my_pi", "/home/u/.gemini/agent/AGENTS.md", "/home/u/.omp/agent/AGENTS.md"},
		{"oh_my_pi", `C:\\Users\\dev\\.gemini\\agent`, `C:\\Users\\dev\\.omp\\agent`},
	} {
		got := applyReverseTable(tc.in, tc.client)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.client, got, tc.want)
		}
	}
}

// applyReverseTable runs one client's whole reverse table in declared order,
// the same way the streaming lane and the non-stream body pass both do.
func applyReverseTable(text, client string) string {
	out := text
	for _, m := range brandReverseTableFor(client) {
		out, _ = replaceInsensitive(out, m.Match, m.Replacement)
	}
	return out
}

// TestIssue49_NoCrossClientGuessing is the guard rail: a path is only ever
// restored for the client that was resolved, never guessed from the content.
func TestIssue49_NoCrossClientGuessing(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const cloaked = "/home/u/.gemini/projects/p/memory.md"
	if got := applyReverseTable(cloaked, "claude_code"); !strings.Contains(got, ".claude/") {
		t.Fatalf("claude_code did not restore its own path: %q", got)
	}
	if got := applyReverseTable(cloaked, "codex"); !strings.Contains(got, ".codex/") {
		t.Fatalf("codex did not restore its own path: %q", got)
	}
	if got := applyReverseTable(cloaked, "oh_my_pi"); !strings.Contains(got, ".omp/") {
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

	for _, in := range []string{
		`{"system":"at /home/user/.omp"}`,
		`{"system":"at /home/user/.omp/agent"}`,
		`{"system":"at C:\\Users\\monet\\.omp\\agent"}`,
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(in), "openai", "oh_my_pi")
		if !changed || strings.Contains(string(got), ".omp") {
			t.Errorf("%s still preserved: %s", in, got)
		}
	}
	// Only the literal dot-prefixed directory is a real operational identifier.
	// A dot-element whose brand is glued to a suffix is a different path, and a
	// brand behind a leading file name is prose: both are still masked.
	for _, in := range []string{
		`{"system":"at /home/user/profile.omp/agent"}`,
	} {
		got, changed, _ := rewriteRequestBodyWithClient([]byte(in), "openai", "oh_my_pi")
		if !changed || !strings.Contains(string(got), ".Antigravity") {
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
		{"claude_code", "see ~/.claude"},
		{"codex", "see ~/.codex"},
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
