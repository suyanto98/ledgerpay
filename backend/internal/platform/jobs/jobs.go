// Package jobs: antrean kerja berbasis Postgres + outbox (ADR-006, docs/design/20-background-jobs.md).
//
//	Enqueue(tx, …)      di DALAM transaksi bisnis → niat ikut commit/rollback bersama efeknya
//	Runner              klaim FOR UPDATE SKIP LOCKED + lease; heartbeat untuk job panjang;
//	                    retry dengan backoff + jitter; dead (DLQ) + OnDead di tx yang sama
//	CompleteInTx(tx, j) FENCING: menandai selesai di tx bisnis yang sama; 0 baris → ErrLeaseLost →
//	                    seluruh tx bisnis rollback (worker zombie tidak bisa menulis)
//	Scheduler           tanpa leader election: dedupe key per bucket waktu
//
// Delivery at-least-once: setiap handler WAJIB idempoten (guard status, unique journal, key ke provider).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/jobs/store"
	"ledgerpay/internal/platform/metrics"
	"ledgerpay/internal/platform/tracing"
)

// ErrLeaseLost: lease job ini sudah diambil worker lain. Kerja bisnis di tx yang sama harus rollback.
var ErrLeaseLost = errors.New("jobs: lease lost (another worker owns this job now)")

type permanentError struct{ err error }

func (p permanentError) Error() string { return "permanent: " + p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent menandai error yang tidak akan sembuh dengan retry (payload rusak, referensi tidak ada):
// job langsung dead, tidak membuang 5–7 percobaan.
func Permanent(err error) error { return permanentError{err} }

// IsPermanent melaporkan apakah err (atau penyebabnya) ditandai Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Job adalah job yang sedang dikerjakan worker ini.
type Job struct {
	ID            uuid.UUID
	Kind          string
	Payload       []byte
	Attempts      int
	MaxAttempts   int
	CorrelationID string
	TraceParent   string

	worker    string
	completed bool
}

// Decode membaca payload. Payload rusak = Permanent (retry tidak akan memperbaikinya).
func (j *Job) Decode(v any) error {
	if err := json.Unmarshal(j.Payload, v); err != nil {
		return Permanent(fmt.Errorf("decode payload: %w", err))
	}
	return nil
}

// Spec adalah job yang akan di-enqueue.
type Spec struct {
	Kind          string
	Payload       any
	Dedupe        string        // dedupe_key; kosong = tanpa dedupe
	Delay         time.Duration // run_at = now() DB + Delay
	MaxAttempts   int           // 0 = default kind (dari registry pemanggil) atau 10
	CorrelationID string
}

// Enqueue menulis job di q (biasanya tx bisnis). Mengembalikan false kalau ditolak dedupe.
func Enqueue(ctx context.Context, q db.DBTX, s Spec) (bool, error) {
	payload, err := json.Marshal(s.Payload)
	if err != nil {
		return false, err
	}
	if s.Payload == nil {
		payload = []byte("{}")
	}
	maxAttempts := s.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	n, err := store.New(q).EnqueueJob(ctx, store.EnqueueJobParams{
		ID: uuid.Must(uuid.NewV7()), Kind: s.Kind, Payload: payload, MaxAttempts: int32(maxAttempts), //nolint:gosec // G115: max_attempts kecil
		DelaySecs: s.Delay.Seconds(), DedupeKey: nonEmpty(s.Dedupe), CorrelationID: nonEmpty(s.CorrelationID),
		TraceParent: nonEmpty(tracing.TraceParent(ctx)), // ditulis di tx bisnis yang sama dari span aktif
	})
	if n == 1 {
		metrics.JobsEnqueued.WithLabelValues(s.Kind).Inc()
	}
	return n == 1, db.MapError(err)
}

// CompleteInTx menandai job selesai di tx bisnis pemanggil (fencing). Kembalian ErrLeaseLost harus
// dikembalikan apa adanya oleh handler, supaya tx bisnis rollback.
func CompleteInTx(ctx context.Context, tx pgx.Tx, j *Job) error {
	n, err := store.New(tx).CompleteJob(ctx, store.CompleteJobParams{ID: j.ID, Worker: &j.worker})
	if err != nil {
		return db.MapError(err)
	}
	if n == 0 {
		return ErrLeaseLost
	}
	j.completed = true
	return nil
}

// DefaultBackoff (ADR-006): min(2s · 2^(attempt−1), 10m) × jitter [0.8, 1.2]. Jitter mencegah
// thundering herd ketika banyak job gagal bersamaan (contoh: provider down).
func DefaultBackoff(attempt int) time.Duration {
	d := 2 * time.Second << max(attempt-1, 0)
	if d <= 0 || d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return Jitter(d, 0.2)
}

// Schedule mengembalikan backoff dengan jadwal tetap (contoh webhook keluar: 1m, 5m, 30m, …).
func Schedule(delays []time.Duration, jitter float64) func(int) time.Duration {
	return func(attempt int) time.Duration {
		i := min(max(attempt-1, 0), len(delays)-1)
		return Jitter(delays[i], jitter)
	}
}

// Jitter mengalikan d dengan faktor acak [1−f, 1+f].
func Jitter(d time.Duration, f float64) time.Duration {
	return time.Duration(float64(d) * (1 - f + 2*f*rand.Float64())) //nolint:gosec // G404: jitter, bukan kriptografi
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Stats mengisi gauge antrean (dipanggil loop periodik worker).
func Stats(ctx context.Context, q db.DBTX) error {
	rows, err := store.New(q).JobStats(ctx)
	if err != nil {
		return db.MapError(err)
	}
	metrics.JobQueueDepth.Reset()
	metrics.JobOldestAge.Reset()
	var lag float64
	for _, r := range rows {
		metrics.JobQueueDepth.WithLabelValues(r.Kind, r.Status).Set(float64(r.N))
		if r.Status == "queued" {
			metrics.JobOldestAge.WithLabelValues(r.Kind).Set(r.OldestDueSecs)
			lag = max(lag, r.LagSecs)
		}
	}
	metrics.OutboxLag.WithLabelValues().Set(lag)
	return nil
}

// Summary adalah ringkasan job untuk ops console: TANPA payload (bisa memuat data sensitif).
type Summary struct {
	ID            uuid.UUID
	Kind          string
	Status        string
	Attempts      int32
	MaxAttempts   int32
	LastError     *string
	RunAt         time.Time
	CorrelationID *string
	CreatedAt     time.Time
}

// List mengembalikan job per status (terbaru dulu), dengan keyset cursor (createdAt, id).
func List(ctx context.Context, q db.DBTX, status string, kind *string, afterAt *time.Time, afterID *uuid.UUID, limit int32) ([]Summary, error) {
	rows, err := store.New(q).ListJobs(ctx, store.ListJobsParams{Status: status, Kind: kind, CursorAt: afterAt, CursorID: afterID, Lim: limit})
	if err != nil {
		return nil, db.MapError(err)
	}
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, Summary{ID: r.ID, Kind: r.Kind, Status: r.Status, Attempts: r.Attempts, MaxAttempts: r.MaxAttempts,
			LastError: r.LastError, RunAt: r.RunAt, CorrelationID: r.CorrelationID, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// RetryDead mengembalikan job dead ke antrean (ops, di tx pemanggil yang juga menulis audit).
// Tidak ada → 404; bukan dead → 409; job pengganti dengan dedupe key yang sama sedang hidup → 409.
func RetryDead(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Summary, error) {
	q := store.New(tx)
	sp, err := tx.Begin(ctx) // savepoint: unique violation tidak boleh meng-abort tx pemanggil
	if err != nil {
		return Summary{}, err
	}
	r, err := store.New(sp).RetryDeadJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = sp.Rollback(ctx)
		exists, xerr := q.JobExists(ctx, id)
		if xerr != nil {
			return Summary{}, db.MapError(xerr)
		}
		if !exists {
			return Summary{}, apperr.ErrNotFound
		}
		return Summary{}, apperr.ErrInvalidState
	}
	if db.IsUniqueViolation(err, "uq_jobs_dedupe_key") {
		_ = sp.Rollback(ctx)
		return Summary{}, apperr.ErrInvalidState.WithDetail("a live job with the same dedupe key already exists")
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return Summary{}, db.MapError(err)
	}
	if err := sp.Commit(ctx); err != nil {
		return Summary{}, err
	}
	return Summary{ID: r.ID, Status: r.Status, Attempts: r.Attempts, RunAt: r.RunAt}, nil
}
