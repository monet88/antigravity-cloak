package cloak

import (
	"strings"

	"github.com/monet88/antigravity-cloak/internal/spec"
)

type rewriteMapping = spec.RewriteMapping

const (
	clientIDCodex = "codex"
	clientIDOMP   = "oh_my_pi"
)

// userAgentEvidence lists conservative User-Agent prefixes that may resolve a
// client, populated by Initialize. Each entry is verified against active
// ToolMappings; an entry without a usable mapping (e.g. opencode before any
// table exists) stays inert and never invents a client.
var userAgentEvidence []struct {
	prefix string
	client string
}

var (
	ompSchemePreserve            string
	mandatoryProtectedOMPAliases []string
	ompIdentityLines             []rewriteMapping
)

// ompProtectedReverseTable is Oh My Pi's entire reverse authority, populated by Initialize.
var ompProtectedReverseTable []rewriteMapping

// filterArgumentReverseTable keeps the rules a tool-argument carrier may run:
// the operational identifiers (paths, domains, SDK and skill names) the model
// can legitimately echo from the cloaked prompt. Prose rules stay out of
// arguments - see rewriteMapping.ArgumentSafe.
func filterArgumentReverseTable(table []rewriteMapping) []rewriteMapping {
	out := make([]rewriteMapping, 0, len(table))
	for _, m := range table {
		if m.ArgumentSafe {
			out = append(out, m)
		}
	}
	return out
}

// brandReverseTableFor returns the table used to resolve and flush a session's
// carried tokens: the protected pair for Oh My Pi, that client's own reverse
// table for every other resolved client.
func brandReverseTableFor(client string) []rewriteMapping {
	if protectedRouteClients[client] {
		return ompProtectedReverseTable
	}
	return reverseBrandMappingsFor(client)
}

// argumentReverseTablesByClient holds brandReverseTableFor(client) narrowed to
// the rules marked ArgumentSafe. Built once in Initialize, so the per-chunk stream
// path never allocates or filters a table.
var argumentReverseTablesByClient = map[string][]rewriteMapping{}

// argumentReverseTableFor returns the rules that may run over a tool-argument
// carrier for one client. See rewriteMapping.ArgumentSafe for why the prose
// rules are excluded: an argument is data the client executes.
func argumentReverseTableFor(client string) []rewriteMapping {
	return argumentReverseTablesByClient[client]
}

var (
	explicitClientAliasMap            map[string]string
	aliasPlanClients                  map[string]bool
	protectedRouteClients             map[string]bool
	sharedAliasesByClient             map[string]map[string]string
	sourceIdentityInventoriesByClient map[string]map[string]bool

	brandMappingsByClient        map[string][]rewriteMapping
	reverseBrandMappingsByClient map[string][]rewriteMapping

	canonicalOMPSafeMappingSet        map[string]string
	ompSourceIdentityInventory        map[string]bool
	ompSharedAliases                  map[string]string
	ompCloakedTargetIdentityInventory map[string]bool

	defaultCloakTables     map[string]map[string]string
	defaultUncloakTables   map[string]map[string]string
	clientDistinctiveTools map[string]map[string]bool
)

func brandMappingsFor(client string) []rewriteMapping {
	return brandMappingsByClient[client]
}

func reverseBrandMappingsFor(client string) []rewriteMapping {
	return reverseBrandMappingsByClient[client]
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Initialize replaces the complete client registry and derived mapping
// tables from specs, then publishes defaultFilterConfig atomically. Repeated
// calls replace rather than accumulate state.
func Initialize(specs ...spec.ClientSpec) {
	explicitClientAliasMap = make(map[string]string)
	aliasPlanClients = make(map[string]bool)
	protectedRouteClients = make(map[string]bool)
	sharedAliasesByClient = make(map[string]map[string]string)
	sourceIdentityInventoriesByClient = make(map[string]map[string]bool)
	brandMappingsByClient = make(map[string][]rewriteMapping)
	reverseBrandMappingsByClient = make(map[string][]rewriteMapping)
	argumentReverseTablesByClient = make(map[string][]rewriteMapping)
	defaultCloakTables = make(map[string]map[string]string)
	defaultUncloakTables = make(map[string]map[string]string)
	clientDistinctiveTools = make(map[string]map[string]bool)
	userAgentEvidence = nil

	ompSchemePreserve = ""
	mandatoryProtectedOMPAliases = nil
	ompIdentityLines = nil
	ompProtectedReverseTable = nil
	canonicalOMPSafeMappingSet = nil
	ompSourceIdentityInventory = nil
	ompSharedAliases = nil
	ompCloakedTargetIdentityInventory = nil

	setDefaultOMPSanitizationPresets(nil)
	setClaudeFingerprintData(nil, nil)

	for _, s := range specs {
		for _, alias := range s.ExplicitAliases {
			if norm := strings.ToLower(strings.TrimSpace(alias)); norm != "" {
				explicitClientAliasMap[norm] = s.ID
			}
		}
		for _, prefix := range s.UserAgentPrefixes {
			userAgentEvidence = append(userAgentEvidence, struct {
				prefix string
				client string
			}{prefix: prefix, client: s.ID})
		}
		if s.UsesAliasPlan {
			aliasPlanClients[s.ID] = true
		}
		if len(s.ForwardBrandMappings) > 0 {
			brandMappingsByClient[s.ID] = append([]rewriteMapping(nil), s.ForwardBrandMappings...)
		}
		if len(s.ReverseBrandMappings) > 0 {
			reverseBrandMappingsByClient[s.ID] = append([]rewriteMapping(nil), s.ReverseBrandMappings...)
		}
		if len(s.DefaultCloakTools) > 0 {
			defaultCloakTables[s.ID] = cloneStringMap(s.DefaultCloakTools)
		}
		if len(s.SharedAliases) > 0 {
			sharedAliasesByClient[s.ID] = cloneStringMap(s.SharedAliases)
		}
		if len(s.SourceIdentityTools) > 0 {
			sourceIdentityInventoriesByClient[s.ID] = cloneBoolMap(s.SourceIdentityTools)
		}
		if len(s.DistinctiveTools) > 0 {
			clientDistinctiveTools[s.ID] = cloneBoolMap(s.DistinctiveTools)
		}

		if s.HasProtectedRoute {
			protectedRouteClients[s.ID] = true
			ompSchemePreserve = s.SchemePreserve
			mandatoryProtectedOMPAliases = append([]string(nil), s.MandatoryBrandAliases...)
			ompIdentityLines = append([]rewriteMapping(nil), s.IdentityLines...)
			ompProtectedReverseTable = append([]rewriteMapping(nil), s.ProtectedReverseBrand...)
			canonicalOMPSafeMappingSet = cloneStringMap(s.DefaultCloakTools)
			ompSourceIdentityInventory = cloneBoolMap(s.SourceIdentityTools)
			ompSharedAliases = cloneStringMap(s.SharedAliases)
			ompCloakedTargetIdentityInventory = cloneBoolMap(s.CloakedTargetTools)
			setDefaultOMPSanitizationPresets(s.PromptSanitizations)
		}
		if s.NewsURLSanitization != nil || s.AgentTier != nil {
			setClaudeFingerprintData(s.NewsURLSanitization, s.AgentTier)
		}
	}

	// Inert User-Agent guard entry: stays inert until a usable ToolMappings table exists.
	userAgentEvidence = append(userAgentEvidence, struct {
		prefix string
		client string
	}{prefix: "opencode/", client: "opencode"})

	for client, table := range reverseBrandMappingsByClient {
		argumentReverseTablesByClient[client] = filterArgumentReverseTable(table)
	}
	if len(ompProtectedReverseTable) > 0 {
		for client := range protectedRouteClients {
			argumentReverseTablesByClient[client] = filterArgumentReverseTable(ompProtectedReverseTable)
		}
	}
	for client, cloaks := range defaultCloakTables {
		uncloaks := make(map[string]string, len(cloaks))
		for orig, mapped := range cloaks {
			uncloaks[mapped] = orig
		}
		defaultUncloakTables[client] = uncloaks
	}

	applyFilterConfig(defaultFilterConfig())
}
