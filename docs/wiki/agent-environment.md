# Agent Environment and Guardrails

Index: [index.md](index.md) | Related: [decisions.md](decisions.md), [omp-pipeline-invariants.md](omp-pipeline-invariants.md)

Historical observations on local tooling, validation scripts, hooks, and architectural guardrails recorded on `2026-11-28` from `monet88/antigravity-cloak@c864f7b` through `monet88/antigravity-cloak@9cfb8df` and the PR `#72` retro pass.

## GitNexus Index State

- **Duplicate repo registration (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** Two local checkouts shared the GitNexus repository name `antigravity-cloak` — the primary local repo checkout and a stale PR-review checkout. That collision broke shorthand MCP calls using `repo: "antigravity-cloak"`, requiring an explicit checkout path in MCP tools or `--repo <primary-checkout>` / `--repo .` with the CLI while the stale entry remained registered.
- **One-commit staleness (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`):** The GitNexus index sat at `8b7976a` (one commit behind `monet88/antigravity-cloak@c864f7b`); commit `c864f7b` modified only docs, comments, and `plugin_explicit_client_test.go`, leaving production symbol edges unchanged while still reporting `behind`.
- **Linked-worktree `detect-changes` false-green (`2026-11-28`, PR `#72`, `monet88/antigravity-cloak@9cfb8df`):** Running `gitnexus detect-changes` from a linked git worktree checked the primary checkout's registered index rather than the linked worktree's uncommitted diff or branch HEAD, producing false-green zero-change reports. Worktree reviews required inspecting the worktree's real `git diff` directly alongside upstream `impact` queries against the indexed repo.

## GitHub CLI Issue-Tracker Trap

- **`gh issue view --comments` 0-byte non-TTY output (`2026-11-28`, PR `#72` retro after `monet88/antigravity-cloak@9cfb8df`):** In `gh` `2.101.0`, `gh issue view <number> --comments` wrote 0 bytes in non-TTY pipes on issues with zero comments. [docs/agents/issue-tracker.md](../agents/issue-tracker.md) switched to `gh issue view <number> --json number,title,state,labels,body,comments` for deterministic non-TTY issue reads.

## Validation Script and Git Hook Gaps

- **`scripts/check.ps1` tracked-only `gofmt` (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`, superseded on `2026-11-28` in PR `#72` retro):** [scripts/check.ps1](../../scripts/check.ps1) ran `gofmt -l (git ls-files '*.go')`, silently skipping newly created untracked `.go` and `*_test.go` files until staged with `git add`. At `monet88/antigravity-cloak@c864f7b`, the baseline `./scripts/check.ps1` run passed in `13.58s` (`gofmt`, `go vet ./...`, `go test -race -count=1 ./...`, `go test -count=1 ./.github/scripts`, and `python -m compileall -q tests`).
- **`scripts/check.ps1` untracked `.go` fix (`2026-11-28`, PR `#72` retro after `monet88/antigravity-cloak@9cfb8df`):** Updated [scripts/check.ps1](../../scripts/check.ps1) to discover Go files via `git ls-files --cached --others --exclude-standard '*.go'` so new untracked `.go` files would fail formatting checks before `git add`.
- **`.githooks/pre-commit` deleted-markdown blind spot (`2026-11-28`, `monet88/antigravity-cloak@c864f7b`, superseded on `2026-11-28` in PR `#72` retro):** Local `.git/config` set `core.hookspath=.githooks`, which linked worktrees inherited (running full `go test -race ./...` on every Go commit). In [.githooks/pre-commit](../../.githooks/pre-commit), staged path discovery used `--diff-filter=ACM` and invoked `go test -run TestArchitectureMarkdownLinksAndDocsLayout .` without `-count=1`; staging only a deleted `.md` file bypassed the broken-link check completely.
- **`.githooks/pre-commit` deleted-markdown and cache fix (`2026-11-28`, PR `#72` retro after `monet88/antigravity-cloak@9cfb8df`):** Updated [.githooks/pre-commit](../../.githooks/pre-commit) to use `--no-renames --diff-filter=ACMD` for staged paths and `.md` link triggers and passed `-count=1` to `go test -count=1 -run TestArchitectureMarkdownLinksAndDocsLayout .`.

## `architecture_test.go` Invariants

As of `monet88/antigravity-cloak@c864f7b` (`2026-11-28`, still enforced at `monet88/antigravity-cloak@9cfb8df`), [architecture_test.go](../../architecture_test.go) enforced four automated repository invariants (mirrored in [CODING_STANDARDS.md](../guides/CODING_STANDARDS.md)):

1. **`TestArchitectureImportBoundaries`:** Kept `internal/spec` stdlib-only, restricted `internal/clients/*` to stdlib plus `internal/spec` with `Spec()` as the sole exported function, and forbade `internal/cloak` from importing `internal/clients/*`.
2. **`TestArchitectureCloakExportedSurface`:** Locked `internal/cloak` to an exact 8-symbol exported API (`HandlePluginLifecycle`, `HandleRequestComplete`, `HandleRequestInterceptBefore`, `HandleResponseIntercept`, `HandleStreamChunkIntercept`, `Initialize`, `MustEnvelope`, `MustErrorEnvelope`).
3. **`TestArchitectureNoUnusedProductionDecls`:** Rejected any unused unexported production declaration across `internal/spec`, `internal/cloak`, and `internal/clients/*`, requiring replaced helpers to be deleted in the same change.
4. **`TestArchitectureMarkdownLinksAndDocsLayout`:** Rejected loose `.md` files directly inside `docs/` and verified every relative markdown link and `#anchor` heading across repo `.md` files while skipping `.git`, `.ref`, `dist`, `.worktrees`, and `worktrees`.
