.PHONY: help build supervisor test test-short lint fmt clean

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

supervisor:  ##  Cross-compile supervisor binaries for Linux amd64/arm64
	@echo "Building supervisor binaries..."
	@mkdir -p ./internal/adapters/supervisor/bin
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-trimpath \
		-ldflags "-s -w" \
		-o ./internal/adapters/supervisor/bin/pokkum-init-linux-amd64 \
		./supervisor/cmd/pokkum-init
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
		-trimpath \
		-ldflags "-s -w" \
		-o ./internal/adapters/supervisor/bin/pokkum-init-linux-arm64 \
		./supervisor/cmd/pokkum-init

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

clean:  ##  Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -f ./pokkum
	@rm -rf ./build
	@rm -f ./internal/adapters/supervisor/bin/pokkum-init-linux-*
