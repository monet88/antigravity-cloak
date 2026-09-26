# ADR 0004: Cloak Codex Tool Names by Wire Position, Keyed on the Code-Mode Surface

## Status
Accepted

## Context

The plugin's second job is renaming a client's native tool names to Antigravity names on the way up and restoring them on the way back. For Codex the rename table sat on the wrong surface for a long time, and the failure was silent.

Three facts, each verified on 2026-09-12, decide the design:

1. **Which names a Codex session declares depends on the model's tool mode.** `code_mode_only` (opencodex's default for every routed provider) declares a single freeform `exec` entry point; shell mode declares `exec_command`, `write_stdin`, `apply_patch` and `view_image` as their own `tools[]` entries. The providers on this workstation all run code mode.
2. **The plugin never sees the Responses shape here.** Codex speaks Responses to opencodex, but opencodex's `openai-chat` adapter lowers the request to `POST {baseUrl}/chat/completions` before it reaches CLIProxyAPI, so the plugin reads `tools[].function.name` and `tools[].function.description`. Namespaced children arrive flattened to `<namespace>__<child>` (opencodex's `namespacedToolName`).
3. **The reverse restores a name only where it appears as a tool name.** `uncloakStreamChunk` matches the shape `"name":"<target>"` and `uncloakJSONNodeOpt` walks name fields; no path rewrites text inside `arguments`.

A live debug-log capture of a `cpa/agy` session listed these declared names: `exec`, `wait`, `request_user_input`, `request_user_input_async`, `clock__sleep`, the six `collaboration__*` children, and `web_search`. The table in the deployed build held the legacy shell-bridge names and no `exec` at all, so detection scored one hit against a floor of two, no client resolved, and every Codex request passed through unmutated — indistinguishable, from the outside, from a correctly cloaked one.

## Decision

1. **Key the rename table on the code-mode wire surface**, and list only names that occupy a tool-name position: `exec` -> `run_command`, `web_search` -> `search_web`, `request_user_input` -> `ask_question`, and `collaboration__spawn_agent` / `collaboration__followup_task` / `collaboration__list_agents` -> `invoke_subagent` / `manage_task` / `manage_subagents`. Keys are the flattened spellings the wire actually carries, which is also what the request-scoped reverse matches a declared name against.

2. **Give `run_command` to `exec`, and leave `exec_command` unmapped.** `defaultUncloakTables` is built by inverting the cloak map, so one target cannot carry two sources. The code-mode entry point wins because it is what every routed provider here sends.

3. **Leave helpers that exist only as prose inside the `exec` description as pass-through** (`apply_patch`, `exec_command`, `write_stdin`, `view_image`, `tool_search`, the goal and MCP-resource tools). The model copies those names into the code it writes, so renaming them would change the helper it calls and the reverse could not change it back, handing the client a helper it never declared. Renaming `exec` does not have this problem because `exec` occupies a real tool-name position.

4. **Keep detection counting `codexSourceIdentityInventory` rather than the rename table alone**, and add the flattened collaboration spellings to that inventory. A code-mode request that declares `exec` plus namespace children and nothing else otherwise scores one hit and stays undetected. Source names an operator adds through `tool_mappings` are still counted, because a static inventory cannot know them; `oh_my_pi` is the exception, whose attribution must stay on the canonical inventory.

5. **Treat `X-Cloak-Client: codex` as identity, not as a mode switch.** When the header is present and valid the client is resolved deterministically and body detection is skipped; detection remains the fallback. Recommended against naming header values per mode (e.g. `codex-code-mode` / `codex-shell-mode`): mode is a property of the request, not of the endpoint, so a per-mode header would make the config the source of truth and could silently apply the wrong table. The mode is to be read from what the request declares, mirroring opencodex's own `declaresCodeModeExec` rule.

6. **Keep the request-scoped reverse narrowing for Codex only.** It is what made it safe to key the table on one mode at a time: a shell-mode request that declares `exec_command` never receives a `run_command` restored to `exec`. Narrowing reads the **source** side of a pair first — a declared source name proves the body is pre-cloak — and reads the target side only for a body that declares no source name at all, which is all the evidence an executed body carries. Whoever still holds the raw body scopes from it and caches the result: request interception stores the scope on the stream session, and the response path and the payload chunks reuse it, because an executed body cannot separate a target the client declared natively from one the rewrite produced. Reading the source side alone dropped the whole reverse on every executed body; reading the target side whenever it matched handed natively declared AGY names back as Codex sources. Only the uncorrelated stream fallback, which has nothing but the executed body, reads the target side.

## Consequences

- Six declaration-level renames are live and verified on the wire: both probe runs logged `rewritten=true client=codex`, all six AGY target names appeared upstream, all six Codex source names appeared zero times, and the probe command executed — which is the practical proof that the reverse restored `run_command` to `exec`.
- **Shell mode is not served by this table.** A provider switched to `codexToolMode: shell` declares `exec_command`, which now has no mapping, so the Codex cloak degrades to almost nothing. Supporting both modes requires a per-mode table selected from what the request declares, which the removal of the injectivity conflict would also make possible.
- **A prose tell remains.** Upstream receives a tool named `run_command` whose description still names `apply_patch`, `exec_command`, `write_stdin`, `view_image` and `tool_search`. Removing it needs a reverse that reaches inside `arguments`, which would in turn require teaching the narrowing that prose names count as declared — a deliberate weakening of the current invariant, so it is left open rather than done.
- **Two identity paths exist, so both must keep working.** The header needs `ocx restart` to take effect, because the running proxy holds the provider runtime from startup; a config write alone is invisible to it. Detection covers the gap.
- **MCP tools are not cloaked, and cannot be with this mechanism.** In code mode they are not `tools[]` declarations at all (`supports_search_tool: true` defers them, and a live capture listed declared names with none of them MCP); AGY's bridge `call_mcp_tool` is a single target for an open-ended family; and AGY identifies an MCP tool as a `(ServerName, ToolName)` pair carrying the server's own published name, not the flattened wire name. Masking one would need identity transformation and argument rewriting, which is beyond a rename and beyond what the reverse can undo.
- **The artifact name is not a version marker.** The build deployed before this ADR was named `v0.5.1` yet had been compiled from an older commit; `vcs.revision` inside the binary is the only reliable provenance. Bumping `pluginVersion` on a behaviour change would make the name honest again.
- Evidence, per-name rationale and the adoption of the opposite (shell-mode) table are recorded in [the Codex surface reference](../research/codex-tool-surface-2026-09-12.md).

## Amendment (2026-09-25 — Issue #32 / PR #36 / PR #38)

1. **Request-Scoped Aliasing Expanded Across All Clients**:
   Request-scoped aliasing is no longer Codex-only. Under parent #32, the request-scoped alias plan architecture is generalized across all supported clients (`claude_code`, `oh_my_pi`, `codex`).
2. **Codex Shell Mode and Collaboration Tools Cloaked**:
   - `exec_command` is cloaked to `run_command` in shell mode via `codexSharedAliases`. Because alias plans resolve targets per-request, `exec` (code mode) and `exec_command` (shell mode) no longer cause an injectivity collision in the static table at initialization.
   - Shell-mode declarations (`apply_patch`, `write_stdin`, `view_image`) and collaboration helpers (`collaboration__followup_task`, `collaboration__list_agents`, `collaboration__wait_agent`, `collaboration__interrupt_agent`, `collaboration__send_message`) are mapped to stable shared aliases (`wp_*`).
   - Dynamic MCP declarations (`mcp__*`) and unknown tools receive deterministic fallback aliases (`wp_ext_<hash>`).
3. **Reversal Scoping**:
   `requestsRequestScopedReverse` now applies to both `codex` and `claude_code` (with OMP using active reverse authority), scoping reverse restoration exclusively to declared tool names and alias plan outputs for each request.

