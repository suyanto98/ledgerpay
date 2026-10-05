-- name: InsertDelivery :exec
-- Ditulis DI tx bisnis (confirm, sweeper) bersama job webhook.deliver: event_id dibuat sekali di sini
-- dan tidak berubah antar retry/replay (merchant memakainya untuk dedupe).
INSERT INTO webhook_deliveries (id, merchant_id, event_id, event_type, payload)
VALUES (@id, @merchant_id, @event_id, @event_type, @payload);

-- name: GetDelivery :one
SELECT id, merchant_id, event_id, event_type, payload, status, attempts FROM webhook_deliveries WHERE id = @id;

-- name: InsertAttempt :exec
INSERT INTO webhook_delivery_attempts (id, delivery_id, attempt_no, status_code, duration_ms, error)
VALUES (@id, @delivery_id, @attempt_no, @status_code, @duration_ms, @error);

-- name: MarkDelivered :execrows
UPDATE webhook_deliveries SET status = 'DELIVERED', delivered_at = now(), attempts = attempts + 1,
       last_status_code = @status_code, last_attempt_at = now(), last_error = NULL
WHERE id = @id AND status = 'PENDING';

-- name: RecordFailedAttempt :exec
UPDATE webhook_deliveries SET attempts = attempts + 1, last_status_code = sqlc.narg(status_code),
       last_error = @last_error, last_attempt_at = now()
WHERE id = @id AND status = 'PENDING';

-- name: MarkDeliveryDead :exec
UPDATE webhook_deliveries SET status = 'DEAD' WHERE id = @id AND status = 'PENDING';

-- name: ReplayDelivery :one
-- DEAD → PENDING. event_id TIDAK berubah: merchant yang sudah menerima versi lama bisa dedupe.
UPDATE webhook_deliveries SET status = 'PENDING', last_error = NULL
WHERE id = @id AND status = 'DEAD'
RETURNING id, event_id, status;

-- name: DeliveryExists :one
SELECT EXISTS (SELECT 1 FROM webhook_deliveries WHERE id = @id);
