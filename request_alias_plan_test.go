package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func isolateRequestAliasPlan(t *testing.T) {
	t.Helper()
	previousConfig := globalFilterConfig.Load()
	previousPlans, previousStreams := globalAliasPlanManager, globalStreamManager
	applyFilterConfig(defaultFilterConfig())
	globalAliasPlanManager = newRequestAliasPlanManager()
	globalStreamManager = newStreamSessionManager()
	t.Cleanup(func() {
		globalFilterConfig.Store(previousConfig)
		globalAliasPlanManager, globalStreamManager = previousPlans, previousStreams
	})
}

func requestAliasPairForTest(plan *requestAliasPlan, source string) *requestAliasPair {
	for i := range plan.pairs {
		if plan.pairs[i].SourceIdentity == source {
			return &plan.pairs[i]
		}
	}
	return nil
}

func TestRequestAliasPlanDeterministicFallbackAndPairs(t *testing.T) {
	sourcesA := []string{"mcp__server__tool", "Functions:CustomTool", "view_file", "functions:run_command"}
	sourcesB := []string{"functions:run_command", "view_file", "Functions:CustomTool", "mcp__server__tool"}
	preferred := map[string]string{
		"view_file":             "view_file",
		"functions:run_command": "functions:run_command",
	}

	planA, err := buildRequestAliasPlan("claude_code", sourcesA, preferred)
	if err != nil {
		t.Fatal(err)
	}
	planB, err := buildRequestAliasPlan("claude_code", sourcesB, preferred)
	if err != nil {
		t.Fatal(err)
	}
	if len(planA.pairs) != 4 || len(planB.pairs) != 4 {
		t.Fatalf("unexpected pair count: %d / %d", len(planA.pairs), len(planB.pairs))
	}

	for _, source := range []string{"mcp__server__tool", "Functions:CustomTool"} {
		a := planA.forward[source]
		b := planB.forward[source]
		if a == "" || a != b || !strings.HasPrefix(a, "wp_ext_") {
			t.Fatalf("fallback alias is not stable: source=%q a=%q b=%q", source, a, b)
		}
		lowerAlias := strings.ToLower(a)
		for _, leaked := range []string{"mcp", "server", "tool", "functions", "custom"} {
			if strings.Contains(lowerAlias, leaked) {
				t.Fatalf("fallback alias leaked source vocabulary %q: %s", leaked, a)
			}
		}
	}

	pair := requestAliasPairForTest(planA, "view_file")
	if pair == nil || pair.SourceIdentity != "view_file" || pair.UpstreamIdentity != "view_file" || pair.Changed {
		t.Fatalf("unchanged native identity not recorded exactly: %#v", pair)
	}
	namespacedNative := requestAliasPairForTest(planA, "functions:run_command")
	if namespacedNative == nil || namespacedNative.UpstreamIdentity != "functions:run_command" || namespacedNative.Changed {
		t.Fatalf("namespaced native identity not preserved: %#v", namespacedNative)
	}
	qualified := requestAliasPairForTest(planA, "Functions:CustomTool")
	if qualified == nil || qualified.SourceIdentity != "Functions:CustomTool" || !qualified.Changed {
		t.Fatalf("qualified source identity not preserved exactly: %#v", qualified)
	}
}

func TestRequestAliasPlanRejectsNonInjectiveTargets(t *testing.T) {
	cases := []struct {
		name      string
		sources   []string
		preferred map[string]string
	}{
		{"static collision", []string{"Read", "Write"}, map[string]string{"Read": "view_file", "Write": "view_file"}},
		{"native target collision", []string{"Read", "view_file"}, map[string]string{"Read": "view_file", "view_file": "view_file"}},
		{"static generated collision", []string{"Read", "DynamicTool"}, map[string]string{"Read": fallbackAliasForSource("DynamicTool")}},
		{"invalid empty override", []string{"Read"}, map[string]string{"Read": ""}},
		{"namespace variants collision", []string{"functions:foo", "default_api:foo"}, nil},
		{"namespace mapped variants collision", []string{"functions:Read", "default_api:Read"}, map[string]string{"Read": "view_file"}},
		{"namespace and bare mapped collision", []string{"Read", "functions:Read"}, map[string]string{"Read": "view_file"}},
		{"namespace and native target collision", []string{"functions:Read", "default_api:view_file"}, map[string]string{"Read": "view_file"}},
		{"namespace and bare unknown collision", []string{"foo", "functions:foo"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if plan, err := buildRequestAliasPlan("claude_code", tc.sources, tc.preferred); err == nil {
				t.Fatalf("expected invalid plan, got %#v", plan)
			}
		})
	}
}

func TestRequestAliasPlanAdmissionFailsClosed(t *testing.T) {
	isolateRequestAliasPlan(t)
	validBody := []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"Read\"}}]}")
	cases := []struct {
		req       pluginapi.RequestInterceptRequest
		preferred map[string]string
	}{
		{req: pluginapi.RequestInterceptRequest{SourceFormat: "openai", Body: validBody}},
		{req: pluginapi.RequestInterceptRequest{RequestID: "unsupported-format", SourceFormat: "responses", Body: validBody}},
		{req: pluginapi.RequestInterceptRequest{RequestID: "unsupported-shape", SourceFormat: "openai", Body: []byte("{\"tools\":[{\"type\":\"custom\",\"name\":\"exec\"}]}")}},
		{req: pluginapi.RequestInterceptRequest{RequestID: "trailing-json", SourceFormat: "openai", Body: append(validBody, []byte(" {}")...)}},
		{
			req:       pluginapi.RequestInterceptRequest{RequestID: "collision", SourceFormat: "openai", Body: []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"Read\"}},{\"type\":\"function\",\"function\":{\"name\":\"Write\"}}]}")},
			preferred: map[string]string{"Read": "view_file", "Write": "view_file"},
		},
	}
	for _, tc := range cases {
		plan, rejection := admitRequestAliasPlanOrReject(&tc.req, pluginapi.RequestInterceptResponse{}, "claude_code", tc.preferred)
		if plan != nil || rejection == nil {
			t.Fatalf("invalid request must reject: request=%+v plan=%#v rejection=%s", tc.req, plan, rejection)
		}
		var env struct {
			OK     bool                               `json:"ok"`
			Result pluginapi.RequestInterceptResponse `json:"result"`
		}
		if err := json.Unmarshal(rejection, &env); err != nil {
			t.Fatal(err)
		}
		if !env.OK || !env.Result.Terminate || env.Result.StatusCode != 503 {
			t.Fatalf("wrong fail-closed envelope: %s", rejection)
		}
		const exact = "{\"error\":{\"code\":\"tool_cloak_required\",\"message\":\"Request could not be safely cloaked.\"}}"
		if string(env.Result.ResponseBody) != exact {
			t.Fatalf("wrong fail-closed response body: %s", env.Result.ResponseBody)
		}
	}
}

func TestRequestAliasPlanPinnedAcrossReloadAndDisposableCleanup(t *testing.T) {
	isolateRequestAliasPlan(t)
	const requestID = "alias-plan-pinned"
	req := pluginapi.RequestInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/model", RequestedModel: "agy/model",
		Body: []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"Read\"}}]}"),
	}
	plan, rejection := admitRequestAliasPlanOrReject(&req, pluginapi.RequestInterceptResponse{}, "claude_code", map[string]string{"Read": "target_a"})
	if rejection != nil || plan == nil {
		t.Fatalf("valid plan rejected: %s", rejection)
	}
	if got := globalAliasPlanManager.get(requestID); got != plan {
		t.Fatal("request plan was not pinned")
	}

	globalStreamManager.deleteSession("req:" + requestID)
	if got := globalAliasPlanManager.get(requestID); got != plan {
		t.Fatal("deleting stream session destroyed request alias authority")
	}

	cfg := defaultFilterConfig()
	cfg.ModelPrefixes = []string{"different/"}
	cfg.ToolMappings["claude_code"]["Read"] = "target_b"
	applyFilterConfig(cfg)

	var response pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID: requestID, SourceFormat: "anthropic", Model: "agy/model", RequestedModel: "agy/model",
		Body: []byte("{\"choices\":[{\"message\":{\"tool_calls\":[{\"function\":{\"name\":\"target_a\",\"arguments\":\"{}\"}}]}}]}"),
	}, &response)
	if !bytes.Contains(response.Body, []byte("\"name\":\"Read\"")) || bytes.Contains(response.Body, []byte("target_a")) {
		t.Fatalf("response did not use pinned exact reverse authority: %s", response.Body)
	}

	// The pinned plan is authoritative even when it has no reverse match.
	// Falling through to the legacy Claude table here would incorrectly turn
	// view_file into Read even though this request mapped Read -> target_a.
	var unrelated pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/model", RequestedModel: "agy/model",
		Body: []byte("{\"choices\":[{\"message\":{\"tool_calls\":[{\"function\":{\"name\":\"view_file\",\"arguments\":\"{}\"}}]}}]}"),
	}, &unrelated)
	if len(unrelated.Body) != 0 {
		t.Fatalf("active alias plan must block legacy reverse fallback: %s", unrelated.Body)
	}

	var completed struct{}
	ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: requestID}, &completed)
	if got := globalAliasPlanManager.get(requestID); got != nil {
		t.Fatal("request.complete did not release alias authority")
	}
}

func TestRequestAliasPlanFragmentedStreamUsesPinnedAuthority(t *testing.T) {
	isolateRequestAliasPlan(t)
	const requestID = "alias-plan-stream"
	req := pluginapi.RequestInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/model", RequestedModel: "agy/model",
		Body: []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"DynamicTool\"}}]}"),
	}
	plan, rejection := admitRequestAliasPlanOrReject(&req, pluginapi.RequestInterceptResponse{}, "claude_code", nil)
	if rejection != nil || plan == nil {
		t.Fatalf("valid plan rejected: %s", rejection)
	}
	target := plan.forward["DynamicTool"]
	if !strings.HasPrefix(target, "wp_ext_") {
		t.Fatalf("expected generated fallback target, got %q", target)
	}

	globalStreamManager.deleteSession("req:" + requestID)
	cfg := defaultFilterConfig()
	cfg.ModelPrefixes = []string{"different/"}
	applyFilterConfig(cfg)
	var headerInit pluginapi.StreamChunkInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/model",
		ChunkIndex: pluginapi.StreamChunkHeaderInitIndex,
	}, &headerInit)

	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"" + target + "\",\"arguments\":\"{}\"}}]}}]}\n\n")
	cut := bytes.Index(frame, []byte(target)) + len(target)/2
	parts := [][]byte{frame[:cut], frame[cut:]}
	var wire []byte
	for i, part := range parts {
		var result pluginapi.StreamChunkInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
			RequestID: requestID, SourceFormat: "openai", Model: "agy/model", ChunkIndex: i, Body: part,
		}, &result)
		wire = appendOMPMeasurementWire(wire, part, result)
	}
	if !bytes.Contains(wire, []byte("\"name\":\"DynamicTool\"")) {
		t.Fatalf("fragmented stream did not restore exact dynamic source: %s", wire)
	}
	if bytes.Contains(wire, []byte(target)) {
		t.Fatalf("fragmented stream leaked generated alias: %s", wire)
	}
	if globalAliasPlanManager.get(requestID) == nil {
		t.Fatal("terminal/disposable stream cleanup destroyed request authority before request.complete")
	}
}

func TestRequestAliasPlanStreamLeavesUnrelatedNameFieldsUntouched(t *testing.T) {
	isolateRequestAliasPlan(t)
	const requestID = "alias-plan-stream-unrelated-name"
	req := pluginapi.RequestInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/model", RequestedModel: "agy/model",
		Body: []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"DynamicTool\"}}]}"),
	}
	plan, rejection := admitRequestAliasPlanOrReject(&req, pluginapi.RequestInterceptResponse{}, "claude_code", map[string]string{"DynamicTool": "wp_shared_tool"})
	if rejection != nil || plan == nil {
		t.Fatalf("valid plan rejected: %s", rejection)
	}

	frame := []byte("data: {\"metadata\":{\"name\":\"wp_shared_tool\"},\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"wp_shared_tool\",\"arguments\":\"{}\"}}]}}]}\n\n")
	var result pluginapi.StreamChunkInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/model", ChunkIndex: 0, Body: frame,
	}, &result)
	wire := appendOMPMeasurementWire(nil, frame, result)

	if !bytes.Contains(wire, []byte(`"metadata":{"name":"wp_shared_tool"}`)) {
		t.Fatalf("unrelated name field was rewritten: %s", wire)
	}
	if !bytes.Contains(wire, []byte(`"name":"DynamicTool"`)) {
		t.Fatalf("tool identity was not restored: %s", wire)
	}
}

func TestRequestAliasPlanAnthropicExactReversal(t *testing.T) {
	isolateRequestAliasPlan(t)
	const requestID = "alias-plan-anthropic"
	req := pluginapi.RequestInterceptRequest{
		RequestID: requestID, SourceFormat: "anthropic",
		Body: []byte("{\"tools\":[{\"name\":\"DynamicTool\",\"input_schema\":{\"type\":\"object\"}}]}"),
	}
	plan, rejection := admitRequestAliasPlanOrReject(&req, pluginapi.RequestInterceptResponse{}, "claude_code", map[string]string{"DynamicTool": "wp_shared_tool"})
	if rejection != nil || plan == nil {
		t.Fatalf("valid anthropic plan rejected: %s", rejection)
	}
	var response pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID: requestID, SourceFormat: "anthropic",
		Body: []byte("{\"content\":[{\"type\":\"tool_use\",\"name\":\"wp_shared_tool\",\"input\":{}}]}"),
	}, &response)
	if !bytes.Contains(response.Body, []byte("\"name\":\"DynamicTool\"")) {
		t.Fatalf("anthropic response did not restore exact source identity: %s", response.Body)
	}
}

func TestRequestAliasPlansDoNotCrossRequestBoundaries(t *testing.T) {
	isolateRequestAliasPlan(t)
	requests := []struct {
		id     string
		source string
	}{
		{"alias-plan-a", "AlphaTool"},
		{"alias-plan-b", "BetaTool"},
	}
	for _, tc := range requests {
		req := pluginapi.RequestInterceptRequest{
			RequestID: tc.id, SourceFormat: "openai",
			Body: []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"" + tc.source + "\"}}]}"),
		}
		if plan, rejection := admitRequestAliasPlanOrReject(&req, pluginapi.RequestInterceptResponse{}, "claude_code", map[string]string{tc.source: "wp_shared_role"}); plan == nil || rejection != nil {
			t.Fatalf("admission failed for %s: %s", tc.id, rejection)
		}
	}

	for _, tc := range requests {
		var response pluginapi.ResponseInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
			RequestID: tc.id, SourceFormat: "openai",
			Body: []byte("{\"choices\":[{\"message\":{\"tool_calls\":[{\"function\":{\"name\":\"wp_shared_role\",\"arguments\":\"{}\"}}]}}]}"),
		}, &response)
		if !bytes.Contains(response.Body, []byte("\"name\":\""+tc.source+"\"")) {
			t.Fatalf("request %s restored wrong authority: %s", tc.id, response.Body)
		}
	}
}

func TestRequestAliasPlanConcurrentIsolation(t *testing.T) {
	isolateRequestAliasPlan(t)

	const workerCount = 16
	type testWorkload struct {
		requestID    string
		client       string
		sourceFormat string
		sourceName   string
		cloakedName  string
		forbidden    string
	}

	workloads := make([]testWorkload, workerCount)
	for i := 0; i < workerCount; i++ {
		client := "claude_code"
		sourceFormat := "anthropic"
		sourceName := "Bash"
		cloakedName := "run_command"
		forbidden := "exec"
		if i%2 == 1 {
			client = "codex"
			sourceFormat = "openai"
			sourceName = "exec"
			cloakedName = "run_command"
			forbidden = "Bash"
		}
		workloads[i] = testWorkload{
			requestID:    fmt.Sprintf("concurrent-req-%02d", i),
			client:       client,
			sourceFormat: sourceFormat,
			sourceName:   sourceName,
			cloakedName:  cloakedName,
			forbidden:    forbidden,
		}
	}

	var startWg sync.WaitGroup
	var doneWg sync.WaitGroup
	startWg.Add(1)

	errors := make(chan error, workerCount*2)

	for _, w := range workloads {
		doneWg.Add(1)
		go func(work testWorkload) {
			defer doneWg.Done()
			startWg.Wait() // all workers start simultaneously

			// 1. Admit alias plan concurrently using legitimate sources mapping to the same target run_command
			var reqBody []byte
			if work.sourceFormat == "anthropic" {
				reqBody = []byte(fmt.Sprintf(`{"tools":[{"name":%q,"description":""}]}`, work.sourceName))
			} else {
				reqBody = []byte(fmt.Sprintf(`{"tools":[{"type":"function","function":{"name":%q}}]}`, work.sourceName))
			}
			req := pluginapi.RequestInterceptRequest{
				RequestID:    work.requestID,
				SourceFormat: work.sourceFormat,
				Body:         reqBody,
			}
			preferred := map[string]string{work.sourceName: work.cloakedName}
			plan, rejection := admitRequestAliasPlanOrReject(&req, pluginapi.RequestInterceptResponse{}, work.client, preferred)
			if rejection != nil || plan == nil {
				errors <- fmt.Errorf("worker %s: admission failed: %s", work.requestID, rejection)
				return
			}

			// 2. Perform concurrent response uncloaking against the common cloaked target run_command
			var respReqBody []byte
			if work.sourceFormat == "anthropic" {
				respReqBody = []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","name":%q,"input":{}}]}`, work.cloakedName))
			} else {
				respReqBody = []byte(fmt.Sprintf(`{"choices":[{"message":{"tool_calls":[{"function":{"name":%q,"arguments":"{}"}}]}}]}`, work.cloakedName))
			}
			var resp pluginapi.ResponseInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
				RequestID:    work.requestID,
				SourceFormat: work.sourceFormat,
				Body:         respReqBody,
			}, &resp)

			// 3. Verify exact source name is restored without cross-contamination or loss
			expectedNeedle := fmt.Sprintf(`"name":%q`, work.sourceName)
			altNeedle := fmt.Sprintf(`"name": %q`, work.sourceName)
			if !bytes.Contains(resp.Body, []byte(expectedNeedle)) && !bytes.Contains(resp.Body, []byte(altNeedle)) {
				errors <- fmt.Errorf("worker %s: reverse map loss: expected %s, got %s", work.requestID, expectedNeedle, string(resp.Body))
				return
			}
			if bytes.Contains(resp.Body, []byte(work.cloakedName)) {
				errors <- fmt.Errorf("worker %s leaked cloaked target %s: %s", work.requestID, work.cloakedName, string(resp.Body))
				return
			}

			// 4. Verify competing client's source identity never contaminated this response
			if bytes.Contains(resp.Body, []byte(work.forbidden)) {
				errors <- fmt.Errorf("worker %s cross-contaminated by competing client source %s: %s", work.requestID, work.forbidden, string(resp.Body))
				return
			}

			// 5. Complete request lifecycle
			var completed struct{}
			ompMeasurementCall(t, pluginabi.MethodRequestComplete, pluginapi.RequestCompletion{RequestID: work.requestID}, &completed)
		}(w)
	}

	startWg.Done() // release all workers simultaneously
	doneWg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrency error: %v", err)
	}
}
