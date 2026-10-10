package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"ledgerpay/internal/app"
	"ledgerpay/internal/mockprovider"
	"ledgerpay/internal/modules/inbox"
	"ledgerpay/internal/platform/webhooksig"
)

// ---------------------------------------------------------------- helpers

func (e env) createTopup(t *testing.T, token, key, amount string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/topups", token, map[string]string{"Idempotency-Key": key},
		map[string]any{"amount": amount, "currency": "IDR"})
}

func chargeIDOf(t *testing.T, topup map[string]any) string {
	t.Helper()
	u, _ := topup["checkout_url"].(string)
	if u == "" {
		t.Fatalf("topup has no checkout_url: %v", topup)
	}
	return u[strings.LastIndex(u, "/")+1:]
}

// waitFor menunggu kondisi (contoh: mock sudah mengirim webhook) tanpa sleep tetap.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e env) deliveries(chargeID string) []mockprovider.Delivery {
	var out []mockprovider.Delivery
	for _, d := range e.mock.Deliveries() {
		if d.ChargeID == chargeID {
			out = append(out, d)
		}
	}
	return out
}

// worker menjalankan satu putaran pemroses inbox (pengganti `ledgerpay worker` di test).
func (e env) worker(t *testing.T, id string) *inbox.Processor {
	t.Helper()
	return app.InboxProcessor(e.deps, inbox.ProcessorConfig{WorkerID: id})
}

func (e env) runWorker(t *testing.T) {
	t.Helper()
	if _, err := e.worker(t, "w-test").RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (e env) topupStatus(t *testing.T, token, id string) map[string]any {
	t.Helper()
	_, b := e.do(t, http.MethodGet, "/api/v1/topups/"+id, token, nil)
	return b
}

func (e env) inboxCount(t *testing.T, where string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_inbox WHERE `+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// postWebhook mengirim webhook yang ditandatangani sendiri (meniru penyerang yang tahu secret, atau replay).
func (e env) postWebhook(t *testing.T, secret string, at time.Time, payload map[string]any) (result, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/api/v1/webhooks/provider", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Provider-Signature", webhooksig.Sign([]byte(secret), at, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return result{StatusCode: resp.StatusCode, Header: resp.Header}, out
}

func chargeEvent(id, typ, chargeID, reference, amount string) map[string]any {
	return map[string]any{"id": id, "type": typ, "created_at": time.Now().UTC(), "data": map[string]any{
		"charge_id": chargeID, "reference": reference, "amount": amount, "currency": "IDR", "status": "SUCCEEDED",
	}}
}

// paidTopup membuat top-up, membayarnya di mock, menunggu webhook, dan menjalankan worker.
func paidTopup(t *testing.T, e env, token, amount string) (topupID, chargeID string) {
	t.Helper()
	r, tp := e.createTopup(t, token, newKey(), amount)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create topup: %d %v", r.StatusCode, tp)
	}
	chargeID = chargeIDOf(t, tp)
	if err := e.mock.Complete(chargeID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "webhook delivery", func() bool { return len(e.deliveries(chargeID)) > 0 })
	e.runWorker(t)
	return tp["id"].(string), chargeID
}

// ---------------------------------------------------------------- end-to-end (DoD Sprint 5)

// Top-up end-to-end: POST /topups tidak menambah saldo; saldo bertambah sekali setelah webhook
// terverifikasi diproses worker.
func TestTopup_EndToEnd_CreditedOnce(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	r, tp := e.createTopup(t, rina, newKey(), "10000000")
	if r.StatusCode != http.StatusCreated || tp["status"] != "PENDING" || tp["checkout_url"] == nil {
		t.Fatalf("create: %d %v", r.StatusCode, tp)
	}
	if balanceOf(t, e, rina) != "0" {
		t.Fatal("creating a top-up must never credit the wallet")
	}
	chargeID := chargeIDOf(t, tp)
	if err := e.mock.Complete(chargeID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "webhook delivery", func() bool { return len(e.deliveries(chargeID)) == 1 })
	if balanceOf(t, e, rina) != "0" {
		t.Fatal("the webhook handler must not credit; only the worker does")
	}
	e.runWorker(t)
	if s := e.topupStatus(t, rina, tp["id"].(string)); s["status"] != "SUCCEEDED" || balanceOf(t, e, rina) != "10000000" {
		t.Fatalf("after worker: topup=%v balance=%s", s, balanceOf(t, e, rina))
	}
	assertLedgerHealthy(t, e)
}

// FS-06 / LI-09: webhook_duplicate:3 (magic amount Rp11.111) → tiga webhook, satu baris inbox, satu journal.
func TestTopup_DuplicateWebhook_OneJournal(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	_, tp := e.createTopup(t, rina, newKey(), "1111100")
	chargeID := chargeIDOf(t, tp)
	_ = e.mock.Complete(chargeID, true)
	waitFor(t, "3 deliveries", func() bool { return len(e.deliveries(chargeID)) == 3 })
	for _, d := range e.deliveries(chargeID) {
		if d.Status != http.StatusOK {
			t.Fatalf("every duplicate must still get 200 (so the provider stops retrying), got %+v", d)
		}
	}
	e.runWorker(t)
	e.runWorker(t)
	if n := e.inboxCount(t, "true"); n != 1 {
		t.Fatalf("inbox rows = %d, want 1 (dedupe on provider_event_id)", n)
	}
	var journals int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM journals WHERE purpose = 'TOPUP_CREDIT'`).Scan(&journals)
	if journals != 1 || balanceOf(t, e, rina) != "1111100" {
		t.Fatalf("want exactly one credit: journals=%d balance=%s", journals, balanceOf(t, e, rina))
	}
}

// webhook_signature_invalid_rejected: skenario webhook_bad_signature → 401, tidak tersimpan, tanpa efek.
func TestWebhook_BadSignatureRejected(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Webhooks: []string{"webhook_bad_signature"}})
	_, tp := e.createTopup(t, rina, newKey(), "10000000")
	chargeID := chargeIDOf(t, tp)
	_ = e.mock.Complete(chargeID, true)
	waitFor(t, "delivery", func() bool { return len(e.deliveries(chargeID)) > 0 })
	if d := e.deliveries(chargeID); len(d) != 1 || d[0].Status != http.StatusUnauthorized {
		t.Fatalf("bad signature: want exactly one attempt answered 401 (4xx is not retried), got %+v", d)
	}
	if n := e.inboxCount(t, "true"); n != 0 || balanceOf(t, e, rina) != "0" {
		t.Fatal("a rejected webhook must leave no trace and no credit")
	}
}

// webhook_replay_outside_window_rejected: rekaman webhook sah dari 6 menit lalu → 400 STALE_WEBHOOK.
func TestWebhook_ReplayOutsideWindowRejected(t *testing.T) {
	e := newEnv(t)
	ev := chargeEvent("evt_replay", "charge.succeeded", "ch_x", uuid.NewString(), "10000000")
	r, b := e.postWebhook(t, testWebhookSecret, e.clk.Now().Add(-6*time.Minute), ev)
	if r.StatusCode != http.StatusBadRequest || b["code"] != "STALE_WEBHOOK" {
		t.Fatalf("old webhook: want 400 STALE_WEBHOOK, got %d %v", r.StatusCode, b)
	}
	if r, _ := e.postWebhook(t, testWebhookSecret, e.clk.Now(), ev); r.StatusCode != http.StatusOK {
		t.Fatalf("the same event signed now must be accepted, got %d", r.StatusCode)
	}
	if r, b := e.postWebhook(t, testWebhookSecret, e.clk.Now(), ev); r.StatusCode != http.StatusOK || b["duplicate"] != true {
		t.Fatalf("replay inside the window is deduplicated (200 duplicate), got %d %v", r.StatusCode, b)
	}
}

// ---------------------------------------------------------------- recovery point (FS-05/FS-10)

// TestTopUp_ProviderTimeout_RetryResumesFromRecoveryPoint: provider SUDAH membuat charge tetapi
// LedgerPay timeout (timeout_after_process). Respons 201 checkout_url null, tidak disimpan. Retry
// dengan key yang sama melanjutkan dari recovery point CREATED, memanggil provider dengan key yang
// sama (= topup id), dan mendapat charge yang SAMA: tidak ada charge ganda di provider.
func TestTopUp_ProviderTimeout_RetryResumesFromRecoveryPoint(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	e.mock.SetScenario(mockprovider.Scenario{Charges: "timeout_after_process"})
	key := newKey()
	r1, b1 := e.createTopup(t, rina, key, "10000000")
	if r1.StatusCode != http.StatusCreated || b1["checkout_url"] != nil || b1["status"] != "PENDING" {
		t.Fatalf("timeout: want 201 PENDING with checkout_url null, got %d %v", r1.StatusCode, b1)
	}
	if e.mock.ChargeCount() != 1 {
		t.Fatalf("precondition: the provider did create the charge (outcome unknown to LedgerPay)")
	}

	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok"})
	r2, b2 := e.createTopup(t, rina, key, "10000000")
	if r2.StatusCode != http.StatusCreated || b2["checkout_url"] == nil || b2["id"] != b1["id"] || r2.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("retry must resume the SAME top-up and obtain the checkout url, got %d %v", r2.StatusCode, b2)
	}
	if n := e.mock.ChargeCount(); n != 1 {
		t.Fatalf("provider charges = %d, want 1 (idempotency key to provider = topup id)", n)
	}
	r3, b3 := e.createTopup(t, rina, key, "10000000")
	if r3.Header.Get("Idempotent-Replayed") != "true" || b3["checkout_url"] != b2["checkout_url"] {
		t.Fatalf("after completion the same key replays, got %d %v", r3.StatusCode, b3)
	}
}

// ---------------------------------------------------------------- transisi monoton (FS-07)

// TestTopup_LateWebhookAfterExpiry_Credited: EXPIRED adalah status lunak. Webhook sukses yang datang
// setelah sweeper menandai EXPIRED tetap dikreditkan (uang user sudah dibayar ke provider).
func TestTopup_LateWebhookAfterExpiry_Credited(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	_, tp := e.createTopup(t, rina, newKey(), "10000000")
	id, chargeID := tp["id"].(string), chargeIDOf(t, tp)
	if _, err := e.pool.Exec(context.Background(), `UPDATE topups SET status = 'EXPIRED', expired_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	_ = e.mock.Complete(chargeID, true)
	waitFor(t, "delivery", func() bool { return len(e.deliveries(chargeID)) > 0 })
	e.runWorker(t)
	if s := e.topupStatus(t, rina, id); s["status"] != "SUCCEEDED" || balanceOf(t, e, rina) != "10000000" {
		t.Fatalf("late success must be credited: %v balance=%s", s, balanceOf(t, e, rina))
	}
}

// Urutan kedatangan tidak relevan: event lama (charge.pending) setelah sukses → STALE; charge.failed
// untuk top-up yang sudah SUCCEEDED → CONFLICT, TIDAK dibalik.
func TestTopup_OutOfOrderAndConflict_NeverReversed(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	e.mock.SetScenario(mockprovider.Scenario{Charges: "ok", Webhooks: []string{"webhook_out_of_order"}})
	topupID, chargeID := paidTopup(t, e, rina, "10000000")
	waitFor(t, "stale event", func() bool { return len(e.deliveries(chargeID)) == 2 })
	e.runWorker(t)
	if n := e.inboxCount(t, "status = 'IGNORED' AND outcome = 'STALE'"); n != 1 {
		t.Fatalf("out-of-order charge.pending must be STALE, got %d", n)
	}

	failed := chargeEvent("evt_conflict", "charge.failed", chargeID, topupID, "10000000")
	if r, _ := e.postWebhook(t, testWebhookSecret, e.clk.Now(), failed); r.StatusCode != http.StatusOK {
		t.Fatalf("post: %d", r.StatusCode)
	}
	e.runWorker(t)
	if n := e.inboxCount(t, "status = 'PROCESSED' AND outcome = 'CONFLICT'"); n != 1 {
		t.Fatalf("failed-after-succeeded must be recorded as CONFLICT, got %d", n)
	}
	if s := e.topupStatus(t, rina, topupID); s["status"] != "SUCCEEDED" || balanceOf(t, e, rina) != "10000000" {
		t.Fatalf("a conflict must never reverse a final status: %v", s)
	}
	assertLedgerHealthy(t, e)
}

// ---------------------------------------------------------------- verify-by-fetch (amandemen ADR-L08)

// Penyerang yang MENGETAHUI secret webhook mengirim charge.succeeded bertanda tangan sah untuk
// top-up yang belum dibayar. HMAC lolos, tetapi verify-by-fetch bertanya ke provider: charge masih
// PENDING → tidak ada kredit (event dijadwalkan ulang). Charge yang tidak dikenal provider → DEAD.
func TestInbox_VerifyByFetch_ForgedWebhookNoCredit(t *testing.T) {
	e := newEnv(t)
	rina, _ := login(t, e, "rina@example.com")
	_, tp := e.createTopup(t, rina, newKey(), "10000000")
	topupID, chargeID := tp["id"].(string), chargeIDOf(t, tp) // TIDAK dibayar di provider

	forged := chargeEvent("evt_forged", "charge.succeeded", chargeID, topupID, "10000000")
	if r, _ := e.postWebhook(t, testWebhookSecret, e.clk.Now(), forged); r.StatusCode != http.StatusOK {
		t.Fatalf("signature is valid, so the webhook is accepted into the inbox; got %d", r.StatusCode)
	}
	e.runWorker(t)
	if balanceOf(t, e, rina) != "0" || e.inboxCount(t, "status = 'RECEIVED' AND attempts = 1") != 1 {
		t.Fatal("forged success for an unpaid charge must not credit; it is retried later")
	}

	ghost := chargeEvent("evt_ghost", "charge.succeeded", "ch_999999", uuid.NewString(), "10000000")
	_, _ = e.postWebhook(t, testWebhookSecret, e.clk.Now(), ghost)
	e.runWorker(t)
	if e.inboxCount(t, "status = 'DEAD' AND last_error LIKE 'verify-by-fetch%'") != 1 {
		t.Fatal("an event for a charge the provider does not know must go DEAD")
	}
}
