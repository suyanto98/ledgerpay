-- Modul ops: READ MODEL lintas tabel untuk ops console (LF-18). Semua query di sini hanya SELECT,
-- kecuali InsertAudit. Aksi tulis (resolve payout, replay webhook, retry job) dijalankan oleh
-- modul PEMILIK tabelnya, di transaksi yang sama dengan audit (ADR-001).

-- name: SearchObjects :many
-- Pencarian presisi (bukan daftar): ID objek / ID journal, provider_ref, merchant_order_id, atau email.
-- Setiap cabang punya predikat sendiri pada kolom berindeks (PK, uq journal_id, indeks user).
WITH u AS (SELECT id FROM users WHERE lower(email) = lower(sqlc.narg(email)::text)),
objs AS (
  SELECT 'transfer'::text AS object_type, t.id, t.status, t.amount, t.currency, t.sender_user_id AS user_id,
         ARRAY[t.journal_id]::uuid[] AS journal_ids, t.created_at
  FROM transfers t
  WHERE t.id = sqlc.narg(id)::uuid OR t.journal_id = sqlc.narg(id)::uuid
     OR t.sender_user_id IN (SELECT id FROM u) OR t.receiver_user_id IN (SELECT id FROM u)
  UNION ALL
  SELECT 'topup', p.id, p.status, p.amount, p.currency, p.user_id, array_remove(ARRAY[p.journal_id], NULL)::uuid[], p.created_at
  FROM topups p
  WHERE p.id = sqlc.narg(id)::uuid OR p.journal_id = sqlc.narg(id)::uuid OR p.provider_ref = sqlc.narg(ref)::text
     OR p.user_id IN (SELECT id FROM u)
  UNION ALL
  SELECT 'payout', o.id, o.status, o.amount, o.currency, o.user_id,
         array_remove(ARRAY[o.hold_journal_id, o.settle_journal_id, o.reversal_journal_id], NULL)::uuid[], o.created_at
  FROM payouts o
  WHERE o.id = sqlc.narg(id)::uuid OR o.hold_journal_id = sqlc.narg(id)::uuid OR o.settle_journal_id = sqlc.narg(id)::uuid
     OR o.reversal_journal_id = sqlc.narg(id)::uuid OR o.user_id IN (SELECT id FROM u)
  UNION ALL
  SELECT 'payment_intent', i.id, i.status, i.amount, i.currency, i.payer_user_id, array_remove(ARRAY[i.journal_id], NULL)::uuid[], i.created_at
  FROM payment_intents i
  WHERE i.id = sqlc.narg(id)::uuid OR i.journal_id = sqlc.narg(id)::uuid OR i.merchant_order_id = sqlc.narg(ref)::text
     OR i.payer_user_id IN (SELECT id FROM u)
  UNION ALL
  SELECT 'refund', r.id, 'SUCCEEDED', r.amount, r.currency, i.payer_user_id, ARRAY[r.journal_id]::uuid[], r.created_at
  FROM refunds r JOIN payment_intents i ON i.id = r.payment_intent_id
  WHERE r.id = sqlc.narg(id)::uuid OR r.journal_id = sqlc.narg(id)::uuid OR r.payment_intent_id = sqlc.narg(id)::uuid
)
SELECT o.object_type, o.id, o.status, o.amount, o.currency, usr.id AS user_id, usr.email AS user_email, o.journal_ids,
       j.correlation_id, o.created_at
FROM objs o
LEFT JOIN users usr ON usr.id = o.user_id
LEFT JOIN journals j ON j.id = o.journal_ids[1]
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(lim);

-- name: GetJournal :one
SELECT j.id, j.purpose, j.source_type, j.source_id, j.reversal_of, rv.id AS reversed_by, j.actor_type, j.actor_id,
       j.correlation_id, j.description, j.currency, j.created_at
FROM journals j
LEFT JOIN journals rv ON rv.reversal_of = j.id
WHERE j.id = @id;

-- name: JournalEntries :many
-- Pemilik akun ditampilkan sebagai label (kode akun sistem / email user / nama merchant).
SELECT e.account_id, e.direction, e.amount, e.currency,
       coalesce(a.system_code, 'USER:' || u.email, 'MERCHANT:' || m.name, a.id::text)::text AS owner
FROM journal_entries e
JOIN accounts a ON a.id = e.account_id
LEFT JOIN users u ON u.id = a.user_id
LEFT JOIN merchants m ON m.id = a.merchant_id
WHERE e.journal_id = @journal_id
ORDER BY e.direction DESC, e.id;

-- name: ListPayouts :many
-- Antrean ops: paling lama menggantung di atas (created_at ASC). Rekening hanya 4 digit terakhir.
SELECT o.id, o.user_id, u.email AS user_email, o.amount, o.currency, o.status, o.bank_code, o.account_number_last4,
       o.holder_name, o.hold_journal_id, o.settle_journal_id, o.reversal_journal_id, o.needs_reconciliation,
       o.status_check_attempts, o.last_provider_status, o.last_error, o.resolution_source, o.resolved_by,
       o.submitted_at, o.resolved_at, o.created_at
FROM payouts o
JOIN users u ON u.id = o.user_id
WHERE (cardinality(sqlc.arg(statuses)::text[]) = 0 OR o.status = ANY(sqlc.arg(statuses)::text[]))
  AND (sqlc.narg(needs_reconciliation)::bool IS NULL OR o.needs_reconciliation = sqlc.narg(needs_reconciliation)::bool)
  AND o.created_at <= now() - make_interval(mins => sqlc.arg(older_than_minutes)::int)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (o.created_at, o.id) > (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY o.created_at, o.id
LIMIT sqlc.arg(lim);

-- name: GetPayout :one
SELECT o.id, o.user_id, u.email AS user_email, o.amount, o.currency, o.status, o.bank_code, o.account_number_last4,
       o.holder_name, o.hold_journal_id, o.settle_journal_id, o.reversal_journal_id, o.needs_reconciliation,
       o.status_check_attempts, o.last_provider_status, o.last_error, o.resolution_source, o.resolved_by,
       o.submitted_at, o.resolved_at, o.created_at
FROM payouts o
JOIN users u ON u.id = o.user_id
WHERE o.id = @id;

-- name: ListDeliveries :many
-- Delivery webhook keluar per status (contoh DEAD untuk replay). Payload tidak ditampilkan.
SELECT d.id, d.merchant_id, m.name AS merchant_name, d.event_id, d.event_type, d.status, d.attempts,
       d.last_status_code, d.last_error, d.last_attempt_at, d.created_at
FROM webhook_deliveries d
JOIN merchants m ON m.id = d.merchant_id
WHERE d.status = sqlc.arg(status)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (d.created_at, d.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg(lim);

-- name: InsertAudit :exec
-- Setiap aksi tulis ops: siapa, apa, target, alasan, dan bukti (diff), di tx yang sama dengan aksinya.
INSERT INTO audit_log (id, actor_type, actor_id, action, target_type, target_id, request_id, correlation_id, ip, user_agent, diff, reason)
VALUES (@id, 'OPS', @actor_id, @action, @target_type, @target_id, @request_id, @correlation_id, @ip, @user_agent, @diff, @reason);
