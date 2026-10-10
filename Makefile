# LedgerPay — developer commands. `make help` untuk daftar target.

GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

COMPOSE := docker compose -f deploy/compose.yml
REDOCLY := npx --yes @redocly/cli@1

.PHONY: help tools up down down-volumes logs ps migrate build test test-integration test-concurrency test-all lint lint-go lint-api lint-actions lint-shell gen gen-check smoke demo-a demo-b demo-c demo-d demo-payment demo-ops demo-verify demo-topup web web-check test-e2e up-obs test-alerts dashboards perf-up perf-seed perf-run perf-down api-docs cover

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

demo-c: ## Demo C: payout timeout_after_process + docker kill worker → tetap 1 transfer
	./scripts/demo/c_payout_recovery.sh

demo-d: ## Demo D: payout UNKNOWN → settlement bank SUCCEEDED → recon STATUS_MISMATCH → ops resolve dengan bukti
	./scripts/demo/d_reconciliation.sh

demo-payment: ## Demo Sprint 4: merchant API key → payment intent → confirm dari dua tab
	./scripts/demo/payment.sh

demo-ops: ## Demo Sprint 8: refund paralel + ops resolve payout UNKNOWN dengan bukti
	./scripts/demo/ops.sh

demo-verify: ## Semua demo headless (nightly CI); berhenti di kegagalan pertama
	./scripts/demo/a_idempotency.sh && ./scripts/demo/b_concurrency.sh && ./scripts/demo/payment.sh \
	  && ./scripts/demo/topup.sh && ./scripts/demo/ops.sh && ./scripts/demo/d_reconciliation.sh \
	  && ./scripts/demo/c_payout_recovery.sh

api-docs: ## Dokumentasi API statis dari backend/api/openapi.yaml → build/api.html
	mkdir -p build && cd backend && npx --yes @redocly/cli@1 build-docs api/openapi.yaml -o ../build/api.html

cover: ## Laporan coverage (indikatif, bukan gate) → build/coverage.html
	mkdir -p build && cd backend && go test -tags testhooks -coverprofile=../build/coverage.out ./internal/... >/dev/null && \
	  go tool cover -func=../build/coverage.out | tail -1 && go tool cover -html=../build/coverage.out -o ../build/coverage.html

# ------------------------------------------------------------------ perf (docs/design/23-performance-plan.md)
PERF = docker compose -p ledgerpay-perf -f deploy/compose.yml -f deploy/perf/compose.perf.yml

perf-up: ## Perf env lokal (project terpisah, pg_stat_statements, pprof, rate limit off)
	$(PERF) up -d --build --wait api worker mock-provider

perf-seed: ## Seed dataset perf (100k user, 1 juta journal) + gate invariant; PROFILE=smoke untuk kecil
	$(PERF) --profile seed run --build --rm seed $${PROFILE:-perf}

perf-run: ## Satu skenario: make perf-run S=lp2|lp3|lp4|lp4-control|lp9 (bukti di perf/results/)
	./perf/run.sh $(S)

perf-down: ## Hentikan perf env DAN hapus datanya
	$(PERF) --profile seed down -v

# ------------------------------------------------------------------ build & test
build: ## Build binary ke bin/ledgerpay
	cd backend && CGO_ENABLED=0 go build -trimpath -o ../bin/ledgerpay ./cmd/ledgerpay

test: ## Unit test (tanpa Docker), dengan race detector
	cd backend && go test -race -short ./...

test-integration: ## Integration test (butuh Docker), termasuk test interleaving (build tag testhooks)
	cd backend && go test -race -count=1 -tags testhooks ./...

test-concurrency: ## Ulangi test concurrency 20× untuk mendeteksi flaky (sebelum merge, roadmap S3)
	cd backend && go test -race -count=20 -tags testhooks -run 'Concurrent|Deadlock|Idempotency|LockTimeout|Naive|Confirm|Parallel|LeaseExpired|CommitOutcome|ConnectionDrop|Payout|JobRunner|MerchantWebhook' ./internal/app/ ./internal/modules/transfer/ ./internal/platform/jobs/

# ------------------------------------------------------------------ observability
up-obs: ## Stack + Prometheus/Pushgateway/Grafana/Jaeger (Grafana http://localhost:3000, Jaeger http://localhost:16686)
	LP_OTLP_ENDPOINT=http://jaeger:4318/v1/traces $(COMPOSE) --profile obs up -d --build --wait api worker mock-provider pushgateway prometheus grafana jaeger

dashboards: ## Generate JSON dashboard Grafana dari deploy/grafana/gen_dashboards.py
	python3 deploy/grafana/gen_dashboards.py

test-alerts: ## Validasi + unit test alert rules Prometheus (promtool)
	docker run --rm -v "$$PWD/deploy/prometheus/rules:/r:ro" -w /r --entrypoint promtool prom/prometheus:v3.5.0 check rules ledgerpay.yml platform.yml
	docker run --rm -v "$$PWD/deploy/prometheus:/p:ro" -w /p/tests --entrypoint promtool prom/prometheus:v3.5.0 test rules ledgerpay_test.yml backup_test.yml recon_test.yml

# ------------------------------------------------------------------ frontend
web: ## Jalankan Vue SPA (Vite) di http://localhost:5173, /api di-proxy ke LP_HTTP_PORT
	cd frontend && yarn install --frozen-lockfile && yarn dev

web-check: ## Typecheck + unit test + build frontend
	cd frontend && yarn install --frozen-lockfile && yarn typecheck && yarn test:unit && yarn build

test-e2e: ## Playwright E2E (stack harus jalan: make up)
	cd frontend && yarn install --frozen-lockfile && yarn e2e

# ------------------------------------------------------------------ lint & codegen
lint: lint-go lint-api lint-actions lint-shell ## Semua lint

lint-go: ## golangci-lint (gosec, depguard, ...)
	cd backend && golangci-lint run ./...

lint-shell: ## shellcheck semua skrip (deploy + demo)
	docker run --rm -v "$$PWD:/w:ro" -w /w koalaman/shellcheck:stable -x -S warning scripts/*.sh scripts/demo/*.sh deploy/prod/*.sh deploy/backup/*.sh

lint-api: ## Lint kontrak OpenAPI
	cd backend && $(REDOCLY) lint api/openapi.yaml

lint-actions: ## Lint workflow GitHub Actions
	actionlint

gen: ## Generate kode dari SQL (sqlc)
	cd backend && sqlc generate

gen-check: gen ## Pastikan kode hasil generate sudah di-commit (dipakai CI)
	git diff --exit-code -- backend/internal/
