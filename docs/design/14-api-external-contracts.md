# Step 5.3 — LedgerPay External Contracts

> Tiga kontrak lintas batas jaringan: (1) LedgerPay → **mock-provider** (PG + Bank), (2) mock-provider → LedgerPay (**webhook masuk**), (3) LedgerPay → **merchant** (webhook keluar).
> Desain mock: ADR-L05. Payout: ADR-L06. Inbox: ADR-L08. Konvensi uang dan waktu sama dengan [`00-index.md`](00-index.md): amount = string minor unit, waktu RFC 3339 UTC.

---

## 1. mock-provider API (dipanggil oleh LedgerPay)

Base URL internal: `http://mock-provider:9090` (compose). Auth LedgerPay → mock: `Authorization: Bearer <MOCK_API_SECRET>` (secret per environment). Endpoint `/admin/*` hanya terbuka di jaringan compose dan dipakai oleh skrip demo/test, tidak pernah oleh kode LedgerPay.

Error mock memakai bentuk ringkas `{ "error": { "code": "…", "message": "…" } }`, meniru PG pada umumnya, bukan RFC 9457. Adapter LedgerPay yang menerjemahkannya ke domain.

### 1.1 `POST /v1/charges` — buat sesi pembayaran (top-up)
| Aspek | Kontrak |
|---|---|
| Header | `Idempotency-Key: <topup_id>` (wajib) |
| Request | `{ "amount": "10000000", "currency": "IDR", "reference": "<topup_id>", "callback_url": "https://<ledgerpay>/api/v1/webhooks/provider", "expires_at": "2026-03-16T08:30:00Z" }` |
| 201 | `{ "id": "ch_01J…", "reference": "<topup_id>", "status": "PENDING", "amount": "10000000", "currency": "IDR", "checkout_url": "http://localhost:9090/checkout/ch_01J…", "created_at": "…" }` |
| Key sama, body sama | 201 dengan body identik (replay) |
| Key sama, body beda | 422 `idempotency_mismatch` |
| Gagal | 400 `invalid_request` · 401 `unauthorized` · 503 `unavailable` (skenario `error_5xx`) · timeout (skenario `timeout_*`) |
| Perilaku LedgerPay | 201 → tx2 (`PROVIDER_CREATED`). 5xx/timeout → biarkan `recovery_point = CREATED`; job `topup.recover` atau retry client mengulang dengan key yang sama |

> **Catatan implementasi Sprint 5:**
> - ID charge/event berformat `ch_<epoch>_<seq>` / `evt_<epoch>_<seq>` (epoch base36 per run). ID berurutan murni (`ch_000001`) dipakai ulang setelah restart atau `/admin/reset`, padahal database LedgerPay tidak ikut di-reset, sehingga `uq_topups_provider_ref` bentrok (ditemukan oleh E2E).
> - LedgerPay mengirim `metadata.correlation_id = topup id` (bukan request ID): body `POST /v1/charges` harus **identik** di setiap retry dengan key yang sama, kalau tidak provider menjawab `422 idempotency_mismatch`.
> - State mock in-memory (bukan DB terpisah); `GET /admin/deliveries` ditambahkan sebagai bukti pengiriman webhook untuk demo/test; `POST /admin/charges/{id}/pay|fail` menggantikan klik tombol untuk otomasi.

### 1.2 `GET /v1/charges/{id}` dan `GET /v1/charges?reference=<topup_id>`
Response `{ "id", "reference", "status": "PENDING|SUCCEEDED|FAILED|EXPIRED", "amount", "currency", "paid_at", "failure_reason" }`. Lookup by `reference` dipakai sweeper sebelum menandai top-up `EXPIRED` (LF-07 AC6) dan oleh `topup.recover` ketika tidak yakin apakah charge sudah dibuat (404 `not_found` → aman membuat charge).

### 1.3 `GET /checkout/{charge_id}` — halaman hosted
HTML sederhana: nama "MockPay", nominal, tombol **Bayar** dan **Gagalkan**. Tombol memanggil endpoint internal mock yang mengubah status charge lalu menjadwalkan webhook sesuai skenario aktif. Setelah selesai, halaman redirect ke `return_url` LedgerPay (`/topups/{id}` di UI). Halaman ini **tidak** pernah menjadi sumber kebenaran pembayaran bagi LedgerPay; yang dipakai hanya webhook + status query.

### 1.4 `POST /v1/payouts` — transfer keluar
| Aspek | Kontrak |
|---|---|
| Header | `Idempotency-Key: <payout_id>` (wajib) |
| Request | `{ "amount": "20000000", "currency": "IDR", "reference": "<payout_id>", "destination": { "bank_code": "MOCKBANK", "account_number": "1234567890", "holder_name": "Rina Wulandari" } }` |
| 200 | `{ "id": "po_01J…", "reference": "<payout_id>", "status": "SUCCEEDED", "processed_at": "…" }` |
| 202 | `{ "id", "reference", "status": "PROCESSING" }`: diterima, hasil menyusul (dijawab lewat status query) |
| 422 | `{ "error": { "code": "declined", "message": "account_closed" } }`: **penolakan definitif** → LedgerPay `FAILED` + reversal |
| 5xx / timeout | **Hasil tidak diketahui** → LedgerPay `UNKNOWN` (ADR-L06) |
| Key sama | Tidak pernah memproses dua kali. Mengembalikan status payout yang sudah ada (200/202/422 sesuai keadaannya) |

Penerjemahan oleh adapter LedgerPay:

| Respons mock | Status LedgerPay |
|---|---|
| 200 `SUCCEEDED` | `SUCCEEDED` + `PAYOUT_SETTLE` |
| 202 `PROCESSING` | tetap `SUBMITTED`, jadwalkan status query |
| 422 `declined` / `invalid_account` | `FAILED` + `PAYOUT_REVERSAL` |
| 400 / 401 (bug kita) | `UNKNOWN` + alert, karena tidak bisa dipastikan bank tidak memprosesnya. Tidak boleh langsung reversal |
| 5xx, timeout, koneksi putus | `UNKNOWN` |

### 1.5 `GET /v1/payouts/{idempotency_key}` — status query
| Response | Arti | Tindakan LedgerPay (ADR-L06) |
|---|---|---|
| 200 `SUCCEEDED` | Dana terkirim | `SUCCEEDED` + settle |
| 200 `FAILED` | Ditolak | `FAILED` + reversal |
| 200 `PROCESSING` | Masih diproses | Jadwal status query berikutnya (30 dtk → 2 mnt → 10 mnt → 1 jam → 6 jam) |
| 404 `NOT_FOUND` | Bank tidak mengenal key ini | **Hanya** jika `now − submitted_at ≥ safe window (5 menit di mock)`, boleh resubmit dengan key yang sama. Sebelum safe window lewat, perlakukan sebagai `PROCESSING` |
| 5xx / timeout | Tidak diketahui | Tetap `UNKNOWN`, lanjut jadwal |

**Safe window** adalah janji kontrak mock: request yang diterima bank selalu terlihat di status query paling lambat 5 menit setelah diterima. Tanpa janji seperti ini dari provider, `NOT_FOUND` tidak boleh dijadikan alasan resubmit. Ini poin interview penting.

### 1.6 `GET /v1/settlements/{business_date}.csv` — laporan settlement harian
`business_date` = `YYYY-MM-DD` (Asia/Jakarta). `Content-Type: text/csv`. Header wajib, urutan tetap:

| Kolom | Tipe | Contoh | Keterangan |
|---|---|---|---|
| `business_date` | date | `2026-03-15` | |
| `type` | enum | `CHARGE` / `PAYOUT` | |
| `provider_ref` | string | `ch_01J…` / `po_01J…` | ID di sisi provider |
| `reference` | uuid | `<topup_id>` / `<payout_id>` | Referensi LedgerPay; kunci matching utama |
| `amount` | integer minor unit | `10000000` | |
| `currency` | string | `IDR` | |
| `status` | enum | `SUCCEEDED` / `FAILED` | Hanya status final |
| `settled_at` | RFC 3339 | `2026-03-15T03:02:11Z` | Dipakai untuk toleransi cut-off |
| `fee` | integer minor unit | `0` | Selalu 0 di V1 (fee P3) |

Baris yang `settled_at`-nya ≤ 15 menit sebelum tengah malam, dan muncul di file hari berikutnya, adalah kasus cut-off. Recon memberi toleransi window sebelum menandainya `MISSING_*`.

### 1.7 Admin & failure injection
| Endpoint | Fungsi |
|---|---|
| `PUT /admin/scenario` | Set skenario global: `{ "charges": "ok", "payouts": "timeout_after_process", "webhooks": ["webhook_duplicate:3"], "payout_status_query": "enabled" \| "disabled", "settlement": "settlement_mismatch:status" }` |
| `GET /admin/scenario` | Lihat skenario aktif |
| `POST /admin/reset` | Hapus state + seed deterministik (`MOCK_SEED`); dipanggil di awal setiap skrip demo |
| `GET /admin/transfers?idempotency_key=<payout_id>` | Bukti demo: `{ "idempotency_key", "transfers_executed": 1, "requests_received": 3 }` |

**Skenario** (nama kanonik dari ADR-L05; LF-06 sudah diselaraskan; kolom terakhir adalah nama lama untuk penelusuran):

| Nilai | Efek | Alias di LF-06 |
|---|---|---|
| `ok` | Normal | — |
| `timeout_after_process` | Diproses (uang "terkirim"), response ditahan sampai client timeout | `timeout_after_success` |
| `timeout_before_process` | Tidak diproses, response ditahan | `timeout_before_processing` |
| `error_5xx` | 503 tanpa memproses | `http_500` |
| `decline` | 422 penolakan definitif | `payout_fail` |
| `webhook_duplicate:<n>` | Webhook dikirim n kali (event id sama) | `duplicate_webhook` |
| `webhook_delay:<dur>` | Webhook ditunda (contoh `25h` untuk melewati expiry top-up) | `delayed_webhook` |
| `webhook_out_of_order` | Mengirim event lama (`charge.pending`/`charge.failed`) setelah event final | `out_of_order_webhook` |
| `webhook_bad_signature` | Signature salah | — |
| `settlement_mismatch:<missing\|amount\|status>` | File settlement sengaja berbeda | — |

**Per request:** header `X-Mock-Scenario: <nilai>` pada panggilan langsung ke mock (dipakai contract test).

**Magic values** (E2E lewat LedgerPay, tanpa mengubah state global; LedgerPay tidak tahu-menahu):

| Input | Skenario |
|---|---|
| Payout `account_number` berakhiran `0408` | `timeout_after_process` (Demo C) |
| … berakhiran `0409` | `timeout_before_process` |
| … berakhiran `0503` | `error_5xx` |
| … berakhiran `0422` | `decline` |
| Top-up `amount` = `1111100` (Rp11.111) | `webhook_duplicate:3` |
| Top-up `amount` = `2222200` | `webhook_delay:25h` |
| Top-up `amount` = `3333300` | `webhook_out_of_order` |
| Top-up `amount` = `4444400` | charge berakhir `FAILED` |

---

## 2. Webhook masuk (mock-provider → LedgerPay)

### 2.1 Transport & signature
| Aspek | Kontrak |
|---|---|
| Endpoint | `POST {callback_url}` = `/api/v1/webhooks/provider` |
| Header | `Content-Type: application/json`, `Provider-Signature: t=<unix_seconds>,v1=<hex>`, `Provider-Event-Id: <event id>` (salinan informatif; sumber kebenaran tetap `id` di body) |
| Signing string | `"<t>" + "." + <raw_body_bytes>` |
| Algoritma | `hex(HMAC-SHA256(webhook_secret, signing_string))` |
| Verifikasi | Constant-time compare (`hmac.Equal`). Boleh ada lebih dari satu `v1=` saat rotasi secret; valid jika salah satu cocok |
| Anti-replay | `abs(now − t) ≤ 300 dtk`, selain itu `400 STALE_WEBHOOK`. Event yang sama di-replay dalam window → dedupe oleh `provider_event_id` |
| Secret | `PROVIDER_WEBHOOK_SECRET` per environment; dirotasi dengan masa transisi dua secret |

### 2.2 Event types (V1)
| `type` | Kapan | Efek di LedgerPay (worker inbox) |
|---|---|---|
| `charge.succeeded` | Charge dibayar | TopUp `PENDING`/`EXPIRED` → `SUCCEEDED` + `TOPUP_CREDIT` |
| `charge.failed` | Charge gagal/dibatalkan | TopUp `PENDING`/`EXPIRED` → `FAILED` |
| `charge.pending` | Hanya muncul di skenario `webhook_out_of_order` | Selalu no-op (`IGNORED`/`STALE`) |

**Payout webhook (`payout.*`) tidak ada di V1.** Status payout ditentukan oleh response submit dan status query (ADR-L06). Status query sudah menangani semua kasus, termasuk saat webhook hilang. Menambah jalur webhook berarti dua jalur yang harus konsisten tanpa menyelesaikan masalah baru. Jalur ini masuk V2 bersama provider nyata yang mendukungnya (ADR-L06 Future Migration), memakai inbox yang sama.

### 2.3 Payload
```json
{
  "id": "evt_01JQ8Z3K6V0B7M2N4P5R6S7T8U",
  "type": "charge.succeeded",
  "created_at": "2026-03-15T08:31:02Z",
  "data": {
    "charge_id": "ch_01JQ8Z2A…",
    "reference": "01JQ8Z1X…",
    "amount": "10000000",
    "currency": "IDR",
    "status": "SUCCEEDED",
    "paid_at": "2026-03-15T08:31:01Z",
    "failure_reason": null
  }
}
```
| Field | Aturan |
|---|---|
| `id` | Unik per event dan **stabil antar retry** → `webhook_inbox.provider_event_id` |
| `data.reference` | `topup_id`. Tidak dikenal → inbox disimpan, diproses menjadi item investigasi (bukan dibuang) |
| `data.amount`/`currency` | Harus sama dengan top-up. Berbeda → inbox `DEAD` + alert (indikasi bug atau serangan) |
| `created_at` | Informasi saja. Urutan tidak dijamin; transisi monoton di LedgerPay yang menentukan efek |

> **Korelasi (Step 8):** LedgerPay mengirim `metadata.correlation_id` saat membuat charge/payout. Mock mengembalikannya di payload webhook (`data.metadata.correlation_id`), sehingga pemrosesan inbox meneruskan alur yang sama di log dan trace.

### 2.4 Perilaku retry provider (mock)
| Respons LedgerPay | Perilaku mock |
|---|---|
| 2xx | Selesai |
| 4xx | **Tidak** di-retry (dianggap masalah permanen: signature/format). Dicatat di log mock |
| 5xx / timeout 10 dtk | Retry dengan backoff: 10 dtk, 1 mnt, 5 mnt, 30 mnt, 2 jam, 6 jam (6 kali), lalu menyerah. Recon menangkap sisanya |

---

## 3. Webhook keluar (LedgerPay → merchant)

### 3.1 Transport
| Aspek | Kontrak |
|---|---|
| Endpoint | `merchants.webhook_url` (HTTPS wajib di live; HTTP hanya dev/demo) |
| Method | `POST`, `Content-Type: application/json`, `User-Agent: LedgerPay-Webhooks/1` |
| Header | `LedgerPay-Signature: t=<unix>,v1=<hex>` (format sama dengan §2.1, secret per merchant), `LedgerPay-Event-Id: <event id>`, `LedgerPay-Delivery-Attempt: <n>` |
| Timeout | 5 detik total. Lebih lambat = gagal |
| Sukses | HTTP 2xx apa pun (body diabaikan) |
| Retry | Gagal (non-2xx, timeout, error koneksi) → **1 mnt, 5 mnt, 30 mnt, 2 jam, 6 jam, 24 jam** (+ jitter ±10%) → `DEAD`. Total 7 percobaan dalam ±33 jam. Replay manual ops (`/ops/webhook-deliveries/{id}/replay`) memakai `event_id` yang sama |
| Redirect | Tidak diikuti (3xx = gagal), untuk mencegah SSRF via redirect |

### 3.2 Envelope & event
```json
{
  "id": "01JQ9A0B…",
  "type": "payment.succeeded",
  "api_version": "2026-03-01",
  "created_at": "2026-03-15T09:00:04Z",
  "data": {
    "payment_intent": {
      "id": "01JQ8Y…",
      "merchant_order_id": "ORD-2026-000123",
      "amount": "7500000",
      "currency": "IDR",
      "refunded_amount": "0",
      "status": "SUCCEEDED",
      "confirmed_at": "2026-03-15T09:00:03Z"
    }
  }
}
```
| `type` | Pemicu | `data` |
|---|---|---|
| `payment.succeeded` | Confirm berhasil | `payment_intent` |
| `payment.refunded` | Setiap refund (partial/full) | `payment_intent` (status terkini) + `refund` `{id, amount, reason, created_at}` |
| `payment.expired` | Sweeper menandai intent `EXPIRED` | `payment_intent` |

`id` dibuat saat event ditulis ke outbox (di transaksi bisnis yang sama) dan **tidak pernah berubah** antar retry atau replay.

### 3.3 Kewajiban merchant (didokumentasikan untuk integrator)
1. **Verifikasi signature** sebelum memproses, dan tolak jika `|now − t| > 300 dtk`.
2. **Dedupe berdasarkan `id`**. Event yang sama bisa datang lebih dari sekali (at-least-once).
3. **Balas 2xx secepatnya** (< 5 dtk), lalu proses secara asinkron.
4. **Jangan mengandalkan urutan.** Contoh: `payment.refunded` bisa tiba sebelum `payment.succeeded` jika percobaan pertama event sukses gagal. Pakai `data.payment_intent.status` + `refunded_amount` sebagai keadaan terkini, dan jika ragu panggil `GET /merchant/payment-intents/{id}` sebagai sumber kebenaran.
5. Simpan secret webhook seperti password. Rotasi secret dilakukan lewat ops.

### 3.4 Contoh verifikasi (Go)
```go
func Verify(secret []byte, header string, body []byte, now time.Time) error {
    var ts int64
    var sigs [][]byte
    for _, part := range strings.Split(header, ",") {
        k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
        switch k {
        case "t":
            ts, _ = strconv.ParseInt(v, 10, 64)
        case "v1":
            if b, err := hex.DecodeString(v); err == nil {
                sigs = append(sigs, b)
            }
        }
    }
    if ts == 0 || len(sigs) == 0 {
        return ErrInvalidSignature
    }
    if d := now.Sub(time.Unix(ts, 0)); d > 5*time.Minute || d < -5*time.Minute {
        return ErrStaleWebhook
    }
    mac := hmac.New(sha256.New, secret)
    mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
    mac.Write(body) // raw bytes persis seperti diterima
    expected := mac.Sum(nil)
    for _, s := range sigs {
        if hmac.Equal(expected, s) { // constant-time
            return nil
        }
    }
    return ErrInvalidSignature
}
```
Fungsi yang sama dipakai LedgerPay untuk memverifikasi webhook masuk (§2.1), karena formatnya sengaja disamakan (ADR-L05).

---

## 4. Versioning kontrak eksternal

| Kontrak | Mekanisme versi | Aturan perubahan |
|---|---|---|
| mock-provider API | Path `/v1` | Mock dikendalikan sendiri, tetapi diperlakukan seperti provider pihak ketiga: adapter LedgerPay hanya bergantung pada field yang terdokumentasi di sini. Contract test (Step 8) menjaga keduanya tetap sinkron |
| Webhook masuk | Field `type` + bentuk `data` per type | Field baru boleh ditambah; parser LedgerPay mengabaikan field tak dikenal. Type tak dikenal → `IGNORED` (bukan error) |
| Webhook keluar | `api_version` (tanggal) di envelope | Perubahan **aditif** (field baru) tidak menaikkan versi. Perubahan breaking → `api_version` baru; merchant memilih versi di konfigurasinya (V2; V1 hanya punya `2026-03-01`). Merchant wajib mengabaikan field yang tidak dikenal |
