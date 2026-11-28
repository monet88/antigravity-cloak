package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func callPluginABI[Req any, Resp any](t *testing.T, method string, req Req) Resp {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	raw, code := handlePluginCall(method, payload)
	if code != 0 {
		t.Fatalf("%s code = %d, want 0; body=%s", method, code, raw)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result Resp `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("%s returned ok=false: %s", method, raw)
	}
	return env.Result
}

func reconfigurePluginYAML(t *testing.T, yamlCfg string) {
	t.Helper()
	raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(yamlCfg)))
	if code != 0 {
		t.Fatalf("plugin.reconfigure code = %d, want 0; body=%s", code, raw)
	}
	var env struct {
		OK bool `json:"ok"`
	}
	mustUnmarshalJSON(t, raw, &env)
	if !env.OK {
		t.Fatalf("plugin.reconfigure ok=false: %s", raw)
	}
}

func completePluginRequest(t *testing.T, requestID string) {
	t.Helper()
	_ = callPluginABI[pluginapi.RequestCompletion, map[string]any](
		t,
		pluginabi.MethodRequestComplete,
		pluginapi.RequestCompletion{RequestID: requestID, Outcome: "succeeded"},
	)
}

func mustMarshalJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func appendStreamResult(dst, input []byte, res pluginapi.StreamChunkInterceptResponse) []byte {
	if res.DropChunk {
		return dst
	}
	if len(res.Body) > 0 {
		return append(dst, res.Body...)
	}
	return append(dst, input...)
}

func makeTool(format, name, description string, schema map[string]any) map[string]any {
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	if format == "openai" || format == "openai-response" {
		fn := map[string]any{
			"name":       name,
			"parameters": schema,
		}
		if description != "" {
			fn["description"] = description
		}
		return map[string]any{
			"type":     "function",
			"function": fn,
		}
	}
	tool := map[string]any{
		"name":         name,
		"input_schema": schema,
	}
	if description != "" {
		tool["description"] = description
	}
	return tool
}
