package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestProtectedOMPEscapedCanonicalRoundTrip(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-escaped-roundtrip"

	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"function":{"name":"_bash","arguments":"{}"}}]},
			{"role":"tool","name":"Functions:_read","content":"ok"}
		],
		"tools":[
			{"type":"function","function":{"name":"Functions:_read"}},
			{"type":"function","function":{"name":"_write"}},
			{"type":"function","function":{"name":"_edit"}},
			{"type":"function","function":{"name":"_bash"}}
		],
		"tool_choice":{"type":"function","function":{"name":"_edit"}}
	}`)

	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("escaped canonical request rejected: %s", admitted.ResponseBody)
	}
	for _, want := range []string{
		`"name":"Functions:view_file"`,
		`"name":"write_to_file"`,
		`"name":"replace_file_content"`,
		`"name":"run_command"`,
	} {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("cloaked request missing %s: %s", want, admitted.Body)
		}
	}
	if bytes.Contains(admitted.Body, []byte(`"name":"_bash"`)) ||
		bytes.Contains(admitted.Body, []byte(`"name":"_edit"`)) ||
		bytes.Contains(admitted.Body, []byte(`"name":"Functions:_read"`)) {
		t.Fatalf("escaped canonical identity leaked upstream: %s", admitted.Body)
	}

	route := globalLifecycleManager.getRoute(requestID)
	if route == nil {
		t.Fatal("ProtectedAGY route not pinned")
	}
	if got := route.activeReverse["Functions:view_file"]; got != "Functions:_read" {
		t.Fatalf("active reverse lost exact namespace/escape: got %q", got)
	}
	if got := route.activeReverse["run_command"]; got != "_bash" {
		t.Fatalf("active reverse lost escape: got %q", got)
	}

	var nonstream pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: "agy/measurement",
		Body: []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Functions:view_file","arguments":"{}"}},{"function":{"name":"run_command","arguments":"{}"}}]}}]}`),
	}, &nonstream)
	if !bytes.Contains(nonstream.Body, []byte(`"name":"Functions:_read"`)) ||
		!bytes.Contains(nonstream.Body, []byte(`"name":"_bash"`)) {
		t.Fatalf("non-stream response did not restore exact source spellings: %s", nonstream.Body)
	}
}

func TestProtectedOMPEscapedCanonicalFragmentedStream(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-escaped-fragmented"
	admitOMPMeasurement(t, requestID, "openai", []byte(`{
		"messages":[],
		"tools":[{"type":"function","function":{"name":"_bash"}}]
	}`))

	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"run_command\",\"arguments\":\"{}\"}}]}}]}\n\n")
	cut := bytes.Index(frame, []byte("run_command")) + len("run_")
	parts := [][]byte{frame[:cut], frame[cut:]}
	var wire []byte
	for i, part := range parts {
		var result pluginapi.StreamChunkInterceptResponse
		ompMeasurementCall(t, pluginabi.MethodResponseInterceptStreamChunk, pluginapi.StreamChunkInterceptRequest{
			RequestID: requestID, SourceFormat: "openai", Model: "agy/measurement", ChunkIndex: i, Body: part,
		}, &result)
		wire = appendOMPMeasurementWire(wire, part, result)
	}
	if !bytes.Contains(wire, []byte(`"name":"_bash"`)) {
		t.Fatalf("fragmented stream did not restore escaped source: %s", wire)
	}
	if bytes.Contains(wire, []byte("run_command")) {
		t.Fatalf("fragmented stream leaked canonical target: %s", wire)
	}
}

func TestProtectedOMPEscapedCanonicalAnthropicCarriers(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-escaped-anthropic"
	body := []byte("{\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"name\":\"_bash\",\"input\":{}}]}],\"tools\":[{\"name\":\"_read\",\"input_schema\":{\"type\":\"object\"}}],\"tool_choice\":{\"type\":\"tool\",\"name\":\"_read\"}}")
	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "anthropic", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("escaped anthropic request rejected: %s", admitted.ResponseBody)
	}
	for _, want := range []string{"\"name\":\"view_file\"", "\"name\":\"run_command\""} {
		if !bytes.Contains(admitted.Body, []byte(want)) {
			t.Fatalf("anthropic carrier missing %s: %s", want, admitted.Body)
		}
	}

	var response pluginapi.ResponseInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodResponseInterceptAfter, pluginapi.ResponseInterceptRequest{
		RequestID: requestID, SourceFormat: "anthropic", Model: "agy/measurement",
		Body: []byte("{\"content\":[{\"type\":\"tool_use\",\"name\":\"view_file\",\"input\":{}}]}"),
	}, &response)
	if !bytes.Contains(response.Body, []byte("\"name\":\"_read\"")) {
		t.Fatalf("anthropic response did not restore escaped source: %s", response.Body)
	}
}

func TestProtectedOMPEscapeRecognitionIsFinite(t *testing.T) {
	isolateOMPMeasurement(t)
	const requestID = "omp-escaped-finite"
	body := []byte(`{
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"_read"}},
			{"type":"function","function":{"name":"_foo"}},
			{"type":"function","function":{"name":"__read"}},
			{"type":"function","function":{"name":":_read"}}
		]
	}`)
	var admitted pluginapi.RequestInterceptResponse
	ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &admitted)
	if admitted.Terminate {
		t.Fatalf("request rejected: %s", admitted.ResponseBody)
	}
	if !bytes.Contains(admitted.Body, []byte(`"name":"view_file"`)) {
		t.Fatalf("verified escaped builtin was not canonicalized: %s", admitted.Body)
	}
	for _, exact := range []string{`"name":"_foo"`, `"name":"__read"`, `"name":":_read"`} {
		if !bytes.Contains(admitted.Body, []byte(exact)) {
			t.Fatalf("unknown escaped identity was silently canonicalized: want %s in %s", exact, admitted.Body)
		}
	}
}

func TestProtectedOMPEscapedCanonicalCollisionsReject(t *testing.T) {
	cases := map[string][]string{
		"bare-plus-escaped":             {"read", "_read"},
		"escaped-plus-native-target":    {"_read", "view_file"},
		"cross-namespace-native-target": {"functions:_read", "default_api:view_file"},
	}
	for name, names := range cases {
		t.Run(name, func(t *testing.T) {
			isolateOMPMeasurement(t)
			var tools strings.Builder
			for i, toolName := range names {
				if i > 0 {
					tools.WriteByte(',')
				}
				tools.WriteString(`{"type":"function","function":{"name":"`)
				tools.WriteString(toolName)
				tools.WriteString(`"}}`)
			}
			body := []byte(`{"messages":[],"tools":[` + tools.String() + `]}`)
			requestID := "omp-escaped-collision-" + name
			var result pluginapi.RequestInterceptResponse
			ompMeasurementCall(t, pluginabi.MethodRequestInterceptBefore, ompMeasurementRequest(requestID, "openai", body), &result)
			if !result.Terminate || result.StatusCode != 503 {
				t.Fatalf("collision must reject with 503: terminate=%t status=%d body=%s", result.Terminate, result.StatusCode, result.ResponseBody)
			}
			const exact = `{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}`
			if string(result.ResponseBody) != exact {
				t.Fatalf("wrong rejection body: %s", result.ResponseBody)
			}
			if route := globalLifecycleManager.getRoute(requestID); route != nil {
				t.Fatal("rejected request must not pin ProtectedAGY route")
			}
		})
	}
}
