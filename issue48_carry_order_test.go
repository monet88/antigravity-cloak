package main

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Ticket #48: held tokens have to reach the client in the order the model
// produced them, and a hold is only worth its latency if it can still grow
// into a boundary-valid match.

func newBrandTestSession(t *testing.T, reqID, client string) *streamSession {
	t.Helper()
	mgr := newStreamSessionManager()
	mgr.resetSession("req:"+reqID, client, ompUncloakCache(t))
	mgr.mu.Lock()
	sess := mgr.sessions["req:"+reqID]
	mgr.mu.Unlock()
	if sess == nil {
		t.Fatalf("session %q was not registered", reqID)
	}
	sess.updatedAt = time.Now()
	return sess
}

// holdOneLane reproduces exactly what applyReverseBrandLanes does for a single
// rule, so a test can put two lanes of one block in the state a
// multi-token-holding table produces. The per-mapping chain can only leave one
// lane of a block holding per chunk, because each rule sees the text the
// previous rule emitted, so the ordering rule is exercised on the state level
// and end to end through the real flush below.
func holdOneLane(t *testing.T, sess *streamSession, laneKey, match, text string) {
	t.Helper()
	lane := getBrandLane(sess, laneKey+reverseBrandLaneSuffix+match)
	applyBrandLaneSet(text, lane, []rewriteMapping{{Match: match, Replacement: reverseReplacementFor(t, match)}})
	stampLaneArrival(sess, lane)
}

func reverseReplacementFor(t *testing.T, match string) string {
	t.Helper()
	for _, m := range brandReverseTableFor("claude_code") {
		if m.Match == match {
			return m.Replacement
		}
	}
	t.Fatalf("no claude_code reverse rule for %q", match)
	return ""
}

// TestIssue48_HeldTokensFlushInSourceOrderNotMappingOrder is the core
// regression: "GEMINI.md" is declared BEFORE "Google Deepmind" in claude_code's
// reverse table, so the old key-string tie-break emitted the GEMINI.md carry
// first even when the model produced Google Deepmind first. The client read the
// two tokens transposed.
func TestIssue48_HeldTokensFlushInSourceOrderNotMappingOrder(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	sess := newBrandTestSession(t, "issue48-order", "claude_code")

	// Model order: "Google Deepmind" first, "GEMINI.md" second.
	holdOneLane(t, sess, "anthropic:0", "Google Deepmind", "made by Google Deepmind")
	holdOneLane(t, sess, "anthropic:0", "GEMINI.md", " per GEMINI.md")

	// Reverse-table order is the opposite, so a key tie-break is detectable.
	first, second := "anthropic:0"+reverseBrandLaneSuffix+"GEMINI.md", "anthropic:0"+reverseBrandLaneSuffix+"Google Deepmind"
	if !(first < second) {
		t.Fatalf("fixture is not discriminating: %q already sorts before %q", first, second)
	}

	flushes := orderedBrandFlushes(sess, brandReverseTableFor("claude_code"))
	if len(flushes) != 2 {
		t.Fatalf("want two flushes, got %d", len(flushes))
	}
	if flushes[0].key != second {
		t.Fatalf("first flush key = %q, want the token the model produced first", flushes[0].key)
	}
	if flushes[1].key != first {
		t.Fatalf("second flush key = %q, want the token the model produced second", flushes[1].key)
	}
	if got := flushes[0].text + flushes[1].text; got != "AnthropicCLAUDE.md" {
		t.Fatalf("reassembled carries = %q, want original data order", got)
	}
}

// TestIssue48_FlushEventsCarryHeldTokensInSourceOrder drives the real flush
// function, not the helpers, and checks the bytes the client receives.
func TestIssue48_FlushEventsCarryHeldTokensInSourceOrder(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue48-flush", "claude_code", ompUncloakCache(t))
	mgr.mu.Lock()
	sess := mgr.sessions["req:issue48-flush"]
	mgr.mu.Unlock()
	sess.updatedAt = time.Now()

	holdOneLane(t, sess, "anthropic:0", "Google Deepmind", "by Google Deepmind")
	holdOneLane(t, sess, "anthropic:0", "GEMINI.md", " per GEMINI.md")

	var out string
	for _, ev := range strings.Split(strings.TrimRight(string(mgr.reverseFlushCloakedBrandLanes(sess, "anthropic", "anthropic:0", false)), "\n\n"), "\n\n") {
		_, data := parseSSEFrame(t, ev+"\n\n")
		delta := data["delta"].(map[string]any)
		s, _ := delta["text"].(string)
		out += s
	}
	if out != "AnthropicCLAUDE.md" {
		t.Fatalf("flushed %q, want Anthropic before CLAUDE.md", out)
	}
}

// TestIssue48_FlushOrderIsStableAcrossMapIteration repeats the same state
// many times: Go randomises map iteration, so a key tie-break is flaky while
// arrival order is stable by construction.
func TestIssue48_FlushOrderIsStableAcrossMapIteration(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	var want string
	for i := range 50 {
		sess := newBrandTestSession(t, "issue48-stable", "claude_code")
		holdOneLane(t, sess, "anthropic:0", "Google Deepmind", "by Google Deepmind")
		holdOneLane(t, sess, "anthropic:0", "GEMINI.md", " per GEMINI.md")
		var got string
		for _, f := range orderedBrandFlushes(sess, brandReverseTableFor("claude_code")) {
			got += f.key + "=" + f.text + "|"
		}
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("flush order unstable at iteration %d: %q != %q", i, got, want)
		}
	}
}

// TestIssue48_LaneIndexStillDominatesSourceOrder keeps Issue #18's guarantee:
// distinct content blocks flush in index order, whatever order they were
// opened in.
func TestIssue48_LaneIndexStillDominatesSourceOrder(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	sess := newBrandTestSession(t, "issue48-index", "claude_code")

	holdOneLane(t, sess, "anthropic:2", "Antigravity", "late Antigravity")
	holdOneLane(t, sess, "anthropic:1", "Antigravity", "early Antigravity")

	flushes := orderedBrandFlushes(sess, brandReverseTableFor("claude_code"))
	if len(flushes) != 2 {
		t.Fatalf("want two flushes, got %d", len(flushes))
	}
	if got, _ := laneIndexNum(flushes[0].key); got != 1 {
		t.Fatalf("block 2 flushed before block 1: %q", flushes[0].key)
	}
}

func TestIssue48_HoldIsLive(t *testing.T) {
	// A partial prefix can always still be completed, so it is held.
	if !holdIsLive("Antigravity", 5) {
		t.Error("a partial match must stay live")
	}
	// A complete word-final match still needs the byte after it to prove the
	// right boundary, so it is held.
	if !holdIsLive("Antigravity", len("Antigravity")) {
		t.Error("a complete word-final match must stay live until its right boundary is seen")
	}
	// A complete match ending in a non-word byte is already boundary-valid at
	// end of text, so holding it defers output for nothing.
	if holdIsLive("https://example.com/", len("https://example.com/")) {
		t.Error("a complete non-word-final match must be emitted, not held")
	}
	// Out-of-range lengths are never live.
	if holdIsLive("omp", 0) || holdIsLive("omp", 4) {
		t.Error("out-of-range hold lengths must not be live")
	}
}

// TestIssue48_NoHoldWhenMatchIsAlreadyBoundaryValid proves the liveness rule
// end to end: a mapping whose last byte is not a word byte is replaced in the
// same chunk instead of being held for a flush.
func TestIssue48_NoHoldWhenMatchIsAlreadyBoundaryValid(t *testing.T) {
	lane := &brandLane{}
	out, changed := applyBrandLaneSet("see https://docs.example.com/", lane,
		[]rewriteMapping{{Match: "https://docs.example.com/", Replacement: "https://claude.ai"}})
	if lane.carry != "" {
		t.Fatalf("already-valid match was held: carry=%q out=%q", lane.carry, out)
	}
	if !changed || !strings.Contains(out, "https://claude.ai") {
		t.Fatalf("out=%q changed=%v, want the replacement emitted immediately", out, changed)
	}
}

// TestIssue48_PathCarryStillReassembles covers the path-mapping case: a lone
// "." IS a live prefix of ".gemini/GEMINI.md", so it stays held across the
// chunk split and the two halves must come back as one token.
func TestIssue48_PathCarryStillReassembles(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	sess := newBrandTestSession(t, "issue48-path", "claude_code")

	if _, changed := applyReverseBrandLanes(sess, "anthropic:0", "config at /home/u/.gemini"); changed {
		t.Fatal("holding a live prefix must not report a replacement yet")
	}
	lane := sess.brandCarries["anthropic:0\x00.gemini/GEMINI.md"]
	if lane == nil || lane.carry != ".gemini" {
		t.Fatalf("expected the .gemini prefix to be held, got %+v", lane)
	}
	out, changed := applyReverseBrandLanes(sess, "anthropic:0", "/GEMINI.md done")
	if !changed {
		t.Fatal("completing the path must report a change")
	}
	// The path is completed and reversed as one token, not split in two.
	if out != ".claude/CLAUDE.md done" {
		t.Fatalf("out = %q, want the completed path reversed", out)
	}
	if lane.carry != "" {
		t.Fatalf("carry not drained: %q", lane.carry)
	}
}

// TestIssue48_StreamDeliversEveryHeldToken drives the real entry point: text
// split across chunks, held at the chunk boundary, must arrive complete and in
// order once the block closes.
func TestIssue48_StreamDeliversEveryHeldToken(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue48-stream", "claude_code", ompUncloakCache(t))

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
		RequestID: "issue48-stream", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"by Google Deepmin\"}}\n\n"),
	}, "anthropic"))
	collect(mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue48-stream", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"d per GEMINI.md\"}}\n\n"),
	}, "anthropic"))
	collect(mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue48-stream", SourceFormat: "anthropic", ChunkIndex: 2,
		Body: []byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
	}, "anthropic"))

	if got := delivered.String(); got != "by Anthropic per CLAUDE.md" {
		t.Fatalf("stream delivered %q, want the fully reversed sentence", got)
	}
}

// TestIssue48_ProseAndArgumentHoldsFlushInArrivalOrder drives the real entry
// point for the case that actually occurs: one content block whose prose and
// whose tool-call arguments both hold text. Reverse-table order would flush the
// prose first here, because "Antigravity" sorts before the argument lane's
// key; the arrival stamp flushes them the way the model produced them.
func TestIssue48_ProseAndArgumentHoldsFlushInArrivalOrder(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue48-prose-args", "claude_code", ompUncloakCache(t))

	// The ARGUMENTS arrive first and hold ".gem"; the prose arrives second and
	// holds ".gem" too, so both lanes of block 0 hold at the terminal flush.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue48-prose-args", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"view_file\",\"input\":{}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"p\\\":\\\"/home/u/.gem\"}}\n\n"),
	}, "anthropic")
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue48-prose-args", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"dir /home/u/.gem\"}}\n\n"),
	}, "anthropic")
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue48-prose-args", SourceFormat: "anthropic", ChunkIndex: 2,
		Body: []byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
	}, "anthropic")

	var order []string
	for _, f := range sseFrames(t, resp.Body) {
		delta, _ := f.data["delta"].(map[string]any)
		switch delta["type"] {
		case "input_json_delta":
			order = append(order, "args")
		case "text_delta":
			order = append(order, "prose")
		}
	}
	if len(order) != 2 {
		t.Fatalf("want both lanes flushed, got %v (body=%q)", order, resp.Body)
	}
	if order[0] != "args" || order[1] != "prose" {
		t.Fatalf("flush order %v, want the arguments the model produced first", order)
	}
}
