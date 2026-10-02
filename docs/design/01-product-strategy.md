# Step 1.1 — LedgerPay: Digital Wallet & Mini Payment Platform

> *"A mini payment platform with a double-entry ledger where financial transactions remain consistent even when requests are duplicated, concurrent, or interrupted by failures."*

---

## 1. Product Vision

**Setiap rupiah yang masuk, keluar, atau berpindah tercatat tepat satu kali. Saldo selalu bisa dijelaskan dari ledger, dan sistem tetap benar walaupun jaringan, provider, atau server-nya sendiri gagal.**

LedgerPay adalah *closed-loop wallet* sederhana. User mengisi saldo (top-up) lewat payment provider, mentransfer ke user lain, membayar merchant, dan menarik dana ke rekening bank. Merchant menerima pembayaran lewat API dan webhook. Tim finance merekonsiliasi catatan internal dengan laporan provider.

Yang **bukan** visinya: bersaing fitur dengan DANA/OVO. Fiturnya sengaja sedikit. Kedalamannya ada pada **jaminan** yang diberikan setiap fitur.

### Satu kalimat untuk HR
> "Aplikasi dompet digital kecil yang dirancang supaya uang tidak pernah hilang atau terhitung dua kali, walaupun ada error, klik ganda, atau server mati."

### Satu kalimat untuk technical interviewer
> "Modular monolith di Go + PostgreSQL. Double-entry ledger append-only, idempotency yang persisten dan transaksional dengan efek finansialnya, transactional outbox untuk side effect, state machine untuk payout dengan *unknown outcome*, dan reconciliation terhadap provider."

---

## 2. Target Users & Personas

| Persona | Siapa | Tujuan | Pain point yang relevan |
|---|---|---|---|
| **Rina** (end user) | Karyawan 25 th, top-up wallet untuk bayar online dan kirim uang ke teman | Transaksi cepat, saldo selalu benar | "Saya klik bayar, loading lama, saya klik lagi. Apakah saya terdebet dua kali?" |
| **Budi** (merchant / developer) | Pemilik toko online kecil yang mengintegrasikan pembayaran lewat API | Menerima pembayaran dan notifikasi andal, bisa refund | "Webhook kadang tidak sampai, kadang datang dua kali. Pesanan saya jadi dobel atau tidak terkonfirmasi." |
| **Sari** (finance / ops) | Staf finance di perusahaan wallet | Memastikan catatan internal cocok dengan uang di bank/provider; menangani transaksi yang menggantung | "Ada payout yang statusnya pending 3 hari. Uangnya sudah keluar dari bank atau belum?" |
| **On-call engineer** (persona internal) | Engineer yang menjaga sistem | Tahu dengan cepat kalau ada yang salah, dan bisa memulihkan tanpa mengedit data secara manual | "Saldo user tidak cocok. Dari mana saya mulai menyelidiki?" |

> Persona *on-call engineer* sengaja dimasukkan. Persona inilah yang membenarkan adanya observability, audit, dan tooling recovery sebagai **fitur produk**, bukan tambahan.

---

## 3. Business Problem

Memindahkan uang antar pihak terlihat seperti `balance = balance - x`. Kenyataannya, masalah bisnisnya adalah:

1. **Kepercayaan.** User dan merchant hanya mau memakai wallet kalau yakin saldonya tidak pernah salah. Satu kasus double-debit yang viral bisa mematikan produk.
2. **Sistem yang terlibat gagal secara independen.** Aplikasi, database, payment provider, bank, dan jaringan masing-masing bisa timeout atau crash. Kegagalan paling berbahaya adalah **"tidak tahu"**: request sudah terkirim ke bank, tetapi jawabannya tidak pernah kembali.
3. **Kewajiban hukum dan akuntansi.** Saldo user adalah **liabilitas** perusahaan (uang milik user yang dititipkan). Perusahaan harus bisa membuktikan total liabilitas cocok dengan dana yang benar-benar ada di rekening penampung. Itu sebabnya dibutuhkan double-entry dan reconciliation.
4. **Audit.** Setiap perubahan saldo harus bisa ditelusuri: siapa, kapan, kenapa, dan dari request mana. Ledger tidak boleh diedit. Koreksi dilakukan lewat transaksi pembalik (reversal).

**Inti problem untuk portfolio:** *bukan* "bagaimana memindahkan angka", tetapi **"bagaimana membuktikan setiap perpindahan angka itu benar, terjadi tepat satu kali, dan bisa dijelaskan, padahal komponen-komponen sistemnya tidak andal."**

---

## 4. Core Workflows

Lima workflow inti. Setiap workflow sengaja dipilih karena memunculkan tantangan engineering yang **berbeda**.

### W1 — Top-up (masuk dari luar, asynchronous, via webhook)
> **Alur final:** webhook disimpan ke inbox, lalu diproses worker dengan verify-by-fetch ke provider sebelum kredit (ADR-L08 + amandemen Step 6). Diagram di bawah adalah versi awal Step 1.
```text
Rina → POST /topups (Idempotency-Key)
     → LedgerPay buat TopUp[PENDING] + panggil Mock Provider → dapat payment URL
Rina → bayar di halaman Mock Provider
Mock Provider → POST /webhooks/provider (signed, bisa duplikat, bisa terlambat, bisa tidak berurutan)
     → verifikasi signature → dedupe event → TopUp[SUCCEEDED]
     → Journal: DR Provider Clearing (asset) / CR Wallet Rina (liability)
     → Outbox: notifikasi
```
**Tantangan:** webhook duplikat atau datang terlambat, pemalsuan webhook, webhook datang sebelum response API kembali.

### W2 — P2P Transfer (internal, synchronous, satu DB transaction)
```text
Rina → POST /transfers {to: Andi, amount} (Idempotency-Key)
     → 1 DB transaction: lock wallet (urutan deterministik) → cek saldo → journal DR Rina / CR Andi
     → simpan response idempotency → commit
```
**Tantangan:** double-spend akibat request paralel, deadlock (A→B dan B→A bersamaan), duplicate submit.

### W3 — Merchant Payment + Refund
```text
Budi (server) → POST /payment-intents {amount, order_id} (API key, Idempotency-Key)
Rina → konfirmasi bayar → Journal DR Rina / CR Merchant Budi
     → Outbox → webhook ke Budi (at-least-once, signed, retry + backoff)
Budi → POST /payments/{id}/refunds {amount}  (partial / full, total refund ≤ amount)
```
**Tantangan:** outbound webhook andal (dual-write problem), refund melebihi nilai pembayaran akibat refund paralel, merchant hanya boleh mengakses pembayarannya sendiri (BOLA).

### W4 — Payout / Withdraw (keluar ke luar, *unknown outcome*)
```text
Rina → POST /payouts {bank_account, amount}
     → Journal: DR Wallet Rina / CR Payout In-Transit   (dana di-hold dulu)
     → Payout[PENDING] + outbox job
Worker → Payout[SUBMITTED] (commit dulu) → panggil Mock Bank (bisa sukses, gagal, atau TIMEOUT)
     → SUCCESS : Payout[SUCCEEDED], Journal DR In-Transit / CR Bank Clearing
     → FAILED  : Payout[FAILED],    Journal reversal (dana kembali ke Rina)
     → TIMEOUT : Payout[UNKNOWN] → JANGAN retry buta → status query / reconciliation
```
**Tantangan:** worker crash setelah memanggil bank tetapi sebelum mencatat hasilnya, retry yang mengakibatkan uang terkirim dua kali, idempotency ke arah provider (LedgerPay sebagai *client*).

### W5 — Reconciliation (harian, ops)
```text
Mock Provider/Bank → menghasilkan settlement report (CSV) per hari
Sari → upload / worker tarik report → matching dengan transaksi internal
     → MATCHED | MISSING_INTERNAL | MISSING_EXTERNAL | AMOUNT_MISMATCH | STATUS_MISMATCH
     → mismatch → antrean investigasi → resolusi (dengan audit trail)
```
**Tantangan:** membuktikan sistem internal benar, menangkap kasus yang lolos dari semua mekanisme lain (contoh: payout UNKNOWN yang ternyata sukses di bank).

### Model akun (ringkas, detail di Step 3)
Dari sudut pandang **perusahaan**:

| Akun | Tipe | Keterangan |
|---|---|---|
| Wallet user / merchant | Liability | Uang milik user yang dititipkan |
| Provider Clearing | Asset | Dana top-up yang akan di-settle oleh provider |
| Payout In-Transit | Liability (suspense) | Dana yang sudah di-hold tetapi belum pasti keluar |
| Bank Clearing | Asset | Rekening penampung di bank |
| Fee Revenue | Revenue | Biaya transaksi (kalau ada) |

Invariant global: **Σ debit = Σ kredit untuk setiap journal**, sehingga keseluruhan ledger selalu seimbang.

---

## 5. Core Business Rules (awal; diformalkan menjadi invariant di Step 3)

| # | Rule |
|---|---|
| BR1 | Setiap journal entry seimbang: Σ debit = Σ kredit, minimal 2 baris |
| BR2 | Saldo wallet user tidak boleh negatif (akun sistem boleh, sesuai tipenya) |
| BR3 | Satu idempotency key (per client + endpoint) menghasilkan maksimal satu efek finansial. Key yang sama dengan payload berbeda **ditolak** |
| BR4 | Ledger append-only. Journal yang sudah posted tidak bisa diubah atau dihapus. Koreksi lewat reversal yang mereferensikan journal asal |
| BR5 | Total refund ≤ nilai pembayaran yang berhasil |
| BR6 | Payout UNKNOWN tidak boleh di-retry ke provider tanpa status query / idempotency key provider yang sama |
| BR7 | Status transaksi hanya bergerak lewat transisi yang sah (state machine). Status final tidak bisa kembali |
| BR8 | Nominal disimpan sebagai integer minor unit (IDR, tanpa float). Single currency di MVP |
| BR9 | Batas saldo dan batas transaksi per user bisa dikonfigurasi (terinspirasi aturan e-money BI; **angka diverifikasi sebelum ditulis di README**) |
| BR10 | Setiap event webhook diproses efeknya maksimal satu kali (dedupe berdasarkan provider event ID) |

---

## 6. MVP Scope (dikelompokkan sesuai Phase 0)

### 6.1 MVP: minimum agar produk benar-benar bisa dipakai
| Fitur | Alasan masuk MVP |
|---|---|
| Register / login (email + password, JWT access + refresh) | Tanpa identitas, tidak ada wallet |
| Wallet IDR per user + saldo + riwayat transaksi | Produk inti |
| **Double-entry ledger** (journal + entries) | Fondasi semua fitur keuangan; tanpa ini, fitur lain hanya CRUD |
| P2P transfer | Workflow internal paling sederhana; tempat membuktikan concurrency |
| Top-up via Mock Provider + webhook | Workflow masuk dari luar; tanpa top-up tidak ada uang di sistem |
| Merchant: API key, payment intent, pembayaran oleh user | Membuat "payment platform", bukan sekadar wallet |
| Idempotency pada semua endpoint yang mengubah uang | Tanpa ini, produk keuangan tidak layak dipakai |
| UI minimal: wallet user + halaman checkout merchant + mock provider page | Dibutuhkan untuk demo ke HR |

### 6.2 Production Signal: technical depth tinggi
| Fitur | Sinyal yang ditunjukkan |
|---|---|
| Pessimistic locking dengan lock ordering + concurrency test | Paham race condition dan deadlock, bukan sekadar teori |
| Transactional outbox + worker (Postgres, `SKIP LOCKED`) | Paham dual-write problem dan at-least-once delivery |
| Outbound webhook ke merchant (signed HMAC, retry + backoff, DLQ) | Paham integrasi antar sistem dari sisi provider |
| Payout dengan state `UNKNOWN` + status query | Sinyal **senior**: memahami kegagalan "tidak tahu" |
| Reconciliation + antrean mismatch | Paham bahwa sistem harus bisa membuktikan dirinya benar |
| Mock Provider/Bank dengan **failure injection** (timeout, duplicate, delay, out-of-order) | Bisa menguji dan mendemokan failure secara deterministik |
| Refund (partial) dengan guard terhadap refund paralel | Invariant lintas baris |
| Audit log + correlation ID dari request sampai journal | Traceability end-to-end |
| Ops console (read-only + aksi resolusi mismatch yang ter-audit) | Persona Sari dan on-call engineer |
| Business-invariant metrics (contoh: `ledger_imbalance_total` = 0) | Observability yang bermakna bisnis, bukan sekadar CPU |

### 6.3 Nice to Have
| Fitur | Kenapa tidak di MVP |
|---|---|
| Fee transaksi (journal 3 kaki) | Menarik, tetapi menambah kasus tanpa sinyal baru yang signifikan |
| Rate limiting per user / per API key (Redis atau Postgres) | Penting di production; masuk P1/P2, dan implementasinya bisa sederhana. **(Final: P1, in-memory per instance, lihat Step 2 LF-20 / Step 6 C-07)** |
| Notifikasi email (via outbox) | Outbox-nya sudah membuktikan pola. Email hanya consumer tambahan |
| PIN transaksi / step-up auth | Bagus untuk diskusi security, bisa ditambah belakangan |
| Risk rules sederhana (velocity limit) | Nilai diskusinya ada, implementasinya kecil, tetapi bukan inti |
| Balance snapshot / checkpoint untuk mempercepat perhitungan saldo | Hanya relevan setelah load test menunjukkan bottleneck |

### 6.4 Out of Scope (sengaja tidak dibuat)
Lihat §9.

---

## 7. High-Impact Features (urut berdasarkan nilai engineering)

| # | Fitur | Kenapa high-impact | Yang dibuktikan |
|---|---|---|---|
| 1 | **Double-entry ledger append-only** | Fondasi semua invariant; saldo = proyeksi dari ledger | Data modelling keuangan, immutability, auditability |
| 2 | **Idempotency yang transaksional** | Key, payload hash, dan response disimpan **dalam transaksi yang sama** dengan efek finansial. Request in-flight dengan key sama ditangani (409 / tunggu) | Memahami bahwa idempotency di Redis terpisah dari DB transaction tidak menjamin apa-apa |
| 3 | **Concurrency-safe balance** | `SELECT … FOR UPDATE` dengan urutan lock deterministik + constraint DB sebagai pertahanan terakhir | Isolation level, locking, deadlock avoidance, defense in depth |
| 4 | **Transactional outbox** | Side effect (webhook, notifikasi, job payout) tidak hilang dan tidak terkirim tanpa commit | Dual-write, at-least-once, idempotent consumer |
| 5 | **Payout unknown-outcome** | Kegagalan yang paling sering salah ditangani di sistem nyata | Distributed systems thinking, state machine, recovery |
| 6 | **Reconciliation** | Jaring pengaman terakhir; bisa didemokan secara visual | Berpikir seperti operator / finance, bukan hanya developer |
| 7 | **Failure-injectable mock provider** | Membuat semua skenario failure bisa di-reproduce dan diuji otomatis | Testability by design |

---

## 8. Killer Demo Features

Tiga demo inti (sesuai master prompt) ditambah satu bonus. Masing-masing harus punya **bukti yang terlihat**, bukan klaim.

### Demo A — Idempotency: "klik dua kali, terdebet sekali"
```text
1. Kirim POST /transfers dengan Idempotency-Key K, amount 50.000
2. Kirim ulang request yang identik dengan K
3. Kirim K dengan amount 60.000
4. Kirim 2 request K' bersamaan (in-flight)

Expected:
#1 → 201 SUCCESS (transfer_id T1)
#2 → 201 SAME RESULT (transfer_id T1, header Idempotent-Replayed: true)
#3 → 422 IDEMPOTENCY_KEY_REUSED (payload berbeda)
#4 → satu 201, satu 409 REQUEST_IN_PROGRESS (atau menunggu lalu replay)
Saldo → berubah satu kali
Ledger → satu journal untuk K
```
**Bukti:** query ledger di layar, header response, log dengan correlation ID yang sama.

### Demo B — Concurrency: "100 request menyerang satu saldo"
```text
Saldo awal Rina: 100.000
k6 / script: 100 transfer paralel × 10.000 dari Rina ke berbagai penerima

Expected:
Berhasil  → tepat 10
Ditolak   → tepat 90 (INSUFFICIENT_BALANCE)
Saldo     → 0, tidak pernah negatif
Ledger    → Σ debit = Σ kredit; Σ semua saldo tidak berubah
```
**Bonus pembanding:** test yang sama dijalankan terhadap implementasi naif (read-check-write tanpa lock). Implementasi naif ini **hanya ada di test suite**, tidak ada di build aplikasi. Hasilnya saldo negatif atau lost update, dan perbedaan inilah yang membuat demo kuat.

### Demo C — Failure Recovery: "worker mati di tengah payout"
```text
1. Mock Bank diset: "terima request, proses sukses, tapi jangan balas (timeout)"
2. Rina payout 200.000 → dana di-hold → worker memanggil bank → timeout
3. Payout → UNKNOWN. Worker di-kill (docker kill) saat itu juga
4. Worker dinyalakan lagi → job diambil ulang dari outbox
5. Worker TIDAK mengirim ulang secara buta. Ia memanggil status query dengan idempotency key yang sama → bank: "sudah sukses"
6. Payout → SUCCEEDED, journal settlement dicatat satu kali

Expected: uang terkirim ke bank 1×, saldo Rina terpotong 1×, tidak ada journal dobel.
```
**Bukti:** log mock bank (1 transfer), timeline status payout, metrik `payout_unknown_total` naik lalu kembali normal.

### Demo D (bonus) — Reconciliation menangkap yang lolos
Mock Bank mencatat payout sukses yang di sistem internal masih `UNKNOWN` (status query dimatikan). Reconciliation besok paginya menandai `STATUS_MISMATCH`. Sari menyelesaikannya dari ops console, dan resolusinya tercatat di audit log.

---

## 9. Production Engineering Challenges

| # | Challenge | Kenapa sulit | Arah solusi (detail di Step 3/7) |
|---|---|---|---|
| C1 | **Double-spend / lost update** | Dua transaksi membaca saldo yang sama sebelum salah satu menulis | Row lock (`FOR UPDATE`) pada wallet + `CHECK (balance >= 0)` sebagai jaring terakhir |
| C2 | **Deadlock** | A→B dan B→A bersamaan mengunci dalam urutan berlawanan | Lock ordering berdasarkan ID; retry terbatas untuk `40P01` |
| C3 | **Semantik idempotency** | Scope key, fingerprint payload, request in-flight, kegagalan sebelum/sesudah commit, retensi key | Tabel idempotency dalam transaksi yang sama; status `IN_PROGRESS`/`COMPLETED`; TTL |
| C4 | **Dual-write** (DB + kirim pesan) | Commit DB sukses tetapi publish gagal, atau sebaliknya | Transactional outbox + relay worker |
| C5 | **Unknown outcome** dari provider | Timeout ≠ gagal | State `UNKNOWN`, status query, idempotency key ke provider, reconciliation |
| C6 | **Webhook masuk** tidak andal dan tidak tepercaya | Duplikat, terlambat, tidak berurutan, palsu, replay | Verifikasi HMAC + timestamp window; dedupe event ID; transisi state yang monoton |
| C7 | **Webhook keluar** ke merchant | Endpoint merchant down atau lambat | Retry dengan exponential backoff + jitter, DLQ, replay manual, signature |
| C8 | **Hot account** | Akun sistem (Provider Clearing) di-update oleh hampir semua transaksi sehingga terjadi lock contention | Akun sistem tidak menyimpan saldo ter-materialisasi (saldo dihitung dari entries / agregasi async), atau dipecah ke beberapa sub-akun. **Dibuktikan dulu dengan load test** sebelum dioptimasi |
| C9 | **Representasi uang** | Float tidak presisi; pembulatan fee | `BIGINT` minor unit; aturan pembulatan eksplisit |
| C10 | **Saldo materialized vs derived** | Saldo dihitung dari ledger (benar tetapi lambat) atau disimpan (cepat tetapi bisa drift) | Simpan saldo + update dalam transaksi yang sama dengan entries; job verifikasi `saldo == Σ entries` sebagai metrik invariant |
| C11 | **Auditability** | Harus bisa menjawab "kenapa saldo ini segini?" | Journal mereferensikan transaksi bisnis + request/correlation ID; audit log untuk aksi ops |

---

## 10. Out-of-Scope Features

| Fitur | Alasan dikeluarkan |
|---|---|
| Integrasi payment gateway sungguhan (Midtrans/Xendit) | Butuh akun bisnis dan sandbox yang tidak deterministik. Mock provider dengan failure injection **lebih bernilai** untuk demo dan testing. Kontrak mock dibuat mirip PG nyata |
| KYC / verifikasi identitas | Domain berbeda (dokumen, OCR, compliance); tidak menambah sinyal ledger |
| Multi-currency / FX | Menggandakan kompleksitas pembulatan dan akun; sinyalnya kecil dibanding biayanya |
| Kartu kredit/debit, tokenisasi, PCI-DSS | Di luar kemampuan dan relevansi portfolio |
| Chargeback / dispute penuh | Workflow panjang; refund sudah mewakili pola "uang kembali" |
| Microservices (ledger service, wallet service, …) | Masalah utamanya konsistensi. Memecah service justru memaksa saga/2PC tanpa alasan bisnis. **Satu-satunya proses terpisah adalah mock provider**, karena memang merepresentasikan sistem eksternal |
| Kafka / RabbitMQ | Postgres outbox + `SKIP LOCKED` cukup untuk volume ini dan menjaga transaksionalitas. Broker eksternal menambah infrastruktur tanpa masalah nyata yang diselesaikan |
| Kubernetes | Docker Compose + satu VM cukup. K8s hanya dibahas sebagai arsitektur V3 di Step 2 |
| Mobile app | Web responsif cukup untuk demo |
| Fraud detection berbasis ML | Di luar fokus; risk rules sederhana sudah memadai sebagai nice-to-have |
| Bunga, pinjaman, paylater, cashback/promo engine | Domain produk keuangan lain, bukan inti payment correctness |
| Event sourcing penuh / CQRS | Ledger append-only sudah memberi properti utamanya (immutability, audit) tanpa kompleksitas infrastruktur event store |

---

## 11. Initial Architecture Direction

> Ini **arah**, belum keputusan final. ADR lengkap dan versi V1→V3 dikerjakan di Step 2.

```text
                  ┌──────────────────────────────────────────┐
  Browser (Vue) ─▶│  ledgerpay api   (Go, modular monolith)  │
  Merchant API  ─▶│  modules: auth, wallet, ledger, transfer,│
                  │  payment, payout, webhook, recon, audit  │
                  └───────────────┬──────────────────────────┘
                                  │ 1 DB transaction:
                                  │ business row + journal + idempotency + outbox
                           ┌──────▼──────┐
                           │ PostgreSQL  │◀──── ledgerpay worker (Go, binary yang sama)
                           └─────────────┘       - outbox relay (SKIP LOCKED)
                                                 - payout executor
                                                 - merchant webhook dispatcher
                                                 - recon job
                                  ▲                         │
          webhook (signed) ───────┘                         ▼
                           ┌──────────────────────────────────┐
                           │ mock-provider (proses terpisah)  │
                           │ PG + Bank, failure injection     │
                           └──────────────────────────────────┘
```

| Aspek | Arah awal | Alasan singkat |
|---|---|---|
| Gaya arsitektur | **Modular monolith**: satu binary dengan subcommand `api` / `worker` / `migrate` | Konsistensi butuh satu DB transaction; batas modul tetap dijaga agar bisa dipecah kalau perlu |
| Database | **PostgreSQL** saja sebagai source of truth | ACID, row lock, constraint, `SKIP LOCKED` sebagai queue |
| Queue | **Postgres outbox + worker**; tanpa broker | Transaksional dengan efek bisnis; satu komponen infrastruktur lebih sedikit |
| Cache / Redis | **Belum dipakai di V1** (berubah dari v1) | Idempotency harus transaksional sehingga harus di Postgres. Redis baru dipertimbangkan untuk rate limiting, setelah ada kebutuhan |
| API | REST + JSON, `Idempotency-Key` header, error RFC 9457 | Standar industri payment (Stripe-like); mudah didemokan dengan curl |
| Auth | JWT access (pendek) + refresh token rotasi (user); API key ber-hash (merchant) | Dua jenis client, dua mekanisme |
| Proses eksternal | `mock-provider` sebagai service terpisah | Merepresentasikan batas jaringan nyata (timeout, signature) |
| Deployment | Docker Compose, satu VM murah | Cukup untuk demo; V2/V3 dibahas di Step 2 |

---

## 12. Key Technical Risks

| # | Risiko | Dampak | Kemungkinan | Mitigasi |
|---|---|---|---|---|
| R1 | **Scope creep**: domain payment tidak ada habisnya | Project tidak selesai | Tinggi | Daftar Out of Scope di atas bersifat mengikat; fitur baru wajib lolos gerbang di `00-overview.md` §4 |
| R2 | **Bug correctness di ledger** yang baru ketahuan saat demo | Kredibilitas runtuh | Sedang | Invariant check otomatis (test + metrik), property-based test, concurrency test di CI |
| R3 | **Demo live yang rapuh** (race condition tidak muncul / muncul tidak konsisten) | Demo gagal di depan interviewer | Sedang | Demo berbasis script deterministik + rekaman video cadangan + mode failure injection yang eksplisit |
| R4 | **Salah memahami konsep akuntansi** (debit/kredit untuk liability) | Diskusi interview terlihat dangkal | Sedang | Model akun ditulis dan divalidasi di Step 3 sebelum coding; jelaskan dari sudut pandang perusahaan |
| R5 | **Over-engineering** (CQRS, event sourcing, Kafka) karena terlihat keren | Waktu habis, sulit dijelaskan | Sedang | ADR wajib untuk setiap komponen infrastruktur baru |
| R6 | **Hot account** baru ketahuan di akhir | Performa buruk saat load test | Rendah–Sedang | Desain akun sistem tanpa saldo materialized dari awal; load test di sprint performa |
| R7 | **Klaim regulasi yang salah** (batas saldo e-money) | Terlihat tidak teliti | Rendah | Semua angka regulasi bisa dikonfigurasi dan ditandai "untuk diverifikasi" |
| R8 | Terlihat seperti tutorial "wallet API" | Tidak menonjol | Sedang | README dan demo dibuka dengan failure scenario, bukan daftar fitur |

---

## 13. Success Metrics

### Product (seandainya produk nyata)
- Persentase transaksi yang selesai tanpa intervensi manual.
- Waktu rata-rata payout `UNKNOWN` sampai resolved.
- Jumlah mismatch reconciliation per 10.000 transaksi.

### Engineering (dapat diverifikasi di portfolio)
| Metrik | Target |
|---|---|
| Pelanggaran invariant (saldo negatif, journal tidak seimbang, efek dobel) di concurrency + chaos test | **0** |
| Concurrency test (N transfer paralel ke saldo yang sama) berjalan di CI | Selalu hijau |
| Semua 10 failure scenario (Step 3) punya test otomatis | 10/10 |
| p95 latency transfer di load test lokal | Dicatat sebagai baseline. **Bukan klaim kapasitas production** |
| Demo A–C bisa dijalankan dari nol (`make demo`) | ≤ 10 menit, deterministik |
