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
- **MCP Tools & Virtual Devices**: In Protected OMP and alias plans (Claude Code, Codex), dynamic top-level `mcp__*` declarations receive deterministic reversible fallback aliases (`wp_ext_<hash>`). Oh My Pi also mounts virtual devices (`xd://mcp__<server>_<tool>`) reached through core `read`/`write` (`view_file`/`write_to_file`).

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

**Extended Tools & Fallback Aliases**:
Tools beyond the canonical Safe Mapping Set are cloaked on Protected routes via shared aliases or deterministic fallbacks:
- Standard tools: `todo` $\to$ `wp_todo`, `hub` $\to$ `wp_hub`, `eval` $\to$ `wp_eval`.
- Vibe Mode: `vibe_spawn`, `vibe_send`, `vibe_wait`, `vibe_kill`, `vibe_list` $\to$ `wp_vibe_*`.
- Autoresearch Mode: `init_experiment`, `run_experiment`, `log_experiment`, `update_notes` $\to$ `wp_*`.
- Autolearn & Memory: `learn` $\to$ `wp_learn`, `manage_skill` $\to$ `wp_manage_skill`.
- Semantic Search: `find` $\to$ `wp_find`.
- Unknown or dynamic tools (including dynamic MCP declarations) receive deterministic reversible fallbacks (`wp_ext_<hash>`). Reverse uncloaking is request-scoped to active pairs.
> **Virtual Devices (`xd://`)**: Auxiliary tools (`ast_grep`, `ast_edit`, `lsp`, `checkpoint`, `rewind`, `browser`, `retain`, `recall`, `reflect`, `memory_edit`, `security_scan`) and MCP servers (`xd://mcp__<server>_<tool>`) in `oh_my_pi` are dispatched through `read`/`write` to `xd://<target>`. Because `read`/`write` are cloaked automatically, these calls need no separate top-level MCP mapping.

##### 2. Claude Code (`claude_code`)

**Status legend:** ✅ live and correct · 🔄 request-scoped shared alias (`wp_*`) · 🔀 deterministic fallback (`wp_ext_<hash>`).

| CC Tool | Antigravity Target | Status | Classification / Note |
| :--- | :--- | :--- | :--- |
| `Read` | `view_file` | ✅ | Direct semantic alias |
| `Write` | `write_to_file` | ✅ | Direct semantic alias |
| `Edit` | `replace_file_content` | ✅ | Direct semantic alias |
| `Bash` | `run_command` | ✅ | Direct semantic alias |
| `Grep` | `grep_search` | ✅ | Direct semantic alias |
| `Glob` | `find_by_name` | ✅ | Direct semantic alias (pattern-matching file finder) |
| `Agent` | `invoke_subagent` | ✅ | Direct semantic alias |
| `AskUserQuestion` | `ask_question` | ✅ | Direct semantic alias |
| `WebSearch` | `search_web` | ✅ | Direct semantic alias |
| `WebFetch` | `read_url_content` | ✅ | Direct semantic alias |

**Tier-2 Shared Aliases & Tier-3 Fallback Aliases** (parent #32 / issue #36):
Tools without a 1:1 AGY equivalent receive stable shared aliases (`wp_*`) via `claudeCodeSharedAliases`:
- Subagent control: `ListAgents` $\to$ `wp_list_workers`, `TaskStop` $\to$ `wp_cancel_task`, `SendMessage` $\to$ `wp_send_message`
- MCP resources: `ListMcpResourcesTool` $\to$ `wp_list_resources`, `ReadMcpResourceTool` $\to$ `wp_read_resource`, `ReadMcpResourceDirTool` $\to$ `wp_list_resource_dir`, `WaitForMcpServers` $\to$ `wp_wait_integrations`
- Extended/Planning/Workflow: `ToolSearch` $\to$ `wp_find_tools`, `Skill` $\to$ `wp_invoke_skill`, `Workflow` $\to$ `wp_run_workflow`, `NotebookEdit` $\to$ `wp_edit_notebook`, `ReportFindings` $\to$ `wp_submit_report`, `EnterPlanMode` / `ExitPlanMode` $\to$ `wp_begin_planning` / `wp_finish_planning`, `EnterWorktree` / `ExitWorktree` $\to$ `wp_open_worktree` / `wp_close_worktree`, `ScheduleWakeup` $\to$ `wp_set_wakeup`, `CronCreate` / `CronDelete` / `CronList` $\to$ `wp_create_schedule` / `wp_delete_schedule` / `wp_list_schedules`
- Deferred tool placeholders: `DeferredToolPlaceholder` $\to$ `wp_resolve_tool` (carried exclusively across normal protocol tool-identity positions: `tools[]`, message `tool_calls`/`tool_use`, and `tool_choice`; no unobserved special discovery carrier is claimed)
- Undeclared tools and dynamic `mcp__*` tools receive deterministic fallback aliases (`wp_ext_<hash>`).
- Request-scoped reversal (`requestsRequestScopedReverse`) narrows uncloaking to the active alias plan for that request, restoring exact declared source identities.

**Known remaining gaps** (historical baseline in [the Claude Code gap analysis](docs/research/claude-code-cloak-gap-analysis-2026-09-23.md)):
- No `ProtectedAGY` admission for `X-Cloak-Client: claude_code` on `agy/*` — the header resolves identity only, so no fail-closed 503 (fail-closed 503 applies to missing/duplicate RequestID or alias-plan collision).
- Brand restoration is one-directional: `Claude Code -> Antigravity` on the request path only; assistant-visible `Antigravity` is not restored for CC as `Antigravity -> omp` is for OMP.

⚠️ **Wire validation:** CC core mappings and alias plans are verified against recorded Anthropic message fixtures and protocol test suites.

##### 3. OpenAI Codex (`codex`)

Chat Completions, as delivered by opencodex's `openai-chat` adapter (verified 2026-09-12 from this plugin's own debug log of a live `cpa/agy` session). Namespaced children arrive flattened as `<namespace>__<child>`, matching opencodex's `namespacedToolName`, so they are keyed in that exact spelling; `splitToolNamespace` splits on `:` alone and deliberately does not resolve them.
- `exec` $\to$ `run_command` (code mode)
- `web_search` $\to$ `search_web`
- `request_user_input` $\to$ `ask_question`
- `collaboration__spawn_agent` $\to$ `invoke_subagent`

All declared tools in `tools[]`, history `tool_calls[]`, tool-role messages, and `tool_choice` are cloaked using request-scoped alias plans (issue #38, parent #32):
- Names with proven AGY role equivalents map to the AGY targets above.
- Tools without 1:1 AGY equivalents in code mode (`wait`, `request_user_input_async`, `clock__sleep`, `collaboration__wait_agent`, `collaboration__interrupt_agent`, `collaboration__send_message`, `collaboration__followup_task`, `collaboration__list_agents`) or shell mode (`exec_command`, `write_stdin`, `apply_patch`, `view_image`) map to stable shared aliases (`wp_*`) via `codexSharedAliases` (`exec_command` maps to `run_command`).
- Dynamic MCP declarations or undeclared tools receive deterministic fallback aliases (`wp_ext_<hash>`).
- Request-scoped reversal (`requestsRequestScopedReverse`) ensures exact restoration of declared names on response and stream paths without collision across modes.
- Client detection independence: generic shared tools (`wait`, `clock__sleep`) and neutral aliases (`wp_*`) are excluded from detection attribution inventory to prevent false-positive Codex attribution.

| `tool_mode` | wire surface | table coverage |
| :--- | :--- | :--- |
| `code_mode_only` — the default for every routed provider here | one freeform `exec`, plus `wait`, `request_user_input*`, `clock__sleep`, the `collaboration__*` children and `web_search` as their own `tools[]` entries | AGY role targets + `codexSharedAliases` (`wp_*`) + deterministic fallback aliases |
| unset, i.e. shell mode (`gpt-5.5` / `5.4` / `5.4-mini`) | `exec_command`, `write_stdin`, `apply_patch`, `view_image` as their own `tools[]` entries | `exec_command -> run_command` + `codexSharedAliases` (`wp_*`) + deterministic fallback aliases |

`shell_command` is a `shell_type` catalog label, not a tool name, and `update_plan` has left the catalog entirely. The rest — `apply_patch`, `view_image`, `tool_search`, the goal tools and the MCP-resource tools — are mode-dependent: `tools[]` entries in shell mode, description text in code mode. The per-mode carriers are tabulated in **[the Codex surface reference](docs/research/codex-tool-surface-2026-09-12.md)**.


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
Correlated responses and streams reverse only pairs whose source tool was actually declared and transformed in that specific request:
- For Protected OMP, only canonical pairs whose source declaration was transformed become active in the request-scoped reverse map; inactive canonical targets and native AGY targets are never reverse-cloaked.
- For Claude Code and OpenAI Codex, request-scoped reversal (`requestsRequestScopedReverse`) scopes the uncloak table to the active alias plan derived from the raw request body. Natively declared AGY targets or unused mode targets are never handed back as client source tools the client never declared.

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
