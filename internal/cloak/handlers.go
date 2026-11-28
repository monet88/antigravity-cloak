package cloak

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func HandleRequestComplete(request []byte) []byte {
	var comp pluginapi.RequestCompletion
	if err := json.Unmarshal(request, &comp); err != nil {
		debugLog("handleRequestComplete: decode error %v", err)
		return MustErrorEnvelope("invalid_request", err.Error())
	}
	debugLog("handleRequestComplete: RequestID=%q Outcome=%s", comp.RequestID, comp.Outcome)
	if comp.RequestID != "" {
		globalLifecycleManager.deleteRoute(comp.RequestID)
		globalAliasPlanManager.delete(comp.RequestID)
		globalStreamManager.deleteSession("req:" + comp.RequestID)
	}
	return MustEnvelope(struct{}{})
}

func HandlePluginLifecycle(request []byte, registration any) []byte {
	if len(request) > 0 {
		cfg, err := filterConfigFromLifecycleRequest(request)
		if err != nil {
			return MustErrorEnvelope("invalid_config", err.Error())
		}
		applyFilterConfig(cfg)
	}
	return MustEnvelope(registration)
}

func protected503Response(resp pluginapi.RequestInterceptResponse) []byte {
	resp.Terminate = true
	resp.StatusCode = http.StatusServiceUnavailable // 503
	resp.ResponseHeaders = http.Header{
		"Content-Type": []string{"application/json"},
	}
	resp.ResponseBody = []byte(`{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}`)
	return MustEnvelope(resp)
}

func toolCloak503Response(resp pluginapi.RequestInterceptResponse) []byte {
	resp.Terminate = true
	resp.StatusCode = http.StatusServiceUnavailable
	resp.ResponseHeaders = http.Header{
		"Content-Type": []string{"application/json"},
	}
	resp.ResponseBody = []byte("{\"error\":{\"code\":\"tool_cloak_required\",\"message\":\"Request could not be safely cloaked.\"}}")
	return MustEnvelope(resp)
}

type protectedDeclInfo struct {
	originalFullName string
	prefix           string
	base             string
	finalBase        string
	transformed      bool
}

func collectRequestSourceIdentitiesWithDeclared(root map[string]any, format string) ([]string, []string, error) {
	var sources []string
	var declared []string

	// 1. Declarations in tools[]
	if toolsValue, exists := root["tools"]; exists && toolsValue != nil {
		tools, ok := toolsValue.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("unsupported tools representation")
		}
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("unsupported tool declaration")
			}
			var name string
			if format == "openai" {
				if typ, hasType := tool["type"].(string); hasType && typ != "" && typ != "function" {
					return nil, nil, fmt.Errorf("unsupported openai tool type %q", typ)
				}
				fn, ok := tool["function"].(map[string]any)
				if !ok {
					return nil, nil, fmt.Errorf("unsupported openai tool declaration")
				}
				name, _ = fn["name"].(string)
			} else {
				name, _ = tool["name"].(string)
			}
			if name == "" {
				return nil, nil, fmt.Errorf("tool declaration is missing a name")
			}
			sources = append(sources, name)
			declared = append(declared, name)
		}
	}

	// 2. History in messages[]
	if msgsValue, exists := root["messages"]; exists && msgsValue != nil {
		if msgs, ok := msgsValue.([]any); ok {
			for _, m := range msgs {
				msg, ok := m.(map[string]any)
				if !ok {
					continue
				}
				if format == "openai" {
					if calls, ok := msg["tool_calls"].([]any); ok {
						for _, c := range calls {
							if call, ok := c.(map[string]any); ok {
								if fn, ok := call["function"].(map[string]any); ok {
									if name, ok := fn["name"].(string); ok && name != "" {
										sources = append(sources, name)
									}
								}
							}
						}
					}
					if role, ok := msg["role"].(string); ok && role == "tool" {
						if name, ok := msg["name"].(string); ok && name != "" {
							sources = append(sources, name)
						}
					}
				} else if format == "anthropic" {
					if contents, ok := msg["content"].([]any); ok {
						for _, cnt := range contents {
							if block, ok := cnt.(map[string]any); ok {
								if t, ok := block["type"].(string); ok && t == "tool_use" {
									if name, ok := block["name"].(string); ok && name != "" {
										sources = append(sources, name)
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// 3. Named tool_choice
	if tcValue, exists := root["tool_choice"]; exists && tcValue != nil {
		if tc, ok := tcValue.(map[string]any); ok {
			if format == "openai" {
				if fn, ok := tc["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok && name != "" {
						sources = append(sources, name)
					}
				}
			} else if format == "anthropic" {
				if name, ok := tc["name"].(string); ok && name != "" {
					sources = append(sources, name)
				}
			}
		}
	}

	return sources, declared, nil
}

func collectRequestSourceIdentities(root map[string]any, format string) ([]string, error) {
	sources, _, err := collectRequestSourceIdentitiesWithDeclared(root, format)
	return sources, err
}

func inspectAndValidateProtectedTools(rootMap map[string]any, format string, mergedCloak map[string]string) ([]protectedDeclInfo, error) {
	names, err := collectRequestSourceIdentities(rootMap, format)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}

	seenNames := make(map[string]bool, len(names))
	var uniqueNames []string
	for _, name := range names {
		if !seenNames[name] {
			seenNames[name] = true
			uniqueNames = append(uniqueNames, name)
		}
	}

	var decls []protectedDeclInfo
	for _, name := range uniqueNames {
		prefix, base, resolvedBase := resolveOMPSourceIdentity(name)
		targetBase, isMapped := mergedCloak[resolvedBase]
		var finalBase string
		if isMapped {
			finalBase = targetBase
		} else {
			finalBase = fallbackAliasForSource(base)
		}
		decls = append(decls, protectedDeclInfo{
			originalFullName: name,
			prefix:           prefix,
			base:             base,
			finalBase:        finalBase,
			transformed:      true,
		})
	}

	byFinalBase := make(map[string][]protectedDeclInfo)
	for _, d := range decls {
		byFinalBase[d.finalBase] = append(byFinalBase[d.finalBase], d)
	}
	for fb, group := range byFinalBase {
		if len(group) > 1 {
			return nil, fmt.Errorf("declaration collision for final base identity %q", fb)
		}
	}
	return decls, nil
}

func cloakProtectedToolNames(rootMap map[string]any, cloakTable map[string]string, sourceFormat string) bool {
	changed := false

	if toolsRaw, ok := rootMap["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			tMap, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if sourceFormat == "openai" {
				if fn, ok := tMap["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok {
						prefix, base := splitToolNamespace(name)
						if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
							fn["name"] = prefix + target
							changed = true
						}
					}
				}
			} else if sourceFormat == "anthropic" {
				if name, ok := tMap["name"].(string); ok {
					prefix, base := splitToolNamespace(name)
					if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
						tMap["name"] = prefix + target
						changed = true
					}
				}
			}
		}
	}

	if msgsRaw, ok := rootMap["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			msg, ok := mRaw.(map[string]any)
			if !ok {
				continue
			}
			if sourceFormat == "openai" {
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
							prefix, base := splitToolNamespace(name)
							if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
								fn["name"] = prefix + target
								changed = true
							}
						}
					}
				}
				if msg["role"] == "tool" {
					if name, ok := msg["name"].(string); ok {
						prefix, base := splitToolNamespace(name)
						if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
							msg["name"] = prefix + target
							changed = true
						}
					}
				}
			} else if sourceFormat == "anthropic" {
				if contents, ok := msg["content"].([]any); ok {
					for _, cntRaw := range contents {
						cnt, ok := cntRaw.(map[string]any)
						if !ok {
							continue
						}
						if cnt["type"] == "tool_use" {
							if name, ok := cnt["name"].(string); ok {
								prefix, base := splitToolNamespace(name)
								if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
									cnt["name"] = prefix + target
									changed = true
								}
							}
						}
					}
				}
			}
		}
	}

	if tc, ok := rootMap["tool_choice"].(map[string]any); ok {
		if sourceFormat == "openai" {
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					prefix, base := splitToolNamespace(name)
					if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
						fn["name"] = prefix + target
						changed = true
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			if name, ok := tc["name"].(string); ok {
				prefix, base := splitToolNamespace(name)
				if target, exists := lookupProtectedOMPTarget(base, cloakTable); exists {
					tc["name"] = prefix + target
					changed = true
				}
			}
		}
	}

	return changed
}

func validateProtectedPostTransform(rootMap map[string]any, sourceFormat string, uncloakedSources map[string]bool) bool {
	hasUncloakedSource := func(name string) bool {
		if uncloakedSources[name] {
			return true
		}
		_, base, resolvedBase := resolveOMPSourceIdentity(name)
		return uncloakedSources[base] || uncloakedSources[resolvedBase]
	}

	if toolsRaw, ok := rootMap["tools"].([]any); ok {
		for _, tRaw := range toolsRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if fn, ok := tMap["function"].(map[string]any); ok {
						if name, ok := fn["name"].(string); ok {
							if hasUncloakedSource(name) {
								return false
							}
						}
					}
				} else if sourceFormat == "anthropic" {
					if name, ok := tMap["name"].(string); ok {
						if hasUncloakedSource(name) {
							return false
						}
					}
				}
			}
		}
	}

	if msgsRaw, ok := rootMap["messages"].([]any); ok {
		for _, mRaw := range msgsRaw {
			if msg, ok := mRaw.(map[string]any); ok {
				if sourceFormat == "openai" {
					if calls, ok := msg["tool_calls"].([]any); ok {
						for _, cRaw := range calls {
							if call, ok := cRaw.(map[string]any); ok {
								if fn, ok := call["function"].(map[string]any); ok {
									if name, ok := fn["name"].(string); ok {
										if hasUncloakedSource(name) {
											return false
										}
									}
								}
							}
						}
					}
					if msg["role"] == "tool" {
						if name, ok := msg["name"].(string); ok {
							if hasUncloakedSource(name) {
								return false
							}
						}
					}
				} else if sourceFormat == "anthropic" {
					if contents, ok := msg["content"].([]any); ok {
						for _, cntRaw := range contents {
							if cnt, ok := cntRaw.(map[string]any); ok {
								if cnt["type"] == "tool_use" {
									if name, ok := cnt["name"].(string); ok {
										if hasUncloakedSource(name) {
											return false
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

	if tc, ok := rootMap["tool_choice"].(map[string]any); ok {
		if sourceFormat == "openai" {
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					if hasUncloakedSource(name) {
						return false
					}
				}
			}
		} else if sourceFormat == "anthropic" {
			if name, ok := tc["name"].(string); ok {
				if hasUncloakedSource(name) {
					return false
				}
			}
		}
	}

	return true
}

func handleProtectedAGY(req *pluginapi.RequestInterceptRequest, resp pluginapi.RequestInterceptResponse, format string) []byte {
	if req.RequestID == "" {
		debugLog("handleProtectedAGY: missing RequestID")
		return protected503Response(resp)
	}

	if format != "openai" && format != "anthropic" {
		debugLog("handleProtectedAGY: unsupported source format %q", format)
		return protected503Response(resp)
	}

	rootMap, ok := decodeStrictProtectedJSON(req.Body)
	if !ok {
		debugLog("handleProtectedAGY: strict JSON decode failed")
		return protected503Response(resp)
	}
	expectedChoiceCount := 1
	if n, valid := jsonIndexValue(rootMap["n"]); valid && n > 1 {
		expectedChoiceCount = n
	}

	cfg := activeFilterConfig()
	effective := cfg.ToolMappings["oh_my_pi"]
	if len(effective) < len(canonicalOMPSafeMappingSet) {
		debugLog("handleProtectedAGY: effective mapping count below canonical minimum: %d < %d", len(effective), len(canonicalOMPSafeMappingSet))
		return protected503Response(resp)
	}
	for orig, target := range canonicalOMPSafeMappingSet {
		if effective[orig] != target {
			debugLog("handleProtectedAGY: canonical mapping mismatch for %q: got %q, want %q", orig, effective[orig], target)
			return protected503Response(resp)
		}
	}

	// Build the merged cloak table: start from shared aliases, overlay operator config,
	// and ensure canonical nine are immutable.
	mergedCloak := make(map[string]string, len(canonicalOMPSafeMappingSet)+len(ompSharedAliases)+len(effective))
	for k, v := range ompSharedAliases {
		mergedCloak[k] = v
	}
	for k, v := range effective {
		if v == "" {
			debugLog("handleProtectedAGY: empty mapping target for %q", k)
			return protected503Response(resp)
		}
		mergedCloak[k] = v
	}
	for k, v := range canonicalOMPSafeMappingSet {
		mergedCloak[k] = v
	}

	decls, collisionErr := inspectAndValidateProtectedTools(rootMap, format, mergedCloak)
	if collisionErr != nil {
		debugLog("handleProtectedAGY: declaration collision: %v", collisionErr)
		return protected503Response(resp)
	}

	// Build the extended cloak table: start from merged (canonical + shared + config),
	// then add deterministic fallback aliases for unknown declarations.
	extendedCloak := make(map[string]string, len(mergedCloak)+len(decls))
	for k, v := range mergedCloak {
		extendedCloak[k] = v
	}
	for _, d := range decls {
		extendedCloak[d.base] = d.finalBase
		if strings.HasPrefix(d.base, "_") {
			candidate := strings.TrimPrefix(d.base, "_")
			if _, exists := extendedCloak[candidate]; !exists {
				extendedCloak[candidate] = d.finalBase
			}
		}
	}

	activeReverse := make(map[string]string, len(decls))
	for _, d := range decls {
		target := d.finalBase
		transformedFullName := d.prefix + target
		activeReverse[transformedFullName] = d.originalFullName
	}

	var protectedCachedUncloak *cachedUncloakPattern
	if len(activeReverse) > 0 {
		targets := make([]string, 0, len(activeReverse))
		lookup := make(map[string]string, len(activeReverse))
		for targetFull, origFull := range activeReverse {
			targets = append(targets, regexp.QuoteMeta(targetFull))
			lookup[targetFull] = origFull
		}
		pattern := `"name"\s*:\s*"(` + strings.Join(targets, "|") + `)"`
		if re, err := regexp.Compile(pattern); err == nil {
			protectedCachedUncloak = &cachedUncloakPattern{
				re:        re,
				lookup:    lookup,
				exactOnly: true,
			}
		}
	}

	cloakProtectedToolNames(rootMap, extendedCloak, format)
	rewriteProtectedBrand(rootMap, format, "oh_my_pi", cfg)
	runOMPSanitization := !carriesOpenAIResponses(req.SourceFormat)
	var sanitizationRules []ompSanitizationRule
	if runOMPSanitization {
		sanitizationRules = cfg.ompSanitizationRules
		sanitizeOMPPrompt(rootMap, format, sanitizationRules)
	}

	// Machine-generated prose has to name the request-scoped aliases, not the
	// client's own tool names. extendedCloak IS the effective declaration map
	// for this request: canonical, shared (wp_*) and the deterministic
	// wp_ext_<hash> fallbacks, so a tool description that says "hub" reaches
	// the model as its alias and the OMP source identity never appears in a
	// system/developer/tool-description surface (Issue #51). Brand mappings
	// are omitted: that pass already ran, and the alias-plan clients run it
	// the same way.
	proseCloak := &cachedCloakPatterns{
		cloakTable: extendedCloak,
		identRe:    buildCloakIdentRe(extendedCloak),
		ambigRe:    buildCloakAmbiguousRe(extendedCloak),
	}
	rewriteToolDescriptions(rootMap, nil, proseCloak, format)
	rewriteOMPConversationProse(rootMap, proseCloak, sanitizationRules)
	if sysVal, ok := rootMap["system"]; ok {
		if next, c := rewriteProseTextContent(sysVal, func(s string) (string, bool) {
			return replaceToolNamesInText(s, proseCloak)
		}); c {
			rootMap["system"] = next
		}
	}

	uncloakedSources := make(map[string]bool)
	for k := range canonicalOMPSafeMappingSet {
		uncloakedSources[k] = true
	}
	for k := range ompSharedAliases {
		uncloakedSources[k] = true
	}
	for k := range effective {
		uncloakedSources[k] = true
	}
	for _, d := range decls {
		uncloakedSources[d.originalFullName] = true
		uncloakedSources[d.base] = true
		_, _, res := resolveOMPSourceIdentity(d.base)
		uncloakedSources[res] = true
	}

	if !validateProtectedPostTransform(rootMap, format, uncloakedSources) {
		debugLog("handleProtectedAGY: post-transform validation failed")
		return protected503Response(resp)
	}

	canonicalBytes, err := safeMarshal(rootMap)
	if err != nil {
		debugLog("handleProtectedAGY: safeMarshal failed: %v", err)
		return protected503Response(resp)
	}
	resp.Body = canonicalBytes

	globalLifecycleManager.setRoute(req.RequestID, &explicitOMPRouteState{
		routeKind:               routeKindProtectedAGY,
		client:                  "oh_my_pi",
		activeReverse:           activeReverse,
		cachedUncloak:           protectedCachedUncloak,
		brandRestorationEnabled: true,
		expected:                expectedChoiceCount,
	})
	globalStreamManager.resetSession("req:"+req.RequestID, "oh_my_pi", protectedCachedUncloak, expectedChoiceCount)
	return MustEnvelope(resp)
}

func HandleRequestInterceptBefore(request []byte) []byte {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return MustErrorEnvelope("invalid_request", fmt.Sprintf("decode request.intercept_before request: %v", err))
	}

	resp := pluginapi.RequestInterceptResponse{}
	marker := parseExplicitClientMarker(req.Headers)
	if marker.present {
		resp.ClearHeaders = marker.matchedKeys
		resp.Headers = filteredHeaders(req.Headers, marker.matchedKeys)
	}

	format := normalizeSourceFormat(req.SourceFormat)
	isAllowedModel := modelAllowsCloak(req.Model, req.RequestedModel)
	debugLog("handleRequestInterceptBefore: SourceFormat=%s (normalized=%s) ToFormat=%q Model=%q RequestedModel=%q isAllowedModel=%t marker=%+v",
		req.SourceFormat, format, req.ToFormat, req.Model, req.RequestedModel, isAllowedModel, marker)

	if marker.isConflict && marker.conflictContainsOMP {
		if isAllowedModel {
			debugLog("handleRequestInterceptBefore: conflicting marker containing OMP on allowed model route -> exact 503")
			return protected503Response(resp)
		}
		debugLog("handleRequestInterceptBefore: conflicting marker containing OMP outside model_prefixes -> durable bypass")
		if req.RequestID != "" {
			globalLifecycleManager.setRoute(req.RequestID, &explicitOMPRouteState{
				routeKind: routeKindExplicitOMPNonAGYBypass,
				client:    "oh_my_pi",
			})
		}
		return MustEnvelope(resp)
	}

	if !marker.isConflict && marker.isOMP {
		if isAllowedModel {
			debugLog("handleRequestInterceptBefore: explicit OMP on allowed model route -> ProtectedAGY admission")
			return handleProtectedAGY(&req, resp, format)
		}
		debugLog("handleRequestInterceptBefore: explicit OMP outside model_prefixes -> durable bypass")
		if req.RequestID != "" {
			globalLifecycleManager.setRoute(req.RequestID, &explicitOMPRouteState{
				routeKind: routeKindExplicitOMPNonAGYBypass,
				client:    "oh_my_pi",
			})
		}
		return MustEnvelope(resp)
	}

	if !isAllowedModel {
		debugLog("handleRequestInterceptBefore: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return MustEnvelope(resp)
	}

	forcedClient := ""
	uaResolvedOMP := false
	if marker.present {
		if marker.valid {
			forcedClient = marker.client
		} else {
			debugLog("handleRequestInterceptBefore: invalid explicit client %q, falling back to body detection", marker.client)
		}
	} else if uaClient, ok := resolveUserAgentClient(req.Headers); ok {
		forcedClient = uaClient
		uaResolvedOMP = uaClient == "oh_my_pi"
		debugLog("handleRequestInterceptBefore: UA evidence client=%q", uaClient)
	}

	// Resolve the effective client. When forcedClient is set and has a table,
	// use it directly; otherwise fall back to body-based detection.
	effectiveClient := forcedClient
	if effectiveClient != "" && len(effectiveCloakTable(effectiveClient)) == 0 {
		effectiveClient = ""
	}
	if effectiveClient == "" {
		var reqRootAny any
		if err := safeUnmarshal(req.Body, &reqRootAny); err == nil {
			if rootMap, ok := reqRootAny.(map[string]any); ok {
				toolNames := extractToolNames(rootMap, format)
				effectiveClient = detectClient(toolNames)
			}
		}
	}

	// Alias-plan path: clients that declare a variable tool surface route
	// through the request-scoped alias plan machinery. This gives them
	// fail-closed admission, deterministic fallback aliases for unknown/MCP
	// declarations, and exact per-request reversal. Missing correlation is
	// rejected (parent #32 fail-closed requirement) rather than falling back
	// to the legacy partial-cloak path.
	if effectiveClient != "" && clientUsesAliasPlan(effectiveClient) {
		if req.RequestID == "" {
			debugLog("handleRequestInterceptBefore: alias plan client=%s with missing RequestID -> 503", effectiveClient)
			return toolCloak503Response(resp)
		}
		preferred := sharedAliasesFor(effectiveClient)
		plan, rejection := admitRequestAliasPlanOrReject(&req, resp, effectiveClient, preferred)
		if rejection != nil {
			debugLog("handleRequestInterceptBefore: alias plan rejected client=%s", effectiveClient)
			return rejection
		}
		if plan != nil {
			// The plan's forward map is the sole cloak authority for this
			// request: it contains static + shared + fallback mappings for
			// every declared tool. Parse once, cloak tools, rewrite brand,
			// marshal once.
			var planRoot any
			if err := safeUnmarshal(req.Body, &planRoot); err != nil {
				debugLog("handleRequestInterceptBefore: alias plan body parse failed: %v", err)
				return toolCloak503Response(resp)
			}
			rootMap, ok := planRoot.(map[string]any)
			if !ok {
				debugLog("handleRequestInterceptBefore: alias plan body is not an object")
				return toolCloak503Response(resp)
			}

			changed := false
			// Issue #55 Contract B: translate the eligible declaration's
			// model-tier enum FIRST, while the declaration still carries its
			// native identity, so CPA's provider conversion derives its help
			// from the translated enum. The tool-call history is re-encoded
			// the same way after name cloaking has resolved the identities.
			// Skipped on the deferred /v1/responses surface.
			responsesCarrier := carriesOpenAIResponses(req.SourceFormat)
			if !responsesCarrier && plan.tier != nil && translateAgentTierEnum(rootMap, plan.tier, format) {
				changed = true
			}
			if len(plan.forward) > 0 {
				toolCloaked := cloakToolNames(rootMap, plan.forward, format)
				changed = changed || toolCloaked
			}

			if !responsesCarrier && plan.tier != nil && applyTierArgsForwardHistory(rootMap, plan.tier, format) {
				changed = true
			}

			// Brand rewriting (same steps as rewriteRequestBodyForClient).
			cfg := activeFilterConfig()
			mappings := effectiveMappings(cfg, effectiveClient)
			rewritten, sysChanged := rewriteSystemFields(rootMap, mappings)
			rootMap = rewritten.(map[string]any)
			changed = changed || sysChanged

			// Build a temporary cachedCloakPatterns from the plan's declared forward
			// table for text-level tool-name replacement in descriptions and
			// system messages. Only tools declared in the current tools[] surface
			// have replacement authority in prose/system fields.
			planCloakCache := &cachedCloakPatterns{
				cloakTable: plan.declaredForward,
				identRe:    buildCloakIdentRe(plan.declaredForward),
				ambigRe:    buildCloakAmbiguousRe(plan.declaredForward),
			}

			descChanged := rewriteToolDescriptions(rootMap, mappings, planCloakCache, format)
			changed = changed || descChanged

			sysMsgChanged := rewriteConversationContent(rootMap, mappings, planCloakCache)
			changed = changed || sysMsgChanged

			if sysVal, ok := rootMap["system"]; ok {
				next, sysToolChanged := replaceToolNamesInValue(sysVal, planCloakCache)
				if sysToolChanged {
					rootMap["system"] = next
					changed = true
				}
			}

			// Issue #55 Contract A: after the prose rewrites, the recognized
			// news sentence (native or default-cloaked spelling) has its URL
			// replaced with the neutral root. Runs only for a claude_code
			// request with the default keyword set enabled, and it is an
			// authority of its own: no custom/prose pass runs over the freshly
			// sanitized URL. Skipped on the deferred /v1/responses surface.
			if !responsesCarrier && effectiveClient == "claude_code" && cfg.UseDefaultKeywords && sanitizeSystemNewsURL(rootMap, format) {
				changed = true
			}

			debugLog("handleRequestInterceptBefore: alias plan rewritten=%t client=%s", changed, effectiveClient)
			if !changed {
				return MustEnvelope(resp)
			}
			raw, err := safeMarshal(rootMap)
			if err != nil {
				debugLog("handleRequestInterceptBefore: alias plan marshal failed: %v", err)
				return toolCloak503Response(resp)
			}
			resp.Body = raw
			return MustEnvelope(resp)
		}
	}

	// Legacy path: clients without alias-plan support.
	runOMPPromptSanitization := uaResolvedOMP &&
		(format == "openai" || format == "anthropic") &&
		!carriesOpenAIResponses(req.SourceFormat)
	body, rewritten, client := rewriteRequestBodyForClient(req.Body, format, forcedClient, runOMPPromptSanitization)
	debugLog("handleRequestInterceptBefore: rewritten=%t client=%s Body=%s", rewritten, client, string(body))
	if req.RequestID != "" {
		if client != "" {
			cached := requestScopedUncloakPattern(client, req.Body, format)
			if cached != nil && cached.re != nil {
				globalStreamManager.resetSession("req:"+req.RequestID, client, cached, requestChoiceCount(req.Body))
			}
		} else if marker.present && !marker.valid {
			debugLog("handleRequestInterceptBefore: negative client resolution recorded for RequestID=%s", req.RequestID)
			globalStreamManager.resetSession("req:"+req.RequestID, negativeClientResolution, nil, requestChoiceCount(req.Body))
		}
	}
	if !rewritten {
		return MustEnvelope(resp)
	}
	resp.Body = body
	return MustEnvelope(resp)
}

func HandleResponseIntercept(request []byte) []byte {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return MustErrorEnvelope("invalid_request", err.Error())
	}

	format := normalizeSourceFormat(req.SourceFormat)
	debugLog("handleResponseIntercept: SourceFormat=%s (normalized=%s) RequestBody=%s Body=%s RequestID=%s",
		req.SourceFormat, format, string(req.RequestBody), string(req.Body), req.RequestID)

	if req.RequestID != "" {
		if route := globalLifecycleManager.getRoute(req.RequestID); route != nil {
			if route.routeKind == routeKindExplicitOMPNonAGYBypass {
				debugLog("handleResponseIntercept: correlated ExplicitOMPNonAGYBypass -> zero mutation")
				return MustEnvelope(pluginapi.ResponseInterceptResponse{})
			}
			if route.routeKind == routeKindProtectedAGY {
				if route.malformed || route.client != "oh_my_pi" {
					debugLog("invariant violation: malformed ProtectedAGY route state for RequestID=%s", req.RequestID)
					return MustEnvelope(pluginapi.ResponseInterceptResponse{})
				}
				modified := req.Body
				changed := false
				if len(route.activeReverse) > 0 {
					if m, c := uncloakResponseBodyExact(req.Body, route.activeReverse, format); c {
						modified = m
						changed = true
					}
				}
				if route.brandRestorationEnabled {
					if rev, c := reverseCloakedBrandBody(modified, route.client); c {
						modified = rev
						changed = true
					}
				}
				if !changed {
					return MustEnvelope(pluginapi.ResponseInterceptResponse{})
				}
				return MustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
			}
		}
		if plan := globalAliasPlanManager.get(req.RequestID); plan != nil {
			// A pinned plan is the sole reverse authority for tool names. If
			// it has no match, do not fall through to global/static inversion:
			// a target untouched by this request must remain untouched.
			// Issue #55 Contract B: the tier pass runs on the RAW upstream body,
			// where a wire name is the identity the request actually admitted.
			// Running it after restoration would let a raw name that merely
			// equals the client's native spelling borrow the pinned authority.
			modified := req.Body
			uncloaked := false
			if next, c := applyTierResponseBodyBytes(modified, plan.tier); c {
				modified = next
				uncloaked = true
			}
			if m, c := uncloakResponseBodyExact(modified, plan.reverse, plan.sourceFormat); c {
				modified = m
				uncloaked = true
			}
			// Brand text introduced by the request path is restored here
			// independently of the tool-name plan: none of those tokens can
			// appear in plan.reverse, so the two authorities never collide.
			if rev, c := reverseCloakedBrandBody(modified, plan.client); c {
				return MustEnvelope(pluginapi.ResponseInterceptResponse{Body: rev})
			}
			if uncloaked {
				return MustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
			}
			return MustEnvelope(pluginapi.ResponseInterceptResponse{})
		}
		// RequestID is present, but neither route nor alias plan exists.
		// For alias-plan clients (Claude Code, Codex), full-declaration restoration
		// requires pinned request authority. If authority is missing (e.g. after
		// request.complete cleanup or missing plan), it MUST pass through rather
		// than guess or reverse static AGY targets (Issue #39).
		if sess := globalStreamManager.getSession("req:" + req.RequestID); sess != nil && clientUsesAliasPlan(sess.client) {
			debugLog("handleResponseIntercept: alias-plan client=%q with missing authority RequestID=%s -> pass-through", sess.client, req.RequestID)
			return MustEnvelope(pluginapi.ResponseInterceptResponse{})
		}
	}

	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleResponseIntercept: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return MustEnvelope(pluginapi.ResponseInterceptResponse{})
	}

	if marker := parseExplicitClientMarker(req.RequestHeaders); marker.present && clientUsesAliasPlan(marker.client) {
		debugLog("handleResponseIntercept: alias-plan explicit marker=%q without pinned authority -> pass-through", marker.client)
		return MustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	var client string
	var uncloakTable map[string]string
	correlated := false
	if req.RequestID != "" {
		if sess := globalStreamManager.getSession("req:" + req.RequestID); sess != nil && sess.client != "" {
			correlated = true
			if sess.client == negativeClientResolution {
				debugLog("handleResponseIntercept: negative client resolution authoritative for RequestID=%s", req.RequestID)
				return MustEnvelope(pluginapi.ResponseInterceptResponse{})
			}
			client = sess.client
			// Reuse the reverse the request interceptor derived from the RAW
			// client body. The executed body below no longer carries source
			// names, so re-deriving the table from it cannot tell an AGY
			// target the client declared natively from one the cloak created.
			if sess.cached != nil && len(sess.cached.lookup) > 0 {
				uncloakTable = sess.cached.lookup
			} else {
				uncloakTable = requestScopedUncloakTable(sess.client, detectionRequestBody(req.OriginalRequest, req.RequestBody), format)
			}
		}
	}
	if !correlated && uncloakTable == nil {
		if uaClient, ok := resolveUserAgentClient(req.RequestHeaders); ok {
			uncloakTable = requestScopedUncloakTable(uaClient, detectionRequestBody(req.OriginalRequest, req.RequestBody), format)
			if client == "" {
				client = uaClient
			}
			debugLog("handleResponseIntercept: UA evidence client=%q", uaClient)
		}
	}
	if !correlated && uncloakTable == nil {
		var detectedClient string
		uncloakTable, detectedClient = buildUncloakTable(detectionRequestBody(req.OriginalRequest, req.RequestBody), format)
		if client == "" {
			client = detectedClient
		}
	}
	debugLog("handleResponseIntercept: client=%s uncloakTable=%v", client, uncloakTable)
	if uncloakTable == nil && client != "oh_my_pi" {
		return MustEnvelope(pluginapi.ResponseInterceptResponse{})
	}

	modified := req.Body
	changed := false
	if uncloakTable != nil {
		if m, c := uncloakResponseBody(req.Body, uncloakTable, format); c {
			modified = m
			changed = true
		}
	}
	// One brand reverse for every resolved client. The table it walks is
	// selected by the client inside the walk (the protected pair and home
	// directory for oh_my_pi, the client's own table otherwise), so there is
	// no second authority to choose between here.
	if rev, c := reverseCloakedBrandBody(modified, client); c {
		modified = rev
		changed = true
	}
	debugLog("handleResponseIntercept: changed=%t Body=%s", changed, string(modified))
	if !changed {
		return MustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	return MustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
}

func HandleStreamChunkIntercept(request []byte) []byte {
	var req pluginapi.StreamChunkInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return MustErrorEnvelope("invalid_request", err.Error())
	}

	format := normalizeSourceFormat(req.SourceFormat)
	debugLog("handleStreamChunkIntercept: SourceFormat=%s (normalized=%s) ChunkIndex=%d Body=%s RequestID=%s",
		req.SourceFormat, format, req.ChunkIndex, string(req.Body), req.RequestID)

	if req.RequestID != "" {
		if route := globalLifecycleManager.getRoute(req.RequestID); route != nil {
			if route.routeKind == routeKindExplicitOMPNonAGYBypass {
				debugLog("handleStreamChunkIntercept: correlated ExplicitOMPNonAGYBypass -> zero mutation")
				return MustEnvelope(pluginapi.StreamChunkInterceptResponse{})
			}
			if route.routeKind == routeKindProtectedAGY {
				if route.malformed || route.client != "oh_my_pi" {
					debugLog("invariant violation: malformed ProtectedAGY route state for RequestID=%s", req.RequestID)
					return MustEnvelope(pluginapi.StreamChunkInterceptResponse{})
				}
				resp := globalStreamManager.processChunk(&req, format)
				return MustEnvelope(resp)
			}
		}
		if plan := globalAliasPlanManager.get(req.RequestID); plan != nil {
			resp := globalStreamManager.processChunk(&req, plan.sourceFormat)
			return MustEnvelope(resp)
		}
		// RequestID is present, but neither route nor alias plan exists.
		// For alias-plan clients (Claude Code, Codex), full-declaration restoration
		// requires pinned request authority. If authority is missing (e.g. after
		// request.complete cleanup or missing plan), it MUST pass through rather
		// than guess or reverse static AGY targets (Issue #39).
		if sess := globalStreamManager.getSession("req:" + req.RequestID); sess != nil && clientUsesAliasPlan(sess.client) {
			debugLog("handleStreamChunkIntercept: alias-plan client=%q with missing authority RequestID=%s -> pass-through", sess.client, req.RequestID)
			return MustEnvelope(pluginapi.StreamChunkInterceptResponse{})
		}
	}

	if !modelAllowsCloak(req.Model, req.RequestedModel) {
		debugLog("handleStreamChunkIntercept: model gate skip Model=%q RequestedModel=%q", req.Model, req.RequestedModel)
		return MustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}

	if marker := parseExplicitClientMarker(req.RequestHeaders); marker.present && clientUsesAliasPlan(marker.client) {
		debugLog("handleStreamChunkIntercept: alias-plan explicit marker=%q without pinned authority -> pass-through", marker.client)
		return MustEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	resp := globalStreamManager.processChunk(&req, format)
	return MustEnvelope(resp)
}
