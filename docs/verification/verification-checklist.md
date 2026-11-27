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
$SourceMain = git show "${TargetCommit}:main.go"
if ($LASTEXITCODE) { throw 'Could not read main.go from pinned source' }
$VersionMatch = [regex]::Match(($SourceMain -join "`n"), 'pluginVersion\s*=\s*"([^"]+)"')
if (!$VersionMatch.Success) { throw 'Could not resolve pluginVersion from pinned source' }
$Version = $VersionMatch.Groups[1].Value
$RegistryText = git show "${TargetCommit}:registry.json"
if ($LASTEXITCODE) { throw 'Could not read registry.json from pinned source' }
$Registry = $RegistryText | ConvertFrom-Json
$RegistryEntry = @($Registry.plugins | Where-Object { $_.id -eq 'antigravity-cloak' })
if ($RegistryEntry.Count -ne 1 -or $RegistryEntry[0].version -ne $Version) {
    throw 'registry.json version does not match pinned pluginVersion'
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
nm -D /src/dist/antigravity-cloak.so | grep -E 'cliproxy_plugin_(init|call|free|shutdown)'
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

**Dirty-source builds need an explicit overlay.** The command above checks out
`$TargetCommit`, so it is only correct when the tree is committed. If you are
accepting uncommitted work, the container-internal `git clone --no-local /src
/build` carries **only committed state** and `git add -A` inside `/build` finds
nothing to commit; the build then silently emits a binary of `HEAD` and drops
every uncommitted edit. Overlay the host working tree onto the clone before
committing, and assert the string you changed is present in the consumed
source. Insert these lines **into `$BuildCommand`**, immediately after
`test -z "$(git status --porcelain)"` and before the `CGO_ENABLED=1 ... go build`
line: the detached checkout of `$CLOAK_BUILD_COMMIT` has to run first, because
the deletion list is diffed against it, and `docker run --rm` removes the clone
when it returns, so they cannot be run afterwards. The overlay's own commit
leaves `/build` clean again, so that assertion keeps its meaning.

```sh
# tar copies only files that exist, so a file deleted on the host survives in
# /build and would be baked into the acceptance commit. Drop deletions before
# extracting the host overlay.
git -C /src diff --name-only --diff-filter=D "$CLOAK_BUILD_COMMIT" |
  while read -r f; do git -C /build rm -q -- "$f" || true; done
tar -C /src --exclude=.git --exclude=dist -cf - . | tar -C /build -xf -
git -C /build add -A && git -C /build -c user.email=b@b -c user.name=b commit -m 'acceptance snapshot'
grep -c '<the string you added>' /build/main.go   # must be >= 1, or stop
```

Record both the host `git diff` sha256 and the built artifact sha256 whenever
the tree is dirty, so the record identifies exactly what was accepted.

**A `Conflict` from `up -d --force-recreate` means the recreate did not happen.**
It is a container-name collision, not a retryable error: the pre-existing
container keeps serving the previous binary, so a newly written `.so` is never
loaded and a newly set `CPA_FILTER_DEBUG` never takes effect. Two causes, both
seen on 2026-09-29:

- **Wrong project name.** `env_file` is commented out in `docker-compose.yml`,
  so `${CPA_FILTER_DEBUG}` is interpolated from the *invoking shell* - export it
  in the same shell that runs `up`. Discover the project name instead of
  guessing it: `docker ps --format '{{.Label "com.docker.compose.project"}}'`.
- **Orphaned container.** If the name is still held after the project name is
  right, `docker rm -f cli-proxy-api`, then `up -d`.

**Never treat a deploy as proven by command output.** Assert from inside the
running container: `env | grep CPA_FILTER_DEBUG` is non-empty, the installed
artifact sha256 equals the built one, and a string you just added is present in
the installed `.so`. On the host, the debug log size and the newest
request-log mtime catch a stale container immediately - a 0-byte log after a
probe means the container under test is not the container you just started.

## 3. Back up, enable controlled logging, and install while stopped

Store local-only acceptance credentials in gitignored `.env.local` (copied
from `.env.example`) or export `$env:CPA_MANAGEMENT_KEY` and
`$env:CPA_API_KEY` in the invoking shell. Never commit real keys, auth files,
full config snapshots, or raw request logs.

```powershell
$Gateway = 'http://127.0.0.1:8317'
if (!$env:CPA_MANAGEMENT_KEY -and (Test-Path .env.local)) {
    Get-Content .env.local | ForEach-Object {
        if ($_ -match '^\s*CPA_MANAGEMENT_KEY\s*=\s*(.+?)\s*$') {
            $env:CPA_MANAGEMENT_KEY = $Matches[1].Trim('"', "'")
        }
    }
}
if (!$env:CPA_MANAGEMENT_KEY) { throw 'Set CPA_MANAGEMENT_KEY or populate .env.local' }
$LocalHeaders = @{ Authorization = "Bearer $($env:CPA_MANAGEMENT_KEY)" }
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
printing the whole credential-bearing file. For custom `api: anthropic-messages`
models on `omp/18.20.0+`, also set `auth: apiKey` (`ProviderAuthSchema`:
`"apiKey" | "none" | "oauth"`) unless deliberately testing `isOAuth: true`
OAuth prompt injection.

If the model is missing (including when only Ollama appears), first check
`GET /v1/models` on this gateway, then run:

```powershell
omp --profile $OmpProfile models refresh
if ($LASTEXITCODE) { throw 'OMP model refresh failed' }
omp --profile $OmpProfile models
```

**Gate:** `$Model` (for example `cpa/agy/gemini-4-hydrogen` from `omp --profile $OmpProfile models`) must resolve before running the smoke.
`Model not found` is a local catalog failure, not an upstream 429 and not
evidence of plugin behavior. Do not delete OMP databases or create a new profile
to work around a stale catalog.

```powershell
$Model = 'cpa/agy/gemini-4-hydrogen'
$ProbeMarker = 'CLOAK_SANITIZATION_' + [guid]::NewGuid().ToString('N')
$OmpOutput = Join-Path $EvidenceDir 'omp-smoke.jsonl'
$ExpectedOutput = (git -C $RepoRoot rev-parse --short HEAD).Trim()
omp --profile $OmpProfile --model $Model --cwd $RepoRoot `
    --tools bash --no-session --auto-approve --max-time 2m --mode json `
    --append-system-prompt "<system-conventions>$ProbeMarker probe. Keep tool usage read-only.</system-conventions>" `
    -p 'Use bash exactly once to run: git rev-parse --short HEAD. Report the exact command output.' *> $OmpOutput
$OmpExit = $LASTEXITCODE
if ($OmpExit) { Write-Warning "OMP exited $OmpExit; inspect local evidence and run cleanup" }
```

> **Non-TTY / automated shells:** Always pass explicit `--profile cloak-live` (otherwise OMP targets the default production profile) and close stdin (e.g. `subprocess.run(..., stdin=subprocess.DEVNULL)` in Python or `$null | omp ...` in PowerShell) so `omp -p` does not hang waiting on TTY input.

Appending the original wrapper ensures a client whose bundle was previously
patched to `<agent-conventions>` still tests the plugin fix. Keep the unique
marker in system text. The provider prefix `cpa/` is OMP-local; the plugin sees
`agy/gemini-4-hydrogen` (or the selected `agy/*` model).

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

## Historical acceptance records

Past local acceptance records (2026-09-01 through 2026-09-30) live in [docs/verification/history.md](history.md).
