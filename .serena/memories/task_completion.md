# Task Completion & Verification Protocol

The verification protocol applies **ONLY when Go source code (`.go` files, build manifests, dependencies) has been created, modified, or refactored**.

- Do **NOT** run the verification test suite for simple user questions, code exploration, planning, or documentation updates (e.g. `docs/roadmap/*.yaml`, `.md` files, docstrings, or memory graph edits).
- Tests must be executed **only before declaring completion of actual code modifications**.

When code changes have occurred, agents **MUST** run the consolidated 5-step suite via the canonical make target:

```bash
make verify
```

`make verify` executes in order (with `BypassSandbox: true` if socket binding / network access fails in standard sandbox):
1. **Formatting & Static Analysis**: `gofmt -s -w . && go vet ./...`
2. **golangci-lint**: `make lint` (or `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`) — catches findings `gofmt`/`go vet`/`go test` alone miss (`errcheck`, `staticcheck`, ...); see `Lessons.md`'s "4-step verification suite does not run golangci-lint" entry for why this step exists.
   - **Do NOT invoke a bare `golangci-lint` from PATH.** As of 2026-08-20 the config is v2 schema (`version: "2"`), which a v1 binary cannot load, and a locally-installed binary may be built with an older Go than `go.mod` targets — golangci-lint then refuses the config and exits non-zero *without linting anything*. This is why the pin is invoked through `go run <module>@<version>`: it builds with the caller's toolchain, so the linter's Go version equals the module's by construction. The same pin appears in the Makefile, `.github/workflows/ci.yml` and `release.yml`; `cmd/pokkum/lintversion_test.go` fails if they drift or if one is downgraded to v1.
   - Both issue caps are set to 0 in `.golangci.yml` on purpose. The defaults (`max-issues-per-linter: 50`, `max-same-issues: 3`) reported 15 findings when there were 57, and varied which ones between runs — never re-enable them.
3. **Adapter Unit Tests**: `go test ./internal/adapters/...`
4. **CLI Compilation Check**: `go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test`
5. **Full Internal Test Suite (includes AST Architecture Purity Check)**: `go test ./internal/...`

`supervisor/` (the `pokkum-init` and `pokkum-static` standalone PID-1 binaries) is part of the same root module — no separate `go.mod`, no `go.work` needed — but `make verify`'s steps above only cover `./internal/...` and `./cmd/pokkum`, not `./supervisor/...`. If a diff touches anything under `supervisor/`, also run `go build ./supervisor/... && go test ./supervisor/...` from the repo root.

**`tests/integration/` golden fixtures are outside `make verify`'s scope too** (found 2026-08-17, see `Lessons.md`): `tests/integration/golden_test.go` pins full OCI manifest/config/index JSON (`testdata/golden/*.json`) independently of `internal/adapters/packager/golden_test.go`'s own golden constants — changing anything that affects compressed layer bytes (gzip/zstd implementation, compression level, layer content) can pass the entire 5-step suite while silently leaving these stale. For any change touching layer compression, tar construction, or OCI manifest/config assembly, also run `go test ./tests/integration/...` (or a full `go test ./...` sweep) before declaring done — and if it fails only on compressed-bytes digests (not DiffIDs/config), regenerate with `go test ./tests/integration/... -run <TestName> -update` and diff the result to confirm only the expected fields moved, the same discipline as re-recording `internal/adapters/packager/golden_test.go`.

Before declaring a non-trivial diff complete, also run it against `mem:self_review_checklist` — a shared, edited-in-place list of hard-to-spot bug patterns (fan-out error paths, resource cleanup on every branch, multi-item/non-first-item test coverage, ordering, clock access). `make verify` passing is necessary but not sufficient: it confirms the tests you wrote pass, not that you wrote the tests that would have caught the bug.