# ADR-L02 — Idempotency Transaksional di PostgreSQL

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Client (browser, merchant server, mobile) akan mengirim ulang request ketika timeout, user klik dua kali, atau koneksi putus. Dari sisi client, *"tidak ada response"* tidak berarti *"tidak terjadi"*. Tanpa idempotency, retry yang benar dari sisi client menghasilkan double-debit.

Pertanyaan yang harus dijawab desain:
1. Di mana key disimpan agar **atomik dengan efek finansial**?
2. Apa yang terjadi kalau dua request dengan key yang sama datang **bersamaan**?
3. Bagaimana dengan key yang sama tetapi payload berbeda?
4. Bagaimana dengan operasi yang harus memanggil pihak luar (provider), yang tidak bisa ada di dalam DB transaction ([ADR-011](ADR-011-transaction-management.md))?

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. Key di Redis (`SETNX` + TTL), efek di Postgres | Cepat | **Tidak atomik**: crash di antara `SETNX` dan commit DB → key ada tanpa efek (retry ditolak) atau efek ada tanpa key (retry dobel) |
| B. Unique constraint di tabel bisnis (`transfers.client_ref`) | Sederhana, atomik | Tidak bisa me-*replay* response, tidak mendeteksi payload berbeda, harus diulang di setiap tabel |
| C. **Tabel `idempotency_keys` di Postgres, ditulis dalam transaksi yang sama dengan efek** | Atomik, bisa replay, deteksi payload berbeda, generik untuk semua endpoint | Menambah satu insert per request; perlu pola tambahan untuk panggilan eksternal |
| D. Status `IN_PROGRESS` di tx terpisah + lease | Mendukung operasi panjang | Butuh pemulihan key yang macet; lebih kompleks untuk operasi sinkron yang sebenarnya cukup satu tx |

## Decision

**Opsi C**, dengan dua pola.

### Skema (ringkas; final di desain database)
`idempotency_keys(id, client_type, client_id, endpoint, key, request_hash, response_status, response_body, resource_type, resource_id, created_at, expires_at)`, `uq_idempotency_keys_scope UNIQUE (client_type, client_id, endpoint, key)` (final di `07-db-shared-tables.md`).

- `client_id` = user ID (JWT) atau merchant ID (API key). Key milik client A tidak pernah bentrok dengan client B.
- `request_hash` = SHA-256 dari `method + path + canonical JSON body` (key diurutkan, whitespace dihilangkan).
- Header `Idempotency-Key` **wajib** pada endpoint yang memindahkan uang (`POST /transfers`, `/topups`, `/payment-intents/{id}/confirm`, `/refunds`, `/payouts`). Tanpa header → `400 IDEMPOTENCY_KEY_REQUIRED`.

### Pola (a) — operasi sinkron, satu transaksi (transfer, confirm payment, refund, create payout)
```text
BEGIN
  INSERT INTO idempotency_keys (...) VALUES (...)
    ON CONFLICT (client_id, endpoint, key) DO NOTHING RETURNING id
  ├─ 1 row  → key baru → lanjut
  └─ 0 row  → SELECT key yang ada
              ├─ request_hash beda       → ROLLBACK → 422 IDEMPOTENCY_KEY_REUSED
              └─ sama, sudah ada response → ROLLBACK → replay response
                                            + header Idempotent-Replayed: true
  lock akun → validasi → journal → update saldo           (ADR-L01, ADR-L03)
  UPDATE idempotency_keys SET response_status, response_body, resource_id
COMMIT → kirim response
```

**Request duplikat bersamaan:** request kedua akan **menunggu di unique index** sampai transaksi pertama selesai.
- Pertama **commit** → `ON CONFLICT` kedua menghasilkan 0 row → SELECT melihat response tersimpan → replay.
- Pertama **rollback** (crash, error infra) → insert kedua berhasil → diproses normal sebagai request pertama.
- Menunggu melebihi `lock_timeout` (2 detik, ADR-011) → `409 REQUEST_IN_PROGRESS`; client boleh retry dengan key yang sama.

**Hasil apa yang disimpan untuk replay:**

| Hasil | Disimpan? | Alasan |
|---|---|---|
| 2xx sukses | Ya | Inti idempotency |
| 422 penolakan domain (`INSUFFICIENT_BALANCE`, `LIMIT_EXCEEDED`) | **Ya**, commit hanya baris key tanpa efek | Hasil deterministik; key yang sama harus memberi jawaban yang sama. Client memakai key baru untuk percobaan baru |
| 400 validasi (body rusak) | Tidak | Request tidak pernah diterima secara bisnis |
| 5xx / error infra | Tidak (tx rollback) | Aman di-retry dengan key yang sama |

### Pola (b) — recovery point, untuk operasi yang memanggil provider (create top-up)
```text
tx1:  INSERT idempotency_key (response_status = NULL, resource = topup_id)
      INSERT topups (status = PENDING, recovery_point = CREATED)   COMMIT
call: POST mock-provider /v1/charges  (Idempotency-Key = topup_id)   ← di luar tx
tx2:  UPDATE topups SET provider_ref, checkout_url, recovery_point = PROVIDER_CREATED
      UPDATE idempotency_keys SET response_status/body             COMMIT
```
`recovery_point` disimpan di **`topups`** (objek bisnis yang durable), bukan di idempotency key yang dihapus setelah 7 hari. Dengan begitu job `topup.recover` bisa menyelesaikan top-up yang macet walaupun client tidak pernah retry. Retry client dengan key yang sama (response masih NULL) memuat top-up dan melanjutkan dari `recovery_point`-nya:
- `CREATED` → ulangi panggilan provider. Aman, karena provider menerima key = `topup_id`.
- `PROVIDER_CREATED` → replay response.

Kalau provider gagal terus, top-up tetap `PENDING` dan job sweeper menandai `EXPIRED` setelah batas waktu. Lihat [ADR-L08](ADR-L08-inbound-webhook-inbox.md) untuk webhook yang datang terlambat.

### Catatan implementasi pola (b) (Sprint 5)
- `idempotency.Reserve` (tx1: key + resource + `resource_id` di key) dan `idempotency.Complete` (dipanggil di tx2 bersama pembaruan resource). Retry saat respons masih NULL mengembalikan resource yang sama untuk dilanjutkan, bukan `REQUEST_IN_PROGRESS`.
- **Body ke provider harus identik di setiap retry.** Semua field diturunkan dari baris resource (top-up), termasuk `correlation_id`. Versi awal mengirim request ID yang berubah di setiap request, sehingga retry setelah timeout selalu ditolak provider (`422 idempotency_mismatch`) dan top-up tidak pernah bisa dilanjutkan. Ditangkap oleh `TestTopUp_ProviderTimeout_RetryResumesFromRecoveryPoint`.

### Retensi
`expires_at = created_at + 7 hari`. Job cleanup harian menghapus key kedaluwarsa secara batch. Setelah kedaluwarsa, key yang sama dianggap baru. Ini didokumentasikan di API docs, dan retry wajar oleh client terjadi dalam hitungan menit, bukan minggu.

## Reason

- Kebenaran idempotency bergantung pada **atomisitas dengan efek**. Hanya Postgres (sumber kebenaran ledger) yang bisa memberikannya tanpa distributed transaction.
- Menunggu di unique index memberi perilaku "request kedua melihat hasil yang pertama" tanpa status `IN_PROGRESS` yang bisa macet.
- Pola recovery point (ala Stripe) memisahkan panggilan jaringan dari transaksi DB tanpa kehilangan jaminan.

## Trade-offs

- Satu insert + satu update tambahan per request uang (murah dibanding biaya double-debit).
- Request duplikat bersamaan menahan koneksi selama menunggu. Dibatasi oleh `lock_timeout`.
- Menyimpan response body menambah storage. Dibatasi retensi 7 hari.
- Canonical JSON harus konsisten; perubahan format body (mis. field opsional ditambah client) bisa membuat hash berbeda. Ini didokumentasikan.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Tabel key besar / cleanup berat | Partisi `idempotency_keys` per hari, drop partisi lama (bukan `DELETE`) |
| Banyak instance API + latency kritis | Tambah *fast-path* Redis untuk menolak duplikat in-flight, **tetap** dengan Postgres sebagai penentu ([ADR-007](ADR-007-redis-not-in-v1.md)) |
| Ledger dipisah menjadi service | Key ikut diteruskan sebagai `idempotency_key` ke API `post_journal` |

## Interview angle

- **"Kenapa tidak di Redis?"** → Redis dan Postgres tidak bisa commit atomik bersama. Crash di antara keduanya memecahkan jaminan.
- **"Dua request key sama datang bersamaan?"** → Yang kedua menunggu di unique index. Kalau yang pertama commit, yang kedua replay; kalau rollback, yang kedua diproses.
- **"Kenapa menyimpan response 422?"** → Supaya jawaban untuk key yang sama deterministik. Kalau tidak disimpan, retry setelah saldo bertambah bisa sukses diam-diam, padahal user mengira transaksinya gagal.
- **"Bagaimana kalau perlu memanggil API luar?"** → Recovery point: catat lokal dulu, panggil di luar tx dengan idempotency key ke provider, lalu catat hasilnya.

## Related

[ADR-L01](ADR-L01-double-entry-ledger.md) · [ADR-L03](ADR-L03-concurrency-pessimistic-locking.md) · [ADR-L05](ADR-L05-mock-provider.md) · [ADR-L08](ADR-L08-inbound-webhook-inbox.md) · [ADR-003](ADR-003-rest-api-openapi.md) · [ADR-007](ADR-007-redis-not-in-v1.md) · [ADR-011](ADR-011-transaction-management.md)
