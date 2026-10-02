# Backlog (lokal) — Sprint 1–3

> Repo masih lokal, jadi backlog ditulis di sini dan nanti dipindah ke GitHub Issues saat ada remote. Sumber: [`design/27-roadmap.md`](design/27-roadmap.md) (baca banner amandemen Step 12) dan [`design/28-definition-of-done.md`](design/28-definition-of-done.md).

## Sprint 0 — Product + Architecture ✅
- [x] Toolchain: sqlc, goose, golangci-lint, actionlint (Go 1.25, Docker, Node 22 sudah ada). k6 ditunda ke S9; sops/age ditunda (Step 12 S2)
- [x] Repo lokal + `.gitignore`, `.editorconfig`, LICENSE MIT, Makefile, template PR
- [x] Kurasi `docs/` (21 ADR + 30 dokumen desain + index)
- [x] OpenAPI skeleton (Money, Problem, Page; auth, wallet, transfers) lolos Redocly lint tanpa warning
- [x] `pr.yml` minimal (actionlint + Redocly), action di-pin ke SHA. Lolos lokal; belum dijalankan di GitHub (repo lokal)
- [x] Spike 1 & 2 (lihat [`spikes/2026-10-02-db-foundations.md`](spikes/2026-10-02-db-foundations.md)); 1 bug desain ditemukan dan diperbaiki
- [ ] Branch protection (menunggu remote GitHub)

## Sprint 1 — Foundation · 🏁 M1
- [ ] `go.mod` + `cmd/ledgerpay` (subcommand `api`, `worker`, `migrate`, `seed`, `healthcheck`)
- [ ] `internal/platform/config` (env, fail fast)
- [ ] `internal/platform/db`: pool, `WithTx` + retry `40001`/`40P01`, pemetaan nama constraint → error, `testdb` (pola spike, role `ledgerpay_app`)
- [ ] `internal/platform/httpx`: chi, request ID, recover, slog JSON, RFC 9457, decoder strict, batas body
- [ ] `internal/platform/observability`: logger + redaction allowlist; health `/healthz` + `/readyz` (port internal, versi DB ≥ minimum)
- [ ] Migrasi awal: schema, role `ledgerpay_owner`/`ledgerpay_app`, `users` (+ `role`), `refresh_tokens`, `audit_log`, `set_updated_at`, `forbid_update_delete`
- [ ] Auth: register, login (argon2id), `GET /me`; JWT HS256 + `kid`
- [ ] Catalogue test: setiap FK punya index (Step 12 C4)
- [ ] Compose dev (postgres + api), target Makefile `up`, `test`, `test-integration`, `migrate`
- [ ] `pr.yml`: golangci-lint (depguard), unit + integration test (`-race`)

## Sprint 2 — Core Domain
- [ ] Migrasi `accounts`, `journals`, `journal_entries` (trigger keseimbangan versi `IF`, append-only, composite FK currency)
- [ ] `ledger.PostJournal` + value object `Money` + validasi template purpose
- [ ] Register membuat user + wallet dalam satu tx; seed akun sistem
- [ ] `GET /wallet`, `GET /wallet/transactions` (cursor), `GET /wallet/transactions/{id}` + scoping owner (404)
- [ ] Invariant checker + property test (journal selalu seimbang, Σ saldo kekal)

## Sprint 3 — Critical Business Logic · 🏁 M2
- [ ] Migrasi `idempotency_keys` + modul idempotency pola (a)
- [ ] `POST /transfers`, `GET /transfers/{id}` dengan lock `ORDER BY id FOR NO KEY UPDATE`
- [ ] `lock_timeout` 2 dtk → `RESOURCE_BUSY` / `REQUEST_IN_PROGRESS`
- [ ] Harness concurrency (barrier, build tag `testhooks`); Demo A & B sebagai test otomatis
