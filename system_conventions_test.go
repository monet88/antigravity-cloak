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

func TestSystemConventionsBuiltInRFC2119PresetsOnProtectedAGY(t *testing.T) {
	const promptOriginal = "<system-conventions>\n" +
		"RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`; `AVOID` = `SHOULD NOT`.\n" +
		"RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER`=`MUST NOT`; `AVOID`=`SHOULD NOT`.\n" +
		"RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`.\n" +
		"RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"rfc 2119 KEYWORDS: must, required, should, recommended, may, optional.\n" +
		"Rfc 2119: Must, Required, Should, Recommended, May, Optional.\n" +
		"- Normative modals: MUST, NEVER, SHOULD, MAY. The RFC 2119 word *is* the instruction.\n" +
		"See system-conventions.md and <SYSTEM-CONVENTIONS> and <system-conventions-extra>.\n" +
		"</system-conventions>"

	const promptSanitized = "<conventions>\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`; `AVOID` = `SHOULD NOT`.\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER`=`MUST NOT`; `AVOID`=`SHOULD NOT`.\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`.\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"- Normative modals: MUST, NEVER, SHOULD, MAY. The RFC 2119 word *is* the instruction.\n" +
		"See system-conventions.md and <SYSTEM-CONVENTIONS> and <system-conventions-extra>.\n" +
		"</conventions>"

	for _, format := range []string{"anthropic", "openai"} {
		t.Run(format, func(t *testing.T) {
			isolateOMPMeasurement(t)
			rawCfg, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: false
model_prefixes:
  - unrelated/
`)))
			if code != 0 {
				t.Fatalf("reconfigure code = %d, body = %s", code, rawCfg)
			}

			textBlock := map[string]any{
				"type":          "text",
				"text":          promptOriginal,
				"cache_control": map[string]any{"type": "ephemeral", "note": promptOriginal},
			}
			inputTextBlock := map[string]any{
				"type": "input_text",
				"text": promptOriginal,
			}
			imageBlock := map[string]any{
				"type": "image",
				"text": promptOriginal,
			}
			toolUseBlock := map[string]any{
				"type":  "tool_use",
				"text":  promptOriginal,
				"input": map[string]any{"text": promptOriginal},
			}
			tool := map[string]any{
				"name":         "read",
				"description":  promptOriginal,
				"input_schema": map[string]any{"type": "object", "description": promptOriginal},
			}
			if format == "openai" {
				tool = map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "read",
						"description": promptOriginal,
						"parameters":  map[string]any{"type": "object", "description": promptOriginal},
					},
				}
			}

			root := map[string]any{
				"system": promptOriginal,
				"messages": []any{
					map[string]any{"role": "system", "content": []any{textBlock, inputTextBlock, imageBlock, toolUseBlock}},
					map[string]any{"role": "developer", "content": promptOriginal},
					map[string]any{"role": "user", "content": promptOriginal},
					map[string]any{"role": "assistant", "content": promptOriginal},
					map[string]any{"role": "tool", "content": promptOriginal},
				},
				"tools": []any{tool},
			}

			reqID := "rfc2119-protected-" + format
			req := ompMeasurementRequest(reqID, format, ompMeasurementJSON(t, root))
			var result pluginapi.RequestInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, req, &result)
			if result.Terminate {
				t.Fatalf("unexpected termination: %s", result.ResponseBody)
			}

			root["system"] = promptSanitized
			textBlock["text"] = promptSanitized
			inputTextBlock["text"] = promptSanitized
			root["messages"].([]any)[1].(map[string]any)["content"] = promptSanitized
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

			// Idempotence at the ABI boundary: running an already-sanitized prompt again produces identical prompt text.
			idemRoot := map[string]any{
				"system": promptSanitized,
				"messages": []any{
					map[string]any{"role": "system", "content": promptSanitized},
					map[string]any{"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": promptSanitized}}},
				},
				"tools": []any{},
			}
			var idemResult pluginapi.RequestInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(reqID+"-idem", format, ompMeasurementJSON(t, idemRoot)), &idemResult)
			var gotIdem map[string]any
			if err := json.Unmarshal(idemResult.Body, &gotIdem); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotIdem, idemRoot) {
				t.Fatalf("sanitization was not idempotent:\ngot %s\nwant %s", idemResult.Body, ompMeasurementJSON(t, idemRoot))
			}

			// Inbound-only: response and stream paths must never reverse Normative keywords or <conventions>.
			assistantText := "Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. <conventions>ok</conventions>"
			respPayload := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"` + assistantText + `"}}]}`)
			sseFrame := []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"` + assistantText + `"}}]}` + "\n\n")
			if format == "anthropic" {
				respPayload = []byte(`{"content":[{"type":"text","text":"` + assistantText + `"}]}`)
				sseFrame = []byte(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + assistantText + `"}}` + "\n\n")
			}
			var respOut pluginapi.ResponseInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
				RequestID: reqID, SourceFormat: format, Model: "agy/measurement",
				OriginalRequest: result.Body, RequestBody: result.Body, Body: respPayload,
			}, &respOut)
			if len(respOut.Body) != 0 && !bytes.Equal(respOut.Body, respPayload) {
				t.Fatalf("response unexpectedly mutated sanitized phrases: %s", respOut.Body)
			}

			var chunkOut pluginapi.StreamChunkInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
				RequestID: reqID, SourceFormat: format, Model: "agy/measurement", ChunkIndex: 0, Body: sseFrame,
			}, &chunkOut)
			emitted := appendOMPMeasurementWire(nil, sseFrame, chunkOut)
			if !bytes.Equal(emitted, sseFrame) {
				t.Fatalf("stream chunk unexpectedly mutated sanitized phrases: %s", emitted)
			}
		})
	}

	t.Run("openai-response-deferred-carrier-skipped", func(t *testing.T) {
		isolateOMPMeasurement(t)
		root := map[string]any{
			"system": promptOriginal,
			"messages": []any{
				map[string]any{"role": "developer", "content": promptOriginal},
			},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}},
			},
		}
		req := ompMeasurementRequest("rfc2119-openai-response", "openai-response", ompMeasurementJSON(t, root))
		var result pluginapi.RequestInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, req, &result)
		if result.Terminate {
			t.Fatalf("unexpected termination: %s", result.ResponseBody)
		}
		var got map[string]any
		if err := json.Unmarshal(result.Body, &got); err != nil {
			t.Fatal(err)
		}
		if got["system"] != promptOriginal {
			t.Fatalf("openai-response top-level system was sanitized: %v", got["system"])
		}
		devContent := got["messages"].([]any)[0].(map[string]any)["content"]
		if devContent != promptOriginal {
			t.Fatalf("openai-response developer message was sanitized: %v", devContent)
		}
	})
}

func TestOMPPromptSanitizationsReconfigureAndPrecedence(t *testing.T) {
	runProtectedPrompt := func(t *testing.T, id, prompt string) string {
		t.Helper()
		root := map[string]any{
			"system": prompt,
			"messages": []any{
				map[string]any{"role": "developer", "content": prompt},
			},
			"tools": []any{},
		}
		var result pluginapi.RequestInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(id, "anthropic", ompMeasurementJSON(t, root)), &result)
		if result.Terminate {
			t.Fatalf("unexpected termination: %s", result.ResponseBody)
		}
		var got map[string]any
		if err := json.Unmarshal(result.Body, &got); err != nil {
			t.Fatal(err)
		}
		sys, _ := got["system"].(string)
		dev, _ := got["messages"].([]any)[0].(map[string]any)["content"].(string)
		if sys != dev {
			t.Fatalf("system (%q) and developer (%q) diverged", sys, dev)
		}
		return sys
	}

	t.Run("null-uses-builtins", func(t *testing.T) {
		isolateOMPMeasurement(t)
		raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
omp_prompt_sanitizations: null
`)))
		if code != 0 {
			t.Fatalf("code = %d, body = %s", code, raw)
		}
		got := runProtectedPrompt(t, "null-builtins", "<system-conventions>RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. <SYSTEM-CONVENTIONS></system-conventions>")
		want := "<conventions>Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. <SYSTEM-CONVENTIONS></conventions>"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("object-override-strip-custom-literal-and-nonrecursive", func(t *testing.T) {
		isolateOMPMeasurement(t)
		raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: false
omp_prompt_sanitizations:
  "rfc 2119 keywords: must, required, should, recommended, may, optional.": "Modal directives ($1/$0/$$): MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."
  "RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.": ""
  "<system-conventions>": "<custom-rules>"
  "</system-conventions>": "</custom-rules>"
  "custom-directive-token": "custom-directive-token-v2"
  "規約ヘッダー": "共通ルール ($1)"
`)))
		if code != 0 {
			t.Fatalf("code = %d, body = %s", code, raw)
		}
		var env map[string]any
		mustUnmarshalJSON(t, raw, &env)
		if env["ok"] != true {
			t.Fatalf("reconfigure failed: %s", raw)
		}

		input := "<system-conventions>\n" +
			"RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
			"RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
			"<SYSTEM-CONVENTIONS>custom-directive-token / 規約ヘッダー</SYSTEM-CONVENTIONS>\n" +
			"</system-conventions>"
		want := "<custom-rules>\n" +
			"Modal directives ($1/$0/$$): MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
			"\n" +
			"<custom-rules>custom-directive-token-v2 / 共通ルール ($1)</custom-rules>\n" +
			"</custom-rules>"
		if got := runProtectedPrompt(t, "object-override", input); got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("longest-match-first-and-tie-break", func(t *testing.T) {
		isolateOMPMeasurement(t)
		raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
omp_prompt_sanitizations:
  "RFC 2119 keywords:": "Short prefix:"
  "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.": "Specific full override."
  "bc": "SECOND"
  "ab": "FIRST"
`)))
		if code != 0 {
			t.Fatalf("code = %d, body = %s", code, raw)
		}
		var env map[string]any
		mustUnmarshalJSON(t, raw, &env)
		if env["ok"] != true {
			t.Fatalf("reconfigure failed: %s", raw)
		}

		input := "RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\nRFC 2119 keywords: EXTRA.\nabc"
		want := "Specific full override.\nShort prefix: EXTRA.\nFIRSTc"
		if got := runProtectedPrompt(t, "longest-match", input); got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("string-and-array-forms", func(t *testing.T) {
		isolateOMPMeasurement(t)
		raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
omp_prompt_sanitizations: |
  strip-me:
  alpha-phrase: beta, gamma
`)))
		if code != 0 {
			t.Fatalf("code = %d, body = %s", code, raw)
		}
		var env map[string]any
		mustUnmarshalJSON(t, raw, &env)
		if env["ok"] != true {
			t.Fatalf("string reconfigure failed: %s", raw)
		}
		got := runProtectedPrompt(t, "string-form", "[strip-me] alpha-phrase | RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.")
		want := "[] beta, gamma | Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."
		if got != want {
			t.Fatalf("string form got %q, want %q", got, want)
		}

		raw, code = handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
omp_prompt_sanitizations:
  - "strip-arr:"
  - "delta-phrase: epsilon"
`)))
		if code != 0 {
			t.Fatalf("code = %d, body = %s", code, raw)
		}
		mustUnmarshalJSON(t, raw, &env)
		if env["ok"] != true {
			t.Fatalf("array reconfigure failed: %s", raw)
		}
		got = runProtectedPrompt(t, "array-form", "[strip-arr] delta-phrase")
		want = "[] epsilon"
		if got != want {
			t.Fatalf("array form got %q, want %q", got, want)
		}
	})
}

func TestSanitizeSystemConventionsZeroAllocsOnMiss(t *testing.T) {
	isolateOMPMeasurement(t)
	cleanText := "Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"- Normative modals: MUST, NEVER, SHOULD, MAY. The RFC 2119 word *is* the instruction.\n" +
		"See system-conventions.md and <conventions>safe</conventions>."
	root := map[string]any{
		"system": cleanText,
		"messages": []any{
			map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": cleanText}}},
			map[string]any{"role": "developer", "content": cleanText},
		},
	}
	allocs := testing.AllocsPerRun(100, func() {
		if sanitizeProtectedSystemConventions(root) {
			t.Fatal("expected no changes on clean prompt")
		}
	})
	if allocs != 0 {
		t.Fatalf("allocs on non-matching prompt = %v, want 0", allocs)
	}
}

func TestSystemConventionsUserAgentOMPRouteGate(t *testing.T) {
	const promptOriginal = "<system-conventions>\n" +
		"RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`.\n" +
		"RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"custom-ua-fingerprint\n" +
		"</system-conventions>"
	const promptSanitized = "<conventions>\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`.\n" +
		"Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\n" +
		"custom-ua-safe\n" +
		"</conventions>"

	configureTestRules := func(t *testing.T, extraYAML string) {
		t.Helper()
		cfgYAML := "use_default_keywords: false\n" +
			"omp_prompt_sanitizations:\n" +
			"  \"custom-ua-fingerprint\": \"custom-ua-safe\"\n" +
			extraYAML
		raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(cfgYAML)))
		if code != 0 {
			t.Fatalf("reconfigure code = %d, body = %s", code, raw)
		}
		var env map[string]any
		mustUnmarshalJSON(t, raw, &env)
		if env["ok"] != true {
			t.Fatalf("reconfigure failed: %s", raw)
		}
	}

	for _, format := range []string{"anthropic", "openai"} {
		for _, route := range []struct {
			name           string
			model          string
			requestedModel string
		}{
			{name: "model-agy", model: "agy/gemini-3.8-flash", requestedModel: "gemini-3.8-flash"},
			{name: "requested-model-agy", model: "gemini-3.8-flash-high", requestedModel: "agy/gemini-3.8-flash"},
		} {
			t.Run("ua-omp-"+format+"-"+route.name, func(t *testing.T) {
				isolateOMPMeasurement(t)
				configureTestRules(t, "")

				textBlock := map[string]any{
					"type":          "text",
					"text":          promptOriginal,
					"cache_control": map[string]any{"type": "ephemeral", "note": promptOriginal},
				}
				inputTextBlock := map[string]any{
					"type": "input_text",
					"text": promptOriginal,
				}
				imageBlock := map[string]any{
					"type": "image",
					"text": promptOriginal,
				}
				// Tool-free and brand-free turn: sanitization alone must trigger serialization.
				root := map[string]any{
					"system": promptOriginal,
					"messages": []any{
						map[string]any{"role": "system", "content": []any{textBlock, inputTextBlock, imageBlock}},
						map[string]any{"role": "developer", "content": promptOriginal},
						map[string]any{"role": "user", "content": promptOriginal},
						map[string]any{"role": "assistant", "content": promptOriginal},
						map[string]any{"role": "tool", "content": promptOriginal},
					},
				}

				req := pluginapi.RequestInterceptRequest{
					RequestID:      "ua-omp-" + format + "-" + route.name,
					SourceFormat:   format,
					Model:          route.model,
					RequestedModel: route.requestedModel,
					Body:           ompMeasurementJSON(t, root),
					Headers:        map[string][]string{"User-Agent": {"omp/18.18.8"}},
				}
				var result pluginapi.RequestInterceptResponse
				ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, req, &result)
				if result.Terminate {
					t.Fatalf("unexpected termination: %s", result.ResponseBody)
				}
				if len(result.Body) == 0 {
					t.Fatalf("expected sanitized body in response, got empty Body")
				}

				root["system"] = promptSanitized
				textBlock["text"] = promptSanitized
				inputTextBlock["text"] = promptSanitized
				root["messages"].([]any)[1].(map[string]any)["content"] = promptSanitized

				var got map[string]any
				if err := json.Unmarshal(result.Body, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, root) {
					t.Fatalf("unexpected transformed request:\ngot  %s\nwant %s", result.Body, ompMeasurementJSON(t, root))
				}
			})
		}
	}

	makeTools := func(format string, names ...string) []any {
		out := make([]any, 0, len(names))
		for _, name := range names {
			if format == "openai" || format == "openai-response" {
				out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": name, "parameters": map[string]any{"type": "object"}}})
			} else {
				out = append(out, map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}})
			}
		}
		return out
	}
	firstToolName := func(format string, got map[string]any) string {
		first := got["tools"].([]any)[0].(map[string]any)
		if format == "openai" || format == "openai-response" {
			return first["function"].(map[string]any)["name"].(string)
		}
		return first["name"].(string)
	}

	t.Run("ua-omp-combined-tools-brand-and-sanitization", func(t *testing.T) {
		isolateOMPMeasurement(t)
		raw, code := handlePluginCall(pluginabi.MethodPluginReconfigure, lifecycleRequestJSON(t, []byte(`
use_default_keywords: true
omp_prompt_sanitizations:
  "custom-ua-fingerprint": "custom-ua-safe"
`)))
		if code != 0 {
			t.Fatalf("reconfigure code = %d, body = %s", code, raw)
		}
		root := map[string]any{
			"system": "<system-conventions>You use Oh My Pi. Use bash tool.\nRFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\ncustom-ua-fingerprint</system-conventions>",
			"messages": []any{
				map[string]any{"role": "developer", "content": "Oh My Pi rule: RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."},
			},
			"tools": makeTools("openai", "bash"),
		}
		var result pluginapi.RequestInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
			RequestID: "ua-omp-combined", SourceFormat: "openai",
			Model: "agy/gemini-3.8-flash", RequestedModel: "agy/gemini-3.8-flash",
			Body: ompMeasurementJSON(t, root), Headers: map[string][]string{"User-Agent": {"omp/18.18.8"}},
		}, &result)
		var got map[string]any
		mustUnmarshalJSON(t, result.Body, &got)
		wantSys := "<conventions>You use Antigravity. Use run_command tool.\nNormative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.\ncustom-ua-safe</conventions>"
		wantDev := "Antigravity rule: Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."
		if got["system"] != wantSys || got["messages"].([]any)[0].(map[string]any)["content"] != wantDev || firstToolName("openai", got) != "run_command" {
			t.Fatalf("unexpected combined output: %s", result.Body)
		}
	})

	for _, tc := range []struct {
		name            string
		extraCfg        string
		format          string
		model           string
		headers         map[string][]string
		msgRole         string
		tools           []string
		wantFirstTool   string // empty means zero body mutation expected
		wantClearHeader bool
	}{
		{
			name:    "non-agy-tool-free",
			format:  "openai",
			model:   "openai/gpt-5",
			headers: map[string][]string{"User-Agent": {"omp/18.18.8"}},
			msgRole: "developer",
		},
		{
			name:          "non-agy-with-omp-tool",
			format:        "openai",
			model:         "openai/gpt-5",
			headers:       map[string][]string{"User-Agent": {"omp/18.18.8"}},
			msgRole:       "developer",
			tools:         []string{"read"},
			wantFirstTool: "view_file",
		},
		{
			name:     "agy-blocked-by-model-prefixes",
			extraCfg: "model_prefixes:\n  - unrelated/\n",
			format:   "openai",
			model:    "agy/gemini-3.8-flash",
			headers:  map[string][]string{"User-Agent": {"omp/18.18.8"}},
			msgRole:  "developer",
		},
		{
			name:            "invalid-explicit-marker-tool-free",
			format:          "openai",
			model:           "agy/gemini-3.8-flash",
			headers:         map[string][]string{"X-Cloak-Client": {"invalid"}, "User-Agent": {"omp/18.18.8"}},
			msgRole:         "developer",
			wantClearHeader: true,
		},
		{
			name:            "invalid-explicit-marker-with-omp-tools",
			format:          "openai",
			model:           "agy/gemini-3.8-flash",
			headers:         map[string][]string{"X-Cloak-Client": {"invalid"}, "User-Agent": {"omp/18.18.8"}},
			msgRole:         "developer",
			tools:           []string{"read", "write", "edit", "bash", "hub"},
			wantFirstTool:   "view_file",
			wantClearHeader: true,
		},
		{
			name:          "unmarked-no-ua-with-omp-tools",
			format:        "anthropic",
			model:         "agy/gemini-3.8-flash",
			msgRole:       "developer",
			tools:         []string{"read", "write", "edit", "bash", "hub"},
			wantFirstTool: "view_file",
		},
		{
			name:            "claude-code-on-agy",
			format:          "anthropic",
			model:           "agy/claude-sonnet-4-6",
			headers:         map[string][]string{"X-Cloak-Client": {"claude_code"}},
			msgRole:         "system",
			tools:           []string{"Bash", "Read"},
			wantFirstTool:   "run_command",
			wantClearHeader: true,
		},
		{
			name:            "codex-on-agy",
			format:          "openai",
			model:           "agy/gemini-3.8-flash",
			headers:         map[string][]string{"X-Cloak-Client": {"codex"}},
			msgRole:         "developer",
			tools:           []string{"exec_command"},
			wantFirstTool:   "run_command",
			wantClearHeader: true,
		},
		{
			name:    "deferred-openai-response-tool-free",
			format:  "openai-response",
			model:   "agy/gemini-3.8-flash",
			headers: map[string][]string{"User-Agent": {"omp/18.18.8"}},
			msgRole: "developer",
		},
		{
			name:          "deferred-openai-response-with-omp-tool",
			format:        "openai-response",
			model:         "agy/gemini-3.8-flash",
			headers:       map[string][]string{"User-Agent": {"omp/18.18.8"}},
			msgRole:       "developer",
			tools:         []string{"read"},
			wantFirstTool: "view_file",
		},
		{
			name:    "unsupported-gemini-format",
			format:  "gemini",
			model:   "agy/gemini-3.8-flash",
			headers: map[string][]string{"User-Agent": {"omp/18.18.8"}},
			msgRole: "developer",
		},
	} {
		t.Run("negative/"+tc.name, func(t *testing.T) {
			isolateOMPMeasurement(t)
			configureTestRules(t, tc.extraCfg)

			root := map[string]any{
				"system": promptOriginal,
				"messages": []any{
					map[string]any{"role": tc.msgRole, "content": promptOriginal},
				},
			}
			if len(tc.tools) > 0 {
				root["tools"] = makeTools(tc.format, tc.tools...)
			}
			var result pluginapi.RequestInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, pluginapi.RequestInterceptRequest{
				RequestID: "neg-" + tc.name, SourceFormat: tc.format,
				Model: tc.model, RequestedModel: tc.model,
				Body: ompMeasurementJSON(t, root), Headers: tc.headers,
			}, &result)
			if result.Terminate {
				t.Fatalf("unexpected rejection: %s", result.ResponseBody)
			}
			if tc.wantClearHeader && !reflect.DeepEqual(result.ClearHeaders, []string{"X-Cloak-Client"}) {
				t.Fatalf("expected X-Cloak-Client cleared, got %v", result.ClearHeaders)
			}
			if tc.wantFirstTool == "" {
				if len(result.Body) != 0 {
					t.Fatalf("expected zero body mutation, got: %s", result.Body)
				}
				return
			}
			var got map[string]any
			mustUnmarshalJSON(t, result.Body, &got)
			if got["system"] != promptOriginal || got["messages"].([]any)[0].(map[string]any)["content"] != promptOriginal {
				t.Fatalf("prompt was unexpectedly sanitized: %s", result.Body)
			}
			if gotTool := firstToolName(tc.format, got); gotTool != tc.wantFirstTool {
				t.Fatalf("first tool = %q, want %q", gotTool, tc.wantFirstTool)
			}
		})
	}
}
