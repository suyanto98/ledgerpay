-- name: InsertKey :one
-- ADR-L02 pola (a). Kalau key yang sama sedang ditulis transaksi lain yang belum selesai,
-- INSERT ini MENUNGGU di unique index sampai transaksi itu commit/rollback (dibatasi lock_timeout).
-- Konflik → DO NOTHING → 0 baris (pgx.ErrNoRows) → pemanggil membaca baris yang sudah ada.
INSERT INTO idempotency_keys (id, client_type, client_id, endpoint, key, request_hash, expires_at)
VALUES (@id, @client_type, @client_id, @endpoint, @key, @request_hash, @expires_at)
ON CONFLICT ON CONSTRAINT uq_idempotency_keys_scope DO NOTHING
RETURNING id;

-- name: GetKey :one
SELECT id, request_hash, response_status, response_body
FROM idempotency_keys
WHERE client_type = @client_type AND client_id = @client_id AND endpoint = @endpoint AND key = @key;

-- name: CompleteKey :exec
UPDATE idempotency_keys
SET response_status = @response_status, response_body = @response_body,
    resource_type = @resource_type, resource_id = @resource_id
WHERE id = @id;
