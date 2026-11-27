package cloak

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

// Debug logging is opt-in. It writes full request/response bodies (which can
// contain user prompts and tool outputs) so it must never run for normal
// production traffic. Set CPA_FILTER_DEBUG to any non-empty value to enable it.
// The log file is opened lazily once and reused under a mutex to avoid the
// per-call open/close cost on hot streaming paths.
var (
	debugLogOnce    sync.Once
	debugLogMu      sync.Mutex
	debugLogFile    *os.File
	debugLogEnabled bool
)

func debugLog(format string, args ...any) {
	debugLogOnce.Do(func() {
		if os.Getenv("CPA_FILTER_DEBUG") == "" {
			return
		}
		f, err := os.OpenFile("logs/cpa-filter-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			f, err = os.OpenFile("cpa-filter-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		}
		if err == nil {
			debugLogFile = f
			debugLogEnabled = true
		}
	})
	if !debugLogEnabled || debugLogFile == nil {
		return
	}
	debugLogMu.Lock()
	fmt.Fprintf(debugLogFile, "[DEBUG] "+format+"\n", args...)
	debugLogMu.Unlock()
}

// normalizeSourceFormat maps the SDK's source-format identifiers onto the two
// branch families the interceptors understand. The proxy emits "claude" and
// "antigravity" (both Anthropic-shaped) and "codex"/"openai-response" (both
// OpenAI-shaped); collapse them so extractToolNames/cloak/uncloak hit the
// correct branch instead of silently no-oping on an unrecognized string.
func normalizeSourceFormat(sourceFormat string) string {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "anthropic", "claude", "antigravity":
		return "anthropic"
	case "openai", "openai-response", "codex":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(sourceFormat))
	}
}

// carriesOpenAIResponses reports whether the RAW source format identifies the
// OpenAI Responses wire surface. Issue #55 defers its two new transformations
// there: the shared helpers key on the normalized carrier, so the entry points
// check the raw spelling before Contract A/Contract B run. Normal behavior on
// that surface is unchanged.
func carriesOpenAIResponses(rawSourceFormat string) bool {
	return strings.EqualFold(strings.TrimSpace(rawSourceFormat), "openai-response")
}

// modelAllowsCloak reports whether cloaking should run for the current model.
// When ModelPrefixes is empty (the default), cloaking runs for every model so
// behavior matches the upstream "cloak by client" design. When non-empty,
// cloaking runs only if the upstream Model or the client RequestedModel starts
// with one of the configured prefixes (e.g. "agy/"). Both handlers and the
// stream path share this gate so request/response/stream stay consistent.
func modelAllowsCloak(model, requestedModel string) bool {
	prefixes := activeFilterConfig().ModelPrefixes
	if len(prefixes) == 0 {
		return true
	}
	candidates := [...]string{strings.TrimSpace(model), strings.TrimSpace(requestedModel)}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.HasPrefix(candidate, prefix) {
				return true
			}
		}
	}
	return false
}

func isAGYRoute(model, requestedModel string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), "agy/") ||
		strings.HasPrefix(strings.TrimSpace(requestedModel), "agy/")
}

func copyToolMappings(m map[string]map[string]string) map[string]map[string]string {
	if m == nil {
		return nil
	}
	res := make(map[string]map[string]string, len(m))
	for k, v := range m {
		if v == nil {
			res[k] = nil
			continue
		}
		inner := make(map[string]string, len(v))
		for ik, iv := range v {
			inner[ik] = iv
		}
		res[k] = inner
	}
	return res
}

type filterConfig struct {
	UseDefaultKeywords bool
	CustomMappings     []rewriteMapping
	ToolMappings       map[string]map[string]string // client → {orig_tool: antigravity_tool}
	// ModelPrefixes gates cloaking by model name. When non-empty, cloaking runs
	// only if the request/response model starts with one of these prefixes.
	// Empty means cloak for every model (legacy behavior, all providers).
	ModelPrefixes          []string
	OMPPromptSanitizations []ompSanitizationEntry

	// Pre-compiled regex patterns and ordered sanitization rules, rebuilt on config change.
	cloakRegexCache      map[string]*cachedCloakPatterns  // client → compiled cloak patterns
	uncloakRegexCache    map[string]*cachedUncloakPattern // client → compiled uncloak pattern
	ompSanitizationRules []ompSanitizationRule
}

// cachedCloakPatterns holds pre-compiled regexes for tool name replacement
// in descriptions and system messages (request cloaking path).
type cachedCloakPatterns struct {
	cloakTable map[string]string // orig → target (for identity replacement lookup)
	identRe    *regexp.Regexp    // single-pass identity replacement (quoted, namespaced, unambiguous)
	ambigRe    *regexp.Regexp    // single-pass contextual replacement for short/ambiguous words
}

// cachedUncloakPattern holds a pre-compiled regex for stream chunk uncloaking.
// Pattern matches: "name"\s*:\s*"(target1|target2|...)" in raw bytes.
type cachedUncloakPattern struct {
	re        *regexp.Regexp
	lookup    map[string]string // matched target → original name
	exactOnly bool              // when true, do not fall back to namespace base stripping
}

var (
	globalFilterConfig atomic.Pointer[filterConfig]
)

func defaultFilterConfig() filterConfig {
	cfg := filterConfig{
		UseDefaultKeywords: true,
		ToolMappings:       copyToolMappings(defaultCloakTables),
	}
	rebuildCachedRegexes(&cfg)
	return cfg
}

func applyFilterConfig(cfg filterConfig) {
	newCfg := filterConfig{
		UseDefaultKeywords:     cfg.UseDefaultKeywords,
		CustomMappings:         append([]rewriteMapping(nil), normalizeMappings(cfg.CustomMappings)...),
		ToolMappings:           copyToolMappings(cfg.ToolMappings),
		ModelPrefixes:          append([]string(nil), cfg.ModelPrefixes...),
		OMPPromptSanitizations: append([]ompSanitizationEntry(nil), cfg.OMPPromptSanitizations...),
	}
	rebuildCachedRegexes(&newCfg)
	globalFilterConfig.Store(&newCfg)
}

// rebuildCachedRegexes pre-compiles all regex patterns from the current
// ToolMappings and precomputes OMP prompt sanitization rules. Called once on
// config change, not on every request.
func rebuildCachedRegexes(cfg *filterConfig) {
	cfg.ompSanitizationRules = buildOMPSanitizationRules(cfg.OMPPromptSanitizations)
	cfg.cloakRegexCache = make(map[string]*cachedCloakPatterns, len(cfg.ToolMappings))
	cfg.uncloakRegexCache = make(map[string]*cachedUncloakPattern, len(cfg.ToolMappings))

	for client, cloakTable := range cfg.ToolMappings {
		// Build cloak patterns (for request path: tool name replacement in text)
		cp := &cachedCloakPatterns{
			cloakTable: cloakTable,
			identRe:    buildCloakIdentRe(cloakTable),
		}
		cp.ambigRe = buildCloakAmbiguousRe(cloakTable)
		cfg.cloakRegexCache[client] = cp

		// Build uncloak pattern (for stream path: regex-based tool name restore)
		targets := make([]string, 0, len(cloakTable))
		lookup := make(map[string]string, len(cloakTable))
		for orig, target := range cloakTable {
			targets = append(targets, regexp.QuoteMeta(target))
			lookup[target] = orig
		}
		if len(targets) > 0 {
			// Match "name" : "(?:[a-zA-Z0-9_-]+:)?<target>" with flexible whitespace
			pattern := `"name"\s*:\s*"((?:[a-zA-Z0-9_-]+:)?(?:` + strings.Join(targets, "|") + `))"`
			if re, err := regexp.Compile(pattern); err == nil {
				cfg.uncloakRegexCache[client] = &cachedUncloakPattern{
					re:     re,
					lookup: lookup,
				}
			}
		}
	}
}

func activeFilterConfig() *filterConfig {
	cfg := globalFilterConfig.Load()
	if cfg == nil {
		d := defaultFilterConfig()
		globalFilterConfig.CompareAndSwap(nil, &d)
		return globalFilterConfig.Load()
	}
	return cfg
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

func filterConfigFromLifecycleRequest(request []byte) (filterConfig, error) {
	var req lifecycleRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return filterConfig{}, fmt.Errorf("decode lifecycle request: %w", err)
	}
	return parseFilterConfigYAML(req.ConfigYAML)
}

func parseFilterConfigYAML(raw []byte) (filterConfig, error) {
	cfg := defaultFilterConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}

	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return filterConfig{}, fmt.Errorf("decode config yaml: %w", err)
	}
	if value, exists := values["use_default_keywords"]; exists {
		boolValue, ok := value.(bool)
		if !ok {
			return filterConfig{}, fmt.Errorf("use_default_keywords must be a boolean")
		}
		cfg.UseDefaultKeywords = boolValue
	}
	if value, exists := values["custom_mappings"]; exists {
		mappings, err := parseCustomMappings(value)
		if err != nil {
			return filterConfig{}, err
		}
		cfg.CustomMappings = mappings
	}
	if value, exists := values["tool_mappings"]; exists {
		parsedMappings, err := parseToolMappings(value)
		if err != nil {
			return filterConfig{}, err
		}
		if cfg.ToolMappings == nil {
			cfg.ToolMappings = make(map[string]map[string]string)
		}
		for client, mappings := range parsedMappings {
			if cfg.ToolMappings[client] == nil {
				cfg.ToolMappings[client] = make(map[string]string)
			}
			for orig, target := range mappings {
				cfg.ToolMappings[client][orig] = target
			}
		}
		for client, mappings := range parsedMappings {
			if client == "oh_my_pi" {
				if err := validateOMPConfigMappings(cfg.ToolMappings[client]); err != nil {
					return filterConfig{}, fmt.Errorf("tool_mappings oh_my_pi: %w", err)
				}
			} else if clientUsesAliasPlan(client) {
				if err := validateAliasPlanConfigMappings(client, mappings); err != nil {
					return filterConfig{}, fmt.Errorf("tool_mappings %s: %w", client, err)
				}
			}
		}
	}
	if value, exists := values["model_prefixes"]; exists {
		prefixes, err := parseModelPrefixes(value)
		if err != nil {
			return filterConfig{}, err
		}
		cfg.ModelPrefixes = prefixes
	}
	if value, exists := values["omp_prompt_sanitizations"]; exists {
		sanitizations, err := parseOMPPromptSanitizations(value)
		if err != nil {
			return filterConfig{}, err
		}
		cfg.OMPPromptSanitizations = sanitizations
	}
	return cfg, nil
}

// parseModelPrefixes accepts an array of strings, a comma/newline-separated
// string, or a single string, and returns the trimmed, non-empty prefixes.
func parseModelPrefixes(value any) ([]string, error) {
	appendPrefix := func(out []string, raw string) []string {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		}) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	var prefixes []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		prefixes = appendPrefix(prefixes, typed)
	case []any:
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("model_prefixes entries must be strings")
			}
			prefixes = appendPrefix(prefixes, str)
		}
	default:
		return nil, fmt.Errorf("model_prefixes must be an array or string")
	}
	return prefixes, nil
}

// normalizeClientKey canonicalizes a configured client id to the key space
// used by defaultCloakTables and the detection logic: lowercase, with known
// aliases folded onto their canonical table key.
func normalizeClientKey(client string) string {
	key := strings.ToLower(strings.TrimSpace(client))
	switch key {
	case "omp", "oh-my-pi":
		return "oh_my_pi"
	default:
		return key
	}
}

// validateOMPConfigMappings validates Oh My Pi tool mappings at configuration time.
// It enforces that canonical nine mappings are immutable, valid noncanonical custom
// mappings are honored, and invalid, non-injective, or forbidden target names fail
// visibly at configuration/reconfigure time.
func validateOMPConfigMappings(mappings map[string]string) error {
	ownerByTarget := make(map[string]string, len(mappings)+len(canonicalOMPSafeMappingSet)+len(ompSharedAliases))
	ownerByFinalBase := make(map[string]string, len(mappings)+len(canonicalOMPSafeMappingSet)+len(ompSharedAliases))

	// Pre-populate canonical nine targets
	for orig, target := range canonicalOMPSafeMappingSet {
		ownerByTarget[target] = orig
		_, base := splitToolNamespace(target)
		ownerByFinalBase[base] = orig
	}

	// Pre-populate shared alias targets (ompSharedAliases)
	for orig, target := range ompSharedAliases {
		ownerByTarget[target] = orig
		_, base := splitToolNamespace(target)
		ownerByFinalBase[base] = orig
	}

	for orig, target := range mappings {
		origTrimmed := strings.TrimSpace(orig)
		targetTrimmed := strings.TrimSpace(target)
		if origTrimmed == "" {
			return fmt.Errorf("empty mapping source")
		}
		if targetTrimmed == "" {
			return fmt.Errorf("empty mapping target for %q", orig)
		}
		if strings.ContainsAny(target, " \t\r\n") {
			return fmt.Errorf("forbidden target naming %q for %q: contains whitespace", target, orig)
		}
		if strings.HasPrefix(target, "_") || strings.HasPrefix(target, ":") {
			return fmt.Errorf("forbidden target naming %q for %q: leading underscore or colon is reserved", target, orig)
		}

		// Canonical nine immutability check
		if canonicalTarget, isCanonical := canonicalOMPSafeMappingSet[orig]; isCanonical {
			if target != canonicalTarget {
				return fmt.Errorf("canonical OMP mapping %q is immutable (cannot remap to %q)", orig, target)
			}
			continue
		}

		// Non-canonical mapping: cannot map to a canonical target or collide with another mapping
		if canonicalOwner, exists := ownerByTarget[target]; exists && canonicalOwner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to already-assigned target %q (owned by %q)", orig, target, canonicalOwner)
		}
		_, finalBase := splitToolNamespace(target)
		if canonicalOwner, exists := ownerByFinalBase[finalBase]; exists && canonicalOwner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to target %q with base %q (owned by %q)", orig, target, finalBase, canonicalOwner)
		}
		ownerByTarget[target] = orig
		ownerByFinalBase[finalBase] = orig
	}
	return nil
}

// validateAliasPlanConfigMappings validates tool mappings for alias-plan clients (claude_code, codex)
// at configuration time, enforcing identical target naming rules (whitespace, empty, leading underscore/colon)
// and injectivity against the client's reserved tables and within custom mappings.
func validateAliasPlanConfigMappings(client string, mappings map[string]string) error {
	client = normalizeClientKey(client)
	ownerByTarget := make(map[string]string)
	ownerByFinalBase := make(map[string]string)

	baseline := make(map[string]string)
	if staticTier1, ok := defaultCloakTables[client]; ok {
		for orig, target := range staticTier1 {
			baseline[orig] = target
		}
	}
	var shared map[string]string
	switch client {
	case "claude_code":
		shared = claudeCodeSharedAliases
	case "codex":
		shared = codexSharedAliases
	}
	if shared != nil {
		for orig, target := range shared {
			if client == "codex" && orig == "exec_command" && target == "run_command" {
				continue
			}
			baseline[orig] = target
		}
	}

	for bOrig, bTarget := range baseline {
		if _, remapped := mappings[bOrig]; remapped {
			continue
		}
		ownerByTarget[bTarget] = bOrig
		_, base := splitToolNamespace(bTarget)
		ownerByFinalBase[base] = bOrig
	}

	keys := make([]string, 0, len(mappings))
	for k := range mappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, orig := range keys {
		target := mappings[orig]
		origTrimmed := strings.TrimSpace(orig)
		targetTrimmed := strings.TrimSpace(target)
		if origTrimmed == "" {
			return fmt.Errorf("empty mapping source")
		}
		if targetTrimmed == "" {
			return fmt.Errorf("empty mapping target for %q", orig)
		}
		if strings.ContainsAny(target, " \t\r\n") {
			return fmt.Errorf("forbidden target naming %q for %q: contains whitespace", target, orig)
		}
		if strings.HasPrefix(target, "_") || strings.HasPrefix(target, ":") {
			return fmt.Errorf("forbidden target naming %q for %q: leading underscore or colon is reserved", target, orig)
		}

		if owner, exists := ownerByTarget[target]; exists && owner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to already-assigned target %q (owned by %q)", orig, target, owner)
		}
		_, finalBase := splitToolNamespace(target)
		if owner, exists := ownerByFinalBase[finalBase]; exists && owner != orig {
			return fmt.Errorf("non-injective target naming: %q cannot map to target %q with base %q (owned by %q)", orig, target, finalBase, owner)
		}
		ownerByTarget[target] = orig
		ownerByFinalBase[finalBase] = orig
	}
	return nil
}

func parseToolMappings(value any) (map[string]map[string]string, error) {
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool_mappings must be an object")
	}
	result := make(map[string]map[string]string)
	for client, clientVal := range typed {
		clientMap, err := parseStringMap(clientVal)
		if err != nil {
			return nil, fmt.Errorf("client %q: %w", client, err)
		}
		// Merge rather than overwrite so multiple case variants of the same
		// client (e.g. "Claude_Code" and "claude_code") combine deterministically.
		normalized := normalizeClientKey(client)
		if result[normalized] == nil {
			result[normalized] = make(map[string]string, len(clientMap))
		}
		for orig, target := range clientMap {
			result[normalized][orig] = target
		}
		if normalized == "oh_my_pi" {
			if err := validateOMPConfigMappings(result[normalized]); err != nil {
				return nil, fmt.Errorf("client %q: %w", client, err)
			}
		} else if clientUsesAliasPlan(normalized) {
			if err := validateAliasPlanConfigMappings(normalized, result[normalized]); err != nil {
				return nil, fmt.Errorf("client %q: %w", client, err)
			}
		}
	}
	return result, nil
}

func parseStringMap(value any) (map[string]string, error) {
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	res := make(map[string]string)
	for k, v := range typed {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("value for key %q must be a string", k)
		}
		res[k] = s
	}
	return res, nil
}

type configKeyValue struct {
	Key   string
	Value string
}

func parseConfigKeyValues(fieldName string, value any, trimStringSides, requireListPair bool) ([]configKeyValue, error) {
	parseString := func(raw string) ([]configKeyValue, error) {
		parts := strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		})
		out := make([]configKeyValue, 0, len(parts))
		for _, entry := range parts {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			k, v, ok := strings.Cut(entry, ":")
			if !ok {
				return nil, fmt.Errorf("%s entries must use match: replacement", fieldName)
			}
			if trimStringSides {
				k = strings.TrimSpace(k)
				v = strings.TrimSpace(v)
			}
			out = append(out, configKeyValue{Key: k, Value: v})
		}
		return out, nil
	}

	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return parseString(typed)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for k := range typed {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		pairs := make([]configKeyValue, 0, len(typed))
		for _, k := range keys {
			text, ok := typed[k].(string)
			if !ok {
				return nil, fmt.Errorf("%s values must be strings", fieldName)
			}
			pairs = append(pairs, configKeyValue{Key: k, Value: text})
		}
		return pairs, nil
	case []any:
		pairs := make([]configKeyValue, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s entries must be strings", fieldName)
			}
			parsed, err := parseString(text)
			if err != nil {
				return nil, err
			}
			if requireListPair && len(parsed) == 0 {
				return nil, fmt.Errorf("%s entries must use match: replacement", fieldName)
			}
			pairs = append(pairs, parsed...)
		}
		return pairs, nil
	default:
		return nil, fmt.Errorf("%s must be an object, array, or string", fieldName)
	}
}

func parseCustomMappings(value any) ([]rewriteMapping, error) {
	pairs, err := parseConfigKeyValues("custom_mappings", value, false, false)
	if err != nil {
		return nil, err
	}
	if pairs == nil {
		return nil, nil
	}
	mappings := make([]rewriteMapping, 0, len(pairs))
	for _, p := range pairs {
		mappings = append(mappings, rewriteMapping{Match: p.Key, Replacement: p.Value})
	}
	return mappings, nil
}

func effectiveMappings(cfg *filterConfig, client string) []rewriteMapping {
	var mappings []rewriteMapping
	if cfg == nil || cfg.UseDefaultKeywords {
		mappings = append(mappings, brandMappingsFor(client)...)
	}
	if cfg != nil && len(cfg.CustomMappings) > 0 {
		mappings = append(mappings, cfg.CustomMappings...)
	}
	// Client-scoped rules are dropped here so every downstream consumer
	// (system fields, tool descriptions, system messages) sees one flat list.
	scoped := make([]rewriteMapping, 0, len(mappings))
	for _, m := range mappings {
		if m.Client != "" && m.Client != client {
			continue
		}
		scoped = append(scoped, m)
	}
	return normalizeMappings(scoped)
}

func normalizeMappings(mappings []rewriteMapping) []rewriteMapping {
	seen := make(map[string]struct{}, len(mappings))
	reversed := make([]rewriteMapping, 0, len(mappings))
	for i := len(mappings) - 1; i >= 0; i-- {
		match := strings.ToLower(strings.TrimSpace(mappings[i].Match))
		replacement := strings.TrimSpace(mappings[i].Replacement)
		if match == "" || replacement == "" {
			continue
		}
		// Dedup key includes the client scope: the same match may legitimately
		// map differently depending on which client sent the request.
		key := match + "\x00" + mappings[i].Client
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		reversed = append(reversed, rewriteMapping{
			Match:         match,
			Replacement:   replacement,
			Client:        mappings[i].Client,
			WholeSegment:  mappings[i].WholeSegment,
			SegmentEnd:    mappings[i].SegmentEnd,
			NotFollowedBy: mappings[i].NotFollowedBy,
		})
	}
	out := make([]rewriteMapping, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		out = append(out, reversed[i])
	}
	return out
}
