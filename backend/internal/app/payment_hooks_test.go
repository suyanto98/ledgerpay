//go:build testhooks

package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"ledgerpay/internal/modules/payment"
)

// Race (e), docs/design/19-tx-concurrency.md §3: confirm vs sweeper kedaluwarsa, dua urutan.
func TestConfirm_VsExpirySweeper(t *testing.T) {
	ctx := context.Background()

	t.Run("confirm locks first: sweeper skips the row, payment succeeds", func(t *testing.T) {
		e := newEnv(t)
		k := merchantKey(t, e, opsToken(t, e), newMerchant(t, e, "Kopi Budi"))
		rina, rinaID := login(t, e, "rina@example.com")
		seedTopup(t, e, rinaID, 100_000)
		_, pi := e.createIntent(t, k, newKey(), "ORD-1", "10000")
		id := pi["id"].(string)
		if _, err := e.pool.Exec(ctx, `UPDATE payment_intents SET expires_at = now() + interval '1 second' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}

		locked, release := pauseFirst(t, "payment.after_intent_lock", nil)
		done := make(chan result, 1)
		go func() { r, _ := e.confirm(t, rina, id, newKey()); done <- r }()
		<-locked // confirm sudah memegang lock intent; transaksinya mulai SEBELUM expires_at

		time.Sleep(1200 * time.Millisecond) // sekarang intent sudah lewat expires_at
		start := time.Now()
		expired, err := payment.ExpireDue(ctx, e.pool, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(expired) != 0 || time.Since(start) > 500*time.Millisecond {
			t.Fatalf("sweeper must SKIP the locked intent without waiting, got %v in %v", expired, time.Since(start))
		}
		release.open()
		if r := <-done; r.StatusCode != http.StatusOK {
			t.Fatalf("confirm that started before expiry must succeed, got %d", r.StatusCode)
		}
		if again, _ := payment.ExpireDue(ctx, e.pool, 100); len(again) != 0 {
			t.Fatal("a SUCCEEDED intent must never be expired by the next sweep")
		}
		assertLedgerHealthy(t, e)
	})

	t.Run("sweeper locks first: confirm waits, then sees EXPIRED", func(t *testing.T) {
		e := newEnv(t)
		k := merchantKey(t, e, opsToken(t, e), newMerchant(t, e, "Kopi Budi"))
		rina, rinaID := login(t, e, "rina@example.com")
		seedTopup(t, e, rinaID, 100_000)
		_, pi := e.createIntent(t, k, newKey(), "ORD-1", "10000")
		id := pi["id"].(string)
		if _, err := e.pool.Exec(ctx, `UPDATE payment_intents SET expires_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}

		sweeper, err := e.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if expired, err := payment.ExpireDue(ctx, sweeper, 100); err != nil || len(expired) != 1 {
			t.Fatalf("sweeper must expire the intent: %v %v", expired, err)
		}
		done := make(chan map[string]any, 1)
		go func() { _, b := e.confirm(t, rina, id, newKey()); done <- b }()
		waitForLockWaiter(t, e) // confirm menunggu lock intent milik sweeper
		if err := sweeper.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if b := <-done; b["code"] != "PAYMENT_EXPIRED" {
			t.Fatalf("confirm after the sweeper committed must get 422 PAYMENT_EXPIRED, got %v", b)
		}
		if balanceOf(t, e, rina) != "100000" {
			t.Fatal("expired intent must not move money")
		}
	})
}

// Interleaving deterministik untuk LI-13: tab 1 memegang lock intent (dijeda oleh hook), tab 2
// HARUS menunggu di lock itu (bukan membaca status lama dan ikut membayar), lalu mendapat 409.
// Test ini ditambahkan karena mutation test menunjukkan TestConfirm_ParallelTabs_OnePayment
// tetap hijau walaupun FOR UPDATE dihapus: timing goroutine tidak menjamin race benar-benar terjadi.
func TestConfirm_SecondTabWaitsForIntentLock(t *testing.T) {
	e := newEnv(t)
	k := merchantKey(t, e, opsToken(t, e), newMerchant(t, e, "Kopi Budi"))
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 100_000)
	_, pi := e.createIntent(t, k, newKey(), "ORD-1", "30000")
	id := pi["id"].(string)

	inserted, release := pauseFirst(t, "payment.after_intent_lock", nil) // hanya tab 1 yang dijeda
	tab1 := make(chan int, 1)
	go func() { r, _ := e.confirm(t, rina, id, newKey()); tab1 <- r.StatusCode }()
	<-inserted
	tab2 := make(chan map[string]any, 1)
	go func() { _, b := e.confirm(t, rina, id, newKey()); tab2 <- b }()
	waitForLockWaiter(t, e) // gagal kalau tab 2 tidak menunggu lock intent
	release.open()

	if s := <-tab1; s != http.StatusOK {
		t.Fatalf("tab 1: want 200, got %d", s)
	}
	if b := <-tab2; b["code"] != "INVALID_STATE_TRANSITION" {
		t.Fatalf("tab 2: want 409 INVALID_STATE_TRANSITION, got %v", b)
	}
	assertLedgerHealthy(t, e)
}
