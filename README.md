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

### 1. Oh My Pi (`oh_my_pi` / `omp`)
| Native Tool | Antigravity Cloaked Name | Description |
| :--- | :--- | :--- |
| `read` | `view_file` | Read files, directories, and web URLs |
| `write` | `write_to_file` | Create or overwrite files |
| `edit` | `replace_file_content` | Line-anchored code patch |
| `bash` | `run_command` | Execute persistent shell commands |
| `grep` | `grep_search` | Regex file search |
| `glob` | `list_dir` | Match and glob files/directories |
| `task` | `invoke_subagent` | Dispatch background subagents |
| `ask` | `ask_question` | Interactive user prompt UI |
| `todo` | `manage_task` | Manage task checklist state |
| `hub` | `send_message` | Peer-to-peer messaging and job control |
| `web_search` | `search_web` | Web search |
| `eval` | `execute_code` | Run code in persistent kernel |

### 2. Claude Code (`claude_code`)
- `Bash` $\to$ `run_command`
- `Edit` $\to$ `replace_file_content`
- `Read` $\to$ `view_file`
- `Write` $\to$ `write_to_file`
- `Grep` $\to$ `grep_search`
- `Glob` $\to$ `list_dir`
- `Agent` $\to$ `invoke_subagent`
- `AskUserQuestion` $\to$ `ask_question`
- `ToolSearch` $\to$ `search_web`
- `Skill` $\to$ `call_mcp_tool`
- `Workflow` $\to$ `schedule`

### 3. OpenAI Codex (`codex`)
- `shell_command` $\to$ `run_command`
- `apply_patch` $\to$ `multi_replace_file_content`
- `request_user_input` $\to$ `ask_question`
- `view_image` $\to$ `generate_image`
- `update_plan` $\to$ `manage_task`
- `tool_search` $\to$ `search_web`
- `get_goal` $\to$ `schedule`
- `create_goal` $\to$ `send_message`
- `update_goal` $\to$ `define_subagent`
- `list_mcp_resources` $\to$ `list_resources`
- `list_mcp_resource_templates` $\to$ `list_permissions`
- `read_mcp_resource` $\to$ `read_resource`

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
