package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// forwardCloakedBody runs the request intercept for a claude_code body and
// returns the rewritten payload the upstream would receive.
func forwardCloakedBody(t *testing.T, reqID, reqBody string) string {
	t.Helper()
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before", makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", []byte(reqBody), h))
	if code != 0 {
		t.Fatalf("code=%d, envelope=%s", code, raw)
	}
	out := string(decodeEnvelopeBody(t, raw))
	if out == "" {
		t.Fatalf("forward rewrite produced an empty body, envelope=%s", raw)
	}
	return out
}

// reverseBrandPass runs the response intercept for a claude_code body and
// returns what the client would see.
func reverseBrandPass(t *testing.T, reqID, reqBody, respBody, format, model string) string {
	t.Helper()
	payload := responseInterceptRequestJSON(t, reqBody, respBody, format)
	var mm map[string]any
	if err := json.Unmarshal(payload, &mm); err != nil {
		t.Fatalf("unmarshal response payload: %v", err)
	}
	mm["RequestID"] = reqID
	mm["Model"] = model
	payload, _ = json.Marshal(mm)
	raw, _ := handlePluginCall("response.intercept_after", payload)
	return string(decodeEnvelopeBody(t, raw))
}

// The home instruction file and the SDK name are introduced by the forward
// pass, so the response path must hand the client's own spelling back. A token
// split across two streaming deltas is the case that breaks naive line-based
// rewriting, so it is exercised explicitly.
func TestBidirectionalBrandRoundTrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	cases := []struct {
		name         string
		req, resp    string
		want         string
		mustNotReach []string
		mustReach    []string
	}{
		{
			// The audit covers the posix spelling and the SDK name; only the
			// windows separator is unique to this path.
			name: "home instruction file, windows path",
			req:  `{"messages":[{"role":"user","content":"<system-reminder>read C:\\\\Users\\\\dev\\\\.claude\\CLAUDE.md and the Anthropic SDK now</system-reminder>"}]}`,
			resp: `{"content":[{"type":"text","text":"I read C:\\\\Users\\\\dev\\\\.gemini\\GEMINI.md and it says hello."}]}`,
			// back is the raw JSON text, so path separators appear escaped.
			want: `C:\\\\Users\\\\dev\\\\.claude\\CLAUDE.md`,
			// asserted against the forward body, in the escaped spelling that
			// body actually carries. The previous list used a forward slash on a
			// backslash path, so it could never fire.
			mustNotReach: []string{`.claude\\CLAUDE.md`, `Anthropic SDK`},
			mustReach:    []string{`.gemini\\GEMINI.md`, `Antigravity SDK`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqID := "req_bidi_" + strings.ReplaceAll(tc.name, " ", "_")
			forward := forwardCloakedBody(t, reqID, tc.req)
			// The client's own spellings must not survive toward the model, and
			// the cloaked ones must actually be there. A leak check with no
			// matching positive assertion passes on a no-op forward pass.
			for _, leak := range tc.mustNotReach {
				if strings.Contains(forward, leak) {
					t.Fatalf("forward pass leaked %q: %s", leak, forward)
				}
			}
			for _, want := range tc.mustReach {
				if !strings.Contains(forward, want) {
					t.Fatalf("forward pass did not produce %q: %s", want, forward)
				}
			}
			back := reverseBrandPass(t, reqID, tc.req, tc.resp, "anthropic", "agy/claude-test")
			if back == "" {
				t.Fatal("reverse pass produced an empty body")
			}
			if !strings.Contains(back, tc.want) {
				t.Fatalf("reverse pass did not restore %q; got %s", tc.want, back)
			}

		})
	}
}

// The repository's own AGENTS.md is a real file with real instances across the
// machine. It must survive a round trip untouched in both directions: mapping
// it back to CLAUDE.md would send the client after a file that does not exist.
func TestRepoAgentsMdIsNeverCloakedInEitherDirection(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const realPath = "/home/user/project/AGENTS.md"
	reqID := "req_bidi_agents"
	req := `{"messages":[{"role":"user","content":"<system-reminder>read ` + realPath + ` and ~/.claude/CLAUDE.md</system-reminder>"}]}`

	forward := forwardCloakedBody(t, reqID, req)
	if !strings.Contains(forward, realPath) {
		t.Fatalf("forward pass must leave the repo AGENTS.md alone: %s", forward)
	}

	back := reverseBrandPass(t, reqID, req, `{"content":[{"type":"text","text":"Read `+realPath+` and ~/.gemini/GEMINI.md."}]}`, "anthropic", "agy/claude-test")
	if !strings.Contains(back, realPath) {
		t.Fatalf("reverse pass must not turn the repo AGENTS.md into CLAUDE.md: %s", back)
	}
	if !strings.Contains(back, ".claude/CLAUDE.md") {
		t.Fatalf("reverse pass should still restore the home file: %s", back)
	}
}

// A tool result is cloaked on the way up and mapped back on the way down, so
// the client still reads exactly what the tool returned.
func TestToolResultRoundTripsToTheClient(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "req_bidi_toolresult"
	const req = `{
		"messages":[
			{"role":"user","content":"read the file"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"/tmp/a.md"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"the ~/.claude/CLAUDE.md mentions the Anthropic SDK"}]}
		],
		"tools":[
			{"name":"Bash","description":""},{"name":"Read","description":""},
			{"name":"Write","description":""},{"name":"Edit","description":""},
			{"name":"Grep","description":""},{"name":"Glob","description":""},
			{"name":"Agent","description":""},{"name":"AskUserQuestion","description":""},
			{"name":"WebSearch","description":""},{"name":"WebFetch","description":""}
		]
	}`

	forward := forwardCloakedBody(t, reqID, req)
	if strings.Contains(forward, ".claude/CLAUDE.md") || strings.Contains(forward, "Anthropic SDK") {
		t.Fatalf("tool_result content still leaks client brand: %s", forward)
	}
	if !strings.Contains(forward, ".gemini/GEMINI.md") || !strings.Contains(forward, "Antigravity SDK") {
		t.Fatalf("tool_result content not cloaked for the model: %s", forward)
	}
}

// Streaming: the token is split across two deltas, so a line-based rewrite
// would see neither half and emit the cloaked spelling to the client.
func TestStreamingReverseJoinsTokenSplitAcrossDeltas(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "req_bidi_stream"
	const req = `{"messages":[{"role":"user","content":"read ~/.claude/CLAUDE.md"}],
		"tools":[{"name":"Read","description":""},{"name":"Bash","description":""},{"name":"Write","description":""},{"name":"Edit","description":""},{"name":"Grep","description":""},{"name":"Glob","description":""},{"name":"Agent","description":""},{"name":"AskUserQuestion","description":""},{"name":"WebSearch","description":""},{"name":"WebFetch","description":""}]}`
	forwardCloakedBody(t, reqID, req)

	sess := &streamSession{client: "claude_code"}
	// Two deltas split mid-token: "~/.gemini/GEM" + "INI.md and more text".
	// The same session carries the lane across both, which is what a real
	// stream does; a fresh session per delta would prove nothing.
	first := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I read ~/.gemini/GEM"}}` + "\n\n")
	second := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"INI.md today."}}` + "\n\n")

	m := globalStreamManager
	out1, _ := m.reverseBrandSSE(sess, first, "anthropic")
	out2, _ := m.reverseBrandSSE(sess, second, "anthropic")
	joined := string(out1) + string(out2)
	if strings.Contains(joined, "GEMINI.md") {
		t.Fatalf("clamped token leaked to the client: %s", joined)
	}
	if !strings.Contains(joined, "CLAUDE.md") {
		t.Fatalf("split token was not restored to CLAUDE.md: %s", joined)
	}
}

// A token whose tail lands on the last event before the stream ends never
// completes, so its lane still holds a partial match at [DONE]. That text must
// be flushed, not dropped: losing the end of a sentence is a worse failure
// than emitting it unreversed.
func TestStreamingReverseFlushesCarryAtStreamEnd(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	sess := &streamSession{client: "claude_code"}
	// The tail "~/.gemini/GEM" is held; the stream then ends without the
	// remainder ever arriving.
	held := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I read ~/.gemini/GEM"}}` + "\n\n")
	done := []byte("event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n")

	m := globalStreamManager
	out1, _ := m.reverseBrandSSE(sess, held, "anthropic")
	out2, _ := m.reverseBrandSSE(sess, done, "anthropic")

	// The flushed carry necessarily arrives as its own delta event, because the
	// bytes were withheld from the delta that held them. Reassemble the deltas
	// the way a streaming client does before asserting on the text; matching the
	// raw stream would only pass if the held token had leaked inline instead.
	joined := string(out1) + string(out2)
	text := ""
	for _, mt := range regexp.MustCompile(`"text":"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(joined, -1) {
		if dec, err := strconv.Unquote(`"` + mt[1] + `"`); err == nil {
			text += dec
		}
	}
	if !strings.Contains(text, "I read") {
		t.Fatalf("text before the held token was lost: %s", joined)
	}
	if !strings.Contains(text, "~/.claude/GEM") {
		t.Fatalf("held token was dropped at stream end instead of flushed: %s", joined)
	}
}

// Three gaps measured on live traffic after the first deploy:
//   - a tool parameter description naming the client home directory
//     (~/.claude/scheduled_tasks.json) survived in the payload;
//   - the plural "Workflows" in prose survived the word-bounded tool rule;
//   - the bare vendor name in the skill catalogue survived every rule.
func TestVendorAndSchemaAndPluralGapsAreClosed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "req_gaps"
	const req = `{
		"system":"Workflows run in the background.",
		"messages":[{"role":"user","content":"<system-reminder>the skill catalogue mentions Anthropic</system-reminder>"}],
		"tools":[{"name":"CronCreate","description":"schedule a task","input_schema":{"type":"object","properties":{"durable":{"type":"boolean","description":"persist to .claude/scheduled_tasks.json on disk"}}}}]
	}`
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before", makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", []byte(req), h))
	if code != 0 {
		t.Fatalf("code=%d, envelope=%s", code, raw)
	}
	forward := string(decodeEnvelopeBody(t, raw))
	for _, leak := range []string{".claude/", "Anthropic", "Workflows"} {
		if strings.Contains(forward, leak) {
			t.Fatalf("forward pass still leaks %q: %s", leak, forward)
		}
	}
	// The parameter schema keeps its shape; only the help text is rewritten.
	if !strings.Contains(forward, `"type":"boolean"`) {
		t.Fatalf("schema shape was damaged: %s", forward)
	}
	if !strings.Contains(forward, ".gemini/scheduled_tasks.json") {
		t.Fatalf("parameter description not remapped: %s", forward)
	}

	back := reverseBrandPass(t, reqID, req, `{"content":[{"type":"text","text":"The catalogue mentions Google Deepmind and .gemini/scheduled_tasks.json."}]}`, "anthropic", "agy/claude-test")
	if strings.Contains(back, "Google Deepmind") {
		t.Fatalf("vendor name not restored on the way back: %s", back)
	}
	if !strings.Contains(back, "Anthropic") {
		t.Fatalf("vendor name not restored to the client spelling: %s", back)
	}
	// The home-directory remap is client-scoped in BOTH directions: this
	// request only ever produced .gemini because the client's own .claude was
	// remapped forward, so handing back a .gemini path would point the client
	// at a directory that does not exist on its machine.
	if !strings.Contains(back, ".claude/scheduled_tasks.json") {
		t.Fatalf("the .gemini home directory was not mapped back to the client spelling: %s", back)
	}
}

// The user's own words must survive untouched. Cloaking them meant the model
// wrote the cloaked spelling to disk, where nothing can reverse it: a real
// test produced a file containing "~/.gemini/GEMINI.md là file hướng dẫn riêng
// của tôi" when the user had typed "~/.claude/CLAUDE.md là file hướng dẫn riêng
// của tôi".
func TestTypedUserTextIsNeverCloaked(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "req_typed_text"
	const typed = "đọc ~/.claude/CLAUDE.md và dùng Anthropic SDK giúp tôi"
	const req = `{"messages":[{"role":"user","content":"` + typed + `"}]}`

	// Nothing in this turn is machine-generated, so the correct outcome is no
	// mutation at all: the interceptor returns an empty body.
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before", makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", []byte(req), h))
	if code != 0 {
		t.Fatalf("code=%d, envelope=%s", code, raw)
	}
	if got := string(decodeEnvelopeBody(t, raw)); got != "" {
		t.Fatalf("typed user text must pass through unmutated; got %s", got)
	}
}

// Tool call arguments stream as raw JSON fragments. Anything the model writes
// to disk arrives through there, so a cloaked token left in place is persisted
// to the user's filesystem with no way back.
func TestStreamedToolArgumentsAreReversed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	sess := &streamSession{client: "claude_code"}
	m := globalStreamManager
	// Split mid-token so the lane has to carry across both deltas, exactly as
	// it did when the real file was written with the cloaked spelling.
	first := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\": \"a.md\", \"content\": \"x ~/.gemini/GEM"}}` + "\n\n")
	second := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"INI.md and Antigravity SDK\"}"}}` + "\n\n")

	out1, _ := m.reverseBrandSSE(sess, first, "anthropic")
	out2, _ := m.reverseBrandSSE(sess, second, "anthropic")
	joined := string(out1) + string(out2)
	if strings.Contains(joined, "GEMINI.md") || strings.Contains(joined, "Antigravity SDK") {
		t.Fatalf("cloaked token left in tool arguments: %s", joined)
	}
	if !strings.Contains(joined, "CLAUDE.md") || !strings.Contains(joined, "Anthropic SDK") {
		t.Fatalf("tool arguments not restored to the client spelling: %s", joined)
	}
}

// The reverse must survive the real entry point. An earlier version dispatched
// correctly inside reverseBrandSSE but the call site in processChunk still
// gated on oh_my_pi, so for every other client the whole streaming reverse was
// unreachable: the skill call went out as "Antigravity-api" and the client
// answered "Unknown skill". Driving processChunk is what catches that class of
// bug; calling the inner helper directly does not.
func TestStreamBrandReverseRunsForClaudeCodeThroughProcessChunk(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "req_entry_point"
	reqBody := []byte(`{"system":"x","messages":[{"role":"user","content":"go"}],
		"tools":[{"name":"Bash","description":""},{"name":"Read","description":""},{"name":"Write","description":""},{"name":"Edit","description":""},{"name":"Grep","description":""},{"name":"Glob","description":""},{"name":"Agent","description":""},{"name":"AskUserQuestion","description":""},{"name":"WebSearch","description":""},{"name":"WebFetch","description":""}]}`)
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before", makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-test", reqBody, h))
	if code != 0 {
		t.Fatalf("code=%d, envelope=%s", code, raw)
	}

	m := globalStreamManager
	key := m.sessionKey(&pluginapi.StreamChunkInterceptRequest{RequestID: reqID})
	if m.getClient(key) != "claude_code" {
		t.Fatalf("session not registered for claude_code (key=%q client=%q)", key, m.getClient(key))
	}

	call := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"skill\": \"Antigravity-api\"}"}}` + "\n\n")
	resp := m.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		Body:         call,
		SourceFormat: "claude",
	}, "anthropic")
	if strings.Contains(string(resp.Body), "Antigravity-api") {
		t.Fatalf("streaming reverse did not run for claude_code: %s", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "claude-api") {
		t.Fatalf("skill slug not restored through processChunk: %s", resp.Body)
	}
}
