#!/usr/bin/env bash
# Demo A — Idempotency (LF-05, ADR-L02). Satu niat user = satu efek finansial.
set -euo pipefail
cd "$(dirname "$0")/../.."
source scripts/demo/lib.sh

RINA="rina-$RUN@example.com"; ANDI="andi-$RUN@example.com"
TOKEN=$(new_user "$RINA" "Rina"); new_user "$ANDI" "Andi S." >/dev/null
credit "$RINA" 10000000   # Rp100.000
echo "saldo awal Rina: $(balance "$TOKEN")"

K="demo-a-$RUN"
post() { # post <amount> → status + header replay + body ringkas
  curl -sS -D /tmp/demo-a-h -o /tmp/demo-a-b -X POST "$API/transfers" \
    -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $K" -H 'Content-Type: application/json' \
    -d "{\"recipient_email\":\"$ANDI\",\"amount\":\"$1\",\"currency\":\"IDR\"}"
  printf '%s | replayed=%s | %s\n' "$(head -1 /tmp/demo-a-h | tr -d '\r')" \
    "$(grep -i '^idempotent-replayed' /tmp/demo-a-h | awk '{print $2}' | tr -d '\r' || true)" \
    "$(jq -c '{id, code, amount}' /tmp/demo-a-b)"
}

echo; echo "== 1) transfer Rp50.000 dengan key $K"; post 5000000
echo "== 2) kirim ULANG persis sama (client timeout lalu retry)"; post 5000000
echo "== 3) key sama, jumlah beda"; post 6000000
echo; echo "saldo akhir Rina: $(balance "$TOKEN")  (harus 5000000: terdebet SEKALI)"
expect "saldo akhir Rina" "$(balance "$TOKEN")" 5000000
verify
