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

## Amendment (Issue #59)
1. **Built-in RFC 2119 Normative Header Presets**:
   - Newer `oh-my-pi` releases (`omp/18.18.8+`) removed the `<system-conventions>` wrapper and reworded the opening normative line across bundled system, advisor, and live-instruction templates to `RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.`. Empirical bisection against `daily-cloudcode-pa.googleapis.com` confirmed upstream rejects both `RFC 2119 keywords: ...` and legacy `RFC 2119: ...` signatures with false `429 RESOURCE_EXHAUSTED`.
   - Alongside exact case-sensitive `<system-conventions>` / `</system-conventions>` $\to$ `<conventions>` / `</conventions>`, `sanitizeOMPPrompt` (`omp_prompt_sanitization.go`) rewrites both `RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` and `RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` case-insensitively to `Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.`, preserving modal directive semantics while leaving unrelated `RFC 2119` prose untouched.
2. **Live-Reconfigurable `omp_prompt_sanitizations`**:
   - Operators can configure `omp_prompt_sanitizations` (`ConfigFieldTypeObject`, accepting `match -> replacement` objects or `,`/`\n`-delimited `match: replacement` string/array entries) via `plugin.register` / `plugin.reconfigure` without rebuilding the `.so` or restarting the gateway.
   - Rules are precomputed on the atomic `filterConfig` snapshot in longest-match-first order: operator keys matching a built-in preset case-insensitively override its replacement (including `<system-conventions>` tags and `""` empty-string stripping), independent of `use_default_keywords` and strictly one-way on inbound requests.
3. **`User-Agent: omp/...` `agy/*` Route Coverage**:
   - Sanitization runs after brand rewriting and before prose tool-alias cloaking on both `ProtectedAGY` (`X-Cloak-Client` OMP marker on `agy/*`) and `User-Agent: omp/...` `agy/*` routes permitted by `model_prefixes`, while leaving `ExplicitOMPNonAGYBypass`, non-`agy/*` routes, unmarked no-UA traffic, other clients, and non-system carriers untouched.

## Follow-up Extensions
1. **Injected Claude Code OAuth Instruction Preset (`omp/18.20.0+`)**:
   - Beginning with `omp/18.20.0`, custom `anthropic-messages` models without an explicit `auth` field default to `isOAuth: true` (`packages/coding-agent/src/config/custom-models.ts`), prepending `"You are Claude Code, Anthropic's official CLI for Claude."` (`cache_control: {"type": "ephemeral"}`) onto requests that still identify as `oh_my_pi`.
   - `defaultOMPSanitizationPresets` (`omp_prompt_sanitization.go`) includes a case-insensitive preset rewriting that string to `antigravityIdentity` rather than stripping it to `""` (preserving non-empty `cache_control` blocks while keeping competitor identity strings out of `ompBrandMappings`).
