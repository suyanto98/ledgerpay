# shellcheck shell=bash
# shellcheck disable=SC2034  # variabel dipakai skrip yang men-source file ini
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

# expect <label> <got> <want>: asersi demo. Gagal → exit 1, sehingga `make demo-verify` (nightly) merah
# saat demo tidak lagi membuktikan apa yang dijanjikan, bukan hanya mencetak angka yang salah.
expect() {
  if [ "$2" = "$3" ]; then echo "  ✓ $1 = $3"; else echo "  ✗ $1: got '$2', want '$3'" >&2; exit 1; fi
}
