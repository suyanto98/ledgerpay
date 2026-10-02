# LedgerPay

> A mini payment platform with a double-entry ledger where financial transactions remain consistent even when requests are duplicated, concurrent, or interrupted by failures.

**Status:** 🚧 Sprint 0 (setup & technical spikes). No product code yet. See [`docs/backlog.md`](docs/backlog.md).

Designed as a production-oriented mini system for learning and portfolio purposes: small in scope, strict about correctness.

---

## What this project demonstrates

| Problem | Guarantee | How (planned) |
|---|---|---|
| Duplicate requests (double click, client retry) | One intent → one financial effect | Transactional idempotency keys ([ADR-L02](docs/adr/ADR-L02-idempotency.md)) |
| Concurrent spending from one balance | Balance never negative, no lost update | Ordered row locks + DB constraints ([ADR-L03](docs/adr/ADR-L03-concurrency-pessimistic-locking.md)) |
| Money created or lost by bugs | Every journal balances, ledger is append-only | Double-entry ledger + deferred constraint trigger ([ADR-L01](docs/adr/ADR-L01-double-entry-ledger.md)) |
| Bank timeouts ("did the money leave?") | No blind resubmission, no double payout | Payout state machine with `UNKNOWN` + status query ([ADR-L06](docs/adr/ADR-L06-payout-unknown-outcome.md)) |
| Lost side effects | Webhooks/notifications never lost, never sent without commit | Transactional outbox + job runner ([ADR-006](docs/adr/ADR-006-background-processing-postgres-queue.md)) |

## Architecture (planned)

Modular monolith in Go + PostgreSQL as the single source of truth, a separate mock payment provider with failure injection, and a Vue 3 SPA. See [`docs/README.md`](docs/README.md) for the design documents and ADRs.

## Tech stack

Go · PostgreSQL · sqlc + pgx · goose · Vue 3 + TypeScript · Docker Compose · GitHub Actions · Prometheus + Grafana

## Getting started

Sprint 0 only ships tooling and contract linting:

```bash
make tools        # install sqlc, goose, golangci-lint, actionlint
make lint         # lint OpenAPI contract + GitHub Actions workflows
```

## Limitations (by design)

- Mock payment provider instead of a real payment gateway (deterministic failure injection matters more for testing and demos).
- Single currency (IDR), no KYC, no cards.
- Performance numbers will be published only after they are measured, together with the environment spec.

## License

[MIT](LICENSE)
