# Spec: Test-Audit Surface Pruning & Dead Seam Elimination

## Problem Statement

Coding CLI agents (Claude Code, OpenAI Codex, Oh My Pi) depend on `antigravity-cloak` for seamless tool cloaking, brand rewriting, and response restoration. Over multiple development cycles (adding request-scoped alias plans, streaming carry buffers, and protected Oh My Pi routes), obsolete helper wrappers, uncalled functions, and test-only inspection utilities accumulated directly inside the core production module. Concurrently, several unit tests directly couple to these private helper functions or replicate scenarios already proven at the black-box plugin ABI boundary. This adds maintenance friction, pollutes the production binary with non-production code, and risks obscuring regressions behind redundant assertions.

## Solution

Perform a disciplined test-pruning and seam-elimination refactor adhering to the repository's test value bar:
1. Purge all confirmed dead production functions and unused lifecycle reset methods from the core plugin module.
2. Eliminate obsolete wrapper functions in the core module whose underlying optimized equivalents are already called directly by the production pipeline.
3. Remove uncalled target corroboration helpers whose exact logic is already embedded directly in the signal-aware client detection engine.
4. Relocate recursive JSON tree exploration and text extraction utilities from the core production module into test-scoped helper utilities.
5. Consolidate redundant unit test cases and eliminate single-test standalone test files into canonical owner test suites at the plugin ABI boundary.
6. Preserve 100% of external behavioral contracts, ABI compatibility, and client compatibility across all supported coding tools.

## User Stories

1. As an operator running CLIProxyAPI with antigravity-cloak, I want the core production plugin to be free of dead code and test-only helper utilities, so that the plugin runtime is lean, auditable, and performant.
2. As a plugin maintainer, I want client resolution logic to flow exclusively through the unified explicit client marker parser, so that obsolete resolver wrappers cannot introduce divergent parsing behavior.
3. As a plugin maintainer, I want route lifecycle states to be cleaned up exclusively through explicit route deletion and request completion events, so that dead reset routines do not create redundant lifecycle paths.
4. As a plugin maintainer, I want streaming SSE boundary splitting to use the single optimized incremental scanner, so that legacy non-incremental wrapper functions do not need to be maintained or independently verified.
5. As a plugin maintainer, I want Oh My Pi cloaked target corroboration to reside entirely within the unified client detection routine, so that duplicate uncalled predicates in the production core do not rot out of sync.
6. As a test author, I want JSON structure exploration helpers used only for prompt inspection to live within the test harness, so that production source code remains purely focused on live proxy traffic handling.
7. As a test author, I want test suites to interact with the plugin through the official ABI entrypoint, so that tests exercise real host-plugin message dispatch rather than internal package functions.
8. As a developer, I want Oh My Pi round-trip streaming tests to reside in the canonical integration suite, so that developers do not maintain duplicate ad-hoc round-trip tests in unit test files.
9. As a developer, I want Codex cloak target uniqueness to be asserted holistically across all supported clients in the table initialization suite, so that redundant client-specific uniqueness checks are eliminated.
10. As a developer, I want stream session stale cleanup behavior for protected routes to be validated within the existing stream session manager test suite, so that single-test standalone test files are eliminated.
11. As an operator, I want all existing brand rewriting, tool cloaking, and streaming restoration behaviors to remain 100% byte-for-byte backward compatible, so that no active client workflow experiences regression.
12. As an operator, I want the test harness execution time to remain well under one second, so that local development and CI validation remain fast and responsive.

## Implementation Decisions

- **Core Production Dead Code Removal**:
  - Remove dead uncalled resolver function `resolveExplicitClient` and unused lifecycle manager method `(*explicitOMPLifecycleManager).reset` from `main.go`.
  - Remove legacy wrapper `splitSSEEvents` in favor of direct usage of `splitSSEEventsWithNewBytes`.
  - Remove uncalled target corroboration helper `corroborateCloakedTargetOMP`, whose logic is already embedded in `detectCloakedClientWithSignal`.
  - Keep `extractToolNames` in production source as it has active production callers.
- **Relocation of Non-Production Utilities**:
  - Move recursive JSON tree inspection and text collection utilities (`walkJSON`, `appendPath`, `collectText`) out of `main.go` into test-scoped helper file `json_test_helpers_test.go`.
- **Test Consolidation & De-duplication**:
  - Retire duplicate round-trip streaming test `TestHandleRequestAndStreamUncloakRoundTripOhMyPi` (covered by `TestIntegration_OhMyPi_DefaultTools_SSEStreamLifecycle` and `TestIntegration_OfflineMockServer_CloakToStreamRoundtrip`).
  - Prune client-specific `TestCodexTableKeepsTargetsUnique` (holistic uniqueness invariant covered by `TestUncloakTablesInitialization`).
  - Merge standalone stale session preservation test for protected routes from `session_cleanup_test.go` into `TestStreamSessionManagerCleanupStalePreservesProtectedActiveSession` in `filter_test.go`, removing the empty standalone file.
  - Retain focused direct unit tests for `splitSSEEventsWithNewBytes` covering LF, CRLF, split boundaries, and trailing fragments.
- **Architecture Invariants**:
  - Maintain the single-seam architecture: all plugin capabilities and lifecycle hooks remain accessible via the binary ABI plugin call entrypoint.
  - Ensure zero behavioral mutation on request cloaking, response uncloaking, and streaming SSE restoration.

## Testing Decisions

- **Description of What Makes a Good Test**:
  - A good test exercises observable behavior and invariants at the highest possible architectural seam (the plugin ABI entrypoint), treating internal state machines as implementation details.
  - Tests verify real client payloads (Claude Code, OpenAI Codex, Oh My Pi) across valid, invalid, fragmented, and concurrent scenarios without requiring synthetic production hooks.
- **Modules Tested**:
  - The dynamic plugin core dispatch module (`handlePluginCall`), exercising request interception, response interception, streaming SSE chunk interception, lifecycle registration/reconfiguration, and request completion.
  - The streaming session manager and request alias plan correlation machinery.
- **Prior Art**:
  - Integration lifecycle tests (`TestIntegration_OhMyPi_DefaultTools_SSEStreamLifecycle` and `TestIntegration_OfflineMockServer_CloakToStreamRoundtrip`).
  - Precedence and explicit client marker suites (`TestIntegration_Precedence_InvalidExplicitSuppressesUA_Lifecycle`).
  - Alias plan deterministic pairing and fail-closed tests (`TestRequestAliasPlanDeterministicFallbackAndPairs`).

## Out of Scope

- Altering the 9-tool Safe Mapping Set for Oh My Pi or alias mappings for Claude Code and Codex.
- Modifying prompt sanitization rules (such as `<system-conventions>`).
- Redesigning the CLIProxyAPI ABI v1 plugin interface or streaming session storage model.
- Adding new client identities or tool mapping tables.

## Further Notes

- This spec directly implements the findings from the read-only `/test-audit` review, following the strict value bar and junk pattern criteria.
- Expected outcome: net reduction of approximately 88 production LOC and 150 test LOC, zero regression in coverage or functionality, and elimination of 1 obsolete test file.
