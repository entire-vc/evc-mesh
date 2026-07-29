.PHONY: build test lint migrate-up migrate-down docker-up docker-down generate clean \
        ci ci-lint ci-test ci-build ci-services-up ci-services-down ci-install-tools

# Binary output directory
BIN_DIR := bin
API_BINARY := $(BIN_DIR)/mesh-api
MCP_BINARY := $(BIN_DIR)/mesh-mcp

# Database connection (matches docker-compose defaults)
DB_DSN ?= postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable

## build: Compile API and MCP server binaries
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(API_BINARY) ./cmd/api
	go build -o $(MCP_BINARY) ./cmd/mcp

## build-prod: Cross-compile API binary for Linux/amd64 with embedded build metadata
build-prod:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 go build \
	  -ldflags "-w -s \
	    -X main.BuildSHA=$(shell git rev-parse HEAD) \
	    -X main.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) \
	    -X main.BuildVersion=$(shell git describe --tags --always 2>/dev/null || echo dev) \
	    -X main.BuildEnv=prod" \
	  -o $(API_BINARY) ./cmd/api

## test: Run all tests with race detection
test:
	go test -race -count=1 ./...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## migrate-up: Apply all pending database migrations
migrate-up:
	goose -dir migrations postgres "$(DB_DSN)" up

## migrate-down: Roll back the last database migration
migrate-down:
	goose -dir migrations postgres "$(DB_DSN)" down

DEPLOY_DIR := deploy/docker/mesh

## docker-up: Start local development infrastructure
docker-up:
	cd $(DEPLOY_DIR) && docker compose up -d

## docker-down: Stop local development infrastructure
docker-down:
	cd $(DEPLOY_DIR) && docker compose down

## docker-prod-up: Start production stack (requires deploy/docker/mesh/.env)
docker-prod-up:
	cd $(DEPLOY_DIR) && docker compose -f docker-compose.prod.yml --env-file .env up -d --build

## docker-prod-down: Stop production stack
docker-prod-down:
	cd $(DEPLOY_DIR) && docker compose -f docker-compose.prod.yml --env-file .env down

## generate: Generate OpenAPI spec and other codegen artifacts
generate:
	@echo "OpenAPI generation not yet configured"

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## help: Show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'

# ── Local CI (make ci) ───────────────────────────────────────────────────────
# Mirrors .github/workflows/ci.yml exactly.
# Prerequisite: colima running (`colima start`). Services are auto-started.
#
# Pinned tool versions — must match ci.yml
GOLANGCI_LINT_VERSION := v2.11.3
GOLANGCI_LINT         := $(shell go env GOPATH)/bin/golangci-lint
GOOSE                 := $(shell go env GOPATH)/bin/goose

# Local CI service DSNs (dev docker-compose ports: 5437 / 6383 / 4223)
CI_DB_DSN       ?= postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable
CI_DATABASE_URL ?= postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable
CI_REDIS_URL    ?= redis://localhost:6383
CI_NATS_URL     ?= nats://localhost:4223

DEPLOY_COMPOSE := deploy/docker/mesh/docker-compose.yml

## ci: Full local CI — lint → test → build. Same gates as .github/workflows/ci.yml.
ci: ci-install-tools ci-lint ci-test ci-build
	@echo ""
	@echo "✅  make ci PASSED — safe to push"

## ci-install-tools: Ensure pinned tool versions are installed (golangci-lint + goose).
ci-install-tools:
	@if ! $(GOLANGCI_LINT) version 2>/dev/null | grep -qF "$(GOLANGCI_LINT_VERSION:v%=%)"; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi
	@if ! command -v $(GOOSE) >/dev/null 2>&1; then \
		echo "Installing goose..."; \
		go install github.com/pressly/goose/v3/cmd/goose@latest; \
	fi

## ci-lint: Run golangci-lint (pinned v2.11.3, matches ci.yml).
ci-lint:
	@echo "── Lint (golangci-lint $(GOLANGCI_LINT_VERSION)) ──────────────────────"
	$(GOLANGCI_LINT) run ./...
	@echo "── Lint OK ✓"

## ci-services-up: Start postgres + redis + nats via dev docker-compose.
ci-services-up:
	@echo "── Starting CI services (postgres redis nats) ──────────────────"
	docker compose -f $(DEPLOY_COMPOSE) up -d --wait postgres redis nats
	@echo "── Services ready ✓"

## ci-services-down: Stop CI services.
ci-services-down:
	docker compose -f $(DEPLOY_COMPOSE) stop postgres redis nats

## ci-test: Run migrations + go test ./... -race (requires ci-services-up).
ci-test: ci-services-up
	@echo "── Migrations ──────────────────────────────────────────────────"
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(CI_DB_DSN)" \
		$(GOOSE) -dir migrations up
	@echo "── Tests (go test -race) ───────────────────────────────────────"
	DATABASE_URL="$(CI_DATABASE_URL)" \
	REDIS_URL="$(CI_REDIS_URL)" \
	NATS_URL="$(CI_NATS_URL)" \
		go test ./... -v -race -coverprofile=coverage.out -covermode=atomic
	@echo "── Tests OK ✓"

## ci-build: Compile Go binaries + frontend typecheck + build.
ci-build:
	@echo "── Build (Go API + MCP) ────────────────────────────────────────"
	go build -o /dev/null ./cmd/api ./cmd/mcp
	@echo "── Frontend: typecheck ─────────────────────────────────────────"
	cd web && pnpm typecheck
	@echo "── Frontend: build ─────────────────────────────────────────────"
	cd web && pnpm build
	@echo "── Build OK ✓"
