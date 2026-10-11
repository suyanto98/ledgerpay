# Runbook LedgerPay

Satu halaman untuk setiap alert (`deploy/prometheus/rules/*.yml`, anotasi `runbook` menunjuk ke sini) dan
prosedur operasional. Format: **Dampak → Lihat dulu → Investigasi → Pulihkan → Setelahnya**.
Dashboard: *LedgerPay — Integrity & Recon*, *LedgerPay — Money & Integrity*, *Platform — HTTP, DB & Jobs*
(Grafana `obs.<domain>` atau `make up-obs` lokal). Log: `docker compose logs <svc> | jq` (JSON, field
`request_id`, `correlation_id`, `payout_id`, …); Loki belum dipasang.

Aturan yang tidak pernah dilanggar: **jangan pernah `UPDATE` saldo atau status langsung di database.**
Koreksi uang selalu lewat journal (resolve ops, `ADJUSTMENT_MANUAL` ber-audit) supaya invariant tetap bisa
dibuktikan.

---

## Integritas ledger

### LedgerInvariantViolated
- **Dampak:** saldo mungkin salah atau ledger tidak bisa dipercaya. Insiden tertinggi.
- **Lihat dulu:** gauge INV mana yang ≠ 0 (drift akun, journal tidak seimbang, imbalance global, refund
  drift) dan sejak kapan; bandingkan dengan deploy terakhir (`app_build_info`, `releases.log` di VM).
- **Investigasi:** `ledgerpay ledger-verify` di container api menyebut akun/intent yang bermasalah. Akun
  drift:
  ```sql
  SELECT a.id, a.balance, coalesce(sum(CASE e.direction WHEN 'C' THEN e.amount ELSE -e.amount END), 0) AS from_entries
  FROM accounts a LEFT JOIN journal_entries e ON e.account_id = a.id
  WHERE NOT a.is_system GROUP BY a.id
  HAVING a.balance <> coalesce(sum(CASE e.direction WHEN 'C' THEN e.amount ELSE -e.amount END), 0);
  ```
  Telusuri journal terakhir akun itu → `correlation_id` → log request/job. Periksa `audit_log` dan
  `pg_stat_activity` untuk akses manual.
- **Pulihkan:** hentikan payout (scale worker ke 0) kalau drift menyentuh banyak akun. Koreksi hanya setelah
  akar masalah dipahami, dengan journal yang ber-referensi. Bug di rilis baru → `rollback.yml` ke SHA
  sebelumnya.
- **Setelahnya:** postmortem; test yang mereproduksi; apakah constraint DB bisa mencegahnya.

### IntegrityVerificationStale
- **Dampak:** gauge INV basi: kita buta, bukan sehat.
- **Investigasi:** job `ledger.verify` (lane maintenance): `GET /api/v1/ops/jobs?status=dead&kind=ledger.verify`;
  readiness worker (`/readyz` di port ops); log `ledger.verify`.
- **Pulihkan:** worker mati → restart; job dead → perbaiki penyebab lalu `POST /api/v1/ops/jobs/{id}/retry`.

## Uang yang menggantung

### PayoutUnknownTooOld
- **Dampak:** dana user tertahan > 1 jam.
- **Lihat dulu:** panel payout UNKNOWN + umur tertua; `ledgerpay_payout_status_checks_total{result}`.
- **Investigasi:** halaman Ops (`/ops`) atau `GET /api/v1/ops/payouts?status=UNKNOWN`. `still_unknown`
  terus-menerus = bank bermasalah; `not_found` = ikuti aturan safe window (ADR-L06).
- **Pulihkan:** bukti dari bank (mutasi, file settlement) → resolve dengan bukti (`/ops` atau
  `POST /api/v1/ops/payouts/{id}/resolve`). Rekonsiliasi harian juga akan memunculkannya sebagai
  `STATUS_MISMATCH`. **Tidak pernah** submit ulang manual.
- **Setelahnya:** catat waktu resolusi; cek item recon hari berikutnya.

## Proses latar

### JobsDeadLettered
- **Dampak:** side effect tertunda (payout, webhook merchant, sweeper).
- **Investigasi:**
  ```sql
  SELECT kind, status, count(*), min(run_at), max(attempts) FROM jobs WHERE status IN ('queued','running','dead') GROUP BY 1, 2;
  ```
  `GET /api/v1/ops/jobs?status=dead` (tanpa payload). Bedakan dependency mati dari bug (`Permanent`).
- **Pulihkan:** perbaiki penyebab → retry job; webhook merchant DEAD → `GET /api/v1/ops/webhook-deliveries?status=DEAD`
  lalu replay (event id tetap sama, merchant bisa dedupe).

### OutboxLagHigh
- **Dampak:** side effect > 30 dtk setelah commit.
- **Investigasi:** worker ready? lane mana yang menumpuk (`app_job_queue_depth`)? lease expired/fenced melonjak
  (worker crash-loop)?
- **Pulihkan:** restart worker; job yang lease-nya habis diklaim ulang otomatis.

## Platform

### High5xxRatio
- **Investigasi:** `app_errors_total{code}` per route; log `level=ERROR` (`request.failed`); trace di Jaeger.
- **Pulihkan:** lonjakan setelah deploy → rollback; dependency mati → lihat readiness.

### TransferLatencySLO
- **Investigasi:** trace `POST /v1/transfers` > 200 ms: span `db.tx` menunggu lock? `pg_stat_statements`
  urut `mean_exec_time`; `ledgerpay_ledger_lock_wait_seconds`.
- **Catatan:** baseline laptop: p95 11 ms pada 200 transfer/dtk (`docs/performance.md`).

### DBPoolSaturated
- **Investigasi:** `pg_stat_activity` (`state <> 'idle'`, `wait_event_type = 'Lock'`), transaksi panjang.
- **Pulihkan:** query liar → `pg_cancel_backend`. Hot row merchant (E-1) → bukti untuk `MERCHANT_PENDING`
  (backlog); sementara batasi tier `money-write`.

### ServiceNotReady
- **Investigasi:** `/readyz` di port ops: `database`, `schema` (`schema_version` vs `min_schema_version`),
  untuk worker: lane mana yang `stalled`.
- **Pulihkan:** skema terlalu lama → jalankan migrate yang tertinggal; lane macet → restart worker.

## Keamanan

### WebhookSignatureFailures
- **Dampak:** kemungkinan serangan (webhook palsu/replay) atau rotasi secret yang salah.
- **Investigasi:** log `webhook.signature_invalid` per IP sumber. Semua event gagal setelah rotasi = salah
  konfigurasi. Sporadis dari IP asing = serangan; verify-by-fetch + HMAC berarti tidak ada saldo tercipta.
- **Pulihkan:** perbaiki secret (`PROVIDER_WEBHOOK_SECRETS` mendukung dua secret saat rotasi) atau blokir IP.

## Backup

### BackupStale
- **Dampak:** RPO yang diklaim (24 jam) tidak terpenuhi.
- **Investigasi:** `/srv/ledgerpay/logs/backup.log` di VM; `docker compose ... --profile backup run --rm backup`
  manual; ruang disk; kredensial offsite.
- **Pulihkan:** jalankan `deploy/prod/run-backup.sh`, lalu `scripts/restore-drill.sh` pada file terbaru.

### BackupMissing
- **Investigasi:** cron `/etc/cron.d/ledgerpay-backup` ada? Pushgateway baru restart (state in-memory)?
- **Pulihkan:** jalankan backup manual; metrik muncul lagi setelah push berikutnya.

### BackupSizeDrop
- **Dampak:** kemungkinan dump kosong/terpotong.
- **Pulihkan:** jalankan `scripts/restore-drill.sh <file> <drill-key>`: drill memulihkan, migrate, dan
  menjalankan `ledger-verify`. Gagal → jangan hapus backup lama; investigasi.

## Rekonsiliasi

### ReconStale
- **Dampak:** selisih dengan provider tidak terdeteksi.
- **Investigasi:** job `recon.daily` (tiap jam, aktif setelah 02:30 WIB untuk D−1); `GET /api/v1/ops/recon-runs`.
- **Pulihkan:** jalankan manual dari `/ops` (tombol "Jalankan recon") atau `POST /api/v1/ops/recon-runs`.

### ReconRunFailed
- **Investigasi:** run `FAILED` + kolom `error` (file rusak: header, tanggal, referensi ganda) atau
  `fetch_failed` (provider tidak terjangkau).
- **Pulihkan:** file rusak → hubungi provider, minta file ulang (hash berbeda → run baru); provider mati →
  tunggu, `recon.daily` mencoba lagi tiap jam.

### ReconItemsUnresolved
- **Dampak:** selisih uang dengan provider belum ditangani > 24 jam.
- **Pulihkan:** `/ops` → item OPEN → aksi yang cocok dengan fakta item (APPLY_PAYOUT_SETTLE hanya kalau
  provider SUCCEEDED, dst.) + referensi bukti + alasan. Semua aksi ter-audit.

---

## Prosedur

### Deploy dan rollback
- Otomatis: merge ke `main` → `main.yml` (build → Trivy → deploy pull-mode → smoke → auto-rollback).
- Manual: `deploy/prod/deploy.sh <host> <sha>`. Rollback: workflow `rollback.yml` (SHA + alasan; hanya SHA
  yang pernah `success` di `releases.log`; migrasi dilewati).

### Restore dari backup
`docs/design/26-deploy-migrations-backup.md` §5.4. Selalu jalankan `scripts/restore-drill.sh` terhadap file
yang akan dipakai sebelum menyentuh database produksi.

### Demo (image demo saja)
- Fault deterministik: `POST /api/v1/demo/faults {"point":"payout.after_bank_call","times":1}` dengan header
  `X-Demo-Token` (token di `api.env`, ditempel presenter; tidak pernah di bundle SPA). Bersihkan:
  `DELETE /api/v1/demo/faults`.
- Skenario mock (timeout, settlement mismatch): `PUT /admin/scenario` di mock (port admin hanya di
  `127.0.0.1` VM; pakai SSH tunnel).
- Sandbox pengunjung ("Coba demo" di halaman login): `POST /api/v1/demo/sessions` membuat Budi + 3 kontak +
  merchant Kopi Senja + riwayat dalam satu transaksi, saldo awal dari akun sistem `DEMO_FUNDING` (bukan
  `PROVIDER_CLEARING`, supaya rekonsiliasi provider tidak terganggu). Job `demo.sandbox_expire` (5 menit)
  menonaktifkan user sandbox yang lewat `DEMO_SANDBOX_TTL` (default 24 jam) dan mencabut refresh token-nya;
  journal tidak dihapus (append-only). Batas: 3/menit + 30/jam per IP, `DEMO_SANDBOX_DAILY_MAX` (default
  2000) sandbox per 24 jam → 503. Pantau: `SELECT count(*) FROM demo_sandboxes WHERE created_at > now() - interval '1 day'`.
  Kalau disk VM menipis: turunkan `DEMO_SANDBOX_DAILY_MAX`, atau reset data demo (volume baru) di luar jam ramai.
- Uji ketahanan pengunjung: `POST /api/v1/demo/stress/transfers` (3/menit per user) membuat "Dompet Uji" baru
  (Rp100.000 dari `DEMO_FUNDING`, ikut kedaluwarsa bersama sandbox) dan menjalankan 100 transfer, paralel
  maksimal 8 transaksi supaya pool koneksi API tetap tersedia untuk pengunjung lain. Log: `demo.stress_done`
  (`consistent=false` = bug ledger, perlakukan seperti `LedgerInvariantViolated`).
- `GET /api/v1/demo/wallet/integrity` (pengunjung sandbox): LI-01 + LI-03 untuk wallet pemanggil dalam satu snapshot
  REPEATABLE READ. `ok=false` di demo = perlakukan seperti `LedgerInvariantViolated`.
