#!/usr/bin/env bash
# Demo C — Payout dengan hasil tak diketahui (ADR-L06, LF-11).
# Rekening berakhiran 0408 = bank MEMPROSES transfer tetapi responsnya tidak pernah sampai.
# Worker dibunuh (docker kill) saat menunggu bank, lalu dinyalakan lagi. Hasil yang benar:
# payout SUCCEEDED, bank mencatat TEPAT 1 transfer, satu journal settle. Tidak ada submit ulang buta.
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh
MOCK=${MOCK:-http://localhost:${LP_MOCK_PORT:-9091}}
t0=$(date +%s); ts() { printf '[+%3ss] ' $(( $(date +%s) - t0 )); }

curl -fsS -X POST "$MOCK/admin/reset" >/dev/null
RINA="rina-c-$RUN@example.com"
TOKEN=$(new_user "$RINA" "Rina")
credit "$RINA" 50000000   # Rp500.000
ts; echo "saldo awal: $(balance "$TOKEN")"

P=$(curl -fsS -X POST "$API/payouts" -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: demo-c-$RUN" \
  -H 'Content-Type: application/json' \
  -d '{"amount":"20000000","currency":"IDR","destination":{"bank_code":"MOCKBANK","account_number":"9900000408","holder_name":"Rina"}}')
ID=$(echo "$P" | jq -r .id)
ts; echo "payout dibuat (202): $(echo "$P" | jq -c '{id, status, display_status}')"
ts; echo "saldo setelah hold: $(balance "$TOKEN")  (dana ditahan seketika)"

status() { curl -fsS "$API/payouts/$ID" -H "Authorization: Bearer $TOKEN" | jq -r .status; }
until [ "$(status)" = "SUBMITTED" ]; do sleep 0.2; done
ts; echo "status SUBMITTED: worker sedang menunggu respons bank (yang tidak akan datang)"
$COMPOSE kill worker >/dev/null 2>&1
ts; echo "💥 docker kill worker"
ts; echo "bank: $(curl -fsS "$MOCK/admin/transfers?idempotency_key=$ID" | jq -c .)  ← uang SUDAH terkirim"

$COMPOSE start worker >/dev/null 2>&1
ts; echo "worker dinyalakan lagi; menunggu lease 30 dtk habis → job diklaim ulang → UNKNOWN → status query"
last=""
for _ in $(seq 1 600); do
  s=$(status); [ "$s" != "$last" ] && { ts; echo "status: $s"; last=$s; }
  [ "$s" = "SUCCEEDED" ] || [ "$s" = "FAILED" ] && break
  sleep 0.5
done
ts; echo "bank: $(curl -fsS "$MOCK/admin/transfers?idempotency_key=$ID" | jq -c .)  ← tetap 1 transfer"
ts; echo "saldo akhir: $(balance "$TOKEN")  (harus 30000000)"
verify
