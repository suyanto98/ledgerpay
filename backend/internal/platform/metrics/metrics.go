// Package metrics: satu tempat untuk semua metrik Prometheus (katalog docs/design/21-observability.md §3).
//
// Aturan:
//   - event → counter (_total); keadaan hasil verifikasi/query periodik → gauge;
//   - label hanya dari himpunan TERBATAS (route template, kind job, code katalog error, outcome) —
//     tidak pernah user_id, merchant_id, email, atau nominal (kardinalitas + PII);
//   - metrik INVARIANT (ledgerpay_ledger_*) normalnya selalu 0; alert-nya critical.
package metrics

import (
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry sendiri (bukan default global), supaya test bisa membaca nilai tanpa efek samping library lain.
var Registry = prometheus.NewRegistry()

var (
	httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2.5, 5} // 0.2/0.3 = batas SLO
	jobBuckets  = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}
)

func counter(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	Registry.MustRegister(c)
	return c
}

func gauge(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	Registry.MustRegister(g)
	return g
}

func histogram(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	Registry.MustRegister(h)
	return h
}

// ---------------------------------------------------------------- platform (app_)
var (
	BuildInfo        = gauge("app_build_info", "Running version (always 1).", "service", "version", "go_version")
	Ready            = gauge("app_ready", "1 = ready to serve, 0 = not ready.", "service")
	HTTPRequests     = counter("app_http_requests_total", "Completed HTTP requests.", "route", "method", "status")
	HTTPDuration     = histogram("app_http_request_duration_seconds", "Server-side HTTP latency.", httpBuckets, "route", "method")
	HTTPInFlight     = gauge("app_http_in_flight_requests", "Requests being served.")
	Errors           = counter("app_errors_total", "Error responses by catalogue code.", "code")
	RateLimited      = counter("app_rate_limited_total", "429 responses by tier.", "tier")
	DBTxRetries      = counter("app_db_tx_retries_total", "WithTx retries.", "reason")
	DBDeadlocks      = counter("app_db_deadlocks_total", "40P01 detected (before retry).")
	DBLockTimeouts   = counter("app_db_lock_timeouts_total", "55P03 lock timeouts.", "kind")
	JobsEnqueued     = counter("app_job_enqueued_total", "Jobs written to the outbox.", "kind")
	JobDuration      = histogram("app_job_duration_seconds", "Job execution time.", jobBuckets, "kind", "outcome")
	JobFailures      = counter("app_job_failures_total", "Failed job attempts.", "kind", "class")
	JobDead          = counter("app_job_dead_total", "Jobs moved to dead (DLQ).", "kind")
	JobFenced        = counter("app_job_fenced_total", "CompleteInTx found the lease taken: business tx rolled back (zombie worker).", "kind")
	JobLeaseLost     = counter("app_job_lease_lost_total", "Heartbeat found the lease taken.", "kind")
	JobQueueDepth    = gauge("app_job_queue_depth", "Jobs per kind and status (periodic query).", "kind", "status")
	JobOldestAge     = gauge("app_job_oldest_age_seconds", "Age of the oldest due queued job per kind.", "kind")
	OutboxLag        = gauge("app_outbox_lag_seconds", "Age of the oldest never-claimed due job: how long side effects are delayed after commit.")
	Outbound         = counter("app_outbound_requests_total", "Outbound HTTP calls.", "target", "outcome")
	OutboundDuration = histogram("app_outbound_request_duration_seconds", "Outbound HTTP latency.", httpBuckets, "target")
	AuthnFailures    = counter("app_authn_failures_total", "Authentication failures.", "reason")
	RefreshReuse     = counter("app_refresh_reuse_detected_total", "Refresh token reuse detected (family revoked).")
	IdemReplays      = counter("app_idempotency_replays_total", "Replayed responses (Idempotent-Replayed).", "route")
	IdemKeyReused    = counter("app_idempotency_key_reused_total", "422 IDEMPOTENCY_KEY_REUSED.", "route")
)

// ---------------------------------------------------------------- LedgerPay (ledgerpay_)
var (
	// INVARIANT: hasil job ledger.verify; normal = 0.
	LedgerDriftAccounts   = gauge("ledgerpay_ledger_balance_drift_accounts", "INV: accounts whose balance != sum of entries (last verification).")
	LedgerUnbalanced      = gauge("ledgerpay_ledger_unbalanced_journals", "INV: journals with sum(D) != sum(C) (last verification).")
	LedgerGlobalImbalance = gauge("ledgerpay_ledger_global_imbalance_minor", "INV: total debits - total credits in minor units.")
	LedgerVerifiedAt      = gauge("ledgerpay_ledger_verification_last_success_timestamp_seconds", "Unix time of the last successful ledger verification.")
	Transfers             = counter("ledgerpay_transfers_total", "Transfer results (first execution, not replays).", "outcome")
	Payments              = counter("ledgerpay_payments_total", "Payment confirmation results.", "outcome")
	Refunds               = counter("ledgerpay_refunds_total", "Refund results.", "outcome")
	Topups                = counter("ledgerpay_topups_total", "Top-up transitions.", "status")
	Payouts               = counter("ledgerpay_payouts_total", "Payout transitions.", "status")
	RefundDriftIntents    = gauge("ledgerpay_payment_refund_drift_intents", "Payment intents whose refunded_amount differs from the sum of refunds (LI-08); normally 0.")
	// Lama menunggu lock baris wallet di PostJournal (query LockWalletAccounts ≈ waktu tunggu lock +
	// eksekusi sub-ms). Dipakai eksperimen E-1 (hot merchant): purpose=PAYMENT memperlihatkan antrean
	// di baris wallet merchant (docs/design/23-performance-plan.md §6).
	LedgerLockWait = histogram("ledgerpay_ledger_lock_wait_seconds", "Time PostJournal spends acquiring wallet row locks.",
		[]float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}, "purpose")
	ReconRuns              = counter("ledgerpay_recon_runs_total", "Reconciliation runs by result.", "status")
	ReconLastCompleted     = gauge("ledgerpay_recon_last_completed_timestamp_seconds", "Finish time of the latest COMPLETED reconciliation run (0 = never).")
	ReconOpenItems         = gauge("ledgerpay_recon_open_items", "Reconciliation items waiting for ops (OPEN).")
	PayoutUnknown          = gauge("ledgerpay_payout_unknown", "Payouts currently UNKNOWN.")
	PayoutUnknownOldest    = gauge("ledgerpay_payout_unknown_oldest_age_seconds", "Age of the oldest UNKNOWN payout.")
	PayoutStatusChecks     = counter("ledgerpay_payout_status_checks_total", "Bank status query results.", "result")
	PayoutMismatch         = counter("ledgerpay_payout_provider_mismatch_total", "Bank success responses that did not match the local record.")
	InboxReceived          = counter("ledgerpay_webhook_inbox_received_total", "Inbound webhook endpoint results.", "outcome")
	WebhookSigFailures     = counter("ledgerpay_webhook_signature_failures_total", "Inbound webhooks with an invalid signature.")
	InboxProcessed         = counter("ledgerpay_webhook_inbox_processed_total", "Inbox processing results.", "outcome")
	InboxPending           = gauge("ledgerpay_webhook_inbox_pending", "Inbox events RECEIVED and not yet processed.")
	InboxDead              = counter("ledgerpay_webhook_inbox_dead_total", "Inbox events moved to DEAD.")
	WebhookLate            = counter("ledgerpay_webhook_late_total", "Credits from a success webhook after the top-up was EXPIRED.")
	WebhookDeliveries      = counter("ledgerpay_webhook_deliveries_total", "Outbound merchant webhook attempts.", "outcome")
	WebhookDeliveryDead    = counter("ledgerpay_webhook_delivery_dead_total", "Outbound deliveries moved to DEAD.")
	WebhookDeliveryBlocked = counter("ledgerpay_webhook_delivery_blocked_total", "Deliveries refused by the SSRF egress guard.")
)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	// Counter INV/alert diinisialisasi 0 untuk label yang diketahui: increase() pada counter yang
	// baru muncul memberi false negative di sampel pertama (21-observability.md §7 catatan).
	for _, k := range []string{"payout.submit", "payout.status_check", "webhook.deliver"} {
		JobDead.WithLabelValues(k)
	}
	WebhookSigFailures.WithLabelValues()
	InboxDead.WithLabelValues()
}

// SetBuildInfo dipanggil sekali saat start.
func SetBuildInfo(service, version string) {
	BuildInfo.WithLabelValues(service, version, runtime.Version()).Set(1)
}

// Handler melayani /metrics (port ops internal, tidak lewat Caddy).
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// Since mengembalikan detik sejak t (helper histogram).
func Since(t time.Time) float64 { return time.Since(t).Seconds() } //nolint:forbidigo // durasi teknis, bukan waktu bisnis

// Start menandai awal pengukuran durasi teknis (pasangan Since); bukan waktu bisnis (C-31).
func Start() time.Time { return time.Now() } //nolint:forbidigo // durasi teknis, bukan waktu bisnis
