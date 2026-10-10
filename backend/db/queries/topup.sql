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

-- name: ListExpiredPending :many
-- Sweeper topup.expire_sweep: top-up PENDING yang lewat expires_at (batch).
SELECT id, provider_ref FROM topups
WHERE status = 'PENDING' AND expires_at <= now()
ORDER BY expires_at LIMIT @batch;

-- name: MarkTopupExpired :execrows
UPDATE topups SET status = 'EXPIRED', expired_at = now() WHERE id = @id AND status = 'PENDING';

-- name: ListStuckCreated :many
-- Job topup.recover: charge provider belum tercatat (crash/timeout di antara tx1 dan tx2) dan
-- client tidak pernah retry. Umur > 30 dtk supaya tidak balapan dengan request yang sedang berjalan.
SELECT id FROM topups
WHERE recovery_point = 'CREATED' AND status = 'PENDING' AND created_at < now() - interval '30 seconds'
ORDER BY created_at LIMIT @batch;
