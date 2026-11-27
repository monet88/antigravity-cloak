package cloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// uncloakStreamChunk uses pre-compiled regex to replace tool names directly in
// raw SSE bytes. Callers MUST pass complete SSE events (assembled by the event
// reassembly buffer) to guarantee that tool names are never split across calls.
func uncloakStreamChunk(body []byte, cached *cachedUncloakPattern) ([]byte, bool) {
	if cached == nil || cached.re == nil {
		return nil, false
	}
	if cached.exactOnly {
		return uncloakStreamChunkExact(body, cached.lookup)
	}

	// Find all matches of "name":"<target_tool_name>" and replace with originals.
	// Uses FindAllSubmatchIndex to extract the tool name capture group and look
	// it up in the uncloak table for precise replacement.
	bodyStr := string(body)
	matches := cached.re.FindAllStringSubmatchIndex(bodyStr, -1)
	if len(matches) == 0 {
		return nil, false
	}

	var buf strings.Builder
	buf.Grow(len(bodyStr))
	lastEnd := 0
	changed := false

	for _, loc := range matches {
		// FindAllStringSubmatchIndex returns 2*(1+groups) ints; guard the
		// capture-group slice indices before slicing to avoid a panic on any
		// unexpected match shape (e.g. an optional group that did not match).
		if len(loc) < 4 || loc[2] < 0 || loc[3] < 0 {
			continue
		}
		// loc[2]:loc[3] is capture group 1 (the tool name)
		toolName := bodyStr[loc[2]:loc[3]]
		orig, ok := lookupUncloak(toolName, cached.lookup)
		if ok {
			buf.WriteString(bodyStr[lastEnd:loc[2]])
			buf.WriteString(orig)
			lastEnd = loc[3]
			changed = true
		}
	}

	if !changed {
		return nil, false
	}

	buf.WriteString(bodyStr[lastEnd:])
	return []byte(buf.String()), true
}

func uncloakStreamChunkExact(body []byte, uncloakTable map[string]string) ([]byte, bool) {
	rewriteJSON := func(payload []byte) ([]byte, bool) {
		if modified, changed := uncloakResponseBodyExact(payload, uncloakTable, "openai"); changed {
			return modified, true
		}
		return uncloakResponseBodyExact(payload, uncloakTable, "anthropic")
	}

	if modified, changed := rewriteJSON(bytes.TrimSpace(body)); changed {
		return modified, true
	}

	events := splitSSEEventsForBrand(body)
	var out bytes.Buffer
	changedOverall := false
	for _, ev := range events {
		lines := strings.Split(string(ev), "\n")
		changedEvent := false
		for i, line := range lines {
			hasCR := strings.HasSuffix(line, "\r")
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			modified, changed := rewriteJSON([]byte(payload))
			if !changed {
				continue
			}
			lines[i] = "data: " + string(modified)
			if hasCR {
				lines[i] += "\r"
			}
			changedEvent = true
		}
		if changedEvent {
			out.WriteString(strings.Join(lines, "\n"))
			changedOverall = true
		} else {
			out.Write(ev)
		}
	}
	if !changedOverall {
		return nil, false
	}
	return out.Bytes(), true
}

// reverseBrandSSE maps model output back onto the client's own spelling. Both
// reverse authorities share this loop - event splitting, the terminal flush
// discipline, and the per-event rewrite; only the applier and the lane flusher
// differ, and brandReverseFuncs picks those once per session.
//
// Oh My Pi runs the protected-brand policy (Antigravity -> omp) on a single
// lane; every other client runs its own reverse table, one lane per token, so a
// match split across two SSE events is held until it completes.
func (m *streamSessionManager) reverseBrandSSE(sess *streamSession, sseBytes []byte, format string) ([]byte, bool) {
	if sess == nil {
		return nil, false
	}
	applyEvent, flushLanes := m.brandReverseFuncs(sess, format)

	events := splitSSEEventsForBrand(sseBytes)
	var out bytes.Buffer
	changedOverall := false
	for _, ev := range events {
		if sseContainsOpenAIDone(ev) {
			// The stream ends here, so any token still held in a lane is never
			// going to complete. Flush it rather than dropping it: losing the
			// tail of a sentence is far worse than emitting it unreversed.
			if fe := flushLanes("", true, nil); fe != nil {
				out.Write(fe)
				changedOverall = true
			}
			out.Write(ev)
			continue
		}
		// Anthropic native termination: flush pending carry before the
		// terminal control event using a protocol-valid assistant text delta
		// that preserves lane identity. content_block_stop flushes its block
		// lane; message_stop flushes all remaining lanes. The session must
		// not be deleted before this carry is delivered (Issue #21).
		if format == "anthropic" {
			if kind, laneKey := sseAnthropicTerminalKind(ev); kind != "" {
				// The lane a held token belongs to is complete by now, so it
				// must be emitted before the stop event.
				if fe := flushLanes(laneKey, kind == "message_stop", nil); fe != nil {
					out.Write(fe)
					changedOverall = true
				}
			}
		}
		// OpenAI termination: a non-null finish_reason ends ONLY its own choice.
		// That choice's prose and tool-argument lanes are flushed into the
		// stream BEFORE the event carrying the finish_reason - a client may
		// finalize the choice the moment it reads it, so a carry delivered after
		// can be dropped and the argument arrives truncated - and the choice is
		// marked terminal for this event (see streamSession.terminalChoices), so
		// a partial token its OWN delta ends on resolves here rather than being
		// carried past the finish_reason to the end of the stream. Choices still
		// streaming are left alone; [DONE] flushes what remains.
		//
		// The lanes that pre-flush must skip are the ones THIS event appends
		// bytes to. Flushing one of those first delivers the partial carry raw
		// and drains the lane, so the remainder arriving in this event can no
		// longer complete the token: a model that streams "Ant" and then
		// "igravity" with the finish_reason would hand the client the cloaked
		// word instead of the client's own. Those lanes resolve inline instead,
		// in the field their text arrived in, because the mark below also makes
		// them terminal.
		markedTerminal := false
		if format == "openai" && openAIMayCarryFinishReason(ev) {
			for _, data := range sseEventDataMaps(ev) {
				for _, ch := range sliceOfMaps(data["choices"]) {
					if fr, has := ch["finish_reason"]; !has || fr == nil {
						continue
					}
					laneKey := openAIChoiceLaneKey(ch)
					continued := openAIChoiceContinuedLanes(ch)
					if fe := flushLanes(laneKey, false, continued); fe != nil {
						out.Write(fe)
						changedOverall = true
					}
					if sess.terminalChoices == nil {
						sess.terminalChoices = map[string]bool{}
					}
					sess.terminalChoices[laneKey] = true
					markedTerminal = true
				}
			}
		}
		modifiedEv, changed := rewriteSSEEventData(ev, applyEvent)
		if markedTerminal {
			sess.terminalChoices = nil
		}
		if changed {
			changedOverall = true
		}
		out.Write(modifiedEv)
	}
	return out.Bytes(), changedOverall
}

// brandReverseFuncs returns the per-event applier and the lane flusher for the
// session's reverse authority. flushLanes(laneKey, allBlocks, skip) emits
// whatever the selected lanes still hold, or nil when nothing is pending. A key
// in skip is left untouched: it belongs to a lane the event being processed is
// already appending to, so its carry still has a chance to complete there.
func (m *streamSessionManager) brandReverseFuncs(sess *streamSession, format string) (func(map[string]any) bool, func(string, bool, map[string]bool) []byte) {
	if sess.client != "oh_my_pi" {
		return func(data map[string]any) bool {
				return m.reverseCloakedBrandStreamingMap(data, sess, format)
			}, func(laneKey string, allBlocks bool, skip map[string]bool) []byte {
				return m.reverseFlushCloakedBrandLanes(sess, format, laneKey, allBlocks, skip)
			}
	}
	applyEvent := func(data map[string]any) bool {
		switch format {
		case "openai":
			return m.reverseBrandOpenAIStreamingMap(data, sess)
		case "anthropic":
			return m.reverseBrandAnthropicStreamingMap(data, sess)
		}
		return false
	}
	flushLanes := func(laneKey string, allBlocks bool, skip map[string]bool) []byte {
		only := []string{laneKey}
		if allBlocks {
			only = nil
		}
		var out []byte
		for _, fe := range m.generateBrandFlushEventsFiltered(sess, format, only, skip) {
			out = append(out, fe...)
		}
		return out
	}
	return applyEvent, flushLanes
}

// reverseCloakedBrandStreamingMap applies the reverse mappings to every text
// field the protocol exposes in a single streaming event.
func (m *streamSessionManager) reverseCloakedBrandStreamingMap(data map[string]any, sess *streamSession, format string) bool {
	changed := false
	// Every field is rewritten against its own lane. The lane key has to match
	// what the terminal flush derives from the same event (sseAnthropicTerminalKind
	// for a content block, openAIChoiceLaneKey for a choice), or the flush looks
	// for lanes that do not exist and a held token is dropped.
	apply := func(m map[string]any, key, laneKey string) {
		txt, ok := m[key].(string)
		if !ok || txt == "" {
			return
		}
		// next != txt is the trigger, not the changed flag: when a lane holds a
		// trailing partial token the remainder is stripped from the text, and
		// that edit has to reach the client even though nothing was replaced
		// yet. Keeping the original here would send the held token inline and
		// then send it a second time from the flush.
		if next, _ := applySemanticBrandLane(sess, laneKey, txt); next != txt {
			m[key] = next
			changed = true
		}
	}
	if format == "anthropic" {
		laneKey := laneKeyWithIndex("anthropic", data)
		if data["type"] == "content_block_start" {
			// Issue #55 Contract B: a named eligible tool call arms the block's
			// argument tier lane. Name and arguments can arrive separately;
			// a completed input object is patched directly here.
			if cb, ok := data["content_block"].(map[string]any); ok {
				if sess.armTierContentBlock(toolArgsLaneKey(laneKey), cb) {
					changed = true
				}
			}
		}
		apply(data, "text", laneKey)
		if cb, ok := data["content_block"].(map[string]any); ok {
			apply(cb, "text", laneKey)
		}
		if delta, ok := data["delta"].(map[string]any); ok {
			apply(delta, "text", laneKey)
			// Tool call arguments stream as raw JSON fragments. They carry the
			// same cloaked tokens as the prose, and anything the model writes to
			// disk arrives through here, so leaving them untouched persisted
			// cloaked text into the user's files. The fragments are treated as
			// plain strings, the same way uncloakStreamChunkExact already treats
			// tool names inside them — JSON-escaped, which is why the path rules
			// in the reverse tables also carry their escaped spelling.
			// Its own lane, not the block's: prose and arguments are different
			// carriers, so a carry held in one must never flush through the other.
			apply(delta, "partial_json", toolArgsLaneKey(laneKey))
		}
		return changed
	}
	// OpenAI: the content index lives on each choice, never on the event root.
	// Reading the root alone put every choice in the "openai:0" lane, so a
	// token held back for choice 0 was emitted through choice 1 and the two
	// choices' text interleaved (choice 0 "A" + choice 1 "hello " came out as
	// "" + "Ahello ").
	rootLane := laneKeyWithIndex("openai", data)
	apply(data, "text", rootLane)
	choices, _ := data["choices"].([]any)
	for _, cRaw := range choices {
		choice, ok := cRaw.(map[string]any)
		if !ok {
			continue
		}
		laneKey := openAIChoiceLaneKey(choice)
		if delta, ok := choice["delta"].(map[string]any); ok {
			if content, exists := delta["content"]; exists {
				if next, c := applyOpenAIProseContent(sess, laneKey, content); c {
					delta["content"] = next
					changed = true
				}
			}
			// Chat Completions streams tool-call arguments as
			// choices[].delta.tool_calls[].function.arguments, the carrier
			// Anthropic calls partial_json. They carry the same text, so
			// reversing one and not the other left a .gemini path or an
			// antigravity.google URL inside the arguments of every
			// OpenAI-compatible client. Each call gets its own lane, because
			// one choice streams several calls' fragments side by side and a
			// shared lane would interleave them.
			for _, tRaw := range sliceOfMaps(delta["tool_calls"]) {
				fn, _ := tRaw["function"].(map[string]any)
				if fn == nil {
					continue
				}
				callLane := openAIToolCallLaneKey(choice, tRaw)
				// The tier lane was armed from the pre-uncloak wire name.
				apply(fn, "arguments", callLane)
			}
		}
		// The final standalone chunk of some providers carries the whole turn as
		// a message instead of a delta, the same shape the Oh My Pi applier
		// already accepts.
		if msg, ok := choice["message"].(map[string]any); ok {
			if content, exists := msg["content"]; exists {
				if next, c := applyOpenAIProseContent(sess, laneKey, content); c {
					msg["content"] = next
					changed = true
				}
			}
			for _, tRaw := range sliceOfMaps(msg["tool_calls"]) {
				fn, _ := tRaw["function"].(map[string]any)
				if fn == nil {
					continue
				}
				callLane := openAIToolCallLaneKey(choice, tRaw)
				apply(fn, "arguments", callLane)
			}
		}
	}
	return changed
}

// toolArgsLaneParts splits a tool-argument lane key back into the content block
// or choice it belongs to and the argument index within it. It reports false
// for an ordinary prose lane.
//
// This is also THE rule for delivering a recovered carry: every flush and every
// standalone merge routes through it, so a held argument fragment always goes
// back as an input_json_delta or a tool_call argument, never as assistant
// text. Sending it through the text carrier would print the recovered bytes in
// the message and leave the call truncated.
func toolArgsLaneParts(laneKey string) (blockLaneKey string, argIndex int, ok bool) {
	i := strings.Index(laneKey, reverseBrandLaneSuffix)
	if i < 0 {
		return laneKey, 0, false
	}
	blockLaneKey = laneKey[:i]
	tail := laneKey[i+len(reverseBrandLaneSuffix):]
	// The third part of the key is the per-mapping lane the reverse table runs
	// in; only the marker just behind the suffix names the carrier.
	if j := strings.Index(tail, reverseBrandLaneSuffix); j >= 0 {
		tail = tail[:j]
	}
	switch {
	case tail == toolArgsLaneMarker:
		return blockLaneKey, 0, true
	case strings.HasPrefix(tail, "tool"):
		n, err := strconv.Atoi(strings.TrimPrefix(tail, "tool"))
		if err != nil {
			return blockLaneKey, 0, false
		}
		return blockLaneKey, n, true
	}
	return blockLaneKey, 0, false
}

// toolArgsLaneMarker names the lane that carries a content block's streamed
// tool-call ARGUMENTS, as opposed to its prose. The two are different fields of
// the same event, so they need different lanes and different flush carriers.
const toolArgsLaneMarker = "args"

// toolArgsLaneKey is the arguments lane of one content block.
func toolArgsLaneKey(blockLaneKey string) string {
	return blockLaneKey + reverseBrandLaneSuffix + toolArgsLaneMarker
}

// openAIToolCallLaneKey derives the brand lane key for one streamed tool call:
// the choice it belongs to plus the call's own index after the lane suffix, so
// two calls of the same choice never share a carry and the flush event still
// reports the choice index the client dispatches on.
func openAIToolCallLaneKey(choice, toolCall map[string]any) string {
	idx := 0
	if n, ok := jsonIndexValue(toolCall["index"]); ok {
		idx = n
	}
	return openAIChoiceLaneKey(choice) + reverseBrandLaneSuffix + "tool" + strconv.Itoa(idx)
}

// openAIContentAppendsProse reports whether the applier's content path would
// append at least one non-empty text fragment from this OpenAI `content` value
// into the choice's prose lane. It is the ONE shape test for that path: the
// applier gates its content branch on it and openAIChoiceContinuedLanes uses it
// to decide which lanes a terminal event still feeds, so a shape the applier can
// append to can never be missing from the pre-flush skip set - and a shape it
// cannot append to can never keep a carry inside the lane past its own
// finish_reason, waiting for a [DONE] that the client has already passed.
//
// It is expressed through walkOpenAITextParts with an identity rewrite, so the
// set of shapes it accepts cannot drift from the set the applier mutates: the
// probe visits exactly the strings the applier would, and returning each string
// unchanged is what keeps the walk from writing anything.
func openAIContentAppendsProse(content any) bool {
	appends := false
	walkOpenAITextParts(content, func(txt string) string {
		if txt != "" {
			appends = true
		}
		return txt
	})
	return appends
}

// applyOpenAIProseContent reverses a choice's `content` value in place, in
// every shape the lane accepts, and reports whether it changed. It is the ONE
// content mutator: both appliers (the Oh My Pi one and the shared one) route
// their content through it, and openAIContentAppendsProse gates it, so the lane
// a terminal event is judged to continue is exactly the lane this appends to.
func applyOpenAIProseContent(sess *streamSession, laneKey string, content any) (any, bool) {
	if !openAIContentAppendsProse(content) {
		return content, false
	}
	return walkOpenAITextParts(content, func(txt string) string {
		next, _ := applySemanticBrandLane(sess, laneKey, txt)
		return next
	})
}

// openAIChoiceContinuedLanes returns the lane keys a choice's own payload in the
// event being processed will append bytes to: the choice's prose lane and the
// argument lane of every tool call whose arguments arrive non-empty. These are
// exactly the lanes reverseCloakedBrandStreamingMap applies - both the prose
// check and the applier's content branch go through openAIContentAppendsProse -
// so a lane listed here must not be pre-flushed at that choice's finish_reason:
// its carry has to stay in the lane and combine with the fragment that is
// already on its way. Choice payloads arrive as a delta or, on a final chunk, as
// a whole message, so both are inspected, the same way the applier inspects both.
func openAIChoiceContinuedLanes(choice map[string]any) map[string]bool {
	var out map[string]bool
	mark := func(key string) {
		if out == nil {
			out = map[string]bool{}
		}
		out[key] = true
	}
	for _, field := range [2]string{"delta", "message"} {
		body, _ := choice[field].(map[string]any)
		if body == nil {
			continue
		}
		if openAIContentAppendsProse(body["content"]) {
			mark(openAIChoiceLaneKey(choice))
		}
		for _, call := range sliceOfMaps(body["tool_calls"]) {
			fn, _ := call["function"].(map[string]any)
			if fn == nil {
				continue
			}
			if args, _ := fn["arguments"].(string); args != "" {
				mark(openAIToolCallLaneKey(choice, call))
			}
		}
	}
	return out
}

// reverseFlushCloakedBrandLanes emits whatever a content block's lanes still
// hold once the block is known to be complete. Each lane flushes as its own
// event so the index survives. A lane in skip is held back: the event being
// processed is appending to it, so its carry may still complete there.
func (m *streamSessionManager) reverseFlushCloakedBrandLanes(sess *streamSession, format, laneKey string, allBlocks bool, skip map[string]bool) []byte {
	// The block's own lane is keyed exactly, and its tool-argument lane is that
	// key plus the carrier marker, so both have to be selected. Draining every
	// block matches on the bare prefix.
	suffix := laneKey + reverseBrandLaneSuffix
	prefix := suffix
	if allBlocks {
		prefix = ""
	}
	var out []byte
	for _, key := range sortedCarryKeys(sess) {
		if skip[key] {
			continue
		}
		if key != laneKey && !strings.HasPrefix(key, prefix) {
			continue
		}
		lane := sess.brandCarries[key]
		text := ""
		if lane != nil && lane.carry != "" {
			text = resolveLaneFlushText(sess, key, lane.carry, lane.lastIsWord)
			lane.setCarry("")
			lane.lastIsWord = false
		} else {
			// Issue #55 Contract B: a tier-pending-only lane still flushes its
			// withheld scalar here (raw when it never completed).
			text = resolveTierFlushText(sess, key, "")
		}
		if text == "" {
			continue
		}
		out = append(out, buildBrandFlushEvent(format, key, text)...)
	}
	return out
}

// buildBrandFlushEvent encodes one flushed lane as the protocol's terminal text
// delta, carrying the lane index so the client attaches the recovered text to
// the content block (Anthropic) or choice (OpenAI) the held bytes came from.
// An Anthropic flush is a real SSE event frame, `event:` plus `data:`, because
// that is how the Anthropic Messages stream itself frames every event: a
// client that dispatches on the SSE event name and never parses the payload
// would otherwise never deliver the recovered text. Returns nil for a format
// with no text-delta encoding.
func buildBrandFlushEvent(format, laneKey, text string) []byte {
	var frame string
	switch format {
	case "anthropic":
		payload, err := safeMarshal(map[string]any{
			"type":  "content_block_delta",
			"index": laneIndexNumOrZero(laneKey),
			"delta": anthropicFlushDelta(laneKey, text),
		})
		if err != nil {
			return nil
		}
		frame = "event: content_block_delta\ndata: " + string(payload) + "\n\n"
	case "openai":
		payload, err := safeMarshal(map[string]any{
			"choices": []any{map[string]any{"index": laneIndexNumOrZero(laneKey), "delta": openAIChoiceDelta(laneKey, text)}},
		})
		if err != nil {
			return nil
		}
		frame = "data: " + string(payload) + "\n\n"
	default:
		return nil
	}
	return []byte(frame)
}

// anthropicFlushDelta picks the carrier toolArgsLaneParts identified.
func anthropicFlushDelta(laneKey, text string) map[string]any {
	if _, _, isArgs := toolArgsLaneParts(laneKey); isArgs {
		return map[string]any{"type": "input_json_delta", "partial_json": text}
	}
	return map[string]any{"type": "text_delta", "text": text}
}

// openAIChoiceDelta picks the carrier toolArgsLaneParts identified.
func openAIChoiceDelta(laneKey, text string) map[string]any {
	_, argIndex, isArgs := toolArgsLaneParts(laneKey)
	if !isArgs {
		return map[string]any{"content": text}
	}
	return map[string]any{"tool_calls": []any{map[string]any{
		"index":    argIndex,
		"function": map[string]any{"arguments": text},
	}}}
}

// laneIndexNumOrZero is laneIndexNum with the unparsable-key case pinned to
// lane 0, so a flush never silently drops a block's recovered text.
func laneIndexNumOrZero(laneKey string) int {
	idx, _ := laneIndexNum(laneKey)
	return idx
}

func splitSSEEventsForBrand(data []byte) [][]byte {
	var events [][]byte
	start := 0
	for i := 0; i < len(data); {
		idx1 := bytes.Index(data[i:], []byte("\n\n"))
		idx2 := bytes.Index(data[i:], []byte("\r\n\r\n"))
		var idx int
		var blen int
		if idx1 >= 0 && idx2 >= 0 {
			if idx1 < idx2 {
				idx = idx1
				blen = 2
			} else {
				idx = idx2
				blen = 4
			}
		} else if idx1 >= 0 {
			idx = idx1
			blen = 2
		} else if idx2 >= 0 {
			idx = idx2
			blen = 4
		} else {
			events = append(events, data[start:])
			break
		}
		end := i + idx + blen
		events = append(events, data[start:end])
		start = end
		i = end
	}
	return events
}

// rewriteSSEEventData rewrites one SSE event by applying apply to every data
// payload in it, rebuilding the frame only when something changed.
func rewriteSSEEventData(ev []byte, apply func(map[string]any) bool) ([]byte, bool) {
	evStr := string(ev)
	lines := strings.Split(strings.ReplaceAll(evStr, "\r\n", "\n"), "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var dataMap map[string]any
		if err := safeUnmarshal([]byte(payload), &dataMap); err != nil {
			continue
		}
		if !apply(dataMap) {
			continue
		}
		newPayload, err := safeMarshal(dataMap)
		if err == nil {
			lines[i] = "data: " + string(newPayload)
			changed = true
		}
	}
	if !changed {
		return ev, false
	}
	return rebuildSSEEvent(ev, lines, evStr), true
}

// rebuildSSEEvent rejoins patched data lines into an SSE frame, preserving
// whichever line terminator the upstream event used.
func rebuildSSEEvent(ev []byte, lines []string, evStr string) []byte {
	rebuilt := strings.Join(lines, "\n")
	if !strings.HasSuffix(rebuilt, "\n\n") {
		if strings.HasSuffix(evStr, "\r\n\r\n") {
			rebuilt += "\r\n"
		}
		if !strings.HasSuffix(rebuilt, "\n\n") {
			if strings.HasSuffix(rebuilt, "\n") {
				rebuilt += "\n"
			} else {
				rebuilt += "\n\n"
			}
		}
	}
	return []byte(rebuilt)
}

func (m *streamSessionManager) reverseBrandOpenAIStreamingMap(data map[string]any, sess *streamSession) bool {
	choices, ok := data["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, chRaw := range choices {
		ch, ok := chRaw.(map[string]any)
		if !ok {
			continue
		}
		laneKey := openAIChoiceLaneKey(ch)
		var delta map[string]any
		if d, ok := ch["delta"].(map[string]any); ok {
			delta = d
		} else if d, ok := ch["message"].(map[string]any); ok {
			delta = d
		}
		if delta == nil {
			continue
		}
		// Streamed tool-call arguments: the Chat Completions twin of Anthropic's
		// input_json_delta. Each call gets its own lane, so two calls of one
		// choice never interleave, and the terminal flush sends the recovered
		// bytes back as an argument fragment rather than assistant text.
		for _, call := range sliceOfMaps(delta["tool_calls"]) {
			if fn, ok := call["function"].(map[string]any); ok {
				if args, ok := fn["arguments"].(string); ok {
					callLane := openAIToolCallLaneKey(ch, call)
					if newArgs, _ := applySemanticBrandLane(sess, callLane, args); newArgs != args {
						fn["arguments"] = newArgs
						changed = true
					}
				}
			}
		}
		if content, exists := delta["content"]; exists {
			if next, c := applyOpenAIProseContent(sess, laneKey, content); c {
				delta["content"] = next
				changed = true
			}
		}
	}
	return changed
}

func (m *streamSessionManager) reverseBrandAnthropicStreamingMap(data map[string]any, sess *streamSession) bool {
	laneKey := laneKeyWithIndex("anthropic", data)
	typ, _ := data["type"].(string)
	switch typ {
	case "content_block_start":
		cb, ok := data["content_block"].(map[string]any)
		if !ok {
			return false
		}
		if cb["type"] != "text" {
			return false
		}
		if txt, ok := cb["text"].(string); ok {
			newTxt, _ := applySemanticBrandLane(sess, laneKey, txt)
			if newTxt != txt {
				cb["text"] = newTxt
				return true
			}
		}
	case "content_block_delta":
		delta, ok := data["delta"].(map[string]any)
		if !ok {
			return false
		}
		switch delta["type"] {
		case "text_delta":
			if txt, ok := delta["text"].(string); ok {
				newTxt, _ := applySemanticBrandLane(sess, laneKey, txt)
				if newTxt != txt {
					delta["text"] = newTxt
					return true
				}
			}
		case "input_json_delta":
			// Tool-call arguments carry the same operational identifiers as the
			// prose, and the model writes them to disk, so they are restored in
			// their own lane: a hold here must never flush as assistant text.
			if txt, ok := delta["partial_json"].(string); ok {
				newTxt, _ := applySemanticBrandLane(sess, toolArgsLaneKey(laneKey), txt)
				if newTxt != txt {
					delta["partial_json"] = newTxt
					return true
				}
			}
		}
	}
	return false
}

type brandFlush struct {
	key  string
	text string
}

// laneIndexNum parses the numeric lane index from a carry key. A child carrier
// lane appends its own marker after the separator ("openai:0\x00tool0", an
// Anthropic block's "anthropic:0\x00args"), so only the part before the
// separator is the lane index. Parsing the whole tail made every such lane
// report index 0 and sent the flush events out under the wrong content block.
func laneIndexNum(key string) (int, bool) {
	i := strings.LastIndexByte(key, ':')
	if i < 0 {
		return 0, false
	}
	num := key[i+1:]
	if j := strings.IndexByte(num, reverseBrandLaneSuffix[0]); j >= 0 {
		num = num[:j]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, false
	}
	return n, true
}

// sortedCarryKeys returns the key of every lane holding a non-empty carry, in
// deterministic lane order: numeric lane indices ascending, then the order the
// lanes were opened. Go map iteration would otherwise randomise the cross-lane
// flush sequence; Issue #18 requires stable event/lane ordering while keeping
// distinct indexed lanes isolated.
//
// Lanes of the SAME content block are ordered by brandLane.seq, the order their
// text arrived in. Tie-breaking those by key string instead made the flush
// order follow reverse-table declaration order, so two held tokens from one
// block could reach the client transposed (Issue #48).
func sortedCarryKeys(sess *streamSession) []string {
	keys := make([]string, 0, len(sess.brandCarries))
	for k, lane := range sess.brandCarries {
		if lane == nil || lane.carry == "" {
			continue
		}
		keys = append(keys, k)
	}
	// Issue #55 Contract B: a tier lane that withholds a pending scalar is a
	// flush candidate even when its brand carry is empty, so the held bytes
	// reach the client in their argument carrier before a terminal.
	for k, tierLane := range sess.tierLanes {
		if tierLane == nil || tierLane.scan == nil || !tierLane.scan.hasPendingHold() {
			continue
		}
		if lane := sess.brandCarries[k]; lane != nil && lane.carry != "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ni, okI := laneIndexNum(keys[i])
		nj, okJ := laneIndexNum(keys[j])
		if okI != okJ {
			return okI
		}
		if ni != nj {
			return ni < nj
		}
		si, sj := laneArrivalSeq(sess, keys[i]), laneArrivalSeq(sess, keys[j])
		if si != sj {
			return si < sj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// laneArrivalSeq reads the order in which a lane first began holding bytes:
// its brand carry if it holds one, otherwise the tier scanner's own stamp.
// Both are recorded against the same session counter, so the two kinds of hold
// compare directly.
func laneArrivalSeq(sess *streamSession, key string) int {
	seq := 0
	if lane := sess.brandCarries[key]; lane != nil && lane.carry != "" {
		seq = lane.seq
	}
	if tierLane := sess.tierLanes[key]; tierLane != nil && tierLane.seq != 0 && (seq == 0 || tierLane.seq < seq) {
		seq = tierLane.seq
	}
	return seq
}

// orderedBrandFlushes resolves the final text of every held carry in
// deterministic lane order (sortedCarryKeys).
func orderedBrandFlushes(sess *streamSession) []brandFlush {
	return orderedBrandFlushesFor(sess, nil)
}

// orderedBrandFlushesFor resolves only the lanes keep admits. Resolution is NOT
// state-free: releasing a tier lane's withheld scalar clears its hold, so a
// lane resolved here and then dropped by a caller's filter would lose those
// bytes while their brand carry stayed undrained. Filtered flush sites must
// therefore pass their predicate here instead of filtering the result.
func orderedBrandFlushesFor(sess *streamSession, keep func(string) bool) []brandFlush {
	keys := sortedCarryKeys(sess)
	flushes := make([]brandFlush, 0, len(keys))
	for _, k := range keys {
		if keep != nil && !keep(k) {
			continue
		}
		text := ""
		if lane := sess.brandCarries[k]; lane != nil && lane.carry != "" {
			text = resolveLaneFlushText(sess, k, lane.carry, lane.lastIsWord)
		} else {
			// Tier-pending-only lane: the withheld scalar resolves (or is
			// released raw) with no brand text to combine.
			text = resolveTierFlushText(sess, k, "")
		}
		if text == "" {
			continue
		}
		flushes = append(flushes, brandFlush{key: k, text: text})
	}
	return flushes
}

func drainBrandFlushes(sess *streamSession, flushes []brandFlush) {
	for _, f := range flushes {
		lane := sess.brandCarries[f.key]
		if lane == nil {
			continue
		}
		lane.setCarry("")
		lane.lastIsWord = isWordByte(f.text[len(f.text)-1])
	}
}

func (m *streamSessionManager) generateBrandFlushEvents(sess *streamSession, format string) [][]byte {
	return m.generateBrandFlushEventsFiltered(sess, format, nil, nil)
}

func hasPendingBrandCarry(sess *streamSession) bool {
	if sess == nil {
		return false
	}
	for _, lane := range sess.brandCarries {
		if lane != nil && lane.carry != "" {
			return true
		}
	}
	// Issue #55 Contract B: a withheld tier scalar is pending too — the session
	// must stay alive until a later fragment resolves it or a terminal releases
	// it raw.
	return hasPendingTierHold(sess)
}

// anthropicTerminalKindFromMap reports whether an Anthropic data map is part
// of the native terminal lifecycle. Returns kind "content_block_stop" (with
// lane key) or "message_stop", or "" if not terminal.
func anthropicTerminalKindFromMap(m map[string]any) (kind, laneKey string) {
	t, _ := m["type"].(string)
	switch t {
	case "content_block_stop":
		idx := 0
		if v, ok := m["index"]; ok {
			if n, ok := jsonIndexValue(v); ok {
				idx = n
			} else if f, ok := v.(float64); ok {
				idx = int(f)
			} else if i, ok := v.(int); ok {
				idx = i
			}
		}
		return "content_block_stop", fmt.Sprintf("anthropic:%d", idx)
	case "message_stop":
		return "message_stop", ""
	default:
		return "", ""
	}
}

// sseAnthropicTerminalKind scans raw SSE bytes for an Anthropic terminal
// control event. Returns kind and laneKey (only for content_block_stop).
func sseAnthropicTerminalKind(ev []byte) (kind, laneKey string) {
	// Without a literal terminal marker, only a JSON escape could decode to one.
	if !bytes.Contains(ev, []byte("content_block_stop")) && !bytes.Contains(ev, []byte("message_stop")) && bytes.IndexByte(ev, '\\') < 0 {
		return "", ""
	}
	for _, m := range sseEventDataMaps(ev) {
		if k, lk := anthropicTerminalKindFromMap(m); k != "" {
			return k, lk
		}
	}
	return "", ""
}

func sseContainsAnthropicMessageStop(sse []byte) bool {
	// Keep escaped terminal types on the exact parser path above.
	if !bytes.Contains(sse, []byte("message_stop")) && bytes.IndexByte(sse, '\\') < 0 {
		return false
	}
	for _, ev := range splitSSEEventsForBrand(sse) {
		if k, _ := sseAnthropicTerminalKind(ev); k == "message_stop" {
			return true
		}
	}
	return false
}

// sseOpenAIDoneIndex returns the byte offset of the start of the `data: [DONE]`
// line in an SSE stream chunk, or -1 if no structural [DONE] frame is present.
// It verifies that [DONE] is the exact payload of an actual SSE data field,
// rather than a substring inside JSON content or tool-call arguments.
func sseOpenAIDoneIndex(sse []byte) int {
	if !bytes.Contains(sse, []byte("[DONE]")) {
		return -1
	}
	data := sse
	offset := 0
	for offset < len(sse) {
		lineStart := offset
		idx := bytes.IndexByte(data, '\n')
		var line []byte
		if idx >= 0 {
			line = data[:idx]
			data = data[idx+1:]
			offset += idx + 1
		} else {
			line = data
			offset += len(data)
			data = nil
		}
		lineNoCR := bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(lineNoCR)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			payload := bytes.TrimSpace(trimmed[5:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				return lineStart
			}
		}
	}
	return -1
}

// sseContainsOpenAIDone reports whether the SSE chunk contains an exact
// OpenAI `data: [DONE]` frame.
func sseContainsOpenAIDone(sse []byte) bool {
	return sseOpenAIDoneIndex(sse) >= 0
}

// generateBrandFlushEventsFiltered emits pending carries filtered to only the
// given lane keys (nil means all) and excluding skip, draining each flushed
// lane. Preserves the deterministic lane ordering of orderedBrandFlushes.
//
// A key selects its own lane and the lanes keyed beneath it: a content block
// or a choice owns the argument and tool-call lanes that stream under it, so
// flushing the block without them left a held argument token behind until the
// end-of-stream flush - after the block it belonged to was already closed. The
// match is the same one flushBrandStandalone makes for the standalone path.
// A key in skip is held back for the opposite reason: the event being
// processed is appending to that lane, so its carry can still complete there.
func (m *streamSessionManager) generateBrandFlushEventsFiltered(sess *streamSession, format string, only []string, skip map[string]bool) [][]byte {
	var keep func(string) bool
	if only != nil || skip != nil {
		keep = func(k string) bool {
			if skip[k] {
				return false
			}
			if only == nil {
				return true
			}
			for _, o := range only {
				if k == o || strings.HasPrefix(k, o+reverseBrandLaneSuffix) {
					return true
				}
			}
			return false
		}
	}
	flushes := orderedBrandFlushesFor(sess, keep)
	if len(flushes) == 0 {
		return nil
	}
	drainBrandFlushes(sess, flushes)
	var out [][]byte
	for _, f := range flushes {
		if ev := buildBrandFlushEvent(format, f.key, f.text); ev != nil {
			out = append(out, ev)
		}
	}
	return out
}

// markStandaloneFinishes records the choices a standalone (non-SSE) chunk
// finished: a non-null finish_reason ends ONLY its own choice lane, never the
// whole stream (with n > 1 other choices keep streaming). It reports the lanes
// finished by this chunk and whether the stream as a whole is done: a global
// terminal payload (bare [DONE] or Anthropic message_stop), or finish_reasons
// covering every expected ROOT choice.
//
// Done is counted over root choice lanes, never over every lane in the session.
// A choice that streams tool-call arguments opens CHILD lanes under its own key
// ("openai:0\x00tool0"), so counting lanes let choice 0 alone satisfy n = 2 and
// free the session before choice 1 had streamed anything - losing its text.
func markStandaloneFinishes(sess *streamSession, body []byte) (finished []string, done bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, false
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, true
	}
	var root map[string]any
	if err := safeUnmarshal(trimmed, &root); err != nil || root == nil {
		return nil, false
	}
	if t, _ := root["type"].(string); t == "message_stop" {
		return nil, true
	}
	if kind, laneKey := anthropicTerminalKindFromMap(root); kind == "content_block_stop" {
		return []string{laneKey}, false
	}
	choices, _ := root["choices"].([]any)
	for _, chRaw := range choices {
		ch, ok := chRaw.(map[string]any)
		if !ok {
			continue
		}
		if fr, has := ch["finish_reason"]; has && fr != nil {
			laneKey := openAIChoiceLaneKey(ch)
			getBrandLane(sess, laneKey).finished = true
			// The finishing choice's own child lanes are finished with it, so
			// their carries flush into this chunk instead of waiting for a
			// completion signal that will never come.
			prefix := laneKey + reverseBrandLaneSuffix
			for k, lane := range sess.brandCarries {
				if strings.HasPrefix(k, prefix) {
					lane.finished = true
				}
			}
			finished = append(finished, laneKey)
		}
	}
	if len(finished) == 0 {
		return finished, false
	}
	// Two counts decide completion, and both are over ROOT lanes only. Every
	// root lane the stream has actually opened must be finished, or a choice
	// the upstream is still streaming would have the session freed under it;
	// and the finished count must cover n, the number of choices the client
	// asked for, or a choice that has not appeared yet would end the stream
	// early. Child lanes (a choice's tool-call arguments) count for neither: an
	// argument lane under choice 0 belongs to that choice, so it can no longer
	// make n = 2 look satisfied on its own.
	rootTotal, rootFinished := countRootLanes(sess)
	if rootFinished < sess.expected || rootFinished < rootTotal {
		return finished, false
	}
	return finished, true
}

// countRootLanes counts a session's root choice lanes and how many of them are
// finished. Root lanes are the per-choice lanes; the argument and tool-call
// lanes opened beneath them carry a lane-key marker and are not choices.
func countRootLanes(sess *streamSession) (total, finished int) {
	for key, lane := range sess.brandCarries {
		if strings.Contains(key, reverseBrandLaneSuffix) {
			continue
		}
		total++
		if lane.finished {
			finished++
		}
	}
	return total, finished
}

// openAIChoiceLaneKey derives the brand lane key for a choice map, matching
// the keys produced by reverseBrandOpenAIStreamingMap.
func openAIChoiceLaneKey(ch map[string]any) string {
	if idxVal, ok := ch["index"]; ok {
		if n, ok := jsonIndexValue(idxVal); ok {
			return fmt.Sprintf("openai:%d", n)
		}
		return fmt.Sprintf("openai:%v", idxVal)
	}
	return "openai:0"
}

// sseEventDataMaps decodes the objects an SSE event's `data:` payloads carry,
// in order. Malformed, empty and terminal ([DONE]) payloads are skipped, the
// same way the event rewrite and the Anthropic terminal scanner treat them.
func sseEventDataMaps(ev []byte) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.ReplaceAll(string(ev), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := safeUnmarshal([]byte(payload), &m); err != nil || m == nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// openAIMayCarryFinishReason reports whether an OpenAI SSE event may carry a
// choice's finish_reason. Detection itself is always on the key the JSON decodes
// to, never on the raw bytes; this only decides whether the payloads are worth
// scanning. A backslash anywhere may be a JSON escape INSIDE the key itself
// ("finish\u005freason" decodes to finish_reason without containing its bytes),
// so those events are scanned too - the same backstop sseAnthropicTerminalKind
// keeps for its markers.
func openAIMayCarryFinishReason(ev []byte) bool {
	return bytes.Contains(ev, []byte("finish_reason")) || bytes.IndexByte(ev, '\\') >= 0
}

// laneKeyWithIndex derives a lane key from an event's own `index` field,
// defaulting to lane 0 only when the event carries none. The wire shape is
// json.Number (safeUnmarshal decodes with UseNumber); float64 and int are
// accepted for maps built in-process. A present index that is none of those
// keeps its own identity rather than collapsing onto lane 0: two such blocks are
// distinct content lanes, and merging them would emit one block's held carry
// through the other. The Anthropic reader has always keyed them apart; the
// shared helper has to preserve that, and a non-integral number has never
// parsed, so it still defaults.
func laneKeyWithIndex(format string, data map[string]any) string {
	idx, present := data["index"]
	if present {
		switch v := idx.(type) {
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return fmt.Sprintf("%s:%d", format, i)
			}
		case float64:
			return fmt.Sprintf("%s:%d", format, int(v))
		case int:
			return fmt.Sprintf("%s:%d", format, v)
		default:
			return fmt.Sprintf("%s:%v", format, v)
		}
	}
	return format + ":0"
}

// requestChoiceCount reads the OpenAI "n" (choices per completion) from a
// request body; absent/invalid means 1.
func requestChoiceCount(body []byte) int {
	var root map[string]any
	if err := safeUnmarshal(body, &root); err != nil || root == nil {
		return 1
	}
	if n, ok := jsonIndexValue(root["n"]); ok && n > 1 {
		return n
	}
	return 1
}

// flushBrandStandalone merges a session's held carries into a standalone
// (non-SSE) chunk and drains them. It is the terminal half of the standalone
// path: the applier already ran over this chunk and left any partial token in
// its lane, so the flush resolves that token and hands it back inside the same
// chunk shape the protocol expects.
func (m *streamSessionManager) flushBrandStandalone(sess *streamSession, body []byte, format string, only []string) ([]byte, bool) {
	if sess == nil || (len(sess.brandCarries) == 0 && !hasPendingTierHold(sess)) {
		return nil, false
	}
	if sess.client == "" || sess.client == negativeClientResolution {
		return nil, false
	}
	var root map[string]any
	trimmed := bytes.TrimSpace(body)
	bareDone := bytes.Equal(trimmed, []byte("[DONE]"))
	if err := safeUnmarshal(body, &root); err != nil || root == nil {
		if !bareDone {
			return nil, false
		}
		root = map[string]any{}
	}
	var keep func(string) bool
	if only != nil {
		keep = func(k string) bool {
			// A cloaked-brand session keeps one lane per carrier under the
			// choice's key - the choice's prose lane plus a lane per streamed
			// tool call - so a finished choice ("openai:...0") flushes the lanes
			// it opened ("openai:...0\x00tool0"), not only its own key.
			for _, o := range only {
				if k == o || strings.HasPrefix(k, o+reverseBrandLaneSuffix) {
					return true
				}
			}
			return false
		}
	}
	flushes := orderedBrandFlushesFor(sess, keep)
	if len(flushes) == 0 {
		return nil, false
	}
	var merged bool
	switch format {
	case "openai":
		merged = mergeOpenAIStandaloneFlush(root, flushes)
	case "anthropic":
		merged = mergeAnthropicStandaloneFlush(root, flushes)
	}
	if !merged {
		if format == "anthropic" {
			if t, _ := root["type"].(string); t == "message_stop" || t == "content_block_stop" {
				if len(flushes) > 0 {
					var buf bytes.Buffer
					for _, f := range flushes {
						ev := buildBrandFlushEvent("anthropic", f.key, f.text)
						if ev == nil {
							continue
						}
						buf.Write(ev)
					}
					buf.WriteString("data: ")
					buf.Write(trimmed)
					buf.WriteString("\n\n")
					drainBrandFlushes(sess, flushes)
					return buf.Bytes(), true
				}
			}
		}
		return nil, false
	}
	drainBrandFlushes(sess, flushes)
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	if bareDone {
		// The host frames the whole payload as one SSE data line, so keep
		// the terminal marker as its own event after the flush content.
		return append(raw, []byte("\n\ndata: [DONE]")...), true
	}
	return raw, true
}

func mergeOpenAIStandaloneFlush(root map[string]any, flushes []brandFlush) bool {
	choices, _ := root["choices"].([]any)
	for _, f := range flushes {
		idx := laneIndexNumOrZero(f.key)
		target := openAIChoiceAt(choices, idx)
		if target == nil {
			target = map[string]any{"index": idx}
			choices = append(choices, target)
		}
		delta, _ := target["delta"].(map[string]any)
		if delta == nil {
			if msg, ok := target["message"].(map[string]any); ok {
				delta = msg
			} else {
				delta = map[string]any{}
				target["delta"] = delta
			}
		}
		appendBrandFlushToChoice(delta, f)
	}
	root["choices"] = choices
	return true
}

// appendBrandFlushToChoice merges one recovered carry into a choice's delta,
// through the carrier toolArgsLaneParts identified.
func appendBrandFlushToChoice(delta map[string]any, f brandFlush) {
	_, argIndex, isArgs := toolArgsLaneParts(f.key)
	if !isArgs {
		if s, ok := delta["content"].(string); ok {
			delta["content"] = s + f.text
		} else {
			delta["content"] = f.text
		}
		return
	}
	calls, fn := openAIToolCallAt(delta["tool_calls"], argIndex)
	delta["tool_calls"] = calls
	if s, ok := fn["arguments"].(string); ok {
		fn["arguments"] = s + f.text
	} else {
		fn["arguments"] = f.text
	}
}

// openAIToolCallAt returns the streamed tool call at idx as it now is in raw,
// creating it when the chunk has not declared it yet. The possibly grown
// slice is returned so the caller can write it back.
func openAIToolCallAt(raw any, idx int) ([]any, map[string]any) {
	calls, _ := raw.([]any)
	for _, cRaw := range calls {
		call, ok := cRaw.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := jsonIndexValue(call["index"]); ok && n == idx {
			fn, ok := call["function"].(map[string]any)
			if !ok {
				fn = map[string]any{}
				call["function"] = fn
			}
			return calls, fn
		}
	}
	fn := map[string]any{}
	return append(calls, map[string]any{"index": idx, "function": fn}), fn
}

func openAIChoiceAt(choices []any, idx int) map[string]any {
	for _, chRaw := range choices {
		ch, ok := chRaw.(map[string]any)
		if !ok {
			continue
		}
		ci := 0
		if v, has := ch["index"]; has {
			if n, ok := jsonIndexValue(v); ok {
				ci = n
			}
		}
		if ci == idx {
			return ch
		}
	}
	return nil
}

func mergeAnthropicStandaloneFlush(root map[string]any, flushes []brandFlush) bool {
	if len(flushes) != 1 {
		return false
	}
	delta, ok := root["delta"].(map[string]any)
	if !ok {
		return false
	}
	if _, _, isArgs := toolArgsLaneParts(flushes[0].key); isArgs {
		if s, ok := delta["partial_json"].(string); ok {
			delta["partial_json"] = s + flushes[0].text
		} else {
			delta["partial_json"] = flushes[0].text
		}
		return true
	}
	txt, ok := delta["text"].(string)
	if !ok {
		return false
	}
	delta["text"] = txt + flushes[0].text
	return true
}

// reverseBrandStandalone reverses the brand tokens of one standalone (non-SSE)
// chunk: the shape CLIProxyAPI hands the plugin for the OpenAI-protocol exits,
// where the host adds the `data:` framing itself after interception. Oh My Pi
// keeps its protected appliers; every other resolved client runs the same
// per-lane applier the SSE path uses.
func (m *streamSessionManager) reverseBrandStandalone(sess *streamSession, body []byte, format string) ([]byte, bool) {
	if sess == nil || sess.client == "" || sess.client == negativeClientResolution {
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, false
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, false
	}
	var root map[string]any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}
	var changed bool
	switch {
	case sess.client != "oh_my_pi":
		changed = m.reverseCloakedBrandStreamingMap(root, sess, format)
	case format == "openai":
		changed = m.reverseBrandOpenAIStreamingMap(root, sess)
	case format == "anthropic":
		if _, ok := root["delta"]; ok || root["type"] == "content_block_delta" || root["type"] == "content_block_start" {
			changed = m.reverseBrandAnthropicStreamingMap(root, sess)
		} else {
			changed = m.reverseBrandOpenAIStreamingMap(root, sess)
		}
	}
	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// ── Stream Session Manager & SSE Event Reassembly ────────────────────────
//
// Handles the "Split-String Chunk" attack: TCP can split a network chunk
// at any byte boundary, including inside a tool name:
//   Chunk 1: data: {"name": "run_c
//   Chunk 2: ommand", "input": {}}\n\n
//
// Without buffering, regex on each chunk misses the match entirely.
//
// Solution: SSE events are delimited by "\n\n". We buffer incomplete events
// (those without a \n\n terminator) and only process/forward complete events
// where all JSON content — including tool names — is guaranteed intact.
//
// In CLIProxyAPI schema_version >= 3, OriginalRequest and RequestBody are
// delivered only on the header-init chunk (ChunkIndex == StreamChunkHeaderInitIndex).
// StreamSessionManager caches the client uncloak pattern under a correlation
// key (RequestID, metadata/headers ids, or for legacy schema < 3 chunks where
// every chunk repeats the request body, an FNV hash of that body), isolating
// concurrent streams. Payload chunks with NO correlation key cannot be
// attributed to any stream: sharing one slot between them would let one
// stream's state overwrite another's (cross-stream corruption), so such
// chunks pass through unmolested instead of being buffered.

type brandLane struct {
	carry      string
	lastIsWord bool
	// seq is the order in which this lane was first opened, i.e. the order its
	// text appeared in the stream. Two lanes of the same choice or content block
	// can hold text at once (its prose lane and an argument lane), and their
	// flushes have to reach the client in the order the text arrived, not in
	// mapping-name order.
	seq int
	// finished marks the lane's choice as ended by a non-null
	// finish_reason on the standalone path; the whole session is freed
	// only once every expected lane is finished.
	finished bool
}

type streamSession struct {
	client       string
	cached       *cachedUncloakPattern
	tail         []byte
	updatedAt    time.Time
	brandCarries map[string]*brandLane
	// laneSeq hands every new brand lane its arrival order, so flushes of two
	// lanes of the same block can be emitted in source order.
	laneSeq int
	// expected is the request's OpenAI "n" (choices per completion),
	// minimum 1; gates standalone stream-end detection.
	expected       int
	laneProgress   map[int]bool
	payloadStarted bool
	// terminalChoices holds the root lane keys ("openai:0") whose choice ends in
	// the event currently being applied. While a choice is marked, none of its
	// lanes may hold a trailing partial token: no further bytes can arrive for
	// it, so a token its own terminal delta ends on is resolved into that event
	// instead of being carried past the finish_reason to the end of the stream.
	// Set and cleared per event, so nothing leaks into the next one.
	terminalChoices map[string]bool
	// tier is the session's Issue #55 Agent model-tier authority, pinned at
	// creation time only when the request's alias plan admitted one. nil means
	// the session runs no tier pass.
	tier *requestTierAuthority
	// tierLanes holds one tier scanner state per argument lane that a named
	// eligible call opened, keyed like the brand argument lanes.
	tierLanes map[string]*streamTierLane
	// tierPendingNames accumulates the wire-name fragments a lane has received
	// so far, so a name split across chunks still resolves to the admitted
	// upstream identity. A decided lane holds its accumulated name or the
	// retired sentinel.
	tierPendingNames map[string]string
	// tierArgsSeen records that argument bytes already reached a lane. Without
	// it, a later complete-name fragment is indistinguishable from the second
	// half of a longer identity, so only a lane that already streamed
	// arguments may treat that fragment as a repetition.
	tierArgsSeen map[string]bool
}

// laneReverseTable picks the table a stream lane's bytes are resolved against:
// the argument-safe subset inside a tool-argument lane, the client's whole
// table in a prose lane. Resolving and flushing both go through here, so a
// lane's hold decision and its flush decision can never use different tables.
func laneReverseTable(sess *streamSession, laneKey string) []rewriteMapping {
	if _, _, isArgs := toolArgsLaneParts(laneKey); isArgs {
		return argumentReverseTableFor(sess.client)
	}
	return brandReverseTableFor(sess.client)
}

type streamSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*streamSession
}

var globalStreamManager = newStreamSessionManager()

func newStreamSessionManager() *streamSessionManager {
	return &streamSessionManager{
		sessions: make(map[string]*streamSession),
	}
}

func (m *streamSessionManager) sessionKey(req *pluginapi.StreamChunkInterceptRequest) string {
	if req.RequestID != "" {
		return "req:" + req.RequestID
	}
	if req.Metadata != nil {
		for _, k := range []string{"request_id", "stream_id", "session_id", "trace_id", "id"} {
			if v, ok := req.Metadata[k].(string); ok && v != "" {
				return "meta:" + k + ":" + v
			}
		}
	}
	if req.RequestHeaders != nil {
		for _, h := range []string{"X-Request-Id", "X-Correlation-Id", "X-Amzn-Trace-Id"} {
			if v := req.RequestHeaders.Get(h); v != "" {
				return "req_hdr:" + h + ":" + v
			}
		}
	}
	if req.ResponseHeaders != nil {
		for _, h := range []string{"X-Request-Id", "X-Correlation-Id"} {
			if v := req.ResponseHeaders.Get(h); v != "" {
				return "resp_hdr:" + h + ":" + v
			}
		}
	}
	// For legacy chunks (schema < 3, ChunkIndex >= 0) where OriginalRequest/RequestBody
	// is populated on every chunk, use the FNV hash of the request body as session key.
	if req.ChunkIndex >= 0 {
		src := req.OriginalRequest
		if len(src) == 0 {
			src = req.RequestBody
		}
		if len(src) > 0 {
			h := fnv.New64a()
			_, _ = h.Write(src)
			return fmt.Sprintf("fnv:%x", h.Sum64())
		}
	}
	return ""
}

// resetSession (re)initializes the session for a fresh stream start, clearing
// any stale tail left by an aborted previous incarnation of the same key.
// expected is the request's choice count ("n"); omitted means 1. Callers that
// own tier authority pin it on the fresh session right after this call.
func (m *streamSessionManager) resetSession(key, client string, cached *cachedUncloakPattern, expected ...int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStaleLocked()
	m.sessions[key] = newStreamSession(client, cached, expected...)
}

func newStreamSession(client string, cached *cachedUncloakPattern, expected ...int) *streamSession {
	n := 1
	if len(expected) > 0 && expected[0] > 1 {
		n = expected[0]
	}
	return &streamSession{
		client:       client,
		cached:       cached,
		updatedAt:    time.Now(),
		brandCarries: make(map[string]*brandLane),
		expected:     n,
	}
}

// attachTier pins a session's Issue #55 tier authority. It runs right after the
// session is (re)created inside the ordering fence between the request pass and
// the stream pass for the same RequestID, so the tier is always pinned before
// any chunk processing begins. Absent sessions are skipped: a deleted session
// is re-created later from the plan that still owns the tier.
func (m *streamSessionManager) attachTier(key string, tier *requestTierAuthority) {
	if tier == nil || key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[key]; sess != nil {
		sess.tier = tier
	}
}

// ensureSession registers a session only when none lives under key yet.
// Losing a race to an already-registered session returns the incumbent;
// both racers derive (client, cached) from the same request body, so either
// outcome is correct.
func (m *streamSessionManager) ensureSession(key, client string, cached *cachedUncloakPattern, expected ...int) *streamSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[key]; sess != nil {
		return sess
	}
	sess := newStreamSession(client, cached, expected...)
	m.sessions[key] = sess
	return sess
}

// getSession returns the live session under key, or nil. Callers must treat the
// returned session as read-only: client and cached are set before publication and
// never rewritten, but the buffered tail and brand carries are mutated in place
// by the stream path.
func (m *streamSessionManager) getSession(key string) *streamSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[key]
}

func (m *streamSessionManager) getClient(key string) string {
	if sess := m.getSession(key); sess != nil {
		return sess.client
	}
	return ""
}

func (m *streamSessionManager) deleteSession(key string) {
	if key == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
}

func (m *streamSessionManager) cleanupStaleLocked() {
	cutoff := time.Now().Add(-5 * time.Minute)
	for k, s := range m.sessions {
		if !s.updatedAt.Before(cutoff) {
			continue
		}
		if strings.HasPrefix(k, "req:") {
			reqID := strings.TrimPrefix(k, "req:")
			if plan := globalAliasPlanManager.get(reqID); plan != nil {
				if s.payloadStarted || plan.getDisposition() == streamDispositionPayloadActive || len(s.tail) > 0 || len(s.laneProgress) > 0 {
					continue
				}
			}
			if route := globalLifecycleManager.getRoute(reqID); route != nil && route.routeKind == routeKindProtectedAGY {
				if s.payloadStarted || route.getDisposition() == streamDispositionPayloadActive || len(s.tail) > 0 || hasPendingBrandCarry(s) || len(s.laneProgress) > 0 {
					continue
				}
			}
		}
		delete(m.sessions, k)
	}
}

func (m *streamSessionManager) processChunk(req *pluginapi.StreamChunkInterceptRequest, format string) pluginapi.StreamChunkInterceptResponse {
	key := m.sessionKey(req)

	var protectedRoute *explicitOMPRouteState
	var aliasPlan *requestAliasPlan
	if req.RequestID != "" {
		protectedRoute = globalLifecycleManager.getRoute(req.RequestID)
		aliasPlan = globalAliasPlanManager.get(req.RequestID)
	}
	if aliasPlan != nil && aliasPlan.getDisposition() == streamDispositionCleanTerminal {
		debugLog("StreamSessionManager: alias-plan late chunk after clean terminal key=%s", key)
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
		if protectedRoute.malformed || protectedRoute.client != "oh_my_pi" {
			debugLog("StreamSessionManager: malformed ProtectedAGY route state key=%s", key)
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if protectedRoute.getDisposition() == streamDispositionCleanTerminal {
			debugLog("StreamSessionManager: late chunk after clean terminal key=%s", key)
			return pluginapi.StreamChunkInterceptResponse{}
		}
	}

	// Header-init chunk: schema_version >= 3 delivers OriginalRequest/RequestBody
	// here. Nothing to uncloak on this chunk; register the stream's session so
	// payload chunks resolve their client from cache.
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex || req.ChunkIndex < 0 {
		// Without a correlation key the future payload chunks cannot be
		// attributed back to this session — detection would be wasted work.
		if key == "" {
			return pluginapi.StreamChunkInterceptResponse{}
		}
		// If session was already pre-registered by request intercept, keep it.
		if sessClient := m.getClient(key); sessClient != "" {
			debugLog("StreamSessionManager: header-init using pre-registered session key=%s client=%s", key, sessClient)
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if aliasPlan != nil {
			if aliasPlan.getDisposition() == streamDispositionNone {
				m.ensureSession(key, aliasPlan.client, aliasPlan.cachedUncloak, aliasPlan.expected)
				m.attachTier(key, aliasPlan.tier)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
			if protectedRoute.getDisposition() == streamDispositionNone {
				m.ensureSession(key, "oh_my_pi", protectedRoute.cachedUncloak, protectedRoute.expected)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		detectSrc := detectionRequestBody(req.OriginalRequest, req.RequestBody)
		n := requestChoiceCount(detectSrc)
		if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
			debugLog("StreamSessionManager: header-init UA evidence key=%s client=%s", key, uaClient)
			if cached := requestScopedUncloakPattern(uaClient, detectSrc, format); cached != nil && cached.re != nil {
				m.resetSession(key, uaClient, cached, n)
			}
			return pluginapi.StreamChunkInterceptResponse{}
		}
		_, client := buildUncloakTable(detectSrc, format)
		debugLog("StreamSessionManager: header-init key=%s client=%s", key, client)
		if client != "" {
			cached := requestScopedUncloakPattern(client, detectSrc, format)
			if cached != nil && cached.re != nil {
				m.resetSession(key, client, cached, n)
			}
		}
		return pluginapi.StreamChunkInterceptResponse{}
	}

	// Payload chunks carrying no correlation key cannot be tied to a stream.
	// They pass through unmolested rather than compete for shared state that
	// would corrupt concurrent streams' buffered tails.
	if key == "" {
		return pluginapi.StreamChunkInterceptResponse{}
	}

	// Opportunistic cleanup runs on every payload chunk, matching the
	// original behavior: a stream that dies between chunks must still be
	// pruned by later traffic.
	m.mu.Lock()
	m.cleanupStaleLocked()
	sess := m.sessions[key]
	m.mu.Unlock()
	if sess == nil {
		if aliasPlan != nil {
			if aliasPlan.getDisposition() == streamDispositionPayloadActive {
				debugLog("StreamSessionManager: alias-plan disposable session lost after payload started key=%s", key)
				return pluginapi.StreamChunkInterceptResponse{}
			}
			if aliasPlan.getDisposition() == streamDispositionNone {
				sess = m.ensureSession(key, aliasPlan.client, aliasPlan.cachedUncloak, aliasPlan.expected)
				m.attachTier(key, aliasPlan.tier)
			}
		} else if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
			if protectedRoute.getDisposition() == streamDispositionPayloadActive {
				debugLog("StreamSessionManager: disposable session lost after payload started key=%s", key)
				return pluginapi.StreamChunkInterceptResponse{}
			}
			if protectedRoute.getDisposition() == streamDispositionNone {
				sess = m.ensureSession(key, "oh_my_pi", protectedRoute.cachedUncloak, protectedRoute.expected)
			}
		} else {
			sess = m.ensureFallbackSession(req, format, key)
		}
	}
	if sess == nil {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	// A cached pattern is what the tool-name uncloak pass needs, but not what
	// the brand reverse needs: that runs off the session's resolved client
	// alone. An alias-plan client pins its tool-name authority in the plan
	// rather than in a pattern, so it reaches here with cached == nil, and
	// bailing on that skipped the brand reverse for every such client: the
	// request went out cloaked and the response came back cloaked. Only bail
	// when no client was resolved. The SSE branch below already handles a nil
	// pattern (modified = completeEvents, no uncloak).
	if sess.cached == nil && (sess.client == "" || sess.client == negativeClientResolution) {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	m.mu.Lock()
	if aliasPlan != nil {
		aliasPlan.setDisposition(streamDispositionPayloadActive)
		sess.payloadStarted = true
	}
	if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
		protectedRoute.setDisposition(streamDispositionPayloadActive)
		sess.payloadStarted = true
	}
	cached := sess.cached

	// Reset buffer on first payload chunk (ChunkIndex == 0)
	if req.ChunkIndex == 0 {
		sess.tail = nil
	}

	var combined []byte
	if len(sess.tail) > 0 {
		combined = append(sess.tail, req.Body...)
	} else {
		combined = req.Body
	}

	// Check if this is an SSE-formatted stream vs individual JSON chunk payload
	isSSE := len(sess.tail) > 0
	if !isSSE {
		trimmed := bytes.TrimLeft(combined, " \t\r\n")
		isSSE = bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.Contains(combined, []byte("\n\n")) || bytes.Contains(combined, []byte("\r\n\r\n"))
	}
	if isSSE {
		completeEvents, incompleteTail := splitSSEEventsWithNewBytes(combined, len(req.Body))
		if len(completeEvents) == 0 {
			if len(sess.tail) == 0 {
				sess.tail = append([]byte(nil), req.Body...)
			} else {
				sess.tail = combined
			}
			sess.updatedAt = time.Now()
			m.mu.Unlock()

			debugLog("StreamSessionManager: no complete events, dropping chunk len=%d", len(incompleteTail))
			return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
		}

		if len(incompleteTail) > 0 {
			sess.tail = append([]byte(nil), incompleteTail...)
		} else {
			sess.tail = nil
		}
		sess.updatedAt = time.Now()
		m.mu.Unlock()

		// Issue #55 Contract B: ownership is decided from the names the
		// upstream actually sent, before any restore pass runs.
		armTierLanesFromRaw(sess, completeEvents, format, true)
		var modified []byte
		changed := false
		if cached != nil && cached.re != nil {
			if mod, ch := uncloakStreamChunk(completeEvents, cached); ch {
				modified = mod
				changed = true
			} else {
				modified = completeEvents
			}
		} else {
			modified = completeEvents
		}
		// Every resolved client runs a brand reverse, not just Oh My Pi.
		// reverseBrandSSE dispatches: oh_my_pi takes the protected-brand lane,
		// everyone else takes its own client's reverse set. A session
		// with no resolved client has nothing to restore against, so it is
		// left alone rather than guessed at.
		brandChanged := false
		if sess.client != "" && sess.client != negativeClientResolution {
			if bm, bc := m.reverseBrandSSE(sess, modified, format); bc {
				modified = bm
				brandChanged = true
			}
		}
		overallChanged := changed || brandChanged
		isDone := sseContainsOpenAIDone(completeEvents) || sseContainsOpenAIDone(modified)
		isAnthropicEnd := format == "anthropic" && (sseContainsAnthropicMessageStop(completeEvents) || sseContainsAnthropicMessageStop(modified))
		if isDone || isAnthropicEnd {
			if isDone && sess.client == "oh_my_pi" {
				// Safety net for [DONE] embedded in a multi-line event that
				// reverseBrandSSE's per-event check misses. generateBrandFlushEvents
				// drains each lane as it emits, so the common case (flush events
				// already written before the [DONE] frame) is a no-op here — no
				// double emission.
				if flush := m.generateBrandFlushEvents(sess, format); len(flush) > 0 {
					doneIdx := sseOpenAIDoneIndex(modified)
					var tmp bytes.Buffer
					if doneIdx >= 0 {
						tmp.Write(modified[:doneIdx])
						for _, fe := range flush {
							tmp.Write(fe)
						}
						tmp.Write(modified[doneIdx:])
					} else {
						for _, fe := range flush {
							tmp.Write(fe)
						}
						tmp.Write(modified)
					}
					modified = tmp.Bytes()
					overallChanged = true
				}
			}
			if !hasPendingBrandCarry(sess) {
				m.deleteSession(key)
				if aliasPlan != nil {
					aliasPlan.setDisposition(streamDispositionCleanTerminal)
				}
				if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
					protectedRoute.setDisposition(streamDispositionCleanTerminal)
				}
			}
		}
		// Empty Body lets the host forward only the current input chunk. After
		// reassembly or withholding a partial next event, emit the complete bytes.
		if !overallChanged && bytes.Equal(modified, req.Body) {
			return pluginapi.StreamChunkInterceptResponse{}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: modified}
	}

	// Standalone JSON chunk payload (e.g. CLIProxyAPI OpenAI protocol chunk)
	sess.tail = nil
	sess.updatedAt = time.Now()
	m.mu.Unlock()

	// Issue #55 Contract B: ownership is decided from the names the upstream
	// actually sent, before any restore pass runs.
	armTierLanesFromRaw(sess, req.Body, format, false)
	var modified []byte
	changed := false
	if cached != nil && cached.re != nil {
		if mod, ch := uncloakStreamChunk(req.Body, cached); ch {
			modified = mod
			changed = true
		} else {
			modified = req.Body
		}
	} else {
		modified = req.Body
	}
	// Every resolved client runs the brand reverse here, not just Oh My Pi: the
	// OpenAI-protocol exits hand the plugin unframed JSON chunks (the host adds
	// `data: ` itself after interception), so this is the path a codex or
	// claude_code session actually takes on those routes.
	if sess.client != "" && sess.client != negativeClientResolution {
		if bm, bc := m.reverseBrandStandalone(sess, modified, format); bc {
			modified = bm
			changed = true
		}
		// A non-null finish_reason ends ONLY its own choice lane: flush that
		// lane's held carry into this chunk. The session is freed — and every
		// remaining carry flushed — only at true stream completion (bare
		// [DONE], message_stop, or finish_reasons covering every expected
		// lane); unfinished lanes keep streaming.
		finished, done := markStandaloneFinishes(sess, modified)
		if done || len(finished) > 0 {
			only := finished
			if done {
				only = nil
			}
			if bm, fc := m.flushBrandStandalone(sess, modified, format, only); fc {
				modified = bm
				changed = true
			}
		}
		if done && !hasPendingBrandCarry(sess) {
			m.deleteSession(key)
			if aliasPlan != nil {
				aliasPlan.setDisposition(streamDispositionCleanTerminal)
			}
			if protectedRoute != nil && protectedRoute.routeKind == routeKindProtectedAGY {
				protectedRoute.setDisposition(streamDispositionCleanTerminal)
			}
		}
	}
	if !changed {
		if bytes.Equal(modified, req.Body) {
			return pluginapi.StreamChunkInterceptResponse{}
		}
	}
	return pluginapi.StreamChunkInterceptResponse{Body: modified}
}

// ensureFallbackSession handles schema_version < 3 streams, where every payload
// chunk repeats OriginalRequest/RequestBody, plus the lazy case where a keyed
// header-init carried no detectable client but later chunks might. Detection
// deliberately runs WITHOUT holding the manager lock: parsing a large request
// body under the lock serialized every concurrent stream's chunk processing.
func (m *streamSessionManager) ensureFallbackSession(req *pluginapi.StreamChunkInterceptRequest, format, key string) *streamSession {
	if key == "" {
		return nil
	}
	src := detectionRequestBody(req.OriginalRequest, req.RequestBody)
	n := requestChoiceCount(src)
	if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
		if cached := requestScopedUncloakPattern(uaClient, src, format); cached != nil && cached.re != nil {
			debugLog("StreamSessionManager: fallback UA evidence key=%s client=%s", key, uaClient)
			return m.ensureSession(key, uaClient, cached, n)
		}
	}
	if len(src) == 0 {
		return nil
	}
	_, client := buildUncloakTable(src, format)
	debugLog("StreamSessionManager: fallback detect key=%s client=%s", key, client)
	if client == "" {
		return nil
	}
	cached := requestScopedUncloakPattern(client, src, format)
	if cached == nil || cached.re == nil {
		return nil
	}
	return m.ensureSession(key, client, cached, n)
}

// splitSSEEventsWithNewBytes splits combined bytes into complete SSE events
// and an incomplete trailing tail. Complete events are those terminated by
// "\n\n" (or "\r\n\r\n") and include the terminating delimiter; only the newly
// arrived newLen bytes plus up to 3 preceding bytes need rescanning.
func splitSSEEventsWithNewBytes(data []byte, newLen int) (completeEvents []byte, incompleteTail []byte) {
	n := len(data)
	if n == 0 {
		return nil, data
	}
	// Only the newly arrived bytes plus at most 3 preceding bytes can form or extend a delimiter.
	scanStart := n - newLen - 3
	if scanStart < 0 {
		scanStart = 0
	}
	window := data[scanStart:]
	for i := len(window) - 1; i >= 1; i-- {
		if window[i] != '\n' {
			continue
		}
		if window[i-1] == '\n' || (i >= 3 && window[i-3] == '\r' && window[i-2] == '\n' && window[i-1] == '\r') {
			splitAt := scanStart + i + 1
			return data[:splitAt], data[splitAt:]
		}
	}
	return nil, data
}
