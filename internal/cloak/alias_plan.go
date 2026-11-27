package cloak

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type explicitOMPRouteKind int

const (
	routeKindNone explicitOMPRouteKind = iota
	routeKindProtectedAGY
	routeKindExplicitOMPNonAGYBypass
)

type streamDisposition int32

const (
	streamDispositionNone streamDisposition = iota
	streamDispositionPayloadActive
	streamDispositionCleanTerminal
)

type requestAliasPair struct {
	SourceIdentity   string
	UpstreamIdentity string
	Changed          bool
}

type requestAliasPlan struct {
	client          string
	sourceFormat    string
	pairs           []requestAliasPair
	forward         map[string]string
	declaredForward map[string]string
	reverse         map[string]string
	cachedUncloak   *cachedUncloakPattern
	expected        int
	disposition     atomic.Int32
	// tier is the pinned Issue #55 Agent model-tier authority. It is set only
	// when the request declared an eligible native Agent declaration; a nil
	// tier activates no tier behavior anywhere on the request/response/stream
	// cycle. It lives and dies with the plan (request.complete cleanup).
	tier *requestTierAuthority
}

func (p *requestAliasPlan) getDisposition() streamDisposition {
	if p == nil {
		return streamDispositionNone
	}
	return streamDisposition(p.disposition.Load())
}

func (p *requestAliasPlan) setDisposition(target streamDisposition) {
	if p == nil {
		return
	}
	for {
		cur := p.disposition.Load()
		if streamDisposition(cur) >= target {
			return
		}
		if p.disposition.CompareAndSwap(cur, int32(target)) {
			return
		}
	}
}

func fallbackAliasForSource(source string) string {
	sum := sha256.Sum256([]byte("request-alias-v1\x00" + source))
	return fmt.Sprintf("wp_ext_%x", sum[:16])
}

func buildRequestAliasUncloakPattern(reverse map[string]string) (*cachedUncloakPattern, error) {
	if len(reverse) == 0 {
		return nil, nil
	}
	targets := make([]string, 0, len(reverse))
	lookup := make(map[string]string, len(reverse))
	for target, source := range reverse {
		targets = append(targets, target)
		lookup[target] = source
	}
	sort.Strings(targets)
	escaped := make([]string, 0, len(targets))
	for _, target := range targets {
		escaped = append(escaped, regexp.QuoteMeta(target))
	}
	pattern := "\"name\"[[:space:]]*:[[:space:]]*\"(" + strings.Join(escaped, "|") + ")\""
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &cachedUncloakPattern{re: re, lookup: lookup, exactOnly: true}, nil
}

func buildRequestAliasPlan(client string, sourceIdentities []string, preferred map[string]string, declared ...[]string) (*requestAliasPlan, error) {
	client = normalizeClientKey(client)
	if client == "" {
		return nil, fmt.Errorf("client identity is required")
	}

	unique := make(map[string]struct{}, len(sourceIdentities))
	for _, source := range sourceIdentities {
		if source == "" {
			return nil, fmt.Errorf("source identity is required")
		}
		unique[source] = struct{}{}
	}
	sources := make([]string, 0, len(unique))
	for source := range unique {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	declaredSet := make(map[string]bool)
	if len(declared) > 0 {
		for _, d := range declared[0] {
			declaredSet[d] = true
			_, dBase := splitToolNamespace(d)
			declaredSet[dBase] = true
		}
	}

	plan := &requestAliasPlan{
		client:          client,
		pairs:           make([]requestAliasPair, 0, len(sources)),
		forward:         make(map[string]string, len(sources)),
		declaredForward: make(map[string]string, len(declaredSet)),
		reverse:         make(map[string]string, len(sources)),
	}
	ownerByTarget := make(map[string]string, len(sources))
	ownerByFinalBase := make(map[string]string, len(sources))
	knownTargets := make(map[string]bool, len(preferred)*2)
	for _, t := range preferred {
		_, tBase := splitToolNamespace(t)
		knownTargets[tBase] = true
		knownTargets[t] = true
	}

	for _, source := range sources {
		prefix, base := splitToolNamespace(source)
		target, overridden := preferred[source]
		if !overridden {
			if targetBase, baseOverridden := preferred[base]; baseOverridden {
				if prefix != "" {
					target = prefix + targetBase
				} else {
					target = targetBase
				}
				overridden = true
			} else if knownTargets[base] {
				// Source declares a native target identity (e.g. read_file or default_api:read_file).
				if prefix != "" {
					target = prefix + base
				} else {
					target = base
				}
				overridden = true
			}
		}
		if overridden {
			if target == "" {
				return nil, fmt.Errorf("empty alias target for %q", source)
			}
		} else {
			target = fallbackAliasForSource(base)
		}

		_, finalBase := splitToolNamespace(target)
		ownerTarget, targetConflict := ownerByTarget[target]
		ownerBase, baseConflict := ownerByFinalBase[finalBase]
		hasCollision := (targetConflict && ownerTarget != source) || (baseConflict && ownerBase != source)

		if hasCollision {
			if targetConflict && ownerTarget != source {
				return nil, fmt.Errorf("alias collision: %q and %q resolve to %q", ownerTarget, source, target)
			}
			return nil, fmt.Errorf("alias collision: %q and %q resolve to same final base %q", ownerBase, source, finalBase)
		}
		ownerByTarget[target] = source
		ownerByFinalBase[finalBase] = source

		changed := source != target
		plan.pairs = append(plan.pairs, requestAliasPair{
			SourceIdentity: source, UpstreamIdentity: target, Changed: changed,
		})
		plan.forward[source] = target
		if declaredSet[source] || declaredSet[base] {
			plan.declaredForward[source] = target
		}
		if changed {
			plan.reverse[target] = source
		}
	}
	cached, err := buildRequestAliasUncloakPattern(plan.reverse)
	if err != nil {
		return nil, fmt.Errorf("compile alias reverse pattern: %w", err)
	}
	plan.cachedUncloak = cached
	return plan, nil
}

type requestAliasPlanManager struct {
	mu    sync.Mutex
	plans map[string]*requestAliasPlan
}

func newRequestAliasPlanManager() *requestAliasPlanManager {
	return &requestAliasPlanManager{plans: make(map[string]*requestAliasPlan)}
}

var globalAliasPlanManager = newRequestAliasPlanManager()

func (m *requestAliasPlanManager) set(requestID string, plan *requestAliasPlan) error {
	if requestID == "" || plan == nil {
		return fmt.Errorf("request alias plan requires correlation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.plans[requestID]; exists {
		return fmt.Errorf("request alias plan already exists for %q", requestID)
	}
	m.plans[requestID] = plan
	return nil
}

func (m *requestAliasPlanManager) get(requestID string) *requestAliasPlan {
	if requestID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plans[requestID]
}

func (m *requestAliasPlanManager) delete(requestID string) {
	if requestID == "" {
		return
	}
	m.mu.Lock()
	delete(m.plans, requestID)
	m.mu.Unlock()
}

func requestAliasSourceIdentities(body []byte, sourceFormat string) ([]string, []string, error) {
	format := normalizeSourceFormat(sourceFormat)
	if format != "openai" && format != "anthropic" {
		return nil, nil, fmt.Errorf("unsupported source format %q", sourceFormat)
	}
	root, ok := decodeStrictProtectedJSON(body)
	if !ok {
		return nil, nil, fmt.Errorf("request body must be one strict JSON object")
	}
	return collectRequestSourceIdentitiesWithDeclared(root, format)
}

func admitRequestAliasPlan(req *pluginapi.RequestInterceptRequest, client string, preferred map[string]string) (*requestAliasPlan, error) {
	if req == nil || req.RequestID == "" {
		return nil, fmt.Errorf("request correlation is required")
	}
	sources, declared, err := requestAliasSourceIdentities(req.Body, req.SourceFormat)
	if err != nil {
		return nil, err
	}
	plan, err := buildRequestAliasPlan(client, sources, preferred, declared)
	if err != nil {
		return nil, err
	}
	plan.sourceFormat = normalizeSourceFormat(req.SourceFormat)
	plan.expected = requestChoiceCount(req.Body)
	// Pin the Agent model-tier authority BEFORE the session is created, so
	// every path that rebuilds the session from the plan carries it (spec:
	// active reversal survives reload and disposable cleanup). Detection reads
	// the RAW client body and resolves through the plan's forward map; it
	// mutates nothing. Skipped on the deferred /v1/responses surface.
	if !carriesOpenAIResponses(req.SourceFormat) {
		plan.tier = detectAgentTierAuthority(req.Body, plan.sourceFormat, plan)
	}
	if err := globalAliasPlanManager.set(req.RequestID, plan); err != nil {
		return nil, err
	}
	globalStreamManager.resetSession("req:"+req.RequestID, plan.client, plan.cachedUncloak, plan.expected)
	globalStreamManager.attachTier("req:"+req.RequestID, plan.tier)
	return plan, nil
}

func admitRequestAliasPlanOrReject(req *pluginapi.RequestInterceptRequest, resp pluginapi.RequestInterceptResponse, client string, preferred map[string]string) (*requestAliasPlan, []byte) {
	plan, err := admitRequestAliasPlan(req, client, preferred)
	if err == nil {
		return plan, nil
	}
	requestID := ""
	if req != nil {
		requestID = req.RequestID
	}
	debugLog("request alias admission rejected RequestID=%q client=%q: %v", requestID, client, err)
	return nil, toolCloak503Response(resp)
}

// sharedAliasesFor returns the merged preferred alias map for a client: the
// static cloak table targets plus the client's shared/neutral aliases.
func sharedAliasesFor(client string) map[string]string {
	cfg := activeFilterConfig()
	cloakTable := cfg.ToolMappings[client]
	shared, ok := sharedAliasesByClient[client]
	if !ok {
		if cloakTable == nil {
			return nil
		}
		return cloakTable
	}
	merged := make(map[string]string, len(cloakTable)+len(shared))
	for k, v := range shared {
		merged[k] = v
	}
	for k, v := range cloakTable {
		merged[k] = v
	}
	if client == clientIDOMP {
		for k, v := range canonicalOMPSafeMappingSet {
			merged[k] = v
		}
	}
	return merged
}

// clientUsesAliasPlan reports whether a client should use the request-scoped
// alias plan machinery for full declaration cloaking.
func clientUsesAliasPlan(client string) bool {
	return aliasPlanClients[client]
}

type explicitOMPRouteState struct {
	routeKind               explicitOMPRouteKind
	client                  string
	activeReverse           map[string]string
	cachedUncloak           *cachedUncloakPattern
	brandRestorationEnabled bool
	expected                int
	disposition             atomic.Int32
	malformed               bool
}

func (s *explicitOMPRouteState) getDisposition() streamDisposition {
	if s == nil {
		return streamDispositionNone
	}
	return streamDisposition(s.disposition.Load())
}

func (s *explicitOMPRouteState) setDisposition(target streamDisposition) {
	if s == nil {
		return
	}
	for {
		cur := s.disposition.Load()
		if streamDisposition(cur) >= target {
			return
		}
		if s.disposition.CompareAndSwap(cur, int32(target)) {
			return
		}
	}
}

type explicitOMPLifecycleManager struct {
	mu     sync.Mutex
	routes map[string]*explicitOMPRouteState
}

var globalLifecycleManager = newExplicitOMPLifecycleManager()

func newExplicitOMPLifecycleManager() *explicitOMPLifecycleManager {
	return &explicitOMPLifecycleManager{
		routes: make(map[string]*explicitOMPRouteState),
	}
}

func (m *explicitOMPLifecycleManager) setRoute(requestID string, state *explicitOMPRouteState) {
	if requestID == "" || state == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[requestID] = state
}

func (m *explicitOMPLifecycleManager) getRoute(requestID string) *explicitOMPRouteState {
	if requestID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routes[requestID]
}

func (m *explicitOMPLifecycleManager) deleteRoute(requestID string) {
	if requestID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.routes, requestID)
}
