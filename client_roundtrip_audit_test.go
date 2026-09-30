package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Every token the forward pass introduces must come back on the response path.
// A one-way rewrite is the worst failure this plugin can have: the request
// looks clean while the client is handed text it cannot resolve, which is the
// "Unknown skill" and "read a file that does not exist" class of bug.
//
// Each case carries its own context, and the comment on every field says where
// that text comes from, because the provenance differs per client and getting
// it wrong is silent:
//
//   - claude_code: the path-qualified ~/.claude/CLAUDE.md and the vendor word
//     are its own. There is no public repo to read, so this row rests on
//     observed Claude Code traffic rather than on source.
//   - codex and oh_my_pi: neither ships a vendor token. Verified by reading
//     their sources, not by assumption. Codex's model-facing prompts
//     (.ref/codex codex-rs/core/gpt_5*.md) contain AGENTS.md eleven times and
//     anthropic/claude/gemini/CLAUDE.md zero times; its claude mentions are all
//     in external-agent-migration, hooks and core-plugins, which read a Claude
//     Code install to migrate config and never reach the wire. Oh My Pi is the
//     same across all 83 prompt files it ships.
//   - What DOES put those tokens in a codex or oh_my_pi request is the project's
//     own context files. This repository's AGENTS.md names Claude Code and
//     .claude/CLAUDE.md throughout, so any client working in it carries them.
//
// The forward pass is client-agnostic, so whatever a request carries must come
// back; the per-client tables below exist to pin each client's own reverse.
//
// Both tests go through the real entry points, not the internal helpers. An
// earlier version dispatched the streaming reverse correctly inside
// reverseBrandSSE while the call site in processChunk still gated on oh_my_pi,
// so it never ran for any client and every helper-level test stayed green.
type clientContext struct {
	client, tools, format, declared string
	// sent is the context this client actually puts on the wire.
	sent string
	// echoes is what the model sends back: the cloaked spelling of sent,
	// because the model only ever saw the cloaked form. Feeding the model the
	// original spelling would make every assertion below trivially true.
	echoes string
	// mustComeBack is a substring of sent that has to survive the round trip.
	mustComeBack string
	// mustNotReach is the cloaked spelling that must never reach the client.
	mustNotReach string
	// brandReply is what the model echoes back, when this client has a brand
	// policy of its own to exercise.
	brandReply string
	// brandBack is what the client must see instead of brandReply.
	brandBack string
}

func clientContexts() []clientContext {
	const claudeTools = `{"name":"Bash"},{"name":"Read"},{"name":"Write"},{"name":"Edit"},` +
		`{"name":"Grep"},{"name":"Glob"},{"name":"Agent"},{"name":"AskUserQuestion"},` +
		`{"name":"WebSearch"},{"name":"WebFetch"}`
	const codexTools = `{"type":"function","function":{"name":"shell"}},` +
		`{"type":"function","function":{"name":"apply_patch"}},` +
		`{"type":"function","function":{"name":"update_plan"}},` +
		`{"type":"function","function":{"name":"view_image"}}`
	const ompTools = `{"type":"function","function":{"name":"read"}},` +
		`{"type":"function","function":{"name":"write"}},` +
		`{"type":"function","function":{"name":"edit"}},` +
		`{"type":"function","function":{"name":"bash"}},` +
		`{"type":"function","function":{"name":"grep"}},` +
		`{"type":"function","function":{"name":"glob"}},` +
		`{"type":"function","function":{"name":"task"}},` +
		`{"type":"function","function":{"name":"ask"}}`

	return []clientContext{
		{
			// Claude Code is the client whose context carries the path-qualified
			// instruction file and the vendor word, so it is the one that
			// exercises the shared cloaked table end to end.
			client: "claude_code", tools: claudeTools, format: "anthropic", declared: "anthropic",
			sent:         "see ~/.claude/CLAUDE.md and the Anthropic SDK from Google Deepmind",
			echoes:       "see ~/.gemini/GEMINI.md and the Antigravity SDK from Google Deepmind",
			mustComeBack: "~/.claude/CLAUDE.md",
			mustNotReach: "~/.gemini/GEMINI.md",
			brandReply:   "",
			brandBack:    "",
		},
		{
			// Codex owns no vendor token, so its table holds only its own name.
			// The bare brand word is both what it rewrites and what the reverse
			// hands back. CLAUDE.md stays verbatim: Codex carries no bare-Claude
			// rule that could interfere with it, so rewriting the file name to
			// AGENTS.md bought nothing and only risked misnaming a real file.
			client: "codex", tools: codexTools, format: "openai", declared: "openai",
			sent:         "read CLAUDE.md, then run Codex",
			echoes:       "read CLAUDE.md, then run Antigravity",
			mustComeBack: "Codex",
			mustNotReach: "Antigravity",
		},
		{
			// Oh My Pi ships bare filenames in prose and brands itself Oh My Pi.
			// CLAUDE.md stays verbatim: with no bare-Claude rule in this table
			// nothing would rewrite it anyway, and rewriting it to AGENTS.md
			// would only misname a file the harness really does load.
			client: "oh_my_pi", tools: ompTools, format: "anthropic", declared: "openai",
			sent:         "read CLAUDE.md first, never grep for CLAUDE.md or .cursorrules",
			echoes:       "read CLAUDE.md first, never grep for CLAUDE.md or .cursorrules",
			mustComeBack: "CLAUDE.md",
			mustNotReach: "",
			brandReply:   "Antigravity",
			brandBack:    "omp",
		},
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := safeMarshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// The non-streaming round trip.
func TestEveryClientReversesWhatItRewrites(t *testing.T) {
	for _, tc := range clientContexts() {
		t.Run(tc.client, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

			reqID := "req_audit_" + tc.client
			body := `{"system":"` + tc.sent + `","messages":[{"role":"user","content":"` + tc.sent + `"}],"tools":[` + tc.tools + `]}`
			h := http.Header{}
			h.Set("X-Cloak-Client", tc.client)

			raw, code := handlePluginCall("request.intercept_before",
				makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, tc.declared, "agy/audit-model", []byte(body), h))
			if code != 0 {
				t.Fatalf("code=%d, envelope=%s", code, raw)
			}
			forward := string(decodeEnvelopeBody(t, raw))
			if forward == "" {
				t.Fatalf("forward pass produced no body: %s", raw)
			}
			// Prove the forward half before testing the reverse half. A non-empty
			// body proves nothing: tool declarations alone keep it non-empty, so
			// a brand rewrite that never ran would still reach every assertion
			// below and the whole round trip would be vacuous.
			if !strings.Contains(forward, tc.echoes) {
				t.Fatalf("%s: forward pass did not produce %q:\n%s", tc.client, tc.echoes, forward)
			}

			echo := tc.echoes
			if tc.brandReply != "" {
				echo += " " + tc.brandReply
			}
			// Put the echo in the carrier THIS protocol's client actually reads.
			// A single body carrying both shapes let the anthropic branch satisfy
			// every case, so the openai string branch was never executed and
			// stayed green while broken.
			var reply string
			if tc.format == "anthropic" {
				reply = `{"content":[{"type":"text","text":"` + echo + `"}]}`
			} else {
				reply = `{"choices":[{"message":{"content":` + mustJSON(t, echo) + `}}]}`
			}
			back := reverseBrandPass(t, reqID, body, reply, tc.format, "agy/audit-model")
			if back == "" {
				t.Fatalf("%s: response reverse produced no body", tc.client)
			}
			if !strings.Contains(back, tc.mustComeBack) {
				t.Errorf("%s: %q did not survive the round trip, client saw: %s", tc.client, tc.mustComeBack, back)
			}
			if tc.mustNotReach != "" && strings.Contains(back, tc.mustNotReach) {
				t.Errorf("%s: cloaked %q reached the client: %s", tc.client, tc.mustNotReach, back)
			}
			if tc.brandReply != "" {
				if strings.Contains(back, tc.brandReply) {
					t.Errorf("%s: brand %q was rewritten forward but never reversed: %s", tc.client, tc.brandReply, back)
				}
				if !strings.Contains(back, tc.brandBack) {
					t.Errorf("%s: expected brand %q, client saw: %s", tc.client, tc.brandBack, back)
				}
			}
		})
	}
}

// The same round trip over SSE. The non-stream path proves nothing about
// streaming: partial matches are held in session state and flushed at different
// points, so both paths need their own check.
func TestEveryClientReversesWhatItRewritesWhileStreaming(t *testing.T) {
	for _, tc := range clientContexts() {
		t.Run(tc.client, func(t *testing.T) {
			defer restoreDefaultFilterConfig(t)
			handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

			reqID := "req_stream_audit_" + tc.client
			reqBody := []byte(`{"system":"` + tc.sent + `","messages":[{"role":"user","content":"go"}],"tools":[` + tc.tools + `]}`)
			h := http.Header{}
			h.Set("X-Cloak-Client", tc.client)
			raw, code := handlePluginCall("request.intercept_before",
				makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, tc.declared, "agy/audit-model", reqBody, h))
			if code != 0 {
				t.Fatalf("code=%d, envelope=%s", code, raw)
			}
			// The stream test proves nothing about the forward pass either, for
			// the same reason: the echo below is only meaningful if the model was
			// really shown the cloaked spelling.
			forward := string(decodeEnvelopeBody(t, raw))
			if !strings.Contains(forward, tc.echoes) {
				t.Fatalf("%s: forward pass did not produce %q:\n%s", tc.client, tc.echoes, forward)
			}

			m := globalStreamManager
			if got := m.getClient(m.sessionKey(&pluginapi.StreamChunkInterceptRequest{RequestID: reqID})); got != tc.client {
				t.Fatalf("session not registered for %s (got %q)", tc.client, got)
			}

			// One content block held open across the fragments, the way a real
			// stream arrives. Closing a block after every fragment would force
			// each token to flush half-written, which is a different and easier
			// problem than the one the lane machinery solves.
			echo := tc.echoes
			if tc.brandReply != "" {
				echo += " " + tc.brandReply
			}
			half := len(echo) / 2
			frags := []string{echo[:half], echo[half:]}

			var all strings.Builder
			for i, frag := range frags {
				ev := ""
				if tc.format == "anthropic" {
					if i == 0 {
						ev += "event: message_start\ndata: " + mustJSON(t, map[string]any{
							"type":    "message_start",
							"message": map[string]any{"type": "message", "role": "assistant"}}) + "\n\n"
						ev += "event: content_block_start\ndata: " + mustJSON(t, map[string]any{
							"type": "content_block_start", "index": 0,
							"content_block": map[string]any{"type": "text", "text": ""}}) + "\n\n"
					}
					ev += "event: content_block_delta\ndata: " + mustJSON(t, map[string]any{
						"type": "content_block_delta", "index": 0,
						"delta": map[string]any{"type": "text_delta", "text": frag}}) + "\n\n"
					if i == len(frags)-1 {
						ev += "event: content_block_stop\ndata: " +
							mustJSON(t, map[string]any{"type": "content_block_stop", "index": 0}) + "\n\n" +
							"event: message_stop\ndata: " +
							mustJSON(t, map[string]any{"type": "message_stop"}) + "\n\n"
					}
				} else {
					ev += "data: " + mustJSON(t, map[string]any{
						"choices": []any{map[string]any{
							"index": 0, "delta": map[string]any{"content": frag}}}}) + "\n\n"
					if i == len(frags)-1 {
						ev += "data: [DONE]\n\n"
					}
				}
				resp := m.processChunk(&pluginapi.StreamChunkInterceptRequest{
					RequestID: reqID, ChunkIndex: i, Body: []byte(ev), SourceFormat: tc.declared,
				}, tc.format)
				// The host forwards the original chunk untouched whenever the
				// plugin does not modify it, so an empty body is not an empty
				// stream. DropChunk means the plugin is buffering a partial
				// event and the chunk is deliberately withheld.
				switch {
				case resp.DropChunk:
				case len(resp.Body) > 0:
					all.Write(resp.Body)
				default:
					all.Write([]byte(ev))
				}
			}

			out := all.String()
			if !strings.Contains(out, tc.mustComeBack) {
				t.Errorf("%s: %q did not survive the streamed round trip, client saw: %s", tc.client, tc.mustComeBack, out)
			}
			if tc.mustNotReach != "" && strings.Contains(out, tc.mustNotReach) {
				t.Errorf("%s: cloaked %q streamed out to the client: %s", tc.client, tc.mustNotReach, out)
			}
			if tc.brandReply != "" {
				if strings.Contains(out, tc.brandReply) {
					t.Errorf("%s: brand %q streamed out unreversed: %s", tc.client, tc.brandReply, out)
				}
				if !strings.Contains(out, tc.brandBack) {
					t.Errorf("%s: expected brand %q in the stream, client saw: %s", tc.client, tc.brandBack, out)
				}
			}
		})
	}
}

// A resolved client with no cached uncloak pattern is the shape an alias-plan
// client actually has: it pins its tool-name authority in the plan, not in a
// pattern, so the stream session arrives with cached == nil. The brand reverse
// does not need the pattern, so it has to run anyway.
//
// This is the case the request-scoped audit above cannot reach: registering
// through request.intercept_before also populates the pattern, so every
// session that test builds has one. A guard that returned early on a nil
// pattern therefore passed that whole audit while never reversing a single
// byte on the wire.
func TestBrandReverseRunsWithNoCachedPattern(t *testing.T) {
	m := globalStreamManager
	const reqID = "req_nil_cached_brand"
	key := m.sessionKey(&pluginapi.StreamChunkInterceptRequest{RequestID: reqID})
	m.mu.Lock()
	m.sessions[key] = &streamSession{
		client: "claude_code", brandCarries: map[string]*brandLane{}, updatedAt: time.Now(),
	}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		delete(m.sessions, key)
		m.mu.Unlock()
	})

	if m.sessions[key].cached != nil {
		t.Fatal("probe must start with a nil cached pattern")
	}

	ev := "event: content_block_delta\ndata: " +
		mustJSON(t, map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "open ~/.gemini/GEMINI.md now"}}) + "\n\n" +
		"event: content_block_stop\ndata: " +
		mustJSON(t, map[string]any{"type": "content_block_stop", "index": 0}) + "\n\n" +
		"event: message_stop\ndata: " +
		mustJSON(t, map[string]any{"type": "message_stop"}) + "\n\n"

	resp := m.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: reqID, ChunkIndex: 0, Body: []byte(ev), SourceFormat: "anthropic",
	}, "anthropic")

	out := string(resp.Body)
	if !resp.DropChunk && out == "" {
		out = ev
	}
	if strings.Contains(out, "~/.gemini/GEMINI.md") {
		t.Fatalf("cloaked token streamed out unreversed: %s", out)
	}
	if !strings.Contains(out, "~/.claude/CLAUDE.md") {
		t.Fatalf("expected the client's own spelling back: %s", out)
	}
}
