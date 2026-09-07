# Oh My Pi <-> Antigravity Live Verification Runbook

Use this runbook for real local acceptance of `antigravity-cloak` with Oh My Pi (OMP), CLIProxyAPI, and an Antigravity-backed model. Deterministic Go tests remain the oracle for edge cases; this runbook proves the actual local round trip.

## Ready-to-use local baseline

The default OMP profile is the primary real-use configuration. `cloak-live` is an isolated acceptance profile only.

Primary/default OMP contract:

- Default OMP config root: `C:\Users\monet\.omp\agent`
- Default provider config: `C:\Users\monet\.omp\agent\models.yml`
- Provider: `cpa`
- Required deterministic client marker: `X-Cloak-Client: oh_my_pi`
- The default profile may use a different CLIProxyAPI endpoint from the local acceptance runtime; the marker, not the model or gateway URL, identifies OMP traffic that must cloak.

The workstation is also provisioned for isolated live acceptance. Reuse the existing local runtime first; do not create a new Docker stack, gateway config, or OMP profile unless the verification steps below prove the existing one is missing or broken.

- Docker container: `cli-proxy-api` (`eceasy/cli-proxy-api:latest`)
- Local gateway: `http://127.0.0.1:8317/v1`
- Acceptance-only OMP profile: `cloak-live`
- OMP profile path: `C:\Users\monet\.omp\profiles\cloak-live\agent`
- Local CLIProxyAPI API key / management password: `Tonight123`
- Installed OMP baseline: `omp/18.1.13`
- Installed AGY baseline: `1.1.27`
- Current primary Antigravity route for live cloak testing: `cpa/agy/gemini-3.8-flash`
- Plugin-visible model for that route: `agy/gemini-3.8-flash`
For production/default-profile checks, verify `omp models`. For isolated local acceptance, verify `docker ps` and `omp --profile cloak-live models`. If the acceptance environment is healthy, use it as-is.

## PASS criteria

A live run is `PASS` only when all of the following are true:

1. CLIProxyAPI loads and registers the intended `antigravity-cloak` version.
2. OMP reaches the local CLIProxyAPI instance through an isolated profile.
3. The request is classified as `oh_my_pi` and native OMP tool names are cloaked upstream.
4. A real streamed model tool call is restored to an OMP-native tool name before OMP executes it.
5. No Antigravity-only tool name leaks across the OMP boundary.
6. No unknown-tool, schema, retry-loop, plugin panic, or model-gate error occurs.
7. Controlled debug logging is disabled and truncated again after verification.

## Tool direction (Canonical Safe Mapping Set)

The plugin enforces the 9-tool AGY CLI-native Safe Mapping Set for Oh My Pi (`oh_my_pi` / `omp`):

| OMP (Source) | Antigravity (Target) | Classification |
| :--- | :--- | :--- |
| `read` | `view_file` | Transport exception (files, dirs, URLs, `xd://` devices) |
| `write` | `write_to_file` | Transport exception (files, `xd://` device execution) |
| `edit` | `replace_file_content` | Semantic alias (preserves OMP hashline wire format) |
| `bash` | `run_command` | Direct semantic alias |
| `grep` | `grep_search` | Direct semantic alias |
| `glob` | `find_by_name` | Direct semantic alias (pattern search) |
| `task` | `invoke_subagent` | Direct semantic alias (batch subagent spawning) |
| `ask` | `ask_question` | Direct semantic alias (interactive UI) |
| `web_search` | `search_web` | Direct semantic alias (when tool is exposed) |

Intentional pass-through tools: `todo`, `hub`, `eval`, `vibe_*` (vibe_spawn, vibe_send, vibe_wait, vibe_kill, vibe_list), and Autoresearch tools (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`). These remain completely unmutated.

The full mapping table lives in [CONTEXT.md](../CONTEXT.md).

OMP auxiliary devices and MCP servers are typically mounted under `xd://...` and invoked through `read`/`write`; top-level `mcp__*` tools, when present, remain pass-through traffic. No parameter schema translation or `xd://` parameter rewriting is performed.
## 1. Discover the real Docker runtime

Do not assume a historical container name or `F:\cliproxy` path. Resolve the active Compose project and bind mounts first:

```powershell
$Container = 'cli-proxy-api'
$i = (docker inspect $Container | ConvertFrom-Json)[0]

$ComposeService = $i.Config.Labels.'com.docker.compose.service'
$ComposeDir = $i.Config.Labels.'com.docker.compose.project.working_dir'
$PluginMount = $i.Mounts | Where-Object Destination -eq '/CLIProxyAPI/plugins'
$ConfigMount = $i.Mounts | Where-Object Destination -eq '/CLIProxyAPI/config.yaml'
$LogsMount = $i.Mounts | Where-Object Destination -eq '/CLIProxyAPI/logs'

[pscustomobject]@{
  Container = $Container
  ComposeService = $ComposeService
  ComposeDir = $ComposeDir
  PluginDir = $PluginMount.Source
  ConfigPath = $ConfigMount.Source
  LogsDir = $LogsMount.Source
}
```

All three mounts must resolve before changing or testing the runtime.

## 2. Verify the plugin that is actually loaded

The active CLIProxyAPI config should enable the plugin. A pinned store version is recommended for release acceptance:

```yaml
plugins:
  enabled: true
  configs:
    antigravity-cloak:
      enabled: true
      store:
        version: "<VERSION>"
      model_prefixes:
        - "agy/"
```

Confirm startup evidence from the container:

```powershell
docker logs $Container 2>&1 |
  Select-String 'pluginhost: plugin (loaded|registered).*antigravity-cloak' |
  Select-Object -Last 4
```

When testing a published release, install its published Linux/amd64 asset into the discovered plugin mount rather than rebuilding different source. A loaded Go shared object cannot be safely hot-swapped; recreate the service before replacing an active binary.

## 3. Use the existing `cloak-live` OMP profile

The local acceptance profile already exists. Reuse it; do not create a replacement profile unless this one is missing or broken:

```powershell
$OmpProfile = 'cloak-live'
$OmpRoot = (omp --profile $OmpProfile config path).Trim()
$OmpRoot
```

`$OmpRoot\models.yml` is expected to contain this local-only provider configuration:

```yaml
providers:
  cpa:
    baseUrl: http://127.0.0.1:8317/v1
    apiKey: "Tonight123"
    api: openai-completions
    headers:
      X-Cloak-Client: oh_my_pi
    discovery:
      type: openai-models-list
```

Verify discovery:

```powershell
omp --profile cloak-live models
```

OMP model selectors include the provider namespace. For the validated route, select:

```text
cpa/agy/gemini-3.8-flash
```

The provider prefix `cpa/` is OMP-local routing metadata; the plugin sees the request model as `agy/gemini-3.8-flash`, which is what `model_prefixes: ["agy/"]` matches.

## 4. Interactive TUI validation

Launch OMP in the target repository:

```powershell
omp --profile cloak-live `
  --model cpa/agy/gemini-3.8-flash `
  --cwd F:\CodeBase\antigravity-cloak `
  --auto-approve
```

Give it a task that requires real file reads, edits/writes, and shell commands. The client should execute normal OMP tools; it must never surface `run_command`, `view_file`, `replace_file_content`, or `write_to_file` as unknown client tools.

For a minimal one-shot smoke instead of the TUI:

```powershell
omp --profile cloak-live `
  --model cpa/agy/gemini-3.8-flash `
  --cwd F:\CodeBase\antigravity-cloak `
  --tools bash,read,edit,write,grep,glob `
  --no-session `
  --auto-approve `
  --max-time 2m `
  -p "Use bash exactly once to run: git rev-parse --short HEAD. Report the exact output."
```
## 5. Controlled debug proof

`CPA_FILTER_DEBUG` writes full request/response/stream bodies. Enable it only for a small controlled run and never publish the raw log.

Add `CPA_FILTER_DEBUG: "1"` to the CLIProxyAPI service environment, then recreate the service. Merely restarting an existing container is insufficient because the plugin caches debug enablement for the process lifetime.

```powershell
Push-Location $ComposeDir
docker compose up -d --force-recreate --pull never $ComposeService
Pop-Location

docker exec $Container sh -lc ': > /CLIProxyAPI/logs/cpa-filter-debug.log'
```

For the request under test, prove these transitions from debug records:

```text
OMP request:          bash / read / edit / write
rewritten upstream:  run_command / view_file / replace_file_content / write_to_file
model stream:        Antigravity-native tool name
next OMP history:    native OMP tool name again
```

Useful markers include:

```text
handleRequestInterceptBefore: ... Model="agy/..." RequestedModel="agy/..."
handleRequestInterceptBefore: rewritten=true client=oh_my_pi
StreamSessionManager: header-init using pre-registered session ... client=oh_my_pi
handleStreamChunkIntercept: ...
```

Do not prove uncloaking by searching the whole log for both names. Compare the model stream event with the following OMP request/history: the native name must reappear there and the cloaked name must not remain at that client boundary.

## 6. Reverse-brand check

For resolved `oh_my_pi` traffic, assistant-visible standalone `Antigravity` is restored to `omp`. Tool arguments, metadata, reasoning/control lanes, IDs, and non-assistant data keep literal `Antigravity`.

When this code changes, add one focused live response containing standalone `Antigravity` and confirm the TUI displays `omp`. The deterministic `reverse_brand_test.go` suite remains the primary edge-case oracle for fragmentation, per-lane buffering, boundaries, and excluded fields.

## 7. Cleanup after every debug run

Remove `CPA_FILTER_DEBUG` from the Compose environment and recreate the service:

```powershell
Push-Location $ComposeDir
docker compose up -d --force-recreate --pull never $ComposeService
Pop-Location

docker exec $Container sh -lc ': > /CLIProxyAPI/logs/cpa-filter-debug.log'
```

Verify the final state:

```powershell
docker inspect $Container --format '{{range .Config.Env}}{{println .}}{{end}}' |
  Select-String '^CPA_FILTER_DEBUG='
```

Expected: no `CPA_FILTER_DEBUG=1` entry, and the debug log is empty.

## Validated baseline - 2026-09-01

The local release acceptance used:

- CLIProxyAPI `v7.2.146`
- `antigravity-cloak v0.4.2`
- OMP `18.0.11`
- OMP route `cpa/agy/gemini-3.7-flash-high`
- plugin-visible model `agy/gemini-3.7-flash-high`

A real interactive OMP task exercised `bash`, `read`, `edit`, and `write`. Correlation across model stream events and following OMP requests found 58 streamed tool-call events with 0 boundary failures. No plugin panic, unknown-tool, or schema failure was observed. Debug was disabled and the log truncated afterward.

## Validated Issue #28 cloak-live baseline - 2026-09-07

The Issue #28 live acceptance gate was verified against the repaired local Docker runtime:

- Acceptance timestamp: `2026-09-07T10:45:00+08:00`
- CLIProxyAPI `v7.2.146` (Commit `d31b159`)
- `antigravity-cloak v0.4.4` (`plugins/linux/amd64/antigravity-cloak-v0.4.4.so`)
- OMP `18.1.13`
- AGY CLI `1.1.27`
- Local gateway: `http://127.0.0.1:8317/v1`
- Protected model route: `agy/gemini-3.8-flash` (`cpa/agy/gemini-3.8-flash`)
- Bypass model route: `gemini-3.8-flash` (`cpa/gemini-3.8-flash`)
- Deterministic client marker: `X-Cloak-Client: oh_my_pi`
- Provenance inventories:
  - OMP callable source inventory (9 canonical): `read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`, `ask`, `web_search`
  - Native AGY CLI target inventory (9 canonical): `view_file`, `write_to_file`, `replace_file_content`, `run_command`, `grep_search`, `find_by_name`, `invoke_subagent`, `ask_question`, `search_web`

### 1. Real OMP End-to-End Execution Evidence (100% Native Client Execution)
All nine canonical mappings were verified directly through native OMP CLI and TUI execution (OMP top-level declaration -> gateway cloak -> streamed AGY tool call -> stream reverse uncloak -> OMP native execution & continuation):
1. `bash -> run_command -> bash`: Real OMP CLI (`omp -p`) ran `git rev-parse --short HEAD`; model streamed `run_command`, reversed to `bash`, executed natively in OMP, and continuation returned commit hash.
2. `read -> view_file -> read`: Real OMP CLI (`omp -p`) ran file read on `README.md`; model streamed `view_file`, reversed to `read`, executed natively in OMP, and continuation reported `# omp Cloak`.
3. `write -> write_to_file -> write`: Real OMP CLI (`omp -p`) wrote `.live_smoke_tmp.txt`; model streamed `write_to_file`, reversed to `write`, executed natively in OMP, followed by verification.
4. `edit -> replace_file_content -> edit`: Real OMP CLI (`omp -p`) edited `.live_smoke_tmp.txt` (`alpha` -> `gamma`); model streamed `replace_file_content`, reversed to `edit`, executed natively in OMP, and continuation verified `gamma beta`.
5. `grep -> grep_search -> grep`: Real OMP CLI (`omp -p`) searched for `ProtectedAGY` in `issue28_live_gate_test.go`; model streamed `grep_search`, reversed to `grep`, executed natively in OMP, and continuation returned line match.
6. `glob -> find_by_name -> glob`: Real OMP CLI (`omp -p`) scanned `*.go` at root; model streamed `find_by_name`, reversed to `glob`, executed natively in OMP, and continuation returned the matching Go files.
7. `task -> invoke_subagent -> task`: Real OMP CLI (`omp -p`) spawned subagent `GetGitRev` to run `git rev-parse --short HEAD`; model streamed `invoke_subagent`, reversed to `task`, OMP executed the subagent task, and continuation reported commit.
8. `ask -> ask_question -> ask`: Real interactive OMP TUI session (`omp` in interactive PTY mode); model streamed `ask_question`, reversed to `ask`, OMP rendered the interactive Ask TUI selection dialog ("Do you want apples or oranges?"), user input `ENTER` selected "Apples", OMP executed tool `ask`, returned tool result continuation, and model responded with "🍎 Noted: you selected Apples."
9. `web_search -> search_web -> web_search`: Isolated `cloak-live` profile was temporarily configured with `providers.webSearchOrder: [exa]` to expose direct top-level `web_search`; real OMP CLI executed `web_search` for `Golang 1.26 release notes`; model streamed `search_web`, reversed to `web_search`, OMP executed real Exa search, and continuation returned comprehensive Go 1.26 release summary. The isolated profile was then restored exactly to `providers.webSearchOrder: []`.

### 2. Mandatory Real OMP Transport-Exception Evidence
Exhaustive real OMP execution proving both transport exception tools across all required modes:
- `read -> view_file`:
  - Local file: Read `README.md` first 3 lines via `read`, model streamed `view_file`, uncloaked to `read`, returned `# omp Cloak` verbatim.
  - Local directory: Read directory `.github/scripts` via `read`, model streamed `view_file`, uncloaked to `read`, returned file entries `package-release.go` and `package-release_test.go`.
  - Supported URL: Read URL `http://127.0.0.1:8317/` via `read`, model streamed `view_file`, uncloaked to `read`, returned JSON endpoints list. Unsupported URI schemes (`memory://root`) return clean reader protocol error without gateway failure.
  - Virtual device read: Read `xd://bash` via `read`, model streamed `view_file`, uncloaked to `read`, OMP virtual device handler returned complete TypeScript tool args schema and documentation markdown.
- `write -> write_to_file`:
  - Normal file write: Wrote `.test_live_write.txt` via `write`, model streamed `write_to_file`, uncloaked to `write`, verified and cleaned up.
  - Safe virtual device dispatch: Dispatched `xd://bash` with `{"command": "echo xd_write_dispatch_success"}` via `write`, model streamed `write_to_file`, uncloaked to `write`, OMP virtual device handler dispatched to native bash runner, returning `xd_write_dispatch_success`.

### 3. Integrated Live Protocol & Lifecycle Evidence
- Representative Protected Rejection:
  - Inbound conflicting marker `X-Cloak-Client: oh_my_pi, claude_code` sent to `/v1/chat/completions` on `agy/gemini-3.8-flash`.
  - Returned exact HTTP 503 `{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}` with `Content-Type: application/json` before upstream execution.
  - Logged with non-empty host RequestID `6c8eb947` in `error-v1-chat-completions-2026-09-07T104421-6c8eb947.log`.
- Explicit OMP Non-AGY Bypass:
  - Model `gemini-3.8-flash` with `X-Cloak-Client: oh_my_pi`.
  - Preserved native `bash` tool call with zero mutation, pinned durable bypass, and cleaned via `request.complete`.
- Runtime Lifecycle Capabilities:
  - Plugin advertises `request_lifecycle_plugin: true` in registration response.
  - Gateway dispatches `MethodRequestComplete`, logging `Outcome=succeeded` and cleaning pinned route authority.

### 4. Supplemental Raw-HTTP Gateway & Protocol Wire Matrix (`tests/test_issue28_live_matrix.py`)
In addition to real OMP CLI verification, `tests/test_issue28_live_matrix.py` acts as a supplemental raw-HTTP protocol harness against `/v1/chat/completions`:
- All 9 canonical pairs passed 2-turn simulated wire round-trips with 200 OK continuation.
- Explicit OMP Non-AGY Bypass on `gemini-3.8-flash`: PASS (zero mutation, tool call preserved as `bash`, durable bypass pinned until `request.complete` with host `RequestID`).
- Protected Brand Restoration: PASS (hardened check confirms assistant stream `Hello from Antigravity and Antigravity` is restored to `Hello from omp and omp` with zero leaked `Antigravity`).
- Protected Brand `.omp` Path Preservation: PASS (path segments `C:\Users\monet\.omp\agent` and `/.omp/config` preserved byte-for-byte).

### 5. Production-Realistic Default Profile Non-Destructive Smoke
Verified production default OMP profile (`C:\Users\monet\.omp\agent`, provider `cpa` with `X-Cloak-Client: oh_my_pi`):
- Executed `omp --model cpa/agy/gemini-3.7-flash --tools read --no-session --auto-approve -p "Use the read tool to read the first line of README.md and report it."`
- Result: Native `read` executed via upstream `view_file` cloaking, streamed response uncloaked to `read`, brand restored to `omp`, output: `# omp Cloak`.

### 6. Debug Cleanliness
- `CPA_FILTER_DEBUG` unset/disabled.
- `/CLIProxyAPI/logs/cpa-filter-debug.log` truncated to 0 bytes.
