#!/bin/sh
# Dijalankan SEKALI oleh image postgres saat volume data masih kosong.
# Membuat role dan schema; tabel dibuat oleh migrasi (dijalankan sebagai ledgerpay_owner).
# Versi Go dari skrip yang sama ada di internal/platform/db/dbtest (dipakai test).
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
  CREATE ROLE ledgerpay_owner LOGIN PASSWORD '${LEDGERPAY_OWNER_PASSWORD}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
  CREATE ROLE ledgerpay_app   LOGIN PASSWORD '${LEDGERPAY_APP_PASSWORD}'   NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
  ALTER ROLE ledgerpay_owner SET search_path = ledgerpay;
  ALTER ROLE ledgerpay_app   SET search_path = ledgerpay;

  REVOKE CREATE ON SCHEMA public FROM PUBLIC;
  CREATE SCHEMA ledgerpay AUTHORIZATION ledgerpay_owner;
  GRANT USAGE ON SCHEMA ledgerpay TO ledgerpay_app;
EOSQL
