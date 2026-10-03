#!/usr/bin/env bash
# Demo Sprint 4 — merchant (API key) membuat payment intent → user membayar → saldo pindah.
# Dua "tab" meng-confirm bersamaan dengan key berbeda → tepat satu pembayaran (LI-13).
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh

OPS="ops-$RUN@example.com"; RINA="rina-p-$RUN@example.com"
new_user "$OPS" "Ops" >/dev/null
$COMPOSE exec -T api /app/ledgerpay grant-ops "$OPS" >/dev/null   # role OPS hanya lewat CLI
OPS_TOKEN=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$OPS\",\"password\":\"$PASS\"}" | jq -r .access_token)   # login ulang: role ada di token
RINA_TOKEN=$(new_user "$RINA" "Rina")
credit "$RINA" 10000000

MERCHANT_ID=$($COMPOSE exec -T api /app/ledgerpay merchant-create "Kopi Budi $RUN" | tr -d '\r')
echo "== merchant dibuat: $MERCHANT_ID"
API_KEY=$(curl -fsS -X POST "$API/ops/merchants/$MERCHANT_ID/api-keys" -H "Authorization: Bearer $OPS_TOKEN" \
  -H "Idempotency-Key: key-$RUN" -H 'Content-Type: application/json' -d '{"label":"demo"}' | jq -r .api_key)
echo "== API key diterbitkan ops (tampil sekali): ${API_KEY:0:17}…"

echo "== merchant membuat intent Rp75.000"
INTENT=$(curl -fsS -X POST "$API/merchant/payment-intents" -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: pi-$RUN" -H 'Content-Type: application/json' \
  -d '{"merchant_order_id":"ORD-'"$RUN"'","amount":"7500000","currency":"IDR","description":"Kopi susu x3"}')
echo "$INTENT" | jq -c '{id, status, amount, checkout_url}'
ID=$(echo "$INTENT" | jq -r .id)

echo "== Rina meng-confirm dari DUA tab bersamaan (key berbeda)"
for tab in 1 2; do
  curl -s -o /dev/null -w "tab $tab: %{http_code}\n" -X POST "$API/payment-intents/$ID/confirm" \
    -H "Authorization: Bearer $RINA_TOKEN" -H "Idempotency-Key: confirm-$RUN-$tab" -H 'Content-Type: application/json' -d '{}' &
done
wait

echo "saldo Rina: $(balance "$RINA_TOKEN")  (harus 2500000: terdebet SEKALI)"
curl -fsS "$API/merchant/payment-intents/$ID" -H "Authorization: Bearer $API_KEY" | jq -c '{status, payer}'
verify
