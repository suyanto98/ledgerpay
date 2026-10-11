-- name: InsertTransfer :one
INSERT INTO transfers (id, sender_user_id, receiver_user_id, sender_account_id, receiver_account_id,
                       amount, currency, note, journal_id, idempotency_key_id)
VALUES (@id, @sender_user_id, @receiver_user_id, @sender_account_id, @receiver_account_id,
        @amount, @currency, @note, @journal_id, @idempotency_key_id)
RETURNING created_at;

-- name: GetTransferForParty :one
-- BOLA: hanya pengirim atau penerima yang bisa membaca; selain itu "tidak ada" (404).
SELECT id, sender_user_id, receiver_user_id, amount, currency, note, status, journal_id, created_at
FROM transfers
WHERE id = @id AND (sender_user_id = @user_id OR receiver_user_id = @user_id);

-- name: ListTransfersForParty :many
-- Pihak lawan + catatan untuk satu halaman riwayat. Filter pihak yang sama dengan GetTransferForParty (BOLA).
SELECT id, sender_user_id, receiver_user_id, note
FROM transfers
WHERE id = ANY(@ids::uuid[]) AND (sender_user_id = @user_id OR receiver_user_id = @user_id);
