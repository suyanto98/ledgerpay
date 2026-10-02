# Spike 2026-10-02 — DB foundations (Sprint 0)

> Kode: `spikes/db/` di branch **`spike/db-foundations`** (module Go terpisah, **kode buang**: tidak di-merge ke `main`).
> Jalankan: `git switch spike/db-foundations && cd spikes/db && go test -count=1 -v ./...` (butuh Docker).

## Pertanyaan yang dijawab

| # | Asumsi desain | Sumber | Hasil |
|---|---|---|---|
| 1 | Test integration bisa memakai Postgres sungguhan dengan satu DB per test hasil clone `TEMPLATE`, < 1 dtk per test | Step 8 `02` §2 | ✅ **avg 14 ms, worst 22 ms** per clone (20 clone). Seluruh suite 6 test ± 3,7 dtk termasuk start container |
| 2a | Deferred constraint trigger menolak journal tidak seimbang **saat COMMIT**, tetapi membiarkan INSERT satu per satu | ADR-L01, design `09` | ✅ setelah perbaikan bug (lihat di bawah). INSERT lolos, COMMIT gagal `23514 / ck_journals_balanced` |
| 2a' | `CHECK (is_system OR balance >= 0)` menolak saldo negatif sebagai jaring terakhir | LI-02 | ✅ `23514 / ck_accounts_balance_non_negative` |
| 2b | `FOR NO KEY UPDATE` pada akun **tidak** memblok INSERT `journal_entries` (cek FK = `KEY SHARE`), sedangkan `FOR UPDATE` **memblok** | ADR-L03 amandemen Step 7 | ✅ NO KEY: INSERT selesai ± 36 ms. FOR UPDATE: INSERT menunggu sampai `lock_timeout` → `55P03` |

## 🐞 Temuan: bug di trigger keseimbangan (design `09`, Step 4 `03`)

Rancangan awal:
```sql
jid uuid := CASE TG_TABLE_NAME WHEN 'journals' THEN NEW.id ELSE NEW.journal_id END;
```
Saat trigger berjalan di tabel `journals`, PL/pgSQL tetap mengevaluasi `NEW.journal_id`, padahal kolom itu tidak ada di `journals`. Hasilnya `ERROR: record "new" has no field "journal_id"` (`42703`) untuk **setiap** journal, termasuk yang seimbang. Jadi trigger yang dimaksudkan sebagai jaring pengaman justru akan mematikan seluruh posting ledger.

Perbaikan (sudah diterapkan di spike, design `09`, dan arsip Step 4 `03`):
```sql
IF TG_TABLE_NAME = 'journals' THEN jid := NEW.id; ELSE jid := NEW.journal_id; END IF;
```
`IF` hanya mengevaluasi cabang yang benar-benar dijalankan.

**Pelajaran:** review dokumen tidak menangkap bug ini, sementara menjalankan kodenya sekali langsung menangkapnya. Itu sebabnya Sprint 0 punya spike, dan kenapa setiap trigger harus punya test (design `22`).

## Pelajaran teknis (untuk dipahami & dipertahankan di interview)

1. **Kenapa clone `TEMPLATE` cepat:** Postgres menyalin file database di level storage. Menjalankan ulang semua migrasi per test jauh lebih lambat. Syaratnya, template tidak boleh punya koneksi aktif saat di-clone, jadi koneksi migrasi harus ditutup lebih dulu (`testdb.Start`).
2. **Deferred vs immediate constraint:** journal dan entry di-INSERT satu per satu. Kalau keseimbangan dicek per statement, journal pertama selalu "tidak seimbang". `DEFERRABLE INITIALLY DEFERRED` menunda cek sampai COMMIT, saat semua entry sudah ada.
3. **Mode row lock Postgres (dari yang lemah ke yang kuat):** `FOR KEY SHARE` < `FOR SHARE` < `FOR NO KEY UPDATE` < `FOR UPDATE`.
   - Cek FK dari tabel anak mengambil `KEY SHARE` di baris induk.
   - `FOR UPDATE` bentrok dengan `KEY SHARE`; `FOR NO KEY UPDATE` tidak.
   - Karena update saldo tidak mengubah kolom key, `FOR NO KEY UPDATE` sudah cukup untuk menserialkan dua transfer (`NO KEY UPDATE` vs `NO KEY UPDATE` saling memblok) **tanpa** menahan INSERT entry dari transaksi lain.
   - Fakta tambahan: `UPDATE` biasa yang tidak mengubah kolom key juga mengambil lock `NO KEY UPDATE` secara implisit.
4. **SQLSTATE sebagai kontrak:** test memeriksa `code` dan `constraint name`, bukan pesan error. Inilah dasar pemetaan error di `platform/db` (design `12` §4): nama constraint yang eksplisit dan stabil memungkinkan error DB dipetakan ke error domain.
5. **`sqlc` menebak nullability dari skema:** parameter `delta` di `UPDATE … SET balance = balance + @delta` di-generate sebagai `*int64`, karena kolom `balance` boleh NULL (akun sistem). Ini wajar, tetapi tipe hasil generate tetap perlu di-review. Di kode produk bisa dipakai `sqlc.arg()` dengan cast eksplisit atau override tipe.

## Implikasi untuk Sprint 1
- Pola `testdb` (container per package + clone template per test) **dipakai** di `internal/platform/db/testdb`, dengan tambahan: koneksi test memakai role `ledgerpay_app` (bukan superuser) agar grant ikut teruji (design `22`).
- Trigger keseimbangan memakai versi `IF`.
- Untuk UUID, `sqlc` sebaiknya di-override ke `github.com/google/uuid.UUID` (lebih ergonomis daripada `pgtype.UUID`).
- Tidak ada ADR yang perlu diganti. Bug ada di detail SQL, bukan di keputusan desain.
