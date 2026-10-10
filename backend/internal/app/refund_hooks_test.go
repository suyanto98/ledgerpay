//go:build testhooks

package app_test

import (
	"testing"
)

// LI-08 dengan interleaving PASTI (TestRefund_ParallelExceedingAmount membuktikan hasil akhirnya,
// tetapi request HTTP jarang benar-benar tumpang tindih). Refund #1 ditahan SAAT memegang lock
// intent; empat refund lain harus MENUNGGU lock itu, lalu membaca refunded_amount terbaru:
// 3×201 dan 2×422 REFUND_EXCEEDS_PAYMENT. Tanpa FOR UPDATE, keempatnya tidak menunggu dan yang
// kalah baru tertangkap guard UPDATE (409), bukan jawaban 422 yang benar.
func TestRefund_ParallelWaitOnIntentLock(t *testing.T) {
	e := newEnv(t)
	k, _, id, _ := paidIntent(t, e, "100000")

	reached, release := pauseFirst(t, "refund.after_intent_lock", nil)
	results := make(chan string, 5)
	send := func() { r, b := e.refund(t, k, id, newKey(), "30000"); results <- refundOutcome(r, b) }
	go send()
	<-reached
	for range 4 {
		go send()
	}
	waitForLockWaiter(t, e) // gagal kalau refund lain tidak menunggu lock intent
	release.open()

	got := map[string]int{}
	for range 5 {
		got[<-results]++
	}
	// Kode error ikut dicek, bukan hanya status: tanpa lock, yang kalah juga 422, tetapi
	// INSUFFICIENT_BALANCE (saldo merchant habis), alasan yang salah untuk merchant.
	if got["201"] != 3 || got["422 REFUND_EXCEEDS_PAYMENT"] != 2 {
		t.Fatalf("want 3×201 and 2×422 REFUND_EXCEEDS_PAYMENT, got %v", got)
	}
	assertLedgerHealthy(t, e)
}
