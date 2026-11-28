# Project Decisions Log

Index: [index.md](index.md) | Related: [omp-pipeline-invariants.md](omp-pipeline-invariants.md), [agent-environment.md](agent-environment.md)

## 2026-11-28 — Persistent In-Repo Wiki and `#68` Execution Contract

- **In-repo wiki location (`2026-11-28`, `monet88/antigravity-cloak@9c8f885`):** Đại Ca confirmed `docs/wiki/` as the persistent project wiki for `antigravity-cloak`, seeded at `monet88/antigravity-cloak@9c8f885` from `monet88/antigravity-cloak@c864f7b`. Placing pages inside `docs/wiki/` satisfied the no-loose-markdown rule under `docs/` enforced by [architecture_test.go](../../architecture_test.go) at `monet88/antigravity-cloak@c864f7b`.
- **Full `implement-spec` ticket sequence for `#68` (`2026-11-28`):** Đại Ca authorized executing the full `implement-spec` flow for umbrella spec `#68` across ordered child tickets `#69 -> #70 -> #71` starting from `monet88/antigravity-cloak@c864f7b`:
  1. `#69`: `ProtectedAGY` brand parity across **Machine-Generated Conversation Turns** and `<system-reminder>` spans.
  2. `#70`: **Inbound Sanitization** expansion across all **Harness-Authored Surfaces** with strict `brand -> sanitize -> tool-alias prose` ordering.
  3. `#71`: Built-in OMP v19.2.1 sanitization preset groups plus doc/changelog synchronization.
- **Release & artifact boundary for `#68` (`2026-11-28`):** Per `#68` constraints confirmed on `2026-11-28`, the `#68` implementation sequence explicitly prohibited bumping the plugin version (`0.7.7` at `monet88/antigravity-cloak@c864f7b`) and prohibited building or deploying a Linux `.so` artifact; changelog additions were scoped to an `## [Unreleased]` section in [CHANGELOG.md](../../CHANGELOG.md) rather than a new release tag.
- **Architectural & domain baseline (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** Implementation decisions for `#68` anchored on the accepted [ADR 0006](../adr/0006-omp-inbound-sanitization-surfaces-and-v19-presets.md#decision) and [GLOSSARY.md](../../GLOSSARY.md#request-surfaces) at `monet88/antigravity-cloak@c864f7b`:
  - Preserved strict separation between two-way **Brand Rewriting** and one-way **Inbound Sanitization**.
  - Adopted the three-tier surface boundary (**Harness-Authored Surfaces** vs **Machine-Generated Conversation Turns** vs byte-for-byte immutable **User-Typed Turns** and structural/literal fields).
  - Kept `omp_prompt_sanitizations` as a flat `match -> replacement` map without per-rule `scope` fields.
  - Superseded prior `ProtectedAGY` contract expectations that left custom brand words verbatim in `assistant` and `tool_result` blocks (see [omp-pipeline-invariants.md](omp-pipeline-invariants.md#protectedagy-vs-user-agent-brand-divergence-69)).

## 2026-11-28 — `#68` Delivery, Pre-Push Review, and PR `#72` Retro Remediation

- **Ordered delivery under PR `#72` (`2026-11-28`):** Child tickets landed sequentially on `feat/issue-68-omp-v19-sanitization` without bumping `pluginVersion` (`0.7.7`) or building `.so` artifacts: `#69` at `monet88/antigravity-cloak@14ca489` (merged via `92ce106`), `#70` at `monet88/antigravity-cloak@61834e9` and `monet88/antigravity-cloak@8c800d0`, and `#71` at `monet88/antigravity-cloak@9cfb8df`.
- **Single-pass fusion over `<system-reminder>` spans (`2026-11-28`, `monet88/antigravity-cloak@8c800d0`, PR `#72`):** After pre-push `ponytail-review` caught that a sanitization rule rewriting or stripping `<system-reminder>` tags caused a subsequent tool-alias walker to miss the span, the team decided to fuse `sanitize -> tool-alias` into a single pass inside `rewriteOMPConversationProse` and codified that rule in [CODING_STANDARDS.md](../guides/CODING_STANDARDS.md#inbound-sanitization-rules).
- **PR `#72` review remediation and environment retro (`2026-11-28`, post-`monet88/antigravity-cloak@9cfb8df`):** A dedicated review/retro remediation pass on `fix/issue-68-review-and-retro` addressed remaining code-review and environment gaps:
  - Passed a single per-request atomic `ompSanitizationRules` snapshot into both `sanitizeOMPPrompt` and `rewriteOMPConversationProse` (preventing split-snapshot reads under concurrent `plugin.reconfigure`, per [ADR 0001](../adr/0001-stream-session-manager-and-atomic-config.md)).
  - Aligned code symbols and docs with [GLOSSARY.md](../../GLOSSARY.md#request-surfaces) (`rewriteOMPConversationBrandTurns`, `runOMPSanitization`, shared `rewriteToolsProse`, and keeping `<system-reminder>` spans classified strictly under **Harness-Authored Surfaces**).
  - Consolidated root black-box ABI helpers (`callPluginABI`, `reconfigurePluginYAML`, `completePluginRequest`, `mustMarshalJSONBytes`, `appendStreamResult`, `makeTool`) into root `abi_test_helpers_test.go`.
  - Hardened local guardrails in [scripts/check.ps1](../../scripts/check.ps1), [.githooks/pre-commit](../../.githooks/pre-commit), and [docs/agents/issue-tracker.md](../agents/issue-tracker.md) (see [agent-environment.md](agent-environment.md)).
