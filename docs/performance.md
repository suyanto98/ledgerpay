# Performance baseline

Laporan lengkap (lingkungan, metode, semua tabel, bukti mentah): [`perf/results/2026-10-05-d442f04/REPORT.md`](../perf/results/2026-10-05-d442f04/REPORT.md).
Rencana dan aturan wording: [`docs/design/23-performance-plan.md`](design/23-performance-plan.md).

> Baseline **satu laptop** (i3-1315U, 8 thread, k6 di mesin yang sama dan aplikasi desktop lain berjalan).
> Bukan klaim kapasitas server. Angka VM target (2 vCPU / 4 GB, k6 terpisah) menyusul saat deploy.

| Skenario | Hasil | p95 | Invariant |
|---|---|---|---|
| LP-2 10.000 transfer (10 → 200/dtk) | semua sukses, belum jenuh | 11,2 ms | ✅ Σ saldo tetap, drift 0 |
| LP-2 eksplorasi (→ 800/dtk) | ±365/dtk rata-rata, jenuh karena CPU laptop | 3,99 dtk | ✅ |
| LP-3 hot wallet (2.001 req @ 500/dtk) | tepat 100 sukses, saldo akhir 0, 0 deadlock | 690 ms | ✅ |
| LP-4 / E-1 satu merchant | jenuh ±116–137 pembayaran/dtk (baris lock) | 16–404 ms @ 100/dtk | ✅ |
| LP-4 kontrol 100 merchant | jenuh ±250 pembayaran/dtk (CPU) | 18 ms @ 100/dtk | ✅ |
| LP-9 login | ±11/dtk; kelebihan → 503 dalam ≤ 2 dtk; endpoint lain p95 15 ms | — | — |

**Keputusan E-1:** aturan terpicu (p95 lock wait > 50 ms pada 100/dtk di salah satu run) → issue `MERCHANT_PENDING` (V2) dibuka di backlog; tidak diimplementasikan untuk trafik demo V1.

**Temuan & tindakan:** bottleneck pertama di jalur pembayaran adalah waktu lock baris wallet merchant (±8,6 ms per pembayaran: SQL ±3 ms + round trip + commit); di jalur transfer biasa, CPU Postgres. Profiling API tidak menemukan hotspot kode, jadi tidak ada perubahan kode berbasis performa di sprint ini.
