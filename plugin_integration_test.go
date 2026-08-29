package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Helper to construct request.intercept_before JSON payload
func makeIntegrationRequestInterceptPayload(t *testing.T, reqID, sourceFormat, model string, body []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.RequestInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		Body:           body,
	})
	if err != nil {
		t.Fatalf("marshal request intercept: %v", err)
	}
	return raw
}

// Helper to construct response.intercept_stream_chunk JSON payload
func makeIntegrationStreamChunkPayload(t *testing.T, reqID, sourceFormat, model string, chunkIndex int, chunkBody, reqBody []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   sourceFormat,
		Model:          model,
		RequestedModel: model,
		ChunkIndex:     chunkIndex,
		Body:           chunkBody,
		RequestBody:    reqBody,
	})
	if err != nil {
		t.Fatalf("marshal stream chunk intercept: %v", err)
	}
	return raw
}

// Helper to decode Base64 result body from interceptor envelope
func decodeEnvelopeBody(t *testing.T, rawEnvelope []byte) []byte {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Body string `json:"Body"`
		} `json:"result"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw: %s)", err, string(rawEnvelope))
	}
	if !env.OK {
		if env.Error != nil {
			t.Fatalf("envelope returned error: %s - %s", env.Error.Code, env.Error.Message)
		}
		t.Fatalf("envelope returned ok=false")
	}
	if env.Result.Body == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(env.Result.Body)
	if err != nil {
		t.Fatalf("base64 decode envelope body: %v", err)
	}
	return decoded
}

// Helper to decode Base64 result body and DropChunk from stream chunk interceptor envelope
func decodeEnvelopeStreamChunk(t *testing.T, rawEnvelope []byte) ([]byte, bool) {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			DropChunk bool   `json:"DropChunk"`
			Body      string `json:"Body"`
		} `json:"result"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw: %s)", err, string(rawEnvelope))
	}
	if !env.OK {
		if env.Error != nil {
			t.Fatalf("envelope returned error: %s - %s", env.Error.Code, env.Error.Message)
		}
		t.Fatalf("envelope returned ok=false")
	}
	if env.Result.Body == "" {
		return nil, env.Result.DropChunk
	}
	decoded, err := base64.StdEncoding.DecodeString(env.Result.Body)
	if err != nil {
		t.Fatalf("base64 decode envelope body: %v", err)
	}
	return decoded, env.Result.DropChunk
}

// TestIntegration_OhMyPi_DefaultTools_SSEStreamLifecycle tests the full lifecycle of an Oh My Pi
// session using standard 9 default tools with SSE stream chunk uncloaking.
func TestIntegration_OhMyPi_DefaultTools_SSEStreamLifecycle(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	reqID := "omp-e2e-sse-001"
	model := "agy/gemini-3.7-flash"

	// 1. Client sends request with 9 standard Oh My Pi tools
	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Oh My Pi, an interactive coding agent."},
			map[string]any{"role": "user", "content": "Please inspect files and run tests using bash and read."},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": "Read file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "write", "description": "Write file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "edit", "description": "Edit file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash", "description": "Run bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "grep", "description": "Grep code"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "glob", "description": "Glob files"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "task", "description": "Subtask"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "ask", "description": "Ask question"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "todo", "description": "Manage todo"}},
		},
		"stream": true,
	}
	clientReqBytes, err := json.Marshal(clientReq)
	if err != nil {
		t.Fatalf("marshal client req: %v", err)
	}

	// 2. Request Interceptor (request.intercept_before)
	interceptReqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptReqPayload)
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d, body=%s", code, rawResp)
	}

	rewrittenReqBytes := decodeEnvelopeBody(t, rawResp)
	if len(rewrittenReqBytes) == 0 {
		t.Fatalf("expected rewritten request body, got empty")
	}

	var cloakedReq map[string]any
	if err := json.Unmarshal(rewrittenReqBytes, &cloakedReq); err != nil {
		t.Fatalf("unmarshal cloaked request: %v", err)
	}

	// Assert brand rewrite in system message
	msgs := cloakedReq["messages"].([]any)
	sysMsg := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sysMsg, "Antigravity") {
		t.Errorf("system message brand rewrite failed, got: %s", sysMsg)
	}

	// Assert tool cloaking
	cloakedTools := cloakedReq["tools"].([]any)
	toolNames := make(map[string]bool)
	for _, item := range cloakedTools {
		toolMap := item.(map[string]any)
		fn := toolMap["function"].(map[string]any)
		toolNames[fn["name"].(string)] = true
	}

	expectedCloaked := []string{
		"run_command",
		"view_file",
		"replace_file_content",
		"grep_search",
		"list_dir",
		"invoke_subagent",
		"ask_question",
		"manage_task",
		"write_to_file",
	}
	for _, exp := range expectedCloaked {
		if !toolNames[exp] {
			t.Errorf("expected cloaked tool %q in request tools, got: %v", exp, toolNames)
		}
	}
	if toolNames["bash"] || toolNames["read"] || toolNames["edit"] {
		t.Errorf("uncloaked client tool names leaked in request: %v", toolNames)
	}

	// 3. Mock Upstream Model generates streaming SSE chunks with Antigravity tool names
	sseChunks := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"run_command\",\"arguments\":\"\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"command\\\":\\\"ls -la\\\"}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_2\",\"type\":\"function\",\"function\":{\"name\":\"view_file\",\"arguments\":\"{\\\"AbsolutePath\\\":\\\"/tmp/foo.txt\\\"}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":2,\"id\":\"call_3\",\"type\":\"function\",\"function\":{\"name\":\"invoke_subagent\",\"arguments\":\"{\\\"prompt\\\":\\\"research\\\"}\"}}]}}]}\n\n",
		"data: [DONE]\n\n",
	}

	// 4. Send each SSE chunk through response.intercept_stream_chunk
	var clientReceivedChunks []string
	for i, chunk := range sseChunks {
		chunkReqPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, i, []byte(chunk), nil)
		chunkRawResp, chunkCode := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunkReqPayload)
		if chunkCode != 0 {
			t.Fatalf("stream chunk %d failed with code %d: %s", i, chunkCode, chunkRawResp)
		}
		decodedChunk := decodeEnvelopeBody(t, chunkRawResp)
		if len(decodedChunk) > 0 {
			clientReceivedChunks = append(clientReceivedChunks, string(decodedChunk))
		} else {
			clientReceivedChunks = append(clientReceivedChunks, chunk)
		}
	}

	fullClientOutput := strings.Join(clientReceivedChunks, "")

	// Assert tool calls uncloaked back to Oh My Pi tools
	if !strings.Contains(fullClientOutput, `"name":"bash"`) {
		t.Errorf("expected 'run_command' uncloaked to 'bash' in SSE stream, got output:\n%s", fullClientOutput)
	}
	if !strings.Contains(fullClientOutput, `"name":"read"`) {
		t.Errorf("expected 'view_file' uncloaked to 'read' in SSE stream, got output:\n%s", fullClientOutput)
	}
	if !strings.Contains(fullClientOutput, `"name":"task"`) {
		t.Errorf("expected 'invoke_subagent' uncloaked to 'task' in SSE stream, got output:\n%s", fullClientOutput)
	}
	if strings.Contains(fullClientOutput, `"name":"run_command"`) || strings.Contains(fullClientOutput, `"name":"view_file"`) {
		t.Errorf("leaked Antigravity tool names in client SSE output:\n%s", fullClientOutput)
	}
}

// TestIntegration_OhMyPi_StandaloneJSONChunks tests streaming uncloak for OpenAI-style
// standalone JSON chunks without \n\n boundaries.
func TestIntegration_OhMyPi_StandaloneJSONChunks(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	reqID := "omp-standalone-json-002"
	model := "agy/gemini-2.5-flash"

	// Request with Oh My Pi tools
	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "run task"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "edit"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "task"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	// Register request
	interceptReqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptReqPayload)

	// Upstream returns individual JSON chunks (no "data:" prefix, no "\n\n")
	jsonChunk1 := `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"ls"}}]}}]}`
	jsonChunk2 := `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"replace_file_content","arguments":"{}"}}]}}]}`

	chunk1Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(jsonChunk1), nil)
	rawResp1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunk1Payload)
	out1 := decodeEnvelopeBody(t, rawResp1)
	if !strings.Contains(string(out1), `"name":"bash"`) {
		t.Errorf("standalone chunk 1 uncloak failed: %s", string(out1))
	}

	chunk2Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 1, []byte(jsonChunk2), nil)
	rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunk2Payload)
	out2 := decodeEnvelopeBody(t, rawResp2)
	if !strings.Contains(string(out2), `"name":"edit"`) {
		t.Errorf("standalone chunk 2 uncloak failed: %s", string(out2))
	}
}

// TestIntegration_OhMyPi_VibeMode_Streaming tests Vibe Mode tools (vibe_spawn, vibe_send, vibe_list).
func TestIntegration_OhMyPi_VibeMode_Streaming(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	reqID := "omp-vibe-stream-003"
	model := "agy/gemini-3.7-flash"

	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "design UI with vibe tools"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "vibe_spawn"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "vibe_send"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "vibe_list"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	// Intercept request
	interceptReqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptReqPayload)
	cloakedBytes := decodeEnvelopeBody(t, rawResp)
	if len(cloakedBytes) == 0 {
		t.Fatalf("expected rewritten request body for vibe mode, got empty")
	}

	var cloakedMap map[string]any
	json.Unmarshal(cloakedBytes, &cloakedMap)
	tools := cloakedMap["tools"].([]any)
	var cloakedNames []string
	for _, item := range tools {
		cloakedNames = append(cloakedNames, item.(map[string]any)["function"].(map[string]any)["name"].(string))
	}

	// Verify vibe tools cloaked to Antigravity equivalents (vibe_spawn -> define_subagent, vibe_send -> schedule)
	if !containsStr(cloakedNames, "define_subagent") || !containsStr(cloakedNames, "schedule") {
		t.Fatalf("vibe mode tools cloaking failed: %v", cloakedNames)
	}

	// Stream chunk returning define_subagent -> must uncloak to vibe_spawn
	sseChunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"define_subagent\"}}]}}]}\n\n"
	chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(sseChunk), nil)
	chunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunkPayload)
	out := string(decodeEnvelopeBody(t, chunkResp))

	if !strings.Contains(out, `"name":"vibe_spawn"`) {
		t.Errorf("expected define_subagent to uncloak to vibe_spawn, got: %s", out)
	}
}

// TestIntegration_OhMyPi_AutoresearchMode_Streaming tests Autoresearch Mode tools (init_experiment, run_experiment).
func TestIntegration_OhMyPi_AutoresearchMode_Streaming(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	reqID := "omp-autoresearch-stream-004"
	model := "agy/gemini-3.7-flash"

	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "conduct experiment"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "init_experiment"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "run_experiment"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "log_experiment"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "update_notes"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	// Intercept request
	interceptReqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	rawResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptReqPayload)
	cloakedBytes := decodeEnvelopeBody(t, rawResp)
	if len(cloakedBytes) == 0 {
		t.Fatalf("expected rewritten request body for autoresearch, got empty")
	}

	var cloakedMap map[string]any
	json.Unmarshal(cloakedBytes, &cloakedMap)
	tools := cloakedMap["tools"].([]any)
	var cloakedNames []string
	for _, item := range tools {
		cloakedNames = append(cloakedNames, item.(map[string]any)["function"].(map[string]any)["name"].(string))
	}

	// Verify autoresearch tools cloaked to Antigravity equivalents (init_experiment -> create_goal, run_experiment -> call_mcp_tool)
	if !containsStr(cloakedNames, "create_goal") || !containsStr(cloakedNames, "call_mcp_tool") {
		t.Fatalf("autoresearch mode tools cloaking failed: %v", cloakedNames)
	}

	// Stream chunk returning call_mcp_tool -> must uncloak to run_experiment
	sseChunk := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"call_mcp_tool\"}}]}}]}\n\n"
	chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(sseChunk), nil)
	chunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunkPayload)
	out := string(decodeEnvelopeBody(t, chunkResp))

	if !strings.Contains(out, `"name":"run_experiment"`) {
		t.Errorf("expected call_mcp_tool to uncloak to run_experiment, got: %s", out)
	}
}

// TestIntegration_SSEChunkFragmentation tests SSE chunks split across arbitrary byte boundaries.
func TestIntegration_SSEChunkFragmentation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	reqID := "omp-frag-005"
	model := "agy/gemini-3.7-flash"

	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "test"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "edit"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "task"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)
	interceptReqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptReqPayload)

	// A single SSE message fragmented across 3 TCP packets
	part1 := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_"
	part2 := "command\",\"arguments\":\"{\\\"cmd\\\":\\\"pwd\\\"}\"}}]}}]}"
	part3 := "\n\n"

	// Send part 1: incomplete frame -> buffer holds fragment, must drop chunk with empty body
	p1Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(part1), nil)
	raw1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p1Payload)
	out1, drop1 := decodeEnvelopeStreamChunk(t, raw1)
	if !drop1 || len(out1) > 0 {
		t.Fatalf("part 1: expected DropChunk=true and empty body, got DropChunk=%v body=%q", drop1, string(out1))
	}

	// Send part 2: still no \n\n boundary -> must drop chunk with empty body
	p2Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 1, []byte(part2), nil)
	raw2, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p2Payload)
	out2, drop2 := decodeEnvelopeStreamChunk(t, raw2)
	if !drop2 || len(out2) > 0 {
		t.Fatalf("part 2: expected DropChunk=true and empty body, got DropChunk=%v body=%q", drop2, string(out2))
	}

	// Send part 3: arrives with \n\n -> full frame completes and uncloaks
	p3Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 2, []byte(part3), nil)
	raw3, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p3Payload)
	out3Bytes, drop3 := decodeEnvelopeStreamChunk(t, raw3)
	if drop3 {
		t.Fatalf("part 3: expected DropChunk=false for completed frame, got DropChunk=true")
	}
	out3 := string(out3Bytes)

	if !strings.Contains(out3, `"name":"bash"`) {
		t.Errorf("fragmented chunk reassembly failed to uncloak to bash: %s", out3)
	}
}

// TestIntegration_OfflineMockServer_HTTPRoundtrip creates an offline mock HTTP server
// and executes a full simulated client roundtrip.
func TestIntegration_OfflineMockServer_HTTPRoundtrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// Set model_prefixes to agy
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")))

	// Spin up mock upstream LLM server
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected http.Flusher")
		}

		// Stream mock response with Antigravity tool name
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\",\"arguments\":\"echo 42\"}}]}}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockUpstream.Close()

	// Simulated client request
	reqID := "offline-http-mock-006"
	model := "agy/claude-3-7-sonnet"

	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Claude Code."},
			map[string]any{"role": "user", "content": "run command"},
		},
		"tools": []any{
			map[string]any{"name": "Bash"},
			map[string]any{"name": "Read"},
			map[string]any{"name": "Edit"},
		},
		"stream": true,
	}
	clientReqBytes, _ := json.Marshal(clientReq)

	// 1. Intercept request before upstream
	reqPayload := makeIntegrationRequestInterceptPayload(t, reqID, "anthropic", model, clientReqBytes)
	rawReqResp, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
	cloakedBody := decodeEnvelopeBody(t, rawReqResp)

	if !strings.Contains(string(cloakedBody), "You are Antigravity.") {
		t.Errorf("system brand rewrite failed: %s", string(cloakedBody))
	}
	if !strings.Contains(string(cloakedBody), `"name":"run_command"`) {
		t.Errorf("tool cloaking failed: %s", string(cloakedBody))
	}

	// 2. Client queries mock upstream server
	httpResp, err := http.Get(mockUpstream.URL)
	if err != nil {
		t.Fatalf("mock upstream request failed: %v", err)
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		t.Fatalf("read mock upstream body: %v", err)
	}

	// 3. Intercept stream chunk from upstream
	chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, bodyBytes, nil)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunkPayload)
	clientFinal := string(decodeEnvelopeBody(t, rawChunkResp))

	if !strings.Contains(clientFinal, `"name":"Bash"`) {
		t.Errorf("stream uncloak to Claude Code Bash failed: %s", clientFinal)
	}
}
// TestIntegration_OfflineMockServer_CloakToStreamRoundtrip proves ONE path carries
// the rewritten client request into a local mock upstream and then its streamed
// response back through the plugin to the simulated client. The mock verifies the
// cloaked tool declarations, system identity, and tool choice before it responds,
// and the client-facing result is checked for native-name restoration with no
// Antigravity leakage, including across a fragmented SSE boundary.
func TestIntegration_OfflineMockServer_CloakToStreamRoundtrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")))

	reqID := "omp-cloak-stream-007"
	model := "agy/gemini-3.7-flash"

	// 1. Client sends the standard nine Oh My Pi tools and a tool_choice.
	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Oh My Pi, an interactive coding agent."},
			map[string]any{"role": "user", "content": "inspect files then run bash"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": "Read file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "write", "description": "Write file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "edit", "description": "Edit file"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash", "description": "Run bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "grep", "description": "Grep code"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "glob", "description": "Glob files"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "task", "description": "Subtask"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "ask", "description": "Ask question"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "todo", "description": "Manage todo"}},
		},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
		"stream":      true,
	}
	clientReqBytes, err := json.Marshal(clientReq)
	if err != nil {
		t.Fatalf("marshal client req: %v", err)
	}

	// 2. Request intercept produces the cloaked body.
	interceptPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptPayload)
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d, body=%s", code, rawResp)
	}
	cloakedBody := decodeEnvelopeBody(t, rawResp)
	if !strings.Contains(string(cloakedBody), "Antigravity") {
		t.Fatalf("system identity not rewritten to Antigravity: %s", string(cloakedBody))
	}
	if !strings.Contains(string(cloakedBody), `"name":"run_command"`) {
		t.Fatalf("tool cloaking did not produce run_command: %s", string(cloakedBody))
	}

	// 3. Mock upstream validates incoming requests, captures body, and streams back SSE.
	bodyCh := make(chan []byte, 1)
	mockErrCh := make(chan error, 1)
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			err := fmt.Errorf("unexpected method: %s", r.Method)
			select {
			case mockErrCh <- err:
			default:
			}
			http.Error(w, err.Error(), http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			err := fmt.Errorf("unexpected Content-Type: %s", ct)
			select {
			case mockErrCh <- err:
			default:
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			select {
			case mockErrCh <- err:
			default:
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bodyCh <- b
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			err := fmt.Errorf("expected http.Flusher")
			select {
			case mockErrCh <- err:
			default:
			}
			return
		}
		// The model replies with the Antigravity cloaked tool name.
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\",\"arguments\":\"echo 42\"}}]}}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockUpstream.Close()

	resp, err := http.Post(mockUpstream.URL, "application/json", strings.NewReader(string(cloakedBody)))
	if err != nil {
		t.Fatalf("post to mock upstream: %v", err)
	}
	defer resp.Body.Close()
	select {
	case mockErr := <-mockErrCh:
		t.Fatalf("mock upstream received unexpected request: %v", mockErr)
	default:
	}
	streamBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read mock stream: %v", err)
	}
	receivedBody := <-bodyCh
	// 4. The mock must have received the CLOAKED request, not the client original.
	if bytes.Contains(receivedBody, []byte(`"name":"read"`)) {
		t.Errorf("mock upstream received uncloaked 'read': %s", string(receivedBody))
	}
	if !bytes.Contains(receivedBody, []byte(`"name":"run_command"`)) {
		t.Errorf("mock upstream did not receive cloaked run_command: %s", string(receivedBody))
	}
	if !bytes.Contains(receivedBody, []byte("Antigravity")) {
		t.Errorf("mock upstream did not receive Antigravity system identity: %s", string(receivedBody))
	}
	// tool_choice must be cloaked too — verify its exact JSON path, independent
	// of the tool declarations which share the same "name" key.
	var received map[string]any
	if err := json.Unmarshal(receivedBody, &received); err != nil {
		t.Fatalf("mock upstream body not JSON: %v", err)
	}
	tc, _ := received["tool_choice"].(map[string]any)
	tcFn, _ := tc["function"].(map[string]any)
	if tcName, _ := tcFn["name"].(string); tcName != "run_command" {
		t.Errorf("tool_choice not cloaked to run_command, got %v", tc["function"])
	}

	// 5. Carry the mock's streamed response back through the plugin. The mock
	// returned a real SSE event; fragment it mid tool name to exercise the
	// reassembly buffer on actual streamed bytes.
	streamStr := string(streamBytes)
	if !strings.Contains(streamStr, "run_command") || !strings.Contains(streamStr, "[DONE]") {
		t.Fatalf("unexpected mock stream: %s", streamStr)
	}
	sep := strings.Index(streamStr, "\n\n")
	if sep < 0 {
		t.Fatalf("mock stream lacks an SSE boundary: %s", streamStr)
	}
	firstEvent := streamStr[:sep]
	rest := streamStr[sep:]
	mid := strings.Index(firstEvent, "run_")
	if mid < 0 {
		t.Fatalf("mock stream lacks run_command: %s", streamStr)
	}
	mid += len("run_")
	parts := []string{
		firstEvent[:mid], // ends inside "run_" — incomplete frame
		firstEvent[mid:], // rest of the JSON event — still no \n\n, buffered
		rest,             // "\n\n" + [DONE] — completes the frame
	}
	var finalOut strings.Builder
	completeFrames := 0
	for i, part := range parts {
		chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, i, []byte(part), nil)
		rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunkPayload)
		out, dropChunk := decodeEnvelopeStreamChunk(t, rawChunk)
		if i < len(parts)-1 {
			// Incomplete fragment: must be dropped with DropChunk=true and empty body
			if !dropChunk || len(out) > 0 {
				t.Fatalf("fragment %d: expected DropChunk=true and empty body, got DropChunk=%v, body=%q", i, dropChunk, string(out))
			}
			continue
		}
		// Final completed frame
		if dropChunk {
			t.Fatalf("final frame had DropChunk=true")
		}
		if len(out) == 0 {
			t.Fatalf("final non-empty frame produced no output")
		}
		completeFrames++
		finalOut.Write(out)
	}
	if completeFrames < 1 {
		t.Fatalf("expected at least one complete frame, got %d", completeFrames)
	}
	if completeFrames > 1 {
		t.Fatalf("expected exactly one complete frame, got %d", completeFrames)
	}
	final := finalOut.String()
	if strings.Contains(final, "run_command") {
		t.Errorf("Antigravity name leaked to client: %s", final)
	}
	if !strings.Contains(final, `"name":"bash"`) {
		t.Errorf("stream uncloak did not restore bash: %s", final)
	}
}

// TestIntegration_OfflineMockServer_VibeMode_Roundtrip proves the same one-path
// lifecycle for Vibe Mode tool calls, which are carried as standalone newline-free
// JSON chunks (no SSE framing) and restored to their native names.
func TestIntegration_OfflineMockServer_VibeMode_Roundtrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")))

	reqID := "omp-vibe-roundtrip-008"
	model := "agy/gemini-3.7-flash"
	clientReq := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Oh My Pi."},
			map[string]any{"role": "user", "content": "spawn a vibe"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "vibe_spawn", "description": "Spawn subagent"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "vibe_send", "description": "Send to subagent"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "vibe_list", "description": "List subagents"}},
		},
		"stream": true,
	}
	clientReqBytes, _ := json.Marshal(clientReq)
	interceptPayload := makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes)
	rawResp, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, interceptPayload)
	if code != 0 {
		t.Fatalf("request.intercept_before code=%d", code)
	}
	cloakedBody := decodeEnvelopeBody(t, rawResp)
	if !strings.Contains(string(cloakedBody), `"name":"define_subagent"`) {
		t.Fatalf("vibe_spawn not cloaked to define_subagent: %s", string(cloakedBody))
	}
	if !strings.Contains(string(cloakedBody), `"name":"schedule"`) {
		t.Fatalf("vibe_send not cloaked to schedule: %s", string(cloakedBody))
	}

	// Model replies with a standalone JSON chunk (no \n\n framing).
	chunkBody := `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"define_subagent","arguments":"{}"}}]}}]}`
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk,
		makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(chunkBody), nil))
	clientFinal := string(decodeEnvelopeBody(t, rawChunk))
	if strings.Contains(clientFinal, "define_subagent") {
		t.Errorf("vibe Antigravity name leaked to client: %s", clientFinal)
	}
	if !strings.Contains(clientFinal, `"name":"vibe_spawn"`) {
		t.Errorf("vibe_spawn not restored to client: %s", clientFinal)
	}
}

// TestIntegration_OfflineMockServer_MalformedPayload fails loudly on malformed
// stream chunks instead of silently passing them through.
func TestIntegration_OfflineMockServer_MalformedPayload(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")))
	reqID := "omp-malformed-009"
	model := "agy/gemini-3.7-flash"
	clientReq := map[string]any{
		"model": model,
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "write"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes))

	// A handler envelope decode failure must surface as an error envelope,
	// never as a silent ok=true no-op.
	rawBad, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, []byte(`{not-json`))
	var env struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rawBad, &env); err != nil {
		t.Fatalf("response not a JSON envelope: %v", err)
	}
	if env.OK {
		t.Fatalf("malformed request must return an error envelope, got ok=true")
	}
}

// TestIntegration_OfflineMockServer_UnexpectedMockRequest fails the test if the
// mock receives a request it was not configured to handle (e.g. a GET when the
// lifecycle demands a POST of the cloaked body).
func TestIntegration_OfflineMockServer_UnexpectedMockRequest(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy]")))

	reqID := "omp-unexpected-010"
	model := "agy/gemini-3.7-flash"
	clientReq := map[string]any{
		"model": model,
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "bash"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "read"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "edit"}},
		},
	}
	clientReqBytes, _ := json.Marshal(clientReq)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeIntegrationRequestInterceptPayload(t, reqID, "openai", model, clientReqBytes))

	mockErrCh := make(chan error, 1)
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			err := fmt.Errorf("unexpected method: %s", r.Method)
			select {
			case mockErrCh <- err:
			default:
			}
			http.Error(w, err.Error(), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer mockUpstream.Close()

	resp, err := http.Get(mockUpstream.URL)
	if err != nil {
		t.Fatalf("GET mock upstream failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405 Method Not Allowed for unexpected mock request, got %d", resp.StatusCode)
	}
	select {
	case err := <-mockErrCh:
		if !strings.Contains(err.Error(), "unexpected method: GET") {
			t.Errorf("unexpected error received: %v", err)
		}
	default:
		t.Fatalf("mock upstream did not record error for unexpected GET request")
	}
}

func containsStr(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
