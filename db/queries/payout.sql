-- name: InsertPayout :one
INSERT INTO payouts (id, user_id, account_id, amount, currency, bank_code, account_number_enc,
                     account_number_last4, holder_name, hold_journal_id)
VALUES (@id, @user_id, @account_id, @amount, @currency, @bank_code, @account_number_enc,
        @account_number_last4, @holder_name, @hold_journal_id)
RETURNING created_at;

-- name: GetPayout :one
-- age_since_submit_secs memakai jam DB (submitted_at juga ditulis dengan now() DB): satu acuan waktu
-- untuk aturan safe window ADR-L06.
SELECT id, user_id, account_id, amount, currency, status, bank_code, account_number_enc, account_number_last4,
       holder_name, hold_journal_id, settle_journal_id, reversal_journal_id, submit_attempts, status_check_attempts,
       needs_reconciliation, last_provider_status, last_error, resolution_source, created_at,
       coalesce(extract(epoch FROM now() - submitted_at), 0)::float8 AS age_since_submit_secs
FROM payouts WHERE id = @id;

-- name: MarkSubmitted :execrows
-- Tx-a: commit SUBMITTED SEBELUM memanggil bank. Setelah ini, crash di mana pun terdeteksi
-- (re-delivery melihat SUBMITTED → UNKNOWN → status query), tidak pernah submit ulang buta.
UPDATE payouts SET status = 'SUBMITTED', submit_attempts = submit_attempts + 1, submitted_at = now()
WHERE id = @id AND status = 'PENDING';

-- name: LockPayoutInStatus :one
-- Guard + lock baris payout (posisi 2c) SEBELUM journal: pihak yang kalah balapan (worker vs ops,
-- race g) menunggu di sini lalu mendapat 0 baris, sehingga tidak pernah sempat memposting journal.
SELECT id FROM payouts WHERE id = @id AND status = ANY(@statuses::text[]) FOR UPDATE;

-- name: MarkPayoutSucceeded :execrows
UPDATE payouts SET status = 'SUCCEEDED', settle_journal_id = @settle_journal_id, resolved_at = now(),
       resolution_source = @resolution_source, last_provider_status = sqlc.narg(provider_status)
WHERE id = @id AND status IN ('SUBMITTED', 'UNKNOWN');

-- name: MarkPayoutFailed :execrows
UPDATE payouts SET status = 'FAILED', reversal_journal_id = @reversal_journal_id, resolved_at = now(),
       resolution_source = @resolution_source, last_provider_status = sqlc.narg(provider_status)
WHERE id = @id AND status IN ('SUBMITTED', 'UNKNOWN');

-- name: MarkPayoutUnknown :execrows
UPDATE payouts SET status = 'UNKNOWN', last_error = @last_error,
       needs_reconciliation = needs_reconciliation OR sqlc.arg(needs_reconciliation)::bool
WHERE id = @id AND status = 'SUBMITTED';

-- name: RecordStatusCheck :exec
UPDATE payouts SET status_check_attempts = status_check_attempts + 1, last_provider_status = @provider_status,
       needs_reconciliation = needs_reconciliation OR sqlc.arg(needs_reconciliation)::bool
WHERE id = @id;

-- name: RecordResubmit :exec
UPDATE payouts SET submit_attempts = submit_attempts + 1 WHERE id = @id;

-- name: FlagNeedsReconciliation :exec
UPDATE payouts SET needs_reconciliation = true, last_error = @last_error WHERE id = @id;

-- name: UnknownStats :one
-- Gauge periodik: payout UNKNOWN saat ini + umur yang tertua (alert > 1 jam).
SELECT count(*)::bigint AS n, coalesce(extract(epoch FROM now() - min(updated_at)), 0)::float8 AS oldest_secs
FROM payouts WHERE status = 'UNKNOWN';
