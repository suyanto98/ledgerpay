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

## Sprint 1 — Foundation · 🏁 M1 ✅
- [x] `go.mod` + `cmd/ledgerpay` (subcommand `api`, `migrate`, `healthcheck`; `worker`/`seed` saat dibutuhkan, S2/S6)
- [x] `internal/platform/config` (env, fail fast)
- [x] `internal/platform/db`: pool, `WithTx` + retry `40001`/`40P01`, pemetaan nama constraint → error, `testdb` (pola spike, role `ledgerpay_app`)
- [x] `internal/platform/httpx`: chi, request ID, recover, slog JSON, RFC 9457, decoder strict, batas body
- [x] `internal/platform/observability`: logger + redaction allowlist; health `/healthz` + `/readyz` (port internal, versi DB ≥ minimum)
- [x] Migrasi awal: schema, role `ledgerpay_owner`/`ledgerpay_app`, `users` (+ `role`), `refresh_tokens`, `audit_log`, `set_updated_at`, `forbid_update_delete`
- [x] Auth: register, login (argon2id), `GET /me`; JWT HS256 + `kid`
- [x] Catalogue test: setiap FK punya index (Step 12 C4)
- [x] Samakan nama constraint email: katalog error menyebut `uq_users_email`, skema `uq_users_email_lower` (pakai nama skema; test `TestDBErrorMapping_AllCatalogueConstraints` menangkap selisih seperti ini)
- [x] Compose dev (postgres + api), target Makefile `up`, `test`, `test-integration`, `migrate`
- [x] `pr.yml`: golangci-lint (depguard), unit + integration test (`-race`)

## Catatan dari Sprint 1 (dibawa ke Sprint 2)
- [x] Log akses belum memuat `user_id` (diperbaiki di Sprint 2: holder request info diisi middleware auth) (logger akses memakai context sebelum autentikasi). Perbaiki dengan menaruh holder field yang bisa diisi middleware auth
- [ ] Path module Go masih `ledgerpay`; ganti ke `github.com/<username>/ledgerpay` saat username GitHub ditentukan (`go mod edit -module` + ganti import)
- [ ] CI belum pernah dijalankan di GitHub (repo masih lokal)

## Sprint 2 — Core Domain ✅
- [x] Migrasi `accounts`, `journals`, `journal_entries` (trigger keseimbangan versi `IF`, append-only, composite FK currency)
- [x] `ledger.PostJournal` + value object `Money` + validasi template purpose
- [x] Register membuat user + wallet dalam satu tx; seed akun sistem
- [x] `GET /wallet`, `GET /wallet/transactions` (cursor), `GET /wallet/transactions/{id}` + scoping owner (404)
- [x] Invariant checker + property test (journal selalu seimbang, Σ saldo kekal)

## Catatan dari Sprint 2 (dibawa ke sprint berikutnya)
- [ ] `accounts.merchant_id` + FK ke `merchants` + perluasan `ck_accounts_owner` (Sprint 4, migrasi expand)
- [ ] `held_amount` di `GET /wallet` (dana payout yang di-hold) saat payout ada (Sprint 6)
- [ ] Job terjadwal `ledger.verify` yang memanggil `ledger.Verify` dan mengisi metrik invariant (Sprint 6/7)

## Sprint 3 — Critical Business Logic · 🏁 M2 ✅
- [x] Migrasi `idempotency_keys` + modul idempotency pola (a) (`internal/platform/idempotency`, SAVEPOINT untuk menyimpan 422)
- [x] `POST /transfers`, `GET /transfers/{id}` dengan lock `ORDER BY id FOR NO KEY UPDATE`
- [x] `lock_timeout` 2 dtk → `RESOURCE_BUSY` / `REQUEST_IN_PROGRESS`
- [x] Harness concurrency (barrier, build tag `testhooks`); Demo A & B sebagai test otomatis + `make demo-a` / `make demo-b`
- [x] Perbaikan bug Sprint 2: `LockAccounts` ikut mengunci akun sistem (melanggar ADR-L07); kini hanya wallet yang dikunci + regression test

## Catatan dari Sprint 3 (dibawa ke sprint berikutnya)
- [ ] `TestBuild_NoTesthooksInRelease`: periksa binary rilis tidak memuat `testhook.Set` (Sprint 8, bersama pipeline rilis). Saat ini dijamin konstruksi: `Set` hanya ada di file `//go:build testhooks`
- [ ] Demo B memakai `xargs -P 100` + curl (k6 belum terpasang); skrip k6 dibuat bersama baseline load test (Sprint 9)
- [ ] `journals.idempotency_key_id` (desain 09 §5) belum ada; referensi key disimpan di `transfers.idempotency_key_id`. Tambahkan saat payment/refund butuh (Sprint 5) atau hapus dari desain
- [ ] Respons replay disimpan sebagai `jsonb`: isinya sama secara semantik, tetapi urutan key/whitespace bisa berbeda dari respons pertama. Didokumentasikan di OpenAPI; ubah ke `bytea` kalau ada client yang membandingkan byte
- [ ] Rate limit `money-write` untuk `POST /transfers` (Sprint 4)
