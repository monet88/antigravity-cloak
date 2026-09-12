package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSanitizeSystemConventions(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""},
		{"system-conventions.md <system-directive>", "system-conventions.md <system-directive>"},
		{"<system-conventions>規約\nMUST</system-conventions>", "<conventions>規約\nMUST</conventions>"},
		{"<system-conventions></system-conventions><system-conventions>x</system-conventions>", "<conventions></conventions><conventions>x</conventions>"},
		{"<conventions>unchanged</conventions>", "<conventions>unchanged</conventions>"},
		{"<SYSTEM-CONVENTIONS><system-conventions-extra>", "<SYSTEM-CONVENTIONS><system-conventions-extra>"},
	} {
		got, changed := sanitizeSystemConventions(tc.input)
		if got != tc.want || changed != (tc.input != tc.want) {
			t.Errorf("input %q: got %q changed=%t; want %q", tc.input, got, changed, tc.want)
		}
		if twice, changedAgain := sanitizeSystemConventions(got); twice != got || changedAgain {
			t.Errorf("not idempotent: %q -> %q changed=%t", got, twice, changedAgain)
		}
	}
}

func TestSanitizeSystemConventionsContent(t *testing.T) {
	const original = "<system-conventions>x</system-conventions>"
	const sanitized = "<conventions>x</conventions>"
	for _, blockType := range []string{"text", "input_text", "image", "tool_use"} {
		value := []any{map[string]any{"type": blockType, "text": original, "input": map[string]any{"text": original}}, nil, 42}
		got := sanitizeSystemConventionsContent(value).([]any)
		want := original
		if blockType == "text" || blockType == "input_text" {
			want = sanitized
		}
		block := got[0].(map[string]any)
		if block["text"] != want || block["input"].(map[string]any)["text"] != original || got[1] != nil || got[2] != 42 {
			t.Fatalf("unexpected %s block transformation: %#v", blockType, got)
		}
	}
}

func TestSystemConventionsRequestScope(t *testing.T) {
	const original = "<system-conventions>RFC 2119: MUST; system-conventions.md; <system-directive> unchanged.</system-conventions>"
	const sanitized = "<conventions>RFC 2119: MUST; system-conventions.md; <system-directive> unchanged.</conventions>"
	for _, format := range []string{"anthropic", "openai"} {
		for _, route := range []string{"protected", "requested-model", "bypass", "unmarked", "other-client"} {
			t.Run(format+"/"+route, func(t *testing.T) {
				isolateOMPMeasurement(t)
				cfg := defaultFilterConfig()
				cfg.UseDefaultKeywords = false
				cfg.ModelPrefixes = []string{"unrelated/"}
				applyFilterConfig(cfg)
				block := map[string]any{"type": "text", "text": original, "cache_control": map[string]any{"type": "ephemeral", "note": original}}
				tool := map[string]any{"name": "read", "description": original, "input_schema": map[string]any{"type": "object", "description": original}}
				if format == "openai" {
					tool = map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": original, "parameters": map[string]any{"type": "object", "description": original}}}
				}
				root := map[string]any{
					"system": original,
					"messages": []any{
						map[string]any{"role": "system", "content": []any{block}},
						map[string]any{"role": "developer", "content": original},
						map[string]any{"role": "user", "content": original},
						map[string]any{"role": "assistant", "content": original},
						map[string]any{"role": "tool", "content": original},
					},
					"tools": []any{tool},
				}
				body := ompMeasurementJSON(t, root)
				req := ompMeasurementRequest(t.Name(), format, body)
				switch route {
				case "requested-model":
					req.Model = "resolved-model"
				case "bypass":
					req.Model, req.RequestedModel = "other/model", "other/model"
				case "unmarked":
					req.Headers = nil
				case "other-client":
					req.Headers.Set("X-Cloak-Client", "codex")
				}
				var result pluginapi.RequestInterceptResponse
				ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, req, &result)
				if result.Terminate {
					t.Fatalf("unexpected rejection: %s", result.ResponseBody)
				}
				if route == "bypass" || route == "unmarked" || route == "other-client" {
					if len(result.Body) != 0 && !bytes.Equal(result.Body, body) {
						t.Fatalf("non-protected body changed: %s", result.Body)
					}
					return
				}
				root["system"] = sanitized
				block["text"] = sanitized
				root["messages"].([]any)[1].(map[string]any)["content"] = sanitized
				if format == "openai" {
					tool["function"].(map[string]any)["name"] = "view_file"
				} else {
					tool["name"] = "view_file"
				}
				var got map[string]any
				if err := json.Unmarshal(result.Body, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, root) {
					t.Fatalf("unexpected transformed request:\ngot %s\nwant %s", result.Body, ompMeasurementJSON(t, root))
				}
			})
		}
	}
}
