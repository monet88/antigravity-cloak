package cloak

import (
	"net/http"
	"sort"
	"strings"
)

// explicitClientHeader is the plugin-owned control header carrying the
// operator-declared client identity. It is consumed on the request path and
// never forwarded upstream, whether its value is valid or not.
const explicitClientHeader = "X-Cloak-Client"

// negativeClientResolution is a sentinel client id recorded under a request's
// correlation key when request-time precedence ran to completion but resolved
// no client (an invalid explicit X-Cloak-Client suppressed User-Agent evidence
// and body classification found nothing). Its presence is authoritative:
// response and stream paths must not reclassify the request from a weaker
// signal that survived after the control header was consumed. It is distinct
// from a missing session, which leaves conservative recovery available.
const negativeClientResolution = "__none__"

// detectionRequestBody returns the body used for client detection. The host runs
// this plugin's request.intercept_before first, and then republishes whatever
// that returned: when the request was rewritten, both OriginalRequest and
// RequestBody hold the executed (cloaked) body, so detection here reads cloak
// TARGETS; when it was not, both hold the raw client body. Fall back to
// RequestBody when OriginalRequest is unavailable.
func detectionRequestBody(originalRequest, requestBody []byte) []byte {
	if len(originalRequest) > 0 {
		return originalRequest
	}
	return requestBody
}

const (
	// minToolNameHits is the minimum number of tool-name hits required before
	// any client is detected from original tool names at all.
	minToolNameHits = 2

	// minCollidingToolMatches is how many tool-name hits a client needs when
	// none of its distinctive harness tools are present.
	minCollidingToolMatches = 4

	// minCloakTargetHitNum / minCloakTargetHitDen define the target-coverage
	// threshold for cloaked-client detection: hits/total >= 4/5 (80%).
	minCloakTargetHitNum = 4
	minCloakTargetHitDen = 5
)

// filteredHeaders returns a copy of headers with every key that matches any of
// remove case-insensitively dropped. It builds a fresh map rather than relying
// on http.Header.Del, which canonicalizes its argument and therefore cannot
// delete a stored key whose spelling is non-canonical. The result carries every
// non-owned inbound header verbatim, which is exactly the set a replacement
// host (the before-auth interceptor) keeps.
func filteredHeaders(headers http.Header, remove []string) http.Header {
	if headers == nil {
		return http.Header{}
	}
	out := make(http.Header, len(headers))
	for k, vs := range headers {
		drop := false
		for _, r := range remove {
			if strings.EqualFold(k, r) {
				drop = true
				break
			}
		}
		if !drop {
			out[k] = append([]string(nil), vs...)
		}
	}
	return out
}

type explicitMarkerResult struct {
	matchedKeys         []string
	present             bool
	uniqueClients       []string
	isOMP               bool
	isConflict          bool
	conflictContainsOMP bool
	client              string
	valid               bool
}

func parseExplicitClientMarker(headers http.Header) explicitMarkerResult {
	var res explicitMarkerResult
	if headers == nil {
		return res
	}
	var matchedKeys []string
	var rawValues []string
	for k, vs := range headers {
		if strings.EqualFold(k, explicitClientHeader) {
			matchedKeys = append(matchedKeys, k)
			rawValues = append(rawValues, vs...)
		}
	}
	if len(matchedKeys) == 0 {
		return res
	}
	sort.Strings(matchedKeys)
	res.matchedKeys = matchedKeys
	res.present = true

	clientSet := make(map[string]struct{})
	for _, raw := range rawValues {
		parts := strings.Split(raw, ",")
		for _, part := range parts {
			token := strings.TrimSpace(part)
			if token == "" {
				continue
			}
			norm := normalizeExplicitClientToken(token)
			if norm != "" {
				clientSet[norm] = struct{}{}
			}
		}
	}

	for c := range clientSet {
		res.uniqueClients = append(res.uniqueClients, c)
	}
	sort.Strings(res.uniqueClients)

	if len(res.uniqueClients) == 1 {
		res.client = res.uniqueClients[0]
		if res.client == "oh_my_pi" {
			res.isOMP = true
			res.valid = true
		} else {
			table := activeFilterConfig().ToolMappings[res.client]
			res.valid = len(table) > 0
		}
	} else if len(res.uniqueClients) > 1 {
		res.isConflict = true
		for _, c := range res.uniqueClients {
			if c == "oh_my_pi" {
				res.conflictContainsOMP = true
				break
			}
		}
	}
	return res
}

func normalizeExplicitClientToken(token string) string {
	lower := strings.ToLower(strings.TrimSpace(token))
	if canonical, ok := explicitClientAliasMap[lower]; ok {
		return canonical
	}
	return normalizeClientKey(token)
}

// resolveUserAgentClient returns a client derived from conservative
// User-Agent evidence. Only prefixes listed in userAgentEvidence may match,
// comparison is case-insensitive, and the match requires a usable active
// ToolMappings entry so entries like opencode/ stay inert until a table
// exists. The UA value is taken from the lexicographically first
// User-Agent key spelling for determinism, mirroring the explicit client
// marker resolution.
func resolveUserAgentClient(headers http.Header) (string, bool) {
	if headers == nil {
		return "", false
	}
	var matchedKeys []string
	for k := range headers {
		if strings.EqualFold(k, "User-Agent") {
			matchedKeys = append(matchedKeys, k)
		}
	}
	if len(matchedKeys) == 0 {
		return "", false
	}
	sort.Strings(matchedKeys)
	ua := ""
	if vs := headers[matchedKeys[0]]; len(vs) > 0 {
		ua = strings.TrimSpace(vs[0])
	}
	if ua == "" {
		return "", false
	}
	lowerUA := strings.ToLower(ua)
	for _, e := range userAgentEvidence {
		if strings.HasPrefix(lowerUA, strings.ToLower(e.prefix)) {
			if table := activeFilterConfig().ToolMappings[e.client]; len(table) > 0 {
				return e.client, true
			}
			return "", false
		}
	}
	return "", false
}

// sourceIdentityInventoryFor returns the static source identity inventory for a
// client, or nil when the client is detected from its runtime rename table.
func sourceIdentityInventoryFor(client string) map[string]bool {
	return sourceIdentityInventoriesByClient[client]
}

// detectClient identifies the client from ORIGINAL (uncloaked) tool names.
// It checks candidate source tool names against the provided tool name list.
// For oh_my_pi, detection keys off the static ompSourceIdentityInventory and
// clientDistinctiveTools rather than arbitrary runtime cfg.ToolMappings["oh_my_pi"].
// The client with the most key matches wins.
func detectClient(toolNames []string) string {
	cfg := activeFilterConfig()
	nameSet := make(map[string]bool, len(toolNames)*2)
	for _, n := range toolNames {
		nameSet[n] = true
		if _, base := splitToolNamespace(n); base != n {
			nameSet[base] = true
		}
	}
	bestClient := ""
	bestCount := 0

	// Candidates to evaluate: clients from cfg.ToolMappings plus "oh_my_pi"
	clients := make([]string, 0, len(cfg.ToolMappings)+1)
	for c := range cfg.ToolMappings {
		if c != "oh_my_pi" {
			clients = append(clients, c)
		}
	}
	clients = append(clients, "oh_my_pi")
	sort.Strings(clients)

	for _, client := range clients {
		count := 0
		// Clients with a static source identity inventory count against it
		// rather than against their runtime rename table: the two differ, and
		// Codex declares one surface or the other depending on the model's
		// tool mode, so a table-only count would drop whichever mode the
		// table does not key on.
		if inventory := sourceIdentityInventoryFor(client); inventory != nil {
			for orig := range inventory {
				if nameSet[orig] {
					count++
				}
			}
			// An operator can add source names to a client's rename table
			// through tool_mappings, and a static inventory cannot know them,
			// so those keys are counted here as well. OMP is exempt: its
			// attribution must stay on the canonical inventory and never take
			// arbitrary configured names.
			if client != "oh_my_pi" {
				for orig := range cfg.ToolMappings[client] {
					if !inventory[orig] && nameSet[orig] {
						count++
					}
				}
			}
		} else {
			cloakTable := cfg.ToolMappings[client]
			for orig := range cloakTable {
				if nameSet[orig] {
					count++
				}
			}
		}

		// Validation rules per client: clients whose source names are mostly
		// common words need either a distinctive harness tool or several
		// simultaneous matches, per clientDistinctiveTools.
		if distinctives := clientDistinctiveTools[client]; len(distinctives) > 0 {
			hasDistinctive := false
			for name := range distinctives {
				if nameSet[name] {
					hasDistinctive = true
					break
				}
			}
			if !hasDistinctive && count < minCollidingToolMatches {
				continue
			}
		}

		if count > bestCount {
			bestCount = count
			bestClient = client
		}
	}

	if bestCount >= minToolNameHits {
		return bestClient
	}
	return ""
}

// cloakTargetMatch records how many of one client's cloak targets appear in
// an observed tool-name set.
type cloakTargetMatch struct {
	client    string
	hits      int
	observed  int
	tableSize int
}

// higherRatioThan orders two matches by hit ratio over observed tools (m > o), using exact integer
// arithmetic. Equal ratios must be broken by the caller.
func (m cloakTargetMatch) higherRatioThan(o cloakTargetMatch) bool {
	return m.hits*o.observed > o.hits*m.observed
}

// atFullCoverage reports whether every one of the client's table targets matched.
func (m cloakTargetMatch) atFullCoverage() bool {
	return m.hits == m.tableSize
}

// detectCloakedClient identifies which client's cloaking was applied by
// checking cloak TARGET names against the observed tool identities.
// Namespace prefixes are normalised away so each declared tool contributes a
// single observed identity and the observed denominator is never inflated by
// an alias.
//
// Target names alone are NEVER sufficient to attribute traffic to oh_my_pi
// because all canonical OMP targets are native Antigravity tool names.
// Non-OMP clients continue to be detected according to their target tables.
func detectCloakedClient(toolNames []string) string {
	return detectCloakedClientWithSignal(toolNames, false)
}

// detectCloakedClientWithSignal extends detectCloakedClient with an explicit
// attribution signal parameter. When ompAttributed is false, oh_my_pi is excluded
// from standalone target-based attribution. When ompAttributed is true (e.g. from
// correlated request state, UA evidence, or source body evidence), the static
// canonical target inventory is used for corroboration rather than runtime tool_mappings.
func detectCloakedClientWithSignal(toolNames []string, ompAttributed bool) string {
	if len(toolNames) < 3 {
		return ""
	}
	cfg := activeFilterConfig()
	observedSet := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		_, base := splitToolNamespace(n)
		observedSet[base] = true
	}

	totalObserved := len(observedSet)
	var matches []cloakTargetMatch

	var clients []string
	if ompAttributed {
		// Corroborate exclusively against OMP's canonical target inventory when
		// an independent OMP attribution signal is already established; racing
		// claude_code here would trigger fullCoverageCount >= 2 because both
		// clients share the same 9 Tier-1 AGY targets.
		clients = []string{"oh_my_pi"}
	} else {
		for client := range cfg.ToolMappings {
			if client != "oh_my_pi" {
				clients = append(clients, client)
			}
		}
		sort.Strings(clients)
	}

	for _, client := range clients {
		hits := 0
		tableSize := 0
		if client == "oh_my_pi" {
			tableSize = len(ompCloakedTargetIdentityInventory)
			for target := range ompCloakedTargetIdentityInventory {
				if observedSet[target] {
					hits++
				}
			}
		} else {
			cloakTable := cfg.ToolMappings[client]
			tableSize = len(cloakTable)
			if tableSize == 0 {
				continue
			}
			for _, target := range cloakTable {
				if observedSet[target] {
					hits++
				}
			}
		}

		if hits >= 3 && hits*minCloakTargetHitDen >= totalObserved*minCloakTargetHitNum {
			matches = append(matches, cloakTargetMatch{
				client:    client,
				hits:      hits,
				observed:  totalObserved,
				tableSize: tableSize,
			})
		}
	}

	if len(matches) == 0 {
		return ""
	}

	// Native Antigravity superset check: if multiple distinct clients see 100% of their table
	// matched, this is native traffic serving every tool table, so cloaking is skipped.
	fullCoverageCount := 0
	for _, m := range matches {
		if m.atFullCoverage() {
			fullCoverageCount++
		}
	}
	if fullCoverageCount >= 2 {
		return ""
	}

	if len(matches) == 1 {
		return matches[0].client
	}

	// Multiple qualifying clients: rank by target-hit ratio over observed, breaking exact
	// ties deterministically by absolute hit count, full-table coverage, and finally by client id,
	// so repeated detections against identical input agree.
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.higherRatioThan(b) || b.higherRatioThan(a) {
			return a.higherRatioThan(b)
		}
		if a.hits != b.hits {
			return a.hits > b.hits
		}
		if a.atFullCoverage() != b.atFullCoverage() {
			return a.atFullCoverage()
		}
		return a.client < b.client
	})

	top, runnerUp := matches[0], matches[1]
	if top.higherRatioThan(runnerUp) {
		return top.client
	}
	if top.hits > runnerUp.hits {
		return top.client
	}
	if top.atFullCoverage() != runnerUp.atFullCoverage() {
		return top.client
	}
	// Full-coverage tie: both at full coverage → native Antigravity superset.
	if top.atFullCoverage() && runnerUp.atFullCoverage() {
		return ""
	}
	return top.client
}
