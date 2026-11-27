# System Conventions & OMP Prompt Sanitization Spec

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

To allow operators to adapt immediately without rebuilding the CGO binary or
restarting the container if upstream signatures evolve, the plugin exposes a
live-reconfigurable `omp_prompt_sanitizations` setting (`omp_prompt_sanitization.go`).
See [ADR 0003](../adr/0003-sanitize-system-prompt-conventions-tag-for-google-upstream.md).

## Scope and ordering

- Hook: `request.intercept_before` via `sanitizeOMPPrompt(rootMap)`, running
  after brand rewriting and before the prose tool-alias pass on both OMP `agy/*`
  request paths (`handleProtectedAGY` and `rewriteRequestBodyForClient`). That
  ordering ensures brand rules never clobber operator replacement text while
  declared OMP tool references inside sanitized prompt text (e.g. `use bash`)
  still cloak upstream (`use run_command`).
- Routes:
  1. `ProtectedAGY`: explicit OMP `X-Cloak-Client` marker plus an `agy/` prefix
     on `Model` or `RequestedModel` (retaining precedence over `model_prefixes`).
  2. `User-Agent` OMP on `agy/*`: verified `User-Agent: omp/...` without
     `X-Cloak-Client` when `isAGYRoute` and `modelAllowsCloak` hold on normalized
     `anthropic` or `openai` Chat Completions / Messages carriers. Any
     `X-Cloak-Client` header value — valid or invalid — suppresses this route:
     the UA fallback requires an absent explicit marker.
- Excluded routes and carriers: `ExplicitOMPNonAGYBypass` (non-`agy/*`),
  non-`agy/*` `User-Agent: omp/...` traffic, unmarked no-UA requests, other
  clients (`claude_code`, `codex`), and deferred `/v1/responses` (`openai-response`)
  carriers do not run this step.
- Visited fields: top-level `system` plus `messages[].content` for roles
  `system` and `developer`. Supported content is a `string` or an array of
  `text` / `input_text` blocks (visiting only each block's string `text` field).
  `user`, `assistant`, and `tool` messages, tool schemas/descriptions/arguments,
  block metadata (`cache_control`), and `image` / `tool_use` blocks are untouched.
- One-way and independent: `use_default_keywords` cannot disable OMP prompt
  sanitization, and responses/streams never reverse `<conventions>` or
  `Normative keywords:` text.

## Rules and `omp_prompt_sanitizations` configuration

- **Built-in presets** (`defaultOMPSanitizationPresets`):
  - Exact case-sensitive `<system-conventions>` $\to$ `<conventions>` and
    `</system-conventions>` $\to$ `</conventions>` (leaving `<SYSTEM-CONVENTIONS>`,
    `system-conventions.md`, and `<system-directive>` unchanged by default).
  - Case-insensitive literal substring matches for both RFC 2119 signatures
    above $\to$ `Normative keywords: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.`
    (leaving prose like `The RFC 2119 word *is* the instruction` unchanged).
  - Case-insensitive literal substring match for `You are Claude Code, Anthropic's official CLI for Claude.`
    $\to$ `antigravityIdentity` (`You are Antigravity, ...`), keeping
    `ompBrandMappings` free of competitor tokens while protecting `omp/18.20.0+`
    requests whose custom `anthropic-messages` models default to `isOAuth: true`.
- **Operator overrides (`omp_prompt_sanitizations`)**:
  - Declared in `ConfigFields` as `ConfigFieldTypeObject`.
  - Accepts `null`/omitted (built-in presets only), a JSON/YAML `map[string]string`
    object (canonical form, required when keys or replacements contain `:` or `,`),
    or `,` / `\n` / `\r`-delimited `match: replacement` string/array entries.
  - Matching keys overlay built-in presets case-insensitively (including
    `<system-conventions>` tags); an empty string replacement `""` strips the
    matched phrase.
  - Malformed entries (non-string values, missing `:`, empty/whitespace match
    keys, invalid UTF-8) fail `plugin.register` / `plugin.reconfigure` with
    `invalid_config` while preserving the previously active atomic `filterConfig`.
- **Hot-path execution**:
  - `buildOMPSanitizationRules` precomputes the merged rule slice on config
    change sorted by descending `len(match)` (ties broken by lowercase `match`).
  - `sanitizeOMPPromptText` runs one left-to-right pass, longest match first
    at each position; replacement output is never rescanned. ASCII
    case-insensitive rules scan via `hasPrefixASCIIFold` prefix checks with
    zero heap allocations when no rule matches.

## Verification

`system_conventions_test.go` and `plugin_test.go` exercise built-in tag and
RFC 2119 presets, idempotence, inbound-only response/stream non-mutation, live
`omp_prompt_sanitizations` reconfigure/override/strip/precedence, `invalid_config`
rejection with active-config preservation, `ProtectedAGY` and `User-Agent: omp/...`
`agy/*` route gates, brand-before-sanitization-before-tool-prose ordering, and
negative carrier/route boundaries through the plugin ABI entrypoint.

Run:

```powershell
go test -count=1 ./...
go test -race -count=1 ./...
go test -count=1 ./.github/scripts
go vet ./...
```

Offline tests establish transformation contracts; live deployment acceptance
follows [the local verification runbook](../verification/verification-checklist.md).
