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
				"default": "Claude",
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
				"example": "Claude",
			},
		},
	}
	before, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	if !rewriteSchemaWithClaudeBrand(schema) {
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
		// A bare string under default/example is protected by the
		// isSchemaTextField allowlist and nothing else, so it has to carry a
		// spelling the forward pass WOULD rewrite: with "Antigravity" here the
		// row holds even if that allowlist is widened to the literal keywords.
		`"default":"Claude"`,
		`"default":{"description":"Tuned by Claude.","note":"Claude tuned"}`,
		`"examples":[{"description":"Built by Claude.","note":"Claude tuned"}]`,
		`"example":"Claude"`,
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
	if !rewriteSchemaWithClaudeBrand(schema) {
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
	if !rewriteSchemaWithClaudeBrand(schema) {
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

// TestIssue50_CollidingEntryNamesStillGetCloaked covers the other side of the
// literal guard: inside a dictionary-of-schemas container the keys are entry
// names, not keywords, so an entry spelled exactly like a literal keyword still
// has its own subschema walked. Applying the guard at every map level skipped
// the whole entry, and its description reached the model with the client's own
// brand word.
func TestIssue50_CollidingEntryNamesStillGetCloaked(t *testing.T) {
	sub := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"const":    sub("Claude const prop."),
			"enum":     sub("Claude enum prop."),
			"default":  sub("Claude default prop."),
			"example":  sub("Claude example prop."),
			"examples": sub("Claude examples prop."),
		},
		"$defs": map[string]any{
			"default": sub("Claude defs default."),
		},
		"definitions": map[string]any{
			"examples": sub("Claude definitions examples."),
		},
		"patternProperties": map[string]any{
			"enum": sub("Claude pattern enum."),
		},
		"dependentSchemas": map[string]any{
			"const": sub("Claude dependent const."),
		},
	}
	if !rewriteSchemaWithClaudeBrand(schema) {
		t.Fatal("colliding entry descriptions were expected to be rewritten")
	}
	got, _ := json.Marshal(schema)
	for _, want := range []string{
		`"description":"Antigravity const prop."`,
		`"description":"Antigravity enum prop."`,
		`"description":"Antigravity default prop."`,
		`"description":"Antigravity example prop."`,
		`"description":"Antigravity examples prop."`,
		`"description":"Antigravity defs default."`,
		`"description":"Antigravity definitions examples."`,
		`"description":"Antigravity pattern enum."`,
		`"description":"Antigravity dependent const."`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("colliding entry schema was skipped, want %s in %s", want, got)
		}
	}
	// The colliding names are structural, so they stay verbatim.
	for _, keep := range []string{`"default":`, `"examples":`, `"dependentSchemas":`} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("structural entry name was lost: %s", got)
		}
	}
}

// TestIssue50_LiteralsUnderCollidingEntriesStayUntouched pins the boundary of
// the fix: a schema reached through a colliding entry name is still a schema
// node, so its own literal keywords hold data and must stay byte-for-byte.
func TestIssue50_LiteralsUnderCollidingEntriesStayUntouched(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"default": map[string]any{
				"type":        "object",
				"enum":        []any{"Claude", "Codex"},
				"const":       "Claude",
				"examples":    []any{map[string]any{"note": "Claude tuned"}},
				"description": "Claude-tuned entry.",
			},
		},
	}
	if !rewriteSchemaWithClaudeBrand(schema) {
		t.Fatal("the entry's own description was expected to be rewritten")
	}
	got, _ := json.Marshal(schema)
	for _, want := range []string{
		`"enum":["Claude","Codex"]`,
		`"const":"Claude"`,
		`"examples":[{"note":"Claude tuned"}]`,
		`"description":"Antigravity-tuned entry."`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("want %s in %s", want, got)
		}
	}
}

// rewriteSchemaWithClaudeBrand runs the descriptive-text pass the request path
// applies to a tool's JSON Schema - brand mappings and request-scoped tool
// aliases - bound to the claude_code brand table, and reports whether anything
// changed.
func rewriteSchemaWithClaudeBrand(schema any) bool {
	return rewriteSchemaTextFields(schema, func(s string) (string, bool) {
		return rewriteDescriptiveText(s, claudeCodeBrandMappings, nil)
	})
}
