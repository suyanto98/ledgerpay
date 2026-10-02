# Step 10.2 — LedgerPay Roadmap (Sprint 0–10)

> Bagian dari [Step 10](00-index.md). Kerangka di `00-index.md` **mengikat**: sprint 1 minggu (± 14–16 jam fokus), Sprint 0–10, milestone M1–M4, dan aturan §2 (nama sprint = *fokus*; test, security, logging, dan CI **tidak ditunda**).
> Referensi: fitur LF-xx (Step 2 `01`), ADR (Step 2 `adr/`), failure scenario FS-xx (Step 3 `03`), endpoint (Step 5 `02`), kontrol C-xx (Step 6 `04`), job (Step 7 `03`), test (Step 8 `02`), skenario LP-x (Step 8 `03`), deploy/backup (Step 9).


## ⚠️ Amandemen Step 12 (otoritatif, menggantikan baris task yang bertentangan di bawah)

Sumber: Step 12 `01-final-technical-review.md` §3.4 dan §5. Jam per task diatur ulang saat sprint planning; tabel di bawah dibiarkan sebagai rencana awal.

| Sprint | Perubahan | Jam |
|---|---|---:|
| S4 | **Role `OPS` + rute `/ops/*` minimal** (buat/cabut API key merchant) masuk P0. Ops console lengkap tetap P1 (S8) | ±1 (dari buffer) |
| S10 | Endpoint demo hanya `faults`, `reset` (S6 Step 12); bukti Demo C lewat ops/log, skenario lewat admin mock via SSH tunnel | −1,5 |
| S7 | Deploy publik pertama **+3 jam**, video **+1,5 jam**. Observability V1 = Prometheus + Grafana + node_exporter; Jaeger di profile `obs-full`. Dashboard 3 (bukan 6). Tanpa Alertmanager | +4,5 / −3,5 |
| S8 | CD + rollback + restore drill **+3,5 jam**. Secret dari GitHub environment secrets (sops ditunda). `deploy-gate` forced command → Production Enhancement. Image release tidak di-push | +3,5 / −6,5 |
| S9 | Perf subset: **LP-2, LP-3, LP-4 (E-1), LP-9**; seed 1 juta journal; perf smoke nightly ditunda setelah M4. Recon Demo D lewat API/curl (tanpa UI ops recon) | −10 |
| S10 | Meta test traceability (M4). Ruleset Redocly kustom ditunda | −3,5 |
| Semua | UI wallet/checkout **+5 jam** tersebar di S4–S7 | +5 |
| | **Perkiraan netto:** ±9 jam dibebaskan → buffer LedgerPay | |

Perbaikan desain Step 12 yang menyentuh sprint LedgerPay: C1 (claim inbox dengan lease) di S5, R1-2 (fencing inbox) di S5, R1-5 (settlement per top-up) di S9, R2-5 (login terkunci tidak bisa dibedakan) di S4, C4 (catalogue test "setiap FK punya index") di S1.

---

## 0. Ringkasan

| Sprint | Tema (master prompt) | Fokus nyata | Milestone | Demo di akhir sprint |
|---|---|---|---|---|
| S0 | Product + Architecture | Setup repo & tooling, kurasi dokumen, OpenAPI skeleton, spike teknis | — | Spike notes + pipeline CI kosong hijau |
| S1 | Foundation | Platform skeleton + identity (register/login) + CI | **M1** walking skeleton | `make up` → health hijau, register/login via curl |
| S2 | Core Domain | Ledger double-entry + wallet read | — | Property test journal selalu seimbang; `GET /wallet` |
| S3 | Critical Business Logic | Transfer + idempotency + locking + concurrency test | **M2** core money correct | **Demo A & B** sebagai test otomatis + script |
| S4 | Security | Hardening auth/session/rate limit + merchant + payment intent | — | BOLA suite hijau; merchant membuat intent, user konfirmasi |
| S5 | Testing | Mock-provider + top-up + inbox + failure scenario test + UI minimal | — | Top-up end-to-end lewat UI; webhook duplikat aman |
| S6 | Background Processing | Job runner + payout state machine + webhook keluar | — | **Demo C** (kill worker saat payout) |
| S7 | Observability | Metrik, dashboard, alert, tracing + **deploy publik minimal** + README + video | **M3 🎯 siap melamar** | Demo A–C di VM publik, dashboard + trace |
| S8 | DevOps | CD otomatis, backup, restore drill, rollback + refund + ops console | — | Deploy otomatis + rollback; refund paralel aman |
| S9 | Performance | Seeding, load test LP-x, eksperimen E-1, profiling + reconciliation | — | Laporan baseline; **Demo D** (recon) |
| S10 | Documentation + Demo | Ops console recon, demo mode, dokumentasi final, audit DoD | **M4** LedgerPay selesai | Demo A–D penuh + `make demo-verify` hijau |

**Kenapa deploy publik masuk S7, bukan menunggu S8:** M3 ("siap melamar") mensyaratkan Demo A–C berjalan di VM publik di akhir S7. Karena itu S7 membawa **deploy minimal yang di-script** (provisioning, Caddy, Compose, env sops, `deploy.sh` manual). S8 (tema DevOps) mengeraskannya: CD otomatis dari `main`, health gate dengan rollback otomatis, `deploy-gate`, backup offsite, restore drill, dan job nightly. Dengan begitu, tema sprint tetap sesuai master prompt, dan milestone tetap tercapai.

**Kenapa merchant payment (LF-09/LF-10) masuk S4:** fitur ini P0 tetapi tidak dibutuhkan Demo A–C. Fitur ini menjadi tempat alami untuk menguji hasil hardening S4 (dua jenis principal: user JWT dan merchant API key, plus BOLA lintas merchant). Kalau jadwal mundur, LF-10 adalah kandidat pertama yang digeser ke S8 tanpa mengorbankan M3 (§14).

---

## Sprint 0 — Product + Architecture

**Objective:** menyiapkan lingkungan kerja dan memvalidasi tiga asumsi teknis paling berisiko sebelum menulis kode produk. Dokumen Step 1–9 sudah selesai, jadi sprint ini pendek di sisi dokumen.

**Deliverables**
- Repo `ledgerpay` dan `infra` (public, ADR-013/Step 9 §keputusan), branch protection, template PR + checklist DoD PR (`04`).
- `docs/` berisi ADR sebagai file terpisah + dokumen desain terkurasi dari folder `version 2`.
- `api/openapi.yaml` skeleton: komponen `Money`, `Problem`, `Page`; path untuk S1–S3 (`/auth/*`, `/me`, `/wallet*`, `/transfers*`) + lint Redocly.
- Catatan spike di `docs/spikes/`.

| Task | Jam | Ref |
|---|---:|---|
| Instal toolchain (Go, Docker, sqlc, goose, golangci-lint, Node, k6, sops/age) | 2 | `01-repository-structure.md` |
| Buat repo + branch protection + template PR + label | 1,5 | Step 9 `02` §9 |
| Kurasi `docs/` (ADR per file, index, link ke step docs) + README skeleton | 2 | Step 11 (struktur README) |
| OpenAPI skeleton + Redocly ruleset (uang = string, error = problem+json) | 3 | ADR-003, Step 5 `00` |
| **Spike 1:** pgx + sqlc + goose + testcontainers dengan clone DB `TEMPLATE` (kode buang) | 3,5 | ADR-005, Step 8 `02` §2 |
| **Spike 2:** deferred constraint trigger keseimbangan + `FOR NO KEY UPDATE` vs `KEY SHARE` FK (validasi ADR-L01/L03) | 1,5 | Step 4 `03`, Step 7 `01` §4 |
| Backlog issue S1–S3 (satu issue per task, label sprint) | 0,5 | — |
| **Total** | **14** | |

**Dependency:** dokumen Step 1–9.
**Definition of Done**
- Workflow `pr.yml` minimal (actionlint + Redocly lint) hijau di repo kosong.
- Spike 1 membuktikan: test integration dengan DB clone < 1 detik per test.
- Spike 2 membuktikan: journal tak seimbang ditolak saat COMMIT, dan INSERT entry tidak terblok oleh lock akun `FOR NO KEY UPDATE`.
- Kalau spike membantah ADR, ADR baru ditulis (bukan diam-diam menyimpang).

**Risk:** spike melebar menjadi kode produk. *Mitigasi:* kode spike di branch `spike/*`, tidak di-merge.
**Demo-able:** catatan spike + CI hijau.

---

## Sprint 1 — Foundation  ·  🏁 M1 walking skeleton

**Objective:** skeleton platform yang dipakai ulang oleh semua fitur (dan nanti disalin ke PayRun), plus satu jalur end-to-end nyata (register/login).

**Deliverables**
- `internal/platform`: `config` (fail fast), `db` (pool, `WithTx` + retry `40001`/`40P01`, mapping nama constraint → error), `httpx` (chi, request ID, recover, slog JSON, RFC 9457, decoder strict, batas body), `auth` (argon2id, JWT HS256 + kid), `clock`, `idgen`.
- Migrasi awal: schema `ledgerpay`, role `ledgerpay_owner`/`ledgerpay_app`, `users` (+ `role`), `refresh_tokens`, `audit_log`, fungsi `set_updated_at`/`forbid_update_delete`.
- Endpoint: `POST /auth/register`, `POST /auth/login`, `GET /me`, `GET /healthz`, `GET /readyz` (port internal).
- Compose dev (postgres + api) + Makefile (`up`, `test`, `test-integration`, `migrate`).
- CI `pr.yml`: lint (golangci-lint + gosec + depguard), unit `-race`, integration (testcontainers), migration up-from-zero.

| Task | Jam | Ref |
|---|---:|---|
| `platform/config` + validasi env | 1 | Step 9 `01` §8 |
| `platform/db`: pool, `WithTx` + retry, error mapping | 2,5 | ADR-011, Step 5 `01` §4 |
| `platform/httpx`: router, middleware, RFC 9457, decoder strict, body limit | 2,5 | ADR-003, C-13, C-14 |
| Migrasi awal (role, users, refresh_tokens, audit_log, trigger function) | 2 | Step 4 `01` |
| Identity: register/login/me (argon2id, JWT, refresh cookie dasar) | 3 | LF-01, ADR-009 |
| Health/readiness di port internal | 0,5 | Step 8 `01` §2 |
| Compose dev + Makefile | 1 | Step 9 `01` §10 |
| CI `pr.yml` (lint, unit, integration, migration) | 2 | Step 9 `02` §3 |
| Test: `authn_generic_error`, `jwt_rejects_*`, `TestMigrations_UpFromZero` | 1,5 | Step 6 `04` §4, Step 8 `02` |
| **Total** | **16** | |

**Dependency:** S0 (repo, spike).
**Definition of Done**
- `make up` dari clone bersih → `/readyz` hijau dalam < 2 menit.
- CI hijau dengan empat job wajib.
- Register → login → `GET /me` berhasil via curl; password salah dan email tak dikenal mengembalikan `401 INVALID_CREDENTIALS` yang identik.
- Log JSON memuat `request_id`; header `X-Request-ID` dikembalikan.

**Risk:** over-engineering skeleton ("framework sendiri"). *Mitigasi:* hanya yang dibutuhkan S1–S3 (Step 1 X7); sisanya ditambah saat dibutuhkan.
**Demo-able:** register/login via curl + CI hijau.

---

## Sprint 2 — Core Domain

**Objective:** ledger double-entry yang tidak bisa dibuat salah oleh bug aplikasi, plus wallet yang bisa dibaca.

**Deliverables**
- Migrasi `accounts` (FK owner nyata + `system_code`, `CHECK (is_system OR balance >= 0)`), `journals` (`UNIQUE (source_type, source_id, purpose)`, `UNIQUE (reversal_of)`), `journal_entries` (composite FK currency), trigger append-only + grant `SELECT, INSERT`, **deferred constraint trigger keseimbangan**.
- `ledger.PostJournal` + `Money` value object + validasi template purpose (ADR-L01).
- Register membuat user **dan** wallet dalam satu transaksi (LF-01 AC1). Seed akun sistem.
- `GET /wallet`, `GET /wallet/transactions` (cursor), `GET /wallet/transactions/{id}` (LF-02) + scoping owner (404).
- Invariant checker (helper test + fungsi verifikasi yang nanti dipanggil job `ledger.verify`).

| Task | Jam | Ref |
|---|---:|---|
| Migrasi accounts/journals/entries + trigger + grant | 3 | Step 4 `03` |
| `ledger.PostJournal`, `Money`, template purpose | 3 | ADR-L01, ADR-L04, Step 3 `01` §6 |
| Wallet di register + seed akun sistem | 1 | LF-01, ADR-L07 |
| Endpoint wallet (cursor, BOLA) | 2,5 | LF-02, Step 5 `02` |
| Invariant checker | 1,5 | Step 8 `02` §6 |
| Test: `TestJournal_Validate`, `TestJournal_UnbalancedRejectedAtCommit`, `TestLedger_AppendOnly`, `TestLedger_DoubleReversalRejected`, `TestProperty_JournalAlwaysBalanced`, `TestJournalPurpose_Templates` | 3 | LI-01, LI-04, LI-06 |
| Error mapping constraint ledger + `TestCatalogue_AppendOnlyGrants` | 1 | Step 5 `01` |
| **Total** | **15** | |

**Dependency:** S1 (platform, identity).
**Definition of Done**
- Semua test di atas hijau di CI; property test berjalan ≥ 1.000 kasus.
- `UPDATE journal_entries …` dengan role app ditolak (trigger **dan** grant).
- `bola_wallet` hijau (user A membaca wallet B → 404).

**Risk:** salah arah debit/kredit untuk liability. *Mitigasi:* tabel template Step 3 `01` §6 jadi test tabel; review ulang ADR-L01 sebelum mulai.
**Demo-able:** property test + `GET /wallet` setelah register.

---

## Sprint 3 — Critical Business Logic  ·  🏁 M2 core money correct

**Objective:** transfer yang benar di bawah request duplikat dan paralel. Ini inti klaim LedgerPay.

**Deliverables**
- Migrasi `idempotency_keys` (`uq_idempotency_keys_scope`) + modul idempotency pola (a): INSERT key di transaksi yang sama dengan efek, `request_hash` dari canonical JSON, replay + `Idempotent-Replayed` (ADR-L02).
- `POST /transfers`, `GET /transfers/{id}`: lock `accounts … ORDER BY id FOR NO KEY UPDATE`, `PostJournal(TRANSFER)`, `INVALID_RECIPIENT` generik (LF-04, ADR-L03).
- `lock_timeout` 2 dtk → `409 RESOURCE_BUSY` / `REQUEST_IN_PROGRESS`.
- Harness concurrency (barrier N goroutine, build tag `testhooks`).
- Script Demo A (curl) dan Demo B (k6 kecil + query invariant).

| Task | Jam | Ref |
|---|---:|---|
| Idempotency: migrasi + modul + canonical hash + replay | 4 | ADR-L02, LF-05 |
| Transfer: endpoint + lock order + journal + error | 3 | LF-04, ADR-L03, Step 7 `01` §2 |
| Verifikasi retry wrapper + pemetaan `55P03` | 1 | ADR-011, Step 7 `00` §3 |
| Harness concurrency + `testhooks` | 2 | Step 7 `01` §7 |
| Test: `TestTransfer_ConcurrentDoubleSpend_100x`, `TestTransfer_NaiveImplementation_Overdraws`, `TestTransfer_OppositeDirections_NoDeadlock`, `TestIdempotency_ConcurrentSameKey_OneEffect`, `TestIdempotency_FirstTxRolledBack_SecondProceeds`, `TestTransfer_IdempotentReplay`, `TestProperty_RandomTransfersConserveTotal` | 4 | LI-02, LI-03, LI-05, FS-01, FS-02 |
| Script Demo A & B | 1,5 | Step 1 `01` §8 |
| Update OpenAPI + catatan ADR jika ada temuan | 0,5 | — |
| **Total** | **16** | |

**Dependency:** S2 (ledger).
**Definition of Done**
- 100 transfer paralel × 10.000 dari saldo 100.000 → tepat 10 sukses, 90 `INSUFFICIENT_BALANCE`, drift 0 (test di CI, bukan manual).
- Implementasi naif di test code menunjukkan overdraft (pembanding Demo B).
- Key sama + payload beda → `422 IDEMPOTENCY_KEY_REUSED`; key sama paralel → satu efek.
- `make demo-a` dan `make demo-b` berjalan lokal dari nol.

**Risk:** test concurrency flaky. *Mitigasi:* barrier + hook berbasis advisory lock (deterministik), `-count=20` lokal sebelum merge; kebijakan flaky Step 8 `02` §7.
**Demo-able:** **Demo A & B** (terminal + query ledger).

---

## Sprint 4 — Security

**Objective:** mengeraskan identitas dan sesi, lalu menambah principal kedua (merchant API key) sehingga authorization diuji di dua dimensi.

**Deliverables**
- Refresh token: rotasi, reuse detection (revoke family), grace 10 dtk (`replaced_by`), `family_expires_at` 30 hari, logout, CSRF (`SameSite=Strict` + `X-Requested-With: fetch` + cek `Origin`) (ADR-009, C-05, C-08).
- Rate limiter in-memory per tier (C-07) + trusted proxy; lockout bertahap + timing equalization (C-03); **semaphore argon2** (C-45).
- Security header middleware (C-10); audit login sukses/gagal (LF-12).
- Merchant: migrasi `merchants`, `merchant_api_keys` (slot 1/2, SHA-256), endpoint ops `POST /ops/merchants/{id}/api-keys` + revoke; middleware API key di `/merchant/*` (LF-09, C-42).
- Payment intent: `POST /merchant/payment-intents`, `GET …`, `GET /payment-intents/{id}` (capability URL), `POST /payment-intents/{id}/confirm` dengan guard expiry (LF-10).

| Task | Jam | Ref |
|---|---:|---|
| Refresh rotation + reuse + grace + logout + CSRF | 3 | ADR-009, C-05, C-08 |
| Rate limiter + lockout + timing + semaphore argon2 | 2 | C-03, C-07, C-45 |
| Security headers + audit login | 1 | C-10, LF-12 |
| Merchant + API key (slot, hash, ops endpoint, middleware) | 2,5 | LF-09, C-42 |
| Payment intent create/get/confirm + expiry guard | 4 | LF-10, Step 7 `01` race (e) |
| Test: `refresh_reuse_revokes_family`, `refresh_parallel_within_grace_ok`, `csrf_refresh_without_header_rejected`, `authn_bruteforce_lockout`, `password_hash_concurrency_bounded`, `api_key_hashed_at_rest`, `api_key_slot_limit`, `bola_payment_intent` (merchant↔merchant), `TestConfirm_ParallelTabs_OnePayment`, `TestConfirm_VsExpirySweeper`, `TestPaymentIntent_DuplicateMerchantOrder_409` | 3,5 | Step 6 `04` §4, LI-13 |
| **Total** | **16** | |

**Dependency:** S1 (auth dasar), S3 (idempotency, lock order).
**Definition of Done**
- Semua test di atas hijau; refresh token lama yang dipakai ulang → seluruh sesi dicabut.
- 6 login gagal beruntun → `403 ACCOUNT_LOCKED` dengan `Retry-After`.
- API key bocor tidak bisa membuat key baru (tidak ada endpoint `/merchant/api-keys`).
- Header keamanan ada di setiap respons (`headers_*_present`).

**Risk:** scope payment intent membengkak (checkout UI, webhook). *Mitigasi:* S4 hanya API. UI checkout di S8, webhook keluar di S6.
**Demo-able:** merchant membuat intent (curl dengan API key) → user konfirmasi → saldo pindah; dua tab konfirmasi → satu pembayaran.

---

## Sprint 5 — Testing

**Objective:** menghadirkan sistem eksternal pertama (mock-provider) dan menjadikan failure scenario sebagai test otomatis, bukan klaim. UI minimal mulai dibangun untuk demo HR.

**Deliverables**
- `mock-provider` (service terpisah): `POST /v1/charges`, `GET /v1/charges/{id}`, halaman `/checkout/{id}`, webhook bertanda tangan `Provider-Signature`, skenario `X-Mock-Scenario` + magic values, `PUT /admin/scenario` (ADR-L05, Step 5 `03`).
- Top-up: `POST /topups` pola recovery point (tx1 → panggil provider → tx2), `GET /topups/{id}` (LF-07, ADR-L02 b).
- `webhook_inbox` + claim loop sendiri + transisi monoton + **verify-by-fetch** (ADR-L08 + amandemen).
- Test failure scenario FS-01, FS-03, FS-04, FS-06, FS-07, FS-10.
- Vue SPA minimal: login/register, dashboard wallet, transfer, top-up (LF-14). Skeleton Playwright.

| Task | Jam | Ref |
|---|---:|---|
| Mock-provider: charges, checkout page, signing, skenario | 4 | LF-06, ADR-L05 |
| Top-up tx1/tx2 + recovery point | 2,5 | LF-07, ADR-L02 |
| Inbox: endpoint webhook, claim loop, transisi monoton, verify-by-fetch | 3 | ADR-L08, C-32, C-33 |
| Test FS: `TestTransfer_CommitOutcomeUnknown`, `TestTransfer_ConnectionDropBeforeCommit`, `TestTopUp_ProviderTimeout_RetryResumesFromRecoveryPoint`, `TestTopup_LateWebhookAfterExpiry_Credited`, `TestInbox_LeaseExpired_ProcessedOnce`, `webhook_signature_invalid_rejected`, `webhook_replay_outside_window_rejected` | 3 | FS-03/04/06/07/10, LI-09 |
| Vue SPA minimal (login, wallet, transfer, top-up) | 2,5 | LF-14 |
| Playwright skeleton + satu E2E (login → transfer) | 1 | Step 8 `02` §3 |
| **Total** | **16** | |

**Dependency:** S3 (transfer, idempotency), S4 (auth lengkap).
**Definition of Done**
- Top-up end-to-end lewat UI + halaman checkout mock → saldo bertambah sekali.
- Skenario `webhook_duplicate:3` → satu journal; `webhook_bad_signature` → 401 tanpa efek.
- Matriks traceability (Step 8 `02` §5) diperbarui: FS-01/02/03/04/06/07/10 berstatus "implemented".
- E2E login → transfer hijau di CI.

**Risk:** mock-provider menyedot waktu (fitur berlebihan). *Mitigasi:* hanya endpoint yang dipakai S5–S6; settlement CSV menunggu S9.
**Demo-able:** top-up lewat UI; terminal menunjukkan webhook duplikat ditolak efeknya.

---

## Sprint 6 — Background Processing

**Objective:** job runner yang tahan crash dan payout yang benar walaupun bank tidak menjawab. Ini Demo C.

**Deliverables**
- Job runner shared: claim `FOR UPDATE SKIP LOCKED`, lease + heartbeat, backoff + jitter, `dead`, `CompleteInTx` dengan **fencing**, `jobs.Permanent`, scheduler `dedupe_key` per bucket waktu (ADR-006, Step 7 `03`).
- Payout: migrasi `payouts`, `POST /payouts` (hold journal), `payout.submit` (commit `SUBMITTED` sebelum memanggil bank), `payout.status_check` (30s/2m/10m/1h/6h → `needs_reconciliation`), respons tak cocok → `UNKNOWN` (LF-11, ADR-L06 + amandemen).
- Mock bank payout + status query + magic value `0408`.
- Sweeper terjadwal: `topup.expire_sweep`, `payment_intent.expire_sweep`, `topup.recover`.
- Webhook keluar ke merchant (`webhook.deliver`, retry 1m…24h, `dead`) + **egress guard SSRF** (LF-15, C-44). LF-15 (P1) ditarik ke sini karena outbox sudah tersedia.

| Task | Jam | Ref |
|---|---:|---|
| Job runner (claim, lease, heartbeat, backoff, fencing, Permanent, scheduler) | 4 | ADR-006 |
| Payout: tabel + `POST /payouts` + hold | 2 | LF-11 |
| `payout.submit` + `status_check` + mismatch + mock bank | 4 | ADR-L06, Step 5 `03` §1 |
| Sweeper terjadwal (expire, recover) | 1 | Step 7 `03` §3 |
| `webhook.deliver` + egress guard | 3 | LF-15, C-44 |
| Test: `TestJobRunner_LeaseExpiry_Reclaims`, `TestJobRunner_FencedCompletion_ZombieRollsBack`, `TestJobRunner_PermanentError_DeadImmediately`, `TestScheduler_DedupeKey_NoDoubleSchedule`, `TestPayoutSubmit_CrashAfterBankAccepted_NoDoubleSend`, `TestPayout_TimeoutAfterProcess_ResolvesViaStatusQuery`, `TestPayout_MismatchedProviderResponse_Unknown`, `TestMerchantWebhook_RetryBackoffThenDead`, `webhook_egress_blocks_private_ip` | 2 | FS-05, FS-08, FS-09, LI-10 |
| **Total** | **16** | |

**Dependency:** S5 (mock-provider, inbox), S4 (merchant untuk webhook keluar).
**Definition of Done**
- Demo C lokal: mock bank `timeout_after_process` + `docker kill` worker → restart → payout `SUCCEEDED`, mock bank mencatat **1** transfer, satu journal settle.
- Worker zombie tidak bisa menyelesaikan job yang lease-nya sudah diambil (test fencing hijau).
- URL webhook `http://169.254.169.254/…` ditolak oleh egress guard.

**Risk:** job runner buatan sendiri punya bug halus. *Mitigasi:* integration test untuk lease kedaluwarsa, crash, dan worker paralel sebelum dipakai payout; `-race` wajib.
**Demo-able:** **Demo C** di lokal (terminal + log mock bank).

---

## Sprint 7 — Observability  ·  🎯 M3 siap melamar

**Objective:** sistem bisa *diamati* (metrik bisnis, dashboard, trace), lalu dipublikasikan di VM publik dengan README dan video demo. Setelah sprint ini, mulai melamar.

**Deliverables**
- Metrik inti dari katalog Step 8 `01` §3: `app_http_*`, `app_db_pool_*`, `app_job_*`, `ledgerpay_ledger_balance_drift` / `ledgerpay_ledger_unbalanced_journals` (gauge INVARIANT), `ledgerpay_payout_unknown_*`, `ledgerpay_idempotency_replay_total`.
- Job `ledger.verify` terjadwal (mengisi gauge invariant).
- OpenTelemetry: span HTTP, pgx, HTTP keluar, job dengan link via `jobs.trace_parent`.
- Grafana (provisioned): "LedgerPay — Money Movement", "Platform — Jobs". Tiga alert: invariant > 0, payout unknown > 1 jam, job dead naik.
- **Deploy publik minimal:** VM (cloud-init + `bootstrap.sh`), Caddy + image `edge`, Compose gabungan di repo `infra`, env dari sops, `deploy.sh` manual dengan SHA (Step 9 `03`).
- README draft (problem, arsitektur, cara menjalankan demo, keputusan kunci) + video demo A–C (± 5 menit).

| Task | Jam | Ref |
|---|---:|---|
| Metrik inti + `ledger.verify` terjadwal | 3 | LF-19, Step 8 `01` §3 |
| Tracing OTel (http, pgx, outbound, job link) | 2 | Step 8 `01` §5 |
| Dashboard (2) + alert (3) | 2 | Step 8 `01` §6–7 |
| Deploy publik minimal (VM, Caddy, compose, sops, `deploy.sh`) | 3,5 | Step 9 `01`, `03` |
| UI halaman payout + status (`display_status`) | 1 | LF-14, Step 5 `02` |
| README draft + diagram arsitektur | 2 | Step 11 |
| Rekam video demo A–C | 1,5 | Step 11 (demo script) |
| Smoke test pasca-deploy manual + checklist M3 | 1 | `04` |
| **Total** | **16** | |

**Dependency:** S3 (Demo A/B), S6 (Demo C), Step 9 (desain infra).
**Definition of Done (= kriteria M3, detail di `04`)**
- Demo A–C berjalan di URL publik HTTPS dari clone bersih + `deploy.sh`.
- Dashboard menampilkan drift = 0 dan payout unknown naik-turun saat Demo C; trace api → job → mock terlihat di Jaeger.
- README menjelaskan masalah, solusi, dan cara memverifikasi klaim (link test + video).
- CI (PR checks) hijau; deploy ter-script dan bisa diulang. (CD otomatis menyusul di S8.)

**Risk:** waktu habis di infra (DNS, TLS, memori VM). *Mitigasi:* pakai profile obs ringan (Prometheus + Grafana + Jaeger; Loki menyusul), VM 4 vCPU / 8 GB; cut list §14.
**Demo-able:** URL publik + video. **Mulai melamar.**

---

## Sprint 8 — DevOps

**Objective:** deploy yang aman dan bisa dipulihkan (CD, rollback, backup, restore drill), plus menyelesaikan fitur P1 refund dan ops console.

**Deliverables**
- `main.yml`: build & push GHCR (tag SHA, varian `release` + `demo`), trivy image scan, deploy via `deploy-gate` (forced command, `flock`), guard migrasi expand/contract, health gate, **rollback otomatis**, smoke test (transfer Rp1 `smoke-<sha>`) (Step 9 `02`).
- `rollback.yml` (manual, input SHA).
- Backup: `pg_dump` per DB → age (dua recipient) → R2/B2, metrik `app_backup_last_success_timestamp_seconds` + alert (Step 9 `03` §5).
- `nightly.yml`: restore drill mingguan + skeleton `demo-verify`.
- Refund: `POST /merchant/payment-intents/{id}/refunds`, guard `refunded_amount ≤ amount`, `INSUFFICIENT_BALANCE` merchant (LF-16).
- Ops console API: search transaksi, payout `UNKNOWN`, `POST /ops/payouts/{id}/resolve` (bukti + alasan), job `dead` + retry, replay webhook delivery (LF-18 sebagian).
- UI checkout merchant (LF-14).

| Task | Jam | Ref |
|---|---:|---|
| `main.yml` + `deploy-gate` + health gate + rollback otomatis + smoke | 4 | Step 9 `02` §4–6 |
| Backup + metrik + alert | 2 | Step 9 `03` §5, C-21 |
| Nightly: restore drill + skeleton demo-verify | 1,5 | Step 9 `02` §7 |
| Refund + test `TestRefund_ParallelExceedingAmount` | 3 | LF-16, LI-08 |
| Ops console API (search, payout resolve, job retry, replay) + audit + test `ops_resolution_requires_evidence`, `TestPayoutStatusCheck_VsOpsResolve` | 3 | LF-18, C-24 |
| UI checkout | 1,5 | LF-14 |
| `rollback.yml` + uji rollback ke SHA sebelumnya | 1 | Step 9 `02` §6 |
| **Total** | **16** | |

**Dependency:** S7 (deploy minimal), S4 (payment intent).
**Definition of Done**
- Merge ke `main` → deploy otomatis; health gate yang sengaja digagalkan memicu rollback ke SHA sebelumnya.
- Backup harian ada di offsite; restore drill mingguan hijau dan memverifikasi invariant setelah restore.
- Dua refund paralel yang totalnya melebihi pembayaran → hanya yang muat yang sukses.
- Aksi ops tanpa bukti/alasan → `422 RESOLUTION_EVIDENCE_REQUIRED`; setiap aksi tercatat di `audit_log`.

**Risk:** CD merusak demo yang sedang dipakai melamar. *Mitigasi:* health gate + rollback otomatis diuji dulu dengan deploy yang sengaja gagal; deploy di luar jam interview.
**Demo-able:** PR → merge → deploy otomatis; refund paralel; ops menyelesaikan payout `UNKNOWN`.

---

## Sprint 9 — Performance

**Objective:** angka baseline yang jujur, eksperimen E-1 (hot merchant) dengan aturan keputusan, dan reconciliation (Demo D).

**Deliverables**
- `seed` subcommand: 100k user, 1k merchant, ±10 juta journal (COPY, `session_replication_role = replica` di container seed) + **gate verifikasi invariant setelah seed** (Step 8 `03` §4).
- Dua VM efemeral perf (SUT + k6). Skenario: LP-2 (10.000 transfer), LP-3 (hot wallet), LP-4 (**E-1** hot merchant), LP-5 (idempotency storm), LP-9 (login argon2). LP-1/6/7/8 jika waktu cukup.
- Profiling (pprof, `pg_stat_statements`, `EXPLAIN (ANALYZE, BUFFERS)`) + perbaikan bottleneck yang terbukti.
- Laporan baseline (template Step 8 `03` §8) di `docs/performance.md`.
- CI perf smoke nightly (threshold fungsional keras, regresi > 30% = peringatan).
- Reconciliation: settlement CSV mock (`GET /v1/settlements/{date}`), `recon.run` terjadwal, `recon_runs`/`recon_items`, kategori mismatch, posting `PROVIDER_SETTLEMENT` (LF-17).

| Task | Jam | Ref |
|---|---:|---|
| Seed generator + gate invariant | 3 | Step 8 `03` §4 |
| VM perf + skrip k6 LP-2/3/4/5/9 | 4 | Step 8 `03` §5 |
| Profiling + perbaikan | 3 | Step 8 `03` §7 |
| Reconciliation (settlement, matching, recon_items, `PROVIDER_SETTLEMENT`) + `TestRecon_ClassifiesAllCategories` | 4 | LF-17, ADR-L01 |
| Laporan baseline + keputusan E-1 | 1 | Step 8 `03` §6, §8 |
| CI perf smoke nightly | 1 | Step 8 `03` §9 |
| **Total** | **16** | |

**Dependency:** S8 (CD, nightly), S6 (job runner untuk `recon.run`).
**Definition of Done**
- Setiap skenario yang dijalankan punya baris di laporan: spesifikasi mesin, git SHA, throughput, p95, **drift = 0**.
- E-1 diputuskan sesuai aturan (< 100 pembayaran/dtk atau p95 lock wait > 50 ms → buka issue `MERCHANT_PENDING` V2; jika tidak, tutup dengan bukti).
- Demo D: skenario `settlement_mismatch:status` → recon menandai `STATUS_MISMATCH`.

**Risk:** VM perf lupa dimatikan (biaya); perbaikan performa memicu regresi correctness. *Mitigasi:* skrip create/destroy satu perintah; semua perbaikan harus melewati suite concurrency + invariant.
**Demo-able:** laporan baseline + **Demo D**.

---

## Sprint 10 — Documentation + Demo  ·  🏁 M4 LedgerPay selesai

**Objective:** menutup semua P1, membuat demo bisa dijalankan siapa pun secara deterministik, dan mengaudit DoD project.

**Deliverables**
- Ops console recon: `GET /ops/recon-runs`, `GET /ops/recon-items`, `POST /ops/recon-items/{id}/resolve` + halaman ops minimal (LF-18 selesai).
- Dashboard "LedgerPay — Integrity & Recon" + alert sisa + link runbook (Step 8 `01` §7–8).
- Mode demo: image `demo` (build tag), `demo_faults` (`migrations_demo/`), `/api/v1/demo/*` dengan `DEMO_MODE` + token (O15, Step 9 `01` §9).
- `make demo-verify` lengkap untuk Demo A–D, berjalan nightly.
- README final, sinkronisasi ADR dengan temuan coding (amandemen), `docs/` rapi.
- Video demo final A–D; latihan demo script 5–10 menit (Step 11).
- Audit DoD (`04`) + `TestTraceability_DocsReferenceExistingTests` hijau.

| Task | Jam | Ref |
|---|---:|---|
| Ops console recon (API + UI minimal) | 3 | LF-18 |
| Dashboard integrity + alert + runbook | 2 | LF-19, Step 8 `01` |
| Mode demo + `demo_faults` + `TestBuild_DemoEndpointsAbsentWithoutDemoMode` | 2 | O15, Step 9 `01` |
| README final + sinkronisasi ADR + docs | 3 | Step 11 |
| `demo-verify` lengkap + video final A–D | 2 | Step 8 `02` §8 |
| Audit DoD + traceability meta test | 2 | `04` |
| Latihan demo + interview pass | 1 | Step 11 |
| **Total** | **15** | |

**Dependency:** semua sprint sebelumnya.
**Definition of Done:** seluruh DoD project di `04` tercentang; semua LF P0 dan P1 selesai; P2 tercatat sebagai "deferred" dengan alasan.
**Risk:** polishing tanpa akhir. *Mitigasi:* batas waktu keras satu sprint; sisa catatan masuk "future work" di README.
**Demo-able:** Demo A–D penuh + dashboard + `make demo-verify` hijau.

---

## 11. Cakupan fitur per sprint

| LF | Fitur | Prioritas | Sprint |
|---|---|---|---|
| LF-01 | User auth | P0 | S1 (dasar), S4 (refresh rotation, lockout) |
| LF-02 | Wallet & riwayat | P0 | S2 |
| LF-03 | Ledger core | P0 | S2 |
| LF-04 | P2P transfer | P0 | S3 |
| LF-05 | Idempotency | P0 | S3 |
| LF-06 | Mock provider | P0 | S5 (charges), S6 (payout), S9 (settlement) |
| LF-07 | Top-up + inbox | P0 | S5 |
| LF-08 | Outbox + job runner | P0 | S6 |
| LF-09 | Merchant + API key | P0 | S4 |
| LF-10 | Payment intent | P0 | S4 |
| LF-11 | Payout unknown outcome | P0 | S6 |
| LF-12 | Audit log | P0 | S1 (tabel), S4 (login), S8 (ops) |
| LF-13 | Health, log, correlation | P0 | S1, S7 |
| LF-14 | UI minimal | P0 | S5, S7, S8 |
| LF-15 | Webhook keluar | P1 | S6 |
| LF-16 | Refund | P1 | S8 |
| LF-17 | Reconciliation | P1 | S9 |
| LF-18 | Ops console | P1 | S8, S10 |
| LF-19 | Business-invariant metrics | P1 | S7, S10 |
| LF-20 | Rate limiting | P2 → **dinaikkan ke MVP** | S4 (C-07 in-memory) |
| LF-21–26 | Batas, PIN, fee, email, risk, snapshot | P2/P3 | Hanya jika ada buffer (§13) |

---

## 12. Gantt per workstream

| Sprint | Domain | Security | Testing | Infra / DevOps | Observability | Docs / Demo |
|---|---|---|---|---|---|---|
| S0 | — | — | Spike testcontainers | Repo, CI lint | — | Kurasi docs, OpenAPI |
| S1 | Identity | argon2id, JWT, decoder strict | Unit + integration harness | Compose dev, CI 4 job | slog + request ID, health | — |
| S2 | Ledger, wallet | Grant append-only, BOLA wallet | Property test | — | — | — |
| S3 | Transfer, idempotency | Recipient generik | **Concurrency suite** | — | — | Script Demo A/B |
| S4 | Merchant, payment intent | **Hardening sesi, rate limit, header, API key** | Security tests | — | Audit login | — |
| S5 | Mock-provider, top-up, inbox | HMAC + window webhook | **FS tests**, E2E skeleton | — | — | UI minimal |
| S6 | Job runner, payout, webhook keluar | **Egress guard SSRF** | Job/payout chaos | — | — | Demo C lokal |
| S7 | — | — | — | **Deploy publik minimal** | **Metrik, dashboard, alert, trace** | README draft, video A–C |
| S8 | Refund, ops console | Audit ops, bukti resolusi | Refund race | **CD, rollback, backup, restore drill** | Alert backup | UI checkout |
| S9 | Reconciliation | — | Perf smoke nightly | VM perf efemeral | Profiling | Laporan baseline, Demo D |
| S10 | Ops recon | Guard mode demo | Traceability meta test | Image demo | Dashboard integrity | README final, video final, audit DoD |

---

## 13. P2/P3 yang ditunda

| Item | Prioritas | Alasan ditunda | Kapan dikerjakan |
|---|---|---|---|
| LF-21 batas saldo/transaksi | P2 | Tidak menambah sinyal correctness baru; angka regulasi perlu diverifikasi | Buffer setelah M4 |
| LF-22 PIN / step-up | P2 | Bernilai untuk threat "payout setelah akun diambil alih", tapi UX + test cukup besar | Buffer; tercatat di threat model sebagai production enhancement |
| LF-23 fee (journal 3 kaki) | P3 | Menarik untuk diskusi, tapi menambah kasus pembulatan | Hanya dibahas di interview |
| LF-24 email | P3 | Consumer outbox tambahan; pola sudah terbukti | Tidak dibangun |
| LF-25 risk rules | P3 | Di luar fokus | Tidak dibangun |
| LF-26 balance snapshot | P3 | Hanya jika load test membuktikan perlu | Tergantung hasil S9 |
| `MERCHANT_PENDING` (O11) | V2 | Hanya jika E-1 memenuhi aturan keputusan | Tergantung hasil S9 |
| Loki + Promtail | Infra | Memori VM; log via `docker logs` cukup untuk M3 | S10 atau setelah M4 |
| cosign signing image | P2 | Supply chain sudah dijaga pin digest + trivy | Setelah M4 |

---

## 14. Cut list jika terlambat (urutan yang dipotong lebih dulu, tetap mengamankan M3)

**Tidak boleh dipotong sebelum M3:** idempotency transaksional, lock ordering + concurrency test, ledger + deferred trigger, payout state machine + job runner dengan fencing, Demo A–C, deploy publik minimal, README + video.

| Urutan | Yang dipotong / digeser | Ke mana | Dampak |
|---|---|---|---|
| 1 | Tracing OTel (S7) | S10 | Demo C tanpa trace; log korelasi tetap ada |
| 2 | Dashboard kedua + alert (S7) | S10 | Satu dashboard "Money Movement" cukup untuk M3 |
| 3 | Webhook keluar + egress guard (S6) | S8 | LF-15 (P1) mundur; tidak dibutuhkan Demo A–C |
| 4 | Payment intent + checkout (S4) | S8 | "Payment platform" baru lengkap setelah M3; hardening S4 tetap jalan |
| 5 | UI top-up/payout (S5/S7) | Demo lewat curl + skrip | Demo HR kurang visual; demo teknis tetap utuh |
| 6 | Playwright E2E (S5) | S8 | Diganti smoke test skrip |
| 7 | Satu sprint buffer (`00-index` §1) | — | Dipakai terakhir, setelah urutan 1–6 |

> Prinsipnya: yang dipotong adalah **lebar**, bukan **kedalaman**. Satu alur uang yang terbukti benar di bawah duplikat, konkurensi, dan crash lebih bernilai daripada lima fitur yang tidak teruji.
