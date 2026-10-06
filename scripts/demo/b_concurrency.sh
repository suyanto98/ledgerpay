#!/usr/bin/env bash
# Demo B — Concurrency (LF-04, ADR-L03). Saldo Rp100.000, 100 transfer Rp10.000 PARALEL,
# masing-masing dengan Idempotency-Key unik → tepat 10× 201, 90× 422, saldo 0, ledger bersih.
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh

RINA="rina-b-$RUN@example.com"; ANDI="andi-b-$RUN@example.com"
TOKEN=$(new_user "$RINA" "Rina"); new_user "$ANDI" "Andi S." >/dev/null
credit "$RINA" 10000000
echo "saldo awal Rina: $(balance "$TOKEN")"

echo "== 100 transfer × 1000000 (Rp10.000) dikirim bersamaan"
export API TOKEN ANDI RUN
seq 1 100 | xargs -P 100 -I{} sh -c 'curl -s -o /dev/null -w "%{http_code}\n" -X POST "$API/transfers" \
  -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: demo-b-$RUN-{}" -H "Content-Type: application/json" \
  -d "{\"recipient_email\":\"$ANDI\",\"amount\":\"1000000\",\"currency\":\"IDR\"}"' | sort | uniq -c | tee /tmp/demo-b-$RUN

echo "saldo akhir Rina: $(balance "$TOKEN")  (harus 0, tidak pernah negatif)"
expect "transfer 201" "$(awk '$2 == 201 {print $1}' /tmp/demo-b-$RUN)" 10
expect "transfer 422" "$(awk '$2 == 422 {print $1}' /tmp/demo-b-$RUN)" 90
expect "saldo akhir Rina" "$(balance "$TOKEN")" 0
verify
