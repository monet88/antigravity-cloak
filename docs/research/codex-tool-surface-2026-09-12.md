# Codex Native Tool Surface Reference

## Overview

This document defines the canonical, ground-truth tool surface of the current Codex CLI (`codex-cli`) runtime as it reaches CLIProxyAPI and `antigravity-cloak`. It lists every tool identity that can appear on the wire, the declaration shape that carries it, and whether the name is a real `tools[]` entry or only text inside another tool's description.

The companion document for the opposing side of the mapping is **[Antigravity Native Tool Surface Reference](antigravity-tool-surface-2026-09-06.md)**. Client-specific cloak tables themselves live in **[CONTEXT.md](../../CONTEXT.md)**.

Verified on 2026-09-12 against three primary sources: the local Codex runtime tool surface observed in-session, the upstream client catalog `.ref/CLIProxyAPI/internal/registry/models/codex_client_models.json`, and the CLIProxyAPI v7.2.143 wire-handling code under `.ref/CLIProxyAPI`.

---

## Architecture & Surface Distinctions

### Responses API vs. Chat Completions

Codex sends the **OpenAI Responses API** shape. CLIProxyAPI hands the raw client body to the plugin's `request.intercept_before` before any protocol translation: `payload := rawJSON` then `applyRequestInterceptorsBeforeAuth` (`.ref/CLIProxyAPI/sdk/api/handlers/handlers_execution.go:67`), which fills `pluginapi.RequestInterceptRequest{… Body: cloneBytes(req.Payload)}` (`.ref/CLIProxyAPI/sdk/api/handlers/handlers_interceptors.go:474`).

| aspect | Chat Completions | Responses (what Codex sends) |
| --- | --- | --- |
| tool declarations | `tools[].function.name` | `tools[].name` (nested `function.name` also resolves) |
| conversation history | `messages[]` | `input[]` |
| tool call item | `messages[].tool_calls[].function.name` | `input[]` item with `type` `function_call` or `custom_tool_call`, flat `name` + optional `namespace` + `call_id` |
| tool result item | `messages[] role=tool` | `input[]` item with `type` `function_call_output` or `custom_tool_call_output` |
| extra declarations | — | `input[]` item with `type` `additional_tools` (Codex Desktop / Responses Lite) |

Evidence: name resolution reads `tool.name` then `tool["function.name"]` (`.ref/CLIProxyAPI/internal/translator/openai/openai/responses/openai_openai-responses_tools.go:158`); history item types (`.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:735`, `.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/orphan_delegation.go:40`); `additional_tools` carrier (`.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:844`).

### Declaration families

| family | declaration | becomes a callable function? |
| --- | --- | --- |
| function tool | `{"type":"function","name":"…"}` (or `type` absent) | yes |
| freeform tool | `{"type":"custom","name":"…"}` with a single freeform `input` string | yes, as a single-string function |
| namespace | `{"type":"namespace","name":"collaboration","tools":[…children…]}` | children yes, container no |
| provider built-in | anything else, e.g. `{"type":"web_search"}` | no; the translator skips the type |

Evidence: the declaration walker accepts `""`/`"function"` and `"custom"`, treats `"namespace"` as a container, and skips every other type (`.ref/CLIProxyAPI/internal/translator/openai/openai/responses/openai_openai-responses_tools.go:39`). Namespace children are flattened to `namespace__child` for Chat Completions targets, while children named `mcp__…` and children already carrying their namespace prefix are left unchanged (`.ref/CLIProxyAPI/internal/translator/openai/openai/responses/openai_openai-responses_tools.go:268`).

### Code mode collapses the local tool set

Every current model carries `tool_mode: code_mode_only`. On those models the single freeform `exec` tool is the only local entry point, and the per-tool surface (`apply_patch`, `exec_command`, `write_stdin`, `view_image`, …) exists only as text inside the `exec` description. The local runtime surface corroborates that reading: its `exec` description enumerates exactly those per-tool definitions. Models without `tool_mode` (`gpt-5.5`, `gpt-5.3-codex-spark`) declare their tools individually, which their base instructions match by naming `apply_patch` and `exec_command` directly.

CLIProxyAPI does not interpret `tool_mode`; the field is consumed by the Codex client. Treat the table below as client catalog data, and the `tools[]` consequence as inference corroborated by the local surface.

### Per-model client configuration

From `.ref/CLIProxyAPI/internal/registry/models/codex_client_models.json` — the catalog the gateway serves to Codex clients.

| model | apply_patch_tool_type | web_search_tool_type | shell_type | tool_mode | multi_agent_version | use_responses_lite | experimental_supported_tools |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `gpt-6-astra` | `freeform` | `text_and_image` | `shell_command` | `code_mode_only` | `v2` | true | `send_user_message_async`, `clock` |
| `gpt-reserve` | `freeform` | `text_and_image` | `shell_command` | `code_mode_only` | `v1` | true | — |
| `gpt-5.6-sol` | `freeform` | `text_and_image` | `shell_command` | `code_mode_only` | `v2` | true | — |
| `gpt-5.6-terra` | `freeform` | `text_and_image` | `shell_command` | `code_mode_only` | `v2` | true | — |
| `gpt-5.6-luna` | `freeform` | `text_and_image` | `shell_command` | `code_mode_only` | `v1` | true | — |
| `gpt-5.5` | `freeform` | `text_and_image` | `shell_command` | — | — | false | — |
| `gpt-5.3-codex-spark` | `freeform` | `text` | `shell_command` | — | — | false | — |
| `codex-auto-review` | `freeform` | `text_and_image` | `shell_command` | `code_mode_only` | `v1` | true | — |

`shell_type: shell_command` labels the shell **variant** the client configures; no current model declares `shell_command` as a `tools[]` entry.

---

## Complete Tool Inventory

`Presence` distinguishes a real wire declaration from a name that appears only inside instruction text. `Evidence` names a file under `.ref/CLIProxyAPI/internal/registry/models/` unless it says otherwise.

Across groups 1-8 and 10 this reference names 33 tool identities plus the open-ended `mcp__<server>__<tool>` family; group 9 collects the subset that is defined inside `exec` on code-mode models.

### 1. Code Mode Entry Point (1 tool)

| wire name | presence | evidence |
| --- | --- | --- |
| `exec` | `tools[]` function/freeform on `code_mode_only` models | `gpt-6-astra.base.txt:115` (`functions.exec`) |

Freeform-style callable whose description enumerates the nested tool definitions in group 9. It carries the sandbox, approval, and code-mode instructions, so most local behaviour arrives through this one declaration.

### 2. Shell & Process Control (3 tools)

| wire name | presence | evidence |
| --- | --- | --- |
| `exec_command` | standalone on non-code-mode models; nested in `exec` otherwise | `gpt-5.5.base.txt:76`; `codex-auto-review.base.txt:81` |
| `write_stdin` | nested in `exec` | local runtime surface |
| `wait` | `tools[]`, top-level | local runtime surface; not referenced by catalog instruction text |

`exec_command` starts a shell command and may yield a background session; `write_stdin` writes to that session; `wait` polls a still-running session. Execution policy fields (`sandbox_permissions`, `justification`, `prefix_rule`) belong to `exec`/`exec_command`, so a rewrite of the name must leave the schema untouched.

### 3. File Editing & Media (2 tools)

| wire name | presence | evidence |
| --- | --- | --- |
| `apply_patch` | `type:"custom"` freeform tool on non-code-mode models; nested in `exec` otherwise | `apply_patch_tool_type: freeform`; `gpt-5.5.base.txt:59` |
| `view_image` | nested in `exec` | local runtime surface |

`apply_patch` carries one freeform patch string, not a JSON object. `view_image` reads a local image; generation is the separate `image_gen.imagegen` entry in group 9.

### 4. User Interaction & Turn Signals (4 tools)

| wire name | presence | evidence |
| --- | --- | --- |
| `request_user_input` | `tools[]` | `gpt-6-astra.mm.collaboration_modes.txt` |
| `request_user_input_async` | `tools[]` | `gpt-6-astra.base.txt:65` (`functions.request_user_input_async`) |
| `send_user_message_async` | `tools[]` on models declaring it | `gpt-6-astra.base.txt:65`; `experimental_supported_tools` |
| `update_up_next` | `tools[]` in persistent mode | `gpt-6-astra.mm.persistent_instructions.txt` |

`send_user_message_async` and `request_user_input_async` are the non-blocking delivery pair; `update_up_next` records what the agent will do after a sleep. The base instructions describe the pair as alternatives rather than both being present.

### 5. Context Window Management (7 tools)

| wire name | presence | evidence |
| --- | --- | --- |
| `new_context` | `tools[]` | `gpt-6-astra.mm.token_budget.txt:5` (`functions.new_context`) |
| `notes` | `tools[]` | `gpt-6-astra.mm.token_budget.txt:5` |
| `history` | `tools[]`, read-only | `gpt-6-astra.mm.token_budget.txt:6` |
| `get_context_remaining` | `tools[]` | `gpt-6-astra.mm.token_budget.txt:6` |
| `read_item` | `tools[]` | `gpt-6-astra.mm.token_budget.txt:6` |
| `list_items` | `tools[]` | `gpt-6-astra.mm.token_budget.txt:6` |
| `search_contents` | `tools[]` | `gpt-6-astra.mm.token_budget.txt:6` |

This group is the history/notes extension. `notes` writes a checkpoint tied to the current thread, `history` reads earlier context-window material, and `read_item` / `list_items` / `search_contents` resolve a specific item by window id and item id. `new_context` opens the fresh window after a checkpoint. All of them are bookkeeping: the instructions tell the model not to mention them to the user.

### 6. Multi-Agent Coordination (6 tools, namespace `collaboration`)

| child name | presence | evidence |
| --- | --- | --- |
| `spawn_agent` | namespace child | `.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:40`; `gpt-6-astra.mm.multi_agent.txt` |
| `followup_task` | namespace child | `.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:42`; `gpt-6-astra.mm.multi_agent.txt` |
| `send_message` | namespace child | `.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:41`; `gpt-6-astra.mm.multi_agent.txt` |
| `list_agents` | namespace child | local runtime surface |
| `wait_agent` | namespace child | local runtime surface |
| `interrupt_agent` | namespace child | local runtime surface |

The namespace name is `collaboration` (`.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:26`), and the upstream optimizer recognizes exactly the first three children as the ones whose `message.encrypted` parameter it must strip (`.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go:39`). The remaining three appear on the local surface only.

Two call shapes exist for the same identity: nested `{type:"namespace",name:"collaboration",tools:[…]}`, and a flat name that upstream handles with both `collaboration.` and `collaboration__` prefixes. Tool call items carry the namespace in a sibling field: `{type:"function_call", name:"spawn_agent", namespace:"collaboration"}`.

### 7. Time (2 tools, namespace `clock`)

| child name | presence | evidence |
| --- | --- | --- |
| `sleep` | namespace child, listed in `experimental_supported_tools` | `gpt-6-astra.mm.persistent_instructions.txt` (`clock.sleep`) |
| `curr_time` | namespace child, nested in `exec` | local runtime surface (`clock__curr_time`) |

### 8. Apps, Connectors & MCP

| wire name | presence | evidence |
| --- | --- | --- |
| `tool_search` | referenced as available, not declared per model | `gpt-6-astra.base.txt:156` |
| `list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource` | nested in `exec` | local runtime surface; `gpt-6-astra.base.txt:157` |
| `mcp__<server>__<tool>` | flat name, exempt from namespace qualification | `.ref/CLIProxyAPI/internal/translator/openai/openai/responses/openai_openai-responses_tools.go:270` |
| `codex_app.create_thread`, `codex_app.send_message_to_thread` | namespace children | `.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/orphan_delegation.go:17` |

`tool_search` is the lazy loader for app-provided MCP tools; the base instructions also tell the model not to call `list_mcp_resources`/`list_mcp_resource_templates` for apps. The `codex_app` children flatten to `codex_app__create_thread` and `codex_app__send_message_to_thread`, and a delegation result whose `namespace` is `codex_app` is what upstream treats as an orphan delegation.

### 9. Tools Nested Inside `exec`

These are defined inside the `exec` description, so on `code_mode_only` models they are reachable only through `exec` and never appear as `tools[]` entries. Observed on the local runtime surface unless a catalog file is named.

| name | purpose |
| --- | --- |
| `apply_patch` | freeform patch application (also standalone on non-code-mode models) |
| `exec_command` | shell execution (also standalone on non-code-mode models) |
| `write_stdin` | write to a running `exec_command` session |
| `view_image` | read a local image |
| `create_goal`, `get_goal`, `update_goal` | goal tracking |
| `list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource` | MCP resource access |
| `clock.curr_time` (`clock__curr_time`) | current time |
| `image_gen.imagegen` (`image_gen__imagegen`) | image generation |
| `node_repl` sub-tools (`mcp__node_repl__js`, `mcp__node_repl__js_reset`, `mcp__node_repl__js_add_node_module_dir`, …) | Node REPL; also referenced in `codex-auto-review.mm.auto_review.txt` |
| `cua_repl` | computer-use REPL; referenced in `codex-auto-review.mm.auto_review.txt` only |

### 10. Parallelism (1 tool)

| wire name | presence | evidence |
| --- | --- | --- |
| `multi_tool_use.parallel` | `tools[]` | `gpt-5.5.base.txt:9` |

Present in the older instruction sets and absent from the `code_mode_only` sets, where parallel calls are expressed with `await Promise.allSettled([...])` inside `exec`.

---

## Retired Names

Names that historical cloak tables still carry, with their status on the current surface.

| legacy name | status |
| --- | --- |
| `shell_command` | Not a tool name. `shell_type: shell_command` is a variant label in the model catalog. |
| `apply_patch` | Still real, but freeform: `apply_patch_tool_type: freeform`. |
| `view_image` | Real, nested in `exec`. |
| `update_plan` | Not referenced anywhere in the current catalog. |
| `tool_search` | Instruction text only. |
| `get_goal`, `create_goal`, `update_goal` | Real, nested in `exec`. |
| `list_mcp_resources`, `list_mcp_resource_templates`, `read_mcp_resource` | Real, nested in `exec`. |

---

## Cloak Implications

The plugin's request-side readers expect Chat Completions. On a Responses body `extractToolNames` (`main.go:4596`) and `cloakToolNames` (`main.go:3781`) find nothing, so no client resolves and the `codex` table never runs. Local probe on 2026-09-12 (throwaway test file, removed after the run):

```
PROBE chat-completions   toolNames=[exec request_user_input] detectClient="codex"
PROBE responses-flat     toolNames=[]                        detectClient=""
PROBE responses-nested   toolNames=[]                        detectClient=""
PROBE responses-history  toolNames=[]                        detectClient=""
```

| path | reader | Responses-compatible? |
| --- | --- | --- |
| `tools[].function.name` | `extractToolNames`, `cloakToolNames` | No. Only the nested `function.name` is read; neither walks `type:"namespace"` children nor `input[].additional_tools`. |
| `messages[].tool_calls[].function.name` | `extractToolNames`, `cloakToolNames` | No. Responses history is `input[]`. |
| response JSON `message.tool_calls[].function.name`, `delta.tool_calls[].function.name` | `uncloakJSONNodeOpt` (`main.go:2815`) | No. Responses output items are `type:"function_call"` / `type:"custom_tool_call"` with a flat `name`. |
| any `"name":"<target>"` in a stream chunk | stream uncloak regex (`main.go:3278`) | Yes. The regex is shape-agnostic and already matches Responses items. |
| request text bodies | `replaceToolNamesInText` (`main.go:4244`), unambiguous-name rule (`main.go:4495`) | Yes, once a client is resolved. |

`X-Cloak-Client: codex` resolves the client on the explicit-header path, which turns on brand rewriting and text rewriting today with no shape change.

| Codex source | AGY native target | In the table today | Recommendation |
| --- | --- | --- | --- |
| `exec` | `run_command` | yes | Keep. |
| `request_user_input` | `ask_question` | yes | Keep. |
| `collaboration.spawn_agent` | `invoke_subagent` | yes | Keep. |
| `collaboration.followup_task` | `manage_task` | yes | Keep. |
| `collaboration.list_agents` | `manage_subagents` | yes | Keep. |
| `apply_patch` | none verified | removed | Pass through. `multi_replace_file_content` is not exposed by live AGY. |
| `view_image` | `generate_image` | removed | Pass through; generation is not inspection. |
| `collaboration.send_message` | `send_message` | absent | No rewrite needed while the identifiers match. |
| `codex_app.create_thread`, `codex_app.send_message_to_thread` | `send_message` | absent | A static rename loses the thread identity; pass through unless an adapter is designed. |
| `web_search` | `search_web` | absent | Built-in provider tools are not function declarations, so a rename does not reach them. |
| `wait`, `exec_command`, `write_stdin` | `run_command` (via `exec`) | absent | Already covered by the `exec` mapping. |
| `request_user_input_async`, `send_user_message_async`, `update_up_next`, `new_context`, `notes`, `history`, `get_context_remaining`, `read_item`, `list_items`, `search_contents`, `multi_tool_use.parallel`, `tool_search`, `clock.sleep`, `wait_agent`, `interrupt_agent` | none | absent | Pass through. |
| `mcp__<server>__<tool>` | `call_mcp_tool` is not a 1:1 target | absent | Pass through, per existing MCP policy. |

---

## Open Questions

- Whether to fix the Responses reader on the request side, and whether the response side needs the same walk over `output[]` items.
- Whether `apply_patch` should stay pass-through or be rewritten in text only. `apply_patch` is a single underscore name, so the unambiguous-name rule does not apply and bare prose would be rewritten.
- Whether namespaced identifiers should be keyed by base name or by full identifier. `splitToolNamespace` (`main.go:1200`) splits on `:` only, so neither `.` nor `__` resolves to a base name today.
- Which intentionally unmapped Codex tools, if any, should be suppressed on a protected route rather than passed through unchanged.

---

## How to Refresh This Document

```powershell
# 1. Per-model client configuration
$catalog = '.ref/CLIProxyAPI/internal/registry/models/codex_client_models.json'
$j = Get-Content $catalog -Raw | ConvertFrom-Json
$j.models | Select-Object slug, apply_patch_tool_type, web_search_tool_type, shell_type, tool_mode, multi_agent_version, use_responses_lite, experimental_supported_tools

# 2. Tool names quoted by the client instructions
$j.models | ForEach-Object { $_.base_instructions } | Select-String -Pattern 'functions\.[a-zA-Z_]+' -AllMatches |
  ForEach-Object { $_.Matches.Value } | Sort-Object -Unique

# 3. Wire handling in the upstream mirror
Get-ChildItem .ref/CLIProxyAPI -Recurse -Include *.go |
  Select-String -Pattern 'codexCollaborationNamespace|codexAppNamespace|== "namespace"|additional_tools|function_call'

# 4. The plugin side
Select-String -Path main.go -Pattern 'func extractToolNames|func cloakToolNames|func uncloakJSONNodeOpt|func splitToolNamespace'
```

Bump the date in the file name when the surface materially changes.

---

## Primary Verification Sources

- **Local Codex runtime surface**: top-level tools and the `exec` nested catalog, observed in-session on 2026-09-12.
- **Upstream client catalog**: `.ref/CLIProxyAPI/internal/registry/models/codex_client_models.json`, carrying per-model tool configuration and the client base instructions.
- **Wire handling**: `.ref/CLIProxyAPI/internal/translator/openai/openai/responses/openai_openai-responses_tools.go`, `.ref/CLIProxyAPI/internal/client/codex/optimize-multi-agent-v2/`, `.ref/CLIProxyAPI/sdk/api/handlers/handlers_interceptors.go`, `.ref/CLIProxyAPI/sdk/api/handlers/handlers_execution.go`.
- **Opposite surface**: [Antigravity Native Tool Surface Reference](antigravity-tool-surface-2026-09-06.md).
