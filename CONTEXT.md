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
- **MCP Pass-through & Virtual Devices**: Top-level `mcp__*` tools bypass cloaking in both directions. Oh My Pi normally mounts MCP tools as virtual devices (`xd://mcp__<server>_<tool>`) and reaches them through core `read`/`write`, which are already cloaked to `view_file`/`write_to_file`.

#### Cloak Mapping Tables

##### 1. Oh My Pi (`oh_my_pi` / `omp`) — Safe Mapping Set
| Native Tool | Antigravity Cloaked Name | Classification | Description |
| :--- | :--- | :--- | :--- |
| `read` | `view_file` | Transport Exception | Read files, directories, URLs, and `xd://` virtual devices |
| `write` | `write_to_file` | Transport Exception | Create/overwrite files and dispatch `xd://` devices |
| `edit` | `replace_file_content` | Semantic Alias | Line-anchored code patch (preserves OMP hashline wire format) |
| `bash` | `run_command` | Direct Semantic Alias | Execute persistent shell commands |
| `grep` | `grep_search` | Direct Semantic Alias | Regex file search |
| `glob` | `find_by_name` | Direct Semantic Alias | Match and glob files/directories by pattern |
| `task` | `invoke_subagent` | Direct Semantic Alias | Dispatch background subagents (supports batch schema) |
| `ask` | `ask_question` | Direct Semantic Alias | Interactive user prompt UI |
| `web_search` | `search_web` | Direct Semantic Alias | Web search (when tool is exposed) |

**Intentional Pass-Through Tools**:
The following tools are intentionally excluded from cloaking and pass through untouched:
- Standard tools: `todo`, `hub`, `eval`.
- Vibe Mode: `vibe_spawn`, `vibe_send`, `vibe_wait`, `vibe_kill`, `vibe_list`.
- Autoresearch Mode: `init_experiment`, `run_experiment`, `log_experiment`, `update_notes`.
- Autolearn & Memory: `learn`, `manage_skill` (OMP long-term memory & managed skill primitives; AGY has no equivalent).
- Semantic Search: `find` (Judge cascade semantic search; cannot map to `grep_search` to avoid declaration collision with `grep`).

These tools remain in the static OMP source identity inventory or pass through unmolested, avoiding ambiguous reverse mappings and schema collisions.
> **Virtual Devices (`xd://`)**: Auxiliary tools (`ast_grep`, `ast_edit`, `lsp`, `checkpoint`, `rewind`, `browser`, `retain`, `recall`, `reflect`, `memory_edit`, `security_scan`) and MCP servers (`xd://mcp__<server>_<tool>`) in `oh_my_pi` are dispatched through `read`/`write` to `xd://<target>`. Because `read`/`write` are cloaked automatically, these calls need no separate top-level MCP mapping.

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

Chat Completions, as delivered by opencodex's `openai-chat` adapter (verified 2026-09-12 from this plugin's own debug log of a live `cpa/agy` session). Namespaced children arrive flattened as `<namespace>__<child>`, matching opencodex's `namespacedToolName`, so they are keyed in that exact spelling; `splitToolNamespace` splits on `:` alone and deliberately does not resolve them.
- `exec` $\to$ `run_command`
- `web_search` $\to$ `search_web`
- `request_user_input` $\to$ `ask_question`
- `collaboration__spawn_agent` $\to$ `invoke_subagent`
- `collaboration__followup_task` $\to$ `manage_task`
- `collaboration__list_agents` $\to$ `manage_subagents`

Only names that occupy a tool-name position are listed. Helpers that exist solely as prose inside the `exec` description — `apply_patch`, `exec_command`, `write_stdin`, `view_image`, `tool_search`, the goal and MCP-resource tools — stay pass-through, because the reverse path restores a name only where it appears as a tool name. `exec` is the sole entry point and therefore owns `run_command`; a shell-mode session declares `exec_command` instead, which is absent because one target cannot carry two sources.

Only `tools[]` entries are renameable, and which names a session declares depends on the model's tool mode:

| `tool_mode` | wire surface | table coverage |
| :--- | :--- | :--- |
| `code_mode_only` — the default for every routed provider here | one freeform `exec`, plus `wait`, `request_user_input*`, `clock__sleep`, the `collaboration__*` children and `web_search` as their own `tools[]` entries | the table above; the prose-only helpers stay pass-through |
| unset, i.e. shell mode (`gpt-5.5` / `5.4` / `5.4-mini`) | `exec_command`, `write_stdin`, `apply_patch`, `view_image` as their own `tools[]` entries | `exec` owns `run_command`, so `exec_command` has no mapping here; `write_stdin` and `apply_patch` pass through |

Intentional pass-through: `wait`, `request_user_input_async`, `clock__sleep`, `collaboration__wait_agent`, `collaboration__interrupt_agent`, `collaboration__send_message` (AGY's bare `send_message` is generic, so the reverse is left out of it), plus the prose-only helpers listed above.

`shell_command` is a `shell_type` catalog label, not a tool name, and `update_plan` has left the catalog entirely. The rest — `apply_patch`, `view_image`, `tool_search`, the goal tools and the MCP-resource tools — are mode-dependent: `tools[]` entries in shell mode, description text in code mode. The per-mode carriers are tabulated in **[the Codex surface reference](docs/research/codex-tool-surface-2026-09-12.md)**, together with the rationale for every name this table maps and every one it passes through.


### 3. Activation Model & Explicit OMP Routing
Every interceptor evaluates explicit client routing and model gating:

#### 1. Explicit OMP Routing Precedence (Issue #25, #27, #28)
When an explicit OMP marker (`X-Cloak-Client: oh_my_pi` / `omp` / `oh-my-pi`) is present:
- **ProtectedAGY (`agy/*` routes)**: Takes precedence over generic `model_prefixes`. The request must pass strict single-document JSON admission, declaration collision validation (comparing final base identities across namespaces), canonical Safe Mapping Set validation, and request-scoped active reverse derivation. Any failure terminates immediately with an exact HTTP 503 JSON rejection (`{"error":{"code":"omp_cloak_required","message":"Protected OMP request could not be safely cloaked."}}`) before upstream execution.
- **ExplicitOMPNonAGYBypass (non-`agy/` routes)**: Marker is consumed and stripped; a durable bypass sentinel is pinned under host `RequestID`. Request, response, and stream operations perform zero tool or brand mutation. Correlated handling never falls back to weaker evidence (UA, body, live config, or static target detection).
- **Marker Conflict Resolution**: Multiple/comma-separated marker values are collected and normalized. Duplicate/alias equivalents (`omp, oh-my-pi`) collapse to one identity. A conflict containing OMP on `agy/*` yields exact 503; on non-`agy/` it conservatively pins bypass; conflicts without OMP retain invalid-explicit negative resolution.

#### 2. Generic Model Gate (`modelAllowsCloak`)
For requests without an explicit OMP marker, `model_prefixes` restricts cloaking to matching model prefixes. Empty `model_prefixes` matches all models.

#### 3. Client Gate Precedence
Client identity is resolved in order:
1. Valid explicit `X-Cloak-Client` control header (consumed, never forwarded upstream).
2. Verified positive User-Agent evidence (`omp/` prefix, requiring a usable active ToolMappings entry).
3. Body-based tool-name classification via static source identity inventory.
An invalid explicit value records an authoritative negative resolution (`negativeClientResolution`) preventing weaker fallback.

#### 4. Identity Inventories & Target Corroboration Rule (Issue #26)
- **OMP Source Identity Inventory**: Static finite set comprising the 9 Safe Mapping Set sources plus legacy detection names (`todo`, `hub`, `eval`, `vibe_*`, Autoresearch names). Does not depend on runtime `ToolMappings`.
- **OMP Cloaked-Target Inventory**: Exactly the 9 canonical AGY targets (`view_file`, `write_to_file`, `replace_file_content`, `run_command`, `grep_search`, `find_by_name`, `invoke_subagent`, `ask_question`, `search_web`).
- **Corroboration-Only Rule**: Because canonical targets are native Antigravity tools, target names alone are never standalone OMP attribution. Target-only no-marker traffic cannot resolve to OMP.

#### 5. Request-Scoped Active Reverse Authority
Correlated Protected responses and streams reverse only canonical pairs whose source tool was actually declared and transformed in that request. Unused canonical targets remain pass-through and are never reverse-cloaked.

#### 6. Request Lifecycle Management
Explicit OMP route state is lifecycle-owned and registered with CLIProxyAPI (`request_lifecycle_plugin: true`). State is cleaned up idempotently on `request.complete` (`succeeded`, `failed`, `rejected`, `canceled`). Disposable stream sessions are cleaned on `[DONE]`, and pre-payload sessions can be deterministically rehydrated from pinned route state without consulting live config or weaker evidence.

#### 7. Terminal Canonical Brand Policy
Protected OMP aliases (`Oh My Pi`, `oh-my-pi`, `omp`) mask to `Antigravity` even with `use_default_keywords: false`. The output `Antigravity` is terminal and cannot be reprocessed or redirected by custom operator mappings. Literal `.omp` filesystem path segments (`.omp/foo`, `C:\Users\...\.omp\agent`) are strictly preserved. Assistant text restores `Antigravity -> omp` using pinned route authority.
### 4. Stream Session Management (`StreamSessionManager`)
- **Schema-Aware Caching**: In CLIProxyAPI schema_version >= 3, request bodies (`OriginalRequest`/`RequestBody`) are delivered only on the header-init chunk (`ChunkIndex == StreamChunkHeaderInitIndex`). The manager caches the uncloak regex pattern under the stream's correlation key - `RequestID`, a metadata/header id, or (schema < 3, where every chunk repeats the request body) an FNV hash of that body.
- **Uncorrelated Chunks**: Payload chunks carrying no correlation key cannot be attributed to any stream and pass through unmolested rather than compete for shared state (which would corrupt concurrent streams).
- **SSE Event Reassembly**: Buffers incomplete TCP fragments (`\n\n` boundaries) and uncloaks complete SSE events without cross-stream pollution.
- **Lifecycle & Cleanup**: Frees sessions on `data: [DONE]` (SSE) and at true standalone stream completion (`message_stop`, bare `[DONE]`, or non-null `finish_reason`s covering every expected choice lane — the request's OpenAI `n`), flushing held reverse-brand carries into the terminal event before deletion so buffered text is never lost; a partial `finish_reason` flushes only its own lane and leaves the session alive for the remaining choices; abandoned/interrupted streams are pruned opportunistically on chunk arrival.

### 5. Configuration Lifecycle
Managed via `atomic.Pointer[filterConfig]`, enabling lock-free, zero-copy configuration reads on hot request and streaming paths with thread-safe live reconfiguration.

### 6. Upstream Safety Sanitization & Envelope Conventions (Google Cloud Code / Antigravity)
- **System Conventions Sanitization**: A compatibility transformation of OMP's exact `<system-conventions>` wrapper to `<conventions>`, retaining the enclosed instructions. Its scope is system/developer prompt text on ProtectedAGY requests; it does not alter tool data or explicit non-AGY bypass traffic. See [ADR 0003](docs/adr/0003-sanitize-system-prompt-conventions-tag-for-google-upstream.md).
- **Request Type Envelope Context**: OMP upstream omitted `requestType: "agent"` to prevent requests from being routed into Google's constrained agent quota bucket on consumer/free-tier accounts. CPA injects `requestType: "agent"` at the executor layer; on Antigravity Pro/paid tier accounts, this agent bucket is unconstrained and behaves normally without patching.
## Key Files & Directories

- `main.go`: Complete plugin implementation (lifecycle hooks, interceptors, brand rewriter, stream manager, configuration store).
- `AGENTS.md`: Operational guide for building, testing, deploying, and debugging the plugin.
- `docs/specs/`: Detailed technical specifications (e.g. `oh-my-pi-cloaking-spec.md`, `system-conventions-sanitization.md`).
- `docs/adr/`: Architecture Decision Records.
