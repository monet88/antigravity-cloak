# Antigravity Cloak Project Wiki

Persistent historical index for `antigravity-cloak` engineering sessions, seeded on 2026-11-28 at `monet88/antigravity-cloak@c864f7b` during the `#68` Chief-of-Staff session.

Conventions: every page records dated history rather than timeless present-tense claims, cites `monet88/antigravity-cloak@<sha>` beside code observations, and links to source issues or repo docs rather than duplicating them.

## Governance and Decisions

- [decisions.md](decisions.md) — Dated scope, workflow, and architecture decisions logged from `#68` onward (`#69` -> `#70` -> `#71`).

## Subsystems and Invariants

- [omp-pipeline-invariants.md](omp-pipeline-invariants.md) — Historical notes on Oh My Pi (`ProtectedAGY` vs `User-Agent: omp/...`) request seams, sanitization ordering, and preset traps (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`).

## Tooling and Environment

- [agent-environment.md](agent-environment.md) — Historical notes on GitNexus indexing, `scripts/check.ps1`, `.githooks/pre-commit`, and `architecture_test.go` guardrails (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`).

## Canonical Repository References

- [CONTEXT.md](../../CONTEXT.md) — Canonical brand, tool-cloaking, activation, and sanitization contract.
- [GLOSSARY.md](../../GLOSSARY.md) — Domain terminology (`Brand Rewriting`, `Inbound Sanitization`, `ProtectedAGY`, request surfaces).
- [ADR 0006](../adr/0006-omp-inbound-sanitization-surfaces-and-v19-presets.md) — OMP inbound sanitization surfaces, v19.2.1 presets, and `ProtectedAGY` brand parity decision.
- [CODING_STANDARDS.md](../guides/CODING_STANDARDS.md) — Go/CGO constraints, test naming, and commit conventions.
