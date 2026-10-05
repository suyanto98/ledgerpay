package app_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"ledgerpay/internal/mockprovider"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/platform/idgen"
)

// ---------------------------------------------------------------- helpers

// today: business_date hari ini (WIB) sesuai jam MOCK (jam nyata). Jam palsu aplikasi dimajukan ke
// sekarang supaya validasi "business_date tidak di masa depan" memakai acuan yang sama.
func today(t *testing.T, e env) string {
	t.Helper()
	if d := time.Until(e.clk.Now()); d < 0 {
		e.clk.Advance(-d)
	}
	return time.Now().In(recon.WIB).Format("2006-01-02")
}

func (e env) reconRun(t *testing.T, ops, date string) (result, map[string]any) {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/v1/ops/recon-runs", ops, map[string]any{"business_date": date, "source": "PULL"})
}

func (e env) openItems(t *testing.T, ops string) []map[string]any {
	t.Helper()
	r, b := e.do(t, http.MethodGet, "/api/v1/ops/recon-items?resolution=OPEN", ops, nil)
	data, ok := b["data"].([]any)
	if !ok {
		t.Fatalf("list recon items: %d %v", r.StatusCode, b)
	}
	var out []map[string]any
	for _, x := range data {
		out = append(out, x.(map[string]any))
	}
	return out
}

func settlementJournals(t *testing.T, e env, topupID string) int {
	t.Helper()
	return countRows(t, e, `SELECT count(*) FROM journals WHERE purpose = 'PROVIDER_SETTLEMENT' AND source_id = $1`, topupID)
}

// ---------------------------------------------------------------- Demo D

// TestRecon_DemoD_StatusMismatchResolvedWithEvidence: bank mengeksekusi payout, respons hilang, status
// query bank mati → payout menggantung UNKNOWN. File settlement bank menunjukkan SUCCEEDED → recon
// menandai STATUS_MISMATCH → ops menyelesaikannya dengan bukti (APPLY_PAYOUT_SETTLE): journal settle
// satu kali, item RESOLVED, dua baris audit, bank tetap mencatat 1 transfer.
func TestRecon_DemoD_StatusMismatchResolvedWithEvidence(t *testing.T) {
	e := newEnv(t)
	date := today(t, e) // SEBELUM login: token diterbitkan dengan jam yang sudah dimajukan
	ops := opsToken(t, e)
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", PayoutStatusQuery: "disabled"})
	_, payoutID := unknownPayout(t, e)

	r, run := e.reconRun(t, ops, date)
	if r.StatusCode != http.StatusCreated || run["status"] != "COMPLETED" || run["status_mismatch_count"] != float64(1) {
		t.Fatalf("recon run: %d %v", r.StatusCode, run)
	}
	items := e.openItems(t, ops)
	if len(items) != 1 || items[0]["category"] != "STATUS_MISMATCH" || items[0]["internal_id"] != payoutID ||
		items[0]["internal_status"] != "UNKNOWN" || items[0]["external_status"] != "SUCCEEDED" {
		t.Fatalf("open items: %v", items)
	}
	itemID := items[0]["id"].(string)
	resolve := func(key string, body map[string]any) (result, map[string]any) {
		return e.doH(t, http.MethodPost, "/api/v1/ops/recon-items/"+itemID+"/resolve", ops, map[string]string{"Idempotency-Key": key}, body)
	}
	if r, b := resolve(newKey(), map[string]any{"action": "APPLY_PAYOUT_SETTLE", "reason": "Settlement bank menunjukkan SUCCEEDED"}); r.StatusCode != http.StatusUnprocessableEntity || b["code"] != "RESOLUTION_EVIDENCE_REQUIRED" {
		t.Fatalf("no evidence: %d %v", r.StatusCode, b)
	}
	if r, b := resolve(newKey(), map[string]any{"action": "APPLY_PAYOUT_REVERSAL", "evidence_ref": "settlement#L1", "reason": "Salah arah: bank bilang SUCCEEDED"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("reversal against a SUCCEEDED settlement must be refused: %d %v", r.StatusCode, b)
	}
	r, item := resolve(newKey(), map[string]any{"action": "APPLY_PAYOUT_SETTLE", "evidence_ref": "settlement-" + date + ".csv#L2", "reason": "Settlement bank menunjukkan SUCCEEDED"})
	if r.StatusCode != http.StatusOK || item["resolution"] != "RESOLVED" || item["resolution_action"] != "APPLY_PAYOUT_SETTLE" {
		t.Fatalf("resolve: %d %v", r.StatusCode, item)
	}
	if s, src, _ := e.payoutRow(t, payoutID); s != "SUCCEEDED" || src != "OPS" {
		t.Fatalf("payout = %s via %s", s, src)
	}
	if j := journalsFor(t, e, payoutID); j["PAYOUT_SETTLE"] != 1 || j["PAYOUT_REVERSAL"] != 0 {
		t.Fatalf("journals: %v", j)
	}
	if a := auditFor(t, e, itemID); len(a) != 1 || a[0].action != "recon.item_resolve" || a[0].diff["evidence_ref"] == nil {
		t.Fatalf("recon item audit: %+v", a)
	}
	if a := auditFor(t, e, payoutID); len(a) != 1 || a[0].action != "payout.manual_resolve" {
		t.Fatalf("payout audit: %+v", a)
	}
	if executed, _ := e.mock.PayoutStats(payoutID); executed != 1 {
		t.Fatalf("bank transfers = %d, want 1", executed)
	}
	if r, b := resolve(newKey(), map[string]any{"action": "NO_ACTION_TIMING", "evidence_ref": "x-1", "reason": "Second attempt at resolving"}); r.StatusCode != http.StatusConflict {
		t.Fatalf("resolving a RESOLVED item: want 409, got %d %v", r.StatusCode, b)
	}
	// LI-12: file bank tidak berubah → hash sama → run yang ada dikembalikan, tidak ada item baru.
	if r, again := e.reconRun(t, ops, date); r.StatusCode != http.StatusOK || again["id"] != run["id"] {
		t.Fatalf("same file again must return the existing run: %d %v", r.StatusCode, again)
	}
	assertLedgerHealthy(t, e)
}

// Top-up yang cocok → journal PROVIDER_SETTLEMENT (DR BANK_CLEARING / CR PROVIDER_CLEARING). File lain
// untuk tanggal yang sama (hash berbeda) → run baru, top-up MATCHED lagi, tetapi journal TETAP satu.
func TestRecon_TopupSettlementPostedOnceAcrossRuns(t *testing.T) {
	e := newEnv(t)
	date := today(t, e)
	ops := opsToken(t, e)
	rina, _ := login(t, e, "rina@example.com")
	topupID, _ := paidTopup(t, e, rina, "5000000")

	r, first := e.reconRun(t, ops, date)
	if r.StatusCode != http.StatusCreated || first["matched_count"] != float64(1) || first["missing_external_count"] != float64(0) {
		t.Fatalf("first run: %d %v", r.StatusCode, first)
	}
	if n := settlementJournals(t, e, topupID); n != 1 {
		t.Fatalf("PROVIDER_SETTLEMENT journals = %d, want 1", n)
	}
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Settlement: "settlement_mismatch:extra"})
	r, second := e.reconRun(t, ops, date)
	if r.StatusCode != http.StatusCreated || second["id"] == first["id"] || second["matched_count"] != float64(1) || second["missing_internal_count"] != float64(1) {
		t.Fatalf("second run (different file): %d %v", r.StatusCode, second)
	}
	if n := settlementJournals(t, e, topupID); n != 1 {
		t.Fatalf("a second run must reference the existing settlement journal, got %d journals", n)
	}
	items := e.openItems(t, ops)
	if len(items) != 1 || items[0]["category"] != "MISSING_INTERNAL" || items[0]["internal_id"] != nil {
		t.Fatalf("open items: %v", items)
	}
	assertLedgerHealthy(t, e)
}

// Roadmap DoD S9: skenario settlement_mismatch:status → STATUS_MISMATCH, tanpa journal settlement.
// settlement_mismatch:amount → AMOUNT_MISMATCH; :missing → MISSING_EXTERNAL.
func TestRecon_MockMismatchScenarios(t *testing.T) {
	for scenario, category := range map[string]string{
		"settlement_mismatch:status":  "STATUS_MISMATCH",
		"settlement_mismatch:amount":  "AMOUNT_MISMATCH",
		"settlement_mismatch:missing": "MISSING_EXTERNAL",
	} {
		t.Run(category, func(t *testing.T) {
			e := newEnv(t)
			date := today(t, e)
			ops := opsToken(t, e)
			rina, _ := login(t, e, "rina@example.com")
			topupID, _ := paidTopup(t, e, rina, "3000000")
			e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Settlement: scenario})
			r, run := e.reconRun(t, ops, date)
			items := e.openItems(t, ops)
			if r.StatusCode != http.StatusCreated || len(items) != 1 || items[0]["category"] != category || items[0]["internal_id"] != topupID {
				t.Fatalf("%s: run %d %v, items %v", scenario, r.StatusCode, run, items)
			}
			if n := settlementJournals(t, e, topupID); n != 0 {
				t.Fatalf("a mismatched top-up must not be settled automatically (journals = %d)", n)
			}
		})
	}
}

type fakeSource struct{ body []byte }

func (f fakeSource) Settlement(context.Context, string) ([]byte, error) { return f.body, nil }

// File rusak → run FAILED tercatat (dengan hash; file yang sama tidak diproses ulang), tidak ada item.
// Dua run bersamaan untuk file yang sama → tepat satu run baru (advisory lock + uq_recon_runs_file).
func TestRecon_MalformedFileAndConcurrentRuns(t *testing.T) {
	e := newEnv(t)
	day := time.Now().In(recon.WIB)
	bad := recon.NewService(e.pool, ledger.NewService(idgen.V7{}), fakeSource{[]byte("not,a,settlement\n")}, idgen.V7{})
	run, created, err := bad.Reconcile(context.Background(), day, nil)
	if err != nil || !created || run.Status != "FAILED" || run.Error == nil {
		t.Fatalf("malformed file: %+v created=%v err=%v", run, created, err)
	}
	if _, again, _ := bad.Reconcile(context.Background(), day, nil); again {
		t.Fatal("the same malformed file must not create a second run")
	}

	file := []byte("business_date,type,provider_ref,reference,amount,currency,status,settled_at,fee\n" +
		day.Format("2006-01-02") + ",CHARGE,ch_z," + uuid.NewString() + ",1000,IDR,SUCCEEDED," + day.UTC().Format(time.RFC3339) + ",0\n")
	svc := recon.NewService(e.pool, ledger.NewService(idgen.V7{}), fakeSource{file}, idgen.V7{})
	var mu sync.Mutex
	createdCount, ids := 0, map[uuid.UUID]bool{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, c, err := svc.Reconcile(context.Background(), day, nil)
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[r.ID] = true
			if c {
				createdCount++
			}
		}()
	}
	wg.Wait()
	if createdCount != 1 || len(ids) != 1 {
		t.Fatalf("4 concurrent runs of the same file: created=%d distinct runs=%d, want 1 and 1", createdCount, len(ids))
	}
	if n := countRows(t, e, `SELECT count(*) FROM recon_items`); n != 1 {
		t.Fatalf("recon items = %d, want 1 (MISSING_INTERNAL, once)", n)
	}
}

// recon.daily: sebelum 02:30 WIB tidak melakukan apa-apa; setelahnya menjalankan D−1 sekali (catch-up
// tiap jam berhenti setelah ada run COMPLETED untuk tanggal itu).
func TestRecon_DailyRunsYesterdayOnceAfter0230(t *testing.T) {
	e := newEnv(t)
	svc := recon.NewService(e.pool, ledger.NewService(idgen.V7{}), dailySource{}, idgen.V7{})
	early := time.Date(2026, 10, 5, 2, 10, 0, 0, recon.WIB)
	if ran, err := svc.Daily(context.Background(), early); err != nil || ran {
		t.Fatalf("before 02:30 WIB: ran=%v err=%v", ran, err)
	}
	late := time.Date(2026, 10, 5, 3, 0, 0, 0, recon.WIB)
	if ran, err := svc.Daily(context.Background(), late); err != nil || !ran {
		t.Fatalf("after 02:30 WIB: ran=%v err=%v", ran, err)
	}
	if ran, _ := svc.Daily(context.Background(), late.Add(time.Hour)); ran {
		t.Fatal("yesterday is already reconciled; the hourly catch-up must not run again")
	}
	var date time.Time
	_ = e.pool.QueryRow(context.Background(), `SELECT business_date FROM recon_runs`).Scan(&date)
	if date.Format("2006-01-02") != "2026-10-04" {
		t.Fatalf("business_date = %s, want 2026-10-04 (D−1)", date.Format("2006-01-02"))
	}
}

// dailySource: file kosong yang valid untuk tanggal apa pun yang diminta.
type dailySource struct{}

func (dailySource) Settlement(_ context.Context, date string) ([]byte, error) {
	return []byte("business_date,type,provider_ref,reference,amount,currency,status,settled_at,fee\n"), nil
}
