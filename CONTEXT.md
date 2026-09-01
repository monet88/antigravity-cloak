# Antigravity Cloak — Domain Context

## Overview

`antigravity-cloak` is a dynamic Go C-shared plugin (`buildmode=c-shared`) for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) v7 (ABI version 1). Its purpose is to disguise traffic from AI coding CLI assistants (Claude Code, OpenAI Codex, Oh My Pi) as Antigravity when communicating with upstream LLM providers.

## Core Concepts & Glossary

### 1. Brand Rewriting
Replaces client-identifying keywords (e.g. `OpenCode`, `Codex`, `Claude Code`, `Oh My Pi`) with `Antigravity` in the request's `system` field and `system`-role messages.
- **Default Keywords**: Preconfigured built-in rewrite mappings for known coding assistants.
- **Custom Mappings**: User-defined rewrite mappings (`custom_mappings`) that can add new keywords or override built-in defaults.
- **Reverse Restoration (`oh_my_pi` only)**: On the response and stream paths, assistant-visible `Antigravity` mentions are restored to `omp` (Issue #18). Scope is strictly assistant text: non-streaming `message.content` / Anthropic `text` blocks, and streaming deltas on indexed semantic lanes. Fragmented matches are held in a per-lane carry and flushed at stream completion (`data: [DONE]`, `message_stop`, bare `[DONE]`, or — on the standalone path — `finish_reason`s covering every expected choice lane; a `finish_reason` flushes only its own lane) in deterministic lane-index order; unmatched buffered text is delivered verbatim, never dropped. Tool arguments, reasoning/control/data parts, and non-assistant roles keep literal `Antigravity`.

### 2. Tool Cloaking & Uncloaking
- **Cloaking (Request Path)**: Translates client-native tool names (e.g., `Bash`, `read`, `shell_command`) into Antigravity-native tool names (e.g., `run_command`, `view_file`) before the request reaches the upstream LLM.
- **Uncloaking (Response & Stream Path)**: Reverses the translation in upstream responses (JSON bodies and SSE stream chunks) back to the client's native tool names so the client remains unaware of the disguise.
- **MCP Pass-through**: MCP tools (`mcp__*`) bypass cloaking in both directions.

#### Cloak Mapping Tables

##### 1. Oh My Pi (`oh_my_pi` / `omp`)
| Native Tool | Antigravity Cloaked Name | Mode / Category | Description |
| :--- | :--- | :--- | :--- |
| `read` | `view_file` | Standard Core | Read files, directories, and web URLs |
| `write` | `write_to_file` | Standard Core | Create or overwrite files |
| `edit` | `replace_file_content` | Standard Core | Line-anchored code patch |
| `bash` | `run_command` | Standard Core | Execute persistent shell commands |
| `grep` | `grep_search` | Standard Core | Regex file search |
| `glob` | `list_dir` | Standard Core | Match and glob files/directories |
| `task` | `invoke_subagent` | Standard Core | Dispatch background subagents |
| `ask` | `ask_question` | Standard Core | Interactive user prompt UI |
| `todo` | `manage_task` | Standard Core | Manage task checklist state |
| `hub` | `send_message` | Standard Core | Peer-to-peer messaging and job control |
| `web_search` | `search_web` | Standard Core | Web search |
| `eval` | `execute_code` | Standard Core | Run code in persistent kernel |
| `vibe_spawn` | `define_subagent` | Vibe Mode | Starts persistent worker session |
| `vibe_send` | `schedule` | Vibe Mode | Message / steer worker session |
| `vibe_wait` | `wait` | Vibe Mode | Block until worker turn completes |
| `vibe_kill` | `cancel` | Vibe Mode | Terminate worker session |
| `vibe_list` | `list` | Vibe Mode | List worker sessions and roster |
| `init_experiment` | `create_goal` | Autoresearch | Initialize benchmark experiment session |
| `run_experiment` | `call_mcp_tool` | Autoresearch | Run benchmark workload |
| `log_experiment` | `update_plan` | Autoresearch | Record metric, commit or discard |
| `update_notes` | `update_goal` | Autoresearch | Update experiment playbook / ideas |

> **Virtual Devices (`xd://`)**: Other auxiliary tools in `oh-my-pi` (`ast_grep`, `ast_edit`, `lsp`, `checkpoint`, `rewind`, `browser`, `retain`, `recall`, `reflect`, `memory_edit`, `security_scan`) are unmounted from top-level tool definitions and dispatched as virtual file payloads via `read`/`write` to `xd://<tool>`, thus automatically protected without needing top-level mappings.

##### 2. Claude Code (`claude_code`)
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

##### 3. OpenAI Codex (`codex`)
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


### 3. Activation Model (Two-Stage Gating)
Every interceptor evaluates two sequential gates:
1. **Model Gate (`modelAllowsCloak`)**: Evaluates `model_prefixes` against `Model` and `RequestedModel`. If empty, all models pass. If configured, non-matching models skip all cloaking/body mutation. Transport sanitation is independent of the gate (Spec #15): `handleRequestInterceptBefore` consumes/clears the plugin-owned `X-Cloak-Client` header before the gate runs, so a gate-skipped request still never leaks the control header upstream.
2. **Client Gate**: Resolves the client identity by precedence (Issue #16, #17): a valid explicit `X-Cloak-Client` control header (consumed, never forwarded upstream) > verified positive User-Agent evidence (`omp/` prefix, gated on a usable active ToolMappings entry) > body-based tool-name classification. An invalid explicit value bypasses UA evidence and falls directly to body detection, so a weaker signal cannot mask operator misconfiguration. Once identified, cloaking proceeds. When the invalid-explicit path also classifies no client from the body, the request interceptor records an authoritative negative resolution (`negativeClientResolution` sentinel session, Issue #20): response and stream paths treat its presence as request-time truth and must not re-infer a client from surviving User-Agent or body evidence. Conservative UA/body recovery remains available only when correlation is genuinely missing.

#### Client Classification Semantics
- **Original-name detection (`detectClient`)** keys off source tool names. Clients whose source names are mostly common words (`read`, `bash`) require either a distinctive harness tool (`hub`, `task`, `todo`, `eval`, `web_search`, `vibe_*`, `*_experiment`) or at least `minCollidingToolMatches` (4) simultaneous matches.
- **Cloaked-target detection (`detectCloakedClient`)** runs against the already-cloaked observed names. Namespace prefixes (`functions:view_file`, `default_api:bash`) are normalised away first, so each declared tool contributes exactly one observed base identity and the denominator is never inflated by an alias. A candidate qualifies only when it has at least 3 hits **and** covers at least 80% of the observed unique base identities (`hits*5 >= observed*4`). The static table length is not an alternative qualification path.
- **Ties & native pass-through**: multiple candidates are ranked by exact integer ratio over observed identities, then by hit count, then by client id. When 2+ distinct tables each reach full-table coverage (`hits == tableSize`), the traffic is treated as a native Antigravity superset and cloaking is skipped.
- **Namespace safety**: a namespaced reference whose prefix is itself a source tool name (`read:write`) is an access-mode / compound token, not a tool reference, and is left untouched.

These semantics are recorded in [ADR 0002](docs/adr/0002-ratio-ranked-client-classification-and-session-pre-registration.md).

### 4. Stream Session Management (`StreamSessionManager`)
- **Schema-Aware Caching**: In CLIProxyAPI schema_version >= 3, request bodies (`OriginalRequest`/`RequestBody`) are delivered only on the header-init chunk (`ChunkIndex == StreamChunkHeaderInitIndex`). The manager caches the uncloak regex pattern under the stream's correlation key - `RequestID`, a metadata/header id, or (schema < 3, where every chunk repeats the request body) an FNV hash of that body.
- **Uncorrelated Chunks**: Payload chunks carrying no correlation key cannot be attributed to any stream and pass through unmolested rather than compete for shared state (which would corrupt concurrent streams).
- **SSE Event Reassembly**: Buffers incomplete TCP fragments (`\n\n` boundaries) and uncloaks complete SSE events without cross-stream pollution.
- **Lifecycle & Cleanup**: Frees sessions on `data: [DONE]` (SSE) and at true standalone stream completion (`message_stop`, bare `[DONE]`, or non-null `finish_reason`s covering every expected choice lane — the request's OpenAI `n`), flushing held reverse-brand carries into the terminal event before deletion so buffered text is never lost; a partial `finish_reason` flushes only its own lane and leaves the session alive for the remaining choices; abandoned/interrupted streams are pruned opportunistically on chunk arrival.

### 5. Configuration Lifecycle
Managed via `atomic.Pointer[filterConfig]`, enabling lock-free, zero-copy configuration reads on hot request and streaming paths with thread-safe live reconfiguration.

## Key Files & Directories

- `main.go`: Complete plugin implementation (lifecycle hooks, interceptors, brand rewriter, stream manager, configuration store).
- `AGENTS.md`: Operational guide for building, testing, deploying, and debugging the plugin.
- `docs/specs/`: Detailed technical specifications (e.g. `oh-my-pi-cloaking-spec.md`).
- `docs/adr/`: Architecture Decision Records.
