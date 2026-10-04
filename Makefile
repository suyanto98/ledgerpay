# LedgerPay — developer commands. `make help` untuk daftar target.

GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

COMPOSE := docker compose -f deploy/compose.yml
REDOCLY := npx --yes @redocly/cli@1

.PHONY: help tools up down down-volumes logs ps migrate build test test-integration test-concurrency test-all lint lint-go lint-api lint-actions gen gen-check smoke demo-a demo-b demo-payment demo-topup web web-check test-e2e

help: ## Tampilkan daftar target
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

tools: ## Pasang toolchain Go (sqlc, goose, golangci-lint, actionlint)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
	go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
	go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

# ------------------------------------------------------------------ local stack
up: ## Jalankan postgres + migrate + api + worker + mock-provider (build ulang image), tunggu sampai siap
	$(COMPOSE) up -d --build --wait api worker mock-provider

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

demo-a: ## Demo A: idempotency (stack harus jalan: make up)
	./scripts/demo/a_idempotency.sh

demo-b: ## Demo B: 100 transfer paralel dari saldo Rp100.000
	./scripts/demo/b_concurrency.sh

demo-topup: ## Demo Sprint 5: top-up lewat mock-provider, webhook duplikat & signature palsu
	./scripts/demo/topup.sh

demo-payment: ## Demo Sprint 4: merchant API key → payment intent → confirm dari dua tab
	./scripts/demo/payment.sh

# ------------------------------------------------------------------ build & test
build: ## Build binary ke bin/ledgerpay
	CGO_ENABLED=0 go build -trimpath -o bin/ledgerpay ./cmd/ledgerpay

test: ## Unit test (tanpa Docker), dengan race detector
	go test -race -short ./...

test-integration: ## Integration test (butuh Docker), termasuk test interleaving (build tag testhooks)
	go test -race -count=1 -tags testhooks ./...

test-concurrency: ## Ulangi test concurrency 20× untuk mendeteksi flaky (sebelum merge, roadmap S3)
	go test -race -count=20 -tags testhooks -run 'Concurrent|Deadlock|Idempotency|LockTimeout|Naive|Confirm|Parallel|LeaseExpired|CommitOutcome|ConnectionDrop' ./internal/app/ ./internal/modules/transfer/

# ------------------------------------------------------------------ frontend
web: ## Jalankan Vue SPA (Vite) di http://localhost:5173, /api di-proxy ke LP_HTTP_PORT
	cd web && yarn install --frozen-lockfile && yarn dev

web-check: ## Typecheck + unit test + build frontend
	cd web && yarn install --frozen-lockfile && yarn typecheck && yarn test:unit && yarn build

test-e2e: ## Playwright E2E (stack harus jalan: make up)
	cd web && yarn install --frozen-lockfile && yarn e2e

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
