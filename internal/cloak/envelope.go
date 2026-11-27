package cloak

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func decodeStrictProtectedJSON(data []byte) (map[string]any, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, false
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return rootMap, true
}

// sliceOfMaps returns the []any members of raw that are objects, so a
// malformed or absent field is skipped instead of panicking.
func sliceOfMaps(raw any) []map[string]any {
	items, _ := raw.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func sliceOfAny(raw any) []any {
	out, _ := raw.([]any)
	return out
}

// jsonIndexValue reads an integer lane index from a safeUnmarshal-produced
// value (UseNumber guarantees json.Number for every JSON number).
func jsonIndexValue(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

func MustEnvelope(result any) []byte {
	raw, err := json.Marshal(pluginabi.Envelope{OK: true, Result: mustRawMessage(result)})
	if err != nil {
		return MustErrorEnvelope("marshal_error", err.Error())
	}
	return raw
}

func MustErrorEnvelope(code, message string) []byte {
	raw, err := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"marshal_error","message":"failed to encode plugin response"}}`)
	}
	return raw
}

func mustRawMessage(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// safeUnmarshal decodes JSON preserving number precision by using json.Number
// instead of float64 for all numeric values.
func safeUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// safeMarshal encodes JSON without escaping HTML characters (<, >, &) to their
// unicode equivalents (\u003c, \u003e, \u0026), preserving raw text fidelity.
func safeMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder.Encode appends a trailing newline; strip it.
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}
