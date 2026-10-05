package app

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"ledgerpay/db/migrations"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/metrics"

	"ledgerpay/internal/modules/inbox"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payment"
	"ledgerpay/internal/modules/payout"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/jobs"
	"ledgerpay/internal/platform/observability"
)

// Worker adalah proses latar LedgerPay (docs/design/20-background-jobs.md §4): satu loop klaim per
// LANE dengan pool goroutine sendiri (bulkhead). Merchant yang webhook-nya lambat tidak bisa menahan
// payout; sweeper tidak bisa menghabiskan slot job uang.
type Worker struct {
	Money       *jobs.Runner // payout.submit, payout.status_check
	WebhookOut  *jobs.Runner // webhook.deliver
	Maintenance *jobs.Runner // sweeper, verifikasi ledger, cleanup (+ scheduler)
	Inbox       *inbox.Processor
	deps        Deps
}

// WorkerConfig: ID unik per proses (hostname:pid:uuid), lease default 30 dtk.
type WorkerConfig struct {
	ID    string
	Lease time.Duration
}

// NewWorker merakit semua lane dari service yang sama dengan API.
func NewWorker(d Deps, cfg WorkerConfig) *Worker {
	sv := build(d)
	rc := func(lane string, n int) jobs.RunnerConfig {
		return jobs.RunnerConfig{Worker: cfg.ID + ":" + lane, Lease: cfg.Lease, Concurrency: n}
	}
	w := &Worker{deps: d,
		Money:      jobs.NewRunner(d.Pool, rc("money", 4), sv.payout.Kinds()...),
		WebhookOut: jobs.NewRunner(d.Pool, rc("webhook-out", 8), sv.webhooks.Kinds()...),
		Inbox:      inbox.NewProcessor(d.Pool, map[string]inbox.Applier{"charge": sv.topup}, inbox.ProcessorConfig{WorkerID: cfg.ID + ":inbox", Lease: cfg.Lease}),
	}
	quick := func(name string, fn func(ctx context.Context) error) jobs.Kind {
		return jobs.Kind{Name: name, MaxAttempts: 3, Timeout: 2 * time.Minute, Handler: func(ctx context.Context, _ *jobs.Job) error { return fn(ctx) }}
	}
	log := func(ctx context.Context, msg string, args ...any) { observability.Logger(ctx).Info(msg, args...) }
	w.Maintenance = jobs.NewRunner(d.Pool, rc("maintenance", 2),
		quick("topup.recover", func(ctx context.Context) error {
			n, err := sv.topup.Recover(ctx, d.Pool, 100)
			log(ctx, "topup.recover_done", "recovered", n)
			return err
		}),
		quick("topup.expire_sweep", func(ctx context.Context) error {
			expired, credited, err := sv.topup.ExpireSweep(ctx, d.Pool, 50)
			log(ctx, "topup.expire_sweep_done", "expired", expired, "credited", credited)
			return err
		}),
		quick("intent.expire_sweep", func(ctx context.Context) error {
			n, err := sv.payment.SweepExpired(ctx, d.Pool, 200)
			log(ctx, "intent.expire_sweep_done", "expired", n)
			return err
		}),
		jobs.Kind{Name: "ledger.verify", MaxAttempts: 2, Timeout: 10 * time.Minute, Heartbeat: true,
			Handler: func(ctx context.Context, _ *jobs.Job) error {
				rep, err := ledger.Verify(ctx, d.Pool)
				if err != nil {
					return err // gauge tidak diperbarui → alert IntegrityVerificationStale
				}
				drift, err := payment.RefundDrift(ctx, d.Pool) // LI-08
				if err != nil {
					return err
				}
				metrics.RefundDriftIntents.WithLabelValues().Set(float64(len(drift)))
				rep.Publish(float64(d.Clock.Now().Unix())) // gauge INVARIANT (alert A-01 kalau ≠ 0)
				if !rep.OK() || len(drift) > 0 {
					observability.Logger(ctx).Error("ledger.invariant_violated", "report", rep.String(), "refund_drift", drift)
				}
				return nil
			}},
		quick("maintenance.jobs_cleanup", func(ctx context.Context) error {
			n, err := jobs.Cleanup(ctx, d.Pool)
			log(ctx, "jobs.cleanup_done", "deleted", n)
			return err
		}),
		jobs.Kind{Name: "recon.daily", MaxAttempts: 5, Timeout: 15 * time.Minute, Heartbeat: true,
			Backoff: jobs.Schedule([]time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour}, 0.1),
			Handler: func(ctx context.Context, _ *jobs.Job) error {
				ran, err := sv.recon.Daily(ctx, d.Clock.Now()) // 02:30 WIB untuk D−1; catch-up tiap jam
				log(ctx, "recon.daily_done", "ran", ran)
				return err
			}},
		quick("maintenance.idempotency_cleanup", func(ctx context.Context) error {
			n, err := idempotency.Cleanup(ctx, d.Pool)
			log(ctx, "idempotency.cleanup_done", "deleted", n)
			return err
		}),
	)
	for kind, every := range map[string]time.Duration{ // katalog §3.1
		"topup.recover": time.Minute, "intent.expire_sweep": time.Minute, "topup.expire_sweep": 5 * time.Minute,
		"ledger.verify": time.Hour, "maintenance.jobs_cleanup": time.Hour, "maintenance.idempotency_cleanup": 24 * time.Hour,
		"recon.daily": time.Hour,
	} {
		w.Maintenance.Every(kind, every)
	}
	return w
}

// Stats mengisi gauge periodik (antrean job, payout UNKNOWN, inbox pending).
func (w *Worker) Stats(ctx context.Context) error {
	if err := jobs.Stats(ctx, w.deps.Pool); err != nil {
		return err
	}
	if err := payout.Stats(ctx, w.deps.Pool); err != nil {
		return err
	}
	if err := recon.Stats(ctx, w.deps.Pool); err != nil {
		return err
	}
	return inbox.Stats(ctx, w.deps.Pool)
}

// Ready: setiap lane mengulang loop klaim baru-baru ini (< 3× interval polling, minimal 5 dtk).
// Worker yang macet (deadlock goroutine, pool habis) menjadi not-ready walaupun prosesnya hidup.
func (w *Worker) Ready(now time.Time, poll time.Duration) map[string]bool {
	limit := max(3*poll, 5*time.Second)
	ok := func(t time.Time) bool { return !t.IsZero() && now.Sub(t) < limit }
	return map[string]bool{
		"money": ok(w.Money.LastLoop()), "webhook-out": ok(w.WebhookOut.LastLoop()),
		"maintenance": ok(w.Maintenance.LastLoop()), "inbox": ok(w.Inbox.LastLoop()),
	}
}

// Run menjalankan semua lane sampai ctx selesai. Saat shutdown: berhenti mengklaim, job in-flight
// diberi waktu menyelesaikan diri (deadline per kind), lalu kembali.
func (w *Worker) Run(ctx context.Context, poll time.Duration) {
	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	run(func() { w.Money.Run(ctx, poll) })
	run(func() { w.WebhookOut.Run(ctx, poll) })
	run(func() { w.Maintenance.Run(ctx, poll) })
	run(func() { w.Inbox.Run(ctx, poll) })
	run(func() { w.Maintenance.RunScheduler(ctx, w.deps.Clock.Now, 15*time.Second) })
	run(func() {
		for {
			if err := w.Stats(ctx); err != nil && ctx.Err() == nil {
				observability.Logger(ctx).Warn("worker.stats_failed", "err", err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
		}
	})
	wg.Wait()
}

// RunOnce menjalankan satu putaran setiap lane (test dan skrip).
func (w *Worker) RunOnce(ctx context.Context) error {
	for _, r := range []*jobs.Runner{w.Money, w.WebhookOut, w.Maintenance} {
		if _, err := r.RunOnce(ctx); err != nil {
			return err
		}
	}
	_, err := w.Inbox.RunOnce(ctx)
	return err
}

// FastForwardJobs membuat semua job queued langsung jatuh tempo (test: tanpa menunggu backoff 30 dtk).
func FastForwardJobs(ctx context.Context, d Deps) error {
	_, err := d.Pool.Exec(ctx, `UPDATE jobs SET run_at = now() - interval '1 second' WHERE status = 'queued'`)
	return err
}

// WorkerOpsRouter: /healthz (proses hidup), /readyz (DB + skema + heartbeat setiap lane), /metrics.
// Port internal; tidak diekspos Caddy.
func WorkerOpsRouter(w *Worker, poll time.Duration) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(rw, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(rw http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), time.Second)
		defer cancel()
		checks := map[string]string{}
		ready := true
		if v, err := migrations.SchemaVersion(ctx, w.deps.Pool); err != nil {
			checks["database"], ready = "unreachable", false
		} else if v < migrations.MinSchemaVersion {
			checks["schema"], ready = "too old", false
		}
		for lane, ok := range w.Ready(time.Now(), poll) { //nolint:forbidigo // heartbeat teknis
			if !ok {
				checks["lane:"+lane], ready = "stalled", false
			}
		}
		status, state := http.StatusOK, "ready"
		if !ready {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		metrics.Ready.WithLabelValues("worker").Set(boolFloat(ready))
		httpx.WriteJSON(rw, status, map[string]any{"status": state, "checks": checks})
	})
	r.Handle("/metrics", metrics.Handler())
	return r
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
