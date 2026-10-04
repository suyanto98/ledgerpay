-- Sprint 5: top-up lewat provider (LF-07, ADR-L02 pola b) + inbox webhook masuk (ADR-L08).
-- Sumber: docs/design/09-db-schema.md §8, §12.

-- +goose Up

-- ---------------------------------------------------------------- topups
-- recovery_point menyimpan progres LINTAS transaksi (tx1 → panggil provider → tx2) di objek bisnis
-- yang durable, bukan di idempotency key yang dihapus setelah 7 hari (ADR-L02 pola b).
CREATE TABLE topups (
    id             uuid        NOT NULL,          -- juga Idempotency-Key ke provider
    user_id        uuid        NOT NULL,
    account_id     uuid        NOT NULL,
    amount         bigint      NOT NULL,
    currency       char(3)     NOT NULL,
    status         text        NOT NULL DEFAULT 'PENDING',
    recovery_point text        NOT NULL DEFAULT 'CREATED',
    provider       text        NOT NULL DEFAULT 'mock',
    provider_ref   text,                          -- charge_id
    checkout_url   text,
    expires_at     timestamptz NOT NULL,
    succeeded_at   timestamptz,
    failed_at      timestamptz,
    expired_at     timestamptz,
    failure_reason text,
    journal_id     uuid,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_topups PRIMARY KEY (id),
    CONSTRAINT fk_topups_user_id FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_topups_account_id FOREIGN KEY (account_id) REFERENCES accounts (id),
    CONSTRAINT fk_topups_journal_id FOREIGN KEY (journal_id) REFERENCES journals (id),
    CONSTRAINT uq_topups_journal_id UNIQUE (journal_id),
    CONSTRAINT uq_topups_provider_ref UNIQUE (provider, provider_ref),
    CONSTRAINT ck_topups_amount_positive CHECK (amount > 0),
    CONSTRAINT ck_topups_status CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED', 'EXPIRED')),
    CONSTRAINT ck_topups_recovery_point CHECK (recovery_point IN ('CREATED', 'PROVIDER_CREATED')),
    CONSTRAINT ck_topups_provider_ref_presence CHECK ((recovery_point = 'PROVIDER_CREATED') = (provider_ref IS NOT NULL)),
    CONSTRAINT ck_topups_journal_presence CHECK ((status = 'SUCCEEDED') = (journal_id IS NOT NULL))
);
CREATE INDEX ix_topups_expire_sweep ON topups (expires_at) WHERE status = 'PENDING';
CREATE INDEX ix_topups_recover ON topups (created_at) WHERE recovery_point = 'CREATED';
CREATE INDEX ix_topups_user_created ON topups (user_id, created_at, id);       -- riwayat + FK (C4)
CREATE INDEX ix_topups_provider_created ON topups (provider, created_at);       -- recon (Sprint 9)
CREATE INDEX ix_topups_account_id ON topups (account_id);                       -- FK (C4)
CREATE TRIGGER trg_topups_updated_at BEFORE UPDATE ON topups
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- SUCCEEDED dan FAILED final. EXPIRED SENGAJA tidak final (status lunak, ADR-L08): webhook sukses
-- yang terlambat tetap dikreditkan, karena provider adalah sumber kebenaran apakah uang dibayar.
-- +goose StatementBegin
CREATE FUNCTION forbid_final_topup_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status IN ('SUCCEEDED', 'FAILED') AND NEW.status <> OLD.status THEN
    RAISE EXCEPTION 'topup % is final (%)', OLD.id, OLD.status
      USING ERRCODE = '23514', CONSTRAINT = 'ck_topups_final';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_topups_final BEFORE UPDATE ON topups
  FOR EACH ROW EXECUTE FUNCTION forbid_final_topup_change();

-- ---------------------------------------------------------------- webhook_inbox
-- Handler webhook hanya verifikasi + simpan + 200; efek bisnis dikerjakan worker (ADR-L08).
CREATE TABLE webhook_inbox (
    id                uuid        NOT NULL,
    provider          text        NOT NULL,
    provider_event_id text        NOT NULL,
    event_type        text        NOT NULL,
    resource_ref      text        NOT NULL,
    payload           jsonb       NOT NULL,
    raw_body          bytea       NOT NULL,     -- bukti persis yang ditandatangani provider
    signature_ts      timestamptz NOT NULL,
    status            text        NOT NULL DEFAULT 'RECEIVED',
    outcome           text,
    attempts          int         NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    locked_by         text,
    locked_until      timestamptz,
    last_error        text,
    received_at       timestamptz NOT NULL DEFAULT now(),
    processed_at      timestamptz,
    CONSTRAINT pk_webhook_inbox PRIMARY KEY (id),
    CONSTRAINT uq_webhook_inbox_event UNIQUE (provider, provider_event_id),   -- LI-09
    CONSTRAINT ck_webhook_inbox_status CHECK (status IN ('RECEIVED', 'PROCESSED', 'IGNORED', 'DEAD')),
    CONSTRAINT ck_webhook_inbox_outcome CHECK (
        (status = 'PROCESSED' AND outcome IN ('APPLIED', 'CONFLICT')) OR
        (status = 'IGNORED' AND outcome IN ('DUPLICATE', 'STALE')) OR
        (status IN ('RECEIVED', 'DEAD') AND outcome IS NULL)),
    CONSTRAINT ck_webhook_inbox_processed_at CHECK ((status IN ('PROCESSED', 'IGNORED')) = (processed_at IS NOT NULL)),
    CONSTRAINT ck_webhook_inbox_attempts CHECK (attempts >= 0)
);
CREATE INDEX ix_webhook_inbox_claimable ON webhook_inbox (next_attempt_at) WHERE status = 'RECEIVED';
CREATE INDEX ix_webhook_inbox_resource ON webhook_inbox (provider, resource_ref, received_at);

-- Baris yang sudah PROCESSED/IGNORED tidak boleh diubah lagi (kecuali DEAD → RECEIVED oleh replay ops).
-- +goose StatementBegin
CREATE FUNCTION guard_webhook_inbox() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.status = 'RECEIVED' OR OLD.received_at >= now() - interval '90 days' THEN
      RAISE EXCEPTION 'webhook_inbox % cannot be deleted (retention 90 days, not RECEIVED)', OLD.id
        USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN OLD;
  END IF;
  IF OLD.status IN ('PROCESSED', 'IGNORED') THEN
    RAISE EXCEPTION 'webhook_inbox % is already % and immutable', OLD.id, OLD.status
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_webhook_inbox_guard BEFORE UPDATE OR DELETE ON webhook_inbox
  FOR EACH ROW EXECUTE FUNCTION guard_webhook_inbox();

-- ---------------------------------------------------------------- grants (least privilege)
GRANT SELECT, INSERT, UPDATE ON topups TO ledgerpay_app;
GRANT SELECT, INSERT, DELETE ON webhook_inbox TO ledgerpay_app;
-- Kolom bukti (payload, raw_body, provider_event_id, signature_ts) tidak bisa diubah: tidak ada grant UPDATE.
GRANT UPDATE (status, outcome, attempts, next_attempt_at, locked_by, locked_until, last_error, processed_at)
  ON webhook_inbox TO ledgerpay_app;

-- +goose Down
DROP TABLE webhook_inbox;
DROP FUNCTION guard_webhook_inbox();
DROP TABLE topups;
DROP FUNCTION forbid_final_topup_change();
