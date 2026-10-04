-- name: InsertTopup :one
-- expires_at memakai waktu DB (sama dengan acuan sweeper, Sprint 6).
INSERT INTO topups (id, user_id, account_id, amount, currency, expires_at)
VALUES (@id, @user_id, @account_id, @amount, @currency, now() + interval '24 hours')
RETURNING created_at;

-- name: GetTopup :one
SELECT id, user_id, account_id, amount, currency, status, recovery_point, provider_ref, checkout_url,
       expires_at, succeeded_at, failed_at, failure_reason, journal_id, created_at
FROM topups WHERE id = @id;

-- name: MarkProviderCreated :execrows
-- Guard WHERE recovery_point = 'CREATED': retry client dan job topup.recover yang balapan tidak
-- saling menimpa; yang kalah mendapat 0 baris dan cukup membaca ulang.
UPDATE topups SET recovery_point = 'PROVIDER_CREATED', provider_ref = @provider_ref, checkout_url = @checkout_url
WHERE id = @id AND recovery_point = 'CREATED';

-- name: LockTopupByProviderRef :one
-- Objek bisnis dikunci SETELAH baris inbox (fencing) dan SEBELUM wallet (urutan lock §0).
SELECT id, account_id, amount, currency, status, recovery_point, provider_ref
FROM topups WHERE provider = 'mock' AND provider_ref = @provider_ref
FOR UPDATE;

-- name: LockTopupByID :one
SELECT id, account_id, amount, currency, status, recovery_point, provider_ref
FROM topups WHERE id = @id
FOR UPDATE;

-- name: MarkTopupSucceeded :execrows
-- EXPIRED ikut diizinkan: status lunak (webhook sukses terlambat tetap dikreditkan, ADR-L08).
UPDATE topups SET status = 'SUCCEEDED', journal_id = @journal_id, succeeded_at = now()
WHERE id = @id AND status IN ('PENDING', 'EXPIRED');

-- name: MarkTopupFailed :execrows
UPDATE topups SET status = 'FAILED', failure_reason = @failure_reason, failed_at = now()
WHERE id = @id AND status IN ('PENDING', 'EXPIRED');
