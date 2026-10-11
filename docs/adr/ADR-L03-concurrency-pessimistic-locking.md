# ADR-L03 — Concurrency Saldo: Pessimistic Locking dengan Lock Ordering

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

### Race condition konkret (lost update / double-spend)
Saldo Rina 100.000. Dua transfer 70.000 datang bersamaan, dengan implementasi naif *read → check → write*:

| t | Tx A | Tx B | Saldo di DB |
|---|---|---|---|
| 1 | `SELECT balance` → 100.000 | | 100.000 |
| 2 | | `SELECT balance` → 100.000 | 100.000 |
| 3 | cek 100.000 ≥ 70.000 ✅ | cek 100.000 ≥ 70.000 ✅ | 100.000 |
| 4 | `UPDATE balance = 30.000` | | 30.000 |
| 5 | | `UPDATE balance = 30.000` | 30.000 |
| 6 | COMMIT | COMMIT | **30.000, padahal 140.000 sudah terkirim** |

Di Read Committed (default Postgres), ini terjadi persis seperti tabel di atas.

### Deadlock konkret
Tx A: Rina → Andi (lock Rina lalu Andi). Tx B: Andi → Rina (lock Andi lalu Rina). Masing-masing memegang satu lock dan menunggu yang lain. Postgres mendeteksinya dan membatalkan salah satu dengan `40P01`.

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. **Pessimistic `SELECT … FOR UPDATE` + urutan lock deterministik** | Mudah dipahami, tidak ada retry storm, benar untuk multi-akun | Request ke wallet yang sama ter-serialisasi; perlu disiplin urutan lock |
| B. Optimistic: kolom `version`, `UPDATE … WHERE version = $v` | Tanpa lock saat baca; bagus untuk konflik jarang | Wallet populer (merchant) → konflik terus → **retry storm**; latency tidak stabil |
| C. Isolation `SERIALIZABLE` | DB menjamin hasil setara eksekusi serial | Abort `40001` di bawah contention → retry storm; perilaku sulit dijelaskan & diprediksi; berlaku ke semua query di tx |
| D. Conditional update `UPDATE accounts SET balance = balance - $x WHERE id = $a AND balance >= $x` | Atomik, satu statement, sangat cepat untuk satu akun | Untuk transfer (2 akun + insert entries) tetap perlu urutan agar tidak deadlock; hasil validasi (saldo kurang vs akun tidak ada) kurang ekspresif |
| E. Advisory lock (`pg_advisory_xact_lock(account_id)`) | Fleksibel, bisa mengunci konsep non-row | Lock tidak terikat ke data; mudah lupa; tidak lebih baik dari row lock untuk kasus ini |

## Decision

**Opsi A**, dengan `CHECK (balance >= 0)` sebagai pertahanan terakhir.

```sql
-- di dalam WithTx, Read Committed
SELECT id, balance, status
FROM accounts
WHERE id = ANY($1)          -- semua akun wallet yang terlibat
ORDER BY id                  -- urutan deterministik → tidak ada siklus → tidak ada deadlock
FOR UPDATE;
-- validasi di aplikasi: status aktif, saldo cukup, limit
-- INSERT journal + entries; UPDATE accounts SET balance = …
```

Aturan:
1. **Semua** jalur yang mengubah saldo wallet mengunci lewat satu fungsi repository (`LockAccountsOrdered`). Tidak ada jalur lain.
2. Akun sistem tidak di-lock ([ADR-L07](ADR-L07-hot-accounts.md)); hanya akun wallet user/merchant.
3. **Tidak ada panggilan jaringan di dalam tx** ([ADR-011](ADR-011-transaction-management.md)), sehingga lock hanya dipegang dalam hitungan milidetik.
4. `lock_timeout = 2s` (nilai tunggal dari ADR-011) → kalau terlampaui, `409 RESOURCE_BUSY` (aman di-retry karena idempotent, [ADR-L02](ADR-L02-idempotency.md)).
5. `40P01` tetap ditangani dengan retry terbatas (3×, jitter) sebagai jaring pengaman jika ada jalur yang terlewat. Metrik `db_deadlock_total` seharusnya selalu 0.
6. `CHECK (balance >= 0)` pada akun wallet: kalau ada bug yang melewati validasi aplikasi, DB tetap menolak.

Hasil yang diharapkan pada contoh di atas: Tx B menunggu di `FOR UPDATE` sampai A commit, lalu membaca saldo 30.000 dan ditolak dengan `INSUFFICIENT_BALANCE`.

### Amandemen (desain transaksi & concurrency): `FOR NO KEY UPDATE`
Baris akun dikunci dengan **`FOR NO KEY UPDATE`**, bukan `FOR UPDATE`. Update saldo tidak mengubah kolom key, sedangkan INSERT `journal_entries` mengambil `KEY SHARE` pada akun lewat pemeriksaan FK. `FOR UPDATE` akan memblok `KEY SHARE` itu tanpa alasan, sementara `FOR NO KEY UPDATE` tidak. Semantik serialisasi antar transfer tetap sama, karena `NO KEY UPDATE` vs `NO KEY UPDATE` saling memblok. Detail di `19-tx-concurrency.md` §4.

### Catatan implementasi (Sprint 3)
Fungsi tunggal untuk aturan 1 bernama `LockWalletAccounts` (query sqlc di `db/queries/ledger.sql`), dipanggil hanya dari `ledger.PostJournal`. Aturan 2 sempat dilanggar di Sprint 2: query lock ikut mengunci akun sistem. Pelanggaran itu ditemukan saat review Sprint 3, diperbaiki, dan dijaga oleh `TestLedger_SystemAccountNotLocked`.

## Reason

- Benar untuk kasus multi-akun (transfer, payment) dengan model mental yang sederhana dan bisa dijelaskan dalam satu kalimat.
- Contention pada wallet yang sama memang **harus** diserialisasi secara bisnis. Locking membuatnya eksplisit dan antre secara adil, bukan gagal lalu retry.
- Defense in depth: lock (kebenaran) + CHECK (jaring terakhir) + idempotency (retry aman).

## Trade-offs

- Throughput per wallet dibatasi oleh durasi tx. Merchant besar dengan ribuan pembayaran/detik akan antre. Ini tidak relevan untuk skala portfolio, tetapi dicatat.
- Disiplin urutan lock harus dijaga; dibantu dengan satu fungsi tunggal + concurrency test.
- Koneksi yang menunggu lock tetap memakai slot pool. Dibatasi oleh `lock_timeout`.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Load test menunjukkan antrean lock di wallet merchant | Kredit ke merchant dicatat ke sub-akun pending lalu di-*sweep* berkala, sehingga merchant tidak di-lock per pembayaran |
| Throughput global ledger jadi bottleneck | Sharding akun berdasarkan `account_id` (V3); transfer lintas shard via saga + akun suspense |
| Kebutuhan validasi lebih kompleks lintas banyak baris | Evaluasi ulang SERIALIZABLE untuk jalur tertentu saja, dengan retry yang terukur |

## Interview angle

- **"Kenapa tidak optimistic locking?"** → Optimistic bagus untuk konflik jarang. Wallet merchant justru konflik terus, jadi hasilnya retry storm. Pessimistic membuat antrean yang adil dan latency yang dapat diprediksi.
- **"Bagaimana mencegah deadlock?"** → Lock semua akun dalam urutan ID yang sama, sehingga tidak mungkin terjadi siklus tunggu.
- **"Kenapa tidak SERIALIZABLE saja?"** → Benar, tetapi memindahkan masalah ke retry dan berlaku ke seluruh tx. Saya lebih memilih lock eksplisit pada baris yang memang harus serial.
- **"Kalau validasi di aplikasi salah?"** → `CHECK (balance >= 0)` di DB menolak commit.

## Related

[ADR-L01](ADR-L01-double-entry-ledger.md) · [ADR-L02](ADR-L02-idempotency.md) · [ADR-L07](ADR-L07-hot-accounts.md) · [ADR-011](ADR-011-transaction-management.md)
