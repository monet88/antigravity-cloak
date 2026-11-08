package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	wireFamilyAnthropic       = "anthropic"
	wireFamilyOpenAIChat      = "openai-chat"
	wireFamilyOpenAIResponses = "openai-responses"
	wireFamilyUnknown         = "unknown"

	responsesChatToolNameLimit = 64
	responsesLanePrefix        = "responses:"

	responsesArgKindFunction = "function_call_arguments"
	responsesArgKindCustom   = "custom_tool_call_input"
)

var geminiFunctionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.:-]`)

func requestWireFamily(format string, root map[string]any) string {
	switch format {
	case "anthropic":
		return wireFamilyAnthropic
	case "openai":
		if root == nil {
			return wireFamilyUnknown
		}
		_, isChat := root["messages"].([]any)
		isResp := false
		switch root["input"].(type) {
		case []any, string:
			isResp = true
		}
		if _, ok := root["instructions"].(string); ok {
			isResp = true
		}
		if isChat && isResp {
			return wireFamilyUnknown
		}
		if isChat {
			return wireFamilyOpenAIChat
		}
		if isResp {
			return wireFamilyOpenAIResponses
		}
		return wireFamilyUnknown
	default:
		return wireFamilyUnknown
	}
}

func responseWireFamily(format string, root map[string]any) string {
	switch format {
	case "anthropic":
		return wireFamilyAnthropic
	case "openai":
		if root == nil {
			return wireFamilyUnknown
		}
		_, isChat := root["choices"].([]any)
		_, isResp := root["output"].([]any)
		if obj, ok := root["object"].(string); ok && obj == "response" {
			isResp = true
		}
		if isChat && isResp {
			return wireFamilyUnknown
		}
		if isChat {
			return wireFamilyOpenAIChat
		}
		if isResp {
			return wireFamilyOpenAIResponses
		}
		return wireFamilyUnknown
	default:
		return wireFamilyUnknown
	}
}

func streamEventFamily(format string, root map[string]any) string {
	switch format {
	case "anthropic":
		return wireFamilyAnthropic
	case "openai":
		if root == nil {
			return wireFamilyUnknown
		}
		_, isChat := root["choices"].([]any)
		typ, isStr := root["type"].(string)
		isResp := isStr && strings.HasPrefix(typ, "response.")
		if isChat && isResp {
			return wireFamilyUnknown
		}
		if isChat {
			return wireFamilyOpenAIChat
		}
		if isResp {
			return wireFamilyOpenAIResponses
		}
		return wireFamilyUnknown
	default:
		return wireFamilyUnknown
	}
}

func isWebSocketUpgradeRequest(headers http.Header) bool {
	if headers == nil {
		return false
	}
	hasUpgradeWebsocket := false
	hasConnectionUpgrade := false
	for k, vs := range headers {
		if strings.EqualFold(k, "Upgrade") {
			for _, v := range vs {
				for _, part := range strings.Split(v, ",") {
					if strings.EqualFold(strings.TrimSpace(part), "websocket") {
						hasUpgradeWebsocket = true
					}
				}
			}
		} else if strings.EqualFold(k, "Connection") {
			for _, v := range vs {
				for _, part := range strings.Split(v, ",") {
					if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
						hasConnectionUpgrade = true
					}
				}
			}
		}
	}
	return hasUpgradeWebsocket && hasConnectionUpgrade
}

type responsesSourceIdentity struct {
	Namespace string
	LocalName string
}

type responsesSelectorRecord struct {
	originalName  string
	rewrittenName string
	rewritten     bool
}

type responsesDeclOccurrence struct {
	toolMap         map[string]any
	fnMap           map[string]any
	useTopLevelName bool
	effectiveName   string
	namespace       string
	custom          bool
	sourcePriority  int
	direct          bool
	chatIncluded    bool
	order           int
	ownerID         responsesSourceIdentity
}

type responsesHistoryRef struct {
	item      map[string]any
	name      string
	namespace string
	target    string
	rewrite   bool
}

type responsesSelectorMutation struct {
	tc            map[string]any
	fnMap         map[string]any
	customMap     map[string]any
	hasFlatName   bool
	hasFnName     bool
	hasCustomName bool
	originalName  string
	targetName    string
	rewrite       bool
}

type responsesMutationPlan struct {
	plan             *requestAliasPlan
	declOccurrences  []responsesDeclOccurrence
	historyRefs      []responsesHistoryRef
	selectorMutation *responsesSelectorMutation
}

func responsesEffectiveToolName(tool map[string]any) (name string, useTopLevel bool, fnMap map[string]any) {
	if rawName, ok := tool["name"].(string); ok {
		if trimmed := strings.TrimSpace(rawName); trimmed != "" {
			fn, _ := tool["function"].(map[string]any)
			return trimmed, true, fn
		}
	}
	if fn, ok := tool["function"].(map[string]any); ok {
		if rawName, ok := fn["name"].(string); ok {
			if trimmed := strings.TrimSpace(rawName); trimmed != "" {
				return trimmed, false, fn
			}
		}
		return "", false, fn
	}
	return "", false, nil
}

func rawResponsesNamespaceQualifiedName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	namespaceName = strings.TrimSpace(namespaceName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if childName == namespaceName || strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func capResponsesChatToolName(name string) string {
	if len(name) <= responsesChatToolNameLimit {
		return name
	}
	truncated := name[len(name)-responsesChatToolNameLimit:]
	if trimmed := strings.TrimLeft(truncated, "_-"); trimmed != "" {
		return trimmed
	}
	return truncated
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	return capResponsesChatToolName(rawResponsesNamespaceQualifiedName(namespaceName, childName))
}

func sanitizeGeminiFunctionName(name string) string {
	if name == "" {
		return ""
	}
	sanitized := geminiFunctionNameSanitizer.ReplaceAllString(name, "_")
	if len(sanitized) > 0 {
		first := sanitized[0]
		if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
			if len(sanitized) >= 64 {
				sanitized = sanitized[:63]
			}
			sanitized = "_" + sanitized
		}
	} else {
		sanitized = "_"
	}
	if len(sanitized) > 64 {
		sanitized = sanitized[:64]
	}
	return sanitized
}

func disambiguateGeminiSanitizedName(base, original string, used map[string]string) string {
	for attempt := 0; ; attempt++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", original, attempt)))
		suffix := "_" + hex.EncodeToString(digest[:6])
		prefix := base
		if maxPrefix := 64 - len(suffix); len(prefix) > maxPrefix {
			prefix = prefix[:maxPrefix]
		}
		candidate := prefix + suffix
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func sanitizeGeminiResponsesToolNames(names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	uniqueNames := make(map[string]struct{}, len(names))
	baseCounts := make(map[string]int, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, exists := uniqueNames[name]; exists {
			continue
		}
		uniqueNames[name] = struct{}{}
		baseCounts[sanitizeGeminiFunctionName(name)]++
	}
	sortedNames := make([]string, 0, len(uniqueNames))
	for name := range uniqueNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	out := make(map[string]string, len(sortedNames))
	used := make(map[string]string, len(sortedNames))
	for _, name := range sortedNames {
		base := sanitizeGeminiFunctionName(name)
		mapped := base
		_, baseUsed := used[base]
		if baseCounts[base] > 1 || baseUsed {
			mapped = disambiguateGeminiSanitizedName(base, name, used)
		}
		out[name] = mapped
		used[mapped] = name
	}
	return out
}

func isSupportedResponsesBuiltInToolType(typ string) bool {
	switch typ {
	case "web_search", "web_search_preview", "web_search_preview_2025_03_11",
		"file_search", "mcp", "local_shell", "computer_use_preview", "code_interpreter", "image_generation":
		return true
	default:
		return false
	}
}

func collectResponsesDeclOccurrences(root map[string]any, strict bool) ([]responsesDeclOccurrence, error) {
	var out []responsesDeclOccurrence
	scanSlice := func(toolsRaw []any, sourcePriority int) error {
		for _, raw := range toolsRaw {
			tMap, ok := raw.(map[string]any)
			if !ok {
				if strict {
					return fmt.Errorf("unsupported tool declaration entry")
				}
				continue
			}
			typ := ""
			if rawType, hasType := tMap["type"]; hasType && rawType != nil {
				s, ok := rawType.(string)
				if !ok {
					if strict {
						return fmt.Errorf("unsupported tool type representation")
					}
					continue
				}
				typ = strings.TrimSpace(s)
			}
			switch typ {
			case "", "function", "custom":
				name, useTop, fnMap := responsesEffectiveToolName(tMap)
				if name == "" {
					if strict {
						return fmt.Errorf("tool declaration is missing a name")
					}
					continue
				}
				out = append(out, responsesDeclOccurrence{
					toolMap:         tMap,
					fnMap:           fnMap,
					useTopLevelName: useTop,
					effectiveName:   name,
					namespace:       "",
					custom:          typ == "custom",
					sourcePriority:  sourcePriority,
					direct:          true,
					chatIncluded:    true,
					order:           len(out),
					ownerID:         responsesSourceIdentity{Namespace: "", LocalName: name},
				})
			case "namespace":
				nsName := ""
				if rawNS, ok := tMap["name"].(string); ok {
					nsName = strings.TrimSpace(rawNS)
				}
				var children []any
				chatInc := false
				if toolsArr, ok := tMap["tools"].([]any); ok {
					children = toolsArr
					chatInc = true
				} else if childrenArr, ok := tMap["children"].([]any); ok {
					children = childrenArr
					chatInc = false
				} else if strict {
					if _, hasTools := tMap["tools"]; hasTools && tMap["tools"] != nil {
						return fmt.Errorf("unsupported namespace tools representation")
					}
					if _, hasChildren := tMap["children"]; hasChildren && tMap["children"] != nil {
						return fmt.Errorf("unsupported namespace children representation")
					}
				}
				for _, cRaw := range children {
					cMap, ok := cRaw.(map[string]any)
					if !ok {
						if strict {
							return fmt.Errorf("unsupported namespace child declaration")
						}
						continue
					}
					cType := ""
					if rawCType, hasCType := cMap["type"]; hasCType && rawCType != nil {
						s, ok := rawCType.(string)
						if !ok {
							if strict {
								return fmt.Errorf("unsupported namespace child type")
							}
							continue
						}
						cType = strings.TrimSpace(s)
					}
					switch cType {
					case "", "function", "custom":
						cName, useTop, fnMap := responsesEffectiveToolName(cMap)
						if cName == "" {
							if strict {
								return fmt.Errorf("namespace child declaration is missing a name")
							}
							continue
						}
						out = append(out, responsesDeclOccurrence{
							toolMap:         cMap,
							fnMap:           fnMap,
							useTopLevelName: useTop,
							effectiveName:   cName,
							namespace:       nsName,
							custom:          cType == "custom",
							sourcePriority:  sourcePriority,
							direct:          false,
							chatIncluded:    chatInc,
							order:           len(out),
							ownerID:         responsesSourceIdentity{Namespace: nsName, LocalName: cName},
						})
					default:
						if strict && !isSupportedResponsesBuiltInToolType(cType) {
							return fmt.Errorf("unsupported namespace child tool type %q", cType)
						}
					}
				}
			default:
				if strict && !isSupportedResponsesBuiltInToolType(typ) {
					return fmt.Errorf("unsupported responses tool type %q", typ)
				}
			}
		}
		return nil
	}

	if toolsVal, exists := root["tools"]; exists && toolsVal != nil {
		toolsArr, ok := toolsVal.([]any)
		if !ok {
			if strict {
				return nil, fmt.Errorf("unsupported tools representation")
			}
		} else if err := scanSlice(toolsArr, 0); err != nil {
			return nil, err
		}
	}

	if inputVal, exists := root["input"]; exists && inputVal != nil {
		if inputArr, ok := inputVal.([]any); ok {
			for _, rawItem := range inputArr {
				item, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				if typ, _ := item["type"].(string); typ == "additional_tools" {
					toolsVal, exists := item["tools"]
					if !exists || toolsVal == nil {
						continue
					}
					toolsArr, ok := toolsVal.([]any)
					if !ok {
						if strict {
							return nil, fmt.Errorf("unsupported additional_tools representation")
						}
						continue
					}
					if err := scanSlice(toolsArr, 1); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	return out, nil
}

func extractResponsesToolNamesForDetection(root map[string]any) []string {
	decls, _ := collectResponsesDeclOccurrences(root, false)
	var names []string
	for _, d := range decls {
		names = append(names, d.effectiveName)
		if d.namespace != "" {
			qual := rawResponsesNamespaceQualifiedName(d.namespace, d.effectiveName)
			if qual != d.effectiveName {
				names = append(names, qual)
			}
		}
	}
	if len(names) == 0 {
		if inputArr, ok := root["input"].([]any); ok {
			for _, rawItem := range inputArr {
				item, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := item["type"].(string)
				if typ == "function_call" || typ == "custom_tool_call" {
					name, _ := item["name"].(string)
					name = strings.TrimSpace(name)
					if name != "" {
						names = append(names, name)
						if ns, _ := item["namespace"].(string); strings.TrimSpace(ns) != "" {
							qual := rawResponsesNamespaceQualifiedName(strings.TrimSpace(ns), name)
							if qual != name {
								names = append(names, qual)
							}
						}
					}
				}
			}
		}
	}
	return names
}

type chatProjectionDecl struct {
	ownerID        responsesSourceIdentity
	localName      string
	namespace      string
	chatName       string
	custom         bool
	sourcePriority int
	direct         bool
	order          int
}

type chatProjectionIndex struct {
	declarations        []chatProjectionDecl
	byChat              map[string]chatProjectionDecl
	byIdentity          map[responsesSourceIdentity]string
	byRaw               map[string]string
	byLocal             map[string]string
	baseCollisionOwners map[string][]responsesSourceIdentity
}

func buildChatProjectionIndex(occs []responsesDeclOccurrence, nameFor func(responsesDeclOccurrence) string) *chatProjectionIndex {
	var decls []chatProjectionDecl
	baseOwners := make(map[string][]responsesSourceIdentity)
	seenBaseOwner := make(map[string]map[responsesSourceIdentity]bool)

	for _, occ := range occs {
		if !occ.chatIncluded {
			continue
		}
		local := strings.TrimSpace(nameFor(occ))
		if local == "" {
			continue
		}
		cName := qualifyResponsesNamespaceToolName(occ.namespace, local)
		d := chatProjectionDecl{
			ownerID:        occ.ownerID,
			localName:      local,
			namespace:      occ.namespace,
			chatName:       cName,
			custom:         occ.custom,
			sourcePriority: occ.sourcePriority,
			direct:         occ.direct,
			order:          occ.order,
		}
		decls = append(decls, d)
		if seenBaseOwner[cName] == nil {
			seenBaseOwner[cName] = make(map[responsesSourceIdentity]bool)
		}
		if !seenBaseOwner[cName][occ.ownerID] {
			seenBaseOwner[cName][occ.ownerID] = true
			baseOwners[cName] = append(baseOwners[cName], occ.ownerID)
		}
	}

	// Disambiguate Chat tool names using the pinned CLIProxyAPI v8.0.4 algorithm.
	claimed := make(map[string]string, len(decls))
	claim := func(candidate, identity string) bool {
		if ownerClaim, taken := claimed[candidate]; !taken {
			claimed[candidate] = identity
			return true
		} else {
			return ownerClaim == identity
		}
	}
	longDeclarations := make([]int, 0)
	identities := make([]string, len(decls))
	localOwners := make(map[string]string)
	ambiguousLocalNames := make(map[string]struct{})
	for i := range decls {
		identity := rawResponsesNamespaceQualifiedName(decls[i].namespace, decls[i].localName)
		identities[i] = identity
		if len(identity) > responsesChatToolNameLimit {
			longDeclarations = append(longDeclarations, i)
		} else {
			claim(identity, identity)
		}
		local := decls[i].localName
		if local == "" || local == identity || len(local) > responsesChatToolNameLimit {
			continue
		}
		if ownerLocal, seen := localOwners[local]; !seen {
			localOwners[local] = identity
		} else if ownerLocal != "" && ownerLocal != identity {
			localOwners[local] = ""
		}
	}
	for local, ownerLocal := range localOwners {
		claim(local, ownerLocal)
		if ownerLocal == "" {
			ambiguousLocalNames[local] = struct{}{}
		}
	}
	isAmbiguous := func(name string) bool {
		_, ambiguous := ambiguousLocalNames[name]
		return ambiguous
	}
	for _, i := range longDeclarations {
		identity := identities[i]
		name := decls[i].chatName
		if !isAmbiguous(name) && claim(name, identity) {
			continue
		}
		for suffix := 1; ; suffix++ {
			candidate := capResponsesChatToolName(name + "_" + strconv.Itoa(suffix))
			if isAmbiguous(candidate) {
				continue
			}
			if claim(candidate, identity) {
				decls[i].chatName = candidate
				break
			}
		}
	}

	idx := &chatProjectionIndex{
		declarations:        decls,
		byChat:              make(map[string]chatProjectionDecl, len(decls)),
		byIdentity:          make(map[responsesSourceIdentity]string, len(decls)),
		byRaw:               make(map[string]string, len(decls)),
		byLocal:             make(map[string]string, len(decls)),
		baseCollisionOwners: baseOwners,
	}
	for _, d := range decls {
		id := responsesSourceIdentity{Namespace: d.namespace, LocalName: d.localName}
		if _, exists := idx.byIdentity[id]; !exists {
			idx.byIdentity[id] = d.chatName
		}
		raw := rawResponsesNamespaceQualifiedName(d.namespace, d.localName)
		if _, exists := idx.byRaw[raw]; !exists {
			idx.byRaw[raw] = d.chatName
		}
		if _, exists := idx.byChat[d.chatName]; exists {
			continue
		}
		idx.byChat[d.chatName] = d
		if _, exists := idx.byLocal[d.localName]; exists {
			idx.byLocal[d.localName] = ""
		} else {
			idx.byLocal[d.localName] = d.chatName
		}
	}
	return idx
}

func (idx *chatProjectionIndex) avoidAlias(candidate string) string {
	if _, taken := idx.byChat[candidate]; !taken {
		return candidate
	}
	for suffix := 1; ; suffix++ {
		variant := capResponsesChatToolName(candidate + "_" + strconv.Itoa(suffix))
		if _, taken := idx.byChat[variant]; !taken {
			return variant
		}
	}
}

func (idx *chatProjectionIndex) namespaceName(namespace, name string) string {
	if chatName, ok := idx.byIdentity[responsesSourceIdentity{Namespace: namespace, LocalName: name}]; ok {
		return chatName
	}
	return idx.avoidAlias(qualifyResponsesNamespaceToolName(namespace, name))
}

func (idx *chatProjectionIndex) canonicalName(name string) string {
	if _, ok := idx.byChat[name]; ok {
		return name
	}
	if chatName, ok := idx.byRaw[name]; ok {
		return chatName
	}
	if chatName := idx.byLocal[name]; chatName != "" {
		return chatName
	}
	return idx.avoidAlias(capResponsesChatToolName(name))
}

func (idx *chatProjectionIndex) resolveOwner(namespace, name string) (responsesSourceIdentity, bool) {
	var emitted string
	if namespace != "" {
		emitted = idx.namespaceName(namespace, name)
	} else {
		emitted = idx.canonicalName(name)
	}
	win, ok := idx.byChat[emitted]
	if !ok {
		return responsesSourceIdentity{}, false
	}
	return win.ownerID, true
}

type geminiProjectionDesc struct {
	ownerID        responsesSourceIdentity
	name           string
	localName      string
	namespace      string
	custom         bool
	sourcePriority int
	direct         bool
	order          int
}

type geminiProjectionIndex struct {
	winners       map[string]geminiProjectionDesc
	winningList   []geminiProjectionDesc
	forwardMap    map[string]string
	reverseMap    map[string]geminiProjectionDesc
	qualOwnersMap map[string][]responsesSourceIdentity
	geminiOwners  map[string][]responsesSourceIdentity
}

func geminiDescPrecedes(left, right geminiProjectionDesc) bool {
	if left.sourcePriority != right.sourcePriority {
		return left.sourcePriority < right.sourcePriority
	}
	if left.direct != right.direct {
		return left.direct
	}
	return left.order < right.order
}

func buildGeminiProjectionIndex(occs []responsesDeclOccurrence, nameFor func(responsesDeclOccurrence) string) *geminiProjectionIndex {
	var descriptors []geminiProjectionDesc
	qualOwners := make(map[string][]responsesSourceIdentity)
	seenQualOwner := make(map[string]map[responsesSourceIdentity]bool)

	for _, occ := range occs {
		local := strings.TrimSpace(nameFor(occ))
		if local == "" {
			continue
		}
		qName := rawResponsesNamespaceQualifiedName(occ.namespace, local)
		if qName == "" {
			continue
		}
		d := geminiProjectionDesc{
			ownerID:        occ.ownerID,
			name:           qName,
			localName:      local,
			namespace:      occ.namespace,
			custom:         occ.custom,
			sourcePriority: occ.sourcePriority,
			direct:         occ.direct,
			order:          len(descriptors),
		}
		descriptors = append(descriptors, d)
		if seenQualOwner[qName] == nil {
			seenQualOwner[qName] = make(map[responsesSourceIdentity]bool)
		}
		if !seenQualOwner[qName][occ.ownerID] {
			seenQualOwner[qName][occ.ownerID] = true
			qualOwners[qName] = append(qualOwners[qName], occ.ownerID)
		}
	}

	winners := make(map[string]geminiProjectionDesc, len(descriptors))
	for _, desc := range descriptors {
		cur, exists := winners[desc.name]
		if !exists || geminiDescPrecedes(desc, cur) {
			winners[desc.name] = desc
		}
	}

	seenNames := make(map[string]struct{}, len(winners))
	var winningList []geminiProjectionDesc
	for _, desc := range descriptors {
		win, ok := winners[desc.name]
		if !ok || win.order != desc.order {
			continue
		}
		if _, seen := seenNames[desc.name]; seen {
			continue
		}
		seenNames[desc.name] = struct{}{}
		winningList = append(winningList, desc)
	}

	qualifiedNames := make([]string, 0, len(winningList))
	for _, desc := range winningList {
		qualifiedNames = append(qualifiedNames, desc.name)
	}
	sanitizedMap := sanitizeGeminiResponsesToolNames(qualifiedNames)

	forwardMap := make(map[string]string, len(winningList)*2)
	reverseMap := make(map[string]geminiProjectionDesc, len(winningList)*2)
	geminiBaseOwners := make(map[string][]responsesSourceIdentity)
	seenGeminiBaseOwner := make(map[string]map[responsesSourceIdentity]bool)

	for _, desc := range winningList {
		geminiName := desc.name
		if mapped, ok := sanitizedMap[desc.name]; ok && mapped != "" {
			geminiName = mapped
		} else {
			geminiName = sanitizeGeminiFunctionName(desc.name)
		}
		forwardMap[desc.name] = geminiName
		if desc.localName != "" && desc.localName != desc.name {
			if _, exists := forwardMap[desc.localName]; !exists {
				forwardMap[desc.localName] = geminiName
			}
		}
		if seenGeminiBaseOwner[geminiName] == nil {
			seenGeminiBaseOwner[geminiName] = make(map[responsesSourceIdentity]bool)
		}
		if !seenGeminiBaseOwner[geminiName][desc.ownerID] {
			seenGeminiBaseOwner[geminiName][desc.ownerID] = true
			geminiBaseOwners[geminiName] = append(geminiBaseOwners[geminiName], desc.ownerID)
		}
		reverseMap[geminiName] = desc
		if desc.name != geminiName {
			reverseMap[desc.name] = desc
			if seenGeminiBaseOwner[desc.name] == nil {
				seenGeminiBaseOwner[desc.name] = make(map[responsesSourceIdentity]bool)
			}
			if !seenGeminiBaseOwner[desc.name][desc.ownerID] {
				seenGeminiBaseOwner[desc.name][desc.ownerID] = true
				geminiBaseOwners[desc.name] = append(geminiBaseOwners[desc.name], desc.ownerID)
			}
		}
	}

	return &geminiProjectionIndex{
		winners:       winners,
		winningList:   winningList,
		forwardMap:    forwardMap,
		reverseMap:    reverseMap,
		qualOwnersMap: qualOwners,
		geminiOwners:  geminiBaseOwners,
	}
}

func (idx *geminiProjectionIndex) mapToolName(name string) string {
	if mapped, ok := idx.forwardMap[name]; ok && mapped != "" {
		return mapped
	}
	return sanitizeGeminiFunctionName(name)
}

func (idx *geminiProjectionIndex) resolveOwner(namespace, name string) (responsesSourceIdentity, bool) {
	qName := name
	if namespace != "" {
		qName = rawResponsesNamespaceQualifiedName(namespace, name)
	}
	gName := idx.mapToolName(qName)
	win, ok := idx.reverseMap[gName]
	if !ok {
		return responsesSourceIdentity{}, false
	}
	return win.ownerID, true
}

func matchesDeclaredNamespaceChild(occs []responsesDeclOccurrence, name string) bool {
	for _, occ := range occs {
		if occ.namespace == "" {
			continue
		}
		if occ.effectiveName == name || rawResponsesNamespaceQualifiedName(occ.namespace, occ.effectiveName) == name || qualifyResponsesNamespaceToolName(occ.namespace, occ.effectiveName) == name {
			return true
		}
	}
	return false
}

func buildResponsesMutationPlan(root map[string]any, client string, preferred map[string]string) (*responsesMutationPlan, error) {
	occs, err := collectResponsesDeclOccurrences(root, true)
	if err != nil {
		return nil, err
	}

	// Step 1: Collect flat declaration sources.
	var flatDeclSources []string
	seenFlatDecl := make(map[string]bool)
	for _, occ := range occs {
		if occ.namespace == "" {
			if !seenFlatDecl[occ.effectiveName] {
				seenFlatDecl[occ.effectiveName] = true
				flatDeclSources = append(flatDeclSources, occ.effectiveName)
			}
		}
	}

	// Build preliminary plan from flat declarations so we know their proposed targets.
	prelimSources := append([]string(nil), flatDeclSources...)
	prelimPlan, err := buildRequestAliasPlan(client, prelimSources, preferred, flatDeclSources)
	if err != nil {
		return nil, err
	}

	// Verify flat alias cap/no-op requirement (§3.2):
	for _, src := range flatDeclSources {
		target := prelimPlan.forward[src]
		if capResponsesChatToolName(target) != target {
			return nil, fmt.Errorf("flat alias target %q for %q exceeds or is modified by Chat 64-byte cap", target, src)
		}
	}

	origChat := buildChatProjectionIndex(occs, func(o responsesDeclOccurrence) string {
		return o.effectiveName
	})
	origGemini := buildGeminiProjectionIndex(occs, func(o responsesDeclOccurrence) string {
		return o.effectiveName
	})
	propChat := buildChatProjectionIndex(occs, func(o responsesDeclOccurrence) string {
		if o.namespace == "" {
			if t, ok := prelimPlan.forward[o.effectiveName]; ok && t != "" {
				return t
			}
		}
		return o.effectiveName
	})
	propGemini := buildGeminiProjectionIndex(occs, func(o responsesDeclOccurrence) string {
		if o.namespace == "" {
			if t, ok := prelimPlan.forward[o.effectiveName]; ok && t != "" {
				return t
			}
		}
		return o.effectiveName
	})

	// Step 2: Validate declaration collisions and winner preservation across both projections (§3.4).
	if err := validateResponsesDeclarationProjections(occs, prelimPlan.forward, origChat, propChat, origGemini, propGemini); err != nil {
		return nil, err
	}

	// Step 3: Inspect history references (`function_call` / `custom_tool_call`) and `tool_choice`.
	var extraFallbackSources []string
	seenAllSources := make(map[string]bool, len(prelimSources))
	for _, s := range prelimSources {
		seenAllSources[s] = true
	}

	resolveReference := func(namespace, name string) (rewrite bool, target string, isFallback bool, err error) {
		origChatOwner, origChatOK := origChat.resolveOwner(namespace, name)
		origGemOwner, origGemOK := origGemini.resolveOwner(namespace, name)

		// Check if this reference resolves to a transformed flat owner in either original projection.
		flatTarget := ""
		hasFlatOwner := false
		if origChatOK && origChatOwner.Namespace == "" {
			hasFlatOwner = true
			flatTarget = prelimPlan.forward[origChatOwner.LocalName]
		}
		if origGemOK && origGemOwner.Namespace == "" {
			if hasFlatOwner && origChatOwner != origGemOwner {
				// Disagreement between two different flat owners (impossible for same name, but guard).
				return false, "", false, fmt.Errorf("reference %q resolves to different flat owners across projections", name)
			}
			hasFlatOwner = true
			flatTarget = prelimPlan.forward[origGemOwner.LocalName]
		}

		// Candidate 1: If it has a flat owner, try rewriting to flatTarget (when namespace == "").
		if hasFlatOwner && namespace == "" && flatTarget != "" {
			afterChatOwner, afterChatOK := propChat.resolveOwner(namespace, flatTarget)
			afterGemOwner, afterGemOK := propGemini.resolveOwner(namespace, flatTarget)
			if afterChatOK == origChatOK && afterChatOwner == origChatOwner &&
				afterGemOK == origGemOK && afterGemOwner == origGemOwner {
				return flatTarget != name, flatTarget, false, nil
			}
		}

		// Candidate 2: If it is a declared reference (or ambiguous namespace child), try keeping original `name` pass-through.
		isDeclaredOrNamespaceMatch := origChatOK || origGemOK || matchesDeclaredNamespaceChild(occs, name)
		if isDeclaredOrNamespaceMatch {
			afterChatOwner, afterChatOK := propChat.resolveOwner(namespace, name)
			afterGemOwner, afterGemOK := propGemini.resolveOwner(namespace, name)
			if afterChatOK == origChatOK && afterChatOwner == origChatOwner &&
				afterGemOK == origGemOK && afterGemOwner == origGemOwner {
				// Also ensure keeping `name` pass-through doesn't leave a transformed flat owner unrenamed!
				if hasFlatOwner && prelimPlan.forward[flatOwnerName(origChatOK, origChatOwner, origGemOwner)] != flatOwnerName(origChatOK, origChatOwner, origGemOwner) {
					return false, "", false, fmt.Errorf("reference %q to transformed flat owner cannot be kept consistent across projections", name)
				}
				return false, name, false, nil
			}
			return false, "", false, fmt.Errorf("reference (namespace=%q, name=%q) owner drifts after aliasing", namespace, name)
		}

		// Truly undeclared reference (neither projection resolved it, and it doesn't match any namespace child).
		if namespace != "" {
			// A namespaced undeclared reference stays pass-through as long as it doesn't resolve onto a declared tool after aliasing.
			_, afterChatOK := propChat.resolveOwner(namespace, name)
			_, afterGemOK := propGemini.resolveOwner(namespace, name)
			if afterChatOK || afterGemOK {
				return false, "", false, fmt.Errorf("undeclared namespaced reference (%q, %q) resolves onto declared tool after aliasing", namespace, name)
			}
			return false, name, false, nil
		}

		// Undeclared bare reference: use request-scoped alias plan authority.
		return true, "", true, nil
	}

	var historyRefs []responsesHistoryRef
	if inputArr, ok := root["input"].([]any); ok {
		for _, rawItem := range inputArr {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := item["type"].(string)
			if typ != "function_call" && typ != "custom_tool_call" {
				continue
			}
			rawName, hasName := item["name"]
			if !hasName || rawName == nil {
				continue
			}
			nameStr, ok := rawName.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported %s name representation", typ)
			}
			nameTrimmed := strings.TrimSpace(nameStr)
			if nameTrimmed == "" {
				continue
			}
			nsTrimmed := ""
			if rawNS, hasNS := item["namespace"]; hasNS && rawNS != nil {
				nsStr, ok := rawNS.(string)
				if !ok {
					return nil, fmt.Errorf("unsupported %s namespace representation", typ)
				}
				nsTrimmed = strings.TrimSpace(nsStr)
			}
			rewrite, target, isFallback, err := resolveReference(nsTrimmed, nameTrimmed)
			if err != nil {
				return nil, err
			}
			if isFallback && !seenAllSources[nameTrimmed] {
				seenAllSources[nameTrimmed] = true
				extraFallbackSources = append(extraFallbackSources, nameTrimmed)
			}
			historyRefs = append(historyRefs, responsesHistoryRef{
				item:      item,
				name:      nameTrimmed,
				namespace: nsTrimmed,
				target:    target,
				rewrite:   rewrite,
			})
		}
	}

	// Inspect tool_choice (§3.5)
	var selMut *responsesSelectorMutation
	willMutateIdentities := false
	for _, p := range prelimPlan.pairs {
		if p.Changed {
			willMutateIdentities = true
			break
		}
	}
	if len(extraFallbackSources) > 0 {
		willMutateIdentities = true
	}

	if tcVal, exists := root["tool_choice"]; exists && tcVal != nil {
		switch tc := tcVal.(type) {
		case string:
			// "auto", "required", "none" remain unchanged.
		case map[string]any:
			sel, isFallback, fbSource, err := inspectResponsesSelector(tc, willMutateIdentities, resolveReference)
			if err != nil {
				return nil, err
			}
			if sel != nil {
				selMut = sel
				if isFallback && fbSource != "" && !seenAllSources[fbSource] {
					seenAllSources[fbSource] = true
					extraFallbackSources = append(extraFallbackSources, fbSource)
				}
			}
		default:
			if willMutateIdentities {
				return nil, fmt.Errorf("unsupported tool_choice representation")
			}
		}
	}

	// Build final requestAliasPlan including any undeclared history/selector fallback sources.
	finalPlan := prelimPlan
	if len(extraFallbackSources) > 0 {
		allSources := append(append([]string(nil), flatDeclSources...), extraFallbackSources...)
		var err error
		finalPlan, err = buildRequestAliasPlan(client, allSources, preferred, flatDeclSources)
		if err != nil {
			return nil, err
		}
		for _, src := range extraFallbackSources {
			target := finalPlan.forward[src]
			if capResponsesChatToolName(target) != target {
				return nil, fmt.Errorf("fallback alias target %q for %q exceeds Chat 64-byte cap", target, src)
			}
			// Verify undeclared fallback target does not resolve onto a declared tool in either projection!
			if _, ok := propChat.resolveOwner("", target); ok {
				return nil, fmt.Errorf("undeclared reference %q target %q collides with declared tool in Chat projection", src, target)
			}
			if _, ok := propGemini.resolveOwner("", target); ok {
				return nil, fmt.Errorf("undeclared reference %q target %q collides with declared tool in Gemini projection", src, target)
			}
			// Also check against pass-through namespace child local/qualified names
			if matchesDeclaredNamespaceChild(occs, target) {
				return nil, fmt.Errorf("undeclared reference %q target %q collides with namespace child", src, target)
			}
		}
	}

	// Fill in fallback targets on historyRefs and selMut
	for i := range historyRefs {
		if historyRefs[i].rewrite && historyRefs[i].target == "" {
			t := finalPlan.forward[historyRefs[i].name]
			historyRefs[i].target = t
			historyRefs[i].rewrite = t != historyRefs[i].name
		}
	}
	if selMut != nil && selMut.rewrite && selMut.targetName == "" {
		t := finalPlan.forward[selMut.originalName]
		selMut.targetName = t
		selMut.rewrite = t != selMut.originalName
	}

	if selMut != nil && selMut.rewrite {
		finalPlan.selectorRecord = &responsesSelectorRecord{
			originalName:  selMut.originalName,
			rewrittenName: selMut.targetName,
			rewritten:     true,
		}
	}

	return &responsesMutationPlan{
		plan:             finalPlan,
		declOccurrences:  occs,
		historyRefs:      historyRefs,
		selectorMutation: selMut,
	}, nil
}

func flatOwnerName(chatOK bool, chatOwner, gemOwner responsesSourceIdentity) string {
	if chatOK && chatOwner.Namespace == "" {
		return chatOwner.LocalName
	}
	return gemOwner.LocalName
}

func validateResponsesDeclarationProjections(
	occs []responsesDeclOccurrence,
	flatForward map[string]string,
	origChat, propChat *chatProjectionIndex,
	origGemini, propGemini *geminiProjectionIndex,
) error {
	isTransformedOwner := func(id responsesSourceIdentity) bool {
		if id.Namespace != "" {
			return false
		}
		t, ok := flatForward[id.LocalName]
		return ok && t != id.LocalName
	}

	// 1. Check Chat base emitted name collisions (before host suffixing).
	// If two distinct owners share a base emitted name in propChat, and either:
	// - they did not share a base emitted name in origChat, OR
	// - one of the owners was transformed,
	// reject!
	for baseName, owners := range propChat.baseCollisionOwners {
		if len(owners) <= 1 {
			continue
		}
		anyTransformed := false
		for _, o := range owners {
			if isTransformedOwner(o) {
				anyTransformed = true
				break
			}
		}
		if anyTransformed {
			return fmt.Errorf("chat projection collision on base emitted name %q involving transformed declaration", baseName)
		}
		// Even if none of `owners` was transformed, check if they only collided because a transformed tool moved out or changed!
		// Wait: baseCollisionOwners is purely a function of each declaration's own `qualifyResponsesNamespaceToolName(ns, local)`.
		// If none of `owners` was transformed, their `qualifyResponsesNamespaceToolName` is identical in origChat!
	}
	// Also check if an original collision in origChat had a transformed participant whose removal/renaming changed the winning owner of that base name!
	for baseName, origOwners := range origChat.baseCollisionOwners {
		if len(origOwners) <= 1 {
			continue
		}
		for _, o := range origOwners {
			if isTransformedOwner(o) {
				return fmt.Errorf("chat projection original collision on %q involves transformed declaration %v", baseName, o)
			}
		}
	}

	// 2. Check Chat disambiguated emitted names and winning declarations:
	// Every declaration in origChat and propChat is at the same index.
	if len(origChat.declarations) != len(propChat.declarations) {
		return fmt.Errorf("chat declaration count changed after aliasing")
	}
	for i := range origChat.declarations {
		od := origChat.declarations[i]
		pd := propChat.declarations[i]
		origWin := origChat.byChat[od.chatName]
		propWin := propChat.byChat[pd.chatName]
		// Preserve winning owner and exact winning occurrence (schema/description/custom precedence).
		if origWin.ownerID != propWin.ownerID || origWin.order != propWin.order {
			return fmt.Errorf("chat winning declaration changed for occurrence %d (%v -> %v)", i, origWin.ownerID, propWin.ownerID)
		}
		// Also if a non-transformed declaration's disambiguated chatName changed because a transformed tool collided or reserved its local name, reject!
		if !isTransformedOwner(od.ownerID) && od.chatName != pd.chatName {
			return fmt.Errorf("chat emitted name for pass-through declaration %v drifted (%q -> %q)", od.ownerID, od.chatName, pd.chatName)
		}
		// If a transformed flat declaration's chatName was disambiguated with a suffix (meaning it collided with a reserved local/qualified name), reject!
		if isTransformedOwner(od.ownerID) && pd.chatName != pd.localName {
			return fmt.Errorf("chat emitted name for transformed flat declaration %v was suffixed (%q != %q)", od.ownerID, pd.chatName, pd.localName)
		}
	}

	// 3. Check Gemini qualified name collisions and winning declarations:
	for qName, owners := range propGemini.qualOwnersMap {
		if len(owners) <= 1 {
			continue
		}
		for _, o := range owners {
			if isTransformedOwner(o) {
				return fmt.Errorf("gemini projection collision on qualified name %q involving transformed declaration %v", qName, o)
			}
		}
	}
	for qName, origOwners := range origGemini.qualOwnersMap {
		if len(origOwners) <= 1 {
			continue
		}
		for _, o := range origOwners {
			if isTransformedOwner(o) {
				return fmt.Errorf("gemini projection original collision on %q involves transformed declaration %v", qName, o)
			}
		}
	}
	for gKey, owners := range propGemini.geminiOwners {
		if len(owners) <= 1 {
			continue
		}
		for _, o := range owners {
			if isTransformedOwner(o) {
				return fmt.Errorf("gemini projection reverse key collision on %q involving transformed declaration %v", gKey, o)
			}
		}
	}
	for gKey, origOwners := range origGemini.geminiOwners {
		if len(origOwners) <= 1 {
			continue
		}
		for _, o := range origOwners {
			if isTransformedOwner(o) {
				return fmt.Errorf("gemini projection original reverse key collision on %q involves transformed declaration %v", gKey, o)
			}
		}
	}
	if len(origGemini.winningList) != len(propGemini.winningList) {
		return fmt.Errorf("gemini winning declaration count changed after aliasing (%d -> %d)", len(origGemini.winningList), len(propGemini.winningList))
	}
	for i := range origGemini.winningList {
		ow := origGemini.winningList[i]
		pw := propGemini.winningList[i]
		if ow.ownerID != pw.ownerID || ow.order != pw.order {
			return fmt.Errorf("gemini winning declaration order/owner changed at %d (%v -> %v)", i, ow.ownerID, pw.ownerID)
		}
		// Verify reverse identity lookup in propGemini resolves the emitted Gemini name back to the same owner!
		gName := propGemini.mapToolName(pw.name)
		rev, ok := propGemini.reverseMap[gName]
		if !ok || rev.ownerID != ow.ownerID || rev.order != ow.order {
			return fmt.Errorf("gemini reverse identity drifted for %v", ow.ownerID)
		}
	}

	return nil
}

func inspectResponsesSelector(
	tc map[string]any,
	willMutateIdentities bool,
	resolveRef func(namespace, name string) (rewrite bool, target string, isFallback bool, err error),
) (*responsesSelectorMutation, bool, string, error) {
	rawType, hasType := tc["type"]
	typ := ""
	if hasType && rawType != nil {
		s, ok := rawType.(string)
		if !ok {
			return nil, false, "", fmt.Errorf("unsupported tool_choice type representation")
		}
		typ = strings.TrimSpace(s)
	}
	switch strings.ToLower(typ) {
	case "none", "auto", "required", "any":
		// Mode-only object selector (if no name fields are populated)
		if !selectorHasAnyNameField(tc) {
			return nil, false, "", nil
		}
		if willMutateIdentities {
			return nil, false, "", fmt.Errorf("unsupported tool_choice object with mode %q and name fields", typ)
		}
		return nil, false, "", nil
	case "function", "custom":
		// Supported named selector types (§3.5)
	default:
		// Outside named-selector support (e.g. type:"tool", absent type shorthand, allowed_tools, etc.)
		// Wait: what if `typ == "function"` vs `typ == "Function"`? Spec §3.5:
		// `Supported named selectors are type:"function" or type:"custom" with a string name in flat name, function.name or custom.name.`
		// Note: Chat converter checks `choiceType != "function" && choiceType != "custom"` case-sensitively!
		if willMutateIdentities {
			return nil, false, "", fmt.Errorf("unsupported structured tool_choice type %q when renaming tool identities", typ)
		}
		return nil, false, "", nil
	}
	if typ != "function" && typ != "custom" {
		if willMutateIdentities {
			return nil, false, "", fmt.Errorf("unsupported tool_choice type casing %q", typ)
		}
		return nil, false, "", nil
	}

	// Check name fields: flat `name`, `function.name`, `custom.name`
	var populatedNames []string
	hasFlatName := false
	hasFnName := false
	hasCustomName := false
	var fnMap, customMap map[string]any

	if rawFlat, exists := tc["name"]; exists && rawFlat != nil {
		s, ok := rawFlat.(string)
		if !ok {
			return nil, false, "", fmt.Errorf("tool_choice.name must be a string")
		}
		trimmed := strings.TrimSpace(s)
		if trimmed != "" {
			hasFlatName = true
			populatedNames = append(populatedNames, trimmed)
		}
	}
	if rawFn, exists := tc["function"]; exists && rawFn != nil {
		m, ok := rawFn.(map[string]any)
		if !ok {
			return nil, false, "", fmt.Errorf("tool_choice.function must be an object")
		}
		fnMap = m
		if rawName, exists := m["name"]; exists && rawName != nil {
			s, ok := rawName.(string)
			if !ok {
				return nil, false, "", fmt.Errorf("tool_choice.function.name must be a string")
			}
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				hasFnName = true
				populatedNames = append(populatedNames, trimmed)
			}
		}
	}
	if rawCust, exists := tc["custom"]; exists && rawCust != nil {
		m, ok := rawCust.(map[string]any)
		if !ok {
			return nil, false, "", fmt.Errorf("tool_choice.custom must be an object")
		}
		customMap = m
		if rawName, exists := m["name"]; exists && rawName != nil {
			s, ok := rawName.(string)
			if !ok {
				return nil, false, "", fmt.Errorf("tool_choice.custom.name must be a string")
			}
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				hasCustomName = true
				populatedNames = append(populatedNames, trimmed)
			}
		}
	}

	if len(populatedNames) == 0 {
		if willMutateIdentities {
			return nil, false, "", fmt.Errorf("named tool_choice is missing a non-empty name")
		}
		return nil, false, "", nil
	}

	// Resolve effective namespace from root `namespace`, then `function.namespace`, then `custom.namespace`
	effNS := ""
	for _, nsHolder := range []map[string]any{tc, fnMap, customMap} {
		if nsHolder == nil {
			continue
		}
		if rawNS, exists := nsHolder["namespace"]; exists && rawNS != nil {
			s, ok := rawNS.(string)
			if !ok {
				return nil, false, "", fmt.Errorf("tool_choice namespace must be a string")
			}
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				effNS = trimmed
				break
			}
		}
	}

	effName := populatedNames[0]
	for _, n := range populatedNames[1:] {
		if n != effName {
			// Conflicting populated name values: if identity mutation is already planned or if any populated name would be mutated by resolveRef, fail closed with 503.
			if willMutateIdentities {
				return nil, false, "", fmt.Errorf("conflicting populated name fields in tool_choice: %q vs %q", effName, n)
			}
			for _, cand := range populatedNames {
				rw, tgt, fb, err := resolveRef(effNS, cand)
				if err != nil || fb || (rw && tgt != "" && tgt != cand) {
					return nil, false, "", fmt.Errorf("conflicting populated name fields in tool_choice: %q vs %q", effName, n)
				}
			}
			return nil, false, "", nil
		}
	}

	rewrite, target, isFallback, err := resolveRef(effNS, effName)
	if err != nil {
		return nil, false, "", err
	}

	mut := &responsesSelectorMutation{
		tc:            tc,
		fnMap:         fnMap,
		customMap:     customMap,
		hasFlatName:   hasFlatName,
		hasFnName:     hasFnName,
		hasCustomName: hasCustomName,
		originalName:  effName,
		targetName:    target,
		rewrite:       rewrite,
	}
	fbSource := ""
	if isFallback {
		fbSource = effName
	}
	return mut, isFallback, fbSource, nil
}

func selectorHasAnyNameField(tc map[string]any) bool {
	if _, ok := tc["name"]; ok {
		return true
	}
	if fn, ok := tc["function"].(map[string]any); ok {
		if _, ok := fn["name"]; ok {
			return true
		}
	}
	if cust, ok := tc["custom"].(map[string]any); ok {
		if _, ok := cust["name"]; ok {
			return true
		}
	}
	return false
}

func applyResponsesRequestMutations(
	root map[string]any,
	mutPlan *responsesMutationPlan,
	mappings []rewriteMapping,
	proseCloak *cachedCloakPatterns,
) bool {
	changed := false

	// 1. Rewrite declarations (flat only; nonempty namespace children stay pass-through)
	for _, occ := range mutPlan.declOccurrences {
		if occ.namespace != "" {
			continue
		}
		target, ok := mutPlan.plan.forward[occ.effectiveName]
		if ok && target != "" && target != occ.effectiveName {
			if occ.useTopLevelName {
				occ.toolMap["name"] = target
			} else if occ.fnMap != nil {
				occ.fnMap["name"] = target
			}
			changed = true
		}
		if rewriteResponsesToolDeclProse(occ, mappings, proseCloak) {
			changed = true
		}
	}

	// 2. Rewrite history references
	for _, ref := range mutPlan.historyRefs {
		if ref.rewrite && ref.target != "" && ref.target != ref.name {
			ref.item["name"] = ref.target
			changed = true
		}
	}

	// 3. Rewrite tool_choice
	if sel := mutPlan.selectorMutation; sel != nil && sel.rewrite && sel.targetName != "" && sel.targetName != sel.originalName {
		if sel.hasFlatName {
			sel.tc["name"] = sel.targetName
		}
		if sel.hasFnName && sel.fnMap != nil {
			sel.fnMap["name"] = sel.targetName
		}
		if sel.hasCustomName && sel.customMap != nil {
			sel.customMap["name"] = sel.targetName
		}
		changed = true
	}

	// 4. Rewrite instructions and input conversation carriers (§3.1)
	if inst, ok := root["instructions"].(string); ok {
		if next, c := rewriteDescriptiveText(inst, mappings, proseCloak); c {
			root["instructions"] = next
			changed = true
		}
	}

	if rewriteResponsesInputCarriers(root, mappings, proseCloak) {
		changed = true
	}

	return changed
}

func rewriteResponsesToolDeclProse(occ responsesDeclOccurrence, mappings []rewriteMapping, proseCloak *cachedCloakPatterns) bool {
	changed := false
	rewriteFn := func(s string) (string, bool) {
		return rewriteDescriptiveText(s, mappings, proseCloak)
	}

	// Rewrite description on toolMap and/or fnMap where present
	if desc, ok := occ.toolMap["description"].(string); ok {
		if next, c := rewriteFn(desc); c {
			occ.toolMap["description"] = next
			changed = true
		}
	}
	if occ.fnMap != nil {
		if desc, ok := occ.fnMap["description"].(string); ok {
			if next, c := rewriteFn(desc); c {
				occ.fnMap["description"] = next
				changed = true
			}
		}
	}

	// Custom declarations preserve `format`; function declarations rewrite schema description/title fields.
	if !occ.custom {
		for _, key := range []string{"parameters", "parametersJsonSchema", "input_schema"} {
			if schema, ok := occ.toolMap[key]; ok {
				if rewriteSchemaTextFields(schema, rewriteFn) {
					changed = true
				}
			}
			if occ.fnMap != nil {
				if schema, ok := occ.fnMap[key]; ok {
					if rewriteSchemaTextFields(schema, rewriteFn) {
						changed = true
					}
				}
			}
		}
	}
	return changed
}

func isResponsesSupportedTextPartType(typ string) bool {
	return typ == "text" || typ == "input_text" || typ == "output_text" || typ == ""
}

func rewriteResponsesSystemContent(content any, mappings []rewriteMapping, proseCloak *cachedCloakPatterns) (any, bool) {
	switch typed := content.(type) {
	case string:
		return rewriteDescriptiveText(typed, mappings, proseCloak)
	case []any:
		changed := false
		for i, rawPart := range typed {
			switch part := rawPart.(type) {
			case string:
				if next, c := rewriteDescriptiveText(part, mappings, proseCloak); c {
					typed[i] = next
					changed = true
				}
			case map[string]any:
				typ, _ := part["type"].(string)
				if !isResponsesSupportedTextPartType(typ) {
					continue
				}
				if txt, ok := part["text"].(string); ok {
					if next, c := rewriteDescriptiveText(txt, mappings, proseCloak); c {
						part["text"] = next
						changed = true
					}
				}
			}
		}
		return typed, changed
	case map[string]any:
		typ, _ := typed["type"].(string)
		if !isResponsesSupportedTextPartType(typ) {
			return content, false
		}
		if txt, ok := typed["text"].(string); ok {
			if next, c := rewriteDescriptiveText(txt, mappings, proseCloak); c {
				typed["text"] = next
				return typed, true
			}
		}
	}
	return content, false
}

func rewriteResponsesUserContent(content any, mappings []rewriteMapping, proseCloak *cachedCloakPatterns) (any, bool) {
	changed := false
	switch typed := content.(type) {
	case string:
		return rewriteSystemReminderSpans(typed, mappings, proseCloak)
	case []any:
		for _, rawBlock := range typed {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := block["type"].(string)
			switch typ {
			case "tool_result":
				if next, c := rewriteSystemValue(block["content"], mappings); c {
					block["content"] = next
					changed = true
				}
				if proseCloak != nil {
					if next, c := replaceToolNamesInValue(block["content"], proseCloak); c {
						block["content"] = next
						changed = true
					}
				}
			case "text", "input_text", "":
				if txt, ok := block["text"].(string); ok {
					if next, c := rewriteSystemReminderSpans(txt, mappings, proseCloak); c {
						block["text"] = next
						changed = true
					}
				}
			}
		}
	case map[string]any:
		typ, _ := typed["type"].(string)
		if typ == "text" || typ == "input_text" || typ == "" {
			if txt, ok := typed["text"].(string); ok {
				if next, c := rewriteSystemReminderSpans(txt, mappings, proseCloak); c {
					typed["text"] = next
					changed = true
				}
			}
		}
	}
	return content, changed
}

func rewriteResponsesAssistantContent(content any, mappings []rewriteMapping, proseCloak *cachedCloakPatterns) (any, bool) {
	return walkOpenAITextParts(content, func(txt string) string {
		next, _ := rewriteDescriptiveText(txt, mappings, proseCloak)
		return next
	})
}

func rewriteResponsesToolOutputValue(output any, mappings []rewriteMapping, proseCloak *cachedCloakPatterns) (any, bool) {
	switch typed := output.(type) {
	case string:
		return rewriteDescriptiveText(typed, mappings, proseCloak)
	case []any:
		changed := false
		for i, rawPart := range typed {
			switch part := rawPart.(type) {
			case string:
				if next, c := rewriteDescriptiveText(part, mappings, proseCloak); c {
					typed[i] = next
					changed = true
				}
			case map[string]any:
				typ, _ := part["type"].(string)
				if isResponsesSupportedTextPartType(typ) {
					if txt, ok := part["text"].(string); ok {
						if next, c := rewriteDescriptiveText(txt, mappings, proseCloak); c {
							part["text"] = next
							changed = true
						}
					}
				}
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for k, v := range typed {
			if next, c := rewriteResponsesToolOutputValue(v, mappings, proseCloak); c {
				typed[k] = next
				changed = true
			}
		}
		return typed, changed
	}
	return output, false
}

func rewriteResponsesInputCarriers(root map[string]any, mappings []rewriteMapping, proseCloak *cachedCloakPatterns) bool {
	// Plain string `input` is a user turn and preserved verbatim (§3.1).
	inputArr, ok := root["input"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, rawItem := range inputArr {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		role, _ := item["role"].(string)

		// Reasoning items and additional_tools items are never conversation messages.
		if typ == "reasoning" || typ == "additional_tools" || typ == "function_call" || typ == "custom_tool_call" {
			continue
		}
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			if outVal, exists := item["output"]; exists {
				if next, c := rewriteResponsesToolOutputValue(outVal, mappings, proseCloak); c {
					item["output"] = next
					changed = true
				}
			}
			continue
		}
		if typ == "message" || (typ == "" && role != "") {
			content, hasContent := item["content"]
			if !hasContent {
				continue
			}
			switch role {
			case "system", "developer":
				if next, c := rewriteResponsesSystemContent(content, mappings, proseCloak); c {
					item["content"] = next
					changed = true
				}
			case "user":
				if next, c := rewriteResponsesUserContent(content, mappings, proseCloak); c {
					item["content"] = next
					changed = true
				}
			case "assistant":
				if next, c := rewriteResponsesAssistantContent(content, mappings, proseCloak); c {
					item["content"] = next
					changed = true
				}
			}
		}
	}
	return changed
}

// ── Section 4: Non-stream Response Reversal (Upstream -> Client) ────────────

func reverseResponsesObject(respMap map[string]any, plan *requestAliasPlan) bool {
	if respMap == nil || plan == nil {
		return false
	}
	changed := false
	client := plan.client

	// 1. Root `output_text` string, when present
	if outTxt, ok := respMap["output_text"].(string); ok && outTxt != "" {
		if next, c := replaceInsensitiveSetWithPrev(outTxt, false, brandReverseTableFor(client)); c {
			respMap["output_text"] = next
			changed = true
		}
	}

	// 2. Echoed `instructions`, when present
	if inst, ok := respMap["instructions"].(string); ok && inst != "" {
		if next, c := replaceInsensitiveSetWithPrev(inst, false, brandReverseTableFor(client)); c {
			respMap["instructions"] = next
			changed = true
		}
	}

	// 3. Echoed `tools[]`, when present
	if toolsArr, ok := respMap["tools"].([]any); ok {
		if reverseResponsesEchoedTools(toolsArr, plan) {
			changed = true
		}
	}

	// 4. Echoed `tool_choice`, when present and recorded as rewritten in this request
	if tcMap, ok := respMap["tool_choice"].(map[string]any); ok {
		if reverseResponsesEchoedToolChoice(tcMap, plan.selectorRecord) {
			changed = true
		}
	}

	// 5. `output[]` items
	if outputArr, ok := respMap["output"].([]any); ok {
		for _, rawItem := range outputArr {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			if reverseResponsesOutputItem(item, plan) {
				changed = true
			}
		}
	}

	return changed
}

func reverseResponsesOutputItem(item map[string]any, plan *requestAliasPlan) bool {
	if item == nil || plan == nil {
		return false
	}
	changed := false
	client := plan.client
	typ, _ := item["type"].(string)

	switch typ {
	case "reasoning":
		// Preserve the entire reasoning item (§4, A6, A9).
		return false
	case "message":
		if content, exists := item["content"]; exists {
			if next, c := reverseBrandInOpenAIContent(content, client); c {
				item["content"] = next
				changed = true
			}
		}
	case "function_call":
		ns, _ := item["namespace"].(string)
		if strings.TrimSpace(ns) == "" {
			if name, ok := item["name"].(string); ok && name != "" {
				if orig, exists := plan.reverse[name]; exists {
					item["name"] = orig
					changed = true
				}
			}
		}
		if args, ok := item["arguments"].(string); ok && args != "" {
			if next, c := replaceInsensitiveSetWithPrev(args, false, argumentReverseTableFor(client)); c {
				item["arguments"] = next
				changed = true
			}
		}
	case "custom_tool_call":
		ns, _ := item["namespace"].(string)
		if strings.TrimSpace(ns) == "" {
			if name, ok := item["name"].(string); ok && name != "" {
				if orig, exists := plan.reverse[name]; exists {
					item["name"] = orig
					changed = true
				}
			}
		}
		if inp, ok := item["input"].(string); ok && inp != "" {
			if next, c := replaceInsensitiveSetWithPrev(inp, false, argumentReverseTableFor(client)); c {
				item["input"] = next
				changed = true
			}
		}
	}
	return changed
}

func reverseResponsesEchoedTools(toolsArr []any, plan *requestAliasPlan) bool {
	changed := false
	brandTable := brandReverseTableFor(plan.client)
	rewriteDesc := func(s string) (string, bool) {
		return replaceInsensitiveSetWithPrev(s, false, brandTable)
	}

	for _, rawTool := range toolsArr {
		tMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := tMap["type"].(string)
		typ = strings.TrimSpace(typ)
		if typ == "namespace" {
			nsName, _ := tMap["name"].(string)
			if strings.TrimSpace(nsName) != "" {
				// Preserve nonempty namespace containers and children
				continue
			}
			for _, key := range []string{"tools", "children"} {
				if childArr, ok := tMap[key].([]any); ok {
					if reverseResponsesEchoedTools(childArr, plan) {
						changed = true
					}
					break
				}
			}
			continue
		}
		if typ != "" && typ != "function" && typ != "custom" {
			// Built-in tool type
			continue
		}
		// Check if flat or nested name matches an active declared reverse entry
		effName, useTop, fnMap := responsesEffectiveToolName(tMap)
		if effName != "" {
			if orig, exists := plan.reverse[effName]; exists {
				// Only reverse if orig was actually declared in this request!
				if _, wasDeclared := plan.declaredForward[orig]; wasDeclared {
					if useTop {
						tMap["name"] = orig
					} else if fnMap != nil {
						fnMap["name"] = orig
					}
					changed = true
				}
			}
		}
		if desc, ok := tMap["description"].(string); ok && desc != "" {
			if next, c := rewriteDesc(desc); c {
				tMap["description"] = next
				changed = true
			}
		}
		if fnMap != nil {
			if desc, ok := fnMap["description"].(string); ok && desc != "" {
				if next, c := rewriteDesc(desc); c {
					fnMap["description"] = next
					changed = true
				}
			}
		}
		if typ != "custom" {
			for _, key := range []string{"parameters", "parametersJsonSchema", "input_schema"} {
				if schema, ok := tMap[key]; ok {
					if rewriteSchemaTextFields(schema, rewriteDesc) {
						changed = true
					}
				}
				if fnMap != nil {
					if schema, ok := fnMap[key]; ok {
						if rewriteSchemaTextFields(schema, rewriteDesc) {
							changed = true
						}
					}
				}
			}
		}
	}
	return changed
}

func reverseResponsesEchoedToolChoice(tcMap map[string]any, rec *responsesSelectorRecord) bool {
	if tcMap == nil || rec == nil || !rec.rewritten || rec.rewrittenName == "" {
		return false
	}
	typ, _ := tcMap["type"].(string)
	typ = strings.TrimSpace(typ)
	if typ != "function" && typ != "custom" {
		return false
	}
	// Check if selector has nonempty namespace; if so, a flat rewrite never produced it
	for _, holder := range []map[string]any{
		tcMap,
		asMap(tcMap["function"]),
		asMap(tcMap["custom"]),
	} {
		if holder == nil {
			continue
		}
		if ns, ok := holder["namespace"].(string); ok && strings.TrimSpace(ns) != "" {
			return false
		}
	}

	changed := false
	if name, ok := tcMap["name"].(string); ok && strings.TrimSpace(name) == rec.rewrittenName {
		tcMap["name"] = rec.originalName
		changed = true
	}
	if fnMap, ok := tcMap["function"].(map[string]any); ok {
		if name, ok := fnMap["name"].(string); ok && strings.TrimSpace(name) == rec.rewrittenName {
			fnMap["name"] = rec.originalName
			changed = true
		}
	}
	if customMap, ok := tcMap["custom"].(map[string]any); ok {
		if name, ok := customMap["name"].(string); ok && strings.TrimSpace(name) == rec.rewrittenName {
			customMap["name"] = rec.originalName
			changed = true
		}
	}
	return changed
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// ── Section 5: Streaming Surface (Framing, Lanes, Events, Flushes) ──────────

type responsesOutputItemMeta struct {
	itemID   string
	itemType string
	argKind  string
}

func responsesTextLaneKey(outputIndex, contentIndex int) string {
	return fmt.Sprintf("%s%d:%d", responsesLanePrefix, outputIndex, contentIndex)
}

func responsesArgsLaneKey(outputIndex int) string {
	return fmt.Sprintf("%s%d:0%s%s", responsesLanePrefix, outputIndex, reverseBrandLaneSuffix, toolArgsLaneMarker)
}

func responsesLaneParts(laneKey string) (outputIndex int, contentIndex int, isArgs bool, ok bool) {
	if !strings.HasPrefix(laneKey, responsesLanePrefix) {
		return 0, 0, false, false
	}
	rest := strings.TrimPrefix(laneKey, responsesLanePrefix)
	base := rest
	if idx := strings.Index(rest, reverseBrandLaneSuffix); idx >= 0 {
		base = rest[:idx]
		tail := rest[idx+len(reverseBrandLaneSuffix):]
		if j := strings.Index(tail, reverseBrandLaneSuffix); j >= 0 {
			tail = tail[:j]
		}
		if tail == toolArgsLaneMarker {
			isArgs = true
		} else {
			return 0, 0, false, false
		}
	}
	colon := strings.IndexByte(base, ':')
	if colon <= 0 || colon == len(base)-1 {
		return 0, 0, false, false
	}
	outIdx, err1 := strconv.Atoi(base[:colon])
	cntIdx, err2 := strconv.Atoi(base[colon+1:])
	if err1 != nil || err2 != nil || outIdx < 0 || cntIdx < 0 {
		return 0, 0, false, false
	}
	return outIdx, cntIdx, isArgs, true
}

func resetResponsesLane(sess *streamSession, laneKey string) {
	if sess == nil || sess.brandCarries == nil {
		return
	}
	if lane := sess.brandCarries[laneKey]; lane != nil {
		lane.setCarry("")
		lane.lastIsWord = false
	}
}

func resetResponsesOutputItemTextLanes(sess *streamSession, outputIndex int) {
	if sess == nil || sess.brandCarries == nil {
		return
	}
	for k, lane := range sess.brandCarries {
		outIdx, _, isArgs, ok := responsesLaneParts(k)
		if ok && !isArgs && outIdx == outputIndex && lane != nil {
			lane.setCarry("")
			lane.lastIsWord = false
		}
	}
}

func ensureResponsesItemMeta(sess *streamSession, outputIndex int) *responsesOutputItemMeta {
	if sess.responsesItems == nil {
		sess.responsesItems = make(map[int]*responsesOutputItemMeta)
	}
	meta := sess.responsesItems[outputIndex]
	if meta == nil {
		meta = &responsesOutputItemMeta{}
		sess.responsesItems[outputIndex] = meta
	}
	return meta
}

func buildResponsesFlushEvent(sess *streamSession, laneKey, text string) []byte {
	if sess == nil || text == "" {
		return nil
	}
	outIdx, cntIdx, isArgs, ok := responsesLaneParts(laneKey)
	if !ok {
		return nil
	}
	meta := sess.responsesItems[outIdx]
	if meta == nil || meta.itemID == "" {
		debugLog("ResponsesStream: dropping unattributable carry for lane=%s (missing item_id)", laneKey)
		return nil
	}
	if !isArgs {
		if meta.itemType != "" && meta.itemType != "message" {
			debugLog("ResponsesStream: dropping text carry for non-message item lane=%s type=%s", laneKey, meta.itemType)
			return nil
		}
		payload, err := safeMarshal(map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       meta.itemID,
			"output_index":  outIdx,
			"content_index": cntIdx,
			"delta":         text,
		})
		if err != nil {
			return nil
		}
		return []byte("event: response.output_text.delta\ndata: " + string(payload) + "\n\n")
	}

	var evType string
	switch meta.argKind {
	case responsesArgKindFunction:
		evType = "response.function_call_arguments.delta"
	case responsesArgKindCustom:
		evType = "response.custom_tool_call_input.delta"
	default:
		debugLog("ResponsesStream: dropping argument carry for lane=%s (missing argument kind)", laneKey)
		return nil
	}
	payload, err := safeMarshal(map[string]any{
		"type":         evType,
		"item_id":      meta.itemID,
		"output_index": outIdx,
		"delta":        text,
	})
	if err != nil {
		return nil
	}
	return []byte("event: " + evType + "\ndata: " + string(payload) + "\n\n")
}

// processResponsesStreamEvent applies §5.1–§5.4 to a single decoded Responses JSON event map.
// Returns (preFlushFrames, eventChanged, isTerminal).
func (m *streamSessionManager) processResponsesStreamEvent(
	data map[string]any,
	sess *streamSession,
	plan *requestAliasPlan,
) (preFlush []byte, changed bool, isTerminal bool) {
	if data == nil || sess == nil || plan == nil {
		return nil, false, false
	}
	typ, _ := data["type"].(string)
	client := plan.client

	switch typ {
	case "response.created", "response.in_progress":
		if respMap, ok := data["response"].(map[string]any); ok {
			if reverseResponsesObject(respMap, plan) {
				changed = true
			}
		}
		return nil, changed, false

	case "response.output_item.added":
		outIdx, hasOutIdx := jsonIndexValue(data["output_index"])
		item, _ := data["item"].(map[string]any)
		if item == nil {
			return nil, false, false
		}
		itemType, _ := item["type"].(string)
		itemID, _ := item["id"].(string)
		if hasOutIdx && outIdx >= 0 {
			meta := ensureResponsesItemMeta(sess, outIdx)
			if itemID != "" {
				meta.itemID = itemID
			}
			if itemType != "" {
				meta.itemType = itemType
			}
			switch itemType {
			case "function_call":
				meta.argKind = responsesArgKindFunction
			case "custom_tool_call":
				meta.argKind = responsesArgKindCustom
			}
		}

		switch itemType {
		case "reasoning":
			return nil, false, false
		case "message":
			if content, exists := item["content"]; exists && hasOutIdx && outIdx >= 0 {
				if arr, ok := content.([]any); ok {
					for cIdx, rawPart := range arr {
						part, ok := rawPart.(map[string]any)
						if !ok {
							continue
						}
						if pType, _ := part["type"].(string); !isAssistantTextPartType(pType) {
							continue
						}
						if txt, ok := part["text"].(string); ok && txt != "" {
							laneKey := responsesTextLaneKey(outIdx, cIdx)
							if next, _ := applySemanticBrandLane(sess, laneKey, txt); next != txt {
								part["text"] = next
								changed = true
							}
						}
					}
				}
			}
		case "function_call":
			ns, _ := item["namespace"].(string)
			if strings.TrimSpace(ns) == "" {
				if name, ok := item["name"].(string); ok && name != "" {
					if orig, exists := plan.reverse[name]; exists {
						item["name"] = orig
						changed = true
					}
				}
			}
			if args, ok := item["arguments"].(string); ok && args != "" && hasOutIdx && outIdx >= 0 {
				laneKey := responsesArgsLaneKey(outIdx)
				if next, _ := applySemanticBrandLane(sess, laneKey, args); next != args {
					item["arguments"] = next
					changed = true
				}
			}
		case "custom_tool_call":
			ns, _ := item["namespace"].(string)
			if strings.TrimSpace(ns) == "" {
				if name, ok := item["name"].(string); ok && name != "" {
					if orig, exists := plan.reverse[name]; exists {
						item["name"] = orig
						changed = true
					}
				}
			}
			if inp, ok := item["input"].(string); ok && inp != "" && hasOutIdx && outIdx >= 0 {
				laneKey := responsesArgsLaneKey(outIdx)
				if next, _ := applySemanticBrandLane(sess, laneKey, inp); next != inp {
					item["input"] = next
					changed = true
				}
			}
		}
		return nil, changed, false

	case "response.output_item.done":
		outIdx, hasOutIdx := jsonIndexValue(data["output_index"])
		item, _ := data["item"].(map[string]any)
		if item == nil {
			return nil, false, false
		}
		itemType, _ := item["type"].(string)
		itemID, _ := item["id"].(string)
		if hasOutIdx && outIdx >= 0 {
			meta := ensureResponsesItemMeta(sess, outIdx)
			if itemID != "" {
				meta.itemID = itemID
			}
			if itemType != "" {
				meta.itemType = itemType
			}
			switch itemType {
			case "function_call":
				meta.argKind = responsesArgKindFunction
			case "custom_tool_call":
				meta.argKind = responsesArgKindCustom
			}
		}
		if itemType == "reasoning" {
			return nil, false, false
		}
		if reverseResponsesOutputItem(item, plan) {
			changed = true
		}
		if hasOutIdx && outIdx >= 0 {
			switch itemType {
			case "message":
				resetResponsesOutputItemTextLanes(sess, outIdx)
			case "function_call", "custom_tool_call":
				resetResponsesLane(sess, responsesArgsLaneKey(outIdx))
			}
		}
		return nil, changed, false

	case "response.output_text.delta":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		cntIdx, okCnt := jsonIndexValue(data["content_index"])
		if !okOut || !okCnt || outIdx < 0 || cntIdx < 0 {
			return nil, false, false
		}
		meta := ensureResponsesItemMeta(sess, outIdx)
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if meta.itemType != "" && meta.itemType != "message" {
			return nil, false, false
		}
		if itemID != "" {
			meta.itemID = itemID
		}
		if meta.itemType == "" {
			meta.itemType = "message"
		}
		if delta, ok := data["delta"].(string); ok && delta != "" {
			laneKey := responsesTextLaneKey(outIdx, cntIdx)
			if next, _ := applySemanticBrandLane(sess, laneKey, delta); next != delta {
				data["delta"] = next
				changed = true
			}
		}
		return nil, changed, false

	case "response.output_text.done":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		cntIdx, okCnt := jsonIndexValue(data["content_index"])
		if !okOut || !okCnt || outIdx < 0 || cntIdx < 0 {
			return nil, false, false
		}
		meta := ensureResponsesItemMeta(sess, outIdx)
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if meta.itemType != "" && meta.itemType != "message" {
			return nil, false, false
		}
		if itemID != "" {
			meta.itemID = itemID
		}
		if meta.itemType == "" {
			meta.itemType = "message"
		}
		if txt, ok := data["text"].(string); ok && txt != "" {
			if next, c := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client)); c {
				data["text"] = next
				changed = true
			}
		}
		resetResponsesLane(sess, responsesTextLaneKey(outIdx, cntIdx))
		return nil, changed, false

	case "response.content_part.added":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		if okOut && outIdx >= 0 {
			if itemID, _ := data["item_id"].(string); itemID != "" {
				meta := ensureResponsesItemMeta(sess, outIdx)
				if meta.itemID == "" {
					meta.itemID = itemID
				}
			}
		}
		return nil, false, false

	case "response.content_part.done":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		cntIdx, okCnt := jsonIndexValue(data["content_index"])
		if !okOut || !okCnt || outIdx < 0 || cntIdx < 0 {
			return nil, false, false
		}
		part, _ := data["part"].(map[string]any)
		if part == nil {
			return nil, false, false
		}
		pType, _ := part["type"].(string)
		// Guard against reasoning or unestablished items! Only assistant-visible text parts positively belonging to a message are reversed.
		meta := sess.responsesItems[outIdx]
		if meta == nil || meta.itemType != "message" {
			return nil, false, false
		}
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if !isAssistantTextPartType(pType) {
			return nil, false, false
		}
		if txt, ok := part["text"].(string); ok && txt != "" {
			if next, c := replaceInsensitiveSetWithPrev(txt, false, brandReverseTableFor(client)); c {
				part["text"] = next
				changed = true
			}
		}
		resetResponsesLane(sess, responsesTextLaneKey(outIdx, cntIdx))
		return nil, changed, false

	case "response.function_call_arguments.delta":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		if !okOut || outIdx < 0 {
			return nil, false, false
		}
		meta := ensureResponsesItemMeta(sess, outIdx)
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if meta.itemType != "" && meta.itemType != "function_call" {
			return nil, false, false
		}
		if itemID != "" {
			meta.itemID = itemID
		}
		meta.itemType = "function_call"
		meta.argKind = responsesArgKindFunction
		if delta, ok := data["delta"].(string); ok && delta != "" {
			laneKey := responsesArgsLaneKey(outIdx)
			if next, _ := applySemanticBrandLane(sess, laneKey, delta); next != delta {
				data["delta"] = next
				changed = true
			}
		}
		return nil, changed, false

	case "response.function_call_arguments.done":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		if !okOut || outIdx < 0 {
			return nil, false, false
		}
		meta := ensureResponsesItemMeta(sess, outIdx)
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if meta.itemType != "" && meta.itemType != "function_call" {
			return nil, false, false
		}
		if itemID != "" {
			meta.itemID = itemID
		}
		meta.itemType = "function_call"
		meta.argKind = responsesArgKindFunction
		if args, ok := data["arguments"].(string); ok && args != "" {
			if next, c := replaceInsensitiveSetWithPrev(args, false, argumentReverseTableFor(client)); c {
				data["arguments"] = next
				changed = true
			}
		}
		resetResponsesLane(sess, responsesArgsLaneKey(outIdx))
		return nil, changed, false

	case "response.custom_tool_call_input.delta":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		if !okOut || outIdx < 0 {
			return nil, false, false
		}
		meta := ensureResponsesItemMeta(sess, outIdx)
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if meta.itemType != "" && meta.itemType != "custom_tool_call" {
			return nil, false, false
		}
		if itemID != "" {
			meta.itemID = itemID
		}
		meta.itemType = "custom_tool_call"
		meta.argKind = responsesArgKindCustom
		if delta, ok := data["delta"].(string); ok && delta != "" {
			laneKey := responsesArgsLaneKey(outIdx)
			if next, _ := applySemanticBrandLane(sess, laneKey, delta); next != delta {
				data["delta"] = next
				changed = true
			}
		}
		return nil, changed, false

	case "response.custom_tool_call_input.done":
		outIdx, okOut := jsonIndexValue(data["output_index"])
		if !okOut || outIdx < 0 {
			return nil, false, false
		}
		meta := ensureResponsesItemMeta(sess, outIdx)
		itemID, _ := data["item_id"].(string)
		if meta.itemID != "" && itemID != "" && meta.itemID != itemID {
			return nil, false, false
		}
		if meta.itemType != "" && meta.itemType != "custom_tool_call" {
			return nil, false, false
		}
		if itemID != "" {
			meta.itemID = itemID
		}
		meta.itemType = "custom_tool_call"
		meta.argKind = responsesArgKindCustom
		if inp, ok := data["input"].(string); ok && inp != "" {
			if next, c := replaceInsensitiveSetWithPrev(inp, false, argumentReverseTableFor(client)); c {
				data["input"] = next
				changed = true
			}
		}
		resetResponsesLane(sess, responsesArgsLaneKey(outIdx))
		return nil, changed, false

	case "response.completed", "response.incomplete":
		coveredItemIDs := make(map[string]bool)
		if respMap, ok := data["response"].(map[string]any); ok {
			if reverseResponsesObject(respMap, plan) {
				changed = true
			}
			if outArr, ok := respMap["output"].([]any); ok {
				for _, rawItem := range outArr {
					if item, ok := rawItem.(map[string]any); ok {
						if id, _ := item["id"].(string); id != "" {
							coveredItemIDs[id] = true
						}
					}
				}
			}
		}
		preFlush = flushResponsesTerminalCarries(sess, coveredItemIDs)
		return preFlush, changed, true

	case "response.failed", "response.error", "response.done":
		preFlush = flushResponsesTerminalCarries(sess, nil)
		return preFlush, false, true

	default:
		// Reasoning events (`response.reasoning_text.*`, `response.reasoning_summary_*`) and unknown `response.*` types are preserved.
		return nil, false, false
	}
}

func flushResponsesTerminalCarries(sess *streamSession, coveredItemIDs map[string]bool) []byte {
	if sess == nil || len(sess.brandCarries) == 0 {
		return nil
	}
	keys := sortedCarryKeys(sess)
	var out []byte
	for _, k := range keys {
		outIdx, _, _, ok := responsesLaneParts(k)
		if !ok {
			continue
		}
		lane := sess.brandCarries[k]
		if lane == nil || lane.carry == "" {
			continue
		}
		meta := sess.responsesItems[outIdx]
		if meta != nil && meta.itemID != "" && coveredItemIDs[meta.itemID] {
			// Discard carry; authoritative output[] already contains the item's complete value (§5.4).
			lane.setCarry("")
			lane.lastIsWord = false
			continue
		}
		text, _ := replaceInsensitiveSetWithPrev(lane.carry, lane.lastIsWord, laneReverseTable(sess, k))
		lane.setCarry("")
		lane.lastIsWord = false
		if text == "" {
			continue
		}
		if frame := buildResponsesFlushEvent(sess, k, text); len(frame) > 0 {
			out = append(out, frame...)
		}
	}
	return out
}

// ── Responses Streaming Framing Seam (§5.1) ─────────────────────────────────

func responsesSSEHasField(chunk []byte, prefix []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func responsesSSENeedsMoreData(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	return responsesSSEHasField(trimmed, []byte("event:")) && !responsesSSEHasField(trimmed, []byte("data:"))
}

func responsesSSEDataPayload(frame []byte) ([]byte, bool) {
	var payload []byte
	found := false
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(trimmed[len("data:"):])
		if found {
			payload = append(payload, '\n')
		}
		payload = append(payload, data...)
		found = true
	}
	return payload, found
}

func responsesSSEDataLinesValid(chunk []byte) bool {
	payload, found := responsesSSEDataPayload(chunk)
	if !found {
		return true
	}
	payload = bytes.TrimSpace(payload)
	return len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || json.Valid(payload)
}

func responsesSSECanEmitWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || responsesSSENeedsMoreData(trimmed) ||
		!responsesSSEHasField(trimmed, []byte("event:")) || !responsesSSEHasField(trimmed, []byte("data:")) {
		return false
	}
	return responsesSSEDataLinesValid(trimmed)
}

func responsesSSECanFlushDataOnlyWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	return len(trimmed) > 0 && !responsesSSEHasField(trimmed, []byte("event:")) &&
		responsesSSEHasField(trimmed, []byte("data:")) && responsesSSEDataLinesValid(trimmed)
}

func responsesSSENeedsLineBreak(pending, chunk []byte) bool {
	if len(pending) == 0 || len(chunk) == 0 {
		return false
	}
	if bytes.HasSuffix(pending, []byte("\n")) || bytes.HasSuffix(pending, []byte("\r")) {
		return false
	}
	if chunk[0] == '\n' || chunk[0] == '\r' {
		return false
	}
	trimmed := bytes.TrimLeft(chunk, " \t")
	if len(trimmed) == 0 {
		return false
	}
	// If pending already has a `data:` line with an incomplete JSON payload,
	// do not inject a newline before a continuation chunk that happens to start
	// with literal text like `data:` inside a JSON string (§5.1, A8).
	if responsesSSEHasField(pending, []byte("data:")) && !responsesSSEDataLinesValid(pending) {
		return false
	}
	for _, prefix := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func responsesSSEFrameLen(chunk []byte) int {
	if len(chunk) == 0 {
		return 0
	}
	lf := bytes.Index(chunk, []byte("\n\n"))
	crlf := bytes.Index(chunk, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		if crlf < 0 {
			return 0
		}
		return crlf + 4
	case crlf < 0:
		return lf + 2
	case lf < crlf:
		return lf + 2
	default:
		return crlf + 4
	}
}

func ensureSSEFrameTerminator(frame []byte) []byte {
	if len(frame) == 0 {
		return frame
	}
	if bytes.HasSuffix(frame, []byte("\n\n")) || bytes.HasSuffix(frame, []byte("\r\n\r\n")) {
		return frame
	}
	out := append([]byte(nil), frame...)
	if bytes.HasSuffix(out, []byte("\r\n")) {
		return append(out, []byte("\r\n")...)
	}
	if bytes.HasSuffix(out, []byte("\n")) {
		return append(out, '\n')
	}
	return append(out, []byte("\n\n")...)
}

// ── Top-level OpenAI Responses Handler Entrypoints & Lane Helpers ───────────

func isResponsesArgumentLaneKey(laneKey string) bool {
	_, _, isArgs, ok := responsesLaneParts(laneKey)
	return ok && isArgs
}

// compareResponsesCarryKeys compares two carry keys if either is a Responses lane key.
// Returns (less, handled).
func compareResponsesCarryKeys(keyI, keyJ string, sess *streamSession) (bool, bool) {
	outI, cntI, argsI, okI := responsesLaneParts(keyI)
	outJ, cntJ, argsJ, okJ := responsesLaneParts(keyJ)
	if !okI && !okJ {
		return false, false
	}
	if okI != okJ {
		return okI, true
	}
	if outI != outJ {
		return outI < outJ, true
	}
	if cntI != cntJ {
		return cntI < cntJ, true
	}
	if argsI != argsJ {
		return !argsI, true
	}
	si, sj := 0, 0
	if sess != nil {
		if li := sess.brandCarries[keyI]; li != nil {
			si = li.seq
		}
		if lj := sess.brandCarries[keyJ]; lj != nil {
			sj = lj.seq
		}
	}
	if si != sj {
		return si < sj, true
	}
	return keyI < keyJ, true
}

func pinResponsesWSNoOp(requestID, client string) error {
	if requestID == "" {
		return nil
	}
	plan := &requestAliasPlan{
		client:       client,
		sourceFormat: "openai",
		wireFamily:   wireFamilyOpenAIResponses,
		wsNoOp:       true,
	}
	return globalAliasPlanManager.set(requestID, plan)
}

func tryHandleOpenAIResponsesRequest(
	req *pluginapi.RequestInterceptRequest,
	resp pluginapi.RequestInterceptResponse,
	format string,
	forcedClient string,
	marker explicitMarkerResult,
) (bool, []byte) {
	if format == "anthropic" {
		return false, nil
	}
	if format != "openai" {
		if forcedClient != "" && clientUsesAliasPlan(forcedClient) {
			debugLog("handleRequestInterceptBefore: unknown source format %q for alias-plan client=%s -> 503", req.SourceFormat, forcedClient)
			return true, toolCloak503Response(resp)
		}
		debugLog("handleRequestInterceptBefore: unknown source format=%q -> untouched body pass-through", req.SourceFormat)
		return true, mustEnvelope(resp)
	}

	rootMap, strictOK := decodeStrictProtectedJSON(req.Body)
	if !strictOK {
		var loose any
		if err := safeUnmarshal(req.Body, &loose); err == nil {
			rootMap, _ = loose.(map[string]any)
		}
	}

	family := requestWireFamily(format, rootMap)
	switch family {
	case wireFamilyOpenAIChat:
		return false, nil
	case wireFamilyUnknown:
		if forcedClient != "" && clientUsesAliasPlan(forcedClient) {
			debugLog("handleRequestInterceptBefore: unknown request wire shape SourceFormat=%q client=%s -> 503", req.SourceFormat, forcedClient)
			return true, toolCloak503Response(resp)
		}
		if marker.present && !marker.valid && req.RequestID != "" {
			globalStreamManager.resetSession("req:"+req.RequestID, negativeClientResolution, nil, 1)
		}
		debugLog("handleRequestInterceptBefore: unknown request wire family SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
		return true, mustEnvelope(resp)
	case wireFamilyOpenAIResponses:
		if !strictOK {
			if forcedClient != "" && clientUsesAliasPlan(forcedClient) {
				debugLog("handleRequestInterceptBefore: non-strict JSON on Responses surface client=%s -> 503", forcedClient)
				return true, toolCloak503Response(resp)
			}
			debugLog("handleRequestInterceptBefore: non-strict JSON on Responses surface SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
			return true, mustEnvelope(resp)
		}
		return true, handleOpenAIResponsesRequest(req, resp, rootMap, forcedClient, marker)
	default:
		return false, nil
	}
}

func handleOpenAIResponsesRequest(
	req *pluginapi.RequestInterceptRequest,
	resp pluginapi.RequestInterceptResponse,
	rootMap map[string]any,
	forcedClient string,
	marker explicitMarkerResult,
) []byte {
	effectiveClient := forcedClient
	if effectiveClient != "" && len(effectiveCloakTable(effectiveClient)) == 0 {
		effectiveClient = ""
	}
	if effectiveClient == "" {
		toolNames := extractResponsesToolNamesForDetection(rootMap)
		effectiveClient = detectClient(toolNames)
	}
	if effectiveClient == "" {
		if marker.present && !marker.valid && req.RequestID != "" {
			debugLog("handleOpenAIResponsesRequest: negative client resolution recorded for RequestID=%s", req.RequestID)
			globalStreamManager.resetSession("req:"+req.RequestID, negativeClientResolution, nil, 1)
		}
		return mustEnvelope(resp)
	}

	if req.RequestID == "" {
		if clientUsesAliasPlan(effectiveClient) {
			debugLog("handleOpenAIResponsesRequest: alias plan client=%s with missing RequestID -> 503", effectiveClient)
			return toolCloak503Response(resp)
		}
		return mustEnvelope(resp)
	}

	if isWebSocketUpgradeRequest(req.Headers) {
		if err := pinResponsesWSNoOp(req.RequestID, effectiveClient); err != nil {
			debugLog("handleOpenAIResponsesRequest: duplicate RequestID=%q on WS upgrade -> 503", req.RequestID)
			return toolCloak503Response(resp)
		}
		debugLog("handleOpenAIResponsesRequest: WebSocket upgrade request RequestID=%s client=%s -> pinned zero mutation", req.RequestID, effectiveClient)
		return mustEnvelope(resp)
	}

	preferred := sharedAliasesFor(effectiveClient)
	mutPlan, err := buildResponsesMutationPlan(rootMap, effectiveClient, preferred)
	if err != nil {
		debugLog("request alias admission rejected RequestID=%q client=%q: %v", req.RequestID, effectiveClient, err)
		return toolCloak503Response(resp)
	}

	plan := mutPlan.plan
	plan.sourceFormat = "openai"
	plan.wireFamily = wireFamilyOpenAIResponses
	plan.expected = 1

	cfg := activeFilterConfig()
	mappings := effectiveMappings(cfg, effectiveClient)
	proseCloak := &cachedCloakPatterns{
		cloakTable: plan.declaredForward,
		identRe:    buildCloakIdentRe(plan.declaredForward),
		ambigRe:    buildCloakAmbiguousRe(plan.declaredForward),
	}

	changed := applyResponsesRequestMutations(rootMap, mutPlan, mappings, proseCloak)
	var raw []byte
	if changed {
		var err error
		raw, err = safeMarshal(rootMap)
		if err != nil {
			debugLog("handleOpenAIResponsesRequest: safeMarshal failed: %v", err)
			return toolCloak503Response(resp)
		}
	}
	if err := globalAliasPlanManager.set(req.RequestID, plan); err != nil {
		debugLog("request alias admission rejected RequestID=%q client=%q: %v", req.RequestID, effectiveClient, err)
		return toolCloak503Response(resp)
	}
	globalStreamManager.resetSession("req:"+req.RequestID, plan.client, plan.cachedUncloak, 1)
	debugLog("handleRequestInterceptBefore: alias plan rewritten=%t client=%s", changed, effectiveClient)
	if !changed {
		return mustEnvelope(resp)
	}
	resp.Body = raw
	return mustEnvelope(resp)
}

func handleOpenAIResponsesResponseIntercept(req *pluginapi.ResponseInterceptRequest, plan *requestAliasPlan) []byte {
	if plan == nil || plan.wsNoOp {
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	var rootMap map[string]any
	if err := safeUnmarshal(req.Body, &rootMap); err != nil || rootMap == nil {
		debugLog("handleOpenAIResponsesResponseIntercept: unknown/unparseable response SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	fam := responseWireFamily("openai", rootMap)
	switch fam {
	case wireFamilyOpenAIResponses:
		if !reverseResponsesObject(rootMap, plan) {
			return mustEnvelope(pluginapi.ResponseInterceptResponse{})
		}
		raw, err := safeMarshal(rootMap)
		if err != nil {
			return mustEnvelope(pluginapi.ResponseInterceptResponse{})
		}
		return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: raw})
	case wireFamilyOpenAIChat:
		modified, uncloaked := uncloakResponseBodyExact(req.Body, plan.reverse, "openai")
		if !uncloaked {
			modified = req.Body
		}
		if rev, c := reverseCloakedBrandBody(modified, plan.client); c {
			return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: rev})
		}
		if uncloaked {
			return mustEnvelope(pluginapi.ResponseInterceptResponse{Body: modified})
		}
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	default:
		debugLog("handleOpenAIResponsesResponseIntercept: unknown response wire family SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
		return mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
}

func tryHandleUncorrelatedResponsesResponse(req *pluginapi.ResponseInterceptRequest, format string) (bool, []byte) {
	if format == "anthropic" {
		return false, nil
	}
	if format != "openai" {
		debugLog("handleResponseIntercept: unknown source format=%q -> untouched body pass-through", req.SourceFormat)
		return true, mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	var rootMap map[string]any
	if err := safeUnmarshal(req.Body, &rootMap); err != nil || rootMap == nil {
		debugLog("handleResponseIntercept: unknown response wire family SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
		return true, mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	switch responseWireFamily(format, rootMap) {
	case wireFamilyOpenAIChat:
		return false, nil
	case wireFamilyOpenAIResponses:
		debugLog("handleResponseIntercept: uncorrelated OpenAI Responses response SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
		return true, mustEnvelope(pluginapi.ResponseInterceptResponse{})
	default:
		debugLog("handleResponseIntercept: unknown response wire family SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
		return true, mustEnvelope(pluginapi.ResponseInterceptResponse{})
	}
}

func tryHandleUncorrelatedResponsesStreamChunk(req *pluginapi.StreamChunkInterceptRequest, format string) (bool, pluginapi.StreamChunkInterceptResponse) {
	if format == "anthropic" {
		return false, pluginapi.StreamChunkInterceptResponse{}
	}
	if format != "openai" {
		debugLog("handleStreamChunkIntercept: unknown source format=%q -> untouched body pass-through", req.SourceFormat)
		return true, pluginapi.StreamChunkInterceptResponse{}
	}
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex || req.ChunkIndex < 0 {
		src := detectionRequestBody(req.OriginalRequest, req.RequestBody)
		if len(src) > 0 {
			var reqRoot map[string]any
			if err := safeUnmarshal(src, &reqRoot); err == nil && reqRoot != nil {
				switch requestWireFamily(format, reqRoot) {
				case wireFamilyOpenAIResponses:
					debugLog("handleStreamChunkIntercept: uncorrelated OpenAI Responses stream header-init SourceFormat=%q -> pass-through", req.SourceFormat)
					return true, pluginapi.StreamChunkInterceptResponse{}
				case wireFamilyUnknown:
					debugLog("handleStreamChunkIntercept: unknown request wire family on header-init SourceFormat=%q -> pass-through", req.SourceFormat)
					return true, pluginapi.StreamChunkInterceptResponse{}
				}
			}
		}
		return false, pluginapi.StreamChunkInterceptResponse{}
	}

	key := globalStreamManager.sessionKey(req)
	if key != "" {
		if sess := globalStreamManager.getSession(key); sess != nil && len(sess.tail) > 0 {
			return false, pluginapi.StreamChunkInterceptResponse{}
		}
	}
	trimmed := bytes.TrimSpace(req.Body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return false, pluginapi.StreamChunkInterceptResponse{}
	}
	if payload, found := responsesSSEDataPayload(trimmed); found {
		trimmed = bytes.TrimSpace(payload)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
			return false, pluginapi.StreamChunkInterceptResponse{}
		}
	}
	var rootMap map[string]any
	if err := safeUnmarshal(trimmed, &rootMap); err == nil && rootMap != nil {
		switch streamEventFamily(format, rootMap) {
		case wireFamilyOpenAIResponses:
			debugLog("handleStreamChunkIntercept: uncorrelated OpenAI Responses stream chunk SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
			return true, pluginapi.StreamChunkInterceptResponse{}
		case wireFamilyUnknown:
			debugLog("handleStreamChunkIntercept: unknown stream event family SourceFormat=%q -> untouched body pass-through", req.SourceFormat)
			return true, pluginapi.StreamChunkInterceptResponse{}
		}
	}
	return false, pluginapi.StreamChunkInterceptResponse{}
}

func handleOpenAIResponsesStreamChunkIntercept(
	req *pluginapi.StreamChunkInterceptRequest,
	plan *requestAliasPlan,
) pluginapi.StreamChunkInterceptResponse {
	if plan == nil || plan.wsNoOp {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if plan.getDisposition() == streamDispositionCleanTerminal {
		debugLog("ResponsesStream: late chunk after clean terminal RequestID=%s", req.RequestID)
		return pluginapi.StreamChunkInterceptResponse{}
	}
	key := globalStreamManager.sessionKey(req)
	if key == "" {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex || req.ChunkIndex < 0 {
		if plan.getDisposition() == streamDispositionNone {
			globalStreamManager.ensureSession(key, plan.client, plan.cachedUncloak, plan.expected)
		}
		return pluginapi.StreamChunkInterceptResponse{}
	}

	globalStreamManager.mu.Lock()
	globalStreamManager.cleanupStaleLocked()
	sess := globalStreamManager.sessions[key]
	globalStreamManager.mu.Unlock()
	if sess == nil {
		if plan.getDisposition() == streamDispositionPayloadActive {
			debugLog("ResponsesStream: disposable session lost after payload started key=%s", key)
			return pluginapi.StreamChunkInterceptResponse{}
		}
		if plan.getDisposition() == streamDispositionNone {
			sess = globalStreamManager.ensureSession(key, plan.client, plan.cachedUncloak, plan.expected)
		}
	}
	if sess == nil {
		return pluginapi.StreamChunkInterceptResponse{}
	}

	globalStreamManager.mu.Lock()
	plan.setDisposition(streamDispositionPayloadActive)
	sess.payloadStarted = true
	if req.ChunkIndex == 0 {
		sess.tail = nil
	}

	chunk := req.Body
	// Standalone raw JSON callback (no pending SSE tail, not starting with event:/data:/:/id:/retry:).
	if len(sess.tail) == 0 {
		trimmed := bytes.TrimSpace(chunk)
		isRawJSON := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') &&
			!bytes.Contains(chunk, []byte("\n\n")) && !bytes.Contains(chunk, []byte("\r\n\r\n"))
		if isRawJSON && json.Valid(trimmed) {
			sess.updatedAt = time.Now()
			globalStreamManager.mu.Unlock()
			return processResponsesStandaloneJSONChunk(req, sess, plan, key)
		}
	}

	frames, newTail := extractResponsesSSEFrames(sess.tail, chunk)
	if len(frames) == 0 {
		sess.tail = append([]byte(nil), newTail...)
		sess.updatedAt = time.Now()
		globalStreamManager.mu.Unlock()
		return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
	}
	if len(newTail) > 0 {
		sess.tail = append([]byte(nil), newTail...)
	} else {
		sess.tail = nil
	}
	sess.updatedAt = time.Now()
	globalStreamManager.mu.Unlock()

	var out bytes.Buffer
	anyChanged := false
	sawTerminal := false

	for _, frame := range frames {
		modFrame, frameChanged, term := processResponsesSingleSSEFrame(frame, sess, plan, req.SourceFormat)
		if frameChanged {
			anyChanged = true
		}
		if term {
			sawTerminal = true
		}
		out.Write(modFrame)
	}

	if sawTerminal && !hasPendingBrandCarry(sess) {
		globalStreamManager.deleteSession(key)
		plan.setDisposition(streamDispositionCleanTerminal)
	}

	resBytes := out.Bytes()
	if !anyChanged && bytes.Equal(resBytes, req.Body) {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	return pluginapi.StreamChunkInterceptResponse{Body: resBytes}
}

func processResponsesStandaloneJSONChunk(
	req *pluginapi.StreamChunkInterceptRequest,
	sess *streamSession,
	plan *requestAliasPlan,
	key string,
) pluginapi.StreamChunkInterceptResponse {
	var rootMap map[string]any
	if err := safeUnmarshal(req.Body, &rootMap); err != nil || rootMap == nil {
		debugLog("ResponsesStream: unknown standalone JSON chunk SourceFormat=%q -> untouched pass-through", req.SourceFormat)
		return pluginapi.StreamChunkInterceptResponse{}
	}
	fam := streamEventFamily("openai", rootMap)
	if fam != wireFamilyOpenAIResponses {
		debugLog("ResponsesStream: unknown standalone stream event family SourceFormat=%q -> untouched pass-through", req.SourceFormat)
		return pluginapi.StreamChunkInterceptResponse{}
	}
	preFlush, changed, isTerminal := globalStreamManager.processResponsesStreamEvent(rootMap, sess, plan)
	var out []byte
	if changed {
		if raw, err := safeMarshal(rootMap); err == nil {
			out = raw
		} else {
			out = req.Body
		}
	} else {
		out = req.Body
	}
	if len(preFlush) > 0 {
		// Standalone JSON chunks flushed before terminal become SSE frames + terminal SSE frame
		typ, _ := rootMap["type"].(string)
		termFrame := []byte("event: " + typ + "\ndata: " + string(out) + "\n\n")
		out = append(append([]byte(nil), preFlush...), termFrame...)
		changed = true
	}
	if isTerminal && !hasPendingBrandCarry(sess) {
		globalStreamManager.deleteSession(key)
		plan.setDisposition(streamDispositionCleanTerminal)
	}
	if !changed && bytes.Equal(out, req.Body) {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	return pluginapi.StreamChunkInterceptResponse{Body: out}
}

func extractResponsesSSEFrames(pending, chunk []byte) (frames [][]byte, remaining []byte) {
	var buf []byte
	if len(pending) > 0 {
		buf = append(buf, pending...)
	}
	if len(chunk) > 0 {
		if responsesSSEStartsNewDataFrame(buf, chunk) {
			frame := ensureSSEFrameTerminator(buf)
			frames = append(frames, frame)
			buf = nil
		}
		if responsesSSENeedsLineBreak(buf, chunk) {
			buf = append(buf, '\n')
		}
		buf = append(buf, chunk...)
	}
	for {
		frameLen := responsesSSEFrameLen(buf)
		if frameLen == 0 {
			break
		}
		frame := append([]byte(nil), buf[:frameLen]...)
		buf = buf[frameLen:]
		if responsesSSENeedsMoreData(frame) {
			trimmedRest := bytes.TrimLeft(buf, " \t\r\n")
			if len(trimmedRest) == 0 {
				buf = bytes.TrimRight(frame, "\r\n")
				buf = append(buf, '\n')
				break
			}
			if bytes.HasPrefix(trimmedRest, []byte("data:")) {
				header := bytes.TrimRight(frame, "\r\n")
				merged := make([]byte, 0, len(header)+1+len(trimmedRest))
				merged = append(merged, header...)
				merged = append(merged, '\n')
				merged = append(merged, trimmedRest...)
				buf = merged
				continue
			}
		}
		if !responsesSSEDataLinesValid(frame) {
			trimmedFrame := bytes.TrimRight(frame, "\r\n")
			trimmedRest := bytes.TrimLeft(buf, "\r\n")
			merged := make([]byte, 0, len(trimmedFrame)+len(trimmedRest))
			merged = append(merged, trimmedFrame...)
			merged = append(merged, trimmedRest...)
			buf = merged
			if responsesSSEFrameLen(buf) == 0 {
				break
			}
			continue
		}
		frames = append(frames, frame)
	}
	if len(buf) > 0 && (responsesSSECanEmitWithoutDelimiter(buf) || responsesSSECanFlushDataOnlyWithoutDelimiter(buf)) {
		frames = append(frames, ensureSSEFrameTerminator(buf))
		buf = nil
	}
	return frames, buf
}

func responsesSSEStartsNewDataFrame(pending, chunk []byte) bool {
	trimmedPending := bytes.TrimSpace(pending)
	if len(trimmedPending) == 0 || responsesSSEHasField(trimmedPending, []byte("event:")) ||
		!responsesSSEHasField(trimmedPending, []byte("data:")) || !responsesSSEDataLinesValid(trimmedPending) {
		return false
	}
	trimmedChunk := bytes.TrimLeft(chunk, " \t\r\n")
	return bytes.HasPrefix(trimmedChunk, []byte("data:"))
}

func processResponsesSingleSSEFrame(
	frame []byte,
	sess *streamSession,
	plan *requestAliasPlan,
	sourceFormat string,
) (outFrame []byte, changed bool, isTerminal bool) {
	payload, found := responsesSSEDataPayload(frame)
	if !found {
		return frame, false, false
	}
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return frame, false, false
	}
	if bytes.Equal(trimmedPayload, []byte("[DONE]")) {
		preFlush := flushResponsesTerminalCarries(sess, nil)
		if len(preFlush) > 0 {
			return append(preFlush, frame...), true, true
		}
		return frame, false, true
	}

	var rootMap map[string]any
	if err := safeUnmarshal(trimmedPayload, &rootMap); err != nil || rootMap == nil {
		debugLog("ResponsesStream: unknown SSE JSON payload SourceFormat=%q -> untouched data bytes", sourceFormat)
		return frame, false, false
	}
	fam := streamEventFamily("openai", rootMap)
	if fam != wireFamilyOpenAIResponses {
		debugLog("ResponsesStream: unknown SSE stream event family SourceFormat=%q -> untouched data bytes", sourceFormat)
		return frame, false, false
	}

	preFlush, evChanged, term := globalStreamManager.processResponsesStreamEvent(rootMap, sess, plan)
	outFrame = frame
	if evChanged {
		if rawJSON, err := safeMarshal(rootMap); err == nil {
			outFrame = replaceSSEFrameDataPayload(frame, rawJSON)
			changed = true
		}
	}
	if len(preFlush) > 0 {
		outFrame = append(append([]byte(nil), preFlush...), outFrame...)
		changed = true
	}
	return outFrame, changed, term
}

func replaceSSEFrameDataPayload(frame []byte, newData []byte) []byte {
	lineEnding := "\n"
	if bytes.Contains(frame, []byte("\r\n")) {
		lineEnding = "\r\n"
	}
	lines := bytes.Split(frame, []byte("\n"))
	var out bytes.Buffer
	wroteData := false
	for i, rawLine := range lines {
		line := bytes.TrimRight(rawLine, "\r")
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			if !wroteData {
				out.WriteString("data: ")
				out.Write(newData)
				out.WriteString(lineEnding)
				wroteData = true
			}
			continue
		}
		if i == len(lines)-1 && len(line) == 0 {
			continue
		}
		out.Write(line)
		out.WriteString(lineEnding)
	}
	if !bytes.HasSuffix(out.Bytes(), []byte(lineEnding+lineEnding)) {
		out.WriteString(lineEnding)
	}
	return out.Bytes()
}
