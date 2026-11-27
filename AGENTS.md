# AGENTS.md - antigravity-cloak

CLIProxyAPI v7 dynamic Go C-shared plugin (`buildmode=c-shared`, Go 1.26.0, ABI v1, `github.com/monet88/antigravity-cloak`) that disguises coding-CLI traffic as Antigravity via reversible brand rewriting and tool-name cloaking.

- **Primary active focus:** **Oh My Pi (`oh_my_pi` / `omp`)** over **`openai-completions`** (OpenAI Chat Completions).
- **Maintenance-only / non-expanding:** `claude_code`, `codex`, Anthropic Messages, and deferred OpenAI Responses stay documented to match existing code, but should not be expanded unless requested.
- **Source layout:**
  - Root (`package main`): `main.go` (thin CGO/C-ABI exports, `handlePluginCall`, registration/config metadata, `init()` calling `cloak.Initialize(...)`) and `plugin_test.go` (dispatcher/metadata/`registry.json` tests).
  - `internal/spec/`: pure declarative client spec types, `PathRules`, and `AntigravityIdentity` (no internal imports).
  - `internal/clients/{omp,claudecode,codex}/`: data-only client definitions and presets (zero runtime hooks).
  - `internal/cloak/`: shared cloaking engine split by domain (`registry.go`, `config.go`, `detect.go`, `handlers.go`, `brand.go`, `tool_cloak.go`, `alias_plan.go`, `stream.go`, `envelope.go`, `omp_prompt_sanitization.go`, `issue55_agent_tier.go`) plus unit/integration tests (`*_test.go`).
  - `tests/`: live e2e `.py` scripts (never run by `go test`); `.ref/`: gitignored upstream clones.

## Navigation pointers

- **Brand tables, tool mappings, activation gates, OMP ProtectedAGY/bypass lifecycle, alias plans, or Issue #55:** read **[CONTEXT.md](CONTEXT.md)** (canonical contract; `internal/clients/*` specs are source of truth for behaviour).
- **Code style, new `.go` / `*_test.go` placement, concurrency/error rules, commit format:** read **[docs/guides/CODING_STANDARDS.md](docs/guides/CODING_STANDARDS.md)**.
- **Clean Linux/amd64 `.so` build, local Docker Compose deploy, controlled debug, or OMP live acceptance:** read **[docs/verification/verification-checklist.md](docs/verification/verification-checklist.md)** (and **[docs/verification/verification-checklist-omp.md](docs/verification/verification-checklist-omp.md)** for the 9-tool OMP matrix).
- **Remote VPS deploy or in-place `.so` upgrade:** read **[docs/deployment/vps.md](docs/deployment/vps.md)**.
- **Extending OMP Google Cloud Code 429 prompt sanitization:** read **[the sanitization spec](docs/specs/system-conventions-sanitization.md)**.
- **Historical root causes & client surface audits:** **[docs/debugging/NOTE-DEBUGS.md](docs/debugging/NOTE-DEBUGS.md)**, **[Codex surface reference](docs/research/codex-tool-surface-2026-09-12.md)**, **[Claude Code gap analysis](docs/research/claude-code-cloak-gap-analysis-2026-09-23.md)**.

## Operational invariants & traps

### Activation, format & tool-cloaking gotchas
- **Header strip before gate:** `handleRequestInterceptBefore` consumes/clears `X-Cloak-Client` before `modelAllowsCloak`, so a gate-skipped request still strips the plugin-owned control header.
- **Gate keys on `Model`/`RequestedModel`:** `ToFormat` is empty at `request.intercept_before` and absent on response/stream structs. `model_prefixes` (empty = match all) reloads live via `reconfigure` without container restart.
- **`normalizeSourceFormat`:** All three handlers normalize `claude`/`antigravity` -> `anthropic` and `codex`/`openai-response` -> `openai` before walking bodies; missing normalization yields empty `extractToolNames`.
- **`isUnambiguousToolName`:** Single-word PascalCase names (`Bash`, `Read`, `Edit`) stay ambiguous (replaced only in tool-reference context), never bare in prose.
- **OMP virtual-device MCP (`xd://mcp__*`):** Invoked via `read`/`write` (`read_file`/`write_file`). Never convert `xd://` calls into `call_mcp_tool`.
- **Google Cloud Code 429 (`omp_prompt_sanitization.go`):** On `ProtectedAGY` and `User-Agent: omp/...` `agy/*` routes, `sanitizeOMPPrompt` runs after brand rewrite and before prose alias cloaking, rewriting `<system-conventions>`, both RFC 2119 headers, `omp/18.20.0+`'s injected Claude Code OAuth block (to `antigravityIdentity`, never `""`, preserving `cache_control`), and live `omp_prompt_sanitizations`.

### Debug logging (`CPA_FILTER_DEBUG`)
- Decided once via `sync.Once` on first `debugLog` call; changing it requires a container recreate. Keep OFF in production (dumping large Claude Code bodies to Windows bind-mounted `logs/` wedged WSL2/Docker).
- Clear inside the running container: `docker exec $Container sh -c ': > /CLIProxyAPI/logs/cpa-filter-debug.log'`.
- `handleResponseIntercept: changed=%t` logs only on non-stream responses; verify streamed restores from `handleStreamChunkIntercept` chunk bodies.

### Build & deploy silent traps
1. **Container `git clone /src /build` drops dirty host edits:** To build uncommitted changes inside the clean Linux/amd64 Docker build, remove tracked host deletions (`git -C /src diff --name-only --diff-filter=D "$CLOAK_BUILD_COMMIT" | while read -r f; do git -C /build rm -q -- "$f"; done`) and overlay the working tree (`tar -C /src --exclude=.git --exclude=dist -cf - . | tar -C /build -xf -`) inside `$BuildCommand` before `go build`.
2. **Discover Compose project, container & mounts (`docker ps` / `docker inspect`):** Never hardcode `F:\cliproxy` or guess `--project-name`; treat any `Conflict` on `up -d --force-recreate` as recreate failed. Export `CPA_FILTER_DEBUG` in the invoking shell (`env_file` is commented out).
3. **Never hot-swap a loaded Go `.so`:** Stop CLIProxyAPI, replace/move the `.so` while stopped, start/recreate, and verify inside the running container (`env | grep CPA_FILTER_DEBUG`, artifact `sha256sum`, expected string in `.so`).
4. **OMP profiles:** Default `C:\Users\monet\.omp\agent` is the primary production target; `cloak-live` is isolated local acceptance only.

## Local checks & post-merge docs

- Validation: `go test -race ./...`, `go test ./.github/scripts`, `go vet ./...`.
- After merging client, mapping, activation, or deploy changes, update `CONTEXT.md`, `docs/guides/CODING_STANDARDS.md`, and `CONTRIBUTING.md` as needed.

<!-- gitnexus:start -->
## GitNexus — Code Intelligence

This project is indexed by GitNexus as **antigravity-cloak** (1962 symbols, 7290 relationships, 172 execution flows).

> Index stale? Run `node D:/GitNexus/antigravity-cloak-ebc9d52a879d/run.cjs analyze --index-only`. No `D:/GitNexus/antigravity-cloak-ebc9d52a879d/run.cjs`? Bootstrap with `npx`, `bunx`, or `pnpm dlx`; e.g. `bunx gitnexus@latest analyze` (#1939).
> On query/context/impact/cypher object results, read staleness.status and branch/lastCommit. Re-analyze only for behind or diverged — current is clone HEAD, not main.

### Always Do

- **MUST run impact before editing.** Use `impact({target: "symbolName", direction: "upstream"})` or `node D:/GitNexus/antigravity-cloak-ebc9d52a879d/run.cjs impact "symbolName" --direction upstream --repo .`; report callers/processes/risk. Never substitute grep for graph analysis.
- **MUST analyze changes before commit.** Run `detect_changes({scope: "all"})` or `node D:/GitNexus/antigravity-cloak-ebc9d52a879d/run.cjs detect-changes --scope all --repo .`. `partial: true` or `truncated: true` is not a clean check — a zero means unseen, not unaffected; re-run it. For regression review, fetch target branch "main" from its remote; compute its merge base with the reviewed HEAD and pass that SHA to `detect_changes({scope: "compare", base_ref: "<merge-base SHA>"})` or `node D:/GitNexus/antigravity-cloak-ebc9d52a879d/run.cjs detect-changes --scope compare --base-ref "<merge-base SHA>" --repo .`.
- MUST warn on HIGH/CRITICAL `risk` pre-edit; never use `riskSharedAxes` to waive a HIGH/CRITICAL `risk` warning. Compare File/symbol: MCP File omits axes; Graph-RAG expands File.
- **MUST treat `risk: UNKNOWN` as unresolved, not as low.** An empty caller set is not evidence the symbol is unused — it can also mean the callers are not resolvable by the index (plain-object property access, dynamic dispatch, cross-language calls). `impact` pairs `UNKNOWN` with a `riskNote` saying so. Confirm with a text search before treating the symbol as safe to change or delete; do not proceed on the strength of a zero.
- **MUST use `query({search_query: "concept"})` for concepts/flows, `context({name: "symbolName"})` for a named symbol, or `impact` for blast radius, on read-only callers, dependencies, imports, or execution flow.** Graph first; text search only for empty/`UNKNOWN`/literals.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source→sink flows; needs `analyze --pdg`).

### Never Do

- NEVER edit a function, class, or method before MCP/CLI impact analysis.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis, and never read `UNKNOWN` as an all-clear — it means the walk could not answer, which is the one verdict that requires confirming by other means.
- NEVER rename symbols with find-and-replace — use `rename` with `dry_run: true`, review semantic spans and coverage, then apply with `dry_run: false` and its `plan_token`.
- NEVER commit before MCP/CLI graph change analysis.

### Resources

| Resource | Use for |
| --- | --- |
| `gitnexus://repo/antigravity-cloak/context` | Codebase overview, check index freshness |
| `gitnexus://repo/antigravity-cloak/clusters` | All functional areas |
| `gitnexus://repo/antigravity-cloak/processes` | All execution flows |
| `gitnexus://repo/antigravity-cloak/process/{name}` | Step-by-step execution trace |

<!-- gitnexus:end -->
