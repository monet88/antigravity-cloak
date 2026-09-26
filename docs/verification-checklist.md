# Oh My Pi <-> Antigravity Local Build and Live Verification Runbook

This is the authoritative workflow for future local Docker deployments and real
OMP acceptance. Run the steps in order in **PowerShell**, from the repository
root. Go tests prove the offline contract; only the correlated live run proves
the installed binary's upstream behavior. Historical results at the end are
reference evidence, not a substitute for checking today's runtime.

For full real-client coverage of all nine OMP mappings, transport variants, and
extended shared/fallback alias tools, use the separate
[OMP full tool-cloak checklist](verification-checklist-omp.md) after preparation
here. The single-tool probe below is not full nine-tool acceptance.

## 1. Pin source and discover the running gateway

Reuse the existing Docker stack and `cloak-live` profile. The default profile
(`C:\Users\monet\.omp\agent`, provider `cpa`, marker
`X-Cloak-Client: oh_my_pi`) is the primary real-use configuration and may point
to a different endpoint; do not change it for local acceptance.

```powershell
$ErrorActionPreference = 'Stop'
$RepoRoot = (git rev-parse --show-toplevel).Trim()
$TargetCommit = (git rev-parse HEAD).Trim()
git status --short
go test ./...
if ($LASTEXITCODE) { throw 'Go tests failed' }
go test -race -run TestRequestAliasPlanConcurrentIsolation ./...
if ($LASTEXITCODE) { throw 'Alias-plan concurrent isolation race test failed' }
go test ./.github/scripts
if ($LASTEXITCODE) { throw 'Packaging tests failed' }
go vet ./...
if ($LASTEXITCODE) { throw 'Go vet failed' }

docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
$Container = 'cli-proxy-api' # Select from the inventory above.
$i = (docker inspect $Container | ConvertFrom-Json)[0]
if ($LASTEXITCODE) { throw 'Gateway inspection failed' }
$ComposeService = $i.Config.Labels.'com.docker.compose.service'
$ComposeDir = $i.Config.Labels.'com.docker.compose.project.working_dir'
$ComposeFiles = $i.Config.Labels.'com.docker.compose.project.config_files' -split ','
$PluginMount = $i.Mounts | Where-Object Destination -eq '/CLIProxyAPI/plugins'
$ConfigMount = $i.Mounts | Where-Object Destination -eq '/CLIProxyAPI/config.yaml'
$LogsMount = $i.Mounts | Where-Object Destination -eq '/CLIProxyAPI/logs'
$AuthMount = $i.Mounts | Where-Object Destination -eq '/root/.cli-proxy-api'
if (!$ComposeService -or !$ComposeDir -or !$PluginMount -or !$ConfigMount -or !$LogsMount -or !$AuthMount) {
    throw 'Incomplete runtime discovery'
}
if (!(Test-Path -LiteralPath $ConfigMount.Source -PathType Leaf)) {
    throw 'Config mount source must be a file'
}
$ComposeArgs = @('compose', '--project-directory', $ComposeDir)
foreach ($file in $ComposeFiles) { $ComposeArgs += @('-f', $file) }
$ComposeArgs += @('--project-name', $i.Config.Labels.'com.docker.compose.project')

# Preserve discovered mounts and the currently running gateway image.
$env:CLI_PROXY_CONFIG_PATH = $ConfigMount.Source
$env:CLI_PROXY_PLUGIN_PATH = $PluginMount.Source
$env:CLI_PROXY_LOG_PATH = $LogsMount.Source
$env:CLI_PROXY_AUTH_PATH = $AuthMount.Source
$env:CLI_PROXY_IMAGE = $i.Image
$ResolvedCompose = docker @ComposeArgs config --format json | ConvertFrom-Json
if ($LASTEXITCODE) { throw 'Compose resolution failed' }
$ResolvedCompose.services.$ComposeService.volumes |
    Select-Object source, target, type
```

**Gate:** resolved config, plugin, log, and auth mounts must match the inspected
container. Preserve every other service setting. Inspect only the relevant
fields; full Compose/config output can contain secrets.

The confirmed 2026-09-12 config source was
`F:/CodeBase/antigravity-cloak/.ref/CLIProxyAPI/config.local.yaml`.
The Compose fallback `./config.yaml` resolved to a directory and caused
`failed to read config file: ... is a directory`. Always pass the discovered
source on **every** recreate, including cleanup and rollback. For future shells,
persist the correct `CLI_PROXY_CONFIG_PATH` entry in the existing Compose
`.env` without replacing its other entries, then recheck resolved mounts.
A `--project-directory` flag alone does not recover an environment override
used when the old container was created.

## 2. Build a clean Linux/amd64 shared library

For a published-release acceptance, install the published Linux/amd64 asset and
verify its published checksum instead of rebuilding. For a source deployment,
commit the intended implementation first and build the pinned commit. A dirty
host checkout must not silently supply uncommitted code to the binary.

Check gateway libc using `docker exec $Container ldd --version`. The verified
gateway uses Debian glibc 2.36. `golang:1.26.0-bookworm` matches that baseline;
the floating `golang:1.26` image tested on 2026-09-12 used glibc 2.41.
Choose a Go version matching `go.mod` and a compatible libc baseline, and record
the image digest. Do not assume a future floating image remains compatible.

```powershell
$BuildImage = 'golang:1.26.0-bookworm'
$Version = '0.5.1' # Must match pluginVersion at TargetCommit.
$SourceMain = git show "${TargetCommit}:main.go"
if ($LASTEXITCODE -or !($SourceMain -match ('pluginVersion\s*=\s*"' + [regex]::Escape($Version) + '"'))) {
    throw 'Version does not match pinned source'
}
docker pull $BuildImage
if ($LASTEXITCODE) { throw 'Build image pull failed' }
docker image inspect $BuildImage --format '{{index .RepoDigests 0}}'
New-Item -ItemType Directory -Force -Path (Join-Path $RepoRoot 'dist') | Out-Null
$BuildCommand = @'
set -eu
git config --global --add safe.directory /src
git clone --quiet --no-local /src /build
cd /build
git checkout --quiet --detach "$CLOAK_BUILD_COMMIT"
test -z "$(git status --porcelain)"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -ldflags '-s -w' -o /src/dist/antigravity-cloak.so .
rm -f /src/dist/antigravity-cloak.h
go version -m /src/dist/antigravity-cloak.so
sha256sum /src/dist/antigravity-cloak.so
readelf -h /src/dist/antigravity-cloak.so
readelf --version-info /src/dist/antigravity-cloak.so
'@
docker run --rm -v "${RepoRoot}:/src" -e "CLOAK_BUILD_COMMIT=$TargetCommit" $BuildImage sh -c $BuildCommand
if ($LASTEXITCODE) { throw 'Shared-library build failed' }
$BuiltArtifact = Join-Path $RepoRoot 'dist/antigravity-cloak.so'
$BuiltHash = (Get-FileHash -LiteralPath $BuiltArtifact -Algorithm SHA256).Hash
```

**Gate:** ELF64 shared object, x86-64, CGO enabled, `GOOS=linux`,
`GOARCH=amd64`, `vcs.revision=$TargetCommit`, `vcs.modified=false`, and required
GLIBC versions supported by the gateway. Direct builds from a Windows bind
mount can report `vcs.modified=true` despite clean host Git status; the fresh
container checkout avoids that ambiguity.

## 3. Back up, enable controlled logging, and install while stopped

The local-only acceptance API key / management password is `Tonight123`.
This is the documented workstation exception; do not publish other credentials,
auth files, full config snapshots, or raw request logs.

```powershell
$Gateway = 'http://127.0.0.1:8317'
$LocalHeaders = @{ Authorization = 'Bearer Tonight123' }
$Inventory = Invoke-RestMethod "$Gateway/v0/management/plugins" -Headers $LocalHeaders
$Loaded = @($Inventory.plugins | Where-Object id -eq 'antigravity-cloak')
if ($Loaded.Count -ne 1 -or !$Loaded[0].registered -or !$Loaded[0].effective_enabled) {
    throw 'Resolve the existing plugin state before replacing it'
}
$OldRelative = $Loaded[0].path -replace '^/?(?:CLIProxyAPI/)?plugins/', ''
$PluginRoot = [IO.Path]::GetFullPath($PluginMount.Source)
$OldArtifact = [IO.Path]::GetFullPath((Join-Path $PluginRoot $OldRelative))
$NewArtifact = [IO.Path]::GetFullPath((Join-Path $PluginRoot "linux/amd64/antigravity-cloak-v$Version.so"))
foreach ($path in @($OldArtifact, $NewArtifact)) {
    if (!$path.StartsWith($PluginRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Plugin path escapes discovered mount'
    }
}
if ($OldArtifact -ne $NewArtifact -and (Test-Path -LiteralPath $NewArtifact)) {
    throw 'Target artifact already exists; inspect its provenance first'
}
$GitDir = (git rev-parse --absolute-git-dir).Trim()
$EvidenceDir = Join-Path $GitDir ('local-acceptance-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Path $EvidenceDir | Out-Null
Copy-Item -LiteralPath $OldArtifact -Destination (Join-Path $EvidenceDir 'previous-plugin.so')
Copy-Item -LiteralPath $ConfigMount.Source -Destination (Join-Path $EvidenceDir 'config-before.yaml')
for ($n = 0; $n -lt $ComposeFiles.Count; $n++) {
    Copy-Item -LiteralPath $ComposeFiles[$n] -Destination (Join-Path $EvidenceDir "compose-before-$n.yaml")
}
$LogsBefore = @(Get-ChildItem -LiteralPath $LogsMount.Source -File | ForEach-Object FullName)
```

For the short acceptance window, edit only these settings in the discovered
local files (record their previous values):

- Compose service environment: `CPA_FILTER_DEBUG: "1"`.
- CLIProxyAPI config root: `request-log: true`.

These are different logs. Plugin debug shows route/stream handling; gateway
request logging captures original client requests and the actual upstream
requests/responses, including successful requests. ProtectedAGY does not emit
the generic `rewritten=true Body=...` debug line, so that line is not an
acceptance requirement for this route.

If the plugin config pins `store.version` or a path, align that pin with the
intended installed version while retaining the original config for rollback.
Keep the old binary **outside** the plugin discovery directory; do not leave
multiple discoverable copies with the same plugin ID.

```powershell
docker @ComposeArgs stop $ComposeService
if ($LASTEXITCODE) { throw 'Gateway stop failed; do not replace binary' }
Copy-Item -LiteralPath $BuiltArtifact -Destination $NewArtifact
if ($OldArtifact -ne $NewArtifact) { Remove-Item -LiteralPath $OldArtifact }
if ((Get-FileHash -LiteralPath $NewArtifact -Algorithm SHA256).Hash -ne $BuiltHash) {
    throw 'Installed checksum differs from build; rollback before starting'
}
docker @ComposeArgs up -d --force-recreate --pull never $ComposeService
if ($LASTEXITCODE) { throw 'Gateway recreation failed; follow rollback' }
```

**Gate:** wait for the management API to respond, not just `docker compose`
to return. A restart loop/config error is a failure, not readiness. Verify the
config mount again with `docker inspect`.

```powershell
$Inventory = $null
$ReadyDeadline = (Get-Date).AddSeconds(30)
do {
    try {
        $Inventory = Invoke-RestMethod "$Gateway/v0/management/plugins" -Headers $LocalHeaders -TimeoutSec 3
    } catch {
        Start-Sleep -Seconds 1
    }
} while (!$Inventory -and (Get-Date) -lt $ReadyDeadline)
if (!$Inventory) { throw 'Gateway readiness deadline exceeded; inspect startup logs and rollback' }
$Loaded = @($Inventory.plugins | Where-Object id -eq 'antigravity-cloak')
if ($Loaded.Count -ne 1 -or !$Loaded[0].registered -or !$Loaded[0].effective_enabled -or $Loaded[0].metadata.version -ne $Version) {
    throw 'Intended plugin did not load'
}
$Loaded | Select-Object id, path, registered, effective_enabled
docker logs --since 2m $Container 2>&1 |
    Select-String 'pluginhost: plugin (loaded|registered)|failed to load|panic'
docker exec $Container sh -c ': > /CLIProxyAPI/logs/cpa-filter-debug.log'
if ($LASTEXITCODE) { throw 'Could not reset controlled debug log' }
```

Confirm the management path resolves to `$NewArtifact`. Any installation,
startup, or verification failure must still go through cleanup; use rollback
if the gateway cannot serve with the new artifact.

## 4. Refresh the isolated OMP profile and run a bounded probe

```powershell
$OmpProfile = 'cloak-live'
$OmpRoot = (omp --profile $OmpProfile config path).Trim()
omp --version
omp --profile $OmpProfile models
```

Verify `$OmpRoot/models.yml` has provider `cpa`, local base URL
`http://127.0.0.1:8317/v1`, `api: openai-completions`, the local acceptance key,
`headers: {X-Cloak-Client: oh_my_pi}`, and
`discovery: {type: openai-models-list}`. Inspect selected fields without
printing the whole credential-bearing file.

If the model is missing (including when only Ollama appears), first check
`GET /v1/models` on this gateway, then run:

```powershell
omp --profile $OmpProfile models refresh
if ($LASTEXITCODE) { throw 'OMP model refresh failed' }
omp --profile $OmpProfile models
```

**Gate:** `cpa/agy/gemini-3.8-flash` must resolve before running the smoke.
`Model not found` is a local catalog failure, not an upstream 429 and not
evidence of plugin behavior. Do not delete OMP databases or create a new profile
to work around a stale catalog.

```powershell
$ProbeMarker = 'CLOAK_SANITIZATION_' + [guid]::NewGuid().ToString('N')
$OmpOutput = Join-Path $EvidenceDir 'omp-smoke.jsonl'
$ExpectedOutput = (git -C $RepoRoot rev-parse --short HEAD).Trim()
omp --profile $OmpProfile --model cpa/agy/gemini-3.8-flash --cwd $RepoRoot `
    --tools bash --no-session --auto-approve --max-time 2m --mode json `
    --append-system-prompt "<system-conventions>$ProbeMarker probe. Keep tool usage read-only.</system-conventions>" `
    -p 'Use bash exactly once to run: git rev-parse --short HEAD. Report the exact command output.' *> $OmpOutput
$OmpExit = $LASTEXITCODE
if ($OmpExit) { Write-Warning "OMP exited $OmpExit; inspect local evidence and run cleanup" }
```

Appending the original wrapper ensures a client whose bundle was previously
patched to `<agent-conventions>` still tests the plugin fix. Keep the unique
marker in system text. The provider prefix `cpa/` is OMP-local; the plugin sees
`agy/gemini-3.8-flash`.

## 5. Correlate the actual request, stream, and native execution

Read OMP JSONL as UTF-8 (allow an optional BOM), parse JSON records, and inspect:

- Exactly one `tool_execution_end`: `toolName: bash`, `isError: false`, and
  the command result contains `$ExpectedOutput`.
- The matching `tool_execution_start` used `git rev-parse --short HEAD`.
- Assistant continuation reports the same output with a successful stop.
- No unknown-tool, schema, retry-loop, plugin panic, or unexpected rejection.

Find the new gateway request-log files containing `$ProbeMarker`; save their
**exact paths** for cleanup. Correlate by request ID and tool-call ID, not by
co-occurrence somewhere in the log:

| Evidence section | Required result |
| --- | --- |
| `REQUEST BODY` | Original system wrapper contains the unique marker; tool declaration is native `bash`. |
| `API REQUEST n` | System wrapper is `<conventions>`; the original exact wrapper is absent from selected prompt text; declaration is `run_command`. |
| `API RESPONSE n` | HTTP 200 with a streamed `run_command` tool call. |
| `RESPONSE` | HTTP 200; the same tool-call ID carries native name `bash`. |
| OMP JSONL and next request | Native `bash` executes successfully and its result participates in continuation. |

Parse each section's JSON/SSE before checking string values. Upstream JSON can
encode `<` and `>` as `\u003c` and `\u003e`; raw substring counting would
falsely report missing replacement tags. Compare tool **name fields**, not IDs:
IDs may legitimately retain a `run_command-` prefix after the tool name is
restored. Review every upstream attempt if the log contains multiple numbered
API sections; success after hidden retries is not a clean no-retry acceptance.

Save a redacted summary: source commit, build-image digest, artifact SHA256,
plugin version/path, model, request IDs, status codes, observed transformations,
native command/result, failures, and cleanup outcome. Keep raw bodies and
credentials only in the local evidence area until cleanup; do not commit them.

This smoke validates one tool round trip and the wrapper fix. For changes to
other mappings, transport modes, lifecycle, or reverse-brand handling, exercise
those affected behaviors as well. The nine-tool mapping and wider historical
matrix below describe additional coverage, not coverage implied by one bash
call. `Antigravity -> omp` checks belong in assistant-visible text; tool
arguments/metadata retain literal brands.

## 6. Cleanup on success AND failure

Before finishing, restore the previous `request-log` setting (normally absent
or false) and remove `CPA_FILTER_DEBUG` from the service environment, including
overrides/env files. Any non-empty value enables plugin debug, even `"0"`.
Restore only the settings changed for the probe; do not overwrite unrelated
concurrent config edits with a whole-file backup.

Recreate with the same discovered mounts and image, then truncate logs:

```powershell
docker @ComposeArgs up -d --force-recreate --pull never $ComposeService
if ($LASTEXITCODE) { throw 'Cleanup recreate failed; gateway is not verified' }
# Wait for readiness as in step 3 before continuing.
docker exec $Container sh -c ': > /CLIProxyAPI/logs/cpa-filter-debug.log'
if ($LASTEXITCODE) { throw 'Debug-log truncation failed' }
$FinalInfo = (docker inspect $Container | ConvertFrom-Json)[0]
if (@($FinalInfo.Config.Env | Where-Object { $_ -match '^CPA_FILTER_DEBUG=.+$' }).Count) {
    throw 'Plugin debug remains enabled'
}
if ((Get-Item -LiteralPath (Join-Path $LogsMount.Source 'cpa-filter-debug.log')).Length -ne 0) {
    throw 'Debug log is not empty'
}
```

Truncate only the exact gateway request logs owned by this probe, after saving
the redacted summary; verify each resolved path stays inside the discovered log
directory. Remove/truncate raw OMP captures if no longer needed. Preserve
unrelated logs and the local rollback files. Do not use broad recursive deletes.

Recheck management registration/version/path, installed SHA256, config mount,
container stability, and authenticated `GET /v1/models` = 200 **after** cleanup.
A prior successful smoke does not prove the final recreated container is healthy.

## 7. Rollback if installation/startup fails

Use the paths captured in step 3. Stop the gateway and verify it has stopped
before changing either binary. Restore `previous-plugin.so` to `$OldArtifact`;
remove `$NewArtifact` only if it is a distinct, verified path for this deployment.
Restore the prior plugin pin/config settings, remove temporary logging settings,
and recreate with the same explicit Compose mounts and image. Verify the old
version/path is registered and the gateway is healthy. Report the failed new
deployment separately; never label a rollback as new-version acceptance.

## Validated sanitization deployment - 2026-09-12

- Merged source: `5d35663659530c3fbd841615c467343269ad616d`
  ([PR #30](https://github.com/monet88/antigravity-cloak/pull/30)); PR and main CI passed.
- Build: Go 1.26.0, Linux/amd64, CGO `c-shared`, clean container checkout,
  `vcs.modified=false`.
- Build image: `golang:1.26.0-bookworm`,
  digest `sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c`.
- Artifact SHA256: `b00a53ab22ea1ddf009cb9a2108da697b8ee69d13329104441e39e594b18fa73`.
- CLIProxyAPI v7.2.146 (`d31b159`), plugin v0.5.1 loaded from
  `plugins/linux/amd64/antigravity-cloak-v0.5.1.so`; OMP 18.1.18,
  `cloak-live`, model `cpa/agy/gemini-3.8-flash`.
- Correlated requests: `53c9ffca`, `5d739ed9`. Both had the original probe
  wrapper at ingress, the sanitized wrapper upstream, and HTTP 200.
  For this fixture, original opening tags changed from 3 to 0 and replacement
  opening tags from 1 to 4; these counts are fixture-specific.
- Streamed `run_command` became native `bash`; OMP executed
  `git rev-parse --short HEAD` and returned `5d35663`, followed by successful
  continuation.
- Repaired Compose's config mount and refreshed the stale OMP model catalog.
  Final registration/health/checksum passed after cleanup; plugin debug was
  disabled, debug log empty, and both controlled request logs truncated.
- Coverage: sanitization plus one native bash round trip. This run did not
  repeat the full nine-tool matrix below.

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
