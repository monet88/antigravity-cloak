# AGENTS.md - antigravity-cloak operations guide

Operational knowledge for working on this plugin. Read this before editing,
building, or debugging. Written for a future agent session.

## What this plugin is

A CLIProxyAPI v7 dynamic plugin (buildmode=c-shared .so) that disguises
coding-CLI traffic as Antigravity. Two jobs:

1. Brand rewrite: replace OpenCode / Codex / Claude Code with Antigravity
   in the request `system` field and `system`-role messages.
2. Tool-name cloaking: rename a client's native tool names to Antigravity tool
   names on the way up (request), then restore them on the way back (response +
   stream), so the client still sees its own tool names.

- Module: github.com/monet88/antigravity-cloak
- Go: 1.26.0. Depends on github.com/router-for-me/CLIProxyAPI/v7 v7.2.143
  (SDK sdk/pluginapi, sdk/pluginabi). Plugin ABI version is 1.
- Source layout: `main.go` (plugin core). Go unit tests (`*_test.go`) reside at
  the repository root alongside `main.go` for `package main`, while `tests/` is
  dedicated to integration/e2e test scripts (`.py`, `.sh`). Upstream references
  are cloned under `.ref/` (gitignored workspace), not part of the module.
- The root Go suite is the **offline lifecycle / unit harness**: an httptest-backed
  round-trip that drives a rewritten request into a local mock upstream and back
  through the stream interceptor. It uses no external network and proves the
  cloak-to-stream round trip within the standard CI test job. The external
  integration/e2e scripts in `tests/` (`.py`, `.sh`) are a separate, runnable
  layer against a live CLIProxyAPI instance and are never invoked by `go test`.

## Activation model (important)

Two gates decide whether cloaking runs, checked in this order in every handler:

1. Model gate (modelAllowsCloak, config field `model_prefixes`). See below.
   This is the FIRST gate for cloak/body mutation in all three handlers; if it
   returns false no detection or rewrite runs. The one exception is transport
   sanitation (Spec #15): in handleRequestInterceptBefore the plugin-owned
   X-Cloak-Client control header is consumed/cleared BEFORE the gate, so a
   gate-skipped request still returns an envelope that strips the header -
   never a fully empty envelope when the header was present.
2. Client gate. After the model gate, client identity must be resolved first
   following strict precedence: valid explicit X-Cloak-Client > verified positive
   User-Agent evidence (`omp/...`) > body detection (detectClient on tool names;
   common-word clients like oh_my_pi require distinctive tools or >= 4 matches,
   distinctive clients require >= 2 matches). Only a resolved supported client
   may trigger brand rewriting and tool cloaking. If no supported client is
   resolved (no client => zero request-body mutation), the request body is
   preserved completely unmutated.

The model gate is the provider gate. It is OFF by default: with `model_prefixes`
empty, modelAllowsCloak returns true for every model, so cloaking runs for ALL
providers (original behavior - point Claude Code at grok and it still cloaks,
harmlessly, because the round-trip is symmetric). When `model_prefixes` is set
(e.g. `agy/`), cloaking only runs when req.Model OR req.RequestedModel starts
with one of the prefixes; everything else is skipped (logged as "model gate
skip").

Why Model and not ToFormat: verified from live logs that ToFormat is EMPTY at
request.intercept_before (it is only filled after credential selection), and the
response/stream structs do not carry ToFormat at all. Model / RequestedModel are
present on all three request types, so the gate keys on those.

The upstream sibling plugin antigravity-coding-filter (see
.ref/cpa-plugin-antigravity-coding-filter, v0.0.3) has NO gate of any kind - it
only does brand rewrite, no tool cloaking. The model gate here is a local
addition, not upstream behavior.

### model_prefixes config field

- Config field name `model_prefixes`, declared in configFields() and parsed by
  parseModelPrefixes. Accepts a YAML array of strings, or a single string with
  comma/newline-separated values. Stored as filterConfig.ModelPrefixes.
- Set it from the management UI (Configure antigravity-cloak panel) or directly
  in config.yaml under plugins.configs.antigravity-cloak. Saving is a
  `reconfigure` call - the plugin reloads config live, NO container restart.
- Empty => match all models (default, backward compatible). Match is
  strings.HasPrefix against trimmed Model and RequestedModel.
- modelAllowsCloak reads activeFilterConfig().ModelPrefixes and is called at the
  top of all three handlers, so the request/response/stream sides stay
  consistent (a request that was not cloaked never gets uncloaked on the way
  back, and vice versa).

## Tool-name maps (defaultCloakTables in main.go)

Cloak direction is clientToolName -> antigravityToolName. Uncloak is the exact
inverse (defaultUncloakTables is built by inverting the cloak table in init()).
Casing MUST match what the client actually sends, because uncloak restores the
exact key string back to the client and tool names are case-sensitive.

Supported clients:
- `claude_code` (PascalCase: `Bash`, `Edit`, `Read`, `Write`, `Grep`, `Glob`, `Agent`, `AskUserQuestion`, `ToolSearch`, `Skill`, `Workflow`)
- `codex` (snake_case: `shell_command`, `apply_patch`, `request_user_input`, `view_image`, `update_plan`, `tool_search`, `get_goal`, `create_goal`, `update_goal`, `list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource`)
- `oh_my_pi` (9-tool Safe Mapping Set: `read -> view_file`, `write -> write_to_file`, `edit -> replace_file_content`, `bash -> run_command`, `grep -> grep_search`, `glob -> find_by_name`, `task -> invoke_subagent`, `ask -> ask_question`, `web_search -> search_web`. Intentional pass-through: `todo`, `hub`, `eval`, `vibe_*`, and Autoresearch tools.)
> Full detailed mapping tables and domain definitions are documented in **[CONTEXT.md](CONTEXT.md)**.
> Past debugging notes, root causes, and verification steps are recorded in **[NOTE-DEBUGS.md](NOTE-DEBUGS.md)**.

### MCP tools behavior
- **Oh My Pi (`oh_my_pi`)** mounts MCP servers under the virtual-device protocol (`xd://mcp__<server>_<tool>`) and invokes them through its standard `read`/`write` tools. Those core tools are already cloaked to `view_file`/`write_to_file`, so OMP MCP traffic is protected without a separate top-level mapping.
- **Do not convert OMP virtual-device MCP calls into `call_mcp_tool`.** That would require additional payload/schema transformation and risks breaking streaming semantics.
- **Top-level `mcp__*` tools** from clients that expose them directly remain pass-through traffic.
### Oh My Pi Routing & Lifecycle (Issue #25, #26, #27, #28)
1. **ProtectedAGY Precedence**: Explicit OMP marker (`X-Cloak-Client: oh_my_pi` / `omp` / `oh-my-pi`) on `agy/*` routes bypasses generic `model_prefixes` and enforces fail-closed protection. The request must pass strict single-document JSON admission, declaration collision validation (comparing final base identities), and canonical validation. Any admission failure terminates with an exact HTTP 503 JSON error (`omp_cloak_required`) before upstream execution.
2. **Request-Scoped Active Reverse**: Only canonical pairs whose source tool was actually declared and transformed in that request become active in the reverse map. Inactive canonical targets and native AGY target-only traffic are never reverse-cloaked.
3. **ExplicitOMPNonAGYBypass**: Explicit OMP marker on non-`agy/` routes consumes the marker, pins a durable bypass state keyed by host `RequestID`, and performs zero tool or brand mutation across request, response, and stream.
4. **Lifecycle Ownership**: Route state (`ProtectedAGY` / `ExplicitOMPNonAGYBypass`) is managed by `explicitOMPLifecycleManager` and cleaned only on `request.complete` (`MethodRequestComplete`). Disposable stream sessions are cleaned on `[DONE]`, but pre-payload disposable state can be rehydrated deterministically solely from pinned route state.
5. **Protected Brand Policy**: `Oh My Pi`, `oh-my-pi`, and `omp` are masked to `Antigravity` as terminal outputs (cannot be overridden or reprocessed by operator custom mappings). Literal `.omp` path segments (`.omp/foo`, `C:\Users\...\.omp\agent`) are strictly preserved. Correlated assistant text restores `Antigravity -> omp` using pinned route authority.
1. sourceFormat normalization. The proxy sends SourceFormat="claude" for
   Claude Code, but the body-walking branches only understand "anthropic" /
   "openai". normalizeSourceFormat maps claude/antigravity -> anthropic and
   codex/openai-response -> openai. Without this, extractToolNames returns empty
   and NOTHING cloaks. All three handlers normalize before use.
2. isUnambiguousToolName. A single-word PascalCase name (Bash, Read, Edit...)
   must stay "ambiguous" so it is only replaced in tool-reference context
   (use Bash, the Edit tool), never bare in prose. Otherwise common English
   words get shredded inside the huge Claude Code system prompt. Only underscore
   names or multi-word camelCase (AskUserQuestion, ToolSearch) are
   "unambiguous" (replace everywhere).

## Debug logging

- Enabled only when env CPA_FILTER_DEBUG is non-empty. Decided once via
  sync.Once on first debugLog call after load - so it is fixed for the life of
  the loaded plugin; changing the env needs a container recreate.
- Writes full request/response/chunk BODIES to logs/cpa-filter-debug.log
  (opened O_CREATE|O_WRONLY|O_APPEND), under a mutex, on the hot streaming path.
- WARNING: with debug ON, dumping the (very large) Claude Code bodies to a
  Windows bind-mounted logs/ dir saturated WSL2 I/O and wedged the Docker engine
  (distro stopped, all docker calls hung). Recovery was wsl --shutdown + full
  quit/relaunch of Docker Desktop. KEEP DEBUG OFF for production; only enable it
  for a few single requests at a time.
- To clear the log while the container runs (the plugin holds the file handle so
  the host cannot delete it), use the `$Container` resolved by the local runbook:
  `docker exec $Container sh -c ': > /CLIProxyAPI/logs/cpa-filter-debug.log'`. `O_APPEND` means the next write restarts at offset 0
  - no sparse file.

Key debug lines to grep:
- handleRequestInterceptBefore: ... rewritten=%t - request-side cloak applied.
- buildUncloakTable: toolNames=%v client=%s and cloakedClient=%s - detection.
  client= / cloakedClient=claude_code means detection worked; empty means it did
  not (e.g. sourceFormat or casing bug).
- handleStreamChunkIntercept: changed=%t - uncloak applied to a stream chunk.

## Build (.so for the running container = linux/amd64)

CGO is required (buildmode=c-shared). Cross-compiling cgo from Windows to linux
has no toolchain, so build inside a golang container:

```powershell
docker run --rm -v F:\CodeBase\antigravity-cloak:/src -w /src golang:1.26 sh -c "mkdir -p dist && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -ldflags '-s -w' -o dist/antigravity-cloak.so . && rm -f dist/antigravity-cloak.h"
```

Local validation on Windows (gcc/mingw present, CGO works):

```powershell
go test ./...
go test ./.github/scripts
go vet ./...
```

CI (.github/workflows/build.yml) builds the full OS/arch matrix and packages
release zips + checksums.txt; version comes from the v* git tag.

## Local Docker deploy and OMP live acceptance

Use **[docs/verification-checklist.md](docs/verification-checklist.md)** as the authoritative local deploy and real acceptance runbook.

Important invariants:
- The **default OMP profile** (`C:\Users\monet\.omp\agent`) is the primary real-use path and must remain the first-class compatibility target. Its `cpa` provider carries `X-Cloak-Client: oh_my_pi`, which is the deterministic OMP identity signal. The current plugin is not yet fail-closed for every marked request failure mode; do not document or assume that guarantee until the corresponding implementation and live rejection tests ship.
- The `cloak-live` OMP profile exists only for isolated local acceptance against the pre-provisioned `cli-proxy-api` Docker container and local gateway. Do not treat `cloak-live` as the production/default OMP configuration.
- Discover the active Compose project, container, config mount, plugin mount, and log mount with `docker inspect`; never rely on an old hardcoded `F:\cliproxy` path or container name.
- The Docker plugin artifact is Linux/amd64 `-buildmode=c-shared`; when testing a published release, use the release asset rather than silently rebuilding different source.
- A loaded Go `.so` cannot be hot-swapped safely. Stop/recreate CLIProxyAPI before replacing an already-loaded binary.
- `CPA_FILTER_DEBUG` is process environment state cached on first debug use; enabling or disabling it requires a container recreate.
- Debug writes full request/response/stream bodies. Enable it only for a controlled acceptance run, then disable it and truncate the log.
- The primary live acceptance gate is Oh My Pi (`omp`) against local CLIProxyAPI. Prove request cloak and streamed response uncloak at the OMP boundary; source/tests remain the oracle for protocol edge cases not exercised by that run.
- Do not add new secrets, management credentials, auth files, OMP profile credentials, or full debug-body logs to the repository. The explicitly documented local-only CLIProxyAPI acceptance key in `docs/verification-checklist.md` is the intentional exception for this workstation.

## Installing a custom (non-official) plugin onto a remote VPS

The step-by-step procedure for deploying custom plugin binaries to remote VPS instances via the CLIProxyAPI management API (`/v0/management`) is documented in **[docs/deployment/vps.md](docs/deployment/vps.md)**.

## Gotchas recap

- Build, deploy, and file ops are one-shell-each on Windows PowerShell; avoid
  piping host paths into docker exec for file deletion. Truncate inside the
  container or while it is stopped.
- **Google Cloud Code Prompt-Injection 429 (`<system-conventions>`)**:
  - Symptom: Requests from Oh My Pi fail upstream with `429 RESOURCE_EXHAUSTED` on `daily-cloudcode-pa.googleapis.com` even with healthy quotas and valid tokens.
  - Confirmed trigger: Oh My Pi's default system prompt wraps its RFC 2119 block in `<system-conventions>`; local payload bisection isolated that tag pair. The upstream classifier's internal implementation is not established by this observation.
  - Rule: On ProtectedAGY requests, sanitize the exact opening/closing tags to `<conventions>` in top-level `system` and system/developer message text. Preserve other roles, tool payloads, metadata, and bypass routes. See [the sanitization spec](docs/specs/system-conventions-sanitization.md) before extending this scope.

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **antigravity-cloak** (671 symbols, 2002 relationships, 55 execution flows).

> Index stale? Run `node .gitnexus/run.cjs analyze --index-only` from the project root — it auto-selects an available runner. No `.gitnexus/run.cjs` yet? Bootstrap with `npx`, `bunx`, or `pnpm dlx` — e.g. `bunx gitnexus@latest analyze` (npm 11 npx crash; #1939).

## Always Do

- **MUST run impact before editing.** Use `impact({target: "symbolName", direction: "upstream"})` or `node .gitnexus/run.cjs impact "symbolName" --direction upstream --repo .`; report callers, processes, and risk. Never substitute grep for graph analysis.
- **MUST analyze graph changes before committing.** Use `detect_changes({scope: "all"})` (MCP) or `node .gitnexus/run.cjs detect-changes --scope all --repo .` (CLI fallback). `partial: true` or `truncated: true` is not a clean check — a zero means unseen, not unaffected; re-run it. For regression review: `detect_changes({scope: "compare", base_ref: "main"})` or `node .gitnexus/run.cjs detect-changes --scope compare --base-ref "main" --repo .`.
- MUST warn on HIGH/CRITICAL `risk` pre-edit; never use `riskSharedAxes` to waive a HIGH/CRITICAL `risk` warning. Compare File/symbol: MCP File omits axes; Graph-RAG expands File.
- **MUST treat `risk: UNKNOWN` as unresolved, not as low.** An empty caller set is not evidence the symbol is unused — it can also mean the callers are not resolvable by the index (plain-object property access, dynamic dispatch, cross-language calls). `impact` pairs `UNKNOWN` with a `riskNote` saying so. Confirm with a text search before treating the symbol as safe to change or delete; do not proceed on the strength of a zero.
- **MUST use `query({search_query: "concept"})` for concepts/flows, `context({name: "symbolName"})` for a named symbol, or `impact` for blast radius, on read-only callers, dependencies, imports, or execution flow.** Graph first; text search only for empty/`UNKNOWN`/literals.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source→sink flows; needs `analyze --pdg`).

## Never Do

- NEVER edit a function, class, or method before MCP/CLI impact analysis.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis, and never read `UNKNOWN` as an all-clear — it means the walk could not answer, which is the one verdict that requires confirming by other means.
- NEVER rename symbols with find-and-replace — use `rename` which understands the call graph.
- NEVER commit before MCP/CLI graph change analysis.

## Resources

| Resource | Use for |
| --- | --- |
| `gitnexus://repo/antigravity-cloak/context` | Codebase overview, check index freshness |
| `gitnexus://repo/antigravity-cloak/clusters` | All functional areas |
| `gitnexus://repo/antigravity-cloak/processes` | All execution flows |
| `gitnexus://repo/antigravity-cloak/process/{name}` | Step-by-step execution trace |

<!-- gitnexus:end -->
