-- name: CreateMerchant :exec
INSERT INTO merchants (id, name) VALUES (@id, @name);

-- name: GetMerchant :one
SELECT id, name, status FROM merchants WHERE id = @id;

-- name: ActiveKeySlots :many
SELECT slot FROM merchant_api_keys WHERE merchant_id = @merchant_id AND revoked_at IS NULL ORDER BY slot;

-- name: InsertAPIKey :one
INSERT INTO merchant_api_keys (id, merchant_id, mode, prefix, key_hash, slot, label, expires_at, created_by)
VALUES (@id, @merchant_id, @mode, @prefix, @key_hash, @slot, @label, @expires_at, @created_by)
RETURNING created_at;

-- name: GetActiveKeyByPrefix :one
-- Autentikasi: prefix -> baris key (unique index), lalu hash dibandingkan constant-time di aplikasi.
SELECT k.id, k.merchant_id, k.key_hash, k.mode, k.expires_at, k.last_used_at
FROM merchant_api_keys k
JOIN merchants m ON m.id = k.merchant_id
WHERE k.prefix = @prefix AND k.revoked_at IS NULL AND m.status = 'ACTIVE';

-- name: TouchKeyLastUsed :exec
-- Throttle 1x/menit: tanpa ini setiap request merchant menjadi satu UPDATE (write amplification).
UPDATE merchant_api_keys SET last_used_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND (last_used_at IS NULL OR last_used_at < sqlc.arg(now)::timestamptz - interval '1 minute');

-- name: RevokeKey :one
-- Idempoten secara alami: revoke yang sudah revoked mengembalikan revoked_at yang ada.
UPDATE merchant_api_keys SET revoked_at = coalesce(revoked_at, @now)
WHERE id = @id
RETURNING id, merchant_id, prefix, revoked_at;

-- name: InsertAudit :exec
-- audit_log adalah tabel shared (append-only); setiap modul menulis aksinya sendiri.
INSERT INTO audit_log (id, actor_type, actor_id, action, target_type, target_id, request_id, reason)
VALUES (@id, @actor_type, @actor_id, @action, @target_type, @target_id, @request_id, @reason);

-- name: SetMerchantWebhook :execrows
UPDATE merchants SET webhook_url = @webhook_url, webhook_secret_enc = @webhook_secret_enc, webhook_secret_rotated_at = now()
WHERE id = @id;

-- name: GetMerchantWebhook :one
SELECT webhook_url, webhook_secret_enc FROM merchants WHERE id = @id;
