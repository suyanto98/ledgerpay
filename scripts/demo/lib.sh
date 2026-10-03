# Helper bersama Demo A/B. Butuh: curl, jq, docker compose (stack `make up` sudah jalan).
API=${API:-http://localhost:${LP_HTTP_PORT:-8080}/api/v1}
COMPOSE=${COMPOSE:-docker compose -f deploy/compose.yml}
PASS="correct horse battery"
RUN=$(date +%s)

# new_user <email> <display_name> → mencetak access token
new_user() {
  curl -fsS -o /dev/null -X POST "$API/auth/register" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$1\",\"password\":\"$PASS\",\"display_name\":\"$2\"}"
  curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$1\",\"password\":\"$PASS\"}" | jq -r .access_token
}

# credit <email> <amount>: isi saldo lewat subcommand dev-only (top-up asli datang di Sprint 5)
credit() { $COMPOSE exec -T api /app/ledgerpay demo-credit "$1" "$2" >/dev/null; }

balance() { curl -fsS "$API/wallet" -H "Authorization: Bearer $1" | jq -r .balance; }

verify() { $COMPOSE exec -T api /app/ledgerpay ledger-verify; }
