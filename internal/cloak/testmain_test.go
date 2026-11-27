package cloak

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/monet88/antigravity-cloak/internal/clients/claudecode"
	"github.com/monet88/antigravity-cloak/internal/clients/codex"
	"github.com/monet88/antigravity-cloak/internal/clients/omp"
	"github.com/monet88/antigravity-cloak/internal/spec"
)

const antigravityIdentity = spec.AntigravityIdentity

var (
	claudeCodeBrandMappings      []rewriteMapping
	claudeContextMappings        []rewriteMapping
	claudeReverseContextMappings []rewriteMapping
	codexContextMappings         []rewriteMapping
	codexReverseContextMappings  []rewriteMapping
	ompBrandMappings             []rewriteMapping
)

func TestMain(m *testing.M) {
	ccSpec := claudecode.Spec()
	codexSpec := codex.Spec()
	ompSpec := omp.Spec()

	claudeCodeBrandMappings = ccSpec.ForwardBrandMappings
	claudeContextMappings = ccSpec.ContextMappings
	claudeReverseContextMappings = ccSpec.ReverseContextMappings
	codexContextMappings = codexSpec.ContextMappings
	codexReverseContextMappings = codexSpec.ReverseContextMappings
	ompBrandMappings = ompSpec.ForwardBrandMappings

	Initialize(ccSpec, codexSpec, ompSpec)
	os.Exit(m.Run())
}

func lifecycleRequestJSON(t *testing.T, configYAML []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"config_yaml":    configYAML,
	})
	if err != nil {
		t.Fatalf("marshal lifecycle request: %v", err)
	}
	return raw
}

func restoreDefaultFilterConfig(t *testing.T) {
	t.Helper()
	applyFilterConfig(defaultFilterConfig())
}

func mustUnmarshalJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}

func responseInterceptRequestJSON(t *testing.T, reqBody, respBody, sourceFormat string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"SourceFormat": sourceFormat,
		"RequestBody":  []byte(reqBody),
		"Body":         []byte(respBody),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
