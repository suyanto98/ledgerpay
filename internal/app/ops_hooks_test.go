//go:build testhooks

package app_test

import (
	"context"
	"net/http"
	"testing"

	"ledgerpay/internal/app"
)

// Race (g), docs/design/19-tx-concurrency.md §3: worker status check vs ops resolve pada payout
// UNKNOWN yang sama. Guard + lock payout diambil SEBELUM journal, jadi yang kalah menunggu lock,
// lalu melihat status final dan tidak pernah sempat memposting. Tepat satu journal hasil.

// Worker menang: status check (bank: SUCCEEDED) memegang lock payout; ops (yang yakin FAILED) menunggu,
// lalu mendapat 409. Payout SUCCEEDED via STATUS_QUERY, tidak ada reversal, tidak ada audit.
func TestPayoutStatusCheck_VsOpsResolve(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	_, id := unknownPayout(t, e)

	reached, release := pauseFirst(t, "payout.resolve.after_lock", nil)
	worker := make(chan error, 1)
	go func() {
		ctx := context.Background()
		if err := app.FastForwardJobs(ctx, e.deps); err != nil {
			worker <- err
			return
		}
		worker <- e.newWorker(t).RunOnce(ctx) // status check → settle → tertahan di hook
	}()
	<-reached
	opsDone := make(chan map[string]any, 1)
	go func() { _, b := e.resolve(t, ops, id, newKey(), evidence("FAILED")); opsDone <- b }()
	waitForLockWaiter(t, e) // gagal kalau ops tidak menunggu lock payout
	release.open()

	if err := <-worker; err != nil {
		t.Fatal(err)
	}
	if b := <-opsDone; b["code"] != "INVALID_STATE_TRANSITION" {
		t.Fatalf("ops must lose with 409 INVALID_STATE_TRANSITION, got %v", b)
	}
	if s, src, _ := e.payoutRow(t, id); s != "SUCCEEDED" || src != "STATUS_QUERY" {
		t.Fatalf("payout = %s via %s, want SUCCEEDED via STATUS_QUERY", s, src)
	}
	if j := journalsFor(t, e, id); j["PAYOUT_SETTLE"] != 1 || j["PAYOUT_REVERSAL"] != 0 {
		t.Fatalf("exactly one outcome journal: %v", j)
	}
	if a := auditFor(t, e, id); len(a) != 0 {
		t.Fatalf("a rejected ops action must leave no audit row (same tx): %+v", a)
	}
	assertLedgerHealthy(t, e)
}

// Ops menang: resolve memegang lock; status check yang datang kemudian menunggu, lalu no-op
// (job selesai tanpa journal). Payout final dari OPS, satu journal, satu audit.
func TestOpsResolve_VsStatusCheck(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	_, id := unknownPayout(t, e)

	reached, release := pauseFirst(t, "payout.resolve.after_lock", nil)
	opsDone := make(chan int, 1)
	go func() { r, _ := e.resolve(t, ops, id, newKey(), evidence("SUCCEEDED")); opsDone <- r.StatusCode }()
	<-reached
	worker := make(chan error, 1)
	go func() {
		ctx := context.Background()
		if err := app.FastForwardJobs(ctx, e.deps); err != nil {
			worker <- err
			return
		}
		worker <- e.newWorker(t).RunOnce(ctx)
	}()
	waitForLockWaiter(t, e)
	release.open()

	if s := <-opsDone; s != http.StatusOK {
		t.Fatalf("ops resolve: want 200, got %d", s)
	}
	if err := <-worker; err != nil {
		t.Fatal(err)
	}
	if s, src, _ := e.payoutRow(t, id); s != "SUCCEEDED" || src != "OPS" {
		t.Fatalf("payout = %s via %s, want SUCCEEDED via OPS", s, src)
	}
	if j := journalsFor(t, e, id); j["PAYOUT_SETTLE"] != 1 {
		t.Fatalf("exactly one settle journal: %v", j)
	}
	if a := auditFor(t, e, id); len(a) != 1 {
		t.Fatalf("one audit row: %+v", a)
	}
	assertLedgerHealthy(t, e)
}
