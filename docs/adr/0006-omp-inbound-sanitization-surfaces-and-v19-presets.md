# ADR 0006: OMP Inbound Sanitization Surfaces, v19.2.1 Presets, and ProtectedAGY Brand Parity

## Status
Accepted

## Context
Auditing `antigravity-cloak` against Oh My Pi (`omp`) v19.2.1 prompts and tool declarations uncovered three classes of upstream leakage:
1. Seven literal patterns from OMP v19.2.1 (including two Tier 1 signatures present on every normal session: the `omp://` harness-docs line and `, \`CLAUDE.md\`, \`.cursorrules\``) plus two additional third-party references (`scan-coordinator` derivation HTML comment and `ChatGPT OAuth account`) were not covered by built-in **Inbound Sanitization** presets.
2. **Inbound Sanitization** only inspected `system` and `system`/`developer` messages, skipping tool descriptions, JSON Schema `description`/`title` fields, and `<system-reminder>` spans inside `user` turns.
3. **ProtectedAGY** skipped **Brand Rewriting** on **Machine-Generated Conversation Turns** (`role: "tool"`, `tool_result` text blocks, `<system-reminder>` spans, and `assistant` `text`/`thinking` blocks), diverging from the `User-Agent: omp/...` route and leaking `omp` in runtime tool notices.

## Decision
1. **Strict Separation Between 1-Way Inbound Sanitization and 2-Way Brand Rewriting**:
   - Keep **Brand Rewriting** strictly two-way, reversible, and owned by Oh My Pi's own identity words and home paths (see [GLOSSARY.md](../../GLOSSARY.md) and [CONTEXT.md](../../CONTEXT.md)).
   - Place all one-way v19.2.1 literal cleanup rules—including embedded competitor/third-party references and the `omp://` harness-docs advertisement line—in **Inbound Sanitization** (extending [ADR 0003](0003-sanitize-system-prompt-conventions-tag-for-google-upstream.md)) on eligible OMP routes matching `model_prefixes` (`ProtectedAGY` and `User-Agent: omp/...`, empty = all models).
2. **Three-Tier Request Surface Boundary**:
   - **Harness-Authored Surfaces** (`system`, `system`/`developer` messages, `tools[].description` + JSON Schema `description`/`title`, and `<system-reminder>` spans inside `user` turns) run all three passes in strict order: `brand -> sanitize -> tool-alias prose`.
   - **Machine-Generated Conversation Turns** (`role: "tool"`, `tool_result` text blocks, and `assistant` `text`/`thinking` blocks) run two-way **Brand Rewriting** and tool-alias cloaking on **ProtectedAGY** (matching the `User-Agent` route), but are **excluded from one-way Inbound Sanitization** so reading repository files containing sanitized phrases never silently mutates file content before edits.
   - **User-Typed Turns** and structural/literal fields (`enum`, `const`, `default`, `example`, `examples`, `required`, property names, `tool_use.input`, `tool_calls[].arguments`, `cache_control`, `image`) remain byte-for-byte immutable.
3. **Flat `omp_prompt_sanitizations` Schema**:
   - Keep `omp_prompt_sanitizations` as a flat `match -> replacement` object without per-rule `scope` fields so existing CLIProxyAPI YAML configurations remain 100% backward compatible.
4. **Ordering & Deliberate Exclusions**:
   - Because `brand` executes before `sanitize`, presets matching strings that also contain `OMP` (such as the `scan-coordinator` derivation HTML comment) register both post-brand (`Antigravity`) and pre-brand (`OMP`) case-insensitive variants, and the `omp://` docs bullet registers CRLF/LF bulleted forms ahead of the bare fallback to avoid leaving orphan `- ` lines.
   - **Operational Identifiers** (`omp://` in `find`/`glob`, `PI_BROWSER_RELAY`, `PI_BROWSER_WEBKIT`, competitor dot-directories `.claude/rules/`, `.codex/`, `.cursor/`), rare unframed `user`-role prompts (`validate-request.md`, unframed `anthropic-usage-wrap-up.md`), CLI command tokens (`omp ps`, `omp worktree clear` taking bare brand rewrite), and the `User-Agent`-only 9-tool cloaking boundary remain unchanged.

## Considered Options
- **Adding competitor names into OMP's Brand Rewriting table**: Rejected because it violates two-way reversibility invariants and risks corrupting model responses mentioning competitor tools.
- **Running one-way Inbound Sanitization on `role: "tool"` / `tool_result`**: Rejected because model file reads (`read`) land in `tool_result`; one-way stripping/rewriting there would silently corrupt repository files prior to `edit`.
- **Adding a `scope` field to `omp_prompt_sanitizations`**: Rejected to avoid breaking existing flat `match: replacement` YAML configs and because the literal presets are specific enough to apply safely across all **Harness-Authored Surfaces**.

## Consequences
- **ProtectedAGY** and `User-Agent: omp/...` routes matching `model_prefixes` share one consistent `brand -> sanitize -> tool-alias prose` contract across **Harness-Authored Surfaces** and uniform **Brand Rewriting** across **Machine-Generated Conversation Turns**.
- Since `0.7.7` replaced the hardcoded `agy/` check with `model_prefixes`, both `ProtectedAGY` and `User-Agent` sanitization follow `model_prefixes` uniformly (empty = all models).
- Prior contract assertions that expected `ProtectedAGY` to leave `assistant` and `tool_result` custom brand words verbatim are superseded in favor of route parity.
- Implementation details and preset tables live in [system-conventions-sanitization.md](../specs/system-conventions-sanitization.md) and [CONTEXT.md](../../CONTEXT.md).
