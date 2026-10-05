package app_test

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/metrics"
)

// ---------------------------------------------------------------- helpers

func (e env) refund(t *testing.T, apiKey, intentID, key, amount string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/merchant/payment-intents/"+intentID+"/refunds", apiKey,
		map[string]string{"Idempotency-Key": key}, map[string]any{"amount": amount, "reason": "Item habis"})
}

// paidIntent: merchant + key + intent yang sudah dibayar Rina. Mengembalikan semua yang dibutuhkan test refund.
func paidIntent(t *testing.T, e env, amount string) (apiKey string, merchantID uuid.UUID, intentID, rina string) {
	t.Helper()
	merchantID = newMerchant(t, e, "Kopi Budi")
	apiKey = merchantKey(t, e, opsToken(t, e), merchantID)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 1_000_000)
	_, pi := e.createIntent(t, apiKey, newKey(), "ORD-"+uuid.NewString()[:8], amount)
	intentID = pi["id"].(string)
	if r, b := e.confirm(t, rina, intentID, newKey()); r.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d %v", r.StatusCode, b)
	}
	return apiKey, merchantID, intentID, rina
}

// refundOutcome: "201" atau "<status> <code>", supaya test membedakan 422 yang satu dengan yang lain.
func refundOutcome(r result, b map[string]any) string {
	if r.StatusCode == http.StatusCreated {
		return "201"
	}
	code, _ := b["code"].(string)
	return strconv.Itoa(r.StatusCode) + " " + code
}

func countRows(t *testing.T, e env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---------------------------------------------------------------- LF-16 / LI-08

// Refund parsial lalu sisa: uang kembali ke payer, status PARTIALLY_REFUNDED → REFUNDED, refunds[] di
// merchant view dan payer view, event payment.refunded tidak dibuat karena merchant tanpa webhook.
func TestRefund_PartialThenFull(t *testing.T) {
	e := newEnv(t)
	k, m, id, rina := paidIntent(t, e, "100000")

	r, b := e.refund(t, k, id, newKey(), "30000")
	pi, _ := b["payment_intent"].(map[string]any)
	if r.StatusCode != http.StatusCreated || b["amount"] != "30000" || pi["status"] != "PARTIALLY_REFUNDED" || pi["refunded_amount"] != "30000" {
		t.Fatalf("partial refund: %d %v", r.StatusCode, b)
	}
	if balanceOf(t, e, rina) != "930000" || merchantBalance(t, e, m) != 70_000 {
		t.Fatalf("money must go back: rina=%s merchant=%d", balanceOf(t, e, rina), merchantBalance(t, e, m))
	}
	r2, b2 := e.refund(t, k, id, newKey(), "70000")
	if pi2, _ := b2["payment_intent"].(map[string]any); r2.StatusCode != http.StatusCreated || pi2["status"] != "REFUNDED" {
		t.Fatalf("full refund: %d %v", r2.StatusCode, b2)
	}
	_, mv := e.doH(t, http.MethodGet, "/api/v1/merchant/payment-intents/"+id, k, nil, nil)
	if rs, _ := mv["refunds"].([]any); len(rs) != 2 || mv["refunded_amount"] != "100000" {
		t.Fatalf("merchant view must list both refunds: %v", mv)
	}
	_, uv := e.do(t, http.MethodGet, "/api/v1/payment-intents/"+id, rina, nil)
	if rs, _ := uv["refunds"].([]any); len(rs) != 2 {
		t.Fatalf("payer view must list refunds: %v", uv)
	}
	// REFUNDED adalah status terminal: refund berikutnya → 409.
	if r3, b3 := e.refund(t, k, id, newKey(), "1"); r3.StatusCode != http.StatusConflict || b3["code"] != "INVALID_STATE_TRANSITION" {
		t.Fatalf("refund after REFUNDED: %d %v", r3.StatusCode, b3)
	}
	if n := countRows(t, e, `SELECT count(*) FROM journals WHERE purpose = 'REFUND'`); n != 2 {
		t.Fatalf("REFUND journals = %d, want 2", n)
	}
	assertLedgerHealthy(t, e)
}

// TestRefund_ParallelExceedingAmount (LI-08, docs/design/05-invariants.md): amount 100.000, lima refund
// paralel × 30.000 (key berbeda) → tepat 3 sukses, 2 ditolak 422 REFUND_EXCEEDS_PAYMENT. Tanpa lock
// intent, kelimanya membaca refunded_amount = 0 dan semuanya "muat".
func TestRefund_ParallelExceedingAmount(t *testing.T) {
	e := newEnv(t)
	k, m, id, rina := paidIntent(t, e, "100000")

	var mu sync.Mutex
	got := map[string]int{}
	runConcurrently(5, func(int) int {
		r, b := e.refund(t, k, id, newKey(), "30000")
		mu.Lock()
		got[refundOutcome(r, b)]++
		mu.Unlock()
		return r.StatusCode
	})
	if got["201"] != 3 || got["422 REFUND_EXCEEDS_PAYMENT"] != 2 {
		t.Fatalf("want 3×201 and 2×422 REFUND_EXCEEDS_PAYMENT, got %v", got)
	}
	var refunded int64
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT refunded_amount, status FROM payment_intents WHERE id = $1`, id).Scan(&refunded, &status); err != nil {
		t.Fatal(err)
	}
	if refunded != 90_000 || status != "PARTIALLY_REFUNDED" {
		t.Fatalf("refunded_amount = %d (%s), want 90000 PARTIALLY_REFUNDED", refunded, status)
	}
	if sum := countRows(t, e, `SELECT coalesce(sum(amount), 0)::int FROM refunds WHERE payment_intent_id = $1`, id); sum != 90_000 {
		t.Fatalf("Σ refunds = %d, must equal refunded_amount", sum)
	}
	if balanceOf(t, e, rina) != "990000" || merchantBalance(t, e, m) != 10_000 {
		t.Fatalf("balances: rina=%s merchant=%d", balanceOf(t, e, rina), merchantBalance(t, e, m))
	}
	assertLedgerHealthy(t, e)
}

// Refund melebihi sisa → 422 + refundable_amount; penolakan domain disimpan (key sama → replay 422).
// Retry sukses dengan key yang sama → replay 201, uang kembali sekali.
func TestRefund_ExceedsAndReplay(t *testing.T) {
	e := newEnv(t)
	k, _, id, rina := paidIntent(t, e, "50000")

	key := newKey()
	r1, b1 := e.refund(t, k, id, key, "60000")
	r2, _ := e.refund(t, k, id, key, "60000")
	if r1.StatusCode != http.StatusUnprocessableEntity || b1["code"] != "REFUND_EXCEEDS_PAYMENT" || b1["refundable_amount"] != "50000" ||
		r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("exceeds: %d %v (replayed=%q)", r1.StatusCode, b1, r2.Header.Get("Idempotent-Replayed"))
	}
	ok := newKey()
	ra, a := e.refund(t, k, id, ok, "20000")
	rb, b := e.refund(t, k, id, ok, "20000")
	if ra.StatusCode != http.StatusCreated || rb.StatusCode != http.StatusCreated || rb.Header.Get("Idempotent-Replayed") != "true" || a["id"] != b["id"] {
		t.Fatalf("retry with the same key must replay: %d %d %v %v", ra.StatusCode, rb.StatusCode, a, b)
	}
	if balanceOf(t, e, rina) != "970000" {
		t.Fatalf("refund must be applied once, rina=%s", balanceOf(t, e, rina))
	}
}

// Saldo merchant tidak cukup → 422 INSUFFICIENT_BALANCE, tanpa refund dan intent tidak berubah.
// Di V1 saldo merchant hanya berkurang lewat refund, jadi kondisi ini dibuat dengan journal
// ADJUSTMENT_MANUAL yang sah (contoh: biaya/chargeback oleh ops), bukan UPDATE saldo langsung.
// Intent yang belum dibayar → 409.
func TestRefund_MerchantInsufficientAndUnpaid(t *testing.T) {
	e := newEnv(t)
	k, m, id, rina := paidIntent(t, e, "40000")
	_, opsID := login2(t, e, "ops@example.com")
	drainMerchant(t, e, m, opsID, 35_000) // tersisa 5.000

	r, b := e.refund(t, k, id, newKey(), "10000")
	if r.StatusCode != http.StatusUnprocessableEntity || b["code"] != "INSUFFICIENT_BALANCE" {
		t.Fatalf("merchant balance 5000, refund 10000: want 422 INSUFFICIENT_BALANCE, got %d %v", r.StatusCode, b)
	}
	if n := countRows(t, e, `SELECT count(*) FROM refunds`); n != 0 || balanceOf(t, e, rina) != "960000" {
		t.Fatalf("rejected refund must not change anything (refunds=%d, rina=%s)", n, balanceOf(t, e, rina))
	}
	if r, b := e.refund(t, k, id, newKey(), "5000"); r.StatusCode != http.StatusCreated {
		t.Fatalf("exact remaining balance must be refundable: %d %v", r.StatusCode, b)
	}

	_, pi2 := e.createIntent(t, k, newKey(), "ORD-UNPAID", "10000")
	if r, b := e.refund(t, k, pi2["id"].(string), newKey(), "1000"); r.StatusCode != http.StatusConflict || b["code"] != "INVALID_STATE_TRANSITION" {
		t.Fatalf("unpaid intent: %d %v", r.StatusCode, b)
	}
	assertLedgerHealthy(t, e)
}

// drainMerchant memindahkan saldo merchant ke FEE_REVENUE lewat journal ADJUSTMENT_MANUAL (aktor OPS).
func drainMerchant(t *testing.T, e env, merchantID, opsID uuid.UUID, amount int64) {
	t.Helper()
	l := ledger.NewService(idgen.V7{})
	ctx := context.Background()
	w, err := l.MerchantWalletOf(ctx, e.pool, merchantID)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := l.SystemAccountID(ctx, e.pool, "FEE_REVENUE")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithTx(ctx, e.pool, func(tx pgx.Tx) error {
		return l.PostJournal(ctx, tx, ledger.Journal{
			ID: uuid.Must(uuid.NewV7()), Purpose: ledger.PurposeAdjustmentManual, SourceType: "ops_action", SourceID: uuid.Must(uuid.NewV7()),
			Currency: "IDR", CorrelationID: "test", Actor: ledger.Actor{Type: "OPS", ID: &opsID}, Description: "test: merchant fee",
			Entries: []ledger.Entry{
				{AccountID: w.AccountID, Direction: ledger.Debit, Amount: amount},
				{AccountID: fee, Direction: ledger.Credit, Amount: amount},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}
}

// bola_refund: merchant B tidak bisa me-refund (atau mengetahui keberadaan) intent merchant A.
func TestRefund_BOLA_MerchantToMerchant(t *testing.T) {
	e := newEnv(t)
	_, ma, id, _ := paidIntent(t, e, "50000")
	ops, _ := login2(t, e, "ops@example.com")
	kb := merchantKey(t, e, ops, newMerchant(t, e, "Merchant B"))
	for _, target := range []string{id, uuid.NewString()} {
		r, b := e.refund(t, kb, target, newKey(), "1000")
		if r.StatusCode != http.StatusNotFound || b["code"] != "NOT_FOUND" {
			t.Fatalf("merchant B refunding %s: want 404, got %d %v", target, r.StatusCode, b)
		}
	}
	if merchantBalance(t, e, ma) != 50_000 || countRows(t, e, `SELECT count(*) FROM refunds`) != 0 {
		t.Fatal("cross-merchant refund must not move money")
	}
}

// GET /merchant/balance: saldo merchant pemegang key.
func TestMerchant_Balance(t *testing.T) {
	e := newEnv(t)
	k, _, _, _ := paidIntent(t, e, "75000")
	r, b := e.doH(t, http.MethodGet, "/api/v1/merchant/balance", k, nil, nil)
	if r.StatusCode != http.StatusOK || b["balance"] != "75000" || b["currency"] != "IDR" || b["as_of"] == nil {
		t.Fatalf("balance: %d %v", r.StatusCode, b)
	}
}

// Deteksi LI-08: job ledger.verify mengisi gauge refund drift. 0 saat sehat; refunded_amount yang
// diubah tanpa baris refund (bug/akses DB langsung) HARUS terlihat.
func TestRefund_DriftDetectedByLedgerVerify(t *testing.T) {
	e := newEnv(t)
	k, _, id, _ := paidIntent(t, e, "100000")
	if r, _ := e.refund(t, k, id, newKey(), "30000"); r.StatusCode != http.StatusCreated {
		t.Fatal("refund")
	}
	run := 0
	verify := func() float64 { // setiap panggilan = bucket jadwal baru (jam berikutnya)
		t.Helper()
		run++
		w := e.newWorker(t)
		if err := w.Maintenance.Tick(context.Background(), time.Now().Add(time.Duration(run)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		e.drain(t, w, 10)
		return testutil.ToFloat64(metrics.RefundDriftIntents.WithLabelValues())
	}
	if v := verify(); v != 0 {
		t.Fatalf("refund drift = %v on a healthy database", v)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE payment_intents SET refunded_amount = 40000 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if v := verify(); v != 1 {
		t.Fatalf("injected refund drift must show up in the gauge, got %v", v)
	}
}
