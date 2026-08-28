package main

import (
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

	// Send part 1: incomplete frame -> buffer holds fragment
	p1Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, []byte(part1), nil)
	raw1, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p1Payload)
	var env1 struct {
		Result struct {
			DropChunk bool   `json:"DropChunk"`
			Body      string `json:"Body"`
		} `json:"result"`
	}
	json.Unmarshal(raw1, &env1)
	if !env1.Result.DropChunk && env1.Result.Body != "" {
		t.Logf("part 1 response: DropChunk=%v, Body=%s", env1.Result.DropChunk, env1.Result.Body)
	}

	// Send part 2: still no \n\n boundary
	p2Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 1, []byte(part2), nil)
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p2Payload)

	// Send part 3: arrives with \n\n -> full frame completes and uncloaks
	p3Payload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 2, []byte(part3), nil)
	raw3, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, p3Payload)
	out3 := string(decodeEnvelopeBody(t, raw3))

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

func containsStr(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
