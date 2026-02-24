.PHONY: build test lint migrate-up migrate-down docker-up docker-down generate clean

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

## docker-up: Start local development infrastructure
docker-up:
	docker compose up -d

## docker-down: Stop local development infrastructure
docker-down:
	docker compose down

## generate: Generate OpenAPI spec and other codegen artifacts
generate:
	@echo "OpenAPI generation not yet configured"

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## help: Show this help
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
