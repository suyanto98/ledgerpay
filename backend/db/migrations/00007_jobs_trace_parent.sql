-- Sprint 7: menyambung trace request → job (docs/design/21-observability.md §5.3).
-- traceparent W3C disimpan di KOLOM (bukan payload): payload adalah kontrak domain berisi ID saja;
-- konteks tracing adalah metadata infrastruktur yang dibaca runner secara generik.

-- +goose Up
ALTER TABLE jobs ADD COLUMN trace_parent text;

-- +goose Down
ALTER TABLE jobs DROP COLUMN trace_parent;
