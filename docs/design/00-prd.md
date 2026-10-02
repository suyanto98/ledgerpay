# Step 12.3 — PRD Terpadu: LedgerPay (Digital Wallet & Mini Payment Platform)

> Dokumen ini merangkum **keadaan final** keputusan produk LedgerPay dari Step 1–11 (termasuk semua amandemen). Kalau ada perbedaan dengan dokumen step lama, dokumen ini dan tabel "Keputusan" di setiap index step yang berlaku. Detail teknis tidak diulang; lihat indeks traceability di §13.
> Status: **Final untuk V1** · Tanggal: 2026-10-02 · Pemilik: penulis portfolio (solo)

---

## 1. Overview

**Vision.** Setiap rupiah yang masuk, keluar, atau berpindah tercatat tepat satu kali. Saldo selalu bisa dijelaskan dari ledger, dan sistem tetap benar walaupun jaringan, provider, atau server-nya sendiri gagal.

**Apa produknya.** Closed-loop wallet IDR: user top-up lewat payment provider, transfer ke user lain, membayar merchant, dan menarik dana ke rekening bank. Merchant menerima pembayaran lewat API + webhook. Tim finance merekonsiliasi catatan internal dengan laporan provider.

| Audiens | Pitch |
|---|---|
| HR / non-teknis | "Aplikasi dompet digital kecil yang dirancang supaya uang tidak pernah hilang atau terhitung dua kali, walaupun ada error, klik ganda, atau server mati." |
| Interviewer teknis | "Modular monolith Go + PostgreSQL. Double-entry ledger append-only, idempotency transaksional, transactional outbox, state machine payout dengan *unknown outcome*, dan reconciliation terhadap provider." |
| Positioning (aturan kejujuran, Step 11) | *"Designed as a production-oriented mini system"*, bukan "production-grade". Angka performa hanya dikutip setelah diukur, lengkap dengan spesifikasi lingkungannya |

---

## 2. Problem, Goals, Non-Goals

### 2.1 Problem
1. **Kepercayaan:** satu kasus double-debit cukup untuk mematikan produk wallet.
2. **Sistem yang gagal secara independen:** aplikasi, DB, provider, bank, dan jaringan bisa gagal. Kegagalan paling berbahaya adalah *tidak tahu* (request terkirim, jawaban tidak kembali).
3. **Kewajiban akuntansi:** saldo user adalah liabilitas perusahaan dan harus bisa dibuktikan cocok dengan dana di rekening penampung.
4. **Audit:** setiap perubahan saldo harus bisa ditelusuri dan tidak boleh diedit.

### 2.2 Goals (V1)
| # | Goal | Ukuran keberhasilan |
|---|---|---|
| G1 | Tidak ada efek finansial ganda atau hilang | 0 pelanggaran invariant di concurrency/chaos test dan di metrik runtime demo |
| G2 | Setiap kegagalan yang terdokumentasi pulih tanpa edit data manual | FS-01…FS-10 punya test otomatis yang hijau |
| G3 | Setiap saldo bisa dijelaskan dan diaudit | Journal → objek bisnis → request/correlation ID → actor (LI-14) |
| G4 | Bisa didemokan secara deterministik dalam ≤ 10 menit | `make demo-verify` hijau nightly; Demo A–D |

### 2.3 Non-Goals
Bersaing fitur dengan DANA/OVO; integrasi payment gateway sungguhan; KYC; multi-currency/FX; kartu/PCI-DSS; chargeback/dispute penuh; microservices; Kafka/RabbitMQ; Kubernetes; mobile app; fraud ML; produk kredit/bunga/promo; event sourcing penuh/CQRS. Alasan per item: Step 1 `01-ledgerpay.md` §10.

---

## 3. Personas & User Journeys

| Persona | Peran | Kebutuhan inti |
|---|---|---|
| **Rina** | End user | Saldo selalu benar; tidak terdebet dua kali saat klik berulang |
| **Budi** | Merchant / developer | Pembayaran dan webhook yang andal, bisa refund |
| **Sari** | Finance / ops | Catatan internal cocok dengan provider/bank; menyelesaikan transaksi yang menggantung |
| **On-call engineer** | Internal | Tahu cepat kalau ada yang salah; pulih tanpa edit data manual |

| Journey | Alur ringkas (final) | Tantangan utama |
|---|---|---|
| **J1 Top-up** | `POST /topups` (idempoten, recovery point di `topups`) → checkout mock provider → webhook bertanda tangan masuk **inbox** → worker **verify-by-fetch** ke provider → journal DR Provider Clearing / CR Wallet | Webhook duplikat/terlambat/palsu; webhook mendahului response API |
| **J2 Transfer P2P** | Satu transaksi: idempotency → lock akun terurut (`FOR NO KEY UPDATE`) → cek saldo → journal | Double spend, deadlock, submit ganda |
| **J3 Merchant payment + refund** | Merchant membuat intent (API key) → user konfirmasi → journal → outbox → webhook ke merchant; refund parsial dengan guard | Dual-write, refund paralel, BOLA antar merchant, SSRF lewat URL webhook |
| **J4 Payout** | Hold → `PENDING` → worker commit `SUBMITTED` **sebelum** memanggil bank → `SUCCEEDED`/`FAILED`/`UNKNOWN` → status query berjadwal → recon/ops | Unknown outcome, crash setelah memanggil bank, larangan resubmit buta |
| **J5 Reconciliation** | Tarik settlement CSV harian → matching → kategori mismatch → antrean ops → resolusi ter-audit; item `MATCHED` memposting `PROVIDER_SETTLEMENT` | Menangkap yang lolos dari semua mekanisme lain |

---

## 4. Scope (final)

| Tingkat | Fitur (ID) |
|---|---|
| **MVP — P0** | LF-01 Auth · LF-02 Wallet & riwayat · LF-03 Double-entry ledger · LF-04 Transfer · LF-05 Idempotency · LF-06 Mock provider + failure injection · LF-07 Top-up + inbox · LF-08 Outbox + job runner · LF-09 Merchant + API key · LF-10 Payment intent + checkout · LF-11 Payout unknown outcome · LF-12 Audit log · LF-13 Health/log/correlation · LF-14 UI minimal |
| **Production signal — P1** | LF-15 Webhook keluar (signed, retry, DLQ, egress guard) · LF-16 Refund · LF-17 Reconciliation · LF-18 Ops console · LF-19 Business-invariant metrics · **LF-20 Rate limiting** (naik dari P2 karena kontrol MVP C-07) |
| **Nice to have — P2** | LF-21 Batas saldo/transaksi yang dapat dikonfigurasi · LF-22 PIN / step-up auth |
| **Opsional — P3** | LF-23 Fee · LF-24 Notifikasi email · LF-25 Risk velocity · LF-26 Balance snapshot (hanya jika load test membuktikan perlu) |
| **Out of scope** | §2.3; ditolak dari matrix: QR payment, split bill, recurring transfer, multi-wallet, admin CRUD user |

---

## 5. Functional Requirements (ringkas)

Acceptance criteria lengkap: Step 2 `01-ledgerpay-features.md` §2. Endpoint: Step 5 `02-ledgerpay-api.md` (38 endpoint).

| ID | Requirement | Acceptance criteria inti |
|---|---|---|
| FR-01 (LF-01) | User mendaftar/login; sesi dengan access JWT 15 menit + refresh token opaque berotasi | Register membuat user + wallet dalam satu tx; reuse refresh mencabut family; error login generik; lockout bertahap |
| FR-02 (LF-02) | User melihat saldo dan riwayat berbasis ledger | Saldo = `accounts.balance` = Σ entries; cursor pagination; wallet orang lain → 404 |
| FR-03 (LF-03) | Semua perubahan saldo hanya lewat `PostJournal` | Journal tidak seimbang ditolak di aplikasi **dan** oleh deferred constraint trigger; append-only; reversal maksimal sekali |
| FR-04 (LF-04) | Transfer instan antar user | Satu tx; `INSUFFICIENT_BALANCE`/`SELF_TRANSFER`/`INVALID_RECIPIENT` (generik); N request paralel tidak pernah overdraft; A↔B tanpa deadlock |
| FR-05 (LF-05) | Endpoint uang wajib `Idempotency-Key` | Replay sama persis + `Idempotent-Replayed: true`; payload beda → 422; in-flight → tunggu/409; 201 dan 422 domain disimpan; 5xx tidak; retensi 7 hari |
| FR-06 (LF-06) | Mock provider terpisah dengan skenario failure | Charge, checkout hosted, payout, status query, settlement CSV, admin skenario; webhook HMAC; magic values (contoh rekening berakhiran `0408` → timeout) |
| FR-07 (LF-07) | Top-up lewat provider | Signature/timestamp invalid → 401/400 tanpa efek; dedupe event; verify-by-fetch sebelum kredit; `EXPIRED` status lunak (webhook sukses terlambat tetap dikredit) |
| FR-08 (LF-08) | Side effect lewat outbox + job runner | Job ditulis di tx bisnis; `SKIP LOCKED` + lease + fencing; backoff + jitter; status `dead` + alert; handler idempoten |
| FR-09 (LF-09) | Merchant menerima API key | Key ditampilkan sekali, disimpan ter-hash, maksimal 2 slot aktif, manajemen **hanya lewat ops** |
| FR-10 (LF-10) | Merchant membuat intent, user membayar | `merchant_order_id` unik; konfirmasi paralel → satu pembayaran; intent kedaluwarsa → 422; checkout = capability URL |
| FR-11 (LF-11) | Payout ke rekening bank | Hold di tx yang sama; `SUBMITTED` di-commit sebelum memanggil bank; timeout → `UNKNOWN` (tampil ke user sebagai `PROCESSING`); status query 30s/2m/10m/1h/6h → `needs_reconciliation`; respons provider yang tidak cocok → `UNKNOWN` |
| FR-12 (LF-12) | Audit aksi sensitif | Append-only; ditulis di tx yang sama; tanpa data sensitif |
| FR-13 (LF-13) | Health dan log terstruktur | `/healthz`, `/readyz` (versi DB ≥ minimum binary); log JSON dengan request/correlation/trace ID; redaction |
| FR-14 (LF-14) | UI demo | Wallet, transfer, checkout, halaman mock provider; tombol di-disable selama request (idempotency tetap jaminan utama) |
| FR-15 (LF-15) | Webhook keluar ke merchant | Signature `t=…,v1=…`; event ID stabil; retry 1m/5m/30m/2h/6h/24h → dead (7 percobaan); egress guard (blok IP privat/metadata, tanpa redirect) |
| FR-16 (LF-16) | Refund parsial/penuh | Σ refund ≤ amount (lock + CHECK); saldo merchant cukup |
| FR-17 (LF-17) | Reconciliation harian | Idempoten per file hash; lima kategori mismatch; posting `PROVIDER_SETTLEMENT` untuk yang cocok |
| FR-18 (LF-18) | Ops console | Tanpa endpoint "edit saldo"; setiap aksi butuh alasan + bukti provider + audit |
| FR-19 (LF-19) | Metrik invariant + dashboard + alert | Katalog final Step 8 `01` §3; invariant > 0 = alert critical |
| FR-20 (LF-20) | Rate limiting per tier | Token bucket in-memory per instance (V1); `429` + `Retry-After` |

---

## 6. Business Rules & Invariants

| BR (Step 1) | Isi | Invariant (Step 3) |
|---|---|---|
| BR1 | Journal seimbang, ≥ 2 entry | LI-01 |
| BR2 | Saldo wallet user tidak negatif | LI-02, LI-03 |
| BR3 | Satu idempotency key = maksimal satu efek; payload beda ditolak | LI-05, LI-06 |
| BR4 | Ledger append-only; koreksi via reversal | LI-04 |
| BR5 | Σ refund ≤ amount | LI-08 |
| BR6 | Payout `UNKNOWN` tidak di-resubmit buta | LI-10 |
| BR7 | Transisi status sah; status final tidak kembali | LI-07 |
| BR8 | Nominal integer minor unit, single currency | LI-11 |
| BR9 | Batas saldo/transaksi dapat dikonfigurasi (angka regulasi diverifikasi dulu) | — (LF-21, P2) |
| BR10 | Efek webhook maksimal sekali | LI-09 |
| — | Intent dibayar maksimal sekali | LI-13 |
| — | Traceability efek finansial | LI-14 |
| — | Ownership resource (dijaga APP + BOLA test, tanpa RLS; pengecualian yang disadari) | LI-15 |
| — | Kecocokan liability vs aset clearing (eventual, via recon) | LI-12 |

Setiap invariant P0 punya minimal satu penjaga di level DB/transaksi (kecuali LI-15, yang dijelaskan sebagai pengecualian). Detail: Step 3 `02-ledgerpay-invariants.md`.

---

## 7. Non-Functional Requirements

| Area | Requirement |
|---|---|
| **Konsistensi** | Strong consistency untuk saldo, journal, idempotency, dan transisi status (satu Postgres, Read Committed + lock eksplisit + guard). Eventual: saldo akun sistem (derived), hasil recon, delivery webhook |
| **Reliability** | Sistem harus menangani 10 failure scenario berikut tanpa efek ganda/hilang. Setiap scenario punya test otomatis |
| **Security** | Kontrol MVP Step 6 (C-01…C-46 yang berstatus MVP), antara lain: argon2id + semaphore hashing (C-45), refresh rotation + reuse detection, BOLA 404, HMAC + window 5 menit + verify-by-fetch untuk webhook masuk, egress guard SSRF (C-44), API key ter-hash, security headers, secrets via sops + age, scan CI |
| **Performance** | **Target desain, bukan klaim:** p95 `POST /transfers` < 200 ms; outbox lag p95 < 5 dtk; ≥ 95% payout `UNKNOWN` selesai otomatis dalam 1 jam; ketersediaan endpoint uang 99,5%/30 hari di lingkungan demo. Angka yang dikutip publik hanya hasil load test (Step 8 `03`) beserta spesifikasi VM |
| **Observability** | Log JSON + correlation lintas api → job → provider; metrik Prometheus berprefix `app_`/`ledgerpay_`; tracing OTel dengan link span job; dashboard di-provision; alert invariant |
| **Operability** | Deploy via CI dengan health gate + auto rollback (M4); migrasi expand/contract; backup harian terenkripsi + restore drill; RPO 24 jam / RTO 2 jam (V1); kill switch untuk payout/webhook/top-up |
| **Maintainability** | Modular monolith dengan batas modul ditegakkan depguard; SQL eksplisit (sqlc); spec-first OpenAPI; ADR untuk setiap keputusan infrastruktur |

| Failure scenario (requirement) | Hasil akhir yang diwajibkan |
|---|---|
| FS-01 Duplicate request | Satu efek; request kedua di-replay |
| FS-02 Concurrent payment | Tidak pernah overdraft; tepat sejumlah yang muat yang berhasil |
| FS-03 Database failure | Tidak ada efek setengah jadi; retry client dengan key sama aman |
| FS-04 API crash setelah commit | Retry client mendapat replay, bukan efek kedua |
| FS-05 Provider timeout | `UNKNOWN` → status query/recon; uang terkirim maksimal sekali |
| FS-06 Webhook duplicate | No-op |
| FS-07 Webhook terlambat / tidak berurutan | Kredit terlambat tetap diterapkan; konflik status final → recon, tidak dibalik otomatis |
| FS-08 Worker crash | Job diambil ulang; status query dulu, bukan resubmit |
| FS-09 Network timeout merchant ↔ LedgerPay | Retry idempoten; webhook keluar di-retry dengan event ID stabil |
| FS-10 Partial processing | Tidak ada keadaan uang berpindah tanpa catatannya |

---

## 8. External Dependencies & Contracts

| Dependency | Kontrak | Catatan |
|---|---|---|
| **mock-provider** (PG + bank) | Step 5 `03` §1: charge (`Idempotency-Key = topup_id`), checkout hosted, payout (`Idempotency-Key = payout_id`), status query dengan `NOT_FOUND` + safe window, settlement CSV, admin skenario | Proses terpisah dengan DB sendiri; `/admin` tidak dipublish (SSH tunnel saat demo) |
| **Webhook masuk** | `Provider-Signature: t=…,v1=…` (HMAC-SHA256 atas `t.raw_body`), window 5 menit, membawa `metadata.correlation_id` | Event payout tidak ada di V1 |
| **Webhook keluar ke merchant** | Envelope `{id, type, created_at, data}`, header `LedgerPay-Signature`, timeout 5 dtk, retry terjadwal | Merchant wajib dedupe berdasarkan event ID |

---

## 9. Success Metrics

| Jenis | Metrik |
|---|---|
| Produk (hipotetis) | % transaksi selesai tanpa intervensi manual; waktu rata-rata `UNKNOWN` → resolved; mismatch recon per 10.000 transaksi |
| Engineering (dapat diverifikasi) | 0 pelanggaran invariant di test & runtime demo; 10/10 FS punya test hijau; matriks traceability LI/FS 100%; concurrency test 100 goroutine selalu hijau; baseline load test dipublikasikan dengan spesifikasi lingkungan; demo dari nol ≤ 10 menit |

---

## 10. Milestones & Release Plan

| Milestone | Sprint (1 minggu) | Kriteria |
|---|---|---|
| M1 Walking skeleton | Akhir S1 | `make up` + health hijau, CI hijau, satu endpoint end-to-end |
| M2 Core money correct | Akhir S3 | Demo A & B lolos sebagai test otomatis; invariant checker hijau |
| **M3 Siap melamar** | **Akhir S7** | Demo A–C di VM publik; README + video; CI (PR checks) + deploy ter-script yang bisa diulang; observability dasar; FS-01/02/04/05/08 hijau |
| M4 LedgerPay selesai | Akhir S10 | DoD project penuh: semua FS, Demo D, CD + rollback teruji, backup + restore drill, baseline load test, dokumentasi |

Rilis diberi tag semver per milestone (contoh `v0.7.0` = M3). Detail sprint: Step 10 `02-ledgerpay-roadmap.md`.

---

## 11. Risks & Assumptions

| Risiko | Mitigasi |
|---|---|
| Scope creep domain payment | Out-of-scope mengikat; gerbang 2-dari-3 + veto |
| Bug correctness ledger baru ketahuan saat demo | Deferred trigger, property test, invariant checker di setiap test, metrik invariant |
| Demo live rapuh | Skrip deterministik, `make demo-verify` nightly, video cadangan |
| Hot row (akun sistem, wallet merchant) | Akun sistem non-materialized (ADR-L07); merchant: ukur dulu (E-1), jalur V2 `MERCHANT_PENDING` |
| Klaim berlebihan | Aturan kejujuran Step 11; placeholder `[UKUR: …]` |

**Asumsi:** ≥ 14 jam/minggu; Go + Vue; single currency IDR; angka regulasi e-money hanya dipakai setelah diverifikasi; dikerjakan solo.

---

## 12. Open Questions / Future Work
- Rotasi secret webhook merchant dengan dual-signature (O16; diputuskan di Step 12 `01`).
- `client_secret` untuk checkout ala Stripe (production enhancement).
- `MERCHANT_PENDING` + sweep jika E-1 membuktikan perlu.
- Arsitektur V2/V3: HA Postgres + PITR, Redis untuk rate limit terdistribusi, ledger service + sharding + saga, CDC → Kafka (Step 2 `02-ledgerpay-architecture.md`).
- PIN/step-up, batas regulasi, fee (P2/P3).

---

## 13. Traceability Index

| PRD | Dokumen desain |
|---|---|
| §1–3 | Step 1 `01-ledgerpay.md` |
| §4–5 | Step 2 `01-ledgerpay-features.md`, Step 5 `02-ledgerpay-api.md` |
| §6 | Step 1 BR, Step 3 `02-ledgerpay-invariants.md` |
| §7 Reliability | Step 3 `03-ledgerpay-failure-scenarios.md`, Step 7 `01` |
| §7 Security | Step 6 `02-ledgerpay-threat-model.md`, `04-security-controls-and-testing.md` |
| §7 Performance/Observability | Step 8 `00-index.md`, `01`, `03` |
| §7 Operability | Step 9 |
| §8 | Step 5 `03-ledgerpay-external-contracts.md`, ADR-L05, ADR-L08 |
| §10 | Step 10 `02-ledgerpay-roadmap.md`, `04-definition-of-done.md` |
| Keputusan arsitektur | Step 2 ADR register + amandemen (ADR-006, L02, L03, L06, L08, 009, 010) |
