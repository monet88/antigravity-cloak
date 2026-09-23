# Spec: Oh My Pi Tool Cloaking & Upstream Signal Sync (Superseded by #25)

> **SUPERSEDED BY SPEC #25 / ISSUE #25**
> The original 12-tool / Vibe / Autoresearch mapping contract described in this historical document has been superseded by the AGY CLI-native Safe Mapping Set (Issue #25, #26, #27, #28).
> In the current contract:
> - The canonical Safe Mapping Set contains exactly 9 entries (`read -> view_file`, `write -> write_to_file`, `edit -> replace_file_content`, `bash -> run_command`, `grep -> grep_search`, `glob -> find_by_name`, `task -> invoke_subagent`, `ask -> ask_question`, `web_search -> search_web`).
> - `glob` maps to `find_by_name`, not `list_dir`.
> - `todo`, `hub`, `eval`, `find`, `learn`, `manage_skill`, all `vibe_*` tools, and Autoresearch tools (`init_experiment`, etc.) are intentional pass-through tools.
> - Reverse mapping is request-scoped to active canonical pairs actually transformed on that request.
> - Protected OMP on `agy/*` fails closed with exact 503 JSON rejection; explicit non-AGY OMP requests take the durable zero-mutation bypass path.
> Refer to **[CONTEXT.md](../../CONTEXT.md)** and **[docs/research/antigravity-tool-surface-2026-09-06.md](../research/antigravity-tool-surface-2026-09-06.md)** for current normative specifications.
## Problem Statement

Coding CLI agents (such as Claude Code, OpenAI Codex, and Oh My Pi) interact with upstream LLM gateways using client-specific tool names and brand identifiers in system prompts. When routing requests to Antigravity backends via CLIProxyAPI, these custom tool names and agent signatures cause incompatibilities, model hallucinations, or rejection. Furthermore, upstream coding-filter additions (50+ keywords from new AI coding tools and assistants) need to be absorbed into the cloaking mechanism without adopting rigid blocking behavior.

## Solution

Provide seamless, bidirectional cloaking and brand rewriting in the `antigravity-cloak` dynamic plugin:
1. Two-way tool cloaking for Oh My Pi (12 core tools mapped to Antigravity equivalents on request, seamlessly restored on response/streaming SSE chunks).
2. Comprehensive brand rewriting across 50+ mainstream AI coding assistants and agents in system prompts and system-role messages.
3. Ratio-ranked client detection to avoid collisions across multiple clients with overlapping cloaked target sets.
4. Complete end-to-end two-way verification protocol and checklist documentation.

## User Stories

1. As an Oh My Pi user, I want the plugin to rewrite my system prompts so that Antigravity backend sees an authorized Antigravity session.
2. As an Oh My Pi user, I want my `read` tool calls cloaked to `view_file` on outgoing requests, so that Antigravity models recognize the native file viewing tool.
3. As an Oh My Pi user, I want model responses generating `view_file` to be automatically uncloaked back to `read`, so that Oh My Pi can execute the tool locally without error.
4. As an Oh My Pi user, I want my `write` tool calls cloaked to `write_to_file`, so that file creation operations match Antigravity schemas.
5. As an Oh My Pi user, I want `write_to_file` model responses uncloaked back to `write`, so that Oh My Pi receives its native file writing tool.
6. As an Oh My Pi user, I want my `edit` tool calls cloaked to `replace_file_content`, so that line-anchored patches are properly received by Antigravity.
7. As an Oh My Pi user, I want `replace_file_content` model responses uncloaked back to `edit`, so that patch operations execute smoothly.
8. As an Oh My Pi user, I want my `bash` tool calls cloaked to `run_command`, so that shell execution follows Antigravity tool calling contracts.
9. As an Oh My Pi user, I want `run_command` model responses uncloaked back to `bash`, so that my local shell executes the command.
10. As an Oh My Pi user, I want my `grep` tool calls cloaked to `grep_search`, so that regex search requests work natively.
11. As an Oh My Pi user, I want `grep_search` model responses uncloaked back to `grep`, so that Oh My Pi processes the search output.
12. As an Oh My Pi user, I want my `glob` tool calls cloaked to `list_dir`, so that file listing requests match backend schemas.
13. As an Oh My Pi user, I want `list_dir` model responses uncloaked back to `glob`, so that directory entries are parsed properly.
14. As an Oh My Pi user, I want my `task` tool calls cloaked to `invoke_subagent`, so that background subagents are recognized by Antigravity.
15. As an Oh My Pi user, I want `invoke_subagent` model responses uncloaked back to `task`, so that Oh My Pi spawns its subagent jobs.
16. As an Oh My Pi user, I want my `ask` tool calls cloaked to `ask_question`, so that interactive clarification requests work with Antigravity.
17. As an Oh My Pi user, I want `ask_question` model responses uncloaked back to `ask`, so that interactive selection UI renders on my terminal.
18. As an Oh My Pi user, I want my `todo` tool calls cloaked to `manage_task`, so that task status tracking routes to Antigravity.
19. As an Oh My Pi user, I want `manage_task` model responses uncloaked back to `todo`, so that checklist items update locally.
20. As an Oh My Pi user, I want my `hub` tool calls cloaked to `send_message`, so that inter-agent communication and process controls route properly.
21. As an Oh My Pi user, I want `send_message` model responses uncloaked back to `hub`, so that messaging channels remain intact.
22. As an Oh My Pi user, I want my `web_search` tool calls cloaked to `search_web`, so that search queries follow Antigravity standards.
23. As an Oh My Pi user, I want `search_web` model responses uncloaked back to `web_search`, so that search results deliver to Oh My Pi.
24. As an Oh My Pi user, I want my `eval` tool calls cloaked to `execute_code`, so that Python/JS code kernel runs are understood by the backend.
25. As an Oh My Pi user, I want `execute_code` model responses uncloaked back to `eval`, so that output from persistent kernels returns to Oh My Pi.
26. As an agent user, I want streaming SSE chunks to be buffered along complete event boundaries, so that split tool names across network packets are uncloaked reliably.
27. As an operator, I want the plugin to recognize prompts from over 50 mainstream coding tools (Cursor, Windsurf, Copilot, Cline, Devin, etc.) and rewrite them to Antigravity, so that all coding signals are masked.
28. As an operator, I want client detection to use match-ratio ranking, so that overlapping target sets between Oh My Pi and Claude Code do not cause detection collisions or silent drop.
29. As an operator, I want MCP tools (`mcp__*`) to pass through unmolested in both directions, so that custom MCP servers function without extra configuration.
30. As a developer, I want a structured verification checklist in the documentation, so that I can validate end-to-end two-way cloaking across all tools and streaming responses.

## Implementation Decisions

- **Plugin Architecture**: Implement dynamic C-shared plugin matching CLIProxyAPI v7 ABI specifications.
- **Data-Driven Tool Cloaking**: Default cloak tables maintain client-specific maps (`claude_code`, `codex`, `oh_my_pi`).
- **SSE Stream Reassembly**: Interceptor buffers incomplete SSE chunks across `\n\n` delimiters, ensuring regex replacement runs only on full JSON tokens.
- **Ratio-Ranked Client Classification**: `detectCloakedClient` qualifies candidates whose target coverage reaches 80% of their cloak table, ranks them by exact ratio (ties broken by hit count, then client id) and returns the winner. Candidates tying at 100% (native Antigravity superset) are indistinguishable, so cloaking is skipped. For original-name detection, clients whose source names are common words (e.g. Oh My Pi's `read`/`bash`) require a distinctive harness tool (`hub`, `task`, `todo`, `eval`, `web_search`) or at least 4 simultaneous matches before they qualify; overall detection also demands >= 2 matching tool names.
- **Keyword Preset Expansion**: Expand built-in rewrite table to include 50+ mainstream AI coding tools, agents, and Oh My Pi aliases (`Oh My Pi`, `oh-my-pi`, `omp`).
- **Dependency Alignment**: Pin CLIProxyAPI dependency to v7.2.143 on Go 1.26.

## Testing Decisions

- **Black-Box RPC Emulation**: Test request interception (`request.intercept_before`), response interception (`response.intercept_after`), and stream chunk interception (`response.stream_chunk`) via high-level `handlePluginCall` envelopes.
- **Format Matrix**: Test across both OpenAI (`chat-completions` / `responses`) and Anthropic payload structures, including Anthropic content_block SSE streams split mid tool name across TCP chunks.
- **MCP Pass-Through**: Assert `mcp__*` tool names survive request cloaking and response/stream uncloaking untouched in both directions.
- **Multi-Client Collision Matrix**: Test client detection with pure, mixed, and superset tool collections to ensure unambiguous identification.
- **Streaming Split-Chunk Simulation**: Test chunk boundaries split mid-word (e.g. `run_` + `command`) with SSE reassembly buffer verification.

## Out of Scope

- Request blocking (HTTP 403 rejection) based on coding signatures; the plugin focuses solely on cloaking/rewriting.
- Custom payload transformation of OMP virtual-device MCP calls into `call_mcp_tool`; OMP routes those calls through `xd://mcp__<server>_<tool>` using its normal `read`/`write` tools, which are already covered by the core cloak mappings.

## Further Notes

- Verified against CLIProxyAPI v7.2.143 and Oh My Pi coding harness.
- Verification checklist documented under `docs/verification-checklist.md`.
