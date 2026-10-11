# Demo script (10 menit)

Semua perintah di sini sama dengan yang dijalankan `make demo-verify` (nightly), dengan asersi: kalau ada
janji yang tidak terpenuhi (contoh "tepat 10 dari 100 transfer"), skripnya gagal. Pola setiap segmen:
**masalah → mekanisme → bukti**.

Disesuaikan dari rencana Step 11 dengan yang benar-benar dibangun: tidak ada `/demo/reset` atau toolbar
demo di UI (setiap skrip membuat user sendiri dan me-reset mock), fault demo dipasang lewat
`POST /api/v1/demo/faults` dengan `X-Demo-Token`.

## Persiapan (10 menit sebelumnya)
| # | Langkah | Siap jika |
|---|---|---|
| 1 | `make up-obs` (lokal; port API bisa diganti `LP_HTTP_PORT=18080`) | `make ps` semua healthy |
| 2 | `make web` → `http://localhost:5173` | Halaman login tampil |
| 3 | Tab browser: Wallet · Grafana **LedgerPay — Integrity & Recon** · Grafana **Money & Integrity** · Jaeger | Grafana "last 15 minutes", refresh 10s |
| 4 | Terminal font besar, satu tab per demo (A, B, C, D) | — |
| 5 | Cadangan: video (`[LINK]`) dan `make demo-verify` hijau tadi malam | Bisa pindah < 10 detik |

## Alur

| Waktu | Segmen | Lakukan | Bukti yang ditunjuk |
|---|---|---|---|
| 0:00 | **Masalah** | "Klik bayar, loading, klik lagi: terpotong dua kali? Kirim uang ke bank, koneksi putus: uangnya keluar atau belum?" | — |
| 1:00 | **Arsitektur** | Diagram di README | Satu binary (api + worker), PostgreSQL sebagai ledger **dan** antrean (outbox), mock provider dengan fault injection |
| 2:00 | **Alur normal** | UI: daftar → top-up lewat checkout mock → transfer → bayar merchant lewat link checkout | Riwayat wallet; panel INV semua 0 |
| 4:00 | **Demo A — idempotency** | `make demo-a` | Request ke-2 `Idempotent-Replayed: true`, key sama nominal beda → 422 `IDEMPOTENCY_KEY_REUSED`, saldo terdebet sekali |
| 5:00 | **Demo B — concurrency** | `make demo-b` | `10 × 201`, `90 × 422`, saldo 0, tidak pernah negatif; `ledger OK` |
| 6:00 | **Demo C — payout tak diketahui** | `make demo-c` | Fault demo: worker mati tepat setelah bank menerima (exit 137) → `UNKNOWN` → status query → `SUCCEEDED`; bank `transfers_executed: 1`; panel "Payouts UNKNOWN" naik lalu turun; Jaeger: job ter-link ke request |
| 8:00 | **Demo D — rekonsiliasi** | `make demo-d` atau halaman **Ops** di UI (login sebagai user OPS) | File settlement bank SUCCEEDED vs payout UNKNOWN → `STATUS_MISMATCH` → resolve dengan bukti; audit log; bank tetap 1 transfer |
| 9:00 | **Penutup** | Grafana Integrity & Recon; `docs/performance.md` | Invariant gauge 0; baseline jujur (laptop) dan temuan E-1; tawarkan diskusi: hot merchant, exactly-once vs at-least-once, kenapa tidak microservices |

## Versi 3 menit (HR / non-teknis, tanpa terminal)
Pengunjung bisa mencoba sendiri lewat link demo publik: tombol **Coba demo** di halaman login membuat sandbox
pribadi (Budi, saldo Rp2.692.000, tiga kontak, tagihan Kopi Senja) tanpa daftar. Diuji end-to-end oleh
`frontend/e2e/demo.spec.ts`.
1. Klik **Coba demo** → Beranda: saldo, riwayat yang menyebut orang dan merchant ("Dari Siti Rahma · Patungan makan siang").
2. **Bayar tagihan Kopi Senja** dari kartu di Beranda: checkout seperti toko online, saldo langsung berkurang.
3. **Kirim uang ke Siti** (kontak sudah terisi), tekan tombol kirim dua kali: saldo hanya terpotong sekali.
4. "Setiap pengunjung punya dunianya sendiri; datanya terhapus sendiri dalam 24 jam" → banner **Mode demo**.

## Kalau ada yang salah saat live
| Gejala | Tindakan |
|---|---|
| Stack belum healthy | `make ps`; `make logs`; pindah ke video |
| Demo C menunggu lama | Normal: lease 30 dtk + status check 30 dtk (≈ 60 dtk). Isi waktunya dengan menjelaskan state machine ADR-L06 |
| Port 8080 terpakai | `LP_HTTP_PORT=18080 make up …` (dan sama untuk `make demo-*`) |
| Fault tertinggal dari latihan | `curl -X DELETE $API/demo/faults -H "Authorization: Bearer …" -H "X-Demo-Token: …"` |

## Pertanyaan yang sering muncul (jawaban 30 detik ada di `learning/`)
- "Exactly-once?" → at-least-once delivery + efek idempoten (unique key, guard status, fencing).
- "Kenapa Postgres sebagai queue?" → tidak ada dual write: niat ikut commit dengan efeknya (ADR-006).
- "Berapa throughput-nya?" → baseline laptop di `docs/performance.md`, dengan batasannya; satu merchant dibatasi lock satu baris (±120/dtk), aturan E-1 terpicu → desain `MERCHANT_PENDING` di backlog.
