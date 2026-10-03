package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Ticket #47: a synthetic Anthropic carry flush is a real Anthropic Messages
// SSE event, not a bare data frame. Anthropic's own stream names every event
// with an `event:` line; a client that dispatches on that name and never parses
// the payload would silently drop the recovered text of a data-only flush.

// parseSSEFrame asserts that raw is a single, correctly named Anthropic event
// and returns its name and payload. It is deliberately strict about the
// `event:` line: a flush delivered as a bare data frame fails here.
func parseSSEFrame(t *testing.T, raw string) (name string, data map[string]any) {
	t.Helper()
	frames := sseFrames(t, []byte(raw))
	if len(frames) != 1 {
		t.Fatalf("want one event, got %d in %q", len(frames), raw)
	}
	if frames[0].name != "content_block_delta" {
		t.Fatalf("flush frame lost its event name: %q in %q", frames[0].name, raw)
	}
	return frames[0].name, frames[0].data
}

func TestIssue47_BuildBrandFlushEvent_AnthropicCarriesEventNameAndIndex(t *testing.T) {
	// A per-token lane key ("anthropic:3\x00Antigravity") must still report the
	// content block it belongs to, not block 0.
	ev := buildBrandFlushEvent("anthropic", "anthropic:3\x00Antigravity", "omp")
	name, data := parseSSEFrame(t, string(ev))
	if name != "content_block_delta" {
		t.Fatalf("event name = %q", name)
	}
	if idx, _ := data["index"].(float64); int(idx) != 3 {
		t.Fatalf("index = %v, want 3 (payload=%s)", data["index"], ev)
	}
	delta := data["delta"].(map[string]any)
	if delta["text"] != "omp" {
		t.Fatalf("delta text = %v, want omp", delta["text"])
	}

	// A bare (Oh My Pi) lane key reports its own block index too.
	_, data = parseSSEFrame(t, string(buildBrandFlushEvent("anthropic", "anthropic:2", "omp")))
	if idx, _ := data["index"].(float64); int(idx) != 2 {
		t.Fatalf("bare lane index = %v, want 2", data["index"])
	}

	// An unparsable key pins to lane 0 rather than dropping the text.
	_, data = parseSSEFrame(t, string(buildBrandFlushEvent("anthropic", "garbage", "omp")))
	if idx, _ := data["index"].(float64); int(idx) != 0 {
		t.Fatalf("unparsable lane index = %v, want 0", data["index"])
	}
}

func TestIssue47_BuildBrandFlushEvent_OpenAIKeepsDataOnlyFrame(t *testing.T) {
	ev := string(buildBrandFlushEvent("openai", "openai:1", "hello"))
	if strings.Contains(ev, "event:") {
		t.Fatalf("openai chat-completions frames carry no event name, got %q", ev)
	}
	if !strings.HasPrefix(ev, "data: {") || !strings.HasSuffix(ev, "\n\n") {
		t.Fatalf("openai flush is not a terminated data frame: %q", ev)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(ev, "data: "), "\n\n")), &m); err != nil {
		t.Fatalf("openai flush payload: %v", err)
	}
	choices := m["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v", m["choices"])
	}
	ch := choices[0].(map[string]any)
	if idx, _ := ch["index"].(float64); int(idx) != 1 {
		t.Fatalf("choice index = %v, want 1", ch["index"])
	}
}

func TestIssue47_AnthropicSSE_BlockStopFlushIsProtocolValid(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue47-sse", "oh_my_pi", ompUncloakCache(t))

	// "Anti" is a live prefix of the protected brand token, so the lane holds it
	// and the block-stop flush has to hand the text back to the client.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue47-sse", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"see Anti\"}}\n\n"),
	}, "anthropic")
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue47-sse", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"),
	}, "anthropic")

	frames := sseFrames(t, resp.Body)
	var flushFrames, stopFrames []sseFrame
	for _, f := range frames {
		switch f.data["type"] {
		case "content_block_delta":
			flushFrames = append(flushFrames, f)
		case "content_block_stop":
			stopFrames = append(stopFrames, f)
		}
	}
	if len(flushFrames) != 1 {
		t.Fatalf("want exactly one flush frame, got %d (body=%q)", len(flushFrames), resp.Body)
	}
	f := flushFrames[0]
	if f.name != "content_block_delta" {
		t.Fatalf("flush frame lost its event name: %q", f.name)
	}
	if idx, _ := f.data["index"].(float64); int(idx) != 1 {
		t.Fatalf("flush frame index = %v, want 1 (the lane's own block)", f.data["index"])
	}
	delta := f.data["delta"].(map[string]any)
	if delta["text"] != "Anti" {
		t.Fatalf("flush text = %v, want the held Anti", delta["text"])
	}
	// The recovered text must reach the client before the block closes.
	if len(stopFrames) != 1 {
		t.Fatalf("content_block_stop lost, body=%q", resp.Body)
	}
	if !strings.HasPrefix(string(resp.Body), "event: content_block_delta\n") {
		t.Fatalf("flush must be the first event, body=%q", resp.Body)
	}
}

func TestIssue47_AnthropicStandalone_CompositeFlushIsProtocolValid(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue47-standalone", "oh_my_pi", ompUncloakCache(t))

	// A standalone terminal payload has no delta to merge into, so the flush is
	// emitted as its own event ahead of the terminal data line.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue47-standalone", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"see Anti"}}`),
	}, "anthropic")
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue47-standalone", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(`{"type":"message_stop"}`),
	}, "anthropic")

	frames := sseFrames(t, resp.Body)
	if len(frames) != 2 {
		t.Fatalf("want flush + terminal, got %d frames (body=%q)", len(frames), resp.Body)
	}
	if frames[0].name != "content_block_delta" || frames[0].data["type"] != "content_block_delta" {
		t.Fatalf("first frame is not a named content_block_delta: %+v", frames[0])
	}
	if frames[1].name != "" || frames[1].data["type"] != "message_stop" {
		t.Fatalf("terminal frame lost or reordered: %+v", frames[1])
	}
	// Every frame must be a terminated SSE event, including the last one.
	if !strings.HasSuffix(string(resp.Body), "\n\n") {
		t.Fatalf("composite body must end with a frame terminator, got %q", resp.Body)
	}
	// No doubled separators between frames.
	if strings.Contains(string(resp.Body), "\n\n\n\n") {
		t.Fatalf("composite body has doubled frame separators: %q", resp.Body)
	}
}
