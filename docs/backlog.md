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
- [ ] `GET /merchant/payment-intents` (list + cursor) belum dibuat; `GET /merchant/balance` selesai Sprint 8
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
- [ ] Event `CONFLICT` hanya dicatat log. Tabel `recon_items` (source `WEBHOOK`) sudah ada sejak Sprint 9; penulisan dari inbox belum
- [x] Metrik `webhook_late_total`, `webhook_inbox_dead_total`, alert DEAD — selesai Sprint 7
- [ ] Replay event inbox `DEAD` dari ops console (`DEAD → RECEIVED`): belum. Replay webhook KELUAR selesai Sprint 8
- [ ] Retensi inbox (hapus > 90 hari, bukan `RECEIVED`) — job cleanup Sprint 6
- [ ] SPA memakai fetch wrapper tangan; desain: client typed dari `openapi-typescript` (Sprint 8, bersama UI checkout)
- [ ] Job CI `frontend`/`e2e` belum pernah berjalan di GitHub (repo lokal)
- [x] Worker belum punya endpoint health/metrics (compose: healthcheck dimatikan) — selesai Sprint 7

## Sprint 6 — Background Processing ✅
- [x] Job runner `internal/platform/jobs`: claim SKIP LOCKED + lease, heartbeat, backoff + jitter, dead + `OnDead` di tx yang sama, `CompleteInTx` dengan fencing, `Permanent`, panic recovery, lane (bulkhead), scheduler tanpa leader election
- [x] Payout: migrasi `payouts` (rekening AES-GCM), `POST /payouts` (hold + outbox), `GET /payouts/{id}`, `payout.submit` (SUBMITTED dicommit sebelum bank), `payout.status_check` (30s…6h → `needs_reconciliation`), respons tak cocok → UNKNOWN, resubmit hanya setelah safe window
- [x] Mock bank (`/v1/payouts`, status query, `/admin/transfers`, magic 0408/0409/0503/0422, `response_mismatch`)
- [x] Sweeper terjadwal: `topup.recover`, `topup.expire_sweep` (cek provider dulu), `intent.expire_sweep` (+ webhook `payment.expired`), `ledger.verify`, cleanup jobs & idempotency
- [x] Webhook keluar (`webhookout`): outbox di tx confirm/sweeper, HMAC per merchant (secret terenkripsi), 7 percobaan → DEAD, egress guard SSRF (`platform/egress`)
- [x] Keyring PII (`platform/crypto`), CLI `merchant-webhook`, validasi dependency saat start, Demo C (`make demo-c`)

## Catatan dari Sprint 6 (dibawa ke sprint berikutnya)
- [x] Endpoint ops: `POST /ops/payouts/{id}/resolve`, `GET /ops/jobs`, `POST /ops/jobs/{id}/retry`, `POST /ops/webhook-deliveries/{id}/replay` — selesai Sprint 8
- [ ] `GET /payouts` (list + cursor) untuk UI (Sprint 8)
- [x] Metrik `job_dead_total`, `payout_unknown_current`, `payout_unknown_oldest_age_seconds`, `db_deadlock_total`, invariant ledger + alert — selesai Sprint 7
- [x] Worker belum punya endpoint health/metrics; compose healthcheck dimatikan — selesai Sprint 7
- [ ] `maintenance.refresh_token_cleanup` dan `maintenance.inbox_cleanup` belum dijadwalkan
- [ ] Job `pii.reencrypt` (dibutuhkan saat rotasi kunci keyring pertama kali)
- [ ] `EXPLAIN (ANALYZE, BUFFERS)` query claim jobs (amandemen ADR-006): belum; subset load test S9 tidak membebani antrean job
- [ ] Mock bank tidak pernah menjawab `202 PROCESSING`; cabang `OutcomeProcessing` belum teruji end-to-end
- [x] `needs_reconciliation` baru ditandai; payout yang menggantung kini muncul di recon sebagai `STATUS_MISMATCH`/`MISSING_EXTERNAL` — Sprint 9
- [ ] Konfigurasi webhook merchant hanya lewat CLI (belum ada rotasi secret dual-window, §6.2)

## Sprint 7 — Observability · M3 ✅
- [x] Metrik Prometheus (`internal/platform/metrics`, registry sendiri): HTTP per route template, error per kode, rate limit, retry/deadlock/lock timeout DB, pool collector, job queue (depth, oldest, lag outbox, dead, fenced, lease lost), outbound per target, auth, idempotency, hasil bisnis (transfer/payment/topup/payout), inbox, webhook keluar
- [x] Gauge invarian ledger dari `ledger.verify` (drift akun, journal tidak seimbang, imbalance global, timestamp sukses); gauge payout `UNKNOWN` + umur tertua dari query DB (bukan counter per proses)
- [x] Tracing OpenTelemetry → Jaeger: span HTTP bernama route template, span DB dengan nama query sqlc (tanpa SQL/parameter), outbound; `jobs.trace_parent` (migrasi 00007) → span job root baru dengan link ke request
- [x] Worker ops server (`/healthz`, `/readyz` berbasis heartbeat lane, `/metrics`); healthcheck compose worker aktif lagi
- [x] Rule alert + `promtool test rules` (`make test-alerts`); dashboard Grafana *Money & Integrity* dan *Platform* digenerate dari Python (`make dashboards`); profil compose `obs` (`make up-obs`)
- [x] SPA: halaman tarik saldo + status payout; E2E diperluas dengan langkah payout
- [x] Deploy produksi (`deploy/prod/`): image edge Caddy (SPA dipanggang), `compose.prod.yml`, `init-env.sh`, `deploy.sh` (git archive, mode `local`), `remote-deploy.sh` (migrate → worker → api, health gate, smoke, rollback otomatis), `bootstrap.sh`, `cloud-init.yaml`; shellcheck di `make lint`
- [x] README draft (bahasa Inggris) dengan placeholder `[LINK]`, `[SCREENSHOT]`, `[MEASURE]`

## Catatan dari Sprint 7 (dibawa ke sprint berikutnya)
- [ ] **Deploy sungguhan ke VM + domain**: **ditunda sampai aplikasi siap dipakai** (keputusan 2026-10-04). Skrip tetap dijaga berfungsi lewat `deploy.sh local` setiap sprint. Nanti dijalankan user: `cloud-init.yaml` / `bootstrap.sh`, `init-env.sh`, `deploy.sh <host> <sha>`; isi link live di README
- [ ] **Video demo ~5 menit** (direkam user) + screenshot Grafana/Jaeger untuk README
- [ ] Sisa katalog metrik: histogram durasi transaksi DB per nama, `app_http_request_size`, metrik scheduler per job terjadwal
- [ ] Loki/agregasi log belum ada (log JSON hanya di `docker logs`)
- [ ] Alertmanager belum dipasang: rule alert dievaluasi Prometheus tetapi tidak ada notifikasi (email/Telegram)
- [x] Image dibangun di VM (`docker build` saat deploy). Desain: build di CI → GHCR → deploy — selesai Sprint 8 (pull mode; tag = SHA, belum digest)
- [x] Secret produksi: diputuskan Sprint 8 — tetap di VM (diunggah sekali), CI tidak memegang secret aplikasi (ADR-013 catatan S8)
- [ ] Konfigurasi deploy ada di repo aplikasi (`deploy/prod/`), bukan repo infra terpisah seperti di desain: cukup untuk satu VM, dicatat sebagai deviasi
- [ ] Rollback = deploy ulang SHA sebelumnya (downtime API beberapa detik saat recreate); blue/green tidak dikerjakan
- [ ] Toolbar demo / `DEMO_TOKEN` untuk demo publik (skenario mock dari UI) — Sprint 8
- [ ] Metrik counter (mis. `payouts_total{submitted}`) hilang saat proses restart; dashboard memakai `increase()`/gauge dari DB, jangan membaca nilai mentah counter

## Sprint 8 — DevOps ✅ (deploy sungguhan ditunda sampai aplikasi siap)
- [x] Refund `POST /merchant/payment-intents/{id}/refunds` (migrasi 00008 `refunds` append-only): lock intent → journal REFUND → `refunded_amount`/status → outbox `payment.refunded`; `refunds[]` di view merchant + payer; `GET /merchant/balance`
- [x] LI-08: test paralel (barrier + hook dengan lock), mutasi "hapus `FOR UPDATE`" tertangkap; deteksi `refunded_amount ≠ Σ refunds` di `ledger.verify` (gauge + alert) dan CLI `ledger-verify`
- [x] Ops console API (modul `ops`, read model): search, journal detail, antrean payout, resolve payout dengan bukti + audit, daftar job + retry, daftar delivery + replay; race (g) status check vs ops diuji dua arah
- [x] UI checkout merchant (`/checkout/:id`) + E2E checkout → refund; label Refund di riwayat
- [x] `make demo-ops` (refund paralel + ops resolve payout UNKNOWN), `make demo-verify` (semua demo)
- [x] Backup: image `deploy/backup` (pg_dump 17 + age + rclone), role read-only `ledgerpay_backup`, dua recipient age, `.sha256`, retensi, offsite opsional, metrik via Pushgateway + alert teruji; cron host lewat `bootstrap.sh`
- [x] Restore drill `scripts/restore-drill.sh` (PASS + tiga kasus gagal yang benar)
- [x] CD: `pr.yml` reusable, `main.yml` (build → push GHCR → Trivy → deploy pull-mode → smoke internet → auto-rollback), `rollback.yml`, `nightly.yml` (demo-verify, restore drill mingguan); deploy lock, `releases.log`, `--skip-migrate`
- [x] OpenAPI diperbarui (refund, balance, 9 endpoint ops)

## Catatan dari Sprint 8 (dibawa ke sprint berikutnya)
- [ ] **Workflow CD belum pernah berjalan di GitHub** (repo masih lokal). Diuji: actionlint + simulasi lokal (registry lokal, deploy pull-mode, rollback). Saat repo di-push: set variable `DEPLOY_ENABLED`, `DEPLOY_HOST`, `PUBLIC_DOMAIN`, secret `DEPLOY_SSH_KEY`, `DEPLOY_KNOWN_HOSTS`; untuk drill `BACKUP_REMOTE`, `BACKUP_S3_*`, `BACKUP_READ_*`, `RESTORE_DRILL_AGE_KEY`
- [ ] Offsite backup belum diuji ke bucket sungguhan (R2/B2); rclone hanya dijalankan kalau `LP_BACKUP_REMOTE` di-set
- [ ] `demo-verify` masih skeleton: asersi sebagian besar lewat `set -e` + `curl -f` + `ledger-verify`; Demo B belum meng-assert "tepat 10 sukses" secara eksplisit
- [ ] Ops console **UI** belum ada (API saja); dijadwalkan bersama recon di S10
- [ ] `GET /payouts` (list user) dan `GET /merchant/payment-intents` (list) masih belum
- [ ] Notifikasi nightly gagal: hanya email bawaan GitHub (issue otomatis `nightly-failure` belum)
- [ ] Pencarian ops `merchant_order_id` tanpa `merchant_id` tidak memakai indeks (uq parsial berawalan merchant_id): belum diukur
- [ ] Rate tier ops write memakai `money-write` (30/menit); desain menyebut tier `write` yang belum didefinisikan
- [ ] Penyimpanan Pushgateway in-memory: restart Pushgateway → `BackupMissing` sampai backup berikutnya (diterima; alert itu sendiri yang memberi tahu)

## Sprint 9 — Performance + Reconciliation ✅
- [x] Rekonsiliasi (LF-17, Demo D): migrasi 00009 (`recon_runs`, `recon_items`, fakta immutable), mock `GET /v1/settlements/{date}.csv` + skenario `settlement_mismatch:missing|amount|status|extra`, modul `recon` (parser + classifier murni, `PROVIDER_SETTLEMENT` dedupe per top-up, lock advisory per tanggal), job `recon.daily` (02:30 WIB, catch-up), `POST/GET /ops/recon-runs`, `GET /ops/recon-items`, `POST /ops/recon-items/{id}/resolve` (APPLY_PAYOUT_SETTLE/REVERSAL lewat `ResolveByOps`, NO_ACTION_*), gauge item OPEN, `make demo-d`
- [x] Seed `ledgerpay seed smoke|perf` (COPY + `session_replication_role`, gate invariant via role aplikasi): 1.001.005 journal dalam 49,6 dtk
- [x] Perf env (`deploy/perf/compose.perf.yml`: pg_stat_statements, auto_explain, pprof, rate limit off — ditolak di luar dev/test/perf), metrik `ledgerpay_ledger_lock_wait_seconds`
- [x] k6 LP-2, LP-3, LP-4 (+ kontrol), LP-9; `perf/run.sh` (bukti + cek invariant per skenario), `perf/stages.py`
- [x] Laporan baseline laptop (`docs/performance.md`, `perf/results/2026-10-05-d442f04/`), keputusan E-1, OpenAPI recon

## Catatan dari Sprint 9 (dibawa ke sprint berikutnya)
- [ ] **E-1 → `MERCHANT_PENDING` (V2)**: aturan keputusan terpicu di baseline laptop (lihat ADR-L07). Langkah murah lebih dulu: kurangi round trip di bagian yang memegang lock merchant; ulangi E-1 di VM target
- [ ] Angka performa resmi di VM 2 vCPU / 4 GB dengan k6 dari mesin terpisah (saat deploy)
- [ ] Skenario opsional LP-1, LP-5–LP-8 belum dijalankan; CI perf smoke nightly ditunda sampai setelah M4 (amandemen Step 12)
- [ ] `POST /ops/recon-runs` sinkron (desain: 202 + job) dan upload file manual (multipart) belum ada
- [ ] Top-up `STATUS_MISMATCH` (provider menerima uang setelah top-up kedaluwarsa) belum punya aksi `APPLY_TOPUP_CREDIT`
- [ ] Aksi `REPLAY_INBOX_EVENT` di resolve recon belum ada
- [ ] Alert untuk item recon OPEN yang menumpuk / run harian yang gagal belum dibuat (gauge sudah ada)
- [ ] LP-4 memakai 1.000 payer (desain 5.000): login 5.000 user = 5.000 hash argon2 di setup k6
- [ ] Ide LP-2 (belum diukur): kurangi round trip per transfer (±10 statement) dengan `pgx.Batch`/fungsi SQL
