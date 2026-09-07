package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)
// TestIssue26_CanonicalSafeMappingSet_StaticProperties validates the nine canonical
// Safe Mapping Set pairs, non-empty names, uniqueness of targets, and deterministic
// reversible static inverse.
func TestIssue26_CanonicalSafeMappingSet_StaticProperties(t *testing.T) {
	// Exactly 9 approved pairs
	if len(canonicalOMPSafeMappingSet) != 9 {
		t.Fatalf("canonicalOMPSafeMappingSet length = %d, want 9", len(canonicalOMPSafeMappingSet))
	}

	expectedPairs := map[string]string{
		"read":       "view_file",
		"write":      "write_to_file",
		"edit":       "replace_file_content",
		"bash":       "run_command",
		"grep":       "grep_search",
		"glob":       "find_by_name",
		"task":       "invoke_subagent",
		"ask":        "ask_question",
		"web_search": "search_web",
	}

	if !reflect.DeepEqual(canonicalOMPSafeMappingSet, expectedPairs) {
		t.Fatalf("canonicalOMPSafeMappingSet = %v, want %v", canonicalOMPSafeMappingSet, expectedPairs)
	}

	// Verify defaultCloakTables["oh_my_pi"] matches canonical set
	ompDefaults := defaultCloakTables["oh_my_pi"]
	if !reflect.DeepEqual(ompDefaults, expectedPairs) {
		t.Fatalf("defaultCloakTables[\"oh_my_pi\"] = %v, want %v", ompDefaults, expectedPairs)
	}

	// Verify non-empty sources and targets, and target uniqueness
	targetSet := make(map[string]string)
	for src, tgt := range canonicalOMPSafeMappingSet {
		if strings.TrimSpace(src) == "" {
			t.Fatalf("empty source name in canonical mapping: %q", src)
		}
		if strings.TrimSpace(tgt) == "" {
			t.Fatalf("empty target name in canonical mapping for source %q", src)
		}
		if existingSrc, duplicate := targetSet[tgt]; duplicate {
			t.Fatalf("target %q is not unique: mapped by both %q and %q", tgt, existingSrc, src)
		}
		targetSet[tgt] = src
	}

	// Verify deterministic reversible static inverse
	ompUncloaks := defaultUncloakTables["oh_my_pi"]
	if len(ompUncloaks) != 9 {
		t.Fatalf("defaultUncloakTables[\"oh_my_pi\"] length = %d, want 9", len(ompUncloaks))
	}
	for src, tgt := range canonicalOMPSafeMappingSet {
		if revSrc, ok := ompUncloaks[tgt]; !ok || revSrc != src {
			t.Fatalf("uncloak mapping for target %q = %q, want %q", tgt, revSrc, src)
		}
	}

	// Verify removed static mappings are NOT in the cloak table
	removedTools := []string{
		"todo", "hub", "eval",
		"vibe_spawn", "vibe_send", "vibe_wait", "vibe_kill", "vibe_list",
		"init_experiment", "run_experiment", "log_experiment", "update_notes",
	}
	for _, tool := range removedTools {
		if tgt, present := ompDefaults[tool]; present {
			t.Fatalf("removed tool %q must not be in defaultCloakTables[\"oh_my_pi\"], mapped to %q", tool, tgt)
		}
	}

	// glob must map to find_by_name, NOT list_dir
	if ompDefaults["glob"] != "find_by_name" {
		t.Fatalf("glob mapped to %q, want 'find_by_name'", ompDefaults["glob"])
	}
}

// TestIssue26_OMPSourceIdentityInventory_FiniteAndDetectionOnly proves that
// ompSourceIdentityInventory contains exactly the 9 safe sources + 12 legacy pass-throughs,
// that unknown vibe names (e.g. vibe_future_tool) are NOT classified as OMP by prefix,
// and that detection-only source names do not cause transformation.
func TestIssue26_OMPSourceIdentityInventory_FiniteAndDetectionOnly(t *testing.T) {
	// 9 safe mapping sources + 12 legacy detection sources = 21 total
	if len(ompSourceIdentityInventory) != 21 {
		t.Fatalf("ompSourceIdentityInventory length = %d, want 21", len(ompSourceIdentityInventory))
	}

	// Verify the finite 5 vibe tools
	vibeTools := []string{"vibe_spawn", "vibe_send", "vibe_wait", "vibe_kill", "vibe_list"}
	for _, v := range vibeTools {
		if !ompSourceIdentityInventory[v] {
			t.Fatalf("expected vibe tool %q in ompSourceIdentityInventory", v)
		}
		if !clientDistinctiveTools["oh_my_pi"][v] {
			t.Fatalf("expected vibe tool %q in clientDistinctiveTools[\"oh_my_pi\"]", v)
		}
	}

	// Regression: unknown vibe_future_tool is not in inventory and not treated as OMP by prefix
	if ompSourceIdentityInventory["vibe_future_tool"] {
		t.Fatal("vibe_future_tool must NOT be in ompSourceIdentityInventory")
	}
	if clientDistinctiveTools["oh_my_pi"]["vibe_future_tool"] {
		t.Fatal("vibe_future_tool must NOT be in clientDistinctiveTools[\"oh_my_pi\"]")
	}

	// Unknown vibe tool paired with a single common tool does not detect as OMP
	if client := detectClient([]string{"read", "vibe_future_tool"}); client != "" {
		t.Fatalf("detectClient([read, vibe_future_tool]) = %q, want empty", client)
	}
	// Even with 3 common tools (below colliding threshold 4), vibe_future_tool does not help
	if client := detectClient([]string{"read", "write", "bash", "vibe_future_tool"}); client != "" {
		t.Fatalf("detectClient([read, write, bash, vibe_future_tool]) = %q, want empty", client)
	}

	// The 5 listed legacy vibe names retain intended source-side detection
	for _, v := range vibeTools {
		if client := detectClient([]string{"read", v}); client != "oh_my_pi" {
			t.Fatalf("detectClient([read, %s]) = %q, want 'oh_my_pi'", v, client)
		}
	}

	// Autoresearch tools retain detection
	autoresearchTools := []string{"init_experiment", "run_experiment", "log_experiment", "update_notes"}
	for _, ar := range autoresearchTools {
		if !ompSourceIdentityInventory[ar] {
			t.Fatalf("expected autoresearch tool %q in ompSourceIdentityInventory", ar)
		}
		if client := detectClient([]string{"read", ar}); client != "oh_my_pi" {
			t.Fatalf("detectClient([read, %s]) = %q, want 'oh_my_pi'", ar, client)
		}
	}

	// Detection-only source names identify OMP but do NOT transform
	passthroughReq := `{
		"model": "agy/gemini-3.7-flash",
		"messages": [{"role": "user", "content": "run experiment"}],
		"tools": [
			{"type": "function", "function": {"name": "todo", "description": "todo"}},
			{"type": "function", "function": {"name": "hub", "description": "hub"}},
			{"type": "function", "function": {"name": "eval", "description": "eval"}},
			{"type": "function", "function": {"name": "vibe_spawn", "description": "spawn"}},
			{"type": "function", "function": {"name": "init_experiment", "description": "init"}}
		]
	}`
	rewrittenBody, changed := rewriteRequestBody([]byte(passthroughReq), "openai")
	// Since no tool is cloaked and no brand keywords are in messages, rewritten is false
	if changed {
		t.Fatalf("request with only pass-through tools should not be rewritten, got: %s", string(rewrittenBody))
	}
}

// TestIssue26_RuntimeToolMappingsDoNotBecomeIdentityEvidence proves that arbitrary
// operator tool_mappings.oh_my_pi entries do not become source or target identity evidence.
func TestIssue26_RuntimeToolMappingsDoNotBecomeIdentityEvidence(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	// Inject operator custom mappings under tool_mappings.oh_my_pi
	cfg := activeFilterConfig()
	customMappings := copyToolMappings(cfg.ToolMappings)
	customMappings["oh_my_pi"]["custom_source_tool"] = "custom_target_tool"
	applyFilterConfig(filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       customMappings,
	})

	// 1. Source side: custom_source_tool must NOT become OMP source identity evidence
	if client := detectClient([]string{"read", "custom_source_tool"}); client != "" {
		t.Fatalf("detectClient with custom tool = %q, want empty (operator mapping leaked into source detection)", client)
	}

	// 2. Target side: custom_target_tool must NOT become OMP target identity evidence
	targets := []string{"view_file", "write_to_file", "custom_target_tool"}
	if corroborateCloakedTargetOMP(targets) {
		t.Fatalf("corroborateCloakedTargetOMP with custom target = true, want false")
	}
	if got := detectCloakedClientWithSignal(targets, true); got == "oh_my_pi" {
		t.Fatalf("detectCloakedClientWithSignal with custom target = %q, want non-oh_my_pi", got)
	}
}

// TestIssue26_TargetInventoryCorroborationOnly_NoStandaloneAttribution validates
// that target identities alone NEVER resolve to oh_my_pi without an independent signal,
// covering the explicit safety cases from Issue #26.
func TestIssue26_TargetInventoryCorroborationOnly_NoStandaloneAttribution(t *testing.T) {
	safetyCases := []struct {
		name    string
		targets []string
	}{
		{
			name:    "case 1: [view_file, write_to_file, find_by_name]",
			targets: []string{"view_file", "write_to_file", "find_by_name"},
		},
		{
			name:    "case 2: [run_command, ask_question, find_by_name]",
			targets: []string{"run_command", "ask_question", "find_by_name"},
		},
		{
			name: "case 3: all nine canonical AGY targets",
			targets: []string{
				"view_file", "write_to_file", "replace_file_content",
				"run_command", "grep_search", "find_by_name",
				"invoke_subagent", "ask_question", "search_web",
			},
		},
	}

	for _, tc := range safetyCases {
		t.Run(tc.name, func(t *testing.T) {
			// Standalone target detection MUST NOT resolve to oh_my_pi
			got := detectCloakedClient(tc.targets)
			if got == "oh_my_pi" {
				t.Fatalf("standalone detectCloakedClient(%v) = %q, MUST NOT resolve to oh_my_pi", tc.targets, got)
			}

			// When independent OMP attribution is present, corroboration succeeds
			if !corroborateCloakedTargetOMP(tc.targets) {
				t.Fatalf("corroborateCloakedTargetOMP(%v) = false, want true for canonical targets", tc.targets)
			}
			if corroborated := detectCloakedClientWithSignal(tc.targets, true); corroborated != "oh_my_pi" {
				t.Fatalf("detectCloakedClientWithSignal(%v, true) = %q, want 'oh_my_pi'", tc.targets, corroborated)
			}
		})
	}
}

// TestIssue26_TargetOnlyNoMarker_NoReverseMutation proves that target-only no-marker
// canonical AGY names cannot independently authorize OMP reverse mutation in
// response or stream chunk interception.
func TestIssue26_TargetOnlyNoMarker_NoReverseMutation(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	reqID := "target-only-nomarker-test"
	model := "agy/gemini-3.7-flash"

	// 1. Non-streaming response intercept with no-marker, no-UA, target-only body
	targetOnlyRespBody := []byte(`{
		"choices": [
			{
				"message": {
					"role": "assistant",
					"tool_calls": [
						{"id": "c1", "type": "function", "function": {"name": "view_file", "arguments": "{\"path\":\"foo\"}"}},
						{"id": "c2", "type": "function", "function": {"name": "run_command", "arguments": "{\"command\":\"ls\"}"}},
						{"id": "c3", "type": "function", "function": {"name": "find_by_name", "arguments": "{\"pattern\":\"*.go\"}"}}
					]
				}
			}
		]
	}`)

	respPayload := makeIntegrationResponseInterceptPayload(t, reqID, "openai", model, targetOnlyRespBody)
	rawResp, _ := handlePluginCall(pluginabi.MethodResponseInterceptAfter, respPayload)
	outResp := string(decodeEnvelopeBody(t, rawResp))

	// If mutated, it would contain OMP names "read", "bash", "glob".
	// It MUST NOT authorize OMP reverse mutation.
	if strings.Contains(outResp, `"name":"read"`) || strings.Contains(outResp, `"name":"bash"`) || strings.Contains(outResp, `"name":"glob"`) {
		t.Fatalf("target-only body authorized OMP reverse mutation: %s", outResp)
	}

	// 2. Stream chunk intercept with target-only names and no prior OMP session
	streamChunk := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"find_by_name\"}}]}}]}\n\n")
	chunkPayload := makeIntegrationStreamChunkPayload(t, reqID, "openai", model, 0, streamChunk, nil)
	rawChunk, _ := handlePluginCall(pluginabi.MethodResponseInterceptStreamChunk, chunkPayload)
	outChunk := string(decodeEnvelopeBody(t, rawChunk))

	if strings.Contains(outChunk, `"name":"glob"`) {
		t.Fatalf("stream chunk authorized OMP reverse mutation for find_by_name -> glob: %s", outChunk)
	}
}

// TestIssue26_DeterministicGeneratedTargetSubsetsDifferential characterises
// classification across every subset of the 9 canonical AGY targets with cardinality >= 3.
// Total subsets = 466.
// For every subset:
// 1. Prove detectCloakedClient never returns oh_my_pi.
// 2. Prove buildUncloakTable never authorizes OMP reverse mutation.
// 3. Document and report any pre-existing generic cross-client ambiguity (Claude/Codex).
func TestIssue26_DeterministicGeneratedTargetSubsetsDifferential(t *testing.T) {
	canonicalTargets := []string{
		"view_file",
		"write_to_file",
		"replace_file_content",
		"run_command",
		"grep_search",
		"find_by_name",
		"invoke_subagent",
		"ask_question",
		"search_web",
	}
	sort.Strings(canonicalTargets)

	// Generate all subsets of size >= 3
	var subsets [][]string
	n := len(canonicalTargets)
	for mask := 1; mask < (1 << n); mask++ {
		var sub []string
		for i := 0; i < n; i++ {
			if (mask & (1 << i)) != 0 {
				sub = append(sub, canonicalTargets[i])
			}
		}
		if len(sub) >= 3 {
			subsets = append(subsets, sub)
		}
	}

	if len(subsets) != 466 {
		t.Fatalf("expected exactly 466 subsets of size >= 3 from 9 targets, got %d", len(subsets))
	}

	ompCount := 0
	claudeCount := 0
	codexCount := 0
	emptyCount := 0

	var claudeExamples []string
	var codexExamples []string

	for _, sub := range subsets {
		got := detectCloakedClient(sub)

		// 1. Invariant: detectCloakedClient MUST NEVER return oh_my_pi on target names alone
		if got == "oh_my_pi" {
			t.Fatalf("detectCloakedClient(%v) = 'oh_my_pi', MUST NOT return oh_my_pi", sub)
		}

		// 2. Invariant: buildUncloakTable on target-only body MUST NOT return oh_my_pi uncloak table
		toolsJSON := make([]map[string]any, len(sub))
		for i, name := range sub {
			toolsJSON[i] = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": name},
			}
		}
		reqMap := map[string]any{"tools": toolsJSON}
		reqBytes, _ := json.Marshal(reqMap)
		uncloakTable, client := buildUncloakTable(reqBytes, "openai")
		if client == "oh_my_pi" {
			t.Fatalf("buildUncloakTable(%v) client = 'oh_my_pi', MUST NOT be oh_my_pi", sub)
		}
		if uncloakTable != nil && (uncloakTable["view_file"] == "read" || uncloakTable["find_by_name"] == "glob") {
			t.Fatalf("buildUncloakTable(%v) returned OMP uncloak table %v without attribution", sub, uncloakTable)
		}

		// 3. Differential accounting
		switch got {
		case "oh_my_pi":
			ompCount++
		case "claude_code":
			claudeCount++
			if len(claudeExamples) < 5 {
				claudeExamples = append(claudeExamples, fmt.Sprintf("%v", sub))
			}
		case "codex":
			codexCount++
			codexExamples = append(codexExamples, fmt.Sprintf("%v", sub))
		case "":
			emptyCount++
		default:
			t.Fatalf("unexpected client %q for subset %v", got, sub)
		}
	}

	t.Logf("Issue #26 Native AGY Differential Characterization over %d subsets (cardinality >= 3):", len(subsets))
	t.Logf("  oh_my_pi classifications: %d (MUST be 0 - verified)", ompCount)
	t.Logf("  empty (unattributed):     %d", emptyCount)
	t.Logf("  claude_code fallback:    %d (pre-existing generic ambiguity)", claudeCount)
	t.Logf("  codex fallback:          %d (pre-existing generic ambiguity: %v)", codexCount, codexExamples)

	if ompCount != 0 {
		t.Fatalf("expected 0 oh_my_pi classifications across all subsets, got %d", ompCount)
	}

	// Verify the pre-existing Claude Code ambiguity:
	// Claude Code's cloak table shares 8 of the 9 native AGY targets (all except find_by_name).
	// Therefore, any subset omitting find_by_name has 100% Claude Code hit ratio (219 subsets).
	// Subsets containing find_by_name match Claude Code if (k-1)/k >= 0.8 (k >= 5).
	if claudeCount == 0 {
		t.Fatalf("expected non-zero pre-existing claude_code characterizations, got 0")
	}
	// In exact 3/3 ties (e.g. {run_command, ask_question, search_web}),
	// claude_code precedes codex alphabetically ("claude_code" < "codex"), so claude_code wins.
	if codexCount != 0 {
		t.Fatalf("expected 0 codex classifications due to claude_code tie-break precedence, got %d", codexCount)
	}
}

// TestIssue26_MixedMappedAndUnmappedTools proves that in a request containing both
// safe-mapped and unmapped OMP tools, only the safe subset is transformed.
func TestIssue26_MixedMappedAndUnmappedTools(t *testing.T) {
	req := `{
		"model": "agy/gemini-3.7-flash",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "read", "description": "read file"}},
			{"type": "function", "function": {"name": "write", "description": "write file"}},
			{"type": "function", "function": {"name": "glob", "description": "find files"}},
			{"type": "function", "function": {"name": "todo", "description": "manage tasks"}},
			{"type": "function", "function": {"name": "vibe_spawn", "description": "spawn worker"}},
			{"type": "function", "function": {"name": "eval", "description": "run code"}}
		]
	}`

	rewritten, changed := rewriteRequestBody([]byte(req), "openai")
	if !changed {
		t.Fatal("expected request to be rewritten for safe tools")
	}

	var parsed map[string]any
	json.Unmarshal(rewritten, &parsed)
	tools := parsed["tools"].([]any)
	names := make(map[string]bool)
	for _, item := range tools {
		fn := item.(map[string]any)["function"].(map[string]any)
		names[fn["name"].(string)] = true
	}

	// Safe subset transformed
	if !names["view_file"] {
		t.Errorf("read not cloaked to view_file: %v", names)
	}
	if !names["write_to_file"] {
		t.Errorf("write not cloaked to write_to_file: %v", names)
	}
	if !names["find_by_name"] {
		t.Errorf("glob not cloaked to find_by_name: %v", names)
	}

	// Unmapped tools must pass through untouched
	if !names["todo"] {
		t.Errorf("todo did not pass through: %v", names)
	}
	if !names["vibe_spawn"] {
		t.Errorf("vibe_spawn did not pass through: %v", names)
	}
	if !names["eval"] {
		t.Errorf("eval did not pass through: %v", names)
	}

	// Stale targets must NOT be present
	stale := []string{"list_dir", "manage_task", "define_subagent", "execute_code"}
	for _, s := range stale {
		if names[s] {
			t.Errorf("stale target %q present in cloaked tools: %v", s, names)
		}
	}
}
