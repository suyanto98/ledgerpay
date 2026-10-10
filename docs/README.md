# LedgerPay — Documentation

> **Layout (2026-10-10):** Go module in `backend/` (`cmd/`, `internal/`, `db/`, `api/`), Vue SPA in `frontend/` (same convention as PayRun). Design documents written earlier refer to the old top-level paths (`internal/…`, `web/…`); read them as `backend/internal/…` and `frontend/…`.

Design documents were written before coding, step by step (product → architecture → domain → database → API → security → transactions → observability/testing → DevOps → roadmap → final review). This folder contains the subset relevant to LedgerPay.

> **Living docs.** If implementation proves a design wrong, the PR that changes behaviour also updates the doc or adds an ADR amendment.
> **Authority when two docs disagree:** [`design/29-final-review.md`](design/29-final-review.md) and the "Amandemen Step 12" banner in [`design/27-roadmap.md`](design/27-roadmap.md) → "Amandemen" sections inside ADRs → later design docs over earlier ones.
> Cross-links inside the documents still use the original archive naming (`Step N`, `step-0N-…/file.md`); use the mapping table below.

## Start here
1. [`design/00-prd.md`](design/00-prd.md): what we build and why
2. [`design/27-roadmap.md`](design/27-roadmap.md): sprint plan (read the Step 12 amendment banner first)
3. [`design/28-definition-of-done.md`](design/28-definition-of-done.md): what "done" means (M3 subset in §2)

## Architecture Decision Records — [`adr/`](adr/)

| ADR | Topic |
|---|---|
| [ADR-001](adr/ADR-001-modular-monolith.md) | Modular monolith |
| [ADR-002](adr/ADR-002-tech-stack-go-vue.md) | Go + Vue 3 |
| [ADR-003](adr/ADR-003-rest-api-openapi.md) | REST + spec-first OpenAPI, RFC 9457 errors |
| [ADR-004](adr/ADR-004-postgresql.md) | PostgreSQL as the only source of truth |
| [ADR-005](adr/ADR-005-data-access-sqlc.md) | sqlc + pgx, goose migrations |
| [ADR-006](adr/ADR-006-background-processing-postgres-queue.md) | Postgres-backed outbox/job queue (+ fencing) |
| [ADR-007](adr/ADR-007-redis-not-in-v1.md) | No Redis in V1 |
| [ADR-008](adr/ADR-008-caching.md) | No application cache in V1 |
| [ADR-009](adr/ADR-009-authentication.md) | JWT access + rotating refresh tokens, API keys |
| [ADR-010](adr/ADR-010-authorization.md) | RBAC + resource policies, 404 vs 403 |
| [ADR-011](adr/ADR-011-transaction-management.md) | Read Committed + explicit locks, no network in tx |
| [ADR-012](adr/ADR-012-object-storage.md) | Object storage (PayRun only) |
| [ADR-013](adr/ADR-013-deployment.md) | Docker Compose on one VM + CI/CD |
| [ADR-L01](adr/ADR-L01-double-entry-ledger.md) | Double-entry ledger, append-only |
| [ADR-L02](adr/ADR-L02-idempotency.md) | Transactional idempotency |
| [ADR-L03](adr/ADR-L03-concurrency-pessimistic-locking.md) | Ordered pessimistic locking (`FOR NO KEY UPDATE`) |
| [ADR-L04](adr/ADR-L04-money-representation.md) | Money as BIGINT minor units |
| [ADR-L05](adr/ADR-L05-mock-provider.md) | Mock provider with failure injection |
| [ADR-L06](adr/ADR-L06-payout-unknown-outcome.md) | Payout state machine with UNKNOWN outcome |
| [ADR-L07](adr/ADR-L07-hot-accounts.md) | Hot accounts (non-materialized system balances) |
| [ADR-L08](adr/ADR-L08-inbound-webhook-inbox.md) | Inbound webhook inbox + verify-by-fetch |

## Design documents — [`design/`](design/)

| File | Original (archive) | Content |
|---|---|---|
| `00-prd.md` | Step 12 `03` | Consolidated PRD |
| `01-product-strategy.md` | Step 1 `01` | Vision, personas, workflows, scope |
| `02-features.md` | Step 2 `01` | Feature matrix (LF-xx) + specs |
| `03-architecture.md` | Step 2 `02` | Architecture V1 → V2 → V3 |
| `04-domain-model.md` | Step 3 `01` | Aggregates, state machines, journal templates |
| `05-invariants.md` | Step 3 `02` | Financial invariants LI-01…LI-15 |
| `06-failure-scenarios.md` | Step 3 `03` | FS-01…FS-10 |
| `07-db-shared-tables.md` | Step 4 `01` | users, refresh_tokens, idempotency_keys, jobs, audit_log |
| `08-db-erd.md` | Step 4 `02` | ERD |
| `09-db-schema.md` | Step 4 `03` | Table specs, constraints, triggers |
| `10-db-consistency-indexing.md` | Step 4 `04` | Strong vs eventual, query → index |
| `11-api-conventions.md` | Step 5 `00` | API conventions |
| `12-api-error-catalogue.md` | Step 5 `01` | Error codes (authoritative) |
| `13-api-endpoints.md` | Step 5 `02` | Endpoint contracts |
| `14-api-external-contracts.md` | Step 5 `03` | Mock provider + webhooks |
| `15-security-shared-threats.md` | Step 6 `01` | Shared threats T-SH |
| `16-security-threat-model.md` | Step 6 `02` | LedgerPay threats T-LP |
| `17-security-controls.md` | Step 6 `04` | Controls C-xx, security tests |
| `18-tx-global-rules.md` | Step 7 `00` | Isolation, global lock order, retry |
| `19-tx-concurrency.md` | Step 7 `01` | Operation catalogue, race timelines |
| `20-background-jobs.md` | Step 7 `03` | Job catalogue, worker model |
| `21-observability.md` | Step 8 `01` | Logs, metrics catalogue, alerts, runbooks |
| `22-testing-strategy.md` | Step 8 `02` | Test pyramid, test catalogue, traceability |
| `23-performance-plan.md` | Step 8 `03` | Load test scenarios, experiments |
| `24-docker-compose.md` | Step 9 `01` | Docker, Compose, Caddy, env vars |
| `25-cicd.md` | Step 9 `02` | CI/CD pipeline |
| `26-deploy-migrations-backup.md` | Step 9 `03` | Environments, migrations, backup, deploy |
| `27-roadmap.md` | Step 10 `02` | Sprint 0–10 |
| `28-definition-of-done.md` | Step 10 `04` | Definition of Done |
| `29-final-review.md` | Step 12 `01` | Final technical review (authoritative) |

## Spikes — [`spikes/`](spikes/)
Throwaway experiments that validate risky assumptions before product code (Sprint 0).
