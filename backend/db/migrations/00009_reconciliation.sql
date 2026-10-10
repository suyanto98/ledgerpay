-- Sprint 9: rekonsiliasi harian (LF-17, Demo D). Sumber: docs/design/09-db-schema.md §14–15.
-- File settlement yang sama untuk tanggal yang sama tidak pernah diproses dua kali (LI-12).

-- +goose Up
CREATE TABLE recon_runs (
    id                     uuid        NOT NULL,
    provider               text        NOT NULL,
    business_date          date        NOT NULL,   -- Asia/Jakarta
    file_hash              bytea       NOT NULL,   -- SHA-256 isi file settlement
    source                 text        NOT NULL,
    status                 text        NOT NULL DEFAULT 'RUNNING',
    row_count              int         NOT NULL DEFAULT 0,
    matched_count          int         NOT NULL DEFAULT 0,
    missing_internal_count int         NOT NULL DEFAULT 0,
    missing_external_count int         NOT NULL DEFAULT 0,
    amount_mismatch_count  int         NOT NULL DEFAULT 0,
    status_mismatch_count  int         NOT NULL DEFAULT 0,
    error                  text,
    started_at             timestamptz NOT NULL DEFAULT now(),
    finished_at            timestamptz,
    created_by             uuid,
    CONSTRAINT pk_recon_runs PRIMARY KEY (id),
    CONSTRAINT fk_recon_runs_created_by FOREIGN KEY (created_by) REFERENCES users (id),
    CONSTRAINT uq_recon_runs_file UNIQUE (provider, business_date, file_hash),
    CONSTRAINT ck_recon_runs_hash_len CHECK (octet_length(file_hash) = 32),
    CONSTRAINT ck_recon_runs_status CHECK (status IN ('RUNNING', 'COMPLETED', 'FAILED')),
    CONSTRAINT ck_recon_runs_source CHECK (source IN ('PULL', 'UPLOAD')),
    CONSTRAINT ck_recon_runs_finished CHECK ((status = 'RUNNING') = (finished_at IS NULL)),
    CONSTRAINT ck_recon_runs_upload_actor CHECK (source <> 'UPLOAD' OR created_by IS NOT NULL)
);
CREATE INDEX ix_recon_runs_date ON recon_runs (provider, business_date);
CREATE INDEX ix_recon_runs_created_by ON recon_runs (created_by);   -- FK (C4)

CREATE TABLE recon_items (
    id                    uuid        NOT NULL,
    recon_run_id          uuid,                    -- NULL jika sumbernya konflik webhook
    source                text        NOT NULL,
    inbox_event_id        uuid,
    object_type           text        NOT NULL,
    internal_id           uuid,                    -- top-up / payout; NULL untuk MISSING_INTERNAL
    external_ref          text,                    -- reference di file; NULL untuk MISSING_EXTERNAL
    provider_ref          text,                    -- ID di sisi provider (ch_… / po_…)
    category              text        NOT NULL,
    internal_amount       bigint,
    external_amount       bigint,
    internal_status       text,
    external_status       text,
    currency              char(3)     NOT NULL,
    resolution            text,
    resolution_action     text,
    resolution_note       text,
    resolved_by           uuid,
    resolved_at           timestamptz,
    settlement_journal_id uuid,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_recon_items PRIMARY KEY (id),
    CONSTRAINT fk_recon_items_run FOREIGN KEY (recon_run_id) REFERENCES recon_runs (id),
    CONSTRAINT fk_recon_items_inbox_event FOREIGN KEY (inbox_event_id) REFERENCES webhook_inbox (id),
    CONSTRAINT fk_recon_items_settlement_journal FOREIGN KEY (settlement_journal_id) REFERENCES journals (id),
    CONSTRAINT fk_recon_items_resolved_by FOREIGN KEY (resolved_by) REFERENCES users (id),
    CONSTRAINT ck_recon_items_source CHECK (
        (source = 'SETTLEMENT' AND recon_run_id IS NOT NULL) OR
        (source = 'WEBHOOK' AND recon_run_id IS NULL AND inbox_event_id IS NOT NULL)),
    CONSTRAINT ck_recon_items_category CHECK (category IN ('MATCHED', 'MISSING_INTERNAL', 'MISSING_EXTERNAL', 'AMOUNT_MISMATCH', 'STATUS_MISMATCH')),
    CONSTRAINT ck_recon_items_object_type CHECK (object_type IN ('topup', 'payout')),
    CONSTRAINT ck_recon_items_refs CHECK (
        (category = 'MISSING_INTERNAL' AND internal_id IS NULL AND external_ref IS NOT NULL) OR
        (category = 'MISSING_EXTERNAL' AND internal_id IS NOT NULL AND external_ref IS NULL) OR
        (category IN ('MATCHED', 'AMOUNT_MISMATCH', 'STATUS_MISMATCH') AND internal_id IS NOT NULL AND external_ref IS NOT NULL) OR
        (source = 'WEBHOOK')),
    CONSTRAINT ck_recon_items_resolution CHECK (
        (category = 'MATCHED') = (resolution IS NULL) AND (resolution IS NULL OR resolution IN ('OPEN', 'RESOLVED'))),
    CONSTRAINT ck_recon_items_resolved CHECK (
        resolution IS DISTINCT FROM 'RESOLVED' OR (resolved_by IS NOT NULL AND resolved_at IS NOT NULL AND resolution_note IS NOT NULL))
);
-- Insert item dalam satu run idempoten (ON CONFLICT DO NOTHING saat run diulang).
CREATE UNIQUE INDEX uq_recon_items_run_internal ON recon_items (recon_run_id, object_type, internal_id) WHERE internal_id IS NOT NULL;
CREATE UNIQUE INDEX uq_recon_items_run_external ON recon_items (recon_run_id, object_type, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX ix_recon_items_run ON recon_items (recon_run_id);   -- FK (C4): unique parsial di atas tidak dihitung
CREATE INDEX ix_recon_items_open ON recon_items (created_at, id) WHERE resolution = 'OPEN';
CREATE INDEX ix_recon_items_internal ON recon_items (object_type, internal_id);
CREATE INDEX ix_recon_items_settlement_journal ON recon_items (settlement_journal_id);   -- FK (C4), tidak unik
CREATE INDEX ix_recon_items_inbox_event ON recon_items (inbox_event_id);                 -- FK (C4)
CREATE INDEX ix_recon_items_resolved_by ON recon_items (resolved_by);                    -- FK (C4)
CREATE TRIGGER trg_recon_items_updated_at BEFORE UPDATE ON recon_items
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Fakta hasil matching tidak boleh diubah setelah insert; hanya kolom resolusi (grant di bawah juga membatasi).
-- +goose StatementBegin
CREATE FUNCTION forbid_recon_fact_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF (NEW.category, NEW.object_type, NEW.internal_id, NEW.external_ref, NEW.internal_amount, NEW.external_amount,
      NEW.internal_status, NEW.external_status, NEW.recon_run_id)
     IS DISTINCT FROM
     (OLD.category, OLD.object_type, OLD.internal_id, OLD.external_ref, OLD.internal_amount, OLD.external_amount,
      OLD.internal_status, OLD.external_status, OLD.recon_run_id) THEN
    RAISE EXCEPTION 'recon item % facts are immutable', OLD.id USING ERRCODE = '23514', CONSTRAINT = 'ck_recon_items_immutable';
  END IF;
  IF OLD.resolution = 'RESOLVED' THEN
    RAISE EXCEPTION 'recon item % is already resolved', OLD.id USING ERRCODE = '23514', CONSTRAINT = 'ck_recon_items_resolved_final';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER trg_recon_items_immutable BEFORE UPDATE ON recon_items
  FOR EACH ROW EXECUTE FUNCTION forbid_recon_fact_change();

GRANT SELECT, INSERT, UPDATE ON recon_runs TO ledgerpay_app;
GRANT SELECT, INSERT ON recon_items TO ledgerpay_app;
GRANT UPDATE (resolution, resolution_action, resolution_note, resolved_by, resolved_at, settlement_journal_id, updated_at) ON recon_items TO ledgerpay_app;

-- +goose Down
DROP TABLE recon_items;
DROP FUNCTION forbid_recon_fact_change();
DROP TABLE recon_runs;
