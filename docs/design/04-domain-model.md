# LedgerPay: Domain Model

> Bagian dari desain domain — Domain Design. Nama aggregate, status, dan ID invariant mengikuti **glosarium kanonik** di index.
> Referensi: [feature spec](02-features.md), [arsitektur](03-architecture.md), ADR-L01…L08, ADR-006, ADR-011.

---

## 1. Domain & Subdomain Map

**Domain:** perpindahan uang elektronik dalam *closed-loop wallet*, dengan pertanggungjawaban akuntansi ke pihak eksternal (provider, bank).

| Subdomain | Jenis | Kenapa jenis ini | Modul (desain fitur & arsitektur §2.3) |
|---|---|---|---|
| **Ledger** | **Core** | Sumber kebenaran tunggal semua saldo. Seluruh nilai produk (kepercayaan) bergantung padanya, dan tidak bisa dibeli jadi | `ledger` |
| **Money Movement**: transfer, payment (+refund), top-up, payout | **Core** | Di sinilah invariant diuji oleh dunia nyata (concurrency, retry, provider gagal). Pembeda utama portfolio | `transfer`, `payment`, `topup`, `payout`, `wallet` (read) |
| Merchant | Supporting | Dibutuhkan agar menjadi *payment platform*, tetapi logikanya standar (API key, profil) | `identity` (merchant + API key) |
| Reconciliation | Supporting | Penting secara bisnis, tetapi berupa pencocokan data; tidak mengubah aturan inti | `recon` |
| Webhook (in/out) | Supporting | Mekanisme integrasi; polanya umum (inbox/outbox), tetapi harus spesifik ke domain kita | `webhook` |
| Idempotency | Supporting | Generik secara konsep, tetapi **wajib atomik dengan ledger** sehingga tidak bisa didelegasikan ke komponen luar | `idempotency` |
| Identity / Auth | Generic | Masalah yang sudah terpecahkan; cukup implementasi yang benar dan standar | `identity`, `platform/auth` |
| Audit | Generic | Pola append-only standar | `audit` |
| Notification | Generic | Consumer outbox biasa (P3: email) | via `platform/jobs` |

> **Implikasi alokasi waktu:** kualitas tertinggi (property test, concurrency test, review paling ketat) dialokasikan ke Core. Supporting dan Generic cukup "benar dan sederhana".

---

## 2. Ubiquitous Language

| Istilah | Arti di LedgerPay | **Jangan** dicampur dengan |
|---|---|---|
| **Account** | Wadah saldo di ledger, bertipe ASSET/LIABILITY/REVENUE/SUSPENSE dari sudut pandang perusahaan | *User account* (identitas login). Login = `User`; saldo = `Account` |
| **Wallet** | Account bertipe LIABILITY milik satu user/merchant (`WALLET:<owner>`) | Account sistem |
| **Journal** | Satu catatan akuntansi atomik yang berisi ≥ 2 entry seimbang | *Transaksi bisnis*. Satu transaksi bisnis (payout) bisa punya beberapa journal (hold, settle/reversal) |
| **Entry** | Satu baris debit **atau** kredit pada satu account | Journal |
| **Transaksi bisnis** | Objek domain yang dipahami user: `Transfer`, `TopUp`, `PaymentIntent`, `Refund`, `Payout` | *DB transaction*. Selalu ditulis lengkap: "DB transaction" vs "transaksi bisnis" |
| **Transfer** | Perpindahan P2P antar wallet user, sinkron | Istilah umum "transfer dana" (payout ke bank disebut **Payout**) |
| **Hold** | Journal yang memindahkan dana dari wallet ke `PAYOUT_IN_TRANSIT` sebelum hasil bank diketahui | *Authorization* kartu kredit (tidak ada di scope) |
| **Settlement** | (1) Journal `PAYOUT_SETTLE`: dana in-transit dianggap keluar ke bank; (2) laporan settlement provider untuk recon | Finalisasi status. Konteksnya selalu disebut: "settle payout" vs "settlement report" |
| **Clearing account** | Akun ASSET sistem yang menampung piutang/dana di pihak luar (`PROVIDER_CLEARING`, `BANK_CLEARING`) | Wallet |
| **Reversal** | Journal pembalik yang mereferensikan journal asal (`reversal_of`). Koreksi teknis/akuntansi | **Refund**, yaitu transaksi bisnis baru dari merchant ke user dengan journal-nya sendiri |
| **Refund** | Pengembalian dana (sebagian/penuh) atas `PaymentIntent` yang `SUCCEEDED`, atas inisiatif merchant | Reversal |
| **Unknown outcome** | Status payout `UNKNOWN`: request ke bank mungkin sudah diproses, tetapi hasilnya tidak diketahui | `FAILED`. Timeout **bukan** kegagalan |
| **Idempotency key** | Kunci dari client yang menyatakan "ini niat yang sama" untuk satu endpoint | Provider reference / event ID |
| **Recovery point** | Langkah terakhir yang sudah di-commit untuk operasi multi-langkah (ADR-L02 pola b) | Status transaksi bisnis |
| **Inbox event** | Webhook masuk yang sudah diverifikasi dan disimpan sebelum diproses | Outbox job (arah keluar) |
| **Mismatch** | Perbedaan antara catatan internal dan laporan provider (`ReconItem` selain `MATCHED`) | Bug ledger. Mismatch bisa sah (timing cut-off) |

---

## 3. Entities vs Value Objects

### 3.1 Value Objects (immutable, dibandingkan berdasarkan nilai)

| Value Object | Isi | Aturan yang ditegakkan konstruktor |
|---|---|---|
| `Money` | `amount_minor int64`, `currency` (ISO 4217) | `amount_minor ≥ 0` (arah ditentukan oleh `EntryDirection`, bukan tanda); aritmetika hanya antar currency yang sama (beda currency → error, bukan konversi); overflow dicek pada penjumlahan; **tidak ada konstruktor dari float**; JSON = string minor unit (ADR-L04) |
| `PositiveMoney` | `Money` dengan amount > 0 | Dipakai untuk amount transfer/payment/payout/entry. Nol ditolak |
| `AccountRef` | `account_id` + `kind` (WALLET/SYSTEM) | Akun sistem hanya bisa direferensikan lewat konstanta (`SystemAccount.ProviderClearing`), tidak dari input user |
| `IdempotencyKey` | string 1–255 char, ASCII printable | Ditolak jika kosong/terlalu panjang; tidak di-trim diam-diam |
| `RequestHash` | SHA-256 dari `method + path + canonical JSON` | Selalu dihitung server; tidak pernah dari client |
| `ProviderRef` | `provider` + `external_id` | Unik per provider; dipakai recon & status query |
| `BankAccount` | `bank_code`, `account_number`, `holder_name` | Validasi format; `String()` selalu ter-mask (`****1234`) agar tidak bocor ke log |
| `EntryDirection` | `D` / `C` | Enum tertutup |
| `JournalPurpose` | `TOPUP_CREDIT`, `TRANSFER`, `PAYMENT`, `REFUND`, `PAYOUT_HOLD`, `PAYOUT_SETTLE`, `PAYOUT_REVERSAL`, `PROVIDER_SETTLEMENT`, `ADJUSTMENT_MANUAL` | Enum tertutup; menentukan template DR/CR yang sah (§6) |
| `CorrelationID` | UUID | Diteruskan dari request ke job, journal, dan webhook |

### 3.2 Entities (punya identitas dan siklus hidup)

| Entity | Identitas | Catatan |
|---|---|---|
| `User` | UUIDv7 | Identitas login; memiliki satu wallet IDR |
| `Merchant` | UUIDv7 | Memiliki wallet, API key, webhook secret |
| `ApiKey` | prefix (publik) | Secret disimpan ter-hash; bagian dari aggregate `Merchant` |
| `Account` | UUIDv7 | Aggregate root (§4) |
| `Journal`, `JournalEntry` | UUIDv7 | Entry hanya hidup di dalam journal |
| `Transfer`, `TopUp`, `PaymentIntent`, `Refund`, `Payout` | UUIDv7 | Transaksi bisnis |
| `IdempotencyRecord`, `InboxEvent`, `WebhookDelivery`, `ReconRun`, `ReconItem`, `Job`, `AuditEntry` | UUIDv7 | Entity pendukung |

> UUIDv7 dipilih agar ID time-ordered (ramah B-tree index) dan tidak bisa ditebak secara berurutan seperti BIGSERIAL (mengurangi enumeration, walaupun otorisasi tetap penjaga utama).

---

## 4. Aggregates

Aggregate di sini berarti **batas konsistensi**: hal-hal yang harus benar bersamaan setiap kali di-commit.

| Aggregate (root) | Isi | Invariant dijaga | Cara load / lock | Kenapa aggregate tersendiri |
|---|---|---|---|---|
| `Account` | id, owner, type, currency, `balance` (NULL jika sistem), `is_system`, status | LI-02, LI-03 | Wallet: `LockAccountsOrdered` (`FOR UPDATE ORDER BY id`). Sistem: **tidak pernah di-lock** (ADR-L07) | Unit serialisasi saldo; dikunci per baris |
| `Journal` (+ `JournalEntry`) | source_type, source_id, purpose, entries, `reversal_of`, correlation_id | LI-01, LI-04, LI-06, LI-11, LI-14 | Insert-only; tidak pernah di-load untuk diubah | Unit keseimbangan akuntansi; immutable |
| `Transfer` | sender, receiver, amount, journal_id | LI-05 (via key), LI-06 | Dibuat sekali, status `COMPLETED` | Bukti bisnis P2P |
| `TopUp` | user, amount, provider_ref, recovery_point, status | LI-07, LI-09 | `UPDATE … WHERE status = $expected` | Siklus hidup async dengan provider |
| `PaymentIntent` (+ `Refund`) | merchant, merchant_order_id, amount, `refunded_amount`, status, refunds | LI-07, LI-08, LI-13 | `SELECT … FOR UPDATE` pada intent saat confirm/refund | Σ refund harus dibandingkan dengan amount **atomik** di satu baris |
| `Payout` | user, bank_account, amount, hold_journal_id, status, `needs_reconciliation`, attempts | LI-07, LI-10 | `UPDATE … WHERE status = $expected` | Siklus hidup unknown-outcome |
| `IdempotencyRecord` | (client_type, client_id, endpoint, key), request_hash, response (NULL = masih berjalan) | LI-05 | Unique index (menunggu insert yang sedang berjalan) | Unit "satu niat = satu hasil" |
| `InboxEvent` | (provider, provider_event_id), payload, raw_body, status | LI-09 | Unique index + claim `SKIP LOCKED` | Bukti mentah + dedupe |
| `WebhookDelivery` | merchant, event_id, attempts, last_status | — (andal, bukan invariant uang) | Claim `SKIP LOCKED` | Siklus retry per event |
| `ReconRun` (+ `ReconItem`) | date, file_hash, counts, items | LI-12 | Unique `(provider, date, file_hash)` | Unit kerja rekonsiliasi per hari |

### 4.1 Deviasi pragmatis: `Account` dan `Journal` ditulis dalam satu DB transaction

Aturan DDD klasik: *satu transaksi memodifikasi satu aggregate*; aggregate lain diperbarui secara eventual. Di LedgerPay aturan ini **sengaja dilanggar**. Satu DB transaction transfer menulis `IdempotencyRecord`, dua `Account`, satu `Journal`, satu `Transfer`, dan satu `Job` (outbox).

| Pertanyaan | Jawaban |
|---|---|
| Kenapa tidak membuat `Account` + `Journal` satu aggregate? | Journal menyentuh ≥ 2 account, termasuk akun sistem yang dipakai semua transaksi. Kalau account adalah bagian aggregate journal (atau sebaliknya), seluruh ledger menjadi satu aggregate raksasa, dan satu lock untuk semua |
| Kenapa tidak eventual (journal dulu, saldo menyusul)? | Saldo eventual = cek saldo terhadap angka basi, sehingga double-spend. LI-02 dan LI-03 harus **strongly consistent** |
| Kenapa pelanggaran ini aman? | Aturan "satu aggregate per tx" ada untuk skalabilitas di sistem terdistribusi. Di sini semua aggregate tinggal di **satu Postgres**, transaksi pendek (tanpa network call, ADR-011), dan urutan lock deterministik (ADR-L03). Biayanya kecil, jaminannya ACID |
| Kapan harus diubah? | Saat ledger dipisah menjadi service atau di-shard (V3). Saat itu transfer lintas shard menjadi saga dengan akun suspense (desain arsitektur §4) |

> **Poin interview:** "Saya memakai konsep aggregate untuk menentukan *apa yang harus di-lock bersama*, bukan sebagai aturan dogmatis. Selama semuanya ada di satu database ACID, menulis beberapa aggregate dalam satu transaksi pendek adalah pilihan yang paling benar."

---

## 5. State Machines

Semua transisi memakai **guard kondisional** `UPDATE <tabel> SET status = $to WHERE id = $id AND status = $from`. Jika 0 baris ter-update, ada pihak lain yang sudah mengubahnya, sehingga tidak ada efek (lihat LI-07). Journal dan outbox event ditulis **di DB transaction yang sama** dengan transisi.

### 5.1 TopUp
```text
          ┌──────── webhook SUCCEEDED ─────────┐
          │                                    ▼
      ┌───────┐  webhook FAILED  ┌────────┐ ┌───────────┐
 ───▶ │PENDING│ ───────────────▶ │ FAILED │ │ SUCCEEDED │
      └───┬───┘                  └────────┘ └───────────┘
          │ sweeper (status provider = belum dibayar, > 24 jam)   ▲
          ▼                                                       │
      ┌───────┐  webhook FAILED → FAILED     webhook SUCCEEDED ───┘ (terlambat; status lunak)
      │EXPIRED│
      └───────┘
```
| From | To | Trigger | Guard | Side effect |
|---|---|---|---|---|
| — | `PENDING` | `POST /topups` (tx1, pola b ADR-L02) | Idempotency key baru | — |
| `PENDING` | `SUCCEEDED` | Inbox event `SUCCEEDED` | amount & currency event = top-up | Journal `TOPUP_CREDIT`; outbox `topup.succeeded` |
| `PENDING` | `FAILED` | Inbox event `FAILED` | — | — |
| `PENDING` | `EXPIRED` | Sweeper | Status query provider ≠ paid, umur > 24 jam | — |
| `EXPIRED` | `SUCCEEDED` | Inbox event `SUCCEEDED` terlambat | sama | Journal `TOPUP_CREDIT`; metrik `webhook_late_total` |
| `EXPIRED` | `FAILED` | Inbox event `FAILED` | — | — |
| `SUCCEEDED` ↔ `FAILED` | (tidak ada) | Event bertentangan | — | **Konflik** → flag recon, tidak dibalik otomatis |

### 5.2 PaymentIntent (+ Refund)
```text
 ──▶ REQUIRES_CONFIRMATION ──confirm──▶ SUCCEEDED ──refund parsial──▶ PARTIALLY_REFUNDED ──refund sisa──▶ REFUNDED
            │        │                       └──────────refund penuh─────────────────────────────▲
            │        └──expiry (30 mnt)──▶ EXPIRED                   (PARTIALLY_REFUNDED ─refund parsial─▶ PARTIALLY_REFUNDED)
            └──merchant cancel──▶ CANCELLED
```
| From | To | Trigger | Guard | Side effect |
|---|---|---|---|---|
| — | `REQUIRES_CONFIRMATION` | Merchant `POST /payment-intents` | `merchant_order_id` belum punya intent aktif | — |
| `REQUIRES_CONFIRMATION` | `SUCCEEDED` | User confirm (idempoten) | `FOR UPDATE` intent; belum kedaluwarsa; saldo cukup | Journal `PAYMENT`; outbox `payment.succeeded` |
| `REQUIRES_CONFIRMATION` | `EXPIRED` | Sweeper | `expires_at < now()` | Outbox `payment.expired` |
| `REQUIRES_CONFIRMATION` | `CANCELLED` | Merchant cancel | — | — |
| `SUCCEEDED` / `PARTIALLY_REFUNDED` | `PARTIALLY_REFUNDED` | Refund | `refunded_amount + x < amount`; saldo merchant cukup | Row `Refund` + journal `REFUND`; outbox `payment.refunded` |
| `SUCCEEDED` / `PARTIALLY_REFUNDED` | `REFUNDED` | Refund | `refunded_amount + x = amount` | sama |
| Lainnya | — | — | — | `409 INVALID_STATE_TRANSITION` |

`Refund` sendiri tidak punya state machine di V1: refund sinkron, jadi row ada = berhasil.

### 5.3 Payout
```text
 ──▶ PENDING ──worker commit──▶ SUBMITTED ──bank 2xx────────▶ SUCCEEDED
                                    │ ──bank 4xx definitif──▶ FAILED
                                    │ ──timeout / 5xx──────▶ UNKNOWN ──status query / recon / ops──▶ SUCCEEDED | FAILED
                                    └ (job diambil ulang setelah crash) ─────▶ UNKNOWN
```
| From | To | Trigger | Guard | Side effect |
|---|---|---|---|---|
| — | `PENDING` | `POST /payouts` | Idempotency; saldo cukup (lock wallet) | Journal `PAYOUT_HOLD`; job `payout.submit` |
| `PENDING` | `SUBMITTED` | Worker, **commit sebelum** memanggil bank | — | — |
| `SUBMITTED` | `SUCCEEDED` | Bank 2xx | — | Journal `PAYOUT_SETTLE`; outbox `payout.succeeded` |
| `SUBMITTED` | `FAILED` | Bank 4xx bisnis | — | Journal `PAYOUT_REVERSAL` (`reversal_of` = hold); outbox `payout.failed` |
| `SUBMITTED` | `UNKNOWN` | Timeout/5xx/koneksi putus, **atau** job diambil ulang dan status masih `SUBMITTED` | — | Job `payout.status_check` (30 dtk → 2 mnt → 10 mnt → 1 jam → 6 jam); outbox `payout.unknown` |
| `UNKNOWN` | `SUCCEEDED`/`FAILED` | Status query / recon / ops (bukti provider, ter-audit) | — | Settle / reversal seperti di atas |
| `UNKNOWN` | (resubmit, tetap `UNKNOWN`/`SUBMITTED`) | Bank menjawab `NOT_FOUND` **setelah** safe window | Key sama (`payout_id`) | — |
| `UNKNOWN` (habis jadwal) | `UNKNOWN` + `needs_reconciliation = true` | Scheduler | — | Masuk antrean ops; alert umur |

### 5.4 InboxEvent
| From | To | Trigger | Side effect |
|---|---|---|---|
| — | `RECEIVED` | Webhook valid (signature + timestamp) | Duplikat (`provider_event_id` sama) → tidak ada row baru, 200 |
| `RECEIVED` | `PROCESSED` | Worker berhasil menerapkan transisi (dalam tx yang sama) | Transisi TopUp + journal |
| `RECEIVED` | `IGNORED` | Transisi tidak berlaku (event lama/duplikat logis) | — |
| `RECEIVED` | `DEAD` | Gagal 8× atau amount/currency tidak cocok | Alert, replay manual dari ops |

### 5.5 WebhookDelivery (keluar)
| From | To | Trigger | Side effect |
|---|---|---|---|
| — | `PENDING` | Outbox event merchant-facing | — |
| `PENDING` | `DELIVERED` | Merchant membalas 2xx dalam 5 detik | Attempt dicatat |
| `PENDING` | `PENDING` | Non-2xx/timeout; jadwal 1m, 5m, 30m, 2j, 6j, 24j | Attempt dicatat |
| `PENDING` | `DEAD` | Jadwal habis | Alert; replay manual (membuat attempt baru dengan event ID yang sama) |

### 5.6 ReconItem
| Klasifikasi | Arti | Resolusi umum |
|---|---|---|
| `MATCHED` | Ada di kedua sisi, amount & status sama | — (tidak masuk antrean) |
| `MISSING_INTERNAL` | Ada di provider, tidak ada di LedgerPay | Investigasi: webhook hilang? Replay inbox / buat top-up manual ter-audit |
| `MISSING_EXTERNAL` | Ada di LedgerPay, tidak ada di provider | Biasanya cut-off; jika tetap hilang di D+1 → eskalasi |
| `AMOUNT_MISMATCH` | Ada di kedua sisi, amount berbeda | Eskalasi (indikasi bug/serangan); tidak ada koreksi otomatis |
| `STATUS_MISMATCH` | Status berbeda (contoh: payout `UNKNOWN` vs provider `SUCCEEDED`) | Ops menerapkan transisi yang benar (settle/reversal) berdasarkan bukti |

Resolusi: `OPEN → RESOLVED` (wajib alasan + actor; aksi finansial tetap lewat journal, bukan edit saldo).

---

## 6. Account Model & Journal Templates

| Akun | Tipe | Normal | Materialized? |
|---|---|---|---|
| `WALLET:<user>` / `WALLET:<merchant>` | Liability | Kredit | Ya (`balance`, `CHECK ≥ 0`) |
| `PROVIDER_CLEARING` | Asset | Debit | Tidak (derived) |
| `BANK_CLEARING` | Asset | Debit | Tidak |
| `PAYOUT_IN_TRANSIT` | Liability (suspense) | Kredit | Tidak |
| `FEE_REVENUE` (P3) | Revenue | Kredit | Tidak |

| `JournalPurpose` | `source_type` | Debit | Kredit | Catatan |
|---|---|---|---|---|
| `TOPUP_CREDIT` | `topup` | `PROVIDER_CLEARING` | `WALLET:user` | Saat TopUp → `SUCCEEDED` |
| `TRANSFER` | `transfer` | `WALLET:sender` | `WALLET:receiver` | Sinkron |
| `PAYMENT` | `payment_intent` | `WALLET:user` | `WALLET:merchant` | Saat confirm |
| `REFUND` | `refund` | `WALLET:merchant` | `WALLET:user` | `source_id` = refund_id (satu intent bisa banyak refund) |
| `PAYOUT_HOLD` | `payout` | `WALLET:user` | `PAYOUT_IN_TRANSIT` | Saat `POST /payouts` |
| `PAYOUT_SETTLE` | `payout` | `PAYOUT_IN_TRANSIT` | `BANK_CLEARING` | Saat `SUCCEEDED` |
| `PAYOUT_REVERSAL` | `payout` | `PAYOUT_IN_TRANSIT` | `WALLET:user` | Saat `FAILED`; `reversal_of` = journal hold |
| `PROVIDER_SETTLEMENT` | `provider_settlement` | `BANK_CLEARING` | `PROVIDER_CLEARING` | Dibuat recon (P1) saat item settlement `MATCHED`; `source_id` = **topup_id** (final review R1-5), sehingga recon ulang, termasuk dengan file ber-hash berbeda, tidak memposting dua kali |
| `ADJUSTMENT_MANUAL` | `ops_action` | sesuai keputusan ops | sesuai keputusan ops | Hanya role OPS, wajib alasan + audit; tetap seimbang |

Ledger memvalidasi bahwa kombinasi `purpose` dan arah akun sesuai tabel ini (APP). Contoh: journal `TRANSFER` yang mendebit akun sistem ditolak. Sementara itu `UNIQUE (source_type, source_id, purpose)` menjamin satu objek bisnis tidak di-posting dua kali untuk tujuan yang sama (LI-06). Untuk payout, ketiga purpose berbeda sehingga hold + settle **atau** hold + reversal bisa hidup bersama, tetapi settle dan reversal dicegah terjadi bersamaan oleh state machine (LI-07). Settlement provider → bank (`BANK_CLEARING` / `PROVIDER_CLEARING`) diposting otomatis oleh recon dengan purpose `PROVIDER_SETTLEMENT` dan source `('provider_settlement', topup_id)`. Jalur ini bukan `ADJUSTMENT_MANUAL`, karena koreksi manual harus tetap menjadi pengecualian yang jarang.

---

## 7. Domain Events

Semua event ditulis ke tabel `jobs` (outbox, ADR-006) **di dalam DB transaction yang menghasilkan perubahan**. Delivery bersifat at-least-once, sehingga setiap consumer wajib dedupe.

| Event | Producer | Kapan | Consumer | Payload inti | Kunci dedupe consumer |
|---|---|---|---|---|---|
| `topup.succeeded` | topup | TopUp → `SUCCEEDED` | Notifikasi (P3) | topup_id, user_id, amount | event_id |
| `transfer.completed` | transfer | Transfer dibuat | Notifikasi (P3) | transfer_id, sender, receiver, amount | event_id |
| `payment.succeeded` | payment | Intent → `SUCCEEDED` | Webhook keluar (LF-15) | intent_id, merchant_order_id, amount | event_id (stabil antar retry) |
| `payment.refunded` | payment | Refund dibuat | Webhook keluar | intent_id, refund_id, amount, refunded_total | event_id |
| `payment.expired` | payment | Intent → `EXPIRED` | Webhook keluar | intent_id, merchant_order_id | event_id |
| `payout.requested` | payout | Payout `PENDING` | Payout executor (job `payout.submit`) | payout_id | payout_id (status guard) |
| `payout.succeeded` | payout | → `SUCCEEDED` | Notifikasi | payout_id, amount | event_id |
| `payout.failed` | payout | → `FAILED` | Notifikasi | payout_id, reason | event_id |
| `payout.unknown` | payout | → `UNKNOWN` | Status checker, metrik | payout_id, attempt | payout_id + attempt |
| `recon.mismatch_detected` | recon | ReconItem bukan `MATCHED` | Ops queue, alert | recon_run_id, item_id, kategori | item_id |

> `payout.requested` sebenarnya adalah **command job** (`payout.submit`) yang ditulis lewat outbox, bukan event yang di-broadcast. Di V1 keduanya memakai tabel yang sama, sehingga perbedaannya cukup dicatat di nama `kind`.

---

## 8. Transaction Boundaries

Notasi: **Tx** = satu DB transaction (Read Committed, ADR-011). Lock dicantumkan sesuai urutannya.

| Use case | Ditulis dalam satu Tx | Lock (urutan) | Di luar Tx | Outbox/job |
|---|---|---|---|---|
| Register | User, Account wallet (balance 0), credential | — (unique email) | — | — |
| Transfer | IdempotencyRecord, Journal + entries, 2× Account.balance, Transfer | 1) unique idempotency key; 2) `FOR UPDATE` wallet sender & receiver `ORDER BY id` | — | `transfer.completed` |
| Create top-up | **Tx1:** IdempotencyRecord (response NULL), TopUp `PENDING` + `recovery_point = CREATED`. **Tx2:** TopUp.provider_ref/checkout_url + `recovery_point = PROVIDER_CREATED`, IdempotencyRecord (response) — recovery point hanya di `topups` (desain database) | unique key | **Antara Tx1 dan Tx2:** `POST provider /charges` (key = topup_id) | — |
| Terima webhook | InboxEvent `RECEIVED` | unique `(provider, event_id)` | Verifikasi HMAC sebelum Tx | — (inbox diklaim worker) |
| Proses inbox top-up | InboxEvent → `PROCESSED`, TopUp transisi, Journal `TOPUP_CREDIT`, Account wallet | 1) claim inbox `SKIP LOCKED`; 2) guard status TopUp; 3) `FOR UPDATE` wallet | — | `topup.succeeded` |
| Create payment intent | IdempotencyRecord, PaymentIntent | unique key; unique `(merchant_id, merchant_order_id)` aktif | — | — |
| Confirm payment | IdempotencyRecord, PaymentIntent → `SUCCEEDED`, Journal `PAYMENT`, 2× Account | 1) key; 2) `FOR UPDATE` intent; 3) wallet user & merchant `ORDER BY id` | — | `payment.succeeded` |
| Refund | IdempotencyRecord, Refund, PaymentIntent (`refunded_amount`, status), Journal `REFUND`, 2× Account | 1) key; 2) `FOR UPDATE` intent; 3) wallet `ORDER BY id` | — | `payment.refunded` |
| Create payout | IdempotencyRecord, Payout `PENDING`, Journal `PAYOUT_HOLD`, Account wallet | 1) key; 2) `FOR UPDATE` wallet | — | job `payout.submit` |
| Payout submit | **Tx-a:** Payout `PENDING → SUBMITTED` (+ attempts). **Tx-b:** transisi hasil + journal settle/reversal (+ wallet saat reversal) + `CompleteInTx` job | Tx-a: guard status. Tx-b: guard status; `FOR UPDATE` wallet jika reversal | **Antara Tx-a dan Tx-b:** `POST bank /payouts` (key = payout_id) | `payout.succeeded/failed/unknown`; job `payout.status_check` jika `UNKNOWN` |
| Payout status check | Transisi `UNKNOWN →` hasil + journal + `CompleteInTx` | guard status; wallet jika reversal | `GET bank /payouts/{id}` **sebelum** Tx | job berikutnya jika masih `UNKNOWN` |
| Recon run | ReconRun + ReconItems (batch per N baris), lalu ReconRun `COMPLETED` | unique `(provider, date, file_hash)` | Unduh settlement report **sebelum** Tx | `recon.mismatch_detected` per item |
| Ops resolve mismatch | ReconItem `RESOLVED`, transisi bisnis yang relevan + journal, AuditEntry | guard status item & objek bisnis; wallet jika perlu | — | event bisnis terkait |

**Aturan yang terlihat dari tabel:**
1. Tidak ada satu pun network call di dalam Tx.
2. Setiap operasi yang memanggil pihak luar dipecah menjadi **Tx sebelum** (mencatat niat) dan **Tx sesudah** (mencatat hasil). Celah di antaranya selalu bisa dideteksi dari status (`CREATED`, `SUBMITTED`).
3. Urutan lock global: **idempotency key → objek bisnis (intent/payout) → wallet (ORDER BY id)**. Urutan yang sama di semua use case mencegah deadlock lintas use case.

---

## 9. Context Map / Interaksi Modul

```text
                    ┌─────────────┐
                    │  identity   │ (User, Merchant, ApiKey)
                    └──────┬──────┘
                           │ principal (client_id)
   ┌───────────┬───────────┼───────────┬────────────┐
   ▼           ▼           ▼           ▼            ▼
┌────────┐ ┌────────┐ ┌─────────┐ ┌────────┐  ┌──────────┐
│transfer│ │payment │ │ topup   │ │ payout │  │  ops     │
└───┬────┘ └───┬────┘ └───┬─────┘ └───┬────┘  └────┬─────┘
    │          │          │           │            │ (memanggil service modul lain,
    │  idempotency.Begin/Complete (tx yang sama)   │  tidak pernah ledger langsung
    │          │          │           │            │  kecuali ADJUSTMENT_MANUAL)
    ▼          ▼          ▼           ▼            ▼
 ┌────────────────────────────────────────────────────┐
 │ ledger.PostJournal(ctx, tx, journal)               │ ← satu-satunya pengubah saldo
 │ ledger.LockAccountsOrdered(ctx, tx, ids)           │
 └────────────────────────────────────────────────────┘
          ▲                         ▲
   ┌──────┴──────┐           ┌──────┴──────┐
   │ webhook     │ inbox ──▶ │ topup (svc) │
   │ (in + out)  │◀── outbox │ payment     │
   └─────────────┘           └─────────────┘
          ▲
   ┌──────┴──────┐  baca entries/status, tidak menulis ledger kecuali
   │   recon     │  lewat ops resolution
   └─────────────┘
```

| Pemanggil `ledger.PostJournal` | Purpose |
|---|---|
| `topup` | `TOPUP_CREDIT` |
| `transfer` | `TRANSFER` |
| `payment` | `PAYMENT`, `REFUND` |
| `payout` | `PAYOUT_HOLD`, `PAYOUT_SETTLE`, `PAYOUT_REVERSAL` |
| `ops` | `ADJUSTMENT_MANUAL` (role OPS, ter-audit) |

`ledger` tidak meng-import modul mana pun. `wallet` hanya membaca (saldo + riwayat). `webhook` tidak memanggil ledger secara langsung: inbox diterapkan oleh service `topup`. `recon` memanggil ledger **hanya** untuk purpose `PROVIDER_SETTLEMENT` (item settlement yang `MATCHED`). Resolusi mismatch lainnya dijalankan lewat `ops`, yang memanggil service pemilik objek bisnis. Aturan ini ditegakkan oleh depguard (ADR-001).
