# Antigravity Cloak

CLIProxyAPI v7 dynamic plugin (`buildmode=c-shared`, ABI v1) for disguising coding-CLI traffic (**Claude Code**, **OpenAI Codex**, **Oh My Pi**) as Antigravity: client-scoped brand rewriting plus bidirectional tool-name cloaking with structured uncloaking, gated by an optional model-prefix allowlist.

## Features

- **Client-Scoped Brand Rewriting**: Replaces the resolved client's identity tokens and context paths (`.claude/`, `.codex/`, `.omp/`, `CLAUDE.md`) with Antigravity equivalents (`.gemini/`, `GEMINI.md`) on request, and restores them in responses and SSE streams. Unaffiliated competitor names are left untouched so reverse restoration stays unambiguous.
- **Bidirectional Tool Cloaking**: Maps client-native tool declarations to Antigravity targets on request and restores original names in JSON responses and SSE streams.
- **SSE Stream Reassembly Buffer**: Event-level buffer across `\n\n` boundaries uncloaks tool names and brand tokens split across network packets.
- **Model-Prefix Gate (`model_prefixes`)**: Restricts cloaking to matching upstream/requested model prefixes (e.g. `agy/`); non-matching models pass through untouched.
- **Request-Scoped Alias Plans**: Tools outside canonical sets map to shared aliases (`wp_*`) or deterministic hash fallbacks (`wp_ext_<hash>`), reversed per request via `RequestID`. Collisions or missing/duplicate `RequestID` fail closed with HTTP 503 (`tool_cloak_required` / `omp_cloak_required`).

## Supported Coding Clients

- **Oh My Pi (`oh_my_pi` / `omp`)**:
  - **Safe Mapping Set (9 canonical)**: `read -> view_file`, `write -> write_to_file`, `edit -> replace_file_content`, `bash -> run_command`, `grep -> grep_search`, `glob -> find_by_name`, `task -> invoke_subagent`, `ask -> ask_question`, `web_search -> search_web`.
  - **Shared Aliases (`wp_*`)**: `todo`, `hub`, `eval`, `goal`, `yield`, `wait`, `vibe_*`, autoresearch (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`), `learn`, `manage_skill`, `find`. Unknown/dynamic tools -> `wp_ext_<hash>`. Virtual devices (`xd://`) route through `read`/`write`.
  - **Routing**: Explicit OMP on `agy/*` enforces ProtectedAGY validation (HTTP 503 `omp_cloak_required` on failure); non-`agy/` routes bypass cloaking durably with zero mutation.
- **Claude Code (`claude_code`)**:
  - **Core Tools**: `Bash -> run_command`, `Edit -> replace_file_content`, `Read -> view_file`, `Write -> write_to_file`, `Grep -> grep_search`, `Glob -> find_by_name`, `Agent -> invoke_subagent`, `AskUserQuestion -> ask_question`, `WebSearch -> search_web`, `WebFetch -> read_url_content`.
  - **Shared Aliases (`wp_*`)**: Subagent (`ListAgents -> wp_list_workers`, `TaskStop -> wp_cancel_task`, `SendMessage -> wp_send_message`), workflow/planning (`ToolSearch -> wp_find_tools`, `Skill -> wp_invoke_skill`, `Workflow -> wp_run_workflow`, `NotebookEdit -> wp_edit_notebook`, `ReportFindings -> wp_submit_report`, `EnterPlanMode`/`ExitPlanMode -> wp_begin_planning`/`wp_finish_planning`, `EnterWorktree`/`ExitWorktree -> wp_open_worktree`/`wp_close_worktree`, `ScheduleWakeup -> wp_set_wakeup`, `CronCreate`/`CronDelete`/`CronList -> wp_create_schedule`/`wp_delete_schedule`/`wp_list_schedules`), MCP resources (`ListMcpResourcesTool -> wp_list_resources`, `ReadMcpResourceTool -> wp_read_resource`, `ReadMcpResourceDirTool -> wp_list_resource_dir`, `WaitForMcpServers -> wp_wait_integrations`), deferred (`DeferredToolPlaceholder -> wp_resolve_tool`). Unknown/`mcp__*` -> `wp_ext_<hash>`.
- **OpenAI Codex (`codex`)**:
  - **Code Mode**: `exec -> run_command`, `web_search -> search_web`, `request_user_input -> ask_question`, `collaboration__spawn_agent -> invoke_subagent`.
  - **Shell Mode**: `exec_command -> run_command`, `apply_patch -> wp_apply_patch`, `write_stdin -> wp_write_stdin`, `view_image -> wp_view_image`.
  - **Shared Aliases (`wp_*`)**: `wait`, `clock__sleep`, `request_user_input_async`, `collaboration__wait_agent`, `collaboration__interrupt_agent`, `collaboration__send_message`, `collaboration__followup_task`, `collaboration__list_agents`. Unknown/`mcp__*` -> `wp_ext_<hash>`.

> Full domain glossary and mapping tables: **[CONTEXT.md](CONTEXT.md)**.

## Configuration

Send `X-Cloak-Client` with `claude_code`, `codex`, or `oh_my_pi` on requests to CLIProxyAPI for deterministic client identity. See [client header setup](docs/client-identity-headers.md) for Claude Code settings, Codex provider headers, and the opencodex proxy hop.

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

## Verification & Docs

- **[Live acceptance record (2026-09-27)](docs/verification-checklist.md#live-acceptance-record---2026-09-27)**: pinned source revision, candidate artifact SHA256 and per-client results. Oh My Pi passed every criterion its installed client exposes (7/9 canonical tools bare; `ask`, `web_search` and the transport/alias variants over the escaped wire); Claude Code and OpenAI Codex are recorded as manual verification pending/deferred, so this is not a release claim.
- **[Deployment and live verification runbook](docs/verification-checklist.md)**: clean build, install, controlled debug capture, correlation and cleanup.
- **[OMP full tool-cloak checklist](docs/verification-checklist-omp.md)**: nine canonical mappings, transport variants and extended alias tools, with the current per-case verdicts.
- **[Client identity headers](docs/client-identity-headers.md)**: configuring `X-Cloak-Client` for Claude Code, Codex (direct and via opencodex) and Oh My Pi.
- **[Domain context](CONTEXT.md)**: glossary, per-client mapping tables and the activation/lifecycle contract.
- **[Architecture decisions](docs/adr/)**: stream sessions, client classification, upstream sanitization, and Codex wire-position cloaking.
- **[Client surface references](docs/research/)**: dated Antigravity, Codex and Claude Code tool surfaces.
- **[Feature spec](docs/specs/oh-my-pi-cloaking-spec.md)**: technical design, user stories, and architecture decisions.

## Tests

```powershell
go test -v ./...
go test -v ./.github/scripts
go vet ./...
```
