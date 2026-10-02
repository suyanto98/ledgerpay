# ADR-L07 — Hot Account: Akun Sistem Tanpa Saldo Materialized

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Hampir setiap transaksi menyentuh akun sistem: setiap top-up mendebit `PROVIDER_CLEARING`, setiap payout mengkredit `PAYOUT_IN_TRANSIT`. Kalau akun sistem diperlakukan seperti wallet (kolom `balance` di-update dengan `FOR UPDATE`), **semua transaksi di seluruh sistem antre pada satu baris**.

### Hitungan kasar
Misalkan lock pada baris akun clearing dipegang ±2 ms per transaksi (lock → insert entries → update → commit dengan fsync). Karena ter-serialisasi, plafonnya:

```text
1000 ms / 2 ms ≈ 500 transaksi/detik untuk SELURUH sistem
```

Di praktiknya lebih rendah, karena lock dipegang sampai commit selesai (termasuk fsync WAL) dan antrean menambah latency untuk semua user. Angka ini **ilustrasi**, bukan hasil ukur. Itu sebabnya keputusan ini disertai syarat pembuktian lewat load test.

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. Materialized + `FOR UPDATE` (sama seperti wallet) | Konsisten dengan wallet; saldo instan | Satu baris jadi titik serialisasi global |
| B. **Derived: saldo akun sistem = Σ entries (tanpa baris yang di-update)** | Insert entries tidak saling mengunci; tidak ada hot row | Query saldo = agregasi; lebih lambat seiring jumlah entries |
| C. Snapshot berkala + delta | Query saldo cepat (snapshot + Σ entries setelah snapshot) | Ada job tambahan; logika query sedikit lebih rumit |
| D. Sub-akun ter-shard (`PROVIDER_CLEARING#0..N`, dipilih acak/hash) | Mengurangi contention N kali; saldo tetap materialized | Saldo = Σ sub-akun; kompleksitas penamaan & reporting |
| E. Update saldo async dalam batch (antrean delta) | Throughput tinggi | Saldo sistem eventually consistent; perlu infrastruktur antrean |

## Decision

**Opsi B di V1, dengan jalur ke Opsi C.**

| Aturan | Detail |
|---|---|
| Akun sistem (`PROVIDER_CLEARING`, `BANK_CLEARING`, `PAYOUT_IN_TRANSIT`, `FEE_REVENUE`) | Baris di `accounts` ada (identitas, tipe), tetapi kolom `balance` **tidak di-update** per transaksi (`is_system = true`, `balance NULL`) |
| Lock | Akun sistem **tidak pernah** masuk ke `LockAccountsOrdered` ([ADR-L03](ADR-L03-concurrency-pessimistic-locking.md)) |
| Invariant "tidak negatif" | Tidak berlaku untuk akun sistem (bisa negatif secara sah sesuai tipenya) |
| Saldo akun sistem | Query `SUM` dengan index `journal_entries (account_id) INCLUDE (direction, amount)` (index-only scan) |
| Pemakaian | Saldo akun sistem dipakai oleh dashboard ops, reconciliation, dan invariant check, bukan oleh jalur transaksi |
| V2 (jika query agregasi lambat) | Tabel `account_balance_snapshots(account_id, as_of_entry_id, balance)` diisi job berkala; saldo = snapshot + Σ entries setelah `as_of_entry_id` |

**Syarat pembuktian:** load test di sprint performa (Step 8) mengukur throughput transaksi dan latency query saldo sistem. Opsi C/D **hanya** diimplementasikan jika ada angka yang membenarkannya.

## Reason

- Akun sistem tidak membutuhkan validasi saldo saat transaksi, sehingga tidak ada alasan menguncinya.
- Insert ke `journal_entries` adalah operasi append yang tidak saling mengunci antar transaksi.
- Menghindari bottleneck global sejak desain jauh lebih murah daripada memperbaikinya setelah ada data production.
- Kebenaran tetap dijamin oleh ledger (Σ D = Σ C per journal); saldo sistem hanyalah proyeksi.

## Trade-offs

- Dua jenis akun dengan aturan berbeda, sehingga kode harus membedakan `is_system`. Dibungkus di repository, dan concurrency test memastikan akun sistem tidak pernah di-lock.
- Query saldo sistem semakin berat seiring pertumbuhan entries. Jalur ke snapshot sudah disiapkan.
- Saldo sistem di dashboard adalah hasil agregasi pada titik baca (konsisten secara snapshot, tidak real-time per milidetik). Ini dapat diterima untuk kebutuhan ops.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Query saldo sistem p95 > target dashboard | Implementasi snapshot + delta (Opsi C) |
| Wallet merchant besar menjadi hot account | Kredit merchant ke sub-akun pending + sweep berkala, atau sub-akun ter-shard (Opsi D) |
| Throughput global masih kurang setelah itu | Sharding ledger (V3, lihat arsitektur) |

## Interview angle

- **"Apa itu hot account dan kenapa berbahaya?"** → Satu baris yang di-lock oleh hampir setiap transaksi menjadi plafon throughput global. Contoh kasar: lock 2 ms berarti maksimum ±500 tps untuk seluruh sistem.
- **"Kalau saldo clearing tidak disimpan, bagaimana tahu nilainya?"** → Agregasi dari entries dengan index-only scan, dan snapshot + delta kalau sudah perlu.
- **"Kenapa tidak langsung shard sejak awal?"** → Belum ada bukti kebutuhan. Desain menghindari hot row dulu, dan optimasi berikutnya menunggu hasil load test.

## Related

[ADR-L01](ADR-L01-double-entry-ledger.md) · [ADR-L03](ADR-L03-concurrency-pessimistic-locking.md) · [ADR-004](ADR-004-postgresql.md)
