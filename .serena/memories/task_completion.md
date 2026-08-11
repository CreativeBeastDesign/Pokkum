# Task Completion & Verification Protocol

When completing a feature or fix in Pokkum, execute the following verification steps:

1. **Unit Test Suite**:
   ```bash
   go test ./internal/adapters/sveltekitutils/... ./internal/adapters/k8s/...
   ```
2. **CLI Build Check**:
   ```bash
   go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test
   ```
3. **Full Internal Test Suite**:
   ```bash
   go test ./internal/...
   ```
