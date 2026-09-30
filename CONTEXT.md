# Antigravity Cloak — Domain Context

## Overview

`antigravity-cloak` is a dynamic Go C-shared plugin (`buildmode=c-shared`) for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) v7 (ABI version 1). Its purpose is to disguise traffic from AI coding CLI assistants (Claude Code, OpenAI Codex, Oh My Pi) as Antigravity when communicating with upstream LLM providers.

## Core Concepts & Glossary

### 1. Brand Rewriting
Replaces the resolved client's own identity keywords with `Antigravity` in the request's `system` field and `system`-role messages.
- **One table per client**: The built-in forward and reverse tables are declared per client (`claudeCodeBrandMappings`, `codexBrandMappings`, `ompBrandMappings`, and the matching `*ReverseBrandMappings`), registered in `brandMappingsByClient` / `reverseBrandMappingsByClient`. The resolved client selects exactly one table. This is what makes the reverse unambiguous: `Antigravity` is produced only by the owning client, so it inverts back to that client's own word (`Claude`, `Codex`, or — through the protected lane — `omp`) and to nothing else. Adding a client is one table plus one registry line.
- **Competitor names are not rewritten**: A product name that belongs to no supported client (`Cursor`, `Devin`, `OpenCode`, ...) has no owning table and is left in place. Over-masking them was what made the bare `Antigravity` token invertible only by guessing.
- **Default Keywords**: Preconfigured built-in rewrite mappings for known coding assistants.
- **Custom Mappings**: User-defined rewrite mappings (`custom_mappings`) that can add new keywords or override built-in defaults.
- **Reverse Restoration (per client)**: On the response and stream paths, assistant-visible cloaked tokens are restored from the resolved client's own reverse table. For `claude_code` that is the path-qualified instruction file, the official domain (`antigravity.google` -> `claude.ai`), the vendor phrase, the skill slug, the SDK name, and the bare `Antigravity`; for `codex` its own home paths and the bare `Antigravity` -> `Codex`; for `oh_my_pi` the protected pair `Antigravity` -> `omp` plus the whole home directory.
- **Context paths are whole-segment, both directions**: a client home directory is rewritten as a whole segment at any position (`.claude/`, `.claude\`, `.codex/`, `.codex\`), never as a bare word, and the file name is rewritten with it for `claude_code` (`CLAUDE.md` -> `GEMINI.md`) but not for `codex`, whose `AGENTS.md` is already neutral. The bare `CLAUDE.md` -> `AGENTS.md` brand rule belongs to `claude_code` alone: that table also carries a bare `Claude` rule, which would otherwise consume the vendor prefix inside the file name and yield `Antigravity.md`, which the reverse can only restore as `Claude.md`, corrupting the case. `codex` and `oh_my_pi` have no bare-Claude rule, so for them the file name is left verbatim. Both halves of a group come from one `pathRules` call with the pairs swapped, and `TestContextGroupsAreExactInverses` holds them together. **The match is deliberately not anchored to a `~/` or `./` spelling**: live acceptance measured 114 absolute `C:\Users\<name>\.claude\...` paths per request reaching the model uncloaked, because Claude Code on Windows uses absolute paths in its system context and never the tilde form. The cost is that a `.claude` belonging to a different project is rewritten too - a path the plugin cannot distinguish. The trade is deliberate: that costs one path the model may wander into, the narrow form cost 114 on every request.

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

**Status legend:** `DECL` = observed and mapped in the 2026-09-27 live Claude Code request; downstream restoration/execution/continuation still pending · `TEST` = mapping covered by fixtures/protocol tests but not observed in that live declaration set. Tier-2 shared aliases (`wp_*`) and Tier-3 fallback aliases (`wp_ext_<hash>`) are listed below rather than as table statuses.

| CC Tool | Antigravity Target | Status | Classification / Note |
| :--- | :--- | :--- | :--- |
| `Read` | `view_file` | `DECL` | Direct semantic alias |
| `Write` | `write_to_file` | `DECL` | Direct semantic alias |
| `Edit` | `replace_file_content` | `DECL` | Direct semantic alias |
| `Bash` | `run_command` | `DECL` | Direct semantic alias |
| `Grep` | `grep_search` | `DECL` | Direct semantic alias |
| `Glob` | `find_by_name` | `DECL` | Direct semantic alias (pattern-matching file finder) |
| `Agent` | `invoke_subagent` | `DECL` | Direct semantic alias |
| `AskUserQuestion` | `ask_question` | `TEST` | Direct semantic alias; not present in the 20-tool live declaration set |
| `WebSearch` | `search_web` | `TEST` | Direct semantic alias; not present in the 20-tool live declaration set |
| `WebFetch` | `read_url_content` | `DECL` | Direct semantic alias |

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
- Claude Code brand restoration (its reverse table, §1) is unattested against real traffic: the 2026-09-27 completion attempt was blocked upstream by HTTP 429, so it is covered only by the offline harness and by synthetic probes.

**Wire validation:** Claude Code `2.1.283` live request declarations verified all 20 observed mappings with zero source-name leakage upstream. Downstream restoration, native execution, and continuation remain unattested because the live completion attempts returned upstream HTTP 429; fixtures and protocol tests cover those mechanics until the deferred manual live session is completed.

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
Oh My Pi's opening sentence `You are omp's trusted coding assistant.` is rewritten **whole** to the Antigravity identity line, not by substituting the alias inside it - otherwise it becomes `You are Antigravity's trusted coding assistant.`, which names no product and no vendor. It is an OMP-owned rule (`ompIdentityLines`) applied before the sentinel pass, on the protected route and the plain OMP branch alike. The bare aliases below then cover every other occurrence. The output `Antigravity` is terminal and cannot be reprocessed or redirected by custom operator mappings. A literal `.omp` filesystem path segment (`.omp/foo`, `C:\Users\...\.omp\agent`) is remapped to `.gemini/...` on the way up and restored to `.omp/...` on the way back. Oh My Pi is the only client that remaps its whole home directory this way. It additionally owns one competitor file: `the user's **global** Claude memory `~/.claude/CLAUDE.md` -> `~/.gemini/AGENTS.md` in both directions, because its own discovery reads that global path (`.ref/oh-my-pi` `discovery/claude.ts:65-69,163-188`) and the file exists on disk (9.4 KB) while its root context file is the neutral `AGENTS.md` (`discovery/agents-md.ts:21`). The scope is the **file**, never the `.claude` directory: a blanket `.claude` -> `.gemini` would collide with `.omp` -> `.gemini`, leaving one target with two possible sources and no way to reverse it. The pair is reversible precisely because `AGENTS.md` directly after `.gemini/` is a shape no `.omp` path produces - OMP's own is `.omp/agent/AGENTS.md`, which carries `agent/` in the middle. The other two remap exactly the context files they inject into the system context, in both directions: `~/.claude/CLAUDE.md`, `./.claude/CLAUDE.md`, `~/.claude/rules/`, `./.claude/rules/`, `~/.claude/projects/<slug>/memory/` for Claude Code; and for Codex, verified against `codex-rs/core/src/agents_md.rs`, the `AGENTS.md`, `AGENTS.override.md` and `skills/` entries under `CODEX_HOME`, which defaults to `~/.codex` but may be set to `./.codex` for a per-repo profile. Codex keeps its file names - `AGENTS.md` is already the neutral name it reads, so only the directory is rewritten - and the home spelling is part of the match rather than a boundary check, which is what lets one helper generate both directions of the table. A plain `.claude` or `.codex` directory is an operational identifier that must survive the round trip byte-for-byte rather than be renamed (Issue #49). The dot-prefix is the whole rule: inside a URL or path a bare vendor word is rewritten only as a literal dot-prefixed segment, and every other position is left byte-for-byte alone - `/omp/`, `\omp\`, `/claude/`, `/codex/`, `https://omp.ai/...` and a directory name that merely contains the brand word are never touched. The guard is symmetric, because a case-insensitive response reverse would otherwise rewrite a path the forward pass deliberately left literal (Issue #49). Assistant text restores `Antigravity -> omp` using pinned route authority.
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
