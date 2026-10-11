-- HANYA image demo (build tag `demo`): sandbox per pengunjung demo publik ("Coba demo tanpa daftar").
-- Satu sandbox = satu pengunjung (Budi) + kontak (Siti, Andi, Rina) + satu merchant, semuanya dibuat lewat
-- service asli, sehingga ledger dan tabel bisnisnya sama sahnya dengan data user sungguhan.

-- +goose Up
CREATE TABLE demo_sandboxes (
    id               uuid        NOT NULL,
    user_id          uuid        NOT NULL,   -- pengunjung (yang login)
    member_user_ids  uuid[]      NOT NULL,   -- pengunjung + kontak: dinonaktifkan bersama saat kedaluwarsa
    contacts         jsonb       NOT NULL,   -- [{name, email}] untuk pilihan cepat di form transfer
    checkout_intent_id uuid,                 -- tagihan merchant yang menunggu dibayar pengunjung
    created_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL,
    expired_at       timestamptz,
    CONSTRAINT pk_demo_sandboxes PRIMARY KEY (id),
    CONSTRAINT uq_demo_sandboxes_user UNIQUE (user_id),
    CONSTRAINT fk_demo_sandboxes_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT ck_demo_sandboxes_expiry CHECK (expires_at > created_at)
);
CREATE INDEX ix_demo_sandboxes_created_at ON demo_sandboxes (created_at);
CREATE INDEX ix_demo_sandboxes_due ON demo_sandboxes (expires_at) WHERE expired_at IS NULL;
GRANT SELECT, INSERT, UPDATE ON demo_sandboxes TO ledgerpay_app;

-- Sumber saldo awal sandbox. BUKAN PROVIDER_CLEARING: top-up palsu dari akun itu akan muncul sebagai
-- selisih di rekonsiliasi provider. Saldo akun ini (debit) = total uang demo yang pernah dibagikan.
INSERT INTO accounts (id, system_code, is_system, type, normal_side, currency, balance)
VALUES (gen_random_uuid(), 'DEMO_FUNDING', true, 'ASSET', 'D', 'IDR', NULL);

-- +goose Down
DELETE FROM accounts WHERE system_code = 'DEMO_FUNDING'
  AND NOT EXISTS (SELECT 1 FROM journal_entries e WHERE e.account_id = accounts.id);
DROP TABLE demo_sandboxes;
