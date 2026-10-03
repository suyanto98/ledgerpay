#!/usr/bin/env bash
# Smoke test DoD Sprint 1: register → login → GET /me, plus login gagal yang identik.
set -euo pipefail
API=${API:-http://localhost:8080/api/v1}
EMAIL="smoke-$(date +%s)@example.com"
PASS="correct horse battery"

echo "== register $EMAIL"
curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\",\"display_name\":\"Smoke\"}"; echo

echo "== login"
TOKEN=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" | sed -E 's/.*"access_token":"([^"]+)".*/\1/')

echo "== GET /me"
curl -fsS "$API/me" -H "Authorization: Bearer $TOKEN" -D - | grep -Ei '^(HTTP|x-request-id)|"email"'; echo

echo "== login gagal: password salah vs email tak dikenal (harus identik kecuali request_id)"
curl -sS -X POST "$API/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$EMAIL\",\"password\":\"wrong password!!\"}"; echo
curl -sS -X POST "$API/auth/login" -H 'Content-Type: application/json' -d '{"email":"nobody@example.com","password":"wrong password!!"}'; echo
