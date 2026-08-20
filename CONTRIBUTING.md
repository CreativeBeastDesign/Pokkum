# Contributing to Pokkum

Thank you for helping improve Pokkum. This guide outlines how to set up your development environment, run the verification suite, and maintain the project's documentation.

## Prerequisites

**Go version:** 1.26.6 (from `go.mod`)

Install Go 1.26.6 or later. Check your version:

```bash
go version
```

**Other tools:**

- `golangci-lint` (for linting; used by `make verify`)
- `git` (for version control)
- `make` (for build targets)

Install golangci-lint following the [official guide](https://golangci-lint.run/usage/install/).

## Building Pokkum

```bash
make build
```

This compiles the `pokkum` CLI binary. It depends on the supervisor and static-server binaries being built first (done automatically).

To clean build artifacts:

```bash
make clean
```

## Verification Suite

Before declaring any code change complete, you **must** run the full verification suite:

```bash
make verify
```

This runs five steps in order:

1. **Formatting & Static Analysis** — `gofmt -s -w . && go vet ./...`
   - Ensures consistent code style and catches obvious errors

2. **golangci-lint** — `make lint`
   Use `make lint` rather than a `golangci-lint` you already have installed. The config uses the v2 schema and the version is pinned and
   built from source via `go run`, so it always matches the Go version in `go.mod`. An older binary will refuse the config and exit without
   linting anything, which looks like a tooling error rather than the silent no-op it is.
   - Runs extended linters (`errcheck`, `staticcheck`, etc.) that `gofmt`/`go vet` miss
   - This step alone has caught multiple real bugs in this codebase; skipping it is not safe

3. **Adapter Unit Tests** — `go test ./internal/adapters/...`
   - Tests concrete implementations of domain ports

4. **CLI Compilation Check** — `go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test`
   - Ensures the CLI compiles without errors

5. **Full Internal Test Suite** — `go test ./internal/...`
   - Runs all internal tests, including the architecture purity verifier (`internal/architecture_test.go`)
   - This enforces hexagonal boundaries (ports may not import adapters, etc.)

### Extra Steps

After the five canonical steps, `make verify` also runs:

- **Embedded PID-1 Blob Freshness** (`make check-embedded-blobs`)
  - Ensures `pokkum-init` and `pokkum-static` binaries match their source
  - Run with `-count=1` to bypass go test's result cache

- **Roadmap Docs Freshness** (`make check-docs-freshness`)
  - Ensures generated docs match the YAML source

### Supervisor Binaries

The supervisor directory (`supervisor/cmd/pokkum-init` and `supervisor/cmd/pokkum-static`) shares the root `go.mod` but is not covered by the five-step suite. If your changes touch `supervisor/`, also run:

```bash
go build ./supervisor/...
go test ./supervisor/...
```

## Documentation

### Generated Docs vs. Source

**These files are GENERATED — do not edit them by hand:**

- `docs/Roadmap.md`
- `docs/Shipped.md`
- `docs/Features.md`
- `docs/items/*.md`

**Regenerate them from YAML:**

```bash
make docs
```

This reads `docs/roadmap/*.yaml` (the single source of truth) and generates all four above.

**Guard against drift:**

```bash
make check-docs-freshness
```

This command fails the build if your changes go out of sync with `docs/roadmap/*.yaml`. Wired into `make verify`.

### Documentation You Can Edit

- **`README.md`** — Project overview and quick-start guide
- **`CONTRIBUTING.md`** — This file (contributor guidelines)
- **`ARCHITECTURE.md`** — Hexagonal layer descriptions and architectural invariants
- **`Vocabulary.md`** — Complete CLI flag reference and environment variables
- **`Lessons.md`** — Post-mortems for bugs caught during development (append new entries)
- **`docs/archive/`** — Read-only historical documents

### House Style

Pokkum's documentation is direct and honest:

- State limitations alongside features; don't market around them
- Prefer specific examples over generic descriptions
- Link to relevant code (`internal/adapters/...`, `cmd/pokkum/...`)
- Keep descriptions brief; use tables for structured information

See `README.md`, `docs/Features.md`, and `docs/archive/README.md` for examples.

## Common Tasks

### Running Tests

Run all tests:

```bash
make test
```

Run tests with network/slow tests skipped (`-short`):

```bash
make test-short
```

Run the concurrency-sensitive race detector (scoped to packages with real concurrency):

```bash
make test-race
```

Run the real Docker boot smoke test (requires bun on PATH, a reachable docker/podman daemon, and network access):

```bash
make e2e-runtime-smoke
```

### Code Formatting

Format all code:

```bash
make fmt
```

Check formatting without modifying:

```bash
make check-fmt
```

### Linting

Run all linters (included in `make verify`):

```bash
make lint
```

## Git Workflow

- **Keep commits logical** — if your change has separable phases (e.g., "add interface" vs. "wire it into the resolver" vs. "fix bug found during self-review"), make separate commits rather than one large squash
- **Write clear commit messages** — describe *what* and *why*, not just *what*
- **Review your own diff** — before committing, read your changes line by line against the spec
- **Don't commit uncommitted work to stash** — if you leave the work uncommitted, it can be lost; commit it with a message, or explicitly note in a discussion that it's deliberately held off

## Pull Request Checklist

Before opening a PR:

- [ ] `make verify` passes
- [ ] New Go code has tests (if adding functionality)
- [ ] Documentation is updated (README.md, ARCHITECTURE.md, Vocabulary.md if flags/env-vars changed)
- [ ] If `docs/roadmap/*.yaml` content changed, run `make docs` to regenerate
- [ ] Commits are logical and messages are clear
- [ ] You've read through your own diff to catch edge cases

## Questions?

- **Architecture**: See `ARCHITECTURE.md`
- **CLI flags & env vars**: See `Vocabulary.md`
- **Known issues & lessons learned**: See `Lessons.md`

---

Thank you for contributing to supply-chain security.
