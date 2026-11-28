# Agent Environment and Guardrails

Index: [index.md](index.md) | Related: [decisions.md](decisions.md), [omp-pipeline-invariants.md](omp-pipeline-invariants.md)

Historical observations on local tooling, validation scripts, hooks, and architectural guardrails recorded on `2026-11-28` at `monet88/antigravity-cloak@c864f7b`.

## GitNexus Index State

- **Duplicate repo registration (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** Two workspace paths shared the GitNexus repository name `antigravity-cloak` — the primary repo `F:\CodeBase\antigravity-cloak` and a stale review checkout `F:\Temp\cloak-pr46-review-142c6e8`. That collision broke shorthand MCP calls using `repo: "antigravity-cloak"`, requiring explicit `repo: "F:\\CodeBase\\antigravity-cloak"` in MCP tools or `--repo .` with the CLI while the stale entry remained registered.
- **One-commit staleness (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** The GitNexus index sat at `8b7976a` (one commit behind `monet88/antigravity-cloak@c864f7b`); commit `c864f7b` modified only docs, comments, and `plugin_explicit_client_test.go`, leaving production symbol edges unchanged while still reporting `behind`.

## Validation Script and Git Hook Gaps

- **`scripts/check.ps1` tracked-only `gofmt` (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** [scripts/check.ps1](../../scripts/check.ps1) ran `gofmt -l (git ls-files '*.go')`, silently skipping newly created untracked `.go` and `*_test.go` files until staged with `git add`. At `monet88/antigravity-cloak@c864f7b`, the baseline `./scripts/check.ps1` run passed in `13.58s` (`gofmt`, `go vet ./...`, `go test -race -count=1 ./...`, `go test -count=1 ./.github/scripts`, and `python -m compileall -q tests`).
- **`.githooks/pre-commit` deleted-markdown blind spot (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** Local `.git/config` set `core.hookspath=.githooks`, which linked worktrees inherited (running full `go test -race ./...` on every Go commit). In [.githooks/pre-commit](../../.githooks/pre-commit), staged path discovery used `--diff-filter=ACM` and invoked `go test -run TestArchitectureMarkdownLinksAndDocsLayout .` without `-count=1`; staging only a deleted `.md` file bypassed the broken-link check completely.

## `architecture_test.go` Invariants

As of `monet88/antigravity-cloak@c864f7b` (`2026-11-28`), [architecture_test.go](../../architecture_test.go) enforced four automated repository invariants (mirrored in [CODING_STANDARDS.md](../guides/CODING_STANDARDS.md)):

1. **`TestArchitectureImportBoundaries`:** Kept `internal/spec` stdlib-only, restricted `internal/clients/*` to stdlib plus `internal/spec` with `Spec()` as the sole exported function, and forbade `internal/cloak` from importing `internal/clients/*`.
2. **`TestArchitectureCloakExportedSurface`:** Locked `internal/cloak` to an exact 8-symbol exported API (`HandlePluginLifecycle`, `HandleRequestComplete`, `HandleRequestInterceptBefore`, `HandleResponseIntercept`, `HandleStreamChunkIntercept`, `Initialize`, `MustEnvelope`, `MustErrorEnvelope`).
3. **`TestArchitectureNoUnusedProductionDecls`:** Rejected any unused unexported production declaration across `internal/spec`, `internal/cloak`, and `internal/clients/*`, requiring replaced helpers to be deleted in the same change.
4. **`TestArchitectureMarkdownLinksAndDocsLayout`:** Rejected loose `.md` files directly inside `docs/` and verified every relative markdown link and `#anchor` heading across repo `.md` files while skipping `.git`, `.ref`, `dist`, `.worktrees`, and `worktrees`.
