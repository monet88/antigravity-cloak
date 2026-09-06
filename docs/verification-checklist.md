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
- Local CLIProxyAPI API key: `Tonight123@`
- Installed OMP baseline: `omp/18.1.11`
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

## Tool direction

Core examples:

| OMP | Antigravity |
| --- | --- |
| `bash` | `run_command` |
| `read` | `view_file` |
| `edit` | `replace_file_content` |
| `write` | `write_to_file` |
| `grep` | `grep_search` |
| `glob` | `list_dir` |

The full mapping table lives in [CONTEXT.md](../CONTEXT.md).

OMP auxiliary devices and MCP servers are typically mounted under `xd://...` and invoked through `read`/`write`; top-level `mcp__*` tools, when present, remain pass-through traffic.

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
    apiKey: "Tonight123@"
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
  --tools bash,read,edit,write,todo `
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
