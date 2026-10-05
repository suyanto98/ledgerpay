#!/usr/bin/env bash
# Restore drill (docs/design/26-deploy-migrations-backup.md §5.5): backup yang belum pernah di-restore
# BELUM terbukti sebagai backup. Skrip ini memulihkan satu file backup ke Postgres SEKALI PAKAI, lalu:
#   1. restore (pg_restore, setelah sha256 diverifikasi)  2. migrate up dengan binary SEKARANG
#   3. ledger-verify (journal seimbang, saldo = Σ entries, refund drift)  4. laporkan durasi (indikator RTO)
#
#   scripts/restore-drill.sh <file.dump.age> <age-identity-file>
# Env: LP_APP_IMAGE (image ledgerpay; default build lokal), LP_BACKUP_IMAGE (default build lokal)
set -euo pipefail
cd "$(dirname "$0")/.."
FILE=$(realpath "${1:?usage: restore-drill.sh <file.dump.age> <identity>}")
IDENTITY=$(realpath "${2:?age identity file}")
ID="drill-$(date +%s)-$$"
APP_IMAGE=${LP_APP_IMAGE:-ledgerpay-drill-app:local}
BACKUP_IMAGE=${LP_BACKUP_IMAGE:-ledgerpay-backup:local}
log() { printf '[drill %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
cleanup() { docker rm -f "$ID-pg" >/dev/null 2>&1 || true; docker network rm "$ID" >/dev/null 2>&1 || true; }
trap cleanup EXIT

if [ -z "${LP_APP_IMAGE:-}" ]; then docker build -q -f deploy/Dockerfile --target ledgerpay -t "$APP_IMAGE" . >/dev/null; fi
if [ -z "${LP_BACKUP_IMAGE:-}" ]; then docker build -q -f deploy/backup/Dockerfile -t "$BACKUP_IMAGE" . >/dev/null; fi

pw() { head -c 24 /dev/urandom | base64 | tr -d '\n=+/'; }
SUPER=$(pw); OWNER=$(pw); APP=$(pw)
start=$(date +%s)
docker network create "$ID" >/dev/null
docker run -d --name "$ID-pg" --network "$ID" --network-alias postgres \
  -e POSTGRES_DB=ledgerpay -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD="$SUPER" \
  -e LEDGERPAY_OWNER_PASSWORD="$OWNER" -e LEDGERPAY_APP_PASSWORD="$APP" \
  -v "$PWD/deploy/postgres/init:/docker-entrypoint-initdb.d:ro" postgres:17-alpine >/dev/null
# pg_isready lolos sebentar saat init (server sementara); tunggu sampai server FINAL menerima koneksi.
for _ in $(seq 1 60); do
  docker exec "$ID-pg" psql -U postgres -d ledgerpay -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'ledgerpay_app'" 2>/dev/null | grep -q 1 \
    && docker logs "$ID-pg" 2>&1 | grep -q "PostgreSQL init process complete" && break
  sleep 1
done
log "empty postgres ready ($(( $(date +%s) - start ))s)"

docker run --rm --network "$ID" --entrypoint restore.sh \
  -e PGHOST=postgres -e PGUSER=postgres -e PGPASSWORD="$SUPER" -e PGDATABASE=ledgerpay \
  -v "$(dirname "$FILE"):/in:ro" -v "$IDENTITY:/run/identity:ro" \
  "$BACKUP_IMAGE" "/in/$(basename "$FILE")" /run/identity
log "restored ($(( $(date +%s) - start ))s)"

app() { docker run --rm --network "$ID" -e APP_ENV=test \
  -e MIGRATE_DATABASE_URL="postgres://ledgerpay_owner:$OWNER@postgres:5432/ledgerpay?sslmode=disable" \
  -e DATABASE_URL="postgres://ledgerpay_app:$APP@postgres:5432/ledgerpay?sslmode=disable" "$APP_IMAGE" "$@"; }
app migrate up
app ledger-verify   # exit ≠ 0 → drill GAGAL: backup ini tidak boleh dipercaya
counts=$(docker exec "$ID-pg" psql -U postgres -d ledgerpay -tAc \
  "SELECT format('%s journals, %s accounts, %s payouts', (SELECT count(*) FROM ledgerpay.journals), (SELECT count(*) FROM ledgerpay.accounts), (SELECT count(*) FROM ledgerpay.payouts))")
log "PASS: $(basename "$FILE") → $counts; restore + migrate + verify in $(( $(date +%s) - start ))s (RTO indicator)"
