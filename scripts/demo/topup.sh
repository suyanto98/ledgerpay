#!/usr/bin/env bash
# Demo Sprint 5 — top-up lewat payment provider (mock-provider, ADR-L05):
#   1) top-up normal: POST /topups TIDAK menambah saldo; saldo bertambah setelah webhook diproses worker
#   2) webhook dikirim 3× (magic amount Rp11.111) → satu journal
#   3) webhook dengan signature palsu → ditolak 401, tanpa efek
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh
MOCK=${MOCK:-http://localhost:${LP_MOCK_PORT:-9091}}

curl -fsS -X POST "$MOCK/admin/reset" >/dev/null
RINA="rina-t-$RUN@example.com"
TOKEN=$(new_user "$RINA" "Rina")

topup() { # topup <amount> → id dan charge_id
  curl -fsS -X POST "$API/topups" -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: topup-$RUN-$1-$RANDOM" \
    -H 'Content-Type: application/json' -d "{\"amount\":\"$1\",\"currency\":\"IDR\"}"
}
pay() { curl -fsS -X POST "$MOCK/admin/charges/$1/pay" >/dev/null; }   # = tombol "Bayar" di halaman checkout
wait_status() { # wait_status <topup_id> <status>
  for _ in $(seq 1 50); do
    s=$(curl -fsS "$API/topups/$1" -H "Authorization: Bearer $TOKEN" | jq -r .status)
    [ "$s" = "$2" ] && return 0; sleep 0.2
  done; echo "timeout menunggu $2 (status $s)"; return 1
}

echo "== 1) top-up Rp100.000"
T=$(topup 10000000); ID=$(echo "$T" | jq -r .id); CH=$(echo "$T" | jq -r .checkout_url | sed 's#.*/##')
echo "$T" | jq -c '{id, status, checkout_url}'
echo "saldo setelah POST /topups: $(balance "$TOKEN")  (harus 0: endpoint ini tidak pernah menambah saldo)"
pay "$CH"; wait_status "$ID" SUCCEEDED
echo "saldo setelah webhook diproses worker: $(balance "$TOKEN")"

echo; echo "== 2) top-up Rp11.111 (magic amount → provider mengirim webhook 3×)"
T=$(topup 1111100); ID=$(echo "$T" | jq -r .id); CH=$(echo "$T" | jq -r .checkout_url | sed 's#.*/##')
pay "$CH"; wait_status "$ID" SUCCEEDED; sleep 0.5
curl -fsS "$MOCK/admin/deliveries" | jq -c "[.[] | select(.charge_id==\"$CH\") | {event_id, status}]"
echo "saldo: $(balance "$TOKEN")  (harus 11111100 = Rp100.000 + Rp11.111: dikreditkan SEKALI walau webhook datang 3×)"

echo; echo "== 3) webhook dengan signature palsu"
curl -fsS -X PUT "$MOCK/admin/scenario" -d '{"charges":"ok","webhooks":["webhook_bad_signature"]}' >/dev/null
T=$(topup 5000000); ID=$(echo "$T" | jq -r .id); CH=$(echo "$T" | jq -r .checkout_url | sed 's#.*/##')
pay "$CH"; sleep 1
curl -fsS "$MOCK/admin/deliveries" | jq -c "[.[] | select(.charge_id==\"$CH\") | {event_id, status, bad_signature}]"
echo "status top-up: $(curl -fsS "$API/topups/$ID" -H "Authorization: Bearer $TOKEN" | jq -r .status)  (tetap PENDING)"
echo "saldo: $(balance "$TOKEN")  (tidak berubah)"
curl -fsS -X PUT "$MOCK/admin/scenario" -d '{"charges":"ok","webhooks":[]}' >/dev/null
verify
