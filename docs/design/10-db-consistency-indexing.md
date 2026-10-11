# LedgerPay: Consistency, Indexing, Growth & Mock-Provider DB

> Bagian dari desain database — Database Design. Spesifikasi tabel: [`09-db-schema.md`](09-db-schema.md).
> Angka volume = skenario uji (index §8), **bukan klaim kapasitas**. Rencana eksekusi (plan) di bawah adalah *ekspektasi* yang wajib diverifikasi dengan `EXPLAIN (ANALYZE, BUFFERS)` saat load test (desain observability & testing/Phase 12).

---

## 1. Strong vs Eventually Consistent Data

| Data | Model | Kenapa | Cara dibaca |
|---|---|---|---|
| Saldo wallet (`accounts.balance`) | **Strong** | Cek saldo terhadap angka basi = double-spend (LI-02) | Selalu dari primary, di dalam tx dengan `FOR UPDATE` saat akan dipakai untuk mengubah uang; tampilan saldo juga dari primary (`Cache-Control: no-store`) |
| `journals` / `journal_entries` | **Strong** | Sumber kebenaran; ditulis atomik dengan saldo | Primary. V2: riwayat boleh dari replica (lihat bawah) |
| `idempotency_keys` | **Strong** | Commit key ⇔ commit efek (LI-05) | Primary, di tx yang sama dengan efek |
| Status `topups`, `payment_intents`, `payouts` | **Strong** | Transisi dijaga guard `WHERE status = $from`; keputusan bisnis bergantung pada status terkini | Primary |
| `refunded_amount` | **Strong** | LI-08 dibandingkan di satu baris yang di-lock | Primary + `FOR UPDATE` |
| `webhook_inbox` (dedupe) | **Strong** | Unique `(provider, provider_event_id)` harus menolak duplikat seketika | Primary |
| `jobs` (outbox) | **Strong saat ditulis** (di tx bisnis), **eventual saat dieksekusi** | Pekerjaan pasti ada jika efek ter-commit; dijalankan beberapa detik kemudian | Worker claim di primary |
| Saldo akun sistem (`PROVIDER_CLEARING`, …) | **Eventual / derived** | Tidak di-lock per transaksi untuk menghindari hot row (ADR-L07); tidak ada keputusan real-time yang bergantung padanya | Agregasi dari `journal_entries` (§5); V2: snapshot + delta |
| Hasil recon (`recon_runs`, `recon_items`) | **Eventual** | Dihitung harian dari laporan provider; mismatch bisa sah karena cut-off | Primary (ops), tidak kritis latensi |
| Pengiriman webhook merchant (`webhook_deliveries`) | **Eventual** | At-least-once dengan retry hingga 24 jam; merchant juga bisa polling status intent | Primary (ops) |
| Riwayat transaksi untuk tampilan | Strong di V1; **eventual di V2** (read replica) | Riwayat boleh tertinggal < 1 detik; **saldo tidak** | V2: replica untuk `GET /history`, saldo tetap dari primary. UI menampilkan saldo dari primary sehingga tidak ada kontradiksi yang menyesatkan; entri terbaru mungkin muncul sedetik kemudian |
| Metrik (drift, umur `UNKNOWN`, queue depth) | **Eventual** | Dikumpulkan berkala (scrape 15 detik, verifikasi 5 menit) | Prometheus |

Aturan praktis: **apa pun yang dipakai untuk memutuskan perpindahan uang dibaca dari primary di dalam tx yang mengubahnya.** Yang hanya ditampilkan atau dianalisis boleh eventual.

---

## 2. Query Patterns → Index Catalogue

| # | Pola | Bentuk SQL (ringkas) | Index | Plan yang diharapkan | Catatan |
|---|---|---|---|---|---|
| Q1 | Riwayat wallet (cursor, terbaru dulu) | `SELECT e.id, e.direction, e.amount, e.created_at, j.purpose, j.source_type, j.source_id FROM journal_entries e JOIN journals j ON j.id = e.journal_id WHERE e.account_id = $1 AND (e.created_at, e.id) < ($ts, $id) ORDER BY e.created_at DESC, e.id DESC LIMIT 51` | `ix_journal_entries_account_created (account_id, created_at, id)` | Index Scan Backward + Nested Loop ke `pk_journals` (51 lookup) | Cursor = `(created_at, id)`; `LIMIT n+1` untuk `has_more`. Waktu konstan berapa pun dalamnya halaman |
| Q2 | Verifikasi saldo per akun (LI-03) | `SELECT account_id, sum(CASE direction WHEN 'C' THEN amount ELSE -amount END) FROM journal_entries WHERE account_id = ANY($touched) GROUP BY account_id` | sama (Q1) dengan `INCLUDE (direction, amount)` | **Index Only Scan** (heap tidak disentuh jika visibility map bersih) | Job `ledger.verify` hanya memeriksa akun yang **disentuh sejak watermark terakhir** (diambil dari entries `created_at > $watermark`), lalu menghitung Σ penuh akun-akun itu. Bandingkan dengan `accounts.balance` → metrik `ledger_balance_drift_total` |
| Q3 | Claim job | (tabel shared) `… WHERE (status='queued' AND run_at <= now()) OR (status='running' AND locked_until < now()) … FOR UPDATE SKIP LOCKED` | Didefinisikan di `07-db-shared-tables.md` | — | — |
| Q4 | Claim inbox | `SELECT … FROM webhook_inbox WHERE status = 'RECEIVED' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT $n FOR UPDATE SKIP LOCKED` | `ix_webhook_inbox_claimable (next_attempt_at) WHERE status = 'RECEIVED'` | Index Scan pada partial index kecil | Partial index hanya berisi baris yang belum diproses (biasanya puluhan baris), walaupun tabelnya berisi jutaan |
| Q5 | Payout yang menunggu status check | Dijadwalkan oleh `jobs` (`payout.status_check`, `run_at`) | Index claim `jobs` | — | `payouts` tidak di-scan untuk penjadwalan (lihat 03 §11) |
| Q6 | Payout `SUBMITTED` menggantung / umur `UNKNOWN` | `SELECT id, updated_at FROM payouts WHERE status = 'UNKNOWN' ORDER BY updated_at LIMIT 1` | `ix_payouts_unknown`, `ix_payouts_stuck_submitted` | Index Scan partial | Sumber metrik `payout_unknown_oldest_age_seconds` |
| Q7 | Sweeper top-up kedaluwarsa | `SELECT id FROM topups WHERE status = 'PENDING' AND expires_at < now() ORDER BY expires_at LIMIT 100` | `ix_topups_expire_sweep` | Index Scan partial | Sweeper memanggil status query provider **sebelum** transisi (desain domain) |
| Q8 | Sweeper intent kedaluwarsa | `… FROM payment_intents WHERE status = 'REQUIRES_CONFIRMATION' AND expires_at < now() …` | `ix_payment_intents_expire_sweep` | Index Scan partial | Transisi dengan guard status |
| Q9 | Recon: muat sisi internal untuk satu tanggal | `SELECT id, provider_ref, amount, status FROM topups WHERE provider = $p AND created_at >= $from AND created_at < $to` (+ toleransi cut-off ±1 jam) | `ix_topups_provider_created`; payout: `ix_payouts_created` | Index Range Scan | Dimuat ke memori per chunk, lalu di-join dengan baris CSV by `provider_ref`/`payout_id` |
| Q10 | Recon: lookup satu referensi | `SELECT … FROM topups WHERE provider = $1 AND provider_ref = $2` | `uq_topups_provider_ref` | Index Scan unik | Untuk baris CSV yang tidak ketemu di rentang tanggal (event lintas hari) |
| Q11 | Ops cari by ID | `SELECT … FROM <tabel> WHERE id = $1` di setiap tabel transaksi | PK masing-masing | 6 lookup PK | UUID tidak membawa tipe. Endpoint ops mencoba semua tabel (murah), lalu menampilkan journal via `uq_journals_source` |
| Q12 | Ops cari by provider ref / merchant order | `topups (provider, provider_ref)`; `payment_intents (merchant_id, merchant_order_id)` | `uq_topups_provider_ref`; `uq_payment_intents_merchant_order` | Index Scan | Pencarian merchant order wajib menyertakan merchant |
| Q13 | Merchant me-list intent | `… FROM payment_intents WHERE merchant_id = $1 AND (created_at, id) < (…) ORDER BY created_at DESC, id DESC LIMIT 51` | `ix_payment_intents_merchant_created` | Index Scan Backward | Scoping BOLA (`merchant_id = $principal`) sekaligus memakai index |
| Q14 | Journal sebuah objek bisnis | `SELECT … FROM journals WHERE source_type = $1 AND source_id = $2` | `uq_journals_source` (prefix) | Index Scan | Dipakai detail transaksi & ops timeline |
| Q15 | Cleanup idempotency kedaluwarsa | (shared) `DELETE … WHERE expires_at < now()` dalam batch | Didefinisikan di `07-db-shared-tables.md` | — | Batch kecil (mis. 5.000 baris) agar tidak menahan lock lama |
| Q16 | Autentikasi API key | `SELECT … FROM merchant_api_keys WHERE prefix = $1 AND revoked_at IS NULL` | `uq_merchant_api_keys_prefix` | Index Scan unik | Lalu bandingkan `key_hash` constant-time |
| Q17 | Lock wallet berurutan | `SELECT id, balance, status FROM accounts WHERE id = ANY($1) ORDER BY id FOR NO KEY UPDATE` (desain transaksi & concurrency, final review) | `pk_accounts` | Index Scan + LockRows | ADR-L03 |

---

## 3. Rasional Composite Index & Index yang Sengaja Tidak Dibuat

### 3.1 Kenapa composite index berbentuk seperti itu
| Index | Urutan kolom | Alasan |
|---|---|---|
| `(account_id, created_at, id) INCLUDE (direction, amount)` | Equality dulu (`account_id`), lalu kolom urutan (`created_at, id`) | Satu index melayani riwayat (Q1), verifikasi saldo (Q2, index-only), dan saldo akun sistem (§5). `id` sebagai tie-breaker membuat cursor stabil walaupun beberapa entry punya `created_at` sama (semua entry dalam satu tx berbagi `now()`) |
| `(merchant_id, created_at, id)` | Equality → urutan | Pola sama untuk list merchant |
| `(provider, created_at)` di `topups` | Equality → range | Recon per provider per hari |
| Partial index `WHERE status = …` | Hanya baris "aktif" | Status punya kardinalitas rendah. Index penuh pada `status` hampir tidak berguna, sedangkan partial index kecil, selalu panas di cache, dan murah dipelihara |

> **Alternatif yang dipertimbangkan:** karena UUIDv7 sudah time-ordered, index `(account_id, id)` saja bisa melayani urutan riwayat. Ini ditolak karena urutan UUIDv7 berasal dari clock aplikasi (bisa berbeda antar instance), sedangkan `created_at` berasal dari DB. Tambahan 8 byte per entry dianggap sepadan untuk urutan yang jelas.

### 3.2 Index yang sengaja **tidak** dibuat
| Kandidat | Kenapa tidak |
|---|---|
| `accounts (balance)` / `accounts (updated_at)` | Tidak ada query yang memakainya, dan index pada kolom yang di-update setiap transaksi akan mematikan HOT update (§6) |
| `journals (created_at)` | Tidak ada pola query di V1; laporan per periode masuk V2 (partisi/warehouse) |
| `journals (correlation_id)` | Investigasi by correlation ID dilakukan di log terstruktur; ops jarang membutuhkannya di DB. Ditambah kalau ops console membutuhkan |
| `transfers (sender_user_id)` / `(receiver_user_id)` | Riwayat dibaca dari `journal_entries` per akun. Index transfer per user hanya menduplikasi |
| Index pada kolom `status` saja (tanpa partial) | Kardinalitas rendah; planner akan memilih seq scan |
| `payment_intents (merchant_order_id)` tanpa merchant | Pencarian order selalu dalam konteks merchant (BOLA) |
| GIN pada `webhook_inbox.payload` / `jobs.payload` | Payload tidak di-query isinya; kolom yang perlu dicari (`resource_ref`, `kind`) sudah dikeluarkan menjadi kolom biasa |
| Index `journal_entries (account_id)` saja | Tercakup oleh prefix index composite |

Setiap index tambahan memperlambat insert ke `journal_entries` (tabel dengan tulisan terbanyak). Default-nya adalah **tidak menambah index tanpa query yang membutuhkannya**.

---

## 4. Hot Spots & Locking Footprint

| Use case | Baris yang di-lock (urutan) | Lama lock | Potensi kontensi |
|---|---|---|---|
| Transfer | `idempotency_keys` (insert unik) → 2 baris `accounts` wallet (`ORDER BY id`) | ms (tanpa network) | Wallet yang sama dipakai paralel (Demo B) → antre, sesuai harapan |
| Confirm payment | key → `payment_intents` (1 baris) → wallet user + **wallet merchant** | ms | **Wallet merchant populer adalah hot row** (lihat bawah) |
| Refund | key → intent → wallet merchant + wallet user | ms | Sama |
| Create payout | key → wallet user | ms | Rendah |
| Payout submit Tx-a / Tx-b | Guard status `payouts` (row lock implisit oleh UPDATE) → (reversal) wallet user | ms per tx | Rendah |
| Proses inbox top-up | Baris inbox (`SKIP LOCKED`) → guard `topups` → wallet user | ms | Rendah |
| Recon | Insert `recon_items` batch | Per chunk | Tidak ada lock pada objek bisnis kecuali resolusi |
| Posting ke akun sistem | **Tidak ada lock** (ADR-L07): hanya insert `journal_entries` | — | Tidak ada |

**Temuan: wallet merchant sebagai hot row.** ADR-L07 menghilangkan kontensi di akun *sistem*, tetapi wallet merchant yang menerima banyak pembayaran tetap di-lock di setiap confirm. Dengan asumsi lock ±2–5 ms, satu merchant bisa diserialisasi ke ratusan pembayaran per detik. Angka itu jauh di atas skenario portfolio, tetapi menjadi batas nyata untuk merchant besar.
- **V1:** diterima dan diukur di load test (skenario "satu merchant, banyak pembayar").
- **V2 (jika terbukti):** kredit ke merchant lewat akun `MERCHANT_PENDING` per merchant yang *tidak* materialized (seperti akun sistem), lalu job sweep berkala memindahkan saldo ke wallet merchant yang materialized. Saldo merchant menjadi sedikit eventual, yang memang wajar untuk merchant (mirip konsep settlement).

---

## 5. Saldo Akun Sistem

Saldo akun sistem tidak disimpan (`balance IS NULL`) dan dihitung dari entries:

```sql
-- Saldo per akun sistem (normal side menentukan tanda)
SELECT a.system_code,
       sum(CASE WHEN e.direction = a.normal_side THEN e.amount ELSE -e.amount END) AS balance
FROM accounts a
JOIN journal_entries e ON e.account_id = a.id
WHERE a.is_system
GROUP BY a.system_code;
```

- Plan: index-only scan pada `ix_journal_entries_account_created` per akun sistem. Di skenario 10 juta journal, akun `PROVIDER_CLEARING` bisa punya beberapa juta entry → **detik**, bukan milidetik. Cukup untuk dashboard ops dan metrik 5-menitan, **tidak** untuk jalur request.
- Invariant global (LI-01 diagregasi): `Σ semua debit = Σ semua kredit` dihitung dengan cara yang sama → metrik `ledger_imbalance_total` (harus 0).

**V2: `account_balance_snapshots`**

| Kolom | Tipe | Fungsi |
|---|---|---|
| `account_id` | `uuid` | Akun sistem |
| `as_of` | `timestamptz` | Batas waktu snapshot (entries dengan `created_at < as_of`) |
| `balance` | `bigint` | Saldo per `as_of` |
| `created_at` | `timestamptz` | |
| PK | `(account_id, as_of)` | |

Saldo terkini = snapshot terakhir + Σ entries `created_at >= as_of`. Snapshot dibuat harian oleh job, dengan `as_of` mundur beberapa menit dari `now()` agar transaksi yang masih berjalan tidak terlewat (entry tidak bisa muncul dengan `created_at` lebih tua dari transaksi terlama yang masih terbuka; job memakai `as_of = min(now() - 5 menit, xact start tertua)`).

---

## 6. Retensi, Pertumbuhan & Pemeliharaan

### 6.1 Pertumbuhan & retensi per tabel
| Tabel | Pertumbuhan (skenario) | Retensi | Mekanisme |
|---|---|---|---|
| `journals` | ~ 10 jt total | **Selamanya** | — |
| `journal_entries` | ~ 2,2 entry/journal | **Selamanya** (arsip partisi lama di V2) | Partisi bulanan V2 |
| `transfers`, `topups`, `payment_intents`, `refunds`, `payouts` | Sebanding dengan journal | Selamanya | — |
| `audit_log` | ~ 5 jt | Minimal 5 tahun (asumsi kebijakan, diverifikasi) | Partisi bulanan V2 |
| `idempotency_keys` | Sebanding request uang | 7 hari | Job `retention.cleanup`, batch delete |
| `jobs` | Sangat cepat (≥ 1 job per transaksi) | `succeeded`: 7 hari; `dead`: 30 hari | Batch delete |
| `webhook_inbox` | 1–3 per top-up | 90 hari untuk status terminal | Batch delete (trigger mengizinkan) |
| `webhook_deliveries` (+ attempts) | 1 per event merchant | 90 hari setelah terminal | Batch delete oleh maintenance (grant terpisah di V2; V1 lewat role owner di job terjadwal) |
| `recon_runs` / `recon_items` | Harian | `MATCHED`: 1 tahun; mismatch: selamanya | Batch delete |
| `refresh_tokens` | Per login | Hapus setelah kedaluwarsa + 7 hari | Batch delete |

### 6.2 Rencana partisi (V2, hanya jika ukuran menuntut)
- **`journal_entries`**: `PARTITION BY RANGE (created_at)` bulanan. PK berubah menjadi `(id, created_at)` karena unique di tabel terpartisi wajib memuat kunci partisi. Tidak ada unique lain di tabel ini, jadi tidak ada yang hilang. FK `journal_entries → journals` tetap bisa dipakai (FK dari tabel terpartisi ke tabel biasa didukung). Query Q1/Q2 tetap memakai index lokal per partisi; partisi lama bisa dipindah ke storage murah / di-detach untuk arsip.
- **`journals` tidak dipartisi.** `uq_journals_source` dan `uq_journals_reversal_of` adalah unique global yang menjaga LI-04/LI-06. Di tabel terpartisi keduanya harus memuat `created_at` dan kehilangan jaminan globalnya. Dengan ~10 juta baris, tabel ini masih nyaman tanpa partisi.
- **`audit_log`**: partisi bulanan, tanpa unique selain PK.

### 6.3 Autovacuum & storage
| Tabel | Pengaturan | Alasan |
|---|---|---|
| `accounts` | `fillfactor = 80`; tanpa index di kolom yang sering di-update | HOT update untuk `balance` di setiap transaksi |
| `jobs` | `fillfactor = 70`, `autovacuum_vacuum_scale_factor = 0.01`, `autovacuum_vacuum_cost_delay = 0`, `autovacuum_analyze_scale_factor = 0.02` | Banyak update status + delete. Dead tuple harus dibersihkan cepat agar claim query tetap cepat |
| `webhook_inbox` | `fillfactor = 80`, `autovacuum_vacuum_scale_factor = 0.02` | Update status sekali per baris + delete retensi |
| `payouts`, `topups`, `payment_intents` | `fillfactor = 90` | Beberapa update status per baris; kolom yang di-update tidak di-index kecuali kolom status di partial index (perubahan status memang mengubah keanggotaan partial index) |
| `journal_entries`, `journals`, `audit_log` | Default (`fillfactor = 100`) | Append-only, tidak ada update. Vacuum hanya untuk visibility map (index-only scan Q2), jadi `autovacuum_vacuum_insert_scale_factor` tetap aktif (default PG ≥ 13) |

---

## 7. Database `mockprovider`

Database terpisah milik service `mock-provider` (ADR-L05). LedgerPay **tidak** punya akses ke database ini; interaksi hanya lewat HTTP. Skema sengaja sederhana (bukan bagian yang dinilai), tetapi harus cukup untuk idempotency, status query, webhook, dan settlement.

| Tabel | Kolom inti | Constraint penting | Fungsi |
|---|---|---|---|
| `charges` | `id`, `idempotency_key`, `merchant_ref` (= topup_id LedgerPay), `amount`, `currency`, `status` (`PENDING/SUCCEEDED/FAILED`), `created_at`, `paid_at` | `UNIQUE (idempotency_key)` | `POST /v1/charges` idempoten; halaman checkout mengubah status |
| `payouts` | `id`, `idempotency_key` (= payout_id LedgerPay), `bank_code`, `account_last4`, `amount`, `status` (`PROCESSING/SUCCEEDED/FAILED`), `created_at`, `processed_at` | **`UNIQUE (idempotency_key)`** | Bank tidak pernah memproses key yang sama dua kali; `GET /v1/payouts/{key}` → `NOT_FOUND` jika tidak ada baris |
| `webhook_outbox` | `id`, `event_id`, `event_type`, `charge_id`, `payload`, `deliver_after`, `attempts`, `status` | `UNIQUE (event_id)` | Mengirim webhook ke LedgerPay; skenario `duplicate`/`delay`/`out_of_order` dimainkan di sini (kirim ulang event yang sama, tunda, atau tukar urutan) |
| `settlement_reports` | `business_date`, `generated_at`, `content` (CSV), `content_hash`, `mismatch_injected` | `PRIMARY KEY (business_date, generated_at)` | `GET /v1/settlements/{date}.csv`; skenario `settlement_mismatch:*` mengubah konten |
| `scenario_config` | `id = 1`, `scenario`, `params jsonb`, `updated_at` | `CHECK (id = 1)` (satu baris) | Toggle global (`PUT /admin/scenario`) |
| `transfer_log` | `id`, `payout_id`, `idempotency_key`, `amount`, `executed_at` | — | Bukti demo "bank hanya mengirim 1×" (`GET /admin/transfers`) |

`POST /admin/reset` men-truncate semua tabel lalu men-seed dengan `MOCK_SEED` (determinisme demo). Storage: Postgres yang sama (database berbeda) di Docker Compose, atau SQLite. Pilihan final diserahkan ke implementasi, karena tidak mempengaruhi desain LedgerPay.

---

## 8. Pemetaan Constraint → Error Domain

> **(final review R1-6)** Tabel pemetaan lama di bagian ini **dihapus** karena bertentangan dengan katalog final, misalnya error append-only (`42501 → 409 IMMUTABLE_RECORD`, bukan `P0001 → 500`) dan deadlock yang retry-nya habis (`409 RESOURCE_BUSY`, bukan `503`). **Katalog kontrak API [`12-api-error-catalogue.md`](12-api-error-catalogue.md) §2 dan §4 otoritatif** untuk nama constraint → code. Yang tetap berlaku di sini hanya perilaku khusus LedgerPay berikut:
> - `uq_journals_source` di jalur **worker** = no-op idempoten (efek sudah ada, tx di-rollback, job selesai).
> - `uq_webhook_inbox_event`, `uq_recon_runs_file`, dan `uq_recon_items_run_*` = bagian alur normal (dedupe), bukan error.
> - `ck_accounts_balance_non_negative` yang tertembak = bug APP: tetap `422 INSUFFICIENT_BALANCE` ke client, ditambah log error + metrik.

Pola umum: **constraint yang seharusnya sudah dicegah oleh APP dipetakan ke error ramah, tetapi juga dicatat sebagai bug** (log error + metrik). Constraint yang menjadi bagian alur normal (dedupe, idempotency) diperlakukan sebagai no-op/replay.
