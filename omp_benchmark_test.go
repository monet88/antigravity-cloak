package main

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Serial attribution benchmarks retain results to prevent dead-code removal.
// Concurrent workers never write these sinks.
var (
	ompMeasurementBytes    []byte
	ompMeasurementCount    int
	ompMeasurementMappings []rewriteMapping
)

func ompSizedRequest(tb testing.TB, format string, size int, textHeavy bool) []byte {
	tb.Helper()
	names := []string{"read", "write", "edit", "bash", "grep", "glob", "task", "ask", "web_search"}
	tools := make([]any, 0, len(names))
	for _, name := range names {
		definition := map[string]any{
			"name": name, "description": "Oh My Pi tool " + name + ". Keep /workspace/.omp/agent paths intact. " + strings.Repeat("Tool documentation. ", 4),
		}
		schema := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
		if format == "openai" {
			definition["parameters"] = schema
			tools = append(tools, map[string]any{"type": "function", "function": definition})
		} else {
			definition["input_schema"] = schema
			tools = append(tools, definition)
		}
	}
	root := map[string]any{"tools": tools, "stream": true, "n": 2}
	system := "You are Oh My Pi. Use bash and read. Keep /workspace/.omp/agent and C:\\Users\\agent\\.omp\\agent intact."
	systemMessage := map[string]any{"role": "system", "content": system}
	var messages []any
	if format == "openai" {
		messages = append(messages, systemMessage)
	} else {
		root["system"] = system
	}
	if !textHeavy {
		// Grow history in tool-call/result pairs, rather than padding only one
		// giant string. Setup and marshaling are outside benchmark timing.
		for i := 0; i < (size-4096)/2560; i++ {
			id := fmt.Sprintf("call-%d", i)
			output := strings.Repeat("file output line\n", 120)
			if format == "openai" {
				messages = append(messages,
					map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"text":"pwd"}`}}}},
					map[string]any{"role": "tool", "tool_call_id": id, "name": "bash", "content": output})
			} else {
				messages = append(messages,
					map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "bash", "input": map[string]any{"text": "pwd"}}}},
					map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": output}}})
			}
		}
	}
	user := map[string]any{"role": "user", "content": "Continue inspecting the project. "}
	messages = append(messages, user)
	root["messages"] = messages
	raw := ompMeasurementJSON(tb, root)
	if len(raw) > size {
		tb.Fatalf("fixture exceeds %d bytes: %d", size, len(raw))
	}
	padding := strings.Repeat("h", size-len(raw))
	if textHeavy {
		if format == "openai" {
			systemMessage["content"] = system + padding
		} else {
			root["system"] = system + padding
		}
	} else {
		user["content"] = user["content"].(string) + padding
	}
	return ompMeasurementJSON(tb, root)
}

func ompTextEvent(tb testing.TB, format string, size int) ([]byte, string) {
	tb.Helper()
	var prefix, suffix string
	if format == "openai" {
		prefix = `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Antigravity `
		suffix = `."}}]}` + "\n\n"
	} else {
		prefix = `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Antigravity `
		suffix = `."}}` + "\n\n"
	}
	padding := strings.Repeat("x", size-len(prefix)-len(suffix))
	return []byte(prefix + padding + suffix), "omp " + padding + "."
}

func prepareOMPMeasurementStream(tb testing.TB, id, format string) *pluginapi.StreamChunkInterceptRequest {
	tb.Helper()
	body := []byte(`{"messages":[],"tools":[{"type":"function","function":{"name":"bash"}}]}`)
	if format == "anthropic" {
		body = []byte(`{"messages":[],"tools":[{"name":"bash","input_schema":{"type":"object"}}]}`)
	}
	admitOMPMeasurement(tb, id, format, body)
	req := &pluginapi.StreamChunkInterceptRequest{RequestID: id, SourceFormat: format, Model: "agy/measurement", ChunkIndex: pluginapi.StreamChunkHeaderInitIndex}
	globalStreamManager.processChunk(req, format)
	req.ChunkIndex = 1
	return req
}

func checkOMPMeasurementEvent(tb testing.TB, req *pluginapi.StreamChunkInterceptRequest, event []byte, fragment int, want string) []byte {
	tb.Helper()
	var wire []byte
	for offset := 0; offset < len(event); offset += fragment {
		end := min(offset+fragment, len(event))
		req.Body = event[offset:end]
		result := globalStreamManager.processChunk(req, req.SourceFormat)
		wire = appendOMPMeasurementWire(wire, req.Body, result)
	}
	if bytes.Count(wire, []byte("data:")) != 1 || !bytes.HasSuffix(wire, []byte("\n\n")) {
		tb.Fatalf("expected exactly one complete frame, got %d bytes", len(wire))
	}
	var root map[string]any
	if err := safeUnmarshal(bytes.TrimPrefix(wire, []byte("data: ")), &root); err != nil {
		tb.Fatal(err)
	}
	var got string
	if req.SourceFormat == "openai" {
		got = root["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"].(string)
	} else {
		got = root["delta"].(map[string]any)["text"].(string)
	}
	if got != want {
		tb.Fatalf("rewritten text differs: got length %d, want %d", len(got), len(want))
	}
	return wire
}

func TestOMPMeasurementFixtures(t *testing.T) {
	isolateOMPMeasurement(t)
	for _, format := range []string{"openai", "anthropic"} {
		for _, size := range []int{8 << 10, 64 << 10, 512 << 10} {
			for _, textHeavy := range []bool{false, true} {
				body := ompSizedRequest(t, format, size, textHeavy)
				if len(body) != size {
					t.Fatalf("size=%d, want=%d", len(body), size)
				}
				admitted := admitOMPMeasurement(t, "fixture", format, body)
				route := globalLifecycleManager.getRoute("fixture")
				if len(route.activeReverse) != 9 || route.activeReverse["run_command"] != "bash" || route.expected != 2 || !route.cachedUncloak.exactOnly {
					t.Fatal("fixture lost canonical request authority or choice count")
				}
				if !bytes.Contains(admitted.Body, []byte("Antigravity")) || !bytes.Contains(admitted.Body, []byte(".omp/agent")) {
					t.Fatalf("fixture lost brand rewrite or preserved path: format=%s size=%d textHeavy=%t", format, size, textHeavy)
				}
			}
		}
		for _, size := range []int{1024, 64 << 10} {
			for _, fragment := range []int{size, 256, 64} {
				req := prepareOMPMeasurementStream(t, fmt.Sprintf("event-%s-%d-%d", format, size, fragment), format)
				event, want := ompTextEvent(t, format, size)
				checkOMPMeasurementEvent(t, req, event, fragment, want)
			}
		}
	}
}

func BenchmarkOMPAdmission(b *testing.B) {
	for _, format := range []string{"openai", "anthropic"} {
		for _, size := range []int{8 << 10, 64 << 10, 512 << 10} {
			layouts := []string{"history"}
			if size == 64<<10 {
				layouts = append(layouts, "system")
			}
			for _, layout := range layouts {
				b.Run(fmt.Sprintf("%s/%dKiB/%s", format, size>>10, layout), func(b *testing.B) {
					isolateOMPMeasurement(b)
					body := ompSizedRequest(b, format, size, layout == "system")
					admitOMPMeasurement(b, "admission", format, body)
					payload := ompMeasurementJSON(b, ompMeasurementRequest("admission", format, body))
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						// Replacing one completed admission's route keeps cardinality
						// constant. request.complete is deliberately not timed here.
						ompMeasurementBytes, _ = handlePluginCall(pluginabi.MethodRequestInterceptBefore, payload)
					}
				})
			}
		}
	}
}

func BenchmarkOMPChoiceCount(b *testing.B) {
	for _, size := range []int{8 << 10, 64 << 10, 512 << 10} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			isolateOMPMeasurement(b)
			body := admitOMPMeasurement(b, "choice", "openai", ompSizedRequest(b, "openai", size, false)).Body
			root, ok := decodeStrictProtectedJSON(body)
			if !ok || requestChoiceCount(body) != 2 {
				b.Fatal("invalid choice-count fixture")
			}
			b.Run("Reparse", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					ompMeasurementCount = requestChoiceCount(body)
				}
			})
			// Attribution control, not an optimized production implementation:
			// measures reading n when the decoded root already exists.
			b.Run("ExistingRoot", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					n, valid := jsonIndexValue(root["n"])
					if !valid || n <= 1 {
						n = 1
					}
					ompMeasurementCount = n
				}
			})
		})
	}
}

func BenchmarkOMPBrandDerivation(b *testing.B) {
	for _, custom := range []int{0, 32} {
		b.Run(fmt.Sprintf("custom%d", custom), func(b *testing.B) {
			cfg := defaultFilterConfig()
			for i := 0; i < custom; i++ {
				cfg.CustomMappings = append(cfg.CustomMappings, rewriteMapping{Match: fmt.Sprintf("custom-brand-%d", i), Replacement: "replacement"})
			}
			b.Run("DeriveOnly", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					// Attribution probe of main.go's rewriteProtectedBrandText
					// mapping-derivation block (680-693 at 1428005). Keep this
					// probe in sync if that production block changes.
					var mappings []rewriteMapping
					if cfg.UseDefaultKeywords {
						for _, m := range defaultRewriteMappings {
							if !isOMPAlias(m.Match) {
								mappings = append(mappings, m)
							}
						}
					}
					for _, m := range cfg.CustomMappings {
						if !isOMPAlias(m.Match) {
							mappings = append(mappings, m)
						}
					}
					ompMeasurementMappings = normalizeMappings(mappings)
				}
			})
			for _, size := range []int{64, 16 << 10} {
				text := "Agent documentation " + strings.Repeat("x", size-20)
				b.Run(fmt.Sprintf("RewriteNoMatch/%dB", size), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						result, _ := rewriteProtectedBrandText(text, &cfg)
						runtime.KeepAlive(result)
					}
				})
			}
		})
	}
}

func benchmarkOMPStreams(b *testing.B, retained, workers int) {
	isolateOMPMeasurement(b)
	requests := make([]*pluginapi.StreamChunkInterceptRequest, retained)
	event, want := ompTextEvent(b, "openai", 1024)
	var expectedWire []byte
	for i := range requests {
		requests[i] = prepareOMPMeasurementStream(b, fmt.Sprintf("stream-%d", i), "openai")
		expectedWire = checkOMPMeasurementEvent(b, requests[i], event, len(event), want)
		requests[i].Body = event
	}
	lastResults := make([]pluginapi.StreamChunkInterceptResponse, workers)
	b.ReportAllocs()
	b.SetBytes(int64(len(event)))
	b.ResetTimer()
	if workers == 1 {
		for i := 0; i < b.N; i++ {
			lastResults[0] = globalStreamManager.processChunk(requests[0], "openai")
		}
	} else {
		var done sync.WaitGroup
		start := make(chan struct{})
		for worker := 0; worker < workers; worker++ {
			n := b.N / workers
			if worker < b.N%workers {
				n++
			}
			done.Add(1)
			go func(worker int, req *pluginapi.StreamChunkInterceptRequest, n int) {
				defer done.Done()
				<-start
				var last pluginapi.StreamChunkInterceptResponse
				for i := 0; i < n; i++ {
					last = globalStreamManager.processChunk(req, "openai")
				}
				lastResults[worker] = last
			}(worker, requests[worker], n)
		}
		close(start)
		done.Wait()
	}
	b.StopTimer()
	for worker, last := range lastResults {
		if worker < b.N && (last.DropChunk || !bytes.Equal(last.Body, expectedWire)) {
			b.Fatalf("worker %d stopped producing the validated wire output", worker)
		}
	}
	if len(globalStreamManager.sessions) != retained || len(globalLifecycleManager.routes) != retained {
		b.Fatal("retained session or route count changed during measurement")
	}
	b.ReportMetric(float64(retained), "sessions")
	b.ReportMetric(float64(workers), "workers")
}

func BenchmarkOMPSessionSweep(b *testing.B) {
	for _, retained := range []int{1, 32, 256} {
		b.Run(fmt.Sprintf("retained%d", retained), func(b *testing.B) { benchmarkOMPStreams(b, retained, 1) })
	}
}

func BenchmarkOMPConcurrentStreams(b *testing.B) {
	for _, workers := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("workers%d", workers), func(b *testing.B) { benchmarkOMPStreams(b, 64, workers) })
	}
}

func BenchmarkOMPSSE(b *testing.B) {
	for _, format := range []string{"openai", "anthropic"} {
		for _, size := range []int{1024, 64 << 10} {
			fragments := []int{size, 256}
			if size == 64<<10 && format == "openai" {
				fragments = append(fragments, 64)
			}
			for _, fragment := range fragments {
				b.Run(fmt.Sprintf("%s/%dKiB/fragment%d", format, size>>10, fragment), func(b *testing.B) {
					isolateOMPMeasurement(b)
					req := prepareOMPMeasurementStream(b, "fragment", format)
					event, want := ompTextEvent(b, format, size)
					expectedWire := checkOMPMeasurementEvent(b, req, event, fragment, want)
					var last pluginapi.StreamChunkInterceptResponse
					b.ReportAllocs()
					b.SetBytes(int64(len(event)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						for offset := 0; offset < len(event); offset += fragment {
							req.Body = event[offset:min(offset+fragment, len(event))]
							last = globalStreamManager.processChunk(req, format)
						}
					}
					b.StopTimer()
					if last.DropChunk || !bytes.Equal(last.Body, expectedWire) || len(globalStreamManager.sessions) != 1 {
						b.Fatal("stateful replay stopped producing the validated wire output")
					}
					b.ReportMetric(float64((size+fragment-1)/fragment), "chunks/event")
				})
			}
		}
	}
}
