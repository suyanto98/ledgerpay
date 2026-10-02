# ADR-L08 — Webhook Masuk via Inbox Table

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Hasil top-up diketahui dari webhook payment provider. Webhook masuk tidak tepercaya dan tidak andal:

| Masalah | Contoh |
|---|---|
| Palsu | Penyerang mengirim `charge.succeeded` palsu untuk mendapat saldo gratis |
| Replay | Webhook asli lama dikirim ulang oleh penyerang |
| Duplikat | Provider me-*retry* karena response kita lambat |
| Terlambat | Webhook sukses tiba setelah top-up kita tandai `EXPIRED` |
| Tidak berurutan | `charge.pending` tiba setelah `charge.succeeded` |
| Lonjakan / dependency lambat | Provider punya timeout pendek (±10 dtk); kalau pemrosesan kita lambat, provider menganggap gagal dan retry |

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. Proses sinkron di handler (verifikasi → update top-up → journal → 200) | Sederhana, latency hasil rendah | Pemrosesan lambat/error → provider retry → beban ganda; tidak ada jejak event mentah; bug pemrosesan = event hilang |
| B. **Inbox: verifikasi → simpan event (unique) → 200 → worker memproses** | Response cepat; dedupe di DB; event mentah tersimpan untuk audit & replay; pemrosesan bisa di-retry tanpa bergantung provider | Ada komponen async tambahan; hasil sedikit tertunda (detik) |
| C. Publish ke message broker | Skalabel | Broker tambahan ([ADR-006](ADR-006-background-processing-postgres-queue.md) menolak); tetap butuh dedupe |

## Decision

**Opsi B.**

### Alur
```text
POST /v1/webhooks/provider
  1. Baca raw body (sebelum parse JSON)
  2. Verifikasi Provider-Signature: HMAC-SHA256(secret, t + "." + raw_body), constant-time compare
       └─ gagal → 401
  3. Cek timestamp |now − t| ≤ 5 menit
       └─ di luar window → 400 (mencegah replay)
  4. INSERT INTO webhook_inbox ... ON CONFLICT (provider, provider_event_id) DO NOTHING
       ├─ baru       → 200
       ├─ duplikat   → 200 (provider berhenti retry; tidak diproses ulang)
       └─ DB error   → 500 (biarkan provider retry)
Worker (SKIP LOCKED) → proses event → status PROCESSED / IGNORED
```

> **(Step 12 C1)** Claim worker menghormati lease: `status='RECEIVED' AND next_attempt_at <= now() AND (locked_until IS NULL OR locked_until < now())`. `attempts` hanya naik saat percobaan gagal atau lease kedaluwarsa, bukan setiap claim. Fencing di dalam tx = `SELECT … WHERE locked_by=$w AND status='RECEIVED' FOR UPDATE`, lalu satu `UPDATE` status final di akhir tx (Step 7 `01` §2.6).

### Skema (ringkas; final di Step 4)
`webhook_inbox(id, provider, provider_event_id, event_type, resource_ref, payload JSONB, raw_body BYTEA, signature_ts, received_at, status [RECEIVED|PROCESSED|IGNORED|DEAD], attempts, next_attempt_at, last_error, processed_at)`, `UNIQUE (provider, provider_event_id)`.

### Transisi monoton saat memproses

| Status top-up saat ini | Event `charge.succeeded` | Event `charge.failed` | Event `charge.pending` |
|---|---|---|---|
| `PENDING` | → `SUCCEEDED` + journal | → `FAILED` | no-op (`IGNORED`) |
| `SUCCEEDED` | no-op (duplikat logis) | **konflik** → flag recon, tidak dibalik | no-op |
| `FAILED` | **konflik** → flag recon | no-op | no-op |
| `EXPIRED` (sweeper lokal) | → `SUCCEEDED` + journal + metrik `webhook_late_total` | → `FAILED` | no-op |

Aturan:
- Transisi memakai `UPDATE topups … WHERE id = $id AND status = $expected` dalam transaksi yang sama dengan journal dan update inbox. Event yang diproses dua kali (mis. worker crash sebelum menandai `PROCESSED`) tidak menghasilkan journal kedua.
- **`EXPIRED` adalah status lunak.** Provider adalah sumber kebenaran apakah uang benar-benar dibayar. Webhook sukses yang terlambat tetap dikreditkan, karena menolaknya berarti uang user hilang.
- Konflik antar status final tidak pernah dibalik otomatis. Kasus ini masuk antrean reconciliation.
- Validasi isi: `amount` dan `currency` event harus sama dengan top-up. Kalau tidak cocok → `DEAD` + alert (indikasi bug provider atau serangan).

### Poison event
Error saat memproses → `attempts++`, backoff exponential + jitter. Setelah **N = 8** percobaan → status `DEAD`, metrik `webhook_inbox_dead_total`, dan alert. Ops bisa *replay* event `DEAD` dari ops console setelah penyebabnya diperbaiki.

### Amandemen (Step 6): verify-by-fetch (P1)
Sebelum menerapkan `charge.succeeded`, worker inbox memanggil `GET /v1/charges/{id}` ke provider. Kredit hanya diberikan kalau status dan amount dari provider cocok. Ini lapisan di atas HMAC: kalau secret webhook bocor, webhook palsu tetap tidak bisa menciptakan saldo (Step 6 T-LP). Kalau provider tidak bisa dihubungi, event di-retry dengan backoff yang sama.

## Reason

- Response cepat memutus ketergantungan antara retry provider dan performa pemrosesan internal.
- Dedupe di DB dengan unique constraint menghasilkan jaminan yang bisa dibuktikan, bukan pengecekan di aplikasi yang bisa race.
- Raw body yang tersimpan memungkinkan audit ("apa persisnya yang dikirim provider?") dan replay setelah bug diperbaiki.
- Transisi monoton membuat urutan kedatangan tidak relevan bagi kebenaran.

## Trade-offs

- Hasil top-up tertunda beberapa ratus milidetik sampai detik (polling worker). Bisa dikurangi dengan `LISTEN/NOTIFY` jika perlu.
- Raw body menambah storage. Retensi inbox: 90 hari, lalu diarsipkan.
- Satu tabel dan satu loop worker tambahan untuk dipelihara.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Volume webhook tinggi | Partisi `webhook_inbox` per bulan; beberapa worker paralel (SKIP LOCKED sudah mendukung) |
| Banyak provider | Verifikator signature per provider (interface), kolom `provider` sudah ada |
| Provider mendukung pull API event | Sinkronisasi berkala sebagai jaring pengaman untuk webhook yang hilang total |

## Interview angle

- **"Webhook datang dua kali?"** → Unique `(provider, provider_event_id)` di inbox, plus transisi state monoton. Duplikat dibalas 200 tanpa efek.
- **"Bagaimana mencegah webhook palsu atau replay?"** → HMAC atas raw body dengan constant-time compare, plus window timestamp 5 menit.
- **"Webhook sukses datang setelah top-up kamu anggap expired?"** → Tetap dikreditkan, karena provider adalah sumber kebenaran pembayaran. Expired hanya status lunak lokal.
- **"Kenapa tidak diproses langsung di handler?"** → Provider punya timeout pendek. Pemrosesan yang lambat memicu retry dan beban ganda; inbox juga menyimpan bukti mentah untuk replay.

## Related

[ADR-L02](ADR-L02-idempotency.md) · [ADR-L05](ADR-L05-mock-provider.md) · [ADR-L06](ADR-L06-payout-unknown-outcome.md) · [ADR-006](ADR-006-background-processing-postgres-queue.md) · [ADR-011](ADR-011-transaction-management.md)
