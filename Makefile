.PHONY: help build supervisor static-server test test-short test-integration test-race coverage check-coverage fuzz-smoke check-arch check-embedded-blobs lint fmt verify clean e2e-runtime-smoke

check-arch:  ##  Run hexagonal architecture purity test suite
	@echo "Checking hexagonal architecture purity..."
	@go test -v ./internal/architecture_test.go

# Guards against the exact incident logged in Lessons.md's 2026-08-19 "no CI
# job built them" entry recurring in its second half: CI now always rebuilds
# pokkum-init/pokkum-static fresh (see the "Build Embedded PID-1 Binaries"
# step in ci.yml), so a *locally* stale go:embedded blob structurally cannot
# reach CI -- but it absolutely can sit around on a developer's or agent's
# machine after editing supervisor/cmd/pokkum-static or supervisor/cmd/
# pokkum-init without rerunning `make supervisor static-server`, and get
# silently exercised by every subsequent local build/test.
#
# TestEmbeddedPID1Binaries_MatchSource (internal/adapters/staticserver/
# blob_freshness_test.go) rebuilds both binaries from source for every
# embedded platform and diffs the result against what is actually embedded.
# It is invoked here with -count=1 rather than as a plain `go test`: go test
# caches successful results per package, and its cache has no visibility
# into a subprocess `go build` of a *different* package's source (see `go
# help test`'s "rule for a match in the cache") -- so a plain `go test
# ./internal/adapters/...` can replay a stale cached PASS from before the
# source changed. -count=1 is the documented, idiomatic way to force a real
# rerun. This is why `verify` (below) calls this target rather than trusting
# its own "Adapter Unit Tests" step to have covered it.
check-embedded-blobs:  ##  Verify embedded pokkum-init/pokkum-static binaries actually match their source (bypasses go test's result cache; see comment above)
	@echo "Checking embedded PID-1 binaries match their source (forces a real rerun; a plain 'go test' can replay a stale cached PASS here -- see Makefile comment)..."
	@go test -count=1 -run TestEmbeddedPID1Binaries_MatchSource -v ./internal/adapters/staticserver/...

test-integration:  ##  Run integration tests (go test ./tests/integration)
	@echo "Running integration tests..."
	@go test -v ./tests/integration

# TestRuntimeSmoke_LayeredStrategy_BootsAndServes (tests/integration/
# runtime_smoke_test.go) is the one test in this repo that actually boots a
# produced image rather than only asserting layer structure/digests — see
# mem:self_review_checklist row 17 and Lessons.md's missing-entrypoint
# incident for why that gap mattered. It needs a real `bun` on PATH, a
# reachable docker/podman daemon, and network access to pull the real
# default base image; it skips cleanly (not a failure) when any of those
# are unavailable. -timeout is explicit and generous: a real SvelteKit
# build, a real base image pull, and a real container boot together are
# slower than go test's 10m default budgets for comfortably.
e2e-runtime-smoke:  ##  Run the real bun+docker runtime smoke test (boots a produced image, polls /healthz+/readyz)
	@echo "Running runtime smoke test (needs bun on PATH + a reachable docker/podman daemon)..."
	@go test -timeout=8m -v -run TestRuntimeSmoke_LayeredStrategy_BootsAndServes ./tests/integration

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
	@echo ""
	@echo "Extra (beyond the 5 canonical steps) - Embedded PID-1 blob freshness guard..."
	@echo "  Run with -count=1 deliberately: see check-embedded-blobs' comment for why a plain"
	@echo "  'go test' cannot be trusted to catch a stale local blob here."
	@$(MAKE) check-embedded-blobs

clean:  ##  Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f ./pokkum
	@rm -rf ./build
	@rm -f ./internal/adapters/supervisor/bin/pokkum-init-linux-*
	@rm -f ./internal/adapters/staticserver/bin/pokkum-static-linux-*
