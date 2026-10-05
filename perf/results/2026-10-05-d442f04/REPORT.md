# Load test baseline — 2026-10-05, commit d442f04 (laptop)

> **Ini baseline SATU LAPTOP, bukan klaim kapasitas server.** Sistem yang diuji (api, worker, Postgres,
> mock) dan generator beban (k6) berjalan di mesin yang sama, dan mesin itu juga menjalankan aplikasi
> desktop lain. Target resmi desain (VM 2 vCPU / 4 GB, k6 dari mesin terpisah, §3) belum tersedia:
> deploy ke VM ditunda sampai aplikasi siap (keputusan 2026-10-04). Angka di sini dipakai untuk menemukan
> bottleneck dan membandingkan skenario satu sama lain, bukan untuk dikutip sebagai throughput produksi.

## Lingkungan
| | |
|---|---|
| Mesin | Intel Core i3-1315U (6 core / 8 thread), 14 GB RAM (±10 GB sudah dipakai aplikasi lain), NVMe |
| Software | Docker 29.6, Postgres 17.11 (`shared_buffers=1GB`, `pg_stat_statements`, `auto_explain`), Go 1.25/1.26 image, k6 1.2.3 |
| App | `APP_ENV=perf`: rate limit dimatikan (`RATE_LIMIT_DISABLED`), pprof di port ops; pool api 20 / worker 10 (tidak diperbesar) |
| Dataset | `ledgerpay seed perf`: 100.006 user, 1.000 merchant, **1.001.005 journal / 2.002.010 entries** (Pareto 80/20, 12 bulan). COPY 43,6 dtk; total termasuk VACUUM + gate 49,6 dtk. **Gate invariant lolos** (ledger.Verify lewat role aplikasi) |
| Alat | `perf/run.sh <skenario>` → bukti per skenario di folder ini (k6 summary, snapshot DB + cek invariant, `pg_stat_statements`, CPU container bertimestamp, `ledger-verify`) |

## Ringkasan
| Skenario | Beban | Hasil | p95 | Invariant | Catatan |
|---|---|---|---|---|---|
| LP-2 10.000 transfer | ramp 10 → 200/dtk, 1.000 user | 10.099 transfer, semua 201 | 11,2 ms | ✅ Σ saldo user sama persis; journal TRANSFER = jumlah 201; ledger-verify OK | Belum jenuh di 200/dtk |
| LP-2 eksplorasi | ramp 200 → 800/dtk | 32.843 transfer dalam 90 dtk (±365/dtk rata-rata); 7.255 iterasi dibuang k6 | 3,99 dtk | ✅ sama | Jenuh: CPU laptop (Postgres ±4,3 core + API + k6 di 8 thread) |
| LP-3 hot wallet | 2.001 req @ 500/dtk, 1 wallet | **tepat 100 sukses**, 1.901 `INSUFFICIENT_BALANCE` | 690 ms | ✅ saldo akhir 0, 100 debit; 0 deadlock, 0 `RESOURCE_BUSY`, 0 5xx | p95 = antre lock satu baris |
| LP-4 hot merchant (E-1) | 25 → 400/dtk, 1 merchant | jenuh di **±116–137 pembayaran/dtk** | lihat tabel E-1 | ✅ journal PAYMENT = confirm 200; saldo merchant naik tepat | CPU longgar saat jenuh → batasnya baris lock |
| LP-4 kontrol | 25 → 400/dtk, 100 merchant | jenuh di **±250 pembayaran/dtk** | 17,7 ms @100/dtk | ✅ sama | Batas sistem, bukan hot row |
| LP-9 login | 5 → 50/dtk selama 5 mnt | ±11 login/dtk sukses; kelebihan → 503 | login 2,4 dtk; 503 2,0 dtk; `GET /wallet` 14,9 ms | — | argon2id m=64 MiB, semaphore 4; RSS API puncak 692 MiB, tanpa OOM |

## E-1 — pembayaran ke satu merchant (O11)
Satu pembayaran = create intent (API key) + confirm (payer). Per tahap 2 menit. Lock wait = histogram
`ledgerpay_ledger_lock_wait_seconds{purpose="PAYMENT"}` (waktu `LockWalletAccounts` di PostJournal),
nilai = batas atas bucket yang memuat p95.

| Target/dtk | Hot run 1: tercapai · p95 confirm · p95 lock wait | Hot run 2: tercapai · p95 confirm · p95 lock wait · CPU api/pg | Kontrol (100 merchant): tercapai · p95 confirm · p95 lock wait · CPU api/pg |
|---|---|---|---|
| 25 | 25,0 · 15 ms · ≤1 ms | 25,0 · 13 ms · ≤1 ms · 18%/29% | 25,0 · 17 ms · ≤2,5 ms · 22%/36% |
| 50 | 49,5 · 14 ms · ≤1 ms | 49,5 · 11 ms · ≤1 ms · 31%/49% | 49,5 · 16 ms · ≤2,5 ms · 36%/57% |
| **100** | 99,0 · **404 ms** · **≤500 ms** | 99,0 · **16 ms** · **≤5 ms** · 49%/86% | 99,0 · 18 ms · ≤2,5 ms · 65%/115% |
| 200 | 116 · 10,3 dtk · ≤500 ms | 137 · 9,2 dtk · ≤500 ms · 81%/164% | 198 · 250 ms · ≤10 ms · 140%/269% |
| 400 | 94 · 17,0 dtk · ≤500 ms | 119 · 11,6 dtk · ≤500 ms · 76%/157% | 257 · 3,1 dtk · ≤25 ms · 183%/383% |

(CPU: rata-rata sampel per tahap, 100% = satu thread. Run 1 tidak punya sampel CPU per tahap: fitur itu
ditambahkan setelahnya. Folder `lp4-INVALID-stage-misaligned` adalah run sebelum perbaikan alat ukur,
lihat "Kesalahan alat ukur".)

**Analisis.** Saat hot merchant jenuh, CPU masih longgar (Postgres ±1,6 core, API ±0,8 core), sedangkan
kontrol dengan laju sama baru jenuh di ±250/dtk dengan CPU ±4 core Postgres. Jadi batas ±116–137/dtk
adalah **baris wallet merchant**, bukan mesin. Dari `pg_stat_statements` run 1: `LockWalletAccounts`
rata-rata **103 ms** (antre), sedangkan semua statement SETELAH lock didapat (journal, 2 entry, 2 update
saldo, update intent, complete idempotency, trigger keseimbangan) hanya ±3 ms SQL; ditambah ±15 round trip
aplikasi↔DB dan COMMIT, lock dipegang ±7–9 ms per pembayaran → batas teoretis 1 / 8,6 ms ≈ 116/dtk, sesuai
pengukuran. Di 100/dtk utilisasi baris ±75–85%: wilayah di mana teori antrean memprediksi ekor latensi
melonjak dan tidak stabil, dan itulah yang terlihat (run 1: 404 ms, run 2: 16 ms pada tahap yang sama).

**Keputusan (aturan §6: laju < 100/dtk ATAU p95 lock wait > 50 ms pada 100/dtk).** Laju 100/dtk tercapai
di kedua run, tetapi p95 lock wait > 50 ms pada 100/dtk di run 1 dan sistem jenuh tidak jauh di atasnya.
Aturan dianggap **terpicu** (keputusan konservatif, dan target VM 2 vCPU kemungkinan lebih lambat dari
laptop ini): **buka issue `MERCHANT_PENDING` (V2)**, tidak diimplementasikan di S9 karena trafik V1 adalah
demo. Langkah lebih murah yang dicatat sebelum V2: mengurangi round trip di dalam bagian yang memegang lock
(contoh: membangun respons dari data yang sudah ada alih-alih `GetIntent` + nama payer setelah update), lalu
ulangi E-1 di VM target.

## LP-2: di mana batas transfer biasa?
- Sampai 200/dtk: p95 11,2 ms, CPU jauh dari penuh.
- Ramp ke 800/dtk: profil CPU API 20 dtk di puncak = **±1,65 core**; 64% di handler transfer, hampir
  semuanya I/O pgx (syscall 27%, runtime, alokasi). Tidak ada fungsi aplikasi yang dominan → **tidak ada
  perubahan kode** dari profiling ini (§7: hanya bottleneck yang terbukti yang diperbaiki).
- Postgres puncak ±4,3 core; waktu rata-rata `INSERT journal_entries` naik dari 0,22 ms (200/dtk) ke
  0,76 ms: antre CPU. Dengan k6 di mesin yang sama, titik jenuh tidak bisa dipisahkan lebih jauh.
- Ide berikut (belum diukur): kurangi round trip per transfer (±10 statement) dengan `pgx.Batch` atau
  fungsi SQL; ukur di mesin terpisah sebelum mengubah apa pun.

## LP-9: login
Login sukses ±11/dtk (argon2id m=64 MiB, t=3, p=2; semaphore 4 hash paralel). Permintaan berlebih menunggu
maksimal 2 dtk lalu `503` (batas antre semaphore), bukan latensi tak terbatas; RSS API puncak 692 MiB
(4 × 64 MiB hash + baseline), tanpa OOM. Endpoint lain tidak terseret: `GET /wallet` p95 14,9 ms selama
login jenuh. Desain memperkirakan 8–15 login/dtk di 2 vCPU; laptop ini ada di rentang itu.

## Kesalahan alat ukur yang ditemukan (dan diperbaiki) selama sprint
| Gejala | Penyebab | Perbaikan |
|---|---|---|
| Tahap 0 LP-4 "hanya" 6,2/dtk padahal target 25 | Tahap dihitung dari `currentTestRunDuration`, yang ikut menghitung setup (login 1.000 payer ±1 menit) | Waktu sejak skenario mulai (`exec.scenario.startTime`) |
| Sampler lock wait tidak pernah mulai, skrip menggantung | Menunggu kata "running" dari k6, padahal `--quiet` tidak mencetaknya | Deteksi awal beban dari counter API sendiri |
| Lock wait "n/a" | Regex mengasumsikan urutan label `le` sebelum `purpose` | Parse label sebagai map |
| Setup kontrol gagal `API_KEY_LIMIT_REACHED` | Dua run sebelumnya meninggalkan 2 key aktif (batas slot bekerja) | `teardown` k6 mencabut key-nya lewat endpoint ops |

## Reproduksi
```bash
make perf-up && make perf-seed            # perf env + 1 juta journal + gate
make perf-run S=lp2                        # RATE_MAX=800 PROFILE_AFTER=60 untuk eksplorasi + profil CPU
make perf-run S=lp3                        # argumen hot user: ./perf/run.sh lp3 1 (hot-0…hot-4, sekali pakai)
STAGE_SECONDS=120 make perf-run S=lp4      # lalu S=lp4-control
make perf-run S=lp9
make perf-down
```
