package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestIssue39_ConcurrentTargetReuseIsolation verifies that concurrent Claude Code,
// Codex, and Oh My Pi requests sharing the exact same AGY targets ("run_command")
// and shared alias targets ("wp_send_message", "wp_list_workers") restore only
// their own exact source identities across non-streaming responses and streaming
// SSE chunks without cross-contamination.
func TestIssue39_ConcurrentTargetReuseIsolation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	const concurrency = 20
	var wg sync.WaitGroup

	for i := range concurrency {
		wg.Add(3)

		// 1. Claude Code worker
		go func(workerID int) {
			defer wg.Done()
			reqID := fmt.Sprintf("cc-iso-%d", workerID)
			reqBody := []byte(`{
				"model": "agy/claude-3-7-sonnet",
				"messages": [{"role": "user", "content": "hello"}],
				"tools": [
					{"name": "Bash", "description": "run shell"},
					{"name": "SendMessage", "description": "send msg"},
					{"name": "ListAgents", "description": "list agents"},
					{"name": "mcp__custom_cc_tool", "description": "custom mcp"}
				]
			}`)
			headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
			reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-3-7-sonnet", reqBody, headers)
			rawEnvelope, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
			if code != 0 {
				t.Errorf("CC req intercept failed: %d", code)
				return
			}
			cloakedReq := decodeEnvelopeBody(t, rawEnvelope)
			if !strings.Contains(string(cloakedReq), `"run_command"`) ||
				!strings.Contains(string(cloakedReq), `"wp_send_message"`) ||
				!strings.Contains(string(cloakedReq), `"wp_list_workers"`) ||
				!strings.Contains(string(cloakedReq), `"wp_ext_`) {
				t.Errorf("CC request cloaking incomplete: %s", cloakedReq)
				return
			}

			// Plan inspection
			plan := globalAliasPlanManager.get(reqID)
			if plan == nil {
				t.Errorf("CC alias plan not pinned: %s", reqID)
				return
			}
			fallbackTarget := plan.forward["mcp__custom_cc_tool"]

			// Non-stream response uncloak
			respBody := []byte(fmt.Sprintf(`{
				"content": [
					{"type": "tool_use", "id": "tu_1", "name": "run_command", "input": {}},
					{"type": "tool_use", "id": "tu_2", "name": "wp_send_message", "input": {}},
					{"type": "tool_use", "id": "tu_3", "name": "wp_list_workers", "input": {}},
					{"type": "tool_use", "id": "tu_4", "name": "%s", "input": {}}
				]
			}`, fallbackTarget))
			respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-3-7-sonnet", respBody)
			rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
			if codeResp != 0 {
				t.Errorf("CC resp intercept failed: %d", codeResp)
				return
			}
			decodedResp := string(decodeEnvelopeBody(t, rawResp))
			if !strings.Contains(decodedResp, `"Bash"`) ||
				!strings.Contains(decodedResp, `"SendMessage"`) ||
				!strings.Contains(decodedResp, `"ListAgents"`) ||
				!strings.Contains(decodedResp, `"mcp__custom_cc_tool"`) {
				t.Errorf("CC response restoration failed: %s", decodedResp)
			}
			// Must NOT contain Codex or OMP sources
			if strings.Contains(decodedResp, `"exec"`) || strings.Contains(decodedResp, `"collaboration__`) || strings.Contains(decodedResp, `"bash"`) {
				t.Errorf("CC response contaminated with foreign identities: %s", decodedResp)
			}

			// Streaming chunk uncloak
			chunkBody := []byte(fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"run_command\"}}\n\n"))
			streamPayload := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/claude-3-7-sonnet", 0, chunkBody, cloakedReq)
			rawChunk, codeChunk := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, streamPayload)
			if codeChunk != 0 {
				t.Errorf("CC stream chunk failed: %d", codeChunk)
				return
			}
			decodedChunk := string(decodeEnvelopeBody(t, rawChunk))
			if !strings.Contains(decodedChunk, `"Bash"`) {
				t.Errorf("CC stream chunk restoration failed: %s", decodedChunk)
			}
			if strings.Contains(decodedChunk, `"exec"`) || strings.Contains(decodedChunk, `"bash"`) {
				t.Errorf("CC stream chunk contaminated: %s", decodedChunk)
			}

			// Complete lifecycle
			var completed pluginapi.RequestCompletion
			ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &completed)
		}(i)

		// 2. Codex worker (code mode)
		go func(workerID int) {
			defer wg.Done()
			reqID := fmt.Sprintf("codex-iso-%d", workerID)
			reqBody := []byte(`{
				"model": "agy/codex-model",
				"messages": [{"role": "user", "content": "hello"}],
				"tools": [
					{"type": "function", "function": {"name": "exec"}},
					{"type": "function", "function": {"name": "collaboration__send_message"}},
					{"type": "function", "function": {"name": "collaboration__list_agents"}},
					{"type": "function", "function": {"name": "mcp__custom_codex_tool"}}
				]
			}`)
			headers := http.Header{"X-Cloak-Client": []string{"codex"}}
			reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/codex-model", reqBody, headers)
			rawEnvelope, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
			if code != 0 {
				t.Errorf("Codex req intercept failed: %d", code)
				return
			}
			cloakedReq := decodeEnvelopeBody(t, rawEnvelope)
			if !strings.Contains(string(cloakedReq), `"run_command"`) ||
				!strings.Contains(string(cloakedReq), `"wp_send_message"`) ||
				!strings.Contains(string(cloakedReq), `"wp_list_workers"`) ||
				!strings.Contains(string(cloakedReq), `"wp_ext_`) {
				t.Errorf("Codex request cloaking incomplete: %s", cloakedReq)
				return
			}

			plan := globalAliasPlanManager.get(reqID)
			if plan == nil {
				t.Errorf("Codex alias plan not pinned: %s", reqID)
				return
			}
			fallbackTarget := plan.forward["mcp__custom_codex_tool"]

			// Non-stream response uncloak
			respBody := []byte(fmt.Sprintf(`{
				"choices": [{
					"message": {
						"tool_calls": [
							{"function": {"name": "run_command", "arguments": "{}"}},
							{"function": {"name": "wp_send_message", "arguments": "{}"}},
							{"function": {"name": "wp_list_workers", "arguments": "{}"}},
							{"function": {"name": "%s", "arguments": "{}"}}
						]
					}
				}]
			}`, fallbackTarget))
			respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "openai", "agy/codex-model", respBody)
			rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
			if codeResp != 0 {
				t.Errorf("Codex resp intercept failed: %d", codeResp)
				return
			}
			decodedResp := string(decodeEnvelopeBody(t, rawResp))
			if !strings.Contains(decodedResp, `"exec"`) ||
				!strings.Contains(decodedResp, `"collaboration__send_message"`) ||
				!strings.Contains(decodedResp, `"collaboration__list_agents"`) ||
				!strings.Contains(decodedResp, `"mcp__custom_codex_tool"`) {
				t.Errorf("Codex response restoration failed: %s", decodedResp)
			}
			// Must NOT contain Claude Code or OMP sources
			if strings.Contains(decodedResp, `"Bash"`) || strings.Contains(decodedResp, `"SendMessage"`) || strings.Contains(decodedResp, `"ListAgents"`) {
				t.Errorf("Codex response contaminated with foreign identities: %s", decodedResp)
			}

			// Streaming chunk uncloak
			chunkBody := []byte(fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"))
			streamPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/codex-model", 0, chunkBody, cloakedReq)
			rawChunk, codeChunk := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, streamPayload)
			if codeChunk != 0 {
				t.Errorf("Codex stream chunk failed: %d", codeChunk)
				return
			}
			decodedChunk := string(decodeEnvelopeBody(t, rawChunk))
			if !strings.Contains(decodedChunk, `"exec"`) {
				t.Errorf("Codex stream chunk restoration failed: %s", decodedChunk)
			}
			if strings.Contains(decodedChunk, `"Bash"`) || strings.Contains(decodedChunk, `"bash"`) {
				t.Errorf("Codex stream chunk contaminated: %s", decodedChunk)
			}

			// Complete lifecycle
			var completed pluginapi.RequestCompletion
			ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &completed)
		}(i)

		// 3. Oh My Pi worker (ProtectedAGY)
		go func(workerID int) {
			defer wg.Done()
			reqID := fmt.Sprintf("omp-iso-%d", workerID)
			reqBody := []byte(`{
				"model": "agy/omp-model",
				"messages": [{"role": "user", "content": "hello"}],
				"tools": [
					{"type": "function", "function": {"name": "bash"}},
					{"type": "function", "function": {"name": "read"}},
					{"type": "function", "function": {"name": "vibe_send"}},
					{"type": "function", "function": {"name": "mcp__custom_omp_tool"}}
				]
			}`)
			headers := http.Header{"X-Cloak-Client": []string{"oh_my_pi"}}
			reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/omp-model", reqBody, headers)
			rawEnvelope, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
			if code != 0 {
				t.Errorf("OMP req intercept failed: %d", code)
				return
			}
			cloakedReq := decodeEnvelopeBody(t, rawEnvelope)
			if !strings.Contains(string(cloakedReq), `"run_command"`) ||
				!strings.Contains(string(cloakedReq), `"view_file"`) ||
				!strings.Contains(string(cloakedReq), `"wp_vibe_send"`) ||
				!strings.Contains(string(cloakedReq), `"wp_ext_`) {
				t.Errorf("OMP request cloaking incomplete: %s", cloakedReq)
				return
			}

			route := globalLifecycleManager.getRoute(reqID)
			if route == nil {
				t.Errorf("OMP route not pinned: %s", reqID)
				return
			}
			var fallbackTarget string
			for target, orig := range route.activeReverse {
				if orig == "mcp__custom_omp_tool" {
					fallbackTarget = target
					break
				}
			}
			if fallbackTarget == "" {
				t.Errorf("OMP custom tool missing active reverse: %v", route.activeReverse)
				return
			}

			// Non-stream response uncloak
			respBody := []byte(fmt.Sprintf(`{
				"choices": [{
					"message": {
						"tool_calls": [
							{"function": {"name": "run_command", "arguments": "{}"}},
							{"function": {"name": "view_file", "arguments": "{}"}},
							{"function": {"name": "wp_vibe_send", "arguments": "{}"}},
							{"function": {"name": "%s", "arguments": "{}"}}
						]
					}
				}]
			}`, fallbackTarget))
			respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "openai", "agy/omp-model", respBody)
			rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
			if codeResp != 0 {
				t.Errorf("OMP resp intercept failed: %d", codeResp)
				return
			}
			decodedResp := string(decodeEnvelopeBody(t, rawResp))
			if !strings.Contains(decodedResp, `"bash"`) ||
				!strings.Contains(decodedResp, `"read"`) ||
				!strings.Contains(decodedResp, `"vibe_send"`) ||
				!strings.Contains(decodedResp, `"mcp__custom_omp_tool"`) {
				t.Errorf("OMP response restoration failed: %s", decodedResp)
			}
			// Must NOT contain Claude Code or Codex sources
			if strings.Contains(decodedResp, `"Bash"`) || strings.Contains(decodedResp, `"exec"`) {
				t.Errorf("OMP response contaminated with foreign identities: %s", decodedResp)
			}

			// Streaming chunk uncloak
			chunkBody := []byte(fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n"))
			streamPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/omp-model", 0, chunkBody, cloakedReq)
			rawChunk, codeChunk := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, streamPayload)
			if codeChunk != 0 {
				t.Errorf("OMP stream chunk failed: %d", codeChunk)
				return
			}
			decodedChunk := string(decodeEnvelopeBody(t, rawChunk))
			if !strings.Contains(decodedChunk, `"bash"`) {
				t.Errorf("OMP stream chunk restoration failed: %s", decodedChunk)
			}
			if strings.Contains(decodedChunk, `"Bash"`) || strings.Contains(decodedChunk, `"exec"`) {
				t.Errorf("OMP stream chunk contaminated: %s", decodedChunk)
			}

			// Complete lifecycle
			var completed pluginapi.RequestCompletion
			ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &completed)
		}(i)
	}

	wg.Wait()
}

// TestIssue39_NegativeCrossAuthority proves that pinned authority for a request
// restores only exact pairs declared by that request, and never restores unearned
// foreign shared aliases or dynamic identities that the client never declared.
func TestIssue39_NegativeCrossAuthority(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	const reqID = "neg-cross-auth-01"
	// Claude Code request declares ONLY Bash (maps to run_command).
	// It does NOT declare SendMessage, ListAgents, or any custom tool.
	reqBody := []byte(`{
		"model": "agy/claude-model",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "Bash", "description": "run shell"}]
	}`)
	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-model", reqBody, headers)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)

	// Upstream returns a response containing:
	// - run_command (declared)
	// - wp_send_message (shared alias, but NOT declared by this CC request)
	// - wp_list_workers (shared alias, but NOT declared by this CC request)
	// - wp_ext_0123456789abcdef (foreign dynamic fallback)
	// - view_file (native AGY target for undeclared Read)
	respBody := []byte(`{
		"content": [
			{"type": "tool_use", "id": "t1", "name": "run_command", "input": {}},
			{"type": "tool_use", "id": "t2", "name": "wp_send_message", "input": {}},
			{"type": "tool_use", "id": "t3", "name": "wp_list_workers", "input": {}},
			{"type": "tool_use", "id": "t4", "name": "wp_ext_0123456789abcdef", "input": {}},
			{"type": "tool_use", "id": "t5", "name": "view_file", "input": {}}
		]
	}`)
	respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-model", respBody)
	rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
	if codeResp != 0 {
		t.Fatalf("response intercept failed: %d", codeResp)
	}
	decodedResp := string(decodeEnvelopeBody(t, rawResp))

	// Declared tool run_command must uncloak to Bash
	if !strings.Contains(decodedResp, `"Bash"`) {
		t.Fatalf("declared run_command was not uncloaked to Bash: %s", decodedResp)
	}
	// Undeclared shared aliases and foreign targets MUST NOT be uncloaked!
	if strings.Contains(decodedResp, `"SendMessage"`) || strings.Contains(decodedResp, `"collaboration__send_message"`) {
		t.Fatalf("undeclared wp_send_message was improperly uncloaked: %s", decodedResp)
	}
	if strings.Contains(decodedResp, `"ListAgents"`) || strings.Contains(decodedResp, `"collaboration__list_agents"`) {
		t.Fatalf("undeclared wp_list_workers was improperly uncloaked: %s", decodedResp)
	}
	if strings.Contains(decodedResp, `"Read"`) {
		t.Fatalf("undeclared view_file was improperly uncloaked to Read: %s", decodedResp)
	}
	// Foreign / undeclared targets must remain byte-for-byte intact
	if !strings.Contains(decodedResp, `"wp_send_message"`) ||
		!strings.Contains(decodedResp, `"wp_list_workers"`) ||
		!strings.Contains(decodedResp, `"wp_ext_0123456789abcdef"`) ||
		!strings.Contains(decodedResp, `"view_file"`) {
		t.Fatalf("undeclared targets were altered or dropped: %s", decodedResp)
	}

	var completed pluginapi.RequestCompletion
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &completed)
}

// TestIssue39_ConfigReloadInFlightPinning proves that a dynamic configuration reload
// (e.g. changing tool_mappings or model_prefixes) cannot alter or invalidate the
// mappings or reverse authority of an in-flight request.
func TestIssue39_ConfigReloadInFlightPinning(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	const reqID = "in-flight-pin-01"
	reqBody := []byte(`{
		"model": "agy/codex-model",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{"type": "function", "function": {"name": "exec"}},
			{"type": "function", "function": {"name": "collaboration__send_message"}}
		]
	}`)
	headers := http.Header{"X-Cloak-Client": []string{"codex"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/codex-model", reqBody, headers)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)

	// Now reconfigure plugin with completely different model_prefixes and tool_mappings
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
model_prefixes: [different/]
tool_mappings:
  codex:
    exec: different_target
`)))

	// In-flight response arrives: it must still restore using its pinned plan
	respBody := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [
					{"function": {"name": "run_command", "arguments": "{}"}},
					{"function": {"name": "wp_send_message", "arguments": "{}"}}
				]
			}
		}]
	}`)
	respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "openai", "agy/codex-model", respBody)
	rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
	if codeResp != 0 {
		t.Fatalf("response intercept failed: %d", codeResp)
	}
	decodedResp := string(decodeEnvelopeBody(t, rawResp))
	if !strings.Contains(decodedResp, `"exec"`) || !strings.Contains(decodedResp, `"collaboration__send_message"`) {
		t.Fatalf("in-flight response failed to restore via pinned plan: %s", decodedResp)
	}

	var completed pluginapi.RequestCompletion
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &completed)
}

// TestIssue39_StreamCleanupDoesNotDestroyAuthorityBeforeComplete proves that
// when a streaming session ends cleanly (via [DONE]), the disposable stream session
// is cleared, but the request authority in the plan/route managers persists until
// request.complete is explicitly delivered.
func TestIssue39_StreamCleanupDoesNotDestroyAuthorityBeforeComplete(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	const reqID = "stream-term-persist-01"
	reqBody := []byte(`{
		"model": "agy/claude-model",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "Bash", "description": "run shell"}]
	}`)
	headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-model", reqBody, headers)
	handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)

	if plan := globalAliasPlanManager.get(reqID); plan == nil {
		t.Fatal("alias plan not pinned")
	}

	// Deliver terminal SSE chunk with [DONE]
	doneChunk := []byte("data: [DONE]\n\n")
	streamPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/claude-model", 0, doneChunk, nil)
	handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, streamPayload)

	// Stream session is cleaned up
	if sess := globalStreamManager.getSession("req:" + reqID); sess != nil {
		t.Fatalf("disposable stream session was not cleaned on terminal frame: %v", sess)
	}

	// BUT the alias plan authority MUST PERSIST!
	if plan := globalAliasPlanManager.get(reqID); plan == nil {
		t.Fatal("terminal stream cleanup prematurely destroyed request alias authority before request.complete")
	}

	// Now deliver request.complete
	var completed pluginapi.RequestCompletion
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &completed)

	// Now the authority is freed
	if plan := globalAliasPlanManager.get(reqID); plan != nil {
		t.Fatal("request.complete failed to free alias plan authority")
	}
}

// TestIssue39_LifecycleCleanupIdempotenceAndLeaks verifies that idempotent completions,
// cancellations, failures, and 503 rejections clean up completely without memory leaks.
func TestIssue39_LifecycleCleanupIdempotenceAndLeaks(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	// Count initial baseline
	initialPlans := len(globalAliasPlanManager.plans)
	initialRoutes := len(globalLifecycleManager.routes)
	initialSessions := len(globalStreamManager.sessions)

	for i := range 10 {
		reqID := fmt.Sprintf("lifecycle-leak-%d", i)
		reqBody := []byte(`{
			"model": "agy/codex-model",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [{"type": "function", "function": {"name": "exec"}}]
		}`)
		headers := http.Header{"X-Cloak-Client": []string{"codex"}}
		reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/codex-model", reqBody, headers)
		handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)

		// 1. Double completion (idempotence)
		var comp1, comp2 pluginapi.RequestCompletion
		ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &comp1)
		ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &comp2)
	}

	// 2. Cancellation and failure completions
	for _, outcome := range []string{"canceled", "failed", "rejected"} {
		reqID := fmt.Sprintf("lifecycle-outcome-%s", outcome)
		reqBody := []byte(`{
			"model": "agy/codex-model",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [{"type": "function", "function": {"name": "exec"}}]
		}`)
		headers := http.Header{"X-Cloak-Client": []string{"codex"}}
		reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/codex-model", reqBody, headers)
		handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)

		var comp struct{}
		ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: pluginapi.RequestCompletionOutcome(outcome)}, &comp)
	}

	// 3. 503 rejection (e.g. declaration collision)
	collidingReqID := "collision-rej-01"
	collidingBody := []byte(`{
		"model": "agy/codex-model",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{"type": "function", "function": {"name": "functions:exec"}},
			{"type": "function", "function": {"name": "default_api:exec"}}
		]
	}`)
	headers := http.Header{"X-Cloak-Client": []string{"codex"}}
	reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, collidingReqID, "openai", "agy/codex-model", collidingBody, headers)
	raw, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
	if code != 0 || !strings.Contains(string(raw), `"StatusCode":503`) {
		t.Fatalf("expected 503 tool_cloak_required, got: %s", raw)
	}
	// Rejection should leave no pinned plan or route
	if plan := globalAliasPlanManager.get(collidingReqID); plan != nil {
		t.Fatalf("rejected request pinned an alias plan: %v", plan)
	}

	// Complete on the rejected ID is a clean no-op
	var compRej struct{}
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: collidingReqID, Outcome: pluginapi.RequestCompletionOutcome("rejected")}, &compRej)

	// Verify all managers returned to baseline counts
	if len(globalAliasPlanManager.plans) != initialPlans {
		t.Fatalf("alias plan manager leaked: before=%d, after=%d", initialPlans, len(globalAliasPlanManager.plans))
	}
	if len(globalLifecycleManager.routes) != initialRoutes {
		t.Fatalf("lifecycle manager leaked: before=%d, after=%d", initialRoutes, len(globalLifecycleManager.routes))
	}
	if len(globalStreamManager.sessions) != initialSessions {
		t.Fatalf("stream manager leaked: before=%d, after=%d", initialSessions, len(globalStreamManager.sessions))
	}
}

// TestIssue39_NoDynamicIdentityGuessingInStaticFallbacks asserts that legacy static
// reverse fallbacks (buildUncloakTable, scopeUncloakTableToDeclaredNames) never uncloak
// or invent dynamic identities (wp_*, wp_ext_*) when given executed or cloaked bodies.
func TestIssue39_NoDynamicIdentityGuessingInStaticFallbacks(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// A cloaked body carrying shared aliases and fallback aliases
	cloakedBody := []byte(`{
		"tools": [
			{"type": "function", "function": {"name": "run_command"}},
			{"type": "function", "function": {"name": "wp_send_message"}},
			{"type": "function", "function": {"name": "wp_list_workers"}},
			{"type": "function", "function": {"name": "wp_ext_1234567890abcdef"}}
		]
	}`)

	table, client := buildUncloakTable(cloakedBody, "openai")
	// If it detected codex or claude_code based on run_command:
	if table != nil {
		// table MUST NOT contain any wp_ or wp_ext_ mappings
		for target, src := range table {
			if strings.HasPrefix(target, "wp_") || strings.HasPrefix(target, "wp_ext_") {
				t.Fatalf("static uncloak table guessed dynamic identity: %q -> %q", target, src)
			}
		}
	}
	_ = client
}
// TestIssue39_AliasPlanMissingAuthority_PassesThroughAGYAndSharedTargets proves that
// when an alias-plan client (Claude Code, Codex) has no pinned alias-plan authority
// (for example after request.complete cleanup, or missing plan), neither AGY-role targets
// (run_command) nor wp_* shared aliases are reversed on either response or stream paths.
// Both paths must pass through unmutated rather than guess or fall back to static tables.
func TestIssue39_AliasPlanMissingAuthority_PassesThroughAGYAndSharedTargets(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte("model_prefixes: [agy/]")))

	// 1. Claude Code: Post-request.complete cleanup
	{
		const reqID = "cc-post-complete-01"
		reqBody := []byte(`{
			"model": "agy/claude-model",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{"name": "Bash", "description": "run shell"},
				{"name": "SendMessage", "description": "send msg"}
			]
		}`)
		headers := http.Header{"X-Cloak-Client": []string{"claude_code"}}
		reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "anthropic", "agy/claude-model", reqBody, headers)
		rawEnv, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
		if code != 0 {
			t.Fatalf("CC req intercept failed: %d", code)
		}
		cloakedReq := decodeEnvelopeBody(t, rawEnv)

		// Lifecycle completes: deletes plan and session
		var comp struct{}
		ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &comp)

		if plan := globalAliasPlanManager.get(reqID); plan != nil {
			t.Fatalf("alias plan should be deleted after complete: %v", plan)
		}

		// Late response arrives with RequestID carrying run_command and wp_send_message
		lateResp := []byte(`{
			"content": [
				{"type": "tool_use", "id": "t1", "name": "run_command", "input": {}},
				{"type": "tool_use", "id": "t2", "name": "wp_send_message", "input": {}}
			]
		}`)
		respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "anthropic", "agy/claude-model", lateResp)
		rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
		if codeResp != 0 {
			t.Fatalf("late response intercept failed: %d", codeResp)
		}
		decodedResp := string(decodeEnvelopeBody(t, rawResp))
		// MUST NOT reverse either AGY-role target (run_command) or wp_* target (wp_send_message)
		if strings.Contains(decodedResp, `"Bash"`) || strings.Contains(decodedResp, `"SendMessage"`) {
			t.Fatalf("late response with missing authority improperly reversed targets: %s", decodedResp)
		}

		// Late stream chunk arrives with RequestID carrying run_command
		lateChunk := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"run_command\"}}\n\n")
		streamPayload := makeIntegrationStreamChunkPayload(t, reqID, "anthropic", "agy/claude-model", 0, lateChunk, cloakedReq)
		rawChunk, codeChunk := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, streamPayload)
		if codeChunk != 0 {
			t.Fatalf("late stream chunk failed: %d", codeChunk)
		}
		decodedChunk := string(decodeEnvelopeBody(t, rawChunk))
		if strings.Contains(decodedChunk, `"Bash"`) {
			t.Fatalf("late stream chunk with missing authority improperly reversed run_command to Bash: %s", decodedChunk)
		}
	}

	// 2. Codex: Post-request.complete cleanup
	{
		const reqID = "codex-post-complete-02"
		reqBody := []byte(`{
			"model": "agy/codex-model",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{"type": "function", "function": {"name": "exec"}},
				{"type": "function", "function": {"name": "collaboration__send_message"}},
				{"type": "function", "function": {"name": "collaboration__list_agents"}}
			]
		}`)
		headers := http.Header{"X-Cloak-Client": []string{"codex"}}
		reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", "agy/codex-model", reqBody, headers)
		rawEnv, code := handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)
		if code != 0 {
			t.Fatalf("Codex req intercept failed: %d", code)
		}
		cloakedReq := decodeEnvelopeBody(t, rawEnv)

		// Lifecycle completes
		var comp struct{}
		ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &comp)

		// Late response arrives with run_command, wp_send_message, wp_list_workers
		lateResp := []byte(`{
			"choices": [{
				"message": {
					"tool_calls": [
						{"function": {"name": "run_command", "arguments": "{}"}},
						{"function": {"name": "wp_send_message", "arguments": "{}"}},
						{"function": {"name": "wp_list_workers", "arguments": "{}"}}
					]
				}
			}]
		}`)
		respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "openai", "agy/codex-model", lateResp)
		rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
		if codeResp != 0 {
			t.Fatalf("late response intercept failed: %d", codeResp)
		}
		decodedResp := string(decodeEnvelopeBody(t, rawResp))
		if strings.Contains(decodedResp, `"exec"`) ||
			strings.Contains(decodedResp, `"collaboration__send_message"`) ||
			strings.Contains(decodedResp, `"collaboration__list_agents"`) {
			t.Fatalf("late Codex response with missing authority improperly reversed targets: %s", decodedResp)
		}

		// Late stream chunk arrives with run_command
		lateChunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\"}}]}}]}\n\n")
		streamPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", "agy/codex-model", 0, lateChunk, cloakedReq)
		rawChunk, codeChunk := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, streamPayload)
		if codeChunk != 0 {
			t.Fatalf("late stream chunk failed: %d", codeChunk)
		}
		decodedChunk := string(decodeEnvelopeBody(t, rawChunk))
		if strings.Contains(decodedChunk, `"exec"`) {
			t.Fatalf("late Codex stream chunk with missing authority improperly reversed run_command to exec: %s", decodedChunk)
		}
	}

	// 3. Correlated request with missing plan (never admitted) carrying executed body
	{
		const unadmittedID = "unadmitted-cc-03"
		executedBody := []byte(`{"tools":[{"type":"function","function":{"name":"run_command"}},{"type":"function","function":{"name":"wp_send_message"}}]}`)
		respBody := []byte(`{"content":[{"type":"tool_use","id":"t1","name":"run_command","input":{}},{"type":"tool_use","id":"t2","name":"wp_send_message","input":{}}]}`)

		var req pluginapi.ResponseInterceptRequest
		req.RequestID = unadmittedID
		req.Model = "agy/claude-model"
		req.SourceFormat = "anthropic"
		req.RequestBody = executedBody
		req.Body = respBody

		payload, _ := json.Marshal(req)
		rawResp, codeResp := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
		if codeResp != 0 {
			t.Fatalf("response intercept failed: %d", codeResp)
		}
		decodedResp := string(decodeEnvelopeBody(t, rawResp))
		if strings.Contains(decodedResp, `"Bash"`) || strings.Contains(decodedResp, `"SendMessage"`) {
			t.Fatalf("unadmitted CC request reversed targets without authority: %s", decodedResp)
		}
	}

	// 4. Explicit marker without RequestID
	{
		respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}},{"function":{"name":"wp_send_message","arguments":"{}"}}]}}]}`)
		headers := http.Header{"X-Cloak-Client": []string{"codex"}}
		rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, makeIntegrationResponseInterceptPayloadWithHeaders(t, "", "openai", "agy/codex", respBody, headers))
		decodedResp := string(decodeEnvelopeBody(t, rawResp))
		if strings.Contains(decodedResp, `"exec"`) || strings.Contains(decodedResp, `"collaboration__send_message"`) {
			t.Fatalf("explicit marker without RequestID reversed targets without authority: %s", decodedResp)
		}
	}
}

// TestIssue39_LegitimateLegacyFallbackStillWorksWhereIntended proves that legitimate
// legacy fallback behavior continues to function for non-alias-plan clients (Oh My Pi)
// across UA evidence and body detection, while protected routes retain exact authority.
func TestIssue39_LegitimateLegacyFallbackStillWorksWhereIntended(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	model := "agy/gemini-3.7-flash"

	// 1. Oh My Pi with surviving UA evidence recovers view_file -> read on response
	{
		reqHeaders := http.Header{"User-Agent": []string{"omp/1.2.3"}}
		respBody := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`)
		rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter,
			makeIntegrationResponseInterceptPayloadWithHeaders(t, "legit-legacy-ua-resp", "openai", model, respBody, reqHeaders))
		out := string(decodeEnvelopeBody(t, rawResp))
		if !strings.Contains(out, `"name":"read"`) {
			t.Fatalf("legitimate OMP UA response fallback failed: %s", out)
		}
	}

	// 2. Oh My Pi with surviving UA evidence recovers run_command -> bash on stream
	{
		streamHeaders := http.Header{"User-Agent": []string{"omp/9.0"}}
		chunkBody := []byte(`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"run_command","arguments":"{}"}}]}}]}`)
		rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk,
			makeIntegrationStreamChunkPayloadWithHeaders(t, "legit-legacy-ua-stream", "openai", model, 0, chunkBody, streamHeaders))
		chunkOut := string(decodeEnvelopeBody(t, rawChunk))
		if !strings.Contains(chunkOut, `"name":"bash"`) {
			t.Fatalf("legitimate OMP UA stream fallback failed: %s", chunkOut)
		}
	}

	// 3. Oh My Pi body detection from uncloaked tools (read, write, bash) uncloaks view_file -> read
	{
		origBody := `{
			"tools": [
				{"type": "function", "function": {"name": "read"}},
				{"type": "function", "function": {"name": "write"}},
				{"type": "function", "function": {"name": "bash"}},
				{"type": "function", "function": {"name": "hub"}}
			]
		}`
		respBody := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"view_file","arguments":"{}"}}]}}]}`
		payload := responseInterceptRequestJSON(t, origBody, respBody, "openai")
		rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, payload)
		out := string(decodeEnvelopeBody(t, rawResp))
		if !strings.Contains(out, `"name":"read"`) {
			t.Fatalf("legitimate OMP body detection fallback failed: %s", out)
		}
	}

	// 4. Protected OMP with pinned route still uncloaks canonical and extended pairs
	{
		const reqID = "legit-protected-omp-01"
		reqBody := []byte(`{
			"model": "agy/gemini-3.7-flash",
			"messages": [{"role": "user", "content": "hello"}],
			"tools": [
				{"type": "function", "function": {"name": "read"}},
				{"type": "function", "function": {"name": "bash"}},
				{"type": "function", "function": {"name": "vibe_send"}}
			]
		}`)
		headers := http.Header{"X-Cloak-Client": []string{"oh_my_pi"}}
		reqPayload := makeIntegrationRequestInterceptPayloadWithHeaders(t, reqID, "openai", model, reqBody, headers)
		handlePluginCall(pluginabi.MethodRequestInterceptBefore, reqPayload)

		respBody := []byte(`{
			"choices": [{
				"message": {
					"tool_calls": [
						{"function": {"name": "view_file", "arguments": "{}"}},
						{"function": {"name": "run_command", "arguments": "{}"}},
						{"function": {"name": "wp_vibe_send", "arguments": "{}"}}
					]
				}
			}]
		}`)
		respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, respBody)
		rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
		out := string(decodeEnvelopeBody(t, rawResp))
		if !strings.Contains(out, `"read"`) || !strings.Contains(out, `"bash"`) || !strings.Contains(out, `"vibe_send"`) {
			t.Fatalf("Protected OMP with pinned route failed to uncloak: %s", out)
		}

		var comp struct{}
		ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: reqID, Outcome: "succeeded"}, &comp)
	}
}
