#!/usr/bin/env python3
"""Menghasilkan JSON dashboard Grafana (deploy/grafana/dashboards/*.json).

Dashboard adalah kode: diubah di sini, di-review di PR, di-provision otomatis. Tidak ada dashboard
yang dibuat lewat klik di UI (docs/design/21-observability.md §6). Jalankan: make dashboards
"""
import json, pathlib

DS = {"type": "prometheus", "uid": "prom"}
OUT = pathlib.Path(__file__).parent / "dashboards"


def panel(pid, title, exprs, x, y, w=12, h=8, kind="timeseries", unit=None, thresholds=None, desc=""):
    p = {
        "id": pid, "type": kind, "title": title, "description": desc, "datasource": DS,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": [{"refId": chr(65 + i), "datasource": DS, "expr": e, "legendFormat": l} for i, (e, l) in enumerate(exprs)],
        "fieldConfig": {"defaults": {}, "overrides": []},
        "options": {},
    }
    if unit:
        p["fieldConfig"]["defaults"]["unit"] = unit
    if thresholds:  # stat INV: hijau di 0, merah di atas 0
        p["fieldConfig"]["defaults"]["thresholds"] = {"mode": "absolute", "steps": thresholds}
        p["fieldConfig"]["defaults"]["color"] = {"mode": "thresholds"}
        p["options"] = {"colorMode": "background", "graphMode": "none", "reduceOptions": {"calcs": ["lastNotNull"]}}
    return p


INV = [{"color": "green", "value": None}, {"color": "red", "value": 1}]


def dashboard(uid, title, panels, desc):
    return {"uid": uid, "title": title, "description": desc, "tags": ["ledgerpay"], "timezone": "browser",
            "schemaVersion": 39, "version": 1, "refresh": "10s", "time": {"from": "now-1h", "to": "now"},
            "panels": panels}


money = dashboard("ledgerpay-money", "LedgerPay — Money & Integrity", [
    panel(1, "INV: balance drift accounts", [("max(ledgerpay_ledger_balance_drift_accounts)", "drift")], 0, 0, 6, 5, "stat", thresholds=INV,
          desc="Akun dengan balance != Σ entries pada verifikasi terakhir. Normal SELALU 0. Runbook §8.1"),
    panel(2, "INV: unbalanced journals", [("max(ledgerpay_ledger_unbalanced_journals)", "unbalanced")], 6, 0, 6, 5, "stat", thresholds=INV),
    panel(3, "INV: global imbalance (minor)", [("max(abs(ledgerpay_ledger_global_imbalance_minor))", "imbalance")], 12, 0, 6, 5, "stat", thresholds=INV),
    panel(4, "Last ledger verification (age)", [("time() - max(ledgerpay_ledger_verification_last_success_timestamp_seconds)", "age")], 18, 0, 6, 5, "stat", unit="s",
          thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 7200}]),
    panel(5, "Transfers by outcome (rate/min)", [("sum by (outcome) (rate(ledgerpay_transfers_total[5m])) * 60", "{{outcome}}")], 0, 5),
    panel(6, "Payments by outcome (rate/min)", [("sum by (outcome) (rate(ledgerpay_payments_total[5m])) * 60", "{{outcome}}")], 12, 5),
    panel(7, "Payouts UNKNOWN now + oldest age", [("max(ledgerpay_payout_unknown)", "unknown now"),
                                                  ("max(ledgerpay_payout_unknown_oldest_age_seconds) / 60", "oldest (min)")], 0, 13,
          desc="Demo C: naik ke 1 saat bank tidak menjawab, turun ke 0 setelah status query. Alert jika > 1 jam."),
    panel(8, "Payout transitions (increase 15m)", [("sum by (status) (increase(ledgerpay_payouts_total[15m]))", "{{status}}")], 12, 13),
    panel(9, "Top-ups by status (increase 15m)", [("sum by (status) (increase(ledgerpay_topups_total[15m]))", "{{status}}")], 0, 21),
    panel(10, "Idempotency replays / key reused (rate/min)", [("sum by (route) (rate(app_idempotency_replays_total[5m])) * 60", "replay {{route}}"),
                                                             ("sum by (route) (rate(app_idempotency_key_reused_total[5m])) * 60", "reused {{route}}")], 12, 21),
    panel(11, "Inbound webhooks (increase 15m)", [("sum by (outcome) (increase(ledgerpay_webhook_inbox_received_total[15m]))", "received {{outcome}}"),
                                                  ("sum by (outcome) (increase(ledgerpay_webhook_inbox_processed_total[15m]))", "processed {{outcome}}")], 0, 29),
    panel(12, "Outbound merchant webhooks (increase 15m)", [("sum by (outcome) (increase(ledgerpay_webhook_deliveries_total[15m]))", "{{outcome}}")], 12, 29),
], "Apakah uang bergerak normal, apakah ledger masih benar, dan apa yang menggantung?")

integrity = dashboard("ledgerpay-integrity", "LedgerPay — Integrity & Recon", [
    panel(1, "INV: balance drift accounts", [("max(ledgerpay_ledger_balance_drift_accounts)", "drift")], 0, 0, 6, 5, "stat", thresholds=INV,
          desc="LI-03. Runbook: docs/runbook.md#ledgerinvariantviolated"),
    panel(2, "INV: unbalanced journals", [("max(ledgerpay_ledger_unbalanced_journals)", "unbalanced")], 6, 0, 6, 5, "stat", thresholds=INV, desc="LI-01"),
    panel(3, "INV: refund drift intents", [("max(ledgerpay_payment_refund_drift_intents)", "refund drift")], 12, 0, 6, 5, "stat", thresholds=INV,
          desc="LI-08: refunded_amount != Σ refunds"),
    panel(4, "INV: global imbalance (minor)", [("max(abs(ledgerpay_ledger_global_imbalance_minor))", "imbalance")], 18, 0, 6, 5, "stat", thresholds=INV),
    panel(5, "Ledger verification age", [("time() - max(ledgerpay_ledger_verification_last_success_timestamp_seconds)", "age")], 0, 5, 6, 5, "stat", unit="s",
          thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 7200}], desc="Gauge 0 yang basi = buta, bukan sehat"),
    panel(6, "Recon: open items", [("max(ledgerpay_recon_open_items)", "open")], 6, 5, 6, 5, "stat",
          thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 1}], desc="Antrean investigasi ops (STATUS/AMOUNT_MISMATCH, MISSING_*)"),
    panel(7, "Recon: last completed run (age)", [("time() - max(ledgerpay_recon_last_completed_timestamp_seconds)", "age")], 12, 5, 6, 5, "stat", unit="s",
          thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 108000}], desc="Harian 02:30 WIB untuk D−1; alert ReconStale > 30 jam"),
    panel(8, "Backup: last success (age)", [("time() - max(app_backup_last_success_timestamp_seconds)", "age")], 18, 5, 6, 5, "stat", unit="s",
          thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 93600}]),
    panel(9, "Recon runs by result (increase 1d)", [("sum by (status) (increase(ledgerpay_recon_runs_total[1d]))", "{{status}}")], 0, 10),
    panel(10, "Wallet lock wait p95 by purpose", [("histogram_quantile(0.95, sum by (le, purpose) (rate(ledgerpay_ledger_lock_wait_seconds_bucket[5m])))", "{{purpose}}")], 12, 10, unit="s",
          desc="E-1: purpose=PAYMENT naik tajam saat satu merchant mendekati ±120 pembayaran/dtk (docs/performance.md)"),
    panel(11, "Payouts UNKNOWN now + oldest age", [("max(ledgerpay_payout_unknown)", "unknown now"),
                                                   ("max(ledgerpay_payout_unknown_oldest_age_seconds) / 60", "oldest (min)")], 0, 18),
    panel(12, "Backup size", [("max by (db) (app_backup_size_bytes)", "{{db}}")], 12, 18, unit="bytes"),
], "Apakah ledger masih benar, apakah catatan kita cocok dengan provider, dan apakah backup masih jalan?")

platform = dashboard("ledgerpay-platform", "Platform — HTTP, DB & Jobs", [
    panel(1, "Requests per route (rps)", [("sum by (route, method) (rate(app_http_requests_total[5m]))", "{{method}} {{route}}")], 0, 0),
    panel(2, "5xx ratio", [("sum(rate(app_http_requests_total{status=~\"5..\"}[5m])) / sum(rate(app_http_requests_total[5m]))", "5xx ratio")], 12, 0, unit="percentunit"),
    panel(3, "p95 latency per route", [("histogram_quantile(0.95, sum by (le, route) (rate(app_http_request_duration_seconds_bucket[5m])))", "{{route}}")], 0, 8, unit="s",
          desc="Garis SLO: POST /v1/transfers p95 < 200 ms."),
    panel(4, "Errors by code (rate/min)", [("topk(8, sum by (code) (rate(app_errors_total[5m]))) * 60", "{{code}}")], 12, 8),
    panel(5, "DB pool: acquired vs max", [("max by (service) (app_db_pool_acquired_conns)", "acquired {{service}}"),
                                         ("max by (service) (app_db_pool_max_conns)", "max {{service}}")], 0, 16),
    panel(6, "Deadlocks / tx retries / lock timeouts", [("sum(increase(app_db_deadlocks_total[15m]))", "deadlocks"),
                                                       ("sum by (reason) (increase(app_db_tx_retries_total[15m]))", "retry {{reason}}"),
                                                       ("sum by (kind) (increase(app_db_lock_timeouts_total[15m]))", "lock timeout {{kind}}")], 12, 16),
    panel(7, "Job queue depth by kind/status", [("sum by (kind, status) (app_job_queue_depth)", "{{kind}} {{status}}")], 0, 24),
    panel(8, "Outbox lag + oldest due job", [("max(app_outbox_lag_seconds)", "outbox lag"), ("max by (kind) (app_job_oldest_age_seconds)", "oldest {{kind}}")], 12, 24, unit="s"),
    panel(9, "Job outcomes (increase 15m)", [("sum by (kind, outcome) (increase(app_job_duration_seconds_count[15m]))", "{{kind}} {{outcome}}")], 0, 32,
          desc="outcome=fenced: worker zombie yang transaksinya di-rollback oleh fencing."),
    panel(10, "Dead letters (increase 1h)", [("sum by (kind) (increase(app_job_dead_total[1h]))", "{{kind}}"),
                                            ("sum(increase(ledgerpay_webhook_inbox_dead_total[1h]))", "inbox")], 12, 32),
    panel(11, "Outbound calls by target/outcome (rate/min)", [("sum by (target, outcome) (rate(app_outbound_requests_total[5m])) * 60", "{{target}} {{outcome}}")], 0, 40),
    panel(12, "Readiness", [("min by (service) (app_ready)", "{{service}}")], 12, 40),
], "Lambat atau error di mana: aplikasi, pool, query, atau job? Ada worker macet atau zombie?")

OUT.mkdir(exist_ok=True)
for d in (money, integrity, platform):
    (OUT / f"{d['uid']}.json").write_text(json.dumps(d, indent=2, ensure_ascii=False) + "\n")
    print("wrote", OUT / f"{d['uid']}.json")
