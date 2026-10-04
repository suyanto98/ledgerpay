-- name: InsertEvent :one
-- Dedupe LI-09: event yang sama (provider_event_id) dikirim ulang provider → 0 baris → "duplicate".
INSERT INTO webhook_inbox (id, provider, provider_event_id, event_type, resource_ref, payload, raw_body, signature_ts)
VALUES (@id, @provider, @provider_event_id, @event_type, @resource_ref, @payload, @raw_body, @signature_ts)
ON CONFLICT ON CONSTRAINT uq_webhook_inbox_event DO NOTHING
RETURNING id;

-- name: ClaimEvents :many
-- Claim dengan lease (Step 12 C1): hormati lease yang masih berlaku; attempts hanya naik saat
-- MENGAMBIL ALIH lease yang kedaluwarsa (worker sebelumnya kemungkinan mati di tengah jalan).
UPDATE webhook_inbox
SET locked_by = sqlc.arg(worker), locked_until = now() + make_interval(secs => sqlc.arg(lease_secs)::float8),
    attempts = attempts + CASE WHEN locked_until IS NOT NULL THEN 1 ELSE 0 END
WHERE id IN (
  SELECT id FROM webhook_inbox
  WHERE status = 'RECEIVED' AND next_attempt_at <= now() AND (locked_until IS NULL OR locked_until < now())
  ORDER BY next_attempt_at
  LIMIT sqlc.arg(batch)
  FOR UPDATE SKIP LOCKED)
RETURNING id, event_type, resource_ref, payload, attempts;

-- name: FenceEvent :one
-- Fencing: hanya pemegang lease saat ini yang boleh menyelesaikan event. Worker yang lease-nya
-- sudah diambil alih mendapat 0 baris (pgx.ErrNoRows) → rollback tanpa efek (race f).
SELECT id FROM webhook_inbox
WHERE id = @id AND locked_by = @worker AND status = 'RECEIVED'
FOR UPDATE;

-- name: FinalizeEvent :exec
-- SATU update final di akhir tx, sehingga CHECK status × outcome tidak pernah melihat keadaan antara.
UPDATE webhook_inbox
SET status = @status, outcome = sqlc.narg(outcome), last_error = sqlc.narg(last_error),
    processed_at = CASE WHEN @status::text IN ('PROCESSED', 'IGNORED') THEN now() END,
    locked_by = NULL, locked_until = NULL
WHERE id = @id;

-- name: FailEvent :exec
-- Percobaan gagal (provider tidak bisa dihubungi, error tx): backoff eksponensial, lease dilepas.
-- Setelah max_attempts kegagalan nyata → DEAD (alert + replay manual ops).
UPDATE webhook_inbox
SET attempts = attempts + 1,
    next_attempt_at = now() + make_interval(secs => least(power(2, attempts + 1), 3600)::float8),
    last_error = sqlc.arg(last_error),
    status = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::int THEN 'DEAD' ELSE 'RECEIVED' END,
    locked_by = NULL, locked_until = NULL
WHERE id = sqlc.arg(id) AND locked_by = sqlc.arg(worker) AND status = 'RECEIVED';

-- name: PendingCount :one
SELECT count(*)::bigint FROM webhook_inbox WHERE status = 'RECEIVED';
