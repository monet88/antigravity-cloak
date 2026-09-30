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
committing, and assert the string you changed is present in the consumed source:

```sh
tar -C /src --exclude=.git --exclude=dist -cf - . | tar -C /build -xf -
git add -A && git -c user.email=b@b -c user.name=b commit -m 'acceptance snapshot'
grep -c '<the string you added>' main.go   # must be >= 1, or stop
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

## Live acceptance record - 2026-09-30 (client path scope)

Claude Code live acceptance for the client-home-path scope change, run on this
workstation against the local gateway with the default `cpa` provider. This is
a working-tree acceptance, not a release claim - no tag exists, and the source
is a dirty snapshot.

### Pinned run

| | |
| :--- | :--- |
| Host commit | `e2c7157` plus the uncommitted working tree |
| `git diff` sha256 | `23abcdaa01048a720d78d063ef3f5b734b95ebcaf1e787e7600aa013ab349407` |
| Built artifact sha256 | `33f1296f46bfbeddc58ba44c96313512b124d0e3baa3d911a79f3758eed5796d` |
| Previous artifact sha256 | `ecb761e9e09085f5532614bb9a7b62cd141758c191b31e27f09257de4fc69caf` |
| Build image | `golang:1.26.0-bookworm` (`golang@sha256:2a0ba12e…6677c`), gateway glibc 2.36 |
| Plugin version | 0.6.0, `registered=true`, `effective_enabled=true` |
| Client under test | `X-Cloak-Client: claude_code`, `User-Agent: claude-cli/2.1.284` |
| Evidence | `.git/local-acceptance-20260929-205449/` (not in the repository) |

The build used the dirty-tree overlay from step 2, asserted by the presence of
`claudeReverseContextMappings` and `func pathRules` in the consumed source. The
installed binary was proved from inside the running container rather than from
command output: `env | grep CPA_FILTER_DEBUG` returned `1`, and the in-container
`sha256sum` equalled the built hash.

### What the measurement changed

The scope was first narrowed to a home spelling (`~/`, `./`, `~\`, `.\`) on the
reasoning that a harness advertises its instruction file in that form, so `~`-form
rules would cover the injected context. The unit suite was green. Live traffic
showed the premise was wrong on this platform:

| token | `=== REQUEST BODY ===` (from Claude Code) | `=== API REQUEST 1 ===` (upstream) |
| :--- | :--- | :--- |
| `Claude Code` | 15 | 0 |
| `CLAUDE.md` | 8 | 0 |
| `Anthropic` | 8 | 1 |
| `C:\Users\monet\.claude\…` | **114** | **114** |

Claude Code on Windows puts absolute client-home paths in its system context
and never uses the tilde form: `transcripts` 97, `plugins` 7, the instruction
file 6, `lsp-shims` 3, `projects` 1. All 114 reached the model uncloaked. The
rules are therefore matched as a whole segment at any position, in both
directions, and are asserted against observed traffic rather than against
documentation.

### Per-case results - Claude Code

| case | result |
| :--- | :--- |
| absolute client-home path to upstream | 114 -> **0** |
| `Claude Code` to upstream | 15 -> **0** |
| `You are Claude` to upstream | 1 -> **0** |
| tool call `C:/Users/monet/.claude` -> client | `C:/Users/monet/.claude` (cloaked, then restored) |

### Honest gaps

- **The residual `.claude`, `CLAUDE.md` and `Anthropic` counts in the upstream
  request are the operator's own text and file contents, not injected context.**
  The session had Claude Code reading this repository's `main.go`, which
  contains `pathRules(".claude", ".gemini", …)` in its own source, and the
  operator's prompt text sat in the conversation history. Rewriting those would
  corrupt code and misquote the operator, so they are correctly left alone.
- **A model-invented `.gemini` at end of string is not reversed.** The model had
  seen the cloaked directory in context and emitted `C:/Users/monet/.gemini`
  with no trailing separator; the segment rule requires one, so the client
  received the cloaked spelling. This is the accepted residual of the
  over-remap design - the reverse pass cannot distinguish a name the model
  echoes from one the forward pass produced.
- **A `.claude` belonging to a different project is rewritten too.** The plugin
  cannot tell which home directory is the client's own. Accepted deliberately:
  that costs one path the model may wander into, where the narrow form cost 114
  on every request.
- Oh My Pi and Codex were not exercised in this run. `pathRules` covers both and
  both are unit-tested, but neither has a live record for this change.

## Live acceptance record - 2026-09-29

Oh My Pi live acceptance for the PR #46 streaming/reverse remediation, plus the
URL/path rule revision that came out of it. Run on this workstation against the
local gateway with the isolated `cloak-live` profile. **This is an uncommitted
working-tree acceptance, not a release claim** - no tag exists, and the source
below is a dirty snapshot reviewed by the coordinator, not a commit.

### Pinned run

- Source: `9af5a97f11639622a2c59182a841a14fa92ac701` (`fix/streaming-brand
  reverse`, tip of `fix/streaming-brand-reverse`) plus a **dirty** tree:
  `main.go`, `filter_test.go`, `issue49_operational_paths_test.go`,
  `reverse_brand_test.go`, `issue48_carry_order_test.go` modified and
  `issue51_operational_roundtrip_test.go` untracked.
  `git diff | sha256sum` = `de8758e0c450fcd871e957018499baad145efbc0176d29f0b5a7541db0cbaef9`,
  recorded before and after the run and unchanged. No commit, no push.
- Build image: `golang:1.26.0-bookworm`, digest
  `sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c`.
- Artifact: `dist/antigravity-cloak.so`, SHA256
  `2f9cd356fed33817e0352e371b834b1c6819fe571d16dd75a453edb89acb1a7f`
  (the four-blocker build) and then
  `d50f905ed51f7eca39a6da154a8d94382d4fe29f3e48c9ad721c8cae41dcd8c3`
  after the URL/path rule revision. 5,280,536 bytes. `go version -m` reports
  `go1.26.0`, `CGO_ENABLED=1`, `GOOS=linux`, `GOARCH=amd64`,
  `vcs.revision=9af5a97…`, and `vcs.modified=true` - the last is expected and
  is the provenance marker for a deliberately dirty snapshot.
  `readelf` reports ELF64 DYN, x86-64, GLIBC requirement <= 2.34 (gateway
  glibc 2.36).
- Installed as `plugins/linux/amd64/antigravity-cloak-v0.6.0.so`; the host
  registered `version=0.6.0` from that path and the in-container SHA256 matched
  the built artifact byte-for-byte. `pluginVersion` and `registry.json` both
  read `0.6.0`; no `v0.6.0` tag exists.
- Gateway: `cli-proxy-api`, CLIProxyAPI **v8.0.3**, image
  `sha256:69326f4bcf4f6e68a7a84885be8e89adb917ef47227a20f3cc9b405e7130bc50`.
  All four discovered mounts (config `config.local.yaml`, `plugins`, `logs`,
  `auths`) preserved across both deployments and both cleanups.
- OMP `18.4.2` on PATH (`--help` reports 18.4.3 after its own update check).
- `cloak-live`: provider `cpa` -> `http://127.0.0.1:8317/v1`,
  `api: openai-completions`, `headers: {X-Cloak-Client: oh_my_pi}`,
  `discovery: {type: openai-models-list}`. All 24 catalog models resolve under
  provider `cpa`.
- **Every protected test used `cpa/agy/gemini-3.8-flash`.** The plugin-visible
  model at ingress was `agy/gemini-3.8-flash` in the request body and in the OMP
  JSONL `provider`/`model` fields. No `3.7*`, no `3.8-flash-high`, no other
  route. The `gemini-3.8-flash-high` name that appears in `API REQUEST` is the
  Antigravity route's own internal upstream model mapping, not a model choice.
- Controlled capture: `request-log: true` throughout. `CPA_FILTER_DEBUG` was
  enabled only for the bounded manual window, then disabled and truncated to
  0 bytes. After cleanup: plugin registered 0.6.0 from the expected path,
  installed SHA256 matching, all mounts preserved, authenticated
  `GET /v1/models` = 200.

### URL/path rule (revised during this run)

A bare vendor word inside a URL or filesystem path is rewritten **only** as a
literal dot-prefixed directory segment, remapped onto the client's neutral
equivalent. Every other position in a path is left byte-for-byte alone.

| Input | Result |
| :--- | :--- |
| `.omp/agent`, `C:\Users\u\.claude\settings.json` | remapped to `.gemini/...` (unchanged behaviour) |
| `/omp/`, `\omp\`, `/claude/`, `/codex/` | never rewritten |
| `F:/CodeBase/antigravity-cloak/main.go` | never rewritten |
| `https://omp.ai/pricing` | never rewritten |
| `.omp-backup/` | never rewritten |
| `profile.omp/` | still masked (prose, not a segment) |
| `claude.ai` (composed domain mapping) | still cloaks to `antigravity.google` |
| `You are omp.` (prose) | still masked to `You are Antigravity.` |

The guard is applied in **both** directions. A forward-only fix still let the
response reverse rewrite `antigravity-cloak` into `omp-cloak`, because the
reverse walk is case-insensitive and cannot know which tokens the forward pass
actually introduced - the same one-way-authority class as the P1/P2 fixes in
this PR. Composed entries (`claude.ai`, `Antigravity SDK`, `.gemini/CLAUDE.md`)
are exempt and keep their existing behaviour; only bare single-word mappings are
subject to the rule. This reverses the earlier recorded decision that
non-dot delimiters should mask to Antigravity.

### Live defect found and fixed during this run

**Minimal repro.** This repository lives at `F:/CodeBase/antigravity-cloak/`.
Asking the real OMP CLI to read a fixture by an absolute path under that
directory produced a corrupted path on the client. The path sits in the user's
typed prompt, so the forward pass correctly left it literal; the model echoed it
back in its `view_file` argument, and the response reverse pass rewrote
`antigravity` -> `omp` with a case-insensitive matcher:

```text
model received : F:/CodeBase/antigravity-cloak/.git/.../sample.txt
client received: F:/CodeBase/omp-cloak/.git/.../sample.txt
=> read failed "Path not found" on two consecutive attempts
```

Root cause: `ompProtectedReverseTable` is a single
`{Match: "Antigravity", Replacement: "omp"}` entry and
`replaceInsensitiveWithPrev` lowercases both sides, while the forward pass is
authority-aware and the reverse is not. This violates the HARD contract that
operational identifiers round-trip client-specifically. Fixed by the URL/path
rule above, applied symmetrically. **No synthetic unit fixture exposes this**:
fixtures like `/home/u/.gemini` are clean by construction, and only a real run
whose working directory contains a brand word reveals it.

### Per-case results - Oh My Pi

All runs: real `omp` CLI, `cloak-live`, `cpa/agy/gemini-3.8-flash`, fixture
root `C:/Users/monet/omp-cloak-fixture` containing a literal `.omp/agent/`
segment plus a `.gemini/agent/decoy.txt` that must never be read.

| Case | Transport | Result |
| :--- | :--- | :--- |
| A. Baseline smoke, `bash` | `POST /v1/chat/completions` | PASS - ingress `bash` -> upstream `run_command` (id `run_command-1790654659627457245-1`) -> client `bash`, result `9af5a97` (matches HEAD), continuation, single upstream attempt |
| B. Canonical matrix `read`/`write`/`edit`/`grep`/`glob` | same | PASS 5/5, 0 errors, on-disk result `MATRIX_EDIT_OK` |
| B. `task` | same | PASS for identity and spawn (`invoke_subagent` -> `task`, subagent `ReadSampleFile` spawned `isError=false`); the parent read `agent://` before the subagent delivered, which is a harness race in the prompt, not a plugin result |
| B. `ask` | - | NOT RUN - declared only in interactive mode; `execute` throws "Ask tool requires interactive mode" headless. Not simulated |
| B. `web_search` | - | NOT RUN - `providers.webSearchOrder` is `[]` in the acceptance profile; `search_web` is declared upstream but was never driven |
| C.1 Tool identity, all cases | same | PASS - every upstream name restored to the exact native OMP name |
| C.2 Operational path, model-derived | same | PASS - see chain below |
| C.2 Operational path, user-typed | same | PASS - the `.omp` path was left literal in both directions and the real fixture was read |
| C.2 Brand-word directory (the defect) | same | PASS after the fix - 1 tool call, 0 errors, byte-identical in both directions |
| C.3 Streamed tool-argument carrier | `openai-completions` | PASS - see chain below |
| C.3 Anthropic transport | - | NOT RUN - the profile is `openai-completions`; switching transport means editing the profile. No Anthropic live coverage is claimed |
| C.4 Flush framing / source order | - | NOT induced live - a split operational token could not be induced reliably through the live model. Still covered offline by the `processChunk` regressions `TestIssue48_ReheldLaneTakesItsNewArrivalOrder` and `TestIssue48_PathCarryStillReassembles`. No live proof is fabricated |
| Extended: shared `wp_*` aliases | same | PASS - `wp_todo`, `wp_find`, `wp_eval`, `wp_vibe_*` all restored |
| Extended: deterministic `wp_ext_<hash>` | same | PASS - `wait` -> `wp_ext_061bef0f1c6ccd0b4819958bcb73eba6`, `yield` -> `wp_ext_6000f482bcb616c9b358f46162f191f6`, `goal` -> `wp_ext_63f44033c2aa095324c02661c93b17b9`; all three reproduce `sha256("request-alias-v1\x00" + source)[:16]` |
| Extended: `learn` / `manage_skill` | same | PASS - required `autolearn.enabled: true` plus `memory.backend: local`; upstream `wp_learn` -> client `learn`, `isError=false`, "Lesson stored." |
| Extended: `xd://` MCP device | same | PASS - `.mcp.json` in the cwd mounts 14 `mcp__gitnexus_*` devices; `read` on `xd://mcp__gitnexus_list_repos` returned the real tool documentation with the URI byte-identical, reached through the cloaked `read` |
| D. `goal` mode (TUI) | same | PASS - 13-tool set (adds `ask_question` and the `goal` fallback alias); `goal` round-tripped with `{"op":"complete"}` |
| D. `vibe` mode (TUI) | same | PASS - toolset shrank to 7 tools with `run_command`, `write_to_file` and `replace_file_content` absent, confirming read-only; `vibe_spawn`, `vibe_wait`, `vibe_kill` all restored |
| D. `loop` mode (TUI) | same | PASS - 8 requests carried the `yield` alias upstream, so the loop really re-submitted, and `yield` restored its exact name each time |

### Correlated evidence

**Operational path round trip, model-derived** (log
`v1-chat-completions-2026-09-29T121034-0000001a.log`). The model was *not*
given the path; it derived it from its own system prompt, which the plugin had
already cloaked:

```text
ingress      declared read                    system .omp x0
upstream     declared view_file               system .omp x0  .gemini x5
upstream call: view_file id=call_281605
              args {"path":"C:/Users/monet/.gemini/agent/AGENTS.md"}   <- cloaked
client sees : read id=view_file-1790655033980858952-6
              args {"path":"C:/Users/monet/.omp/agent/AGENTS.md"}      <- restored
OMP executed: read on the real file -> "# Global Agent Rules", isError=false
```

**Brand-word directory, after the fix** (log
`v1-chat-completions-2026-09-29T123605-00000002.log`), byte-identical in both
directions with no retry:

```text
UPSTREAM view_file  path = F:/CodeBase/antigravity-cloak/.../fixture/.omp/agent/sample.txt
CLIENT   read       path = F:/CodeBase/antigravity-cloak/.../fixture/.omp/agent/sample.txt
OMP read isError=false -> "OMP_PATH_MARKER_LINE_1\nsecond line"
```

**Manual matrix run** (14 requests), every tool restored and every `.omp` path
intact:

| upstream | client |
| :--- | :--- |
| `find_by_name` | `glob` |
| `grep_search` | `grep` |
| `view_file` | `read` |
| `write_to_file` | `write` |
| `replace_file_content` | `edit` |
| `run_command` | `bash` |
| `invoke_subagent` | `task` |
| `wp_ext_6000f482bcb616c9b358f46162f191f6` | `yield` |

**Mode runs** (31 requests): declaration sets of 13 / 7 / 11 tools for goal /
vibe / normal, every name mapped, and the upstream `Antigravity` count in the
system instruction dropped from 43 to 14 after the URL/path rule, which is the
fewer-wrong-path-writes signal.

**Cleanliness across the whole run.** 0 client-side `.gemini` leaks in 126
streamed tool-argument streams. The decoy `.gemini/agent/decoy.txt` was never
read - its marker appears in 0 of the 13 logs that touched the fixture. The
plugin debug log contains 0 matches for `panic`, `unknown tool`,
`admission rejected`, `503` or `schema mismatch`. Every request carried the
explicit `X-Cloak-Client: oh_my_pi` marker. No hidden retries.

### Setup and cleanup notes for the next run

- **MCP config location.** `.mcp.json` is discovered from the **working
  directory**, not from the profile. `mcp.enableProjectConfig` defaults to
  `true`. Putting `mcp.json` in the profile root does nothing.
- **`learn` gating.** `autolearn.enabled` defaults to `false`, so the `learn`
  and `manage_skill` tools are absent until it is set, and `LearnTool.createIf`
  additionally requires `memory.backend` to be `hindsight`, `mnemopi` or
  `local`.
- **Modes are TUI commands.** `vibe`, `goal` and `loop` are slash commands, not
  tools; the plugin never sees their names. `goal.enabled` defaults to `true`.
  They must be driven from an interactive session.
- **`CLAUDE.md -> AGENTS.md` remains intentionally one-way** and was not
  exercised in this run.
- **Profile edits must preserve `.env`.** Overwriting `.env` with only
  `CPA_FILTER_DEBUG` drops `CLI_PROXY_CONFIG_PATH` and the gateway restart-loops
  with `failed to read config file: /CLIProxyAPI/config.yaml: is a directory`.
  Always keep both lines.
- Raw captures, OMP JSONL, the redacted summary, and the pre-change backups stay
  in the gitignored local evidence area and are not committed.

### Remaining gaps against the HARD contract

- Claude Code and OpenAI Codex were not re-attested in this pass; the 2026-09-27
  record's pending items stand.
- The Anthropic transport and the `ask` / `web_search` tools have no live
  coverage from this run.
- Source-order flush reproduction remains offline-only.
- The response reverse still leaves a bare vendor word inside a path untouched
  when the model copies a prose-masked token into a tool argument (for example
  `/tmp/Antigravity`). That is the deliberate cost of a symmetric path rule: a
  cosmetic leftover was chosen over a path that does not exist.

## Live acceptance record - 2026-09-27

Real-client acceptance for Oh My Pi, Claude Code and OpenAI Codex against the
request-scoped alias-plan build, run on this workstation against the local
gateway.

**Status.** Oh My Pi passed every criterion the installed client (`18.3.4`)
actually exposes: seven of the nine canonical tools under their bare spelling,
and `ask`, `web_search` plus the escaped-canonical, shared-alias,
deterministic-fallback and `xd://` variants over the `anthropic-messages` wire.
Bare `ask` and bare `web_search` remain unexercised. Claude Code and OpenAI
Codex ran against the same artifact but are **manual verification
pending/deferred by the user**: Issue #40 is therefore not complete, and nothing
in this record is a release claim (no `v0.6.0` tag exists). Raw captures stay in
the local evidence area under `.git/` (gitignored) and are not committed.

### Pinned run

- Source revision: `4e946acddea8a387efcb5fafd4635cf4b98bd6c3` (tip of `main` at
  capture time); worktree clean, so the artifact contains exactly that commit.
  The acceptance work ran on branch `feat/issue-40-three-client-acceptance`,
  whose follow-up commits after `4e946ac` are documentation-only
  (`git diff --stat 4e946ac..<branch>` touches no code).
- Build image: `golang:1.26.0-bookworm`,
  digest `sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c`.
- Artifact: `dist/antigravity-cloak.so`, SHA256
  `a9b4e88833784e14063e25b0716bedbdebe91a615386394e0bd6ca4153ca013b`,
  5,234,840 bytes. `go version -m` reports `go1.26.0`, `CGO_ENABLED=1`,
  `GOOS=linux`, `GOARCH=amd64`, `vcs.revision=4e946ac…`, `vcs.modified=false`;
  `readelf` reports ELF64 shared object, x86-64, GLIBC requirement ≤ 2.34
  (gateway glibc 2.36).
- Installed as `plugins/linux/amd64/antigravity-cloak-v0.6.0.so`; the host
  registered `version=0.6.0` from that path. `pluginVersion`, `registry.json`
  and the candidate 0.6.0 changelog section agree; no `v0.6.0` tag exists yet.
- Gateway: `cli-proxy-api` (CLIProxyAPI `v7.3.19`, image
  `sha256:d8fb8d2d7a847696332d8abf66bd907b129174fa626d84fd1c95e587d620010a`);
  config mount `.ref/CLIProxyAPI/config.local.yaml`, plugin mount
  `.ref/CLIProxyAPI/plugins`, both preserved across the acceptance window.
- Controlled capture: `CPA_FILTER_DEBUG=1` plus `request-log: true` for the
  first window, and `request-log: true` alone (plugin debug empty) for the
  escaped-canonical window. Both were reverted afterwards, the container
  restarted, the plugin debug log truncated to 0 bytes, `CPA_FILTER_DEBUG` left
  empty, and an authenticated `GET /v1/models` returned 200 after cleanup with
  `plugin registered … version=0.6.0` in the host log.
- Request-body size isolation: probe bodies of 409 B, 150 KB and 1.1 MB to
  `agy/gemini-3.7-flash-high` all returned HTTP 200, so the Claude Code 429s
  below are not explained by body size.

### Per-client results

| Client | Transport | Result |
| :--- | :--- | :--- |
| Oh My Pi `18.3.4` (`cloak-live`, `openai-completions`, bare canonical) | `POST /v1/chat/completions` → local `127.0.0.1:8317` | `read`, `write`, `edit`, `bash`, `grep`, `glob` executed end-to-end with continuation; `task` spawned a subagent that read the fixture (one subagent `read` errored) |
| Oh My Pi `18.3.4` (`cloak-live`, `anthropic-messages`, escaped canonical) | `POST /v1/messages` → local `127.0.0.1:8317` | PASS (escaped wire) for `_read`, `_bash`, `_ask`, `_web_search`, shared alias (`_todo → wp_todo`), deterministic fallbacks and `xd://` device dispatch; every case restored exactly, executed natively and continued. Bare `ask` and bare `web_search` are NOT RUN |
| Oh My Pi `18.3.4` (default profile, unchanged, remote endpoint) | the default profile's own remote gateway URL (discovered from that profile, not pinned here) | PASS for `read`; validates the default-profile path and the remote deployment, not this local artifact |
| Claude Code `2.1.283` | `POST /v1/messages` → local `127.0.0.1:8317`, model `agy/gemini-3.7-flash-high` | Declaration/mapping PASS (20/20, zero source leakage); execution/continuation BLOCKED by upstream HTTP 429 — **manual verification pending** |
| OpenAI Codex `codex-cli 0.157.1` via `opencodex 2.67.0` | `POST /v1/chat/completions` → local `127.0.0.1:8317` | PASS in both declaration modes (`exec` code mode, `exec_command` shell mode) with exact restoration, real execution and continuation — **manual repetition deferred** |

Selected correlated evidence (ingress declaration set → upstream set → client
set), read from the gateway request logs by section and parsed as JSON/SSE:

- OMP `bash`: ingress `bash, edit, eval, find, glob, grep, read, task, todo, wait,
  web_search, write` → upstream `find_by_name, grep_search, invoke_subagent,
  replace_file_content, run_command, search_web, view_file, wp_eval,
  wp_ext_061bef0f1c6ccd0b4819958bcb73eba6, wp_find, wp_todo, write_to_file` →
  client `bash`. OMP executed `printf OMP_BASH_OK`, then continued.
- OMP `todo`/`find`: `todo → wp_todo`, `find → wp_find` upstream, restored to
  `todo`/`find` client-side; both executed against the fixture.
- OMP wrapper sanitization on the same request: ingress system text
  `<system-conventions>` ×2 → upstream 0, `<conventions>` 1 → 3.
- OMP escaped canonical (`OMP-ESC-01`, `POST /v1/messages`): ingress
  `tools[].name="_read"` and `"_bash"` → upstream `functionDeclarations`
  `view_file`/`run_command` with no `_read` present → upstream response
  `functionCall {"name":"view_file",…,"id":"call_2243389"}` → downstream SSE
  `content_block_start … "name":"_read" … "type":"tool_use"` → the client
  executed `read {path: esc-note.txt}` (content `ESCAPE_FIXTURE_LINE`) and then
  `bash {"command":"echo ESCAPE-OK"}`. The continuation request carried `_read`
  and `_bash` in its history, upstream carried `view_file`/`run_command`, and the
  model produced the final summary (exit 0).
- OMP shared alias plus deterministic fallbacks (`OMP-ESC-02`): ingress `_todo`,
  `_find`, `_wait` → upstream `wp_todo`,
  `wp_ext_8bfa04d75e222553a5dc712e5ec3671e`,
  `wp_ext_416dac4969d214f84545c92795d9a734` → downstream restored to `_todo`,
  `_find`, `_wait` verbatim. Both hashes reproduce `fallbackAliasForSource`
  (`sha256("request-alias-v1\x00" + source)[:16]`) for `_find` and `_wait`. The
  client executed `todo` (init + done), `find` and `wait` without error.
- OMP `xd://` device dispatch (`OMP-ESC-03`): ingress carried `_read` with
  `path: xd://todo`; upstream carried `view_file` with the same
  `path: xd://todo` byte-identical, and the downstream SSE delta restored the
  tool name while leaving the URI intact. The client dispatched the read to the
  `xd://todo` device and returned its output.
- OMP `ask` (`OMP-ESC-05`, real TUI session driven over a pty): ingress `_ask` →
  upstream `ask_question` → upstream `functionCall {"name":"ask_question",…,
  "id":"call_2727813"}` → downstream `content_block_start … "name":"_ask" …` →
  the TUI rendered the Ask overlay and the operator selected "Green". The
  continuation request carried
  `functionResponse {"name":"ask_question","response":{"result":{"text":"User
  selected: Green"}}}` upstream while the client history kept `_ask`.
- OMP `web_search` (`OMP-ESC-04`): ingress `_web_search` → upstream
  `search_web` → upstream `functionCall {"name":"search_web",…}` → downstream
  `content_block_start … "name":"_web_search" …` → the client executed
  `web_search` and returned live results, then continued.
- Claude Code: ingress `Agent, Bash, CronCreate, CronDelete, CronList, Edit,
  EnterWorktree, ExitWorktree, Glob, Grep, ListAgents, NotebookEdit, Read,
  ReportFindings, ScheduleWakeup, SendMessage, TaskStop, WebFetch, Workflow,
  Write` → upstream `invoke_subagent, run_command, wp_create_schedule,
  wp_delete_schedule, wp_list_schedules, replace_file_content, wp_open_worktree,
  wp_close_worktree, find_by_name, grep_search, wp_list_workers,
  wp_edit_notebook, view_file, wp_submit_report, wp_set_wakeup,
  wp_send_message, wp_cancel_task, read_url_content, wp_run_workflow,
  write_to_file`. Every source name is absent upstream. The same 20-entry set
  repeats across the retry attempts, so the rewrite is stable per request.
- Codex code mode: ingress `exec, wait, request_user_input,
  request_user_input_async, clock__sleep, web_search` plus eight
  `mcp__fastctx__*` declarations → upstream `run_command, wp_wait, ask_question,
  wp_request_user_input_async, wp_clock_sleep, search_web` plus eight
  `wp_ext_<hash>` fallbacks; upstream response carried
  `wp_ext_3d1c4299180f8340de28e92e17bd9290` and the client-facing response
  carried the original `mcp__fastctx__run`.
- Codex shell mode: `exec_command → run_command`, `apply_patch → wp_apply_patch`,
  `write_stdin → wp_write_stdin`, `view_image → wp_view_image`,
  `clock__sleep → wp_clock_sleep`, plus reference and goal tools receiving
  `wp_ext_<hash>`; the streamed `run_command` was restored to `exec_command`.
  The shell-mode trace executed `printf CODEX_SHELL_OK` and continued, and the
  code-mode trace executed through `mcp__fastctx__run` and reported
  `CODEX_CODE_OK`.

### Protocol checks (local, no upstream dispatch required)

| Case | Result |
| :--- | :--- |
| Conflicting marker `X-Cloak-Client: oh_my_pi, claude_code` on an `agy/` route | PASS: HTTP 503 `{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}`, no upstream attempt |
| Declaration collision (`read` plus a natively declared `view_file`) | PASS: HTTP 503 `omp_cloak_required` |
| Explicit OMP marker on a non-`agy/` route | PASS: marker consumed, zero mutation — ingress `bash` equals upstream `bash` |

### Deferred / blocked in this pass

- Claude Code execution and continuation remain unattested: every attempt with
  the installed client on `agy/gemini-3.7-flash-high` returned 429
  `RESOURCE_EXHAUSTED` upstream, while the size probes above hit that same model
  successfully, so request size is ruled out. The remaining cause is unattested
  — per-route quota, concurrency and token cost were not separated. The
  criterion is therefore BLOCKED and its final verification is **deferred to a
  manual live session**.
- OpenAI Codex passed both declaration modes with no blocker observed; its final
  acceptance is nonetheless **deferred to manual repetition** by the operator.
- OMP `ask` is only reachable from an interactive session. The installed client
  registers the tool through `createIf`/`canPromptUser` and its `execute` throws
  "Ask tool requires interactive mode" when the session has no UI, so headless
  `-p` runs never declare it at all. Verified by driving the real TUI over a pty
  (`OMP-ESC-05`), which is the supported path for this criterion.
- OMP `web_search` needs a search provider: `providers.webSearchOrder` was `[]`
  in the acceptance profile. It was enabled reversibly for one case (`- exa`,
  key already present in the environment) and reverted afterwards.
- OMP escaped canonical spellings are only produced on the `anthropic-messages`
  wire: the client applies its builtin underscore escape inside the Anthropic
  client module, so `--tools read` on `openai-completions` declares bare `read`.
  The escaped run above therefore pins `api: anthropic-messages` in the
  acceptance profile (reverted afterwards).

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
