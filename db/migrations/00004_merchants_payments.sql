-- Sprint 4: principal kedua (merchant + API key, LF-09) dan payment intent (LF-10).
-- Sumber: docs/design/09-db-schema.md §2, §3, §4 (merchant_id), §9.

-- +goose Up

-- ---------------------------------------------------------------- merchants
CREATE TABLE merchants (
    id                        uuid        NOT NULL,
    name                      text        NOT NULL,
    status                    text        NOT NULL DEFAULT 'ACTIVE',
    webhook_url               text,
    webhook_secret_enc        bytea,                 -- AES-GCM (Sprint 6, webhook keluar)
    webhook_secret_rotated_at timestamptz,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    version                   int         NOT NULL DEFAULT 1,
    CONSTRAINT pk_merchants PRIMARY KEY (id),
    CONSTRAINT ck_merchants_name_len CHECK (length(name) BETWEEN 1 AND 100),
    CONSTRAINT ck_merchants_status CHECK (status IN ('ACTIVE', 'INACTIVE')),
    CONSTRAINT ck_merchants_webhook_url CHECK (webhook_url IS NULL OR webhook_url ~ '^https?://'),
    CONSTRAINT ck_merchants_webhook_pair CHECK ((webhook_url IS NULL) = (webhook_secret_enc IS NULL))
);
CREATE TRIGGER trg_merchants_updated_at BEFORE UPDATE ON merchants
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------- merchant_api_keys
-- Secret berentropi tinggi → cukup SHA-256 (ADR-009). "Maksimal 2 key aktif" lewat SLOT:
-- partial unique (merchant_id, slot) untuk key aktif + slot ∈ {1,2} ⇒ paling banyak dua.
CREATE TABLE merchant_api_keys (
    id           uuid        NOT NULL,
    merchant_id  uuid        NOT NULL,
    mode         text        NOT NULL,
    prefix       text        NOT NULL,
    key_hash     bytea       NOT NULL,
    slot         smallint    NOT NULL,
    label        text,
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    created_by   uuid        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_merchant_api_keys PRIMARY KEY (id),
    CONSTRAINT fk_merchant_api_keys_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id),
    CONSTRAINT fk_merchant_api_keys_created_by FOREIGN KEY (created_by) REFERENCES users (id),
    CONSTRAINT uq_merchant_api_keys_prefix UNIQUE (prefix),
    CONSTRAINT uq_merchant_api_keys_key_hash UNIQUE (key_hash),
    CONSTRAINT ck_merchant_api_keys_mode CHECK (mode IN ('live', 'test')),
    CONSTRAINT ck_merchant_api_keys_prefix CHECK (prefix ~ '^[a-f0-9]{8}$'),
    CONSTRAINT ck_merchant_api_keys_hash_len CHECK (octet_length(key_hash) = 32),
    CONSTRAINT ck_merchant_api_keys_slot CHECK (slot IN (1, 2)),
    CONSTRAINT ck_merchant_api_keys_label_len CHECK (label IS NULL OR length(label) <= 100)
);
CREATE UNIQUE INDEX uq_merchant_api_keys_active_slot ON merchant_api_keys (merchant_id, slot) WHERE revoked_at IS NULL;
CREATE INDEX ix_merchant_api_keys_merchant_id ON merchant_api_keys (merchant_id);   -- FK (C4)
CREATE INDEX ix_merchant_api_keys_created_by ON merchant_api_keys (created_by);     -- FK (C4)

-- ---------------------------------------------------------------- accounts: wallet merchant (expand)
-- Migrasi expand: kolom nullable + constraint diperluas. Baris lama (wallet user, akun sistem)
-- tetap valid, jadi tidak ada backfill.
ALTER TABLE accounts ADD COLUMN merchant_id uuid;
ALTER TABLE accounts ADD CONSTRAINT fk_accounts_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id);
ALTER TABLE accounts DROP CONSTRAINT ck_accounts_owner;
ALTER TABLE accounts ADD CONSTRAINT ck_accounts_owner CHECK (
    (is_system AND user_id IS NULL AND merchant_id IS NULL AND system_code IS NOT NULL) OR
    (NOT is_system AND system_code IS NULL AND num_nonnulls(user_id, merchant_id) = 1));
CREATE UNIQUE INDEX uq_accounts_merchant_currency ON accounts (merchant_id, currency) WHERE merchant_id IS NOT NULL;
CREATE INDEX ix_accounts_merchant_id ON accounts (merchant_id);   -- FK (C4)

-- ---------------------------------------------------------------- payment_intents
CREATE TABLE payment_intents (
    id                  uuid        NOT NULL,
    merchant_id         uuid        NOT NULL,
    merchant_account_id uuid        NOT NULL,
    merchant_order_id   text        NOT NULL,
    amount              bigint      NOT NULL,
    currency            char(3)     NOT NULL,
    refunded_amount     bigint      NOT NULL DEFAULT 0,
    status              text        NOT NULL DEFAULT 'REQUIRES_CONFIRMATION',
    description         text,
    payer_user_id       uuid,
    payer_account_id    uuid,
    journal_id          uuid,
    expires_at          timestamptz NOT NULL,
    confirmed_at        timestamptz,
    cancelled_at        timestamptz,
    expired_at          timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_payment_intents PRIMARY KEY (id),
    CONSTRAINT fk_payment_intents_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id),
    CONSTRAINT fk_payment_intents_merchant_account_id FOREIGN KEY (merchant_account_id) REFERENCES accounts (id),
    CONSTRAINT fk_payment_intents_payer_user_id FOREIGN KEY (payer_user_id) REFERENCES users (id),
    CONSTRAINT fk_payment_intents_payer_account_id FOREIGN KEY (payer_account_id) REFERENCES accounts (id),
    CONSTRAINT fk_payment_intents_journal_id FOREIGN KEY (journal_id) REFERENCES journals (id),
    CONSTRAINT uq_payment_intents_journal_id UNIQUE (journal_id),
    CONSTRAINT ck_payment_intents_amount_positive CHECK (amount > 0),
    CONSTRAINT ck_payment_intents_order_format CHECK (merchant_order_id ~ '^[A-Za-z0-9_\-.:/]{1,64}$'),
    CONSTRAINT ck_payment_intents_description_len CHECK (description IS NULL OR char_length(description) <= 200),
    CONSTRAINT ck_payment_intents_refund_bound CHECK (refunded_amount >= 0 AND refunded_amount <= amount),   -- LI-08
    CONSTRAINT ck_payment_intents_status CHECK (status IN ('REQUIRES_CONFIRMATION', 'SUCCEEDED',
        'PARTIALLY_REFUNDED', 'REFUNDED', 'EXPIRED', 'CANCELLED')),
    CONSTRAINT ck_payment_intents_paid_consistency CHECK (
        (status IN ('SUCCEEDED', 'PARTIALLY_REFUNDED', 'REFUNDED')) = (journal_id IS NOT NULL AND payer_user_id IS NOT NULL)),
    CONSTRAINT ck_payment_intents_refund_status CHECK (
        (status = 'REFUNDED' AND refunded_amount = amount) OR
        (status = 'PARTIALLY_REFUNDED' AND refunded_amount > 0 AND refunded_amount < amount) OR
        (status NOT IN ('REFUNDED', 'PARTIALLY_REFUNDED') AND refunded_amount = 0))
);
-- LI-13: satu order hanya boleh punya satu intent aktif/terbayar. EXPIRED/CANCELLED boleh dibuat ulang.
CREATE UNIQUE INDEX uq_payment_intents_merchant_order ON payment_intents (merchant_id, merchant_order_id)
  WHERE status IN ('REQUIRES_CONFIRMATION', 'SUCCEEDED', 'PARTIALLY_REFUNDED', 'REFUNDED');
CREATE INDEX ix_payment_intents_merchant_created ON payment_intents (merchant_id, created_at, id);   -- list + FK
CREATE INDEX ix_payment_intents_expire_sweep ON payment_intents (expires_at) WHERE status = 'REQUIRES_CONFIRMATION';
CREATE INDEX ix_payment_intents_merchant_account_id ON payment_intents (merchant_account_id);   -- FK (C4)
CREATE INDEX ix_payment_intents_payer_user_id ON payment_intents (payer_user_id);               -- FK (C4)
CREATE INDEX ix_payment_intents_payer_account_id ON payment_intents (payer_account_id);         -- FK (C4)
CREATE TRIGGER trg_payment_intents_updated_at BEFORE UPDATE ON payment_intents
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Status terminal tidak boleh berubah lagi (jaring terakhir di bawah guard WHERE status = … aplikasi).
-- +goose StatementBegin
CREATE FUNCTION forbid_terminal_intent_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status IN ('EXPIRED', 'CANCELLED', 'REFUNDED') THEN
    RAISE EXCEPTION 'payment intent % is terminal (%)', OLD.id, OLD.status
      USING ERRCODE = '23514', CONSTRAINT = 'ck_payment_intents_terminal';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_payment_intents_terminal BEFORE UPDATE ON payment_intents
  FOR EACH ROW EXECUTE FUNCTION forbid_terminal_intent_change();

-- ---------------------------------------------------------------- grants (least privilege)
GRANT SELECT, INSERT, UPDATE ON merchants TO ledgerpay_app;
GRANT SELECT, INSERT ON merchant_api_keys TO ledgerpay_app;
GRANT UPDATE (last_used_at, revoked_at, label) ON merchant_api_keys TO ledgerpay_app;   -- hash/prefix/slot tidak bisa diubah
GRANT SELECT, INSERT, UPDATE ON payment_intents TO ledgerpay_app;

-- +goose Down
DROP TABLE payment_intents;
DROP FUNCTION forbid_terminal_intent_change();
DROP INDEX ix_accounts_merchant_id;
DROP INDEX uq_accounts_merchant_currency;
ALTER TABLE accounts DROP CONSTRAINT ck_accounts_owner;
ALTER TABLE accounts ADD CONSTRAINT ck_accounts_owner CHECK (
    (is_system AND user_id IS NULL AND system_code IS NOT NULL) OR
    (NOT is_system AND system_code IS NULL AND user_id IS NOT NULL));
ALTER TABLE accounts DROP CONSTRAINT fk_accounts_merchant_id;
ALTER TABLE accounts DROP COLUMN merchant_id;
DROP TABLE merchant_api_keys;
DROP TABLE merchants;
