-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, display_name)
VALUES (@id, @email, @password_hash, @display_name)
RETURNING id, email, display_name, role, created_at;

-- name: GetUserByEmail :one
-- Pencarian case-insensitive memakai index uq_users_email_lower.
SELECT id, email, password_hash, display_name, role, status, created_at
FROM users
WHERE lower(email) = lower(@email);

-- name: GetUserByID :one
SELECT id, email, display_name, role, status, created_at
FROM users
WHERE id = @id;

-- name: RecordLoginSuccess :exec
UPDATE users
SET last_login_at = @now, failed_login_count = 0
WHERE id = @id;

-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, issued_at, expires_at, family_expires_at, user_agent, ip)
VALUES (@id, @user_id, @family_id, @token_hash, @issued_at, @expires_at, @family_expires_at, @user_agent, @ip);

-- name: InsertAudit :exec
INSERT INTO audit_log (id, actor_type, actor_id, action, target_type, target_id, request_id, ip, user_agent)
VALUES (@id, @actor_type, @actor_id, @action, @target_type, @target_id, @request_id, @ip, @user_agent);
