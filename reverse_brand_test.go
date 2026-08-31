package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
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
			DropChunk bool `json:"DropChunk"`
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
	// body1 should contain Hello without Anti? Our hold logic withholds Anti, so first delta becomes "Hello "
	var assembled strings.Builder
	if len(body1) > 0 {
		// extract delta content
		s := string(body1)
		// parse data JSON
		parts := strings.Split(s, "data: ")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || p == "[DONE]" {
				continue
			}
			// take first line
			line := strings.Split(p, "\n")[0]
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				if choices, ok := m["choices"].([]any); ok {
					for _, cRaw := range choices {
						c := cRaw.(map[string]any)
						if delta, ok := c["delta"].(map[string]any); ok {
							if txt, ok := delta["content"].(string); ok {
								assembled.WriteString(txt)
							}
						}
					}
				}
			}
		}
	}
	// Chunk 2 continues with gravity
	chunk2 := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"gravity world\"}}]}\n\n"
	p2 := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 1, []byte(chunk2), nil)
	raw2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p2)
	body2, _ := decodeStreamBody(t, raw2)
	if len(body2) > 0 {
		s := string(body2)
		parts := strings.Split(s, "data: ")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || p == "[DONE]" {
				continue
			}
			line := strings.Split(p, "\n")[0]
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				if choices, ok := m["choices"].([]any); ok {
					for _, cRaw := range choices {
						c := cRaw.(map[string]any)
						if delta, ok := c["delta"].(map[string]any); ok {
							if txt, ok := delta["content"].(string); ok {
								assembled.WriteString(txt)
							}
						}
					}
				}
			}
		}
	}
	// Flush at DONE
	doneChunk := "data: [DONE]\n\n"
	pDone := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 2, []byte(doneChunk), nil)
	rawDone, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, pDone)
	bodyDone, _ := decodeStreamBody(t, rawDone)
	if len(bodyDone) > 0 {
		s := string(bodyDone)
		parts := strings.Split(s, "data: ")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || p == "[DONE]" {
				continue
			}
			line := strings.Split(p, "\n")[0]
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				if choices, ok := m["choices"].([]any); ok {
					for _, cRaw := range choices {
						c := cRaw.(map[string]any)
						if delta, ok := c["delta"].(map[string]any); ok {
							if txt, ok := delta["content"].(string); ok {
								assembled.WriteString(txt)
							}
						}
					}
				}
			}
		}
	}
	final := assembled.String()
	if !strings.Contains(final, "omp") {
		t.Fatalf("fragmented brand not replaced, assembled=%q body1=%q body2=%q done=%q", final, string(body1), string(body2), string(bodyDone))
	}
	if strings.Contains(final, "Antigravity") || strings.Contains(final, "Anti") {
		t.Fatalf("leak in fragmented, final=%q", final)
	}
	// Expected Hello omp world (with Hello prefix)
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

	done := "data: [DONE]\n\n"
	pDone := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/model", 2, []byte(done), nil)
	rawDone, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, pDone)
	bDone, _ := decodeStreamBody(t, rawDone)

	// Extract texts
	var texts []string
	for _, b := range [][]byte{b1, b2, bDone} {
		s := string(b)
		parts := strings.Split(s, "data: ")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || p == "[DONE]" {
				continue
			}
			line := strings.Split(p, "\n")[0]
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				if m["type"] == "content_block_delta" {
					if delta, ok := m["delta"].(map[string]any); ok {
						if txt, ok := delta["text"].(string); ok {
							texts = append(texts, txt)
						}
					}
				}
			}
		}
	}
	joined := strings.Join(texts, "")
	if !strings.Contains(joined, "omp") {
		t.Fatalf("anthropic fragmented not replaced, joined=%q b1=%q b2=%q", joined, string(b1), string(b2))
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

func TestReverseBrand_NonOMPNoRewrite(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	// Claude Code request
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
	// Should be no change (empty Body) since not OMP
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &env)
	if env.Result.Body != "" {
		dec, _ := base64.StdEncoding.DecodeString(env.Result.Body)
		var resp map[string]any
		json.Unmarshal(dec, &resp)
		content := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if strings.Contains(content, "omp") {
			t.Fatalf("non-OMP incorrectly rewritten to omp: %q", content)
		}
	}
}

func TestReverseBrand_InterleavedLanesIsolated(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-interleaved"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}},{"type":"function","function":{"name":"hub"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)

	// Choice 0 gets Anti, Choice 1 gets gravity, they should not combine to omp
	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Anti\"}},{\"index\":1,\"delta\":{\"content\":\"gravity\"}}]}\n\n"
	p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk), nil)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
	body, _ := decodeStreamBody(t, raw)
	// Decode choices
	var assembled0, assembled1 string
	if len(body) > 0 {
		s := string(body)
		parts := strings.Split(s, "data: ")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || part == "[DONE]" {
				continue
			}
			line := strings.Split(part, "\n")[0]
			var m map[string]any
			json.Unmarshal([]byte(line), &m)
			if choices, ok := m["choices"].([]any); ok {
				for _, cRaw := range choices {
					c := cRaw.(map[string]any)
					idx := int(c["index"].(float64))
					if delta, ok := c["delta"].(map[string]any); ok {
						if txt, ok := delta["content"].(string); ok {
							if idx == 0 {
								assembled0 += txt
							} else {
								assembled1 += txt
							}
						}
					}
				}
			}
		}
	}
	// Neither lane should have omp, since fragments isolated
	if strings.Contains(assembled0, "omp") || strings.Contains(assembled1, "omp") {
		t.Fatalf("interleaved lanes incorrectly combined: 0=%q 1=%q body=%q", assembled0, assembled1, string(body))
	}
	// Flush DONE should emit remaining carries without creating false omp
	doneChunk := "data: [DONE]\n\n"
	pDone := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 1, []byte(doneChunk), nil)
	rawDone, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, pDone)
	bDone, _ := decodeStreamBody(t, rawDone)
	// After DONE, check that no omp was synthesized from cross-lane
	combined := string(body) + string(bDone)
	if strings.Contains(combined, "\"content\":\"omp\"") {
		t.Fatalf("false omp from interleaved: %q", combined)
	}
	// Flush carries should be Anti and gravity respectively, not omp
	// Parse flush events if any
}

func TestReverseBrand_UnmatchedCarryFlush(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	reqID := "rev-flush"
	reqBody := `{"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"task"}}],"messages":[]}`
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, makeIntegrationRequestInterceptPayload(t, reqID, "openai", "agy/model", []byte(reqBody)))
	initPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", -1, []byte(""), []byte(reqBody))
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, initPayload)

	chunk := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello Anti\"}}]}\n\n"
	p := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 0, []byte(chunk), nil)
	raw, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p)
	b, _ := decodeStreamBody(t, raw)
	// b should contain Hello (without Anti)
	if strings.Contains(string(b), "Anti") {
		// Might still contain Anti as held? Actually hold should make first chunk not contain Anti
	}

	// DONE should flush Anti
	done := "data: [DONE]\n\n"
	pDone := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/model", 1, []byte(done), nil)
	rawDone, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, pDone)
	bDone, _ := decodeStreamBody(t, rawDone)
	combined := string(b) + string(bDone)
	// Extract all delta contents
	var texts []string
	for _, s := range []string{string(b), string(bDone)} {
		parts := strings.Split(s, "data: ")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || part == "[DONE]" {
				continue
			}
			line := strings.Split(part, "\n")[0]
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				if choices, ok := m["choices"].([]any); ok {
					for _, cRaw := range choices {
						c := cRaw.(map[string]any)
						if delta, ok := c["delta"].(map[string]any); ok {
							if txt, ok := delta["content"].(string); ok {
								texts = append(texts, txt)
							}
						}
					}
				}
			}
		}
	}
	joined := strings.Join(texts, "")
	if !strings.Contains(joined, "Anti") {
		t.Fatalf("flush lost Anti, joined=%q b=%q bDone=%q", joined, string(b), string(bDone))
	}
	if strings.Contains(joined, "omp") {
		t.Fatalf("unexpected omp for unmatched Anti, joined=%q", joined)
	}
	_ = combined
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
			DropChunk bool `json:"DropChunk"`
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
	// Native Antigravity tools: not matching any client, so no cloak
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
	if env.Result.Body != "" {
		dec, _ := base64.StdEncoding.DecodeString(env.Result.Body)
		if strings.Contains(string(dec), "omp") {
			t.Fatalf("native antigravity incorrectly rewrote: %s", string(dec))
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
