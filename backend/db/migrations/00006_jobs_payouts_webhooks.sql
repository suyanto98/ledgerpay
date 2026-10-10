-- Sprint 6: job runner (ADR-006), payout dengan hasil tak diketahui (ADR-L06), webhook keluar (LF-15).
-- Sumber: docs/design/07-db-shared-tables.md §4, docs/design/09-db-schema.md §11, §13.

-- +goose Up

-- ---------------------------------------------------------------- jobs (outbox + work queue)
-- Outbox = INSERT ke jobs DI DALAM transaksi bisnis: "niat dikerjakan" ikut commit/rollback bersama
-- efek bisnisnya. Delivery at-least-once; setiap handler wajib idempoten.
CREATE TABLE jobs (
    id             uuid        NOT NULL,
    kind           text        NOT NULL,
    payload        jsonb       NOT NULL DEFAULT '{}',   -- hanya ID/parameter, bukan sumber kebenaran
    status         text        NOT NULL DEFAULT 'queued',
    attempts       int         NOT NULL DEFAULT 0,      -- naik saat klaim
    max_attempts   int         NOT NULL DEFAULT 10,
    run_at         timestamptz NOT NULL DEFAULT now(),
    locked_by      text,
    locked_until   timestamptz,
    last_error     text,
    dedupe_key     text,
    correlation_id text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz,
    CONSTRAINT pk_jobs PRIMARY KEY (id),
    CONSTRAINT ck_jobs_status CHECK (status IN ('queued', 'running', 'succeeded', 'dead')),
    CONSTRAINT ck_jobs_attempts CHECK (attempts >= 0 AND max_attempts > 0),
    CONSTRAINT ck_jobs_running_has_lease CHECK (status <> 'running' OR (locked_by IS NOT NULL AND locked_until IS NOT NULL)),
    CONSTRAINT ck_jobs_terminal_has_completed CHECK (status NOT IN ('succeeded', 'dead') OR completed_at IS NOT NULL)
);
-- Dedupe hanya selama job masih hidup: scheduler di banyak replika tidak bisa membuat job ganda per bucket.
CREATE UNIQUE INDEX uq_jobs_dedupe_key ON jobs (dedupe_key) WHERE dedupe_key IS NOT NULL AND status IN ('queued', 'running');
CREATE INDEX ix_jobs_claimable ON jobs (run_at) WHERE status = 'queued';
CREATE INDEX ix_jobs_lease_expired ON jobs (locked_until) WHERE status = 'running';
CREATE INDEX ix_jobs_dead ON jobs (kind, completed_at) WHERE status = 'dead';
CREATE INDEX ix_jobs_succeeded_cleanup ON jobs (completed_at) WHERE status = 'succeeded';
CREATE TRIGGER trg_jobs_updated_at BEFORE UPDATE ON jobs
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------- payouts
-- Tidak ada status "mungkin": SUBMITTED dicommit SEBELUM memanggil bank, sehingga crash setelahnya
-- terdeteksi (re-delivery melihat SUBMITTED → UNKNOWN → status query), bukan submit ulang buta.
CREATE TABLE payouts (
    id                    uuid        NOT NULL,          -- idempotency key ke bank
    user_id               uuid        NOT NULL,
    account_id            uuid        NOT NULL,
    amount                bigint      NOT NULL,
    currency              char(3)     NOT NULL,
    status                text        NOT NULL DEFAULT 'PENDING',
    bank_code             text        NOT NULL,
    account_number_enc    bytea       NOT NULL,          -- AES-GCM, AAD = payout id (PII)
    account_number_last4  char(4)     NOT NULL,
    holder_name           text        NOT NULL,
    hold_journal_id       uuid        NOT NULL,
    settle_journal_id     uuid,
    reversal_journal_id   uuid,
    submit_attempts       int         NOT NULL DEFAULT 0,
    status_check_attempts int         NOT NULL DEFAULT 0,
    needs_reconciliation  boolean     NOT NULL DEFAULT false,
    last_provider_status  text,
    last_error            text,
    submitted_at          timestamptz,
    resolved_at           timestamptz,
    resolved_by           uuid,
    resolution_source     text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_payouts PRIMARY KEY (id),
    CONSTRAINT fk_payouts_user_id FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_payouts_account_id FOREIGN KEY (account_id) REFERENCES accounts (id),
    CONSTRAINT fk_payouts_hold_journal_id FOREIGN KEY (hold_journal_id) REFERENCES journals (id),
    CONSTRAINT fk_payouts_settle_journal_id FOREIGN KEY (settle_journal_id) REFERENCES journals (id),
    CONSTRAINT fk_payouts_reversal_journal_id FOREIGN KEY (reversal_journal_id) REFERENCES journals (id),
    CONSTRAINT fk_payouts_resolved_by FOREIGN KEY (resolved_by) REFERENCES users (id),
    CONSTRAINT uq_payouts_hold_journal_id UNIQUE (hold_journal_id),
    CONSTRAINT uq_payouts_settle_journal_id UNIQUE (settle_journal_id),
    CONSTRAINT uq_payouts_reversal_journal_id UNIQUE (reversal_journal_id),
    CONSTRAINT ck_payouts_amount_positive CHECK (amount > 0),
    CONSTRAINT ck_payouts_status CHECK (status IN ('PENDING', 'SUBMITTED', 'SUCCEEDED', 'FAILED', 'UNKNOWN')),
    -- Settle dan reversal tidak mungkin keduanya terisi (LI-07, LI-10).
    CONSTRAINT ck_payouts_outcome_journal CHECK (
        (status = 'SUCCEEDED') = (settle_journal_id IS NOT NULL) AND (status = 'FAILED') = (reversal_journal_id IS NOT NULL)),
    CONSTRAINT ck_payouts_resolution_source CHECK (resolution_source IS NULL OR resolution_source IN ('BANK_RESPONSE', 'STATUS_QUERY', 'RECON', 'OPS')),
    CONSTRAINT ck_payouts_resolved CHECK ((status IN ('SUCCEEDED', 'FAILED')) = (resolved_at IS NOT NULL AND resolution_source IS NOT NULL)),
    CONSTRAINT ck_payouts_ops_resolution CHECK (resolution_source IS DISTINCT FROM 'OPS' OR resolved_by IS NOT NULL),
    CONSTRAINT ck_payouts_last4 CHECK (account_number_last4 ~ '^[0-9]{4}$'),
    CONSTRAINT ck_payouts_bank_code CHECK (bank_code ~ '^[A-Z0-9]{3,10}$'),
    CONSTRAINT ck_payouts_holder_len CHECK (char_length(holder_name) BETWEEN 2 AND 100)
);
CREATE INDEX ix_payouts_user_created ON payouts (user_id, created_at, id);          -- riwayat + FK (C4)
CREATE INDEX ix_payouts_account_id ON payouts (account_id);                         -- FK (C4)
CREATE INDEX ix_payouts_resolved_by ON payouts (resolved_by);                       -- FK (C4)
CREATE INDEX ix_payouts_unknown ON payouts (updated_at) WHERE status = 'UNKNOWN';
CREATE INDEX ix_payouts_needs_recon ON payouts (created_at) WHERE needs_reconciliation;
CREATE INDEX ix_payouts_stuck_submitted ON payouts (updated_at) WHERE status = 'SUBMITTED';
CREATE INDEX ix_payouts_created ON payouts (created_at);
CREATE TRIGGER trg_payouts_updated_at BEFORE UPDATE ON payouts
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose StatementBegin
CREATE FUNCTION forbid_final_payout_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status IN ('SUCCEEDED', 'FAILED') AND NEW.status <> OLD.status THEN
    RAISE EXCEPTION 'payout % is final (%)', OLD.id, OLD.status
      USING ERRCODE = '23514', CONSTRAINT = 'ck_payouts_final';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_payouts_final BEFORE UPDATE ON payouts
  FOR EACH ROW EXECUTE FUNCTION forbid_final_payout_change();

-- ---------------------------------------------------------------- webhook keluar (LF-15)
-- event_id dibuat SEKALI di tx bisnis dan tidak berubah antar retry/replay (merchant dedupe).
CREATE TABLE webhook_deliveries (
    id               uuid        NOT NULL,
    merchant_id      uuid        NOT NULL,
    event_id         uuid        NOT NULL,
    event_type       text        NOT NULL,
    payload          jsonb       NOT NULL,
    status           text        NOT NULL DEFAULT 'PENDING',
    attempts         int         NOT NULL DEFAULT 0,
    last_status_code int,
    last_error       text,
    last_attempt_at  timestamptz,
    delivered_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_webhook_deliveries PRIMARY KEY (id),
    CONSTRAINT fk_webhook_deliveries_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id),
    CONSTRAINT uq_webhook_deliveries_event UNIQUE (merchant_id, event_id),
    CONSTRAINT ck_webhook_deliveries_status CHECK (status IN ('PENDING', 'DELIVERED', 'DEAD')),
    CONSTRAINT ck_webhook_deliveries_delivered CHECK ((status = 'DELIVERED') = (delivered_at IS NOT NULL)),
    CONSTRAINT ck_webhook_deliveries_type CHECK (event_type IN ('payment.succeeded', 'payment.refunded', 'payment.expired'))
);
CREATE INDEX ix_webhook_deliveries_merchant_created ON webhook_deliveries (merchant_id, created_at);
CREATE INDEX ix_webhook_deliveries_dead ON webhook_deliveries (created_at) WHERE status = 'DEAD';
CREATE TRIGGER trg_webhook_deliveries_updated_at BEFORE UPDATE ON webhook_deliveries
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE webhook_delivery_attempts (
    id          uuid        NOT NULL,
    delivery_id uuid        NOT NULL,
    attempt_no  int         NOT NULL,
    status_code int,                     -- NULL: timeout / koneksi gagal / diblok egress guard
    duration_ms int         NOT NULL,
    error       text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_webhook_delivery_attempts PRIMARY KEY (id),
    CONSTRAINT fk_webhook_delivery_attempts_delivery_id FOREIGN KEY (delivery_id) REFERENCES webhook_deliveries (id),
    CONSTRAINT uq_webhook_delivery_attempts_no UNIQUE (delivery_id, attempt_no)
);
CREATE TRIGGER trg_webhook_delivery_attempts_append_only BEFORE UPDATE OR DELETE ON webhook_delivery_attempts
  FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

-- ---------------------------------------------------------------- grants (least privilege)
GRANT SELECT, INSERT, UPDATE, DELETE ON jobs TO ledgerpay_app;              -- DELETE: cleanup retensi
GRANT SELECT, INSERT, UPDATE ON payouts TO ledgerpay_app;
GRANT SELECT, INSERT, UPDATE ON webhook_deliveries TO ledgerpay_app;
GRANT SELECT, INSERT ON webhook_delivery_attempts TO ledgerpay_app;         -- append-only

-- +goose Down
DROP TABLE webhook_delivery_attempts;
DROP TABLE webhook_deliveries;
DROP TABLE payouts;
DROP FUNCTION forbid_final_payout_change();
DROP TABLE jobs;
