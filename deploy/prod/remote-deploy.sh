#!/usr/bin/env bash
# Dijalankan DI VM oleh deploy.sh, dari folder rilis /srv/ledgerpay/releases/<sha>.
# Recreate per service dengan health gate (docs/design/26-deploy-migrations-backup.md §6.1):
#   build/pull → migrate (gagal = berhenti, app lama tetap jalan) → worker → api → health gate → smoke
#   sukses → current = rilis ini; gagal → jalankan ulang rilis sebelumnya.
#
#   remote-deploy.sh <sha> [--skip-migrate]
#   LP_IMAGE_PREFIX kosong  → image dibangun di VM (deploy manual dari laptop, Sprint 7)
#   LP_IMAGE_PREFIX=ghcr.io/<owner>/ → image yang SUDAH dibangun + dipindai CI di-pull (CD, Sprint 8)
#   --skip-migrate          → rollback aplikasi ke rilis lama (skema tidak pernah diturunkan, ADR-013)
set -euo pipefail
SHA=${1:?sha}
SKIP_MIGRATE=${2:-}
ROOT=${LP_ROOT:-/srv/ledgerpay}
export LP_ENV_DIR=$ROOT/env LP_VERSION=$SHA LP_IMAGE_PREFIX=${LP_IMAGE_PREFIX:-}
# Satu deploy pada satu waktu di VM ini (CD yang antre + operator manual tidak saling tabrak).
exec 9>"$ROOT/deploy.lock"
flock -n 9 || { echo "another deploy is running (lock $ROOT/deploy.lock)"; exit 1; }
started=$(date +%s)
DOMAIN=$(grep '^LP_DOMAIN=' "$LP_ENV_DIR/edge.env" | cut -d= -f2)
PORT=${LP_HTTPS_PUBLIC_PORT:-443}
C="docker compose -f deploy/prod/compose.prod.yml"
log() { printf '[deploy %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

prev=""
[ -L "$ROOT/current" ] && prev=$(basename "$(readlink -f "$ROOT/current")")
printf %s "$LP_IMAGE_PREFIX" > "$ROOT/releases/$SHA/.image-prefix"
log "deploying $SHA (previous: ${prev:-none})"

record() { printf '%s\t%s\t%s\t%ss\t%s\n' "$(date -u +%FT%TZ)" "$SHA" "$1" "$(( $(date +%s) - started ))" "${LP_DEPLOY_ACTOR:-manual}" >> "$ROOT/releases.log"; }

rollback() {
  log "FAILED — rolling back to ${prev:-<none>}"
  record failed
  [ -n "$prev" ] || { log "no previous release; stack left as is"; exit 1; }
  # Rilis lama memakai image dari mode deploy-nya sendiri (build lokal atau registry).
  (cd "$ROOT/releases/$prev" && LP_VERSION=$prev LP_IMAGE_PREFIX=$(cat .image-prefix 2>/dev/null || true) \
    docker compose -f deploy/prod/compose.prod.yml up -d --no-deps --wait worker api edge)
  log "rolled back to $prev"
  exit 1
}

if [ -n "$LP_IMAGE_PREFIX" ]; then
  log "pull images ${LP_IMAGE_PREFIX}*:$SHA (built + scanned by CI)"
  $C --profile migrate --profile backup pull migrate mock-provider edge backup
else
  log "build images"
  $C --profile migrate --profile backup build migrate mock-provider edge backup
fi
log "postgres"
$C up -d --wait postgres
if [ "$SKIP_MIGRATE" = --skip-migrate ]; then
  log "migrate SKIPPED (rollback: the schema stays at its current version; expand/contract keeps old code compatible)"
else
  log "migrate (owner DSN only here)"
  $C --profile migrate run --rm migrate || { log "migration failed — app NOT restarted (old version keeps serving)"; record migrate_failed; exit 1; }
fi
log "worker (SIGTERM: stop claiming, finish in-flight ≤ 25s)"
$C up -d --no-deps --wait worker || rollback
log "api (graceful shutdown; Caddy holds requests during recreate)"
$C up -d --no-deps --wait api || rollback
$C up -d --no-deps --wait mock-provider edge pushgateway prometheus grafana jaeger || rollback

log "health gate + smoke via Caddy (TLS)"
CURL=(curl -fsS --max-time 10 --resolve "${DOMAIN}:${PORT}:127.0.0.1")
[ -n "${LP_SMOKE_INSECURE:-}" ] && CURL+=(-k)   # hanya uji lokal (sertifikat internal Caddy)
base="https://${DOMAIN}:${PORT}"
ok=""
for _ in $(seq 1 30); do "${CURL[@]}" -o /dev/null "$base/" 2>/dev/null && { ok=1; break; }; sleep 2; done
[ -n "$ok" ] || rollback
email="smoke-$(date +%s)@example.com"
"${CURL[@]}" -o /dev/null -X POST "$base/api/v1/auth/register" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$email\",\"password\":\"smoke test password\",\"display_name\":\"Smoke\"}" || rollback
token=$("${CURL[@]}" -X POST "$base/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$email\",\"password\":\"smoke test password\"}" | sed -E 's/.*"access_token":"([^"]+)".*/\1/') || rollback
"${CURL[@]}" "$base/api/v1/wallet" -H "Authorization: Bearer $token" | grep -q '"balance":"0"' || rollback
"${CURL[@]}" -o /dev/null "https://pay.${DOMAIN}:${PORT}/checkout/does-not-exist" 2>/dev/null || true   # 404 = rute checkout hidup

ln -sfn "$ROOT/releases/$SHA" "$ROOT/current"
record success
[ -n "$prev" ] && [ "$prev" != "$SHA" ] && ln -sfn "$ROOT/releases/$prev" "$ROOT/previous"
log "OK — current = $SHA"
