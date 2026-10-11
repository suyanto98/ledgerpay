# LedgerPay Threat Model

> Bagian dari threat model. Format, skala risiko, dan skema ID mengikuti `00-index.md`.
> Threat generik yang berlaku di kedua project (brute force login, JWT, refresh token, XSS, CSRF, CORS, SQL injection, secrets, supply chain) ada di [`15-security-shared-threats.md`](15-security-shared-threats.md) dan tidak diulang di sini. Kontrol `C-xx` didefinisikan di [`17-security-controls.md`](17-security-controls.md).
> Asumsi penyerang: bermotif finansial, punya akun user dan/atau akun merchant yang sah, bisa mengirim request paralel dalam jumlah besar, dan bisa mengamati traffic miliknya sendiri. Penyerang tidak punya akses ke host atau database, kecuali disebut eksplisit (T-LP-17, T-LP-18).

---

## 1. Aset

| Aset | Kenapa berharga | Disimpan di | Pemilik / penjaga |
|---|---|---|---|
| **Saldo & ledger** (`accounts`, `journals`, `journal_entries`) | Uang. Penciptaan saldo tanpa dana = pencurian dari perusahaan; pengurangan tidak sah = pencurian dari user | Postgres (`ledgerpay`) | Modul `ledger` (satu-satunya penulis, ADR-001) |
| **Idempotency records** (`idempotency_keys`) | Menentukan apakah request "sudah terjadi". Manipulasi = efek ganda atau penolakan request sah | Postgres | Modul `idempotency` |
| **Merchant API key** | Membuat payment intent dan refund atas nama merchant, serta membaca data pembayaran merchant | Hash SHA-256 di `merchant_api_keys`; plaintext hanya sekali di respons ops | Ops (pembuatan/pencabutan, kontrak API) |
| **Secret webhook masuk** (provider → LedgerPay) | Siapa pun yang memegangnya bisa memalsukan `charge.succeeded` | Env/secret file (`PROVIDER_WEBHOOK_SECRET`) | Platform |
| **Secret webhook keluar per merchant** | Pemegangnya bisa memalsukan notifikasi pembayaran ke merchant | `merchants.webhook_secret_enc` (AES-GCM, AAD = `merchant_id`) | Ops + merchant |
| **Kunci penandatangan JWT** | Pemegangnya bisa menerbitkan token untuk user mana pun, termasuk `role = OPS` | Env/secret file (ber-`kid`, ADR-009) | Platform |
| **Hak OPS** | Resolusi manual payout `UNKNOWN`, resolusi recon, retry job, replay webhook, pembuatan API key | `users.role = 'OPS'` (hanya lewat seed) | Platform |
| **Rekening tujuan payout** | PII finansial; target pengalihan dana | `payouts.account_number_enc` (AAD = `payout_id`) + `last4` | Modul `payout` |
| **File settlement / recon** | Sumber kebenaran pembanding. Manipulasi dapat menyembunyikan selisih | `recon_runs` (hash file), `recon_items` | Modul `recon` |
| **Audit log** | Bukti untuk investigasi dan non-repudiation | `audit_log` (append-only) | Platform |
| **Secret mock-provider** (`MOCK_API_SECRET`, endpoint `/admin/*`) | Mengendalikan hasil pembayaran/payout di lingkungan demo | Env + jaringan compose | Platform (demo) |

---

## 2. Trust boundary & data flow

```text
                     (TB1) HTTPS + JWT                         (TB2) HTTPS + API key
  Browser user/ops ─────────────────────▶ ┌───────────────────────┐ ◀───────────────── Server merchant
                                           │  Caddy (TLS, header)  │
  mock-provider ──(TB3) HTTPS + HMAC──────▶│  ledgerpay api        │
     (webhook masuk)                       └──────────┬────────────┘
                                                      │ (TB5) pgx, role ledgerpay_app
                                               ┌──────▼──────┐
                                               │  PostgreSQL │
                                               └──────▲──────┘
                                                      │ (TB5)
                                           ┌──────────┴────────────┐  (TB4) HTTPS + Bearer MOCK_API_SECRET
                                           │  ledgerpay worker     │──────────────────────▶ mock-provider API
                                           │                       │──(TB6) HTTPS + HMAC──▶ webhook_url merchant
                                           └───────────────────────┘        (URL dikendalikan merchant!)
   Ops ──(TB7) HTTPS + JWT role=OPS──▶ /api/v1/ops/*
```

| Boundary | Siapa mengautentikasi siapa | Catatan |
|---|---|---|
| TB1 Browser → api | api mengautentikasi user (JWT 15 menit; refresh cookie + `X-Requested-With`) | Browser tidak dipercaya untuk nilai apa pun selain niat user |
| TB2 Merchant → api `/merchant/*` | api mengautentikasi merchant (API key) | Merchant tidak dipercaya terhadap data merchant lain |
| TB3 mock-provider → `/webhooks/provider` | api memverifikasi provider (HMAC + timestamp) | Endpoint publik. Isi event divalidasi terhadap record lokal |
| TB4 worker → mock-provider | Provider mengautentikasi LedgerPay (Bearer). LedgerPay mengautentikasi provider lewat TLS (di V1 demo: jaringan internal) | Respons provider tidak dipercaya buta (T-LP-14) |
| TB5 api/worker → Postgres | Postgres mengautentikasi role `ledgerpay_app` | Role app tidak punya UPDATE/DELETE pada ledger |
| TB6 worker → merchant | Merchant memverifikasi LedgerPay (HMAC per merchant) | **Arah keluar ke URL yang dikendalikan pihak luar → risiko SSRF** (T-LP-09) |
| TB7 Ops → `/ops/*` | api mengautentikasi user + `role = OPS` | Insider dengan hak tinggi (T-LP-16) |

---

## 3. STRIDE per boundary (ringkas)

| Boundary | S | T | R | I | D | E |
|---|---|---|---|---|---|---|
| TB1 Browser | Akun dibajak (01) | Manipulasi amount/recipient (T-LP-03) | Menyangkal transfer → journal + `request_id` + audit | BOLA (T-LP-04), enumerasi (T-LP-05) | Spam endpoint uang (T-LP-19) | Akses `/ops` dengan token user → role check |
| TB2 Merchant | API key dicuri (T-LP-11) | Refund berlebih (T-LP-15) | Menyangkal refund → audit + journal | Membaca intent merchant lain (T-LP-04) | Flood intent (T-LP-19) | — |
| TB3 Webhook masuk | Webhook palsu (T-LP-06) | Payload diubah (T-LP-08) | — (event mentah disimpan di inbox) | — | Flood webhook (T-LP-20) | Kredit saldo tanpa bayar (T-LP-06/07) |
| TB4 Provider API | Provider palsu / DNS spoof | Respons berbahaya (T-LP-14) | — | — | Provider lambat → worker tertahan (timeout) | — |
| TB5 Database | Kredensial DB bocor (T-LP-17) | Edit ledger langsung (T-LP-17) | Hapus jejak → append-only | Dump DB → PII terenkripsi | Pool exhaustion (T-LP-19) | Role app = owner → dicegah konvensi role |
| TB6 Webhook keluar | Webhook palsu ke merchant (T-LP-10) | — | — | Payload ke URL salah | Merchant lambat → bulkhead | **SSRF** (T-LP-09) |
| TB7 Ops | Token OPS dicuri | Resolusi palsu (T-LP-16) | Menyangkal aksi → audit wajib alasan | Pencarian data user tanpa alasan | — | User biasa menjadi OPS → hanya seed |

---

## 4. Threats

### T-LP-01 — Double spend lewat request paralel (race exploitation)  [Inherent: Critical → Residual: Low]
```text
STRIDE        : T, E
Aset          : Saldo & ledger
Threat        : Penyerang menghabiskan saldo yang sama lebih dari sekali dengan mengeksploitasi jeda read-check-write.
Attack scenario: Saldo Rp100.000. Script mengirim 50 POST /transfers (atau confirm payment / payout) sebesar
                Rp100.000 secara paralel, masing-masing dengan Idempotency-Key BERBEDA (agar tidak di-replay),
                dari beberapa koneksi sekaligus. Pada implementasi naif, beberapa request membaca saldo 100.000
                sebelum ada yang menulis, sehingga semuanya lolos.
Mitigation    : - Row lock SELECT … FOR UPDATE pada akun yang terlibat, urutan account_id ascending (ADR-L03).
                - Cek saldo DI DALAM transaksi setelah lock, bukan sebelum.
                - Jaring terakhir di DB: ck_accounts_balance_non_negative (LI-02). Bug di cek aplikasi tetap
                  membuat transaksi gagal, bukan saldo negatif.
                - Payment intent: SELECT intent FOR UPDATE + guard status (LI-13). Refund: lock intent +
                  ck_payment_intents_refund_bound (LI-08).
Detection     : Lonjakan 422 INSUFFICIENT_BALANCE dan 409 RESOURCE_BUSY per user (pola serangan, bukan bug);
                ledger_balance_drift_total dan ledger_unbalanced_journal_total harus tetap 0.
Test          : Concurrency test CI: 100 goroutine × transfer dari satu saldo → tepat N sukses, saldo ≥ 0,
                Σ entries konsisten (Demo B). Test pembanding implementasi naif (hanya di test code).
Status        : MVP
```

### T-LP-02 — Penyalahgunaan idempotency key  [Inherent: High → Residual: Low]
```text
STRIDE        : T, D
Aset          : Idempotency records, saldo
Threat        : (a) Key yang sama dipakai dengan payload berbeda untuk "menukar" efek; (b) menebak/memakai key
                user lain agar request korban di-replay ke respons penyerang (atau sebaliknya); (c) membanjiri
                tabel dengan key unik untuk menghabiskan storage.
Attack scenario: (a) Kirim transfer Rp10.000 dengan key K → sukses; kirim lagi K dengan amount Rp1.000.000,
                berharap server hanya mengecek key. (b) Penyerang mengamati key korban (mis. dari log klien
                yang bocor) lalu mengirim request dengan key yang sama dari akunnya sendiri. (c) 1 juta request
                dengan key acak.
Mitigation    : - (a) request_hash = SHA-256(method + path + canonical body). Hash berbeda → 422
                  IDEMPOTENCY_KEY_REUSED tanpa efek (ADR-L02).
                - (b) Scope key = (client_type, client_id, endpoint, key) (uq_idempotency_keys_scope). Key yang
                  sama dari principal lain adalah key yang berbeda. Tidak ada respons milik orang lain yang
                  bisa di-replay.
                - (c) Panjang key 8–255 (ck_idempotency_keys_key_len); rate limit money-write 30/menit per
                  principal; retensi 7 hari + job cleanup; key hanya disimpan untuk request yang lolos
                  autentikasi dan validasi dasar (400 tidak disimpan).
Detection     : Metrik idempotency_replay_total dan hitungan 422 IDEMPOTENCY_KEY_REUSED per principal
                (spike = klien rusak atau probing); ukuran tabel idempotency_keys.
Test          : Integration test: key sama + payload beda → 422 tanpa efek; key sama dari dua user berbeda →
                dua efek independen; key concurrent → satu efek (Demo A).
Status        : MVP
```

### T-LP-03 — Manipulasi nominal (amount tampering)  [Inherent: High → Residual: Low]
```text
STRIDE        : T
Aset          : Saldo & ledger
Threat        : Nilai nominal yang tidak sah menghasilkan perpindahan uang terbalik, overflow, atau pembulatan
                yang menguntungkan penyerang.
Attack scenario: Mengirim amount "-5000000" (berharap transfer terbalik), "0", "1e9", "5000000.5",
                "99999999999999999999" (overflow int64), angka JSON (bukan string) dengan presisi float,
                currency "USD", atau mengirim field amount pada confirm payment untuk membayar lebih murah.
Mitigation    : - Amount wajib string berpola ^[1-9][0-9]{0,15}$ (tanpa tanda, desimal, eksponen, spasi);
                  parse ke int64 dengan cek overflow; batas per endpoint (transfer Rp1–Rp10.000.000,
                  payout Rp10.000–Rp25.000.000; kontrak API). Angka JSON ditolak (ADR-L04).
                - CHECK amount > 0 di semua tabel transaksi dan journal_entries (LI-11).
                - Currency hanya IDR (CURRENCY_NOT_SUPPORTED + CHECK); composite FK (journal_id, currency) dan
                  (account_id, currency) mencegah entry lintas currency.
                - Arah uang ditentukan server dari jenis operasi dan template purpose (desain domain §6), bukan dari
                  tanda angka.
                - Confirm payment: body kosong; amount diambil dari intent di server. Refund: dibatasi
                  refundable_amount.
Detection     : 400 VALIDATION_ERROR pada field amount per principal; ledger_unbalanced_journal_total = 0.
Test          : Table-driven validation test untuk semua nilai di atas; property test PostJournal.
Status        : MVP
```

### T-LP-04 — BOLA / IDOR pada wallet, transfer, payout, intent, refund  [Inherent: High → Residual: Low]
```text
STRIDE        : I, E
Aset          : Data transaksi, saldo merchant
Threat        : Principal membaca atau mengubah resource milik principal lain dengan mengganti ID di path.
Attack scenario: User A memanggil GET /transfers/{id milik B}, GET /payouts/{id milik B}; Merchant A memanggil
                GET /merchant/payment-intents/{id milik merchant B} atau POST …/{id B}/refunds. ID didapat dari
                kebocoran log/screenshot atau ditebak (bagian timestamp UUIDv7 bisa diperkirakan).
Mitigation    : - Setiap query di-scope ke principal (WHERE user_id = $principal / merchant_id = $principal),
                  bukan "ambil lalu cek" (ADR-010). Tidak ada endpoint yang menerima "from"/"merchant_id" dari
                  client: sumber selalu principal.
                - Resource milik pihak lain → 404 NOT_FOUND (tidak membedakan "ada tapi bukan milikmu").
                - Prefix rute per principal: API key hanya berlaku di /merchant/*, JWT user tidak berlaku di
                  /merchant/*, dan JWT non-OPS ditolak di /ops/*.
                - Refund hanya ke wallet payer asli (lihat T-LP-15).
Detection     : Rasio 404 tinggi per principal pada endpoint ber-ID (probing); log berisi principal + target id.
Test          : BOLA test suite: untuk setiap endpoint ber-ID, fixture dua user dan dua merchant → akses silang
                selalu 404. Ini adalah penjaga LI-15 (APP + TEST, pengecualian yang disadari di desain domain).
Status        : MVP
```

### T-LP-05 — Enumerasi penerima lewat transfer  [Inherent: Medium → Residual: Low]
```text
STRIDE        : I
Aset          : Data user (email terdaftar)
Threat        : Penyerang memakai POST /transfers sebagai oracle untuk mengetahui email mana yang terdaftar.
Attack scenario: Mengirim transfer Rp1 ke ribuan email; respons berbeda untuk "tidak terdaftar" vs "saldo kurang"
                vs "sukses" membocorkan keberadaan akun.
Mitigation    : - Email tidak terdaftar, akun penerima frozen, dan penerima nonaktif → satu code generik
                  422 INVALID_RECIPIENT.
                - Rate limit money-write per user; transfer yang berhasil benar-benar memindahkan uang (biaya
                  nyata bagi penyerang) dan hanya menampilkan display_name terpotong ("Andi S.").
                - Cek INVALID_RECIPIENT dilakukan sebelum cek saldo, agar urutan error tidak membocorkan informasi.
Detection     : Hitungan INVALID_RECIPIENT per user per jam (alert di atas ambang).
Test          : Respons identik (status, code, timing kasar) untuk email tidak ada vs penerima frozen.
Status        : MVP. Enumerasi lewat register (EMAIL_ALREADY_REGISTERED) dibahas di 01.
```

### T-LP-06 — Pemalsuan webhook masuk  [Inherent: Critical → Residual: Low]
```text
STRIDE        : S, E
Aset          : Saldo (kredit top-up)
Threat        : Penyerang mengirim charge.succeeded palsu ke endpoint publik agar top-up miliknya dikreditkan tanpa
                membayar.
Attack scenario: Penyerang membuat top-up Rp5.000.000 (dapat topup_id dari respons), lalu POST
                /webhooks/provider dengan payload charge.succeeded untuk topup_id tersebut, tanpa signature
                atau dengan signature karangan.
Mitigation    : - HMAC-SHA256 atas "<t>.<raw_body>" (bytes persis, sebelum decode), constant-time compare;
                  gagal → 401 INVALID_SIGNATURE tanpa menyentuh DB (ADR-L08, kontrak `14-api-external-contracts.md` §2).
                - Secret provider hanya di env/secret store; tidak pernah di repo, log, atau respons.
                - Validasi isi oleh worker: amount + currency event harus sama dengan record top-up lokal;
                  tidak cocok → inbox DEAD + alert (T-LP-08).
                - Jaring terakhir: recon harian menandai MISSING_EXTERNAL untuk kredit yang tidak ada di
                  settlement provider.
Detection     : Metrik webhook_signature_failure_total (alert jika > 0 berkelanjutan); log IP sumber.
Test          : Webhook tanpa header / signature salah / body diubah satu byte → 401, inbox tidak bertambah.
Status        : MVP
```

### T-LP-07 — Replay webhook  [Inherent: High → Residual: Low]
```text
STRIDE        : S, T
Aset          : Saldo
Threat        : Penyerang menangkap webhook sah (signature valid) lalu mengirim ulang untuk mengkredit dua kali,
                atau mengirim event lama untuk membalik status.
Attack scenario: Webhook charge.succeeded yang valid terekam (proxy/log yang bocor). Penyerang mengirimnya ulang
                10 kali, hari ini dan minggu depan.
Mitigation    : - Window timestamp: |now − t| ≤ 300 detik → di luar itu 400 STALE_WEBHOOK. Timestamp ikut
                  ditandatangani, jadi tidak bisa diganti.
                - Dedupe di dalam window: UNIQUE (provider, provider_event_id) di webhook_inbox → duplikat 200
                  tanpa efek.
                - Transisi state monoton: event untuk top-up yang sudah SUCCEEDED adalah no-op;
                  uq_journals_source (topup, id, TOPUP_CREDIT) menolak journal kedua (LI-06, LI-09).
Detection     : webhook_inbox outcome DUPLICATE/STALE (metrik); 400 STALE_WEBHOOK per IP.
Test          : Event sama dikirim 3× → satu journal; event dengan t − 301 detik → 400.
Status        : MVP
```

### T-LP-08 — Tampering isi webhook / provider memberi data tidak konsisten  [Inherent: High → Residual: Low]
```text
STRIDE        : T
Aset          : Saldo
Threat        : Event bertanda tangan sah tetapi isinya tidak cocok dengan transaksi lokal (bug provider, atau
                secret provider bocor dan penyerang mengubah amount).
Attack scenario: Top-up Rp10.000 dibuat; event charge.succeeded datang dengan amount Rp10.000.000 untuk
                topup_id yang sama.
Mitigation    : - Worker inbox membandingkan amount, currency, dan resource_ref dengan record lokal. Nilai yang
                  dikreditkan SELALU nilai record lokal, bukan nilai event. Tidak cocok → DEAD + alert, tanpa
                  kredit.
                - Event untuk resource yang tidak dikenal → disimpan, ditandai investigasi, tidak pernah
                  membuat top-up baru.
Detection     : webhook_inbox_dead_total (alert segera); recon AMOUNT_MISMATCH.
Test          : Event dengan amount ≠ record → DEAD, saldo tidak berubah.
Status        : MVP
```

### T-LP-09 — SSRF lewat `webhook_url` merchant  [Inherent: High → Residual: Low]
```text
STRIDE        : E, I
Aset          : Jaringan internal (Postgres, mock-provider /admin, metadata cloud), secret di environment
Threat        : Worker LedgerPay mengirim HTTP POST ke URL yang ditentukan pihak luar. URL yang mengarah ke alamat
                internal menjadikan worker sebagai proxy ke jaringan internal.
Attack scenario: webhook_url diset ke http://169.254.169.254/latest/meta-data/ (metadata cloud),
                http://mock-provider:9090/admin/scenario, http://localhost:5432, atau domain milik penyerang yang
                awalnya resolve ke IP publik lalu berganti ke 10.0.0.5 saat koneksi (DNS rebinding), atau yang
                membalas 302 ke alamat internal.
Mitigation    : - Di V1, webhook_url hanya bisa diset oleh ops (merchant di-seed). Ini mengurangi likelihood,
                  tetapi kontrol teknis tetap wajib karena V2 akan memberi merchant self-service.
                - HTTP client khusus webhook (C-44 egress guard): resolve DNS sekali, tolak jika IP termasuk
                  loopback, private (10/8, 172.16/12, 192.168/16), link-local (169.254/16 termasuk metadata),
                  CGNAT 100.64/10, 0.0.0.0/8, multicast, IPv6 ::1/fc00::/7/fe80::/10. Lalu connect ke IP yang
                  sudah divalidasi (custom DialContext), sehingga DNS rebinding tidak bisa mengganti target.
                - HTTPS wajib di mode live (validasi APP; CHECK DB hanya ^https?://). Hanya port 443 (dan 80
                  di dev).
                - Redirect tidak diikuti (3xx = gagal; kontrak `14-api-external-contracts.md` §3.1). Timeout total 5 detik. Body
                  respons dibaca maksimal 64 KB lalu dibuang, dan tidak pernah disimpan atau ditampilkan utuh
                  (mencegah SSRF "baca"). Yang dicatat hanya status code dan durasi.
                - Mode demo: allowlist eksplisit untuk host simulator merchant lokal; daftar blok tetap aktif
                  untuk host lain.
                - Secara jaringan: container worker tidak bisa menjangkau /admin mock-provider di deployment
                  publik (T-LP-21).
Detection     : Metrik webhook_delivery_blocked_total{reason}; audit saat webhook_url diubah.
Test          : Unit test egress guard untuk setiap rentang IP; integration test DNS yang resolve ke 127.0.0.1
                → diblokir; target 302 → gagal tanpa mengikuti.
Status        : MVP (karena murah dan bernilai tinggi untuk interview)
```

### T-LP-10 — Secret webhook merchant bocor / pemalsuan webhook ke merchant  [Inherent: Medium → Residual: Low]
```text
STRIDE        : S
Aset          : Kepercayaan merchant terhadap notifikasi; barang/jasa merchant
Threat        : Penyerang yang tahu secret merchant (atau merchant yang tidak memverifikasi signature) mengirim
                payment.succeeded palsu ke endpoint merchant agar pesanan dikirim tanpa pembayaran.
Attack scenario: Penyerang menemukan endpoint webhook merchant dan mengirim JSON payment.succeeded karangan.
Mitigation    : - Signature LedgerPay-Signature t=…,v1=… per merchant dengan window timestamp; event id stabil
                  untuk dedupe (kontrak `14-api-external-contracts.md` §3).
                - Secret disimpan terenkripsi (webhook_secret_enc), ditampilkan sekali saat dibuat.
                - Rotasi oleh ops dengan masa transisi: dua signature (v1 lama dan v1 baru) dikirim di header
                  selama 24 jam, sehingga merchant bisa berganti tanpa downtime.
                - Dokumentasi kewajiban merchant: verifikasi signature, dan konfirmasi via
                  GET /merchant/payment-intents/{id} sebelum memenuhi pesanan bernilai tinggi.
Detection     : Di luar kendali LedgerPay (sisi merchant). Audit rotasi secret.
Test          : Contoh kode verifikasi di kontrak diuji terhadap vektor signature yang diketahui.
Status        : MVP (signature + rotasi dasar). Dual-signature saat rotasi: Production enhancement.
```

### T-LP-11 — Pencurian merchant API key  [Inherent: High → Residual: Medium]
```text
STRIDE        : S
Aset          : Merchant API key, dana merchant, data pembayaran merchant
Threat        : Key bocor (commit ke GitHub, log aplikasi merchant, laptop developer) lalu dipakai penyerang.
Attack scenario: Penyerang menemukan lp_live_… di repo publik. Ia membaca semua intent merchant (data pembeli),
                membuat intent palsu (phishing ke user), dan mencoba refund untuk menguras saldo merchant.
Mitigation    : - Format berprefix (lp_live_/lp_test_ + prefix publik) sehingga bisa dideteksi secret scanner
                  (gitleaks di CI kita; program secret scanning GitHub = production enhancement).
                - Secret 256-bit; disimpan sebagai SHA-256 (entropi tinggi sehingga hash cepat sudah memadai,
                  ADR-009); plaintext ditampilkan sekali.
                - Pembuatan/pencabutan HANYA lewat ops (kontrak API): key curian tidak bisa membuat key pengganti
                  bagi dirinya sendiri. Dua slot aktif memungkinkan rotasi tanpa downtime.
                - Dampak refund dibatasi: refund hanya ke wallet payer asli dan ≤ amount pembayaran (T-LP-15).
                  Penyerang tidak bisa mengarahkan dana ke dirinya.
                - Kolom last_used_at + log IP per key; key test tidak bisa mengakses data live.
                - Production: IP allowlist per key, key ber-scope (read-only vs write), masa berlaku key.
Detection     : last_used_at dari IP/ASN baru (production); lonjakan refund per merchant
                (refund_amount_total{merchant}); alert jika refund > X% volume harian.
Test          : Key yang di-revoke → 401; key merchant A tidak bisa mengakses resource B (T-LP-04).
Status        : MVP (hash, prefix, ops-only, slot). IP allowlist/scope: Production enhancement.
                Residual Medium: kebocoran data pembayaran merchant sampai key dicabut tetap mungkin.
```

### T-LP-12 — Capability URL checkout ditebak atau bocor (O13)  [Inherent: Medium → Residual: Low]
```text
STRIDE        : I
Aset          : Data intent sebelum dibayar (nama merchant, merchant_order_id, amount, deskripsi)
Threat        : Siapa pun yang memegang ID intent (user login mana pun) bisa melihat ringkasan dan membayarnya
                (keputusan kontrak API). Risikonya: data order terbaca pihak lain, atau ID dipakai untuk phishing.
Attack scenario: (a) Menebak ID intent. (b) ID bocor lewat header Referer, riwayat browser, screenshot, atau log
                proxy. (c) Penyerang membuat intent di merchant miliknya sendiri, lalu mengirim link ke korban
                ("bayar tagihan ini").
Mitigation    : - Analisis tebakan: UUIDv7 punya 48 bit timestamp dan 74 bit acak (google/uuid memakai
                  crypto/rand). Walaupun timestamp bisa dikira-kira, 2^74 kemungkinan per milidetik membuat
                  brute force tidak realistis. Ditambah lagi, endpoint memerlukan login dan dibatasi rate limit
                  read.
                - Data sebelum bayar diminimalkan: nama merchant, amount, deskripsi, expires_at. Tanpa data
                  payer, checkout_url, atau refund (model "viewer sebelum bayar", kontrak API §2.6). Merchant
                  diinstruksikan untuk tidak menaruh PII di description/merchant_order_id.
                - TTL 30 menit, lalu EXPIRED. Setelah SUCCEEDED, hanya payer yang bisa melihat; user lain 404.
                - Halaman checkout memakai Referrer-Policy: no-referrer (C-10 security headers).
                - "Membayar intent orang lain" tidak merugikan pemilik order (merchant tetap dibayar; payer
                  rugi atas kemauannya sendiri). Phishing (c) dimitigasi dengan nama merchant yang jelas di
                  halaman konfirmasi dan merchant yang di-onboard oleh ops (V1).
                - Keputusan: TIDAK menambah client_secret terpisah (pola Stripe) di V1. Alasannya, ID sudah
                  tidak bisa ditebak dan data yang terekspos minim. Client_secret menjadi production enhancement
                  jika checkout dibuka tanpa login atau intent membawa data sensitif.
Detection     : 404 rate per user pada /payment-intents/{id}.
Test          : Intent SUCCEEDED dibuka user lain → 404; field viewer-sebelum-bayar tidak memuat payer/refunds.
Status        : MVP (minimasi data, TTL, 404 setelah bayar). client_secret: Production enhancement.
```

### T-LP-13 — Payout ke rekening penyerang setelah account takeover  [Inherent: High → Residual: Medium]
```text
STRIDE        : S, E
Aset          : Saldo user
Threat        : Penyerang yang berhasil login sebagai korban (password reuse, phishing, sesi dicuri) langsung
                menarik seluruh saldo ke rekening miliknya. Payout adalah satu-satunya jalan keluar uang dari
                sistem, jadi inilah titik yang paling diincar.
Attack scenario: Credential stuffing berhasil → POST /payouts dengan rekening penyerang, berulang sampai saldo
                habis.
Mitigation    : MVP:
                - Pencegahan pengambilalihan akun (brute force lockout, argon2id, refresh reuse detection) → 01.
                - Batas per payout Rp25.000.000 (validasi) + rate limit money-write.
                - Payout bersifat asinkron (202): ada jendela waktu antara request dan submit worker.
                P2 (dari feature matrix):
                - PIN transaksi / step-up auth untuk payout (LF-22).
                - Batas harian payout per user (LF-21).
                Production enhancement:
                - Cooling period 24 jam untuk rekening tujuan baru + notifikasi email/push saat rekening baru
                  dipakai.
                - Device binding / risk scoring (login dari perangkat baru → step-up).
Detection     : Payout pertama ke rekening baru sesaat setelah login dari IP baru (rule sederhana, production);
                audit login.
Test          : Rate limit money-write untuk payout; validasi batas amount.
Status        : MVP sebagian; P2 untuk PIN/limit. Residual Medium diterima di V1 (tidak ada uang sungguhan;
                kontrol anti-ATO lengkap butuh kanal notifikasi yang belum dibangun).
```

### T-LP-14 — Respons provider berbahaya atau tidak konsisten  [Inherent: Medium → Residual: Low]
```text
STRIDE        : T, S
Aset          : Saldo, status payout
Threat        : Provider (atau pihak yang menyamar sebagai provider) membalas dengan data yang tidak sesuai:
                status sukses untuk payout yang tidak diproses, amount berbeda, atau respons raksasa/lambat.
Attack scenario: DNS spoofing di jaringan worker mengarahkan provider ke server penyerang yang selalu membalas
                "SUCCEEDED"; atau bug provider mengembalikan payout milik ID lain.
Mitigation    : - TLS dengan verifikasi sertifikat ke host provider (production); di demo, mock-provider berada
                  di jaringan compose internal.
                - Respons divalidasi terhadap record lokal: idempotency_key/payout_id dan amount yang dikembalikan
                  harus cocok, kalau tidak → UNKNOWN + needs_reconciliation (bukan SUCCEEDED).
                - Timeout klien (tanpa network call di dalam DB tx, ADR-011); body respons dibatasi ukurannya.
                - Recon terhadap settlement report sebagai pembanding independen dari jalur status.
                - Batas: kalau provider sendiri yang disusupi, recon terhadap laporan provider yang sama tidak
                  independen. Production: recon tambahan terhadap mutasi rekening bank (sumber ketiga).
Detection     : recon STATUS_MISMATCH / AMOUNT_MISMATCH; payout_unknown_total.
Test          : Mock mengembalikan amount berbeda → payout UNKNOWN, tidak ada journal settle.
Status        : MVP (validasi respons). Recon sumber ketiga: Production enhancement.
```

### T-LP-15 — Penyalahgunaan refund  [Inherent: Medium → Residual: Low]
```text
STRIDE        : T, E
Aset          : Saldo merchant
Threat        : Refund dipakai untuk mencetak uang (refund melebihi pembayaran, refund paralel) atau untuk
                memindahkan dana merchant ke penyerang.
Attack scenario: Merchant (atau pencuri API key) mengirim 5 refund paralel masing-masing 60% dari pembayaran;
                atau mencoba menentukan wallet tujuan refund.
Mitigation    : - Tujuan refund tidak bisa dipilih: selalu wallet payer asli dari intent (tidak ada field tujuan
                  di API). Pencuri key tidak bisa mengarahkan dana ke dirinya.
                - Lock intent FOR UPDATE + cek refunded_amount + amount ≤ amount; jaring DB
                  ck_payment_intents_refund_bound (LI-08); saldo merchant cukup (LI-02).
                - Hanya intent SUCCEEDED/PARTIALLY_REFUNDED yang bisa di-refund.
Detection     : refund_amount_total per merchant; 422 REFUND_EXCEEDS_PAYMENT per merchant.
Test          : Concurrency test refund paralel (FS-02 Setup B): Σ refund ≤ amount.
Status        : MVP
```

### T-LP-16 — Penyalahgunaan hak OPS (insider)  [Inherent: High → Residual: Medium]
```text
STRIDE        : T, R, E
Aset          : Saldo, status payout, recon
Threat        : Operator (atau token OPS yang dicuri) memakai aksi resolusi untuk memindahkan uang secara tidak sah
                atau menutupi selisih.
Attack scenario: OPS menandai payout UNKNOWN milik kaki tangannya sebagai FAILED padahal bank sudah mengirim
                dana (dana dikembalikan ke wallet, sehingga uang dobel); atau me-resolve mismatch recon tanpa
                bukti agar selisih tidak terlihat.
Mitigation    : - TIDAK ADA endpoint "edit saldo". Semua resolusi menghasilkan journal bertemplate (PAYOUT_SETTLE /
                  PAYOUT_REVERSAL), jadi jejaknya ada di ledger.
                - evidence_ref + reason wajib (422 RESOLUTION_EVIDENCE_REQUIRED); audit_log mencatat actor,
                  alasan, before/after; audit append-only (trigger + grant).
                - Role OPS hanya lewat seed/migrasi, dan tidak ada endpoint untuk memberi role.
                - Resolusi yang salah tetap terdeteksi: recon berikutnya membandingkan dengan settlement provider
                  (payout yang ditandai FAILED tetapi ada di settlement → STATUS_MISMATCH).
                - Production: two-person rule (resolusi di atas ambang butuh approver kedua), sesi OPS lebih
                  pendek + MFA, dan akses OPS dari jaringan terbatas.
Detection     : Laporan harian aksi ops (count per operator); audit; recon mismatch setelah resolusi manual.
Test          : Resolve tanpa evidence → 422; resolve menghasilkan journal + audit dalam satu tx; user non-OPS
                → 403 di /ops/*.
Status        : MVP (evidence + audit + no-edit-balance). Two-person rule/MFA: Production enhancement.
                Residual Medium: satu operator jahat masih bisa membuat resolusi palsu yang baru ketahuan di recon.
```

### T-LP-17 — Manipulasi ledger lewat akses database langsung  [Inherent: High → Residual: Medium]
```text
STRIDE        : T, R
Aset          : Ledger & saldo
Threat        : Penyerang yang mendapatkan kredensial DB (env bocor, RCE di api) mengubah saldo atau menghapus
                journal.
Attack scenario: Dengan kredensial ledgerpay_app: UPDATE accounts SET balance = 999999999 WHERE …; atau DELETE
                FROM journal_entries ….
Mitigation    : - Role app tidak punya UPDATE/DELETE pada journals dan journal_entries (grant) + trigger
                  append-only (dua lapis).
                - accounts.balance MEMANG bisa di-UPDATE oleh role app (saldo materialized), sehingga ini titik
                  lemahnya. Mitigasinya deteksi: job verifikasi membandingkan balance dengan Σ entries; selisih →
                  ledger_balance_drift_total > 0 → alert (LI-03). Penyerang harus memalsukan entries juga,
                  padahal role app tidak bisa memalsukan entries tanpa journal seimbang (deferred trigger LI-01).
                - Kredensial owner (migrasi) tidak pernah ada di container runtime.
                - Port Postgres tidak diekspos ke internet (compose network).
Detection     : ledger_balance_drift_total; audit log Postgres (production: pgaudit).
Test          : Test grant: role app mencoba UPDATE/DELETE journal_entries → 42501; test drift job mendeteksi
                UPDATE balance manual.
Status        : MVP. Residual Medium: penyerang dengan RCE bisa membuat journal seimbang yang sah secara teknis
                (misalnya ADJUSTMENT_MANUAL); ini terdeteksi oleh recon, bukan dicegah.
```

### T-LP-18 — Kebocoran data rekening tujuan payout  [Inherent: Medium → Residual: Low]
```text
STRIDE        : I
Aset          : Nomor rekening user
Threat        : Nomor rekening bocor lewat API, log, atau dump DB.
Attack scenario: Dump DB/backup dicuri; atau respons GET /payouts menampilkan nomor penuh.
Mitigation    : - account_number_enc (AES-GCM, AAD = payout_id, sehingga ciphertext tidak bisa dipindah ke baris
                  lain) + account_number_last4 untuk tampilan (******7890).
                - API hanya mengembalikan nomor ter-mask; nomor penuh hanya didekripsi worker saat submit ke bank.
                - Logger me-redact field account_number (C-23 log redaction).
Detection     : —
Test          : Respons API dan log tidak mengandung nomor penuh (test redaction).
Status        : MVP
```

### T-LP-19 — DoS pada endpoint uang dan exhaustion pool koneksi  [Inherent: Medium → Residual: Low]
```text
STRIDE        : D
Aset          : Ketersediaan layanan
Threat        : Request bervolume tinggi yang menahan lock atau koneksi membuat user lain tidak bisa bertransaksi.
Attack scenario: Banyak akun mengirim transfer ke satu merchant populer, sehingga baris wallet merchant menjadi
                hot row (O11) dan request mengantre di lock; atau ribuan request lambat menghabiskan pool koneksi.
Mitigation    : - Rate limit money-write 30/menit per principal; merchant-api 300/menit per key.
                - lock_timeout 2 detik → 409 RESOURCE_BUSY (tidak mengantre tanpa batas);
                  statement_timeout 5 detik; idle_in_transaction_session_timeout 10 detik (ADR-011).
                - Transaksi pendek tanpa network call (lock ditahan milidetik).
                - Ukuran pool terbatas + readiness gagal saat DB tidak sehat → 503 cepat, bukan menggantung.
                - Hot row merchant: diukur di load test (O11); opsi V2 akun MERCHANT_PENDING + sweep.
Detection     : Latency p95/p99, 409 RESOURCE_BUSY rate, pool wait time, 429 rate.
Test          : Load test (desain DevOps) dengan skenario hot merchant.
Status        : MVP (rate limit + timeout). Proteksi DDoS jaringan (CDN/WAF): Production enhancement.
```

### T-LP-20 — DoS pada endpoint webhook publik  [Inherent: Medium → Residual: Low]
```text
STRIDE        : D
Aset          : Ketersediaan, storage inbox
Threat        : Endpoint /webhooks/provider tidak memakai auth berbasis sesi dan tidak dibatasi rate limit per
                provider, sehingga bisa dibanjiri.
Attack scenario: Jutaan POST dengan body besar atau signature acak.
Mitigation    : - Urutan cek dari yang termurah: ukuran body (64 KB) → format header → timestamp window → HMAC.
                  Semua ditolak SEBELUM menyentuh database. Hanya event bertanda tangan sah yang di-INSERT.
                - Handler tidak memproses efek bisnis (target < 50 ms).
                - Production: IP allowlist provider di Caddy/WAF + rate limit per IP untuk request yang gagal
                  signature.
Detection     : webhook_signature_failure_total, 413 rate.
Test          : Benchmark handler: request bersignature salah tidak menghasilkan query DB.
Status        : MVP (urutan cek). IP allowlist: Production enhancement.
```

### T-LP-21 — Endpoint admin mock-provider dan demo toolbar terbuka di deployment publik  [Inherent: High → Residual: Low]
```text
STRIDE        : E, T
Aset          : Hasil pembayaran/payout di lingkungan demo; kredibilitas demo
Threat        : /admin/* mock-provider (ubah skenario, reset state) atau endpoint demo toolbar (memicu failure)
                bisa dipanggil siapa pun di internet.
Attack scenario: Pengunjung demo publik memanggil PUT /admin/scenario {"payouts":"decline"} atau POST
                /admin/reset tepat saat interview, sehingga demo rusak; atau memakai failure injection untuk
                membuat payout UNKNOWN massal.
Mitigation    : - Caddy hanya mengekspos /checkout/* milik mock-provider ke internet. /v1/* (API provider) dan
                  /admin/* hanya bisa dijangkau dari jaringan compose. Skrip demo dijalankan dari host lewat SSH
                  / docker exec.
                - /admin/* tetap memerlukan header MOCK_ADMIN_TOKEN (defense in depth) dan aktif hanya jika
                  MOCK_ADMIN_ENABLED=true.
                - Endpoint demo toolbar di LedgerPay (/api/v1/demo/*) hanya didaftarkan jika DEMO_MODE=true,
                  memerlukan token demo terpisah, dan dikecualikan dari build produksi (build tag). Test
                  memastikan rute itu tidak ada saat DEMO_MODE=false.
                - Production nyata tidak memakai mock sama sekali (adapter provider asli, desain fitur & arsitektur V2).
Detection     : Log akses /admin/* dan /demo/*.
Test          : Test router: DEMO_MODE=false → /api/v1/demo/* 404; smoke test deployment: /admin dari luar →
                tidak terjangkau.
Status        : MVP
```

### T-LP-22 — TOCTOU pada kedaluwarsa intent dan status  [Inherent: Medium → Residual: Low]
```text
STRIDE        : T
Aset          : Konsistensi status pembayaran
Threat        : Pembayaran dikonfirmasi tepat saat intent kedaluwarsa atau dibatalkan, atau sweeper meng-expire
                intent yang sedang dibayar.
Attack scenario: User menahan halaman checkout lalu menekan bayar di detik ke-1.799 sementara sweeper berjalan;
                atau sengaja memanipulasi jam perangkat.
Mitigation    : - expires_at dibandingkan dengan now() DATABASE di dalam tx setelah SELECT intent FOR UPDATE;
                  jam client tidak pernah dipakai.
                - Sweeper memakai guard UPDATE … WHERE status = 'REQUIRES_CONFIRMATION' AND expires_at < now().
                  Kalau confirm menang lebih dulu, sweeper tidak mendapat baris, dan sebaliknya.
Detection     : 409 INVALID_STATE_TRANSITION dan 422 PAYMENT_EXPIRED.
Test          : Integration test confirm vs sweeper bersamaan → tepat satu yang menang, tanpa journal untuk intent
                yang EXPIRED.
Status        : MVP
```

---

## 5. Ringkasan

| ID | Judul | STRIDE | Inherent | Residual | Status |
|---|---|---|---|---|---|
| T-LP-01 | Double spend lewat request paralel | T, E | Critical | Low | MVP |
| T-LP-02 | Penyalahgunaan idempotency key | T, D | High | Low | MVP |
| T-LP-03 | Manipulasi nominal | T | High | Low | MVP |
| T-LP-04 | BOLA/IDOR resource keuangan | I, E | High | Low | MVP |
| T-LP-05 | Enumerasi penerima | I | Medium | Low | MVP |
| T-LP-06 | Pemalsuan webhook masuk | S, E | Critical | Low | MVP |
| T-LP-07 | Replay webhook | S, T | High | Low | MVP |
| T-LP-08 | Isi webhook tidak konsisten | T | High | Low | MVP |
| T-LP-09 | SSRF via webhook_url merchant | E, I | High | Low | MVP |
| T-LP-10 | Pemalsuan webhook ke merchant / secret bocor | S | Medium | Low | MVP (+ dual-signature: Production) |
| T-LP-11 | Pencurian merchant API key | S | High | Medium | MVP (+ IP allowlist/scope: Production) |
| T-LP-12 | Capability URL checkout (O13) | I | Medium | Low | MVP (+ client_secret: Production) |
| T-LP-13 | Payout ke rekening penyerang setelah ATO | S, E | High | Medium | MVP sebagian, P2, Production |
| T-LP-14 | Respons provider berbahaya | T, S | Medium | Low | MVP (+ recon sumber ketiga: Production) |
| T-LP-15 | Penyalahgunaan refund | T, E | Medium | Low | MVP |
| T-LP-16 | Penyalahgunaan hak OPS | T, R, E | High | Medium | MVP (+ two-person/MFA: Production) |
| T-LP-17 | Manipulasi ledger via DB | T, R | High | Medium | MVP |
| T-LP-18 | Kebocoran rekening tujuan payout | I | Medium | Low | MVP |
| T-LP-19 | DoS endpoint uang / pool | D | Medium | Low | MVP (+ WAF: Production) |
| T-LP-20 | DoS endpoint webhook | D | Medium | Low | MVP (+ IP allowlist: Production) |
| T-LP-21 | Admin mock / demo toolbar terbuka | E, T | High | Low | MVP |
| T-LP-22 | TOCTOU kedaluwarsa intent | T | Medium | Low | MVP |

---

## 6. Risiko sisa yang diterima di V1

| Risiko | Kenapa diterima | Kapan harus ditutup |
|---|---|---|
| **ATO → payout** (T-LP-13, residual Medium) | Tidak ada uang sungguhan. Cooling period dan notifikasi butuh kanal email/push yang belum dibangun (LF-24 P3). PIN (LF-22) dan limit harian (LF-21) ada di P2 | Sebelum ada dana nyata. Minimal: PIN + limit harian + notifikasi rekening baru |
| **Satu operator jahat** (T-LP-16, residual Medium) | Two-person rule butuh workflow approval ops dan lebih dari satu operator. Untuk portfolio, evidence + audit + deteksi lewat recon sudah cukup menunjukkan pola | Saat ada lebih dari satu operator dan resolusi bernilai material |
| **Penyerang dengan RCE/kredensial DB** (T-LP-17, residual Medium) | Pencegahan penuh butuh pemisahan layanan ledger dan HSM/signing journal, di luar skala V1. Yang diberikan: tidak bisa diam-diam (drift job, recon, append-only) | V3 (ledger service terpisah, desain fitur & arsitektur) |
| **Data merchant terbaca dengan key curian sampai dicabut** (T-LP-11, residual Medium) | Deteksi anomali penggunaan key butuh baseline traffic. Dampak finansial sudah dibatasi oleh aturan refund | Saat ada merchant nyata: IP allowlist + key ber-scope |
| **Provider yang disusupi** (T-LP-14) | Recon terhadap laporan provider yang sama tidak independen. Recon terhadap mutasi bank butuh integrasi bank | Go-live dengan provider asli |
| **Webhook palsu oleh pemegang secret provider yang bocor** (T-LP-06/08) | Kredit hanya mungkin untuk top-up milik penyerang dengan amount yang persis sama dengan record lokal, dan recon menandai MISSING_EXTERNAL keesokan harinya. Konfirmasi ke provider sebelum kredit (*verify-by-fetch*: GET /v1/charges/{id} sebelum menerapkan charge.succeeded) dijadwalkan **P1**, karena menambah satu panggilan jaringan di worker inbox | P1 sebelum project dianggap selesai |
