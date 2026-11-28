# Antigravity Cloak Project Wiki

Persistent historical index for `antigravity-cloak` engineering sessions, seeded on `2026-11-28` at `monet88/antigravity-cloak@9c8f885` (from baseline `monet88/antigravity-cloak@c864f7b`) and updated on `2026-11-28` through `monet88/antigravity-cloak@9cfb8df` and the PR `#72` review/retro pass.

Conventions: every page records dated history rather than timeless present-tense claims, cites `monet88/antigravity-cloak@<sha>` beside code observations, and links to source issues or repo docs rather than duplicating them.

## Governance and Decisions

- [decisions.md](decisions.md) — Dated scope, workflow, and architecture decisions logged on `2026-11-28` across `#68` (`#69` -> `#70` -> `#71`), PR `#72`, and the post-`9cfb8df` review/retro pass.

## Subsystems and Invariants

- [omp-pipeline-invariants.md](omp-pipeline-invariants.md) — Historical notes on Oh My Pi (`ProtectedAGY` vs `User-Agent: omp/...`) request seams, sanitization ordering, fused `<system-reminder>` passes, and v19.2.1 presets (`2026-11-28`, `monet88/antigravity-cloak@c864f7b` through `monet88/antigravity-cloak@9cfb8df` + PR `#72` review pass).

## Tooling and Environment

- [agent-environment.md](agent-environment.md) — Historical notes on GitNexus indexing and linked worktrees, `gh` CLI non-TTY behavior, `scripts/check.ps1`, `.githooks/pre-commit`, and `architecture_test.go` guardrails (`2026-11-28`, `monet88/antigravity-cloak@c864f7b` through PR `#72` retro pass).

## Canonical Repository References

- [CONTEXT.md](../../CONTEXT.md) — Canonical brand, tool-cloaking, activation, and sanitization contract.
- [GLOSSARY.md](../../GLOSSARY.md) — Domain terminology (`Brand Rewriting`, `Inbound Sanitization`, `ProtectedAGY`, request surfaces).
- [ADR 0006](../adr/0006-omp-inbound-sanitization-surfaces-and-v19-presets.md) — OMP inbound sanitization surfaces, v19.2.1 presets, and `ProtectedAGY` brand parity decision.
- [CODING_STANDARDS.md](../guides/CODING_STANDARDS.md) — Go/CGO constraints, test naming, inbound sanitization rules, and commit conventions.
