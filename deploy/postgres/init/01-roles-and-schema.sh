#!/bin/sh
# Dijalankan SEKALI oleh image postgres saat volume data masih kosong.
# Membuat role dan schema; tabel dibuat oleh migrasi (dijalankan sebagai ledgerpay_owner).
# Versi Go dari skrip yang sama ada di internal/platform/db/dbtest (dipakai test).
set -eu

# Role backup hanya bisa login kalau password-nya dikonfigurasi (environment tanpa backup: NOLOGIN).
BACKUP_LOGIN="NOLOGIN"
[ -n "${LEDGERPAY_BACKUP_PASSWORD:-}" ] && BACKUP_LOGIN="LOGIN PASSWORD '${LEDGERPAY_BACKUP_PASSWORD}'"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
  CREATE ROLE ledgerpay_owner LOGIN PASSWORD '${LEDGERPAY_OWNER_PASSWORD}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
  CREATE ROLE ledgerpay_app   LOGIN PASSWORD '${LEDGERPAY_APP_PASSWORD}'   NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  -- Backup (Sprint 8): hanya BACA semua data (predefined role PG14+), tidak bisa menulis/DDL.
  -- Kalau kredensial backup bocor, penyerang bisa membaca tetapi tidak bisa mengubah uang.
  CREATE ROLE ledgerpay_backup ${BACKUP_LOGIN} NOSUPERUSER NOCREATEDB NOCREATEROLE;
  GRANT pg_read_all_data TO ledgerpay_backup;
  ALTER ROLE ledgerpay_owner SET search_path = ledgerpay;
  ALTER ROLE ledgerpay_app   SET search_path = ledgerpay;

  REVOKE CREATE ON SCHEMA public FROM PUBLIC;
  CREATE SCHEMA ledgerpay AUTHORIZATION ledgerpay_owner;
  GRANT USAGE ON SCHEMA ledgerpay TO ledgerpay_app;
EOSQL
