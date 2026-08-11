# Task Completion & Verification Protocol

When Go source code (`.go` files, build manifests, dependencies) has been created, modified, or refactored in Pokkum, execute the following 3-step verification protocol (with `BypassSandbox: true` if socket binding / network access fails in standard sandbox):

1. **Adapter Tests**:
   ```bash
   go test ./internal/adapters/...
   ```
2. **CLI Compilation Check**:
   ```bash
   go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test
   ```
3. **Full Internal Test Suite**:
   ```bash
   go test ./internal/...
   ```
