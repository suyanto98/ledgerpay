//go:build testhooks

package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/platform/idgen"
)

// Dua file BERBEDA untuk tanggal yang sama, keduanya mencocokkan top-up yang sama. Run #1 ditahan
// tepat setelah mengecek "journal settlement belum ada"; run #2 HARUS menunggu lock advisory tanggal
// itu, lalu melihat journal run #1 dan merujuknya. Tanpa lock, run #2 memposting duluan dan run #1
// gagal di uq_journals_source (500 untuk ops, job retry).
func TestRecon_ConcurrentDifferentFiles_OneSettlementJournal(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	topupID, _ := paidTopup(t, e, rina, "5000000")
	day := time.Now().In(recon.WIB)
	line := func(ref, amount string) string {
		return day.Format("2006-01-02") + ",CHARGE,ch_" + ref[:8] + "," + ref + "," + amount + ",IDR,SUCCEEDED," + day.UTC().Format(time.RFC3339) + ",0\n"
	}
	head := "business_date,type,provider_ref,reference,amount,currency,status,settled_at,fee\n"
	run := func(file string) <-chan error {
		ch := make(chan error, 1)
		svc := recon.NewService(e.pool, ledger.NewService(idgen.V7{}), fakeSource{[]byte(file)}, idgen.V7{})
		go func() { _, _, err := svc.Reconcile(context.Background(), day, nil); ch <- err }()
		return ch
	}

	reached, release := pauseFirst(t, "recon.before_settlement_post", nil)
	first := run(head + line(topupID, "5000000"))
	<-reached
	second := run(head + line(topupID, "5000000") + line(uuid.NewString(), "1000"))
	waitForLockWaiter(t, e) // gagal kalau run #2 tidak menunggu lock tanggal
	release.open()

	if err := <-first; err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if n := settlementJournals(t, e, topupID); n != 1 {
		t.Fatalf("PROVIDER_SETTLEMENT journals = %d, want 1", n)
	}
	if n := countRows(t, e, `SELECT count(DISTINCT settlement_journal_id) FROM recon_items WHERE internal_id = $1`, topupID); n != 1 {
		t.Fatalf("both runs' MATCHED items must reference the same journal, got %d distinct", n)
	}
	assertLedgerHealthy(t, e)
}
