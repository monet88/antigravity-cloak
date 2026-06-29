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
- Go: 1.26.0. Depends on github.com/router-for-me/CLIProxyAPI/v7 v7.2.42
  (SDK sdk/pluginapi, sdk/pluginabi). Plugin ABI version is 1.
- Source layout: everything lives in main.go (plus filter_test.go,
  plugin_test.go). Upstream references are cloned under .ref/ (gitignored
  workspace), not part of the module.

## Activation model (important)

Two gates decide whether cloaking runs, checked in this order in every handler:

1. Model gate (modelAllowsCloak, config field `model_prefixes`). See below.
   This runs FIRST in all three handlers; if it returns false the handler
   returns an empty (no-op) envelope before any detection or rewrite.
2. Client gate (detectClient). rewriteRequestBody keys purely off request
   content: detectClient(toolNames) counts how many tool names match a client's
   cloak table; >= 2 matches => that client is detected and cloaking runs.
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

claude_code (Claude Code sends PascalCase tool names):

```
Bash            -> run_command
Edit            -> replace_file_content
Read            -> view_file
Write           -> write_to_file
Grep            -> grep_search
Glob            -> list_dir
Agent           -> invoke_subagent
AskUserQuestion -> ask_question
ToolSearch      -> search_web
Skill           -> call_mcp_tool
Workflow        -> schedule
```

codex (Codex sends snake_case tool names):

```
shell_command               -> run_command
apply_patch                 -> multi_replace_file_content
request_user_input          -> ask_question
view_image                  -> generate_image
update_plan                 -> manage_task
tool_search                 -> search_web
get_goal                    -> schedule
create_goal                 -> send_message
update_goal                 -> define_subagent
list_mcp_resources          -> list_resources
list_mcp_resource_templates -> list_permissions
read_mcp_resource           -> read_resource
```

The right-hand side are real Antigravity native tool names. MCP tools (mcp__*)
are NOT in any table, so they pass through untouched both directions - intended.

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

This is for the production CLIProxyAPI on the GCP VM (compose dir ~/cliproxy on
chang-gateway-vm), reached only through the reverse-proxied management API at
https://vps.monet.uno/api-cli. Do everything through the management API - no
SSH, no editing files on the box directly. Management key goes in the
Authorization: Bearer <key> header; the panel/API lives under /v0/management.

Key facts learned the hard way:
- The official store registry is always loaded. Your own plugin is only
  visible after its registry is added as an extra store-source. If you skip
  this, install returns 404 (host does not know the plugin id).
- The VPS config is NOT guaranteed to match the local config.yaml. The running
  VPS box was missing both the custom store-source and the antigravity-cloak
  block even though local had them. Always read the live VPS config first.
- There is NO narrow endpoint for store-sources (tried
  /v0/management/plugin-store/sources, /plugin-store-sources, /store-sources -
  all 404). The only way to add a store-source is to edit the full config via
  GET/PUT /v0/management/config.yaml.
- config.yaml GET returns raw YAML bytes; in PowerShell read with
  Invoke-WebRequest and decode .Content as UTF-8 (it comes back as a byte[],
  not a string - .Substring fails on it).

Procedure (PowerShell, $base/$key set to the VPS API + management key):
1. Back up the live config to a local file FIRST:
   GET /v0/management/config.yaml -> save bytes verbatim (LF, no CRLF).
2. Build the new config by inserting ONLY the store-sources lines under
   `plugins:` (right after `enabled: true`, before `configs:`). Diff against the
   backup and confirm the ONLY additions are those 2 lines. Do not touch
   anything else.
   ```yaml
   plugins:
     enabled: true
     store-sources:
       - https://raw.githubusercontent.com/monet88/antigravity-cloak/main/registry.json
     configs:
       ...
   ```
3. PUT /v0/management/config.yaml with the new YAML. Expect
   {"ok":true,"changed":["config"]}. Config reloads live, no restart.
4. GET /v0/management/plugin-store and confirm the new source + the
   antigravity-cloak entry appear (installed:false).
5. POST /v0/management/plugin-store/antigravity-cloak/install
   (body {"version":"0.2.0"} or omit for latest). Host downloads the matching
   GOOS/GOARCH .so from the GitHub release into
   plugins/linux/amd64/antigravity-cloak-v<ver>.so. Expect
   restart_required:false.
6. PATCH /v0/management/plugins/antigravity-cloak/enabled body {"enabled":true}.
7. PATCH /v0/management/plugins/antigravity-cloak/config to set fields, e.g.
   {"model_prefixes":["agy"]}. (GET .../config to read first; PUT replaces,
   PATCH merges.)
8. Verify: GET /v0/management/plugins, find antigravity-cloak with
   registered:true, effective_enabled:true, and path pointing at the v<ver> .so.

The install/enable/patch calls persist their own plugins.configs.antigravity-cloak
block (enabled, model_prefixes, store metadata) back into the VPS config - you
only had to hand-add the store-source. Keep the local backup for rollback.

## Gotchas recap

- Dependency is pinned to CLIProxyAPI v7.2.42 while the container ran v7.2.43.
  ABI is 1 so load/register works, but watch this if behavior looks off.
- Build, deploy, and file ops are one-shell-each on Windows PowerShell; avoid
  piping host paths into docker exec for file deletion. Truncate inside the
  container or while it is stopped.
