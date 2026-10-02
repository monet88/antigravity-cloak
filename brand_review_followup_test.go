package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Regressions for the brand/path/stream review findings on PR #46. Each test
// names the behaviour it pins, and each one fails on the code it replaces:
// the escaped Windows file rules, the whole-segment boundary, the URL-scheme
// scan, the exclusion that replaced the fake "omp.google"/"Codex.google"
// repair, the per-client identity prose, and the standalone choice counter.

// TestEscapedWindowsAbsolutePathRestoresExactlyOnEveryCarrier pins requirement
// 1: a path inside a tool call's arguments arrives as JSON text, where every
// backslash is written twice, and both carriers must hand the client exactly
// its own path back - not the mixed ".claude\\GEMINI.md" the plain directory
// rule produced when it claimed the directory and left the file name behind.
func TestEscapedWindowsAbsolutePathRestoresExactlyOnEveryCarrier(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, tc := range []struct {
		name    string
		client  string
		arg     string
		want    string
		mustNot string
	}{
		{
			// claude_code renames the file as well as the directory.
			name: "claude_code global memory", client: "claude_code",
			arg:  `{"path":"C:\\Users\\dev\\.gemini\\GEMINI.md"}`,
			want: `{"path":"C:\\Users\\dev\\.claude\\CLAUDE.md"}`,
		},
		{
			// Oh My Pi's global Claude memory keeps the same file pair as the
			// protected forward mapping produces; a normal .omp path ("agent")
			// is covered by TestIssue49_ReversePathSegmentPerClient.
			name: "oh_my_pi global memory", client: "oh_my_pi",
			arg:  `{"path":"C:\\Users\\dev\\.gemini\\AGENTS.md"}`,
			want: `{"path":"C:\\Users\\dev\\.claude\\CLAUDE.md"}`,
		},
		{
			name: "oh_my_pi home directory stays .omp", client: "oh_my_pi",
			arg:  `{"path":"C:\\Users\\dev\\.gemini\\agent\\AGENTS.md"}`,
			want: `{"path":"C:\\Users\\dev\\.omp\\agent\\AGENTS.md"}`,
		},
	} {
		t.Run(tc.name+"/stream", func(t *testing.T) {
			for _, format := range []string{"openai", "anthropic"} {
				mgr := newStreamSessionManager()
				reqID := "escaped-" + format + "-" + tc.client
				mgr.resetSession("req:"+reqID, tc.client, ompUncloakCache(t))

				wire := jsonEscape(t, tc.arg)
				// Split inside the file name, so the carry has to survive the
				// fragment and the file rule has to be reachable from the lane.
				split := strings.Index(wire, ".md")
				if split < 0 {
					t.Fatalf("fixture lost its file name: %q", wire)
				}
				frames := []string{
					streamToolArgumentEvent(format, 0, 0, wire[:split]),
					streamToolArgumentEvent(format, 0, 0, wire[split:]),
				}
				if format == "anthropic" {
					frames = append(frames,
						"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
						"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					frames = append(frames, "data: [DONE]\n\n")
				}

				var delivered strings.Builder
				for _, out := range driveStreamFrames(t, mgr, reqID, format, frames) {
					delivered.WriteString(streamedToolArgumentText(t, format, []byte(out)))
				}
				got := delivered.String()
				if got != tc.want {
					t.Fatalf("%s streamed arguments = %q, want %q", format, got, tc.want)
				}
				if strings.Contains(got, ".gemini") || strings.Contains(got, "GEMINI.md") {
					t.Fatalf("%s leaked the cloaked spelling: %q", format, got)
				}
			}
		})
	}

	// Non-stream: the OpenAI carrier is the string-valued function.arguments
	// (escaped JSON text), the Anthropic carrier is tool_use.input, which the
	// body walker has already decoded.
	t.Run("non-stream openai", func(t *testing.T) {
		const reqID = "escaped-nonstream-openai"
		// The request half only has to register the session (and rewrite
		// something, or there is no body to hand back); the carrier under test
		// is in the response.
		req := `{"system":"cloak me: Claude","messages":[{"role":"user","content":"go"}]}`
		forwardCloakedBody(t, reqID, req)
		resp := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":` +
			strconv.Quote(`{"path":"C:\\Users\\dev\\.gemini\\GEMINI.md"}`) + `}}]}}]}`
		back := reverseBrandPass(t, reqID, req, resp, "openai", "agy/claude-test")
		if got := openAIToolArguments(t, back); got != `{"path":"C:\\Users\\dev\\.claude\\CLAUDE.md"}` {
			t.Fatalf("openai arguments = %q, want the client's own path", got)
		}
	})
	t.Run("non-stream anthropic", func(t *testing.T) {
		const reqID = "escaped-nonstream-anthropic"
		req := `{"system":"cloak me: Claude","messages":[{"role":"user","content":"go"}]}`
		forwardCloakedBody(t, reqID, req)
		resp := `{"content":[{"type":"tool_use","name":"run_command","input":{"path":"C:\\Users\\dev\\.gemini\\GEMINI.md"}}]}`
		back := reverseBrandPass(t, reqID, req, resp, "anthropic", "agy/claude-test")
		if got := anthropicToolInputPath(t, back); got != `C:\Users\dev\.claude\CLAUDE.md` {
			t.Fatalf("anthropic tool_use input = %q, want the client's own path", got)
		}
	})

	// The forward pass produces the escaped spelling too, so the pair really is
	// symmetric in the carrier that survives as JSON text.
	t.Run("forward escaped", func(t *testing.T) {
		const in = `read C:\Users\u\.claude\CLAUDE.md now`
		body, changed, _ := rewriteRequestBodyWithClient(
			[]byte(`{"system":`+strconv.Quote(in)+`}`), "openai", "claude_code")
		if !changed {
			t.Fatal("forward pass made no change")
		}
		const want = `read C:\Users\u\.gemini\GEMINI.md now`
		if got := systemText(t, body); got != want {
			t.Fatalf("forward = %q, want %q", got, want)
		}
	})
}

// TestRedirectedChoiceCompletionCountsRootChoices is requirement 2: a tool-call
// argument lane is a child of its choice, never a choice of its own, so it must
// not let n=2 look complete before choice 1 has streamed anything. The old
// counter compared the number of lanes with the expected choice count, so
// choice 0's two lanes satisfied n=2 and the session was freed - losing
// whatever choice 1 said next.
func TestRedirectedChoiceCompletionCountsRootChoices(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	const reqID = "n2-root-choices"
	const key = "req:" + reqID
	mgr.resetSession(key, "claude_code", ompUncloakCache(t), 2)

	chunk := func(body string) pluginapi.StreamChunkInterceptResponse {
		return mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: "openai", Body: []byte(body),
		}, "openai")
	}

	chunk(`{"choices":[{"index":0,"delta":{"content":"hello"}}]}`)
	chunk(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"/tmp/x\"}"}}]}}]}`)

	mgr.mu.Lock()
	sess := mgr.sessions[key]
	childLane := false
	if sess != nil {
		_, childLane = sess.brandCarries["openai:0"+reverseBrandLaneSuffix+"tool0"]
	}
	mgr.mu.Unlock()
	if sess == nil {
		t.Fatal("session missing before any finish_reason")
	}
	if !childLane {
		t.Fatal("fixture did not open a child tool-argument lane under choice 0")
	}

	// Choice 0 finishes. One of the two expected choices has been seen, so the
	// session must survive even though it already holds two lanes.
	chunk(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	mgr.mu.Lock()
	_, alive := mgr.sessions[key]
	mgr.mu.Unlock()
	if !alive {
		t.Fatal("session freed after choice 0, before choice 1 streamed anything")
	}

	// Choice 1 streams a held reverse token and finishes: only now is the
	// stream done, and only now may the session be freed and the carry flushed.
	chunk(`{"choices":[{"index":1,"delta":{"content":"reply Anti"}}]}`)
	final := chunk(`{"choices":[{"index":1,"delta":{"content":"gravity"},"finish_reason":"stop"}]}`)
	mgr.mu.Lock()
	_, stillAlive := mgr.sessions[key]
	mgr.mu.Unlock()
	if stillAlive {
		t.Fatal("session kept after every expected choice finished")
	}
	// Choice 1's held "Anti" + "gravity" is a cloaked token, so the finish chunk
	// must carry its reversal into choice 1's own content.
	if !strings.Contains(string(final.Body), `"content":"Claude"`) {
		t.Fatalf("choice 1 carry was not flushed into its chunk: %s", final.Body)
	}
	if strings.Contains(string(final.Body), "Antigravity") {
		t.Fatalf("cloaked token reached the client: %s", final.Body)
	}
}

// TestNativeDomainSurvivesFragmentedReverse pins requirement 6's domain rule
// with the split that a post-hoc repair rule cannot survive: the host arrives
// as "antigravity.oo" + "gle". Resolving the bare brand word as soon as its
// right boundary is provable emits "omp"/"Codex" and leaves the second half
// unrecoverable, which is exactly what the deleted "omp.google" /
// "Codex.google" repair could not undo.
func TestNativeDomainSurvivesFragmentedReverse(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, client := range []string{"oh_my_pi", "codex"} {
		t.Run(client, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "domain-" + client
			mgr.resetSession("req:"+reqID, client, nil)

			var delivered strings.Builder
			collect := func(resp pluginapi.StreamChunkInterceptResponse) {
				for _, f := range sseFrames(t, resp.Body) {
					delta, _ := f.data["delta"].(map[string]any)
					s, _ := delta["text"].(string)
					delivered.WriteString(s)
				}
			}
			frames := []string{
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"docs at antigravity.go\"}}\n\n",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ogle/readme\"}}\n\n",
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			}
			for i, body := range frames {
				collect(mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
					RequestID: reqID, SourceFormat: "anthropic", ChunkIndex: i, Body: []byte(body),
				}, "anthropic"))
			}
			got := delivered.String()
			if got != "docs at antigravity.google/readme" {
				t.Fatalf("fragmented host = %q, want it preserved byte-for-byte", got)
			}
			if strings.Contains(got, "omp.") || strings.Contains(got, "Codex.") {
				t.Fatalf("client was handed an invented host: %q", got)
			}
		})
	}
}

// TestURLSchemeContextPreservesBareBrand is requirement 4: the scheme scan
// tested the bytes BEFORE the colon for slashes, so "https://" never matched
// and a host-like token whose brand is not glued to a slash was classified as
// prose and rewritten.
func TestURLSchemeContextPreservesBareBrand(t *testing.T) {
	const value = "docs at https://www.omp.ai/mcp today"
	idx := strings.Index(value, "omp")
	if idx < 0 {
		t.Fatal("fixture lost its host")
	}
	if !inURLPathContext(value, idx, idx+len("omp")) {
		t.Fatalf("inURLPathContext(%q, %d) = false: the scheme was not recognised", value, idx)
	}

	rule := rewriteMapping{Match: "omp", Replacement: "Antigravity"}
	got, changed := replaceInsensitiveRule(value, rule, true)
	if changed || got != value {
		t.Fatalf("bare brand inside a host name was rewritten: %q -> %q", value, got)
	}
	// Control: the same word in ordinary prose is still masked, with the casing
	// it was written in.
	prose, changed := replaceInsensitiveRule("run the omp binary", rule, true)
	if !changed || prose != "run the antigravity binary" {
		t.Fatalf("prose brand = %q (changed=%v), want it masked", prose, changed)
	}
}

// TestCodexIdentityVendorProseAndCasing is requirement 5. The client's opening
// sentence is rewritten whole onto the native identity, the residual prose
// round-trips pair by pair, and the bare client name keeps the casing it was
// written in through one rule instead of three that shadow each other.
func TestCodexIdentityVendorProseAndCasing(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	forced := func(in string) string {
		t.Helper()
		body, changed, _ := rewriteRequestBodyWithClient(
			[]byte(`{"system":`+strconv.Quote(in)+`}`), "openai", "codex")
		if !changed {
			return in
		}
		return systemText(t, body)
	}

	// The real opening sentence, verbatim from codex_client_models.json, with
	// the rest of its prose after it.
	const identity = codexIdentityLine + " You and the user share one workspace."
	want := antigravityIdentity + " You and the user share one workspace."
	if got := forced(identity); got != want {
		t.Fatalf("identity line = %q, want %q", got, want)
	}

	// Longer identity prose keeps its residual vendor vocabulary, and every
	// token the forward pass introduced comes back.
	//
	// The whole identity line is a terminal rewrite, like the Claude Code and
	// Oh My Pi ones: it is what the native prompt says, so it is not inverted
	// sentence-for-sentence. What must round-trip is the residual vocabulary,
	// which is what a response can actually echo back.
	const residual = "based on GPT-6, built by OpenAI, shipped as Codex"
	const residualForward = "based on Gemini 3, built by Google Deepmind, shipped as Antigravity"
	if got := forced(residual); got != residualForward {
		t.Fatalf("residual prose = %q, want %q", got, residualForward)
	}
	if back := applyReverseTable(residualForward, "codex"); back != residual {
		t.Fatalf("residual prose did not round-trip: %q", back)
	}
	// The identity sentence itself is rewritten whole.
	if got := forced(codexIdentityLine); got != antigravityIdentity {
		t.Fatalf("identity sentence = %q, want %q", got, antigravityIdentity)
	}

	// One bare rule, three spellings, casing preserved on both sides.
	casing := "run codex via Codex, or CODEX for short"
	casingWant := "run antigravity via Antigravity, or ANTIGRAVITY for short"
	if got := forced(casing); got != casingWant {
		t.Fatalf("casing = %q, want %q", got, casingWant)
	}
	for _, tc := range []struct{ cloaked, original string }{
		{"antigravity", "codex"},
		{"Antigravity", "Codex"},
		{"ANTIGRAVITY", "CODEX"},
	} {
		if got := applyReverseTable(tc.cloaked, "codex"); got != tc.original {
			t.Errorf("reverse %q = %q, want %q", tc.cloaked, got, tc.original)
		}
	}

	// The native host is not a Codex host: it is handed back untouched, with no
	// invented "Codex.google" source domain in the table.
	if got := applyReverseTable("https://antigravity.google/docs", "codex"); got != "https://antigravity.google/docs" {
		t.Fatalf("codex rewrote a domain it never introduced: %q", got)
	}

	// The routing field is operational, never prose: the model name is not
	// rewritten even though the same words in the prompt are.
	body, changed, _ := rewriteRequestBodyWithClient(
		[]byte(`{"model":"agy/gpt-6-codex","system":`+strconv.Quote(codexIdentityLine)+`}`), "openai", "codex")
	if !changed {
		t.Fatal("forward pass made no change")
	}
	var root map[string]any
	if err := safeUnmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := root["model"].(string); got != "agy/gpt-6-codex" {
		t.Fatalf("model routing field was rewritten: %q", got)
	}
}

// TestOMPIdentitySchemeAndGlobalMemory is requirement 6: the shipped identity
// sentence, the real "omp://" internal URIs byte-for-byte, and the accepted
// global Claude-memory pair including its escaped Windows spelling.
func TestOMPIdentitySchemeAndGlobalMemory(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	const uri = "omp://xd/mcp__exa_web_search_exa"
	got, changed := rewriteProtectedBrandText(
		"owner: "+uri+" and the omp binary", activeFilterConfig(), "oh_my_pi")
	if !changed {
		t.Fatal("protected walker made no change")
	}
	if !strings.Contains(got, uri) {
		t.Fatalf("omp:// URI was rewritten: %q", got)
	}
	if !strings.Contains(got, "the Antigravity binary") {
		t.Fatalf("bare alias in prose was not masked: %q", got)
	}

	// The alias rule in the plain OMP table carries the same exclusion.
	for _, m := range effectiveMappings(activeFilterConfig(), "oh_my_pi") {
		if m.Match != "omp" {
			continue
		}
		if next, changed := replaceInsensitiveRule("see "+uri, m, true); changed || next != "see "+uri {
			t.Fatalf("table rule rewrote the scheme: %q (changed=%v)", next, changed)
		}
	}

	// The shipped identity sentence, whole.
	if got, _ := rewriteProtectedBrandText("You are omp's trusted coding assistant.", activeFilterConfig(), "oh_my_pi"); got != antigravityIdentity {
		t.Fatalf("identity sentence = %q, want %q", got, antigravityIdentity)
	}

	// End to end on the protected route: the URI survives the real request
	// path, not just the text walker.
	t.Run("protected request", func(t *testing.T) {
		isolateOMPMeasurement(t)
		body := []byte(`{"system":"owner: ` + uri + ` and the omp binary",` +
			`"messages":[{"role":"user","content":"go"}],` +
			`"tools":[{"name":"bash","input_schema":{"type":"object"}}]}`)
		res := admitOMPMeasurement(t, "req:issue-omp-uri", "anthropic", body)
		var root map[string]any
		if err := safeUnmarshal(res.Body, &root); err != nil {
			t.Fatalf("unmarshal rewritten body: %v", err)
		}
		sys, _ := root["system"].(string)
		if !strings.Contains(sys, uri) {
			t.Fatalf("omp:// URI was rewritten on the protected route: %q", sys)
		}
		if !strings.Contains(sys, "the Antigravity binary") {
			t.Fatalf("bare alias in prose was not masked: %q", sys)
		}
	})

	// The global Claude memory pair, plain and JSON-escaped.
	for _, tc := range []struct{ in, want string }{
		{"~/.claude/CLAUDE.md", "~/.gemini/AGENTS.md"},
		{`C:\Users\monet\.claude\CLAUDE.md`, `C:\Users\monet\.gemini\AGENTS.md`},
		{`C:\\Users\\monet\\.claude\\CLAUDE.md`, `C:\\Users\\monet\\.gemini\\AGENTS.md`},
	} {
		mid := applyTable(tc.in, ompBrandMappings)
		if mid != tc.want {
			t.Errorf("forward %q = %q, want %q", tc.in, mid, tc.want)
			continue
		}
		if back := applyTable(mid, ompProtectedReverseTable); back != tc.in {
			t.Errorf("reverse %q = %q, want %q", mid, back, tc.in)
		}
	}
}

// openAIToolArguments decodes the first tool call's argument string out of an
// OpenAI-shaped response body, message or delta alike.
func openAIToolArguments(t *testing.T, body string) string {
	t.Helper()
	var root map[string]any
	if err := safeUnmarshal([]byte(body), &root); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, body)
	}
	choices, _ := root["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("response has no choices: %s", body)
	}
	choice, _ := choices[0].(map[string]any)
	holder, _ := choice["message"].(map[string]any)
	if holder == nil {
		holder, _ = choice["delta"].(map[string]any)
	}
	for _, call := range sliceOfMaps(holder["tool_calls"]) {
		fn, _ := call["function"].(map[string]any)
		if args, ok := fn["arguments"].(string); ok {
			return args
		}
	}
	t.Fatalf("response has no tool-call arguments: %s", body)
	return ""
}

// anthropicToolInputPath decodes the path value of the first tool_use block.
func anthropicToolInputPath(t *testing.T, body string) string {
	t.Helper()
	var root map[string]any
	if err := safeUnmarshal([]byte(body), &root); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, body)
	}
	for _, block := range sliceOfMaps(root["content"]) {
		if block["type"] != "tool_use" {
			continue
		}
		input, _ := block["input"].(map[string]any)
		if path, ok := input["path"].(string); ok {
			return path
		}
	}
	t.Fatalf("response has no tool_use path: %s", body)
	return ""
}

// TestBareGeminiReverseRequiresSegmentEnd is the P2 boundary finding: the bare
// ".gemini" -> ".omp" rule is the end-of-string inverse of the forward
// dot-segment remap, but WholeSegment only enforces the LEFT boundary. The
// generic right boundary accepts any non-word byte, so ".gemini-backup" and
// ".gemini.foo" came back as ".omp-backup" and ".omp.foo" - spellings the
// forward pass deliberately preserves and never produces.
func TestBareGeminiReverseRequiresSegmentEnd(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, tc := range []struct {
		name, in, want string
	}{
		// Lookalikes the forward pass never remapped: handed back verbatim.
		{"hyphen suffix", `C:\Users\u\.gemini-backup\agent`, `C:\Users\u\.gemini-backup\agent`},
		{"dot suffix", "~/.gemini.foo/notes", "~/.gemini.foo/notes"},
		{"word suffix", "~/.gemini2/notes", "~/.gemini2/notes"},
		// The real end-of-segment forms still invert.
		{"bare end of string", "persist to ~/.gemini", "persist to ~/.omp"},
		{"trailing separator", `C:\Users\u\.gemini\`, `C:\Users\u\.omp\`},
		{"directory", "~/.gemini/agent/AGENTS.md", "~/.omp/agent/AGENTS.md"},
		{"json-escaped separator", `C:\\Users\\u\\.gemini\\agent`, `C:\\Users\\u\\.omp\\agent`},
		// The global Claude memory file keeps its own, longer rule.
		{"global memory", "~/.gemini/AGENTS.md", "~/.claude/CLAUDE.md"},
	} {
		if got := applyReverseTable(tc.in, "oh_my_pi"); got != tc.want {
			t.Errorf("%s: reverse %q = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}

	// Forward symmetry, for the same two lookalikes: the forward pass leaves
	// them alone, which is exactly why the reverse may not touch them.
	for _, in := range []string{`C:\Users\u\.omp-backup\agent`, "~/.omp.foo/notes"} {
		if got, _ := rewriteProtectedBrandText(in, activeFilterConfig(), "oh_my_pi"); got != in {
			t.Errorf("forward remapped lookalike %q -> %q", in, got)
		}
	}

	// Chunk-split: a complete word-final match is held while its right boundary
	// is unproven, so the lookalike is decided after the next fragment arrives
	// instead of being resolved early and corrupting the path.
	t.Run("streamed", func(t *testing.T) {
		for _, tc := range []struct {
			name, first, second, want string
		}{
			{"lookalike split", "notes in ~/.gemini", "-backup/agent", "notes in ~/.gemini-backup/agent"},
			{"valid split", "notes in ~/.gemini", "/agent", "notes in ~/.omp/agent"},
			{"bare at end of stream", "persist to ~/.gemini", "", "persist to ~/.omp"},
		} {
			mgr := newStreamSessionManager()
			reqID := "segment-end-" + strings.ReplaceAll(tc.name, " ", "-")
			mgr.resetSession("req:"+reqID, "oh_my_pi", ompUncloakCache(t))
			frames := []string{
				`data: {"choices":[{"index":0,"delta":{"content":` + mustJSON(t, tc.first) + `}}]}` + "\n\n",
			}
			if tc.second != "" {
				frames = append(frames, `data: {"choices":[{"index":0,"delta":{"content":`+mustJSON(t, tc.second)+`}}]}`+"\n\n")
			}
			frames = append(frames, "data: [DONE]\n\n")
			var bodies [][]byte
			for _, out := range driveStreamFrames(t, mgr, reqID, "openai", frames) {
				bodies = append(bodies, []byte(out))
			}
			if got := sseAssistantJoined(t, bodies...); got != tc.want {
				t.Fatalf("%s: streamed content = %q, want %q", tc.name, got, tc.want)
			}
		}
	})
}

// forwardCloakedBodyFor runs the request intercept with an explicit client
// marker, so the response pass has a pinned authority for that client.
func forwardCloakedBodyFor(t *testing.T, client, reqID, format string, body []byte) string {
	t.Helper()
	h := http.Header{}
	h.Set("X-Cloak-Client", client)
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, format, "agy/claude-test", body, h))
	if code != 0 {
		t.Fatalf("client=%s code=%d envelope=%s", client, code, raw)
	}
	return string(decodeEnvelopeBody(t, raw))
}

// The payload a tool call carries in these tests: a shell command the user
// typed, whose directory name contains a brand word, plus an operational path
// the forward pass really produced.
const (
	typedCommand = `cd antigravity-cloak && go test ./...`
	argPayload   = `{"command":"` + typedCommand + `","path":".gemini/config.toml"}`
)

// TestToolArgumentsKeepProseRulesOut pins the per-carrier half of the reverse
// contract. `antigravity-cloak` is a word the model copies out of the user's
// own prompt, and the forward pass never writes a brand word into a tool
// argument, so reversing one there only corrupts data the client executes: the
// reverse pass handed back `cd Claude-cloak` and the client ran a directory
// that does not exist. Prose keeps the bare rule; arguments do not.
func TestToolArgumentsKeepProseRulesOut(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	// claude_code and codex both speak through a pinned alias plan, so the
	// request pass runs first with the client marker.
	for _, tc := range []struct {
		client, format, wantDir, wantProse string
	}{
		{client: "claude_code", format: "openai", wantDir: ".claude", wantProse: "Claude ships it"},
		{client: "claude_code", format: "anthropic", wantDir: ".claude", wantProse: "Claude ships it"},
		{client: "codex", format: "openai", wantDir: ".codex", wantProse: "Codex ships it"},
	} {
		t.Run("non-stream/"+tc.client+"/"+tc.format, func(t *testing.T) {
			reqID := "args-" + tc.client + "-" + tc.format
			req := `{"system":"cloak me: Claude","messages":[{"role":"user","content":"go"}]}`

			var resp string
			if tc.format == "openai" {
				resp = `{"choices":[{"message":{"content":"Antigravity ships it","tool_calls":[{"function":{"name":"run_command","arguments":` +
					strconv.Quote(argPayload) + `}}]}}]}`
			} else {
				resp = `{"content":[{"type":"text","text":"Antigravity ships it"},{"type":"tool_use","name":"run_command","input":` +
					argPayload + `}]}`
			}

			forwardCloakedBodyFor(t, tc.client, reqID, tc.format, []byte(req))
			back := reverseBrandPass(t, reqID, req, resp, tc.format, "agy/claude-test")

			if !strings.Contains(back, tc.wantProse) {
				t.Fatalf("assistant prose lost its reverse: %s", back)
			}
			if tc.format == "openai" {
				want := `{"command":"` + typedCommand + `","path":"` + tc.wantDir + `/config.toml"}`
				if got := openAIToolArguments(t, back); got != want {
					t.Fatalf("openai arguments = %q, want %q", got, want)
				}
			} else {
				got := anthropicToolInput(t, back)
				if got["command"] != typedCommand {
					t.Fatalf("anthropic argument command = %q, want it untouched", got["command"])
				}
				if got["path"] != tc.wantDir+"/config.toml" {
					t.Fatalf("anthropic argument path = %q, want the operational rule to apply", got["path"])
				}
			}
		})
	}

	// The operational rules stay in the argument carriers: they are what the
	// model legitimately echoes out of the cloaked prompt, so narrowing the
	// carrier must not take them with it.
	t.Run("operational-rules-still-apply", func(t *testing.T) {
		const reqID = "args-operational"
		req := `{"system":"cloak me: Claude","messages":[{"role":"user","content":"go"}]}`
		payload := `{"url":"https://antigravity.google/docs","skill":"Antigravity-api","sdk":"Antigravity SDK"}`
		resp := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":` +
			strconv.Quote(payload) + `}}]}}]}`

		forwardCloakedBodyFor(t, "claude_code", reqID, "openai", []byte(req))
		back := reverseBrandPass(t, reqID, req, resp, "openai", "agy/claude-test")
		want := `{"url":"https://claude.ai/docs","skill":"claude-api","sdk":"Anthropic SDK"}`
		if got := openAIToolArguments(t, back); got != want {
			t.Fatalf("operational identifiers = %q, want %q", got, want)
		}
	})

	// Streamed carriers: the same payload arrives in fragments, split inside
	// the brand word, which is exactly where a prose rule would have held bytes
	// and flushed them back into the call.
	for _, tc := range []struct{ client, format, wantDir string }{
		{client: "claude_code", format: "anthropic", wantDir: ".claude"},
		{client: "claude_code", format: "openai", wantDir: ".claude"},
		{client: "codex", format: "openai", wantDir: ".codex"},
		{client: "oh_my_pi", format: "anthropic", wantDir: ".omp"},
		{client: "oh_my_pi", format: "openai", wantDir: ".omp"},
	} {
		t.Run("stream/"+tc.client+"/"+tc.format, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "args-stream-" + tc.client + "-" + tc.format
			var cache *cachedUncloakPattern
			if tc.client == "oh_my_pi" {
				cache = ompUncloakCache(t)
			}
			mgr.resetSession("req:"+reqID, tc.client, cache)

			wire := jsonEscape(t, argPayload)
			split := strings.Index(wire, "antigravity-")
			if split < 0 {
				t.Fatalf("fixture lost the typed directory: %q", wire)
			}
			first := wire[:split+len("antigravity-")]
			second := wire[split+len("antigravity-"):]
			frames := []string{
				streamToolArgumentEvent(tc.format, 0, 0, first),
				streamToolArgumentEvent(tc.format, 0, 0, second),
			}
			if tc.format == "anthropic" {
				frames = append(frames,
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			} else {
				frames = append(frames, "data: [DONE]\n\n")
			}

			var delivered, prose strings.Builder
			for _, out := range driveStreamFrames(t, mgr, reqID, tc.format, frames) {
				delivered.WriteString(streamedToolArgumentText(t, tc.format, []byte(out)))
				for _, f := range sseFrames(t, []byte(out)) {
					if delta, ok := f.data["delta"].(map[string]any); ok {
						if txt, ok := delta["text"].(string); ok {
							prose.WriteString(txt)
						}
						if txt, ok := delta["content"].(string); ok {
							prose.WriteString(txt)
						}
					}
				}
			}
			want := `{"command":"` + typedCommand + `","path":"` + tc.wantDir + `/config.toml"}`
			if got := delivered.String(); got != want {
				t.Fatalf("streamed arguments = %q, want %q", got, want)
			}
			if got := prose.String(); got != "" {
				t.Fatalf("a hold leaked into the prose carrier: %q", got)
			}
		})
	}
}

// TestToolArgumentsKeepProseRulesOutOMP is the same contract on the protected
// Oh My Pi lane, whose bare pair is the one the live acceptance run exercised.
func TestToolArgumentsKeepProseRulesOutOMP(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	isolateOMPMeasurement(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "args-omp-nonstream"
	req := `{"tools":[{"name":"read"},{"name":"task"},{"name":"hub"}],"messages":[]}`
	pay := `{"command":"` + typedCommand + `","path":"~/.gemini/agent/AGENTS.md"}`
	resp := `{"content":[{"type":"text","text":"Antigravity ships it"},{"type":"tool_use","name":"view_file","input":` + pay + `}]}`

	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/model", []byte(req)))
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	back := reverseBrandPass(t, reqID, req, resp, "anthropic", "agy/model")
	if !strings.Contains(back, "omp ships it") {
		t.Fatalf("protected prose lost its reverse: %s", back)
	}
	input := anthropicToolInput(t, back)
	if input["command"] != typedCommand {
		t.Fatalf("protected argument command = %q, want it untouched", input["command"])
	}
	if input["path"] != "~/.omp/agent/AGENTS.md" {
		t.Fatalf("protected argument path = %q, want the operational rule to apply", input["path"])
	}
}

// TestClaudeCodeWorkflowProseRestoresTheDeclaredName is the LOW finding: the
// forward rule names Antigravity's capability (teamwork_preview_layer) for
// Claude Code's own workflow tool, but the reverse table had no entry, so a
// model that echoed the name handed the client an identifier it never declared.
func TestClaudeCodeWorkflowProseRestoresTheDeclaredName(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	const reqID = "workflow-prose"
	req := `{"system":"cloak me: Claude","messages":[{"role":"user","content":"go"}]}`
	resp := `{"choices":[{"message":{"content":"teamwork_preview_layer runs the agents"}}]}`
	forwardCloakedBodyFor(t, "claude_code", reqID, "openai", []byte(req))
	back := reverseBrandPass(t, reqID, req, resp, "openai", "agy/claude-test")
	if !strings.Contains(back, "Workflow runs the agents") {
		t.Fatalf("workflow capability name was not restored to the declared tool: %s", back)
	}
}

// workflowLiteralPayload is the client's own text carrying the capability name
// the forward pass writes into prompts: a shell command naming it. Nothing in
// the forward pass puts this literal in an argument, so the argument carrier
// must hand it back byte-for-byte.
const workflowLiteralPayload = `{"command":"echo teamwork_preview_layer > out.txt"}`

// TestClaudeCodeWorkflowNameIsProseOnly pins the split the LOW finding needs:
// the workflow capability name is restored in assistant prose, where the model
// echoes what the cloaked prompt named, and left untouched in a tool argument,
// where the same literal is data the client executes.
func TestClaudeCodeWorkflowNameIsProseOnly(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	req := `{"system":"cloak me: Claude","messages":[{"role":"user","content":"go"}]}`

	t.Run("non-stream/openai", func(t *testing.T) {
		const reqID = "workflow-args-openai"
		resp := `{"choices":[{"message":{"content":"teamwork_preview_layer runs the agents","tool_calls":[{"function":{"name":"run_command","arguments":` +
			strconv.Quote(workflowLiteralPayload) + `}}]}}]}`
		forwardCloakedBodyFor(t, "claude_code", reqID, "openai", []byte(req))
		back := reverseBrandPass(t, reqID, req, resp, "openai", "agy/claude-test")
		if !strings.Contains(back, "Workflow runs the agents") {
			t.Fatalf("assistant prose lost its restore: %s", back)
		}
		if got := openAIToolArguments(t, back); got != workflowLiteralPayload {
			t.Fatalf("openai argument = %q, want the literal untouched", got)
		}
	})

	t.Run("non-stream/anthropic", func(t *testing.T) {
		const reqID = "workflow-args-anthropic"
		resp := `{"content":[{"type":"text","text":"teamwork_preview_layer runs the agents"},{"type":"tool_use","name":"run_command","input":` +
			workflowLiteralPayload + `}]}`
		forwardCloakedBodyFor(t, "claude_code", reqID, "anthropic", []byte(req))
		back := reverseBrandPass(t, reqID, req, resp, "anthropic", "agy/claude-test")
		if !strings.Contains(back, "Workflow runs the agents") {
			t.Fatalf("assistant prose lost its restore: %s", back)
		}
		if got := anthropicToolInput(t, back)["command"]; got != "echo teamwork_preview_layer > out.txt" {
			t.Fatalf("anthropic argument = %v, want the literal untouched", got)
		}
	})

	// Streamed, split inside the literal, so the lane itself has to decide and
	// a hold would show up as bytes arriving through the prose carrier.
	for _, format := range []string{"openai", "anthropic"} {
		t.Run("stream/"+format, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "workflow-args-stream-" + format
			mgr.resetSession("req:"+reqID, "claude_code", nil)

			wire := jsonEscape(t, workflowLiteralPayload)
			split := strings.Index(wire, "teamwork_")
			if split < 0 {
				t.Fatalf("fixture lost the literal: %q", wire)
			}
			first := wire[:split+len("teamwork_")]
			second := wire[split+len("teamwork_"):]
			frames := []string{
				streamToolArgumentEvent(format, 0, 0, first),
				streamToolArgumentEvent(format, 0, 0, second),
			}
			if format == "anthropic" {
				frames = append(frames,
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			} else {
				frames = append(frames, "data: [DONE]\n\n")
			}

			var delivered, prose strings.Builder
			for _, out := range driveStreamFrames(t, mgr, reqID, format, frames) {
				delivered.WriteString(streamedToolArgumentText(t, format, []byte(out)))
				for _, f := range sseFrames(t, []byte(out)) {
					if delta, ok := f.data["delta"].(map[string]any); ok {
						if txt, ok := delta["text"].(string); ok {
							prose.WriteString(txt)
						}
						if txt, ok := delta["content"].(string); ok {
							prose.WriteString(txt)
						}
					}
				}
			}
			if got := delivered.String(); got != workflowLiteralPayload {
				t.Fatalf("streamed %s argument = %q, want the literal untouched", format, got)
			}
			if got := prose.String(); got != "" {
				t.Fatalf("argument bytes leaked into the prose carrier: %q", got)
			}
		})
	}
}

// anthropicArgumentFrame builds one Anthropic input_json_delta frame for a
// block index, carrying an already JSON-escaped fragment (the wire text).
func anthropicArgumentFrame(index int, escapedFragment string) string {
	return "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":` + jsonNumber(index) +
		`,"delta":{"type":"input_json_delta","partial_json":"` + escapedFragment + `"}}` + "\n\n"
}

// openAIProseFrame builds one OpenAI content-delta frame for a choice, carrying
// an already JSON-escaped fragment (the wire text).
func openAIProseFrame(choice int, escapedContent string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"content":"` + escapedContent + `"}}]}` + "\n\n"
}

// openAIArgsFrame builds one OpenAI tool-call argument frame for a choice,
// carrying an already JSON-escaped fragment (the wire text).
func openAIArgsFrame(choice, call int, escapedFragment string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"tool_calls":[{"index":` + jsonNumber(call) +
		`,"function":{"arguments":"` + escapedFragment + `"}}]}}]}` + "\n\n"
}

// openAIFinishFrame builds the frame that carries a choice's finish_reason.
func openAIFinishFrame(choice int, reason string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{},"finish_reason":"` + reason + `"}]}` + "\n\n"
}

// openAIArrayProseFrame builds one OpenAI content-delta frame whose content is
// the content-parts array shape rather than a plain string, which is the other
// form the applier feeds to the choice's prose lane.
func openAIArrayProseFrame(choice int, escapedContent string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"content":[{"type":"text","text":"` +
		escapedContent + `"}]}}]}` + "\n\n"
}

// openAITerminalArrayContentFrame is openAIArrayProseFrame with the choice's
// finish_reason carried in the same event.
func openAITerminalArrayContentFrame(choice int, escapedContent, reason string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"content":[{"type":"text","text":"` +
		escapedContent + `"}]},"finish_reason":"` + reason + `"}]}` + "\n\n"
}

// driveStreamFrames feeds each frame through the stream interceptor and returns
// the bytes emitted for it: the plugin's rewritten body, or the input frame when
// the plugin left it untouched.
func driveStreamFrames(t *testing.T, mgr *streamSessionManager, reqID, format string, frames []string) []string {
	t.Helper()
	emitted := make([]string, 0, len(frames))
	for i, frame := range frames {
		resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: format, ChunkIndex: i, Body: []byte(frame),
		}, format)
		body := resp.Body
		if len(body) == 0 && !resp.DropChunk {
			body = []byte(frame)
		}
		emitted = append(emitted, string(body))
	}
	return emitted
}

// firstEmittedFrame returns the index of the first emitted frame that carries
// needle, or -1 when none does.
func firstEmittedFrame(emitted []string, needle string) int {
	for i, frame := range emitted {
		if strings.Contains(frame, needle) {
			return i
		}
	}
	return -1
}

// TestHeldArgumentTailFlushesBeforeItsBlockCloses pins the first formal-review
// blocker: the filtered terminal flush selected lanes by exact key, so a content
// block's own argument lane ("anthropic:1\x00args") was not selected when its
// parent ("anthropic:1") closed, and the held token reached the client at
// message_stop - after content_block_stop. A client that finalizes the block at
// the stop reads a truncated tool call, which is what Oh My Pi's protected route
// was doing on the primary live path.
func TestHeldArgumentTailFlushesBeforeItsBlockCloses(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// The fixture stops mid-token, so the lane really holds: Oh My Pi's bare
	// ".gemini" rule needs the byte after the match to prove its right boundary,
	// and for an ordinary client ".gemini" is a live prefix of its ".gemini/"
	// directory rule. Either way the bytes are in the lane, not in the stream,
	// when the block closes.
	const fragment = `{"path":"/home/u/.gemini`
	for _, tc := range []struct{ name, client, held string }{
		// Recovered: the protected pair resolves the held token on flush.
		{name: "oh_my_pi", client: "oh_my_pi", held: ".omp"},
		// Ordinary client: same hold, no rule of its own for the bare spelling,
		// so the held bytes go back verbatim - but still before the stop.
		{name: "claude_code", client: "claude_code", held: ".gemini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "stop-flush-" + tc.name
			var cache *cachedUncloakPattern
			if tc.client == "oh_my_pi" {
				cache = ompUncloakCache(t)
			}
			mgr.resetSession("req:"+reqID, tc.client, cache)

			frames := []string{
				"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"run_command"}}` + "\n\n",
				anthropicArgumentFrame(1, jsonEscape(t, fragment)),
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			}
			emitted := driveStreamFrames(t, mgr, reqID, "anthropic", frames)

			// The fixture must really hold the token, or the ordering assertion
			// below can never fail and proves nothing.
			if strings.Contains(emitted[1], tc.held) {
				t.Fatalf("fixture never held the token: %q", emitted[1])
			}
			iStop := firstEmittedFrame(emitted, "content_block_stop")
			if iStop < 0 {
				t.Fatalf("fixture lost content_block_stop: %q", emitted)
			}
			if iTail := firstEmittedFrame(emitted, tc.held); iTail != iStop {
				t.Fatalf("held argument tail landed in frame %d, not with the block's stop in frame %d: %q", iTail, iStop, emitted)
			}
			stopOut := emitted[iStop]
			iHeld := strings.Index(stopOut, tc.held)
			iStopAt := strings.Index(stopOut, "content_block_stop")
			if iHeld < 0 || iStopAt < 0 || iHeld > iStopAt {
				t.Fatalf("argument tail was not emitted before the stop it belongs to: %q", stopOut)
			}
			if rest := stopOut[iStopAt:]; strings.Contains(rest, "input_json_delta") {
				t.Fatalf("a second argument delta arrived after content_block_stop: %q", rest)
			}
			want := `{"path":"/home/u/` + tc.held
			if got := streamedToolArgumentText(t, "anthropic", []byte(strings.Join(emitted, ""))); got != want {
				t.Fatalf("joined arguments = %q, want %q", got, want)
			}
		})
	}
}

// TestOpenAIChoiceFinishFlushesBeforeItsFinishReason pins the second
// formal-review blocker: the SSE path held a choice's carry until [DONE], so it
// arrived after that choice's finish_reason - which a client may already have
// acted on, since finish_reason is what finalizes the choice. A non-null
// finish_reason now flushes that choice's own lanes into the same emitted frame,
// before the event that carries it.
func TestOpenAIChoiceFinishFlushesBeforeItsFinishReason(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// Both carriers and both authorities. The fragments are chosen per table so
	// each lane genuinely holds at the finish_reason: Oh My Pi's protected pair
	// carries the host exclusion ("visit Antigravity.go"), the ordinary clients'
	// bare word does not ("visit Antigravity SD" is a live prefix of the
	// "Antigravity SDK" rule), and the argument case holds a partial path. `tail`
	// is the held text the finished choice must deliver; `want` is the whole
	// carrier afterwards, so a lost or duplicated byte fails too.
	for _, tc := range []struct {
		name, client, fragment, tail, want string
		args, prose                        bool
	}{
		{name: "oh_my_pi/prose", client: "oh_my_pi", fragment: "visit Antigravity.go", tail: "omp.go", want: "visit omp.go", prose: true},
		{name: "claude_code/prose", client: "claude_code", fragment: "visit Antigravity SD", tail: "Claude SD", want: "visit Claude SD", prose: true},
		{name: "codex/prose", client: "codex", fragment: "visit Antigravity.go", tail: "Codex.go", want: "visit Codex.go", prose: true},
		{name: "oh_my_pi/arguments", client: "oh_my_pi", fragment: `{"path":"/home/u/.gemini`, tail: ".omp", want: `{"path":"/home/u/.omp`, args: true},
		{name: "claude_code/arguments", client: "claude_code", fragment: `{"path":"/home/u/.gemini`, tail: ".gemini", want: `{"path":"/home/u/.gemini`, args: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "finish-flush-" + strings.ReplaceAll(tc.name, "/", "-")
			var cache *cachedUncloakPattern
			if tc.client == "oh_my_pi" {
				cache = ompUncloakCache(t)
			}
			mgr.resetSession("req:"+reqID, tc.client, cache)

			var frames []string
			if tc.args {
				frames = append(frames, openAIArgsFrame(0, 0, ""))
				frames = append(frames, openAIArgsFrame(0, 0, jsonEscape(t, tc.fragment)))
			} else {
				frames = append(frames, openAIProseFrame(0, jsonEscape(t, tc.fragment)))
			}
			frames = append(frames, openAIFinishFrame(0, "tool_calls"), "data: [DONE]\n\n")

			emitted := driveStreamFrames(t, mgr, reqID, "openai", frames)

			// The fixture must really hold the token through the delta frame.
			deltaFrame := 0
			if tc.args {
				deltaFrame = 1
			}
			if strings.Contains(emitted[deltaFrame], tc.tail) {
				t.Fatalf("fixture never held the token: %q", emitted[deltaFrame])
			}
			iFinish := firstEmittedFrame(emitted, `"finish_reason":"tool_calls"`)
			if iFinish < 0 {
				t.Fatalf("fixture lost the finish_reason frame: %q", emitted)
			}
			finishOut := emitted[iFinish]
			iHeld := strings.Index(finishOut, tc.tail)
			iReason := strings.Index(finishOut, `"finish_reason"`)
			if iHeld < 0 || iReason < 0 || iHeld > iReason {
				t.Fatalf("the finished choice's carry was not emitted before its finish_reason: %q", finishOut)
			}
			// Nothing is left for [DONE] to flush: the terminal frame is
			// forwarded untouched.
			if last := emitted[len(emitted)-1]; last != "data: [DONE]\n\n" {
				t.Fatalf("[DONE] frame = %q, want it untouched", last)
			}
			joined := []byte(strings.Join(emitted, ""))
			if tc.prose {
				if got := sseAssistantJoined(t, joined); got != tc.want {
					t.Fatalf("joined content = %q, want %q", got, tc.want)
				}
			} else if got := streamedToolArgumentText(t, "openai", joined); got != tc.want {
				t.Fatalf("joined arguments = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenAIChoiceFinishLeavesOtherChoicesStreaming pins the other half of the
// per-choice flush: finishing choice 0 must take only its own lane. Choice 1 is
// still streaming and its token may still complete, so its carry has to survive
// until choice 1 finishes (or [DONE] ends the stream).
func TestOpenAIChoiceFinishLeavesOtherChoicesStreaming(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	mgr := newStreamSessionManager()
	const reqID = "multi-choice-finish"
	mgr.resetSession("req:"+reqID, "oh_my_pi", ompUncloakCache(t))

	const sentence = "checked /home/u/.gemini"
	frames := []string{
		openAIProseFrame(0, jsonEscape(t, sentence)),
		openAIProseFrame(1, jsonEscape(t, sentence)),
		openAIFinishFrame(0, "stop"),
		"data: [DONE]\n\n",
	}
	emitted := driveStreamFrames(t, mgr, reqID, "openai", frames)

	// Both choices must really be holding, or the isolation assertion below is
	// vacuous.
	for i := 0; i < 2; i++ {
		if strings.Contains(emitted[i], ".omp") {
			t.Fatalf("choice %d never held its token: %q", i, emitted[i])
		}
	}
	iFinish := firstEmittedFrame(emitted, `"finish_reason":"stop"`)
	if iFinish < 0 {
		t.Fatalf("fixture lost the finish_reason frame: %q", emitted)
	}
	head := emitted[iFinish][:strings.Index(emitted[iFinish], `"finish_reason"`)]
	if !strings.Contains(head, ".omp") {
		t.Fatalf("the finished choice's carry was not flushed before its finish_reason: %q", emitted[iFinish])
	}
	if strings.Contains(head, `"index":1`) {
		t.Fatalf("an unfinished choice's lane was flushed with the finished one: %q", head)
	}
	done := emitted[len(emitted)-1]
	if !strings.Contains(done, `"index":1`) || !strings.Contains(done, ".omp") {
		t.Fatalf("[DONE] did not flush the still-open choice's carry: %q", done)
	}
	// Nothing was lost: each choice's sentence came back resolved, so the
	// recovered token appears exactly once per choice.
	all := strings.Join(emitted, "")
	if n := strings.Count(all, "checked /home/u/"); n != 2 {
		t.Fatalf("expected both choices' prose to survive: %q", all)
	}
	if n := strings.Count(all, ".omp"); n != 2 {
		t.Fatalf("expected each choice's token once, got %d: %q", n, all)
	}
}

// openAITerminalContentFrame builds the shape that carries a choice's content
// and its finish_reason in the SAME event.
func openAITerminalContentFrame(choice int, escapedContent, reason string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"content":"` + escapedContent +
		`"},"finish_reason":"` + reason + `"}]}` + "\n\n"
}

// openAITerminalArgsFrame is the same shape with streamed tool-call arguments.
func openAITerminalArgsFrame(choice, call int, escapedFragment, reason string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"tool_calls":[{"index":` + jsonNumber(call) +
		`,"function":{"arguments":"` + escapedFragment + `"}}]},"finish_reason":"` + reason + `"}]}` + "\n\n"
}

// TestOpenAITerminalEventResolvesItsOwnCarry pins the residual the per-choice
// flush left behind: that flush runs before the terminal event is applied, so a
// token the event's OWN delta ends on is created after it and still waited for
// [DONE] - arriving behind the finish_reason that finalized the choice. A choice
// marked terminal for the event being applied may not hold, so the token is
// resolved inside the same payload, in the field it arrived in.
func TestOpenAITerminalEventResolvesItsOwnCarry(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, tc := range []struct {
		name, client, fragment, tail, want string
		args                               bool
	}{
		{name: "oh_my_pi/content", client: "oh_my_pi", fragment: "visit Antigravity.go", tail: "omp.go", want: "visit omp.go"},
		{name: "claude_code/content", client: "claude_code", fragment: "visit Antigravity SD", tail: "Claude SD", want: "visit Claude SD"},
		{name: "codex/content", client: "codex", fragment: "visit Antigravity.go", tail: "Codex.go", want: "visit Codex.go"},
		{name: "oh_my_pi/arguments", client: "oh_my_pi", fragment: `{"path":"/home/u/.gemini`, tail: ".omp", want: `{"path":"/home/u/.omp`, args: true},
		{name: "claude_code/arguments", client: "claude_code", fragment: `{"path":"/home/u/.gemini`, tail: ".gemini", want: `{"path":"/home/u/.gemini`, args: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			escaped := jsonEscape(t, tc.fragment)
			delta, terminal := openAIProseFrame(0, escaped), openAITerminalContentFrame(0, escaped, "stop")
			if tc.args {
				delta = openAIArgsFrame(0, 0, escaped)
				terminal = openAITerminalArgsFrame(0, 0, escaped, "tool_calls")
			}
			reqID := "terminal-" + strings.ReplaceAll(tc.name, "/", "-")
			newSession := func(id string) *streamSessionManager {
				mgr := newStreamSessionManager()
				var cache *cachedUncloakPattern
				if tc.client == "oh_my_pi" {
					cache = ompUncloakCache(t)
				}
				mgr.resetSession("req:"+id, tc.client, cache)
				return mgr
			}

			// Guard: the same delta, in an event with no finish_reason, really
			// holds the token - otherwise the assertion below cannot fail and
			// proves nothing.
			guardMgr := newSession(reqID + "-guard")
			guard := driveStreamFrames(t, guardMgr, reqID+"-guard", "openai", []string{delta})
			if strings.Contains(guard[0], tc.tail) {
				t.Fatalf("fixture never held the token: %q", guard[0])
			}

			mgr := newSession(reqID)
			emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{terminal, "data: [DONE]\n\n"})

			iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
			if iTerm < 0 {
				t.Fatalf("fixture lost the terminal event: %q", emitted)
			}
			termOut := emitted[iTerm]
			// The payload is JSON, so the expectation is compared in its wire
			// form: an argument fragment arrives with its quotes escaped.
			iWant := strings.Index(termOut, jsonEscape(t, tc.want))
			iReason := strings.Index(termOut, `"finish_reason"`)
			if iWant < 0 || iWant > iReason {
				t.Fatalf("the terminal event did not carry its own resolved text ahead of the finish_reason: %q", termOut)
			}
			// Nothing may survive the terminal event: [DONE] has nothing left.
			if last := emitted[len(emitted)-1]; last != "data: [DONE]\n\n" {
				t.Fatalf("a carry outlived the terminal event: %q", last)
			}
			joined := []byte(strings.Join(emitted, ""))
			if tc.args {
				if got := streamedToolArgumentText(t, "openai", joined); got != tc.want {
					t.Fatalf("joined arguments = %q, want %q", got, tc.want)
				}
			} else if got := sseAssistantJoined(t, joined); got != tc.want {
				t.Fatalf("joined content = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenAITerminalEventLeavesOtherChoicesCarryHeld keeps the per-choice
// isolation the terminal mark must not break: choice 1 is still streaming, so
// its carry survives choice 0's terminal event and is flushed at [DONE].
func TestOpenAITerminalEventLeavesOtherChoicesCarryHeld(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	mgr := newStreamSessionManager()
	const reqID = "terminal-isolation"
	mgr.resetSession("req:"+reqID, "oh_my_pi", ompUncloakCache(t))

	const open = "checked /home/u/.gemini"
	emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{
		openAIProseFrame(1, jsonEscape(t, open)),
		openAITerminalContentFrame(0, jsonEscape(t, "visit Antigravity.go"), "stop"),
		"data: [DONE]\n\n",
	})

	if strings.Contains(emitted[0], ".omp") {
		t.Fatalf("choice 1 never held its token: %q", emitted[0])
	}
	iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
	if iTerm < 0 {
		t.Fatalf("fixture lost the terminal event: %q", emitted)
	}
	head, tail, _ := strings.Cut(emitted[iTerm], `"finish_reason"`)
	if !strings.Contains(head, "visit omp.go") {
		t.Fatalf("the terminal choice's own carry was not resolved: %q", emitted[iTerm])
	}
	if strings.Contains(head+tail, `"index":1`) {
		t.Fatalf("the terminal event touched another choice's lanes: %q", emitted[iTerm])
	}
	done := emitted[len(emitted)-1]
	if !strings.Contains(done, `"index":1`) || !strings.Contains(done, ".omp") {
		t.Fatalf("[DONE] did not flush the still-open choice's carry: %q", done)
	}
}

// anthropicToolInput decodes the first tool_use block's input object, so a test
// can assert on the values a client would act on.
func anthropicToolInput(t *testing.T, body string) map[string]any {
	t.Helper()
	var root map[string]any
	if err := safeUnmarshal([]byte(body), &root); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, body)
	}
	for _, block := range sliceOfMaps(root["content"]) {
		if block["type"] != "tool_use" {
			continue
		}
		input, _ := block["input"].(map[string]any)
		return input
	}
	t.Fatalf("response has no tool_use input: %s", body)
	return nil
}

// openAITerminalContentFrameEscapedKey is openAITerminalContentFrame with the
// finish_reason key spelled through a JSON escape: "\u005f" decodes to "_", so
// the key IS finish_reason once parsed while the raw bytes carry no literal
// "finish_reason" substring. Valid JSON a strict encoder may emit.
func openAITerminalContentFrameEscapedKey(choice int, escapedContent, reason string) string {
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"content":"` + escapedContent +
		`"},"finish\u005freason":"` + reason + `"}]}` + "\n\n"
}

// TestOpenAIChoiceFinishKeepsTheLaneTheSameEventContinues is the regression for
// the split token the pre-flush broke: event N leaves a live partial carry and
// event N+1 both finishes the choice and supplies the remainder. Draining the
// partial before the event was applied delivered it raw and emptied the lane, so
// the remainder could no longer complete it and the client received the CLOAKED
// spelling - "Ant" then "igravity" arrived as "Antigravity" where the request had
// cloaked "omp"/"Claude". A lane the terminal event itself appends to must stay
// untouched and resolve inline, in the field its text arrived in.
func TestOpenAIChoiceFinishKeepsTheLaneTheSameEventContinues(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, tc := range []struct {
		name, client, first, second, want, termTail string
		args                                        bool
	}{
		{name: "oh_my_pi/prose", client: "oh_my_pi", first: "Ant", second: "igravity", want: "omp", termTail: "omp"},
		{name: "claude_code/prose", client: "claude_code", first: "Ant", second: "igravity", want: "Claude", termTail: "Claude"},
		{name: "oh_my_pi/arguments", client: "oh_my_pi", first: `{"path":"/home/u/.gemini`, second: `/agent"}`, want: `{"path":"/home/u/.omp/agent"}`, termTail: `.omp/agent"}`},
		{name: "claude_code/arguments", client: "claude_code", first: `{"path":"/home/u/.gemini`, second: `/agent"}`, want: `{"path":"/home/u/.claude/agent"}`, termTail: `.claude/agent"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "continue-" + strings.ReplaceAll(tc.name, "/", "-")
			var cache *cachedUncloakPattern
			if tc.client == "oh_my_pi" {
				cache = ompUncloakCache(t)
			}
			mgr.resetSession("req:"+reqID, tc.client, cache)

			var first, terminal string
			if tc.args {
				first = openAIArgsFrame(0, 0, jsonEscape(t, tc.first))
				terminal = openAITerminalArgsFrame(0, 0, jsonEscape(t, tc.second), "tool_calls")
			} else {
				first = openAIProseFrame(0, jsonEscape(t, tc.first))
				terminal = openAITerminalContentFrame(0, jsonEscape(t, tc.second), "stop")
			}
			emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{first, terminal, "data: [DONE]\n\n"})

			// The fixture must really hold the partial, or the assertion below
			// cannot fail and proves nothing.
			if strings.Contains(emitted[0], tc.first) {
				t.Fatalf("fixture never held the partial token: %q", emitted[0])
			}
			iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
			if iTerm < 0 {
				t.Fatalf("fixture lost the terminal event: %q", emitted)
			}
			// The continued lane must not have been pre-flushed into an event of
			// its own: the terminal frame carries one event, and the field inside
			// it carries the resolved tail.
			termFrames := sseFrames(t, []byte(emitted[iTerm]))
			if len(termFrames) != 1 {
				t.Fatalf("the continued lane was pre-flushed as its own event: %q", emitted[iTerm])
			}
			// The fixture's terminal event carries exactly one choice, so position
			// is the choice (sseFrames decodes with encoding/json, where an index
			// is a float64 rather than the session's json.Number).
			termChoices := sliceOfMaps(termFrames[0].data["choices"])
			if len(termChoices) != 1 {
				t.Fatalf("terminal event carries %d choices, want 1: %q", len(termChoices), emitted[iTerm])
			}
			delta, _ := termChoices[0]["delta"].(map[string]any)
			gotField := ""
			if tc.args {
				for _, call := range sliceOfMaps(delta["tool_calls"]) {
					fn, _ := call["function"].(map[string]any)
					gotField, _ = fn["arguments"].(string)
				}
			} else {
				gotField, _ = delta["content"].(string)
			}
			if gotField != tc.termTail {
				t.Fatalf("the terminal event resolved its continued carry to %q, want %q", gotField, tc.termTail)
			}
			if last := emitted[len(emitted)-1]; last != "data: [DONE]\n\n" {
				t.Fatalf("a carry outlived the terminal event: %q", last)
			}
			joined := []byte(strings.Join(emitted, ""))
			if tc.args {
				if got := streamedToolArgumentText(t, "openai", joined); got != tc.want {
					t.Fatalf("joined arguments = %q, want %q", got, tc.want)
				}
			} else if got := sseAssistantJoined(t, joined); got != tc.want {
				t.Fatalf("joined content = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenAIChoiceFinishContinuesOnlyItsOwnChoice keeps the isolation the
// terminal skip must not break: the finishing choice's continued lane resolves
// inline, while another choice's held carry survives untouched to [DONE].
func TestOpenAIChoiceFinishContinuesOnlyItsOwnChoice(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	mgr := newStreamSessionManager()
	const reqID = "continue-isolation"
	mgr.resetSession("req:"+reqID, "oh_my_pi", ompUncloakCache(t))

	emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{
		openAIProseFrame(0, jsonEscape(t, "Ant")),
		openAIProseFrame(1, jsonEscape(t, "visit Antigravity.go")),
		"data: " + `{"choices":[{"index":0,"delta":{"content":"igravity"},"finish_reason":"stop"},{"index":1,"delta":{}}]}` + "\n\n",
		"data: [DONE]\n\n",
	})

	// Both choices must be holding, or the isolation assertion is vacuous.
	if strings.Contains(emitted[0], "Ant") {
		t.Fatalf("choice 0 never held its partial: %q", emitted[0])
	}
	if strings.Contains(emitted[1], ".go") {
		t.Fatalf("choice 1 never held its partial: %q", emitted[1])
	}
	iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
	if iTerm < 0 {
		t.Fatalf("fixture lost the terminal event: %q", emitted)
	}
	head, _, _ := strings.Cut(emitted[iTerm], `"finish_reason"`)
	if !strings.Contains(head, "omp") {
		t.Fatalf("the continued carry was not resolved with its own event: %q", emitted[iTerm])
	}
	// One event, no flush prepended for the choice that continued.
	if n := strings.Count(emitted[iTerm], "data:"); n != 1 {
		t.Fatalf("the continued choice was pre-flushed anyway (%d events): %q", n, emitted[iTerm])
	}
	done := emitted[len(emitted)-1]
	if !strings.Contains(done, `"index":1`) || !strings.Contains(done, "omp.go") {
		t.Fatalf("[DONE] did not flush the still-open choice's carry: %q", done)
	}
	if strings.Contains(done, "igravity") {
		t.Fatalf("[DONE] re-emitted the finished choice's text: %q", done)
	}
}

// TestOpenAITerminalEventDetectedThroughEscapedFinishReasonKey pins the terminal
// detection to the DECODED key: "\u005f" is a legal spelling of "_", so
// "finish\u005freason" is a finish_reason that raw byte matching misses. When it
// was missed, the terminal mark never happened and the choice's carry was held
// past its own finish_reason to [DONE] - arriving behind the event a client
// finalizes the choice on.
func TestOpenAITerminalEventDetectedThroughEscapedFinishReasonKey(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, tc := range []struct{ name, client, content, want string }{
		{name: "oh_my_pi", client: "oh_my_pi", content: "visit Antigravity.go", want: "visit omp.go"},
		{name: "claude_code", client: "claude_code", content: "visit Antigravity SD", want: "visit Claude SD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newStreamSessionManager()
			reqID := "escaped-key-" + tc.name
			var cache *cachedUncloakPattern
			if tc.client == "oh_my_pi" {
				cache = ompUncloakCache(t)
			}
			mgr.resetSession("req:"+reqID, tc.client, cache)

			terminal := openAITerminalContentFrameEscapedKey(0, jsonEscape(t, tc.content), "stop")
			if strings.Contains(terminal, "finish_reason") {
				t.Fatalf("fixture still spells the key literally: %q", terminal)
			}
			emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{terminal, "data: [DONE]\n\n"})

			iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
			if iTerm != 0 {
				t.Fatalf("the escaped terminal key was not detected at all: %q", emitted)
			}
			head, _, _ := strings.Cut(emitted[0], `"finish_reason"`)
			if !strings.Contains(head, tc.want) {
				t.Fatalf("the terminal event did not resolve its own carry: %q", emitted[0])
			}
			if last := emitted[len(emitted)-1]; last != "data: [DONE]\n\n" {
				t.Fatalf("a carry outlived the terminal event: %q", last)
			}
			if got := sseAssistantJoined(t, []byte(strings.Join(emitted, ""))); got != tc.want {
				t.Fatalf("joined content = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("malformed line beside it stays untouched", func(t *testing.T) {
		mgr := newStreamSessionManager()
		const reqID = "escaped-key-malformed"
		mgr.resetSession("req:"+reqID, "oh_my_pi", ompUncloakCache(t))

		chunk := "data: {not json\n\n" + openAITerminalContentFrameEscapedKey(0, jsonEscape(t, "visit Antigravity.go"), "stop")
		emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{chunk, "data: [DONE]\n\n"})

		if !strings.Contains(emitted[0], "data: {not json") {
			t.Fatalf("malformed event was not forwarded verbatim: %q", emitted[0])
		}
		if !strings.Contains(emitted[0], "visit omp.go") {
			t.Fatalf("the escaped terminal event beside a malformed one was not resolved: %q", emitted[0])
		}
		if last := emitted[len(emitted)-1]; last != "data: [DONE]\n\n" {
			t.Fatalf("a carry outlived the terminal event: %q", last)
		}
	})
}

// openAIContentShapeFrame renders one OpenAI content-delta frame in any of the
// shapes the applier feeds to a choice's prose lane; `terminal` adds the
// choice's finish_reason to the same event. `escaped` is already wire-escaped.
func openAIContentShapeFrame(choice int, shape, escaped string, terminal bool) string {
	var content string
	switch shape {
	case "array-part":
		content = `[{"type":"text","text":"` + escaped + `"}]`
	case "array-string":
		content = `["` + escaped + `"]`
	case "object":
		content = `{"type":"text","text":"` + escaped + `"}`
	default:
		content = `"` + escaped + `"`
	}
	finish := ""
	if terminal {
		finish = `,"finish_reason":"stop"`
	}
	return "data: " + `{"choices":[{"index":` + jsonNumber(choice) + `,"delta":{"content":` + content + `}` + finish + `}]}` + "\n\n"
}

// TestOpenAITerminalArrayContentResolvesItsOwnCarry is the content-parts half of
// the split-token fix. The pre-flush skip set was computed from a string-only
// content check, so a terminal event whose continuation arrived in the array (or
// single text-part object) shape looked like it had no continuation at all: the
// live carry was drained raw before the event and the remainder was appended
// after it, so "Ant" + "igravity" reached the client as the CLOAKED
// "Antigravity". Every content shape the applier feeds to the prose lane must
// count as a continuation.
func TestOpenAITerminalArrayContentResolvesItsOwnCarry(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, tc := range []struct{ name, client, want string }{
		{name: "oh_my_pi", client: "oh_my_pi", want: "omp"},
		{name: "claude_code", client: "claude_code", want: "Claude"},
	} {
		for _, shape := range []string{"string", "array-part", "array-string", "object"} {
			t.Run(tc.name+"/"+shape, func(t *testing.T) {
				newSession := func(id string) *streamSessionManager {
					mgr := newStreamSessionManager()
					var cache *cachedUncloakPattern
					if tc.client == "oh_my_pi" {
						cache = ompUncloakCache(t)
					}
					mgr.resetSession("req:"+id, tc.client, cache)
					return mgr
				}
				reqID := "array-" + tc.name + "-" + shape
				first := openAIContentShapeFrame(0, shape, jsonEscape(t, "Ant"), false)
				terminal := openAIContentShapeFrame(0, shape, jsonEscape(t, "igravity"), true)

				// Guard: the same delta without a finish_reason really holds the
				// partial, or the assertion below cannot fail and proves nothing.
				guard := driveStreamFrames(t, newSession(reqID+"-guard"), reqID+"-guard", "openai", []string{first})
				if strings.Contains(guard[0], "Ant") {
					t.Fatalf("fixture never held the partial token: %q", guard[0])
				}

				emitted := driveStreamFrames(t, newSession(reqID), reqID, "openai",
					[]string{first, terminal, "data: [DONE]\n\n"})
				iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
				if iTerm != 1 {
					t.Fatalf("terminal event arrived at frame %d, want 1: %q", iTerm, emitted)
				}
				// The continued lane must not have been pre-flushed as an event
				// of its own inside the terminal frame.
				if n := strings.Count(emitted[iTerm], "data:"); n != 1 {
					t.Fatalf("the continued lane was pre-flushed as its own event (%d events): %q", n, emitted[iTerm])
				}
				head, _, _ := strings.Cut(emitted[iTerm], `"finish_reason"`)
				if !strings.Contains(head, tc.want) {
					t.Fatalf("the terminal event did not resolve its own %s carry: %q", shape, emitted[iTerm])
				}
				if strings.Contains(head, "Antigravity") {
					t.Fatalf("the raw cloaked spelling leaked ahead of the finish_reason: %q", head)
				}
				if last := emitted[len(emitted)-1]; last != "data: [DONE]\n\n" {
					t.Fatalf("a carry outlived the terminal event: %q", last)
				}
				// sseAssistantJoined reads string and array content, not the
				// single-object shape; assert the object form on the frame itself.
				if shape == "object" {
					if !strings.Contains(head, `"text":"`+tc.want+`"`) {
						t.Fatalf("the object content was not resolved in place: %q", head)
					}
					return
				}
				if got := sseAssistantJoined(t, []byte(strings.Join(emitted, ""))); got != tc.want {
					t.Fatalf("joined content = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

// TestOpenAITerminalArrayContentContinuesOnlyItsOwnChoice keeps the per-choice
// isolation the array-content skip must not break: the finishing choice resolves
// its array-content continuation inline and still has its own held tool-argument
// carry flushed ahead of the finish_reason, while another choice's prose carry
// survives untouched to [DONE].
func TestOpenAITerminalArrayContentContinuesOnlyItsOwnChoice(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	mgr := newStreamSessionManager()
	const reqID = "array-continue-isolation"
	mgr.resetSession("req:"+reqID, "oh_my_pi", ompUncloakCache(t))

	terminal := "data: " + `{"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"igravity"}]},` +
		`"finish_reason":"stop"},{"index":1,"delta":{}}]}` + "\n\n"
	emitted := driveStreamFrames(t, mgr, reqID, "openai", []string{
		openAIArrayProseFrame(0, jsonEscape(t, "Ant")),
		openAIProseFrame(1, jsonEscape(t, "visit Antigravity.go")),
		openAIArgsFrame(0, 0, jsonEscape(t, `{"path":"/home/u/.gemini`)),
		terminal,
		"data: [DONE]\n\n",
	})

	// Every lane must really be holding, or the isolation assertions are vacuous.
	if strings.Contains(emitted[0], "Ant") {
		t.Fatalf("choice 0 never held its array-content partial: %q", emitted[0])
	}
	if strings.Contains(emitted[1], ".go") {
		t.Fatalf("choice 1 never held its partial: %q", emitted[1])
	}
	if strings.Contains(emitted[2], ".gemini") {
		t.Fatalf("choice 0 never held its argument partial: %q", emitted[2])
	}
	iTerm := firstEmittedFrame(emitted, `"finish_reason"`)
	if iTerm != 3 {
		t.Fatalf("terminal event arrived at frame %d, want 3: %q", iTerm, emitted)
	}
	head, _, _ := strings.Cut(emitted[iTerm], `"finish_reason"`)
	// Choice 0's own argument carry is flushed ahead of its finish_reason...
	if !strings.Contains(head, ".omp") || !strings.Contains(head, `"index":0`) {
		t.Fatalf("choice 0's argument carry was not flushed before its finish_reason: %q", emitted[iTerm])
	}
	// ...the unfinished choice's lane is not...
	if strings.Contains(head, `"index":1`) {
		t.Fatalf("an unfinished choice's lane was flushed with the finished one: %q", head)
	}
	// ...and the continued array lane resolved inline instead of being pre-flushed.
	if !strings.Contains(head, "omp") || strings.Contains(head, "igravity") {
		t.Fatalf("the array-content continuation was not resolved inline: %q", emitted[iTerm])
	}
	if n := strings.Count(emitted[iTerm], "data:"); n != 2 {
		t.Fatalf("expected choice 0's own argument-lane flush plus the terminal event, got %d events: %q", n, emitted[iTerm])
	}
	pre, _, _ := strings.Cut(emitted[iTerm], "\n\n")
	if strings.Contains(pre, "igravity") || strings.Contains(pre, "Ant") {
		t.Fatalf("the continued prose lane was pre-flushed instead of resolving inline: %q", pre)
	}
	done := emitted[len(emitted)-1]
	if !strings.Contains(done, `"index":1`) || !strings.Contains(done, "omp.go") {
		t.Fatalf("[DONE] did not flush the still-open choice's carry: %q", done)
	}
	if strings.Contains(done, "igravity") {
		t.Fatalf("[DONE] re-emitted the finished choice's text: %q", done)
	}
	if got := streamedToolArgumentText(t, "openai", []byte(strings.Join(emitted, ""))); got != `{"path":"/home/u/.omp` {
		t.Fatalf("joined arguments = %q, want %q", got, `{"path":"/home/u/.omp`)
	}
	joined := sseAssistantJoined(t, []byte(strings.Join(emitted, "")))
	if strings.Contains(joined, "Antigravity") || strings.Contains(joined, "igravity") {
		t.Fatalf("a raw cloaked spelling reached the client: %q", joined)
	}
	// Choice 0's resolved token once, choice 1's resolved token once - no
	// duplication, no loss.
	if n := strings.Count(joined, "omp"); n != 2 {
		t.Fatalf("expected one resolved token per choice, got %d: %q", n, joined)
	}
	if !strings.Contains(joined, "visit ") || !strings.Contains(joined, ".go") {
		t.Fatalf("choice 1's prose did not survive: %q", joined)
	}
}
