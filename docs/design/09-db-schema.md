# Step 4.3 — LedgerPay: Spesifikasi Skema

> Bagian dari [Step 4 — Database Design](00-index.md). ERD dan keputusan pemodelan D1–D8: [`02-ledgerpay-erd.md`](02-ledgerpay-erd.md).
> Schema `ledgerpay`. Semua tabel di bawah dimiliki `ledgerpay_owner`. Kolom **Grant** menyebut hak `ledgerpay_app`.
> Tabel shared (`users`, `refresh_tokens`, `idempotency_keys`, `jobs`, `audit_log`): lihat [`01-shared-tables.md`](01-shared-tables.md). Kebutuhan khusus LedgerPay atas tabel shared ada di §0.

Notasi kolom: **N** = nullable (✓ = boleh NULL). Default `now()` selalu `timestamptz`.

---

## 0. Kebutuhan LedgerPay atas tabel shared

| Tabel shared | Kebutuhan |
|---|---|
| `users` | Kolom tambahan `role text NOT NULL DEFAULT 'USER'` + `ck_users_role CHECK (role IN ('USER','OPS'))` (D4). Akun OPS hanya dibuat oleh seed |
| `idempotency_keys` | `client_id` = user_id atau merchant_id; `recovery_point` dipakai pola (b) ADR-L02 untuk `POST /topups`. Tanpa FK masuk dari `journals` (D6) |
| `jobs` | Kind LedgerPay: `payout.submit`, `payout.status_check`, `webhook.deliver`, `topup.expire_sweep`, `intent.expire_sweep`, `topup.recover` (recovery point `CREATED` yang tertinggal), `ledger.verify`, `recon.run`, `maintenance.*` (cleanup), `notify.send` (P3) — katalog kanonik di Step 7 `03-background-processing.md` |
| `audit_log` | Tanpa `tenant_id` (LedgerPay tidak multi-tenant). `actor_type` mencakup `USER`, `MERCHANT`, `OPS`, `SYSTEM` |

---

## 1. Fungsi & trigger bersama

```sql
-- Menolak UPDATE/DELETE pada tabel append-only (LI-04, LI-14)
CREATE FUNCTION ledgerpay.forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% on %.% is forbidden (append-only)', TG_OP, TG_TABLE_SCHEMA, TG_TABLE_NAME
    USING ERRCODE = 'P0001', HINT = 'Use a reversal journal';
END $$;

-- Memelihara updated_at pada tabel mutable
CREATE FUNCTION ledgerpay.set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.updated_at := now(); RETURN NEW; END $$;
```

Pola pemasangan:
- Append-only: `CREATE TRIGGER trg_<t>_append_only BEFORE UPDATE OR DELETE ON <t> FOR EACH ROW EXECUTE FUNCTION ledgerpay.forbid_mutation();` ditambah `BEFORE TRUNCATE … FOR EACH STATEMENT` dengan fungsi yang sama.
- Mutable: `CREATE TRIGGER trg_<t>_updated_at BEFORE UPDATE ON <t> FOR EACH ROW EXECUTE FUNCTION ledgerpay.set_updated_at();`

Trigger hanya lapis pertama. Lapis kedua adalah **grant**: tabel append-only tidak memberi `UPDATE`/`DELETE` ke `ledgerpay_app` sama sekali.

---

## 2. `merchants`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK (UUIDv7) |
| `name` | `text` | | | Nama tampilan |
| `status` | `text` | | `'ACTIVE'` | `ACTIVE` / `INACTIVE` (deaktivasi, tanpa delete) |
| `webhook_url` | `text` | ✓ | | Endpoint penerima webhook (D3) |
| `webhook_secret_enc` | `bytea` | ✓ | | Secret HMAC, terenkripsi AES-GCM (`v1:nonce:ct`, AAD = `merchant_id`) |
| `webhook_secret_rotated_at` | `timestamptz` | ✓ | | Info rotasi |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | Audit |
| `version` | `int` | | `1` | Optimistic concurrency untuk edit ops |

**Constraint**
- `pk_merchants PRIMARY KEY (id)`
- `ck_merchants_status CHECK (status IN ('ACTIVE','INACTIVE'))`
- `ck_merchants_webhook_url CHECK (webhook_url IS NULL OR webhook_url ~ '^https?://')` (https diwajibkan di APP untuk mode live; `http` hanya untuk dev/demo lokal)
- `ck_merchants_webhook_pair CHECK ((webhook_url IS NULL) = (webhook_secret_enc IS NULL))`

**Index:** tidak ada selain PK (1 rb baris).
**Trigger:** `updated_at`. **Grant:** `SELECT, INSERT, UPDATE`.

---

## 3. `merchant_api_keys`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `merchant_id` | `uuid` | | | Pemilik |
| `mode` | `text` | | | `live` / `test` (prefix `lp_live_`/`lp_test_`, ADR-009) |
| `prefix` | `text` | | | 8 karakter publik untuk lookup dan log |
| `key_hash` | `bytea` | | | SHA-256 dari secret (entropi tinggi, jadi tidak perlu argon2, ADR-009) |
| `slot` | `smallint` | | | 1 atau 2 (rotasi: maksimal dua key aktif) |
| `label` | `text` | ✓ | | Catatan ops |
| `last_used_at` | `timestamptz` | ✓ | | Diperbarui maksimal 1×/menit per key (throttle di APP untuk menghindari write amplification) |
| `expires_at` | `timestamptz` | ✓ | | Opsional |
| `revoked_at` | `timestamptz` | ✓ | | NULL = aktif |
| `created_by` | `uuid` | | | User OPS |
| `created_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_merchant_api_keys PRIMARY KEY (id)`
- `fk_merchant_api_keys_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id)`
- `uq_merchant_api_keys_prefix UNIQUE (prefix)`
- `uq_merchant_api_keys_key_hash UNIQUE (key_hash)`
- `ck_merchant_api_keys_mode CHECK (mode IN ('live','test'))`
- `ck_merchant_api_keys_slot CHECK (slot IN (1,2))`
- `uq_merchant_api_keys_active_slot UNIQUE (merchant_id, slot) WHERE revoked_at IS NULL` (partial unique index)

**"Maksimal dua key aktif per merchant"** tidak bisa ditulis sebagai "COUNT ≤ 2" di index. Solusinya adalah **slot**: setiap key aktif menempati slot 1 atau 2, dan partial unique memastikan tidak ada dua key aktif di slot yang sama, sehingga paling banyak ada dua. Rotasi = buat key di slot yang kosong, lalu revoke key lama. Key yang kedaluwarsa tetapi belum di-revoke masih menempati slot; job housekeeping me-revoke key kedaluwarsa.

**Index:** lookup autentikasi memakai `uq_merchant_api_keys_prefix` (prefix → hash, lalu bandingkan constant-time).
**Grant:** `SELECT, INSERT, UPDATE (last_used_at, revoked_at, label)`.

---

## 4. `accounts`

> **Implementasi Sprint 2 (penyimpangan yang disengaja):**
> - `accounts.merchant_id` + FK ke `merchants` belum ada; ditambahkan di Sprint 4 lewat migrasi expand (tabel `merchants` belum ada). `ck_accounts_owner` sementara: sistem ⇔ `system_code`, wallet ⇔ `user_id`.
> - `journals.correlation_id` bertipe `text`, bukan `uuid`: request ID dari client boleh berbentuk bebas (selaras dengan `audit_log.correlation_id`).
> - Index FK composite menyertakan `currency`: `ix_journal_entries_journal_id (journal_id, currency)` dan `ix_journal_entries_account_created (account_id, currency, created_at, id)`, karena FK-nya `(journal_id, currency)` dan `(account_id, currency)` (catalogue test C4).
> - Tambahan `ix_accounts_user_id (user_id)` penuh: partial unique `uq_accounts_user_currency` tidak bisa dipakai cek FK.

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK; juga kunci urutan lock (`ORDER BY id`, ADR-L03) |
| `user_id` | `uuid` | ✓ | | Pemilik wallet user (D1) |
| `merchant_id` | `uuid` | ✓ | | Pemilik wallet merchant (D1) |
| `system_code` | `text` | ✓ | | `PROVIDER_CLEARING`, `BANK_CLEARING`, `PAYOUT_IN_TRANSIT`, `FEE_REVENUE` |
| `is_system` | `boolean` | | | Akun sistem tidak punya saldo materialized (ADR-L07) |
| `type` | `text` | | | `ASSET` / `LIABILITY` / `REVENUE` / `SUSPENSE` |
| `normal_side` | `char(1)` | | | `D` / `C` |
| `currency` | `char(3)` | | `'IDR'` | ISO 4217 |
| `balance` | `bigint` | ✓ | | Saldo materialized (minor unit) untuk wallet; NULL untuk akun sistem |
| `status` | `text` | | `'ACTIVE'` | `ACTIVE` / `FROZEN` |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | |

Saldo wallet (normal kredit) = Σ kredit − Σ debit. Nilai ini yang disimpan di `balance` dan diverifikasi oleh job `ledger.verify` (LI-03).

**Constraint**
- `pk_accounts PRIMARY KEY (id)`
- `uq_accounts_id_currency UNIQUE (id, currency)` (target composite FK dari `journal_entries`)
- `fk_accounts_user_id FOREIGN KEY (user_id) REFERENCES users (id)`
- `fk_accounts_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id)`
- `uq_accounts_user_currency UNIQUE (user_id, currency) WHERE user_id IS NOT NULL`
- `uq_accounts_merchant_currency UNIQUE (merchant_id, currency) WHERE merchant_id IS NOT NULL`
- `uq_accounts_system_code_currency UNIQUE (system_code, currency) WHERE system_code IS NOT NULL`
- `ck_accounts_owner CHECK ((is_system AND user_id IS NULL AND merchant_id IS NULL AND system_code IS NOT NULL) OR (NOT is_system AND system_code IS NULL AND num_nonnulls(user_id, merchant_id) = 1))`
- `ck_accounts_type CHECK (type IN ('ASSET','LIABILITY','REVENUE','SUSPENSE'))`
- `ck_accounts_normal_side CHECK ((type = 'ASSET' AND normal_side = 'D') OR (type IN ('LIABILITY','REVENUE','SUSPENSE') AND normal_side = 'C'))`
- `ck_accounts_wallet_is_liability CHECK (is_system OR type = 'LIABILITY')`
- `ck_accounts_balance_presence CHECK (is_system = (balance IS NULL))` (ADR-L07)
- **`ck_accounts_balance_non_negative CHECK (is_system OR balance >= 0)`** (LI-02, jaring terakhir)
- `ck_accounts_currency CHECK (currency ~ '^[A-Z]{3}$')`
- `ck_accounts_status CHECK (status IN ('ACTIVE','FROZEN'))`

**Storage:** `WITH (fillfactor = 80)`. `balance` di-update di setiap transaksi; ruang kosong di page + **tidak ada index pada `balance`/`updated_at`** memungkinkan *HOT update* (tanpa menulis ulang index), sehingga bloat dan write amplification turun.

**Index:** PK dan partial unique di atas sudah melayani semua lookup (by id untuk lock, by user/merchant untuk resolve wallet, by system_code untuk konstanta akun sistem).
**Trigger:** `updated_at`.
**Grant:** `SELECT, INSERT, UPDATE (balance, status, updated_at)`. Hak UPDATE kolom dibatasi: `type`, `currency`, dan owner tidak bisa diubah oleh aplikasi sama sekali.

> **Alternatif yang dipertimbangkan:** saldo dipelihara trigger DB (`AFTER INSERT ON journal_entries` → update `accounts.balance`), sehingga LI-03 benar secara konstruksi. Alternatif ini ditolak untuk V1. Alasannya, cek saldo cukup (422 yang ramah) tetap harus dilakukan aplikasi *sebelum* insert, logika tersebar di dua tempat, dan perilaku trigger lebih sulit diuji/didemokan. Verifikasi berkala + metrik drift dipilih sebagai gantinya.

---

## 5. `journals`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `purpose` | `text` | | | `JournalPurpose` (Step 3 §6) |
| `source_type` | `text` | | | Jenis objek bisnis sumber |
| `source_id` | `uuid` | | | ID objek bisnis (tanpa FK, D5) |
| `currency` | `char(3)` | | | Semua entry wajib currency ini (composite FK) |
| `reversal_of` | `uuid` | ✓ | | Journal asal yang dibalik |
| `correlation_id` | `uuid` | | | Dari request/job (LI-14) |
| `idempotency_key_id` | `uuid` | ✓ | | Referensi longgar ke `idempotency_keys` (D6); NULL untuk journal dari worker |
| `actor_type` | `text` | | | `USER` / `MERCHANT` / `OPS` / `SYSTEM` |
| `actor_id` | `uuid` | ✓ | | NULL untuk `SYSTEM` |
| `description` | `text` | ✓ | | Wajib untuk `ADJUSTMENT_MANUAL` (alasan ops) |
| `created_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_journals PRIMARY KEY (id)`
- `uq_journals_id_currency UNIQUE (id, currency)` (target composite FK)
- **`uq_journals_source UNIQUE (source_type, source_id, purpose)`** (LI-06)
- `fk_journals_reversal_of FOREIGN KEY (reversal_of) REFERENCES journals (id)`
- **`uq_journals_reversal_of UNIQUE (reversal_of)`** (LI-04: satu journal maksimal di-reverse sekali; NULL boleh banyak)
- `ck_journals_purpose CHECK (purpose IN ('TOPUP_CREDIT','TRANSFER','PAYMENT','REFUND','PAYOUT_HOLD','PAYOUT_SETTLE','PAYOUT_REVERSAL','PROVIDER_SETTLEMENT','ADJUSTMENT_MANUAL'))`
- `ck_journals_purpose_source` (pasangan sah purpose ↔ source_type):
  ```sql
  CHECK (
    (purpose = 'TOPUP_CREDIT'        AND source_type = 'topup') OR
    (purpose = 'TRANSFER'            AND source_type = 'transfer') OR
    (purpose = 'PAYMENT'             AND source_type = 'payment_intent') OR
    (purpose = 'REFUND'              AND source_type = 'refund') OR
    (purpose IN ('PAYOUT_HOLD','PAYOUT_SETTLE','PAYOUT_REVERSAL') AND source_type = 'payout') OR
    (purpose = 'PROVIDER_SETTLEMENT' AND source_type = 'provider_settlement') OR  -- source_id = topup_id (Step 12 R1-5)
    (purpose = 'ADJUSTMENT_MANUAL'   AND source_type = 'ops_action')
  )
  ```
- `ck_journals_reversal_purpose CHECK (reversal_of IS NULL OR purpose IN ('PAYOUT_REVERSAL','ADJUSTMENT_MANUAL'))`
- `ck_journals_actor CHECK (actor_type IN ('USER','MERCHANT','OPS','SYSTEM') AND ((actor_type = 'SYSTEM') = (actor_id IS NULL)))`
- `ck_journals_manual_reason CHECK (purpose <> 'ADJUSTMENT_MANUAL' OR (actor_type = 'OPS' AND description IS NOT NULL))`

**Index:** `uq_journals_source` juga melayani lookup "journal dari objek X" (prefix `source_type, source_id`). Tidak ada index `created_at` di V1 (lihat dok 04 §3).
**Trigger:** `trg_journals_append_only`; constraint trigger keseimbangan (§6).
**Grant:** `SELECT, INSERT` saja.

---

## 6. `journal_entries`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `journal_id` | `uuid` | | | Journal induk |
| `account_id` | `uuid` | | | Akun yang diposting |
| `direction` | `char(1)` | | | `D` / `C` |
| `amount` | `bigint` | | | Minor unit, selalu positif (arah dari `direction`) |
| `currency` | `char(3)` | | | Sama dengan journal dan akun (dipaksa composite FK) |
| `created_at` | `timestamptz` | | `now()` | Sama dengan journal (satu tx); dipakai history dan partisi V2 |

**Constraint**
- `pk_journal_entries PRIMARY KEY (id)`
- `fk_journal_entries_journal FOREIGN KEY (journal_id, currency) REFERENCES journals (id, currency)`
- `fk_journal_entries_account FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency)`
- `ck_journal_entries_direction CHECK (direction IN ('D','C'))`
- `ck_journal_entries_amount_positive CHECK (amount > 0)` (LI-01, LI-11)

> **Kenapa composite FK, bukan trigger, untuk currency:** `(journal_id, currency) → journals(id, currency)` membuat entry ber-currency berbeda dari journal-nya **mustahil secara deklaratif**. Hal yang sama berlaku untuk akun. Lebih sederhana dan lebih kuat daripada trigger `BEFORE INSERT`, dengan biaya satu unique index tambahan di `journals` dan `accounts`.

**Constraint trigger keseimbangan (LI-01)**

Entry di-insert satu per satu di dalam satu transaksi, sehingga pemeriksaan harus **ditunda sampai COMMIT**:

```sql
CREATE FUNCTION ledgerpay.check_journal_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  jid uuid; sum_d bigint; sum_c bigint; n int;
BEGIN
  -- IF, bukan CASE (Sprint 0 Spike 2a): PL/pgSQL mengevaluasi seluruh ekspresi CASE,
  -- sehingga NEW.journal_id error saat trigger berjalan di tabel journals.
  IF TG_TABLE_NAME = 'journals' THEN
    jid := NEW.id;
  ELSE
    jid := NEW.journal_id;
  END IF;
  SELECT coalesce(sum(amount) FILTER (WHERE direction = 'D'), 0),
         coalesce(sum(amount) FILTER (WHERE direction = 'C'), 0),
         count(*)
    INTO sum_d, sum_c, n
    FROM ledgerpay.journal_entries
   WHERE journal_id = jid;

  IF n < 2 OR sum_d <> sum_c THEN
    RAISE EXCEPTION 'journal % unbalanced: entries=%, debit=%, credit=%', jid, n, sum_d, sum_c
      USING ERRCODE = '23514', CONSTRAINT = 'ck_journals_balanced';
  END IF;
  RETURN NULL;
END $$;

-- Pada journals: menangkap journal tanpa entry sama sekali
CREATE CONSTRAINT TRIGGER trg_journals_balanced
  AFTER INSERT ON ledgerpay.journals
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION ledgerpay.check_journal_balanced();

-- Pada entries: menangkap Σ D ≠ Σ C atau hanya satu entry
CREATE CONSTRAINT TRIGGER trg_journal_entries_balanced
  AFTER INSERT ON ledgerpay.journal_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION ledgerpay.check_journal_balanced();
```

Catatan:
- Pemicu dipasang di **kedua tabel**: trigger hanya di entries tidak akan pernah jalan untuk journal tanpa entry.
- Trigger berjalan per baris (constraint trigger wajib `FOR EACH ROW`), jadi journal 2 entry dicek 3×. Biayanya kecil karena `ix_journal_entries_journal_id`. Kalau profil menunjukkan beban, fungsi bisa memakai penanda per-transaksi (`set_config('ledgerpay.checked_<jid>', …, true)`) untuk melewati cek ulang.
- `CONSTRAINT = 'ck_journals_balanced'` membuat error bisa dipetakan oleh `platform/db` seperti constraint biasa.
- `ledger.PostJournal` sudah memvalidasi hal yang sama sebelum insert (APP, error ramah). Trigger ini **jaring terakhir** kalau ada bug atau jalur lain.

**Index**
- `ix_journal_entries_journal_id (journal_id)`: trigger keseimbangan, detail journal, validasi reversal (cermin entry asal).
- `ix_journal_entries_account_created (account_id, created_at, id) INCLUDE (direction, amount)`: riwayat wallet (cursor), verifikasi saldo per akun (*index-only scan*), saldo akun sistem.

**Trigger:** `trg_journal_entries_append_only` + constraint trigger di atas.
**Grant:** `SELECT, INSERT` saja.

---

## 7. `transfers`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK; juga `source_id` journal |
| `sender_user_id` / `receiver_user_id` | `uuid` | | | Pihak |
| `sender_account_id` / `receiver_account_id` | `uuid` | | | Wallet yang dikunci |
| `amount` | `bigint` | | | Minor unit |
| `currency` | `char(3)` | | | |
| `note` | `text` | ✓ | | Catatan user (≤ 140 karakter) |
| `status` | `text` | | `'COMPLETED'` | Satu-satunya status (Step 3 glosarium) |
| `journal_id` | `uuid` | | | Journal `TRANSFER` |
| `idempotency_key_id` | `uuid` | | | Referensi longgar (D6) |
| `created_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_transfers PRIMARY KEY (id)`
- `fk_transfers_sender_user_id`, `fk_transfers_receiver_user_id` → `users (id)`
- `fk_transfers_sender_account_id`, `fk_transfers_receiver_account_id` → `accounts (id)`
- `fk_transfers_journal_id FOREIGN KEY (journal_id) REFERENCES journals (id)`; `uq_transfers_journal_id UNIQUE (journal_id)`
- `ck_transfers_amount_positive CHECK (amount > 0)`
- `ck_transfers_not_self CHECK (sender_user_id <> receiver_user_id)`
- `ck_transfers_status CHECK (status = 'COMPLETED')`
- `ck_transfers_note_length CHECK (note IS NULL OR char_length(note) <= 140)`

Transfer yang gagal tidak membuat baris. Respons error-nya disimpan di `idempotency_keys` untuk replay (ADR-L02).

> **Catatan implementasi Sprint 3 (migrasi `00003`):** index FK ditambahkan untuk keempat FK user/akun
> (`ix_transfers_sender_user_id (sender_user_id, created_at)`, `ix_transfers_receiver_user_id (receiver_user_id, created_at)`,
> `ix_transfers_sender_account_id`, `ix_transfers_receiver_account_id`), karena catalogue test C4 mewajibkan setiap FK
> punya index. Tabel juga diberi trigger append-only (`forbid_update_delete`), konsisten dengan grant `SELECT, INSERT`.

**Index:** tidak ada tambahan. Riwayat dibaca dari `journal_entries` per akun; detail transfer dibaca by PK.
**Trigger:** append-only. **Grant:** `SELECT, INSERT`.

---

## 8. `topups`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK; dipakai juga sebagai idempotency key ke provider |
| `user_id` / `account_id` | `uuid` | | | Pemilik dan wallet tujuan |
| `amount` | `bigint` | | | |
| `currency` | `char(3)` | | | |
| `status` | `text` | | `'PENDING'` | Lihat CHECK |
| `recovery_point` | `text` | | `'CREATED'` | `CREATED` (charge belum dibuat di provider) → `PROVIDER_CREATED` |
| `provider` | `text` | | `'mock'` | Nama provider |
| `provider_ref` | `text` | ✓ | | `charge_id` dari provider |
| `checkout_url` | `text` | ✓ | | URL hosted checkout |
| `expires_at` | `timestamptz` | | | Batas sweeper (default +24 jam) |
| `succeeded_at` / `failed_at` / `expired_at` | `timestamptz` | ✓ | | Waktu transisi |
| `failure_reason` | `text` | ✓ | | Dari event provider |
| `journal_id` | `uuid` | ✓ | | Journal `TOPUP_CREDIT` |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_topups PRIMARY KEY (id)`
- FK `user_id → users`, `account_id → accounts`, `journal_id → journals`
- `uq_topups_journal_id UNIQUE (journal_id)`
- `uq_topups_provider_ref UNIQUE (provider, provider_ref)` (NULL boleh banyak selama `CREATED`)
- `ck_topups_amount_positive CHECK (amount > 0)`
- `ck_topups_status CHECK (status IN ('PENDING','SUCCEEDED','FAILED','EXPIRED'))`
- `ck_topups_recovery_point CHECK (recovery_point IN ('CREATED','PROVIDER_CREATED'))`
- `ck_topups_provider_ref_presence CHECK ((recovery_point = 'PROVIDER_CREATED') = (provider_ref IS NOT NULL))`
- `ck_topups_journal_presence CHECK ((status = 'SUCCEEDED') = (journal_id IS NOT NULL))`

**Index**
- `ix_topups_expire_sweep (expires_at) WHERE status = 'PENDING'`: sweeper `topup.expire_sweep`.
- `ix_topups_recover (created_at) WHERE recovery_point = 'CREATED'`: job `topup.recover` (charge provider yang belum sempat dibuat karena crash di antara Tx1 dan Tx2).
- `ix_topups_user_created (user_id, created_at, id)`: daftar top-up user (riwayat yang menampilkan `PENDING`, yang belum punya journal).
- `ix_topups_provider_created (provider, created_at)`: recon memuat sisi internal per rentang tanggal.

**Trigger:** `updated_at`; trigger guard status terminal (opsional, Step 3 LI-07): menolak UPDATE yang mengubah `status` dari `SUCCEEDED` atau `FAILED`.
**Grant:** `SELECT, INSERT, UPDATE`.

---

## 9. `payment_intents`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `merchant_id` / `merchant_account_id` | `uuid` | | | Penerima |
| `merchant_order_id` | `text` | | | ID order dari merchant |
| `amount` | `bigint` | | | |
| `currency` | `char(3)` | | | |
| `refunded_amount` | `bigint` | | `0` | Σ refund (LI-08) |
| `status` | `text` | | `'REQUIRES_CONFIRMATION'` | Lihat CHECK |
| `description` | `text` | ✓ | | Ditampilkan di checkout |
| `payer_user_id` / `payer_account_id` | `uuid` | ✓ | | Terisi saat confirm |
| `journal_id` | `uuid` | ✓ | | Journal `PAYMENT` |
| `expires_at` | `timestamptz` | | | Default +30 menit |
| `confirmed_at` / `cancelled_at` / `expired_at` | `timestamptz` | ✓ | | |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_payment_intents PRIMARY KEY (id)`
- FK `merchant_id → merchants`, `merchant_account_id → accounts`, `payer_user_id → users`, `payer_account_id → accounts`, `journal_id → journals`
- `uq_payment_intents_journal_id UNIQUE (journal_id)`
- **`uq_payment_intents_merchant_order UNIQUE (merchant_id, merchant_order_id) WHERE status IN ('REQUIRES_CONFIRMATION','SUCCEEDED','PARTIALLY_REFUNDED','REFUNDED')`** (LI-13). Order yang intent-nya `EXPIRED`/`CANCELLED` boleh dibuat ulang; order yang sudah dibayar tidak
- `ck_payment_intents_amount_positive CHECK (amount > 0)`
- **`ck_payment_intents_refund_bound CHECK (refunded_amount >= 0 AND refunded_amount <= amount)`** (LI-08)
- `ck_payment_intents_status CHECK (status IN ('REQUIRES_CONFIRMATION','SUCCEEDED','PARTIALLY_REFUNDED','REFUNDED','EXPIRED','CANCELLED'))`
- `ck_payment_intents_paid_consistency CHECK ((status IN ('SUCCEEDED','PARTIALLY_REFUNDED','REFUNDED')) = (journal_id IS NOT NULL AND payer_user_id IS NOT NULL))`
- `ck_payment_intents_refund_status CHECK ((status = 'REFUNDED' AND refunded_amount = amount) OR (status = 'PARTIALLY_REFUNDED' AND refunded_amount > 0 AND refunded_amount < amount) OR (status NOT IN ('REFUNDED','PARTIALLY_REFUNDED') AND refunded_amount = 0))`

**Index**
- `ix_payment_intents_merchant_created (merchant_id, created_at, id)`: merchant me-list intent miliknya (cursor).
- `ix_payment_intents_expire_sweep (expires_at) WHERE status = 'REQUIRES_CONFIRMATION'`: sweeper `intent.expire_sweep`.
- `uq_payment_intents_merchant_order` juga melayani lookup merchant by `merchant_order_id`.

**Trigger:** `updated_at`; guard status terminal (`EXPIRED`, `CANCELLED`, `REFUNDED` tidak boleh berubah).
**Grant:** `SELECT, INSERT, UPDATE`.

---

## 10. `refunds`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK; `source_id` journal `REFUND` |
| `payment_intent_id` | `uuid` | | | Pembayaran asal |
| `merchant_id` | `uuid` | | | Denormalisasi untuk scoping BOLA (`WHERE merchant_id = $principal`) |
| `amount` | `bigint` | | | |
| `currency` | `char(3)` | | | |
| `reason` | `text` | ✓ | | Dari merchant |
| `journal_id` | `uuid` | | | Journal `REFUND` |
| `idempotency_key_id` | `uuid` | | | Referensi longgar |
| `created_at` | `timestamptz` | | `now()` | |

**Constraint:** `pk_refunds`; FK `payment_intent_id → payment_intents`, `merchant_id → merchants`, `journal_id → journals`; `uq_refunds_journal_id UNIQUE (journal_id)`; `ck_refunds_amount_positive CHECK (amount > 0)`.
**Index:** `ix_refunds_payment_intent (payment_intent_id, created_at)`: daftar refund per intent.
**Trigger:** append-only. **Grant:** `SELECT, INSERT`.

---

## 11. `payouts`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK; **idempotency key ke bank** (ADR-L06) |
| `user_id` / `account_id` | `uuid` | | | Pemilik dan wallet sumber |
| `amount` | `bigint` | | | |
| `currency` | `char(3)` | | | |
| `status` | `text` | | `'PENDING'` | Lihat CHECK |
| `bank_code` | `text` | | | Snapshot tujuan (D2) |
| `account_number_enc` | `bytea` | | | AES-GCM, AAD = `payout_id` |
| `account_number_last4` | `char(4)` | | | Untuk tampilan ter-mask (`****1234`) |
| `holder_name` | `text` | | | Snapshot |
| `hold_journal_id` | `uuid` | | | Journal `PAYOUT_HOLD` |
| `settle_journal_id` | `uuid` | ✓ | | Journal `PAYOUT_SETTLE` |
| `reversal_journal_id` | `uuid` | ✓ | | Journal `PAYOUT_REVERSAL` |
| `submit_attempts` | `int` | | `0` | Termasuk resubmit setelah `NOT_FOUND` |
| `status_check_attempts` | `int` | | `0` | Jadwal 30 dtk → … → 6 jam |
| `needs_reconciliation` | `boolean` | | `false` | Jadwal status check habis (ADR-L06) |
| `last_provider_status` | `text` | ✓ | | Jawaban mentah terakhir (`PROCESSING`, `NOT_FOUND`, …) |
| `last_error` | `text` | ✓ | | Error teknis terakhir |
| `submitted_at` / `resolved_at` | `timestamptz` | ✓ | | |
| `resolved_by` | `uuid` | ✓ | | User OPS jika diselesaikan manual |
| `resolution_source` | `text` | ✓ | | `BANK_RESPONSE` / `STATUS_QUERY` / `RECON` / `OPS` |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_payouts PRIMARY KEY (id)`
- FK `user_id → users`, `account_id → accounts`, ketiga `*_journal_id → journals`
- `uq_payouts_hold_journal_id`, `uq_payouts_settle_journal_id`, `uq_payouts_reversal_journal_id` (UNIQUE)
- `ck_payouts_amount_positive CHECK (amount > 0)`
- `ck_payouts_status CHECK (status IN ('PENDING','SUBMITTED','SUCCEEDED','FAILED','UNKNOWN'))`
- `ck_payouts_outcome_journal CHECK ((status = 'SUCCEEDED') = (settle_journal_id IS NOT NULL) AND (status = 'FAILED') = (reversal_journal_id IS NOT NULL))`. Settle dan reversal tidak mungkin keduanya terisi (LI-07, LI-10)
- `ck_payouts_resolution_source CHECK (resolution_source IS NULL OR resolution_source IN ('BANK_RESPONSE','STATUS_QUERY','RECON','OPS'))`
- `ck_payouts_resolved CHECK ((status IN ('SUCCEEDED','FAILED')) = (resolved_at IS NOT NULL AND resolution_source IS NOT NULL))`
- `ck_payouts_ops_resolution CHECK (resolution_source <> 'OPS' OR resolved_by IS NOT NULL)`
- `ck_payouts_last4 CHECK (account_number_last4 ~ '^[0-9]{4}$')`

**Index**
- `ix_payouts_user_created (user_id, created_at, id)`: riwayat payout user.
- `ix_payouts_unknown (updated_at) WHERE status = 'UNKNOWN'`: metrik umur `UNKNOWN` tertua + daftar ops.
- `ix_payouts_needs_recon (created_at) WHERE needs_reconciliation`: antrean investigasi ops.
- `ix_payouts_stuck_submitted (updated_at) WHERE status = 'SUBMITTED'`: deteksi `SUBMITTED` yang menggantung (crash sebelum hasil dicatat).
- `ix_payouts_created (created_at)`: recon memuat payout per rentang tanggal.

> `next_status_check_at` **sengaja tidak disimpan** di `payouts`. Penjadwalan status check sudah tercatat di `jobs.run_at` (job `payout.status_check`), dan menyimpan dua sumber jadwal mengundang drift. Ops UI membaca jadwal berikutnya dari job terkait.

**Trigger:** `updated_at`; guard status terminal (`SUCCEEDED`/`FAILED` tidak berubah).
**Grant:** `SELECT, INSERT, UPDATE`.

---

## 12. `webhook_inbox`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `provider` | `text` | | | `mock` |
| `provider_event_id` | `text` | | | ID event dari provider |
| `event_type` | `text` | | | `charge.succeeded`, `charge.failed`, `charge.pending`, … |
| `resource_ref` | `text` | | | `charge_id` / `provider_ref` |
| `payload` | `jsonb` | | | Body ter-parse |
| `raw_body` | `bytea` | | | Body mentah persis (bukti signature, replay) |
| `signature_ts` | `timestamptz` | | | Timestamp dari header signature |
| `status` | `text` | | `'RECEIVED'` | `RECEIVED` / `PROCESSED` / `IGNORED` / `DEAD` |
| `outcome` | `text` | ✓ | | `APPLIED` / `DUPLICATE` / `STALE` / `CONFLICT` (O2) |
| `attempts` | `int` | | `0` | Percobaan pemrosesan |
| `next_attempt_at` | `timestamptz` | | `now()` | Backoff |
| `locked_by` / `locked_until` | `text` / `timestamptz` | ✓ | | Claim `SKIP LOCKED` + lease |
| `last_error` | `text` | ✓ | | |
| `received_at` | `timestamptz` | | `now()` | |
| `processed_at` | `timestamptz` | ✓ | | |

**Arti status × outcome (O2)**

| status | outcome yang sah | Arti |
|---|---|---|
| `RECEIVED` | NULL | Belum diproses |
| `PROCESSED` | `APPLIED` | Transisi diterapkan (+ journal jika ada) |
| `PROCESSED` | `CONFLICT` | Bertentangan dengan status final; tidak dibalik; satu `recon_items` (`source = 'WEBHOOK'`, `STATUS_MISMATCH`) dibuat di tx yang sama |
| `IGNORED` | `DUPLICATE` / `STALE` | Duplikat logis atau event lama yang datang belakangan (no-op) |
| `DEAD` | NULL | Gagal 8× atau amount/currency tidak cocok; alert + replay manual |

**Constraint**
- `pk_webhook_inbox PRIMARY KEY (id)`
- **`uq_webhook_inbox_event UNIQUE (provider, provider_event_id)`** (LI-09)
- `ck_webhook_inbox_status CHECK (status IN ('RECEIVED','PROCESSED','IGNORED','DEAD'))`
- `ck_webhook_inbox_outcome CHECK ((status = 'PROCESSED' AND outcome IN ('APPLIED','CONFLICT')) OR (status = 'IGNORED' AND outcome IN ('DUPLICATE','STALE')) OR (status IN ('RECEIVED','DEAD') AND outcome IS NULL))`
- `ck_webhook_inbox_processed_at CHECK ((status IN ('PROCESSED','IGNORED')) = (processed_at IS NOT NULL))`

**Index:** `ix_webhook_inbox_claimable (next_attempt_at) WHERE status = 'RECEIVED'`: claim worker. `ix_webhook_inbox_resource (provider, resource_ref, received_at)`: investigasi ops ("semua event untuk charge X").

**Mutabilitas terbatas:** inbox bersifat *insert + update kolom pemrosesan*.
- **Grant:** `SELECT, INSERT, UPDATE (status, outcome, attempts, next_attempt_at, locked_by, locked_until, last_error, processed_at), DELETE`. Kolom bukti (`payload`, `raw_body`, `provider_event_id`, `signature_ts`) tidak bisa diubah karena tidak ada grant UPDATE atasnya.
- **Trigger** `trg_webhook_inbox_guard BEFORE UPDATE OR DELETE`: menolak UPDATE pada baris yang sudah `PROCESSED`/`IGNORED` (kecuali oleh replay ops yang mengembalikan `DEAD → RECEIVED`), dan menolak DELETE kecuali `status <> 'RECEIVED' AND received_at < now() - interval '90 days'` (retensi).

---

## 13. `webhook_deliveries` & `webhook_delivery_attempts`

**`webhook_deliveries`** (status bisnis per event per merchant, D7)

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `merchant_id` | `uuid` | | | Tujuan |
| `event_id` | `uuid` | | | **Stabil antar retry**; merchant memakainya untuk dedupe |
| `event_type` | `text` | | | `payment.succeeded`, `payment.refunded`, `payment.expired` |
| `payload` | `jsonb` | | | Body yang ditandatangani |
| `status` | `text` | | `'PENDING'` | `PENDING` / `DELIVERED` / `DEAD` |
| `attempts` | `int` | | `0` | |
| `last_status_code` | `int` | ✓ | | |
| `last_error` | `text` | ✓ | | |
| `last_attempt_at` / `delivered_at` | `timestamptz` | ✓ | | |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | |

Constraint: `pk_webhook_deliveries`; FK `merchant_id → merchants`; `uq_webhook_deliveries_event UNIQUE (merchant_id, event_id)`; `ck_webhook_deliveries_status CHECK (status IN ('PENDING','DELIVERED','DEAD'))`; `ck_webhook_deliveries_delivered CHECK ((status = 'DELIVERED') = (delivered_at IS NOT NULL))`.
Index: `ix_webhook_deliveries_merchant_created (merchant_id, created_at)` (ops/merchant melihat riwayat); `ix_webhook_deliveries_dead (created_at) WHERE status = 'DEAD'` (antrean replay).
Ditulis **di tx bisnis yang sama** dengan job `webhook.deliver` (payload: `delivery_id`). Replay manual = status `DEAD → PENDING` + job baru dengan `event_id` yang sama.
Grant: `SELECT, INSERT, UPDATE`.

**`webhook_delivery_attempts`** (riwayat per percobaan, LF-15 AC3)

| Kolom | Tipe | N | Fungsi |
|---|---|---|---|
| `id` | `uuid` | | PK |
| `delivery_id` | `uuid` | | FK → `webhook_deliveries` |
| `attempt_no` | `int` | | 1, 2, … |
| `status_code` | `int` | ✓ | NULL jika timeout/koneksi gagal |
| `duration_ms` | `int` | | |
| `error` | `text` | ✓ | |
| `created_at` | `timestamptz` | | |

Constraint: `uq_webhook_delivery_attempts_no UNIQUE (delivery_id, attempt_no)`. Index: tercakup oleh unique tersebut. Append-only; grant `SELECT, INSERT`.

---

## 14. `recon_runs`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `provider` | `text` | | | |
| `business_date` | `date` | | | Tanggal settlement (zona waktu Asia/Jakarta) |
| `file_hash` | `bytea` | | | SHA-256 file settlement |
| `source` | `text` | | | `PULL` (job) / `UPLOAD` (ops) |
| `status` | `text` | | `'RUNNING'` | `RUNNING` / `COMPLETED` / `FAILED` |
| `matched_count`, `missing_internal_count`, `missing_external_count`, `amount_mismatch_count`, `status_mismatch_count` | `int` | | `0` | Ringkasan |
| `error` | `text` | ✓ | | Jika `FAILED` |
| `started_at` / `finished_at` | `timestamptz` | | / ✓ | |
| `created_by` | `uuid` | ✓ | | User OPS untuk `UPLOAD` |

Constraint: `pk_recon_runs`; **`uq_recon_runs_file UNIQUE (provider, business_date, file_hash)`** (LI-12: file yang sama tidak diproses dua kali; insert konflik → kembalikan run yang ada); `ck_recon_runs_status CHECK (status IN ('RUNNING','COMPLETED','FAILED'))`; `ck_recon_runs_source CHECK (source IN ('PULL','UPLOAD'))`; `ck_recon_runs_finished CHECK ((status = 'RUNNING') = (finished_at IS NULL))`.
Index: `ix_recon_runs_date (provider, business_date)` (run terbaru per tanggal).
Grant: `SELECT, INSERT, UPDATE`.

---

## 15. `recon_items`

| Kolom | Tipe | N | Default | Fungsi |
|---|---|---|---|---|
| `id` | `uuid` | | | PK |
| `recon_run_id` | `uuid` | ✓ | | NULL jika sumbernya konflik webhook |
| `source` | `text` | | | `SETTLEMENT` / `WEBHOOK` |
| `inbox_event_id` | `uuid` | ✓ | | Event pemicu (untuk `WEBHOOK`) |
| `object_type` | `text` | | | `topup` / `payout` |
| `internal_id` | `uuid` | ✓ | | ID top-up/payout; NULL untuk `MISSING_INTERNAL` |
| `external_ref` | `text` | ✓ | | `charge_id` / payout key dari provider; NULL untuk `MISSING_EXTERNAL` |
| `category` | `text` | | | Lihat CHECK |
| `internal_amount` / `external_amount` | `bigint` | ✓ | | |
| `internal_status` / `external_status` | `text` | ✓ | | |
| `currency` | `char(3)` | | | |
| `resolution` | `text` | ✓ | | `OPEN` / `RESOLVED`; NULL untuk `MATCHED` |
| `resolution_action` | `text` | ✓ | | Contoh: `APPLIED_PAYOUT_SETTLE`, `REPLAYED_INBOX`, `NO_ACTION_TIMING` |
| `resolution_note` | `text` | ✓ | | Alasan ops |
| `resolved_by` / `resolved_at` | `uuid` / `timestamptz` | ✓ | | |
| `settlement_journal_id` | `uuid` | ✓ | | Journal `PROVIDER_SETTLEMENT` (item settlement top-up `MATCHED`). Journal di-dedupe per top-up (`source = ('provider_settlement', topup_id)`, Step 12 R1-5); item dari recon ulang merujuk journal yang sudah ada |
| `created_at` / `updated_at` | `timestamptz` | | `now()` | |

**Constraint**
- `pk_recon_items PRIMARY KEY (id)`
- FK `recon_run_id → recon_runs`, `inbox_event_id → webhook_inbox`, `settlement_journal_id → journals`; index `ix_recon_items_settlement_journal (settlement_journal_id)` (tidak unik: recon ulang bisa merujuk journal yang sama, Step 12 R1-5)
- `ck_recon_items_source CHECK ((source = 'SETTLEMENT' AND recon_run_id IS NOT NULL) OR (source = 'WEBHOOK' AND recon_run_id IS NULL AND inbox_event_id IS NOT NULL))`
- `ck_recon_items_category CHECK (category IN ('MATCHED','MISSING_INTERNAL','MISSING_EXTERNAL','AMOUNT_MISMATCH','STATUS_MISMATCH'))`
- `ck_recon_items_object_type CHECK (object_type IN ('topup','payout'))`
- `ck_recon_items_refs CHECK ((category = 'MISSING_INTERNAL' AND internal_id IS NULL AND external_ref IS NOT NULL) OR (category = 'MISSING_EXTERNAL' AND internal_id IS NOT NULL AND external_ref IS NULL) OR (category IN ('MATCHED','AMOUNT_MISMATCH','STATUS_MISMATCH') AND internal_id IS NOT NULL AND external_ref IS NOT NULL) OR (source = 'WEBHOOK'))`
- `ck_recon_items_resolution CHECK ((category = 'MATCHED') = (resolution IS NULL) AND (resolution IS NULL OR resolution IN ('OPEN','RESOLVED')))`
- `ck_recon_items_resolved CHECK (resolution IS DISTINCT FROM 'RESOLVED' OR (resolved_by IS NOT NULL AND resolved_at IS NOT NULL AND resolution_note IS NOT NULL))`
- `uq_recon_items_run_internal UNIQUE (recon_run_id, object_type, internal_id) WHERE internal_id IS NOT NULL`
- `uq_recon_items_run_external UNIQUE (recon_run_id, object_type, external_ref) WHERE external_ref IS NOT NULL`

Dua partial unique terakhir membuat batch insert item dalam satu run idempoten (`ON CONFLICT DO NOTHING` saat chunk recon diulang).

**Index:** `ix_recon_items_open (created_at) WHERE resolution = 'OPEN'` (antrean ops); `ix_recon_items_internal (object_type, internal_id)` (ops membuka top-up/payout dan melihat item recon terkait).
**Trigger:** `updated_at`; menolak perubahan `category`, `internal_*`, `external_*` setelah insert (hanya kolom resolusi yang boleh berubah).
**Grant:** `SELECT, INSERT, UPDATE (resolution, resolution_action, resolution_note, resolved_by, resolved_at, settlement_journal_id, updated_at)`.

---

## 16. Ringkasan grant `ledgerpay_app`

| Tabel | SELECT | INSERT | UPDATE | DELETE |
|---|:-:|:-:|:-:|:-:|
| `accounts` | ✓ | ✓ | `balance, status, updated_at` | — |
| `journals`, `journal_entries` | ✓ | ✓ | — | — |
| `transfers`, `refunds`, `webhook_delivery_attempts` | ✓ | ✓ | — | — |
| `topups`, `payment_intents`, `payouts`, `merchants`, `webhook_deliveries`, `recon_runs` | ✓ | ✓ | ✓ | — |
| `merchant_api_keys` | ✓ | ✓ | `last_used_at, revoked_at, label` | — |
| `webhook_inbox` | ✓ | ✓ | kolom pemrosesan | ✓ (dibatasi trigger retensi) |
| `recon_items` | ✓ | ✓ | kolom resolusi | — |

Tidak ada tabel bisnis yang memberi `DELETE`. Penghapusan hanya terjadi di tabel efemeral shared (`idempotency_keys`, `jobs`, `refresh_tokens`) dan `webhook_inbox` (retensi).
