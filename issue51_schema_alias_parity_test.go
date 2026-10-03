package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Ticket #51: a tool's JSON Schema is machine-generated prose too. A declared
// tool named in a nested description or title has to reach the model under the
// same request-scoped alias the declaration uses - in both carriers, for the
// canonical tools and for the Oh My Pi shared/fallback aliases.
//
// Ticket #50 owns the other half of the same walk: only the descriptive fields
// (description/title) may be rewritten. Every structural or literal field -
// property names, required, enum, const, default, examples - keeps the client's
// spelling byte-for-byte, or the schema can no longer be satisfied. The fixture
// below carries those tokens under the literal-bearing keywords so a regression
// that descends into them fails here.

// nestedProseSchema is the schema half of the fixture. Its descriptive fields
// name declared tools in tool-reference context ("Call Read", "use Edit", "the
// Bash tool"), the only context the ambiguous-name tier rewrites; a bare
// "Bash" in prose is deliberately left alone by isUnambiguousToolName.
func nestedProseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cmd":  map[string]any{"type": "string", "description": "Call Read first, then use Edit."},
			"note": map[string]any{"type": "string", "title": "The Bash tool helper."},
			"mode": map[string]any{"type": "string", "enum": []any{"Read", "Edit"}},
			"kind": map[string]any{"type": "string", "const": "Read", "default": "Read", "examples": []any{"Read", "Bash"}},
			"Read": map[string]any{"type": "string", "description": "Use Read here."},
		},
		"required": []any{"Read", "Edit"},
	}
}

// nestedProseToolsBody declares Bash/Read/Edit with the same nested schema, in
// whichever carrier shape the format uses, so the two carriers are compared on
// identical logical content.
func nestedProseToolsBody(format string) []byte {
	mk := func(name, desc string) map[string]any {
		if format == "openai" {
			return map[string]any{"type": "function", "function": map[string]any{
				"name": name, "description": desc, "parameters": nestedProseSchema(),
			}}
		}
		return map[string]any{"name": name, "description": desc, "input_schema": nestedProseSchema()}
	}
	root := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
		"tools": []any{
			mk("Bash", "Use the Bash tool to run commands."),
			mk("Read", "Reads a file."),
			mk("Edit", "Edits a file."),
		},
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return raw
}

// nestedProseFields reads the first declared tool's top-level description plus
// the "cmd" description and "note" title inside its nested schema.
func nestedProseFields(t *testing.T, forward []byte, format string) (topLevel, cmd, note string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(forward, &root); err != nil {
		t.Fatalf("%s: decode forward body: %v", format, err)
	}
	tools, _ := root["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("%s: forward body declared no tools: %s", format, forward)
	}
	first, _ := tools[0].(map[string]any)
	owner := first
	schemaKey := "input_schema"
	if format == "openai" {
		fn, ok := first["function"].(map[string]any)
		if !ok {
			t.Fatalf("%s: first tool has no function object: %s", format, forward)
		}
		owner = fn
		schemaKey = "parameters"
	}
	topLevel, _ = owner["description"].(string)
	schema, _ := owner[schemaKey].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	cmdPart, _ := props["cmd"].(map[string]any)
	notePart, _ := props["note"].(map[string]any)
	cmd, _ = cmdPart["description"].(string)
	note, _ = notePart["title"].(string)
	if cmd == "" || note == "" || topLevel == "" {
		t.Fatalf("%s: fixture did not survive the round trip (top=%q cmd=%q note=%q): %s", format, topLevel, cmd, note, forward)
	}
	return topLevel, cmd, note
}

// TestIssue51_NestedSchemaProseUsesRequestScopedAliasesAcrossCarriers proves the
// nested-schema half of the acceptance criterion, identically in the OpenAI
// (tools[].function.parameters) and Anthropic (tools[].input_schema) shapes: a
// declared tool named inside a property description/title reaches the model as
// its request-scoped alias, and the same alias on both carriers.
func TestIssue51_NestedSchemaProseUsesRequestScopedAliasesAcrossCarriers(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	type surface struct{ top, cmd, note, aliases string }
	seen := map[string]surface{}

	for _, format := range []string{"openai", "anthropic"} {
		h := http.Header{"X-Cloak-Client": {"claude_code"}}
		raw, code := handlePluginCall("request.intercept_before",
			makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue51-schema-"+format, format, "agy/probe", nestedProseToolsBody(format), h))
		if code != 0 {
			t.Fatalf("%s: code=%d envelope=%s", format, code, raw)
		}
		forward := decodeEnvelopeBody(t, raw)
		aliases := declaredToolNames(forward, format)
		if len(aliases) != 3 {
			t.Fatalf("%s: declared %d tools, want 3: %s", format, len(aliases), forward)
		}
		sources := []string{"Bash", "Read", "Edit"}
		for i, source := range sources {
			if aliases[i] == source {
				t.Fatalf("%s: declared tool %q kept its source identity: %v", format, source, aliases)
			}
		}
		if aliases[0] == aliases[1] || aliases[1] == aliases[2] || aliases[0] == aliases[2] {
			t.Fatalf("%s: declared aliases are not distinct: %v", format, aliases)
		}
		aliasBash, aliasRead, aliasEdit := aliases[0], aliases[1], aliases[2]

		top, cmd, note := nestedProseFields(t, forward, format)
		if want := "Use the " + aliasBash + " tool to run commands."; top != want {
			t.Fatalf("%s: top-level description = %q, want %q (top-level behaviour must not change)", format, top, want)
		}
		if want := "Call " + aliasRead + " first, then use " + aliasEdit + "."; cmd != want {
			t.Fatalf("%s: nested schema description = %q, want %q", format, cmd, want)
		}
		if want := "The " + aliasBash + " tool helper."; note != want {
			t.Fatalf("%s: nested schema title = %q, want %q", format, note, want)
		}
		for _, phrase := range []string{"Call Read first", "use Edit", "The Bash tool helper", "Use the Bash tool to run commands"} {
			if strings.Contains(string(forward), phrase) {
				t.Fatalf("%s: source phrasing leaked into machine-generated prose: %q", format, phrase)
			}
		}

		// Issue #50, restated on the same fixture: only descriptive fields are
		// rewritten, so every structural and literal field is byte-for-byte.
		for _, literal := range []string{
			`"required":["Read","Edit"]`,
			`"mode":{"enum":["Read","Edit"],"type":"string"}`,
			`"kind":{"const":"Read","default":"Read","examples":["Read","Bash"],"type":"string"}`,
			`"Read":{"description":"Use ` + aliasRead + ` here.","type":"string"}`,
		} {
			if !strings.Contains(string(forward), literal) {
				t.Fatalf("%s: structural/literal schema field was rewritten or lost, want %s in %s", format, literal, forward)
			}
		}

		seen[format] = surface{top: top, cmd: cmd, note: note, aliases: strings.Join(aliases, ",")}
	}

	if seen["openai"] != seen["anthropic"] {
		t.Fatalf("carriers disagree on the alias surface (description prose and alias table must match): openai=%+v anthropic=%+v", seen["openai"], seen["anthropic"])
	}
}

// TestIssue51_OMPNestedSchemaProseUsesSharedToolAliases proves the same
// acceptance criterion on the Oh My Pi protected route, where the shared and
// deterministic fallback aliases live: a nested title naming the canonical
// bash tool and a nested description naming the shared hub tool must both reach
// the model under their request-scoped aliases, and neither source name may
// appear in the schema prose.
func TestIssue51_OMPNestedSchemaProseUsesSharedToolAliases(t *testing.T) {
	defer restoreDefaultFilterConfig(t)
	isolateOMPMeasurement(t)
	handlePluginCall("plugin.reconfigure", lifecycleRequestJSON(t, []byte(`model_prefixes: [agy]`)))

	tools := []any{
		map[string]any{"name": "bash", "description": "Runs a command.", "input_schema": map[string]any{"type": "object"}},
		map[string]any{"name": "read", "description": "Reads a file.", "input_schema": map[string]any{"type": "object"}},
		map[string]any{"name": "hub", "description": "Lists jobs.", "input_schema": map[string]any{
			"type":     "object",
			"required": []any{"what"},
			"properties": map[string]any{
				"what": map[string]any{
					"type":        "string",
					"description": "Call the hub tool. Oh My Pi keeps omp state in .omp/state.",
					"const":       "omp",
				},
				"where": map[string]any{
					"type":    "string",
					"title":   "The bash tool for Oh My Pi.",
					"default": "hub",
					"enum":    []any{"Oh My Pi", "omp"},
				},
			},
		}},
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
		"tools":    tools,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := http.Header{"X-Cloak-Client": {"oh_my_pi"}}
	raw, code := handlePluginCall("request.intercept_before",
		makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue51-omp-schema", "anthropic", "agy/omp-schema", body, h))
	if code != 0 {
		t.Fatalf("code=%d envelope=%s", code, raw)
	}
	forward := decodeEnvelopeBody(t, raw)
	aliases := declaredToolNames(forward, "anthropic")

	aliasHub := sharedAliasesFor("oh_my_pi")["hub"]
	// Pinned directly: a fallback-compatible assertion (alias == "" -> wp_ext
	// hash) would hold after the named entry is deleted, so it would not test
	// the named-alias contract at all.
	if aliasHub != "wp_hub" {
		t.Fatalf("OMP hub alias = %q, want the named alias %q", aliasHub, "wp_hub")
	}
	aliasBash := canonicalOMPSafeMappingSet["bash"]
	if aliasBash == "" {
		t.Fatal("no canonical alias exists for the bash tool")
	}
	for _, want := range []string{aliasHub, aliasBash} {
		found := false
		for _, got := range aliases {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("request-scoped alias %q is missing from the declaration: %v", want, aliases)
		}
	}

	var root map[string]any
	if err := json.Unmarshal(forward, &root); err != nil {
		t.Fatal(err)
	}
	hubTool := toolByName(t, root, "anthropic", aliasHub)
	hubSchema, _ := hubTool["input_schema"].(map[string]any)
	hubProps, _ := hubSchema["properties"].(map[string]any)
	whatPart, _ := hubProps["what"].(map[string]any)
	wherePart, _ := hubProps["where"].(map[string]any)
	desc, _ := whatPart["description"].(string)
	title, _ := wherePart["title"].(string)

	if want := "Call the " + aliasHub + " tool. Antigravity keeps Antigravity state in .gemini/state."; desc != want {
		t.Fatalf("OMP nested description = %q, want %q: %s", desc, want, forward)
	}
	if want := "The " + aliasBash + " tool for Antigravity."; title != want {
		t.Fatalf("OMP nested title = %q, want the canonical alias and brand %q: %s", title, want, forward)
	}
	for _, phrase := range []string{"the hub tool", "the bash tool", "Oh My Pi", ".omp/"} {
		for _, got := range []string{desc, title} {
			if strings.Contains(got, phrase) {
				t.Fatalf("OMP source phrasing %q leaked into schema prose: %q", phrase, got)
			}
		}
	}
	// Issue #50 on the same fixture: literals keep the source spelling while the
	// descriptive fields beside them are cloaked. These values are brand words,
	// so a literal guard that stopped skipping - or a descriptive allowlist that
	// widened to reach them - would rewrite the contract the model's arguments
	// have to satisfy.
	for _, want := range []string{
		`"required":["what"]`,
		`"const":"omp"`,
		`"enum":["Oh My Pi","omp"]`,
		`"default":"hub"`,
	} {
		if !strings.Contains(string(forward), want) {
			t.Fatalf("schema literal %s was rewritten: %s", want, forward)
		}
	}
	if hubProps["what"] == nil || hubProps["where"] == nil {
		t.Fatalf("property names were rewritten: %s", forward)
	}
}

// TestIssue51_AliasesInsideCollidingSchemaEntries proves the request-scoped
// tool aliases reach a description that sits under a property whose name
// collides with a schema literal keyword. The literal-key guard is a schema
// keyword guard: at the "properties" dictionary the key is an entry name, so
// the subschema under a property called "default" or "examples" must still take
// the same alias pass as any other declared tool's prose.
func TestIssue51_AliasesInsideCollidingSchemaEntries(t *testing.T) {
	defer restoreDefaultFilterConfig(t)

	for _, format := range []string{"openai", "anthropic"} {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"default":  map[string]any{"type": "string", "description": "Call Read when unset."},
				"examples": map[string]any{"type": "string", "title": "Helper for the Edit tool."},
			},
		}
		mk := func(name, desc string) map[string]any {
			if format == "openai" {
				return map[string]any{"type": "function", "function": map[string]any{
					"name": name, "description": desc, "parameters": schema,
				}}
			}
			return map[string]any{"name": name, "description": desc, "input_schema": schema}
		}
		body, err := json.Marshal(map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "go"}},
			"tools":    []any{mk("Bash", "Runs."), mk("Read", "Reads."), mk("Edit", "Edits.")},
		})
		if err != nil {
			t.Fatal(err)
		}
		h := http.Header{"X-Cloak-Client": {"claude_code"}}
		raw, code := handlePluginCall("request.intercept_before",
			makeIntegrationRequestInterceptPayloadWithHeaders(t, "issue51-collide-"+format, format, "agy/probe", body, h))
		if code != 0 {
			t.Fatalf("%s: code=%d envelope=%s", format, code, raw)
		}
		forward := decodeEnvelopeBody(t, raw)
		aliases := declaredToolNames(forward, format)
		if len(aliases) != 3 {
			t.Fatalf("%s: declared %d tools, want 3: %s", format, len(aliases), forward)
		}
		aliasRead, aliasEdit := aliases[1], aliases[2]

		var out map[string]any
		if err := json.Unmarshal(forward, &out); err != nil {
			t.Fatalf("%s: decode forward body: %v", format, err)
		}
		owner := out["tools"].([]any)[0].(map[string]any)
		schemaKey := "input_schema"
		if format == "openai" {
			owner = owner["function"].(map[string]any)
			schemaKey = "parameters"
		}
		props := owner[schemaKey].(map[string]any)["properties"].(map[string]any)
		defDesc := props["default"].(map[string]any)["description"].(string)
		exTitle := props["examples"].(map[string]any)["title"].(string)
		if want := "Call " + aliasRead + " when unset."; defDesc != want {
			t.Fatalf("%s: colliding property 'default' description = %q, want %q", format, defDesc, want)
		}
		if want := "Helper for the " + aliasEdit + " tool."; exTitle != want {
			t.Fatalf("%s: colliding property 'examples' title = %q, want %q", format, exTitle, want)
		}
		if strings.Contains(string(forward), "Call Read when unset") || strings.Contains(string(forward), "for the Edit tool") {
			t.Fatalf("%s: source phrasing leaked under a colliding entry: %s", format, forward)
		}
	}
}

// toolByName returns the declaration whose name is name for the given carrier.
func toolByName(t *testing.T, root map[string]any, format, name string) map[string]any {
	t.Helper()
	tools, _ := root["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		owner := tool
		if format == "openai" {
			fn, _ := tool["function"].(map[string]any)
			owner = fn
		}
		if got, _ := owner["name"].(string); got == name {
			return owner
		}
	}
	t.Fatalf("%s carrier has no declared tool %q: %v", format, name, root["tools"])
	return nil
}
