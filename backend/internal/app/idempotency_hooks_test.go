//go:build testhooks

// Test interleaving deterministik untuk idempotency (docs/design/19-tx-concurrency.md §3c).
// Butuh build tag: go test -tags testhooks ./internal/app/
package app_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"ledgerpay/internal/platform/db/dbtest"
	"ledgerpay/internal/platform/testhook"
)

// gate adalah sinyal "lanjutkan" untuk hook yang sedang menahan request. Gate SELALU dibuka di
// t.Cleanup: kalau test gagal di tengah jalan, request yang tertahan tetap dilepas. Tanpa ini,
// httptest.Server.Close() menunggu request itu selamanya dan seluruh suite macet.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate(t *testing.T) *gate {
	g := &gate{ch: make(chan struct{})}
	t.Cleanup(g.open) // didaftarkan SETELAH newEnv → berjalan SEBELUM server ditutup (LIFO)
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

func (g *gate) wait(ctx context.Context) error {
	select {
	case <-g.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pauseFirst memasang hook bernama yang MENAHAN eksekusi pertama yang sampai di titik itu,
// memberi sinyal lewat reached, dan melepasnya saat release dibuka. Eksekusi berikutnya lewat.
// failWith != nil mensimulasikan kegagalan eksekusi pertama setelah dilepas.
func pauseFirst(t *testing.T, hook string, failWith error) (reached chan struct{}, release *gate) {
	t.Helper()
	reached, release = make(chan struct{}), newGate(t)
	var once sync.Once
	t.Cleanup(testhook.Set(hook, func(ctx context.Context) error {
		first := false
		once.Do(func() { first = true })
		if !first {
			return nil
		}
		close(reached)
		if err := release.wait(ctx); err != nil {
			return err
		}
		return failWith
	}))
	return reached, release
}

// waitForLockWaiter menunggu sampai ada sesi lain di DB test yang sedang MENUNGGU lock
// (contoh: INSERT key kedua di unique index, atau confirm kedua di lock intent).
func waitForLockWaiter(t *testing.T, e env) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := e.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no other session ever waited on a lock (expected the second request to block)")
}

type outcome struct {
	status   int
	replayed bool
	body     map[string]any
}

func (e env) transferAsync(t *testing.T, token, key string, body map[string]any) <-chan outcome {
	ch := make(chan outcome, 1)
	go func() {
		r, b := e.transfer(t, token, key, body)
		ch <- outcome{status: r.StatusCode, replayed: r.Header.Get("Idempotent-Replayed") == "true", body: b}
	}()
	return ch
}

// Race (c), LI-05: R1 memegang key (belum commit), R2 dengan key sama MENUNGGU di unique index,
// R1 commit → R2 me-replay respons R1. Tepat satu efek.
func TestIdempotency_ConcurrentSameKey_OneEffect(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	inserted, release := pauseFirst(t, "idempotency.after_insert", nil)
	key, body := newKey(), transferBody("andi@example.com", "40000")

	r1 := e.transferAsync(t, rina, key, body)
	<-inserted // R1 sudah INSERT key, transaksinya masih terbuka
	r2 := e.transferAsync(t, rina, key, body)
	waitForLockWaiter(t, e) // R2 tertahan di unique index
	release.open()

	o1, o2 := <-r1, <-r2
	if o1.status != http.StatusCreated || o1.replayed {
		t.Fatalf("R1: want fresh 201, got %d replayed=%v %v", o1.status, o1.replayed, o1.body)
	}
	if o2.status != http.StatusCreated || !o2.replayed || o2.body["id"] != o1.body["id"] {
		t.Fatalf("R2: want replay of R1, got %d replayed=%v %v", o2.status, o2.replayed, o2.body)
	}
	if n := countTransfers(t, e); n != 1 {
		t.Fatalf("transfers = %d, want exactly 1", n)
	}
	if got := balanceOf(t, e, rina); got != "60000" {
		t.Fatalf("balance = %s, want 60000", got)
	}
	assertLedgerHealthy(t, e)
}

// R1 gagal (rollback) setelah INSERT key → INSERT R2 yang menunggu justru BERHASIL, dan R2
// diproses sebagai eksekusi pertama. Tidak ada key "macet" yang perlu dibersihkan.
func TestIdempotency_FirstTxRolledBack_SecondProceeds(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	inserted, release := pauseFirst(t, "idempotency.after_insert", errors.New("injected: connection lost mid-transaction"))
	key, body := newKey(), transferBody("andi@example.com", "40000")

	r1 := e.transferAsync(t, rina, key, body)
	<-inserted
	r2 := e.transferAsync(t, rina, key, body)
	waitForLockWaiter(t, e)
	release.open()

	o1, o2 := <-r1, <-r2
	if o1.status != http.StatusInternalServerError {
		t.Fatalf("R1: want 500 (injected failure), got %d %v", o1.status, o1.body)
	}
	if o2.status != http.StatusCreated || o2.replayed {
		t.Fatalf("R2: want fresh 201 (first real execution), got %d replayed=%v %v", o2.status, o2.replayed, o2.body)
	}
	if n := countTransfers(t, e); n != 1 {
		t.Fatalf("transfers = %d, want exactly 1", n)
	}
	assertLedgerHealthy(t, e)
}

// R1 masih berjalan lebih lama dari lock_timeout → R2 dengan key sama mendapat 409
// REQUEST_IN_PROGRESS (aman di-retry). Setelah R1 selesai, retry R2 me-replay hasil R1.
func TestIdempotency_SameKeyStillRunning_RequestInProgress(t *testing.T) {
	e := newEnv(t, dbtest.LockTimeout(300*time.Millisecond))
	rina, rinaID := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	seedTopup(t, e, rinaID, 100_000)
	inserted, release := pauseFirst(t, "idempotency.after_insert", nil)
	key, body := newKey(), transferBody("andi@example.com", "40000")

	r1 := e.transferAsync(t, rina, key, body)
	<-inserted
	r2, b2 := e.transfer(t, rina, key, body) // menunggu 300 ms di unique index, lalu menyerah
	release.open()
	if r2.StatusCode != http.StatusConflict || b2["code"] != "REQUEST_IN_PROGRESS" {
		t.Fatalf("R2: want 409 REQUEST_IN_PROGRESS, got %d %v", r2.StatusCode, b2)
	}
	if o1 := <-r1; o1.status != http.StatusCreated {
		t.Fatalf("R1: want 201, got %d %v", o1.status, o1.body)
	}
	r3, _ := e.transfer(t, rina, key, body)
	if r3.StatusCode != http.StatusCreated || r3.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry after R1 finished must replay, got %d", r3.StatusCode)
	}
	assertLedgerHealthy(t, e)
}
