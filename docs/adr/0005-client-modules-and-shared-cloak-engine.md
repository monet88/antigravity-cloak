---
status: accepted
---

# Client modules with shared cloaking mechanics

The refactor will localize each coding client's identity detection, brand/path mappings, tool mappings, and client-specific policy in a client module, with shared protocol processing, streaming, and request lifecycle mechanics. Adding a client that uses supported protocols and lifecycle behavior should require its module, explicit registration, and tests; new protocol or lifecycle behavior can require changes to shared code. This trades complete client self-containment for a single implementation of the mechanics that must remain consistent across clients.

The refactor preserves observable request, response, stream, configuration, and plugin ABI behavior. Discovered bugs are recorded separately and behavior changes require a separate decision; reorganizing code must not silently change cloak, bypass, admission, or error policy.

Unit tests move with their production packages and retain appropriate package-local access. Integration/e2e tests remain under `tests/`; the repository root retains entrypoint code and directly related tests. A separate directory for every test would force unnecessary exposure of package internals or a redesign of existing verification.

## Package ownership

The first refactor keeps the C exports, plugin method dispatcher, registration response, and config field metadata at the root. `internal/spec` defines client data types without importing other repository packages. `internal/clients/claudecode`, `internal/clients/codex`, and `internal/clients/omp` provide client definitions and depend only on `spec`. Root code registers these definitions with `internal/cloak`.

`internal/cloak` owns the request/response handlers, detection, configuration, rewriting, alias plans, streaming, and lifecycle mechanics. Split these responsibilities into named files within that package. Streaming currently reads route/alias state while admission and response handling access stream sessions, so a separate stream package would require additional coordination interfaces. Defer that split until there is an independently useful interface.

Keep the global configuration and the three distinct managers private, preserving their existing semantics and synchronization. Moving them does not establish behavior equivalence by itself. Manager consolidation and engine-instance injection are separate follow-ups. Keep exported operations limited to actual production callers; eight symbols is a design target, not a reason to combine unrelated operations or add test-only exports.

## Client policy and initialization

Start with data-only client definitions and zero hooks. A future hook needs a current client requirement that declarative data cannot express, with its rationale recorded here.

Execution mode is selected per request. Engine ordering remains: conflicting explicit marker containing OMP, explicit OMP, model gate, identity resolution, then alias-plan or legacy processing. Policy data must preserve the distinction between explicit markers, conflicting markers, UA evidence, and body evidence. In particular, OMP's protected and durable-bypass routes precede the model gate; UA/body attribution alone does not activate them. Existing conflict handling is not generalized to other clients. Claude Code/Codex alias-plan behavior applies after successful resolution and the applicable gates, not to every raw identity signal.

Preserve validation at its existing call sites and times. The current `init()` builds inverse maps and publishes default configuration; it does not explicitly validate injectivity or panic on collisions. New startup validation is a separate follow-up. An independent invariant test can be added only where existing coverage does not already protect it.

Use one startup operation, `cloak.Initialize(specs...)`: replace the complete registry, derive the built-in mapping tables and inverse tool tables, then publish configuration through `applyFilterConfig(defaultFilterConfig())`. Populate all client-derived inventories, brand/path rules, aliases, and policies before building the configuration and regex caches. Preserve existing internal lookup call sites where possible. This reuses the configuration publication path; observable equivalence to the old initialization remains a verification requirement, not an assumption about identical internal work.

Root `init()` calls Initialize with the three built-in clients. Package `cloak` must not publish or cache an incomplete client configuration in its own initialization. Its `TestMain` uses the same Initialize operation; importing client definitions creates no cycle because those packages depend only on `spec`. Root tests use root initialization. Existing package-local config reset helpers remain usable.

Repeated initialization with identical definitions replaces rather than accumulates registry/configuration data. This is a startup contract, not runtime registration, a concurrent reconfiguration mechanism, or a reset of live request state. Preserve the three managers and their cleanup semantics; initialization does not promise to clear active sessions. No new duplicate-key or injectivity validation is introduced.

## Test ownership

Keep the real method dispatcher at root despite the migration cost. Tests asserting method routing, unknown methods, plugin lifecycle dispatch, registration responses, and configuration metadata stay with that entrypoint. Engine tests that previously used `handlePluginCall` only as transport to a handler move to `cloak` and call the corresponding handler, retaining input and meaningful assertions. Assign envelope assertions to the operation that actually produces the envelope; a JSON envelope alone does not make a test a dispatcher test. Do not copy the dispatcher into test helpers or export private stores for tests.

## Migration and verification

1. Refresh GitNexus with `node .gitnexus/run.cjs analyze --index-only` and verify index freshness before recording the baseline source revision and workspace state. Pin that revision for the later comparison; run `go test ./...`, `go test ./.github/scripts`, and `go vet ./...`. Build the Linux shared library through the local runbook and record provenance and checksum.
2. Extract `spec` and the three client definitions while mechanics remain at root. Keep existing test assertions and behavior unchanged.
3. Move mechanics and their owning tests together into `internal/cloak`, preserving logic. Review moved code separately from necessary visibility/import/test-entry adaptations. Keep genuine dispatcher tests at root; never duplicate the dispatcher in test helpers to make a moved test compile.
4. Finish the thin root entrypoint and run the full checks, shared-library build, and GitNexus change analysis against a pinned baseline. Verify log prefixes and exact 503 responses as observable contracts; binary string inspection is supplementary evidence only.
5. Perform live acceptance separately under the existing runbook before deployment readiness is claimed.

Each implementation checkpoint must be independently verifiable. Characterization tests address uncovered observable contracts and credible regressions, including cross-handler state and ordering; zero coverage alone does not justify a test. Preserve meaningful assertions when moving tests and document necessary harness changes rather than claiming the test source can remain entirely unchanged.

The consolidated design was confirmed on 2026-09-30, concluding the design interview. Package ownership, client policy, initialization, test ownership, and migration direction are accepted. No production implementation, commit, push, PR creation, or live binary replacement is authorized by this decision record alone.

## Addendum (2026-11-27, Issue #67)

Confirmed during the repository reorganization interview (Issue #67, baseline `5df8966`) to cover post-2026-09-30 subsystems and documentation layout:

- **Client-specific runtime machinery stays in `internal/cloak`, presets/static data move to `internal/clients/*`**: `omp_prompt_sanitization.go` and `issue55_agent_tier.go` interact directly with private engine state (`filterConfig`, `streamSession`, `requestAliasPlan`, SSE framing). To preserve the zero-hook, data-only client module invariant and clean DAG (`main -> clients + cloak`, `clients -> spec`, `cloak -> spec`), built-in Oh My Pi prompt sanitization presets and Claude Code Issue #55 static data live declaratively in `internal/clients/omp` and `internal/clients/claudecode` (typed in `internal/spec`), while their execution walkers/scanners remain in `internal/cloak`.
- **Domain file split & vocabulary in `internal/cloak`**: Split the monolithic engine into cohesive domain files (`registry.go`, `config.go`, `detect.go`, `handlers.go`, `brand.go`, `tool_cloak.go`, `alias_plan.go`, `stream.go`, `envelope.go`, `omp_prompt_sanitization.go`, `issue55_agent_tier.go`). Use `cloak` as canonical terminology for new modules; retain legacy `filter` naming only on external operator contracts (`CPA_FILTER_DEBUG`, `logs/cpa-filter-debug.log`) and preserved legacy test filenames (`filter_test.go`).
- **Test filename preservation**: Existing Go test files move to their owning packages via `git mv` with filenames preserved (no issue-file consolidation mixed into the layout refactor).
- **Root & documentation taxonomy**: Root retains `go.mod`, `go.sum`, `registry.json` (external plugin-store raw URL contract), `main.go`, `plugin_test.go`, `README.md`, `LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, `AGENTS.md`, `CONTEXT.md` (sole canonical domain glossary), and dotfiles. Operational standards, guides, verification checklists, and debugging notes live under `docs/guides/`, `docs/verification/`, and `docs/debugging/` alongside existing `docs/` subdirectories.
- **Phased delivery**: Implemented in three independently verified checkpoints on a dedicated refactor branch: (1) documentation layout, link updates, local leftover cleanup, and this addendum; (2) `internal/spec` and `internal/clients/{omp,claudecode,codex}` extraction; (3) `internal/cloak` extraction, thin root completion, full checks, and Linux/amd64 shared-library Docker build verification.
