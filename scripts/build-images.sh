#!/usr/bin/env bash
# Membangun keempat image rilis untuk satu SHA (dipakai CI main.yml dan uji lokal):
#   scripts/build-images.sh <prefix> <sha> [--push]      contoh prefix: ghcr.io/<owner>/
# Nama sama dengan compose.prod.yml: ${prefix}ledgerpay-{app,mock,edge,backup}:<sha>.
set -euo pipefail
cd "$(dirname "$0")/.."
PREFIX=${1?usage: build-images.sh <prefix> <sha> [--push]}
SHA=${2:?sha}
PUSH=${3:-}
build() { # <name> <dockerfile> [extra args...]
  local name=$1 file=$2; shift 2
  docker build -q -f "$file" -t "${PREFIX}ledgerpay-$name:$SHA" --label "org.opencontainers.image.revision=$SHA" "$@" . >/dev/null
  echo "built ${PREFIX}ledgerpay-$name:$SHA"
  if [ "$PUSH" = --push ]; then docker push -q "${PREFIX}ledgerpay-$name:$SHA" >/dev/null && echo "pushed ${PREFIX}ledgerpay-$name:$SHA"; fi
}
# Image yang di-deploy ke VM demo publik = varian demo (amandemen roadmap S8: image release dibangun
# dan dipindai, tidak di-push). Endpoint demo tetap mati kecuali DEMO_MODE=true + DEMO_TOKEN.
build app deploy/Dockerfile --target ledgerpay --build-arg "VERSION=$SHA" --build-arg BUILD_TAGS=demo
build mock deploy/Dockerfile --target mock-provider
build edge deploy/prod/edge.Dockerfile
build backup deploy/backup/Dockerfile
