# Antigravity Cloak

CLIProxyAPI v7 dynamic plugin for disguising coding-CLI traffic (**Claude Code**, **OpenAI Codex**, **Oh My Pi**) as Antigravity: brand rewriting plus two-way tool-name cloaking with structured uncloaking, gated by an optional model-prefix allowlist.

---

## Features

- **Brand Signal Rewriting**: Automatically replaces 50+ coding software names, terminal agents, and IDE brands with `Antigravity` in `system` prompts, `system`-role messages, and `tools[].description`.
- **Bidirectional Tool Name Cloaking**: Maps client-native tool names (`read`, `bash`, `edit`, `task`, ...) to Antigravity standard tool names (`view_file`, `run_command`, `replace_file_content`, `invoke_subagent`, ...) on request, and seamlessly uncloaks them on non-streaming responses and real-time SSE stream chunks.
- **SSE Stream Reassembly Buffer**: Event-level buffer across `\n\n` boundaries ensures tool names split across network packets are uncloaked reliably.
- **Model-Prefix Gate (`model_prefixes`)**: Restricts cloaking only to specific upstream/requested models (e.g. `agy/`), leaving other models completely untouched.
- **MCP Tool Pass-Through**: Model Context Protocol (`mcp__*`) tools pass through untouched in both directions.

---

## Supported Coding Clients

`antigravity-cloak` seamlessly cloaks and uncloaks native tool definitions and stream chunks for:
- **Oh My Pi (`oh_my_pi` / `omp`)**: Standard core tools (`read`, `write`, `edit`, `bash`, `grep`, `task`, `ask`, `todo`, `hub`, `eval`, `web_search`), Vibe Mode (`vibe_spawn`, `vibe_send`, `vibe_wait`, `vibe_kill`, `vibe_list`), and Autoresearch Mode (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`).
- **Claude Code (`claude_code`)**: `Bash`, `Edit`, `Read`, `Write`, `Grep`, `Glob`, `Agent`, `AskUserQuestion`, `ToolSearch`, `Skill`, `Workflow`.
- **OpenAI Codex (`codex`)**: `shell_command`, `apply_patch`, `request_user_input`, `view_image`, `update_plan`, `tool_search`, `get_goal`, `create_goal`, `update_goal`, `list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource`.

> For complete domain glossary and full mapping tables across all clients and modes, refer to **[CONTEXT.md](CONTEXT.md)**.
---

## Built-in Keyword Preset (50+ Signals)

The built-in preset is enabled by default and covers major AI coding editors, assistants, terminal agents, and harnesses:
- **Major Assistants**: Claude Code, OpenAI Codex, OpenCode, GitHub Copilot, Gemini Code Assist / CLI, Oh My Pi (`Oh My Pi`, `oh-my-pi`, `omp`)
- **IDE & Editors**: Cursor, Windsurf, Codeium, Cline, Roo Code, Kilo Code, Aider, Continue.dev, Trae, Tabnine, Sourcegraph Cody, Augment Code, Zed AI, Void Editor, PearAI, Refact.ai, Tabby, GitLab Duo, Visual Studio IntelliCode
- **Enterprise & Cloud**: Amazon Q Developer, Amazon CodeWhisperer, JetBrains AI Assistant, JetBrains Junie, Kiro, Qoder, Qwen Code, Replit Agent, Replit Ghostwriter, CodeBuddy, Blackbox AI, Pieces, Qodo, CodiumAI, Rovo Dev CLI, Factory Droid
- **Autonomous Agents**: Devin, OpenHands, SWE-agent, Goose, OpenClaw, Clawdbot, Moltbot, Hermes Agent, WorkBuddy

---

## Configuration

In CLIProxyAPI `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-cloak:
      enabled: true
      priority: 1
      use_default_keywords: true
      # Restrict cloaking to models starting with these prefixes (empty = all models)
      model_prefixes:
        - "agy/"
        - "antigravity/"
      # Custom brand rewrite mappings
      custom_mappings:
        MyInternalBot: Antigravity
      # Custom tool mappings override per client
      tool_mappings:
        oh_my_pi:
          custom_tool: run_command
        claude_code:
          CustomTool: call_mcp_tool
```

---

## Build

CLIProxyAPI dynamic plugins require CGO (`CGO_ENABLED=1`).

### Linux amd64 (via Docker)
```powershell
docker run --rm -v ${PWD}:/src -w /src golang:1.26 sh -c "mkdir -p dist && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -ldflags '-s -w' -o dist/antigravity-cloak.so . && rm -f dist/antigravity-cloak.h"
```

### Windows amd64
```powershell
go build -buildmode=c-shared -o plugins/windows/amd64/antigravity-cloak.dll .
Remove-Item plugins/windows/amd64/antigravity-cloak.h
```

---

## Verification & Docs

- **[Two-Way Verification Checklist](docs/verification-checklist.md)**: End-to-end verification checklist and trigger commands across all 12 core tools and debug log validation.
- **[Feature Spec](docs/specs/oh-my-pi-cloaking-spec.md)**: Technical design, 30 user stories, and architecture decisions.

---

## Tests

```powershell
go test -v ./...
go test -v ./.github/scripts
go vet ./...
```
