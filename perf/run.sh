#!/usr/bin/env bash
# Menjalankan satu skenario load test terhadap perf env lokal dan menyimpan bukti ke
# perf/results/<tanggal>-<sha>/<skenario>/ (docs/design/23-performance-plan.md §5–§8):
#   perf/run.sh lp2 | lp3 [hot-index] | lp4 | lp4-control | lp9
# Per run: k6 summary, snapshot DB sebelum/sesudah + cek invariant, top pg_stat_statements,
# sampel CPU/RAM container, ledger-verify. Skenario yang invariant-nya gagal → exit 1.
set -euo pipefail
cd "$(dirname "$0")/.."
SCENARIO=${1:?usage: perf/run.sh lp2|lp3 [hot]|lp4|lp4-control|lp9}
SHA=$(git rev-parse --short HEAD)
OUT=perf/results/$(date -u +%F)-$SHA/$SCENARIO${RATE_MAX:+-max$RATE_MAX}
mkdir -p "$OUT"
P="docker compose -p ledgerpay-perf -f deploy/compose.yml -f deploy/perf/compose.perf.yml"
PG=ledgerpay-perf-postgres-1
OPS_URL=http://localhost:${LP_OPS_PORT:-9090}
sql() { docker exec "$PG" psql -U postgres -d ledgerpay -tAX -c "SET search_path = ledgerpay, public; $1" | tail -1; }
log() { printf '[perf %s] %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$OUT/run.log"; }

k6() { # <script> [env...]
  local script=$1; shift
  local envs=()
  for e in "$@"; do envs+=(-e "$e"); done
  docker run --rm --network host -u "$(id -u)" -v "$PWD/perf:/perf" "${envs[@]}" grafana/k6:1.2.3 run --quiet \
    --summary-export "/perf/${OUT#perf/}/k6-summary.json" "/perf/k6/$script" 2>&1 | tee "$OUT/k6.txt"
  # k6 menyimpan setup_data (access token user seed) di summary: buang sebelum file bisa ter-commit
  # (ditemukan gitleaks di Sprint 10).
  python3 - "$OUT/k6-summary.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1])); d.pop("setup_data", None); json.dump(d, open(sys.argv[1], "w"), indent=1)
PY
}
count() { python3 - "$OUT/k6-summary.json" "$1" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))["metrics"].get(sys.argv[2], {})
print(int(m.get("count", 0)))
PY
}

# Sampel CPU/RAM bertimestamp (≈ tiap 2 dtk) → rata-rata per tahap di stages.py.
(
  while true; do
    docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' ledgerpay-perf-api-1 ledgerpay-perf-postgres-1 ledgerpay-perf-worker-1 \
      2>/dev/null | sed "s/^/$(date +%s),/"
  done
) > "$OUT/docker-stats.csv" & STATS=$!
trap 'pkill -P $STATS 2>/dev/null || true; kill $STATS 2>/dev/null || true' EXIT
sql "SELECT pg_stat_statements_reset()" >/dev/null
{
  echo "git $SHA ($(git log -1 --format=%cI))"
  echo "machine: $(lscpu | sed -n 's/^Model name: *//p') / $(nproc) threads / $(free -h | awk '/Mem/{print $2}') RAM (shared with desktop apps + k6)"
  echo "postgres: $(docker exec "$PG" postgres --version); k6 grafana/k6:1.2.3 on the SAME machine (not separate)"
  echo "dataset: $(sql "SELECT count(*) FROM journals") journals, $(sql "SELECT count(*) FROM journal_entries") entries, $(sql "SELECT count(*) FROM users") users"
} > "$OUT/env.txt"
log "scenario $SCENARIO → $OUT"

USER_SUM_BEFORE=$(sql "SELECT sum(balance) FROM accounts WHERE user_id IS NOT NULL")
TRANSFERS_BEFORE=$(sql "SELECT count(*) FROM journals WHERE purpose = 'TRANSFER'")
PAYMENTS_BEFORE=$(sql "SELECT count(*) FROM journals WHERE purpose = 'PAYMENT'")
fail=0
case "$SCENARIO" in
  lp2)
    if [ -n "${PROFILE_AFTER:-}" ]; then # profil CPU 20 dtk, PROFILE_AFTER detik setelah transfer pertama
      transfers() { curl -fsS "$OPS_URL/metrics" | awk '/^app_http_requests_total\{method="POST",route="\/v1\/transfers",status="201"\}/{print $2+0}'; }
      base=$(transfers)
      ( for _ in $(seq 1 3000); do [ "$(transfers)" != "$base" ] && break; sleep 0.2; done
        sleep "$PROFILE_AFTER"; curl -fsS "$OPS_URL/debug/pprof/profile?seconds=20" > "$OUT/cpu.pprof" ) & PROFILER=$!
    fi
    k6 lp2_transfers.js "RATE_MAX=${RATE_MAX:-200}"
    [ -n "${PROFILER:-}" ] && wait "$PROFILER"
    ok=$(count 'http_reqs{name:transfer,status:201}')
    delta=$(( $(sql "SELECT count(*) FROM journals WHERE purpose = 'TRANSFER'") - TRANSFERS_BEFORE ))
    after=$(sql "SELECT sum(balance) FROM accounts WHERE user_id IS NOT NULL")
    log "201=$ok TRANSFER journals +$delta; Σ user balances before=$USER_SUM_BEFORE after=$after"
    [ "$ok" = "$delta" ] && [ "$after" = "$USER_SUM_BEFORE" ] || { log "INVARIANT FAILED"; fail=1; }
    ;;
  lp3)
    HOT=${2:-0}
    WALLET=$(sql "SELECT a.id FROM accounts a JOIN users u ON u.id = a.user_id WHERE u.email = 'hot-$HOT@seed.ledgerpay.test'")
    start_bal=$(sql "SELECT balance FROM accounts WHERE id = '$WALLET'")
    [ "$start_bal" = 100000000 ] || { log "hot-$HOT already used (balance $start_bal); pick another index or reseed"; exit 1; }
    k6 lp3_hot_wallet.js "HOT_USER=$HOT"
    ok=$(count 'http_reqs{name:transfer,status:201}')
    bal=$(sql "SELECT balance FROM accounts WHERE id = '$WALLET'")
    debits=$(sql "SELECT count(*) FROM journal_entries WHERE account_id = '$WALLET' AND direction = 'D'")
    log "201=$ok (want exactly 100), hot wallet balance=$bal (want 0), debit entries=$debits, 409=$(count 'http_reqs{name:transfer,status:409}'), 422=$(count 'http_reqs{name:transfer,status:422}')"
    [ "$ok" = 100 ] && [ "$bal" = 0 ] && [ "$debits" = 100 ] || { log "INVARIANT FAILED"; fail=1; }
    ;;
  lp4|lp4-control)
    N=1; [ "$SCENARIO" = lp4-control ] && N=100
    IDS=$(sql "SELECT string_agg(id::text, ',') FROM (SELECT id FROM merchants ORDER BY name LIMIT $N) m")
    MBEFORE=$(sql "SELECT coalesce(sum(balance), 0) FROM accounts WHERE merchant_id = ANY(string_to_array('$IDS', ',')::uuid[])")
    STAGE=${STAGE_SECONDS:-120}
    # Histogram lock wait diambil di batas setiap tahap. Awal beban dideteksi dari counter API sendiri
    # (intent pertama dibuat), bukan dari output k6 (--quiet tidak mencetak progres; versi pertama skrip
    # ini menunggu kata "running" dan menggantung).
    intents() { curl -fsS "$OPS_URL/metrics" | awk '/^app_http_requests_total\{.*route="\/v1\/merchant\/payment-intents",status="201"\}/{s+=$2} END{print s+0}'; }
    base=$(intents)
    (
      for _ in $(seq 1 3000); do [ "$(intents)" != "$base" ] && break; sleep 0.2; done
      date +%s > "$OUT/stage-start"
      for i in 0 1 2 3 4 5; do curl -fsS "$OPS_URL/metrics" > "$OUT/metrics-$i.txt" || true; [ $i -lt 5 ] && sleep "$STAGE"; done
    ) & SAMPLER=$!
    k6 lp4_hot_merchant.js "MERCHANT_IDS=$IDS" "STAGE_SECONDS=$STAGE"
    wait $SAMPLER || true
    ok=$(count 'http_reqs{name:confirm,status:200}')
    delta=$(( $(sql "SELECT count(*) FROM journals WHERE purpose = 'PAYMENT'") - PAYMENTS_BEFORE ))
    MAFTER=$(sql "SELECT coalesce(sum(balance), 0) FROM accounts WHERE merchant_id = ANY(string_to_array('$IDS', ',')::uuid[])")
    log "confirm 200=$ok PAYMENT journals +$delta; merchant balance +$(( MAFTER - MBEFORE )) (want $(( ok * 100000 )))"
    [ "$ok" = "$delta" ] && [ $(( MAFTER - MBEFORE )) = $(( ok * 100000 )) ] || { log "INVARIANT FAILED"; fail=1; }
    python3 perf/stages.py "$OUT" "$STAGE" | tee -a "$OUT/run.log"
    ;;
  lp9)
    k6 lp9_login.js "DURATION_SECONDS=${DURATION_SECONDS:-300}"
    log "login 200=$(count 'http_reqs{name:login,status:200}') 503=$(count 'http_reqs{name:login,status:503}') 500=$(count 'http_reqs{status:500}')"
    ;;
  *) echo "unknown scenario $SCENARIO"; exit 2 ;;
esac

[ "$(count 'http_reqs{status:500}')" = 0 ] || { log "5xx responses: $(count 'http_reqs{status:500}')"; fail=1; }
pkill -P $STATS 2>/dev/null || true; kill $STATS 2>/dev/null || true
docker exec "$PG" psql -U postgres -d ledgerpay -X -c "SELECT calls, round(total_exec_time::numeric, 0) AS total_ms,
  round(mean_exec_time::numeric, 3) AS mean_ms, shared_blks_read AS blks_read, left(regexp_replace(query, '\s+', ' ', 'g'), 90) AS query
  FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = 'ledgerpay')
  ORDER BY total_exec_time DESC LIMIT 15" > "$OUT/pg_stat_statements.txt"
$P exec -T api /app/ledgerpay ledger-verify | tee -a "$OUT/run.log" || fail=1
python3 - "$OUT/docker-stats.csv" <<'PY' | tee -a "$OUT/run.log"
import sys, collections
peak = collections.defaultdict(lambda: [0.0, ""])
for line in open(sys.argv[1]):
    parts = line.strip().split(",")
    if len(parts) != 4: continue
    _, name, cpu, mem = parts
    try: c = float(cpu.rstrip("%"))
    except ValueError: continue
    if c > peak[name][0]: peak[name] = [c, mem.split("/")[0].strip()]
for n, (c, m) in sorted(peak.items()): print(f"peak {n}: cpu {c:.0f}% (of {100}×cores), mem at peak {m}")
PY
[ $fail = 0 ] && log "PASS" || { log "FAIL"; exit 1; }
