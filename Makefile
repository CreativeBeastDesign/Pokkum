.PHONY: help build supervisor test test-short test-integration check-arch lint fmt verify clean

check-arch:  ##  Run hexagonal architecture purity test suite
	@echo "Checking hexagonal architecture purity..."
	@go test -v ./internal/architecture_test.go

test-integration:  ##  Run integration tests (go test ./tests/integration)
	@echo "Running integration tests..."
	@go test -v ./tests/integration

help:  ##  Show this help message
	@echo "Pokkum build targets:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*##"}; {printf "  %-15s %s\n", $$1, $$2}' | sort

build: supervisor  ##  Build the pokkum CLI binary (depends on supervisor)
	@echo "Building pokkum CLI..."
	@VERSION=$$(git describe --tags --always --dirty) && \
	COMMIT=$$(git rev-parse --short HEAD) && \
	TIMESTAMP=$$(git log -1 --pretty=%ct) && \
	DATE=$$(date -u -d @$$TIMESTAMP +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r $$TIMESTAMP +%Y-%m-%dT%H:%M:%SZ) && \
	go build \
		-trimpath \
		-ldflags "-X main.version=$$VERSION -X main.commit=$$COMMIT -X main.buildDate=$$DATE -s -w" \
		-o ./pokkum \
		./cmd/pokkum

SUPERVISOR_BIN := ./internal/adapters/supervisor/bin

# Cross-compile the pokkum-init supervisor binaries for Linux amd64/arm64 and
# embed them zstd-compressed (see internal/adapters/supervisor and
# internal/ports/supervisor.go). Raw ELF binaries are built into a temporary
# staging directory, compressed into $(SUPERVISOR_BIN)/pokkum-init-*.zst, and
# never left behind: go:embed all:bin must only see .zst (plus .gitkeep) so the
# pokkum CLI embeds ~4.7 MB instead of ~12 MB of loose ELF bytes.
supervisor:  ##  Cross-compile + zstd-compress supervisor binaries for Linux amd64/arm64
	@echo "Building + compressing supervisor binaries..."
	@mkdir -p $(SUPERVISOR_BIN)
	@stage=$$(mktemp -d); \
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build \
			-trimpath \
			-ldflags "-s -w" \
			-o $$stage/pokkum-init-linux-$$arch \
			./supervisor/cmd/pokkum-init || { rm -rf $$stage; exit 1; }; \
		go run ./scripts/compress-zstd.go $$stage/pokkum-init-linux-$$arch $(SUPERVISOR_BIN)/pokkum-init-linux-$$arch.zst || { rm -rf $$stage; exit 1; }; \
	done; \
	rm -rf $$stage; \
	rm -f $(SUPERVISOR_BIN)/pokkum-init-linux-amd64 $(SUPERVISOR_BIN)/pokkum-init-linux-arm64
	@echo "Supervisor binaries compressed into $(SUPERVISOR_BIN)/"
	@ls -la $(SUPERVISOR_BIN)

test:  ##  Run all tests (go test ./...)
	@echo "Running tests..."
	@go test ./...

test-short:  ##  Run tests with -short flag (skips network-gated tests)
	@echo "Running short tests..."
	@go test -short ./...

lint:  ##  Run golangci-lint
	@echo "Running linters..."
	@golangci-lint run ./...

fmt:  ##  Format code with gofmt and goimports
	@echo "Formatting code..."
	@gofmt -s -w .
	@go run golang.org/x/tools/cmd/goimports@latest -w .

check-fmt:  ##  Check code formatting with gofmt
	@echo "Checking code formatting..."
	@test -z "$$(gofmt -s -l .)"

verify:  ##  Full agent verification suite: fmt+vet, adapter tests, CLI build, internal tests
	@echo "Step 0/4 - Compiler build optimization flags regression guard..."
	@bash scripts/check-build-flags.sh
	@echo "Step 1/4 - Formatting & static analysis..."
	@gofmt -s -w . && go vet ./...
	@echo "Step 2/4 - Adapter unit tests..."
	@go test ./internal/adapters/...
	@echo "Step 3/4 - CLI compilation check..."
	@go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test
	@echo "Step 4/4 - Full internal test suite (incl. architecture purity)..."
	@go test ./internal/...

clean:  ##  Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f ./pokkum
	@rm -rf ./build
	@rm -f ./internal/adapters/supervisor/bin/pokkum-init-linux-*
