# ADR-L04 — Representasi Uang: BIGINT Minor Unit

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Uang harus disimpan, dihitung, dan dikirim lewat API tanpa kehilangan presisi. `float64` tidak bisa merepresentasikan banyak nilai desimal secara eksak (`0.1 + 0.2 ≠ 0.3`). Di sisi lain, JavaScript (frontend Vue, client merchant) merepresentasikan angka JSON sebagai float64 yang hanya presisi sampai 2^53.

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. `float` / `double` | Mudah | Tidak presisi. **Ditolak mutlak** untuk uang |
| B. `NUMERIC(p,s)` + library decimal di Go | Presisi arbitrer, mendukung pecahan | Lebih lambat, alokasi memori di Go, mudah tercampur dengan operasi pembagian yang menghasilkan pecahan tak terbatas |
| C. **`BIGINT` minor unit + kode currency** | Eksak, cepat, aritmetika integer native di Go & SQL, tidak ada pembulatan tersembunyi | Butuh konversi untuk tampilan; harus memilih exponent per currency |
| D. Library money (mis. `Rhymond/go-money`) | API nyaman | Dependensi tambahan; tetap butuh keputusan representasi DB |

## Decision

**Opsi C.**

| Aspek | Keputusan |
|---|---|
| DB | `amount BIGINT NOT NULL`, `currency CHAR(3) NOT NULL DEFAULT 'IDR'`, `CHECK (amount > 0)` di entries |
| Exponent | Ikuti **ISO 4217**: IDR exponent **2** (Rp 10.000 = `1000000`). UI menampilkan tanpa desimal karena sen tidak dipakai dalam praktik |
| Go | Tipe `money.Amount` (int64) dengan operasi `Add/Sub` yang memeriksa overflow; tidak ada konversi ke float di jalur bisnis |
| JSON / API | `"amount": "1000000"` sebagai **string** berisi integer minor unit, plus `"currency": "IDR"` |
| Validasi input | String harus cocok `^[1-9][0-9]{0,17}$`; batas maksimum per transaksi dari konfigurasi (BR9) |
| Single currency | V1 hanya IDR; transaksi dengan currency berbeda ditolak (`422 CURRENCY_NOT_SUPPORTED`) |
| Pembulatan | Tidak ada di V1 karena tidak ada operasi pembagian. Placeholder untuk fee (P3): bulatkan **ke bawah untuk fee yang dibebankan ke user** (user tidak pernah membayar lebih dari tarif), sisa pecahan dicatat eksplisit |

**Kenapa string di JSON:** int64 maksimum ±9,22 × 10^18, sedangkan `Number.MAX_SAFE_INTEGER` JS ±9 × 10^15. Dengan exponent 2, batas aman JS ≈ Rp 90 triliun. Nilai ini tidak akan tercapai di demo, tetapi **kontrak API tidak boleh bergantung pada asumsi client**. Ini juga praktik umum di API pembayaran. Biayanya kecil (parse string di client), dan bug presisi tidak mungkin terjadi.

**Batas overflow:** BIGINT dengan exponent 2 menampung ±Rp 92 kuadriliun, jauh di atas kebutuhan. `Add` tetap memeriksa overflow sebagai disiplin.

## Reason

- Aritmetika ledger hanya penjumlahan dan pengurangan nilai yang sudah eksak, sehingga integer adalah representasi yang paling sederhana dan paling benar.
- Constraint DB (`CHECK amount > 0`, `CHECK balance >= 0`) bekerja natural pada integer.
- Mengikuti ISO 4217 menghindari migrasi data kalau suatu saat currency dengan sen (USD) ditambahkan.

### Kontras dengan PayRun ([ADR-P06](ADR-P06-decimal-rounding.md))
PayRun memakai `NUMERIC` + decimal karena payroll melibatkan **persentase, prorata, dan tarif per jam** yang menghasilkan pecahan sebelum dibulatkan. LedgerPay hanya memindahkan nilai yang sudah final. Domain yang berbeda menghasilkan representasi yang berbeda, dan ini topik interview yang bagus.

## Trade-offs

- Konversi tampilan (`1000000` → "Rp10.000") harus konsisten di frontend. Dibuat satu helper format.
- Exponent 2 untuk IDR membuat angka terlihat "100× lebih besar" di DB dan log. Ini didokumentasikan di README dan nama kolom memakai akhiran `_minor` jika ambigu.
- String di JSON sedikit kurang ergonomis untuk merchant. Diatasi dengan contoh di OpenAPI.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Multi-currency | Tabel `currencies(code, exponent)`, akun per currency, journal FX dengan akun konversi; tidak ada journal yang mencampur currency |
| Fee berbasis persentase | Fungsi pembulatan terpusat + test properti "Σ setelah fee = nominal awal" |

## Interview angle

- **"Kenapa tidak float?"** → Float tidak bisa merepresentasikan 0,1 secara eksak; error menumpuk di jutaan transaksi.
- **"Kenapa amount di JSON berupa string?"** → JavaScript kehilangan presisi di atas 2^53. Kontrak API tidak boleh bergantung pada bahasa client.
- **"Kenapa PayRun pakai NUMERIC padahal LedgerPay pakai integer?"** → Payroll menghitung pecahan (prorata, persentase) sebelum dibulatkan; ledger hanya memindahkan nilai final.

## Related

[ADR-L01](ADR-L01-double-entry-ledger.md) · [ADR-003](ADR-003-rest-api-openapi.md) · [ADR-P06](ADR-P06-decimal-rounding.md)
