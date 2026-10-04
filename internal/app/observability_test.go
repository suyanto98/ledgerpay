package app_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"ledgerpay/internal/app"
	"ledgerpay/internal/platform/metrics"
)

// Metrik HTTP memakai ROUTE TEMPLATE, bukan path konkret: satu seri untuk semua transfer, bukan satu
// seri per ID (kardinalitas). Replay idempotency dan hasil transfer ikut tercatat.
func TestMetrics_HTTPRouteTemplateAndBusinessCounters(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	replaysBefore := testutil.ToFloat64(metrics.IdemReplays.WithLabelValues("POST /v1/transfers"))
	completedBefore := testutil.ToFloat64(metrics.Transfers.WithLabelValues("completed"))
	getBefore := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("/v1/transfers/{id}", "GET", "200"))

	key := newKey()
	_, tr := e.transfer(t, rina, key, transferBody("andi@example.com", "1000"))
	e.transfer(t, rina, key, transferBody("andi@example.com", "1000")) // replay
	e.do(t, http.MethodGet, "/api/v1/transfers/"+tr["id"].(string), rina, nil)

	if d := testutil.ToFloat64(metrics.IdemReplays.WithLabelValues("POST /v1/transfers")) - replaysBefore; d != 1 {
		t.Fatalf("idempotency replays +%v, want +1", d)
	}
	if d := testutil.ToFloat64(metrics.Transfers.WithLabelValues("completed")) - completedBefore; d != 1 {
		t.Fatalf("a replay must not count as a second completed transfer: +%v", d)
	}
	if d := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("/v1/transfers/{id}", "GET", "200")) - getBefore; d != 1 {
		t.Fatalf("GET must be recorded under the route template, +%v", d)
	}
}

// ledger.verify mengisi gauge INVARIANT (0 saat sehat) + timestamp sukses (untuk alert "verifikasi basi").
func TestMetrics_LedgerVerifyPublishesInvariantGauges(t *testing.T) {
	e := newEnv(t)
	_, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 100_000)
	metrics.LedgerDriftAccounts.WithLabelValues().Set(99) // nilai basi dari "verifikasi sebelumnya"
	w := e.newWorker(t)
	if err := w.Maintenance.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.drain(t, w, 10)
	if v := testutil.ToFloat64(metrics.LedgerDriftAccounts.WithLabelValues()); v != 0 {
		t.Fatalf("drift gauge = %v, want 0 after a clean verification", v)
	}
	if v := testutil.ToFloat64(metrics.LedgerVerifiedAt.WithLabelValues()); v != float64(e.clk.Now().Unix()) {
		t.Fatalf("verification timestamp = %v", v)
	}

	// Drift buatan (UPDATE saldo langsung, melewati ledger) HARUS terlihat di gauge.
	if _, err := e.pool.Exec(context.Background(), `UPDATE accounts SET balance = balance + 1 WHERE user_id = $1`, rinaID); err != nil {
		t.Fatal(err)
	}
	_, _ = e.pool.Exec(context.Background(), `DELETE FROM jobs WHERE kind = 'ledger.verify'`)
	if err := w.Maintenance.Tick(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.drain(t, w, 10)
	if v := testutil.ToFloat64(metrics.LedgerDriftAccounts.WithLabelValues()); v != 1 {
		t.Fatalf("injected drift must show up in the INV gauge, got %v", v)
	}
}

// Gauge payout UNKNOWN naik saat payout menggantung, turun saat terselesaikan (panel Demo C).
func TestMetrics_PayoutUnknownGaugeRisesAndFalls(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	e.createPayout(t, rina, "2000000", "9900000408") // timeout_after_process
	w := e.newWorker(t)
	_ = w.RunOnce(context.Background())
	_ = w.Stats(context.Background())
	if v := testutil.ToFloat64(metrics.PayoutUnknown.WithLabelValues()); v != 1 {
		t.Fatalf("payout_unknown = %v, want 1 while the outcome is unknown", v)
	}
	e.drain(t, w, 5)
	_ = w.Stats(context.Background())
	if v := testutil.ToFloat64(metrics.PayoutUnknown.WithLabelValues()); v != 0 {
		t.Fatalf("payout_unknown = %v, want 0 after the status query resolved it", v)
	}
}

// /metrics di port ops worker + readiness berbasis heartbeat lane.
func TestWorkerOps_MetricsAndLaneReadiness(t *testing.T) {
	e := newEnv(t)
	w := e.newWorker(t)
	ops := app.WorkerOpsRouter(w, 500*time.Millisecond)
	get := func(path string) (int, string) {
		rec := newRecorder()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		ops.ServeHTTP(rec, req)
		return rec.code, rec.body.String()
	}
	if code, body := get("/readyz"); code != http.StatusServiceUnavailable || !strings.Contains(body, "stalled") {
		t.Fatalf("lanes that never looped must be not-ready, got %d %s", code, body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx, 50*time.Millisecond)
	waitFor(t, "lanes to loop", func() bool { c, _ := get("/readyz"); return c == http.StatusOK })
	cancel()
	if code, body := get("/metrics"); code != http.StatusOK || !strings.Contains(body, "app_job_queue_depth") && !strings.Contains(body, "ledgerpay_payout_unknown") {
		t.Fatalf("/metrics: %d (missing core metrics)", code)
	}
}

type recorder struct {
	code int
	body strings.Builder
	h    http.Header
}

func newRecorder() *recorder                            { return &recorder{code: http.StatusOK, h: http.Header{}} }
func (r *recorder) Header() http.Header                 { return r.h }
func (r *recorder) WriteHeader(c int)                   { r.code = c }
func (r *recorder) Write(b []byte) (int, error)         { return r.body.Write(b) }
func (r *recorder) ReadFrom(x io.Reader) (int64, error) { return io.Copy(&r.body, x) }
