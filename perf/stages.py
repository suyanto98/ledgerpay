#!/usr/bin/env python3
"""E-1: per tahap LP-4, laju confirm yang tercapai, p95 confirm (k6), dan p95 lock wait wallet di
PostJournal (purpose=PAYMENT) dari selisih bucket histogram ledgerpay_ledger_lock_wait_seconds.

    perf/stages.py <out-dir> <stage-seconds>
"""
import json
import re
import sys

out, stage = sys.argv[1], int(sys.argv[2])
rates = [25, 50, 100, 200, 400]
summary = json.load(open(f"{out}/k6-summary.json"))["metrics"]


def buckets(i):
    b = {}
    try:
        text = open(f"{out}/metrics-{i}.txt").read()
    except FileNotFoundError:
        return None
    # Urutan label tidak dijamin (client Go menaruh "le" terakhir): parse label sebagai map.
    for m in re.finditer(r'ledgerpay_ledger_lock_wait_seconds_bucket\{([^}]*)\} (\S+)', text):
        labels = dict(re.findall(r'(\w+)="([^"]*)"', m.group(1)))
        if labels.get("purpose") != "PAYMENT":
            continue
        le = labels["le"]
        b[float("inf") if le == "+Inf" else float(le)] = float(m.group(2))
    return b


def p95(lo, hi):
    if lo is None or hi is None or not hi:
        return None
    d = {le: hi[le] - lo.get(le, 0) for le in hi}
    total = d.get(float("inf"), 0)
    if total == 0:
        return None
    for le in sorted(d):
        if d[le] >= 0.95 * total:
            return le
    return None


cpu = {}  # (container, stage) → [cpu%...]
try:
    t0 = int(open(f"{out}/stage-start").read())
    for line in open(f"{out}/docker-stats.csv"):
        p = line.strip().split(",")
        if len(p) != 4:
            continue
        st = (int(p[0]) - t0) // stage
        if 0 <= st < len(rates):
            cpu.setdefault((p[1].replace("ledgerpay-perf-", "").replace("-1", ""), st), []).append(float(p[2].rstrip("%")))
except (FileNotFoundError, ValueError):
    pass
avg = lambda c, i: (f"{sum(cpu[(c, i)]) / len(cpu[(c, i)]):.0f}%" if cpu.get((c, i)) else "n/a")

print("stage | target/s | confirms 200 | achieved/s | p95 confirm ms | p95 lock wait (bucket ≤) | avg CPU api / postgres (100% = 1 thread)")
for i, r in enumerate(rates):
    n = summary.get(f"http_reqs{{name:confirm,stage:{i},status:200}}", {}).get("count", 0)
    dur = summary.get(f"http_req_duration{{name:confirm,stage:{i}}}", {})
    lw = p95(buckets(i), buckets(i + 1))
    lw_s = "n/a" if lw is None else ("> 2.5 s" if lw == float("inf") else f"{lw * 1000:g} ms")
    print(f"{i} | {r} | {n} | {n / stage:.1f} | {dur.get('p(95)', 0):.1f} | {lw_s} | {avg('api', i)} / {avg('postgres', i)}")
