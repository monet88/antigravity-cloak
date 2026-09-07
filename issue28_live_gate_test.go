package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestIssue28_AllNineCanonicalSafeMappings_RoundTrip validates that every one of the nine
// canonical Safe Mapping Set entries completes a real round trip:
// OMP declaration -> AGY-facing mapped name -> streamed reverse -> native OMP name.
func TestIssue28_AllNineCanonicalSafeMappings_RoundTrip(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	canonicalPairs := []struct {
		src string
		tgt string
	}{
		{"read", "view_file"},
		{"write", "write_to_file"},
		{"edit", "replace_file_content"},
		{"bash", "run_command"},
		{"grep", "grep_search"},
		{"glob", "find_by_name"},
		{"task", "invoke_subagent"},
		{"ask", "ask_question"},
		{"web_search", "search_web"},
	}

	model := "agy/gemini-3.8-flash"

	for i, pair := range canonicalPairs {
		t.Run(fmt.Sprintf("%s_to_%s", pair.src, pair.tgt), func(t *testing.T) {
			reqID := fmt.Sprintf("req-issue28-roundtrip-%d", i)
			headers := http.Header{}
			headers.Set("X-Cloak-Client", "oh_my_pi")

			reqBody := []byte(fmt.Sprintf(`{
				"model":"%s",
				"messages":[{"role":"user","content":"do work"}],
				"tools":[{"type":"function","function":{"name":"%s","parameters":{"type":"object"}}}]
			}`, model, pair.src))

			// 1. Request Intercept Before
			rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
			resp, err := decodeProtectedRequestIntercept(t, rawReq)
			if err != nil {
				t.Fatalf("[%s] decode request intercept: %v", pair.src, err)
			}
			if resp.Terminate {
				t.Fatalf("[%s] request terminated unexpectedly: %s", pair.src, string(resp.ResponseBody))
			}
			if !strings.Contains(string(resp.Body), fmt.Sprintf(`"name":"%s"`, pair.tgt)) {
				t.Fatalf("[%s] expected cloaked target %q in request body: %s", pair.src, pair.tgt, string(resp.Body))
			}
			if strings.Contains(string(resp.Body), fmt.Sprintf(`"name":"%s"`, pair.src)) {
				t.Fatalf("[%s] source name %q leaked into request body: %s", pair.src, pair.src, string(resp.Body))
			}
			if resp.Headers.Get("X-Cloak-Client") != "" {
				t.Fatalf("[%s] X-Cloak-Client control header was not stripped", pair.src)
			}

			// 2. Response Intercept After (non-streaming)
			upstreamResp := []byte(fmt.Sprintf(`{
				"choices":[{"message":{"tool_calls":[{"function":{"name":"%s","arguments":"{}"}}]}}]
			}`, pair.tgt))
			rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
				makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, upstreamResp))
			outRespBody := string(decodeEnvelopeBody(t, rawResp))
			if !strings.Contains(outRespBody, fmt.Sprintf(`"name":"%s"`, pair.src)) {
				t.Fatalf("[%s] response did not restore to native OMP name %q: %s", pair.src, pair.src, outRespBody)
			}
			if strings.Contains(outRespBody, fmt.Sprintf(`"name":"%s"`, pair.tgt)) {
				t.Fatalf("[%s] response leaked cloaked target name %q: %s", pair.src, pair.tgt, outRespBody)
			}

			// 3. Stream Chunk Intercept
			streamChunk := []byte(fmt.Sprintf("data: %s\n\n", fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"%s"}}]}}]}`, pair.tgt)))
			chunkReq := pluginapi.StreamChunkInterceptRequest{
				RequestID:    reqID,
				ChunkIndex:   0,
				SourceFormat: "openai",
				Model:        model,
				Body:         streamChunk,
			}
			rawChunkReq, _ := json.Marshal(chunkReq)
			rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
			chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
			outChunk := string(chunkBody)
			if !strings.Contains(outChunk, fmt.Sprintf(`"name":"%s"`, pair.src)) {
				t.Fatalf("[%s] stream chunk did not restore to native OMP name %q: %s", pair.src, pair.src, outChunk)
			}
			if strings.Contains(outChunk, fmt.Sprintf(`"name":"%s"`, pair.tgt)) {
				t.Fatalf("[%s] stream chunk leaked cloaked target name %q: %s", pair.src, pair.tgt, outChunk)
			}

			// 4. Lifecycle completion cleanup
			handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
		})
	}
}

// TestIssue28_ReadWrite_TransportExceptions proves the 4 required read cases and 2 required
// write cases without introducing parameter/schema conversion.
func TestIssue28_ReadWrite_TransportExceptions(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.8-flash"

	readCases := []struct {
		name string
		path string
	}{
		{"local_file", "src/main.go"},
		{"local_directory", "src/"},
		{"public_url", "https://example.com/api/v1"},
		{"xd_virtual_device", "xd://ast_grep"},
		{"xd_mcp_device", "xd://mcp__context_resolve_library_id"},
	}

	for i, rc := range readCases {
		t.Run("read_"+rc.name, func(t *testing.T) {
			reqID := fmt.Sprintf("req-issue28-read-trans-%d", i)
			headers := http.Header{}
			headers.Set("X-Cloak-Client", "oh_my_pi")

			reqBody := []byte(fmt.Sprintf(`{
				"model":"%s",
				"messages":[{"role":"user","content":"read %s"}],
				"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]
			}`, model, rc.path))

			rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
			resp, _ := decodeProtectedRequestIntercept(t, rawReq)
			if resp.Terminate {
				t.Fatalf("[%s] request terminated: %s", rc.name, string(resp.ResponseBody))
			}
			bodyStr := string(resp.Body)
			if !strings.Contains(bodyStr, `"name":"view_file"`) {
				t.Fatalf("[%s] tool name was not cloaked to view_file: %s", rc.name, bodyStr)
			}
			// Verify exact path preserved without schema rewrite
			if !strings.Contains(bodyStr, rc.path) {
				t.Fatalf("[%s] expected exact path %q preserved in request body: %s", rc.name, rc.path, bodyStr)
			}

			// Simulated model tool call with view_file and the exact path argument
			toolArgs := fmt.Sprintf(`{"path":"%s"}`, rc.path)
			upstreamResp := []byte(fmt.Sprintf(`{
				"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":%q}}]}}]
			}`, toolArgs))

			rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
				makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, upstreamResp))
			outBody := string(decodeEnvelopeBody(t, rawResp))
			if !strings.Contains(outBody, `"name":"read"`) {
				t.Fatalf("[%s] response did not restore to read: %s", rc.name, outBody)
			}
			if !strings.Contains(outBody, rc.path) {
				t.Fatalf("[%s] response corrupted path %q: %s", rc.name, rc.path, outBody)
			}

			handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
		})
	}

	writeCases := []struct {
		name    string
		path    string
		content string
	}{
		{"normal_file", "output.txt", "hello world"},
		{"xd_virtual_device", "xd://ast_edit", `{"ops":[]}`},
		{"xd_checkpoint", "xd://checkpoint", `{"goal":"verify"}`},
	}

	for i, wc := range writeCases {
		t.Run("write_"+wc.name, func(t *testing.T) {
			reqID := fmt.Sprintf("req-issue28-write-trans-%d", i)
			headers := http.Header{}
			headers.Set("X-Cloak-Client", "oh_my_pi")

			reqBody := []byte(fmt.Sprintf(`{
				"model":"%s",
				"messages":[{"role":"user","content":"write to %s"}],
				"tools":[{"type":"function","function":{"name":"write","parameters":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}}}]
			}`, model, wc.path))

			rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
			resp, _ := decodeProtectedRequestIntercept(t, rawReq)
			if resp.Terminate {
				t.Fatalf("[%s] request terminated: %s", wc.name, string(resp.ResponseBody))
			}
			bodyStr := string(resp.Body)
			if !strings.Contains(bodyStr, `"name":"write_to_file"`) {
				t.Fatalf("[%s] tool name was not cloaked to write_to_file: %s", wc.name, bodyStr)
			}
			if !strings.Contains(bodyStr, wc.path) {
				t.Fatalf("[%s] path %q missing from request body: %s", wc.name, wc.path, bodyStr)
			}

			// Stream chunk with write_to_file
			streamChunk := []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"write_to_file"}}]}}]}` + "\n\n")
			chunkReq := pluginapi.StreamChunkInterceptRequest{
				RequestID:    reqID,
				ChunkIndex:   0,
				SourceFormat: "openai",
				Model:        model,
				Body:         streamChunk,
			}
			rawChunkReq, _ := json.Marshal(chunkReq)
			rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
			chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
			outChunk := string(chunkBody)
			if !strings.Contains(outChunk, `"name":"write"`) {
				t.Fatalf("[%s] stream chunk did not restore to write: %s", wc.name, outChunk)
			}

			handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
		})
	}
}

// TestIssue28_MixedMappedAndUnmapped_NoAmbiguity proves that safe-mapped tools cloak while
// intentional pass-through tools remain untouched and inactive canonical targets do not reverse.
func TestIssue28_MixedMappedAndUnmapped_NoAmbiguity(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.8-flash"
	reqID := "req-issue28-mixed-1"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqBody := []byte(`{
		"model":"agy/gemini-3.8-flash",
		"messages":[{"role":"user","content":"run mixed tools"}],
		"tools":[
			{"type":"function","function":{"name":"read"}},
			{"type":"function","function":{"name":"bash"}},
			{"type":"function","function":{"name":"todo"}},
			{"type":"function","function":{"name":"hub"}},
			{"type":"function","function":{"name":"eval"}},
			{"type":"function","function":{"name":"vibe_spawn"}},
			{"type":"function","function":{"name":"init_experiment"}}
		]
	}`)

	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
	resp, _ := decodeProtectedRequestIntercept(t, rawReq)
	if resp.Terminate {
		t.Fatalf("mixed request terminated: %s", string(resp.ResponseBody))
	}

	bodyStr := string(resp.Body)
	// Safe-mapped tools must cloak
	if !strings.Contains(bodyStr, `"name":"view_file"`) {
		t.Errorf("read was not cloaked to view_file: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"name":"run_command"`) {
		t.Errorf("bash was not cloaked to run_command: %s", bodyStr)
	}
	// Intentional pass-through tools must remain untouched
	for _, unmapped := range []string{"todo", "hub", "eval", "vibe_spawn", "init_experiment"} {
		if !strings.Contains(bodyStr, fmt.Sprintf(`"name":"%s"`, unmapped)) {
			t.Errorf("unmapped tool %q was modified or lost: %s", unmapped, bodyStr)
		}
	}

	// Correlated response uncloak:
	// - view_file -> read (active)
	// - run_command -> bash (active)
	// - todo -> remains todo (unmapped)
	// - search_web -> remains search_web (canonical target, but web_search was INACTIVE!)
	upstreamResp := []byte(`{
		"choices":[{"message":{"tool_calls":[
			{"function":{"name":"view_file","arguments":"{}"}},
			{"function":{"name":"run_command","arguments":"{}"}},
			{"function":{"name":"todo","arguments":"{}"}},
			{"function":{"name":"search_web","arguments":"{}"}}
		]}}]
	}`)

	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, upstreamResp))
	outBody := string(decodeEnvelopeBody(t, rawResp))

	if !strings.Contains(outBody, `"name":"read"`) {
		t.Errorf("view_file did not restore to read: %s", outBody)
	}
	if !strings.Contains(outBody, `"name":"bash"`) {
		t.Errorf("run_command did not restore to bash: %s", outBody)
	}
	if !strings.Contains(outBody, `"name":"todo"`) {
		t.Errorf("todo did not remain todo: %s", outBody)
	}
	// Inactive canonical target MUST NOT be reverse-cloaked!
	if strings.Contains(outBody, `"name":"web_search"`) {
		t.Errorf("inactive canonical target search_web was improperly reverse-cloaked to web_search: %s", outBody)
	}
	if !strings.Contains(outBody, `"name":"search_web"`) {
		t.Errorf("search_web was expected to remain search_web: %s", outBody)
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
}

// TestIssue28_RequestScopedActiveReverse_TargetOnlyAndInactiveSafety proves the deterministic
// oracle: run_command and functions:run_command remain pass-through when bash was not declared.
func TestIssue28_RequestScopedActiveReverse_TargetOnlyAndInactiveSafety(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.8-flash"

	// Case 1: tools=[run_command] -> native target declared, bash NOT declared
	{
		reqID := "req-active-rev-target-1"
		headers := http.Header{}
		headers.Set("X-Cloak-Client", "oh_my_pi")

		reqBody := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"run command"}],
			"tools":[{"type":"function","function":{"name":"run_command"}}]
		}`)

		rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, rawReq)
		if resp.Terminate {
			t.Fatalf("case 1 terminated: %s", string(resp.ResponseBody))
		}
		if !strings.Contains(string(resp.Body), `"name":"run_command"`) {
			t.Fatalf("case 1 request altered run_command: %s", string(resp.Body))
		}

		// Correlated stream emitting run_command must remain run_command
		streamChunk := []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n")
		chunkReq := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        model,
			Body:         streamChunk,
		}
		rawChunkReq, _ := json.Marshal(chunkReq)
		rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
		chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
		outChunk := string(chunkBody)
		if len(outChunk) > 0 && strings.Contains(outChunk, `"name":"bash"`) {
			t.Fatalf("case 1 reverse-cloaked run_command -> bash when bash was not declared: %s", outChunk)
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	}

	// Case 2: tools=[functions:run_command]
	{
		reqID := "req-active-rev-target-2"
		headers := http.Header{}
		headers.Set("X-Cloak-Client", "oh_my_pi")

		reqBody := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"run command"}],
			"tools":[{"type":"function","function":{"name":"functions:run_command"}}]
		}`)

		rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, rawReq)
		if resp.Terminate {
			t.Fatalf("case 2 terminated: %s", string(resp.ResponseBody))
		}
		if !strings.Contains(string(resp.Body), `"name":"functions:run_command"`) {
			t.Fatalf("case 2 request altered functions:run_command: %s", string(resp.Body))
		}

		streamChunk := []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"functions:run_command"}}]}}]}` + "\n\n")
		chunkReq := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        model,
			Body:         streamChunk,
		}
		rawChunkReq, _ := json.Marshal(chunkReq)
		rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
		chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
		outChunk := string(chunkBody)
		if len(outChunk) > 0 && strings.Contains(outChunk, "bash") {
			t.Fatalf("case 2 reverse-cloaked functions:run_command -> bash: %s", outChunk)
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	}

	// Case 3: tools=[bash] -> activates run_command -> bash, but NOT other targets
	{
		reqID := "req-active-rev-target-3"
		headers := http.Header{}
		headers.Set("X-Cloak-Client", "oh_my_pi")

		reqBody := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"run bash"}],
			"tools":[{"type":"function","function":{"name":"bash"}}]
		}`)

		rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, rawReq)
		if resp.Terminate {
			t.Fatalf("case 3 terminated: %s", string(resp.ResponseBody))
		}

		// Emits both run_command and find_by_name in stream
		streamChunk := []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command"}},{"function":{"name":"find_by_name"}}]}}]}` + "\n\n")
		chunkReq := pluginapi.StreamChunkInterceptRequest{
			RequestID:    reqID,
			ChunkIndex:   0,
			SourceFormat: "openai",
			Model:        model,
			Body:         streamChunk,
		}
		rawChunkReq, _ := json.Marshal(chunkReq)
		rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
		chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
		outChunk := string(chunkBody)

		// run_command MUST restore to bash (active)
		if !strings.Contains(outChunk, `"name":"bash"`) {
			t.Fatalf("case 3 run_command did not restore to bash: %s", outChunk)
		}
		// find_by_name MUST NOT reverse to glob (glob was inactive)
		if strings.Contains(outChunk, `"name":"glob"`) {
			t.Fatalf("case 3 find_by_name improperly reversed to glob: %s", outChunk)
		}
		if !strings.Contains(outChunk, `"name":"find_by_name"`) {
			t.Fatalf("case 3 find_by_name was not preserved: %s", outChunk)
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	}
}

// TestIssue28_ProtectedBrand_DotOmp_And_Restoration proves:
// - Protected aliases mask with use_default_keywords:false
// - Custom mappings cannot redirect/reprocess canonical protected alias results
// - Literal .omp path segments are preserved byte-for-byte
// - Assistant-visible Antigravity is restored to omp even when request brand masking was a no-op.
func TestIssue28_ProtectedBrand_DotOmp_And_Restoration(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.8-flash"

	// Configure use_default_keywords:false and conflicting custom mappings
	cfg := activeFilterConfig()
	cfgCopy := *cfg
	cfgCopy.UseDefaultKeywords = false
	cfgCopy.CustomMappings = []rewriteMapping{
		{Match: "omp", Replacement: "CustomBot"},
		{Match: "Antigravity", Replacement: "CustomBot"},
	}
	applyFilterConfig(cfgCopy)

	reqID := "req-issue28-brand-1"
	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqBody := []byte(`{
		"model":"agy/gemini-3.8-flash",
		"messages":[
			{"role":"system","content":"You are Oh My Pi (omp). Config at C:\\Users\\monet\\.omp\\agent and /home/.omp/config."},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"read"}}]
	}`)

	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
	resp, _ := decodeProtectedRequestIntercept(t, rawReq)
	if resp.Terminate {
		t.Fatalf("brand request terminated: %s", string(resp.ResponseBody))
	}

	bodyStr := string(resp.Body)
	// Brand aliases must mask to Antigravity, NOT CustomBot
	if strings.Contains(bodyStr, "CustomBot") {
		t.Fatalf("custom mapping reprocessed or redirected Protected alias to CustomBot: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Antigravity") {
		t.Fatalf("Oh My Pi / omp were not masked to Antigravity: %s", bodyStr)
	}
	// Literal .omp path segments MUST be preserved
	if !strings.Contains(bodyStr, `.omp\\agent`) {
		t.Fatalf("windows path segment .omp was corrupted: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `/.omp/config`) {
		t.Fatalf("unix path segment .omp was corrupted: %s", bodyStr)
	}

	// Correlated assistant response restoration:
	// Assistant mentions Antigravity -> restored to omp
	upstreamResp := []byte(`{
		"choices":[{"message":{"role":"assistant","content":"I am Antigravity assistant."}}]
	}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, upstreamResp))
	outBody := string(decodeEnvelopeBody(t, rawResp))

	if !strings.Contains(outBody, "I am omp assistant.") {
		t.Fatalf("assistant Antigravity was not restored to omp: %s", outBody)
	}

	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))

	// Test request where brand masking was a NO-OP (no brand in request)
	{
		reqID2 := "req-issue28-brand-noop-2"
		reqBody2 := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"calculate 2+2"}],
			"tools":[{"type":"function","function":{"name":"bash"}}]
		}`)

		rawReq2, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID2, "openai", model, reqBody2, headers))
		resp2, _ := decodeProtectedRequestIntercept(t, rawReq2)
		if resp2.Terminate {
			t.Fatalf("noop request terminated: %s", string(resp2.ResponseBody))
		}

		// Correlated assistant response containing Antigravity MUST STILL be restored to omp
		upstreamResp2 := []byte(`{
			"choices":[{"message":{"role":"assistant","content":"Powered by Antigravity engine."}}]
		}`)
		rawResp2, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
			makeIntegrationResponseInterceptPayload(t, reqID2, "openai", model, upstreamResp2))
		outBody2 := string(decodeEnvelopeBody(t, rawResp2))
		if !strings.Contains(outBody2, "Powered by omp engine.") {
			t.Fatalf("assistant Antigravity was not restored to omp when request had no brand text: %s", outBody2)
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID2, "succeeded"))
	}
}

// TestIssue28_NonAGYBypass_DurableZeroMutation proves:
// - Explicit OMP + non-AGY request pins durable bypass state and performs zero request mutation
// - Correlated non-streaming response and stream perform zero uncloak/reverse-brand mutation
// - Bypass state survives until request.complete
func TestIssue28_NonAGYBypass_DurableZeroMutation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	nonAGYModel := "deepseek/deepseek-v4-pro"
	reqID := "req-issue28-bypass-1"

	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqBody := []byte(`{
		"model":"deepseek/deepseek-v4-pro",
		"messages":[{"role":"system","content":"You are Oh My Pi."},{"role":"user","content":"run bash"}],
		"tools":[{"type":"function","function":{"name":"bash"}}]
	}`)

	// 1. Request Intercept Before
	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", nonAGYModel, reqBody, headers))
	resp, _ := decodeProtectedRequestIntercept(t, rawReq)
	if resp.Terminate {
		t.Fatalf("bypass request terminated: %s", string(resp.ResponseBody))
	}
	if resp.Headers.Get("X-Cloak-Client") != "" {
		t.Fatalf("control header was not consumed on bypass path")
	}

	// Zero OMP mutation: resp.Body is empty (no rewrite performed)
	if len(resp.Body) != 0 {
		t.Fatalf("non-AGY bypass request must have zero mutation, got: %s", string(resp.Body))
	}
	route := globalLifecycleManager.getRoute(reqID)
	if route == nil || route.routeKind != routeKindExplicitOMPNonAGYBypass {
		t.Fatalf("expected routeKindExplicitOMPNonAGYBypass, got: %+v", route)
	}
	if route.brandRestorationEnabled {
		t.Fatalf("brandRestorationEnabled must be false for non-AGY bypass")
	}

	// 2. Correlated non-streaming response: zero mutation
	upstreamResp := []byte(`{
		"choices":[{"message":{
			"role":"assistant",
			"content":"I am Antigravity.",
			"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]
		}}]
	}`)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
		makeIntegrationResponseInterceptPayload(t, reqID, "openai", nonAGYModel, upstreamResp))
	outBody := decodeEnvelopeBody(t, rawResp)
	if len(outBody) != 0 {
		t.Fatalf("correlated response for non-AGY bypass must be zero mutation, got: %s", string(outBody))
	}

	// 3. Correlated stream chunk: zero mutation
	streamChunk := []byte("data: " + `{"choices":[{"delta":{"content":"Antigravity","tool_calls":[{"function":{"name":"run_command"}}]}}]}` + "\n\n")
	chunkReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        nonAGYModel,
		Body:         streamChunk,
	}
	rawChunkReq, _ := json.Marshal(chunkReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
	chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	if len(chunkBody) != 0 {
		t.Fatalf("correlated stream chunk for non-AGY bypass must be zero mutation, got: %s", string(chunkBody))
	}

	// 4. Lifecycle completion cleanup
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
}

// TestIssue28_ProtectedLifecycle_RouteSeparation_And_SafeRehydration proves:
// - Pinned ProtectedAGY route state survives eviction of disposable SSE state
// - Pre-payload rehydration reconstructs disposable state solely from pinned route state
// - Terminal [DONE] does not remove route state before request.complete
func TestIssue28_ProtectedLifecycle_RouteSeparation_And_SafeRehydration(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.8-flash"
	reqID := "req-issue28-rehydrate-1"

	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")

	reqBody := []byte(`{
		"model":"agy/gemini-3.8-flash",
		"messages":[{"role":"user","content":"run edit"}],
		"tools":[{"type":"function","function":{"name":"edit"}}]
	}`)

	rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
		makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
	resp, _ := decodeProtectedRequestIntercept(t, rawReq)
	if resp.Terminate {
		t.Fatalf("request terminated: %s", string(resp.ResponseBody))
	}

	// Evict disposable stream session while route state remains
	globalStreamManager.deleteSession("req:" + reqID)
	globalStreamManager.mu.Lock()
	sess := globalStreamManager.sessions["req:"+reqID]
	globalStreamManager.mu.Unlock()
	if sess != nil {
		t.Fatalf("expected disposable session deleted")
	}

	// Now send stream chunk. Safe rehydration should rebuild session from pinned route state!
	streamChunk := []byte("data: " + `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"replace_file_content"}}]}}]}` + "\n\n")
	chunkReq := pluginapi.StreamChunkInterceptRequest{
		RequestID:    reqID,
		ChunkIndex:   0,
		SourceFormat: "openai",
		Model:        model,
		Body:         streamChunk,
	}
	rawChunkReq, _ := json.Marshal(chunkReq)
	rawChunkResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)
	chunkBody, _ := decodeEnvelopeStreamChunk(t, rawChunkResp)
	outChunk := string(chunkBody)

	if !strings.Contains(outChunk, `"name":"edit"`) {
		t.Fatalf("rehydrated stream chunk failed to uncloak replace_file_content -> edit: %s", outChunk)
	}

	// Terminal [DONE] chunk
	terminalChunk := []byte("data: [DONE]\n\n")
	chunkReq.ChunkIndex = 1
	chunkReq.Body = terminalChunk
	rawChunkReq, _ = json.Marshal(chunkReq)
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, rawChunkReq)

	// Disposable session is now deleted by [DONE], but route state MUST survive until request.complete
	st := globalLifecycleManager.getRoute(reqID)
	if st == nil {
		t.Fatalf("expected route state to survive [DONE] until request.complete")
	}

	// request.complete arrives
	handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))

	// Now route state must be cleaned up
	stAfter := globalLifecycleManager.getRoute(reqID)
	if stAfter != nil {
		t.Fatalf("expected route state cleaned after request.complete")
	}
}

// TestIssue28_NamespaceCollisionRejection_BaseIdentity proves:
// - Collision keys compare final base identities
// - Collision with mapped base or pass-through target in another namespace returns 503
// - Distinct final bases across namespaces are admitted and reversed namespace-exact.
func TestIssue28_NamespaceCollisionRejection_BaseIdentity(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.8-flash"

	// Case 1: functions:bash + default_api:bash -> both map to run_command -> 503
	{
		reqID := "req-coll-base-1"
		headers := http.Header{}
		headers.Set("X-Cloak-Client", "oh_my_pi")

		reqBody := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"test"}],
			"tools":[
				{"type":"function","function":{"name":"functions:bash"}},
				{"type":"function","function":{"name":"default_api:bash"}}
			]
		}`)

		rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, rawReq)
		assertExact503Rejection(t, resp, "functions:bash + default_api:bash")
	}

	// Case 2: functions:bash + default_api:run_command -> one mapped, one pass-through -> same final base -> 503
	{
		reqID := "req-coll-base-2"
		headers := http.Header{}
		headers.Set("X-Cloak-Client", "oh_my_pi")

		reqBody := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"test"}],
			"tools":[
				{"type":"function","function":{"name":"functions:bash"}},
				{"type":"function","function":{"name":"default_api:run_command"}}
			]
		}`)

		rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, rawReq)
		assertExact503Rejection(t, resp, "functions:bash + default_api:run_command")
	}

	// Case 3: Distinct bases: functions:bash + default_api:read -> distinct final bases -> admitted!
	{
		reqID := "req-coll-distinct-3"
		headers := http.Header{}
		headers.Set("X-Cloak-Client", "oh_my_pi")

		reqBody := []byte(`{
			"model":"agy/gemini-3.8-flash",
			"messages":[{"role":"user","content":"test"}],
			"tools":[
				{"type":"function","function":{"name":"functions:bash"}},
				{"type":"function","function":{"name":"default_api:read"}}
			]
		}`)

		rawReq, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", model, reqBody, headers))
		resp, _ := decodeProtectedRequestIntercept(t, rawReq)
		if resp.Terminate {
			t.Fatalf("distinct namespace bases terminated: %s", string(resp.ResponseBody))
		}

		bodyStr := string(resp.Body)
		if !strings.Contains(bodyStr, `"name":"functions:run_command"`) {
			t.Fatalf("functions:bash not cloaked to functions:run_command: %s", bodyStr)
		}
		if !strings.Contains(bodyStr, `"name":"default_api:view_file"`) {
			t.Fatalf("default_api:read not cloaked to default_api:view_file: %s", bodyStr)
		}

		// Correlated response reverses namespace-exact
		upstreamResp := []byte(`{
			"choices":[{"message":{"tool_calls":[
				{"function":{"name":"functions:run_command","arguments":"{}"}},
				{"function":{"name":"default_api:view_file","arguments":"{}"}},
				{"function":{"name":"other_ns:run_command","arguments":"{}"}}
			]}}]
		}`)

		rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
			makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, upstreamResp))
		outBody := string(decodeEnvelopeBody(t, rawResp))

		if !strings.Contains(outBody, `"name":"functions:bash"`) {
			t.Fatalf("functions:run_command did not restore to functions:bash: %s", outBody)
		}
		if !strings.Contains(outBody, `"name":"default_api:read"`) {
			t.Fatalf("default_api:view_file did not restore to default_api:read: %s", outBody)
		}
		// Cross-namespace target MUST NOT be reversed!
		if strings.Contains(outBody, `"name":"other_ns:bash"`) {
			t.Fatalf("cross-namespace other_ns:run_command was improperly reversed: %s", outBody)
		}
		if !strings.Contains(outBody, `"name":"other_ns:run_command"`) {
			t.Fatalf("other_ns:run_command was not preserved: %s", outBody)
		}

		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	}
}

func makeProtectedIntegrationRequestWithModels(t *testing.T, reqID, format, model, requestedModel string, body []byte, headers http.Header) []byte {
	t.Helper()
	req := pluginapi.RequestInterceptRequest{
		RequestID:      reqID,
		SourceFormat:   format,
		Model:          model,
		RequestedModel: requestedModel,
		Body:           body,
		Headers:        headers,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return b
}

func TestIssue28_ExplicitMarker_DeterministicCoverage(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	agyModel := "agy/gemini-2.5-flash"
	nonAGYModel := "openai/gpt-4o"
	validBody := []byte(`{"model":"agy/gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`)
	validNonAGYBody := []byte(`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}`)

	// 1. Equivalent repeated OMP aliases independent of order and empty comma tokens
	t.Run("EquivalentRepeatedOMPAliases_AGY_Admitted", func(t *testing.T) {
		testCases := []struct {
			name    string
			headers func() http.Header
		}{
			{
				name: "canonical_omp_repeated_forward",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "oh_my_pi, omp, oh-my-pi")
					return h
				},
			},
			{
				name: "canonical_omp_repeated_reverse",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "oh-my-pi, omp, oh_my_pi")
					return h
				},
			},
			{
				name: "canonical_omp_with_empty_comma_tokens_and_spaces",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", " , oh_my_pi , , omp, , oh-my-pi , ")
					return h
				},
			},
			{
				name: "canonical_omp_multiple_case_insensitive_header_keys",
				headers: func() http.Header {
					return http.Header(map[string][]string{
						"X-Cloak-Client": {"oh_my_pi"},
						"x-cloak-client": {"omp , "},
						"X-CLOAK-CLIENT": {" , oh-my-pi"},
					})
				},
			},
		}

		for idx, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				reqID := fmt.Sprintf("req-marker-equiv-%d", idx)
				headers := tc.headers()
				raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
					makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, validBody, headers))
				resp, err := decodeProtectedRequestIntercept(t, raw)
				if err != nil {
					t.Fatalf("decode intercept: %v", err)
				}
				if resp.Terminate {
					t.Fatalf("expected admitted ProtectedAGY, got termination: %s", string(resp.ResponseBody))
				}
				// Verify all owned header variants are cleared
				for k := range headers {
					cleared := false
					for _, c := range resp.ClearHeaders {
						if strings.EqualFold(k, c) {
							cleared = true
							break
						}
					}
					if !cleared {
						t.Fatalf("header key %q was not marked in ClearHeaders (%v)", k, resp.ClearHeaders)
					}
					if resp.Headers != nil && resp.Headers.Get(k) != "" {
						t.Fatalf("header %q leaked into forwarded request headers", k)
					}
				}
				route := globalLifecycleManager.getRoute(reqID)
				if route == nil || route.routeKind != routeKindProtectedAGY {
					t.Fatalf("expected routeKindProtectedAGY, got %+v", route)
				}
				handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
			})
		}
	})

	// 2. Equivalent repeated OMP aliases on Non-AGY -> durable zero-mutation bypass
	t.Run("EquivalentRepeatedOMPAliases_NonAGY_DurableBypass", func(t *testing.T) {
		headers := http.Header(map[string][]string{
			"X-Cloak-Client": {"omp, oh_my_pi"},
			"x-cloak-client": {" , oh-my-pi, "},
		})

		reqID := "req-marker-nonagy-equiv-1"
		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", nonAGYModel, validNonAGYBody, headers))
		resp, err := decodeProtectedRequestIntercept(t, raw)
		if err != nil {
			t.Fatalf("decode intercept: %v", err)
		}
		if resp.Terminate {
			t.Fatalf("expected bypass, got termination: %s", string(resp.ResponseBody))
		}
		if len(resp.Body) > 0 {
			t.Fatalf("bypass route must not mutate body, got %s", string(resp.Body))
		}
		// Headers consumed
		for k := range headers {
			cleared := false
			for _, c := range resp.ClearHeaders {
				if strings.EqualFold(k, c) {
					cleared = true
					break
				}
			}
			if !cleared {
				t.Fatalf("header key %q was not marked in ClearHeaders (%v)", k, resp.ClearHeaders)
			}
		}
		route := globalLifecycleManager.getRoute(reqID)
		if route == nil || route.routeKind != routeKindExplicitOMPNonAGYBypass {
			t.Fatalf("expected routeKindExplicitOMPNonAGYBypass, got %+v", route)
		}
		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	})

	// 3. OMP+non-OMP conflicts in both orders on AGY with exact protected 503 and zero upstream
	t.Run("OMP_NonOMP_Conflicts_AGY_Exact503_ZeroUpstream", func(t *testing.T) {
		testCases := []struct {
			name    string
			headers func() http.Header
		}{
			{
				name: "omp_first_then_claude_code",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "oh_my_pi, claude_code")
					return h
				},
			},
			{
				name: "claude_code_first_then_omp",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "claude_code, oh_my_pi")
					return h
				},
			},
			{
				name: "omp_alias_then_codex",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "omp, codex")
					return h
				},
			},
			{
				name: "codex_then_oh-my-pi_alias",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "codex, oh-my-pi")
					return h
				},
			},
			{
				name: "split_headers_with_empty_tokens",
				headers: func() http.Header {
					return http.Header(map[string][]string{
						"X-Cloak-Client": {" , claude_code , "},
						"x-cloak-client": {" , omp, "},
					})
				},
			},
		}

		for idx, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				reqID := fmt.Sprintf("req-conflict-agy-%d", idx)
				headers := tc.headers()
				raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
					makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, validBody, headers))
				resp, err := decodeProtectedRequestIntercept(t, raw)
				if err != nil {
					t.Fatalf("decode intercept: %v", err)
				}
				assertExact503Rejection(t, resp, tc.name)
				// Zero-upstream guarantee: body must be empty
				if len(resp.Body) > 0 {
					t.Fatalf("zero-upstream violated: resp.Body must be empty, got %s", string(resp.Body))
				}
				// All headers consumed
				for k := range headers {
					cleared := false
					for _, c := range resp.ClearHeaders {
						if strings.EqualFold(k, c) {
							cleared = true
							break
						}
					}
					if !cleared {
						t.Fatalf("header key %q was not marked in ClearHeaders (%v)", k, resp.ClearHeaders)
					}
				}
				// No route should be pinned on 503 rejection
				if route := globalLifecycleManager.getRoute(reqID); route != nil {
					t.Fatalf("route must not be pinned on 503 rejection, got %+v", route)
				}
			})
		}
	})

	// 4. OMP+non-OMP conflicts in both orders on Non-AGY with durable zero-mutation bypass
	t.Run("OMP_NonOMP_Conflicts_NonAGY_DurableZeroMutationBypass", func(t *testing.T) {
		testCases := []struct {
			name    string
			headers func() http.Header
		}{
			{
				name: "nonagy_omp_first_then_claude_code",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "oh_my_pi, claude_code")
					return h
				},
			},
			{
				name: "nonagy_claude_code_first_then_omp",
				headers: func() http.Header {
					h := http.Header{}
					h.Set("X-Cloak-Client", "claude_code, oh_my_pi")
					return h
				},
			},
			{
				name: "nonagy_split_headers_omp_codex",
				headers: func() http.Header {
					return http.Header(map[string][]string{
						"X-Cloak-Client": {"omp"},
						"x-cloak-client": {"codex"},
					})
				},
			},
		}

		for idx, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				reqID := fmt.Sprintf("req-conflict-nonagy-%d", idx)
				headers := tc.headers()
				raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
					makeProtectedIntegrationRequest(t, reqID, "openai", nonAGYModel, validNonAGYBody, headers))
				resp, err := decodeProtectedRequestIntercept(t, raw)
				if err != nil {
					t.Fatalf("decode intercept: %v", err)
				}
				if resp.Terminate {
					t.Fatalf("expected non-AGY conflict to bypass, got termination: %s", string(resp.ResponseBody))
				}
				if len(resp.Body) > 0 {
					t.Fatalf("zero-mutation violated: resp.Body must be empty, got %s", string(resp.Body))
				}
				for k := range headers {
					cleared := false
					for _, c := range resp.ClearHeaders {
						if strings.EqualFold(k, c) {
							cleared = true
							break
						}
					}
					if !cleared {
						t.Fatalf("header key %q was not marked in ClearHeaders (%v)", k, resp.ClearHeaders)
					}
				}
				route := globalLifecycleManager.getRoute(reqID)
				if route == nil || route.routeKind != routeKindExplicitOMPNonAGYBypass {
					t.Fatalf("expected routeKindExplicitOMPNonAGYBypass, got %+v", route)
				}
				// Correlated response receives zero mutation
				dummyResp := []byte(`{"choices":[{"message":{"content":"Hello Antigravity"}}]}`)
				rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
					makeIntegrationResponseInterceptPayload(t, reqID, "openai", nonAGYModel, dummyResp))
				outBody := decodeEnvelopeBody(t, rawResp)
				if len(outBody) > 0 {
					t.Fatalf("expected zero mutation in correlated response on bypass, got %s", string(outBody))
				}
				handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
			})
		}
	})

	// 5. Preserve consumption of every owned case-insensitive header occurrence
	t.Run("PreserveConsumptionOfEveryOwnedCaseInsensitiveHeaderOccurrence", func(t *testing.T) {
		headers := http.Header(map[string][]string{
			"X-Cloak-Client": {"oh_my_pi"},
			"x-cloak-client": {"omp"},
			"X-CLOAK-CLIENT": {"oh-my-pi"},
			"X-cLoAk-cLiEnT": {"omp"},
		})

		reqID := "req-all-case-variants"
		raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
			makeProtectedIntegrationRequest(t, reqID, "openai", agyModel, validBody, headers))
		resp, err := decodeProtectedRequestIntercept(t, raw)
		if err != nil {
			t.Fatalf("decode intercept: %v", err)
		}
		if resp.Terminate {
			t.Fatalf("unexpected termination: %s", string(resp.ResponseBody))
		}
		if len(resp.ClearHeaders) != 4 {
			t.Fatalf("expected 4 ClearHeaders entries, got %d (%v)", len(resp.ClearHeaders), resp.ClearHeaders)
		}
		for _, expectedKey := range []string{"X-Cloak-Client", "x-cloak-client", "X-CLOAK-CLIENT", "X-cLoAk-cLiEnT"} {
			found := false
			for _, c := range resp.ClearHeaders {
				if c == expectedKey {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected %q in ClearHeaders, got %v", expectedKey, resp.ClearHeaders)
			}
		}
		handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
	})
}

func TestIssue28_AGYRouting_DirectAndExplicitIntegration(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// 1. Direct unit assertions on isAGYRoute
	directCases := []struct {
		model          string
		requestedModel string
		expected       bool
		reason         string
	}{
		// Model-only
		{"agy/gemini-2.5-flash", "", true, "Model-only agy/ prefix"},
		{"agy/", "", true, "Model-only minimal agy/ prefix"},
		// RequestedModel-only
		{"", "agy/gemini-2.5-flash", true, "RequestedModel-only agy/ prefix"},
		{"", "agy/", true, "RequestedModel-only minimal agy/ prefix"},
		// Disagreement
		{"agy/gemini-2.5-flash", "openai/gpt-4o", true, "Disagreement: Model has agy/, RequestedModel does not"},
		{"openai/gpt-4o", "agy/gemini-2.5-flash", true, "Disagreement: RequestedModel has agy/, Model does not"},
		{"agy/model-a", "anthropic/claude-3-5", true, "Disagreement: Model has agy/, RequestedModel is Claude"},
		// Leading/trailing whitespace
		{"  agy/gemini-2.5-flash  ", "", true, "Model with leading and trailing spaces"},
		{"", " \t\r\n agy/gemini-2.5-flash \n\t ", true, "RequestedModel with tabs and newlines"},
		{" \n agy/m \t ", " \r openai/m \n ", true, "Both have whitespace, Model matches"},
		// Neither-side AGY
		{"openai/gpt-4o", "anthropic/claude-3-5-sonnet", false, "Neither side is agy/"},
		{"", "", false, "Both empty"},
		{"   ", " \t\n ", false, "Both whitespace only"},
		{"other/agy/model", "", false, "Prefix inside path does not match"},
		// Case-sensitivity of agy/ prefix
		{"AGY/gemini-2.5-flash", "", false, "Uppercase AGY/ prefix"},
		{"", "Agy/gemini-2.5-flash", false, "Mixed case Agy/ prefix"},
		{"AGY/gemini-2.5-flash", "Agy/gemini-2.5-flash", false, "Both non-lowercase agy/"},
	}

	for _, c := range directCases {
		got := isAGYRoute(c.model, c.requestedModel)
		if got != c.expected {
			t.Errorf("[%s] isAGYRoute(%q, %q) = %v, expected %v", c.reason, c.model, c.requestedModel, got, c.expected)
		}
	}

	// 2. Integration routing: explicit OMP must be ProtectedAGY when either trimmed Model
	// or RequestedModel has case-sensitive prefix agy/, regardless of generic model_prefixes.
	// Configure restrictive model_prefixes that DO NOT include agy/
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [\"unrelated-provider/\"]")))

	headers := http.Header{}
	headers.Set("X-Cloak-Client", "oh_my_pi")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	integrationCases := []struct {
		name              string
		model             string
		requestedModel    string
		expectedRouteKind explicitOMPRouteKind
	}{
		{
			name:              "model_only_agy",
			model:             "agy/gemini-2.5-flash",
			requestedModel:    "",
			expectedRouteKind: routeKindProtectedAGY,
		},
		{
			name:              "requested_model_only_agy",
			model:             "openai/gpt-4o",
			requestedModel:    "agy/gemini-2.5-flash",
			expectedRouteKind: routeKindProtectedAGY,
		},
		{
			name:              "disagreement_model_agy",
			model:             "agy/gemini-2.5-flash",
			requestedModel:    "openai/gpt-4o",
			expectedRouteKind: routeKindProtectedAGY,
		},
		{
			name:              "disagreement_requested_model_agy",
			model:             "anthropic/claude-3-5",
			requestedModel:    "agy/gemini-2.5-flash",
			expectedRouteKind: routeKindProtectedAGY,
		},
		{
			name:              "whitespace_trimmed_agy",
			model:             "  agy/gemini-2.5-flash \t ",
			requestedModel:    "",
			expectedRouteKind: routeKindProtectedAGY,
		},
		{
			name:              "neither_side_agy_bypass",
			model:             "openai/gpt-4o",
			requestedModel:    "anthropic/claude-3-5",
			expectedRouteKind: routeKindExplicitOMPNonAGYBypass,
		},
		{
			name:              "uppercase_agy_treated_as_non_agy_bypass",
			model:             "AGY/gemini-2.5-flash",
			requestedModel:    "AGY/gemini-2.5-flash",
			expectedRouteKind: routeKindExplicitOMPNonAGYBypass,
		},
	}

	for idx, tc := range integrationCases {
		t.Run(tc.name, func(t *testing.T) {
			reqID := fmt.Sprintf("req-agy-routing-%d", idx)
			raw, _ := handlePluginCall(pluginabi.MethodRequestInterceptBefore,
				makeProtectedIntegrationRequestWithModels(t, reqID, "openai", tc.model, tc.requestedModel, body, headers))
			resp, err := decodeProtectedRequestIntercept(t, raw)
			if err != nil {
				t.Fatalf("decode intercept: %v", err)
			}
			if resp.Terminate {
				t.Fatalf("unexpected termination: %s", string(resp.ResponseBody))
			}
			route := globalLifecycleManager.getRoute(reqID)
			if route == nil {
				t.Fatalf("expected pinned route for %s, got nil", reqID)
			}
			if route.routeKind != tc.expectedRouteKind {
				t.Fatalf("expected routeKind=%v, got %v", tc.expectedRouteKind, route.routeKind)
			}
			handlePluginCall(pluginabi.MethodRequestComplete, makeRequestCompletePayload(t, reqID, "succeeded"))
		})
	}
}
