# Coding Standards

Standards for `antigravity-cloak`. The codebase is a single Go C-shared plugin
(`buildmode=c-shared`, `package main`). These standards derive from observed
patterns in the existing code, not from aspirational rules.

## Language & Toolchain

- Go 1.26.0, `CGO_ENABLED=1`.
- Dependencies: `CLIProxyAPI/v7` SDK (`sdk/pluginapi`, `sdk/pluginabi`) and
  `gopkg.in/yaml.v3`. No other external deps — keep the surface minimal.
- Format: `gofmt` (the Go default). No custom linter config.
- Vet: `go vet ./...` must pass.

## Source Layout

| Path | Purpose |
| :--- | :--- |
| `main.go` | Thin root entrypoint (`package main`): C-ABI exports, method dispatcher, registration metadata, config fields, and `init()` wiring `cloak.Initialize(...)`. |
| `plugin_test.go`, `issue<N>_*_test.go`, `abi_test_helpers_test.go` | Root `package main` black-box ABI-seam suites, shared ABI test helpers, dispatcher/metadata checks, and `registry.json` version-parity tests. |
| `internal/spec/` | Pure declarative client data types, `PathRules`, and shared constants (no internal repo imports). |
| `internal/clients/{omp,claudecode,codex}/` | Data-only client specs, brand/tool/alias tables, and static presets (zero runtime hooks). |
| `internal/cloak/` | Shared cloaking engine split by domain (`registry.go`, `config.go`, `detect.go`, `handlers.go`, `brand.go`, `tool_cloak.go`, `alias_plan.go`, `stream.go`, `envelope.go`, `omp_prompt_sanitization.go`, `issue55_agent_tier.go`) + owning `*_test.go` suites. |
| `tests/` | Integration/e2e scripts (`.py`, `.sh`). Never invoked by `go test`. |
| `.github/scripts/` | CI helper Go scripts with their own tests (`package-release_test.go`). |
| `docs/` | Categorized documentation (`adr/`, `agents/`, `benchmarks/`, `debugging/`, `deployment/`, `guides/`, `research/`, `specs/`, `verification/`, `wiki/`). |
| `.ref/` | Gitignored upstream reference clones. Not part of the module. |

### When to add a new `.go` file

Add or extend a domain file inside `internal/cloak/` when a subsystem has a cohesive domain responsibility (like `omp_prompt_sanitization.go` or `issue55_agent_tier.go`). Keep client tables and presets declarative inside `internal/clients/<client>/`, and keep root `main.go` strictly limited to C-ABI exports, dispatch, and registration metadata.

### Test file naming

- **Issue-scoped tests**: `issue<N>_<slug>_test.go` (e.g.
  `issue36_cc_test.go`, `issue49_context_inverse_test.go`). Group related
  regressions under the issue that introduced or fixed them.
- **Domain-scoped tests**: `<domain>_test.go` (e.g. `reverse_brand_test.go`,
  `plugin_integration_test.go`). For cross-cutting concerns not tied to a
  single issue.
- **Root black-box ABI-seam suites**: `plugin_test.go` plus root `issue<N>_*_test.go` (e.g. `issue69_protected_agy_brand_parity_test.go`, `issue70_inbound_sanitization_surfaces_test.go`, `issue71_omp_v19_presets_test.go`) driving `handlePluginCall` end to end through the C-ABI seam.
- **Filter/plugin lifecycle**: `filter_test.go`, `plugin_test.go`.
- **Shared test helpers**: root `abi_test_helpers_test.go` for black-box ABI suites and `internal/cloak/json_test_helpers_test.go` for package unit suites. Test-only utilities live in `_test.go` files, never in production source.

## Naming Conventions

- **Exported symbols**: Root `package main` exports only the C ABI entrypoints
  (`cliproxy_plugin_init`, `cliproxy_plugin_call`, `cliproxy_plugin_free`,
  `cliproxy_plugin_shutdown`). Internal packages (`internal/spec`,
  `internal/clients/*`, and `internal/cloak`'s 8 production functions) export
  only symbols consumed by real production callers; everything else stays
  unexported.
- **Client identifiers**: Snake-case string constants matching the wire
  protocol (`claude_code`, `codex`, `oh_my_pi`).
- **Tool names**: Case-sensitive, matching client wire format exactly
  (PascalCase for Claude Code, snake_case for Codex/OMP).
- **Config field names**: Snake-case (`model_prefixes`, `custom_mappings`,
  `tool_mappings`, `omp_prompt_sanitizations`).

## Concurrency & State

- Configuration: `atomic.Pointer[filterConfig]` for lock-free hot-path reads.
  Writes go through `applyFilterConfig`.
- Debug logging: `sync.Once` + `sync.Mutex`. Fixed for plugin lifetime.
- Stream sessions: `StreamSessionManager` with internal locking. Sessions are
  keyed by `RequestID` or body hash.
- Route state: `explicitOMPLifecycleManager` and alias plan managers, each with
  their own sync. Cleaned on `request.complete`.
- **Never hold a lock across a host call or I/O.** The plugin runs inside the
  gateway's goroutine pool.

## Error Handling

- **Fail-closed admission**: Missing `RequestID`, duplicate `RequestID`,
  declaration collision, or OMP admission failure → HTTP 503 JSON response
  (`tool_cloak_required` or `omp_cloak_required`) before upstream dispatch.
  Never silently pass through a request that cannot be safely cloaked.
- **Gate ordering**: Model gate first, then client resolution, then
  client-specific processing. If any gate rejects, the request body is
  preserved unmutated (except control-header consumption).
- **No panics in production paths.** A plugin panic inside the gateway crashes
  the gateway process. Defensive nil checks and early returns.

## Testing

- **Offline harness**: `httptest`-backed round-trip driving rewritten requests
  into a local mock upstream and back through the stream interceptor. No
  external network.
- **Table-driven tests**: Preferred for mapping coverage, collision detection,
  and protocol variants.
- **Invariant tests**: `TestContextGroupsAreExactInverses`,
  `TestBuiltInBrandTablesCoverEachClientsOwnIdentity`,
  `TestHandlePluginCallRegisterVersionMatchesRegistry` — these guard
  structural contracts and block regressions.
- **Round-trip tests**: Every cloak mapping asserts request→upstream→response
  round trip, not just one direction.
- **Mechanical architecture & doc checks**: `architecture_test.go` enforces package import boundaries, `internal/cloak`'s 8-symbol exported surface, zero unused unexported production declarations, and valid internal Markdown links/anchors.
- **Run before commit** (enforced by CI and tracked `.githooks/pre-commit`; enable with `git config core.hooksPath .githooks`):
  ```powershell
  ./scripts/check.ps1
  ```

## Brand Rewriting Rules

When adding or modifying a brand mapping:

1. **One table per client.** The forward table lives in
   `brandMappingsByClient`; the reverse in `reverseBrandMappingsByClient` (or
   the protected reverse table for OMP). A mapping is owned by exactly one
   client.
2. **Every forward rule must be reversible** — unless it is an approved
   one-way convention (the bare `CLAUDE.md` → `AGENTS.md` filename rule, the
   Issue #55 news-URL rule) or an approved non-bijective pair (SDK,
   Workflow). No new one-way rules without explicit approval.
3. **No competitor names in tables.** A product name that belongs to no
   supported client has no table entry and is left verbatim.
4. **Identity lines are terminal.** They are replaced whole and never reversed
   at sentence level.
5. **Context paths are whole-segment.** Match `.claude/`, `.codex/`, `.omp/`
   as whole elements, never as suffixes of larger path components.
6. **Tool arguments get only operational rules.** Prose rules (identity,
   vendor word, bare brand, capability names) do not run over tool-call
   argument carriers. Only paths, domains, SDK/skill names are
   `ArgumentSafe`.

## Tool Cloaking Rules

When adding or modifying a tool mapping:

1. **Canonical mappings are immutable at config time.** The OMP Safe Mapping
   Set (9 canonical pairs) cannot be overridden by `tool_mappings`.
2. **Alias plan clients** (`claude_code`, `codex`) route through
   request-scoped alias plans. Core tools → AGY role targets, secondary →
   `wp_*`, unknown → `wp_ext_<hash>`.
3. **Casing must match the client wire format.** The uncloak key is the exact
   string the client expects back.
4. **Request-scoped reversal.** Only tools actually declared and transformed
   in a request are active in the reverse map. A natively declared AGY target
   is never reverse-cloaked.

## Inbound Sanitization Rules

When adding or modifying one-way OMP inbound sanitization logic:

1. **Keep sanitization strictly one-way and separate from Brand Rewriting.**
   Sanitization runs request-only on eligible OMP routes and is never reversed
   on responses or streams; third-party and harness-signature rules belong here
   rather than in reversible client brand tables.
2. **Reuse one atomic rule snapshot per request.** Capture
   `activeFilterConfig().ompSanitizationRules` once per request and pass that
   same slice into every sanitization pass (`sanitizeOMPPrompt` and
   `rewriteOMPConversationProse`) so concurrent `plugin.reconfigure` cannot split
   rule views mid-request (ADR 0001).
3. **Fuse multi-pass walkers over delimited spans.** When a delimited carrier
   (such as `<system-reminder>` spans inside `user` turns) runs both
   sanitization and tool-alias cloaking, execute both in one span pass so
   arbitrary rules that rewrite or strip delimiter tags cannot hide the span
   from downstream cloaking or bleed into surrounding **User-Typed Turns**.

## Documentation

- **CONTEXT.md**: Canonical domain reference. Update when adding clients,
  mappings, activation rules, or architectural changes.
- **AGENTS.md**: Operational guide for agents. Update when build, deploy,
  debug, or verification procedures change.
- **CHANGELOG.md**: Keep a Changelog format. Every user-visible change gets
  an entry under `[Unreleased]`.
- **docs/adr/**: Architecture Decision Records for non-trivial design
  choices. Status: `proposed` → `accepted`.
- **docs/specs/**: Technical specifications for complex subsystems.
- **docs/wiki/**: Dated historical engineering memory (decisions, subsystem
  invariants, environment guardrails).
- **docs/debugging/NOTE-DEBUGS.md**: Diagnosed, fixed, and verified bugs. Record root
  cause, not symptoms.

## Commit Messages

```
<type>: <summary> (#<issue>)
```

Types: `feat`, `fix`, `refactor`, `docs`, `chore`, `ci`, `test`.
Reference the issue number. Keep the summary line under 72 characters.
Body explains *why*, not *what* (the diff shows *what*).
