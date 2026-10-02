# Step 2.2 — LedgerPay: System Architecture V1 → V2 → V3

> Prinsip: **mulai dari arsitektur paling sederhana yang memenuhi requirement**, lalu ubah hanya ketika ada *pemicu terukur*. Setiap transisi menjawab: *apa yang berubah, kenapa, dan kapan.*
> Keputusan rinci: lihat [ADR register](00-index.md#adr-register).

---

## 1. Requirement yang menggerakkan arsitektur

| Requirement | Konsekuensi arsitektural |
|---|---|
| Efek finansial + idempotency + outbox harus atomik | Satu database relasional, satu transaksi. Tidak ada pemecahan service di jalur uang |
| Pihak eksternal tidak andal (timeout, duplikat) | Panggilan eksternal **di luar** DB transaction, lewat worker; state machine yang eksplisit |
| Bisa diaudit | Ledger append-only, correlation ID end-to-end |
| Bisa didemokan dan diuji secara deterministik | Mock provider sebagai proses terpisah dengan failure injection |
| Dikerjakan solo, biaya rendah | Satu binary, satu VM, satu database |

---

## 2. Version 1 — Portfolio MVP (yang akan dibangun)

### 2.1 Context diagram
```text
   ┌────────┐     ┌──────────┐       ┌─────────┐
   │  Rina  │     │  Budi    │       │  Sari   │
   │ (user) │     │(merchant)│       │ (ops)   │
   └───┬────┘     └────┬─────┘       └────┬────┘
       │ browser       │ server-to-server │ browser
       ▼               ▼                  ▼
   ┌─────────────────────────────────────────────┐        ┌────────────────────┐
   │                 LedgerPay                   │◀──────▶│   mock-provider    │
   │   (wallet, ledger, payment, payout, recon)  │ HTTPS  │ (PG + Bank simul.) │
   └─────────────────────────────────────────────┘ webhook└────────────────────┘
                       │ webhook (signed)
                       ▼
                ┌──────────────┐
                │ Merchant     │
                │ endpoint     │
                └──────────────┘
```

### 2.2 Container diagram
```text
                         ┌──────────────────────────────┐
  Browser ──HTTPS──▶     │ Caddy (reverse proxy, TLS)   │
                         └──────┬───────────────┬───────┘
                                │ /             │ /api/*
                                ▼               ▼
                    ┌──────────────────┐  ┌───────────────────────────────┐
                    │ web (Vue SPA,    │  │ ledgerpay api (Go)            │
                    │ static files)    │  │ - HTTP handlers               │
                    └──────────────────┘  │ - sync use cases (transfer,   │
                                          │   payment confirm, payout req)│
                                          └──────────────┬────────────────┘
                                                         │ pgx pool
                                          ┌──────────────▼────────────────┐
                                          │ PostgreSQL                    │
                                          │ ledger · wallet · payment ·   │
                                          │ payout · idempotency · outbox │
                                          │ · jobs · inbox · recon · audit│
                                          └──────────────▲────────────────┘
                                                         │ pgx pool
                                          ┌──────────────┴────────────────┐
                                          │ ledgerpay worker (Go)         │
                                          │ - job runner (SKIP LOCKED)    │
                                          │ - inbox processor (webhook)   │
                                          │ - payout executor + status    │
                                          │ - merchant webhook dispatcher │
                                          │ - schedulers: recon, expiry,  │
                                          │   ledger verification, cleanup│
                                          └──────────────┬────────────────┘
                                                         │ HTTPS
                                          ┌──────────────▼────────────────┐
                                          │ mock-provider (Go, terpisah)  │
                                          │ + storage sendiri             │
                                          └───────────────────────────────┘

  Observability (compose profile "obs"): Prometheus ← /metrics (api, worker), Grafana, log JSON → stdout
```

**Kenapa `api` dan `worker` dipisah sebagai proses (walaupun satu binary):**
- Request HTTP tidak boleh tertahan oleh pekerjaan lambat (panggilan bank, webhook ke merchant).
- Keduanya bisa di-scale dan di-restart secara independen. Demo C (*kill worker*) membutuhkan pemisahan ini.
- Kode tetap satu codebase dan satu deployment artifact, sehingga tidak ada biaya operasional microservices.

### 2.3 Module view (di dalam binary)
```text
internal/
├── platform/        config · db (WithTx, error map) · httpx · auth · jobs · observability · clock · idgen
└── modules/
    ├── identity/    user, credential, refresh token, merchant API key
    ├── ledger/      account, journal, entry, PostJournal(), verification   ← inti, tanpa dependency ke modul lain
    ├── wallet/      wallet = account milik user/merchant, history (read)
    ├── idempotency/ key store + middleware/helper
    ├── transfer/    P2P
    ├── payment/     payment intent, confirm, refund
    ├── topup/       top-up + provider client
    ├── payout/      payout state machine + bank client
    ├── webhook/     inbound inbox + outbound delivery
    ├── recon/       settlement import + matching
    ├── audit/       audit log
    └── ops/         read model + aksi ops (memanggil service modul lain)
```

**Aturan dependency (ditegakkan oleh depguard di CI):**
1. `platform/*` tidak boleh meng-import `modules/*`.
2. `ledger` tidak meng-import modul bisnis apa pun. Modul lain memanggil `ledger.Service`.
3. Modul tidak boleh membaca atau menulis tabel milik modul lain. Interaksi lewat interface service, dengan `pgx.Tx` diteruskan agar tetap dalam satu transaksi.
4. Transaksi dibuka di service layer modul yang menangani use case (misalnya `transfer.Service`), lalu diteruskan ke `ledger`, `idempotency`, dan `jobs`.

### 2.4 Alur kunci V1

**Transfer (sinkron, satu transaksi)**
```text
API ─ BEGIN
     ├─ idempotency.Begin(key, hash)        -- INSERT; duplikat → tunggu/replay
     ├─ SELECT accounts … ORDER BY id FOR NO KEY UPDATE   (Step 7/12)
     ├─ validasi saldo
     ├─ ledger.PostJournal(DR sender, CR receiver)  -- update balance + insert entries
     ├─ transfer row (COMPLETED)
     ├─ outbox: transfer.completed (notifikasi, opsional)
     ├─ idempotency.Complete(response)
     └─ COMMIT  → 201
```

**Payout (asinkron, beberapa transaksi + panggilan eksternal di antaranya)**
```text
API    tx1: idempotency + journal hold + payout PENDING + job "payout.submit" → COMMIT → 202
Worker claim job → tx2: PENDING→SUBMITTED → COMMIT   (dicatat SEBELUM memanggil bank)
       → (tidak ada tx terbuka) → POST bank /payouts (Idempotency-Key = payout_id)
       ├─ 2xx sukses  → tx3: SUBMITTED→SUCCEEDED + journal settle
       ├─ 4xx definit → tx3: FAILED + journal reversal
       └─ timeout/5xx → tx3: UNKNOWN + job "payout.status_check" (backoff)
Crash di titik mana pun → job di-lease ulang → payout berstatus SUBMITTED diperlakukan sebagai UNKNOWN
       → status query ke bank dulu; resubmit (key yang sama) hanya jika bank menjawab NOT_FOUND
         setelah safe window (ADR-L06)
```

**Top-up webhook (inbox)**
```text
mock-provider → POST /webhooks/provider → verify HMAC + ts → INSERT inbox ON CONFLICT DO NOTHING → 200
Worker: claim inbox row → tx: transisi topup (monoton) + journal + outbox → mark processed
```

### 2.5 Deployment V1
```text
1 VM (2 vCPU / 2–4 GB) ── docker compose:
  caddy · web · api · worker · postgres (volume) · mock-provider · [prometheus · grafana]
CI: GitHub Actions → image GHCR (tag SHA) → SSH deploy → migrate → restart → health check
Backup: pg_dump harian ke storage eksternal (detail Step 9)
```

### 2.6 Batas V1 yang diakui (jujur, untuk interview)
| Batas | Dampak | Kenapa diterima di V1 |
|---|---|---|
| Single Postgres, tanpa replica | DB mati = sistem mati; RPO ≈ jarak antar backup | Biaya; fokusnya correctness, bukan HA |
| Satu instance api/worker | Deploy = downtime singkat | Cukup untuk demo; desain sudah stateless sehingga siap di-scale |
| Prometheus + Grafana lokal, tanpa tracing terdistribusi | Investigasi lintas proses mengandalkan correlation ID di log | Proses sedikit; OTel tracing masuk V2 |
| Secrets di env file pada VM | Rotasi manual | Wajar untuk portfolio; V2 memakai secret manager |

---

## 3. Version 2 — Production Readiness

**Pemicu:** produk dipakai user sungguhan dengan uang sungguhan. Ukuran pemicunya: ada SLO (misalnya 99,9%), ada kewajiban audit, atau downtime saat deploy tidak bisa diterima.

```text
            ┌──────── Load Balancer / WAF ────────┐
            ▼                                     ▼
     api (N ≥ 2, stateless)                 api ...
            │                                     │
            ├────── Redis (rate limit terdistribusi, ADR-007) ──────┤
            ▼
   Managed PostgreSQL (primary + standby sinkron/HA, PITR)
            │                         └── read replica → history/ops/reporting
            ▲
     worker pool (N ≥ 2; queue terpisah per jenis job: payout, webhook, recon)
            │
     Provider nyata (PG, bank) via egress terkontrol

  Observability: OpenTelemetry (traces + metrics) → collector → backend terkelola; log terpusat; alert ke on-call
  Secrets: secret manager + rotasi; key webhook/JWT ber-kid
```

| Perubahan | Pemicu | Kenapa |
|---|---|---|
| ≥ 2 instance api di belakang LB, rolling deploy | Downtime saat deploy tidak bisa diterima | API sudah stateless; tinggal diatur readiness gate |
| Managed Postgres HA + PITR | Kehilangan data atau downtime DB tidak bisa diterima | RPO menit/detik; failover otomatis |
| Redis untuk rate limiting | > 1 instance api, sehingga counter in-memory tidak lagi akurat | Shared state yang memang tidak perlu ACID |
| Read replica untuk riwayat/ops | Query riwayat/ops mengganggu latency jalur tulis | Riwayat boleh *slightly stale*; saldo tetap dibaca dari primary |
| Queue terpisah per jenis job + batas konkurensi | Webhook merchant yang lambat menahan payout | Isolasi bulkhead |
| Partisi `journal_entries` per bulan | Tabel > ratusan juta baris; vacuum/index jadi berat | Arsip partisi lama; query aktif tetap cepat |
| OTel tracing | Investigasi lintas api → worker → provider lambat | Trace ID = correlation ID yang sudah ada |
| Secret manager, rotasi kunci, audit akses | Kewajiban compliance | — |
| Provider nyata menggantikan mock | Go-live | Kontrak mock sudah dibuat mirip PG nyata, jadi adapter tinggal diganti (port & adapter) |

**Yang sengaja tetap sama di V2:** modular monolith, Postgres sebagai queue, ledger di DB yang sama dengan wallet. Belum ada pemicu untuk mengubahnya.

---

## 4. Version 3 — Scaling Architecture (hanya didiskusikan, tidak dibangun)

**Pemicu terukur (salah satu):**
- Write throughput ledger mendekati batas satu primary Postgres setelah optimasi (contoh indikatif: ribuan journal/detik berkelanjutan, lock wait dominan di profil).
- Tim tumbuh menjadi beberapa tim yang perlu deploy independen (*Conway's law*).
- Konsumen event bertambah banyak (analytics, fraud, notifikasi, loyalty) sehingga polling outbox per konsumen tidak efisien.
- Kebutuhan multi-region / data residency.

```text
                        API Gateway
          ┌──────────────┼───────────────┬──────────────┐
          ▼              ▼               ▼              ▼
     Payments svc    Payouts svc     Identity svc   Recon svc
          │              │                               │
          └────── Ledger service (sumber kebenaran) ─────┘
                    │  shard by account_id (N shard Postgres)
                    │  transfer lintas shard → saga dengan suspense account
                    ▼
        outbox → CDC (Debezium) → Kafka → fraud · analytics · notifications · data warehouse
```

| Perubahan | Kenapa | Harga yang dibayar |
|---|---|---|
| Ledger jadi service sendiri | Ledger adalah batas konsistensi paling tegas; bisa di-scale dan diaudit terpisah | Panggilan jaringan di jalur uang; idempotency antar service wajib |
| Sharding akun | Satu primary tidak cukup | Transfer lintas shard tidak lagi atomik. Butuh saga: debit shard A ke suspense, lalu kredit shard B, dengan kompensasi |
| Hot account dipecah ke sub-akun | Contention pada akun sistem | Pelaporan harus mengagregasi sub-akun |
| CDC + Kafka | Fan-out ke banyak konsumen, replay histori | Infrastruktur dan operasional berat; semantik at-least-once tetap berlaku |
| Multi-region active-passive | DR dan regulasi | Konsistensi lintas region; failover yang teruji |

> **Poin interview:** batas modul di V1 (ledger tanpa dependency, akses tabel hanya lewat service modul pemiliknya) membuat pemisahan V3 menjadi *ekstraksi*, bukan *rewrite*. Inilah alasan modular monolith dipilih, bukan "monolith biasa" (ADR-001).

---

## 5. Ringkasan evolusi

| Aspek | V1 (dibangun) | V2 (production) | V3 (scale) |
|---|---|---|---|
| Proses | api + worker + mock-provider | api×N + worker pool per queue | Beberapa service + ledger service |
| Database | 1 Postgres | Managed HA + PITR + read replica | Ledger ter-shard |
| Queue | Postgres outbox/job | Postgres, queue per jenis | Outbox → CDC → Kafka |
| Cache/Redis | Tidak ada | Redis untuk rate limit | Redis + cache read model |
| Observability | Log JSON + Prometheus/Grafana | OTel traces, log terpusat, alert | + SLO per service, tracing lintas service |
| Deploy | Compose, 1 VM, ada downtime singkat | Rolling deploy di belakang LB | K8s/orchestrator, deploy per service |
| Konsistensi transfer | Satu transaksi ACID | Satu transaksi ACID | Saga lintas shard |
