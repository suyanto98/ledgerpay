# LedgerPay: Feature Matrix & Feature Specification

> Referensi: [strategi produk LedgerPay](01-product-strategy.md) (workflow W1–W5, business rules BR1–BR10, challenges C1–C11).
> Skala dan arti prioritas: lihat `00-index.md`.

---

## 1. Feature Matrix

| ID | Feature | Business Value | Technical Complexity | Production Relevance | Demo Impact | Priority |
|---|---|---:|---:|---:|---:|---|
| LF-01 | User auth (register, login, refresh, logout) | 4 | 2 | 4 | 1 | **P0** |
| LF-02 | Wallet: saldo + riwayat transaksi | 5 | 2 | 4 | 3 | **P0** |
| LF-03 | Double-entry ledger core | 5 | 4 | 5 | 4 | **P0** |
| LF-04 | P2P transfer (concurrency-safe) | 5 | 4 | 5 | 5 | **P0** |
| LF-05 | Idempotency untuk endpoint yang memindahkan uang | 5 | 4 | 5 | 5 | **P0** |
| LF-06 | Mock provider (PG + Bank) + failure injection | 3 | 3 | 4 | 5 | **P0** (enabler) |
| LF-07 | Top-up via provider + webhook masuk (inbox) | 5 | 4 | 5 | 4 | **P0** |
| LF-08 | Transactional outbox + job runner | 4 | 4 | 5 | 3 | **P0** |
| LF-09 | Merchant onboarding + API key | 4 | 2 | 4 | 2 | **P0** |
| LF-10 | Payment intent + pembayaran oleh user (checkout) | 5 | 3 | 5 | 4 | **P0** |
| LF-11 | Payout dengan *unknown outcome* | 5 | 5 | 5 | 5 | **P0** |
| LF-12 | Audit log (aksi user, ops, sistem) | 4 | 2 | 4 | 3 | **P0** |
| LF-13 | Health check + structured log + correlation ID | 3 | 2 | 5 | 3 | **P0** |
| LF-14 | UI: wallet user, checkout, halaman mock provider | 4 | 2 | 2 | 4 | **P0** |
| LF-15 | Outbound webhook ke merchant (signed, retry, DLQ) | 4 | 4 | 5 | 4 | **P1** |
| LF-16 | Refund (partial/full) | 4 | 3 | 4 | 3 | **P1** |
| LF-17 | Reconciliation harian + antrean mismatch | 5 | 4 | 5 | 5 | **P1** |
| LF-18 | Ops console (investigasi + resolusi ter-audit) | 4 | 3 | 4 | 4 | **P1** (rute `/ops/*` + role OPS minimal untuk API key merchant sudah P0 di S4; final review) |
| LF-19 | Business-invariant metrics + dashboard | 3 | 3 | 5 | 4 | **P1** |
| LF-20 | Rate limiting (login, endpoint uang, API key) | 3 | 2 | 5 | 2 | **P1** (dinaikkan dari P2 di threat model: kontrol MVP C-07) |
| LF-21 | Batas saldo & batas transaksi yang dapat dikonfigurasi | 3 | 2 | 3 | 2 | **P2** |
| LF-22 | PIN transaksi / step-up auth | 3 | 3 | 4 | 2 | **P2** |
| LF-23 | Fee transaksi (journal 3 kaki) | 3 | 3 | 3 | 2 | **P3** |
| LF-24 | Notifikasi email | 2 | 2 | 2 | 1 | **P3** |
| LF-25 | Risk rule sederhana (velocity limit) | 3 | 3 | 3 | 2 | **P3** |
| LF-26 | Balance snapshot / checkpoint | 2 | 3 | 3 | 1 | **P3** (hanya jika load test membuktikan perlu) |

### Ringkasan per prioritas
| Prioritas | Jumlah | Tujuan |
|---|---|---|
| P0 | 14 | MVP yang bisa dipakai + Demo A, B, C |
| P1 | 6 | "Selesai": Demo D, integrasi merchant lengkap, observability bermakna |
| P2 | 2 | Hardening production |
| P3 | 4 | Bahan diskusi interview; tidak dibangun kecuali waktu sangat longgar |

### Fitur yang dipertimbangkan lalu ditolak (tidak masuk matrix)
| Fitur | Kenapa ditolak |
|---|---|
| QR payment | Hanya mengganti cara input (UI); alur uangnya sama dengan payment intent |
| Split bill / request money | CRUD di atas transfer, tanpa tantangan baru |
| Scheduled / recurring transfer | Butuh scheduler, tetapi tantangannya sudah dicakup job runner dan payout |
| Multi-wallet per user (tabungan, kantong) | Menambah akun tanpa invariant baru |
| Admin CRUD user | Tidak ada persona yang membutuhkannya di MVP |

---

## 2. Feature Specification (P0 & P1)

Format per fitur: **Tujuan · User story · Acceptance criteria · Business rules · Edge cases · Dependency · Bukti (test/demo)**.
Detail tabel, endpoint, dan isolation level **tidak** ditulis di sini. Bagian itu masuk desain database (database), kontrak API (API), dan desain transaksi & concurrency (transaction).

---

### LF-01 — User Authentication (P0)
**Tujuan:** identitas user untuk kepemilikan wallet. **Persona:** Rina.

**User story:** Sebagai user, saya ingin mendaftar dan login dengan email + password agar hanya saya yang bisa memakai wallet saya.

**Acceptance criteria**
1. Register membuat user **dan** wallet IDR dengan saldo 0 dalam satu transaksi.
2. Login berhasil mengembalikan access token (15 menit) dan menetapkan refresh token di cookie HttpOnly.
3. Refresh merotasi token. Refresh token lama yang dipakai ulang mencabut seluruh *token family* (ADR-009).
4. Logout mencabut refresh token aktif.
5. Pesan error login tidak membedakan "email tidak ada" dari "password salah".

**Business rules:** email unik (case-insensitive); password minimal 10 karakter.
**Edge cases:** register paralel dengan email yang sama → satu berhasil, satu 409 (unique constraint); refresh paralel dari dua tab → satu berhasil, yang lain ditolak tanpa memicu false-positive reuse detection (grace window singkat, diputuskan di threat model).
**Dependency:** skeleton (DB, HTTP, config).
**Bukti:** integration test rotasi dan reuse detection.

---

### LF-02 — Wallet: Saldo & Riwayat (P0)
**Tujuan:** user melihat saldo dan riwayat yang **bisa dijelaskan**.

**User story:** Sebagai user, saya ingin melihat saldo dan riwayat transaksi beserta statusnya agar saya yakin uang saya aman.

**Acceptance criteria**
1. Saldo yang ditampilkan = `accounts.balance` milik wallet user, dan selalu konsisten dengan Σ entries (ADR-L01).
2. Riwayat menampilkan transaksi bisnis (top-up, transfer, payment, refund, payout) dengan status dan arah (masuk/keluar), cursor-paginated, terbaru di atas.
3. Detail transaksi menampilkan journal ID dan timestamp, untuk keperluan dukungan pelanggan.
4. User hanya bisa melihat wallet miliknya. Wallet milik user lain → 404.

**Edge cases:** riwayat besar (10k+ baris) tetap cepat lewat index + cursor; transaksi `PENDING` (payout yang di-hold) tampil dengan jelas sebagai "dana ditahan".
**Dependency:** LF-03.
**Bukti:** BOLA test (user A membaca wallet B → 404).

---

### LF-03 — Double-Entry Ledger Core (P0)
**Tujuan:** sumber kebenaran tunggal untuk semua perpindahan uang. **Persona:** Sari, on-call engineer.

**User story:** Sebagai finance, saya ingin setiap perpindahan uang tercatat sebagai journal seimbang yang tidak bisa diubah, agar saldo selalu bisa diaudit.

**Acceptance criteria**
1. API internal `PostJournal(ctx, tx, journal)` adalah **satu-satunya** cara mengubah saldo. Modul lain tidak boleh meng-update `accounts.balance` secara langsung (ditegakkan lewat batas modul, ADR-001).
2. Journal ditolak jika Σ debit ≠ Σ kredit, jika kurang dari 2 entry, jika amount ≤ 0, atau jika currency campur.
3. Journal dan entries tidak bisa di-UPDATE/DELETE (trigger DB + grant).
4. Reversal membuat journal baru yang mereferensikan journal asal. Journal hanya bisa di-reverse sekali.
5. Job verifikasi berkala membandingkan `balance` dengan Σ entries per akun dan menerbitkan metrik drift.

**Business rules:** BR1, BR2, BR4, BR8.
**Edge cases:** akun sistem boleh bersaldo negatif sesuai tipenya (ADR-L07); posting ke akun yang dibekukan → ditolak.
**Dependency:** skeleton.
**Bukti:** property-based test (journal acak selalu seimbang; Σ semua saldo user + sistem konsisten), test trigger immutability.

---

### LF-04 — P2P Transfer (P0)
**Tujuan:** perpindahan uang internal; panggung untuk membuktikan concurrency. **Persona:** Rina.

**User story:** Sebagai user, saya ingin mengirim uang ke user lain secara instan, dan tidak pernah terdebet dua kali walaupun saya menekan tombol berkali-kali.

**Acceptance criteria**
1. Transfer sukses: saldo pengirim berkurang dan saldo penerima bertambah dalam **satu DB transaction**, dengan satu journal.
2. Saldo tidak cukup → 422 `INSUFFICIENT_BALANCE`, tanpa efek apa pun.
3. Transfer ke diri sendiri → 422. Penerima tidak ada → 404 (pesan tidak membocorkan informasi lebih).
4. Wajib `Idempotency-Key` (LF-05).
5. N transfer paralel dari satu saldo tidak pernah menghasilkan saldo negatif atau lost update (ADR-L03).
6. Transfer A→B dan B→A bersamaan tidak deadlock (lock ordering). Kalau deadlock tetap terdeteksi oleh DB, transaksi di-retry secara terbatas (ADR-011).

**Business rules:** BR1, BR2, BR3, BR8; batas transaksi (LF-21, P2).
**Dependency:** LF-03, LF-05.
**Bukti:** **Demo B**; concurrency test di CI (100 goroutine) + test pembanding implementasi naif (hanya di test code).

---

### LF-05 — Idempotency (P0)
**Tujuan:** satu niat user = satu efek finansial. **Persona:** Rina, Budi.

**User story:** Sebagai client (app/merchant), saya ingin mengirim ulang request yang timeout dengan aman tanpa takut uang berpindah dua kali.

**Acceptance criteria**
1. Endpoint yang memindahkan uang menolak request tanpa `Idempotency-Key` (400).
2. Key yang sama + payload sama → response yang sama persis (status + body) dengan header `Idempotent-Replayed: true`.
3. Key yang sama + payload berbeda → 422 `IDEMPOTENCY_KEY_REUSED`.
4. Dua request dengan key sama yang datang bersamaan → tepat satu efek. Request kedua menunggu lalu replay, atau 409 `REQUEST_IN_PROGRESS` jika melewati `lock_timeout`.
5. Request yang gagal karena validasi (4xx) **juga** disimpan dan di-replay. Kegagalan sistem (5xx sebelum commit) **tidak** disimpan, jadi client boleh retry.
6. Key disimpan per `(client_id, endpoint)`. Key yang sama dari user berbeda tidak saling bentrok.
7. Key kedaluwarsa setelah 7 hari (job pembersih).

**Business rules:** BR3.
**Dependency:** LF-03, skeleton DB.
**Bukti:** **Demo A**; integration test untuk keempat kasus di atas.

> Desain lengkapnya ada di ADR-L02. Poin kunci untuk interview: **kenapa key disimpan di transaksi yang sama dengan efek finansialnya**.

---

### LF-06 — Mock Provider + Failure Injection (P0, enabler)
**Tujuan:** sistem eksternal yang realistis dan **bisa dikendalikan** untuk testing dan demo.

**User story:** Sebagai engineer, saya ingin mensimulasikan provider yang timeout, mengirim webhook ganda, atau terlambat, agar setiap failure scenario bisa di-reproduce secara otomatis.

**Acceptance criteria**
1. Service terpisah dengan API: buat sesi pembayaran (top-up), halaman checkout hosted, payout submit, payout status query, settlement report harian (CSV), dan admin skenario.
2. Webhook ditandatangani HMAC-SHA256 + timestamp.
3. Skenario yang bisa diinjeksi: `timeout_after_process`, `timeout_before_process`, `error_5xx`, `decline`, `webhook_duplicate:<n>`, `webhook_delay:<dur>`, `webhook_out_of_order`, `webhook_bad_signature`, `settlement_mismatch:<…>` (nama kanonik ADR-L05; daftar lengkap di `14-api-external-contracts.md` §1.7). Skenario bisa diatur per request (header `X-Mock-Scenario` dari test harness), lewat *magic values* (misalnya nomor rekening berakhiran `0408` memicu timeout; berguna untuk E2E yang lewat LedgerPay), atau global (admin toggle untuk demo).
4. Payout idempoten berdasarkan idempotency key dari LedgerPay (perilaku provider nyata).
5. State mock disimpan (SQLite atau schema Postgres terpisah) sehingga status query dan settlement report konsisten.

**Dependency:** tidak ada (bisa dibangun paralel).
**Bukti:** dipakai Demo C, Demo D, dan test failure scenario.

---

### LF-07 — Top-up + Inbound Webhook (P0)
**Tujuan:** uang masuk dari luar sistem. **Persona:** Rina.

**User story:** Sebagai user, saya ingin mengisi saldo lewat payment provider, dan saldo bertambah tepat satu kali setelah pembayaran berhasil.

**Acceptance criteria**
1. `POST /topups` (idempoten) membuat TopUp `PENDING` dan mengembalikan URL checkout provider (pola *recovery point*, ADR-L02).
2. Webhook dengan signature tidak valid → 401. Timestamp di luar window 5 menit → 400. Keduanya tanpa efek.
3. Webhook valid disimpan ke inbox (unique `provider_event_id`) dan langsung dijawab 200. Worker memprosesnya secara asinkron (ADR-L08).
4. Event `SUCCEEDED` → TopUp `SUCCEEDED` + journal (DR Provider Clearing / CR Wallet) dalam satu transaksi.
5. Event duplikat atau event lama yang datang belakangan → no-op (transisi monoton).
6. TopUp `PENDING` yang lewat batas waktu (misalnya 24 jam) → `EXPIRED` oleh job, setelah status di provider dicek terlebih dahulu. `EXPIRED` adalah status *lunak*: webhook sukses yang datang terlambat tetap mengkredit saldo (uang memang sudah diterima provider) dan menaikkan metrik `webhook_late_total` (ADR-L08).

**Business rules:** BR7, BR10.
**Edge cases:** webhook datang **sebelum** response `POST /topups` diterima client; webhook untuk TopUp yang tidak dikenal → disimpan dan ditandai untuk investigasi, bukan dibuang.
**Dependency:** LF-03, LF-05, LF-06, LF-08.
**Bukti:** test failure scenario #6 dan #7 (desain domain).

---

### LF-08 — Transactional Outbox + Job Runner (P0)
**Tujuan:** side effect yang tidak hilang dan tidak terjadi tanpa commit. **Persona:** on-call engineer.

**User story:** Sebagai engineer, saya ingin setiap side effect (webhook, panggilan provider, notifikasi) dicatat di transaksi yang sama dengan perubahan bisnisnya, dan dieksekusi minimal sekali oleh worker.

**Acceptance criteria**
1. Event/job ditulis ke tabel outbox/job **di transaksi yang sama** dengan perubahan bisnis.
2. Beberapa worker bisa berjalan paralel tanpa mengambil job yang sama (`SKIP LOCKED` + lease).
3. Worker yang mati di tengah job → job diambil ulang setelah lease habis.
4. Gagal → retry dengan exponential backoff + jitter. Setelah N kali → status `dead` dan alert.
5. Setiap handler job wajib idempoten (didokumentasikan + diuji).
6. Job `dead` bisa di-retry manual dari ops console (P1).

**Dependency:** skeleton. **Shared** dengan PayRun (ADR-006).
**Bukti:** test kill worker di tengah job; metrik queue depth dan umur job tertua.

---

### LF-09 — Merchant Onboarding + API Key (P0)
**Tujuan:** merchant bisa memakai API secara server-to-server. **Persona:** Budi.

**Acceptance criteria**
1. Merchant didaftarkan lewat seed/ops (tanpa self-service signup di MVP). Merchant punya akun ledger sendiri.
2. API key dibuat, ditampilkan **sekali**, dan disimpan ter-hash. Key bisa di-revoke dan dirotasi (dua key aktif selama masa transisi).
3. Webhook secret per merchant untuk menandatangani webhook keluar (LF-15).
4. Request dengan API key hanya bisa mengakses resource milik merchant tersebut.

**Dependency:** LF-01 (pola auth), LF-03.
**Bukti:** test BOLA antar merchant.

---

### LF-10 — Payment Intent + Checkout (P0)
**Tujuan:** merchant menerima pembayaran dari user wallet. **Persona:** Budi, Rina.

**User story:** Sebagai merchant, saya ingin membuat permintaan pembayaran untuk sebuah order, dan sebagai user saya ingin membayarnya dengan saldo wallet.

**Acceptance criteria**
1. Merchant `POST /payment-intents {amount, merchant_order_id}` (API key, idempoten) → intent `REQUIRES_CONFIRMATION` + URL checkout.
2. `merchant_order_id` unik per merchant. Order yang sama tidak bisa dibuat dua intent aktif.
3. User membuka checkout dan mengonfirmasi (idempoten) → journal DR Wallet user / CR Wallet merchant → intent `SUCCEEDED` + event outbox `payment.succeeded`.
4. Intent kedaluwarsa (misalnya 30 menit) → `EXPIRED`. Konfirmasi setelah kedaluwarsa → 422.
5. Konfirmasi paralel dari dua tab → tepat satu pembayaran (row lock pada intent + idempotency).
6. Merchant bisa membaca status intent (polling) sebagai alternatif webhook.

**Business rules:** BR1–BR3, BR7.
**Dependency:** LF-03, LF-05, LF-08, LF-09.
**Bukti:** E2E checkout di Playwright.

---

### LF-11 — Payout dengan Unknown Outcome (P0)
**Tujuan:** menarik dana ke rekening bank dengan benar walaupun bank tidak menjawab. **Persona:** Rina, Sari.

**User story:** Sebagai user, saya ingin menarik saldo ke rekening bank, dan uang tidak pernah terkirim dua kali atau hilang walaupun sistem gagal di tengah proses.

**Acceptance criteria**
1. `POST /payouts` (idempoten) → journal hold (DR Wallet / CR Payout In-Transit) + Payout `PENDING` + job, dalam satu transaksi.
2. Worker mencatat `SUBMITTED` (commit) **sebelum** memanggil bank, lalu submit dengan idempotency key = `payout_id`. Kalau crash setelah commit ini, worker berikutnya tahu bahwa request *mungkin* sudah terkirim.
3. Sukses → `SUCCEEDED` + journal settlement (DR In-Transit / CR Bank Clearing).
4. Gagal definitif → `FAILED` + journal reversal (dana kembali).
5. Timeout / 5xx / koneksi putus → `UNKNOWN`. **Tidak ada resubmit buta.** Status query berjalan dengan backoff (ADR-L06).
6. Worker crash setelah submit tetapi sebelum mencatat hasil → saat job diambil ulang, worker memanggil **status query** dulu, bukan submit ulang.
7. Payout `UNKNOWN` melewati batas umur → masuk antrean investigasi ops + metrik/alert.

**Business rules:** BR6, BR7.
**Dependency:** LF-03, LF-05, LF-06, LF-08.
**Bukti:** **Demo C**; test failure scenario #4, #5, #8, #9.

---

### LF-12 — Audit Log (P0)
**Tujuan:** menjawab "siapa melakukan apa, kapan, dari mana" untuk aksi yang tidak tercakup ledger.

**Acceptance criteria**
1. Dicatat: login sukses/gagal, rotasi/revoke API key, perubahan status manual oleh ops, resolusi recon, retry job manual, reversal manual.
2. Field: actor (user/merchant/system/ops), action, target, before/after (ringkas, tanpa data sensitif), request_id, IP, timestamp.
3. Append-only (sama seperti ledger).
4. Aksi audit yang terkait perubahan bisnis ditulis di transaksi yang sama dengan perubahannya.

**Dependency:** skeleton.

---

### LF-13 — Health, Structured Log, Correlation ID (P0)
**Acceptance criteria**
1. `GET /healthz` (liveness: proses hidup) dan `GET /readyz` (readiness: DB terjangkau, migrasi up-to-date).
2. Log JSON dengan `request_id`, `correlation_id` (diteruskan ke job dan webhook), `user_id`/`merchant_id`, `transaction_id` jika ada.
3. Request ID dikembalikan di header response dan di body error RFC 9457.
4. Tidak ada token, password, atau API key di log (redaction diuji).

Detail lengkap di desain observability & testing (Observability).

---

### LF-14 — UI Minimal (P0)
**Tujuan:** demo ke HR tanpa curl. **Persona:** Rina, Budi (simulasi), HR yang melihat demo.

**Halaman:** login/register · dashboard wallet (saldo, tombol top-up/transfer/payout, riwayat) · form transfer · checkout merchant · halaman mock provider (bayar / gagal) · halaman "demo toolbar" (memicu skenario failure, hanya di mode demo).

**Acceptance criteria:** responsif di mobile; tombol aksi uang di-disable selama request berjalan, **tetapi** idempotency tetap menjadi jaminan utama, bukan UI; error ditampilkan dari `code` RFC 9457.

---

### LF-15 — Outbound Webhook ke Merchant (P1)
**User story:** Sebagai merchant, saya ingin menerima notifikasi status pembayaran yang andal dan terverifikasi.

**Acceptance criteria**
1. Event `payment.succeeded`, `payment.refunded`, `payment.expired` dikirim dari outbox, dengan payload + header signature `t=…,v1=…`.
2. Respons non-2xx atau timeout (5 detik) → retry dengan backoff (misalnya 1m, 5m, 30m, 2j, 6j, 24j) → `dead`.
3. Setiap delivery attempt tercatat (status code, durasi, error) dan bisa dilihat di ops console.
4. Event ID stabil antar retry agar merchant bisa dedupe.
5. Replay manual dari ops console.

**Dependency:** LF-08, LF-09, LF-10. **Bukti:** test dengan endpoint merchant palsu yang gagal N kali.

---

### LF-16 — Refund (P1)
**Acceptance criteria**
1. Merchant `POST /payments/{id}/refunds {amount}` (idempoten) → journal DR Wallet merchant / CR Wallet user.
2. Σ refund ≤ amount pembayaran. Refund paralel yang secara total melebihi → tepat yang muat yang berhasil (row lock pada payment).
3. Saldo merchant tidak cukup → 422 (tidak ada saldo negatif merchant di MVP).
4. Hanya pembayaran `SUCCEEDED` yang bisa di-refund. Status payment menjadi `PARTIALLY_REFUNDED` atau `REFUNDED`.

**Business rules:** BR5. **Bukti:** concurrency test refund paralel.

---

### LF-17 — Reconciliation (P1)
**User story:** Sebagai finance, saya ingin membandingkan catatan internal dengan laporan settlement provider setiap hari, agar selisih ketahuan sebelum menjadi kerugian.

**Acceptance criteria**
1. Job harian menarik settlement report (CSV) dari mock provider untuk tanggal D. Upload manual juga didukung. File di-hash agar tidak diproses dua kali.
2. Matching berdasarkan provider reference: `MATCHED`, `MISSING_INTERNAL`, `MISSING_EXTERNAL`, `AMOUNT_MISMATCH`, `STATUS_MISMATCH`.
3. Hasil per run tersimpan (jumlah per kategori, durasi). Mismatch masuk antrean investigasi.
4. Recon run idempoten: menjalankan ulang untuk tanggal yang sama menghasilkan hasil yang sama (bukan duplikat).
5. Transaksi di dekat batas hari (cut-off) ditangani dengan toleransi window (diputuskan di desain domain).

**Dependency:** LF-06, LF-07, LF-11. **Bukti:** **Demo D**.

---

### LF-18 — Ops Console (P1)
**Persona:** Sari, on-call engineer.

**Acceptance criteria**
1. Role `OPS` (dibuat lewat seed). Semua endpoint ops terpisah di `/v1/ops/*`.
2. Read: cari transaksi berdasarkan ID/reference, lihat journal + timeline status + log korelasi, lihat payout `UNKNOWN`, job `dead`, dan mismatch recon.
3. Aksi (semuanya wajib alasan dan ter-audit): resolve mismatch, tandai payout sukses/gagal **berdasarkan bukti dari provider** (memicu journal yang benar, bukan edit saldo), retry job `dead`, replay webhook.
4. **Tidak ada** aksi "edit saldo". Koreksi selalu lewat journal/reversal.

**Dependency:** LF-11, LF-15, LF-17.

---

### LF-19 — Business-Invariant Metrics (P1)
**Acceptance criteria**
1. Metrik minimal: `ledger_balance_drift_total`, `ledger_unbalanced_journal_total` (harus selalu 0), `payout_unknown_total` + umur tertua, `outbox_lag_seconds`, `job_dead_total`, `webhook_delivery_failure_total`, `recon_mismatch_total`, `idempotency_replay_total`, serta latency dan error rate per endpoint.
2. Dashboard Grafana (di-provision dari file) dan aturan alert sederhana.

Detail di desain observability & testing.

---

## 3. Traceability: fitur → demo → failure scenario

| Demo / Scenario | Fitur |
|---|---|
| Demo A: Idempotency | LF-04, LF-05 |
| Demo B: Concurrency | LF-03, LF-04 |
| Demo C: Failure recovery payout | LF-06, LF-08, LF-11 |
| Demo D: Reconciliation | LF-06, LF-17, LF-18 |
| Duplicate request | LF-05 |
| Concurrent payment | LF-04, LF-10, LF-16 |
| Webhook duplicate / terlambat | LF-07 (ADR-L08) |
| Provider timeout | LF-11 (ADR-L06) |
| Worker / server crash | LF-08, LF-11 |
| Partial processing | LF-05 (recovery point), LF-08 |

> 10 failure scenario lengkap (Request → Failure → Detection → Handling → Recovery → Final State) dikerjakan di **Desain domain**.
