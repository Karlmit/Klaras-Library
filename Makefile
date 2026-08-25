# Klaras Library
SHELL := /bin/bash
GO    ?= go
BIN   := bin/klaras
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

DEV_DSN  ?= postgres://klaras:klaras@127.0.0.1:55432/klaras?sslmode=disable
TEST_DSN ?= postgres://klaras:klaras@127.0.0.1:55433/klaras_test?sslmode=disable

export KLARAS_DATABASE_URL ?= $(DEV_DSN)
export KLARAS_LIBRARY_ROOT ?= $(CURDIR)/.dev/library
export KLARAS_CACHE_DIR    ?= $(CURDIR)/.dev/cache
export KLARAS_INGEST_DIR   ?= $(CURDIR)/.dev/ingest

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n",$$1,$$2}'

## ---- build ----------------------------------------------------------------

.PHONY: build
build: web-build ## Build the binary with the SPA embedded
	@mkdir -p bin
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/klaras
	@echo "built $(BIN) ($(VERSION))"

.PHONY: build-fast
build-fast: ## Build the Go binary only, skipping the frontend
	@mkdir -p bin
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/klaras

.PHONY: web-build
web-build: ## Build the React SPA into web/dist
	@if [ -f web/package.json ]; then cd web && npm ci --no-audit --no-fund && npm run build; \
	 else echo "web/ not scaffolded yet, skipping"; fi

## ---- dev ------------------------------------------------------------------

.PHONY: dev-up
dev-up: ## Start dev + test Postgres
	docker compose -f docker-compose.dev.yml up -d
	@echo "dev  : $(DEV_DSN)"
	@echo "test : $(TEST_DSN)"

.PHONY: dev-down
dev-down: ## Stop dev databases
	docker compose -f docker-compose.dev.yml down

.PHONY: dev-reset
dev-reset: ## Drop and rebuild the dev schema
	psql "$(DEV_DSN)" -q -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
	$(MAKE) migrate

.PHONY: run
run: build-fast ## Run the server against the dev database
	@mkdir -p $(KLARAS_LIBRARY_ROOT) $(KLARAS_CACHE_DIR) $(KLARAS_INGEST_DIR)
	KLARAS_LOG_LEVEL=debug $(BIN) serve

.PHONY: migrate
migrate: build-fast ## Apply migrations to the dev database
	$(BIN) migrate up

.PHONY: seed
seed: build-fast ## Seed the dev database with 30k synthetic books
	$(BIN) dev-seed --books $${BOOKS:-30000}

## ---- quality --------------------------------------------------------------

.PHONY: test
test: ## Run unit tests (integration tests skip without a test database)
	$(GO) test ./... -race -count=1

.PHONY: test-integration
test-integration: ## Run every test, including those needing Postgres
	KLARAS_TEST_DATABASE_URL='$(TEST_DSN)' $(GO) test ./... -race -count=1 -v

.PHONY: lint
lint: ## Vet and format-check
	$(GO) vet ./...
	@out=$$(gofmt -l . | grep -v '^web/' || true); \
	 if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: tidy
tidy: ## Tidy go.mod
	$(GO) mod tidy

## ---- docker ---------------------------------------------------------------

.PHONY: docker-build
docker-build: ## Build the production image
	docker build -t klaras-library:$(VERSION) .
