package cloak

import (
	"regexp"
	"sort"
	"strings"
)

func buildUncloakTable(requestBody []byte, sourceFormat string) (map[string]string, string) {
	var reqRoot map[string]any
	if err := safeUnmarshal(requestBody, &reqRoot); err != nil {
		debugLog("buildUncloakTable: unmarshal err=%v", err)
		return nil, ""
	}
	toolNames := extractToolNames(reqRoot, sourceFormat)
	// Try detecting from original (uncloaked) tool names first
	client := detectClient(toolNames)
	debugLog("buildUncloakTable: toolNames=%v client=%s", toolNames, client)
	if client == "" {
		// Request body may already be cloaked — detect from cloak targets
		client = detectCloakedClient(toolNames)
		debugLog("buildUncloakTable: cloakedClient=%s", client)
	}
	if client == "" {
		return nil, ""
	}
	if clientUsesAliasPlan(client) {
		// Claude Code and Codex cloak through a request-scoped alias plan: their
		// surface is mode-dependent and the exact forward mapping is minted per
		// request. A body alone — whether it still carries source names or only
		// cloak TARGETS — cannot prove that mapping, so it is NOT reverse
		// authority here (Issue #39). Without pinned request authority the caller
		// must pass through instead of guessing a static reverse.
		debugLog("buildUncloakTable: alias-plan client=%q withheld without pinned authority", client)
		return nil, ""
	}
	return scopeUncloakTableToDeclaredNames(effectiveUncloakTable(client), client, toolNames), client
}

// requestsRequestScopedReverse reports whether a client's reverse table must be
// narrowed to the names the current request actually declared. Codex and
// Claude Code require this: their tool surfaces include mode-dependent
// declarations or shared/fallback aliases (wp_*), so a request that declared
// one tool would otherwise receive an unearned target back in place of an
// upstream target -- a tool name that client never declared.
func requestsRequestScopedReverse(client string) bool {
	return client == "codex" || client == "claude_code"
}

// scopeUncloakTableToDeclaredNames drops every reverse pair whose names the
// request never declared, keeping the table when narrowing is not required or no
// names could be read.
//
// Which side of a pair the declared names land on depends on when the body was
// read. Request interception sees the raw client body, so its names are the
// sources the cloak would rename; the response and stream interceptors are handed
// the executed body, which the host only republishes after the rewrite, so its
// names are the targets the rewrite produced.
//
// The source side decides whenever it matches anything: a source name in the
// declared set proves the body is pre-cloak. Only a body carrying no source name
// at all is read as executed, where the targets are the only evidence left of
// which pairs the rewrite applied.
//
// An executed body cannot separate a target the client declared natively from one
// the cloak produced, so a caller holding the raw body must scope from that
// instead of re-deriving from the executed one: request interception scopes from
// the raw body and stores the result on the stream session, which the response
// path and the payload chunks reuse. The uncorrelated stream fallback has only
// the executed body and accepts that ambiguity.
func scopeUncloakTableToDeclaredNames(table map[string]string, client string, declared []string) map[string]string {
	if !requestsRequestScopedReverse(client) || len(table) == 0 || len(declared) == 0 {
		return table
	}
	declaredSet := make(map[string]bool, len(declared)*2)
	for _, name := range declared {
		declaredSet[name] = true
		if _, base := splitToolNamespace(name); base != name {
			declaredSet[base] = true
		}
	}
	declaredIsSource := false
	for _, src := range table {
		if declaredSet[src] {
			declaredIsSource = true
			break
		}
	}
	scoped := make(map[string]string, len(table))
	for target, src := range table {
		if strings.HasPrefix(target, "wp_") {
			// Dynamic identities are NEVER reconstructed by legacy static reverse fallbacks
			continue
		}
		if declaredSet[src] || (!declaredIsSource && declaredSet[target]) {
			scoped[target] = src
		}
	}
	if len(scoped) == 0 {
		return nil
	}
	return scoped
}

// declaredToolNames reads the tool names a request declared. An unreadable
// body yields no names, which scopeUncloakTableToDeclaredNames treats as "do not
// narrow" rather than "declared nothing".
func declaredToolNames(requestBody []byte, sourceFormat string) []string {
	var reqRoot map[string]any
	if err := safeUnmarshal(requestBody, &reqRoot); err != nil {
		return nil
	}
	return extractToolNames(reqRoot, sourceFormat)
}

// requestScopedUncloakPattern returns the client's precompiled stream pattern,
// narrowed to the names the request declared when narrowing applies. The regex
// is shared; only the lookup map is restricted, and a lookup miss leaves the
// matched text untouched, so an undeclared target passes through unchanged.
func requestScopedUncloakPattern(client string, requestBody []byte, sourceFormat string) *cachedUncloakPattern {
	cached := activeFilterConfig().uncloakRegexCache[client]
	if cached == nil || cached.re == nil {
		return nil
	}
	scoped := scopeUncloakTableToDeclaredNames(cached.lookup, client, declaredToolNames(requestBody, sourceFormat))
	if len(scoped) == 0 {
		return nil
	}
	if sameLookup(scoped, cached.lookup) {
		return cached
	}
	return &cachedUncloakPattern{re: cached.re, lookup: scoped, exactOnly: cached.exactOnly}
}

// requestScopedUncloakTable resolves a client's reverse table for one request,
// narrowing it to the declared names when the client requires it.
func requestScopedUncloakTable(client string, requestBody []byte, sourceFormat string) map[string]string {
	return scopeUncloakTableToDeclaredNames(effectiveUncloakTable(client), client, declaredToolNames(requestBody, sourceFormat))
}

// sameLookup reports whether two reverse lookups hold the same pairs, letting the
// scoped path reuse the shared precompiled pattern instead of allocating.
func sameLookup(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}

// splitToolNamespace separates an optional namespace prefix (e.g. "functions:", "default_api:")
// from the base tool name. It returns (prefix, baseName). If no prefix is present, it returns ("", name).
func splitToolNamespace(name string) (string, string) {
	if idx := strings.LastIndex(name, ":"); idx > 0 {
		return name[:idx+1], name[idx+1:]
	}
	return "", name
}

// resolveOMPSourceIdentity recognizes one OMP wire-escape underscore on the
// base identity only when the unescaped spelling is a verified OMP built-in.
// It preserves the exact namespace and original base for downstream reversal.
func resolveOMPSourceIdentity(name string) (prefix, originalBase, resolvedBase string) {
	prefix, originalBase = splitToolNamespace(name)
	resolvedBase = originalBase
	if strings.HasPrefix(originalBase, "_") {
		candidate := strings.TrimPrefix(originalBase, "_")
		if ompSourceIdentityInventory[candidate] {
			resolvedBase = candidate
		}
	}
	return prefix, originalBase, resolvedBase
}

func lookupProtectedOMPTarget(base string, cloakTable map[string]string) (string, bool) {
	_, _, resolvedBase := resolveOMPSourceIdentity(base)
	target, ok := cloakTable[resolvedBase]
	return target, ok
}

// lookupCloak maps a bare or namespaced tool name to its cloaked target.
func lookupCloak(name string, cloakTable map[string]string) (string, bool) {
	if target, exists := cloakTable[name]; exists {
		return target, true
	}
	if prefix, base := splitToolNamespace(name); prefix != "" {
		if target, exists := cloakTable[base]; exists {
			return prefix + target, true
		}
	}
	return "", false
}

// lookupUncloak maps a cloaked tool name (with or without namespace prefix) back to its original name.
func lookupUncloak(name string, uncloakTable map[string]string) (string, bool) {
	if orig, exists := uncloakTable[name]; exists {
		return orig, true
	}
	if prefix, base := splitToolNamespace(name); prefix != "" {
		if orig, exists := uncloakTable[base]; exists {
			return prefix + orig, true
		}
	}
	return "", false
}

func effectiveUncloakTable(client string) map[string]string {
	cloakTable := activeFilterConfig().ToolMappings[client]
	if cloakTable == nil {
		return nil
	}
	uncloak := make(map[string]string, len(cloakTable))
	for orig, target := range cloakTable {
		uncloak[target] = orig
	}
	return uncloak
}

func uncloakResponseBody(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}

	changed := uncloakJSONNode(root, uncloakTable, sourceFormat)
	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func uncloakResponseBodyExact(body []byte, uncloakTable map[string]string, sourceFormat string) ([]byte, bool) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false
	}

	changed := uncloakJSONNodeExact(root, uncloakTable, sourceFormat)
	if !changed {
		return nil, false
	}
	raw, err := safeMarshal(root)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func uncloakJSONNode(node any, uncloakTable map[string]string, sourceFormat string) bool {
	return uncloakJSONNodeOpt(node, uncloakTable, sourceFormat, false)
}

func uncloakJSONNodeExact(node any, uncloakTable map[string]string, sourceFormat string) bool {
	return uncloakJSONNodeOpt(node, uncloakTable, sourceFormat, true)
}

func uncloakJSONNodeOpt(node any, uncloakTable map[string]string, sourceFormat string, exactOnly bool) bool {
	changed := false

	lookupFn := lookupUncloak
	if exactOnly {
		lookupFn = func(name string, table map[string]string) (string, bool) {
			orig, exists := table[name]
			return orig, exists
		}
	}

	switch typed := node.(type) {
	case map[string]any:
		if sourceFormat == "openai" {
			if msg, ok := typed["message"].(map[string]any); ok {
				if toolCalls, ok := msg["tool_calls"].([]any); ok {
					for _, tcRaw := range toolCalls {
						if tc, ok := tcRaw.(map[string]any); ok {
							if fn, ok := tc["function"].(map[string]any); ok {
								if name, ok := fn["name"].(string); ok {
									if orig, exists := lookupFn(name, uncloakTable); exists {
										fn["name"] = orig
										changed = true
									}
								}
							}
						}
					}
				}
			}
			if delta, ok := typed["delta"].(map[string]any); ok {
				if toolCalls, ok := delta["tool_calls"].([]any); ok {
					for _, tcRaw := range toolCalls {
						if tc, ok := tcRaw.(map[string]any); ok {
							if fn, ok := tc["function"].(map[string]any); ok {
								if name, ok := fn["name"].(string); ok {
									if orig, exists := lookupFn(name, uncloakTable); exists {
										fn["name"] = orig
										changed = true
									}
								}
							}
						}
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			if typeVal, ok := typed["type"].(string); ok && typeVal == "tool_use" {
				if name, ok := typed["name"].(string); ok {
					if orig, exists := lookupFn(name, uncloakTable); exists {
						typed["name"] = orig
						changed = true
					}
				}
			}
		}

		for _, v := range typed {
			if childChanged := uncloakJSONNodeOpt(v, uncloakTable, sourceFormat, exactOnly); childChanged {
				changed = true
			}
		}

	case []any:
		for _, v := range typed {
			if childChanged := uncloakJSONNodeOpt(v, uncloakTable, sourceFormat, exactOnly); childChanged {
				changed = true
			}
		}
	}

	return changed
}

// rewriteRequestBodyForClient rewrites brand text, optionally sanitizes inbound
// Oh My Pi system/developer prompt text on agy/* routes, and cloaks tool names.
// When forcedClient is non-empty (a validated explicit X-Cloak-Client override
// or verified User-Agent evidence), that client's mapping table is used
// deterministically instead of body-based detection. Both brand rewriting and
// tool cloaking only run when a supported client has been resolved; if no
// supported client is resolved, the request body is returned unmutated.
//
// runOMPPromptSanitization (only the User-Agent-resolved Oh My Pi agy/* legacy
// path; callers compute the gate because only they hold the request context)
// splits the conversation pass around sanitizeOMPPrompt: brand prose first,
// sanitization second, tool-alias prose last.
func rewriteRequestBodyForClient(body []byte, sourceFormat string, forcedClient string, runOMPPromptSanitization bool) ([]byte, bool, string) {
	var root any
	if err := safeUnmarshal(body, &root); err != nil {
		return nil, false, ""
	}

	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false, ""
	}

	client := forcedClient
	if client != "" && len(effectiveCloakTable(client)) == 0 {
		client = ""
	}
	if client == "" {
		toolNames := extractToolNames(rootMap, sourceFormat)
		client = detectClient(toolNames)
	}
	if client == "" || len(effectiveCloakTable(client)) == 0 {
		return nil, false, ""
	}

	changed := false
	cfg := activeFilterConfig()
	cloakTable := effectiveCloakTable(client)
	var cachedCloak *cachedCloakPatterns
	if len(cloakTable) > 0 {
		toolCloaked := cloakToolNames(rootMap, cloakTable, sourceFormat)
		changed = changed || toolCloaked
		cachedCloak = cfg.cloakRegexCache[client]
	}

	mappings := effectiveMappings(cfg, client)
	descChanged := rewriteToolDescriptions(rootMap, mappings, cachedCloak, sourceFormat)
	changed = changed || descChanged

	if runOMPPromptSanitization {
		if sysVal, ok := rootMap["system"]; ok {
			if next, one := rewriteSystemBlockProse(sysVal, mappings); one {
				rootMap["system"] = next
				changed = true
			}
		}
		if rewriteSystemDeveloperMessages(rootMap, true, func(s string) (string, bool) {
			next, c := rewriteSystemBlockProse(s, mappings)
			out, _ := next.(string)
			return out, c
		}) {
			changed = true
		}
		// The OpenAI tool-role result is machine-generated prose too: brand it
		// in the same pre-sanitization pass the system/developer messages get,
		// so a brand word reaching the model through a result body is cloaked
		// and the sanitization output written afterward is never rebranded.
		if msgsRaw, ok := rootMap["messages"].([]any); ok {
			for _, mRaw := range msgsRaw {
				msg, ok := mRaw.(map[string]any)
				if !ok || msg["role"] != "tool" {
					continue
				}
				if content, exists := msg["content"]; exists {
					if next, one := rewriteProseTextContent(content, func(s string) (string, bool) {
						next, c := rewriteSystemValue(s, mappings)
						out, _ := next.(string)
						return out, c
					}); one {
						msg["content"] = next
						changed = true
					}
				}
			}
		}
		if sanitizeOMPPrompt(rootMap) {
			changed = true
		}
		if cachedCloak != nil && rewriteOMPConversationProse(rootMap, mappings, cachedCloak) {
			changed = true
		}
	} else {
		// The top-level system field is branded here in both lanes: the
		// conversation walker only reaches system/developer-role messages,
		// never root["system"]. The block-prose helper keeps structural
		// fields (cache_control, tool_use payloads) byte-for-byte.
		if sysVal, ok := rootMap["system"]; ok {
			if next, one := rewriteSystemBlockProse(sysVal, mappings); one {
				rootMap["system"] = next
				changed = true
			}
		}
		if rewriteConversationContent(rootMap, mappings, cachedCloak) {
			changed = true
		}
	}

	if cachedCloak != nil {
		if sysVal, ok := rootMap["system"]; ok {
			var next any
			var sysToolChanged bool
			if runOMPPromptSanitization {
				next, sysToolChanged = rewriteProseTextContent(sysVal, func(s string) (string, bool) {
					return replaceToolNamesInText(s, cachedCloak)
				})
			} else {
				next, sysToolChanged = replaceToolNamesInValue(sysVal, cachedCloak)
			}
			if sysToolChanged {
				rootMap["system"] = next
				changed = true
			}
		}
	}
	if !changed {
		return nil, false, client
	}
	raw, err := safeMarshal(rootMap)
	if err != nil {
		return nil, false, client
	}
	return raw, true, client
}

func effectiveCloakTable(client string) map[string]string {
	return activeFilterConfig().ToolMappings[client]
}

func cloakToolNames(body map[string]any, cloakTable map[string]string, sourceFormat string) bool {
	changed := false

	// Cloak tools[] array
	if toolsRaw, ok := body["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			tMap, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if sourceFormat == "openai" {
				fn, ok := tMap["function"].(map[string]any)
				if !ok {
					continue
				}
				if name, ok := fn["name"].(string); ok {
					if target, exists := lookupCloak(name, cloakTable); exists {
						fn["name"] = target
						changed = true
					}
				}
			} else if sourceFormat == "anthropic" {
				if name, ok := tMap["name"].(string); ok {
					if target, exists := lookupCloak(name, cloakTable); exists {
						tMap["name"] = target
						changed = true
					}
				}
			}
		}
	}

	// Cloak tool refs in messages[]
	if msgsRaw, ok := body["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			msg, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}

			if sourceFormat == "openai" {
				// tool_calls[].function.name
				if calls, ok := msg["tool_calls"].([]any); ok {
					for _, cRaw := range calls {
						call, ok := cRaw.(map[string]any)
						if !ok {
							continue
						}
						fn, ok := call["function"].(map[string]any)
						if !ok {
							continue
						}
						if name, ok := fn["name"].(string); ok {
							if target, exists := lookupCloak(name, cloakTable); exists {
								fn["name"] = target
								changed = true
							}
						}
					}
				}
				// tool result message: msg["name"]
				if msg["role"] == "tool" {
					if name, ok := msg["name"].(string); ok {
						if target, exists := lookupCloak(name, cloakTable); exists {
							msg["name"] = target
							changed = true
						}
					}
				}
			} else if sourceFormat == "anthropic" {
				// content blocks with type == "tool_use"
				if contents, ok := msg["content"].([]any); ok {
					for _, cntRaw := range contents {
						cnt, ok := cntRaw.(map[string]any)
						if !ok {
							continue
						}
						if cnt["type"] == "tool_use" {
							if name, ok := cnt["name"].(string); ok {
								if target, exists := lookupCloak(name, cloakTable); exists {
									cnt["name"] = target
									changed = true
								}
							}
						}
					}
				}
			}
		}
	}

	// Cloak tool_choice (handle both string and object shapes)
	if tc, ok := body["tool_choice"].(map[string]any); ok {
		if sourceFormat == "openai" {
			// {type: "function", function: {name: "..."}}
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					if target, exists := lookupCloak(name, cloakTable); exists {
						fn["name"] = target
						changed = true
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			// {type: "tool", name: "..."}
			if name, ok := tc["name"].(string); ok {
				if target, exists := lookupCloak(name, cloakTable); exists {
					tc["name"] = target
					changed = true
				}
			}
		}
	}

	return changed
}

func rewriteToolDescriptions(root map[string]any, mappings []rewriteMapping, cached *cachedCloakPatterns, sourceFormat string) bool {
	changed := false
	toolsRaw, ok := root["tools"].([]any)
	if !ok {
		return false
	}
	for _, tRaw := range toolsRaw {
		tMap, ok := tRaw.(map[string]any)
		if !ok {
			continue
		}
		// The tool's own description is machine-generated prose too, and so is
		// the parameter help inside the JSON Schema one or two levels below it.
		// Claude Code ships tools whose parameter description names the client
		// home directory (~/.claude/scheduled_tasks.json), so a top-level-only
		// walk left that sitting in the payload. Both take this pass's text
		// rule - the brand mappings plus the request-scoped tool aliases - and
		// the schema is walked rather than re-serialized, so key order and
		// nesting survive.
		if rewriteToolProse(tMap, sourceFormat, func(s string) (string, bool) {
			return rewriteDescriptiveText(s, mappings, cached)
		}) {
			changed = true
		}
	}
	return changed
}

// isSchemaTextField reports whether a JSON Schema keyword holds human-readable
// help rather than structure. Every other string in a schema is structure: a
// property name, a `required` member, an `enum`/`const` value, a `pattern`.
// Rewriting one of those while the property or value it validates keeps the
// client's spelling leaves a schema no arguments can satisfy
// (required:["Antigravity"] over a property still called "Claude").
func isSchemaTextField(key string) bool {
	return key == "description" || key == "title"
}

// rewriteToolProse rewrites the machine-generated prose of one tool
// declaration: its own description, then its JSON Schema's descriptive fields.
// The owner is the tool itself, or its `function` object on the OpenAI wire
// shape; a structural or literal-bearing field is never touched.
func rewriteToolProse(tool map[string]any, sourceFormat string, rewrite func(string) (string, bool)) bool {
	owner := tool
	if sourceFormat == "openai" {
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			return false
		}
		owner = fn
	}
	changed := false
	if desc, ok := owner["description"].(string); ok {
		if next, c := rewrite(desc); c {
			owner["description"] = next
			changed = true
		}
	}
	// The schema is walked rather than re-serialized, so key order and nesting
	// survive the rewrite.
	for _, key := range []string{"input_schema", "parameters"} {
		schema, ok := owner[key]
		if !ok {
			continue
		}
		if rewriteSchemaTextFields(schema, rewrite) {
			changed = true
		}
	}
	return changed
}

// schemaLiteralKeys are the keywords whose value is DATA the caller validates
// against, not a schema. Rewriting anything below one of them changes a
// constant while the property carrying it keeps the client's spelling, so the
// schema can no longer be satisfied: enum:["Antigravity"] over a property
// still called "Claude" rejects every argument the client can send. A nested
// schema is reached through a schema keyword or a dictionary entry, never
// through one of these literal keywords, so skipping a literal subtree costs
// no real traversal.
var schemaLiteralKeys = map[string]bool{
	"const":    true,
	"enum":     true,
	"default":  true,
	"example":  true,
	"examples": true,
}

// schemaDictionaryKeys are the schema keywords whose value is a
// dictionary-of-schemas: the keys are arbitrary property or definition names,
// never keywords. A name that happens to collide with a literal keyword - a
// property called "default", a definition called "examples" - is still an
// entry whose subschema has to be walked, so the literal-key skip applies only
// on a schema node, never inside one of these containers.
var schemaDictionaryKeys = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
}

// rewriteSchemaTextFields applies rewrite to the descriptive strings of a JSON
// Schema value, recursing through properties, items, $defs and the composition
// keywords to reach nested schemas, and leaving every structural string exactly
// as it was. The subtree under a literal-bearing keyword is never descended
// into, but only where that keyword holds data: inside a dictionary-of-schemas
// container the keys are names, so an entry spelled like a literal keyword is
// still walked. Containers are mutated in place, so only the changed flag
// travels back. The transform is a parameter because two passes need the same
// walk with different text rules: the alias-plan clients run the brand mappings
// plus the request-scoped tool aliases, while the protected Oh My Pi route runs
// its own sentinel-aware brand text and gets the aliases from the later alias
// pass.
func rewriteSchemaTextFields(value any, rewrite func(string) (string, bool)) bool {
	return rewriteSchemaValue(value, false, rewrite)
}

// rewriteSchemaValue walks one JSON Schema value. dictOfSchemas is true when
// value is the contents of a dictionary-of-schemas container, so its keys are
// entry names rather than keywords.
func rewriteSchemaValue(value any, dictOfSchemas bool, rewrite func(string) (string, bool)) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			if s, isString := child.(string); isString {
				// A dictionary entry's value is a schema, so a string here is
				// not keyword help; only a schema node's descriptive field is.
				if dictOfSchemas || !isSchemaTextField(key) {
					continue
				}
				if next, c := rewrite(s); c {
					typed[key] = next
					changed = true
				}
				continue
			}
			// Only containers are walked, and never under a literal-bearing
			// keyword: a nested string there is a validated constant, not help
			// text, and rewriting it would break the contract the model's
			// arguments have to satisfy. This is a schema-keyword guard, so it
			// does not fire on an entry name inside a dictionary.
			if !dictOfSchemas && schemaLiteralKeys[key] {
				continue
			}
			switch child.(type) {
			case map[string]any, []any:
				if rewriteSchemaValue(child, !dictOfSchemas && schemaDictionaryKeys[key], rewrite) {
					changed = true
				}
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			switch child.(type) {
			case map[string]any, []any:
				if rewriteSchemaValue(child, false, rewrite) {
					changed = true
				}
			}
		}
		return changed
	}
	return false
}

// rewriteDescriptiveText applies the brand mappings and then the request-scoped
// tool-name aliases to one piece of human-readable text. It is the single seam
// every descriptive surface uses - a tool's top-level description and the
// description/title fields inside its JSON Schema - so machine-generated prose
// that names a declared tool is cloaked to the same alias everywhere. Only
// descriptive text may take the tool aliases: running them over structural
// strings would rewrite a contract the model's arguments have to satisfy.
func rewriteDescriptiveText(text string, mappings []rewriteMapping, cached *cachedCloakPatterns) (string, bool) {
	next := text
	changed := false
	for _, mapping := range mappings {
		var replaced bool
		next, replaced = replaceInsensitiveRule(next, mapping, true)
		changed = changed || replaced
	}
	if cached != nil {
		var toolReplaced bool
		next, toolReplaced = replaceToolNamesInText(next, cached)
		changed = changed || toolReplaced
	}
	return next, changed
}

// rewriteProseTextContent applies fn only to prose-bearing fields of a content
// value: a plain string, or each block's text / input_text / thinking field.
// Structural fields (cache_control) and tool payloads (tool_use id, name,
// input, tool_result identity) are data the caller validates against, so they
// stay byte-for-byte.
func rewriteProseTextContent(value any, fn func(string) (string, bool)) (any, bool) {
	switch content := value.(type) {
	case string:
		if next, changed := fn(content); changed {
			return next, true
		}
	case []any:
		changed := false
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			field := ""
			switch block["type"] {
			case "text", "input_text":
				field = "text"
			case "thinking":
				field = "thinking"
			}
			if field == "" {
				continue
			}
			if text, ok := block[field].(string); ok {
				if next, one := fn(text); one {
					block[field] = next
					changed = true
				}
			}
		}
		return value, changed
	}
	return value, false
}

// rewriteSystemBlockProse brands a system content value the way
// rewriteSystemFields does, but keeps a block's structural fields
// (cache_control) byte-for-byte: only the prose text and recursively nested
// prose carry the client's identity.
func rewriteSystemBlockProse(value any, mappings []rewriteMapping) (any, bool) {
	switch content := value.(type) {
	case string:
		next, changed := rewriteSystemValue(content, mappings)
		return next, changed
	case []any:
		changed := false
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			btype, _ := block["type"].(string)
			if btype != "text" && btype != "input_text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if next, one := rewriteSystemValue(text, mappings); one {
					block["text"] = next
					changed = true
				}
			}
		}
		return value, changed
	default:
		return value, false
	}
}

// rewriteOMPConversationProse is the Oh My Pi tool-alias prose pass over the
// conversation, shared by the Protected OMP and User-Agent OMP agy/* routes. It
// keeps the user-turn contract (only the client's own <system-reminder> spans
// and tool_result payloads) and cloaks prose fields of the other roles, while
// structural fields and tool arguments stay untouched.
func rewriteOMPConversationProse(root map[string]any, mappings []rewriteMapping, cached *cachedCloakPatterns) bool {
	msgsRaw, ok := root["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, mRaw := range msgsRaw {
		msg, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		content, exists := msg["content"]
		if !exists {
			continue
		}
		switch role, _ := msg["role"].(string); role {
		case "user":
			if next, c := rewriteUserTurnContent(content, mappings, cached); c {
				msg["content"] = next
				changed = true
			}
			continue
		case "assistant":
			if next, c := rewriteAssistantProse(content, mappings, cached); c {
				msg["content"] = next
				changed = true
			}
			continue
		}
		// The sanitize lane already brand-rewrote the non-user, non-assistant
		// roles (system/developer messages and the OpenAI tool role) before
		// sanitizeOMPPrompt ran, so this post-sanitize catch-all applies the
		// request-scoped tool aliases only: re-running the brand table here would
		// rewrite the client's own brand words that the fresh sanitization
		// replacement text carries as literal content.
		if next, c := rewriteProseTextContent(content, func(s string) (string, bool) {
			return replaceToolNamesInText(s, cached)
		}); c {
			msg["content"] = next
			changed = true
		}
	}
	return changed
}

// rewriteAssistantProse applies the brand mappings and request-scoped tool
// aliases to an assistant turn's prose. Unlike system/developer content, an
// assistant turn interleaves the model's text with the replayed tool_use calls
// whose arguments are data the client validates against, so the payload stays
// byte-for-byte and only the text/thinking fields carry the cloaks.
func rewriteAssistantProse(content any, mappings []rewriteMapping, cached *cachedCloakPatterns) (any, bool) {
	changed := false
	switch typed := content.(type) {
	case string:
		return rewriteMachineSpan(typed, mappings, cached)
	case []any:
		for _, blockRaw := range typed {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				continue
			}
			field := ""
			switch block["type"] {
			case "text":
				field = "text"
			case "thinking":
				field = "thinking"
			}
			if field == "" {
				continue
			}
			if text, ok := block[field].(string); ok {
				if next, one := rewriteMachineSpan(text, mappings, cached); one {
					block[field] = next
					changed = true
				}
			}
		}
	}
	return content, changed
}

// rewriteConversationContent applies the brand mapping and the tool-name cloak
// to every turn of the conversation, not just the system ones. Claude Code
// inlines its own private global instructions and its whole skill catalogue as
// a <system-reminder> block inside a user message, so a system-only walk left
// the client fingerprint sitting in the payload. tool_result payloads go
// through the same pass: the model is shown the cloaked text and the response
// path maps it back, so the client still reads exactly what the tool returned.
func rewriteConversationContent(root map[string]any, mappings []rewriteMapping, cached *cachedCloakPatterns) bool {
	changed := false
	msgsRaw, ok := root["messages"].([]any)
	if !ok {
		return changed
	}
	for _, mRaw := range msgsRaw {
		msg, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		content, exists := msg["content"]
		if !exists {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "user" {
			// The words the user typed are theirs, byte for byte. Only the
			// client's own machine-generated blocks inside the turn (its
			// <system-reminder> spans and its tool_result payloads) are
			// rewritten, for brands AND for tool names. A blanket
			// replaceToolNamesInValue over the whole turn used to corrupt an
			// ordered typed user string - a file the user really wrote in
			// out.txt was silently persisted as the declaration's wp_find_tools
			// literal instead.
			if next, c := rewriteUserTurnContent(content, mappings, cached); c {
				msg["content"] = next
				changed = true
			}
			continue
		}
		next, contentChanged := rewriteSystemValue(content, mappings)
		if contentChanged {
			msg["content"] = next
			changed = true
		}
		if cached != nil {
			toolNext, toolChanged := replaceToolNamesInValue(msg["content"], cached)
			if toolChanged {
				msg["content"] = toolNext
				changed = true
			}
		}
	}
	return changed
}

// rewriteMachineSpan runs both cloaks over one client-generated span: the brand
// mappings, then the request-scoped tool aliases.
func rewriteMachineSpan(block string, mappings []rewriteMapping, cached *cachedCloakPatterns) (string, bool) {
	next, c := rewriteSystemValue(block, mappings)
	out, _ := next.(string)
	if cached != nil {
		withTools, tc := replaceToolNamesInText(out, cached)
		out = withTools
		c = c || tc
	}
	return out, c
}

// rewriteToolResultContentProse applies the request-scoped tool aliases to a
// tool_result's content, whether it is a string or an array of text blocks, and
// touches no metadata field.
func rewriteToolResultContentProse(value any, mappings []rewriteMapping, cached *cachedCloakPatterns) (any, bool) {
	prose := func(s string) (string, bool) {
		return rewriteMachineSpan(s, mappings, cached)
	}
	switch content := value.(type) {
	case string:
		return prose(content)
	case []any:
		changed := false
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			// The Anthropic wire types a tool_result content array as
			// TextBlockParam | ImageBlockParam; only type:"text" blocks carry a
			// prose field. Rewriting a stray "text" key on any other block type
			// (image metadata, base64 data) corrupts non-prose data the way the
			// recursive pass did, so the block type decides.
			btype, _ := block["type"].(string)
			if btype != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if next, one := prose(text); one {
					block["text"] = next
					changed = true
				}
			}
		}
		return value, changed
	}
	return value, false
}

// rewriteUserTurnContent applies the brand mapping to a user turn, but only to
// the parts the client generated. A user turn is not one blob: the typed
// message, the <system-reminder> block carrying the client's private
// instructions and skill catalogue, and tool_result payloads all arrive
// interleaved under the same role, and they must be treated differently.
//
// The typed text is left alone on purpose. It is the user's own words, and
// anything the model then writes to disk is persisted in whatever spelling it
// saw, because a file leaves the process and never flows back through the
// response path that reverses the other direction. Cloaking the
// <system-reminder> and tool_result blocks costs nothing in correctness: that
// text is machine-produced context, not the user's.
func rewriteUserTurnContent(content any, mappings []rewriteMapping, cached *cachedCloakPatterns) (any, bool) {
	changed := false
	switch typed := content.(type) {
	case string:
		// A bare string turn: the typed message, possibly with the client's
		// own <system-reminder> block spliced into it. Only the span is
		// cloaked; the words the user typed around it are theirs.
		next, c := rewriteSystemReminderSpans(typed, mappings, cached)
		return next, c
	case []any:
		for _, blockRaw := range typed {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "tool_result":
				// A tool result is machine-generated output echoing the alias
				// names the model was given, so it is cloaked on both surfaces -
				// but only on its content, never on the block's identity fields.
				if next, c := rewriteToolResultContentProse(block["content"], mappings, cached); c {
					block["content"] = next
					changed = true
				}
			case "text":
				txt, ok := block["text"].(string)
				if !ok {
					continue
				}
				// Same span-aware rule as the bare-string turn: ordinary text is
				// untouched, <system-reminder> spans get brands + tool aliases.
				if next, c := rewriteSystemReminderSpans(txt, mappings, cached); c {
					block["text"] = next
					changed = true
				}
			}
		}
	}
	return content, changed
}

// rewriteSystemReminderSpans rewrites only the text between <system-reminder>
// markers, leaving the surrounding typed message byte-identical.
// rewriteSystemReminderSpans applies the client's own cloaks to the bytes
// INSIDE its <system-reminder> spans and to nothing else.
//
// The span body gets both surfaces: the brand mappings and, when cached is
// non-nil, the request-scoped tool aliases. A tool named in a reminder is text
// the client generated from the same conversation state the model saw, so it
// carries aliases upstream just like the prose does.
//
// Everything outside a span is written through verbatim. The user typed those
// bytes. Running either surface across the whole turn rewrote real content -
// "Write the literal ToolSearch into out.txt" was silently persisted as
// "Write the literal wp_find_tools into out.txt", and the model was told to
// create a file whose name the user never asked for.
func rewriteSystemReminderSpans(text string, mappings []rewriteMapping, cached *cachedCloakPatterns) (string, bool) {
	const openTag, closeTag = "<system-reminder>", "</system-reminder>"
	var b strings.Builder
	rest := text
	changed := false
	for {
		start := strings.Index(rest, openTag)
		if start < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		rest = rest[start:]
		end := strings.Index(rest, closeTag)
		if end < 0 {
			// Unterminated marker: the client truncated the turn. Treat the
			// remainder as context rather than leaving half a block unclaked.
			next, c := rewriteMachineSpan(rest, mappings, cached)
			b.WriteString(next)
			changed = changed || c
			break
		}
		block := rest[:end+len(closeTag)]
		next, c := rewriteMachineSpan(block, mappings, cached)
		b.WriteString(next)
		changed = changed || c
		rest = rest[end+len(closeTag):]
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

type textSpanReplacement struct {
	start int
	end   int
	repl  string
}

// replaceToolNamesInText uses pre-compiled regex patterns from cachedCloakPatterns.
// Patterns are compiled once on config change (rebuildCachedRegexes), not per call.
// It executes a single-pass reconstruction from non-overlapping match spans
// evaluated against the original text, ensuring that a target which is also a
// source key is translated exactly once per token and never cascades across tiers.
func replaceToolNamesInText(text string, cached *cachedCloakPatterns) (string, bool) {
	if cached == nil || len(cached.cloakTable) == 0 {
		return text, false
	}

	var replacements []textSpanReplacement

	// Tier 1: single-pass identity replacement. A single regex covers quoted
	// references, namespaced (qualified) identifiers, and unambiguous names.
	if cached.identRe != nil {
		identText := text
		if len(text) >= 8<<10 {
			// Every Tier 1 match contains an exact source tool name. On long prompt
			// text, stop the regex just past the last possible source occurrence;
			// one lookahead byte preserves \b. Short descriptions use the direct
			// regex path to avoid paying for the pre-scan.
			identEnd := 0
			for orig := range cached.cloakTable {
				if index := strings.LastIndex(text, orig); index >= 0 {
					identEnd = max(identEnd, index+len(orig))
				}
			}
			if identEnd == 0 {
				identText = ""
			} else {
				identText = text[:min(len(text), identEnd+1)]
			}
		}
		matches := cached.identRe.FindAllStringIndex(identText, -1)
		for _, m := range matches {
			sub := text[m[0]:m[1]]
			repl := replaceToolIdentity(sub, cached)
			if repl != sub {
				replacements = append(replacements, textSpanReplacement{
					start: m[0],
					end:   m[1],
					repl:  repl,
				})
			}
		}
	}

	// Tier 3: Pattern-based replacement for ambiguous names in tool-reference
	// contexts ("the bash tool", "use read"). Evaluated against original text
	// and skipped if overlapping with Tier 1.
	if cached.ambigRe != nil {
		matches := cached.ambigRe.FindAllStringSubmatchIndex(text, -1)
		for _, sub := range matches {
			if len(sub) < 14 {
				continue
			}
			fullStart, fullEnd := sub[0], sub[1]
			var lead, tool, trail string
			if sub[2] >= 0 && sub[3] >= 0 {
				lead = text[sub[2]:sub[3]]
				tool = text[sub[4]:sub[5]]
				trail = text[sub[6]:sub[7]]
			} else if sub[8] >= 0 && sub[9] >= 0 {
				lead = text[sub[8]:sub[9]]
				tool = text[sub[10]:sub[11]]
				trail = text[sub[12]:sub[13]]
			}
			if tool == "" {
				continue
			}
			prefix, base := splitToolNamespace(tool)
			if prefix != "" && isToolSourceName(strings.TrimSuffix(prefix, ":"), cached.cloakTable) {
				// Access-mode / compound token like "read:write", do not cloak.
				continue
			}
			if target, ok := lookupCloakFold(base, cached.cloakTable); ok {
				newStr := lead + prefix + target + trail
				if newStr != text[fullStart:fullEnd] {
					overlaps := false
					for _, r := range replacements {
						if fullStart < r.end && fullEnd > r.start {
							overlaps = true
							break
						}
					}
					if !overlaps {
						replacements = append(replacements, textSpanReplacement{
							start: fullStart,
							end:   fullEnd,
							repl:  newStr,
						})
					}
				}
			}
		}
	}

	if len(replacements) == 0 {
		return text, false
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})

	var builder strings.Builder
	builder.Grow(len(text))
	last := 0
	for _, r := range replacements {
		if r.start < last {
			continue
		}
		builder.WriteString(text[last:r.start])
		builder.WriteString(r.repl)
		last = r.end
	}
	builder.WriteString(text[last:])

	return builder.String(), true
}

// isToolSourceName reports whether name is one of the cloak table's original
// (source) tool names. Used to avoid mistaking an access-mode or compound
// token such as "read:write" for a qualified tool reference.
func isToolSourceName(name string, cloakTable map[string]string) bool {
	if _, ok := cloakTable[name]; ok {
		return true
	}
	for orig := range cloakTable {
		if strings.EqualFold(orig, name) {
			return true
		}
	}
	return false
}

// lookupCloakFold resolves a possibly different-cased tool base name to its
// cloaked target. The ambiguous context patterns match case-insensitively
// ("use Bash" matches the source key "bash"), so the base must be resolved
// against the table with case folding.
func lookupCloakFold(base string, cloakTable map[string]string) (string, bool) {
	if target, ok := cloakTable[base]; ok {
		return target, true
	}
	for orig, target := range cloakTable {
		if strings.EqualFold(orig, base) {
			return target, true
		}
	}
	return "", false
}

// lookupCloakText maps a bare or qualified tool identity to its cloaked form,
// preserving any namespace prefix. A namespaced reference whose prefix is
// itself a source tool name (e.g. "read:write") is an access-mode / compound
// token, not a tool reference, so it is left unchanged.
func lookupCloakText(ident string, cloakTable map[string]string) (string, bool) {
	if prefix, base := splitToolNamespace(ident); prefix != "" {
		if isToolSourceName(strings.TrimSuffix(prefix, ":"), cloakTable) {
			return "", false
		}
		if _, ok := cloakTable[base]; !ok {
			return "", false
		}
	}
	return lookupCloak(ident, cloakTable)
}

// replaceToolIdentity rewrites a single matched tool identity: it strips any
// surrounding quote, maps the identity through the cloak table, and rebuilds
// the identical quoted form so quoted and unquoted references stay consistent.
func replaceToolIdentity(m string, cached *cachedCloakPatterns) string {
	q := byte(0)
	if len(m) >= 2 {
		first, last := m[0], m[len(m)-1]
		if (first == '`' || first == '"') && first == last {
			q = first
			m = m[1 : len(m)-1]
		}
	}
	repl, ok := lookupCloakText(m, cached.cloakTable)
	if !ok {
		if q != 0 {
			return string(q) + m + string(q)
		}
		return m
	}
	if q != 0 {
		return string(q) + repl + string(q)
	}
	return repl
}

// buildCloakIdentRe compiles a single regex that matches any tool identity in
// prose: quoted names, namespaced (qualified) identifiers, and unambiguous
// names. The alternation order matters — quoted first, then namespaced, then
// unambiguous — so the most specific form wins and bare ambiguous names are
// never rewritten here (they are handled by the contextual Tier 3 rules).
func buildCloakIdentRe(cloakTable map[string]string) *regexp.Regexp {
	all := make([]string, 0, len(cloakTable))
	unambig := make([]string, 0, len(cloakTable))
	for orig := range cloakTable {
		all = append(all, regexp.QuoteMeta(orig))
		if isUnambiguousToolName(orig) {
			unambig = append(unambig, regexp.QuoteMeta(orig))
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Strings(all)
	sort.Strings(unambig)
	allRe := strings.Join(all, "|")
	pattern := "[`\"](?:[a-zA-Z0-9_-]+:)?(?:" + allRe + ")[`\"]|\\b(?:[a-zA-Z0-9_-]+:)(?:" + allRe + ")\\b"
	if len(unambig) > 0 {
		pattern += "|\\b(?:[a-zA-Z0-9_-]+:)?(?:" + strings.Join(unambig, "|") + ")\\b"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// buildCloakAmbiguousRe compiles a single regex that matches ambiguous tool
// names only inside explicit tool-reference contexts ("the bash tool",
// "use read", "call edit"). A single pass guarantees that a target which is
// also a source key is never re-translated within one rewrite.
func buildCloakAmbiguousRe(cloakTable map[string]string) *regexp.Regexp {
	var ambig []string
	for orig := range cloakTable {
		if !isUnambiguousToolName(orig) {
			ambig = append(ambig, regexp.QuoteMeta(orig))
		}
	}
	if len(ambig) == 0 {
		return nil
	}
	sort.Strings(ambig)
	ambigRe := strings.Join(ambig, "|")
	toolRe := `(?:[a-zA-Z0-9_-]+:)?(?:` + ambigRe + `)`
	// "the X tool" requires a trailing tool/function/command word; the verb
	// forms ("use X", "call X") only require a word boundary. Keep the capture
	// groups consistent: 1=lead,2=tool,3=trail for the "the" branch and
	// 4=verb,5=tool,6=trail for the verb branch.
	pattern := `(?i)\b(?:(the\s+)(` + toolRe + `)(\s+(?:tool|function|command)\b)|((?:use|call|run|invoke|with)\s+)(` + toolRe + `)(\b))`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// isUnambiguousToolName returns true if a tool name is specific enough for
// safe word-boundary replacement. Names with underscores or camelCase are
// identifiers, not common English words.
func isUnambiguousToolName(name string) bool {
	if strings.Contains(name, "_") {
		return true
	}
	// camelCase / multi-word names (an uppercase rune AFTER the first one) are
	// distinctive enough to replace anywhere in prose, e.g. "AskUserQuestion",
	// "ToolSearch". Single capitalized words like "Read", "Edit", "Bash" collide
	// with ordinary English prose, so treat them as ambiguous and only replace
	// them inside explicit tool-reference contexts (e.g. "use Bash", "the Read tool").
	for i, r := range name {
		if i == 0 {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func replaceToolNamesInValue(value any, cached *cachedCloakPatterns) (any, bool) {
	switch typed := value.(type) {
	case string:
		return replaceToolNamesInText(typed, cached)
	case map[string]any:
		changed := false
		for key, child := range typed {
			next, childChanged := replaceToolNamesInValue(child, cached)
			if childChanged {
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	case []any:
		changed := false
		for i, child := range typed {
			next, childChanged := replaceToolNamesInValue(child, cached)
			if childChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func extractToolNames(body map[string]any, sourceFormat string) []string {
	var names []string

	// Check tools array first
	if toolsRaw, ok := body["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if fn, ok := tMap["function"].(map[string]any); ok {
						if name, ok := fn["name"].(string); ok {
							names = append(names, name)
						}
					}
				} else if sourceFormat == "anthropic" {
					if name, ok := tMap["name"].(string); ok {
						names = append(names, name)
					}
				}
			}
		}
	}

	// Fallback to history when tools[] is empty or absent
	if len(names) == 0 {
		if msgsRaw, ok := body["messages"].([]any); ok {
			for _, mRaw := range msgsRaw {
				if msg, ok := mRaw.(map[string]any); ok {
					if sourceFormat == "openai" {
						if calls, ok := msg["tool_calls"].([]any); ok {
							for _, cRaw := range calls {
								if call, ok := cRaw.(map[string]any); ok {
									if fn, ok := call["function"].(map[string]any); ok {
										if name, ok := fn["name"].(string); ok {
											names = append(names, name)
										}
									}
								}
							}
						}
					} else if sourceFormat == "anthropic" {
						if contents, ok := msg["content"].([]any); ok {
							for _, cntRaw := range contents {
								if cnt, ok := cntRaw.(map[string]any); ok {
									if typeVal, ok := cnt["type"].(string); ok && typeVal == "tool_use" {
										if name, ok := cnt["name"].(string); ok {
											names = append(names, name)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return names
}
