# ADR-L01 — Double-Entry Ledger Append-Only

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Setiap fitur LedgerPay (top-up, transfer, payment, refund, payout) mengubah saldo. Kalau saldo hanya disimpan sebagai satu kolom `balance` yang di-update langsung, tiga pertanyaan tidak bisa dijawab:

1. *Kenapa saldo Rina 150.000?* Tidak ada jejak yang bisa dijumlahkan ulang.
2. *Apakah ada uang yang tercipta atau hilang?* Tidak ada pasangan yang harus seimbang.
3. *Berapa total kewajiban perusahaan ke seluruh user, dan apakah cocok dengan dana di bank?* Tidak ada model akun perusahaan.

Dibutuhkan model data yang membuat setiap perpindahan uang **seimbang, permanen, dan bisa ditelusuri**.

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. Single-entry: kolom `balance` + tabel `transactions` | Sederhana, cepat dibuat | Tidak ada invariant keseimbangan; uang bisa "tercipta" lewat bug tanpa terdeteksi; tidak ada sisi perusahaan |
| B. **Double-entry: `journals` + `journal_entries`, saldo materialized untuk akun user/merchant** | Invariant Σ debit = Σ kredit bisa ditegakkan DB; saldo bisa direkonstruksi; standar akuntansi | Lebih banyak tabel dan konsep; butuh pemahaman debit/kredit |
| C. Event sourcing penuh (saldo = fold atas event stream) | Audit sempurna, replay | Butuh event store, projection, snapshot; kompleksitas infrastruktur tanpa masalah yang benar-benar diselesaikan di skala ini |
| D. Double-entry tanpa saldo materialized (saldo selalu `SUM`) | Tidak ada risiko drift | Cek saldo saat transfer jadi `SUM` atas ribuan baris di bawah lock; lambat dan sulit di-lock dengan benar |

## Decision

**Opsi B.**

### Model akun (sudut pandang perusahaan)

| Tipe | Normal balance | Contoh akun | Saldo = |
|---|---|---|---|
| Asset | Debit | `PROVIDER_CLEARING`, `BANK_CLEARING` | Σ debit − Σ kredit |
| Liability | Kredit | `WALLET:<user>`, `WALLET:<merchant>` | Σ kredit − Σ debit |
| Liability (suspense) | Kredit | `PAYOUT_IN_TRANSIT` | Σ kredit − Σ debit |
| Revenue | Kredit | `FEE_REVENUE` (P3) | Σ kredit − Σ debit |

### Contoh journal

| Workflow | Debit | Kredit | Arti |
|---|---|---|---|
| Top-up 100.000 sukses | `PROVIDER_CLEARING` 100.000 | `WALLET:rina` 100.000 | Perusahaan punya piutang ke provider, berutang ke Rina |
| Transfer Rina → Andi 30.000 | `WALLET:rina` 30.000 | `WALLET:andi` 30.000 | Utang pindah dari Rina ke Andi |
| Bayar merchant 20.000 | `WALLET:rina` 20.000 | `WALLET:budi_store` 20.000 | Sama seperti transfer |
| Payout hold 50.000 | `WALLET:rina` 50.000 | `PAYOUT_IN_TRANSIT` 50.000 | Dana dikunci, belum pasti keluar |
| Payout settle | `PAYOUT_IN_TRANSIT` 50.000 | `BANK_CLEARING` 50.000 | Uang benar-benar keluar dari rekening penampung |
| Payout gagal (reversal) | `PAYOUT_IN_TRANSIT` 50.000 | `WALLET:rina` 50.000 | Journal baru dengan `reversal_of = <hold journal>` |
| Settlement provider → bank | `BANK_CLEARING` | `PROVIDER_CLEARING` | Piutang provider lunas (dari reconciliation) |

### Aturan penegakan

| Aturan | Mekanisme |
|---|---|
| Setiap entry `amount > 0`, `direction IN ('D','C')` | `CHECK` constraint |
| Journal minimal 2 entry dan Σ D = Σ C | `CONSTRAINT TRIGGER … DEFERRABLE INITIALLY DEFERRED` yang mengecek saat commit |
| Append-only | Trigger `BEFORE UPDATE OR DELETE` → `RAISE EXCEPTION`; role aplikasi hanya punya `INSERT, SELECT` pada `journals` & `journal_entries` |
| Koreksi | Journal pembalik baru dengan `reversal_of` (FK ke journal asal, `UNIQUE` agar satu journal hanya bisa dibalik sekali) |
| Traceability | Journal menyimpan `source_type`, `source_id`, `purpose` (transfer/payout/…; `UNIQUE (source_type, source_id, purpose)`), `correlation_id`, `idempotency_key_id` |
| Saldo user/merchant | `accounts.balance` di-update **dalam transaksi yang sama** dengan insert entries; `CHECK (balance >= 0)` untuk akun wallet |
| Saldo akun sistem | Tidak materialized, lihat [ADR-L07](ADR-L07-hot-accounts.md) |

### Verifikasi invariant

Job berkala (worker, tiap 5 menit, plus on-demand di ops console):
- `accounts.balance == Σ entries` per akun wallet → selisih dihitung di metrik `ledger_balance_drift_total` (target **selalu 0**, alert jika > 0).
- Σ seluruh debit global = Σ seluruh kredit global → `ledger_imbalance_total`.

## Reason

- Invariant keuangan paling penting (uang tidak tercipta/hilang) **ditegakkan database**, bukan hanya kode aplikasi.
- Saldo materialized membuat cek saldo O(1) di bawah row lock ([ADR-L03](ADR-L03-concurrency-pessimistic-locking.md)), sementara entries tetap menjadi bukti yang bisa diverifikasi.
- Memodelkan saldo user sebagai *liability* membuka reconciliation: total liability harus didukung oleh asset nyata.
- Properti utama event sourcing (immutability, audit) didapat tanpa event store.

## Trade-offs

- Dua sumber kebenaran untuk saldo user (kolom + entries), sehingga ada risiko drift. Mitigasinya: update di transaksi yang sama + job verifikasi + metrik.
- Lebih banyak baris per transaksi (1 journal + ≥2 entries + update akun).
- Constraint trigger deferred menambah biaya kecil saat commit.
- Konsep debit/kredit untuk liability terasa "terbalik" bagi yang belum terbiasa dan perlu dijelaskan di README.

## Future Migration

| Pemicu | Langkah |
|---|---|
| `journal_entries` > ±100 juta baris / query riwayat lambat | Partisi per bulan (`created_at`), arsip partisi lama ke cold storage |
| Fee / FX diperkenalkan | Journal multi-kaki (≥3 entry) sudah didukung model; tambah akun revenue/FX |
| Kebutuhan reporting akuntansi | Ekspor journal ke report DB / general ledger eksternal via outbox |
| Throughput ledger jadi bottleneck tunggal | Pisahkan ledger menjadi service tersendiri (V3), dengan API `post_journal` yang idempoten |

## Interview angle

- **"Kenapa tidak cukup kolom balance?"** → Tidak ada invariant yang bisa diverifikasi; bug yang menambah saldo tidak akan terdeteksi. Dengan double-entry, uang yang tercipta pasti membuat ledger tidak seimbang.
- **"Saldo user itu debit atau kredit?"** → Dari sisi perusahaan saldo user adalah liability, jadi normalnya kredit. Top-up = kredit wallet, debit clearing.
- **"Bagaimana memperbaiki transaksi yang salah?"** → Tidak pernah UPDATE. Buat journal reversal yang mereferensikan journal asal, sehingga jejak salah dan koreksinya tetap ada.
- **"Bagaimana tahu saldo materialized tidak drift?"** → Ditulis dalam transaksi yang sama, plus job verifikasi dan metrik `ledger_balance_drift_total` dengan alert.

## Related

[ADR-L03](ADR-L03-concurrency-pessimistic-locking.md) · [ADR-L04](ADR-L04-money-representation.md) · [ADR-L06](ADR-L06-payout-unknown-outcome.md) · [ADR-L07](ADR-L07-hot-accounts.md) · [ADR-004](ADR-004-postgresql.md) · [ADR-011](ADR-011-transaction-management.md)
