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
- [x] `accounts.merchant_id` + FK ke `merchants` + perluasan `ck_accounts_owner` (Sprint 4, migrasi expand `00004`)
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
- [x] Rate limit `money-write` untuk `POST /transfers` (Sprint 4)

## Sprint 4 — Security ✅
- [x] Refresh token: rotasi, reuse detection (revoke family + audit), grace 10 dtk, batas family 30 hari, logout, CSRF (`X-Requested-With` + cek `Origin`)
- [x] Rate limiter in-memory per tier (`internal/platform/ratelimit`) + trusted proxy (`httpx.ClientIP`); lockout bertahap 1→15 menit; semaphore argon2 (4 paralel, antre 2 dtk → 503)
- [x] Security header middleware; audit `auth.login_failed` / `auth.refresh_reuse_detected` / `auth.logged_out`
- [x] Merchant + API key (slot 1/2, SHA-256, prefix lookup, constant-time compare), endpoint ops create/revoke, middleware API key di `/merchant/*`; CLI `merchant-create`, `grant-ops`
- [x] Payment intent: create (idempotent, `DUPLICATE_MERCHANT_ORDER` + `existing_resource_id`), GET merchant, GET user (capability URL), confirm (lock intent → wallet, guard status, expiry `now()` awal tx); fungsi sweeper `payment.ExpireDue`
- [x] `make demo-payment`

## Catatan dari Sprint 4 (dibawa ke sprint berikutnya)
- [ ] **Keputusan desain yang bertentangan, diselesaikan:** threat model T-SH-01 poin (5) dan DoD roadmap S4 menulis `403 ACCOUNT_LOCKED`; katalog error (Step 12 R2-5) menulis `401 INVALID_CREDENTIALS + Retry-After`. Dipilih **401 tanpa `Retry-After`**, karena header itu hanya muncul untuk email terdaftar yang terkunci sehingga tetap menjadi oracle enumerasi. Perbarui T-SH-01, roadmap S4, dan katalog agar seragam
- [ ] Grace window refresh: server tidak bisa mengirim ulang refresh token penerus (hanya hash yang disimpan), jadi refresh kedua dalam grace hanya mendapat access token tanpa cookie baru. Asumsinya kedua tab berbagi cookie jar (browser yang sama). Catat di ADR-009
- [ ] Demo B butuh `RATE_LIMIT_MONEY_WRITE_PER_MIN=600` di compose dev (default 30/menit akan menolak 70 dari 100 request dengan 429). Demo/production memakai default
- [ ] Job terjadwal `intent.expire_sweep` yang memanggil `payment.ExpireDue` (Sprint 6, job runner). Sampai saat itu intent lewat waktu ditampilkan `EXPIRED` (status efektif) dan confirm-nya ditolak
- [ ] Outbox webhook `payment.succeeded` di tx confirm (Sprint 6)
- [ ] `GET /merchant/payment-intents` (list + cursor) dan `GET /merchant/balance` belum dibuat (bukan deliverable S4)
- [ ] Respons login belum memuat objek `user` (spesifikasi endpoint §3); SPA bisa memanggil `GET /me` (Sprint 5)
- [ ] Housekeeping: revoke otomatis API key yang `expires_at`-nya lewat (masih menempati slot) — job Sprint 6
- [ ] Rate limiter in-memory: batas efektif × jumlah instance; versi Redis bila >1 instance (ADR-007)


## Sprint 5 — Testing ✅
- [x] `mock-provider` (service terpisah, `cmd/mock-provider`): charges + idempotency, halaman checkout, webhook bertanda tangan, skenario global/per-request/magic amount, `/admin/*`
- [x] Top-up `POST /topups` pola recovery point (idempotency `Reserve`/`Complete`), `GET /topups/{id}`
- [x] `webhook_inbox` + endpoint (HMAC, window 5 menit, dedupe) + worker (claim dengan lease, fencing, transisi monoton, verify-by-fetch, backoff → DEAD); subcommand `ledgerpay worker`
- [x] Test FS-03/04/06/07/10 + lease kedaluwarsa (`testhooks`), matriks traceability diperbarui
- [x] Vue SPA minimal di `web/` (login/daftar, wallet, transfer, top-up) + Playwright E2E (`web/e2e`), job CI `frontend` dan `e2e`
- [x] `make demo-topup`, `make web`, `make web-check`, `make test-e2e`

## Catatan dari Sprint 5 (dibawa ke sprint berikutnya)
- [ ] mock-provider menyimpan state **in-memory** (desain: DB terpisah `migrations_mock`). Cukup untuk dev/E2E; DB dibutuhkan kalau mock harus bertahan restart (settlement CSV, Sprint 9)
- [ ] Endpoint mock payout (`/v1/payouts`) dan settlement CSV belum ada (Sprint 6 dan 9)
- [ ] Job `topup.recover` (top-up macet di `CREATED` tanpa retry client) dan `topup.expire_sweep` (cek provider dulu) — Sprint 6 job runner
- [ ] Event `CONFLICT` hanya dicatat log; `recon_items` di tx yang sama menunggu tabel recon (Sprint 9)
- [ ] Metrik `webhook_late_total`, `webhook_inbox_dead_total`, alert DEAD — Sprint 7 observability
- [ ] Replay event `DEAD` dari ops console (`DEAD → RECEIVED`) — Sprint 8
- [ ] Retensi inbox (hapus > 90 hari, bukan `RECEIVED`) — job cleanup Sprint 6
- [ ] SPA memakai fetch wrapper tangan; desain: client typed dari `openapi-typescript` (Sprint 8, bersama UI checkout)
- [ ] Job CI `frontend`/`e2e` belum pernah berjalan di GitHub (repo lokal)
- [ ] Worker belum punya endpoint health/metrics (compose: healthcheck dimatikan) — Sprint 6/7
