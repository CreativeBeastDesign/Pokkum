# Task Completion & Verification Protocol

The verification protocol applies **ONLY when Go source code (`.go` files, build manifests, dependencies) has been created, modified, or refactored**.

- Do **NOT** run the verification test suite for simple user questions, code exploration, planning, or documentation updates (e.g. `Roadmap.md`, `.md` files, docstrings, or memory graph edits).
- Tests must be executed **only before declaring completion of actual code modifications**.

When code changes have occurred, agents **MUST** run the consolidated 4-step suite via the canonical make target:

```bash
make verify
```

`make verify` executes in order (with `BypassSandbox: true` if socket binding / network access fails in standard sandbox):
1. **Formatting & Static Analysis**: `gofmt -s -w . && go vet ./...`
2. **Adapter Unit Tests**: `go test ./internal/adapters/...`
3. **CLI Compilation Check**: `go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test`
4. **Full Internal Test Suite (includes AST Architecture Purity Check)**: `go test ./internal/...`