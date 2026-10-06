#!/usr/bin/env bash
# Membuat env file per service untuk satu environment, dengan secret ACAK yang dihitung (bukan
# diketik: dua kali kunci 31 byte di Sprint 1 dan 6 mengajarkan itu). Hasil: deploy/prod/env/ (gitignored).
#   ./init-env.sh <domain> <acme-email> [out-dir]
# Simpan salinan terenkripsi (sops/age, password manager) — file ini adalah satu-satunya sumber secret.
set -euo pipefail
DOMAIN=${1:?usage: init-env.sh <domain> <acme-email> [out-dir]}
EMAIL=${2:?acme email}
OUT=${3:-$(dirname "$0")/env}
[ -e "$OUT/api.env" ] && { echo "refuse: $OUT already has secrets (delete it explicitly to regenerate)"; exit 1; }
mkdir -p "$OUT" && chmod 700 "$OUT"

rand() { head -c "$1" /dev/urandom | base64 | tr -d '\n=+/' | head -c "$2"; }    # password URL-safe
key32() { local k; k=$(head -c 32 /dev/urandom | base64 | tr -d '\n'); [ "$(printf %s "$k" | base64 -d | wc -c)" = 32 ] || exit 1; printf %s "$k"; }

PG_SUPER=$(rand 48 40); PG_OWNER=$(rand 48 40); PG_APP=$(rand 48 40); PG_BACKUP=$(rand 48 40)
JWT=$(key32); PII=$(key32); DEMO_TOKEN=$(rand 48 40)
MOCK_API=$(rand 48 40); WHSEC=$(rand 48 40); GRAFANA=$(rand 48 32)
APP_DSN="postgres://ledgerpay_app:${PG_APP}@postgres:5432/ledgerpay?sslmode=disable"

w() { umask 077; cat > "$OUT/$1"; }
w postgres.env <<E
POSTGRES_DB=ledgerpay
POSTGRES_USER=postgres
POSTGRES_PASSWORD=${PG_SUPER}
LEDGERPAY_OWNER_PASSWORD=${PG_OWNER}
LEDGERPAY_APP_PASSWORD=${PG_APP}
LEDGERPAY_BACKUP_PASSWORD=${PG_BACKUP}
E
w migrate.env <<E
MIGRATE_DATABASE_URL=postgres://ledgerpay_owner:${PG_OWNER}@postgres:5432/ledgerpay?sslmode=disable
E
w api.env <<E
APP_ENV=demo
LOG_LEVEL=info
DATABASE_URL=${APP_DSN}
JWT_SIGNING_KEYS={"k1":"${JWT}"}
JWT_ACTIVE_KID=k1
COOKIE_SECURE=true
ALLOWED_ORIGINS=https://${DOMAIN}
PUBLIC_BASE_URL=https://${DOMAIN}
TRUSTED_PROXIES=172.16.0.0/12
PROVIDER_BASE_URL=http://mock-provider:9090
PROVIDER_API_SECRET=${MOCK_API}
PROVIDER_WEBHOOK_SECRETS=${WHSEC}
PROVIDER_CALLBACK_URL=http://api:8080/api/v1/webhooks/provider
PII_KEYRING={"v1":"${PII}"}
PII_ACTIVE_KEY_VERSION=v1
RATE_LIMIT_MONEY_WRITE_PER_MIN=600
# Toolbar demo (O15): presenter menempelkan token ini di tab; TIDAK PERNAH masuk bundle SPA.
DEMO_MODE=true
DEMO_TOKEN=${DEMO_TOKEN}
OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318/v1/traces
E
w worker.env <<E
APP_ENV=demo
LOG_LEVEL=info
DATABASE_URL=${APP_DSN}
PROVIDER_BASE_URL=http://mock-provider:9090
PROVIDER_API_SECRET=${MOCK_API}
PUBLIC_BASE_URL=https://${DOMAIN}
PII_KEYRING={"v1":"${PII}"}
PII_ACTIVE_KEY_VERSION=v1
OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318/v1/traces
E
w mock.env <<E
MOCK_API_SECRET=${MOCK_API}
MOCK_WEBHOOK_SECRET=${WHSEC}
MOCK_PUBLIC_URL=https://pay.${DOMAIN}
E
w grafana.env <<E
GF_SECURITY_ADMIN_PASSWORD=${GRAFANA}
GF_AUTH_ANONYMOUS_ENABLED=false
GF_USERS_ALLOW_SIGN_UP=false
GF_SERVER_ROOT_URL=https://obs.${DOMAIN}
E
w edge.env <<E
LP_DOMAIN=${DOMAIN}
LP_ACME_EMAIL=${EMAIL}
LP_CADDY_GLOBAL=${LP_CADDY_GLOBAL:-}
E
# Backup dienkripsi untuk DUA recipient (§5.2): operator (key disimpan OFFLINE) dan restore drill
# (key menjadi secret CI). Private key tidak pernah ada di VM: VM hanya bisa mengenkripsi.
agekeygen() {
  if command -v age-keygen >/dev/null; then age-keygen 2>/dev/null
  else docker run --rm alpine:3.22 sh -c 'apk add -q --no-cache age >/dev/null && age-keygen 2>/dev/null'; fi
}
(umask 077; agekeygen > "$OUT/backup-operator.agekey"; agekeygen > "$OUT/backup-drill.agekey")
pub() { grep -o 'age1[0-9a-z]*' "$1" | head -1; }
w backup.env <<E
PGHOST=postgres
PGUSER=ledgerpay_backup
PGPASSWORD=${PG_BACKUP}
PGDATABASE=ledgerpay
LP_BACKUP_AGE_RECIPIENTS=$(pub "$OUT/backup-operator.agekey") $(pub "$OUT/backup-drill.agekey")
LP_PUSHGATEWAY_URL=http://pushgateway:9091
# Offsite (R2/B2, kredensial WRITE-ONLY). Kosong = backup hanya di disk VM.
LP_BACKUP_REMOTE=
# RCLONE_CONFIG_OFFSITE_TYPE=s3
# RCLONE_CONFIG_OFFSITE_PROVIDER=Cloudflare
# RCLONE_CONFIG_OFFSITE_ACCESS_KEY_ID=
# RCLONE_CONFIG_OFFSITE_SECRET_ACCESS_KEY=
# RCLONE_CONFIG_OFFSITE_ENDPOINT=https://<account>.r2.cloudflarestorage.com
E
echo "env files written to $OUT (mode 0600). Grafana admin password: see grafana.env"
echo "backup keys: move $OUT/backup-operator.agekey OFFLINE (password manager), then delete it here;"
echo "             $OUT/backup-drill.agekey → GitHub secret RESTORE_DRILL_AGE_KEY. Neither is copied to the VM."
