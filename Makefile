# LedgerPay — developer commands.
# Sprint 0: hanya target tooling & lint. Target app (up, test, migrate, ...) ditambahkan di Sprint 1.

GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

REDOCLY := npx --yes @redocly/cli@1

.PHONY: help tools lint lint-api lint-actions

help: ## Tampilkan daftar target
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

tools: ## Pasang toolchain Go (sqlc, goose, golangci-lint, actionlint)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install github.com/rhysd/actionlint/cmd/actionlint@latest

lint: lint-api lint-actions ## Semua lint yang tersedia di Sprint 0

lint-api: ## Lint kontrak OpenAPI (Redocly, ruleset recommended)
	$(REDOCLY) lint api/openapi.yaml

lint-actions: ## Lint workflow GitHub Actions
	actionlint
