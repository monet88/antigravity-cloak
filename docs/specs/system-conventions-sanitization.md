# System Conventions & OMP Inbound Sanitization Spec

## Problem and decision

Empirical payload bisections of Oh My Pi (`omp/18.1.18`, `omp/18.18.8`, and `omp/18.20.0`)
requests to `agy/gemini-3.8-flash` on `daily-cloudcode-pa.googleapis.com`
isolated three prompt signatures that deterministically trigger upstream
`429 RESOURCE_EXHAUSTED` even when account quotas are healthy:

1. Legacy `<system-conventions>` / `</system-conventions>` XML wrappers around
   Oh My Pi's normative directive block (rewritten to `<conventions>` /
   `</conventions>`).
2. Current and legacy opening normative modal clauses across Oh My Pi's system,
   advisor, and live-instruction templates:
   - `RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.`
   - `RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.`
   Both are rewritten case-insensitively to
   `Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` so
   enclosed `MUST` / `NEVER` / `SHOULD` / `AVOID` rules keep their full meaning.
3. Injected Claude Code OAuth system instruction in `omp/18.20.0+`: custom
   `anthropic-messages` models without an explicit `auth` field default to
   `isOAuth: true`, prepending `"You are Claude Code, Anthropic's official CLI for Claude."`
   with `cache_control: {"type": "ephemeral"}` onto requests that still advertise
   Oh My Pi identity (`X-Cloak-Client: oh_my_pi` / `User-Agent: omp/...`).
   Rewriting this block case-insensitively to `antigravityIdentity` removes the
   upstream `429` trigger while keeping a non-empty `cache_control` block (empty
   `""` stripping would trigger upstream `400` errors on Claude models).

A subsequent audit of Oh My Pi `v19.2.1` prompts and tool declarations (Spec #68:
Issues #69, #70, #71) identified three additional gaps addressed in
[ADR 0006](../adr/0006-omp-inbound-sanitization-surfaces-and-v19-presets.md):

4. Eight additional third-party and harness-signature preset groups (nine
   v19.2.1 patterns, 12 case-insensitive literal rules, including the Tier 1
   `omp://` harness-docs bullet and ``, `CLAUDE.md`, `.cursorrules` `` list,
   plus security cloud/OAuth labels, Ratchet attribution, Anthropic usage
   wrap-up prefix, agent-architect `CLAUDE.md` phrases, and the
   `scan-coordinator` HTML derivation comment) required one-way **Inbound
   Sanitization** without polluting OMP's two-way **Brand Rewriting** table.
5. **Inbound Sanitization** previously visited only top-level `system` and
   `system`/`developer` messages, missing tool descriptions, JSON Schema
   `description`/`title` fields, and `<system-reminder>` spans in `user` turns.
6. `ProtectedAGY` previously skipped **Brand Rewriting** on **Machine-Generated
   Conversation Turns** (`role: "tool"`, `tool_result` text blocks, and
   `assistant` `text`/`thinking` blocks) and `<system-reminder>`
   **Harness-Authored Surface** spans inside `user` turns, diverging from
   `User-Agent: omp/...`.

To allow operators to adapt immediately without rebuilding the CGO binary or
restarting the container if upstream signatures evolve, the plugin exposes a
live-reconfigurable flat `omp_prompt_sanitizations` setting
(`internal/cloak/omp_prompt_sanitization.go`). See
[ADR 0003](../adr/0003-sanitize-system-prompt-conventions-tag-for-google-upstream.md),
[ADR 0006](../adr/0006-omp-inbound-sanitization-surfaces-and-v19-presets.md), and
[GLOSSARY.md](../../GLOSSARY.md).

## Scope and ordering

- **Three-tier request surface boundary** ([GLOSSARY.md](../../GLOSSARY.md),
  [ADR 0006](../adr/0006-omp-inbound-sanitization-surfaces-and-v19-presets.md)):
  1. **Harness-Authored Surfaces** — top-level `system`, `system`/`developer`
     messages (`text`/`input_text` blocks), `tools[].description` and JSON Schema
     `description`/`title` fields, and `<system-reminder>` spans inside `user`
     turns — execute all three passes in strict order:
     `brand -> sanitize -> tool-alias prose`.
  2. **Machine-Generated Conversation Turns** — `role: "tool"`, Anthropic
     `tool_result` text blocks, and `assistant` `text`/`thinking` blocks — run
     two-way **Brand Rewriting** and request-scoped **Tool Cloaking** on both
     `ProtectedAGY` and `User-Agent: omp/...` routes, and are **excluded from
     one-way Inbound Sanitization** so model file reads never silently mutate
     repository content before edits.
  3. **User-Typed Turns** (plain `user` text outside `<system-reminder>` spans
     and `tool_result` blocks) and structural/literal fields (`const`, `enum`,
     `default`, `example`, `examples`, `required`, property names, `tool_use.input`,
     `tool_calls[].arguments`, `cache_control`, `image`) remain byte-for-byte
     immutable.
- **Hooks and pipeline execution** (`request.intercept_before` on both
  `handleProtectedAGY` and `rewriteRequestBodyForClient`):
  1. **Brand Rewriting** runs first across all Harness-Authored Surfaces and
     Machine-Generated Conversation Turns (`rewriteProtectedBrand` on
     `ProtectedAGY`, or pre-sanitization brand passes in
     `rewriteRequestBodyForClient` on `User-Agent: omp/...`).
  2. **`sanitizeOMPPrompt(rootMap, sourceFormat, sanitizationRules)`** sanitizes
     top-level `system`, `system`/`developer` messages (`text`/`input_text`
     blocks), and tool declarations via `rewriteToolsProse` -> `rewriteToolProse`
     (visiting `description` and JSON Schema `description`/`title` while
     skipping `schemaLiteralKeys` and still descending `schemaDictionaryKeys`
     such as `properties`, `$defs`, and `definitions`), using one atomic
     `ompSanitizationRules` snapshot captured per request (ADR 0001).
  3. **Tool-alias prose & fused `<system-reminder>` pass**: alias-only
     `rewriteToolDescriptions(rootMap, nil, cachedCloak, sourceFormat)` runs on
     tool declarations, and `rewriteOMPConversationProse(rootMap, cachedCloak, sanitizationRules)`
     runs a **fused single-pass** `sanitize -> tool-alias` rewrite inside each
     `user` `<system-reminder>` span using that same per-request rule snapshot
     (so sanitization rules that strip or rewrite `<system-reminder>` tags
     cannot bypass tool-alias cloaking or bleed into surrounding **User-Typed
     Turns**), while keeping `tool_result`, `assistant`, `role: "tool"`, and
     already-sanitized `system`/`developer` messages strictly alias-only.
- **Eligible routes**:
  1. `ProtectedAGY`: explicit OMP `X-Cloak-Client` marker plus a model matching
     `model_prefixes` (empty = all models).
  2. `User-Agent` OMP: verified `User-Agent: omp/...` without `X-Cloak-Client`
     when `modelAllowsCloak` holds on normalized `anthropic` or `openai`
     Chat Completions / Messages carriers. Any `X-Cloak-Client` header value —
     valid or invalid — suppresses this route: the UA fallback requires an
     absent explicit marker.
- **Excluded routes, carriers, and Operational Identifiers**:
  - `ExplicitOMPNonAGYBypass` (models outside `model_prefixes`), `User-Agent: omp/...`
    outside `model_prefixes`, unmarked no-UA requests, other clients
    (`claude_code`, `codex`), and deferred `/v1/responses` (`openai-response`)
    carriers do not run this step.
  - **Operational Identifiers** stay untouched verbatim: `omp://` URIs in
    `find`/`glob` tool descriptions (`omp://docs/...`, `omp://xd/mcp__*`),
    `PI_BROWSER_RELAY`, `PI_BROWSER_WEBKIT`, and competitor dot-directories
    (`.claude/rules/`, `.codex/`, `.cursor/`).
- **One-way and independent**: `use_default_keywords` cannot disable OMP Inbound
  Sanitization, and non-stream JSON responses and SSE streams never reverse any
  sanitized strings (while two-way **Brand Rewriting** and request-scoped
  **Tool Cloaking** still round-trip cleanly).

## Rules and `omp_prompt_sanitizations` configuration

- **Built-in presets** (17 declarative entries in `internal/clients/omp/spec.go`,
  loaded into `defaultOMPSanitizationPresets` at `cloak.Initialize`):

| Group | Match literal | Replacement | Case-sensitive |
| :--- | :--- | :--- | :--- |
| System conventions tags (2) | `<system-conventions>` | `<conventions>` | Yes |
| | `</system-conventions>` | `</conventions>` | Yes |
| RFC 2119 opening headers (2) | `RFC 2119 keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` | `Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` | No |
| | `RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` | `Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` | No |
| Injected OAuth block (`omp/18.20.0+`) | `You are Claude Code, Anthropic's official CLI for Claude.` | `antigravityIdentity` (`You are Antigravity, ...`) | No |
| v19.2.1: `omp://` harness-docs line (3) | `- omp://: harness docs, AVOID unless asked.\r\n` | `""` (strip line) | No |
| | `- omp://: harness docs, AVOID unless asked.\n` | `""` (strip line) | No |
| | `omp://: harness docs, AVOID unless asked.` | `""` (bare fallback) | No |
| v19.2.1: Competitor context filenames | ``, `CLAUDE.md`, `.cursorrules` `` | `""` | No |
| v19.2.1: Security cloud reference | `Codex Security cloud` | `remote security cloud` | No |
| v19.2.1: Security OAuth reference | `ChatGPT OAuth account` | `remote OAuth account` | No |
| v19.2.1: Ratchet attribution | `Adapted from the claude-api skill's build-eval, eval-audit, and hillclimb guides (Apache-2.0, github.com/anthropics/skills).` | `""` | No |
| v19.2.1: Usage wrap-up prefix | `Claude usage limit reached;` | `Usage limit reached;` | No |
| v19.2.1: Architect instructions (2) | ``project-specific `CLAUDE.md` `` | ``project-specific `AGENTS.md` `` | No |
| | ``and `CLAUDE.md` coding`` | ``and `AGENTS.md` coding`` | No |
| v19.2.1: `scan-coordinator` derivation comment (2) | `<!-- Derived from openai/codex-security f22d4a36f26d16287bcdfd707b369116e02a08c3: sdk/typescript/_bundled_plugin/skills/security-scan/SKILL.md and finding-discovery/SKILL.md. Ported to Antigravity AgentSession/task semantics; Codex workspace, plugin, app-server, and CODEX_HOME instructions intentionally omitted. -->` | `""` (post-brand form) | No |
| | `<!-- Derived from openai/codex-security f22d4a36f26d16287bcdfd707b369116e02a08c3: sdk/typescript/_bundled_plugin/skills/security-scan/SKILL.md and finding-discovery/SKILL.md. Ported to OMP AgentSession/task semantics; Codex workspace, plugin, app-server, and CODEX_HOME instructions intentionally omitted. -->` | `""` (pre-brand form) | No |

  Notes on preset interactions:
  - Because `omp://` is excluded from **Brand Rewriting** via `SchemePreserve`
    (`"://"`), the CRLF and LF bulleted rules outrank the bare fallback via
    longest-match-first scanning, removing the bulleted line cleanly with no
    orphan `"- "` line while leaving operational `omp://` URIs in `find`/`glob`
    intact.
  - Because **Brand Rewriting** runs before **Inbound Sanitization**, the
    `scan-coordinator` derivation comment registers both post-brand
    (`Antigravity`, matching both `ProtectedAGY`'s `"Antigravity"` and
    `User-Agent` case-mirrored `"ANTIGRAVITY"` when `use_default_keywords: true`)
    and pre-brand (`OMP`, matching `User-Agent` when `use_default_keywords: false`)
    case-insensitive forms, stripping the comment before `task` tool-alias
    cloaking runs.
  - Exact `<system-conventions>` / `</system-conventions>` tags leave
    `<SYSTEM-CONVENTIONS>`, `system-conventions.md`, and `<system-directive>`
    unchanged by default; RFC 2119 presets leave prose like
    `The RFC 2119 word *is* the instruction` unchanged.
- **Operator overrides (`omp_prompt_sanitizations`)**:
  - Declared in `ConfigFields` as `ConfigFieldTypeObject` (flat
    `match -> replacement` schema preserved for full YAML backward compatibility).
  - Accepts `null`/omitted (built-in presets only), a JSON/YAML `map[string]string`
    object (canonical form, required when keys or replacements contain `:` or `,`),
    or `,` / `\n` / `\r`-delimited `match: replacement` string/array entries.
  - Matching keys overlay any built-in preset case-insensitively (including
    `<system-conventions>` tags and all v19.2.1 presets); an empty string
    replacement `""` strips the matched phrase.
  - Malformed entries (non-string values, missing `:`, empty/whitespace match
    keys, invalid UTF-8) fail `plugin.register` / `plugin.reconfigure` with
    `invalid_config` while preserving the previously active atomic `filterConfig`.
- **Hot-path execution**:
  - `ompSanitizationPreset` derives `lowerMatch`, the ASCII fast-scan flag, and
    (for any non-ASCII case-insensitive rule) an anchored `(?i)^` literal prefix
    regex; `buildOMPSanitizationRules` reuses it for operator entries and
    precomputes the merged rule slice sorted by descending `len(match)` (ties
    broken by lowercase `match`).
  - `sanitizeOMPPromptText` runs one rune-stepped left-to-right pass, longest
    match first at each position; replacement output is never rescanned
    (non-cascading). ASCII case-insensitive rules scan via `hasPrefixASCIIFold`
    with zero heap allocations when no rule matches.

## Verification

- **ABI-seam regression suites** (`handlePluginCall`):
  - `issue69_protected_agy_brand_parity_test.go`: `ProtectedAGY` brand parity across
    **Machine-Generated Conversation Turns**, `<system-reminder>` spans, and
    response/stream round-trips.
  - `issue70_inbound_sanitization_surfaces_test.go`: expanded **Harness-Authored
    Surfaces** (tools, JSON Schema, fused `<system-reminder>` pass), strict
    `brand -> sanitize -> tool-alias prose` ordering, conversation/literal
    exclusions, negative gates, and non-reversal.
  - `issue71_omp_v19_presets_test.go`: all 8 OMP v19.2.1 built-in preset groups
    (9 v19.2.1 patterns, 12 case-insensitive literal rules) across OpenAI &
    Anthropic (`ProtectedAGY` and `User-Agent: omp/...`,
    configured and empty `model_prefixes`), CRLF/LF bullet stripping, post-brand
    & pre-brand `scan-coordinator` comments, case-insensitive non-cascading
    operator overrides, verbatim **Operational Identifiers**, and response/SSE
    non-reversal with 2-way round-trip parity.
  - `plugin_test.go` and `architecture_test.go`: plugin metadata, lifecycle,
    package boundaries, export invariants, and Markdown link/anchor checks.
- **Package unit suites**: `internal/clients/omp/spec_test.go`,
  `internal/cloak/system_conventions_test.go`, and
  `internal/cloak/issue59_sanitization_regressions_test.go` (including
  non-ASCII case-insensitive preset compilation).

Run:

```powershell
./scripts/check.ps1
```

Offline tests establish transformation contracts; live deployment acceptance
follows [the local verification runbook](../verification/verification-checklist.md).
