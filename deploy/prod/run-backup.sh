#!/usr/bin/env bash
# Backup harian, dijalankan cron host (/etc/cron.d/ledgerpay-backup, 02:00 UTC) sebagai user deploy.
# Memakai image backup dari rilis yang sedang aktif. Gagal → exit ≠ 0 + log; alert BackupStale
# berbunyi kalau tidak ada backup sukses > 26 jam (metrik didorong ke Pushgateway oleh backup.sh).
set -euo pipefail
ROOT=${LP_ROOT:-/srv/ledgerpay}
cd "$ROOT/current"
LP_VERSION=$(basename "$(readlink -f "$ROOT/current")")
export LP_ENV_DIR="$ROOT/env" LP_VERSION LP_BACKUP_HOST_DIR="$ROOT/backups"
exec docker compose -f deploy/prod/compose.prod.yml --profile backup run --rm backup
