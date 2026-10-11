# LedgerPay: Transactions & Concurrency

> Bagian dari desain transaksi & concurrency. Keputusan global (isolation default Read Committed, urutan lock, retry, aturan transaksi) ada di `00-index.md` dan **tidak diulang** di sini. File ini menerapkannya ke setiap operasi LedgerPay.
> Referensi: ADR-011, ADR-006, ADR-L02/L03/L06/L07/L08; `04-domain-model.md` §8, `02`, `03`; `09-db-schema.md`, `04`; `13-api-endpoints.md`.

---

## 0. Urutan lock LedgerPay (diperinci)

Urutan global di `00-index.md` §2 diperinci untuk posisi 2 (objek bisnis), karena beberapa operasi menyentuh lebih dari satu objek bisnis:

```text
1. idempotency_keys                (INSERT → lock implisit pada uq_idempotency_keys_scope)
2a. recon_items                    (guard UPDATE)
2b. webhook_inbox                  (claim SKIP LOCKED / fencing UPDATE)
2c. payment_intents | payouts | topups   (FOR UPDATE atau guard UPDATE … WHERE status)
3. accounts (wallet saja)          WHERE id = ANY($ids) ORDER BY id FOR NO KEY UPDATE
4. INSERT journals, journal_entries, transfers, refunds, webhook_deliveries, jobs, audit_log
```

**Aturan FK:** INSERT baris anak yang mereferensikan `accounts` (`journal_entries`, `transfers`, `payouts`, `topups`) mengambil lock `FOR KEY SHARE` implisit pada baris akun induk. Lock implisit ini dihitung **di posisi 3**, jadi INSERT tersebut hanya boleh dilakukan **setelah** akun wallet yang relevan dikunci, atau pada akun yang tidak pernah dikunci siapa pun (akun sistem, ADR-L07). Satu pengecualian aman: `topups` Tx1 INSERT tanpa mengunci wallet. Alasannya ada di §4.

---

## 1. Katalog operasi

Notasi: **RC** = Read Committed, **RR-RO** = Repeatable Read Read Only. "Key" = INSERT `idempotency_keys` (ADR-L02). Semua transaksi API memakai `lock_timeout 2s`, `statement_timeout 5s`.

| # | Operasi | Transaction boundary | Iso | Lock (urutan) | Race yang dijaga | Retry | Idempotency | Rollback / sisa jika gagal |
|---|---|---|---|---|---|---|---|---|
| 1 | Register | 1 tx: `users` + `accounts` wallet (balance 0) | RC | `uq_users_email` (implisit) | Dua register email sama → satu menang, satu `409 EMAIL_ALREADY_REGISTERED` | `40P01` wrapper | Natural (email unik) | Gagal → tidak ada user maupun wallet (atomik) |
| 2 | Transfer | 1 tx (§2.1) | RC | 1 → 3 (2 wallet) | Double spend, deadlock A↔B, key duplikat paralel | Wrapper 3× untuk `40P01`; `55P03` → 409 | Key (wajib); 201 dan 422 domain disimpan | Atomik: tidak ada efek parsial |
| 3 | Create top-up | **Tx1:** key (response NULL) + `topups` (`PENDING`, `CREATED`). **Panggilan** `POST /v1/charges` (key = topup_id) **di luar tx**. **Tx2:** `topups.provider_ref/checkout_url`, `recovery_point = PROVIDER_CREATED` + response key | RC | Tx1: 1. Tx2: 2c (guard `recovery_point='CREATED'`) | Retry paralel → provider idempoten, Tx2 hanya menang sekali | Client retry dengan key sama → lanjut dari `recovery_point` | Key + `topup_id` sebagai key ke provider | Gagal setelah Tx1 → top-up `CREATED` tanpa charge; dipulihkan oleh retry client **atau** job `topup.recover` |
| 4 | Job `topup.recover` | Scan `ix_topups_recover` (umur > 1 menit) → panggil provider di luar tx → Tx2 yang sama dengan #3 | RC | 2c guard | Bentrok dengan retry client → guard `recovery_point='CREATED'`, yang kalah no-op | Job backoff | Provider key = topup_id | Gagal → tetap `CREATED`, dicoba lagi; akhirnya `EXPIRED` oleh sweeper |
| 5 | Terima webhook (HTTP) | Verifikasi HMAC + window **sebelum** tx → 1 tx: INSERT `webhook_inbox` `ON CONFLICT DO NOTHING` | RC | `uq_webhook_inbox_provider_event` | Webhook ganda paralel → satu baris | Provider retry jika 500 | Unique event id | Gagal sebelum commit → 500, provider mengirim ulang |
| 6 | Proses inbox `charge.succeeded` | Claim (SKIP LOCKED + lease) → **verify-by-fetch** `GET /v1/charges/{id}` di luar tx (P1) → 1 tx (§2.6) | RC | 2b (fencing) → 2c `topups FOR UPDATE` → 3 wallet | Double-processing setelah lease habis; event duplikat/out-of-order; late vs sweeper | Backoff inbox (8×) → `DEAD` | Fencing + transisi monoton + `uq_journals_source` | Atomik: inbox tetap `RECEIVED`, top-up tidak berubah |
| 7 | Sweep top-up kedaluwarsa | Per top-up: cek status di provider **di luar tx** → 1 tx: guard `UPDATE topups SET status='EXPIRED' WHERE status='PENDING'`. Jika provider bilang `SUCCEEDED` → jalankan jalur kredit yang sama dengan #6 | RC | 2c guard | Late webhook vs sweeper (§3h) | Job backoff | Guard status | Gagal → tetap `PENDING`, sweep berikutnya |
| 8 | Create payment intent | 1 tx: key + `payment_intents` (`REQUIRES_CONFIRMATION`) | RC | 1; `uq_payment_intents_merchant_order` | Order sama dari dua request → `409 DUPLICATE_MERCHANT_ORDER` | Wrapper | Key (merchant) + unique order | Atomik |
| 9 | Confirm payment | 1 tx (§2.2) | RC | 1 → 2c intent `FOR UPDATE` → 3 (payer + merchant) | Confirm ganda dari dua tab; confirm vs expiry (§3e) | Wrapper; `55P03` → 409 | Key (payer) + guard status | Atomik |
| 10 | Sweep intent kedaluwarsa | 1 tx per batch: `UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED)` + delivery `payment.expired` + job | RC | 2c (SKIP LOCKED) | Bentrok dengan confirm → SKIP LOCKED melewati intent yang sedang dikonfirmasi | Batch berikutnya | Guard `status='REQUIRES_CONFIRMATION' AND expires_at <= now()` | Atomik per batch |
| 11 | Refund | 1 tx (§2.3) | RC | 1 → 2c intent `FOR UPDATE` → 3 (merchant + payer) | Dua refund melebihi amount (§3d) | Wrapper | Key (merchant) | Atomik |
| 12 | Create payout (hold) | 1 tx (§2.4) | RC | 1 → 3 (wallet user) | Payout ganda / double spend vs transfer paralel | Wrapper | Key (user); job `dedupe_key = payout.submit:<id>` | Atomik: tidak ada hold tanpa payout, dan sebaliknya |
| 13 | Job `payout.submit` | **Tx-a:** `PENDING → SUBMITTED`. **Panggilan** bank di luar tx. **Tx-b:** hasil + journal + `CompleteInTx` (§2.5) | RC | Tx-a: 2c guard. Tx-b: 2c guard → 3 (hanya jika reversal) | Worker zombie; crash setelah bank menerima | **Tidak** ada retry submit buta; re-delivery → status query | Key ke bank = `payout_id`; fencing | Crash di antara Tx-a dan Tx-b → `SUBMITTED` menggantung → re-delivery memperlakukannya sebagai `UNKNOWN` |
| 14 | Job `payout.status_check` | `GET /v1/payouts/{id}` di luar tx → 1 tx: transisi `UNKNOWN →` hasil + journal + `CompleteInTx`; atau jadwalkan cek berikutnya | RC | 2c guard → 3 (reversal) | Bentrok dengan ops resolve (§3g) | Jadwal 30 dtk → 2 mnt → 10 mnt → 1 jam → 6 jam → `needs_reconciliation` | Guard status + `uq_journals_source` | Atomik; status tetap `UNKNOWN` |
| 15 | Ops resolve payout | 1 tx: guard `UNKNOWN →` hasil + journal + `audit_log` + evidence | RC | 2c guard → 3 (reversal) | Bentrok dengan status_check (§3g) | Tidak (manusia mengulang) | Guard status | Atomik |
| 16 | Recon run + `PROVIDER_SETTLEMENT` | Unduh file **di luar tx** → Tx-1: `recon_runs` (unique run) → baca sisi internal (RR-RO) → Tx per batch 500: `recon_items ON CONFLICT DO NOTHING` + journal settlement untuk item top-up `MATCHED` → Tx akhir: hitungan + `COMPLETED` | RC / RR-RO | Tidak ada lock wallet (hanya akun sistem) | Recon ganda untuk file sama; crash di tengah | Job retry → lanjut (insert idempoten) | Unique run + `uq_recon_items_run_*` + `uq_journals_source (provider_settlement, topup_id, PROVIDER_SETTLEMENT)` (final review R1-5; `ON CONFLICT` → pakai journal yang ada) | Crash → batch yang sudah commit tetap, run `RUNNING`; retry melanjutkan |
| 17 | Ops resolve recon item | 1 tx: guard `recon_items OPEN → RESOLVED` + aksi bisnis (misalnya resolve payout #15) + audit | RC | 2a → 2c → 3 | Dua ops me-resolve item yang sama | Tidak | Guard resolution | Atomik |
| 18 | Job `webhook.deliver` | HTTP ke merchant **di luar tx** (egress guard C-44) → 1 tx: `webhook_delivery_attempts` + update `webhook_deliveries` + `CompleteInTx` atau jadwal ulang | RC | 2c-setara (guard `status='PENDING'`) | Re-delivery setelah lease habis | Backoff 1m, 5m, 30m, 2j, 6j, 24j → `DEAD` | `event_id` stabil (merchant dedupe) | At-least-once; attempt tercatat |
| 19 | Idempotency cleanup | Loop tx kecil: `DELETE … WHERE id IN (SELECT id … WHERE expires_at < now() LIMIT 1000)` | RC | — | — | Lanjut di run berikutnya | — | Parsial aman |
| 20 | Ledger verification | RR-RO per batch akun: bandingkan `balance` dengan Σ entries | RR-RO | **Tanpa lock** | — | — | Read-only | Hanya metrik `ledger_balance_drift_total` |
| 21 | API key create | 1 tx: pilih slot kosong → INSERT `merchant_api_keys` + audit | RC | `uq_merchant_api_keys_active_slot` | Dua create paralel memilih slot sama → satu `23505` → coba slot lain / `409 API_KEY_LIMIT_REACHED` | 1× coba slot lain | Natural (slot) | Atomik |
| 22 | API key revoke | 1 tx: `UPDATE … SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL` + audit | RC | Guard | Revoke ganda → 0 baris = no-op | — | Guard | Atomik |

---

## 2. Operasi kunci secara rinci

### 2.1 Transfer

```sql
BEGIN;                                             -- RC; SET LOCAL lock_timeout='2s', statement_timeout='5s'
-- [1] idempotency
INSERT INTO idempotency_keys (id, client_type, client_id, endpoint, key, request_hash, expires_at)
VALUES ($k, 'USER', $user, 'POST /v1/transfers', $key, $hash, now() + interval '7 days')
ON CONFLICT ON CONSTRAINT uq_idempotency_keys_scope DO NOTHING
RETURNING id;
-- 0 baris → baris sudah ada (dan SUDAH commit, karena INSERT menunggu tx pertama selesai):
--   request_hash beda → ROLLBACK, 422 IDEMPOTENCY_KEY_REUSED
--   sama              → ROLLBACK, replay response_status/body + Idempotent-Replayed: true

-- (tanpa lock) resolve penerima: users.email → accounts wallet; tidak ada → 422 INVALID_RECIPIENT (lompat ke [8])

-- [3] lock wallet berurutan
SELECT id, balance, status FROM accounts
WHERE id = ANY(ARRAY[$sender_acc, $receiver_acc]) ORDER BY id
FOR NO KEY UPDATE;
-- [4] validasi: status ACTIVE, balance >= amount → selain itu 422 (lompat ke [8])

-- [5] ledger.PostJournal
INSERT INTO journals (id, source_type, source_id, purpose, currency, correlation_id, idempotency_key_id)
VALUES ($j, 'transfer', $t, 'TRANSFER', 'IDR', $corr, $k);
INSERT INTO journal_entries (id, journal_id, account_id, direction, amount, currency) VALUES
  ($e1, $j, $sender_acc,   'D', $amt, 'IDR'),
  ($e2, $j, $receiver_acc, 'C', $amt, 'IDR');
UPDATE accounts SET balance = balance - $amt WHERE id = $sender_acc;     -- ck_accounts_balance_non_negative jaring terakhir
UPDATE accounts SET balance = balance + $amt WHERE id = $receiver_acc;
-- [6]
INSERT INTO transfers (id, sender_account_id, receiver_account_id, amount, currency, journal_id, …) VALUES (…);
-- [7] (opsional) INSERT INTO jobs (kind='notify.transfer_completed', …)
-- [8] simpan respons (201 atau 422 domain)
UPDATE idempotency_keys SET response_status = $s, response_body = $b, resource_type='transfer', resource_id=$t
WHERE id = $k;
COMMIT;                                            -- deferred trigger ck_journals_balanced berjalan di sini
```

| Gagal di | State setelahnya | Pemulihan |
|---|---|---|
| [1] `55P03` (key sama sedang diproses > 2 dtk) | Tidak ada | `409 REQUEST_IN_PROGRESS`; client retry dengan key sama |
| [3] `55P03` | Tidak ada (rollback) | `409 RESOURCE_BUSY`; retry dengan key sama aman |
| [3]/[5] `40P01` | Tidak ada | Wrapper mengulang seluruh fungsi (maks. 3×) |
| [5] CHECK saldo (bug validasi) | Tidak ada | `422 INSUFFICIENT_BALANCE` lewat pemetaan constraint; metrik bug |
| COMMIT: trigger keseimbangan | Tidak ada | `500` + alert (bug ledger) |
| Koneksi putus saat COMMIT | Tidak diketahui | `503`; client retry dengan key → replay (sudah commit) atau eksekusi pertama (belum) |

### 2.2 Confirm payment

```sql
BEGIN;
-- [1] key (client = payer)  … sama seperti 2.1
-- [2] objek bisnis
SELECT id, status, amount, merchant_account_id, expires_at FROM payment_intents WHERE id = $pi FOR UPDATE;
--   status <> 'REQUIRES_CONFIRMATION' → SUCCEEDED oleh payer yang sama tanpa key = 409 INVALID_STATE_TRANSITION
--   expires_at <= now()               → 422 PAYMENT_EXPIRED (disimpan untuk replay)
-- [3] SELECT … FROM accounts WHERE id = ANY(ARRAY[$payer_acc, $merchant_acc]) ORDER BY id FOR NO KEY UPDATE;
-- [4] cek saldo payer → 422 INSUFFICIENT_BALANCE
-- [5] PostJournal(PAYMENT: DR payer / CR merchant) + UPDATE balances
UPDATE payment_intents
   SET status='SUCCEEDED', payer_user_id=$u, payer_account_id=$payer_acc, journal_id=$j, confirmed_at=now()
 WHERE id = $pi AND status = 'REQUIRES_CONFIRMATION';      -- guard eksplisit (baris sudah di-lock)
-- [6] INSERT webhook_deliveries (event_id stabil, 'payment.succeeded') + INSERT jobs ('webhook.deliver')
-- [7] simpan respons key
COMMIT;
```

| Gagal di | State | Pemulihan |
|---|---|---|
| [2] menunggu > 2 dtk (tab lain sedang confirm) | Tidak ada | `409 RESOURCE_BUSY`; retry → replay atau `409 INVALID_STATE_TRANSITION` (sudah dibayar tab lain, dengan key lain) |
| Setelah COMMIT, sebelum respons | Intent `SUCCEEDED` | Retry dengan key yang sama → replay 201 |
| [6] (masih di dalam tx) | Rollback total | Webhook tidak pernah terkirim tanpa pembayaran, dan pembayaran tidak pernah tanpa webhook terjadwal |

### 2.3 Refund

```sql
BEGIN;
-- [1] key (client = merchant)
-- [2] SELECT … FROM payment_intents WHERE id = $pi AND merchant_id = $principal FOR UPDATE;   -- scoping BOLA + lock
--     status NOT IN ('SUCCEEDED','PARTIALLY_REFUNDED') → 409 INVALID_STATE_TRANSITION
--     refunded_amount + $amt > amount                  → 422 REFUND_EXCEEDS_PAYMENT (refundable_amount)
-- [3] SELECT … FROM accounts WHERE id = ANY(ARRAY[$merchant_acc, $payer_acc]) ORDER BY id FOR NO KEY UPDATE;
-- [4] saldo merchant >= $amt → selain itu 422 INSUFFICIENT_BALANCE
-- [5] PostJournal(REFUND, source = ('refund', $r)): DR merchant / CR payer + UPDATE balances
INSERT INTO refunds (id, payment_intent_id, merchant_id, amount, currency, journal_id, …) VALUES ($r, …);
UPDATE payment_intents
   SET refunded_amount = refunded_amount + $amt,
       status = CASE WHEN refunded_amount + $amt = amount THEN 'REFUNDED' ELSE 'PARTIALLY_REFUNDED' END
 WHERE id = $pi;                                                      -- ck_payment_intents_refund_bound = jaring terakhir
-- [6] delivery 'payment.refunded' + job; [7] respons key
COMMIT;
```
Lock pada baris intent ([2]) yang membuat pemeriksaan `refunded_amount + amt ≤ amount` aman: refund kedua menunggu, lalu membaca `refunded_amount` yang sudah diperbarui.

### 2.4 Create payout (hold)

```sql
BEGIN;
-- [1] key (client = user)
-- [3] SELECT id, balance, status FROM accounts WHERE id = $wallet FOR NO KEY UPDATE;
-- [4] balance >= amount → selain itu 422 INSUFFICIENT_BALANCE (disimpan)
-- [5] PostJournal(PAYOUT_HOLD, source = ('payout', $p)): DR wallet / CR PAYOUT_IN_TRANSIT  (akun sistem: tanpa lock)
UPDATE accounts SET balance = balance - $amt WHERE id = $wallet;
INSERT INTO payouts (id, user_id, account_id, amount, currency, status, bank_code, account_number_enc,
                     account_number_last4, holder_name, hold_journal_id, …)
VALUES ($p, …, 'PENDING', …, $j, …);                          -- FK ke accounts sudah aman: wallet terkunci di [3]
INSERT INTO jobs (kind, payload, dedupe_key, run_at) VALUES ('payout.submit', {"payout_id": $p}, 'payout.submit:'||$p, now());
-- [7] respons 202 ke key
COMMIT;
```
Sejak commit ini, dana sudah tidak bisa dipakai untuk transfer lain (saldo turun). Uang belum keluar, tetapi **niat** sudah tercatat dan pasti dieksekusi oleh worker (outbox).

### 2.5 Job `payout.submit`

```text
claim job (SKIP LOCKED, lease 30 dtk)

Tx-a:
  UPDATE payouts SET status='SUBMITTED', submit_attempts = submit_attempts + 1, submitted_at = now()
   WHERE id = $p AND status = 'PENDING'
  RETURNING …;
  0 baris → baca status:
     SUBMITTED  → re-delivery setelah crash: JANGAN submit. Tandai UNKNOWN + jadwalkan status_check (Tx-b varian UNKNOWN)
     terminal   → CompleteInTx (no-op) dan selesai
  COMMIT                                  ← titik "mungkin sudah terkirim" sekarang bisa dideteksi

(di luar tx) POST bank /v1/payouts   Idempotency-Key: $p   timeout 10 dtk

Tx-b (salah satu):
  sukses & cocok (payout_id, amount, rekening):
     UPDATE payouts SET status='SUCCEEDED', settle_journal_id=$j, resolved_at=now(), resolution_source='BANK_RESPONSE'
      WHERE id=$p AND status='SUBMITTED';
     PostJournal(PAYOUT_SETTLE: DR PAYOUT_IN_TRANSIT / CR BANK_CLEARING)    -- hanya akun sistem → tanpa lock wallet
  penolakan definitif (4xx bisnis):
     UPDATE … status='FAILED' … WHERE id=$p AND status='SUBMITTED';
     SELECT … FROM accounts WHERE id = $wallet FOR NO KEY UPDATE;           -- posisi 3 setelah 2c ✓
     PostJournal(PAYOUT_REVERSAL, reversal_of = hold): DR PAYOUT_IN_TRANSIT / CR wallet; UPDATE balance
  timeout / 5xx / respons tidak cocok (amandemen ADR-L06):
     UPDATE … status='UNKNOWN', last_error=… WHERE id=$p AND status='SUBMITTED';
     INSERT jobs ('payout.status_check', run_at = now() + 30s, dedupe_key = 'payout.status_check:'||$p||':1')
  jobs.CompleteInTx(job)    -- fencing: 0 baris → ROLLBACK seluruh Tx-b
  COMMIT
```

| Gagal di | State | Pemulihan |
|---|---|---|
| Sebelum Tx-a commit | `PENDING` | Lease habis → job diulang dari awal |
| Setelah Tx-a, sebelum panggilan bank | `SUBMITTED` (bank tidak pernah menerima) | Re-delivery melihat `SUBMITTED` → `UNKNOWN` → status query → `NOT_FOUND` + safe window → resubmit dengan key sama (ADR-L06) |
| Bank menerima, respons hilang (timeout) | `SUBMITTED` → `UNKNOWN` | Status query → `SUCCEEDED` → settle (Demo C) |
| Setelah respons sukses, sebelum Tx-b commit (crash) | `SUBMITTED` | Re-delivery → `UNKNOWN` → status query → `SUCCEEDED`. Bank tidak dipanggil submit lagi |
| Tx-b `CompleteInTx` 0 baris (zombie) | Tidak berubah oleh worker ini | Worker pemilik lease yang baru yang menyelesaikan |

### 2.6 Proses inbox event `charge.succeeded`

```text
claim:
  UPDATE webhook_inbox SET locked_by=$w, locked_until=now()+'30s'
   WHERE id IN (SELECT id FROM webhook_inbox WHERE status='RECEIVED' AND next_attempt_at <= now()
                  AND (locked_until IS NULL OR locked_until < now())   -- (final review C1: hormati lease)
                ORDER BY next_attempt_at LIMIT $n FOR UPDATE SKIP LOCKED)
  RETURNING …;
  -- attempts TIDAK dinaikkan saat claim. attempts++ (+ next_attempt_at = now() + backoff) hanya saat
  -- percobaan benar-benar gagal (error/verify gagal), atau saat claim mengambil alih lease yang kedaluwarsa.
  -- Event yang sedang diproses lambat (verify-by-fetch 3–10 dtk) tidak diklaim ulang.

(di luar tx, P1) verify-by-fetch: GET /v1/charges/{resource_ref}
   status bukan SUCCEEDED / amount beda → jadwal ulang atau DEAD + alert (amandemen ADR-L08)

BEGIN;
  [2b] SELECT id FROM webhook_inbox
        WHERE id = $ev AND locked_by = $w AND status = 'RECEIVED' FOR UPDATE; -- fencing; 0 baris → ROLLBACK
       (final review R1-2, menutup O19: status/outcome final ditulis lewat SATU UPDATE di akhir tx [5],
        sehingga CHECK status × outcome tidak pernah melihat keadaan antara; urutan lock tetap 2b → 2c)
  [2c] SELECT … FROM topups WHERE provider = 'mock' AND provider_ref = $ref FOR UPDATE;
       amount/currency ≠ event → inbox DEAD + alert (COMMIT tanpa efek uang)
       status PENDING atau EXPIRED → lanjut kredit  (EXPIRED = status lunak)
       status SUCCEEDED            → inbox IGNORED/DUPLICATE
       status FAILED               → inbox PROCESSED/CONFLICT + INSERT recon_items (source WEBHOOK, STATUS_MISMATCH)
  [3]  SELECT … FROM accounts WHERE id = $user_wallet FOR NO KEY UPDATE;
  [4]  PostJournal(TOPUP_CREDIT, source = ('topup', $t)): DR PROVIDER_CLEARING / CR wallet; UPDATE balance
       UPDATE topups SET status='SUCCEEDED', journal_id=$j, succeeded_at=now()
        WHERE id=$t AND status IN ('PENDING','EXPIRED');
       (jika sebelumnya EXPIRED → metrik webhook_late_total)
  [5]  UPDATE webhook_inbox SET status = $final, outcome = $outcome,
                               processed_at = CASE WHEN $final IN ('PROCESSED','IGNORED') THEN now() END,
                               locked_by = NULL, locked_until = NULL
        WHERE id = $ev;                                                        -- satu UPDATE final
COMMIT;
```

| Gagal di | State | Pemulihan |
|---|---|---|
| Verify-by-fetch gagal (provider down) | Inbox `RECEIVED` | `attempts++`, `next_attempt_at` backoff, lease dilepas; setelah 8 kegagalan nyata → `DEAD` + alert |
| Di dalam tx (error apa pun) | Rollback total | Inbox tetap `RECEIVED`, dicoba lagi |
| Worker lambat melewati lease, worker lain sudah memproses | Fencing 0 baris | Rollback; event diproses tepat sekali oleh pemilik lease baru (§3f) |

---

## 3. Race condition konkret

### (a) Double spend: dua transfer 70.000 dari saldo 100.000

**Naif** (read → check → write tanpa lock):

| t | T1 | T2 |
|---|---|---|
| 1 | `SELECT balance` → 100.000 | |
| 2 | | `SELECT balance` → 100.000 |
| 3 | cek 100.000 ≥ 70.000 ✓ | cek 100.000 ≥ 70.000 ✓ |
| 4 | `UPDATE balance = 30.000` | |
| 5 | COMMIT | `UPDATE balance = 30.000` (*lost update*) atau `balance - 70.000 = -40.000` |
| — | **Hasil:** 140.000 terkirim dari 100.000 | |

**Dijaga** (`FOR NO KEY UPDATE` + CHECK):

| t | T1 | T2 |
|---|---|---|
| 1 | `SELECT … FOR NO KEY UPDATE` → 100.000 (lock) | |
| 2 | | `SELECT … FOR NO KEY UPDATE` → **menunggu** |
| 3 | cek ✓, journal, `balance = 30.000` | (menunggu) |
| 4 | COMMIT (lock dilepas) | membaca versi terbaru → **30.000** |
| 5 | | cek 30.000 ≥ 70.000 ✗ → `422 INSUFFICIENT_BALANCE` (disimpan untuk replay) |
| — | **Hasil:** satu transfer, saldo 30.000, satu journal. Kalau validasi aplikasi dihapus oleh bug, `ck_accounts_balance_non_negative` menolak UPDATE di T2 | |

### (b) Deadlock A→B dan B→A

**Tanpa urutan** (lock "pengirim dulu"):

| t | T1 (A→B) | T2 (B→A) |
|---|---|---|
| 1 | lock A | |
| 2 | | lock B |
| 3 | lock B → menunggu T2 | |
| 4 | | lock A → menunggu T1 → **siklus** → Postgres membatalkan salah satu (`40P01`) setelah `deadlock_timeout` 1 dtk |

**Dengan `ORDER BY id`** (misal `id(A) < id(B)`):

| t | T1 (A→B) | T2 (B→A) |
|---|---|---|
| 1 | lock A | |
| 2 | | lock **A** dulu (bukan B) → menunggu |
| 3 | lock B ✓, journal, COMMIT | (menunggu) |
| 4 | | lock A ✓, lock B ✓ → lanjut |
| — | Tidak ada siklus; T2 hanya menunggu beberapa ms | |

### (c) Key idempotency duplikat yang datang bersamaan

| t | R1 (key K) | R2 (key K, payload sama) |
|---|---|---|
| 1 | `INSERT idempotency_keys … K` → baris baru (belum commit) | |
| 2 | | `INSERT … K` → **menunggu** pada unique index (tx R1 belum selesai) |
| 3 | lock wallet, journal, simpan respons 201, COMMIT | (menunggu) |
| 4 | | INSERT berakhir dengan konflik → `DO NOTHING` → 0 baris |
| 5 | | `SELECT` baris K → hash sama, `response_status = 201` → **replay** |
| — | Satu journal. Kalau R1 rollback di t3, INSERT R2 justru berhasil dan R2 menjadi eksekusi pertama. Kalau R2 menunggu > 2 dtk → `409 REQUEST_IN_PROGRESS` | |

### (d) Dua refund yang bersama-sama melebihi amount (intent 100.000)

| t | R1 refund 60.000 | R2 refund 70.000 |
|---|---|---|
| 1 | `SELECT intent FOR UPDATE` → refunded 0 | |
| 2 | | `SELECT intent FOR UPDATE` → menunggu |
| 3 | 0 + 60.000 ≤ 100.000 ✓ → journal, `refunded_amount = 60.000`, `PARTIALLY_REFUNDED`, COMMIT | |
| 4 | | membaca refunded 60.000 → 60.000 + 70.000 > 100.000 → `422 REFUND_EXCEEDS_PAYMENT` (`refundable_amount: "4000000"`) |
| — | Naif (tanpa lock): keduanya membaca 0 → total refund 130.000. `ck_payment_intents_refund_bound` tetap menolak salah satu UPDATE, tetapi sebagai error tak terduga, bukan respons yang benar | |

### (e) Confirm payment vs sweeper kedaluwarsa (intent `expires_at = 12:30:00`)

| t | Confirm (12:29:59.9) | Sweeper (12:30:00.2) |
|---|---|---|
| 1 | `SELECT intent FOR UPDATE`; status `REQUIRES_CONFIRMATION`, `expires_at > now()` (now = awal tx = 12:29:59.9) ✓ | |
| 2 | | `UPDATE … WHERE id IN (SELECT … WHERE status='REQUIRES_CONFIRMATION' AND expires_at <= now() FOR UPDATE SKIP LOCKED)` → **melewati** intent ini (terkunci) |
| 3 | journal, `SUCCEEDED`, COMMIT | |
| 4 | | Batch berikutnya: intent sudah `SUCCEEDED` → tidak cocok dengan predikat |

Kalau urutannya terbalik (sweeper mengunci lebih dulu): confirm menunggu, lalu membaca `EXPIRED` → `422 PAYMENT_EXPIRED`. Batas waktunya ditentukan oleh `now()` = waktu mulai transaksi, sehingga deterministik dan bisa dijelaskan.

### (f) Inbox diproses dua kali setelah lease habis

| t | W1 | W2 |
|---|---|---|
| 1 | claim event E (lease s/d t+30s) | |
| 2 | verify-by-fetch lambat / GC pause 40 dtk | |
| 3 | | claim E (lease W1 kedaluwarsa) → `locked_by = W2` |
| 4 | | tx: fencing OK → kredit, `SUCCEEDED`, inbox `PROCESSED`, COMMIT |
| 5 | tx: `UPDATE webhook_inbox … WHERE locked_by = 'W1' AND status='RECEIVED'` → **0 baris** → ROLLBACK | |
| — | Satu journal. Tiga lapis: fencing (t5), guard `topups.status IN ('PENDING','EXPIRED')`, dan `uq_journals_source ('topup', t, 'TOPUP_CREDIT')` | |

### (g) `payout.status_check` vs ops manual resolve

| t | Worker status_check | Ops (Sari) |
|---|---|---|
| 1 | bank: `SUCCEEDED` (di luar tx) | Melihat bukti di portal bank, klik "Mark succeeded" |
| 2 | `UPDATE payouts SET status='SUCCEEDED' … WHERE id=$p AND status='UNKNOWN'` → row lock | |
| 3 | | `UPDATE … WHERE id=$p AND status='UNKNOWN'` → menunggu |
| 4 | journal settle, COMMIT | |
| 5 | | Postgres mengevaluasi ulang predikat pada versi baru (status `SUCCEEDED`) → **0 baris** → `409 INVALID_STATE_TRANSITION` ("sudah diselesaikan oleh STATUS_QUERY") |
| — | Satu journal settle. **Aturan penting:** guard UPDATE dijalankan **sebelum** INSERT journal, supaya pihak yang kalah tidak pernah sempat memposting. `uq_journals_source ('payout', p, 'PAYOUT_SETTLE')` adalah lapis kedua | |

### (h) Webhook terlambat vs sweeper top-up

| t | Inbox worker (`charge.succeeded`) | Sweeper |
|---|---|---|
| 1 | | Cek provider: `PENDING` (webhook belum dikirim) |
| 2 | | `UPDATE topups SET status='EXPIRED' WHERE id=$t AND status='PENDING'`, COMMIT |
| 3 | Provider akhirnya sukses; event diproses: `topups` berstatus `EXPIRED` → status lunak → kredit, `SUCCEEDED`, `webhook_late_total++` | |
| — | **Urutan mana pun berakhir sama:** `SUCCEEDED` + satu journal. Kalau inbox lebih dulu, guard sweeper (`status='PENDING'`) gagal → no-op. Menolak kredit di t3 berarti uang user yang sudah dibayar ke provider hilang | |

---

## 4. Analisis deadlock

### 4.1 Transaksi dengan lebih dari satu lock

| Operasi | Lock yang diambil (urutan aktual) | Sesuai urutan global? |
|---|---|---|
| Transfer | key → wallet A, B (`ORDER BY id`) | ✅ 1 → 3 |
| Confirm payment | key → intent → payer, merchant (`ORDER BY id`) | ✅ 1 → 2c → 3 |
| Refund | key → intent → merchant, payer (`ORDER BY id`) | ✅ 1 → 2c → 3 |
| Create payout | key → wallet | ✅ 1 → 3 |
| `payout.submit` Tx-b (FAILED) | payout (guard) → wallet | ✅ 2c → 3 |
| `payout.status_check` / ops resolve (FAILED) | payout → wallet | ✅ 2c → 3 |
| Proses inbox | inbox (fencing) → topup → wallet | ✅ 2b → 2c → 3 |
| Ops resolve recon item | recon_item → payout/topup → wallet | ✅ 2a → 2c → 3 |
| Sweep intent | intent (SKIP LOCKED) saja | ✅ |

Tidak ada transaksi yang mengunci wallet lalu objek bisnis, atau objek bisnis lalu inbox/recon. Karena itu tidak ada siklus di antara jenis transaksi yang berbeda.

### 4.2 Di mana deadlock masih bisa muncul: lock implisit dari FK

Setiap INSERT baris anak mengambil `FOR KEY SHARE` pada baris induk (pemeriksaan FK). Mode ini **bentrok dengan `FOR UPDATE`**, tetapi **tidak bentrok dengan `FOR NO KEY UPDATE`**.

**Contoh kalau kita memakai `FOR UPDATE`:**

| t | T1: create top-up Tx1 untuk user U | T2: transfer U → V |
|---|---|---|
| 1 | | `SELECT accounts U, V … FOR UPDATE` |
| 2 | `INSERT topups (account_id = U)` → FK check butuh `KEY SHARE` pada U → **menunggu T2** | |

Ini tidak deadlock, tetapi menimbulkan antrean yang tidak perlu: membuat top-up tidak seharusnya menunggu transfer. Lock FK yang tidak terlihat di kode juga menjadi sumber deadlock yang sulit ditemukan. Contohnya: T1 memegang `FOR UPDATE` pada wallet W lalu meminta lock X, sementara T2 sudah memegang X dan meng-INSERT baris anak yang mereferensikan W (butuh `KEY SHARE` pada W). Urutan global (§0, aturan FK) mencegah pola ini. `FOR NO KEY UPDATE` menghapus seluruh kelas masalah ini sebagai lapis kedua.

**Keputusan: wallet dikunci dengan `FOR NO KEY UPDATE`, bukan `FOR UPDATE`.**
- Kita tidak pernah mengubah kolom kunci `accounts` (`id`, `currency` immutable; `uq (id, currency)` dipakai composite FK). Mode `NO KEY UPDATE` sudah cukup untuk menyerialisasi penulis saldo, karena dua `FOR NO KEY UPDATE` saling memblok, dan `UPDATE accounts SET balance` sendiri mengambil mode yang sama.
- FK check dari tabel anak (`KEY SHARE`) tidak lagi diblok oleh pemegang lock saldo, sehingga sumber deadlock implisit dari FK hilang.
- Ini menyimpang secara sadar dari pseudo-SQL ADR-L03 (`FOR UPDATE`); semantik serialisasinya identik. ADR-L03 perlu catatan amandemen.
- `payment_intents` dan `payouts` tetap memakai `FOR UPDATE`/guard UPDATE, karena tabel anaknya (`refunds`) diinsert oleh transaksi yang sudah memegang lock intent itu sendiri.

### 4.3 Deteksi dan respons
- Postgres mendeteksi siklus setelah `deadlock_timeout` (default 1 dtk) dan membatalkan satu transaksi dengan `40P01`.
- Wrapper `WithTx` mengulang seluruh fungsi transaksi maksimal 3× (backoff 10 ms · 2ⁿ ± 50%). Idempotency membuat pengulangan aman, karena tidak ada yang tersimpan dari percobaan yang dibatalkan.
- Metrik `db_deadlock_total{operation}` harus **selalu 0**. Nilai > 0 adalah bug urutan lock: alert, lalu investigasi lewat `log_lock_waits = on` di Postgres.

---

## 5. Hot row

| Baris | Kontensi | Penanganan |
|---|---|---|
| **Wallet user** | Serialisasi per user (Demo B) | Memang yang diinginkan secara bisnis. Antre ms per transaksi, adil (FIFO lock queue) |
| **Wallet merchant** | Semua pembayaran ke satu merchant mengunci baris yang sama (O11) | V1: diterima dan diukur. Rinciannya di bawah |
| **Akun sistem** (`PROVIDER_CLEARING`, `PAYOUT_IN_TRANSIT`, `BANK_CLEARING`) | Hampir semua transaksi menyentuhnya | **Tanpa lock**: `balance` NULL, hanya INSERT entries (ADR-L07) |
| **Tabel `jobs` / `webhook_inbox`** | Banyak worker meng-claim | `FOR UPDATE SKIP LOCKED` + partial index kecil: worker tidak saling menunggu |

**Wallet merchant: hitungan O11.** Durasi lock = waktu dari `SELECT … FOR NO KEY UPDATE` sampai COMMIT. Tanpa network di dalam tx: INSERT journal + 2 entries + 2 UPDATE + INSERT delivery/job + respons key ≈ 2–5 ms di VM kecil. Batas serial untuk satu merchant ≈ 1000/5 = 200 hingga 1000/2 = 500 pembayaran/detik. Antrean mulai terasa jauh sebelum batas itu: p99 naik saat utilisasi lock > ~70%. Load test (desain DevOps) memakai skenario "1 merchant, N payer paralel" dan melaporkan `lock wait time` dari `pg_stat_activity` / `pg_locks`.

**Jalur V2 (didokumentasikan, tidak dibangun):**
- Pembayaran mengkredit akun `MERCHANT_PENDING:<merchant>` yang **tidak materialized** (seperti akun sistem, `balance` NULL). Akibatnya pembayaran sama sekali tidak mengunci baris merchant; cukup INSERT entries.
- Job `merchant.settle` berkala (misalnya tiap 1 menit) per merchant menghitung Σ entries pending sejak sweep terakhir, lalu memposting journal `MERCHANT_SETTLEMENT` (purpose baru): DR pending / CR wallet merchant. Ini satu lock wallet per menit, bukan per pembayaran.
- Sharding (`:<shard n>`) **tidak diperlukan**, karena akun non-materialized tidak di-lock. Sharding hanya relevan kalau pending harus materialized.
- **Trade-off:**
  - Saldo merchant yang bisa dipakai menjadi *eventually* tersedia (± interval sweep). Ini wajar bagi merchant (mirip konsep settlement).
  - Refund hanya mendebit wallet yang sudah di-settle. Refund untuk pembayaran yang belum di-sweep bisa gagal `INSUFFICIENT_BALANCE`. Mitigasinya: sweep on-demand sebelum refund, atau refund boleh mendebit pending dengan pemeriksaan Σ pending di bawah advisory lock per merchant.
  - Invariant LI-02 untuk akun pending dijaga oleh konstruksi (pending hanya dikredit oleh pembayaran dan didebit oleh sweep sebesar Σ yang terlihat).

---

## 6. Timeout dan connection pool

| Parameter | Nilai | Di mana | Alasan |
|---|---|---|---|
| `lock_timeout` | 2 dtk (API), 5 dtk (worker) | `SET LOCAL` di `WithTx` | Request user tidak boleh menunggu lock lama; worker lebih sabar |
| `statement_timeout` | 5 dtk (API); worker per kind: `payout.*` 5 dtk, `webhook.deliver` 5 dtk, recon batch 30 dtk, ledger verification 60 dtk (RR-RO), cleanup 30 dtk | `SET LOCAL` | Query liar tidak menahan koneksi |
| `idle_in_transaction_session_timeout` | 10 dtk | Role `ledgerpay_app` | Bug yang lupa COMMIT tidak menahan lock selamanya |
| `deadlock_timeout` | 1 dtk (default) | Server | — |
| HTTP client ke provider | Connect 2 dtk, total 10 dtk | Worker | Di luar tx, jadi tidak menahan lock |

**Ukuran pool** (VM 2 vCPU, Postgres di VM yang sama, `max_connections = 100`):

| Pool | Ukuran | Alasan |
|---|---|---|
| `api` | 20 | Kerja CPU aktif ≈ 2× core. Sisanya headroom untuk koneksi yang sedang menunggu lock (≤ 2 dtk) dan latensi I/O |
| `worker` | 10 | Konkurensi worker per kind dibatasi agar total ≤ 8 (2 koneksi sisa untuk claim dan heartbeat) |
| `mock-provider` (DB terpisah) | 5 | Beban kecil |
| Migrasi / admin | 1–2 | — |

Total ±37 dari 100, dengan sisa untuk `psql`, monitoring, dan instance kedua saat rolling deploy.

**Pool habis:** akuisisi koneksi memakai context dengan batas 2 dtk. Kalau habis → `503 SERVICE_UNAVAILABLE` (aman di-retry dengan key yang sama) + metrik `db_pool_acquire_timeout_total` + `db_pool_in_use`. Request **tidak** mengantre tanpa batas di aplikasi, karena antrean tanpa batas hanya memindahkan masalah ke timeout klien.

---

## 7. Bagaimana setiap jaminan dibuktikan

### 7.1 Teknik

| Teknik | Cara | Dipakai untuk |
|---|---|---|
| **Barrier goroutine** | N goroutine menunggu satu `chan struct{}` ditutup, lalu memanggil HTTP/service bersamaan (testcontainers Postgres sungguhan) | Contention massal (Demo B), key duplikat |
| **Test hook titik eksekusi** | `testhook.Pause(ctx, "transfer.after_lock")` (no-op di build produksi, aktif dengan build tag `testhooks`). Test memegang channel per titik, sehingga bisa mengatur urutan T1/T2 persis seperti tabel §3 | Interleaving deterministik (a), (d), (e), (g) |
| **Hook berbasis advisory lock** | Untuk worker di proses terpisah: hook menjalankan `SELECT pg_advisory_lock(hashtext('hook:payout.after_submit'))`; test memegang lock itu dari koneksinya sendiri lalu melepasnya pada saat yang dipilih | Interleaving lintas proses: crash setelah submit, lease habis (f) |
| **Kill proses** | `docker kill` / `cmd.Process.Kill()` pada worker di titik hook | Crash-recovery (FS-04, FS-08, Demo C) |
| **Fault injection mock-provider** | Skenario `timeout_after_process`, `error_5xx`, `webhook_duplicate:n`, `webhook_out_of_order` | Unknown outcome, webhook |
| **Pembanding naif** | Implementasi read-check-write tanpa lock **hanya di test code** | Membuktikan test benar-benar menangkap race |
| **Invariant checker** | Setelah setiap test: `Σ debit = Σ kredit` global, `balance = Σ entries` per wallet, tidak ada saldo negatif, `count(journal) per source` sesuai | Semua test concurrency |

### 7.2 Daftar test

| Test | Membuktikan | Teknik |
|---|---|---|
| `TestTransfer_ConcurrentDoubleSpend_100x` | (a), LI-02: tepat 10 dari 100 transfer 10.000 berhasil atas saldo 100.000 | Barrier |
| `TestTransfer_NaiveImplementation_Overdraws` | Test di atas sensitif terhadap race | Barrier + pembanding naif |
| `TestTransfer_OppositeDirections_NoDeadlock` | (b): 50 pasang A→B/B→A, `db_deadlock_total = 0` | Barrier |
| `TestIdempotency_ConcurrentSameKey_OneEffect` | (c), LI-05 | Hook `after_idem_insert` |
| `TestIdempotency_SameKeyDifferentPayload_422` | LI-05 | Sekuensial |
| `TestRefund_ConcurrentExceedingAmount` | (d), LI-08 | Hook `refund.after_lock` |
| `TestConfirm_VsExpirySweeper` | (e), dua urutan | Hook |
| `TestInbox_LeaseExpired_ProcessedOnce` | (f), LI-09 | Advisory-lock hook + lease pendek (1 dtk) |
| `TestPayoutStatusCheck_VsOpsResolve` | (g), satu journal settle | Hook |
| `TestTopup_LateWebhookAfterExpiry_Credited` | (h) | Fault injection `webhook_delay` |
| `TestPayoutSubmit_CrashAfterBankAccepted_NoDoubleSend` | §2.5, LI-10; mock bank mencatat tepat 1 transfer | Advisory-lock hook + kill |
| `TestPayoutSubmit_CrashBeforeBankCall_ResubmitAfterSafeWindow` | Jalur `NOT_FOUND` | Hook + kill + clock fake di mock |
| `TestAccountsLock_TopupInsertNotBlockedByTransfer` | §4.2 (`FOR NO KEY UPDATE`) | Hook: transfer memegang lock, INSERT top-up harus selesai < 100 ms |
| `TestJournal_UnbalancedRejectedAtCommit` | Deferred trigger LI-01 | Insert langsung dengan role app |
| `TestPool_Exhaustion_Returns503` | §6 | Pool kecil + hook yang menahan koneksi |
