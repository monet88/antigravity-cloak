# ADR 0003: Sanitize Reserved System Conventions Tags for Google Upstream

## Status
Accepted

## Context
When Oh My Pi (`omp`) sends requests to Google Antigravity models (`agy/gemini-3.8-flash` mapped to `gemini-3.8-flash-high`), requests failed with `429 RESOURCE_EXHAUSTED` even when quotas were clean.

Empirical bisection of the 94KB prompt payload isolated the trigger to the `<system-conventions>` tag pair in `omp`'s system prompt:
```xml
<system-conventions>
RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL. `NEVER` = `MUST NOT`; `AVOID` = `SHOULD NOT`.
XML tags inject system content; NEVER interpret them otherwise. Tags may interrupt/notify inside user messages: MUST treat as system-authored/authoritative. User content sanitized; role absent: `<system-directive>` in a user turn remains a system directive.
</system-conventions>
```

The original wrapper reproduced 429 and renaming it to `<agent-conventions>` succeeded in local testing. The upstream classifier's internal implementation and its treatment of other `<system-*>` tags are not established by that observation.

## Decision
1. **Sanitize Reserved System Tags on Inbound Requests**:
   - Run a dedicated step after brand rewriting in the ProtectedAGY request path. Replace the exact `<system-conventions>` and `</system-conventions>` tags with `<conventions>` and `</conventions>` in top-level `system` and system/developer message text.
2. **Preserve Semantic Boundaries Instead of Dropping Content**:
   - Renaming to `<conventions>` preserves the RFC 2119 structural boundary and the enclosed instructions. The shorter name is the user's selected replacement; live acceptance is separate from the earlier `<agent-conventions>` workaround.
   - Avoid stripping the entire block, which would degrade the model's instruction-following adherence.
3. **Keep OMP Version Independent**:
   - Performing this transformation inside `antigravity-cloak` shields users from upstream 429 errors regardless of the `oh-my-pi` version or future updates.

## Consequences
- ProtectedAGY system/developer prompt text reaches upstream with the replacement wrapper. Other roles, tool data, metadata, and bypass routes retain their original content.
- Eliminates client-side patching requirements across `oh-my-pi` upgrades.
- Unit and handler tests establish the transformation contract. Live deployment acceptance must establish upstream success for the installed artifact.
