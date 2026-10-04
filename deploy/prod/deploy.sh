#!/usr/bin/env bash
# Deploy manual (Sprint 7) — dari laptop ke VM, satu SHA:
#   deploy/prod/deploy.sh <host> <git-sha>
# - kode dikirim dengan `git archive <sha>` (tidak butuh GitHub/registry; CD + GHCR di Sprint 8);
# - env file dari deploy/prod/env/ disalin ke /srv/ledgerpay/env (0600);
# - remote-deploy.sh di VM: build, migrate, health gate, smoke, rollback otomatis.
# DEPLOY_HOST=local menjalankan semuanya di mesin ini (uji lokal, LP_ROOT ke folder sementara).
set -euo pipefail
HOST=${1:?usage: deploy.sh <host|local> <git-sha>}
SHA=$(git rev-parse --verify "${2:?git sha}^{commit}")
ENV_DIR=${ENV_DIR:-$(dirname "$0")/env}
ROOT=${LP_ROOT:-/srv/ledgerpay}
USER_AT=${DEPLOY_USER:-deploy}@${HOST}
[ -f "$ENV_DIR/api.env" ] || { echo "missing $ENV_DIR — run deploy/prod/init-env.sh first"; exit 1; }
git diff --quiet "$SHA" -- deploy/ db/ cmd/ internal/ web/ 2>/dev/null || echo "note: working tree differs from $SHA; deploying the COMMITTED $SHA"

run() { if [ "$HOST" = local ]; then bash -c "$1"; else ssh -o BatchMode=yes "$USER_AT" "$1"; fi; }
echo "== upload release $SHA"
if [ "$HOST" = local ]; then
  mkdir -p "$ROOT/releases/$SHA" && git archive "$SHA" | tar -x -C "$ROOT/releases/$SHA"
  mkdir -p "$ROOT/env" && install -m 600 "$ENV_DIR"/*.env "$ROOT/env/"
else
  git archive "$SHA" | ssh -o BatchMode=yes "$USER_AT" "mkdir -p $ROOT/releases/$SHA && tar -x -C $ROOT/releases/$SHA"
  ssh -o BatchMode=yes "$USER_AT" "mkdir -p $ROOT/env && chmod 700 $ROOT/env"
  scp -q "$ENV_DIR"/*.env "$USER_AT:$ROOT/env/" && ssh -o BatchMode=yes "$USER_AT" "chmod 600 $ROOT/env/*.env"
fi
echo "== remote deploy"
run "cd $ROOT/releases/$SHA && LP_ROOT=$ROOT LP_HTTPS_PUBLIC_PORT=${LP_HTTPS_PUBLIC_PORT:-443} LP_HTTP_PUBLIC_PORT=${LP_HTTP_PUBLIC_PORT:-80} LP_MOCK_ADMIN_PORT=${LP_MOCK_ADMIN_PORT:-9091} LP_SMOKE_INSECURE=${LP_SMOKE_INSECURE:-} bash deploy/prod/remote-deploy.sh $SHA"
