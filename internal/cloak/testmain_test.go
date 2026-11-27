package cloak

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/monet88/antigravity-cloak/internal/clients/claudecode"
	"github.com/monet88/antigravity-cloak/internal/clients/codex"
	"github.com/monet88/antigravity-cloak/internal/clients/omp"
)

func TestMain(m *testing.M) {
	Initialize(
		claudecode.Spec(),
		codex.Spec(),
		omp.Spec(),
	)
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
