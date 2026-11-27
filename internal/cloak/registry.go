package cloak

import (
	"strings"

	"github.com/monet88/antigravity-cloak/internal/spec"
)

const (
	clientIDClaudeCode = "claude_code"
	clientIDCodex      = "codex"
	clientIDOMP        = "oh_my_pi"
)

// userAgentEvidence lists conservative User-Agent prefixes that may resolve a
// client, populated by initializeClients. Each entry is verified against active
// ToolMappings; an entry without a usable mapping (e.g. opencode before any
// table exists) stays inert and never invents a client.
var userAgentEvidence []struct {
	prefix string
	client string
}

const (
	ompSchemePreserve        = "://"
	ompClaudeCodeInstruction = "You are Claude Code, Anthropic's official CLI for Claude."
	codexIdentityLine        = "You are Codex, an agent based on GPT-6."
	codexGeneralIdentityLine = "You are Codex, an OpenAI general-purpose agentic assistant that helps the user complete tasks across coding, browsing, apps, documents, research, and other digital workflows."
)

var (
	mandatoryProtectedOMPAliases []string
	ompIdentityLines             []rewriteMapping
)

const (
	reverseBrandMatch       = "Antigravity"
	reverseBrandReplacement = "omp"
)

// ompProtectedReverseTable is Oh My Pi's entire reverse authority, populated by initializeClients.
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
	if client == "oh_my_pi" {
		return ompProtectedReverseTable
	}
	return reverseBrandMappingsFor(client)
}

// argumentReverseTablesByClient holds brandReverseTableFor(client) narrowed to
// the rules marked ArgumentSafe. Built once in init(), so the per-chunk stream
// path never allocates or filters a table.
var argumentReverseTablesByClient = map[string][]rewriteMapping{}

// argumentReverseTableFor returns the rules that may run over a tool-argument
// carrier for one client. See rewriteMapping.ArgumentSafe for why the prose
// rules are excluded: an argument is data the client executes.
func argumentReverseTableFor(client string) []rewriteMapping {
	return argumentReverseTablesByClient[client]
}

const (
	antigravityIdentity     = spec.AntigravityIdentity
	antigravityHostPreserve = spec.AntigravityHostPreserve
)

type rewriteMapping = spec.RewriteMapping

func pathRules(fromDir, toDir, fromFile, toFile string) []rewriteMapping {
	return spec.PathRules(fromDir, toDir, fromFile, toFile)
}

var (
	explicitClientAliasMap            map[string]string
	aliasPlanClients                  map[string]bool
	sharedAliasesByClient             map[string]map[string]string
	sourceIdentityInventoriesByClient map[string]map[string]bool

	brandMappingsByClient        map[string][]rewriteMapping
	reverseBrandMappingsByClient map[string][]rewriteMapping

	claudeCodeBrandMappings        []rewriteMapping
	claudeCodeReverseBrandMappings []rewriteMapping
	claudeContextMappings          []rewriteMapping
	claudeReverseContextMappings   []rewriteMapping
	claudeCodeSharedAliases        map[string]string

	codexBrandMappings           []rewriteMapping
	codexReverseBrandMappings    []rewriteMapping
	codexContextMappings         []rewriteMapping
	codexReverseContextMappings  []rewriteMapping
	codexSharedAliases           map[string]string
	codexSourceIdentityInventory map[string]bool

	ompBrandMappings                  []rewriteMapping
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

// initializeClients replaces the complete client registry and derived mapping
// tables from specs, then publishes defaultFilterConfig atomically. Repeated
// calls replace rather than accumulate state.
func Initialize(specs ...spec.ClientSpec) {
	explicitClientAliasMap = make(map[string]string)
	aliasPlanClients = make(map[string]bool)
	sharedAliasesByClient = make(map[string]map[string]string)
	sourceIdentityInventoriesByClient = make(map[string]map[string]bool)
	brandMappingsByClient = make(map[string][]rewriteMapping)
	reverseBrandMappingsByClient = make(map[string][]rewriteMapping)
	argumentReverseTablesByClient = make(map[string][]rewriteMapping)
	defaultCloakTables = make(map[string]map[string]string)
	defaultUncloakTables = make(map[string]map[string]string)
	clientDistinctiveTools = make(map[string]map[string]bool)
	userAgentEvidence = nil

	mandatoryProtectedOMPAliases = nil
	ompIdentityLines = nil
	ompProtectedReverseTable = nil
	ompBrandMappings = nil
	canonicalOMPSafeMappingSet = nil
	ompSourceIdentityInventory = nil
	ompSharedAliases = nil
	ompCloakedTargetIdentityInventory = nil

	claudeCodeBrandMappings = nil
	claudeCodeReverseBrandMappings = nil
	claudeContextMappings = nil
	claudeReverseContextMappings = nil
	claudeCodeSharedAliases = nil

	codexBrandMappings = nil
	codexReverseBrandMappings = nil
	codexContextMappings = nil
	codexReverseContextMappings = nil
	codexSharedAliases = nil
	codexSourceIdentityInventory = nil

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

		switch s.ID {
		case clientIDOMP:
			mandatoryProtectedOMPAliases = append([]string(nil), s.MandatoryBrandAliases...)
			ompIdentityLines = append([]rewriteMapping(nil), s.IdentityLines...)
			ompProtectedReverseTable = append([]rewriteMapping(nil), s.ProtectedReverseBrand...)
			ompBrandMappings = append([]rewriteMapping(nil), s.ForwardBrandMappings...)
			canonicalOMPSafeMappingSet = cloneStringMap(s.DefaultCloakTools)
			ompSourceIdentityInventory = cloneBoolMap(s.SourceIdentityTools)
			ompSharedAliases = cloneStringMap(s.SharedAliases)
			ompCloakedTargetIdentityInventory = cloneBoolMap(s.CloakedTargetTools)
			setDefaultOMPSanitizationPresets(s.PromptSanitizations)
		case clientIDClaudeCode:
			claudeContextMappings = append([]rewriteMapping(nil), s.ContextMappings...)
			claudeReverseContextMappings = append([]rewriteMapping(nil), s.ReverseContextMappings...)
			claudeCodeBrandMappings = append([]rewriteMapping(nil), s.ForwardBrandMappings...)
			claudeCodeReverseBrandMappings = append([]rewriteMapping(nil), s.ReverseBrandMappings...)
			claudeCodeSharedAliases = cloneStringMap(s.SharedAliases)
			setClaudeFingerprintData(s.NewsURLSanitization, s.AgentTier)
		case clientIDCodex:
			codexContextMappings = append([]rewriteMapping(nil), s.ContextMappings...)
			codexReverseContextMappings = append([]rewriteMapping(nil), s.ReverseContextMappings...)
			codexBrandMappings = append([]rewriteMapping(nil), s.ForwardBrandMappings...)
			codexReverseBrandMappings = append([]rewriteMapping(nil), s.ReverseBrandMappings...)
			codexSharedAliases = cloneStringMap(s.SharedAliases)
			codexSourceIdentityInventory = cloneBoolMap(s.SourceIdentityTools)
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
		argumentReverseTablesByClient[clientIDOMP] = filterArgumentReverseTable(ompProtectedReverseTable)
	}
	for client, cloaks := range defaultCloakTables {
		uncloaks := make(map[string]string, len(cloaks))
		for orig, mapped := range cloaks {
			uncloaks[mapped] = orig
		}
		defaultUncloakTables[client] = uncloaks
	}

	cfg := defaultFilterConfig()
	globalFilterConfig.Store(&cfg)
}
