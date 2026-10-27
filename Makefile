# Short aliases for the commands run every day. Each target is a plain `go`
# command, so nothing here is magic. Read a target to learn the real command.
#
#   make help     list targets
#   make check    what CI runs; run it before every commit

GOEXE   := $(shell go env GOEXE)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMPOSE := docker compose -f deploy/docker-compose.yml
LDFLAGS := -X main.version=$(VERSION)

.DEFAULT_GOAL := help
.PHONY: help build run test race vet fmt fmt-check cover tidy check clean test-integration db-up db-down up down logs load-tokens load-smoke load-step vuln

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-17s %s\n", $$1, $$2}'

build: ## Compile binaries into bin/ with the version stamped in
	go build -ldflags "$(LDFLAGS)" -o bin/cexd$(GOEXE) ./cmd/cexd
	go build -ldflags "$(LDFLAGS)" -o bin/replay$(GOEXE) ./cmd/replay
	go build -o bin/loadgen$(GOEXE) ./cmd/loadgen

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

test-integration: ## Tests against a real PostgreSQL in Docker (Docker must be running)
	go test -tags=integration -count=1 ./internal/projector ./cmd/cexd

db-up: ## Start ONLY PostgreSQL, for running cexd outside Docker
	$(COMPOSE) up -d --wait postgres

db-down: ## Stop PostgreSQL (its data volume is kept)
	$(COMPOSE) stop postgres

up: ## Build cexd and start the whole stack: exchange, PostgreSQL, Prometheus, Grafana
	$(COMPOSE) up -d --build --wait
	@echo "exchange http://localhost:8080  metrics http://localhost:6060/metrics  prometheus http://localhost:9090  grafana http://localhost:3000"

down: ## Stop the whole stack (data volumes are kept; add -v by hand to wipe them)
	$(COMPOSE) down

logs: ## Follow the exchange's logs
	$(COMPOSE) logs -f cexd

load-tokens: ## Print CEX_API_TOKENS for the load-test accounts
	go run ./cmd/loadgen tokens loadtest/workloads/step.json

load-smoke: ## A few seconds of load against a running server (make up, or make run with load-tokens)
	go run ./cmd/loadgen run -workload loadtest/workloads/smoke.json -out tmp/load-smoke

load-step: ## The step-to-saturation experiment; results in tmp/load-step (copy to evidence/ to keep)
	go run ./cmd/loadgen run -workload loadtest/workloads/step.json -out tmp/load-step -profile-stage 6

vuln: ## Check dependencies for known vulnerabilities (what CI runs)
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

clean: ## Remove build and test output
	rm -rf bin tmp coverage.out
