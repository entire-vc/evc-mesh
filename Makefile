.PHONY: build test lint migrate-up migrate-down docker-up docker-down generate clean \
        ci ci-lint ci-test ci-build ci-services-up ci-services-down ci-install-tools

# Binary output directory
BIN_DIR := bin
API_BINARY := $(BIN_DIR)/mesh-api
# No MCP_BINARY here on purpose: the MCP server lives in its own repository
# (entire-vc/evc-mesh-mcp). This repo's copy (cmd/mcp + internal/mcp) was a
# duplicate that had drifted 12 tools behind and was deleted — Mesh #e85e4e05.

# Database connection (matches docker-compose defaults)
DB_DSN ?= postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable

## build: Compile the API binary
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(API_BINARY) ./cmd/api

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

# ── Per-checkout isolation (#89bf0595) ───────────────────────────────────────
# `docker compose -f deploy/docker/mesh/docker-compose.yml` derives its
# default project name from the COMPOSE FILE'S OWN DIRECTORY, not from the
# checkout root — so every checkout of this repo (evc-mesh, evc-mesh-linus,
# evc-mesh-daedalus, every ephemeral per-task worktree) resolved to the same
# project "mesh". A second agent's `make ci` didn't get its own stand: compose
# adopted the first agent's containers under that shared name and recreated
# them, because the `./volumes/*` bind paths (relative to the compose file's
# directory) resolved into a different checkout — the first agent's live data
# silently became empty volumes mid-run.
#
# Fix: derive project name + service ports from the checkout directory's own
# basename, which is already unique per checkout (and per ephemeral
# session-worktree, whose names are randomly prefixed). ?= so an operator can
# still force a specific value.
CHECKOUT_ID          := $(shell printf '%s' '$(notdir $(CURDIR))' | tr 'A-Z' 'a-z' | tr -c 'a-z0-9' '-')
# Modulus must stay under the SMALLEST gap between any two of the four base
# ports below (4223/5437/6383/8223 -> smallest consecutive gap is 946,
# between 5437 and 6383), or two checkouts with different offsets could
# still collide with each other on, say, one's DB_PORT landing on another's
# REDIS_PORT range. 900 leaves comfortable margin.
PORT_OFFSET          := $(shell printf '%s' '$(CHECKOUT_ID)' | cksum | awk '{print $$1 % 900}')
COMPOSE_PROJECT_NAME ?= mesh-ci-$(CHECKOUT_ID)
DB_PORT              ?= $(shell echo $$(( 5437 + $(PORT_OFFSET) )))
REDIS_PORT           ?= $(shell echo $$(( 6383 + $(PORT_OFFSET) )))
NATS_PORT            ?= $(shell echo $$(( 4223 + $(PORT_OFFSET) )))
NATS_MONITOR_PORT    ?= $(shell echo $$(( 8223 + $(PORT_OFFSET) )))
# Deliberately NOT exported file-wide: `docker-prod-up` reads its own
# COMPOSE_PROJECT_NAME=evc-mesh from deploy/docker/mesh/.env
# (docs/self-hosting.md's established convention) via --env-file, and a
# blanket export here would silently override that. ci-project-env below
# writes these same values into that same .env file instead — Compose
# auto-loads it from the compose file's own directory with no flag needed,
# which is what makes them reach `docker compose` without an export.

# Local CI service DSNs — ports follow the per-checkout derivation above, not
# the old hardcoded 5437/6383/4223 (which is still each variable's value for
# the FIRST checkout to claim offset 0, so nothing changes there).
CI_DB_DSN       ?= postgres://mesh:mesh@localhost:$(DB_PORT)/mesh?sslmode=disable
CI_DATABASE_URL ?= postgres://mesh:mesh@localhost:$(DB_PORT)/mesh?sslmode=disable
CI_REDIS_URL    ?= redis://localhost:$(REDIS_PORT)
CI_NATS_URL     ?= nats://localhost:$(NATS_PORT)

DEPLOY_COMPOSE := $(DEPLOY_DIR)/docker-compose.yml

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

## ci-project-env: Write deploy/docker/mesh/.env with this checkout's
## COMPOSE_PROJECT_NAME + ports, if that file doesn't already exist. Compose
## auto-loads .env from the compose file's own directory with NO flags
## needed, so this is what makes a bare `docker compose -f
## deploy/docker/mesh/docker-compose.yml config` (no make involved) resolve
## to a per-checkout project name too — not just `make ci`. Idempotent and
## additive-only: never overwrites an existing .env, since that file doing
## double duty as the self-host PROD stack's config (docs/self-hosting.md's
## `cp .env.prod.example .env`) is a pre-existing, unrelated convention this
## must not clobber.
ci-project-env:
	@if [ ! -f $(DEPLOY_DIR)/.env ]; then \
		printf 'COMPOSE_PROJECT_NAME=%s\nDB_PORT=%s\nREDIS_PORT=%s\nNATS_PORT=%s\nNATS_MONITOR_PORT=%s\n' \
			"$(COMPOSE_PROJECT_NAME)" "$(DB_PORT)" "$(REDIS_PORT)" "$(NATS_PORT)" "$(NATS_MONITOR_PORT)" \
			> $(DEPLOY_DIR)/.env && \
		echo "── Wrote $(DEPLOY_DIR)/.env — project=$(COMPOSE_PROJECT_NAME) db=$(DB_PORT) redis=$(REDIS_PORT) nats=$(NATS_PORT) ──"; \
	fi

## ci-services-up: Start postgres + redis + nats via dev docker-compose.
##
## Reaps stale mesh-ci-* projects from killed sessions FIRST (see
## scripts/ci/reap_stale_ci_containers.py — the EXIT trap in ci-test below
## cannot survive SIGKILL, so this is the backstop that doesn't depend on the
## dying process running any cleanup at all). Runs on every `ci-services-up`
## call, i.e. every time someone is about to start a new CI stack anyway, so
## steady-state container count self-corrects on ordinary fleet usage.
## Best-effort: `|| true` so a bug in the reaper never blocks CI from running.
ci-services-up: ci-project-env
	@echo "── Reaping stale CI containers from killed sessions ─────────────"
	@python3 $(CURDIR)/scripts/ci/reap_stale_ci_containers.py || true
	@echo "── Starting CI services (postgres redis nats) ──────────────────"
	docker compose -f $(DEPLOY_COMPOSE) up -d --wait postgres redis nats
	@echo "── Services ready ✓"

## ci-services-down: Stop AND REMOVE this checkout's CI containers + network
## (`down -v`, not `stop`). `stop` — the previous behavior — left the
## containers and this project's docker network sitting around forever: it
## releases neither, so even a caller that remembered to call it would only
## have paused the leak, not fixed it. See #03eed881: nothing ever called
## this target at all, so 26 abandoned per-checkout networks (~78 containers)
## piled up across every worktree/push on this shared host until Docker's
## whole address pool was exhausted and `ci-services-up` started failing for
## everyone, not just the checkout that leaked.
ci-services-down:
	docker compose -f $(DEPLOY_COMPOSE) down -v

## ci-test: Run migrations + go test ./... -race (requires ci-services-up).
##
## The whole recipe is ONE shell invocation (line-continued with `\`) so a
## single `trap ... EXIT` covers it end to end: services come down whether
## the migration fails, the tests fail, or everything passes. Before this,
## a failure here left the just-started stack (and its network) running
## forever, which is the majority case that produced #03eed881 — a failing
## `make ci` is a normal, frequent outcome, not an edge case worth
## leaving unhandled.
##
## That fix is still incomplete on its own (#01fc9e81): EXIT alone is not
## guaranteed to run for every way this shell dies, and NOTHING can trap
## SIGKILL — the most common way a run actually dies here (hung-turn-
## watchdog, a fiddler relaunch, /switch, Ctrl+C, the rm guard force-killing
## a wedged lane). Two changes:
##   1. Also trap INT/TERM/HUP so a plain kill/Ctrl+C runs the same cleanup
##      instead of relying on bash's default signal disposition to get there.
##      Still necessary, still not sufficient — SIGKILL bypasses this too.
##   2. Write a liveness marker (.ci-active-pid: this shell's own PID plus
##      its own `ps -o lstart=`) before doing anything else, so that a run
##      killed by SIGKILL — which leaves the marker behind pointing at a now-
##      dead PID — can be told apart from a genuinely live run by
##      scripts/ci/reap_stale_ci_containers.py (invoked from ci-services-up),
##      which is the actual backstop for the case this trap can't cover.
ci-test: ci-services-up
	@trap '$(MAKE) ci-services-down; rm -f $(DEPLOY_DIR)/.ci-active-pid' EXIT; \
	trap 'exit 143' INT TERM HUP; \
	printf '%s\n%s\n' "$$$$" "$$(ps -o lstart= -p $$$$)" > $(DEPLOY_DIR)/.ci-active-pid; \
	echo "── Migrations ──────────────────────────────────────────────────"; \
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(CI_DB_DSN)" \
		$(GOOSE) -dir migrations up && \
	echo "── Tests (go test -race) ───────────────────────────────────────" && \
	DATABASE_URL="$(CI_DATABASE_URL)" \
	REDIS_URL="$(CI_REDIS_URL)" \
	NATS_URL="$(CI_NATS_URL)" \
		go test ./... -v -race -coverprofile=coverage.out -covermode=atomic && \
	echo "── Tests OK ✓"

## ci-build: Compile Go binaries + frontend typecheck + build.
ci-build:
	@echo "── Build (Go API) ──────────────────────────────────────────────"
	go build -o /dev/null ./cmd/api
	@echo "── Frontend: typecheck (negative controls first) ────────────────"
	./scripts/assert-typecheck-is-not-vacuous.sh
	./scripts/assert-typecheck-covers-e2e.sh
	cd web && pnpm typecheck
	@echo "── Frontend: build ─────────────────────────────────────────────"
	cd web && pnpm build
	@echo "── Build OK ✓"
