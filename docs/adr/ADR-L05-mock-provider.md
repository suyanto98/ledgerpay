# ADR-L05 — Mock Payment Provider sebagai Service Terpisah dengan Failure Injection

> Status: **Accepted** · Tanggal: 2026-10-01 · Berlaku untuk: LedgerPay

## Context / Problem

Top-up, payout, dan reconciliation bergantung pada pihak luar (payment gateway dan bank). Nilai portfolio LedgerPay justru ada pada **cara sistem bertahan saat pihak luar berperilaku buruk**: timeout, webhook duplikat, webhook terlambat atau tidak berurutan, laporan settlement yang tidak cocok. Semua perilaku ini harus bisa **di-reproduce secara deterministik** di test otomatis dan di demo live.

## Options

| Opsi | Kelebihan | Kekurangan |
|---|---|---|
| A. Sandbox PG nyata (Midtrans/Xendit) | Realistis, terlihat "sungguhan" | Butuh akun bisnis; tidak bisa memaksa timeout/duplikat secara deterministik; bergantung internet saat demo; tidak bisa dijalankan di CI |
| B. Mock in-process (interface + fake di Go) | Cepat, mudah untuk unit test | Tidak ada batas jaringan nyata (timeout, signature, HTTP retry); tidak bisa didemokan sebagai "sistem lain" |
| C. **Service terpisah `mock-provider` (Go) dengan admin API untuk skenario** | Batas jaringan nyata; failure deterministik; berjalan di Compose & CI; bisa didemokan | Harus dibangun dan dipelihara; risiko kontraknya tidak mirip provider nyata |
| D. Tool generik (WireMock, Prism) | Tidak perlu menulis kode | Sulit memodelkan perilaku stateful (webhook asinkron, settlement report, idempotency di sisi provider) |

## Decision

**Opsi C.** Fake in-process (Opsi B) tetap dipakai untuk unit test service layer. `mock-provider` dipakai untuk integration test, E2E, dan demo.

### Endpoint (sketsa; kontrak final di Step 5)

| Area | Endpoint | Perilaku |
|---|---|---|
| PG | `POST /v1/charges` (header `Idempotency-Key`) | Buat charge → `{charge_id, checkout_url, status: PENDING}`; key sama → response sama |
| PG | `GET /checkout/{charge_id}` | Halaman hosted sederhana: tombol **Pay** / **Fail** (untuk demo HR) |
| PG | (keluar) webhook `charge.succeeded` / `charge.failed` → LedgerPay | Ditandatangani; mengikuti skenario aktif |
| Bank | `POST /v1/payouts` (header `Idempotency-Key = payout_id`) | Proses transfer keluar; menghormati idempotency |
| Bank | `GET /v1/payouts/{idempotency_key}` | Status query: `SUCCEEDED / FAILED / PROCESSING / NOT_FOUND` |
| Recon | `GET /v1/settlements/{date}.csv` | Laporan harian (charge & payout) untuk reconciliation |
| Admin | `PUT /admin/scenario` · `GET /admin/scenario` | Toggle skenario global |
| Admin | `GET /admin/transfers` | Bukti di demo: "bank hanya mengirim 1×" |
| Admin | `POST /admin/reset` | Reset state + seed deterministik |

### Skenario failure

| Skenario | Efek |
|---|---|
| `ok` | Normal |
| `timeout_after_process` | Diproses (uang "terkirim") tetapi response ditahan sampai client timeout → **unknown outcome** |
| `timeout_before_process` | Tidak diproses, response ditahan |
| `error_5xx` | 503 tanpa memproses |
| `decline` | Ditolak secara definitif |
| `webhook_duplicate:n` | Webhook dikirim n kali |
| `webhook_delay:<dur>` | Webhook dikirim terlambat |
| `webhook_out_of_order` | Event lama dikirim setelah event final |
| `webhook_bad_signature` | Signature salah (uji penolakan) |
| `settlement_mismatch:<type>` | Laporan settlement sengaja berbeda (missing / amount / status) |

### Cara memilih skenario

1. **Global toggle** via `PUT /admin/scenario`: dipakai di demo live dan E2E.
2. **Per-request header `X-Mock-Scenario`**: dipakai test harness yang memanggil mock langsung (contract test mock ↔ client LedgerPay).
3. **Magic values** (seperti nomor kartu test Stripe) untuk E2E lewat LedgerPay tanpa mengubah state global. Contoh: rekening tujuan berakhiran `...0408` = `timeout_after_process`. LedgerPay **tidak** tahu-menahu soal skenario; kodenya tetap kode produksi.

### Webhook signing
Header `Provider-Signature: t=<unix_ts>,v1=<hex(HMAC-SHA256(secret, t + "." + raw_body))>`. Secret per environment. Format ini sama dengan yang dipakai LedgerPay untuk webhook keluar ke merchant, sehingga kode verifikasinya bisa dibagi.

### Determinisme
Semua delay dan pilihan acak memakai `MOCK_SEED`. Skrip demo memanggil `/admin/reset` sebelum mulai, sehingga hasilnya identik setiap kali dijalankan.

## Reason

- Failure yang bisa di-reproduce adalah syarat untuk **menguji** dan **mendemokan** reliability. Sandbox nyata tidak memberikannya.
- Proses terpisah membuat timeout, signature, dan retry HTTP terjadi di batas jaringan yang nyata. Ini satu-satunya proses terpisah yang dibenarkan, karena memang merepresentasikan sistem eksternal ([ADR-001](ADR-001-modular-monolith.md)).
- Kontrak dibuat mengikuti pola umum PG di Indonesia dan Stripe (idempotency key, hosted checkout, webhook ber-signature, status query), sehingga perpindahan ke provider nyata tidak mengubah desain inti.

## Trade-offs

- Ada kode tambahan yang harus dipelihara (perkiraan ±1 minggu kerja).
- Risiko "mock yang terlalu baik hati": kontrak tidak mencerminkan keanehan provider nyata. Mitigasinya: daftar skenario di atas diambil dari failure scenario Step 3, bukan dari happy path.
- Interviewer mungkin bertanya "kenapa tidak integrasi sungguhan?". Jawabannya ada di bagian Interview angle.

## Future Migration

| Pemicu | Langkah |
|---|---|
| Integrasi provider nyata | Implementasi baru dari interface `PaymentProvider` / `PayoutProvider`; mock tetap dipakai di CI |
| Banyak provider | Routing per provider; `provider` menjadi kolom di charge/payout/inbox (sudah disiapkan) |

## Interview angle

- **"Kenapa tidak pakai sandbox Midtrans?"** → Sandbox tidak bisa memaksa *"proses sukses tapi response hilang"*. Justru skenario itu yang paling penting untuk dibuktikan, dan harus bisa diulang di CI.
- **"Bagaimana memastikan mock tidak terlalu ideal?"** → Skenario diturunkan dari daftar failure (timeout setelah proses, duplikat, out-of-order, settlement mismatch), bukan dari happy path.
- **"Apakah kode produksi tahu sedang dites?"** → Tidak. Skenario dipilih di sisi mock (global toggle / magic value), bukan oleh flag di LedgerPay.

## Related

[ADR-L02](ADR-L02-idempotency.md) · [ADR-L06](ADR-L06-payout-unknown-outcome.md) · [ADR-L08](ADR-L08-inbound-webhook-inbox.md) · [ADR-001](ADR-001-modular-monolith.md)
