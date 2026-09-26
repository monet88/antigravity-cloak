# Changelog

All notable changes to `antigravity-cloak` are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Current releases use semantic version tags; the one-off `v.26.09.06` CalVer release is retained below as historical release metadata.

## [Unreleased]

### Changed
- Pruned obsolete internal seams and test-only utilities from core production source: removed `resolveExplicitClient`, `(*explicitOMPLifecycleManager).reset`, legacy `splitSSEEvents` wrapper, and `corroborateCloakedTargetOMP` (Issue #42).
- Relocated test-only JSON exploration utilities (`walkJSON`, `appendPath`, `collectText`) to `json_test_helpers_test.go` (Issue #42).
- Consolidated duplicate round-trip streaming tests and client-specific uniqueness tests into canonical integration and table initialization suites, merging `session_cleanup_test.go` into `filter_test.go` (Issue #42).

## [0.6.0] - 2026-09-25

### Added
- Generalized request-scoped alias plan architecture for dynamic-surface clients (Claude Code, OpenAI Codex) alongside extended alias handling for Oh My Pi, with fail-closed correlation (`tool_cloak_required` for alias-plan clients, `omp_cloak_required` for Protected OMP), collision detection, and deterministic reversible fallback aliases (`wp_ext_<hash>`) for dynamic/MCP tool declarations (Issue #32).
- Full declaration-level cloaking for Claude Code (`claude_code`): core tools map to AGY role names (`Glob -> find_by_name`, `WebFetch -> read_url_content`), Tier-2 tools map to stable shared aliases (`wp_*`), and deferred tools/placeholders are handled across normal protocol identity positions (`tools[]`, `tool_calls`/`tool_use`, `tool_choice`) (Issue #36, Issue #32).
- Extended OMP tool coverage beyond the canonical nine on Protected routes via shared aliases (`wp_*`) and deterministic fallback aliases (`wp_ext_<hash>`), while preserving `xd://` virtual device transport and protocol integrity (implementation complete, live default-profile acceptance pending) (Issue #37, Issue #32).
- Protected OMP config-time validation rejecting mutable canonical nine, non-injective targets, and invalid target naming in `tool_mappings` during initialization and reconfigure (Issue #37, Issue #32).
- Full declaration-level cloaking for OpenAI Codex (`codex`) across code mode and shell mode (`exec_command -> run_command`, `apply_patch -> wp_apply_patch`, `write_stdin -> wp_write_stdin`, `view_image -> wp_view_image`) with request-scoped reversal preventing cross-mode collision (Issue #38, Issue #32).
- Namespace collision gate failing closed when distinct source identities or namespace variants resolve to the same final target or base identity (Issue #32).
- Missing RequestID or duplicate RequestID rejection with HTTP 503 `tool_cloak_required` for alias-plan clients (Issue #32).

### Changed
- Aligned Codex collaboration tools (`collaboration__send_message -> wp_send_message`, `collaboration__list_agents -> wp_list_workers`, `collaboration__followup_task -> wp_collaboration_followup_task`) to cross-client shared vocabulary to avoid premature speculative AGY mapping (Issue #38, Issue #32).
- Added full-coverage tie-breaker in `detectCloakedClientWithSignal` so clients with 100% table match are not misattributed to partial superset tables under identical ratio and hit counts (Issue #36).

## [0.5.2] - 2026-09-12

### Added
- Cloaked Codex traffic on the code-mode wire surface: `exec -> run_command`, `web_search -> search_web`, `request_user_input -> ask_question`, and the flattened `collaboration__spawn_agent` / `collaboration__followup_task` / `collaboration__list_agents` children to `invoke_subagent` / `manage_task` / `manage_subagents`. Only names that occupy a tool-name position are listed; helpers that exist solely as prose inside the `exec` description stay pass-through (PR #31).
- Shell-mode declaration inventory (`exec_command`, `write_stdin`, `apply_patch`, `view_image`, ...) retained for detection only, so a shell-mode session still resolves as Codex without the table carrying two sources that want one target (PR #31).
- Codex surface reference and ADR 0004 recording which names the wire carries in each mode, why MCP stays pass-through, and how the reverse table is scoped (PR #31).

### Fixed
- Codex sessions were detected but never cloaked: a request declaring `exec` plus namespaced children scored one hit against a floor of two, because namespaced declarations were not reduced to their base identities and keys configured through `tool_mappings` never counted (PR #31).
- The reverse table was narrowed against the executed body, which carries no source name, so an already-cloaked Codex stream lost its reverse and payload tool names passed through unmutated (PR #31).
- A natively declared AGY target is no longer handed back as a Codex source name the client never declared, including on the correlated response path, which now reuses the scope the request interceptor derived from the raw body (PR #31).

## [0.5.0] - 2026-09-07
### Added
- Adopted the 9-tool AGY CLI-native Safe Mapping Set for Oh My Pi (`read -> view_file`, `write -> write_to_file`, `edit -> replace_file_content`, `bash -> run_command`, `grep -> grep_search`, `glob -> find_by_name`, `task -> invoke_subagent`, `ask -> ask_question`, `web_search -> search_web`) with static source and target identity inventories (Issue #26).
- Fail-closed Protected OMP routing for explicit OMP markers on `agy/*` routes with strict single-document JSON validation, base identity declaration collision rejection, and exact 503 JSON rejection (`omp_cloak_required`) before upstream execution (Issue #27).
- Request-scoped active reverse authority: only canonical pairs whose source declaration was actually transformed become active for reverse uncloaking in correlated responses and streams (Issue #27).
- Durable `ExplicitOMPNonAGYBypass` path for marked OMP traffic on non-`agy/` routes, pinning zero-mutation bypass state by host `RequestID` across request, response, and stream (Issue #27).
- Request lifecycle management via CLIProxyAPI `request_lifecycle_plugin` and `request.complete` callback, keeping route state logically separate from disposable SSE state with pre-payload rehydration support (Issue #27).
- Terminal canonical Protected brand policy: `Oh My Pi`, `oh-my-pi`, and `omp` mask to terminal `Antigravity` without operator override or reprocessing, while preserving literal `.omp` path segments and restoring assistant-visible `Antigravity -> omp` (Issue #27).
- Deterministic release-gate acceptance test suite proving all nine canonical Safe Mapping pairs, transport exceptions, mixed pass-through traffic, RequestID correlation, and lifecycle invariants (Issue #28).
- Configured isolated `cloak-live` profile with explicit `X-Cloak-Client: oh_my_pi` marker (Issue #28).

### Changed
- Converted `todo`, `hub`, `eval`, `vibe_*`, and Autoresearch tools into intentional pass-through tools that remain unmutated (Issue #26).
- Replaced `glob -> list_dir` with `glob -> find_by_name` (Issue #26).
- Clarified that canonical AGY targets alone are corroboration-only and cannot authoritatively attribute no-marker traffic to OMP (Issue #26).
- Updated documentation (`README.md`, `CONTEXT.md`, `AGENTS.md`, `docs/verification-checklist.md`, `docs/specs/oh-my-pi-cloaking-spec.md`) and live test fixtures (`tests/test_omp.py`, `tests/test_client.py`) to the shipped Safe Mapping Set and routing contracts (Issue #28).

## [0.4.4] - 2026-09-06

### Fixed
- Gate request-body brand rewriting and tool cloaking on a resolved supported client identity, preserving ordinary API request bodies when no supported client is identified.
- Preserve client-precedence semantics across explicit `X-Cloak-Client`, verified User-Agent evidence, and body-based classification while keeping recognized Oh My Pi round trips intact.

### Changed
- Returned release numbering to SemVer so CLIProxyAPI Plugin Store discovery and asset matching use the expected `v<version>` tag convention.

## [26.09.06] - 2026-09-06

### Changed
- Switched release numbering from SemVer to date-based CalVer (`YY.MM.DD`), with Git tags using the `v.YY.MM.DD` form.

## [0.4.3] - 2026-09-02

### Fixed
- Preserved only the exact dot-prefixed `.omp` filesystem path segment during forward OMP brand rewriting, while continuing to mask bare `omp`, `/omp/`, `\omp\`, `.omp-backup`, `profile.omp`, and other brand aliases.

### Removed
- Removed obsolete Claude-specific repository guidance after retiring that workflow.

## [0.4.2] - 2026-09-01

### Added
- Added deterministic `X-Cloak-Client` request identity override with control-header consumption so the header never leaks upstream.
- Added conservative positive User-Agent evidence for thin Oh My Pi requests while preserving body-based classification as fallback.
- Added Oh My Pi response identity restoration: assistant-visible standalone `Antigravity` text is rewritten back to `omp` without touching tool arguments or unrelated data.

### Fixed
- Preserved invalid explicit-client precedence across response and stream paths so weaker UA evidence cannot override a bad explicit signal.
- Hardened reverse-brand streaming across OpenAI and Anthropic terminal events, fragmented semantic lanes, singleton content maps, and multi-choice completion.
- Ensured Anthropic standalone terminal flushes keep correct data framing.

## [0.4.1] - 2026-08-29

### Added
- Added ADR 0002 for ratio-ranked client classification and request/session pre-registration.
- Added an offline integration/lifecycle test suite and Python CI validation.

### Changed
- Streamlined repository documentation and synchronized GitNexus guidance/statistics.

## [0.4.0] - 2026-08-27

### Added
- Added Oh My Pi Vibe Mode tool cloaking (`vibe_*`).
- Added Oh My Pi Autoresearch tool cloaking (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`).

## [0.3.1] - 2026-08-27

### Fixed
- Fixed streamed Oh My Pi tool-call uncloaking so Antigravity tool names are restored correctly at the OMP client boundary.

### Added
- Added integration scripts covering Oh My Pi and streaming client behavior.

## [0.3.0] - 2026-08-27

### Added
- Added Oh My Pi client detection and bidirectional tool-name cloaking.
- Added remote VPS custom-plugin installation documentation.

### Changed
- Synchronized upstream coding-agent brand keywords.

## [0.2.0] - 2026-06-29

### Added
- Added `model_prefixes` gating so cloaking can be restricted to selected model/provider prefixes.

### Fixed
- Fixed client detection behavior to make cloak/uncloak classification more reliable.

## [0.1.1] - 2026-06-28

### Fixed
- Renamed the Go module path to the fork repository path so module metadata matches distribution.

## [0.1.0] - 2026-06-28

### Added
- Added structured tool-name cloaking on requests and symmetric uncloaking on responses/streams.
- Added reload hardening for runtime configuration changes.
- Added plugin store registry metadata for distribution.

## [0.0.3] - 2026-06-20

### Added
- Added request-side rewriting of coding-agent identity signals to `Antigravity`.

## [0.0.2] - 2026-06-19

### Added
- Added configurable brand-rewrite keywords.

## [0.0.1] - 2026-06-19

### Added
- Initial plugin implementation.
- Added repository metadata, MIT license, and release build workflow.

[Unreleased]: https://github.com/monet88/antigravity-cloak/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/monet88/antigravity-cloak/compare/v0.5.2...v0.6.0
[0.5.2]: https://github.com/monet88/antigravity-cloak/compare/v0.5.1...v0.5.2
[0.5.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.5.0
[0.4.4]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.4
[26.09.06]: https://github.com/monet88/antigravity-cloak/releases/tag/v.26.09.06
[0.4.3]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.3
[0.4.2]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.2
[0.4.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.1
[0.4.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.0
[0.3.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.3.1
[0.3.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.3.0
[0.2.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.2.0
[0.1.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.1.1
[0.1.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.1.0
[0.0.3]: https://github.com/monet88/antigravity-cloak/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/monet88/antigravity-cloak/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/monet88/antigravity-cloak/tree/v0.0.1
