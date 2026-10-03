package app_test

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"ledgerpay/internal/app"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/idgen"
)

// ---------------------------------------------------------------- helpers

// opsToken membuat user OPS (lewat jalur CLI grant-ops) dan login.
func opsToken(t *testing.T, e env) string {
	t.Helper()
	register(t, e, "ops@example.com")
	if err := app.GrantOps(context.Background(), e.pool, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	token, _ := login2(t, e, "ops@example.com")
	return token
}

// login2: login untuk user yang sudah terdaftar.
func login2(t *testing.T, e env, email string) (string, uuid.UUID) {
	t.Helper()
	_, body := e.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": email, "password": password})
	token, _ := body["access_token"].(string)
	_, me := e.do(t, http.MethodGet, "/api/v1/me", token, nil)
	id, _ := me["id"].(string)
	return token, uuid.MustParse(id)
}

func newMerchant(t *testing.T, e env, name string) uuid.UUID {
	t.Helper()
	id, err := app.CreateMerchant(context.Background(), e.pool, idgen.V7{}, name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e env) issueKey(t *testing.T, ops string, merchantID uuid.UUID, key string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/ops/merchants/"+merchantID.String()+"/api-keys", ops,
		map[string]string{"Idempotency-Key": key}, map[string]any{"label": "server 1"})
}

// merchantKey menerbitkan API key baru dan mengembalikan nilai rahasianya.
func merchantKey(t *testing.T, e env, ops string, merchantID uuid.UUID) string {
	t.Helper()
	r, b := e.issueKey(t, ops, merchantID, newKey())
	k, _ := b["api_key"].(string)
	if r.StatusCode != http.StatusCreated || k == "" {
		t.Fatalf("issue key: %d %v", r.StatusCode, b)
	}
	return k
}

func (e env) createIntent(t *testing.T, apiKey, idemKey, orderID, amount string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/merchant/payment-intents", apiKey, map[string]string{"Idempotency-Key": idemKey},
		map[string]any{"merchant_order_id": orderID, "amount": amount, "currency": "IDR", "description": "Kopi susu x3"})
}

func (e env) confirm(t *testing.T, token, intentID, key string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/payment-intents/"+intentID+"/confirm", token, map[string]string{"Idempotency-Key": key}, map[string]any{})
}

func merchantBalance(t *testing.T, e env, merchantID uuid.UUID) int64 {
	t.Helper()
	w, err := ledger.NewService(idgen.V7{}).MerchantWalletOf(context.Background(), e.pool, merchantID)
	if err != nil {
		t.Fatal(err)
	}
	return w.Balance
}

// ---------------------------------------------------------------- API key (C-42)

// api_key_hashed_at_rest: yang tersimpan hanya SHA-256(secret); secret tidak ada di kolom mana pun,
// termasuk respons idempotency yang disimpan (replay mengembalikan api_key = null).
func TestAPIKey_HashedAtRest(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	m := newMerchant(t, e, "Kopi Budi")
	idem := newKey()
	r1, b1 := e.issueKey(t, ops, m, idem)
	apiKey, _ := b1["api_key"].(string)
	if r1.StatusCode != http.StatusCreated || !strings.HasPrefix(apiKey, "lp_test_") || b1["slot"] != float64(1) {
		t.Fatalf("issue: %d %v", r1.StatusCode, b1)
	}
	parts := strings.SplitN(apiKey, "_", 4)
	secret := parts[3]

	var stored []byte
	var keysRow, idemRow string
	ctx := context.Background()
	_ = e.pool.QueryRow(ctx, `SELECT key_hash, row_to_json(k)::text FROM merchant_api_keys k WHERE prefix = $1`, parts[2]).Scan(&stored, &keysRow)
	_ = e.pool.QueryRow(ctx, `SELECT coalesce(string_agg(response_body::text, ''), '') FROM idempotency_keys`).Scan(&idemRow)
	sum := sha256.Sum256([]byte(secret))
	if string(stored) != string(sum[:]) {
		t.Fatal("key_hash must be SHA-256(secret)")
	}
	if strings.Contains(keysRow, secret) || strings.Contains(idemRow, secret) {
		t.Fatal("secret must not be stored in plaintext anywhere")
	}

	r2, b2 := e.issueKey(t, ops, m, idem) // replay
	if r2.StatusCode != http.StatusCreated || r2.Header.Get("Idempotent-Replayed") != "true" || b2["api_key"] != nil || b2["id"] != b1["id"] {
		t.Fatalf("replay must return the same key id without the secret, got %d %v", r2.StatusCode, b2)
	}
	if r, b := e.createIntent(t, apiKey, newKey(), "ORD-1", "5000"); r.StatusCode != http.StatusCreated {
		t.Fatalf("issued key must authenticate, got %d %v", r.StatusCode, b)
	}
}

// api_key_slot_limit: maksimal 2 key aktif; revoke membebaskan slot; key yang dicabut langsung mati.
func TestAPIKey_SlotLimitAndRevoke(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	m := newMerchant(t, e, "Kopi Budi")
	_, k1 := e.issueKey(t, ops, m, newKey())
	merchantKey(t, e, ops, m)
	if r, b := e.issueKey(t, ops, m, newKey()); r.StatusCode != http.StatusConflict || b["code"] != "API_KEY_LIMIT_REACHED" {
		t.Fatalf("third active key: want 409 API_KEY_LIMIT_REACHED, got %d %v", r.StatusCode, b)
	}

	revokePath := "/api/v1/ops/api-keys/" + k1["id"].(string) + "/revoke"
	if r, b := e.do(t, http.MethodPost, revokePath, ops, map[string]string{"reason": "short"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("revoke without a real reason: want 400, got %d %v", r.StatusCode, b)
	}
	r, b := e.do(t, http.MethodPost, revokePath, ops, map[string]string{"reason": "leaked in a public repo"})
	if r.StatusCode != http.StatusOK || b["revoked_at"] == nil {
		t.Fatalf("revoke: %d %v", r.StatusCode, b)
	}
	if r2, b2 := e.do(t, http.MethodPost, revokePath, ops, map[string]string{"reason": "leaked in a public repo"}); r2.StatusCode != http.StatusOK || b2["revoked_at"] != b["revoked_at"] {
		t.Fatalf("revoke must be idempotent (same revoked_at), got %d %v", r2.StatusCode, b2)
	}
	if r, _ := e.createIntent(t, k1["api_key"].(string), newKey(), "ORD-1", "5000"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key must be rejected, got %d", r.StatusCode)
	}
	if r, b := e.issueKey(t, ops, m, newKey()); r.StatusCode != http.StatusCreated || b["slot"] != k1["slot"] {
		t.Fatalf("revoke frees the slot: want 201 in slot %v, got %d %v", k1["slot"], r.StatusCode, b)
	}
}

// Ops hanya untuk role OPS; API key tidak bisa mengelola API key (tidak ada /merchant/api-keys).
func TestOps_RoleRequiredAndNoSelfServiceKeys(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	user, _ := login(t, e, "rina@example.com")
	m := newMerchant(t, e, "Kopi Budi")
	if r, b := e.issueKey(t, user, m, newKey()); r.StatusCode != http.StatusForbidden || b["code"] != "FORBIDDEN" {
		t.Fatalf("non-OPS user: want 403, got %d %v", r.StatusCode, b)
	}
	k := merchantKey(t, e, ops, m)
	r, _ := e.doH(t, http.MethodPost, "/api/v1/merchant/api-keys", k, map[string]string{"Idempotency-Key": newKey()}, map[string]any{})
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("a merchant key must not be able to mint keys (no such endpoint), got %d", r.StatusCode)
	}
	for _, bad := range []string{"", "lp_test_deadbeef_" + strings.Repeat("A", 32), "not-a-key"} {
		if r, _ := e.createIntent(t, bad, newKey(), "ORD-1", "5000"); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("bad key %q: want 401, got %d", bad, r.StatusCode)
		}
	}
}

// ---------------------------------------------------------------- payment intent (LF-10)

// Demo Sprint 4: merchant membuat intent → user konfirmasi → saldo pindah.
func TestPaymentIntent_CreateConfirmMovesMoney(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	m := newMerchant(t, e, "Kopi Budi")
	k := merchantKey(t, e, ops, m)
	rina, rinaID := loginAs(t, e, "rina@example.com", "Rina")
	eve, _ := login(t, e, "eve@example.com")
	seedTopup(t, e, rinaID, 100_000)

	r, pi := e.createIntent(t, k, newKey(), "ORD-2026-000123", "75000")
	id, _ := pi["id"].(string)
	if r.StatusCode != http.StatusCreated || pi["status"] != "REQUIRES_CONFIRMATION" || pi["checkout_url"] != "http://localhost:5173/checkout/"+id {
		t.Fatalf("create: %d %v", r.StatusCode, pi)
	}
	_, view := e.do(t, http.MethodGet, "/api/v1/payment-intents/"+id, eve, nil) // capability URL sebelum bayar
	if view["amount"] != "75000" || view["checkout_url"] != nil || view["merchant"].(map[string]any)["id"] != nil {
		t.Fatalf("pre-payment view must show checkout data only: %v", view)
	}

	rc, paid := e.confirm(t, rina, id, newKey())
	if rc.StatusCode != http.StatusOK || paid["status"] != "SUCCEEDED" {
		t.Fatalf("confirm: %d %v", rc.StatusCode, paid)
	}
	if balanceOf(t, e, rina) != "25000" || merchantBalance(t, e, m) != 75_000 {
		t.Fatalf("money must move: rina=%s merchant=%d", balanceOf(t, e, rina), merchantBalance(t, e, m))
	}
	_, mv := e.doH(t, http.MethodGet, "/api/v1/merchant/payment-intents/"+id, k, nil, nil)
	if payer, _ := mv["payer"].(map[string]any); payer["display_name"] != "Rina" {
		t.Fatalf("merchant view must show the payer name: %v", mv)
	}
	if r, _ := e.do(t, http.MethodGet, "/api/v1/payment-intents/"+id, eve, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("after payment only the payer may view it, got %d", r.StatusCode)
	}
	assertLedgerHealthy(t, e)
}

// TestPaymentIntent_DuplicateMerchantOrder_409: order sama + key beda → 409 + existing_resource_id;
// key sama (retry setelah timeout) → replay 201 (FS-09).
func TestPaymentIntent_DuplicateMerchantOrder_409(t *testing.T) {
	e := newEnv(t)
	k := merchantKey(t, e, opsToken(t, e), newMerchant(t, e, "Kopi Budi"))
	idem := newKey()
	_, first := e.createIntent(t, k, idem, "ORD-7", "5000")
	r, dup := e.createIntent(t, k, newKey(), "ORD-7", "5000")
	if r.StatusCode != http.StatusConflict || dup["code"] != "DUPLICATE_MERCHANT_ORDER" || dup["existing_resource_id"] != first["id"] {
		t.Fatalf("want 409 DUPLICATE_MERCHANT_ORDER with existing id %v, got %d %v", first["id"], r.StatusCode, dup)
	}
	r2, replay := e.createIntent(t, k, idem, "ORD-7", "5000")
	if r2.StatusCode != http.StatusCreated || r2.Header.Get("Idempotent-Replayed") != "true" || replay["id"] != first["id"] {
		t.Fatalf("same key must replay, got %d %v", r2.StatusCode, replay)
	}
}

// bola_payment_intent: merchant B tidak bisa membaca intent merchant A (404 yang sama dengan ID acak).
func TestPaymentIntent_BOLA_MerchantToMerchant(t *testing.T) {
	e := newEnv(t)
	ops := opsToken(t, e)
	ka := merchantKey(t, e, ops, newMerchant(t, e, "Merchant A"))
	kb := merchantKey(t, e, ops, newMerchant(t, e, "Merchant B"))
	_, pi := e.createIntent(t, ka, newKey(), "ORD-1", "5000")
	for _, id := range []string{pi["id"].(string), uuid.NewString()} {
		r, b := e.doH(t, http.MethodGet, "/api/v1/merchant/payment-intents/"+id, kb, nil, nil)
		if r.StatusCode != http.StatusNotFound || b["code"] != "NOT_FOUND" {
			t.Fatalf("merchant B reading %s: want 404, got %d %v", id, r.StatusCode, b)
		}
	}
}

// TestConfirm_ParallelTabs_OnePayment (LI-13): dua tab, dua key berbeda, bersamaan → satu 200,
// satu 409 INVALID_STATE_TRANSITION, payer terdebet sekali.
func TestConfirm_ParallelTabs_OnePayment(t *testing.T) {
	e := newEnv(t)
	m := newMerchant(t, e, "Kopi Budi")
	k := merchantKey(t, e, opsToken(t, e), m)
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 100_000)
	_, pi := e.createIntent(t, k, newKey(), "ORD-1", "30000")
	id := pi["id"].(string)

	statuses := runConcurrently(2, func(int) int {
		r, _ := e.confirm(t, rina, id, newKey())
		return r.StatusCode
	})
	if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("want one 200 and one 409, got %v", statuses)
	}
	if balanceOf(t, e, rina) != "70000" || merchantBalance(t, e, m) != 30_000 {
		t.Fatal("payer must be debited exactly once")
	}
	var journals int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM journals WHERE purpose = 'PAYMENT'`).Scan(&journals)
	if journals != 1 {
		t.Fatalf("PAYMENT journals = %d, want 1", journals)
	}
	assertLedgerHealthy(t, e)
}

// Intent kedaluwarsa → 422 PAYMENT_EXPIRED (disimpan: key yang sama me-replay 422). Saldo kurang → 422.
func TestConfirm_ExpiredAndInsufficient(t *testing.T) {
	e := newEnv(t)
	k := merchantKey(t, e, opsToken(t, e), newMerchant(t, e, "Kopi Budi"))
	rina, rinaID := login(t, e, "rina@example.com")
	seedTopup(t, e, rinaID, 1_000)

	_, big := e.createIntent(t, k, newKey(), "ORD-BIG", "5000")
	if r, b := e.confirm(t, rina, big["id"].(string), newKey()); r.StatusCode != http.StatusUnprocessableEntity || b["code"] != "INSUFFICIENT_BALANCE" {
		t.Fatalf("insufficient: got %d %v", r.StatusCode, b)
	}

	_, pi := e.createIntent(t, k, newKey(), "ORD-OLD", "500")
	id := pi["id"].(string)
	if _, err := e.pool.Exec(context.Background(), `UPDATE payment_intents SET expires_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, view := e.do(t, http.MethodGet, "/api/v1/payment-intents/"+id, rina, nil); view["status"] != "EXPIRED" {
		t.Fatalf("overdue intent must be shown as EXPIRED even before the sweeper runs: %v", view)
	}
	key := newKey()
	r1, b1 := e.confirm(t, rina, id, key)
	r2, _ := e.confirm(t, rina, id, key)
	if r1.StatusCode != http.StatusUnprocessableEntity || b1["code"] != "PAYMENT_EXPIRED" || r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("expired: want stored 422 PAYMENT_EXPIRED, got %d %v (replayed=%q)", r1.StatusCode, b1, r2.Header.Get("Idempotent-Replayed"))
	}
	if balanceOf(t, e, rina) != "1000" {
		t.Fatal("rejected confirms must not move money")
	}
}
