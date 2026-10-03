package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// P1 - OMP streamed tool arguments must restore operational paths.
//
// A model that writes a cloaked path into a file hands the client a path that
// does not exist. Anthropic streams those bytes as input_json_delta and
// Chat Completions as tool_calls[].function.arguments; both are argument
// carriers, not prose, and each needs its own lane so a hold there is flushed
// back as arguments rather than as assistant text.
func TestIssue51_OMPStreamRestoresToolArguments(t *testing.T) {
	for _, format := range []string{"anthropic", "openai"} {
		t.Run(format, func(t *testing.T) {
			isolateOMPMeasurement(t)
			handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))
			mgr := globalStreamManager
			reqID := "req_issue51_args_" + format
			t.Cleanup(func() {
				mgr.mu.Lock()
				mgr.sessions = nil
				mgr.mu.Unlock()
			})
			h := http.Header{}
			h.Set("X-Cloak-Client", "oh_my_pi")
			// The canonical OMP declaration shape: admission is strict on purpose.
			orig := `{"messages":[],"tools":[{"type":"function","function":{"name":"bash"}}]}`
			raw, code := handlePluginCall("request.intercept_before",
				makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai",
					"agy/model", []byte(orig), h))
			if code != 0 || strings.Contains(string(raw), `"StatusCode":503`) {
				t.Fatalf("admission rejected: code=%d %s", code, raw)
			}

			// The upstream model echoed a cloaked operational path, split so a
			// hold is forced mid-token.
			var chunks []string
			if format == "anthropic" {
				chunks = []string{
					"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"run_command\",\"input\":{}}}\n\n",
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"cat /home/u/.gem\"}}\n\n",
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"ini/GEMINI.md\\\"}\"}}\n\n",
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
				}
			} else {
				chunks = []string{
					`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"run_command","arguments":"{\"command\":\"cat /home/u/.gem"}}]}}]}`,
					`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ini/GEMINI.md\"}"}}]}}]}`,
				}
			}
			var delivered strings.Builder
			for i, c := range chunks {
				resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
					RequestID: reqID, SourceFormat: format, ChunkIndex: i + 1,
					Body: []byte(c),
				}, format)
				if len(resp.Body) == 0 {
					delivered.WriteString(c)
					continue
				}
				delivered.Write(resp.Body)
			}
			body := delivered.String()
			if strings.Contains(body, ".gemini/GEMINI.md") {
				t.Fatalf("cloaked operational path reached the client: %q", body)
			}
			if !strings.Contains(body, ".omp/GEMINI.md") {
				t.Fatalf("operational path was not restored in tool arguments: %q", body)
			}
			// The identity of the tool is exact-uncloak authority, not brand
			// reverse: the client declared bash, so bash is what comes back and
			// the cloaked name must not. This is asserted on the concatenated
			// delivered bytes, including any chunk the plugin passed through
			// unchanged, because a chunk that falls back to its raw form would
			// still reach the client.
			if !strings.Contains(body, "bash") {
				t.Fatalf("tool identity was not restored: %q", body)
			}
			if strings.Contains(body, "run_command") {
				t.Fatalf("cloaked tool identity reached the client: %q", body)
			}
		})
	}
}

// P2 - the non-stream brand reverse must not corrupt tool identity.
//
// Claude Code declares a tool whose name is spelled like a cloaked brand token.
// Exact uncloak restores it correctly, and the old recursive all-string brand
// walk then rewrote it a second time, so the client received a tool it never
// declared.
func TestIssue51_ExactUncloakOwnsToolIdentity(t *testing.T) {
	isolateOMPMeasurement(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))
	reqID := "req_issue51_identity"
	// The declaration itself carries a cloaked-looking name, so the forward
	// pass has to give it an alias plan entry.
	orig := `{"system":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Antigravity-api","description":"d","input_schema":{"type":"object"}}]}`
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/model", []byte(orig), h))
	if code != 0 {
		t.Fatalf("admission rejected: code=%d %s", code, raw)
	}
	forward := string(decodeEnvelopeBody(t, raw))
	if forward == "" {
		t.Fatalf("empty forward envelope: %s", raw)
	}
	alias := ""
	var parsed map[string]any
	if err := json.Unmarshal(decodeEnvelopeBody(t, raw), &parsed); err == nil {
		if tools, ok := parsed["tools"].([]any); ok && len(tools) > 0 {
			if tool, ok := tools[0].(map[string]any); ok {
				alias, _ = tool["name"].(string)
			}
		}
	}
	if alias == "" {
		t.Fatalf("no alias plan entry in forward body: %s", forward)
	}
	if strings.Contains(alias, "Antigravity") {
		t.Fatalf("declaration name was not cloaked upstream: %q", alias)
	}

	reply := `{"choices":[],"content":[{"type":"tool_use","id":"toolu_1","name":"` + alias + `","input":{"command":"ls"}}]}`
	back := reverseBrandPass(t, reqID, orig, reply, "anthropic", "agy/model")
	if back == "" {
		t.Fatal("response reverse produced no body")
	}
	if !strings.Contains(back, `"Antigravity-api"`) {
		t.Fatalf("exact tool identity was corrupted by the brand reverse: %s", back)
	}
	if strings.Contains(back, "claude-api") {
		t.Fatalf("tool name was rewritten by a brand rule: %s", back)
	}
}

// P4 - typed user prose is the user's, byte for byte.
//
// The request-side tool-name pass used to run over every role, and then over
// the whole user string even after the role check was added. A file the user
// really named was silently persisted under an alias, and the model was told
// to create a file whose name the user never asked for.
//
// The bytes OUTSIDE a client-generated span are never rewritten - for brands or
// for tool names. Inside a <system-reminder> span both apply, because that text
// came from the same conversation state the model was given the aliases for. A
// tool_result payload is machine-generated output and is cloaked on both
// surfaces. The structural declaration is still cloaked.
func TestIssue51_TypedUserProseIsUntouched(t *testing.T) {
	isolateOMPMeasurement(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))
	reqID := "req_issue51_user"

	const typed = "Write the literal ToolSearch into out.txt"
	const reminder = "Prefer ToolSearch when hunting for files."
	body := `{"system":"x","messages":[{"role":"user","content":[
		{"type":"text","text":"` + typed + `\n<system-reminder>` + reminder + `</system-reminder>"},
		{"type":"tool_result","tool_use_id":"toolu_1","content":"ToolSearch returned out.txt"}
	],"tool_choice":{"type":"tool","name":"Write"}}],
	"tools":[{"name":"ToolSearch","description":"d","input_schema":{"type":"object"}},
	         {"name":"Write","description":"d","input_schema":{"type":"object"}}]}`
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/model", []byte(body), h))
	if code != 0 || strings.Contains(string(raw), `"StatusCode":503`) {
		t.Fatalf("admission rejected: %d %s", code, raw)
	}
	forward := string(decodeEnvelopeBody(t, raw))
	if forward == "" {
		t.Fatalf("empty forward envelope: %s", raw)
	}

	// 1. The user's own bytes survive exactly, including a tool name typed
	//    deliberately as a literal. This is the regression.
	if !strings.Contains(forward, typed) {
		t.Fatalf("typed user prose was rewritten: %s", forward)
	}
	if !strings.Contains(forward, "out.txt") {
		t.Fatalf("the literal file name was rewritten: %s", forward)
	}

	// 2. Inside the client-generated span, BOTH surfaces apply.
	if !strings.Contains(forward, "wp_find_tools") {
		t.Fatalf("neither the declaration nor the reminder span was aliased: %s", forward)
	}
	if strings.Contains(forward, reminder) {
		t.Fatalf("tool name inside the machine-generated reminder span was not cloaked: %s", forward)
	}

	// 3. Structural declaration cloaking is untouched by any of the above:
	//    the declared ToolSearch became its request-scoped alias, and Write
	//    became its proven AGY target.
	if strings.Contains(forward, `"name":"ToolSearch"`) {
		t.Fatalf("declared tool name was not cloaked: %s", forward)
	}
	if !strings.Contains(forward, `"name":"write_to_file"`) {
		t.Fatalf("core tool declaration was not cloaked: %s", forward)
	}

	// 4. A tool_result payload is machine-generated, so its alias names go
	//    upstream with the rest.
	if strings.Contains(forward, "ToolSearch returned out.txt") {
		t.Fatalf("tool_result payload was not cloaked: %s", forward)
	}
}

// The coordinator's exact probe, end to end: the user names ToolSearch as a
// literal to be written into a file, the model runs the declared Write tool,
// and the file content the client ends up holding is still "ToolSearch" while
// the tool identity comes back as Write.
func TestIssue51_UserTypedToolNameSurvivesWriteRoundTrip(t *testing.T) {
	isolateOMPMeasurement(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))
	reqID := "req_issue51_write"

	orig := `{"system":"x","messages":[{"role":"user","content":"Write the literal ToolSearch into out.txt"}],` +
		`"tools":[{"name":"ToolSearch","description":"d","input_schema":{"type":"object"}},` +
		`{"name":"Write","description":"d","input_schema":{"type":"object"}}]}`
	h := http.Header{}
	h.Set("X-Cloak-Client", "claude_code")
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/model", []byte(orig), h))
	if code != 0 || strings.Contains(string(raw), `"StatusCode":503`) {
		t.Fatalf("admission rejected: %d %s", code, raw)
	}
	forward := string(decodeEnvelopeBody(t, raw))
	if !strings.Contains(forward, "Write the literal ToolSearch into out.txt") {
		t.Fatalf("user turn was rewritten upstream: %s", forward)
	}
	if !strings.Contains(forward, "wp_find_tools") {
		t.Fatalf("declaration was not aliased: %s", forward)
	}
	// Write's proven AGY target is what the model is told to call.
	writeAlias := ""
	var parsed map[string]any
	if err := json.Unmarshal(decodeEnvelopeBody(t, raw), &parsed); err == nil {
		if tools, ok := parsed["tools"].([]any); ok {
			for _, tr := range tools {
				if tm, ok := tr.(map[string]any); ok {
					if tm["name"] == "write_to_file" {
						writeAlias = "write_to_file"
					}
				}
			}
		}
	}
	if writeAlias == "" {
		t.Fatalf("Write did not resolve to its AGY target: %s", forward)
	}

	// The model calls the aliased tool with the user's literal file content.
	reply := `{"content":[{"type":"tool_use","id":"toolu_1","name":"` + writeAlias + `",` +
		`"input":{"file_path":"out.txt","content":"ToolSearch"}}]}`
	back := reverseBrandPass(t, reqID, orig, reply, "anthropic", "agy/model")
	if back == "" {
		t.Fatal("response reverse produced no body")
	}
	// The file content is the user's own bytes and must survive untouched.
	if !strings.Contains(back, `"ToolSearch"`) {
		t.Fatalf("file content was rewritten: %s", back)
	}
	// Tool identity is exact-uncloak authority: write_to_file came from the
	// client's own Write and goes back to Write.
	if !strings.Contains(back, `"name":"Write"`) {
		t.Fatalf("tool identity did not round-trip: %s", back)
	}
	if strings.Contains(back, "write_to_file") {
		t.Fatalf("the alias leaked to the client: %s", back)
	}
}
