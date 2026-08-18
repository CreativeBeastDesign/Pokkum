.PHONY: help build supervisor static-server test test-short test-integration test-race coverage check-coverage fuzz-smoke check-arch lint fmt verify clean

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

build: supervisor static-server  ##  Build the pokkum CLI binary (depends on supervisor + static-server)
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

STATIC_BIN := ./internal/adapters/staticserver/bin

# Cross-compile the pokkum-static PID-1 static file server binaries for Linux
# amd64/arm64 and embed them zstd-compressed (see internal/adapters/staticserver
# and internal/ports/staticserver.go), mirroring the supervisor build exactly.
# Raw ELF binaries are built into a temporary staging directory, compressed into
# $(STATIC_BIN)/pokkum-static-*.zst, and never left behind: go:embed all:bin
# must only see .zst (plus .gitkeep).
static-server:  ##  Cross-compile + zstd-compress static server binaries for Linux amd64/arm64
	@echo "Building + compressing static server binaries..."
	@mkdir -p $(STATIC_BIN)
	@stage=$$(mktemp -d); \
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build \
			-trimpath \
			-ldflags "-s -w" \
			-o $$stage/pokkum-static-linux-$$arch \
			./supervisor/cmd/pokkum-static || { rm -rf $$stage; exit 1; }; \
		go run ./scripts/compress-zstd.go $$stage/pokkum-static-linux-$$arch $(STATIC_BIN)/pokkum-static-linux-$$arch.zst || { rm -rf $$stage; exit 1; }; \
	done; \
	rm -rf $$stage; \
	rm -f $(STATIC_BIN)/pokkum-static-linux-amd64 $(STATIC_BIN)/pokkum-static-linux-arm64
	@echo "Static server binaries compressed into $(STATIC_BIN)/"
	@ls -la $(STATIC_BIN)

test:  ##  Run all tests (go test ./...)
	@echo "Running tests..."
	@go test ./...

test-short:  ##  Run tests with -short flag (skips network-gated tests)
	@echo "Running short tests..."
	@go test -short ./...

# Packages where concurrency actually lives: the registry mount observer
# (internal/adapters/registry/mount_test.go's mountStats concurrency test
# specifically documents itself as needing -race to exercise its guarantees),
# domain pipeline orchestration (internal/core), the layer packager, and the
# supervisor PID-1 binaries (pokkum-init/pokkum-static) which do their own
# process reaping/signal handling. Running the FULL suite under -race is
# 2-3x slower for comparatively little payoff outside these packages, so CI
# scopes -race to just this set rather than ./....
RACE_PACKAGES := ./internal/adapters/registry/... ./internal/core/... ./internal/adapters/packager/... ./supervisor/...

test-race:  ##  Run go test -race, scoped to packages with real concurrency
	@echo "Running race detector on concurrency-bearing packages..."
	@go test -race $(RACE_PACKAGES)

COVERAGE_OUT := coverage.out

coverage:  ##  Generate a coverage profile (coverage.out) across the whole module
	@echo "Generating coverage profile..."
	@go test -covermode=atomic -coverpkg=./... -coverprofile=$(COVERAGE_OUT) ./...

check-coverage:  ##  Enforce the coverage floor against an existing coverage.out
	@bash scripts/check-coverage.sh $(COVERAGE_OUT)

fuzz-smoke:  ##  Run every FuzzXxx target briefly (30s each); see scripts/run-fuzz.sh
	@bash scripts/run-fuzz.sh

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

verify:  ##  Full agent verification suite: fmt+vet, lint, adapter tests, CLI build, internal tests
	@echo "Step 0/5 - Compiler build optimization flags regression guard..."
	@bash scripts/check-build-flags.sh
	@echo "Step 1/5 - Formatting & static analysis..."
	@gofmt -s -w . && go vet ./...
	@echo "Step 2/5 - golangci-lint (catches findings gofmt/vet/test don't, e.g. errcheck, staticcheck)..."
	@golangci-lint run ./...
	@echo "Step 3/5 - Adapter unit tests..."
	@go test ./internal/adapters/...
	@echo "Step 4/5 - CLI compilation check..."
	@go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test
	@echo "Step 5/5 - Full internal test suite (incl. architecture purity)..."
	@go test ./internal/...

clean:  ##  Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f ./pokkum
	@rm -rf ./build
	@rm -f ./internal/adapters/supervisor/bin/pokkum-init-linux-*
	@rm -f ./internal/adapters/staticserver/bin/pokkum-static-linux-*
