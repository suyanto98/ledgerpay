-- Sprint 3: idempotency (ADR-L02 pola a) + transfer P2P (LF-04).
-- Sumber: docs/design/07-db-shared-tables.md §3.1, docs/design/09-db-schema.md §7.

-- +goose Up

-- ---------------------------------------------------------------- idempotency_keys
-- Ditulis di transaksi YANG SAMA dengan efek finansialnya: key ada ⇔ efek ada (atau penolakan
-- domain 422 yang disimpan). Request duplikat yang datang bersamaan menunggu di unique index.
CREATE TABLE idempotency_keys (
    id              uuid        NOT NULL,
    client_type     text        NOT NULL,
    client_id       uuid        NOT NULL,     -- users.id (JWT) atau merchants.id (API key, Sprint 5)
    endpoint        text        NOT NULL,     -- template rute, contoh 'POST /v1/transfers'
    key             text        NOT NULL,     -- nilai header Idempotency-Key
    request_hash    bytea       NOT NULL,     -- SHA-256(method, path, canonical JSON body)
    response_status smallint,                 -- NULL = belum selesai (hanya pola b)
    response_body   jsonb,
    resource_type   text,
    resource_id     uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_idempotency_keys PRIMARY KEY (id),
    CONSTRAINT uq_idempotency_keys_scope UNIQUE (client_type, client_id, endpoint, key),
    CONSTRAINT ck_idempotency_keys_key_len CHECK (length(key) BETWEEN 8 AND 255),
    CONSTRAINT ck_idempotency_keys_hash_len CHECK (octet_length(request_hash) = 32),
    CONSTRAINT ck_idempotency_keys_client_type CHECK (client_type IN ('USER', 'MERCHANT')),
    CONSTRAINT ck_idempotency_keys_response_pair CHECK ((response_status IS NULL) = (response_body IS NULL))
);
-- Job cleanup harian (Sprint 6) menghapus key kedaluwarsa per batch.
CREATE INDEX ix_idempotency_keys_expires_at ON idempotency_keys (expires_at);
CREATE TRIGGER trg_idempotency_keys_updated_at BEFORE UPDATE ON idempotency_keys
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------- transfers
-- Hanya transfer yang BERHASIL punya baris (status satu-satunya: COMPLETED). Penolakan 422
-- disimpan di idempotency_keys untuk replay.
CREATE TABLE transfers (
    id                  uuid        NOT NULL,     -- juga source_id journal TRANSFER
    sender_user_id      uuid        NOT NULL,
    receiver_user_id    uuid        NOT NULL,
    sender_account_id   uuid        NOT NULL,
    receiver_account_id uuid        NOT NULL,
    amount              bigint      NOT NULL,     -- minor unit (ADR-L04)
    currency            char(3)     NOT NULL,
    note                text,
    status              text        NOT NULL DEFAULT 'COMPLETED',
    journal_id          uuid        NOT NULL,
    idempotency_key_id  uuid,                     -- referensi longgar (D6): key dihapus setelah 7 hari
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_transfers PRIMARY KEY (id),
    CONSTRAINT fk_transfers_sender_user_id FOREIGN KEY (sender_user_id) REFERENCES users (id),
    CONSTRAINT fk_transfers_receiver_user_id FOREIGN KEY (receiver_user_id) REFERENCES users (id),
    CONSTRAINT fk_transfers_sender_account_id FOREIGN KEY (sender_account_id) REFERENCES accounts (id),
    CONSTRAINT fk_transfers_receiver_account_id FOREIGN KEY (receiver_account_id) REFERENCES accounts (id),
    CONSTRAINT fk_transfers_journal_id FOREIGN KEY (journal_id) REFERENCES journals (id),
    CONSTRAINT uq_transfers_journal_id UNIQUE (journal_id),
    CONSTRAINT ck_transfers_amount_positive CHECK (amount > 0),
    CONSTRAINT ck_transfers_not_self CHECK (sender_user_id <> receiver_user_id),
    CONSTRAINT ck_transfers_status CHECK (status = 'COMPLETED'),
    CONSTRAINT ck_transfers_note_length CHECK (note IS NULL OR char_length(note) <= 140)
);
-- Index FK (catalogue test C4). Index user juga melayani "daftar transfer saya" di masa depan.
CREATE INDEX ix_transfers_sender_user_id ON transfers (sender_user_id, created_at);
CREATE INDEX ix_transfers_receiver_user_id ON transfers (receiver_user_id, created_at);
CREATE INDEX ix_transfers_sender_account_id ON transfers (sender_account_id);
CREATE INDEX ix_transfers_receiver_account_id ON transfers (receiver_account_id);

CREATE TRIGGER trg_transfers_append_only BEFORE UPDATE OR DELETE ON transfers
  FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

-- ---------------------------------------------------------------- grants (least privilege)
-- idempotency_keys: response diisi sekali (UPDATE kolom tertentu), DELETE hanya untuk cleanup retensi.
GRANT SELECT, INSERT, DELETE ON idempotency_keys TO ledgerpay_app;
GRANT UPDATE (response_status, response_body, resource_type, resource_id, updated_at) ON idempotency_keys TO ledgerpay_app;
GRANT SELECT, INSERT ON transfers TO ledgerpay_app;   -- append-only

-- +goose Down
DROP TABLE transfers;
DROP TABLE idempotency_keys;
