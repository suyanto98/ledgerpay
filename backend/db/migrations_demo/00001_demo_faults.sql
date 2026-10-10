-- HANYA image demo (build tag `demo`): titik kegagalan sekali pakai untuk demo (O18).
-- Versi dicatat di tabel goose terpisah (goose_demo_version), jadi skema release tetap identik
-- dengan skema yang dites, dan database release tidak pernah memiliki tabel ini.

-- +goose Up
CREATE TABLE demo_faults (
    id         bigint      GENERATED ALWAYS AS IDENTITY,
    point      text        NOT NULL,
    remaining  int         NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    fired_at   timestamptz,
    CONSTRAINT pk_demo_faults PRIMARY KEY (id),
    CONSTRAINT ck_demo_faults_point CHECK (point IN ('payout.after_bank_call', 'inbox.before_apply')),
    CONSTRAINT ck_demo_faults_remaining CHECK (remaining BETWEEN 0 AND 10)
);
CREATE INDEX ix_demo_faults_point ON demo_faults (point) WHERE remaining > 0;
GRANT SELECT, INSERT, UPDATE, DELETE ON demo_faults TO ledgerpay_app;

-- +goose Down
DROP TABLE demo_faults;
