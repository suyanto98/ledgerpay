package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// ---------------------------------------------------------------- helpers

// unknownPayout: payout Rina ke rekening magic 0408 (bank memproses, respons timeout) → UNKNOWN.
func unknownPayout(t *testing.T, e env) (rina, payoutID string) {
	t.Helper()
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 10_000_000)
	_, po := e.createPayout(t, rina, "2000000", "9900000408")
	payoutID = po["id"].(string)
	_ = e.newWorker(t).RunOnce(context.Background())
	if s, _, _ := e.payoutRow(t, payoutID); s != "UNKNOWN" {
		t.Fatalf("setup: payout must be UNKNOWN, got %s", s)
	}
	return rina, payoutID
}

func (e env) resolve(t *testing.T, ops, payoutID, key string, body map[string]any) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/ops/payouts/"+payoutID+"/resolve", ops, map[string]string{"Idempotency-Key": key}, body)
}

func evidence(outcome string) map[string]any {
	return map[string]any{"outcome": outcome, "evidence_ref": "MOCKBANK-STMT-2026-10-04#L42", "reason": "Mutasi bank menunjukkan dana terkirim 10:02"}
}

type auditRow struct {
	actorType, action, targetType, reason string
	diff                                  map[string]any
}

func auditFor(t *testing.T, e env, targetID string) []auditRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT actor_type, action, target_type, coalesce(reason, ''), coalesce(diff, '{}') FROM audit_log WHERE target_id = $1 ORDER BY created_at`, targetID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		var raw []byte
		if err := rows.Scan(&a.actorType, &a.action, &a.targetType, &a.reason, &raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &a.diff)
		out = append(out, a)
	}
	return out
}

// ---------------------------------------------------------------- resolve payout (LF-18, ADR-L06)

// ops_resolution_requires_evidence: tanpa bukti/alasan → 422 RESOLUTION_EVIDENCE_REQUIRED; user biasa →
// 403; tidak ada yang berubah (status, journal, audit).
func TestOps_ResolutionRequiresEvidence(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	rina, id := unknownPayout(t, e)
	for name, body := range map[string]map[string]any{
		"no evidence":  {"outcome": "SUCCEEDED", "reason": "Mutasi bank menunjukkan dana terkirim"},
		"short reason": {"outcome": "SUCCEEDED", "evidence_ref": "STMT#1", "reason": "ok"},
	} {
		r, b := e.resolve(t, ops, id, newKey(), body)
		if r.StatusCode != http.StatusUnprocessableEntity || b["code"] != "RESOLUTION_EVIDENCE_REQUIRED" {
			t.Fatalf("%s: want 422 RESOLUTION_EVIDENCE_REQUIRED, got %d %v", name, r.StatusCode, b)
		}
	}
	if r, b := e.resolve(t, rina, id, newKey(), evidence("SUCCEEDED")); r.StatusCode != http.StatusForbidden || b["code"] != "FORBIDDEN" {
		t.Fatalf("non-OPS user: want 403, got %d %v", r.StatusCode, b)
	}
	if s, _, _ := e.payoutRow(t, id); s != "UNKNOWN" || journalsFor(t, e, id)["PAYOUT_SETTLE"] != 0 || len(auditFor(t, e, id)) != 0 {
		t.Fatalf("rejected resolutions must not change anything (status %s)", s)
	}
}

// ops_action_audited: resolve SUCCEEDED → journal PAYOUT_SETTLE (bukan edit saldo), resolution_source
// OPS + resolved_by, audit berisi bukti, di satu tx. Status check yang masih terjadwal menjadi no-op.
// Retry dengan key sama → replay; resolve ulang dengan key lain → 409.
func TestOps_ResolveUnknownPayout_SettledAndAudited(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	_, opsID := login2(t, e, "ops@example.com")
	_, id := unknownPayout(t, e)

	key := newKey()
	r, b := e.resolve(t, ops, id, key, evidence("SUCCEEDED"))
	if r.StatusCode != http.StatusOK || b["status"] != "SUCCEEDED" || b["resolution_source"] != "OPS" || b["resolved_by"] != opsID.String() {
		t.Fatalf("resolve: %d %v", r.StatusCode, b)
	}
	if j := journalsFor(t, e, id); j["PAYOUT_HOLD"] != 1 || j["PAYOUT_SETTLE"] != 1 || j["PAYOUT_REVERSAL"] != 0 {
		t.Fatalf("journals: %v", j)
	}
	var actorType string
	_ = e.pool.QueryRow(context.Background(), `SELECT actor_type FROM journals WHERE source_id = $1 AND purpose = 'PAYOUT_SETTLE'`, id).Scan(&actorType)
	if actorType != "OPS" {
		t.Fatalf("settle journal actor = %s, want OPS", actorType)
	}
	a := auditFor(t, e, id)
	if len(a) != 1 || a[0].actorType != "OPS" || a[0].action != "payout.manual_resolve" || a[0].diff["evidence_ref"] != "MOCKBANK-STMT-2026-10-04#L42" ||
		a[0].diff["outcome"] != "SUCCEEDED" || a[0].reason == "" {
		t.Fatalf("audit: %+v", a)
	}
	if r2, _ := e.resolve(t, ops, id, key, evidence("SUCCEEDED")); r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("same key must replay")
	}
	if r3, b3 := e.resolve(t, ops, id, newKey(), evidence("FAILED")); r3.StatusCode != http.StatusConflict || b3["code"] != "INVALID_STATE_TRANSITION" {
		t.Fatalf("resolving a final payout: want 409, got %d %v", r3.StatusCode, b3)
	}
	e.drain(t, e.newWorker(t), 5) // status check terjadwal: payout sudah final → no-op
	if j := journalsFor(t, e, id); j["PAYOUT_SETTLE"] != 1 {
		t.Fatalf("a later status check must not settle again: %v", j)
	}
	assertLedgerHealthy(t, e)
}

// Resolve FAILED → PAYOUT_REVERSAL, dana kembali ke wallet. PENDING (belum pernah dikirim) → 409.
func TestOps_ResolveFailed_ReversesHold(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	rina, id := unknownPayout(t, e)
	if r, b := e.resolve(t, ops, id, newKey(), evidence("FAILED")); r.StatusCode != http.StatusOK || b["status"] != "FAILED" {
		t.Fatalf("resolve FAILED: %d %v", r.StatusCode, b)
	}
	if balanceOf(t, e, rina) != "10000000" || journalsFor(t, e, id)["PAYOUT_REVERSAL"] != 1 {
		t.Fatalf("hold must be reversed: balance %s, journals %v", balanceOf(t, e, rina), journalsFor(t, e, id))
	}
	_, po := e.createPayout(t, rina, "1000000", "9900001234") // belum diproses worker → PENDING
	if r, b := e.resolve(t, ops, po["id"].(string), newKey(), evidence("SUCCEEDED")); r.StatusCode != http.StatusConflict {
		t.Fatalf("PENDING payout must not be resolvable by ops, got %d %v", r.StatusCode, b)
	}
	assertLedgerHealthy(t, e)
}

// Antrean ops: ?status=UNKNOWN hanya payout yang menggantung, dengan field ops + email user.
func TestOps_ListUnknownPayouts(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	rina, id := unknownPayout(t, e)
	e.createPayout(t, rina, "1000000", "9900001234") // PENDING, tidak boleh ikut
	r, b := e.do(t, http.MethodGet, "/api/v1/ops/payouts?status=UNKNOWN", ops, nil)
	data, _ := b["data"].([]any)
	if r.StatusCode != http.StatusOK || len(data) != 1 {
		t.Fatalf("list UNKNOWN: %d %v", r.StatusCode, b)
	}
	p := data[0].(map[string]any)
	if p["id"] != id || p["user"].(map[string]any)["email"] != "rina@example.com" || p["last_error"] == nil {
		t.Fatalf("ops payout view: %v", p)
	}
	if r, b := e.do(t, http.MethodGet, "/api/v1/ops/payouts?status=BOGUS", ops, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid status filter: %d %v", r.StatusCode, b)
	}
}

// ---------------------------------------------------------------- search & journal

func TestOps_SearchAndJournalDetail(t *testing.T) {
	e := newEnv(t)
	k, _, intentID, rina := paidIntent(t, e, "25000") // juga membuat user OPS
	ops, _ := login2(t, e, "ops@example.com")
	_, intent := e.doH(t, http.MethodGet, "/api/v1/merchant/payment-intents/"+intentID, k, nil, nil)

	search := func(q string) []any {
		t.Helper()
		r, b := e.do(t, http.MethodGet, "/api/v1/ops/transactions/search?q="+url.QueryEscape(q), ops, nil)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("search %q: %d %v", q, r.StatusCode, b)
		}
		d, _ := b["data"].([]any)
		return d
	}
	byEmail := search("RINA@example.com") // email tidak case-sensitive
	types := map[string]bool{}
	for _, h := range byEmail {
		types[h.(map[string]any)["object_type"].(string)] = true
	}
	if !types["payment_intent"] {
		t.Fatalf("search by email must find the user's objects, got %v", byEmail)
	}
	login(t, e, "andi@example.com")
	if r, b := e.transfer(t, rina, newKey(), transferBody("andi@example.com", "1000")); r.StatusCode != http.StatusCreated {
		t.Fatalf("transfer: %d %v", r.StatusCode, b)
	}
	if hits := search("andi@example.com"); len(hits) != 1 || hits[0].(map[string]any)["object_type"] != "transfer" {
		t.Fatalf("search by the RECEIVER's email must find the transfer: %v", hits)
	}
	byOrder := search(intent["merchant_order_id"].(string))
	if len(byOrder) != 1 || byOrder[0].(map[string]any)["id"] != intentID {
		t.Fatalf("search by merchant_order_id: %v", byOrder)
	}
	journalID := byOrder[0].(map[string]any)["journal_ids"].([]any)[0].(string)
	if byJournal := search(journalID); len(byJournal) != 1 || byJournal[0].(map[string]any)["object_type"] != "payment_intent" {
		t.Fatalf("search by journal id must return the source object: %v", byJournal)
	}

	r, j := e.do(t, http.MethodGet, "/api/v1/ops/journals/"+journalID, ops, nil)
	entries, _ := j["entries"].([]any)
	if r.StatusCode != http.StatusOK || j["purpose"] != "PAYMENT" || len(entries) != 2 {
		t.Fatalf("journal detail: %d %v", r.StatusCode, j)
	}
	owners := map[string]string{}
	for _, en := range entries {
		m := en.(map[string]any)
		owners[m["direction"].(string)] = m["owner"].(string)
	}
	if owners["D"] != "USER:rina@example.com" || owners["C"] != "MERCHANT:Kopi Budi" {
		t.Fatalf("entry owners: %v", owners)
	}
	if r, _ := e.do(t, http.MethodGet, "/api/v1/ops/transactions/search?q=x", rina, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("ops search as a normal user: want 403, got %d", r.StatusCode)
	}
	if r, _ := e.do(t, http.MethodGet, "/api/v1/ops/transactions/search", ops, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty q: want 400, got %d", r.StatusCode)
	}
}

// ---------------------------------------------------------------- dead jobs & webhook replay

// Merchant down → 7 percobaan → job dead + delivery DEAD. Ops: daftar dead, replay delivery (event_id
// sama, audit) → merchant sudah pulih → terkirim. Replay ulang → 409. Retry job dead → queued + audit.
func TestOps_ReplayDeadWebhookAndRetryJob(t *testing.T) {
	e := newEnv(t)
	s := newSink(t, http.StatusInternalServerError)
	paidIntentWithWebhook(t, e, s.srv.URL)
	ops, _ := login2(t, e, "ops@example.com")
	e.drain(t, e.newWorker(t), 12)

	_, jl := e.do(t, http.MethodGet, "/api/v1/ops/jobs?status=dead&kind=webhook.deliver", ops, nil)
	deadJobs, _ := jl["data"].([]any)
	if len(deadJobs) != 1 {
		t.Fatalf("want 1 dead webhook job, got %v", jl)
	}
	if _, ok := deadJobs[0].(map[string]any)["payload"]; ok {
		t.Fatal("job payload must never be shown to ops")
	}
	_, dl := e.do(t, http.MethodGet, "/api/v1/ops/webhook-deliveries?status=DEAD", ops, nil)
	dead, _ := dl["data"].([]any)
	if len(dead) != 1 {
		t.Fatalf("want 1 DEAD delivery, got %v", dl)
	}
	delivery := dead[0].(map[string]any)
	deliveryID, eventID := delivery["id"].(string), delivery["event_id"].(string)

	replayPath := "/api/v1/ops/webhook-deliveries/" + deliveryID + "/replay"
	if r, b := e.do(t, http.MethodPost, replayPath, ops, map[string]any{"reason": "x"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("short reason: want 400, got %d %v", r.StatusCode, b)
	}
	s.mu.Lock()
	s.status = http.StatusOK // merchant sudah pulih
	s.mu.Unlock()
	r, b := e.do(t, http.MethodPost, replayPath, ops, map[string]any{"reason": "Merchant confirmed endpoint is back up"})
	if r.StatusCode != http.StatusAccepted || b["event_id"] != eventID || b["status"] != "PENDING" {
		t.Fatalf("replay: %d %v", r.StatusCode, b)
	}
	if r2, b2 := e.do(t, http.MethodPost, replayPath, ops, map[string]any{"reason": "Merchant confirmed endpoint is back up"}); r2.StatusCode != http.StatusConflict {
		t.Fatalf("replaying a PENDING delivery: want 409, got %d %v", r2.StatusCode, b2)
	}
	e.drain(t, e.newWorker(t), 3)
	var status string
	_ = e.pool.QueryRow(context.Background(), `SELECT status FROM webhook_deliveries WHERE id = $1`, deliveryID).Scan(&status)
	if status != "DELIVERED" || s.got[len(s.got)-1].Header.Get("LedgerPay-Event-Id") != eventID {
		t.Fatalf("replayed delivery must arrive with the SAME event id; status=%s", status)
	}
	if a := auditFor(t, e, deliveryID); len(a) != 1 || a[0].action != "webhook.replay" {
		t.Fatalf("replay must be audited: %+v", a)
	}

	jobID := deadJobs[0].(map[string]any)["id"].(string)
	retryPath := "/api/v1/ops/jobs/" + jobID + "/retry"
	rr, rb := e.do(t, http.MethodPost, retryPath, ops, map[string]any{"reason": "Retry after merchant recovered"})
	if rr.StatusCode != http.StatusOK || rb["status"] != "queued" || rb["attempts"] != float64(0) {
		t.Fatalf("retry dead job: %d %v", rr.StatusCode, rb)
	}
	if rr2, _ := e.do(t, http.MethodPost, retryPath, ops, map[string]any{"reason": "Retry after merchant recovered"}); rr2.StatusCode != http.StatusConflict {
		t.Fatalf("retrying a queued job: want 409, got %d", rr2.StatusCode)
	}
	if a := auditFor(t, e, jobID); len(a) != 1 || a[0].action != "job.retry" {
		t.Fatalf("retry must be audited: %+v", a)
	}
	// Job yang di-retry menemukan delivery sudah DELIVERED → no-op (handler idempoten), tidak ada kiriman ganda.
	before := s.count()
	e.drain(t, e.newWorker(t), 3)
	if s.count() != before {
		t.Fatalf("retried job must not deliver again: %d → %d", before, s.count())
	}
}
