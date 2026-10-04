package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"ledgerpay/internal/app"
	"ledgerpay/internal/mockprovider"
	"ledgerpay/internal/platform/webhooksig"
)

// ---------------------------------------------------------------- helpers

func (e env) newWorker(t *testing.T) *app.Worker {
	t.Helper()
	return app.NewWorker(e.deps, app.WorkerConfig{ID: "test-" + uuid.NewString()[:6], Lease: 30 * time.Second})
}

// drain menjalankan worker berulang, mempercepat backoff/jadwal (FastForwardJobs), sampai tidak ada
// job queued lagi atau batas putaran habis.
func (e env) drain(t *testing.T, w *app.Worker, rounds int) {
	t.Helper()
	ctx := context.Background()
	for range rounds {
		if err := app.FastForwardJobs(ctx, e.deps); err != nil {
			t.Fatal(err)
		}
		if err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		var queued int
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status = 'queued'`).Scan(&queued)
		if queued == 0 {
			return
		}
	}
}

func (e env) createPayout(t *testing.T, token, amount, account string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/payouts", token, map[string]string{"Idempotency-Key": newKey()}, map[string]any{
		"amount": amount, "currency": "IDR",
		"destination": map[string]string{"bank_code": "MOCKBANK", "account_number": account, "holder_name": "Rina Wulandari"},
	})
}

func (e env) payout(t *testing.T, token, id string) map[string]any {
	t.Helper()
	_, b := e.do(t, http.MethodGet, "/api/v1/payouts/"+id, token, nil)
	return b
}

func (e env) payoutRow(t *testing.T, id string) (status, source string, needsRecon bool) {
	t.Helper()
	var src *string
	if err := e.pool.QueryRow(context.Background(), `SELECT status, resolution_source, needs_reconciliation FROM payouts WHERE id = $1`, id).Scan(&status, &src, &needsRecon); err != nil {
		t.Fatal(err)
	}
	if src != nil {
		source = *src
	}
	return status, source, needsRecon
}

func journalsFor(t *testing.T, e env, payoutID string) map[string]int {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT purpose, count(*) FROM journals WHERE source_type = 'payout' AND source_id = $1 GROUP BY purpose`, payoutID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		_ = rows.Scan(&p, &n)
		out[p] = n
	}
	return out
}

// ---------------------------------------------------------------- payout (LF-11, ADR-L06)

func TestPayout_HappyPath_HoldThenSettle(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	r, po := e.createPayout(t, rina, "2000000", "1234567890")
	id, _ := po["id"].(string)
	dest, _ := po["destination"].(map[string]any)
	if r.StatusCode != http.StatusAccepted || po["status"] != "PENDING" || po["display_status"] != "PROCESSING" || dest["account_number_masked"] != "******7890" {
		t.Fatalf("create: %d %v", r.StatusCode, po)
	}
	if balanceOf(t, e, rina) != "8000000" {
		t.Fatal("funds must be held immediately (hold journal), before the bank is called")
	}
	var stored string
	_ = e.pool.QueryRow(context.Background(), `SELECT row_to_json(p)::text FROM payouts p WHERE id = $1`, id).Scan(&stored)
	if strings.Contains(stored, "1234567890") {
		t.Fatal("the full account number must never be stored in plaintext")
	}

	e.drain(t, e.newWorker(t), 5)
	if s, src, _ := e.payoutRow(t, id); s != "SUCCEEDED" || src != "BANK_RESPONSE" {
		t.Fatalf("want SUCCEEDED via BANK_RESPONSE, got %s %s", s, src)
	}
	if executed, received := e.mock.PayoutStats(id); executed != 1 || received != 1 {
		t.Fatalf("bank: executed=%d received=%d, want 1/1", executed, received)
	}
	if j := journalsFor(t, e, id); j["PAYOUT_HOLD"] != 1 || j["PAYOUT_SETTLE"] != 1 || j["PAYOUT_REVERSAL"] != 0 {
		t.Fatalf("journals: %v", j)
	}
	assertLedgerHealthy(t, e)
}

// Penolakan DEFINITIF (422) → FAILED + reversal: dana kembali ke wallet.
func TestPayout_Declined_Reversed(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	_, po := e.createPayout(t, rina, "2000000", "9900000422") // magic: decline
	id := po["id"].(string)
	e.drain(t, e.newWorker(t), 5)
	if s, _, _ := e.payoutRow(t, id); s != "FAILED" || balanceOf(t, e, rina) != "10000000" {
		t.Fatalf("declined: want FAILED and funds returned, got %s balance %s", s, balanceOf(t, e, rina))
	}
	if j := journalsFor(t, e, id); j["PAYOUT_HOLD"] != 1 || j["PAYOUT_REVERSAL"] != 1 || j["PAYOUT_SETTLE"] != 0 {
		t.Fatalf("journals: %v", j)
	}
	assertLedgerHealthy(t, e)
}

// TestPayout_TimeoutAfterProcess_ResolvesViaStatusQuery (FS-05, inti Demo C): bank SUDAH mengirim
// uang tetapi respons tidak sampai. LedgerPay TIDAK menebak: UNKNOWN → status query → SUCCEEDED.
// Bank mencatat tepat satu transfer.
func TestPayout_TimeoutAfterProcess_ResolvesViaStatusQuery(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	_, po := e.createPayout(t, rina, "2000000", "9900000408") // magic: timeout_after_process
	id := po["id"].(string)
	w := e.newWorker(t)
	_ = w.RunOnce(context.Background()) // submit: timeout
	if s, _, _ := e.payoutRow(t, id); s != "UNKNOWN" {
		t.Fatalf("after a timeout the outcome is unknown, got %s", s)
	}
	if p := e.payout(t, rina, id); p["display_status"] != "PROCESSING" {
		t.Fatalf("user sees PROCESSING, got %v", p)
	}
	e.drain(t, w, 5) // status check (jadwal 30 dtk dipercepat)
	if s, src, _ := e.payoutRow(t, id); s != "SUCCEEDED" || src != "STATUS_QUERY" {
		t.Fatalf("want SUCCEEDED via STATUS_QUERY, got %s %s", s, src)
	}
	if executed, received := e.mock.PayoutStats(id); executed != 1 || received != 1 {
		t.Fatalf("bank must have executed exactly 1 transfer (no blind resubmit): executed=%d received=%d", executed, received)
	}
	assertLedgerHealthy(t, e)
}

// Amandemen ADR-L06: respons "sukses" yang isinya tidak cocok → UNKNOWN + needs_reconciliation.
func TestPayout_MismatchedProviderResponse_Unknown(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Payouts: "response_mismatch"})
	_, po := e.createPayout(t, rina, "2000000", "1234567890")
	id := po["id"].(string)
	_ = e.newWorker(t).RunOnce(context.Background())
	if s, _, recon := e.payoutRow(t, id); s != "UNKNOWN" || !recon {
		t.Fatalf("mismatched success must not settle: got %s needs_reconciliation=%v", s, recon)
	}
	if j := journalsFor(t, e, id); j["PAYOUT_SETTLE"] != 0 {
		t.Fatal("no settle journal for a mismatched response")
	}
}

// Aturan resubmit: NOT_FOUND baru berarti "tidak pernah diterima" SETELAH safe window. Sebelum itu
// diperlakukan PROCESSING. Setelah lewat → submit ulang dengan key yang SAMA.
func TestPayout_TimeoutBeforeProcess_ResubmitOnlyAfterSafeWindow(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Payouts: "timeout_before_process"})
	_, po := e.createPayout(t, rina, "2000000", "1234567890")
	id := po["id"].(string)

	strict := e.newWorker(t) // safe window env = 1 jam
	_ = strict.RunOnce(context.Background())
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Payouts: "ok"})
	_ = app.FastForwardJobs(context.Background(), e.deps)
	_ = strict.RunOnce(context.Background())
	if s, _, _ := e.payoutRow(t, id); s != "UNKNOWN" {
		t.Fatalf("NOT_FOUND inside the safe window must NOT trigger a resubmit, got %s", s)
	}
	if executed, _ := e.mock.PayoutStats(id); executed != 0 {
		t.Fatal("no transfer may happen inside the safe window")
	}

	d := e.deps
	d.PayoutSafeWindow = time.Millisecond // window sudah lewat
	relaxed := app.NewWorker(d, app.WorkerConfig{ID: "relaxed"})
	e.drain(t, relaxed, 5)
	if s, src, _ := e.payoutRow(t, id); s != "SUCCEEDED" || src != "STATUS_QUERY" {
		t.Fatalf("after the safe window: resubmit with the same key → SUCCEEDED, got %s %s", s, src)
	}
	if executed, received := e.mock.PayoutStats(id); executed != 1 || received != 2 {
		t.Fatalf("bank: executed=%d received=%d, want 1 transfer from 2 requests", executed, received)
	}
}

// Jadwal status check habis (5 check) dan hasil masih tak pasti → needs_reconciliation, bukan dead.
func TestPayout_StatusChecksExhausted_NeedsReconciliation(t *testing.T) {
	e := newEnv(t)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Payouts: "timeout_before_process"})
	_, po := e.createPayout(t, rina, "2000000", "1234567890")
	id := po["id"].(string)
	e.drain(t, e.newWorker(t), 10)
	var checks int
	_ = e.pool.QueryRow(context.Background(), `SELECT status_check_attempts FROM payouts WHERE id = $1`, id).Scan(&checks)
	if s, _, recon := e.payoutRow(t, id); s != "UNKNOWN" || !recon || checks != 5 {
		t.Fatalf("want UNKNOWN + needs_reconciliation after 5 checks, got %s recon=%v checks=%d", s, recon, checks)
	}
	if balanceOf(t, e, rina) != "8000000" {
		t.Fatal("funds stay held while the outcome is unknown (never reversed on a guess)")
	}
}

func TestPayout_ValidationAndInsufficientBalance(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	if r, b := e.createPayout(t, rina, "2000000", "12"); r.StatusCode != http.StatusBadRequest || b["code"] != "VALIDATION_ERROR" {
		t.Fatalf("bad account number: %d %v", r.StatusCode, b)
	}
	if r, b := e.createPayout(t, rina, "2000000", "1234567890"); r.StatusCode != http.StatusUnprocessableEntity || b["code"] != "INSUFFICIENT_BALANCE" {
		t.Fatalf("no balance: %d %v", r.StatusCode, b)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs`).Scan(&n)
	if n != 0 {
		t.Fatal("a rejected payout must not enqueue a bank job (outbox rolls back with the business tx)")
	}
}

// ---------------------------------------------------------------- webhook keluar (LF-15)

type sink struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []*http.Request
	bodies [][]byte
	status int
}

func newSink(t *testing.T, status int) *sink {
	t.Helper()
	s := &sink{status: status}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.got, s.bodies = append(s.got, r), append(s.bodies, b)
		s.mu.Unlock()
		w.WriteHeader(s.status)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.got) }

// paidIntentWithWebhook: merchant dengan webhook ke sink, intent dibayar Rina.
func paidIntentWithWebhook(t *testing.T, e env, url string) (secret string) {
	t.Helper()
	m := newMerchant(t, e, "Kopi Budi")
	secret, err := app.SetMerchantWebhook(context.Background(), e.pool, e.deps.Keyring, m, url, false)
	if err != nil {
		t.Fatal(err)
	}
	k := merchantKey(t, e, opsToken(t, e), m)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 100_000)
	_, pi := e.createIntent(t, k, newKey(), "ORD-1", "30000")
	if r, b := e.confirm(t, rina, pi["id"].(string), newKey()); r.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d %v", r.StatusCode, b)
	}
	return secret
}

// Confirm → event payment.succeeded di outbox (tx yang sama) → dikirim bertanda tangan HMAC.
func TestMerchantWebhook_PaymentSucceeded_SignedAndDelivered(t *testing.T) {
	e := newEnv(t)
	s := newSink(t, http.StatusOK)
	secret := paidIntentWithWebhook(t, e, s.srv.URL+"/hooks")
	e.drain(t, e.newWorker(t), 3)
	if s.count() != 1 {
		t.Fatalf("want exactly 1 delivery, got %d", s.count())
	}
	req, body := s.got[0], s.bodies[0]
	if _, err := webhooksig.Verify(req.Header.Get("LedgerPay-Signature"), body, [][]byte{[]byte(secret)}, e.clk.Now(), 5*time.Minute); err != nil {
		t.Fatalf("merchant must be able to verify the signature with its secret: %v", err)
	}
	var ev struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			PaymentIntent struct {
				Status string `json:"status"`
				Amount string `json:"amount"`
			} `json:"payment_intent"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &ev)
	if ev.Type != "payment.succeeded" || ev.Data.PaymentIntent.Status != "SUCCEEDED" || ev.Data.PaymentIntent.Amount != "30000" || req.Header.Get("LedgerPay-Event-Id") != ev.ID {
		t.Fatalf("unexpected event: %s (header id %s)", body, req.Header.Get("LedgerPay-Event-Id"))
	}
	var status string
	_ = e.pool.QueryRow(context.Background(), `SELECT status FROM webhook_deliveries`).Scan(&status)
	if status != "DELIVERED" {
		t.Fatalf("delivery status = %s", status)
	}
}

// TestMerchantWebhook_RetryBackoffThenDead (FS-09): merchant terus gagal → 7 percobaan → DEAD.
// event id SAMA di setiap percobaan (merchant dedupe); nomor percobaan naik.
func TestMerchantWebhook_RetryBackoffThenDead(t *testing.T) {
	e := newEnv(t)
	s := newSink(t, http.StatusInternalServerError)
	paidIntentWithWebhook(t, e, s.srv.URL)
	e.drain(t, e.newWorker(t), 12)
	if s.count() != 7 {
		t.Fatalf("want 7 attempts (1 + 6 retries), got %d", s.count())
	}
	ids := map[string]bool{}
	for i, r := range s.got {
		ids[r.Header.Get("LedgerPay-Event-Id")] = true
		if r.Header.Get("LedgerPay-Delivery-Attempt") != string(rune('1'+i)) {
			t.Fatalf("attempt header %d = %q", i+1, r.Header.Get("LedgerPay-Delivery-Attempt"))
		}
	}
	var status string
	var attempts int
	_ = e.pool.QueryRow(context.Background(), `SELECT d.status, (SELECT count(*) FROM webhook_delivery_attempts) FROM webhook_deliveries d`).Scan(&status, &attempts)
	if len(ids) != 1 || status != "DEAD" || attempts != 7 {
		t.Fatalf("want one stable event id, DEAD, 7 recorded attempts; got ids=%d status=%s attempts=%d", len(ids), status, attempts)
	}
}

// webhook_egress_blocks_private_ip (integration): merchant mendaftarkan URL metadata cloud →
// pengiriman diblok egress guard sebelum koneksi dibuat, percobaan tercatat sebagai gagal.
func TestMerchantWebhook_EgressBlocksMetadataURL(t *testing.T) {
	e := newEnv(t)
	paidIntentWithWebhook(t, e, "http://169.254.169.254/latest/meta-data/iam/security-credentials/")
	_ = e.newWorker(t).RunOnce(context.Background())
	var errText string
	_ = e.pool.QueryRow(context.Background(), `SELECT coalesce(error, '') FROM webhook_delivery_attempts`).Scan(&errText)
	if !strings.Contains(errText, "destination address is not allowed") {
		t.Fatalf("metadata URL must be blocked by the egress guard, got attempt error %q", errText)
	}
}

// ---------------------------------------------------------------- sweeper terjadwal

func (e env) tickAndDrain(t *testing.T) {
	t.Helper()
	w := e.newWorker(t)
	if err := w.Maintenance.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.drain(t, w, 10)
}

// intent.expire_sweep: intent lewat waktu → EXPIRED + webhook payment.expired (satu tx).
func TestSweep_IntentExpired_PublishesWebhook(t *testing.T) {
	e := newEnv(t)
	s := newSink(t, http.StatusOK)
	m := newMerchant(t, e, "Kopi Budi")
	if _, err := app.SetMerchantWebhook(context.Background(), e.pool, e.deps.Keyring, m, s.srv.URL, false); err != nil {
		t.Fatal(err)
	}
	k := merchantKey(t, e, opsToken(t, e), m)
	_, pi := e.createIntent(t, k, newKey(), "ORD-1", "30000")
	_, _ = e.pool.Exec(context.Background(), `UPDATE payment_intents SET expires_at = now() - interval '1 second'`)
	e.tickAndDrain(t)
	var status string
	_ = e.pool.QueryRow(context.Background(), `SELECT status FROM payment_intents WHERE id = $1`, pi["id"]).Scan(&status)
	if status != "EXPIRED" || s.count() != 1 || !strings.Contains(string(s.bodies[0]), `"payment.expired"`) {
		t.Fatalf("want EXPIRED + one payment.expired webhook, got %s, %d deliveries", status, s.count())
	}
}

// topup.expire_sweep: provider ditanya DULU. Top-up yang sebenarnya sudah dibayar (webhook-nya hilang)
// dikreditkan; yang belum dibayar menjadi EXPIRED.
func TestSweep_TopupExpire_ChecksProviderFirst(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Webhooks: []string{"webhook_bad_signature"}}) // webhook "hilang"
	_, paid := e.createTopup(t, rina, newKey(), "10000000")
	_ = e.mock.Complete(chargeIDOf(t, paid), true)
	_, unpaid := e.createTopup(t, rina, newKey(), "5000000")
	waitFor(t, "rejected webhook", func() bool { return len(e.deliveries(chargeIDOf(t, paid))) == 1 })
	_, _ = e.pool.Exec(context.Background(), `UPDATE topups SET expires_at = now() - interval '1 second'`)

	e.tickAndDrain(t)
	if s := e.topupStatus(t, rina, paid["id"].(string)); s["status"] != "SUCCEEDED" {
		t.Fatalf("paid-at-provider top-up must be credited by the sweeper, got %v", s)
	}
	if s := e.topupStatus(t, rina, unpaid["id"].(string)); s["status"] != "EXPIRED" {
		t.Fatalf("unpaid top-up must expire, got %v", s)
	}
	if balanceOf(t, e, rina) != "10000000" {
		t.Fatalf("balance = %s", balanceOf(t, e, rina))
	}
	assertLedgerHealthy(t, e)
}

// TestTopupRecover_StuckCreated_Completed (FS-10): provider gagal saat create dan client TIDAK retry.
// Job topup.recover melanjutkan dari recovery point; retry client kelak tidak membuat charge baru.
func TestTopupRecover_StuckCreated_Completed(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	e.mock.SetScenario(mockprovider.Scenario{Charges: "error_5xx"})
	key := newKey()
	_, tp := e.createTopup(t, rina, key, "10000000")
	if tp["checkout_url"] != nil {
		t.Fatal("precondition: provider failed, no checkout url")
	}
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok"})
	_, _ = e.pool.Exec(context.Background(), `UPDATE topups SET created_at = now() - interval '1 minute'`)
	e.tickAndDrain(t)
	if s := e.topupStatus(t, rina, tp["id"].(string)); s["checkout_url"] == nil {
		t.Fatalf("recover job must create the provider charge, got %v", s)
	}
	r, b := e.createTopup(t, rina, key, "10000000")
	if r.StatusCode != http.StatusCreated || b["checkout_url"] == nil || e.mock.ChargeCount() != 1 {
		t.Fatalf("later client retry must reuse the recovered charge: %d %v (charges %d)", r.StatusCode, b, e.mock.ChargeCount())
	}
}
