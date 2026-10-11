-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, display_name)
VALUES (@id, @email, @password_hash, @display_name)
RETURNING id, email, display_name, role, created_at;

-- name: GetUserByEmail :one
-- Pencarian case-insensitive memakai index uq_users_email_lower.
SELECT id, email, password_hash, display_name, role, status, failed_login_count, locked_until, created_at
FROM users
WHERE lower(email) = lower(@email);

-- name: GetUserByID :one
SELECT id, email, display_name, role, status, created_at
FROM users
WHERE id = @id;

-- name: RecordLoginSuccess :exec
UPDATE users
SET last_login_at = @now, failed_login_count = 0, locked_until = NULL
WHERE id = @id;

-- name: RecordLoginFailure :one
-- Lockout bertahap (C-03): increment atomik di SQL (dua login gagal paralel tidak saling menimpa).
-- (sqlc.arg dipakai karena @param::type berulang membuat parser sqlc salah menulis ulang query.)
-- Mulai gagal ke-threshold: kunci base_secs * 2^(n - threshold), maksimal max_secs.
UPDATE users
SET failed_login_count = failed_login_count + 1,
    locked_until = CASE
      WHEN failed_login_count + 1 >= sqlc.arg(threshold)::int
      THEN sqlc.arg(now)::timestamptz + make_interval(secs => least(sqlc.arg(base_secs)::float8 * power(2, failed_login_count + 1 - sqlc.arg(threshold)::int), sqlc.arg(max_secs)::float8))
      ELSE locked_until
    END
WHERE id = sqlc.arg(id)
RETURNING failed_login_count, locked_until;

-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, issued_at, expires_at, family_expires_at, user_agent, ip)
VALUES (@id, @user_id, @family_id, @token_hash, @issued_at, @expires_at, @family_expires_at, @user_agent, @ip);

-- name: GetRefreshTokenForUpdate :one
-- FOR UPDATE: dua refresh paralel dengan token yang sama diserialkan di baris ini, sehingga yang
-- kedua pasti melihat used_at milik yang pertama (grace window, bukan reuse palsu).
SELECT t.id, t.user_id, t.family_id, t.used_at, t.revoked_at, t.expires_at, t.family_expires_at,
       u.role, u.status AS user_status
FROM refresh_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = @token_hash
FOR UPDATE OF t;

-- name: MarkRefreshTokenUsed :exec
UPDATE refresh_tokens SET used_at = @now, replaced_by = @replaced_by WHERE id = @id;

-- name: CreateRotatedRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, parent_id, issued_at, expires_at, family_expires_at, user_agent, ip)
VALUES (@id, @user_id, @family_id, @token_hash, @parent_id, @issued_at, @expires_at, @family_expires_at, @user_agent, @ip);

-- name: RevokeFamily :execrows
UPDATE refresh_tokens SET revoked_at = @now, revoke_reason = @reason
WHERE family_id = @family_id AND revoked_at IS NULL;

-- name: GetFamilyByTokenHash :one
SELECT family_id, user_id FROM refresh_tokens WHERE token_hash = @token_hash;

-- name: GrantOpsRole :execrows
UPDATE users SET role = 'OPS' WHERE lower(email) = lower(@email);

-- name: InsertAudit :exec
INSERT INTO audit_log (id, actor_type, actor_id, action, target_type, target_id, request_id, ip, user_agent, reason)
VALUES (@id, @actor_type, @actor_id, @action, @target_type, @target_id, @request_id, @ip, @user_agent, @reason);

-- name: GetDisplayName :one
SELECT display_name FROM users WHERE id = @id;

-- name: ListDisplayNames :many
-- Nama pihak lawan untuk satu halaman riwayat (satu query, bukan satu per baris).
SELECT id, display_name FROM users WHERE id = ANY(@ids::uuid[]);

-- name: DisableUsers :many
UPDATE users SET status = 'DISABLED'
WHERE id = ANY(@ids::uuid[]) AND status <> 'DISABLED'
RETURNING id;

-- name: RevokeUsersTokens :execrows
UPDATE refresh_tokens SET revoked_at = @now, revoke_reason = 'ADMIN'
WHERE user_id = ANY(@ids::uuid[]) AND revoked_at IS NULL;
