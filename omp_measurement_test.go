package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Measurement fixtures own fresh globals for their lifetime. Do not use
// t.Parallel: concurrent benchmarks share these managers deliberately, while
// each worker owns a distinct RequestID and processes its chunks sequentially.
func isolateOMPMeasurement(tb testing.TB) {
	tb.Helper()
	if os.Getenv("CPA_FILTER_DEBUG") != "" {
		tb.Fatal("run measurements with CPA_FILTER_DEBUG unset")
	}
	previousConfig := globalFilterConfig.Load()
	previousRoutes, previousStreams := globalLifecycleManager, globalStreamManager
	applyFilterConfig(defaultFilterConfig())
	globalLifecycleManager = newExplicitOMPLifecycleManager()
	globalStreamManager = newStreamSessionManager()
	tb.Cleanup(func() {
		globalFilterConfig.Store(previousConfig)
		globalLifecycleManager, globalStreamManager = previousRoutes, previousStreams
	})
}

func ompMeasurementJSON(tb testing.TB, value any) []byte {
	tb.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

func ompMeasurementCall(tb testing.TB, method string, request any, result any) {
	tb.Helper()
	raw, code := handlePluginCall(method, ompMeasurementJSON(tb, request))
	var envelope struct {
		OK     bool
		Result json.RawMessage
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || code != 0 || !envelope.OK {
		tb.Fatalf("%s failed: code=%d envelope=%s err=%v", method, code, raw, err)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		tb.Fatal(err)
	}
}

func ompMeasurementRequest(id, format string, body []byte) pluginapi.RequestInterceptRequest {
	return pluginapi.RequestInterceptRequest{
		RequestID: id, SourceFormat: format,
		Model: "agy/measurement", RequestedModel: "agy/measurement", Body: body,
		Headers: http.Header{"X-Cloak-Client": {"oh_my_pi"}},
	}
}

func admitOMPMeasurement(tb testing.TB, id, format string, body []byte) pluginapi.RequestInterceptResponse {
	tb.Helper()
	var result pluginapi.RequestInterceptResponse
	ompMeasurementCall(tb, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(id, format, body), &result)
	if result.Terminate || len(result.Body) == 0 {
		tb.Fatalf("Protected admission failed: terminate=%t body=%s", result.Terminate, result.ResponseBody)
	}
	route := globalLifecycleManager.getRoute(id)
	if route == nil || route.routeKind != routeKindProtectedAGY {
		tb.Fatal("fixture did not exercise ProtectedAGY")
	}
	return result
}

// Host semantics: DropChunk suppresses input; a nonempty replacement replaces
// it; an empty response forwards the original input. Compare reconstructed wire
// bytes, not just the plugin's replacement field.
func appendOMPMeasurementWire(dst, input []byte, result pluginapi.StreamChunkInterceptResponse) []byte {
	if result.DropChunk {
		return dst
	}
	if len(result.Body) > 0 {
		return append(dst, result.Body...)
	}
	return append(dst, input...)
}

func TestOMPMeasurementFragmentedNoChange(t *testing.T) {
	for _, ending := range []struct{ name, bytes string }{{"LF", "\n\n"}, {"CRLF", "\r\n\r\n"}} {
		for _, fragmented := range []bool{false, true} {
			mode := "intact"
			if fragmented {
				mode = "fragmented"
			}
			t.Run(ending.name+"/"+mode, func(t *testing.T) {
				isolateOMPMeasurement(t)
				const id = "measurement-no-change"
				admitOMPMeasurement(t, id, "openai", []byte(`{"messages":[],"tools":[{"type":"function","function":{"name":"bash"}}]}`))
				frame := []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"hello world."}}]}` + ending.bytes)
				parts := [][]byte{frame}
				if fragmented {
					cut := bytes.Index(frame, []byte("world"))
					parts = [][]byte{frame[:cut], frame[cut : len(frame)-len(ending.bytes)], frame[len(frame)-len(ending.bytes):]}
				}
				var wire []byte
				for i, part := range parts {
					var result pluginapi.StreamChunkInterceptResponse
					ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
						RequestID: id, SourceFormat: "openai", Model: "agy/measurement", ChunkIndex: i, Body: part,
					}, &result)
					if !fragmented && (result.DropChunk || len(result.Body) != 0) {
						t.Error("intact no-change event should use host passthrough")
					}
					if fragmented && i < len(parts)-1 && !result.DropChunk {
						t.Errorf("incomplete part %d was not held", i)
					}
					wire = appendOMPMeasurementWire(wire, part, result)
				}
				if !bytes.Equal(wire, frame) {
					t.Errorf("no-change SSE lost bytes: got %d bytes %q; want %d bytes %q", len(wire), wire, len(frame), frame)
				}
			})
		}
	}
}

func TestOMPMeasurementCompleteEventWithPartialNext(t *testing.T) {
	for _, ending := range []struct{ name, bytes string }{{"LF", "\n\n"}, {"CRLF", "\r\n\r\n"}} {
		t.Run(ending.name, func(t *testing.T) {
			isolateOMPMeasurement(t)
			const id = "measurement-complete-plus-partial"
			const otherID = "measurement-other-request"
			body := []byte(`{"messages":[],"tools":[{"type":"function","function":{"name":"bash"}}]}`)
			admitOMPMeasurement(t, id, "openai", body)
			admitOMPMeasurement(t, otherID, "openai", body)
			first := []byte(`data: {"choices":[{"index":0,"delta":{"content":"first event."}}]}` + ending.bytes)
			second := []byte(`data: {"choices":[{"index":0,"delta":{"content":"second event."}}]}` + ending.bytes)
			other := []byte(`data: {"choices":[{"index":0,"delta":{"content":"other request."}}]}` + ending.bytes)
			cut := bytes.Index(second, []byte("event"))
			chunks := [][]byte{append(append([]byte(nil), first...), second[:cut]...), second[cut:]}
			wantEvents := [][]byte{first, second}
			var wire []byte
			for i, chunk := range chunks {
				var result pluginapi.StreamChunkInterceptResponse
				ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
					RequestID: id, SourceFormat: "openai", Model: "agy/measurement", ChunkIndex: i, Body: chunk,
				}, &result)
				emitted := appendOMPMeasurementWire(nil, chunk, result)
				if !bytes.Equal(emitted, wantEvents[i]) {
					t.Errorf("chunk %d: emitted %q, want exactly %q", i, emitted, wantEvents[i])
				}
				wire = append(wire, emitted...)
				if i == 0 {
					var otherResult pluginapi.StreamChunkInterceptResponse
					ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
						RequestID: otherID, SourceFormat: "openai", Model: "agy/measurement", ChunkIndex: 0, Body: other,
					}, &otherResult)
					if otherResult.DropChunk || len(otherResult.Body) != 0 {
						t.Error("another request's buffered tail must not affect intact passthrough")
					}
				}
			}
			wantWire := append(append([]byte(nil), first...), second...)
			if !bytes.Equal(wire, wantWire) {
				t.Errorf("reassembled events lost or duplicated bytes: got %q, want %q", wire, wantWire)
			}
		})
	}
}
