# Antigravity Coding Filter

CLIProxyAPI v7 dynamic plugin for protecting the Antigravity route from non-Antigravity coding software traffic.

## Detection Rules

The plugin blocks a request when its JSON body contains any of these signals:

- `system` contains one of: `OpenCode`, `Codex`, `Claude Code`
- any JSON object contains `prompt_cache_key`
- any JSON object contains `metadata.user_id`

Keyword matching is case-insensitive and only scans `system`. Mentions in user prompts, `messages`, or other fields do not block by themselves.

## Build

CLIProxyAPI dynamic plugins require CGO. Confirm `CGO_ENABLED=1` before building.

Windows amd64:

```powershell
go build -buildmode=c-shared -o plugins/windows/amd64/antigravity-coding-filter.dll .
Remove-Item plugins/windows/amd64/antigravity-coding-filter.h
```

The plugin ID is derived from the dynamic library filename, so this build path registers the plugin as `antigravity-coding-filter`.

## CLIProxyAPI Config

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-coding-filter:
      enabled: true
      priority: 1
```

CLIProxyAPI searches `plugins/<GOOS>/<GOARCH>-<variant>`, then `plugins/<GOOS>/<GOARCH>`, then `plugins`.

## Runtime Verification

After starting CLIProxyAPI, call:

```text
GET /v0/management/plugins
```

Confirm the plugin reports `registered: true` and `effective_enabled: true`.

## Tests

```powershell
go test ./...
```
