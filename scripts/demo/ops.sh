#!/usr/bin/env bash
# Demo Sprint 8 — refund paralel (LI-08) dan ops menyelesaikan payout UNKNOWN (LF-18, ADR-L06).
#  1. Pembayaran Rp100.000, lima refund Rp30.000 BERSAMAAN (key berbeda) → tepat 3 sukses.
#  2. Status query bank dimatikan → payout menggantung di UNKNOWN → ops resolve:
#     tanpa bukti → 422; dengan bukti → SUCCEEDED lewat journal PAYOUT_SETTLE (bukan edit saldo).
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh
MOCK=${MOCK:-http://localhost:${LP_MOCK_PORT:-9091}}
curl -fsS -X POST "$MOCK/admin/reset" >/dev/null

OPS="ops-o-$RUN@example.com"; RINA="rina-o-$RUN@example.com"
new_user "$OPS" "Ops" >/dev/null
$COMPOSE exec -T api /app/ledgerpay grant-ops "$OPS" >/dev/null
OPS_TOKEN=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$OPS\",\"password\":\"$PASS\"}" | jq -r .access_token)
RINA_TOKEN=$(new_user "$RINA" "Rina")
credit "$RINA" 50000000   # Rp500.000

echo "== 1. Refund paralel"
MERCHANT_ID=$($COMPOSE exec -T api /app/ledgerpay merchant-create "Kopi Budi $RUN" | tr -d '\r')
API_KEY=$(curl -fsS -X POST "$API/ops/merchants/$MERCHANT_ID/api-keys" -H "Authorization: Bearer $OPS_TOKEN" \
  -H "Idempotency-Key: key-$RUN" -H 'Content-Type: application/json' -d '{"label":"demo"}' | jq -r .api_key)
ID=$(curl -fsS -X POST "$API/merchant/payment-intents" -H "Authorization: Bearer $API_KEY" -H "Idempotency-Key: pi-$RUN" \
  -H 'Content-Type: application/json' -d '{"merchant_order_id":"ORD-'"$RUN"'","amount":"10000000","currency":"IDR"}' | jq -r .id)
curl -fsS -o /dev/null -X POST "$API/payment-intents/$ID/confirm" -H "Authorization: Bearer $RINA_TOKEN" \
  -H "Idempotency-Key: c-$RUN" -H 'Content-Type: application/json' -d '{}'
echo "Rina membayar Rp100.000; saldo: $(balance "$RINA_TOKEN")"
for i in 1 2 3 4 5; do
  curl -s -X POST "$API/merchant/payment-intents/$ID/refunds" -H "Authorization: Bearer $API_KEY" \
    -H "Idempotency-Key: rf-$RUN-$i" -H 'Content-Type: application/json' -d '{"amount":"3000000","reason":"demo"}' \
    | jq -c --arg i "$i" '{refund: $i, result: (.code // "201"), refundable_amount}' &
done
wait
curl -fsS "$API/merchant/payment-intents/$ID" -H "Authorization: Bearer $API_KEY" | jq -c '{status, refunded_amount, refunds: (.refunds | length)}'
echo "saldo Rina: $(balance "$RINA_TOKEN")  (harus 49000000: bayar 100rb, kembali 90rb)"
expect "saldo Rina" "$(balance "$RINA_TOKEN")" 49000000

echo "== 2. Ops menyelesaikan payout UNKNOWN"
curl -fsS -X PUT "$MOCK/admin/scenario" -H 'Content-Type: application/json' -d '{"charges":"ok","payout_status_query":"disabled"}' >/dev/null
P=$(curl -fsS -X POST "$API/payouts" -H "Authorization: Bearer $RINA_TOKEN" -H "Idempotency-Key: po-$RUN" -H 'Content-Type: application/json' \
  -d '{"amount":"2000000","currency":"IDR","destination":{"bank_code":"MOCKBANK","account_number":"9900000408","holder_name":"Rina"}}' | jq -r .id)
until [ "$(curl -fsS "$API/payouts/$P" -H "Authorization: Bearer $RINA_TOKEN" | jq -r .status)" = "UNKNOWN" ]; do sleep 0.5; done
echo "payout $P: UNKNOWN (bank timeout, status query bank sedang tidak tersedia)"
curl -fsS "$API/ops/payouts?status=UNKNOWN" -H "Authorization: Bearer $OPS_TOKEN" | jq -c '.data[] | {id, status, last_error, user: .user.email}'
echo "bank (portal/mutasi): $(curl -fsS "$MOCK/admin/transfers?idempotency_key=$P" | jq -c .)"
echo "-- resolve TANPA bukti:"
curl -s -X POST "$API/ops/payouts/$P/resolve" -H "Authorization: Bearer $OPS_TOKEN" -H "Idempotency-Key: r0-$RUN" \
  -H 'Content-Type: application/json' -d '{"outcome":"SUCCEEDED","reason":"yakin sudah terkirim"}' | jq -c '{code}'
echo "-- resolve DENGAN bukti:"
curl -fsS -X POST "$API/ops/payouts/$P/resolve" -H "Authorization: Bearer $OPS_TOKEN" -H "Idempotency-Key: r1-$RUN" \
  -H 'Content-Type: application/json' \
  -d '{"outcome":"SUCCEEDED","evidence_ref":"MOCKBANK-ADMIN-TRANSFERS#'"$P"'","reason":"Mutasi bank menunjukkan transfer tereksekusi"}' \
  | jq -c '{status, resolution_source, settle_journal_id}'
curl -fsS -X PUT "$MOCK/admin/scenario" -H 'Content-Type: application/json' -d '{"charges":"ok"}' >/dev/null
J=$(curl -fsS "$API/ops/transactions/search?q=$P" -H "Authorization: Bearer $OPS_TOKEN" | jq -r '.data[0].journal_ids[-1]')
echo "journal settle:"; curl -fsS "$API/ops/journals/$J" -H "Authorization: Bearer $OPS_TOKEN" | jq -c '{purpose, actor_type, entries: [.entries[] | {owner, direction, amount}]}'
verify
