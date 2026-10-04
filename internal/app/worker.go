package app

import (
	"context"
	"sync"
	"time"

	"ledgerpay/internal/modules/inbox"
	"ledgerpay/internal/modules/ledger"
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
					return err
				}
				if !rep.OK() { // metrik invariant + alert menyusul di Sprint 7
					observability.Logger(ctx).Error("ledger.invariant_violated", "report", rep.String())
				}
				return nil
			}},
		quick("maintenance.jobs_cleanup", func(ctx context.Context) error {
			n, err := jobs.Cleanup(ctx, d.Pool)
			log(ctx, "jobs.cleanup_done", "deleted", n)
			return err
		}),
		quick("maintenance.idempotency_cleanup", func(ctx context.Context) error {
			n, err := idempotency.Cleanup(ctx, d.Pool)
			log(ctx, "idempotency.cleanup_done", "deleted", n)
			return err
		}),
	)
	for kind, every := range map[string]time.Duration{ // katalog §3.1
		"topup.recover": time.Minute, "intent.expire_sweep": time.Minute, "topup.expire_sweep": 5 * time.Minute,
		"ledger.verify": time.Hour, "maintenance.jobs_cleanup": time.Hour, "maintenance.idempotency_cleanup": 24 * time.Hour,
	} {
		w.Maintenance.Every(kind, every)
	}
	return w
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
