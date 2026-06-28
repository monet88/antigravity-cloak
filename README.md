# Antigravity Coding Filter

CLIProxyAPI v7 dynamic plugin for rewriting non-Antigravity coding software signals to Antigravity.

## Rewrite Rules

The plugin rewrites configured coding-client names (case-insensitive) in the following fields of request payloads:
- Inside any JSON field named `system` (or nested `system` fields).
- Inside `tools[].description` and `tools[].function.description` fields.
- Inside message history when the role is `system` (`messages[].content` where `role == "system"`).

The built-in mapping preset is enabled by default:

- `OpenCode` -> `Antigravity`
- `Codex` -> `Antigravity`
- `Claude Code` -> `Antigravity`

Mentions in user prompts, assistant messages, or other general text fields are NOT rewritten.

## Tool Name Cloaking & Response Uncloaking

The plugin automatically detects the target coding tool (e.g. Codex or Claude Code) based on the client signatures present in the request's tools array.

1. **Request Interception (`request.intercept_before`):** Maps client-specific tool names to standard Antigravity tool names (e.g. `bash` -> `run_command`).
2. **Response Interception (`response.intercept_after`):** Reverses the mapping (uncloaking) in non-streaming responses, targeting only tool-name fields (`choices[].message.tool_calls[].function.name` or `content[].name` where `type == "tool_use"`).
3. **Stream Chunk Interception (`response.intercept_stream_chunk`):** Parses server-sent event (SSE) `data:` lines and uncloaks standard tool names back to client-specific tool names in tool-name-bearing fields only.

To avoid false positives, response and stream chunk uncloaking use a structured JSON replacement approach instead of raw string matching on the raw response bytes. This ensures that prose texts containing tool names remain unaltered.

## Mapping Configuration

You can disable the built-in preset and provide your own mapping relationships in the plugin config:

```yaml
plugins:
  configs:
    antigravity-cloak:
      enabled: true
      priority: 1
      use_default_keywords: false
      custom_mappings:
        Cursor: Antigravity
        Windsurf: Antigravity
        JetBrains AI: Antigravity
```

`custom_mappings` also accepts a comma- or newline-delimited `from: to` string for simpler one-line config. Blank entries and duplicate source names are ignored.

### Tool Name Cloaking Configuration

You can also override or extend the default tool name mapping tables for specific coding clients via `tool_mappings`. Keys represent the client names (`claude_code` or `codex`), and values are mapping pairs of `original_tool_name: antigravity_target_name`:

```yaml
plugins:
  configs:
    antigravity-cloak:
      enabled: true
      priority: 1
      tool_mappings:
        claude_code:
          bash: run_command
          my_custom_tool: ask_permission
        codex:
          shell_command: run_command
```

## Build

CLIProxyAPI dynamic plugins require CGO. Confirm `CGO_ENABLED=1` before building.

Windows amd64:

```powershell
go build -buildmode=c-shared -o plugins/windows/amd64/antigravity-cloak.dll .
Remove-Item plugins/windows/amd64/antigravity-cloak.h
```

The plugin ID is derived from the dynamic library filename, so this build path registers the plugin as `antigravity-cloak`.

## CLIProxyAPI Config

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-cloak:
      enabled: true
      priority: 1
      use_default_keywords: true
      custom_mappings: {}
      tool_mappings: {}
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
