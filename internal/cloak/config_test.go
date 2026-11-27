package cloak

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/monet88/antigravity-cloak/internal/clients/claudecode"
	"github.com/monet88/antigravity-cloak/internal/clients/codex"
	"github.com/monet88/antigravity-cloak/internal/clients/omp"
)

func TestReconfigureWithToolMappingsOverride(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	raw := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
tool_mappings:
  claude_code:
    Bash: run_command
    my_custom_tool: ask_permission
  codex:
    exec: run_command
  Claude_Code:
    Grep: grep_search
`)), nil)
	var env struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("ok = false; body=%s", raw)
	}
	cfg := activeFilterConfig()
	// Verify ToolMappings parsed correctly
	if cfg.ToolMappings["claude_code"]["my_custom_tool"] != "ask_permission" {
		t.Fatalf("expected custom tool mapping")
	}
	// Client keys are normalized to lowercase, so "Claude_Code" merges into
	// "claude_code" rather than creating a separate, unreachable entry.
	if cfg.ToolMappings["claude_code"]["Grep"] != "grep_search" {
		t.Fatalf("expected mixed-case client key to normalize into claude_code; got %#v", cfg.ToolMappings["claude_code"])
	}
	if _, exists := cfg.ToolMappings["Claude_Code"]; exists {
		t.Fatalf("expected no mixed-case client key after normalization")
	}
}

func TestHandlePluginLifecycleReconfigureAppliesCustomMappingsAndDefaultToggle(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	raw := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
enabled: true
priority: 1
use_default_keywords: false
custom_mappings:
  Cursor: Antigravity
  Windsurf: Antigravity
`)), nil)
	var env struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("ok = false; body=%s", raw)
	}

	if got, rewritten, _ := rewriteRequestBodyForClient([]byte(`{"system":"You are Codex."}`), "openai", "claude_code", false); rewritten {
		t.Fatalf("Codex rewritten after disabling defaults; body=%s", got)
	}
	got, rewritten, _ := rewriteRequestBodyForClient([]byte(`{"system":"route this Cursor session"}`), "openai", "claude_code", false)
	if !rewritten {
		t.Fatalf("Cursor rewritten = false, want true")
	}
	if !strings.Contains(string(got), "route this Antigravity session") {
		t.Fatalf("body = %s, want Cursor replaced with Antigravity", got)
	}
}

func TestHandlePluginLifecycleReconfigureAcceptsDelimitedCustomMappings(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	raw := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
custom_mappings: |
  Cursor: Antigravity
  Windsurf: Antigravity
`)), nil)
	var env struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("ok = false; body=%s", raw)
	}

	got, rewritten, _ := rewriteRequestBodyForClient([]byte(`{"system":"Windsurf rules"}`), "openai", "claude_code", false)
	if !rewritten {
		t.Fatalf("Windsurf rewritten = false, want true")
	}
	if !strings.Contains(string(got), "Antigravity rules") {
		t.Fatalf("body = %s, want Windsurf replaced with Antigravity", got)
	}
}

func TestHandlePluginLifecycleReconfigureKeepsPreviousConfigOnInvalidInput(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	raw := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
use_default_keywords: false
custom_mappings:
  Cursor: Antigravity
`)), nil)
	var initEnv struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &initEnv)
	if !initEnv.OK {
		t.Fatalf("initial reconfigure ok = false; body=%s", raw)
	}

	raw = HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
custom_mappings:
  - nested: invalid
`)), nil)

	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	mustUnmarshalJSON(t, raw, &envelope)
	if envelope.OK {
		t.Fatalf("ok = true, want false")
	}
	if envelope.Error.Code != "invalid_config" {
		t.Fatalf("error code = %q, want invalid_config", envelope.Error.Code)
	}

	if _, rewritten, _ := rewriteRequestBodyForClient([]byte(`{"system":"route this Cursor session"}`), "openai", "claude_code", false); !rewritten {
		t.Fatalf("Cursor rewritten = false after invalid config, want previous config retained")
	}
	if got, rewritten, _ := rewriteRequestBodyForClient([]byte(`{"system":"You are Codex."}`), "openai", "claude_code", false); rewritten {
		t.Fatalf("Codex rewritten after invalid config, want previous disabled-default state retained; body=%s", got)
	}
}

func TestHandlePluginLifecycleReconfigureOMPPromptSanitizationsPreservesConfigOnInvalidInput(t *testing.T) {
	isolateOMPMeasurement(t)

	raw := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(`
use_default_keywords: false
omp_prompt_sanitizations:
  "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.": "Active custom header."
  "active-sentinel": "preserved-sentinel"
`)), nil)
	var initEnv struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &initEnv)
	if !initEnv.OK {
		t.Fatalf("initial reconfigure ok = false; body=%s", raw)
	}

	invalidConfigs := []struct {
		name string
		yaml string
	}{
		{name: "int-map-value", yaml: "omp_prompt_sanitizations:\n  foo: 123\n"},
		{name: "bool-map-value", yaml: "omp_prompt_sanitizations:\n  foo: true\n"},
		{name: "null-map-value", yaml: "omp_prompt_sanitizations:\n  foo: null\n"},
		{name: "nested-map-value", yaml: "omp_prompt_sanitizations:\n  foo:\n    bar: baz\n"},
		{name: "array-map-value", yaml: "omp_prompt_sanitizations:\n  foo:\n    - bar\n"},
		{name: "top-level-bool", yaml: "omp_prompt_sanitizations: true\n"},
		{name: "top-level-int", yaml: "omp_prompt_sanitizations: 42\n"},
		{name: "empty-object-key", yaml: "omp_prompt_sanitizations:\n  \"\": \"bar\"\n"},
		{name: "whitespace-object-key", yaml: "omp_prompt_sanitizations:\n  \"   \": \"bar\"\n"},
		{name: "empty-string-key", yaml: "omp_prompt_sanitizations: \": bar\"\n"},
		{name: "whitespace-string-key", yaml: "omp_prompt_sanitizations: \"   : bar\"\n"},
		{name: "missing-colon-string", yaml: "omp_prompt_sanitizations: \"no-colon-here\"\n"},
		{name: "malformed-comma-missing-colon", yaml: "omp_prompt_sanitizations: \"foo: bar, bad\"\n"},
		{name: "malformed-comma-empty-key", yaml: "omp_prompt_sanitizations: \"foo: bar, : baz\"\n"},
		{name: "non-string-array-item", yaml: "omp_prompt_sanitizations:\n  - 99\n"},
		{name: "empty-array-item", yaml: "omp_prompt_sanitizations:\n  - \"\"\n"},
		{name: "whitespace-array-item", yaml: "omp_prompt_sanitizations:\n  - \"   \"\n"},
		{name: "empty-key-array-item", yaml: "omp_prompt_sanitizations:\n  - \": bar\"\n"},
		{name: "invalid-utf8-binary-key", yaml: "omp_prompt_sanitizations:\n  ? !!binary \"/w==\"\n  : \"bar\"\n"},
		{name: "invalid-utf8-binary-array-item", yaml: "omp_prompt_sanitizations:\n  - !!binary \"/zpiYXI=\"\n"},
	}

	for _, tc := range invalidConfigs {
		t.Run(tc.name, func(t *testing.T) {
			rawErr := HandlePluginLifecycle(lifecycleRequestJSON(t, []byte(tc.yaml)), nil)
			var envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			mustUnmarshalJSON(t, rawErr, &envelope)
			if envelope.OK {
				t.Fatalf("HandlePluginLifecycle unexpectedly accepted %s: %s", tc.name, rawErr)
			}
			if envelope.Error.Code != "invalid_config" {
				t.Fatalf("HandlePluginLifecycle error code = %q, want invalid_config", envelope.Error.Code)
			}

			reqBody := ompMeasurementJSON(t, map[string]any{
				"system":   "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. active-sentinel",
				"messages": []any{},
				"tools":    []any{},
			})
			req := ompMeasurementRequest("preserve-"+tc.name, "anthropic", reqBody)
			rawIntercept := HandleRequestInterceptBefore(ompMeasurementJSON(t, req))
			var icEnv struct {
				OK     bool `json:"ok"`
				Result struct {
					Body string `json:"Body"`
				} `json:"result"`
			}
			mustUnmarshalJSON(t, rawIntercept, &icEnv)
			decoded, decErr := base64.StdEncoding.DecodeString(icEnv.Result.Body)
			if decErr != nil {
				t.Fatalf("decode body: %v", decErr)
			}
			if !strings.Contains(string(decoded), "Active custom header. preserved-sentinel") {
				t.Fatalf("active config was clobbered after %s: got %s", tc.name, decoded)
			}
		})
	}
}

func TestInitializeRepeatedIsIdempotent(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	uaCountBefore := len(userAgentEvidence)
	presetCountBefore := len(defaultOMPSanitizationPresets)
	cloakTableCountBefore := len(defaultCloakTables)

	Initialize(claudecode.Spec(), codex.Spec(), omp.Spec())
	Initialize(claudecode.Spec(), codex.Spec(), omp.Spec())

	if len(userAgentEvidence) != uaCountBefore {
		t.Fatalf("userAgentEvidence len = %d, want %d (must not accumulate)", len(userAgentEvidence), uaCountBefore)
	}
	if len(defaultOMPSanitizationPresets) != presetCountBefore {
		t.Fatalf("defaultOMPSanitizationPresets len = %d, want %d (must not accumulate)", len(defaultOMPSanitizationPresets), presetCountBefore)
	}
	if len(defaultCloakTables) != cloakTableCountBefore {
		t.Fatalf("defaultCloakTables len = %d, want %d (must not accumulate)", len(defaultCloakTables), cloakTableCountBefore)
	}
	if got := len(activeFilterConfig().ompSanitizationRules); got != presetCountBefore {
		t.Fatalf("activeFilterConfig().ompSanitizationRules len = %d, want %d", got, presetCountBefore)
	}
}
