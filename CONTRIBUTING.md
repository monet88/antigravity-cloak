# Contributing to Antigravity Cloak

## Prerequisites

- **Go 1.26.0** with CGO enabled (`CGO_ENABLED=1`).
- **gcc/mingw** on Windows for local `go test` (CGO is required even for
  tests because the package uses `buildmode=c-shared` types).
- **Docker** for building the Linux/amd64 `.so` plugin artifact.
- Familiarity with [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
  v7 plugin ABI (version 1).

## Quick Start

```powershell
# Clone
git clone https://github.com/monet88/antigravity-cloak.git
cd antigravity-cloak

# Run tests
go test ./...
go test ./.github/scripts
go vet ./...

# Build Linux amd64 plugin (via Docker; match gateway glibc with bookworm)
docker run --rm -v ${PWD}:/src -w /src golang:1.26.0-bookworm sh -c \
  "mkdir -p dist && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
   go build -trimpath -buildmode=c-shared \
   -ldflags '-s -w' -o dist/antigravity-cloak.so . && \
   rm -f dist/antigravity-cloak.h"
```

## Repository Layout

| Path | What's there |
| :--- | :--- |
| `main.go` | Thin root C-ABI entrypoint, method dispatcher, and registration metadata |
| `plugin_test.go` | Root black-box dispatcher and `registry.json` parity tests |
| `internal/spec/` | Zero-dependency client specification types |
| `internal/clients/` | Data-only client definitions (`omp`, `claudecode`, `codex`) |
| `internal/cloak/` | Shared cloaking engine (domain-split `.go` files) and owning `*_test.go` suites |
| `tests/` | Integration/e2e scripts (not run by `go test`) |
| `.github/` | CI workflows and release packaging scripts |
| `docs/` | Categorized specs, ADRs, checklists, guides, research, deployment, debugging |
| `CONTEXT.md` | Canonical domain reference |
| `AGENTS.md` | Agent-oriented operational guide |
| `docs/guides/CODING_STANDARDS.md` | Code conventions and patterns |

See [docs/guides/CODING_STANDARDS.md](docs/guides/CODING_STANDARDS.md) for naming conventions, test
patterns, and rules for brand/tool mapping changes.

## Development Workflow

### 1. Understand the domain

Read [CONTEXT.md](CONTEXT.md) for the full domain glossary, per-client
mapping tables, activation model, and architectural contracts. Read
[AGENTS.md](AGENTS.md) for build, deploy, and debug procedures.

### 2. Make changes

- Engine mechanics live in `internal/cloak/` and declarative client specs live in `internal/clients/<client>/`; root `main.go` is a thin C-ABI/dispatcher shell.
- Brand mapping changes: follow the reversibility rules in
  [docs/guides/CODING_STANDARDS.md](docs/guides/CODING_STANDARDS.md#brand-rewriting-rules).
- Tool mapping changes: follow the alias plan rules in
  [docs/guides/CODING_STANDARDS.md](docs/guides/CODING_STANDARDS.md#tool-cloaking-rules).
- Adding a new client: one forward brand table + one reverse table + one
  registry line + detection inventory entries + tests.

### 3. Write tests

- **Required**: every brand mapping change needs a round-trip test (request →
  upstream → response/stream → client).
- **Required**: every tool mapping change needs declaration-level and
  reverse-restoration coverage.
- Place issue-specific regressions in `issue<N>_<slug>_test.go`.
- Place domain-wide coverage in `<domain>_test.go`.
- Test-only helpers go in `*_test.go` files, never in production source.

### 4. Validate locally

Enable the versioned pre-commit hook once per clone:

```powershell
git config core.hooksPath .githooks
./scripts/check.ps1
```

All checks (`gofmt`, `go vet`, `go test -race ./...`, `go test ./.github/scripts`, and `compileall`) must pass before opening a PR.

### 5. Update documentation

| What changed | Update |
| :--- | :--- |
| Client, mapping, or activation model | [CONTEXT.md](CONTEXT.md) |
| Build, deploy, or debug procedure | [AGENTS.md](AGENTS.md) |
| User-visible feature or fix | [CHANGELOG.md](CHANGELOG.md) (`[Unreleased]`) |
| Non-trivial design decision | New ADR in `docs/adr/` |
| Complex subsystem specification | New or updated spec in `docs/specs/` |

### 6. Commit

```
<type>: <summary> (#<issue>)
```

Types: `feat`, `fix`, `refactor`, `docs`, `chore`, `ci`, `test`.
Keep subject ≤ 72 chars. Body explains *why*.

## Building the Plugin Artifact

The plugin runs as a Linux/amd64 C-shared library inside the CLIProxyAPI
Docker container. For the full clean-build and provenance-verification
procedure, see
[docs/verification/verification-checklist.md](docs/verification/verification-checklist.md#2-build-a-clean-linuxamd64-shared-library).

> [!WARNING]
> Read the [silent build/deploy traps](AGENTS.md#build--deploy-silent-traps)
> before your first build. Both traps produce a wrong artifact with no error.

## Live Acceptance

The primary acceptance gate is Oh My Pi (`omp`) against local CLIProxyAPI.
For the full procedure:

- [Deployment and live verification runbook](docs/verification/verification-checklist.md)
- [OMP full tool-cloak checklist](docs/verification/verification-checklist-omp.md)
- [VPS deployment guide](docs/deployment/vps.md)

## CI

`.github/workflows/build.yml` builds the full OS/arch matrix and packages
release zips + `checksums.txt`. Version is derived from the `v*` git tag.

## Architecture Decision Records

Non-trivial design choices are recorded in `docs/adr/`. Current ADRs:

| ADR | Decision |
| :--- | :--- |
| [0001](docs/adr/0001-stream-session-manager-and-atomic-config.md) | Stream session manager and atomic config |
| [0002](docs/adr/0002-ratio-ranked-client-classification-and-session-pre-registration.md) | Ratio-ranked client classification and session pre-registration |
| [0003](docs/adr/0003-sanitize-system-prompt-conventions-tag-for-google-upstream.md) | Sanitize system-prompt conventions tag for Google upstream |
| [0004](docs/adr/0004-cloak-codex-tool-names-by-wire-position.md) | Cloak Codex tool names by wire position |
| [0005](docs/adr/0005-client-modules-and-shared-cloak-engine.md) | Client modules with shared cloaking mechanics (accepted, implemented in #67) |

## License

MIT — see [LICENSE](LICENSE).
