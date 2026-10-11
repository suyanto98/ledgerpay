# LedgerPay API Contract

> Konvensi, header, auth, rate-limit tier, dan format endpoint: `00-index.md`. Error code: [`12-api-error-catalogue.md`](12-api-error-catalogue.md). Kontrak ke mock-provider dan merchant: [`14-api-external-contracts.md`](14-api-external-contracts.md).
> Base URL: `https://<host>/api/v1`. Semua contoh uang memakai minor unit IDR (exponent 2): `"5000000"` = Rp50.000,00.

---

## 1. Ringkasan endpoint

| # | Method | Path | Fitur | Auth | Idempotency | Rate tier | Prio |
|---|---|---|---|---|---|---|---|
| 1 | POST | `/auth/register` | LF-01 | — | natural (`uq_users_email`) | `auth-strict` | P0 |
| 2 | POST | `/auth/login` | LF-01 | — | n.a. | `auth-strict` | P0 |
| 3 | POST | `/auth/refresh` | LF-01 | Refresh cookie | n.a. (rotasi) | `auth-strict` | P0 |
| 4 | POST | `/auth/logout` | LF-01 | Refresh cookie | natural | `auth-ip` | P0 |
| 5 | GET | `/me` | LF-01 | User JWT | — | `read` | P0 |
| 6 | GET | `/wallet` | LF-02 | User JWT | — | `read` | P0 |
| 7 | GET | `/wallet/transactions` | LF-02 | User JWT | — | `read` | P0 |
| 8 | GET | `/wallet/transactions/{journal_id}` | LF-02 | User JWT | — | `read` | P0 |
| 9 | POST | `/transfers` | LF-04, LF-05 | User JWT | **required** | `money-write` | P0 |
| 10 | GET | `/transfers/{id}` | LF-04 | User JWT | — | `read` | P0 |
| 11 | POST | `/topups` | LF-07 | User JWT | **required** (pola b) | `money-write` | P0 |
| 12 | GET | `/topups/{id}` | LF-07 | User JWT | — | `read` | P0 |
| 13 | GET | `/payment-intents/{id}` | LF-10 | User JWT | — | `read` | P0 |
| 14 | POST | `/payment-intents/{id}/confirm` | LF-10 | User JWT | **required** | `money-write` | P0 |
| 15 | POST | `/payouts` | LF-11 | User JWT | **required** | `money-write` | P0 |
| 16 | GET | `/payouts/{id}` | LF-11 | User JWT | — | `read` | P0 |
| 17 | GET | `/payouts` | LF-11 | User JWT | — | `read` | P0 |
| 18 | POST | `/merchant/payment-intents` | LF-10 | API key | **required** | `merchant-api` | P0 |
| 19 | GET | `/merchant/payment-intents/{id}` | LF-10 | API key | — | `merchant-api` | P0 |
| 20 | GET | `/merchant/payment-intents` | LF-10 | API key | — | `merchant-api` | P0 |
| 21 | POST | `/merchant/payment-intents/{id}/refunds` | LF-16 | API key | **required** | `merchant-api` | P1 |
| 22 | GET | `/merchant/balance` | LF-09 | API key | — | `merchant-api` | P0 |
| 23 | POST | `/webhooks/provider` | LF-07 | Signature | natural (`provider_event_id`) | `webhook-in` | P0 |
| 24 | GET | `/ops/transactions/search` | LF-18 | OPS | — | `read` | P1 |
| 25 | GET | `/ops/journals/{id}` | LF-18 | OPS | — | `read` | P1 |
| 26 | GET | `/ops/payouts` | LF-18 | OPS | — | `read` | P1 |
| 27 | POST | `/ops/payouts/{id}/resolve` | LF-18, LF-11 | OPS | **required** | `write` | P1 |
| 28 | GET | `/ops/recon-runs` | LF-17 | OPS | — | `read` | P1 |
| 29 | POST | `/ops/recon-runs` | LF-17 | OPS | natural (`uq_recon_runs_file`) | `write` | P1 |
| 30 | GET | `/ops/recon-items` | LF-17 | OPS | — | `read` | P1 |
| 31 | POST | `/ops/recon-items/{id}/resolve` | LF-17, LF-18 | OPS | **required** | `write` | P1 |
| 32 | GET | `/ops/jobs` | LF-18 | OPS | — | `read` | P1 |
| 33 | POST | `/ops/jobs/{id}/retry` | LF-18 | OPS | natural (guard status) | `write` | P1 |
| 34 | POST | `/ops/webhook-deliveries/{id}/replay` | LF-15, LF-18 | OPS | natural (guard status) | `write` | P1 |
| 35 | POST | `/ops/merchants/{id}/api-keys` | LF-09 | OPS | **required** | `write` | P0 |
| 36 | POST | `/ops/api-keys/{id}/revoke` | LF-09 | OPS | natural | `write` | P0 |
| 37 | GET | `/healthz` | LF-13 | — | — | — | P0 |
| 38 | GET | `/readyz` | LF-13 | — | — | — | P0 |

### Keputusan yang dibuat di file ini
| Topik | Keputusan | Alasan |
|---|---|---|
| Manajemen API key | **Hanya ops di V1** (#35, #36). Tidak ada `/merchant/api-keys` | Merchant dibuat lewat seed dan tidak punya login dashboard di V1. Mengizinkan API key membuat API key baru berarti satu key yang bocor bisa melahirkan key pengganti dan bertahan setelah di-revoke. Rotasi tetap didukung: ops membuat key di slot kosong, merchant berpindah, lalu ops me-revoke key lama |
| Visibilitas checkout (#13) | Sebelum dibayar: user login mana pun yang memegang ID intent boleh melihat ringkasan (ID = *capability URL*). Setelah `SUCCEEDED`: hanya payer; user lain → `404` | Checkout harus bisa dibuka dari link merchant. Setelah dibayar, detail pembayaran adalah data milik payer |
| Riwayat wallet | Berbasis **ledger** (journal entries milik akun user). Top-up `PENDING` tidak muncul (belum ada journal); payout `PENDING/SUBMITTED/UNKNOWN` muncul sebagai "dana ditahan" (journal hold sudah ada) | Riwayat = kebenaran uang, bukan daftar niat. Status top-up dipantau lewat `GET /topups/{id}` |
| Payout webhook dari bank | **Tidak ada di V1.** Status payout diketahui dari response submit + status query (ADR-L06) | Lihat file 03 §2.2 |
| `/metrics` | Tidak diekspos lewat Caddy; hanya bisa diakses dari jaringan internal compose (Prometheus) | Metrik bisa membocorkan volume bisnis |

---

## 2. Response models

### 2.1 `Money`
| Field | Tipe | Keterangan |
|---|---|---|
| `amount` | string | Integer minor unit, `^[1-9][0-9]{0,17}$` untuk input; output boleh `"0"` (saldo) |
| `currency` | string | `"IDR"` (satu-satunya di V1) |

Di objek lain `Money` ditulis rata (`amount`, `currency`) agar payload tetap datar.

### 2.2 `Wallet`
| Field | Tipe | Keterangan |
|---|---|---|
| `account_id` | uuid | Akun ledger wallet |
| `balance` | string | Saldo tersedia (`accounts.balance`) |
| `held_amount` | string | Σ payout `PENDING/SUBMITTED/UNKNOWN` (sudah berpindah ke `PAYOUT_IN_TRANSIT`; informasi saja, sudah tidak termasuk `balance`) |
| `currency` | string | `IDR` |
| `status` | string | `ACTIVE` / `FROZEN` |
| `as_of` | timestamp | Waktu baca (selalu dari primary) |

### 2.3 `Transaction` (item riwayat, polimorfik)
| Field | Tipe | Keterangan |
|---|---|---|
| `journal_id` | uuid | ID item riwayat (stabil) |
| `type` | string | `TOPUP`, `TRANSFER_OUT`, `TRANSFER_IN`, `PAYMENT`, `REFUND_IN`, `PAYOUT_HOLD`, `PAYOUT_RETURNED` |
| `direction` | string | `IN` / `OUT` (dari sisi wallet user) |
| `amount`, `currency` | string | Selalu positif; arah di `direction` |
| `source_type`, `source_id` | string, uuid | Objek bisnis (`topup`, `transfer`, `payment_intent`, `refund`, `payout`) |
| `status` | string | Status objek bisnis saat ini (contoh payout `UNKNOWN` → UI menampilkan "Sedang diproses") |
| `counterparty` | object/null | `{ "type": "USER"/"MERCHANT"/"BANK", "display_name": "Andi S.", "masked": "****1234" }` (tanpa ID internal pihak lain) |
| `description` | string/null | Catatan transfer / deskripsi intent |
| `created_at` | timestamp | Waktu journal |

### 2.4 `Transfer`
`id`, `sender_account_id`, `receiver` (`{display_name}`, tanpa ID user penerima untuk pengirim), `amount`, `currency`, `note`, `status` (`COMPLETED`), `journal_id`, `created_at`.

### 2.5 `TopUp`
`id`, `amount`, `currency`, `status` (`PENDING/SUCCEEDED/FAILED/EXPIRED`), `checkout_url` (null jika provider belum terhubung), `expires_at`, `failure_reason`, `journal_id`, `created_at`, `updated_at`.

### 2.6 `PaymentIntent`
| Field | Tipe | Merchant view | Payer view | Viewer sebelum bayar |
|---|---|---|---|---|
| `id` | uuid | ✓ | ✓ | ✓ |
| `merchant` | `{id?, name}` | `id` + name | name | name |
| `merchant_order_id` | string | ✓ | ✓ | ✓ |
| `amount`, `currency` | string | ✓ | ✓ | ✓ |
| `description` | string | ✓ | ✓ | ✓ |
| `status` | string | ✓ | ✓ | ✓ |
| `refunded_amount` | string | ✓ | ✓ | — |
| `checkout_url` | string | ✓ | — | — |
| `payer` | `{display_name}` | ✓ (nama saja) | — | — |
| `refunds` | `Refund[]` | ✓ | ✓ | — |
| `expires_at`, `confirmed_at`, `created_at` | timestamp | ✓ | ✓ | `expires_at` |

### 2.7 `Refund`
`id`, `payment_intent_id`, `amount`, `currency`, `reason`, `journal_id`, `created_at`.

### 2.8 `Payout`
`id`, `amount`, `currency`, `status` (`PENDING/SUBMITTED/SUCCEEDED/FAILED/UNKNOWN`), `display_status` (`PROCESSING` untuk `PENDING/SUBMITTED/UNKNOWN`, `SUCCEEDED`, `FAILED`), `destination` (`{bank_code, account_number_masked, holder_name}`), `hold_journal_id`, `settle_journal_id`, `reversal_journal_id`, `created_at`, `resolved_at`. Field ops tambahan (#26): `needs_reconciliation`, `status_check_attempts`, `last_provider_status`, `resolution_source`.

> **Kenapa `display_status`:** user tidak perlu (dan tidak sebaiknya) melihat kata `UNKNOWN`. Untuk user, kondisinya adalah "sedang diproses, dana aman ditahan". Status mentah tetap dikirim untuk transparansi dan debugging.

### 2.9 `Problem`
RFC 9457, lihat `00-index.md` §4.

---

## 3. Auth

### POST /auth/register
```text
Use case / fitur      : LF-01
Authentication        : —
Authorization         : —
Request               : body { "email": "rina@example.com", "password": "…", "display_name": "Rina" }
Response              : 201 { "user": { "id", "email", "display_name", "role": "USER" },
                              "wallet": { "account_id", "balance": "0", "currency": "IDR" } }
Validation            : email RFC 5322 sederhana, ≤ 254, di-lowercase; password 10–128 karakter;
                        display_name 1–60 karakter, tanpa karakter kontrol
Errors                : 400 VALIDATION_ERROR · 409 EMAIL_ALREADY_REGISTERED · 429 RATE_LIMITED
Idempotency           : natural: register ganda → satu sukses, sisanya 409 (uq_users_email)
Pagination            : n.a.
Rate limit            : auth-strict
Catatan               : Satu tx: users + accounts (wallet LIABILITY, balance 0). Tidak auto-login;
                        client memanggil /auth/login (alur auth tetap satu jalur).
```

### POST /auth/login
```text
Use case / fitur      : LF-01
Authentication        : —
Request               : body { "email", "password" }
Response              : 200 { "access_token": "eyJ…", "token_type": "Bearer", "expires_in": 900,
                              "user": { "id", "email", "display_name", "role" } }
                        + Set-Cookie: lp_refresh=<opaque>; HttpOnly; Secure; SameSite=Strict; Path=/api/v1/auth; Max-Age=2592000
Validation            : email & password wajib
Errors                : 400 VALIDATION_ERROR · 401 INVALID_CREDENTIALS · 403 ACCOUNT_LOCKED · 429 RATE_LIMITED
Idempotency           : n.a. (setiap login membuat token family baru)
Rate limit            : auth-strict (IP + email)
Catatan               : Verifikasi argon2id dilakukan juga untuk email yang tidak ada (dummy hash) agar
                        waktu respons tidak membocorkan keberadaan akun. Gagal ≥ 5× → locked_until (ADR-009).
                        Audit: auth.login_succeeded / auth.login_failed.
```

### POST /auth/refresh
```text
Use case / fitur      : LF-01
Authentication        : cookie lp_refresh + header X-Requested-With: fetch
Request               : body kosong
Response              : 200 { "access_token", "token_type", "expires_in" } + Set-Cookie refresh baru
Errors                : 401 UNAUTHENTICATED (cookie tidak ada / kedaluwarsa / family kedaluwarsa 30 hari)
                        · 401 REFRESH_TOKEN_REUSED (seluruh family dicabut) · 429 RATE_LIMITED
Idempotency           : n.a. Refresh paralel dari dua tab dalam grace window 10 dtk mendapat pasangan token yang
                        sama dengan penerus (replaced_by), bukan reuse detection (`07-db-shared-tables.md`-shared-tables)
Rate limit            : auth-strict
```

### POST /auth/logout
```text
Authentication        : cookie lp_refresh + X-Requested-With
Response              : 204 + Set-Cookie lp_refresh kedaluwarsa
Errors                : — (cookie tidak valid tetap 204: logout harus selalu "berhasil")
Idempotency           : natural (revoke yang sudah revoked = no-op)
Rate limit            : auth-ip
```

### GET /me
```text
Authentication        : User JWT
Response              : 200 { "id", "email", "display_name", "role", "created_at" }
Errors                : 401 UNAUTHENTICATED · 401 TOKEN_EXPIRED
Rate limit            : read
```

---

## 4. Wallet

### GET /wallet
```text
Use case / fitur      : LF-02
Authentication        : User JWT
Authorization         : Wallet milik principal (dicari dari user_id, tidak ada ID di path → tidak ada permukaan BOLA)
Response              : 200 Wallet
                        { "account_id": "01J…", "balance": "15000000", "held_amount": "20000000",
                          "currency": "IDR", "status": "ACTIVE", "as_of": "2026-03-15T08:30:00Z" }
Errors                : 401 UNAUTHENTICATED · 401 TOKEN_EXPIRED
Rate limit            : read
Catatan               : Selalu dari primary (bukan replica, juga di V2): saldo adalah data strong consistency.
```

### GET /wallet/transactions
```text
Use case / fitur      : LF-02
Authentication        : User JWT
Authorization         : Hanya entries akun wallet principal
Request               : query limit (1–100, default 20), cursor, type (multi: ?type=TOPUP&type=PAYMENT),
                        direction (IN|OUT), from, to (YYYY-MM-DD, zona Asia/Jakarta, rentang ≤ 366 hari)
Response              : 200 { "data": [Transaction…], "page": { "next_cursor": "eyJ…", "has_more": true } }
Validation            : from ≤ to; type ∈ enum; filter tidak dikenal → 400
Errors                : 400 VALIDATION_ERROR · 400 INVALID_CURSOR · 401
Idempotency           : —
Pagination            : cursor (created_at DESC, id DESC) → ix_journal_entries_account_created
Rate limit            : read
```

### GET /wallet/transactions/{journal_id}
```text
Authentication        : User JWT
Authorization         : journal harus memiliki entry pada akun wallet principal; jika tidak → 404
Response              : 200 Transaction + "journal": { "id", "purpose", "created_at",
                        "entries": [ { "side": "D"/"C", "amount", "account_label": "Wallet Anda" / "Sistem" } ] }
Errors                : 404 NOT_FOUND · 401
Rate limit            : read
Catatan               : Entry akun pihak lain ditampilkan sebagai label generik ("Wallet pengguna lain",
                        "Sistem"), tanpa ID akun. Detail lengkap hanya di /ops/journals/{id}.
```

---

## 5. Transfer

### POST /transfers
```text
Use case / fitur      : LF-04, LF-05
Authentication        : User JWT
Authorization         : Sumber selalu wallet principal (tidak ada field "from")
Request               : header Idempotency-Key: 7c9e6679-7425-40de-944b-e07fc1f90ae7
                        body { "recipient_email": "andi@example.com", "amount": "5000000",
                               "currency": "IDR", "note": "Patungan makan" }
Response              : 201 Transfer
                        { "id": "01J…", "amount": "5000000", "currency": "IDR", "status": "COMPLETED",
                          "receiver": { "display_name": "Andi S." }, "note": "Patungan makan",
                          "journal_id": "01J…", "created_at": "…" }
Validation            : Idempotency-Key ^[A-Za-z0-9_\-:.]{8,255}$; amount 100 (Rp1) – 1000000000 (Rp10.000.000);
                        currency = IDR; note ≤ 140 karakter; recipient_email valid
Errors                : 400 VALIDATION_ERROR · 400 IDEMPOTENCY_KEY_REQUIRED · 409 REQUEST_IN_PROGRESS
                        · 409 RESOURCE_BUSY · 422 IDEMPOTENCY_KEY_REUSED · 422 INSUFFICIENT_BALANCE
                        · 422 SELF_TRANSFER · 422 INVALID_RECIPIENT · 422 ACCOUNT_FROZEN
                        · 422 CURRENCY_NOT_SUPPORTED · 422 LIMIT_EXCEEDED (P2) · 429 · 503
Idempotency           : required. Disimpan untuk replay: 201 dan seluruh 422 domain (INSUFFICIENT_BALANCE, SELF_TRANSFER,
                        INVALID_RECIPIENT, ACCOUNT_FROZEN, LIMIT_EXCEEDED). Tidak disimpan: 400, 409, 5xx (ADR-L02)
Pagination            : n.a.
Rate limit            : money-write
Catatan               : Satu tx: INSERT idempotency → SELECT 2 akun ORDER BY id FOR UPDATE → cek saldo →
                        PostJournal(TRANSFER) → transfers → simpan response → COMMIT (ADR-L03).
                        Penerima dicari via email; email tidak terdaftar = INVALID_RECIPIENT (sama dengan akun
                        frozen/nonaktif penerima, agar tidak bisa dipakai enumerasi). FS-01, FS-02, FS-03, FS-04. Demo A, B.
```

### GET /transfers/{id}
```text
Authentication        : User JWT
Authorization         : principal = sender atau receiver; selain itu 404
Response              : 200 Transfer (receiver melihat "sender": { "display_name" } sebagai ganti "receiver")
Errors                : 404 NOT_FOUND · 401
Rate limit            : read
```

---

## 6. Top-up

### POST /topups
```text
Use case / fitur      : LF-07
Authentication        : User JWT
Request               : header Idempotency-Key; body { "amount": "10000000", "currency": "IDR" }
Response              : 201 TopUp
                        { "id": "01J…", "amount": "10000000", "currency": "IDR", "status": "PENDING",
                          "checkout_url": "https://mock-provider.local/checkout/ch_01J…",
                          "expires_at": "2026-03-16T08:30:00Z", "created_at": "…" }
                        201 dengan "checkout_url": null jika provider gagal/timeout setelah tx1 (lihat Catatan)
Validation            : amount 1000000 (Rp10.000) – 1000000000 (Rp10.000.000); currency = IDR
Errors                : 400 VALIDATION_ERROR · 400 IDEMPOTENCY_KEY_REQUIRED · 409 REQUEST_IN_PROGRESS
                        · 422 IDEMPOTENCY_KEY_REUSED · 422 ACCOUNT_FROZEN · 422 LIMIT_EXCEEDED (P2) · 429 · 503
Idempotency           : required, pola (b) recovery point (ADR-L02). tx1 commit TopUp(recovery_point=CREATED) + key
                        (response NULL) → panggil provider (Idempotency-Key = topup_id) → tx2 provider_ref,
                        checkout_url, recovery_point=PROVIDER_CREATED + simpan response 201.
                        Retry dengan key yang sama saat response masih NULL → lanjutkan dari recovery_point
                        (panggil provider lagi; aman karena key provider = topup_id).
Rate limit            : money-write
Catatan               : Kalau provider timeout/5xx: response 201 dengan checkout_url null, status PENDING, dan
                        response TIDAK disimpan (key masih "berjalan"). Client boleh retry dengan key yang sama
                        atau polling GET /topups/{id}; job topup.recover juga melanjutkan tanpa client. FS-05 (varian
                        top-up), FS-10. Saldo bertambah hanya lewat webhook (#23), tidak pernah dari endpoint ini.
```

### GET /topups/{id}
```text
Authentication        : User JWT
Authorization         : topups.user_id = principal; selain itu 404
Response              : 200 TopUp
Errors                : 404 · 401
Rate limit            : read
Catatan               : Endpoint polling untuk halaman "menunggu pembayaran". EXPIRED adalah status lunak:
                        bisa berubah ke SUCCEEDED jika webhook sukses datang terlambat (FS-07).
```

---

## 7. Checkout (user membayar merchant)

### GET /payment-intents/{id}
```text
Use case / fitur      : LF-10
Authentication        : User JWT
Authorization         : status REQUIRES_CONFIRMATION/EXPIRED/CANCELLED → user login mana pun (capability URL);
                        status SUCCEEDED/PARTIALLY_REFUNDED/REFUNDED → hanya payer, selain itu 404
Response              : 200 PaymentIntent (payer view atau viewer-sebelum-bayar; lihat §2.6)
Errors                : 404 NOT_FOUND · 401
Rate limit            : read
```

### POST /payment-intents/{id}/confirm
```text
Use case / fitur      : LF-10
Authentication        : User JWT
Authorization         : user login mana pun yang memegang ID intent; payer = principal
Request               : header Idempotency-Key; body {} (amount TIDAK dikirim client: diambil dari intent)
Response              : 200 PaymentIntent (payer view, status SUCCEEDED)
Validation            : —
Errors                : 400 IDEMPOTENCY_KEY_REQUIRED · 404 NOT_FOUND · 409 REQUEST_IN_PROGRESS · 409 RESOURCE_BUSY
                        · 409 INVALID_STATE_TRANSITION (sudah dibayar orang lain / CANCELLED)
                        · 422 IDEMPOTENCY_KEY_REUSED · 422 PAYMENT_EXPIRED · 422 INSUFFICIENT_BALANCE
                        · 422 ACCOUNT_FROZEN · 422 SELF_TRANSFER (payer = pemilik akun merchant, jika kelak ada) · 429 · 503
Idempotency           : required. Disimpan: 200 dan 422 domain. Confirm paralel dari dua tab dengan key berbeda →
                        row lock intent + guard status: satu 200, satu 409 INVALID_STATE_TRANSITION (LI-13)
Rate limit            : money-write
Catatan               : Satu tx: key → SELECT intent FOR UPDATE → cek status & expires_at → lock akun payer &
                        merchant (ORDER BY id) → PostJournal(PAYMENT) → intent SUCCEEDED → webhook_deliveries +
                        job webhook.deliver (outbox, P1) → response. 200 (bukan 201) karena tidak membuat resource
                        baru; resource yang ada berubah status. FS-02 (Setup A).
```

---

## 8. Payout

### POST /payouts
```text
Use case / fitur      : LF-11
Authentication        : User JWT
Request               : header Idempotency-Key
                        body { "amount": "20000000", "currency": "IDR",
                               "destination": { "bank_code": "MOCKBANK", "account_number": "1234567890",
                                                "holder_name": "Rina Wulandari" } }
Response              : 202 Payout
                        { "id": "01J…", "amount": "20000000", "currency": "IDR", "status": "PENDING",
                          "display_status": "PROCESSING",
                          "destination": { "bank_code": "MOCKBANK", "account_number_masked": "******7890",
                                           "holder_name": "Rina Wulandari" },
                          "hold_journal_id": "01J…", "created_at": "…" }
Validation            : amount 1000000 (Rp10.000) – 2500000000 (Rp25.000.000); bank_code ∈ daftar bank yang didukung
                        (^[A-Z0-9]{3,10}$; V1: MOCKBANK, BCA, BNI, BRI, MANDIRI — semua diarahkan ke mock);
                        account_number ^[0-9]{6,20}$; holder_name 2–100 karakter
Errors                : 400 VALIDATION_ERROR · 400 IDEMPOTENCY_KEY_REQUIRED · 409 REQUEST_IN_PROGRESS
                        · 409 RESOURCE_BUSY · 422 IDEMPOTENCY_KEY_REUSED · 422 INSUFFICIENT_BALANCE
                        · 422 ACCOUNT_FROZEN · 422 LIMIT_EXCEEDED (P2) · 429 · 503
Idempotency           : required. Disimpan: 202 dan 422 domain.
Rate limit            : money-write
Catatan               : 202 karena hasil akhir ditentukan worker. Satu tx: key → lock wallet → PostJournal(PAYOUT_HOLD)
                        → payouts PENDING (account_number terenkripsi, last4) → job payout.submit → COMMIT.
                        Tidak ada panggilan bank di request ini (ADR-011). Status akhir dipantau via GET /payouts/{id}.
                        FS-05, FS-08. Demo C.
```

### GET /payouts/{id}
```text
Authentication        : User JWT
Authorization         : payouts.user_id = principal; selain itu 404
Response              : 200 Payout
Errors                : 404 · 401
Rate limit            : read
```

### GET /payouts
```text
Authentication        : User JWT
Request               : query limit, cursor, status (multi), display_status
Response              : 200 { "data": [Payout…], "page": {…} }
Pagination            : cursor (created_at DESC, id DESC) → ix_payouts_user_created
Errors                : 400 VALIDATION_ERROR · 400 INVALID_CURSOR · 401
Rate limit            : read
```

---

## 9. Merchant API (`/merchant/*`, API key)

> Principal = `merchant_id` dari API key (prefix → hash, constant-time compare). Semua query di-scope `WHERE merchant_id = $principal`. Resource merchant lain → `404`. Key dengan `mode=test` hanya untuk environment non-produksi (V1 hanya punya test).

### POST /merchant/payment-intents
```text
Use case / fitur      : LF-10
Authentication        : Authorization: Bearer lp_test_<prefix>_<secret>
Request               : header Idempotency-Key
                        body { "merchant_order_id": "ORD-2026-000123", "amount": "7500000", "currency": "IDR",
                               "description": "Kopi susu x3", "expires_in_seconds": 1800 }
Response              : 201 PaymentIntent (merchant view)
                        { "id": "01J…", "merchant_order_id": "ORD-2026-000123", "amount": "7500000",
                          "currency": "IDR", "status": "REQUIRES_CONFIRMATION",
                          "checkout_url": "https://<host>/checkout/01J…", "expires_at": "…", "created_at": "…" }
Validation            : merchant_order_id ^[A-Za-z0-9_\-.:/]{1,64}$; amount 100 – 10000000000 (Rp100.000.000);
                        description ≤ 200; expires_in_seconds 300–86400 (default 1800)
Errors                : 400 VALIDATION_ERROR · 400 IDEMPOTENCY_KEY_REQUIRED · 401 UNAUTHENTICATED
                        · 409 REQUEST_IN_PROGRESS · 409 DUPLICATE_MERCHANT_ORDER (+existing_resource_id)
                        · 422 IDEMPOTENCY_KEY_REUSED · 422 CURRENCY_NOT_SUPPORTED · 429 · 503
Idempotency           : required (client_type = MERCHANT). Retry setelah network timeout dengan key yang sama → replay 201
                        (FS-09). Order yang sama dengan key BERBEDA → 409 DUPLICATE_MERCHANT_ORDER + ID intent yang ada,
                        sehingga merchant tetap bisa melanjutkan.
Rate limit            : merchant-api
```

### GET /merchant/payment-intents/{id}
```text
Authentication        : API key
Authorization         : intent.merchant_id = principal; selain itu 404
Response              : 200 PaymentIntent (merchant view, termasuk refunds[])
Errors                : 404 · 401
Rate limit            : merchant-api
Catatan               : Jalur polling resmi: merchant yang ragu dengan webhook (out-of-order, terlambat) menjadikan GET
                        ini sumber kebenaran (file 03 §3).
```

### GET /merchant/payment-intents
```text
Authentication        : API key
Request               : query limit, cursor, status (multi), merchant_order_id (exact), from, to
Response              : 200 { "data": [PaymentIntent…], "page": {…} }
Pagination            : cursor (created_at DESC, id DESC) → ix_payment_intents_merchant_created
Errors                : 400 VALIDATION_ERROR · 400 INVALID_CURSOR · 401
Rate limit            : merchant-api
```

### POST /merchant/payment-intents/{id}/refunds
```text
Use case / fitur      : LF-16
Authentication        : API key
Authorization         : intent.merchant_id = principal; selain itu 404
Request               : header Idempotency-Key; body { "amount": "2500000", "reason": "Item habis" }
Response              : 201 Refund + "payment_intent": { "id", "status", "refunded_amount" }
Validation            : amount 1 – (amount − refunded_amount); reason ≤ 200
Errors                : 400 VALIDATION_ERROR · 400 IDEMPOTENCY_KEY_REQUIRED · 404 NOT_FOUND · 409 REQUEST_IN_PROGRESS
                        · 409 RESOURCE_BUSY · 409 INVALID_STATE_TRANSITION (intent belum SUCCEEDED / sudah REFUNDED)
                        · 422 IDEMPOTENCY_KEY_REUSED · 422 REFUND_EXCEEDS_PAYMENT (+refundable_amount)
                        · 422 INSUFFICIENT_BALANCE (saldo merchant) · 429 · 503
Idempotency           : required. Disimpan: 201 dan 422 domain.
Rate limit            : merchant-api
Catatan               : Satu tx: key → SELECT intent FOR UPDATE → cek refunded_amount + amount ≤ amount → lock akun
                        (ORDER BY id) → PostJournal(REFUND) → refunds + update refunded_amount/status → outbox
                        payment.refunded. Refund paralel diserialkan oleh lock intent (FS-02 Setup B, LI-08).
```

### GET /merchant/balance
```text
Authentication        : API key
Response              : 200 { "account_id", "balance": "182500000", "currency": "IDR", "as_of": "…" }
Errors                : 401
Rate limit            : merchant-api
```

---

## 10. Webhook masuk

### POST /webhooks/provider
```text
Use case / fitur      : LF-07
Authentication        : Provider-Signature: t=1773563400,v1=5f2b… (HMAC-SHA256 atas "<t>.<raw_body>"; file 03 §2)
Authorization         : —
Request               : raw body JSON (file 03 §2.3). Server membaca raw bytes SEBELUM decode JSON; signature dihitung
                        atas bytes persis yang diterima (tidak boleh re-serialize). Batas 64 KB.
Response              : 200 { "received": true }      ← event baru (disimpan ke inbox)
                        200 { "received": true, "duplicate": true }   ← provider_event_id sudah ada
Validation            : signature valid; |now − t| ≤ 300 dtk; JSON valid; event_type dikenal (tidak dikenal → tetap
                        disimpan dengan status IGNORED, 200)
Errors                : 401 INVALID_SIGNATURE · 400 STALE_WEBHOOK · 400 MALFORMED_JSON · 413 PAYLOAD_TOO_LARGE
                        · 500 INTERNAL_ERROR / 503 SERVICE_UNAVAILABLE (DB gagal: biarkan provider retry)
Idempotency           : natural: INSERT webhook_inbox ON CONFLICT (provider, provider_event_id) DO NOTHING
Pagination            : n.a.
Rate limit            : webhook-in
Catatan               : Handler TIDAK memproses efek bisnis; hanya verifikasi + simpan + 200 (< 50 ms target).
                        Worker inbox menerapkan transisi monoton + journal (ADR-L08). Validasi isi (amount/currency
                        cocok dengan top-up) dilakukan worker → DEAD + alert jika tidak cocok. FS-06, FS-07.
```

---

## 11. Ops (`/ops/*`, role OPS)

> Semua aksi tulis ops wajib `reason` (≥ 10 karakter) dan tercatat di `audit_log` dalam transaksi yang sama (LF-12, LF-18). **Tidak ada endpoint "edit saldo".**

### GET /ops/transactions/search
```text
Use case / fitur      : LF-18
Authentication        : User JWT, role = OPS (selain itu 403 FORBIDDEN: endpoint ops sah diketahui keberadaannya)
Request               : query q (wajib: UUID objek/journal, provider_ref, merchant_order_id, atau email user), limit
Response              : 200 { "data": [ { "object_type": "payout", "id", "status", "amount", "currency",
                                           "user": { "id", "email" }, "journal_ids": [...], "created_at",
                                           "correlation_id" } ] }
Errors                : 400 VALIDATION_ERROR · 401 · 403 FORBIDDEN
Pagination            : n.a. (maks 50 hasil; pencarian presisi, bukan daftar)
Rate limit            : read
```

### GET /ops/journals/{id}
```text
Authentication        : OPS
Response              : 200 { "id", "purpose", "source_type", "source_id", "reversal_of", "reversed_by",
                              "actor_type", "actor_id", "correlation_id", "created_at",
                              "entries": [ { "account_id", "account_code"/"owner", "direction", "amount", "currency" } ] }
Errors                : 404 · 401 · 403
Rate limit            : read
```

### GET /ops/payouts
```text
Authentication        : OPS
Request               : query status (multi; contoh ?status=UNKNOWN), needs_reconciliation (bool),
                        older_than_minutes, limit, cursor
Response              : 200 { "data": [Payout + field ops], "page": {…} }
Pagination            : cursor (created_at ASC: yang paling lama menggantung di atas)
Errors                : 400 · 401 · 403
Rate limit            : read
```

### POST /ops/payouts/{id}/resolve
```text
Use case / fitur      : LF-18, LF-11 (resolusi manual UNKNOWN; ADR-L06)
Authentication        : OPS
Request               : header Idempotency-Key
                        body { "outcome": "SUCCEEDED" | "FAILED",
                               "evidence_ref": "MOCKBANK-STMT-2026-03-15#L42",
                               "reason": "Mutasi bank menunjukkan dana terkirim 15 Mar 10:02" }
Response              : 200 Payout (status final, resolution_source = OPS)
Validation            : outcome ∈ enum; evidence_ref 3–200; reason ≥ 10
Errors                : 400 VALIDATION_ERROR · 404 · 409 INVALID_STATE_TRANSITION (payout bukan UNKNOWN/SUBMITTED-tanpa-hasil)
                        · 409 RESOURCE_BUSY · 422 RESOLUTION_EVIDENCE_REQUIRED · 422 IDEMPOTENCY_KEY_REUSED · 403
Idempotency           : required
Rate limit            : write
Catatan               : Satu tx: guard UPDATE … WHERE status IN ('UNKNOWN','SUBMITTED') → journal PAYOUT_SETTLE
                        atau PAYOUT_REVERSAL (bukan edit saldo) → audit payout.manual_resolve. Disarankan menjalankan
                        status query dulu; endpoint ini untuk kasus bank tidak bisa memberi jawaban otomatis.
```

### GET /ops/recon-runs
```text
Authentication        : OPS
Request               : query from, to (business_date), status, limit, cursor
Response              : 200 { "data": [ { "id", "provider", "business_date", "source", "status",
                                           "matched_count", "missing_internal_count", "missing_external_count",
                                           "amount_mismatch_count", "status_mismatch_count",
                                           "started_at", "finished_at" } ], "page": {…} }
Rate limit            : read
```

### POST /ops/recon-runs
```text
Use case / fitur      : LF-17
Authentication        : OPS
Request               : (a) application/json { "business_date": "2026-03-15", "source": "PULL" }
                        (b) multipart/form-data: business_date, file (text/csv, ≤ 5 MB, kolom sesuai file 03 §1.6)
Response              : 202 { "id", "status": "RUNNING", … }  — run baru (job recon.run)
                        200 { "id", "status": … }               — file dengan hash sama untuk tanggal itu sudah ada
Validation            : business_date ≤ hari ini (Asia/Jakarta); CSV header persis; content-type text/csv
Errors                : 400 VALIDATION_ERROR · 413 PAYLOAD_TOO_LARGE · 415 UNSUPPORTED_MEDIA_TYPE · 401 · 403
Idempotency           : natural (uq_recon_runs_file: provider + business_date + file_hash)
Rate limit            : write
```

### GET /ops/recon-items
```text
Authentication        : OPS
Request               : query resolution (OPEN|RESOLVED), category (multi), recon_run_id, object_type, limit, cursor
Response              : 200 { "data": [ { "id", "recon_run_id", "source", "object_type", "internal_id", "external_ref",
                                           "category", "internal_amount", "external_amount", "internal_status",
                                           "external_status", "resolution", "created_at" } ], "page": {…} }
Rate limit            : read
```

### POST /ops/recon-items/{id}/resolve
```text
Use case / fitur      : LF-17, LF-18
Authentication        : OPS
Request               : header Idempotency-Key
                        body { "action": "APPLY_PAYOUT_SETTLE" | "APPLY_PAYOUT_REVERSAL" | "REPLAY_INBOX_EVENT"
                                         | "NO_ACTION_TIMING" | "NO_ACTION_PROVIDER_ERROR",
                               "evidence_ref": "…", "reason": "…" }
Response              : 200 ReconItem (resolution = RESOLVED, resolution_action, resolved_by, resolved_at)
Validation            : action valid untuk category/object_type item (contoh APPLY_PAYOUT_SETTLE hanya untuk payout
                        STATUS_MISMATCH dengan external SUCCEEDED); reason ≥ 10
Errors                : 400 · 404 · 409 INVALID_STATE_TRANSITION (sudah RESOLVED) · 422 RESOLUTION_EVIDENCE_REQUIRED
                        · 422 IDEMPOTENCY_KEY_REUSED · 403
Idempotency           : required
Rate limit            : write
Catatan               : Aksi APPLY_* memanggil service payout (journal settle/reversal + transisi status) di tx yang sama
                        dengan resolusi item + audit. Demo D.
```

### GET /ops/jobs
```text
Authentication        : OPS
Request               : query status (default dead), kind, limit, cursor
Response              : 200 { "data": [ { "id", "kind", "status", "attempts", "max_attempts", "last_error",
                                           "run_at", "correlation_id", "created_at" } ], "page": {…} }
Catatan               : payload job TIDAK ditampilkan (bisa memuat data sensitif); hanya ringkasan per kind.
Rate limit            : read
```

### POST /ops/jobs/{id}/retry
```text
Authentication        : OPS
Request               : body { "reason": "…" }
Response              : 200 { "id", "status": "queued", "attempts": 0, "run_at": "…" }
Errors                : 404 · 409 INVALID_STATE_TRANSITION (job bukan dead) · 400 · 403
Idempotency           : natural: UPDATE jobs SET status='queued', attempts=0 WHERE id=$1 AND status='dead'
Rate limit            : write
```

### POST /ops/webhook-deliveries/{id}/replay
```text
Use case / fitur      : LF-15, LF-18
Authentication        : OPS
Request               : body { "reason": "…" }
Response              : 202 { "id", "event_id", "status": "PENDING" }
Errors                : 404 · 409 INVALID_STATE_TRANSITION (bukan DEAD) · 400 · 403
Idempotency           : natural: DEAD → PENDING + job webhook.deliver baru; event_id TETAP sama (merchant dedupe)
Rate limit            : write
```

### POST /ops/merchants/{id}/api-keys
```text
Use case / fitur      : LF-09
Authentication        : OPS
Request               : header Idempotency-Key; body { "label": "Server produksi 2026-Q2", "expires_at": null }
Response              : 201 { "id", "prefix": "a1b2c3d4", "slot": 2, "api_key": "lp_test_a1b2c3d4_<secret>",
                              "created_at" }   ← api_key HANYA ditampilkan sekali
Errors                : 404 · 409 API_KEY_LIMIT_REACHED (dua slot terisi) · 422 IDEMPOTENCY_KEY_REUSED · 400 · 403
Idempotency           : required. Replay TIDAK mengembalikan secret lagi: response replay berisi field api_key = null +
                        catatan, karena secret tidak disimpan plaintext (dicatat di response_body tanpa secret).
Rate limit            : write
```

### POST /ops/api-keys/{id}/revoke
```text
Authentication        : OPS
Request               : body { "reason": "…" }
Response              : 200 { "id", "prefix", "revoked_at" }
Errors                : 404 · 400 · 403
Idempotency           : natural (revoke yang sudah revoked mengembalikan revoked_at yang ada)
Rate limit            : write
```

---

## 12. Health

### GET /healthz
```text
Authentication        : —
Response              : 200 { "status": "ok" } — proses hidup (tidak menyentuh DB)
Catatan               : Liveness. Gagal = container di-restart.
```

### GET /readyz
```text
Authentication        : —
Response              : 200 { "status": "ready", "checks": { "db": "ok", "migrations": "ok" } }
                        503 { "status": "not_ready", "checks": { "db": "timeout" } } (problem+json SERVICE_UNAVAILABLE)
Catatan               : Readiness: SELECT 1 (timeout 500 ms) + versi migrasi DB **≥ versi minimum** yang dibutuhkan binary (final review R3-8; bukan `=`, agar rollback app N-1 tetap ready).
                        Gagal = load balancer/Caddy berhenti mengirim trafik (FS-03). Detail dependency tidak
                        mengekspos hostname/versi.
```

---

## 13. Demo A–D → panggilan konkret

```bash
API=https://localhost/api/v1; AUTH="Authorization: Bearer $TOKEN_RINA"

# Demo A — Idempotency
K=demo-a-$(date +%s)
curl -si -X POST $API/transfers -H "$AUTH" -H "Idempotency-Key: $K" \
  -d '{"recipient_email":"andi@example.com","amount":"5000000","currency":"IDR"}'   # 201
curl -si -X POST $API/transfers -H "$AUTH" -H "Idempotency-Key: $K" \
  -d '{"recipient_email":"andi@example.com","amount":"5000000","currency":"IDR"}'   # 201 + Idempotent-Replayed: true
curl -si -X POST $API/transfers -H "$AUTH" -H "Idempotency-Key: $K" \
  -d '{"recipient_email":"andi@example.com","amount":"6000000","currency":"IDR"}'   # 422 IDEMPOTENCY_KEY_REUSED
curl -s $API/wallet -H "$AUTH"                                                      # saldo turun sekali

# Demo B — Concurrency (saldo awal "10000000" = Rp100.000; 100 transfer × Rp10.000)
k6 run scripts/demo/b_concurrency.js   # tiap VU: Idempotency-Key unik → tepat 10× 201, 90× 422 INSUFFICIENT_BALANCE

# Demo C — Payout unknown outcome (rekening berakhiran 0408 = timeout_after_process, file 03 §1.7)
curl -s -X POST $API/payouts -H "$AUTH" -H "Idempotency-Key: demo-c-1" \
  -d '{"amount":"20000000","currency":"IDR","destination":{"bank_code":"MOCKBANK","account_number":"9900000408","holder_name":"Rina"}}'
docker compose kill worker && docker compose start worker
curl -s $API/payouts/$PAYOUT_ID -H "$AUTH"         # UNKNOWN → SUCCEEDED setelah status query
# mock admin lewat SSH tunnel: ssh -L 9091:mock-provider:8080 (admin tidak dipublish)
curl -s http://localhost:9091/admin/transfers?idempotency_key=$PAYOUT_ID   # mock: transfers = 1

# Demo D — Reconciliation
curl -s -X PUT http://localhost:9091/admin/scenario -d '{"payout_status_query":"disabled"}'
curl -s -X POST $API/ops/recon-runs -H "Authorization: Bearer $TOKEN_OPS" -d '{"business_date":"2026-03-15","source":"PULL"}'
curl -s "$API/ops/recon-items?resolution=OPEN" -H "Authorization: Bearer $TOKEN_OPS"   # STATUS_MISMATCH
curl -s -X POST $API/ops/recon-items/$ITEM/resolve -H "Authorization: Bearer $TOKEN_OPS" -H "Idempotency-Key: demo-d-1" \
  -d '{"action":"APPLY_PAYOUT_SETTLE","evidence_ref":"settlement-2026-03-15.csv#L7","reason":"Settlement bank menunjukkan SUCCEEDED"}'
```
