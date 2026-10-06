#!/usr/bin/env bash
# Demo D — Rekonsiliasi (LF-17). Bank mengeksekusi payout, responsnya hilang, dan status query bank
# sedang mati → payout menggantung UNKNOWN. File settlement bank (sumber kebenaran kedua) menunjukkan
# SUCCEEDED → recon menandai STATUS_MISMATCH → ops menyelesaikannya DENGAN BUKTI dari file itu.
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh
MOCK=${MOCK:-http://localhost:${LP_MOCK_PORT:-9091}}
DATE=$(TZ=Asia/Jakarta date +%F)   # business_date = hari ini (WIB)
curl -fsS -X POST "$MOCK/admin/reset" >/dev/null
curl -fsS -X PUT "$MOCK/admin/scenario" -H 'Content-Type: application/json' -d '{"charges":"ok","payout_status_query":"disabled"}' >/dev/null

OPS="ops-d-$RUN@example.com"; RINA="rina-d-$RUN@example.com"
new_user "$OPS" "Ops" >/dev/null
$COMPOSE exec -T api /app/ledgerpay grant-ops "$OPS" >/dev/null
OPS_TOKEN=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$OPS\",\"password\":\"$PASS\"}" | jq -r .access_token)
RINA_TOKEN=$(new_user "$RINA" "Rina")
credit "$RINA" 10000000

P=$(curl -fsS -X POST "$API/payouts" -H "Authorization: Bearer $RINA_TOKEN" -H "Idempotency-Key: d-$RUN" -H 'Content-Type: application/json' \
  -d '{"amount":"3000000","currency":"IDR","destination":{"bank_code":"MOCKBANK","account_number":"9900000408","holder_name":"Rina"}}' | jq -r .id)
until [ "$(curl -fsS "$API/payouts/$P" -H "Authorization: Bearer $RINA_TOKEN" | jq -r .status)" = "UNKNOWN" ]; do sleep 0.5; done
echo "== payout $P: UNKNOWN (status query bank tidak tersedia)"

echo "== recon $DATE"
curl -fsS -X POST "$API/ops/recon-runs" -H "Authorization: Bearer $OPS_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"business_date\":\"$DATE\",\"source\":\"PULL\"}" \
  | jq -c '{status, row_count, matched_count, status_mismatch_count, missing_internal_count, missing_external_count, amount_mismatch_count}'
ITEM=$(curl -fsS "$API/ops/recon-items?resolution=OPEN&category=STATUS_MISMATCH" -H "Authorization: Bearer $OPS_TOKEN" \
  | jq -r --arg p "$P" '.data[] | select(.internal_id == $p) | .id')
curl -fsS "$API/ops/recon-items?resolution=OPEN" -H "Authorization: Bearer $OPS_TOKEN" \
  | jq -c --arg p "$P" '.data[] | select(.internal_id == $p) | {category, internal_status, external_status, external_amount}'

echo "== ops resolve: APPLY_PAYOUT_SETTLE dengan bukti dari file settlement"
curl -fsS -X POST "$API/ops/recon-items/$ITEM/resolve" -H "Authorization: Bearer $OPS_TOKEN" -H "Idempotency-Key: d-res-$RUN" \
  -H 'Content-Type: application/json' \
  -d "{\"action\":\"APPLY_PAYOUT_SETTLE\",\"evidence_ref\":\"settlement-$DATE.csv#$P\",\"reason\":\"Settlement bank menunjukkan SUCCEEDED\"}" \
  | jq -c '{resolution, resolution_action}'
echo "payout: $(curl -fsS "$API/payouts/$P" -H "Authorization: Bearer $RINA_TOKEN" | jq -c '{status, display_status}')"
echo "bank:   $(curl -fsS "$MOCK/admin/transfers?idempotency_key=$P" | jq -c '{transfers_executed}')  ← tetap 1"
expect "status payout" "$(curl -fsS "$API/payouts/$P" -H "Authorization: Bearer $RINA_TOKEN" | jq -r .status)" SUCCEEDED
expect "transfer di bank" "$(curl -fsS "$MOCK/admin/transfers?idempotency_key=$P" | jq -r .transfers_executed)" 1
curl -fsS -X PUT "$MOCK/admin/scenario" -H 'Content-Type: application/json' -d '{"charges":"ok"}' >/dev/null
verify
