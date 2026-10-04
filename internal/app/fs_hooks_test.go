//go:build testhooks

package app_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"ledgerpay/internal/app"
	"ledgerpay/internal/modules/inbox"
	"ledgerpay/internal/platform/testhook"
)

// failFirst: eksekusi PERTAMA yang sampai di titik hook langsung gagal dengan err; berikutnya lewat.
func failFirst(t *testing.T, hook string, err error) {
	t.Helper()
	var once sync.Once
	t.Cleanup(testhook.Set(hook, func(context.Context) error {
		var out error
		once.Do(func() { out = err })
		return out
	}))
}

// FS-03 (database failure, commit outcome unknown): COMMIT sudah berhasil, tetapi koneksi putus
// sebelum aplikasi menerima konfirmasinya → client mendapat 5xx. Retry dengan key yang SAMA harus
// me-replay hasil (uang sudah pindah), bukan memindahkan uang lagi.
func TestTransfer_CommitOutcomeUnknown(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	failFirst(t, "db.after_commit", errors.New("injected: connection lost after COMMIT was sent"))
	key := newKey()

	r1, _ := e.transfer(t, rina, key, transferBody("andi@example.com", "30000"))
	if r1.StatusCode < 500 {
		t.Fatalf("client must see a server error when the commit outcome is unknown, got %d", r1.StatusCode)
	}
	if balanceOf(t, e, rina) != "70000" {
		t.Fatal("precondition: the transaction did commit")
	}
	r2, b2 := e.transfer(t, rina, key, transferBody("andi@example.com", "30000"))
	if r2.StatusCode != http.StatusCreated || r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry with the same key must replay the committed result, got %d %v", r2.StatusCode, b2)
	}
	if balanceOf(t, e, rina) != "70000" || countTransfers(t, e) != 1 {
		t.Fatal("money must move exactly once")
	}
	assertLedgerHealthy(t, e)
}

// FS-04 (API crash / koneksi putus SEBELUM commit): transaksi rollback, tidak ada efek; retry dengan
// key yang sama dieksekusi sebagai yang pertama (bukan replay).
func TestTransfer_ConnectionDropBeforeCommit(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	failFirst(t, "db.before_commit", errors.New("injected: connection lost before COMMIT"))
	key := newKey()

	r1, _ := e.transfer(t, rina, key, transferBody("andi@example.com", "30000"))
	if r1.StatusCode < 500 || balanceOf(t, e, rina) != "100000" || countTransfers(t, e) != 0 {
		t.Fatalf("rolled back: want 5xx and no effect, got %d (balance %s)", r1.StatusCode, balanceOf(t, e, rina))
	}
	r2, _ := e.transfer(t, rina, key, transferBody("andi@example.com", "30000"))
	if r2.StatusCode != http.StatusCreated || r2.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("retry must execute fresh (nothing was stored), got %d", r2.StatusCode)
	}
	if balanceOf(t, e, rina) != "70000" || countTransfers(t, e) != 1 {
		t.Fatal("money must move exactly once")
	}
}

// TestInbox_LeaseExpired_ProcessedOnce (race f, LI-09): W1 meng-claim event lalu "macet" (GC pause /
// verify-by-fetch lambat) melewati lease. W2 mengambil alih dan memproses. Saat W1 bangun, fencing
// menolak (0 baris) → rollback tanpa efek. Satu journal.
func TestInbox_LeaseExpired_ProcessedOnce(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	_, tp := e.createTopup(t, rina, newKey(), "10000000")
	chargeID := chargeIDOf(t, tp)
	_ = e.mock.Complete(chargeID, true)
	waitFor(t, "delivery", func() bool { return len(e.deliveries(chargeID)) == 1 })

	cfg := func(id string) inbox.ProcessorConfig { return inbox.ProcessorConfig{WorkerID: id, Lease: time.Second} }
	w1 := app.InboxProcessor(e.deps, cfg("w1"))
	w2 := app.InboxProcessor(e.deps, cfg("w2"))
	reached, release := pauseFirst(t, "inbox.before_apply", nil)

	done := make(chan struct{})
	go func() { _, _ = w1.RunOnce(context.Background()); close(done) }()
	<-reached                           // W1 sudah claim + verify, belum masuk tx
	time.Sleep(1200 * time.Millisecond) // lease W1 (1 dtk) kedaluwarsa
	if n, err := w2.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("W2 must take over the expired lease: n=%d err=%v", n, err)
	}
	release.open()
	<-done

	var journals, attempts int
	ctx := context.Background()
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM journals WHERE purpose = 'TOPUP_CREDIT'`).Scan(&journals)
	_ = e.pool.QueryRow(ctx, `SELECT attempts FROM webhook_inbox`).Scan(&attempts)
	if journals != 1 || balanceOf(t, e, rina) != "10000000" {
		t.Fatalf("exactly one credit expected: journals=%d balance=%s", journals, balanceOf(t, e, rina))
	}
	if e.inboxCount(t, "status = 'PROCESSED' AND outcome = 'APPLIED'") != 1 || attempts != 1 {
		t.Fatalf("event processed once by the new lease owner; takeover counts as one attempt (got %d)", attempts)
	}
	assertLedgerHealthy(t, e)
}
