package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Every token the forward pass introduces must come back on the response path.
// A one-way rewrite is the worst failure this plugin can have: the request
// looks clean while the client is handed text it cannot resolve, which is the
// "Unknown skill" and "read a file that does not exist" class of bug.
//
// These cases carry each client's REAL context, not a shared one. That
// distinction is the whole point: Oh My Pi names context files by their bare
// filename (CLAUDE.md -> AGENTS.md, no vendor token anywhere) and brands itself
// Oh My Pi -> Antigravity -> omp, while Claude Code and Codex carry the
// path-qualified ~/.claude/CLAUDE.md and the vendor word Anthropic. Driving all
// three with Claude Code's context asserts a reverse Oh My Pi never needs and
// misses the one reverse it actually depends on.
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
			sent:         "see .claude/CLAUDE.md and the Anthropic SDK from Google Deepmind",
			echoes:       "see .gemini/GEMINI.md and the Antigravity SDK from Google Deepmind",
			mustComeBack: ".claude/CLAUDE.md",
			mustNotReach: ".gemini/GEMINI.md",
			brandReply:   "",
			brandBack:    "",
		},
		{
			client: "codex", tools: codexTools, format: "openai", declared: "openai",
			sent:         "see .claude/CLAUDE.md and the Anthropic SDK from Google Deepmind",
			echoes:       "see .gemini/GEMINI.md and the Antigravity SDK from Google Deepmind",
			mustComeBack: ".claude/CLAUDE.md",
			mustNotReach: ".gemini/GEMINI.md",
		},
		{
			// Oh My Pi ships bare filenames in prose and brands itself Oh My Pi.
			// The forward pass turns CLAUDE.md into AGENTS.md, which is Oh My
			// Pi's own convention and needs no reverse; the only reverse this
			// client has is the protected brand pair.
			client: "oh_my_pi", tools: ompTools, format: "anthropic", declared: "openai",
			sent:         "read AGENTS.md first, never grep for CLAUDE.md or .cursorrules",
			echoes:       "read AGENTS.md first, never grep for AGENTS.md or .cursorrules",
			mustComeBack: "AGENTS.md",
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

			echo := tc.echoes
			if tc.brandReply != "" {
				echo += " " + tc.brandReply
			}
			reply := `{"choices":[{"message":{"content":"ok"}}],"content":[{"type":"text","text":"` + echo + `"}]}`
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
			if raw, code := handlePluginCall("request.intercept_before",
				makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, tc.declared, "agy/audit-model", reqBody, h)); code != 0 {
				t.Fatalf("code=%d, envelope=%s", code, raw)
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
