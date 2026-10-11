# LedgerPay: Failure Scenarios

> Referensi: glosarium & ID invariant di `00-index.md`; ADR-L02 (idempotency), ADR-L03 (locking), ADR-L05 (mock provider), ADR-L06 (payout), ADR-L08 (inbox), ADR-006 (job runner), ADR-011 (transaksi).
> Format setiap skenario: **Request → Failure → Detection → Handling → Recovery → Final State**.
> Skenario dipicu secara deterministik lewat mock provider (`PUT /admin/scenario`, header `X-Mock-Scenario`, atau *magic values*) dan *fault injection* di test harness (kill proses, putus koneksi DB).

---

## FS-01 — Duplicate Request

**Setup:** Rina saldo Rp100.000. Rina transfer Rp50.000 ke Andi dari aplikasi mobile dengan `Idempotency-Key: K1`. Sinyal jelek: aplikasi tidak menerima response dan retry otomatis. Rina juga menekan tombol lagi.

| Tahap | Detail |
|---|---|
| **Request** | `POST /v1/transfers {to: andi, amount: "5000000"}` + `Idempotency-Key: K1` (amount dalam minor unit, ADR-L04) |
| **Failure** | 1. Request #1 diproses dan commit. 2. Response hilang di jaringan. 3. Client mengirim request #2 (key + body identik). 4. User mengirim request #3 dengan key K1 tetapi amount Rp60.000 (bug client) |
| **Detection** | Request #2: `INSERT … ON CONFLICT (client_type, client_id, endpoint, key) DO NOTHING` mengembalikan 0 row → SELECT key yang ada → `request_hash` sama dan response tersimpan. Request #3: `request_hash` berbeda |
| **Handling** | #2 → ROLLBACK (tidak ada efek), lalu replay `201` + body identik (transfer_id T1) + header `Idempotent-Replayed: true`. #3 → `422 IDEMPOTENCY_KEY_REUSED`. Metrik `idempotency_replay_total` naik |
| **Recovery** | Tidak perlu recovery. Client memakai response replay. Untuk transfer baru, client wajib membuat key baru |
| **Final State** | Saldo Rina Rp50.000, saldo Andi +Rp50.000. **1** row `Transfer`, **1** `Journal` (2 entry), **1** `IdempotencyRecord` |

**Invariant yang dijaga:** LI-05, LI-06, LI-01.
**Test:** integration `TestTransfer_IdempotentReplay` (replay, payload berbeda, key per client) + `TestTransfer_RejectedResultIsReplayed` (422 `INSUFFICIENT_BALANCE` di-replay, bukan diproses ulang setelah saldo bertambah).
**Demo:** Demo A.

---

## FS-02 — Concurrent Payment

**Setup A (saldo sama):** Rina saldo Rp100.000. Pada milidetik yang sama: (A) transfer Rp70.000 ke Andi, (B) bayar merchant Budi Rp70.000. Key berbeda, jadi idempotency tidak relevan; yang diuji adalah lost update.
**Setup B (refund):** Payment P1 Rp100.000 berhasil. Budi mengirim dua refund paralel: Rp60.000 (R1) dan Rp70.000 (R2).

| Tahap | Detail |
|---|---|
| **Request** | A: `POST /v1/transfers`. B: `POST /v1/payment-intents/{id}/confirm`. Setup B: dua `POST /v1/payments/P1/refunds` |
| **Failure** (tanpa locking, sebagai pembanding) | Keduanya membaca saldo 100.000, keduanya lolos validasi, dan keduanya menulis. Saldo akhirnya -40.000 atau lost update 30.000 |

Timeline dengan desain final (ADR-L03):

| t | Tx A (transfer Rina→Andi) | Tx B (payment Rina→Budi) |
|---|---|---|
| 1 | `BEGIN`; insert idempotency K_A | `BEGIN`; insert idempotency K_B |
| 2 | `SELECT … WHERE id = ANY({rina, andi}) ORDER BY id FOR UPDATE` → dapat lock | `SELECT … WHERE id = ANY({rina, budi}) ORDER BY id FOR UPDATE` → **menunggu** lock Rina |
| 3 | Saldo 100.000 ≥ 70.000 → journal, saldo Rina 30.000 | (menunggu) |
| 4 | `COMMIT` → 201 | Dapat lock, membaca saldo **30.000** (Read Committed membaca versi terbaru setelah lock) |
| 5 | | 30.000 < 70.000 → hanya simpan key + response 422 → `COMMIT` → `422 INSUFFICIENT_BALANCE` |

| Tahap | Detail |
|---|---|
| **Detection** | Validasi saldo dilakukan **setelah** lock didapat. Pertahanan terakhir: `CHECK (balance >= 0)` (SQLSTATE `23514`) kalau ada jalur yang lolos validasi. Deadlock (`40P01`) seharusnya tidak terjadi karena urutan lock (`ORDER BY id`); kalau terjadi, metrik `db_deadlock_total` naik |
| **Handling** | Request yang kalah → `422 INSUFFICIENT_BALANCE` (disimpan untuk replay). Menunggu lebih lama dari `lock_timeout` → `409 RESOURCE_BUSY` (aman di-retry dengan key yang sama). Setup B: refund mengunci baris `PaymentIntent` (`FOR UPDATE`), lalu cek `refunded_amount + amount ≤ amount`. R1 lolos (60.000), R2 ditolak `422 REFUND_EXCEEDS_PAYMENT` (60.000 + 70.000 > 100.000) |
| **Recovery** | Tidak perlu. `40P01` di-retry otomatis seluruh transaksi (maksimal 3×, ADR-011) |
| **Final State** | Setup A: saldo Rina **Rp30.000** (tidak pernah negatif); **1** journal (transfer); payment intent tetap `REQUIRES_CONFIRMATION`. Setup B: **1** refund (Rp60.000), intent `PARTIALLY_REFUNDED`, `refunded_amount` = 60.000, **2** journal total (payment + 1 refund) |

**Invariant yang dijaga:** LI-02, LI-03, LI-08, LI-01.
**Test:** concurrency `TestTransfer_100ParallelFromSameWallet` (100 goroutine × Rp10.000 dari saldo Rp100.000 → tepat 10 sukses, saldo 0, Σ ledger seimbang); `TestTransfer_OppositeDirectionNoDeadlock` (A→B dan B→A × 500); `TestRefund_ParallelExceedingAmount`; pembanding implementasi naif (hanya di test code) yang **harus** gagal.
**Demo:** Demo B.

---

## FS-03 — Database Failure

**Setup:** Rina transfer Rp50.000 ke Andi (`K2`). Ada dua varian kegagalan DB.

### Varian 3a — koneksi putus **sebelum** COMMIT

| Tahap | Detail |
|---|---|
| **Request** | `POST /v1/transfers` + `K2` |
| **Failure** | 1. `BEGIN`, insert key, lock, journal ditulis. 2. Postgres restart / koneksi TCP putus **sebelum** `COMMIT` dikirim |
| **Detection** | pgx mengembalikan error koneksi. Postgres membatalkan transaksi yang tidak di-commit secara otomatis |
| **Handling** | API → `503 SERVICE_UNAVAILABLE` (RFC 9457, `request_id`). Tidak ada yang disimpan, termasuk key idempotency, karena seluruh tx rollback |
| **Recovery** | Client retry dengan `K2` yang sama → diproses sebagai request baru (key belum pernah ada) → 201 |
| **Final State** | Tepat **1** transfer, **1** journal, saldo Rina Rp50.000. Tidak ada efek parsial dari percobaan pertama |

### Varian 3b — COMMIT terkirim, tetapi hasilnya tidak diketahui API (kasus halus)

| Tahap | Detail |
|---|---|
| **Failure** | 1. API mengirim `COMMIT`. 2. Postgres berhasil commit (WAL di-flush). 3. Koneksi putus sebelum acknowledgement sampai ke API. API **tidak tahu** apakah commit berhasil |
| **Detection** | pgx mengembalikan error pada `Commit()`. API tidak boleh berasumsi "gagal" |
| **Handling** | API → `503`. API **tidak** me-retry transaksi sendiri, karena retry otomatis hanya untuk `40001`/`40P01`, bukan error koneksi saat commit (ADR-011) |
| **Recovery** | Client retry dengan `K2` → `ON CONFLICT` menemukan key yang **sudah ter-commit** → replay `201` dengan transfer_id yang sama. Kalau ternyata commit gagal, key tidak ada → diproses normal. **Kedua kemungkinan menghasilkan satu efek** |
| **Final State** | Tepat **1** transfer, **1** journal, apa pun hasil commit sebenarnya |

### Varian 3c — DB down beberapa menit
- `GET /readyz` gagal (cek `SELECT 1` dengan timeout 1 detik) → load balancer/Caddy health check berhenti mengirim traffic; `/healthz` (liveness) tetap OK agar proses tidak di-restart berulang tanpa alasan.
- Worker: klaim job gagal → backoff loop, tidak ada job yang hilang (job ada di DB yang sama).
- Alert: `up{job="postgres"} == 0`, lonjakan `http_requests_total{status="503"}`.
- Setelah DB pulih, readiness hijau dan traffic kembali. Job dengan lease kedaluwarsa diambil ulang (ADR-006).

**Invariant yang dijaga:** LI-05, LI-06, LI-01, LI-03.
**Test:** chaos `TestTransfer_ConnectionDropBeforeCommit` dan `TestTransfer_CommitOutcomeUnknown` (memutus koneksi tepat setelah `COMMIT` lewat test hook; Toxiproxy **opsional, dihapus di final review (S11)**) → retry client dengan key sama → 1 efek.

---

## FS-04 — Server (API) Crash

**Setup:** Budi (merchant) membuat payment intent, lalu Rina mengonfirmasi pembayaran Rp75.000 (`K3`).

| Tahap | Detail |
|---|---|
| **Request** | `POST /v1/payment-intents/PI1/confirm` + `K3` |
| **Failure** | Timeline: 1. Tx: key, lock wallet Rina + Budi, journal, intent → `SUCCEEDED`, outbox `payment.succeeded`, simpan response. 2. `COMMIT` sukses. 3. **Proses API di-kill (OOM / `docker kill`) sebelum response ditulis ke socket** |
| **Detection** | Client: koneksi terputus / 502 dari Caddy. Server: container restart (Docker restart policy); log `request_id` berhenti di "committed" tanpa "response_sent" |
| **Handling** | Tidak ada yang perlu dibatalkan di server. Transaksi sudah atomik |
| **Recovery** | Client retry dengan `K3` → replay `200` (intent `SUCCEEDED`). Event outbox sudah ter-commit, jadi webhook ke Budi tetap dikirim oleh worker. Crash di **tengah** tx (sebelum commit) → Postgres rollback → retry diproses normal |
| **Final State** | Saldo Rina −Rp75.000, Budi +Rp75.000. **1** journal, intent `SUCCEEDED`, **1** event `payment.succeeded` di outbox, tepat satu webhook *logis* (bisa terkirim > 1× secara fisik, tetapi dengan event_id yang sama) |

**Invariant yang dijaga:** LI-05, LI-06, LI-13, LI-14.
**Test:** chaos `TestConfirm_CrashAfterCommit`: hook test yang mem-panic setelah `Commit()` sebelum menulis response (build tag `chaos`), lalu retry → replay + 1 journal.

---

## FS-05 — Payment Provider Timeout

**Setup (utama — payout):** Rina saldo Rp500.000, payout Rp200.000 ke rekening BCA. Mock bank diset `timeout_after_process` (uang terkirim, response ditahan sampai client timeout 10 detik).

| Tahap | Detail |
|---|---|
| **Request** | `POST /v1/payouts {amount: "20000000", bank_account}` + `K4` → tx1: journal hold (DR `WALLET:rina` / CR `PAYOUT_IN_TRANSIT`) + `Payout PENDING` + job `payout.submit` → `202` |
| **Failure** | Worker: tx2 `PENDING → SUBMITTED` (commit) → `POST bank /v1/payouts` (Idempotency-Key = payout_id) → bank **memproses** → response tidak kembali → HTTP client timeout |
| **Detection** | Timeout di HTTP client worker. **Timeout ≠ gagal**: hasilnya tidak diketahui |
| **Handling** | tx3: `SUBMITTED → UNKNOWN` (guard `WHERE status='SUBMITTED'`) + job `payout.status_check` (run_at +30 detik). **Tidak ada resubmit dan tidak ada reversal.** Dana tetap di `PAYOUT_IN_TRANSIT`. UI menampilkan "Sedang diproses". Metrik `payout_unknown_total` naik |
| **Recovery** | Status query `GET bank /v1/payouts/{payout_id}` dengan jadwal 30 dtk → 2 mnt → 10 mnt → 1 jam → 6 jam (ADR-L06). Jawaban `SUCCEEDED` → `UNKNOWN → SUCCEEDED` + journal settle (DR `PAYOUT_IN_TRANSIT` / CR `BANK_CLEARING`). `FAILED` → reversal. `NOT_FOUND` setelah safe window 5 menit → resubmit dengan **key yang sama** (bank tetap idempoten). Semua jadwal habis → `needs_reconciliation = true` → antrean ops + reconciliation (FS di Demo D) |
| **Final State** | Payout `SUCCEEDED`. Saldo Rina Rp300.000. **2** journal (hold + settle). Bank mencatat **1** transfer. `PAYOUT_IN_TRANSIT` kembali netral untuk payout ini |

**Varian top-up (pola recovery point, ADR-L02):** `POST /v1/topups` → tx1 commit `TopUp PENDING` + key (`recovery_point = CREATED`) → panggil `POST provider /v1/charges` (Idempotency-Key = topup_id) → timeout → API mengembalikan `503`. Client retry dengan key yang sama → mulai dari `CREATED` → panggil provider lagi (provider mengembalikan charge yang sama karena idempoten) → tx2 `PROVIDER_CREATED` + `checkout_url` → `201`. Tidak ada uang yang bergerak sebelum webhook `SUCCEEDED`, sehingga timeout di sini hanya menunda, tidak pernah menggandakan.

**Invariant yang dijaga:** LI-10, LI-07, LI-01, LI-04.
**Test:** integration `TestPayout_TimeoutAfterProcess_ResolvesViaStatusQuery` (scenario mock `timeout_after_process`, magic value rekening `…0408`); `TestTopUp_ProviderTimeout_RetryResumesFromRecoveryPoint`.
**Demo:** Demo C (bagian timeout).

---

## FS-06 — Webhook Duplicate

**Setup:** Rina top-up Rp100.000 (TopUp TU1 `PENDING`). Provider mengirim event `charge.succeeded` (event_id `evt_1`) **3 kali**, karena response 200 pertama dianggap timeout di sisi provider (`webhook_duplicate:3`). Dua di antaranya datang hampir bersamaan.

| Tahap | Detail |
|---|---|
| **Request** | 3× `POST /v1/webhooks/provider` dengan body identik + header signature `t=…,v1=…` |
| **Failure** | Tanpa dedupe, saldo bertambah 3× (Rp300.000 dari Rp100.000 yang dibayar) |
| **Detection** | 1. Verifikasi HMAC (constant-time) + timestamp window 5 menit. 2. `INSERT INTO webhook_inbox … ON CONFLICT (provider, provider_event_id) DO NOTHING` → insert #2 dan #3 mengembalikan 0 row (dua insert bersamaan diserialisasi oleh unique index) |
| **Handling** | Ketiganya dibalas `200` (agar provider berhenti retry). Hanya satu `InboxEvent` `RECEIVED`. Worker memproses: tx `UPDATE topups SET status='SUCCEEDED' WHERE id=TU1 AND status='PENDING'` → 1 row → journal (DR `PROVIDER_CLEARING` / CR `WALLET:rina`) → inbox `PROCESSED`, semuanya dalam satu tx |
| **Recovery** | Kalau worker crash setelah commit tetapi sebelum ack job, event diproses ulang → guard status menghasilkan 0 row → `IGNORED` (no-op). Lapis ketiga: `UNIQUE (source_type, source_id, purpose)` pada journal (LI-06) menolak journal kedua untuk TU1 |
| **Final State** | TU1 `SUCCEEDED`. Saldo Rina +Rp100.000 (**1×**). **1** journal. **1** row inbox (`PROCESSED`). 3 HTTP request tercatat di access log |

**Invariant yang dijaga:** LI-09, LI-06, LI-07, LI-01.
**Test:** integration `TestWebhook_DuplicateEvents_CreditOnce` (3 event paralel + 1 event berulang setelah diproses); `TestWebhook_BadSignature_401`; `TestWebhook_StaleTimestamp_400`.

---

## FS-07 — Webhook Datang Terlambat / Tidak Berurutan

**Setup 7a (setelah EXPIRED):** Rina membuat TopUp TU2 Rp50.000 tetapi baru membayar di halaman provider 25 jam kemudian (`webhook_delay`). Sweeper sudah menandai TU2 `EXPIRED` pada jam ke-24, setelah status query ke provider menjawab "belum dibayar".
**Setup 7b (out-of-order):** TU3 sudah `SUCCEEDED`. Provider kemudian mengirim event lama `charge.pending`, lalu event `charge.failed` yang bertentangan (`webhook_out_of_order`).

| Tahap | Detail |
|---|---|
| **Request** | `POST /v1/webhooks/provider` dengan event valid |
| **Failure** | 7a: menolak event karena "sudah expired" berarti uang Rina diterima provider tetapi tidak pernah dikreditkan. 7b: menerapkan event berdasarkan urutan kedatangan akan mengubah `SUCCEEDED` menjadi `PENDING`/`FAILED` |
| **Detection** | Worker membandingkan status saat ini dengan tabel transisi monoton (ADR-L08) |
| **Handling** | 7a: `EXPIRED` adalah status lunak → `UPDATE … WHERE status IN ('PENDING','EXPIRED')` → `SUCCEEDED` + journal; metrik `webhook_late_total` naik. 7b: `charge.pending` atas `SUCCEEDED` → `IGNORED`. `charge.failed` atas `SUCCEEDED` → **konflik antar status final**: tidak dibalik otomatis; inbox `PROCESSED` dengan flag konflik + item investigasi recon + alert `webhook_conflict_total` |
| **Recovery** | 7a: otomatis. 7b: manual. Sari memeriksa status di provider/settlement report (reconciliation harian akan menandai `STATUS_MISMATCH` jika memang berbeda), lalu menyelesaikannya dari ops console. Kalau provider benar-benar membatalkan, koreksinya lewat **journal reversal** ter-audit, bukan edit status/saldo |
| **Final State** | 7a: TU2 `SUCCEEDED`, saldo Rina +Rp50.000, **1** journal. 7b: TU3 tetap `SUCCEEDED`, **1** journal, **1** item recon `OPEN` sampai diselesaikan |

**Invariant yang dijaga:** LI-07, LI-09, LI-04, LI-12.
**Test:** integration `TestWebhook_LateSuccessAfterExpired_Credits`; `TestWebhook_OutOfOrder_NoRegression`; `TestWebhook_FinalStateConflict_FlagsRecon`.

---

## FS-08 — Worker Crash

**Setup:** Payout PO7 Rp200.000 milik Rina. Mock bank normal (`ok`). Worker di-kill dengan `docker kill` tepat setelah bank menerima dan memproses request, sebelum worker sempat menulis hasilnya.

| Tahap | Detail |
|---|---|
| **Request** | (Sudah diterima API) Job `payout.submit` untuk PO7 |
| **Failure** | Timeline: 1. Worker klaim job (lease `locked_until = now()+30s`). 2. tx: `PENDING → SUBMITTED` **commit**. 3. `POST bank` → bank memproses, uang terkirim. 4. **Worker mati** sebelum tx hasil |
| **Detection** | Lease job kedaluwarsa → query klaim (`status='running' AND locked_until < now()`) mengambil job kembali; `attempts` = 2. Metrik `job_lease_expired_total` naik |
| **Handling** | Handler melihat PO7 berstatus `SUBMITTED` tanpa hasil → **diperlakukan sebagai `UNKNOWN`** (ADR-L06): tx `SUBMITTED → UNKNOWN` → status query, **bukan** submit ulang |
| **Recovery** | `GET bank /v1/payouts/PO7` → `SUCCEEDED` → tx `UNKNOWN → SUCCEEDED` + journal settle + `jobs.CompleteInTx`. Kalau worker mati **sebelum** langkah 2 (masih `PENDING`), job yang diambil ulang men-submit normal; kalaupun request pertama ternyata sudah sampai, bank idempoten terhadap key `PO7` |
| **Final State** | PO7 `SUCCEEDED`. Saldo Rina −Rp200.000. **2** journal (hold + settle). Bank mencatat **1** transfer. Job `succeeded`, attempts = 2 |

**Invariant yang dijaga:** LI-10, LI-06, LI-07.
**Test:** chaos `TestPayout_WorkerKilledAfterBankAccepted` (hook kill setelah HTTP call, lease dipendekkan ke 1 detik di test); `TestJobRunner_LeaseExpiry_Reclaims`.
**Demo:** Demo C.

---

## FS-09 — Network Timeout (merchant ↔ LedgerPay)

**Setup 9a (masuk):** Server Budi membuat payment intent untuk order `ORD-881` Rp120.000 (`Idempotency-Key: ord-881-create`). Koneksi ke LedgerPay timeout setelah 5 detik di sisi Budi, padahal LedgerPay sudah commit.
**Setup 9b (keluar):** Endpoint webhook Budi lambat (> 5 detik) selama 3 jam.

| Tahap | Detail |
|---|---|
| **Request** | 9a: `POST /v1/payment-intents {amount, merchant_order_id: "ORD-881"}`. 9b: worker mengirim `payment.succeeded` (event_id `evt_ps_42`) ke URL webhook Budi |
| **Failure** | 9a: Budi tidak tahu intent sudah dibuat. 9b: setiap delivery timeout |
| **Detection** | 9a: retry Budi dengan key yang sama → idempotency record ditemukan. Lapis kedua: `UNIQUE (merchant_id, merchant_order_id)` untuk intent aktif (LI-13), sehingga retry tanpa key yang sama pun tidak membuat intent kedua (→ `409 DUPLICATE_MERCHANT_ORDER` + ID intent yang ada). 9b: HTTP client timeout 5 detik → `WebhookDelivery` attempt dicatat (durasi, error) |
| **Handling** | 9a: replay `201` dengan intent yang sama. 9b: retry dengan backoff 1m, 5m, 30m, 2j, 6j, 24j; event_id dan payload **stabil** antar retry; signature dihitung ulang dengan timestamp baru |
| **Recovery** | 9a: otomatis. 9b: kalau Budi pulih di jam ke-3, delivery berikutnya sukses → `DELIVERED`. Kalau semua attempt habis → `DEAD` + alert `webhook_delivery_dead_total`; Sari bisa *replay* dari ops console; Budi juga bisa polling `GET /v1/payment-intents/{id}`. Budi wajib dedupe berdasarkan `event_id`, karena request yang timeout di sisi kita bisa saja sudah diproses Budi |
| **Final State** | 9a: **1** intent untuk `ORD-881`. 9b: event `evt_ps_42` diterima Budi ≥ 1× secara fisik, diproses **1×** secara logis oleh Budi; tidak ada efek finansial tambahan di LedgerPay (webhook keluar tidak pernah memindahkan uang) |

**Invariant yang dijaga:** LI-05, LI-13, LI-14.
**Test:** integration `TestPaymentIntent_RetryAfterClientTimeout`; `TestMerchantWebhook_RetryBackoffThenDead` (server palsu yang gagal N kali); `TestMerchantWebhook_StableEventID`.

---

## FS-10 — Partial Processing

**Pertanyaan inti:** apakah ada titik di mana uang sudah bergerak tetapi catatannya belum ada (atau sebaliknya)?

**Setup:** tiga titik "di tengah jalan" yang paling berisiko.

| Titik | Keadaan parsial | Kenapa aman | Siapa yang menyelesaikan |
|---|---|---|---|
| **10a** Top-up: tx1 commit (`TopUp PENDING`, `recovery_point = CREATED`), panggilan provider belum terjadi (API crash) | Ada record lokal, belum ada charge di provider | Belum ada uang yang bergerak. Saldo hanya berubah saat webhook `SUCCEEDED` diproses | Retry client dengan key yang sama melanjutkan dari `CREATED` (FS-05 varian). Kalau client tidak pernah retry → sweeper: status query ke provider (`NOT_FOUND`) → `EXPIRED` |
| **10b** Payout: tx1 commit (journal hold + `PENDING` + job), worker belum jalan | Saldo sudah di-hold, bank belum dipanggil | Hold adalah journal yang sah (uang pindah ke `PAYOUT_IN_TRANSIT`, bukan hilang). Job ada di transaksi yang sama, jadi tidak mungkin ada hold tanpa job | Worker mengambil job kapan pun ia hidup. Metrik `outbox_lag_seconds` / umur job tertua memberi alert jika worker mati terlalu lama |
| **10c** Payment confirm: journal + intent `SUCCEEDED` + outbox event ter-commit, webhook ke merchant belum terkirim | Uang sudah pindah, merchant belum tahu | Event dan efek finansial di satu `COMMIT` (transactional outbox). Tidak ada "uang pindah tanpa event" atau "event tanpa uang pindah" | Worker dispatcher mengirim saat berjalan (FS-09b) |

| Tahap | Detail (contoh 10b) |
|---|---|
| **Request** | `POST /v1/payouts` Rp100.000 |
| **Failure** | Worker down 20 menit tepat setelah API commit |
| **Detection** | `job_queue_depth{kind="payout.submit"}` dan `job_oldest_age_seconds` > ambang (misalnya 5 menit) → alert |
| **Handling** | API tetap menerima payout baru (`202`). UI menampilkan "Diproses". Tidak ada yang hilang karena semua tersimpan di Postgres |
| **Recovery** | Worker hidup → klaim job sesuai `run_at` → alur normal (atau FS-08 jika crash di tengah) |
| **Final State** | Setiap payout punya tepat **1** journal hold, lalu **1** settle **atau** **1** reversal. Bank: **1** transfer per payout sukses |

**Kontra-contoh yang dicegah desain:** "panggil bank dulu, lalu tulis journal". Kalau proses mati di antaranya, uang keluar tanpa catatan. Desain final selalu **mencatat niat (intent) secara lokal dulu, lalu memanggil pihak luar** (ADR-011, ADR-L06).

**Invariant yang dijaga:** LI-01, LI-06, LI-14, LI-10.
**Test:** chaos `TestPartial_*`: kill proses di setiap *checkpoint* bernomor dalam alur top-up, payout, dan payment (fault-injection hook per checkpoint), lalu jalankan job verifikasi ledger (`ledger_balance_drift_total == 0`) dan pastikan setiap objek bisnis berakhir di status terminal atau status yang bisa dilanjutkan.

---

## Ringkasan

| Skenario | Detection utama | Recovery | Invariant | Jenis test |
|---|---|---|---|---|
| FS-01 Duplicate request | Unique `(client_type, client_id, endpoint, key)` + `request_hash` | Otomatis (replay) | LI-05, LI-06 | Integration |
| FS-02 Concurrent payment | Row lock terurut + validasi setelah lock + `CHECK` | Otomatis (antre / 422 / retry `40P01`) | LI-02, LI-03, LI-08 | Concurrency |
| FS-03 Database failure | Error koneksi / commit tidak diketahui; `/readyz` | Otomatis (retry client + replay) | LI-05, LI-06 | Chaos (test hook; Toxiproxy opsional, dihapus final review S11) |
| FS-04 API crash | Koneksi client putus; tx atomik | Otomatis (retry client + replay) | LI-05, LI-13 | Chaos (hook) |
| FS-05 Provider timeout | HTTP timeout → `UNKNOWN` | Otomatis (status query), fallback manual (recon/ops) | LI-10, LI-07 | Integration (mock scenario) |
| FS-06 Webhook duplicate | Unique inbox + guard status + unique journal source | Otomatis (no-op) | LI-09, LI-06 | Integration |
| FS-07 Webhook terlambat / tidak berurutan | Tabel transisi monoton | Otomatis (late credit); manual (konflik final) | LI-07, LI-12 | Integration |
| FS-08 Worker crash | Lease kedaluwarsa | Otomatis (status query, bukan resubmit) | LI-10, LI-06 | Chaos (kill) |
| FS-09 Network timeout | Idempotency + unique `merchant_order_id`; delivery attempt log | Otomatis (retry/backoff), manual (replay `DEAD`) | LI-05, LI-13 | Integration |
| FS-10 Partial processing | Outbox atomik; metrik umur job | Otomatis (job dilanjutkan) | LI-01, LI-14 | Chaos (checkpoint kill) |

## Failure yang sengaja di luar cakupan V1

| Failure | Kenapa di luar cakupan | Penanganan di versi berikutnya |
|---|---|---|
| Kehilangan data Postgres / disk corrupt | V1 single instance; yang ada hanya `pg_dump` harian (RPO ≤ 24 jam) | V2: managed Postgres HA + PITR (RPO menit/detik), restore drill berkala |
| Outage satu region / VM hilang | Satu VM | V2/V3: multi-AZ, lalu multi-region active-passive |
| Provider mengirim amount/currency yang tidak cocok | Sudah ditangani minimal: event → `DEAD` + alert (ADR-L08), tanpa efek | Prosedur investigasi formal dengan provider |
| Bank melaporkan sukses lalu membatalkan berhari-hari kemudian (*return*) | Butuh alur return/chargeback | Status `RETURNED` + journal reversal dari settlement report |
| Clock skew besar antar server | Satu VM; timestamp dari Postgres `now()` | NTP + timestamp dari DB, bukan dari aplikasi |
| Bug logika yang membuat journal seimbang tetapi salah akun | Tidak terdeteksi oleh invariant seimbang | Property test + review; reconciliation sebagai jaring terakhir |

## Prinsip yang muncul berulang

1. **Tidak ada network call di dalam DB transaction.** Lock dipegang milidetik, dan kegagalan jaringan tidak pernah bisa membuat setengah transaksi.
2. **Catat niat secara lokal dulu, lalu panggil pihak luar** (`SUBMITTED` sebelum bank, recovery point sebelum provider). Crash di tengah selalu meninggalkan jejak yang bisa dilanjutkan.
3. **Idempotency di kedua sisi:** client → LedgerPay (`Idempotency-Key`) dan LedgerPay → provider (`payout_id` / `topup_id` sebagai key).
4. **Timeout ≠ gagal.** Hasil yang tidak diketahui adalah state tersendiri (`UNKNOWN`) yang diselesaikan dengan bertanya, bukan dengan menebak.
5. **Transisi state monoton dengan guard `WHERE status = $expected`.** Event duplikat atau terlambat menjadi no-op, bukan regresi.
6. **Defense in depth di database:** unique index (key, inbox, journal source), `CHECK (balance >= 0)`, trigger append-only. Bug di aplikasi tidak bisa menembus constraint.
7. **Reconciliation adalah jaring terakhir**, dan konflik antar status final selalu diselesaikan manusia lewat journal ter-audit, bukan otomatis.
