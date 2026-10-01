# Short aliases for the commands run every day. Each target is a plain `go`
# command, so nothing here is magic. Read a target to learn the real command.
#
#   make help     list targets
#   make check    what CI runs; run it before every commit

GOEXE   := $(shell go env GOEXE)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.DEFAULT_GOAL := help
.PHONY: help build run test race vet fmt fmt-check cover tidy check clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

build: ## Compile binaries into bin/ with the version stamped in
	go build -ldflags "$(LDFLAGS)" -o bin/cexd$(GOEXE) ./cmd/cexd
	go build -ldflags "$(LDFLAGS)" -o bin/replay$(GOEXE) ./cmd/replay

run: ## Run the server locally (Ctrl-C for graceful shutdown)
	go run -ldflags "$(LDFLAGS)" ./cmd/cexd

test: ## Run all unit tests
	go test ./...

race: ## Run all tests with the race detector (needs cgo / a C compiler)
	go test -race ./...

vet: ## Static analysis for suspicious constructs
	go vet ./...

fmt: ## Rewrite all files with gofmt
	gofmt -w .

fmt-check: ## Fail if any file is not gofmt-formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

cover: ## Test with coverage and print per-function totals
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

tidy: ## Sync go.mod/go.sum with the imports actually used
	go mod tidy

check: fmt-check vet race ## Everything CI checks

clean: ## Remove build and test output
	rm -rf bin coverage.out
