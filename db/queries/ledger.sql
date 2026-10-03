-- name: CreateUserWallet :one
INSERT INTO accounts (id, user_id, is_system, type, normal_side, currency, balance)
VALUES (@id, @user_id, false, 'LIABILITY', 'C', @currency, 0)
RETURNING id;

-- name: CreateMerchantWallet :one
INSERT INTO accounts (id, merchant_id, is_system, type, normal_side, currency, balance)
VALUES (@id, @merchant_id, false, 'LIABILITY', 'C', @currency, 0)
RETURNING id;

-- name: GetWalletByMerchant :one
SELECT id, currency, balance, status FROM accounts
WHERE merchant_id = @merchant_id AND currency = @currency;

-- name: GetWalletByUser :one
SELECT id, currency, balance, status FROM accounts
WHERE user_id = @user_id AND currency = @currency;

-- name: GetSystemAccountID :one
SELECT id FROM accounts WHERE system_code = @system_code AND currency = @currency;

-- name: LockWalletAccounts :many
-- ADR-L03: kunci akun WALLET yang terlibat dengan urutan deterministik (ORDER BY id) supaya
-- dua transaksi yang menyentuh akun yang sama tidak saling menunggu dalam siklus (deadlock).
-- FOR NO KEY UPDATE: menserialkan sesama penulis saldo, tetapi tidak memblok cek FK (KEY SHARE)
-- dari INSERT journal_entries/transfers (Spike 2b).
-- Akun sistem SENGAJA tidak ikut (ADR-L07): kalau ikut dikunci, semua top-up akan antre di
-- satu baris PROVIDER_CLEARING (hot row). Akun sistem dibaca tanpa lock lewat GetSystemAccounts.
SELECT id, user_id, system_code, is_system, type, currency, balance, status
FROM accounts
WHERE id = ANY(@ids::uuid[]) AND NOT is_system
ORDER BY id
FOR NO KEY UPDATE;

-- name: GetSystemAccounts :many
-- Tanpa lock: akun sistem tidak punya saldo materialized, dan type/currency/status-nya tidak
-- bisa diubah role aplikasi kecuali status (grant kolom), jadi tidak ada yang perlu diserialkan.
SELECT id, user_id, system_code, is_system, type, currency, balance, status
FROM accounts
WHERE id = ANY(@ids::uuid[]) AND is_system;

-- name: AddToBalance :exec
UPDATE accounts SET balance = balance + @delta::bigint WHERE id = @id;

-- name: InsertJournal :exec
INSERT INTO journals (id, purpose, source_type, source_id, currency, reversal_of, correlation_id, actor_type, actor_id, description)
VALUES (@id, @purpose, @source_type, @source_id, @currency, @reversal_of, @correlation_id, @actor_type, @actor_id, @description);

-- name: InsertEntry :exec
INSERT INTO journal_entries (id, journal_id, account_id, direction, amount, currency)
VALUES (@id, @journal_id, @account_id, @direction, @amount, @currency);

-- name: ListAccountEntries :many
-- Riwayat wallet, terbaru dulu, cursor (created_at, id). Filter opsional memakai NULL = "semua".
SELECT e.id AS entry_id, e.journal_id, e.direction, e.amount, e.currency, e.created_at, j.purpose
FROM journal_entries e
JOIN journals j ON j.id = e.journal_id
WHERE e.account_id = @account_id
  AND e.currency = @currency
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (e.created_at, e.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
  AND (sqlc.narg(direction)::text IS NULL OR e.direction = sqlc.narg(direction)::text)
  AND (cardinality(@purposes::text[]) = 0 OR j.purpose = ANY(@purposes::text[]))
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.created_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.created_at < sqlc.narg(to_at)::timestamptz)
ORDER BY e.created_at DESC, e.id DESC
LIMIT @lim;

-- name: GetJournal :one
SELECT id, purpose, currency, created_at FROM journals WHERE id = @id;

-- name: ListJournalEntriesWithAccounts :many
SELECT e.account_id, e.direction, e.amount, e.created_at, a.user_id, a.system_code
FROM journal_entries e
JOIN accounts a ON a.id = e.account_id
WHERE e.journal_id = @journal_id
ORDER BY e.direction DESC, e.id;

-- name: BalanceDrift :many
-- LI-03: saldo materialized harus sama dengan Σ kredit − Σ debit dari entries (wallet = liability).
SELECT a.id, a.balance,
       coalesce(sum(CASE e.direction WHEN 'C' THEN e.amount ELSE -e.amount END), 0)::bigint AS computed
FROM accounts a
LEFT JOIN journal_entries e ON e.account_id = a.id AND e.currency = a.currency
WHERE NOT a.is_system
GROUP BY a.id, a.balance
HAVING a.balance <> coalesce(sum(CASE e.direction WHEN 'C' THEN e.amount ELSE -e.amount END), 0);

-- name: UnbalancedJournals :many
-- LI-01 (cek global): seharusnya selalu kosong karena trigger; kalau tidak kosong = bug serius.
SELECT journal_id FROM journal_entries
GROUP BY journal_id
HAVING sum(CASE direction WHEN 'D' THEN amount ELSE 0 END) <> sum(CASE direction WHEN 'C' THEN amount ELSE 0 END);

-- name: JournalExists :one
SELECT EXISTS (SELECT 1 FROM journals WHERE id = @id);

-- name: ReversalExists :one
SELECT EXISTS (SELECT 1 FROM journals WHERE reversal_of = @reversal_of);
