-- Sprint 2: double-entry ledger (ADR-L01, ADR-L04, ADR-L07; docs/design/09-db-schema.md §4–6).
-- Penyimpangan dari desain (dicatat di backlog/learning):
--   * accounts.merchant_id + FK ke merchants ditambahkan di Sprint 4 (tabel merchants belum ada).
--   * journals.correlation_id bertipe text (bukan uuid): request ID dari client boleh berbentuk bebas.

-- +goose Up

-- ---------------------------------------------------------------- accounts
CREATE TABLE accounts (
    id          uuid        NOT NULL,
    user_id     uuid,
    system_code text,
    is_system   boolean     NOT NULL,
    type        text        NOT NULL,
    normal_side char(1)     NOT NULL,
    currency    char(3)     NOT NULL DEFAULT 'IDR',
    balance     bigint,                         -- minor unit; NULL untuk akun sistem (ADR-L07)
    status      text        NOT NULL DEFAULT 'ACTIVE',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_accounts PRIMARY KEY (id),
    CONSTRAINT uq_accounts_id_currency UNIQUE (id, currency),          -- target composite FK
    CONSTRAINT fk_accounts_user_id FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT ck_accounts_owner CHECK (
        (is_system AND user_id IS NULL AND system_code IS NOT NULL) OR
        (NOT is_system AND system_code IS NULL AND user_id IS NOT NULL)),
    CONSTRAINT ck_accounts_type CHECK (type IN ('ASSET', 'LIABILITY', 'REVENUE', 'SUSPENSE')),
    CONSTRAINT ck_accounts_normal_side CHECK (
        (type = 'ASSET' AND normal_side = 'D') OR
        (type IN ('LIABILITY', 'REVENUE', 'SUSPENSE') AND normal_side = 'C')),
    CONSTRAINT ck_accounts_wallet_is_liability CHECK (is_system OR type = 'LIABILITY'),
    CONSTRAINT ck_accounts_balance_presence CHECK (is_system = (balance IS NULL)),
    CONSTRAINT ck_accounts_balance_non_negative CHECK (is_system OR balance >= 0),   -- LI-02, jaring terakhir
    CONSTRAINT ck_accounts_currency CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_accounts_status CHECK (status IN ('ACTIVE', 'FROZEN'))
) WITH (fillfactor = 80);   -- ruang untuk HOT update saat balance berubah (tanpa index di balance)

CREATE UNIQUE INDEX uq_accounts_user_currency ON accounts (user_id, currency) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX uq_accounts_system_code_currency ON accounts (system_code, currency) WHERE system_code IS NOT NULL;
-- Index FK penuh untuk user_id (catalogue test C4: partial unique di atas tidak dihitung).
CREATE INDEX ix_accounts_user_id ON accounts (user_id);
CREATE TRIGGER trg_accounts_updated_at BEFORE UPDATE ON accounts
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------- journals (append-only)
CREATE TABLE journals (
    id                 uuid        NOT NULL,
    purpose            text        NOT NULL,
    source_type        text        NOT NULL,
    source_id          uuid        NOT NULL,     -- tanpa FK: objek sumber ada di modul lain
    currency           char(3)     NOT NULL,
    reversal_of        uuid,
    correlation_id     text        NOT NULL,     -- LI-14: telusuri ke request/job
    idempotency_key_id uuid,
    actor_type         text        NOT NULL,
    actor_id           uuid,
    description        text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_journals PRIMARY KEY (id),
    CONSTRAINT uq_journals_id_currency UNIQUE (id, currency),
    CONSTRAINT uq_journals_source UNIQUE (source_type, source_id, purpose),            -- LI-06
    CONSTRAINT fk_journals_reversal_of FOREIGN KEY (reversal_of) REFERENCES journals (id),
    CONSTRAINT uq_journals_reversal_of UNIQUE (reversal_of),                            -- LI-04
    CONSTRAINT ck_journals_purpose CHECK (purpose IN ('TOPUP_CREDIT', 'TRANSFER', 'PAYMENT', 'REFUND',
        'PAYOUT_HOLD', 'PAYOUT_SETTLE', 'PAYOUT_REVERSAL', 'PROVIDER_SETTLEMENT', 'ADJUSTMENT_MANUAL')),
    CONSTRAINT ck_journals_purpose_source CHECK (
        (purpose = 'TOPUP_CREDIT'        AND source_type = 'topup') OR
        (purpose = 'TRANSFER'            AND source_type = 'transfer') OR
        (purpose = 'PAYMENT'             AND source_type = 'payment_intent') OR
        (purpose = 'REFUND'              AND source_type = 'refund') OR
        (purpose IN ('PAYOUT_HOLD', 'PAYOUT_SETTLE', 'PAYOUT_REVERSAL') AND source_type = 'payout') OR
        (purpose = 'PROVIDER_SETTLEMENT' AND source_type = 'provider_settlement') OR
        (purpose = 'ADJUSTMENT_MANUAL'   AND source_type = 'ops_action')),
    CONSTRAINT ck_journals_reversal_purpose CHECK (reversal_of IS NULL OR purpose IN ('PAYOUT_REVERSAL', 'ADJUSTMENT_MANUAL')),
    CONSTRAINT ck_journals_actor CHECK (actor_type IN ('USER', 'MERCHANT', 'OPS', 'SYSTEM')
        AND ((actor_type = 'SYSTEM') = (actor_id IS NULL))),
    CONSTRAINT ck_journals_manual_reason CHECK (purpose <> 'ADJUSTMENT_MANUAL' OR (actor_type = 'OPS' AND description IS NOT NULL))
);

-- ---------------------------------------------------------------- journal_entries (append-only)
CREATE TABLE journal_entries (
    id         uuid        NOT NULL,
    journal_id uuid        NOT NULL,
    account_id uuid        NOT NULL,
    direction  char(1)     NOT NULL,
    amount     bigint      NOT NULL,
    currency   char(3)     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_journal_entries PRIMARY KEY (id),
    -- Composite FK: currency entry WAJIB sama dengan journal dan akunnya (mustahil secara deklaratif).
    CONSTRAINT fk_journal_entries_journal FOREIGN KEY (journal_id, currency) REFERENCES journals (id, currency),
    CONSTRAINT fk_journal_entries_account FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency),
    CONSTRAINT ck_journal_entries_direction CHECK (direction IN ('D', 'C')),
    CONSTRAINT ck_journal_entries_amount_positive CHECK (amount > 0)                  -- LI-01, LI-11
);
CREATE INDEX ix_journal_entries_journal_id ON journal_entries (journal_id, currency);
-- Riwayat wallet (cursor) + verifikasi saldo per akun (index-only scan).
CREATE INDEX ix_journal_entries_account_created ON journal_entries (account_id, currency, created_at, id) INCLUDE (direction, amount);

-- ---------------------------------------------------------------- keseimbangan (LI-01)
-- Deferred: dicek saat COMMIT karena entry di-INSERT satu per satu.
-- IF, bukan CASE: temuan Sprint 0 Spike 2a (CASE mengevaluasi NEW.journal_id di tabel journals).
-- +goose StatementBegin
CREATE FUNCTION check_journal_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  jid uuid; sum_d bigint; sum_c bigint; n int;
BEGIN
  IF TG_TABLE_NAME = 'journals' THEN
    jid := NEW.id;
  ELSE
    jid := NEW.journal_id;
  END IF;
  SELECT coalesce(sum(amount) FILTER (WHERE direction = 'D'), 0),
         coalesce(sum(amount) FILTER (WHERE direction = 'C'), 0),
         count(*)
    INTO sum_d, sum_c, n
    FROM journal_entries WHERE journal_id = jid;
  IF n < 2 OR sum_d <> sum_c THEN
    RAISE EXCEPTION 'journal % unbalanced: entries=%, debit=%, credit=%', jid, n, sum_d, sum_c
      USING ERRCODE = '23514', CONSTRAINT = 'ck_journals_balanced';
  END IF;
  RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER trg_journals_balanced
  AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION check_journal_balanced();
CREATE CONSTRAINT TRIGGER trg_journal_entries_balanced
  AFTER INSERT ON journal_entries DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION check_journal_balanced();

CREATE TRIGGER trg_journals_append_only BEFORE UPDATE OR DELETE ON journals
  FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();
CREATE TRIGGER trg_journal_entries_append_only BEFORE UPDATE OR DELETE ON journal_entries
  FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

-- ---------------------------------------------------------------- akun sistem (ADR-L07)
-- Tidak punya saldo materialized: saldonya diturunkan dari entries, jadi tidak di-lock per transaksi.
INSERT INTO accounts (id, system_code, is_system, type, normal_side, currency, balance) VALUES
    (gen_random_uuid(), 'PROVIDER_CLEARING', true, 'ASSET',    'D', 'IDR', NULL),
    (gen_random_uuid(), 'BANK_CLEARING',     true, 'ASSET',    'D', 'IDR', NULL),
    (gen_random_uuid(), 'PAYOUT_IN_TRANSIT', true, 'SUSPENSE', 'C', 'IDR', NULL),
    (gen_random_uuid(), 'FEE_REVENUE',       true, 'REVENUE',  'C', 'IDR', NULL);

-- ---------------------------------------------------------------- grants (least privilege)
GRANT SELECT, INSERT ON accounts TO ledgerpay_app;
GRANT UPDATE (balance, status, updated_at) ON accounts TO ledgerpay_app;  -- type/currency/owner tidak bisa diubah app
GRANT SELECT, INSERT ON journals TO ledgerpay_app;                         -- append-only
GRANT SELECT, INSERT ON journal_entries TO ledgerpay_app;                  -- append-only

-- +goose Down
DROP TABLE journal_entries;
DROP TABLE journals;
DROP TABLE accounts;
DROP FUNCTION check_journal_balanced();
