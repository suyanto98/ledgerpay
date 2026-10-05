-- name: LockReconDate :exec
-- Dua run untuk tanggal yang sama (job + ops, atau file berbeda) diserialkan: journal
-- PROVIDER_SETTLEMENT tidak pernah dipost dua kali bersamaan.
SELECT pg_advisory_xact_lock(hashtextextended(@key::text, 0));

-- name: InsertRun :one
-- LI-12: file yang sama untuk tanggal yang sama → tidak ada baris baru (pemanggil membaca run yang ada).
INSERT INTO recon_runs (id, provider, business_date, file_hash, source, created_by)
VALUES (@id, @provider, @business_date, @file_hash, @source, @created_by)
ON CONFLICT (provider, business_date, file_hash) DO NOTHING
RETURNING id;

-- name: InsertFailedRun :one
INSERT INTO recon_runs (id, provider, business_date, file_hash, source, created_by, status, error, finished_at)
VALUES (@id, @provider, @business_date, @file_hash, @source, @created_by, 'FAILED', @error, now())
ON CONFLICT (provider, business_date, file_hash) DO NOTHING
RETURNING id;

-- name: GetRunByFile :one
SELECT id FROM recon_runs WHERE provider = @provider AND business_date = @business_date AND file_hash = @file_hash;

-- name: CompleteRun :exec
UPDATE recon_runs SET status = 'COMPLETED', finished_at = now(), row_count = @row_count,
       matched_count = @matched_count, missing_internal_count = @missing_internal_count,
       missing_external_count = @missing_external_count, amount_mismatch_count = @amount_mismatch_count,
       status_mismatch_count = @status_mismatch_count
WHERE id = @id AND status = 'RUNNING';

-- name: GetRun :one
SELECT id, provider, business_date, source, status, row_count, matched_count, missing_internal_count, missing_external_count,
       amount_mismatch_count, status_mismatch_count, error, started_at, finished_at
FROM recon_runs WHERE id = @id;

-- name: ListRuns :many
SELECT id, provider, business_date, source, status, row_count, matched_count, missing_internal_count, missing_external_count,
       amount_mismatch_count, status_mismatch_count, error, started_at, finished_at
FROM recon_runs
WHERE (sqlc.narg(from_date)::date IS NULL OR business_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR business_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (started_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg(lim);

-- name: HasCompletedRun :one
SELECT EXISTS (SELECT 1 FROM recon_runs WHERE provider = @provider AND business_date = @business_date AND status = 'COMPLETED');

-- name: TopupsByIDs :many
-- Sisi internal (read model): recon membaca top-up/payout, tidak pernah mengubahnya.
SELECT id, amount, status, currency FROM topups WHERE id = ANY(@ids::uuid[]);

-- name: PayoutsByIDs :many
SELECT id, amount, status, currency FROM payouts WHERE id = ANY(@ids::uuid[]);

-- name: ExpectedTopups :many
-- Top-up yang uangnya SUDAH masuk pada business_date → harus ada di file. created_at memakai indeks
-- (ix_topups_provider_created); succeeded_at menyaring. cutoff = akhir hari − toleransi (lihat Classify).
SELECT id FROM topups
WHERE provider = @provider AND created_at >= @created_from::timestamptz AND created_at < @cutoff_ts::timestamptz
  AND status = 'SUCCEEDED' AND succeeded_at >= @from_ts::timestamptz AND succeeded_at < @cutoff_ts::timestamptz;

-- name: ExpectedPayouts :many
-- Payout yang DIKIRIM ke bank pada business_date (termasuk yang masih UNKNOWN: justru itu yang dicari).
SELECT id FROM payouts
WHERE created_at >= @created_from::timestamptz AND created_at < @cutoff_ts::timestamptz
  AND submitted_at >= @from_ts::timestamptz AND submitted_at < @cutoff_ts::timestamptz;

-- name: InsertItem :exec
INSERT INTO recon_items (id, recon_run_id, source, object_type, internal_id, external_ref, provider_ref, category,
                         internal_amount, external_amount, internal_status, external_status, currency, resolution, settlement_journal_id)
VALUES (@id, @recon_run_id, 'SETTLEMENT', @object_type, @internal_id, @external_ref, @provider_ref, @category,
        @internal_amount, @external_amount, @internal_status, @external_status, @currency, @resolution, @settlement_journal_id)
ON CONFLICT DO NOTHING;

-- name: ListItems :many
SELECT id, recon_run_id, source, object_type, internal_id, external_ref, provider_ref, category, internal_amount, external_amount,
       internal_status, external_status, currency, resolution, resolution_action, resolution_note, resolved_by, resolved_at,
       settlement_journal_id, created_at
FROM recon_items
WHERE (sqlc.narg(resolution)::text IS NULL OR resolution = sqlc.narg(resolution)::text)
  AND (cardinality(sqlc.arg(categories)::text[]) = 0 OR category = ANY(sqlc.arg(categories)::text[]))
  AND (sqlc.narg(run_id)::uuid IS NULL OR recon_run_id = sqlc.narg(run_id)::uuid)
  AND (sqlc.narg(object_type)::text IS NULL OR object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (created_at, id) > (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at, id
LIMIT sqlc.arg(lim);

-- name: LockItem :one
SELECT id, object_type, internal_id, category, external_status, resolution
FROM recon_items WHERE id = @id FOR UPDATE;

-- name: ResolveItem :execrows
UPDATE recon_items SET resolution = 'RESOLVED', resolution_action = @action, resolution_note = @note,
       resolved_by = @resolved_by, resolved_at = now()
WHERE id = @id AND resolution = 'OPEN';

-- name: GetItem :one
SELECT id, recon_run_id, source, object_type, internal_id, external_ref, provider_ref, category, internal_amount, external_amount,
       internal_status, external_status, currency, resolution, resolution_action, resolution_note, resolved_by, resolved_at,
       settlement_journal_id, created_at
FROM recon_items WHERE id = @id;

-- name: OpenItemsCount :one
SELECT count(*)::bigint FROM recon_items WHERE resolution = 'OPEN';
