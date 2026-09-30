package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Ticket #51: the protocol/carrier parity gate. OpenAI-compatible and
// Anthropic Messages carry the same logical content in different JSON, so the
// hard surfaces — tool names, tool-call arguments, operational paths and
// URLs — have to behave identically. Brand prose on the response side is
// explicitly out of scope.

// declaredToolsFor returns one declaration per logical tool name, the same
// surface in both carriers: OpenAI nests it under function, Anthropic does not.
func declaredToolsFor(format string, names ...string) string {
	tools := make([]any, 0, len(names))
	for _, n := range names {
		if format == "openai" {
			tools = append(tools, map[string]any{
				"type":     "function",
				"function": map[string]any{"name": n, "description": "tool " + n, "parameters": map[string]any{"type": "object"}},
			})
			continue
		}
		tools = append(tools, map[string]any{
			"name": n, "description": "tool " + n, "input_schema": map[string]any{"type": "object"},
		})
	}
	root := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "go"}}, "tools": tools}
	raw, _ := json.Marshal(root)
	return string(raw)
}

// TestIssue51_AliasTablesAreEquivalentAcrossCarriers asserts the request the
// model sees names the same aliases whichever carrier delivered them.
func TestIssue51_AliasTablesAreEquivalentAcrossCarriers(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	for _, client := range []string{"claude_code", "codex"} {
		names := []string{"Bash", "Read", "Edit"}
		if client == "codex" {
			names = []string{"exec", "web_search", "request_user_input"}
		}
		seen := map[string]string{}
		for _, format := range []string{"openai", "anthropic"} {
			toolName := "shared/probe-" + client + "-" + format
			body := declaredToolsFor(format, names...)
			raw, code := handlePluginCall("request.intercept_before",
				makeIntegrationRequestInterceptPayload(t, toolName, format, "agy/probe", []byte(body)))
			if code != 0 {
				t.Fatalf("%s/%s code=%d envelope=%s", client, format, code, raw)
			}
			forward := string(decodeEnvelopeBody(t, raw))
			for _, n := range names {
				if strings.Contains(forward, `"name":"`+n+`"`) || strings.Contains(forward, `"name": "`+n+`"`) {
					t.Fatalf("%s/%s leaked the source tool name %q: %s", client, format, n, forward)
				}
			}
			seen[format] = strings.Join(declaredToolNames([]byte(forward), format), ",")
		}
		if seen["openai"] != seen["anthropic"] {
			t.Fatalf("%s: alias tables differ across carriers: openai=%v anthropic=%v", client, seen["openai"], seen["anthropic"])
		}
		if len(strings.Split(seen["openai"], ",")) != len(names) {
			t.Fatalf("%s: alias table is not a bijection: %v", client, seen["openai"])
		}
	}
}

// TestIssue51_StreamToolArgumentsRestoreLikeNonStream is the carrier parity
// check for operational identifiers. OpenAI streams them as
// choices[].delta.tool_calls[].function.arguments, Anthropic as partial_json,
// and both must give the client its own spelling back, with or without the
// fragmented carriage.
func TestIssue51_StreamToolArgumentsRestoreLikeNonStream(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	const wire = `{\"path\":\"~/.gemini/rules/style.md\",\"url\":\"https://antigravity.google/docs\"}`

	for _, format := range []string{"openai", "anthropic"} {
		mgr := newStreamSessionManager()
		reqID := "issue51-args-" + format
		mgr.resetSession("req:"+reqID, "claude_code", ompUncloakCache(t))

		// Split inside the path, so the carry has to survive the fragment.
		first, second := wire[:20], wire[20:]
		frames := []string{
			streamToolArgumentEvent(format, 0, 0, first),
			streamToolArgumentEvent(format, 0, 0, second),
		}
		var delivered strings.Builder
		for i, body := range frames {
			resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
				RequestID: reqID, SourceFormat: format, ChunkIndex: i, Body: []byte(body),
			}, format)
			delivered.WriteString(streamedToolArgumentText(t, format, resp.Body))
		}
		got := delivered.String()
		if strings.Contains(got, ".gemini") || strings.Contains(got, "antigravity.google") {
			t.Fatalf("%s: streamed arguments kept cloaked text: %q", format, got)
		}
		if !strings.Contains(got, "~/.claude/rules/style.md") {
			t.Fatalf("%s: streamed arguments lost the path: %q", format, got)
		}
		if !strings.Contains(got, "https://claude.ai/docs") {
			t.Fatalf("%s: streamed arguments lost the URL: %q", format, got)
		}
	}
}

// streamToolArgumentEvent builds one streaming event carrying a fragment of a
// tool call's arguments, in whichever carrier the format uses.
func streamToolArgumentEvent(format string, choice, call int, fragment string) string {
	if format == "anthropic" {
		return "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"` + fragment + `"}}` + "\n\n"
	}
	return "data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":` + jsonNumber(call) +
		`,"function":{"arguments":"` + fragment + `"}}]}}]}` + "\n\n"
}

func jsonNumber(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// streamedToolArgumentText joins the argument fragments a response delivered.
func streamedToolArgumentText(t *testing.T, format string, body []byte) string {
	t.Helper()
	var out strings.Builder
	if format == "anthropic" {
		for _, f := range sseFrames(t, body) {
			delta, _ := f.data["delta"].(map[string]any)
			s, _ := delta["partial_json"].(string)
			out.WriteString(s)
		}
		return out.String()
	}
	for _, f := range sseFrames(t, body) {
		choices, _ := f.data["choices"].([]any)
		for _, cRaw := range choices {
			ch, _ := cRaw.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			for _, tRaw := range sliceOfMaps(delta["tool_calls"]) {
				fn, _ := tRaw["function"].(map[string]any)
				s, _ := fn["arguments"].(string)
				out.WriteString(s)
			}
		}
	}
	return out.String()
}

// TestIssue51_OMPMachineGeneratedProseUsesRequestScopedAliases is the OMP half
// of the contract: a machine-generated system block and a tool description that
// name a declared OMP tool must carry that request's alias, including for the
// shared and fallback tools that have no canonical mapping.
func TestIssue51_OMPMachineGeneratedProseUsesRequestScopedAliases(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	isolateOMPMeasurement(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	// hub is not one of the nine canonical tools, so it is admitted through the
	// shared/fallback alias plan for this request only.
	const req = `{
		"system":"You are Antigravity. Use the bash tool for shell work, and the hub tool to list jobs. A bare hub mention stays prose.",
		"messages":[{"role":"user","content":"go"}],
		"tools":[
			{"name":"bash","description":"Run a shell command.","input_schema":{"type":"object"}},
			{"name":"hub","description":"List jobs; do not use read for this.","input_schema":{"type":"object"}}
		]
	}`
	h := map[string][]string{"X-Cloak-Client": {"oh_my_pi"}}
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue51-omp", "anthropic", "agy/omp-probe", []byte(req), h))
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	forward := string(decodeEnvelopeBody(t, raw))
	// A declared tool named in tool-reference context is cloaked in the system
	// block, the description surface and the declaration itself.
	for _, leak := range []string{`"hub"`, "use read for this", "the hub tool to list", "the bash tool"} {
		if strings.Contains(forward, leak) {
			t.Fatalf("OMP source identity leaked into machine-generated prose (%q): %s", leak, forward)
		}
	}
	// The canonical tool and the shared tool both reach the model under an alias.
	if !strings.Contains(forward, "run_command") {
		t.Fatalf("canonical OMP tool was not aliased: %s", forward)
	}
	alias := sharedAliasesFor("oh_my_pi")["hub"]
	if alias == "" {
		alias = fallbackAliasForSource("hub")
	}
	if alias == "" || !strings.Contains(forward, alias) {
		t.Fatalf("shared OMP tool alias %q missing from the request: %s", alias, forward)
	}
	// A bare mention is prose, not a tool reference, and the ambiguous tier
	// leaves it alone. That is the shared isUnambiguousToolName policy every
	// client runs, not an Oh My Pi gap: rewriting bare English words in a large
	// system prompt is the failure the policy exists to prevent.
	if !strings.Contains(forward, "A bare hub mention stays prose.") {
		t.Fatalf("bare prose was rewritten, which the shared policy forbids: %s", forward)
	}
	// The same alias must come back on the way home, exactly.
	plan := globalLifecycleManager.getRoute("issue51-omp")
	if plan == nil {
		t.Fatal("no route state pinned for the protected request")
	}
	if got := plan.activeReverse[alias]; got != "hub" {
		t.Fatalf("alias %q does not reverse to hub: %v", alias, plan.activeReverse)
	}
}

// TestIssue51_NonStreamToolArgumentsMatchStream is the other half of the
// carrier parity check: a non-stream response carries the arguments in a
// finished JSON string instead of a fragment, and the client must get the same
// spelling back in both.
func TestIssue51_NonStreamToolArgumentsMatchStream(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	const cloakedArgs = `{\"path\":\"~/.gemini/rules/style.md\",\"url\":\"https://antigravity.google/docs\"}`
	for _, format := range []string{"openai", "anthropic"} {
		var respBody string
		if format == "openai" {
			respBody = `{"choices":[{"message":{"content":"done","tool_calls":[{"function":{"name":"view_file","arguments":"` +
				jsonEscape(t, cloakedArgs) + `"}}]}}]}`
		} else {
			respBody = `{"content":[{"type":"text","text":"done"},{"type":"tool_use","id":"1","name":"view_file","input":{"path":"~/.gemini/rules/style.md","url":"https://antigravity.google/docs"}}]}`
		}
		reqID := "issue51-nonstream-" + format
		reqBody := declaredToolsFor(format, "Read", "Bash")
		// Pin the client explicitly: the same operational text resolves to a
		// different spelling per client, which is the point of the gate.
		h := http.Header{}
		h.Set("X-Cloak-Client", "claude_code")
		handlePluginCall("request.intercept_before",
			makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, format, "agy/model", []byte(reqBody), h))
		payload := responseInterceptRequestJSON(t, reqBody, respBody, format)
		var reqMap map[string]any
		if err := json.Unmarshal(payload, &reqMap); err != nil {
			t.Fatal(err)
		}
		reqMap["RequestID"] = reqID
		reqMap["Model"] = "agy/model"
		payload, _ = json.Marshal(reqMap)
		raw, _ := handlePluginCall("response.intercept_after", payload)
		out := string(decodeBody(t, raw))
		if strings.Contains(out, ".gemini") || strings.Contains(out, "antigravity.google") {
			t.Fatalf("%s: non-stream response kept cloaked operational text: %s", format, out)
		}
		if !strings.Contains(out, "~/.claude/rules/style.md") || !strings.Contains(out, "https://claude.ai/docs") {
			t.Fatalf("%s: non-stream response lost the client spelling: %s", format, out)
		}
	}
}

// jsonEscape encodes a raw JSON string as a JSON string value.
func jsonEscape(t *testing.T, raw string) string {
	t.Helper()
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(out[1 : len(out)-1])
}

// TestIssue51_HeldToolArgumentFlushesIntoTheArgumentCarrier is the regression
// for the carrier mix-up: a hold inside a streamed tool call's arguments was
// flushed through the PROSE carrier, so the recovered bytes appeared in the
// assistant message while the call's arguments stayed truncated.
func TestIssue51_HeldToolArgumentFlushesIntoTheArgumentCarrier(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	t.Run("openai", func(t *testing.T) {
		mgr := newStreamSessionManager()
		mgr.resetSession("req:issue51-hold-openai", "claude_code", ompUncloakCache(t))
		// The tail of the path is a live prefix, so it is held.
		resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: "issue51-hold-openai", SourceFormat: "openai", ChunkIndex: 0,
			Body: []byte(streamToolArgumentEvent("openai", 0, 0, `{\"path\":\"~/.gem`)),
		}, "openai")
		_ = resp
		done := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: "issue51-hold-openai", SourceFormat: "openai", ChunkIndex: 1,
			Body: []byte("data: [DONE]\n\n"),
		}, "openai")
		body := string(done.Body)
		if strings.Contains(body, `"content"`) {
			t.Fatalf("held argument bytes were flushed as assistant content: %s", body)
		}
		// The recovered bytes are the held partial itself: the model was cut off
		// mid-token, so ".gem" is the most that can be handed back.
		if !strings.Contains(body, `"arguments":".gem"`) {
			t.Fatalf("held argument bytes were not flushed into the tool call: %s", body)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		mgr := newStreamSessionManager()
		mgr.resetSession("req:issue51-hold-anthropic", "claude_code", ompUncloakCache(t))
		mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: "issue51-hold-anthropic", SourceFormat: "anthropic", ChunkIndex: 0,
			Body: []byte(streamToolArgumentEvent("anthropic", 0, 0, `{\"path\":\"~/.gem`)),
		}, "anthropic")
		done := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: "issue51-hold-anthropic", SourceFormat: "anthropic", ChunkIndex: 1,
			Body: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		}, "anthropic")
		var sawArgs, sawText bool
		for _, f := range sseFrames(t, done.Body) {
			delta, _ := f.data["delta"].(map[string]any)
			if delta["type"] == "input_json_delta" {
				sawArgs = true
			}
			if delta["type"] == "text_delta" {
				sawText = true
			}
		}
		if sawText {
			t.Fatalf("held argument bytes were flushed as assistant text: %s", done.Body)
		}
		if !sawArgs {
			t.Fatalf("held argument bytes were dropped instead of flushed: %s", done.Body)
		}
	})
}
