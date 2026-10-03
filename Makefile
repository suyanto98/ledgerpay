# LedgerPay — developer commands. `make help` untuk daftar target.

GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

COMPOSE := docker compose -f deploy/compose.yml
REDOCLY := npx --yes @redocly/cli@1

.PHONY: help tools up down down-volumes logs ps migrate build test test-integration test-all lint lint-go lint-api lint-actions gen gen-check smoke

help: ## Tampilkan daftar target
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

tools: ## Pasang toolchain Go (sqlc, goose, golangci-lint, actionlint)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
	go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

# ------------------------------------------------------------------ local stack
up: ## Jalankan postgres + migrate + api (build ulang image), tunggu sampai sehat
	$(COMPOSE) up -d --build --wait api

down: ## Hentikan stack (data postgres tetap di volume)
	$(COMPOSE) down

down-volumes: ## Hentikan stack DAN hapus data postgres
	$(COMPOSE) down -v

logs: ## Ikuti log api
	$(COMPOSE) logs -f api

ps: ## Status container
	$(COMPOSE) ps

migrate: ## Jalankan migrasi lagi (one-shot container, role owner)
	$(COMPOSE) run --rm migrate

smoke: ## Register → login → GET /me lewat curl ke stack lokal
	./scripts/smoke.sh

# ------------------------------------------------------------------ build & test
build: ## Build binary ke bin/ledgerpay
	CGO_ENABLED=0 go build -trimpath -o bin/ledgerpay ./cmd/ledgerpay

test: ## Unit test (tanpa Docker), dengan race detector
	go test -race -short ./...

test-integration: ## Integration test (butuh Docker: Postgres via testcontainers)
	go test -race -count=1 ./...

# ------------------------------------------------------------------ lint & codegen
lint: lint-go lint-api lint-actions ## Semua lint

lint-go: ## golangci-lint (gosec, depguard, ...)
	golangci-lint run ./...

lint-api: ## Lint kontrak OpenAPI
	$(REDOCLY) lint api/openapi.yaml

lint-actions: ## Lint workflow GitHub Actions
	actionlint

gen: ## Generate kode dari SQL (sqlc)
	sqlc generate

gen-check: gen ## Pastikan kode hasil generate sudah di-commit (dipakai CI)
	git diff --exit-code -- internal/
