# Antigravity Cloak

Domain language for the `antigravity-cloak` gateway plugin, which disguises coding-CLI traffic as Antigravity on upstream LLM requests and restores client-native tokens on responses and streams.

## Language

### Cloaking & Sanitization Mechanisms

**Brand Rewriting**:
Two-way, client-owned substitution that disguises a supported client's own identity words and home paths as Antigravity on inbound requests and restores them on responses and streams.
_Avoid_: Prompt sanitization, keyword scrubbing, competitor masking

**Inbound Sanitization**:
One-way, request-only literal rewriting that removes or neutralizes upstream-rejected signatures and embedded third-party references on eligible Oh My Pi `agy/*` routes without reversing them on responses.
_Avoid_: Brand rewriting, bidirectional cloaking

**Tool Cloaking**:
Two-way translation of client-native tool names into Antigravity-native targets, shared `wp_*` aliases, or deterministic `wp_ext_<hash>` fallbacks across declarations, history, `tool_choice`, and machine prose, restored per request on responses and streams.
_Avoid_: Tool sanitization

**Operational Identifier**:
An executable or address-bearing literal—such as a virtual-device scheme (`omp://`), environment variable (`PI_BROWSER_*`), filesystem dot-directory (`.omp/`, `.claude/CLAUDE.md`), or JSON Schema literal (`enum`, `const`, `default`, `required`, property name)—that must either pass through verbatim or undergo exact two-way remapping so client execution never breaks.
_Avoid_: Brand prose, free text

### Routes & Authority

**ProtectedAGY**:
Fail-closed Oh My Pi route triggered by an explicit `X-Cloak-Client` OMP marker targeting an `agy/*` model, enforcing full tool cloaking, terminal brand rewriting, inbound sanitization, and request-scoped reversal, or rejecting with HTTP 503.
_Avoid_: Legacy OMP route, generic model gate

**ExplicitOMPNonAGYBypass**:
Durable zero-mutation route triggered by an explicit `X-Cloak-Client` OMP marker targeting a non-`agy/*` model, consuming only the control header while leaving request, response, and stream bodies byte-for-byte unchanged.
_Avoid_: Fallback route, model-gate skip

**Safe Mapping Set**:
The immutable nine-entry canonical Oh My Pi tool mapping table (`read`, `write`, `edit`, `bash`, `grep`, `glob`, `task`, `ask`, `web_search` to native Antigravity targets) required on `ProtectedAGY` routes.
_Avoid_: Extended tools, shared aliases

**Alias Plan**:
A request-scoped, fail-closed mapping table pinned by `RequestID` for clients with variable tool surfaces (`claude_code`, `codex`) so only tools declared in that request are reversed on responses and streams.
_Avoid_: Global uncloak table, static fallback

### Request Surfaces

**Harness-Authored Surface**:
Machine-generated instruction carriers authored by the coding CLI harness rather than the end user or repository files: top-level `system`, `system`/`developer` messages, tool `description` and JSON Schema `description`/`title` fields, and `<system-reminder>` spans inside `user` turns.
_Avoid_: Prompt text, all messages

**Machine-Generated Conversation Turn**:
Non-user conversation prose produced by tools or assistant turns (`role: "tool"`, Anthropic `tool_result` text blocks, and `assistant` `text`/`thinking` blocks) that may carry repository file contents and therefore receives two-way brand rewriting and tool-alias cloaking, never one-way inbound sanitization.
_Avoid_: Harness-authored surface, user turn

**User-Typed Turn**:
Plain text authored by the end user in a `user` message outside `<system-reminder>` spans and `tool_result` blocks, preserved byte-for-byte across all cloaking and sanitization passes.
_Avoid_: User message
