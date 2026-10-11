-- Sprint 1: fondasi identitas (docs/design/07-db-shared-tables.md §1, §2, §5, §6).
-- Prasyarat (dibuat skrip init, bukan migrasi): role ledgerpay_owner / ledgerpay_app,
-- schema ledgerpay milik ledgerpay_owner, search_path role = ledgerpay.
-- Migrasi dijalankan sebagai ledgerpay_owner. Aplikasi berjalan sebagai ledgerpay_app.

-- +goose Up

-- ---------------------------------------------------------------- trigger functions
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION forbid_update_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
    USING ERRCODE = 'insufficient_privilege', HINT = 'Create a correcting record instead';
END $$;
-- +goose StatementEnd

-- ---------------------------------------------------------------- users
CREATE TABLE users (
    id                 uuid        NOT NULL,
    email              text        NOT NULL,
    password_hash      text        NOT NULL,
    display_name       text        NOT NULL,
    role               text        NOT NULL DEFAULT 'USER',     -- varian LedgerPay (USER/OPS)
    status             text        NOT NULL DEFAULT 'ACTIVE',
    failed_login_count int         NOT NULL DEFAULT 0,
    locked_until       timestamptz,
    last_login_at      timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    version            int         NOT NULL DEFAULT 1,
    CONSTRAINT pk_users PRIMARY KEY (id),
    CONSTRAINT ck_users_role CHECK (role IN ('USER', 'OPS')),
    CONSTRAINT ck_users_status CHECK (status IN ('ACTIVE', 'LOCKED', 'DISABLED')),
    CONSTRAINT ck_users_email_format CHECK (position('@' in email) > 1 AND length(email) <= 254),
    CONSTRAINT ck_users_failed_login_non_negative CHECK (failed_login_count >= 0),
    CONSTRAINT ck_users_display_name_len CHECK (length(display_name) BETWEEN 1 AND 80)
);
-- Unik case-insensitive. Nama index = nama constraint yang dipetakan ke EMAIL_ALREADY_REGISTERED.
CREATE UNIQUE INDEX uq_users_email_lower ON users (lower(email));
CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------- refresh_tokens
CREATE TABLE refresh_tokens (
    id                uuid        NOT NULL,
    user_id           uuid        NOT NULL,
    family_id         uuid        NOT NULL,
    token_hash        bytea       NOT NULL,          -- SHA-256(token); token mentah tidak disimpan
    parent_id         uuid,
    replaced_by       uuid,
    issued_at         timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    family_expires_at timestamptz NOT NULL,
    used_at           timestamptz,
    revoked_at        timestamptz,
    revoke_reason     text,
    user_agent        text,
    ip                inet,
    CONSTRAINT pk_refresh_tokens PRIMARY KEY (id),
    CONSTRAINT fk_refresh_tokens_user_id FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_refresh_tokens_parent_id FOREIGN KEY (parent_id) REFERENCES refresh_tokens (id),
    CONSTRAINT uq_refresh_tokens_token_hash UNIQUE (token_hash),
    CONSTRAINT ck_refresh_tokens_hash_len CHECK (octet_length(token_hash) = 32),
    CONSTRAINT ck_refresh_tokens_expiry CHECK (expires_at > issued_at AND expires_at <= family_expires_at),
    CONSTRAINT ck_refresh_tokens_revoke_reason CHECK (revoke_reason IS NULL OR revoke_reason IN
        ('LOGOUT', 'REUSE_DETECTED', 'PASSWORD_CHANGED', 'ADMIN')),
    CONSTRAINT ck_refresh_tokens_revoked_has_reason CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);
-- Index FK penuh (bukan partial): cek FK saat DELETE/UPDATE parent butuh index atas SEMUA baris
-- (final review C4: setiap FK punya index yang diawali kolom FK-nya).
CREATE INDEX ix_refresh_tokens_user_id   ON refresh_tokens (user_id);
CREATE INDEX ix_refresh_tokens_parent_id ON refresh_tokens (parent_id);
CREATE INDEX ix_refresh_tokens_family_active ON refresh_tokens (family_id) WHERE revoked_at IS NULL;
CREATE INDEX ix_refresh_tokens_expires_at    ON refresh_tokens (expires_at);   -- cleanup

-- ---------------------------------------------------------------- audit_log (append-only)
CREATE TABLE audit_log (
    id             uuid        NOT NULL,
    actor_type     text        NOT NULL,
    actor_id       uuid,
    action         text        NOT NULL,
    target_type    text,
    target_id      uuid,
    request_id     text,
    correlation_id text,
    ip             inet,
    user_agent     text,
    diff           jsonb,
    reason         text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_audit_log PRIMARY KEY (id),
    CONSTRAINT ck_audit_log_actor_type CHECK (actor_type IN ('USER', 'MERCHANT', 'SYSTEM', 'OPS')),
    CONSTRAINT ck_audit_log_action_format CHECK (action ~ '^[a-z_]+\.[a-z_]+$'),
    CONSTRAINT ck_audit_log_actor CHECK (actor_type = 'SYSTEM' OR actor_id IS NOT NULL)
);
CREATE INDEX ix_audit_log_target  ON audit_log (target_type, target_id, created_at DESC);
CREATE INDEX ix_audit_log_actor   ON audit_log (actor_id, created_at DESC);
CREATE INDEX ix_audit_log_created ON audit_log (created_at DESC, id);
CREATE TRIGGER trg_audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

-- ---------------------------------------------------------------- grants (least privilege, C-15)
GRANT SELECT, INSERT, UPDATE ON users TO ledgerpay_app;
GRANT SELECT, INSERT, UPDATE ON refresh_tokens TO ledgerpay_app;
GRANT SELECT, INSERT ON audit_log TO ledgerpay_app;          -- append-only: tanpa UPDATE/DELETE
GRANT SELECT ON goose_db_version TO ledgerpay_app;           -- readiness: cek versi skema

-- +goose Down
DROP TABLE audit_log;
DROP TABLE refresh_tokens;
DROP TABLE users;
DROP FUNCTION forbid_update_delete();
DROP FUNCTION set_updated_at();
