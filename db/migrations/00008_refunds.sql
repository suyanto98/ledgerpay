-- Sprint 8: refund (LF-16, LI-08). Sumber: docs/design/09-db-schema.md §10.
-- Refund tidak punya state machine di V1: sinkron, baris ada = berhasil. Jaminan Σ refund ≤ amount
-- ada di payment_intents (lock FOR UPDATE + ck_payment_intents_refund_bound), bukan di tabel ini.

-- +goose Up
CREATE TABLE refunds (
    id                 uuid        NOT NULL,   -- source_id journal REFUND
    payment_intent_id  uuid        NOT NULL,
    merchant_id        uuid        NOT NULL,   -- denormalisasi untuk scoping BOLA
    amount             bigint      NOT NULL,
    currency           char(3)     NOT NULL,
    reason             text,
    journal_id         uuid        NOT NULL,
    idempotency_key_id uuid,                   -- referensi longgar (record idempotency dibersihkan setelah 7 hari)
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_refunds PRIMARY KEY (id),
    CONSTRAINT fk_refunds_payment_intent_id FOREIGN KEY (payment_intent_id) REFERENCES payment_intents (id),
    CONSTRAINT fk_refunds_merchant_id FOREIGN KEY (merchant_id) REFERENCES merchants (id),
    CONSTRAINT fk_refunds_journal_id FOREIGN KEY (journal_id) REFERENCES journals (id),
    CONSTRAINT uq_refunds_journal_id UNIQUE (journal_id),
    CONSTRAINT ck_refunds_amount_positive CHECK (amount > 0),
    CONSTRAINT ck_refunds_reason_len CHECK (reason IS NULL OR char_length(reason) <= 200)
);
CREATE INDEX ix_refunds_payment_intent ON refunds (payment_intent_id, created_at);
CREATE INDEX ix_refunds_merchant_id ON refunds (merchant_id);   -- FK (C4)
CREATE TRIGGER trg_refunds_append_only BEFORE UPDATE OR DELETE ON refunds
  FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

GRANT SELECT, INSERT ON refunds TO ledgerpay_app;   -- append-only

-- +goose Down
DROP TABLE refunds;
