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
   This runs FIRST in all three handlers; if it returns false the handler
   returns an empty (no-op) envelope before any detection or rewrite.
2. Client gate (detectClient). rewriteRequestBody keys purely off request
   content: detectClient(toolNames) matches tool names against known client
   cloak tables (common-word clients like oh_my_pi require distinctive tools or >= 4 matches; distinctive clients require >= 2 matches). Once identified, cloaking runs.
   Brand replace on `system` runs unconditionally once the model gate passes
   (independent of client detect).

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
- `oh_my_pi` (lowercase: standard tools `read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`, `ask`, `todo`, `hub`, `web_search`, `eval`, plus Vibe Mode `vibe_*` and Autoresearch Mode `*_experiment`, `update_notes`)

> Full detailed mapping tables and domain definitions are documented in **[CONTEXT.md](CONTEXT.md)**.
> Past debugging notes, root causes, and verification steps are recorded in **[NOTE-DEBUGS.md](NOTE-DEBUGS.md)**.

### Two casing rules that bite
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
  the host cannot delete it): docker exec cli-proxy-api-origin sh -c ': >
  logs/cpa-filter-debug.log'. O_APPEND means the next write restarts at offset 0
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

## Deploy to the running container

Container cli-proxy-api-origin (compose project at F:\cliproxy,
docker-compose.yml). Relevant bind mounts:
- F:\cliproxy\plugins -> /CLIProxyAPI/plugins
- F:\cliproxy\logs -> /CLIProxyAPI/logs
- F:\cliproxy\config.yaml -> /CLIProxyAPI/config.yaml

The deployed artifact is the VERSIONED filename
F:\cliproxy\plugins\linux\amd64\antigravity-cloak-v0.1.1.so. The plugin .so is
mmap'd by the process and a Go plugin can never be unloaded, so STOP the
container before overwriting the file:

```powershell
cd F:\cliproxy
docker compose stop
Copy-Item F:\CodeBase\antigravity-cloak\dist\antigravity-cloak.so `
  F:\cliproxy\plugins\linux\amd64\antigravity-cloak-v0.1.1.so -Force
# optional: truncate debug log while stopped
Set-Content -LiteralPath F:\cliproxy\logs\cpa-filter-debug.log -Value $null
docker compose start
```

Verify load (these lines go to docker stdout at startup, NOT into main.log):

```powershell
docker logs cli-proxy-api-origin 2>&1 | Select-String 'antigravity-cloak' | Select-Object -Last 4
```

Expect pluginhost: plugin loaded + plugin registered ... antigravity-cloak
version=0.1.1.

### Enabling / disabling debug needs a recreate (not just start)

CPA_FILTER_DEBUG lives in docker-compose.yml under the service environment:.
Editing it requires docker compose up -d (recreate) to take effect - a plain
start keeps the old env. The plugin is enabled in config.yaml under
plugins.configs.antigravity-cloak.enabled: true; the store source is
plugins.store-sources pointing at monet88/antigravity-cloak/registry.json.

## Verifying cloak from logs (round-trip must be closed)

With debug on, send ONE request, then check:
- Request side: tool name fields contain only cloaked names (run_command etc.),
  zero leftover PascalCase (Bash/Edit/...).
- Response/stream side: every tool_use the model returns under a cloaked name is
  restored to the client's real name (run_command -> Bash), and NO cloaked name
  is delivered to the client (zero leak).

Because stream chunk bodies are multi-line SSE, parse by [DEBUG] record, not by
line, when separating input bodies from changed=true output bodies.

## Installing a custom (non-official) plugin onto a remote VPS

The step-by-step procedure for deploying custom plugin binaries to remote VPS instances via the CLIProxyAPI management API (`/v0/management`) is documented in **[docs/deployment/vps.md](docs/deployment/vps.md)**.

## Gotchas recap

- Build, deploy, and file ops are one-shell-each on Windows PowerShell; avoid
  piping host paths into docker exec for file deletion. Truncate inside the
  container or while it is stopped.

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **antigravity-cloak** (497 symbols, 1417 relationships, 43 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> Index stale? Run `node .gitnexus/run.cjs analyze` from the project root — it auto-selects an available runner. No `.gitnexus/run.cjs` yet? `npx gitnexus analyze` (npm 11 crash → `npm i -g gitnexus`; #1939).

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows. For regression review, compare against the default branch: `detect_changes({scope: "compare", base_ref: "main"})`.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `query({search_query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `context({name: "symbolName"})`.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source→sink flows; needs `analyze --pdg`).

## Never Do

- NEVER edit a function, class, or method without first running `impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `rename` which understands the call graph.
- NEVER commit changes without running `detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/antigravity-cloak/context` | Codebase overview, check index freshness |
| `gitnexus://repo/antigravity-cloak/clusters` | All functional areas |
| `gitnexus://repo/antigravity-cloak/processes` | All execution flows |
| `gitnexus://repo/antigravity-cloak/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->
