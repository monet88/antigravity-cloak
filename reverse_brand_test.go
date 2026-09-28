package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Helpers duplicated to avoid import cycles: use same as existing tests
func decodeBody(t *testing.T, rawEnvelope []byte) []byte {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v %s", err, rawEnvelope)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %s", rawEnvelope)
	}
	if env.Result.Body == "" {
		return nil
	}
	dec, err := base64.StdEncoding.DecodeString(env.Result.Body)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return dec
}

func decodeStreamBody(t *testing.T, rawEnvelope []byte) ([]byte, bool) {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body      string `json:"Body"`
			DropChunk bool   `json:"DropChunk"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok")
	}
	if env.Result.Body == "" {
		return nil, env.Result.DropChunk
	}
	dec, _ := base64.StdEncoding.DecodeString(env.Result.Body)
	return dec, env.Result.DropChunk
}

// sseAssistantJoined centralizes repeated SSE assistant-text extraction for
// both Anthropic content_block_delta and OpenAI choice delta content.
// It joins delta texts in event order, handling the string and array content
// shapes. Local test refactor for Issue #21 (small, justified).
func sseAssistantJoined(t *testing.T, bodies ...[]byte) string {
	t.Helper()
	var sb strings.Builder
	for _, body := range bodies {
		if len(body) == 0 {
			continue
		}
		s := string(body)
		// Standalone JSON without SSE framing (used by standalone path tests
		// that also call this helper for convenience) – try direct parse.
		if !strings.Contains(s, "data:") {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err == nil {
				if m["type"] == "content_block_delta" {
					if delta, ok := m["delta"].(map[string]any); ok {
						if txt, ok := delta["text"].(string); ok {
							sb.WriteString(txt)
						}
					}
				}
			}
			continue
		}
		parts := strings.Split(s, "data: ")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || p == "[DONE]" {
				continue
			}
			line := strings.Split(p, "\n")[0]
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if m["type"] == "content_block_delta" {
				if delta, ok := m["delta"].(map[string]any); ok {
					if txt, ok := delta["text"].(string); ok {
						sb.WriteString(txt)
					}
				}
				continue
			}
			if choices, ok := m["choices"].([]any); ok {
				for _, cRaw := range choices {
					c, ok := cRaw.(map[string]any)
					if !ok {
						continue
					}
					var target map[string]any
					if d, ok := c["delta"].(map[string]any); ok {
						target = d
					} else if d, ok := c["message"].(map[string]any); ok {
						target = d
					} else {
						continue
					}
					if txt, ok := target["content"].(string); ok {
						sb.WriteString(txt)
					} else if arr, ok := target["content"].([]any); ok {
						for _, partRaw := range arr {
							if part, ok := partRaw.(map[string]any); ok {
								if typ, _ := part["type"].(string); isAssistantTextPartType(typ) {
									if txt, ok := part["text"].(string); ok {
										sb.WriteString(txt)
									}
								}
							} else if s, ok := partRaw.(string); ok {
								sb.WriteString(s)
							}
						}
					}
				}
			}
		}
	}
	return sb.String()
}

func TestReverseBrand_OpenAI_NonStream_OMP(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-omp-openai-nonstream"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read","description":"r"}},{"type":"function","function":{"name":"task","description":"t"}},{"type":"function","function":{"name":"hub","description":"h"}}],"messages":[]}`
	// Pre-register via request intercept (distinctive OMP tools => detect)
	interceptPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody))
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptPayload)

	respBody := `{"choices":[{"message":{"role":"assistant","content":"Hello Antigravity world","tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`
	payload := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	// Need to include RequestID correlation
	var reqMap map[string]any
	json.Unmarshal(payload, &reqMap)
	reqMap["RequestID"] = reqID
	reqMap["Model"] = "agy/model"
	payload, _ = json.Marshal(reqMap)

	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
	dec := decodeBody(t, raw)
	if dec == nil {
		t.Fatal("expected body changed")
	}
	var resp map[string]any
	json.Unmarshal(dec, &resp)
	choices := resp["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content := msg["content"].(string)
	if !strings.Contains(content, "omp") {
		t.Fatalf("expected omp in content, got %q", content)
	}
	if strings.Contains(content, "Antigravity") {
		t.Fatalf("Antigravity leak in assistant content: %q", content)
	}
	// tool still uncloaked
	tc := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	if tc != "bash" {
		t.Fatalf("tool not uncloaked, got %q", tc)
	}
}

func TestReverseBrand_Anthropic_NonStream_OMP(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-omp-anthropic-nonstream"
	reqBody := `{"tools":[{"name":"read"},{"name":"task"},{"name":"hub"}],"messages":[]}`
	interceptPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/model", []byte(reqBody))
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptPayload)

	respBody := `{"content":[{"type":"text","text":"We are Antigravity now"},{"type":"tool_use","id":"1","name":"view_file","input":{"path":"/tmp/Antigravity"}}]}`
	payload := responseInterceptRequestJSON(t, reqBody, respBody, "anthropic")
	var reqMap map[string]any
	json.Unmarshal(payload, &reqMap)
	reqMap["RequestID"] = reqID
	reqMap["Model"] = "agy/model"
	payload, _ = json.Marshal(reqMap)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
	dec := decodeBody(t, raw)
	var resp map[string]any
	json.Unmarshal(dec, &resp)
	content := resp["content"].([]any)
	textBlock := content[0].(map[string]any)
	txt := textBlock["text"].(string)
	if !strings.Contains(txt, "omp") || strings.Contains(txt, "Antigravity") {
		t.Fatalf("anthropic brand reverse failed, got %q", txt)
	}
	// tool input must preserve Antigravity
	toolBlock := content[1].(map[string]any)
	input := toolBlock["input"].(map[string]any)
	if input["path"] != "/tmp/Antigravity" {
		t.Fatalf("tool arg mangled, got %v", input["path"])
	}
	// tool name uncloaked
	if toolBlock["name"] != "read" {
		t.Fatalf("tool name not uncloaked: %q", toolBlock["name"])
	}
}

func TestReverseBrand_Streaming_OpenAI_Fragmentation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-frag-openai"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}},{"type":"function","function":{"name":"hub"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))

	// Header init already done via request intercept pre-register, but also need stream header init
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)

	// Chunk 1 ends with Anti
	chunk1 := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello Anti\"}}]}\n\n"
	p1 := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk1), nil)
	raw1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p1)
	body1, drop1 := decodeStreamBody(t, raw1)
	if drop1 {
		t.Fatal("unexpected drop")
	}
	// Chunk 2 continues with gravity
	chunk2 := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"gravity world\"}}]}\n\n"
	p2 := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 1, []byte(chunk2), nil)
	raw2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p2)
	body2, _ := decodeStreamBody(t, raw2)
	// Flush at DONE
	doneChunk := "data: [DONE]\n\n"
	pDone := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 2, []byte(doneChunk), nil)
	rawDone, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, pDone)
	bodyDone, _ := decodeStreamBody(t, rawDone)
	final := sseAssistantJoined(t, body1, body2, bodyDone)
	if !strings.Contains(final, "omp") {
		t.Fatalf("fragmented brand not replaced, assembled=%q body1=%q body2=%q done=%q", final, string(body1), string(body2), string(bodyDone))
	}
	if strings.Contains(final, "Antigravity") || strings.Contains(final, "Anti") {
		t.Fatalf("leak in fragmented, final=%q", final)
	}
	if !strings.Contains(final, "Hello") {
		t.Fatalf("lost Hello, final=%q", final)
	}
}

func TestReverseBrand_Streaming_Anthropic_Fragmentation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-frag-anthropic"
	reqBody := `{"tools":[{"name":"read"},{"name":"task"},{"name":"hub"}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/model", []byte(reqBody)))
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)

	chunk1 := "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi Anti\"}}\n\n"
	p1 := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", 0, []byte(chunk1), nil)
	raw1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p1)
	b1, _ := decodeStreamBody(t, raw1)

	chunk2 := "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"gravity there\"}}\n\n"
	p2 := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", 1, []byte(chunk2), nil)
	raw2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p2)
	b2, _ := decodeStreamBody(t, raw2)

	// Protocol-realistic native Anthropic termination instead of synthetic [DONE] (Issue #21).
	term := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	pTerm := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", 2, []byte(term), nil)
	rawTerm, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, pTerm)
	bTerm, _ := decodeStreamBody(t, rawTerm)

	joined := sseAssistantJoined(t, b1, b2, bTerm)
	if !strings.Contains(joined, "omp") {
		t.Fatalf("anthropic fragmented not replaced, joined=%q b1=%q b2=%q term=%q", joined, string(b1), string(b2), string(bTerm))
	}
	if strings.Contains(joined, "Antigravity") {
		t.Fatalf("leak anthropic %q", joined)
	}
}

func TestReverseBrand_CaseInsensitiveAndWordBoundary(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-boundary"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}},{"type":"function","function":{"name":"hub"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	respBody := `{"choices":[{"message":{"content":"ANTIGRAVITY and AntigravityX and preAntigravity"}}]}`
	payload := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	var m map[string]any
	json.Unmarshal(payload, &m)
	m["RequestID"] = reqID
	m["Model"] = "agy/model"
	payload, _ = json.Marshal(m)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
	dec := decodeBody(t, raw)
	var resp map[string]any
	json.Unmarshal(dec, &resp)
	content := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "omp") {
		t.Fatalf("case insensitive failed, got %q", content)
	}
	if strings.Contains(content, "AntigravityX") == false {
		// AntigravityX should remain unchanged (contains AntigravityX substring, but we replaced prefix? Should not replace)
		// Our content originally has "AntigravityX" which should stay as is, not become "ompX"
		if strings.Contains(content, "ompX") {
			t.Fatalf("larger token incorrectly rewritten: %q", content)
		}
	}
	if strings.Contains(content, "preAntigravity") == false {
		// preAntigravity has preceding word char, should not replace? Actually preAntigravity starts with p, then Antigravity without boundary? "preAntigravity" contains "Antigravity" after "pre" without boundary (e is word char), so left boundary fails, should not replace. But our content has "preAntigravity" as part of string " and preAntigravity" -> there is space before preAntigravity, but inside token preAntigravity, the Antigravity part is not standalone. Our replace should not replace inside that token.
		// Check not replaced
		if strings.Contains(content, "preomp") {
			t.Fatalf("inside larger token incorrectly: %q", content)
		}
	}
	// Ensure standalone ANTIGRAVITY became omp
	if strings.Count(content, "omp") != 1 {
		t.Fatalf("expected exactly one omp, got %q count %d", content, strings.Count(content, "omp"))
	}
}

func TestReverseBrand_ToolArgsPreserved(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-toolargs"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	// Response contains assistant text with Antigravity and tool args with Antigravity
	respBody := `{"choices":[{"message":{"content":"I am Antigravity","tool_calls":[{"function":{"name":"run_command","arguments":"{\"file\":\"Antigravity\"}"}}]}}]}`
	payload := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	var mm map[string]any
	json.Unmarshal(payload, &mm)
	mm["RequestID"] = reqID
	mm["Model"] = "agy/model"
	payload, _ = json.Marshal(mm)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
	dec := decodeBody(t, raw)
	var resp map[string]any
	json.Unmarshal(dec, &resp)
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	content := msg["content"].(string)
	if strings.Contains(content, "Antigravity") {
		t.Fatalf("assistant content not rewritten: %q", content)
	}
	tc := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	args := tc["arguments"].(string)
	if !strings.Contains(args, "Antigravity") {
		t.Fatalf("tool args mangled, expected Antigravity preserved, got %q", args)
	}
}

func TestReverseBrand_NonOMPGetsItsOwnWordNotOmp(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Claude Code request. A non-OMP client now runs its own reverse table, so
	// the bare brand word must come back as Claude and never as omp. Asserting
	// only "the payload does not contain omp" would pass either way.
	reqID := "rev-non-omp"
	reqBody := `{"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}},{"type":"function","function":{"name":"Edit"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	respBody := `{"choices":[{"message":{"content":"Hello Antigravity"}}]}`
	payload := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	var mm map[string]any
	json.Unmarshal(payload, &mm)
	mm["RequestID"] = reqID
	mm["Model"] = "agy/model"
	payload, _ = json.Marshal(mm)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &env)
	if env.Result.Body == "" {
		t.Fatal("claude_code reverse must rewrite the bare brand word, not pass it through")
	}
	dec, _ := base64.StdEncoding.DecodeString(env.Result.Body)
	content := string(dec)
	if !strings.Contains(content, "Hello Claude") {
		t.Fatalf("bare Antigravity was not reversed to Claude: %s", content)
	}
	if strings.Contains(content, "omp") {
		t.Fatalf("non-OMP incorrectly rewritten to omp: %s", content)
	}
}

func TestReverseBrand_ModelGateRejectedNoRewrite(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Set model gate to agy/
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))
	reqID := "rev-gate"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}}],"messages":[]}`
	// Use non-matching model
	payload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", "openai/gpt-4", []byte(reqBody))
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, payload)
	respBody := `{"choices":[{"message":{"content":"Antigravity hello"}}]}`
	rPayload := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	var m map[string]any
	json.Unmarshal(rPayload, &m)
	m["RequestID"] = reqID
	m["Model"] = "openai/gpt-4"
	m["RequestedModel"] = "openai/gpt-4"
	rPayload, _ = json.Marshal(m)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, rPayload)
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &env)
	if env.Result.Body != "" {
		dec, _ := base64.StdEncoding.DecodeString(env.Result.Body)
		if strings.Contains(string(dec), "omp") {
			t.Fatalf("model gate rejected but still rewrote: %s", string(dec))
		}
	}
	// Also test streaming gate
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "openai/gpt-4", -1, []byte(""), []byte(reqBody))
	// This header init should still be allowed to create session? But gate will block processChunk
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Antigravity\"}}]}\n\n"
	p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "openai/gpt-4", 0, []byte(chunk), nil)
	rawStream, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
	var env2 struct {
		OK     bool `json:"ok"`
		Result struct {
			Body      string `json:"Body"`
			DropChunk bool   `json:"DropChunk"`
		} `json:"result"`
	}
	json.Unmarshal(rawStream, &env2)
	if env2.Result.Body != "" {
		dec, _ := base64.StdEncoding.DecodeString(env2.Result.Body)
		if strings.Contains(string(dec), "omp") {
			t.Fatalf("stream model gate rejected but rewrote: %s", string(dec))
		}
	}
	_ = initPayload
}

func TestReverseBrand_NativeAntigravityNoRewrite(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-native"
	// Native Antigravity tools: no client is resolved, so nothing may mutate.
	reqBody := `{"tools":[{"type":"function","function":{"name":"view_file"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	respBody := `{"choices":[{"message":{"content":"Antigravity should stay"}}]}`
	payload := responseInterceptRequestJSON(t, reqBody, respBody, "openai")
	var m map[string]any
	json.Unmarshal(payload, &m)
	m["RequestID"] = reqID
	m["Model"] = "agy/model"
	payload, _ = json.Marshal(m)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &env)
	// An unresolved client means no reverse table at all, so the response comes
	// back untouched. Checking for the absence of "omp" would pass even if the
	// text had been rewritten to some other client's word.
	if env.Result.Body != "" {
		dec, _ := base64.StdEncoding.DecodeString(env.Result.Body)
		if string(dec) != respBody {
			t.Fatalf("unresolved client must not mutate the response: got %s want %s", dec, respBody)
		}
	}
}

func TestReverseBrand_ExplicitHeader_OMP_Streaming(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-explicit-stream"
	// Thin request with only read, but explicit header omp should force OMP
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}}],"messages":[]}`
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "omp")
	headers.Set("Content-Type", "application/json")
	payload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/model", []byte(reqBody), headers)
	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, payload)
	// Ensure header consumed
	_, hdrs, clearHdrs := decodeEnvelopeRequestIntercept(t, rawReq)
	if headerContainsFold(hdrs, "X-Cloak-Client") {
		t.Fatal("header not consumed")
	}
	found := false
	for _, h := range clearHdrs {
		if strings.EqualFold(h, "X-Cloak-Client") {
			found = true
		}
	}
	if !found {
		t.Fatal("ClearHeaders missing")
	}
	// Now stream with Antigravity
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Antigravity is here\"}}]}\n\n"
	p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk), nil)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
	body, _ := decodeStreamBody(t, raw)
	if !strings.Contains(string(body), "omp") {
		t.Fatalf("explicit header OMP streaming not reversed, got %q", string(body))
	}
}

func TestReverseBrand_StandaloneJSON_OMP(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-standalone-omp"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	// Standalone chunk with Antigravity text
	jsonChunk := `{"choices":[{"delta":{"content":"Hello Antigravity world"}}]}`
	payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(jsonChunk), nil)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, payload)
	body, _ := decodeStreamBody(t, raw)
	if body == nil {
		t.Fatal("expected standalone brand rewrite, got nil")
	}
	var m map[string]any
	json.Unmarshal(body, &m)
	choices := m["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	txt := delta["content"].(string)
	if !strings.Contains(txt, "omp") || strings.Contains(txt, "Antigravity") {
		t.Fatalf("standalone OMP reverse failed, txt=%q", txt)
	}
	// Ensure tool args in same standalone chunk preserved - use spaced content to allow immediate rewrite
	jsonChunk2 := `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"Antigravity"}}],"content":"Hi Antigravity there"}}]}`
	payload2 := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 1, []byte(jsonChunk2), nil)
	raw2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, payload2)
	body2, _ := decodeStreamBody(t, raw2)
	var m2 map[string]any
	json.Unmarshal(body2, &m2)
	choices2 := m2["choices"].([]any)
	delta2 := choices2[0].(map[string]any)["delta"].(map[string]any)
	// content should be rewritten (interior with spaces)
	if txt2, ok := delta2["content"].(string); ok {
		if !strings.Contains(txt2, "omp") || strings.Contains(txt2, "Antigravity") {
			t.Fatalf("standalone content not rewritten: %q", txt2)
		}
	}
	// tool args should stay
	if tcs, ok := delta2["tool_calls"].([]any); ok {
		args := tcs[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
		if !strings.Contains(args, "Antigravity") {
			t.Fatalf("standalone tool args mangled: %q", args)
		}
	}
}

func TestReverseBrand_Streaming_ToolAndBrandTogether(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-tool-brand-stream"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}},{"type":"function","function":{"name":"hub"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi Antigravity there\",\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"
	p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk), nil)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
	body, _ := decodeStreamBody(t, raw)
	s := string(body)
	if !strings.Contains(s, "\"name\":\"bash\"") {
		t.Fatalf("tool not uncloaked in brand stream, got %q", s)
	}
	if !strings.Contains(s, "omp") {
		t.Fatalf("brand not reversed alongside tool, got %q", s)
	}
	if strings.Contains(s, "Antigravity") {
		t.Fatalf("Antigravity leak in combined stream: %q", s)
	}
}

func TestReverseBrand_Streaming_LargerTokenUnchanged(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-larger-stream"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"AntigravityX stays\"}}]}\n\n"
	p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk), nil)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
	body, _ := decodeStreamBody(t, raw)
	// Larger token should not be rewritten, so plugin returns no Body (pass-through)
	if body != nil && strings.Contains(string(body), "ompX") {
		t.Fatalf("larger token incorrectly rewritten in stream: %q", string(body))
	}
	// If body is nil, it means no modification, which is correct (original AntigravityX preserved downstream)
	// Ensure we didn't incorrectly drop or rewrite
	if body != nil && !strings.Contains(string(body), "AntigravityX") {
		// If we returned a body, it should still contain original (if we had to return due to other changes, but this case no change)
		t.Fatalf("larger token incorrectly handled: %q", string(body))
	}
}
func TestReverseBrand_Streaming_AnthropicToolArgsPreserved(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-anthropic-args-stream"
	reqBody := `{"tools":[{"name":"read"},{"name":"task"},{"name":"hub"}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", "agy/model", []byte(reqBody)))
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
	// content_block with text Antigravity, next tool_use with input containing Antigravity
	chunk1 := "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Antigravity text\"}}\n\n"
	p1 := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", 0, []byte(chunk1), nil)
	raw1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p1)
	body1, _ := decodeStreamBody(t, raw1)
	if !strings.Contains(string(body1), "omp") {
		t.Fatalf("anthropic stream text not reversed: %q", string(body1))
	}
	chunk2 := "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"Antigravity\\\"}\"}}\n\n"
	p2 := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", 1, []byte(chunk2), nil)
	raw2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p2)
	body2, _ := decodeStreamBody(t, raw2)
	// input_json_delta must preserve Antigravity and return no Body (pass-through) since no text change
	if body2 != nil && strings.Contains(string(body2), "omp") {
		t.Fatalf("anthropic tool input incorrectly rewritten: %q", string(body2))
	}
	// If body2 is nil, it means no modification (correct), otherwise it should still contain Antigravity
	if body2 != nil && !strings.Contains(string(body2), "Antigravity") {
		t.Fatalf("anthropic tool input lost: %q", string(body2))
	}
}

// ── Spec #15 review remediation regressions ──────────────────────────────

func ompUncloakCache(t *testing.T) *cachedUncloakPattern {
	t.Helper()
	cached := activeFilterConfig().uncloakRegexCache["oh_my_pi"]
	if cached == nil || cached.re == nil {
		t.Fatal("missing oh_my_pi uncloak regex cache")
	}
	return cached
}

// Review finding 1: a standalone (non-SSE) stream ending on a terminal chunk
// (finish_reason or bare [DONE]) must flush held reverse-brand carries into
// that chunk and clean up the session — unmatched buffered text is never lost.
func TestReviewFix_StandaloneCarryFlushAtFinishReason(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:sa-finish", "oh_my_pi", ompUncloakCache(t))

	resp0 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-finish", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte(`{"choices":[{"index":0,"delta":{"content":"Hello Anti"}}]}`),
	}, "openai")
	if len(resp0.Body) == 0 {
		t.Fatal("expected rewritten standalone chunk (held carry stripped)")
	}
	if bytes.Contains(resp0.Body, []byte("Anti")) {
		t.Fatalf("held carry leaked before terminal chunk: %s", resp0.Body)
	}

	resp1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-finish", SourceFormat: "openai", ChunkIndex: 1,
		Body: []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	}, "openai")
	if !bytes.Contains(resp1.Body, []byte("Anti")) {
		t.Fatalf("terminal flush lost unmatched carry: %s", resp1.Body)
	}
	if bytes.Contains(resp1.Body, []byte("omp")) {
		t.Fatalf("unmatched carry falsely replaced: %s", resp1.Body)
	}
	if !bytes.Contains(resp1.Body, []byte("finish_reason")) {
		t.Fatalf("terminal chunk truth (finish_reason) dropped: %s", resp1.Body)
	}
	mgr.mu.Lock()
	_, alive := mgr.sessions["req:sa-finish"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after terminal standalone chunk")
	}
}

func TestReviewFix_StandaloneCarryFlushAtBareDone(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:sa-done", "oh_my_pi", ompUncloakCache(t))

	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-done", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte(`{"choices":[{"index":0,"delta":{"content":"tail Antigravit"}}]}`),
	}, "openai")

	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-done", SourceFormat: "openai", ChunkIndex: 1,
		Body: []byte(`[DONE]`),
	}, "openai")
	if !bytes.Contains(resp.Body, []byte("Antigravit")) {
		t.Fatalf("bare [DONE] flush lost partial carry: %s", resp.Body)
	}
	if !bytes.Contains(resp.Body, []byte("[DONE]")) {
		t.Fatalf("bare [DONE] marker swallowed: %s", resp.Body)
	}
	mgr.mu.Lock()
	_, alive := mgr.sessions["req:sa-done"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after bare [DONE] standalone chunk")
	}
}

// Review finding 2: cross-lane flush ordering must be deterministic (numeric
// lane index ascending), not Go map iteration order. Distinct lanes stay
// isolated (covered by TestReverseBrand_InterleavedLanesIsolated).
func TestReviewFix_DeterministicMultiLaneFlushOrder(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	cached := ompUncloakCache(t)
	for iter := range 25 {
		reqID := fmt.Sprintf("order-%d", iter)
		mgr := newStreamSessionManager()
		mgr.resetSession("req:"+reqID, "oh_my_pi", cached)
		mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: "openai", ChunkIndex: 0,
			Body: []byte("data: {\"choices\":[" +
				"{\"index\":2,\"delta\":{\"content\":\"a2 Anti\"}}," +
				"{\"index\":10,\"delta\":{\"content\":\"a10 Anti\"}}," +
				"{\"index\":1,\"delta\":{\"content\":\"a1 Anti\"}}," +
				"{\"index\":7,\"delta\":{\"content\":\"a7 Ant\"}}]}\n\n"),
		}, "openai")
		resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
			RequestID: reqID, SourceFormat: "openai", ChunkIndex: 1,
			Body: []byte("data: [DONE]\n\n"),
		}, "openai")
		got := flushLaneOrder(t, resp.Body)
		want := []int{1, 2, 7, 10}
		if len(got) != len(want) {
			t.Fatalf("iter %d: expected %d flush events, got %v (body=%q)", iter, len(want), got, resp.Body)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("iter %d: flush order %v, want %v (body=%q)", iter, got, want, resp.Body)
			}
		}
	}
}

// flushLaneOrder extracts choice indexes of flush events preceding [DONE].
func flushLaneOrder(t *testing.T, body []byte) []int {
	t.Helper()
	var order []int
	for _, ev := range strings.Split(string(body), "\n\n") {
		ev = strings.TrimSpace(ev)
		if !strings.HasPrefix(ev, "data: {") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(ev, "data: ")), &m); err != nil {
			t.Fatalf("unparseable flush event %q: %v", ev, err)
		}
		choices, ok := m["choices"].([]any)
		if !ok {
			continue
		}
		for _, cRaw := range choices {
			c, ok := cRaw.(map[string]any)
			if !ok {
				continue
			}
			if idx, ok := c["index"].(float64); ok {
				order = append(order, int(idx))
			}
		}
	}
	return order
}

// Review finding 3: OpenAI streaming content arrays must honor the
// assistant-text allowlist (text/output_text/untyped) like the non-stream
// path; excluded data/control/tool/reasoning parts keep literal Antigravity.
func TestReviewFix_OpenAIStreamingContentArrayAllowlist(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	arr := `[` +
		`{"type":"text","text":"a Antigravity b"},` +
		`{"type":"output_text","text":"c Antigravity d"},` +
		`{"type":"refusal","text":"e Antigravity f"},` +
		`{"type":"reasoning_text","text":"g Antigravity h"},` +
		`{"type":"tool_call_part","text":"i Antigravity j"},` +
		`{"type":"data","data":{"text":"k Antigravity l"}},` +
		`{"text":"m Antigravity n"}` +
		`]`
	wantRewritten := []string{"a omp b", "c omp d", "m omp n"}
	wantLiteral := []string{"e Antigravity f", "g Antigravity h", "i Antigravity j", "k Antigravity l"}

	// SSE-framed event
	mgr := newStreamSessionManager()
	mgr.resetSession("req:arr-sse", "oh_my_pi", ompUncloakCache(t))
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "arr-sse", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":" + arr + "}}]}\n\n"),
	}, "openai")
	checkArrayAllowlist(t, "sse", resp.Body, wantRewritten, wantLiteral)

	// Standalone (non-SSE) chunk
	mgr2 := newStreamSessionManager()
	mgr2.resetSession("req:arr-sa", "oh_my_pi", ompUncloakCache(t))
	resp2 := mgr2.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "arr-sa", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte("{\"choices\":[{\"index\":0,\"delta\":{\"content\":" + arr + "}}]}"),
	}, "openai")
	checkArrayAllowlist(t, "standalone", resp2.Body, wantRewritten, wantLiteral)
}

func checkArrayAllowlist(t *testing.T, label string, body []byte, rewritten, literal []string) {
	t.Helper()
	if len(body) == 0 {
		t.Fatalf("%s: expected rewritten body", label)
	}
	for _, s := range rewritten {
		if !strings.Contains(string(body), s) {
			t.Fatalf("%s: allowed assistant text %q not rewritten: %s", label, s, body)
		}
	}
	for _, s := range literal {
		if !strings.Contains(string(body), s) {
			t.Fatalf("%s: excluded part text %q was mangled: %s", label, s, body)
		}
	}
}

// Review finding (Spec #15 follow-up): in a standalone OpenAI stream with
// multiple choices, a finish_reason on choice 0 must NOT flush choice 1's
// held carry or delete the session — choice 1 is still streaming. Only a
// bare [DONE] / message_stop, or finish_reasons covering every known lane,
// ends the stream. Per-lane isolation must hold and the split token must
// still be rewritten when its continuation arrives.
func TestReviewFix_StandalonePartialFinishKeepsOtherLane(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:sa-partial", "oh_my_pi", ompUncloakCache(t))

	// Chunk 0: choice 1 holds a split "Anti" carry (stripped from output).
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-partial", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte(`{"choices":[{"index":0,"delta":{"content":"A"}},{"index":1,"delta":{"content":"Hello Anti"}}]}`),
	}, "openai")

	// Chunk 1: ONLY choice 0 finishes. Choice 1's carry must stay held and
	// the session must survive.
	resp1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-partial", SourceFormat: "openai", ChunkIndex: 1,
		Body: []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	}, "openai")
	if bytes.Contains(resp1.Body, []byte("Anti")) {
		t.Fatalf("unfinished choice 1 carry flushed by choice 0 finish: %s", resp1.Body)
	}
	mgr.mu.Lock()
	sess, alive := mgr.sessions["req:sa-partial"]
	var carry string
	if alive {
		if lane := sess.brandCarries["openai:1"]; lane != nil {
			carry = lane.carry
		}
	}
	mgr.mu.Unlock()
	if !alive {
		t.Fatal("session deleted while choice 1 still streaming")
	}
	if carry != "Anti" {
		t.Fatalf("choice 1 carry lost on partial finish: %q", carry)
	}
	// Chunk 2: choice 1 continues the split token and finishes — the full
	// "Antigravity" must be rewritten across the finish boundary, then the
	// session freed at true stream completion.
	resp2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-partial", SourceFormat: "openai", ChunkIndex: 2,
		Body: []byte(`{"choices":[{"index":1,"delta":{"content":"gravity world"},"finish_reason":"stop"}]}`),
	}, "openai")
	if !bytes.Contains(resp2.Body, []byte("omp world")) {
		t.Fatalf("split token across partial finish not rewritten: %s", resp2.Body)
	}
	if bytes.Contains(resp2.Body, []byte("Anti")) {
		t.Fatalf("literal brand leaked: %s", resp2.Body)
	}
	mgr.mu.Lock()
	_, alive = mgr.sessions["req:sa-partial"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after every lane finishes")
	}
}

// Spec #15 finding: the singleton-map branch of reverseBrandInOpenAIContent
// must honor the same assistant-text allowlist (text/output_text/untyped) as
// the array branch — an explicit non-text part (refusal/reasoning/tool/data)
// keeps the literal Antigravity brand.
func TestReviewFix_OpenAIContentSingletonMapAllowlist(t *testing.T) {
	literal := []map[string]any{
		{"type": "refusal", "text": "a Antigravity b"},
		{"type": "reasoning", "text": "c Antigravity d"},
		{"type": "tool_call", "text": "e Antigravity f"},
		{"type": "data", "text": "g Antigravity h"},
	}
	for _, part := range literal {
		got, changed := reverseBrandInOpenAIContent(part, "claude_code")
		if changed {
			t.Fatalf("explicit non-text part was rewritten: %v", got)
		}
		if txt, _ := part["text"].(string); !strings.Contains(txt, "Antigravity") {
			t.Fatalf("literal brand mangled in %v", part)
		}
	}
	rewritten := []map[string]any{
		{"type": "text", "text": "a Antigravity b"},
		{"type": "output_text", "text": "c Antigravity d"},
		{"text": "e Antigravity f"},
	}
	for _, part := range rewritten {
		if _, changed := reverseBrandInOpenAIContent(part, "claude_code"); !changed {
			t.Fatalf("assistant text part not rewritten: %v", part)
		}
	}
}

// Spec #15 finding (PR #19 review MEDIUM): the streaming map path
// reverseBrandOpenAIStreamingMap handled string and []any content but not the
// singleton-map content part the non-streaming path already supports. At the
// handlePluginCall stream seam: an allowed assistant text singleton map
// (text/output_text/untyped) must reverse Antigravity -> omp; an explicit
// non-text/control/tool/reasoning/refusal/data singleton map keeps the
// literal Antigravity brand.
func TestReviewFix_OpenAIStreamingSingletonMapContent(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	streamContentText := func(t *testing.T, reqID, part string) string {
		t.Helper()
		reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}}],"messages":[]}`
		handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
		initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
		handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)
		chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":" + part + "}}]}\n\n"
		p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk), nil)
		raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
		body, _ := decodeStreamBody(t, raw)
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(chunk), "data:"))
		if len(bytes.TrimSpace(body)) > 0 {
			payload = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "data:"))
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("decode streamed chunk %q: %v", payload, err)
		}
		content := m["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"]
		partMap, ok := content.(map[string]any)
		if !ok {
			t.Fatalf("content singleton map not preserved, got %T in %q", content, payload)
		}
		txt, _ := partMap["text"].(string)
		return txt
	}
	rewritten := []string{`{"type":"text","text":"a Antigravity b"}`, `{"type":"output_text","text":"c Antigravity d"}`, `{"text":"e Antigravity f"}`}
	for i, part := range rewritten {
		txt := streamContentText(t, fmt.Sprintf("rev-smap-ok-%d", i), part)
		if !strings.Contains(txt, "omp") || strings.Contains(txt, "Antigravity") {
			t.Fatalf("assistant text singleton map %s not reversed: %q", part, txt)
		}
	}
	literal := []string{`{"type":"refusal","text":"a Antigravity b"}`, `{"type":"reasoning","text":"c Antigravity d"}`, `{"type":"tool_call","text":"e Antigravity f"}`, `{"type":"data","text":"g Antigravity h"}`}
	for i, part := range literal {
		txt := streamContentText(t, fmt.Sprintf("rev-smap-lit-%d", i), part)
		if !strings.Contains(txt, "Antigravity") {
			t.Fatalf("non-text singleton map %s was rewritten: %q", part, txt)
		}
	}
}

// ── Issue #21: Anthropic native termination flush ───────────────────────────

func TestIssue21_AnthropicSSE_NativeTermination_HeldCarryFlush(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue21-sse-hold", "oh_my_pi", ompUncloakCache(t))

	// content_block_delta with trailing "Anti" holds the brand prefix.
	resp0 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sse-hold", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello Anti\"}}\n\n"),
	}, "anthropic")
	if bytes.Contains(resp0.Body, []byte("Anti")) {
		t.Fatalf("carry should be held, got %q", resp0.Body)
	}
	if !bytes.Contains(resp0.Body, []byte("Hello ")) {
		t.Fatalf("prefix lost %q", resp0.Body)
	}
	mgr.mu.Lock()
	carry := ""
	if sess := mgr.sessions["req:issue21-sse-hold"]; sess != nil {
		if lane := sess.brandCarries["anthropic:0"]; lane != nil {
			carry = lane.carry
		}
	}
	mgr.mu.Unlock()
	if carry != "Anti" {
		t.Fatalf("carry not held want Anti got %q", carry)
	}

	// Native terminal sequence without synthetic [DONE].
	term := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	respTerm := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sse-hold", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(term),
	}, "anthropic")
	body := string(respTerm.Body)
	// Flush must be an Anthropic-valid assistant text delta before the stop event, preserving lane 0 identity.
	if !strings.Contains(body, `"type":"content_block_delta"`) || !strings.Contains(body, `"text":"Anti"`) {
		t.Fatalf("held carry not flushed as valid delta before termination, body=%q", body)
	}
	if !strings.Contains(body, `"index":0`) {
		t.Fatalf("flush must preserve lane index, body=%q", body)
	}
	idxDelta := strings.Index(body, "content_block_delta")
	idxStop := strings.Index(body, "content_block_stop")
	if idxDelta < 0 || idxStop < 0 || idxDelta > idxStop {
		t.Fatalf("flush must precede content_block_stop, body=%q", body)
	}
	// Text must not be lost; joined via helper should contain Hello Anti.
	joined := sseAssistantJoined(t, resp0.Body, respTerm.Body)
	if joined != "Hello Anti" {
		t.Fatalf("lossless joined want %q got %q body0=%q term=%q", "Hello Anti", joined, string(resp0.Body), body)
	}
	// Control payloads keep literal Antigravity (none present here, but ensure no leak).
	if strings.Contains(joined, "Antigravity") {
		t.Fatalf("leak %q", joined)
	}
	// Session cleanup only after successful flush.
	mgr.mu.Lock()
	_, alive := mgr.sessions["req:issue21-sse-hold"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after native termination with successful flush")
	}
}

func TestAnthropicTerminalDetectionPreservesEscapedTypes(t *testing.T) {
	messageStop := []byte("data: {\"type\":\"message\\u005fstop\"}\n\n")
	if !sseContainsAnthropicMessageStop(messageStop) {
		t.Fatal("escaped message_stop must remain detectable")
	}

	blockStop := []byte("data: {\"type\":\"content\\u005fblock_stop\",\"index\":2}\n\n")
	kind, laneKey := sseAnthropicTerminalKind(blockStop)
	if kind != "content_block_stop" || laneKey != "anthropic:2" {
		t.Fatalf("escaped content_block_stop = (%q, %q), want (%q, %q)", kind, laneKey, "content_block_stop", "anthropic:2")
	}
}

func TestIssue21_AnthropicStandalone_MessageStopFlush(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue21-sa-msgstop", "oh_my_pi", ompUncloakCache(t))

	// Standalone content_block_delta holds Anti.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-msgstop", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi Anti"}}`),
	}, "anthropic")
	mgr.mu.Lock()
	carry := ""
	if sess := mgr.sessions["req:issue21-sa-msgstop"]; sess != nil {
		if lane := sess.brandCarries["anthropic:0"]; lane != nil {
			carry = lane.carry
		}
	}
	mgr.mu.Unlock()
	if carry != "Anti" {
		t.Fatalf("standalone carry not held %q", carry)
	}

	// Terminal payload has no delta.text field; flush must still be emitted before cleanup.
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-msgstop", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(`{"type":"message_stop"}`),
	}, "anthropic")
	body := string(resp.Body)
	if !strings.Contains(body, `"type":"content_block_delta"`) || !strings.Contains(body, `"text":"Anti"`) {
		t.Fatalf("standalone message_stop must flush pending carry as valid delta, body=%q", body)
	}
	if !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("terminal message_stop payload lost, body=%q", body)
	}
	if !strings.Contains(body, `"index":0`) {
		t.Fatalf("flush must preserve lane index, body=%q", body)
	}
	// Flush must precede terminal in the composite body.
	if strings.Index(body, "content_block_delta") > strings.Index(body, "message_stop") {
		t.Fatalf("flush must precede message_stop, body=%q", body)
	}
	mgr.mu.Lock()
	_, alive := mgr.sessions["req:issue21-sa-msgstop"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after standalone message_stop with successful flush")
	}
}

func TestIssue21_AnthropicSSE_InterleavedLanesIsolatedAndDeterministic(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue21-lanes", "oh_my_pi", ompUncloakCache(t))

	// Interleaved deltas: lane 1 holds Anti, lane 0 holds Anti — distinct lanes must not combine.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-lanes", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"b1 Anti\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"b0 Anti\"}}\n\n"),
	}, "anthropic")
	term := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-lanes", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(term),
	}, "anthropic")
	body := string(resp.Body)
	// Both lanes must flush in deterministic lane-index order (0 before 1).
	idx0 := strings.Index(body, `"index":0`)
	// Find second index 0? The flush for lane 0 has index 0, lane 1 has index 1. The stop events also have indexes. Check flush ordering by locating flush delta texts.
	// Extract flush delta order by scanning events before first content_block_stop.
	flushOrder := flushAnthropicLaneOrder(t, resp.Body)
	if len(flushOrder) != 2 || flushOrder[0] != 0 || flushOrder[1] != 1 {
		t.Fatalf("lane flush order want [0 1] got %v body=%q", flushOrder, body)
	}
	if idx0 < 0 {
		t.Fatalf("missing lane 0 flush %q", body)
	}
	// No cross-lane brand synthesis: neither lane's "Anti" combined with the other to form "Antigravity" -> omp.
	if strings.Contains(body, "omp") {
		t.Fatalf("false brand match across lanes, body=%q", body)
	}
	joined := sseAssistantJoined(t, resp.Body)
	if strings.Contains(joined, "omp") {
		t.Fatalf("false brand across lanes leaked into joined %q", joined)
	}
}

func flushAnthropicLaneOrder(t *testing.T, body []byte) []int {
	t.Helper()
	var order []int
	for _, ev := range strings.Split(string(body), "\n\n") {
		ev = strings.TrimSpace(ev)
		if !strings.HasPrefix(ev, "data: {") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(ev, "data: ")), &m); err != nil {
			continue
		}
		if m["type"] != "content_block_delta" {
			continue
		}
		if idx, ok := m["index"].(float64); ok {
			order = append(order, int(idx))
		}
	}
	return order
}

func TestIssue21_AnthropicSSE_ToolPayloadPreservedThroughNativeTermination(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue21-tool", "oh_my_pi", ompUncloakCache(t))

	// Text delta holds Anti.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-tool", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello Anti\"}}\n\n"),
	}, "anthropic")
	// Tool/input_json_delta with literal Antigravity must stay untouched, plus native termination.
	term := "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\" Antigravity \"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-tool", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(term),
	}, "anthropic")
	body := string(resp.Body)
	// Tool partial_json must retain literal Antigravity.
	if !strings.Contains(body, "Antigravity") {
		t.Fatalf("tool payload Antigravity should remain literal, body=%q", body)
	}
	// Flush text delta must be present before stop, but not corrupt tool payload.
	if !strings.Contains(body, `"text":"Anti"`) {
		t.Fatalf("text carry not flushed, body=%q", body)
	}
	// Ensure the input_json_delta block itself was not rewritten to omp.
	if strings.Contains(body, `input_json_delta`) {
		// locate the input_json_delta event line
		for _, ev := range strings.Split(body, "\n\n") {
			if strings.Contains(ev, "input_json_delta") && strings.Contains(ev, "omp") {
				t.Fatalf("tool delta incorrectly rewritten, ev=%q", ev)
			}
		}
	}
}

func TestIssue21_AnthropicStandalone_ContentBlockStopPerLaneFlush(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue21-sa-block", "oh_my_pi", ompUncloakCache(t))
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-block", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi Anti"}}`),
	}, "anthropic")
	// Also hold a second lane that must not be flushed by block 0 stop.
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-block", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"B Anti"}}`),
	}, "anthropic")
	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-block", SourceFormat: "anthropic", ChunkIndex: 2,
		Body: []byte(`{"type":"content_block_stop","index":0}`),
	}, "anthropic")
	body := string(resp.Body)
	if !strings.Contains(body, `"text":"Anti"`) {
		t.Fatalf("block 0 carry not flushed, body=%q", body)
	}
	if strings.Count(body, `"text":"Anti"`) != 1 {
		t.Fatalf("only lane 0 should flush on block 0 stop, body=%q", body)
	}
	if !strings.Contains(body, `"content_block_stop"`) {
		t.Fatalf("terminal stop lost, body=%q", body)
	}
	mgr.mu.Lock()
	_, alive := mgr.sessions["req:issue21-sa-block"]
	var carry1 string
	if sess := mgr.sessions["req:issue21-sa-block"]; sess != nil {
		if lane := sess.brandCarries["anthropic:1"]; lane != nil {
			carry1 = lane.carry
		}
	}
	mgr.mu.Unlock()
	if !alive {
		t.Fatal("session must survive content_block_stop (stream continues)")
	}
	if carry1 != "Anti" {
		t.Fatalf("other lane carry lost, got %q", carry1)
	}
	// Final message_stop must flush remaining lane.
	resp2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-block", SourceFormat: "anthropic", ChunkIndex: 3,
		Body: []byte(`{"type":"message_stop"}`),
	}, "anthropic")
	if !strings.Contains(string(resp2.Body), `"index":1`) {
		t.Fatalf("remaining lane not flushed on message_stop, body=%q", resp2.Body)
	}
	mgr.mu.Lock()
	_, alive = mgr.sessions["req:issue21-sa-block"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after message_stop")
	}
}

// sseEventsStrict parses a composite body as SSE events and fails on any
// non-blank line that is not a valid `data:`/`event:` field. A bare-JSON
// (unframed) line makes this fatal.
func sseEventsStrict(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	events := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n")
	var out []map[string]any
	for _, ev := range events {
		ev = strings.TrimSpace(ev)
		if ev == "" {
			continue
		}
		var data map[string]any
		gotData := false
		for _, line := range strings.Split(ev, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "event:") {
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				t.Fatalf("SSE event line not data:-framed: %q in body=%q", line, string(body))
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				// The OpenAI terminal marker is a valid frame, not a JSON event.
				gotData = true
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(payload), &m); err != nil {
				t.Fatalf("data: payload not valid JSON %q: %v", payload, err)
			}
			data = m
			gotData = true
		}
		if !gotData {
			t.Fatalf("event missing data: field, ev=%q body=%q", ev, string(body))
		}
		out = append(out, data)
	}
	return out
}

// Issue #21 final-review blocker: the Anthropic standalone terminal fallback
// in flushBrandStandalone must emit protocol-valid SSE. The host forwards the
// intercepted Body verbatim to the HTTP writer (handlers_stream.go payload →
// dataChan; ClaudeCodeAPIHandler c.Writer.Write(chunk)), so every event in a
// multi-event composite — synthetic flush deltas AND the terminal event —
// must carry its own `data:` framing. Previously the first synthetic delta
// was bare JSON and parsed as a malformed stream at the client.
func TestIssue21_AnthropicStandalone_TerminalFlushCompositeIsSSEFramed(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// Hold carries on two lanes so the terminal composite contains two
	// synthetic flush deltas — the exact case where the first delta used to
	// ship unframed.
	mgr := newStreamSessionManager()
	mgr.resetSession("req:issue21-sa-frame", "oh_my_pi", ompUncloakCache(t), 2)
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-frame", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi Anti"}}`),
	}, "anthropic")
	mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-frame", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Bye Anti"}}`),
	}, "anthropic")

	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-frame", SourceFormat: "anthropic", ChunkIndex: 2,
		Body: []byte(`{"type":"message_stop"}`),
	}, "anthropic")
	events := sseEventsStrict(t, resp.Body)
	if len(events) != 3 {
		t.Fatalf("want 2 flush deltas + message_stop, got %d events body=%q", len(events), resp.Body)
	}
	// Order: flush deltas first (deterministic lane order), terminal last.
	if events[0]["type"] != "content_block_delta" || events[1]["type"] != "content_block_delta" || events[2]["type"] != "message_stop" {
		t.Fatalf("event order wrong: %v %v %v body=%q", events[0]["type"], events[1]["type"], events[2]["type"], resp.Body)
	}
	// Lane-index preservation: lane 0 flush before lane 1 flush.
	i0, ok0 := events[0]["index"].(float64)
	i1, ok1 := events[1]["index"].(float64)
	if !ok0 || !ok1 || int(i0) != 0 || int(i1) != 1 {
		t.Fatalf("flush lane indices want [0 1] got %v %v body=%q", events[0]["index"], events[1]["index"], resp.Body)
	}
	t0, _ := events[0]["delta"].(map[string]any)
	t1, _ := events[1]["delta"].(map[string]any)
	if t0 == nil || t0["text"] != "Anti" || t1 == nil || t1["text"] != "Anti" {
		t.Fatalf("flush texts wrong: %v %v body=%q", events[0]["delta"], events[1]["delta"], resp.Body)
	}
	// Cleanup-safe: carries drained and session deleted after flush.
	mgr.mu.Lock()
	_, alive := mgr.sessions["req:issue21-sa-frame"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be deleted after flushed message_stop")
	}

	// Single-lane content_block_stop composite must also be fully framed.
	mgr2 := newStreamSessionManager()
	mgr2.resetSession("req:issue21-sa-frame2", "oh_my_pi", ompUncloakCache(t))
	mgr2.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-frame2", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi Anti"}}`),
	}, "anthropic")
	resp2 := mgr2.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "issue21-sa-frame2", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(`{"type":"content_block_stop","index":0}`),
	}, "anthropic")
	events2 := sseEventsStrict(t, resp2.Body)
	if len(events2) != 2 || events2[0]["type"] != "content_block_delta" || events2[1]["type"] != "content_block_stop" {
		t.Fatalf("single-lane composite wrong: %v body=%q", events2, resp2.Body)
	}
	mgr2.mu.Lock()
	sess2 := mgr2.sessions["req:issue21-sa-frame2"]
	pending := hasPendingBrandCarry(sess2)
	mgr2.mu.Unlock()
	if sess2 == nil || pending {
		t.Fatalf("session must survive block stop with drained carry: sess=%v pending=%v", sess2 != nil, pending)
	}
}

// ── Cloaked-brand stream review regressions (claude_code / codex) ─────────

// A held token flushed at content_block_stop must carry the index of the block
// it was held in. Without an index the client cannot attach the text to a
// content block, so the end of the sentence is unplaceable and is dropped.
// "A" is a prefix of every Antigravity rule, so it is held, not emitted inline.
func TestCloakedFlushKeepsContentBlockIndex(t *testing.T) {
	sess := &streamSession{client: "claude_code"}
	m := globalStreamManager

	held := []byte("event: content_block_delta\ndata: " + mustJSON(t, map[string]any{
		"type": "content_block_delta", "index": 2,
		"delta": map[string]any{"type": "text_delta", "text": "A"}}) + "\n\n")
	stop := []byte("event: content_block_stop\ndata: " + mustJSON(t, map[string]any{
		"type": "content_block_stop", "index": 2}) + "\n\n")

	out1, _ := m.reverseBrandSSE(sess, held, "anthropic")
	out2, _ := m.reverseBrandSSE(sess, stop, "anthropic")
	joined := string(out1) + string(out2)
	if strings.Contains(string(out1), `"text":"A"`) {
		t.Fatalf("the partial token should be held until the block closes, body=%q", out1)
	}

	var flush map[string]any
	for _, ev := range sseEventsStrict(t, []byte(joined)) {
		if d, ok := ev["delta"].(map[string]any); ok && d["text"] == "A" {
			flush = ev
		}
	}
	if flush == nil {
		t.Fatalf("held token was never flushed at content_block_stop, body=%q", joined)
	}
	if got, _ := flush["index"].(float64); int(got) != 2 {
		t.Fatalf("flush index = %v, want 2 (body=%q)", flush["index"], joined)
	}
	if flush["type"] != "content_block_delta" {
		t.Fatalf("flush event type = %v, body=%q", flush["type"], joined)
	}
}

// A token held open by a longer rule's lane has to be reversed by the shorter
// rule that actually owns it. "Antigravity" sits in the "Antigravity-api" lane
// because it is a prefix of that rule; resolving the carry with that rule alone
// emitted the cloaked word to the client verbatim.
func TestCloakedTerminalCarryResolvesAgainstTheWholeTable(t *testing.T) {
	sess := &streamSession{client: "claude_code"}
	m := globalStreamManager

	held := []byte("event: content_block_delta\ndata: " + mustJSON(t, map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "say Antigravity"}}) + "\n\n")
	stop := []byte("event: content_block_stop\ndata: " + mustJSON(t, map[string]any{
		"type": "content_block_stop", "index": 0}) + "\n\n")

	out1, _ := m.reverseBrandSSE(sess, held, "anthropic")
	out2, _ := m.reverseBrandSSE(sess, stop, "anthropic")
	joined := string(out1) + string(out2)
	if strings.Contains(joined, "Antigravity") {
		t.Fatalf("terminal carry kept the cloaked word, body=%q", joined)
	}
	if !strings.Contains(joined, `"text":"Claude"`) {
		t.Fatalf("terminal carry not restored to the client's word, body=%q", joined)
	}
}

// OpenAI streamed choices carry their index on the choice, not on the event
// root. Reading the root alone put every choice in one lane, so a token held
// for choice 0 was emitted through choice 1: "A" + "hello " came back as
// "" + "Ahello ".
func TestCloakedOpenAIChoicesKeepTheirOwnCarry(t *testing.T) {
	sess := &streamSession{client: "codex"}
	m := globalStreamManager

	chunk := []byte("data: " + mustJSON(t, map[string]any{"choices": []any{
		map[string]any{"index": 0, "delta": map[string]any{"content": "A"}},
		map[string]any{"index": 1, "delta": map[string]any{"content": "hello "}},
	}}) + "\n\n")
	done := []byte("data: [DONE]\n\n")

	out1, _ := m.reverseBrandSSE(sess, chunk, "openai")
	out2, _ := m.reverseBrandSSE(sess, done, "openai")
	joined := string(out1) + string(out2)
	if strings.Contains(joined, "Ahello") {
		t.Fatalf("choice 0's held token bled into choice 1, body=%q", joined)
	}

	// A client concatenates every delta addressed to a choice, so the held
	// token arriving in a later event still belongs to choice 0.
	byChoice := map[int]string{}
	for _, ev := range sseEventsStrict(t, []byte(joined)) {
		choices, _ := ev["choices"].([]any)
		for _, cRaw := range choices {
			c, ok := cRaw.(map[string]any)
			if !ok {
				continue
			}
			n, _ := c["index"].(float64)
			d, _ := c["delta"].(map[string]any)
			s, _ := d["content"].(string)
			byChoice[int(n)] += s
		}
	}
	if got := byChoice[0]; got != "A" {
		t.Fatalf("choice 0 content = %q, want %q (body=%q)", got, "A", joined)
	}
	if got := byChoice[1]; got != "hello " {
		t.Fatalf("choice 1 content = %q, want %q (body=%q)", got, "hello ", joined)
	}
}

// Tool-call arguments stream as raw JSON, so the backslash of a Windows path is
// written twice there. The whole-path rule missed the escaped spelling and only
// the bare GEMINI.md rule fired, handing the client C:\...\.gemini\CLAUDE.md.
func TestCloakedStreamedToolArgsRestoreEscapedWindowsPath(t *testing.T) {
	sess := &streamSession{client: "claude_code"}
	m := globalStreamManager

	// Split inside the path so the lane has to carry the escaped form across
	// two deltas.
	first := []byte("event: content_block_delta\ndata: " + mustJSON(t, map[string]any{
		"type": "content_block_delta", "index": 1,
		"delta": map[string]any{"type": "input_json_delta",
			"partial_json": `{"path":"C:\\Users\\dev\\.gemini\\`}}) + "\n\n")
	second := []byte("event: content_block_delta\ndata: " + mustJSON(t, map[string]any{
		"type": "content_block_delta", "index": 1,
		"delta": map[string]any{"type": "input_json_delta",
			"partial_json": `GEMINI.md"}`}}) + "\n\n")

	out1, _ := m.reverseBrandSSE(sess, first, "anthropic")
	out2, _ := m.reverseBrandSSE(sess, second, "anthropic")

	// Reassemble the fragments the way a streaming client does.
	var args strings.Builder
	for _, ev := range sseEventsStrict(t, []byte(string(out1)+string(out2))) {
		if d, ok := ev["delta"].(map[string]any); ok {
			if pj, ok := d["partial_json"].(string); ok {
				args.WriteString(pj)
			}
		}
	}
	got := args.String()
	if strings.Contains(got, ".gemini") || strings.Contains(got, "GEMINI.md") {
		t.Fatalf("cloaked path reached the client, args=%q", got)
	}
	if !strings.Contains(got, `C:\\Users\\dev\\.claude\\CLAUDE.md`) {
		t.Fatalf("Windows path not restored to the client spelling, args=%q", got)
	}
}

// ── Standalone (non-SSE) chunks: brand reverse for every resolved client ──

// The OpenAI-protocol exits hand the plugin unframed JSON chunks — the host
// adds `data: ` itself after interception (`openai_handlers.go` does
// `fmt.Fprintf(c.Writer, "data: %s\n\n", chunk)`) — so a codex or claude_code
// session takes the standalone branch, not the SSE one. Brand reverse used to be
// gated on Oh My Pi there, which left the cloaked word in every such response.
func TestStandaloneBrandReverseRunsForCodex(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:sa-codex", "codex", nil)

	resp1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-codex", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte(`{"choices":[{"index":0,"delta":{"content":"say Antigravity"}}]}`),
	}, "openai")
	if strings.Contains(string(resp1.Body), "Antigravity") {
		t.Fatalf("held token emitted inline on the standalone path: %s", resp1.Body)
	}

	resp2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-codex", SourceFormat: "openai", ChunkIndex: 1,
		Body: []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	}, "openai")
	body2 := string(resp2.Body)
	if strings.Contains(body2, "Antigravity") {
		t.Fatalf("standalone brand reverse did not run for codex: %s", body2)
	}
	if !strings.Contains(body2, `"content":"Codex"`) {
		t.Fatalf("held carry not flushed as the client's word: %s", body2)
	}

	mgr.mu.Lock()
	_, alive := mgr.sessions["req:sa-codex"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be freed at standalone stream completion")
	}
}

func TestStandaloneBrandReverseRunsForClaudeCode(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:sa-cc", "claude_code", nil)

	resp1 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-cc", SourceFormat: "anthropic", ChunkIndex: 0,
		Body: []byte(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"see .gemini/GEMINI.md then Antigravity"}}`),
	}, "anthropic")
	body1 := string(resp1.Body)
	if !strings.Contains(body1, ".claude/CLAUDE.md") || strings.Contains(body1, ".gemini/GEMINI.md") {
		t.Fatalf("standalone path did not reverse the instruction file: %s", body1)
	}
	if strings.Contains(body1, "Antigravity") {
		t.Fatalf("held token emitted inline: %s", body1)
	}

	resp2 := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-cc", SourceFormat: "anthropic", ChunkIndex: 1,
		Body: []byte(`{"type":"message_stop"}`),
	}, "anthropic")
	body2 := string(resp2.Body)
	if strings.Contains(body2, "Antigravity") || !strings.Contains(body2, `"text":"Claude"`) {
		t.Fatalf("standalone carry not flushed as the client's word: %s", body2)
	}
	if !strings.Contains(body2, `"index":2`) {
		t.Fatalf("standalone flush lost the content block index: %s", body2)
	}

	mgr.mu.Lock()
	_, alive := mgr.sessions["req:sa-cc"]
	mgr.mu.Unlock()
	if alive {
		t.Fatal("session must be freed at standalone stream completion")
	}
}

// A standalone chunk with nothing to reverse must pass through untouched,
// otherwise every such chunk would be re-encoded for no reason.
func TestStandalonePlainChunkPassesThroughForNonOMP(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	mgr := newStreamSessionManager()
	mgr.resetSession("req:sa-plain", "claude_code", nil)

	resp := mgr.processChunk(&pluginapi.StreamChunkInterceptRequest{
		RequestID: "sa-plain", SourceFormat: "openai", ChunkIndex: 0,
		Body: []byte(`{"choices":[{"index":0,"delta":{"content":"plain text"}}]}`),
	}, "openai")
	if resp.DropChunk || len(resp.Body) != 0 {
		t.Fatalf("untouched standalone chunk must pass through, got body=%q drop=%v", resp.Body, resp.DropChunk)
	}
}
