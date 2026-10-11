#!/usr/bin/env bash
# Deploy satu SHA ke VM — dari laptop (manual) atau dari CI (main.yml / rollback.yml):
#   deploy/prod/deploy.sh <host|local> <git-sha> [--skip-migrate]
# - konfigurasi (compose, Prometheus, Grafana, init SQL) dikirim dengan `git archive <sha>`;
# - env file dari deploy/prod/env/ disalin ke /srv/ledgerpay/env (0600), KECUALI LP_SKIP_ENV=1 (CI:
#   secret sudah ada di VM sejak deploy pertama dan tidak pernah disimpan di GitHub);
# - LP_IMAGE_PREFIX=ghcr.io/<owner>/ → VM mem-pull image yang sudah dibangun + dipindai CI;
#   LP_REGISTRY_TOKEN (opsional) → docker login sementara di VM, logout setelah deploy;
# - remote-deploy.sh di VM: build/pull, migrate, health gate, smoke, rollback otomatis.
# HOST=local menjalankan semuanya di mesin ini (uji lokal, LP_ROOT ke folder sementara).
set -euo pipefail
HOST=${1:?usage: deploy.sh <host|local> <git-sha> [--skip-migrate]}
SHA=$(git rev-parse --verify "${2:?git sha}^{commit}")
SKIP_MIGRATE=${3:-}
case "$SKIP_MIGRATE" in ""|--skip-migrate) ;; *) echo "unknown option $SKIP_MIGRATE"; exit 2 ;; esac
ENV_DIR=${ENV_DIR:-$(dirname "$0")/env}
ROOT=${LP_ROOT:-/srv/ledgerpay}
USER_AT=${DEPLOY_USER:-deploy}@${HOST}
[ -n "${LP_SKIP_ENV:-}" ] || [ -f "$ENV_DIR/api.env" ] || { echo "missing $ENV_DIR — run deploy/prod/init-env.sh first"; exit 1; }
git diff --quiet "$SHA" -- deploy/ backend/ frontend/ 2>/dev/null || echo "note: working tree differs from $SHA; deploying the COMMITTED $SHA"

run() { if [ "$HOST" = local ]; then bash -c "$1"; else ssh -o BatchMode=yes "$USER_AT" "$1"; fi; }
echo "== upload release $SHA"
if [ "$HOST" = local ]; then
  mkdir -p "$ROOT/releases/$SHA" && git archive "$SHA" | tar -x -C "$ROOT/releases/$SHA"
  [ -n "${LP_SKIP_ENV:-}" ] || { mkdir -p "$ROOT/env" && install -m 600 "$ENV_DIR"/*.env "$ROOT/env/"; }
else
  git archive "$SHA" | ssh -o BatchMode=yes "$USER_AT" "mkdir -p $ROOT/releases/$SHA && tar -x -C $ROOT/releases/$SHA"
  if [ -z "${LP_SKIP_ENV:-}" ]; then
    ssh -o BatchMode=yes "$USER_AT" "mkdir -p $ROOT/env && chmod 700 $ROOT/env"
    scp -q "$ENV_DIR"/*.env "$USER_AT:$ROOT/env/" && ssh -o BatchMode=yes "$USER_AT" "chmod 600 $ROOT/env/*.env"
  fi
fi
PREFIX=${LP_IMAGE_PREFIX:-}
REGISTRY=${PREFIX%%/*}
if [ -n "${LP_REGISTRY_TOKEN:-}" ] && [ -n "$REGISTRY" ]; then
  # Token CI berumur pendek (habis saat job selesai); tetap di-logout supaya tidak tertinggal di VM.
  printf %s "$LP_REGISTRY_TOKEN" | run "docker login $REGISTRY -u ${LP_REGISTRY_USER:-ci} --password-stdin >/dev/null"
  trap 'run "docker logout $REGISTRY >/dev/null" || true' EXIT
fi
echo "== remote deploy"
# Setiap nilai di-quote (%q) sebelum masuk string perintah remote: alasan rollback berisi spasi
# ("rollback:<actor>:<reason>") pernah terpecah menjadi perintah terpisah di uji lokal.
q() { printf '%q' "$1"; }
run "cd $(q "$ROOT/releases/$SHA") && LP_ROOT=$(q "$ROOT") LP_IMAGE_PREFIX=$(q "${LP_IMAGE_PREFIX:-}") \
  LP_DEPLOY_ACTOR=$(q "${LP_DEPLOY_ACTOR:-manual}") LP_HTTPS_PUBLIC_PORT=$(q "${LP_HTTPS_PUBLIC_PORT:-443}") \
  LP_HTTP_PUBLIC_PORT=$(q "${LP_HTTP_PUBLIC_PORT:-80}") LP_MOCK_ADMIN_PORT=$(q "${LP_MOCK_ADMIN_PORT:-9091}") \
  LP_SMOKE_INSECURE=$(q "${LP_SMOKE_INSECURE:-}") LP_TRACING=$(q "${LP_TRACING:-}") bash deploy/prod/remote-deploy.sh $(q "$SHA") $SKIP_MIGRATE"
