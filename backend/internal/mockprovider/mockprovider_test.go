package mockprovider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func createCharge(t *testing.T, h http.Handler, key, ref string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"amount": "1000000", "currency": "IDR", "reference": ref, "callback_url": "http://x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/charges", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var c Charge
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return c.ID
}

// Database LedgerPay tidak ikut di-reset: ID charge tidak boleh terulang setelah /admin/reset atau
// restart, kalau tidak uq_topups_provider_ref bentrok (temuan E2E Sprint 5).
func TestChargeIDsUniqueAcrossReset(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	m := New(Config{APISecret: "s", WebhookSecret: "w", Now: func() time.Time { now = now.Add(time.Millisecond); return now }})
	defer m.Close()
	h := m.Handler()
	first := createCharge(t, h, "k1", "r1")
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/admin/reset", nil))
	second := createCharge(t, h, "k1", "r1")
	restarted := New(Config{APISecret: "s", WebhookSecret: "w", Now: func() time.Time { now = now.Add(time.Millisecond); return now }})
	defer restarted.Close()
	third := createCharge(t, restarted.Handler(), "k1", "r1")
	if first == second || second == third || first == third {
		t.Fatalf("charge ids must never repeat: %s %s %s", first, second, third)
	}
}

// Idempotency provider: key + body sama → charge sama; key sama + body beda → 422.
func TestCreateChargeIdempotent(t *testing.T) {
	m := New(Config{APISecret: "s", WebhookSecret: "w"})
	defer m.Close()
	h := m.Handler()
	a, b := createCharge(t, h, "k", "r"), createCharge(t, h, "k", "r")
	if a != b || m.ChargeCount() != 1 {
		t.Fatalf("same key+body must return the same charge: %s %s (count %d)", a, b, m.ChargeCount())
	}
	body, _ := json.Marshal(map[string]any{"amount": "2000000", "currency": "IDR", "reference": "r", "callback_url": "http://x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/charges", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different body: want 422, got %d", rec.Code)
	}
}
