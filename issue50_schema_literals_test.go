package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Ticket #50: a JSON Schema is the contract the model's arguments have to
// satisfy. Descriptive help text is cloaked; a validated constant is not.
//
// The fixture uses literal subtrees that THEMSELVES carry a description or
// title spelled the way the FORWARD pass would rewrite it, which is the only
// shape the guard can reach: a bare string under const/enum was already left
// alone by the isSchemaTextField check, so a simpler fixture would pass with
// the guard deleted.

func TestIssue50_LiteralDescendantsAreNeverRewritten(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"description": "Use Claude mode for reviews.",
				"enum":        []any{"Claude", "Codex", "plain"},
			},
			"kind": map[string]any{
				"const":       "Claude",
				"description": "Always Claude.",
			},
			"target": map[string]any{
				"type":    "string",
				"default": "Antigravity",
			},
			"tuning": map[string]any{
				"type":    "object",
				"default": map[string]any{"description": "Tuned by Claude.", "note": "Claude tuned"},
				"examples": []any{
					map[string]any{"description": "Built by Claude.", "note": "Claude tuned"},
				},
			},
			"legacy": map[string]any{
				"type":    "string",
				"example": "Antigravity",
			},
		},
	}
	before, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	if _, changed := rewriteSchemaText(schema, claudeCodeBrandMappings); !changed {
		t.Fatal("descriptions were expected to be rewritten")
	}

	// The literals survived verbatim.
	got, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"enum":["Claude","Codex","plain"]`,
		`"const":"Claude"`,
		`"default":"Antigravity"`,
		`"default":{"description":"Tuned by Claude.","note":"Claude tuned"}`,
		`"examples":[{"description":"Built by Claude.","note":"Claude tuned"}]`,
		`"example":"Antigravity"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("literal %s was rewritten: %s", want, got)
		}
	}
	// The description next to a literal was still cloaked.
	if !strings.Contains(string(got), `"description":"Use Antigravity mode for reviews."`) {
		t.Errorf("descriptive text was not cloaked: %s", got)
	}
	if !strings.Contains(string(got), `"description":"Always Antigravity."`) {
		t.Errorf("description beside a const was not cloaked: %s", got)
	}
	// The structural property name and the required list keep the client
	// spelling, so the schema is still satisfiable.
	if !strings.Contains(string(got), `"mode"`) || !strings.Contains(string(got), `"kind"`) {
		t.Errorf("property names were rewritten: %s", got)
	}
	if string(before) == string(got) {
		t.Error("nothing changed at all, the fixture proves nothing")
	}
}

// TestIssue50_NestedSchemasAreStillTraversed proves the literal guard did not
// cost any real traversal: a description buried four schema keywords deep is
// still cloaked, and a schema nested inside items/$defs/allOf is reached.
func TestIssue50_NestedSchemasAreStillTraversed(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rows": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"who": map[string]any{"type": "string", "description": "Written by Claude."},
					},
				},
			},
		},
		"$defs": map[string]any{
			"shared": map[string]any{"type": "string", "description": "Shared by Codex."},
		},
		"allOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{
				"deep": map[string]any{"type": "string", "description": "Deep Claude rule."},
			}},
		},
	}
	if _, changed := rewriteSchemaText(schema, claudeCodeBrandMappings); !changed {
		t.Fatal("nested descriptions were expected to be rewritten")
	}
	got, _ := json.Marshal(schema)
	for _, want := range []string{
		`"description":"Written by Antigravity."`,
		`"description":"Shared by Codex."`,
		`"description":"Deep Antigravity rule."`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("nested traversal lost %s: %s", want, got)
		}
	}
}

// TestIssue50_RequestedAndPatternPropertiesAreNeverRewritten covers the
// structural arrays: they are not literal keywords, but their members are
// names and regular expressions, never help text.
func TestIssue50_RequestedAndPatternPropertiesAreNeverRewritten(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"required":             []any{"Claude", "Antigravity"},
		"patternProperties":    map[string]any{"^Claude": map[string]any{"type": "string", "description": "Matched by Claude."}},
		"propertyNames":        map[string]any{"description": "Name of the Claude field."},
		"dependentSchemas":     map[string]any{"Claude": map[string]any{"type": "object"}},
		"additionalProperties": map[string]any{"type": "string", "description": "Extra Claude value."},
	}
	if _, changed := rewriteSchemaText(schema, claudeCodeBrandMappings); !changed {
		t.Fatal("descriptions were expected to be rewritten")
	}
	got, _ := json.Marshal(schema)
	if !strings.Contains(string(got), `"required":["Claude","Antigravity"]`) {
		t.Errorf("required members were rewritten: %s", got)
	}
	if !strings.Contains(string(got), `"^Claude"`) {
		t.Errorf("pattern was rewritten: %s", got)
	}
	for _, want := range []string{
		`"description":"Matched by Antigravity."`,
		`"description":"Name of the Antigravity field."`,
		`"description":"Extra Antigravity value."`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("real schema under a structural keyword was not reached: %s", got)
		}
	}
}
