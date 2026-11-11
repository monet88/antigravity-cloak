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
| `main.go` | Plugin core: lifecycle, interceptors, brand rewriter, stream manager, configuration. ~7.5 k lines. |
| `issue55_agent_tier.go` | Issue #55 Claude Code fingerprint machinery (news-URL sanitation, reversible Agent model tiers). |
| `omp_prompt_sanitization.go` | Oh My Pi `agy/*` prompt sanitization (system-conventions tags, RFC 2119 presets, live config). |
| `*_test.go` (root) | Unit/lifecycle tests. `package main` — full internal access. |
| `tests/` | Integration/e2e scripts (`.py`, `.sh`). Never invoked by `go test`. |
| `.github/scripts/` | CI helper Go scripts with their own tests (`package-release_test.go`). |
| `docs/` | Specs, ADRs, checklists, research notes, deployment guides. |
| `.ref/` | Gitignored upstream reference clones. Not part of the module. |

### When to add a new `.go` file

A new source file at root is justified when the code has a distinct domain (like
`issue55_agent_tier.go` or `omp_prompt_sanitization.go`) and would otherwise
bloat `main.go` with a logically separable subsystem. Routine helpers, mapping
tables, and small features belong in `main.go`.

### Test file naming

- **Issue-scoped tests**: `issue<N>_<slug>_test.go` (e.g.
  `issue36_cc_test.go`, `issue49_context_inverse_test.go`). Group related
  regressions under the issue that introduced or fixed them.
- **Domain-scoped tests**: `<domain>_test.go` (e.g. `reverse_brand_test.go`,
  `plugin_integration_test.go`). For cross-cutting concerns not tied to a
  single issue.
- **Filter/plugin lifecycle**: `filter_test.go`, `plugin_test.go`.
- **Shared test helpers**: `json_test_helpers_test.go`. Test-only utilities
  live in `_test.go` files, never in production source.

## Naming Conventions

- **Exported symbols**: Only those required by the C ABI exports
  (`ClipxPluginInit`, `cliproxy_plugin_call`, etc.). Everything else is
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

- ~388 test functions across 32 test files, with ~78 subtests.
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
- **Run before commit**:
  ```powershell
  go test ./...
  go test ./.github/scripts
  go vet ./...
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
- **NOTE-DEBUGS.md**: Diagnosed, fixed, and verified bugs. Record root
  cause, not symptoms.

## Commit Messages

```
<type>: <summary> (#<issue>)
```

Types: `feat`, `fix`, `refactor`, `docs`, `chore`, `ci`, `test`.
Reference the issue number. Keep the summary line under 72 characters.
Body explains *why*, not *what* (the diff shows *what*).
