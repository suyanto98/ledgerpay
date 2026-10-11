-- name: InsertIntent :one
-- expires_at memakai waktu DB (now() = awal transaksi), sama dengan acuan cek expiry saat confirm
-- dan sweeper, supaya batas waktunya satu jam yang sama (docs/design/19-tx-concurrency.md §3e).
INSERT INTO payment_intents (id, merchant_id, merchant_account_id, merchant_order_id, amount, currency, description, expires_at)
VALUES (@id, @merchant_id, @merchant_account_id, @merchant_order_id, @amount, @currency, @description,
        now() + make_interval(secs => @expires_in_seconds::int))
RETURNING expires_at, created_at;

-- name: FindLiveIntentByOrder :one
SELECT id FROM payment_intents
WHERE merchant_id = @merchant_id AND merchant_order_id = @merchant_order_id
  AND status IN ('REQUIRES_CONFIRMATION', 'SUCCEEDED', 'PARTIALLY_REFUNDED', 'REFUNDED');

-- name: GetIntent :one
SELECT i.id, i.merchant_id, m.name AS merchant_name, i.merchant_order_id, i.amount, i.currency, i.refunded_amount,
       i.status, i.description, i.payer_user_id, i.journal_id, i.expires_at, i.confirmed_at, i.created_at,
       (i.status = 'REQUIRES_CONFIRMATION' AND i.expires_at <= now()) AS overdue
FROM payment_intents i
JOIN merchants m ON m.id = i.merchant_id
WHERE i.id = @id;

-- name: LockIntent :one
-- Objek bisnis dikunci SEBELUM akun (urutan lock §0). expired dihitung dengan now() = awal tx.
SELECT id, merchant_id, merchant_account_id, amount, currency, status,
       (expires_at <= now()) AS expired
FROM payment_intents
WHERE id = @id
FOR UPDATE;

-- name: MarkIntentSucceeded :execrows
-- Guard eksplisit WHERE status (baris sudah terkunci, tapi guard membuat niatnya jelas dan aman).
UPDATE payment_intents
SET status = 'SUCCEEDED', payer_user_id = @payer_user_id, payer_account_id = @payer_account_id,
    journal_id = @journal_id, confirmed_at = now()
WHERE id = @id AND status = 'REQUIRES_CONFIRMATION';

-- name: ExpireDueIntents :many
-- Sweeper (dijadwalkan sebagai job di Sprint 6). SKIP LOCKED: intent yang sedang di-confirm
-- dilewati, bukan ditunggu; batch berikutnya tidak lagi cocok kalau confirm berhasil.
UPDATE payment_intents SET status = 'EXPIRED', expired_at = now()
WHERE id IN (
  SELECT id FROM payment_intents
  WHERE status = 'REQUIRES_CONFIRMATION' AND expires_at <= now()
  ORDER BY expires_at
  LIMIT @batch
  FOR UPDATE SKIP LOCKED)
RETURNING id;

-- name: LockPaidIntent :one
-- Refund: intent dikunci SEBELUM wallet (urutan lock §0). Refund paralel diserialkan di sini, dan
-- yang menunggu membaca refunded_amount terbaru setelah yang pertama commit (LI-08).
SELECT id, merchant_id, merchant_account_id, payer_account_id, amount, refunded_amount, currency, status
FROM payment_intents
WHERE id = @id
FOR UPDATE;

-- name: ApplyRefund :execrows
-- Guard di WHERE + CHECK constraint sebagai jaring terakhir (ck_payment_intents_refund_bound).
UPDATE payment_intents
SET refunded_amount = refunded_amount + @amount,
    status = CASE WHEN refunded_amount + @amount = amount THEN 'REFUNDED' ELSE 'PARTIALLY_REFUNDED' END
WHERE id = @id AND status IN ('SUCCEEDED', 'PARTIALLY_REFUNDED') AND refunded_amount + @amount <= amount;

-- name: InsertRefund :one
INSERT INTO refunds (id, payment_intent_id, merchant_id, amount, currency, reason, journal_id, idempotency_key_id)
VALUES (@id, @payment_intent_id, @merchant_id, @amount, @currency, @reason, @journal_id, @idempotency_key_id)
RETURNING created_at;

-- name: ListRefunds :many
SELECT id, payment_intent_id, amount, currency, reason, journal_id, created_at
FROM refunds
WHERE payment_intent_id = @payment_intent_id
ORDER BY created_at, id;

-- name: RefundDrift :many
-- LI-08 (deteksi): refunded_amount materialized harus sama dengan Σ refunds. Normalnya kosong.
SELECT i.id
FROM payment_intents i
LEFT JOIN (SELECT payment_intent_id, sum(amount) AS total FROM refunds GROUP BY payment_intent_id) r
       ON r.payment_intent_id = i.id
WHERE i.refunded_amount <> coalesce(r.total, 0);

-- name: ListPaidIntentLabels :many
-- Nama merchant + deskripsi untuk riwayat payer. Hanya intent yang dibayar user ini (BOLA).
SELECT i.id, m.name AS merchant_name, i.description
FROM payment_intents i
JOIN merchants m ON m.id = i.merchant_id
WHERE i.id = ANY(@ids::uuid[]) AND i.payer_user_id = @user_id;
