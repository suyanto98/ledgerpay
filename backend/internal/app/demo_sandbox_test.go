//go:build demo

package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ledgerpay/internal/app"
	"ledgerpay/internal/platform/ratelimit"
)

// demoServer: API dengan DEMO_MODE=true (lapis runtime) di atas env test biasa.
func demoServer(t *testing.T, e env, tweak func(*app.Deps)) *httptest.Server {
	t.Helper()
	d := e.deps
	d.DemoMode, d.DemoToken = true, "demo-token-0123456789abcdef"
	if tweak != nil {
		tweak(&d)
	}
	srv := httptest.NewServer(app.APIRouter(d))
	t.Cleanup(srv.Close)
	return srv
}

// startDemo: klik "Coba demo" dari SPA (X-Requested-With: fetch). Mengembalikan status, access token, cookie refresh.
func startDemo(t *testing.T, e env, srv *httptest.Server, ip string) (int, string, *http.Cookie) {
	t.Helper()
	ee := e
	ee.srv = srv
	h := map[string]string{"X-Requested-With": "fetch", "X-Forwarded-For": ip}
	r, b := ee.doH(t, http.MethodPost, "/api/v1/demo/sessions", "", h, nil)
	token, _ := b["access_token"].(string)
	var refresh *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == "lp_refresh" {
			refresh = c
		}
	}
	return r.StatusCode, token, refresh
}

// Satu klik → login sebagai Budi dengan saldo, riwayat berlabel, kontak, dan tagihan menunggu. Invariant
// ledger dicek otomatis di akhir test (t.Cleanup newEnv): data sandbox sama sahnya dengan data asli.
func TestDemoSandbox_OneClickSessionWithLabelledHistory(t *testing.T) {
	e := newEnv(t)
	srv := demoServer(t, e, nil)
	ee := e
	ee.srv = srv

	if r, b := ee.do(t, http.MethodGet, "/api/v1/demo", "", nil); r.StatusCode != http.StatusOK || b["sandbox"] != true || b["ttl_seconds"] != float64(86400) {
		t.Fatalf("GET /demo (feature discovery): %d %v", r.StatusCode, b)
	}
	if r, _ := ee.do(t, http.MethodPost, "/api/v1/demo/sessions", "", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("without X-Requested-With (CSRF guard): want 403, got %d", r.StatusCode)
	}
	status, token, refresh := startDemo(t, e, srv, "203.0.113.10")
	if status != http.StatusOK || token == "" || refresh == nil || !refresh.HttpOnly {
		t.Fatalf("demo session: status %d, token %q, refresh cookie %v", status, token, refresh)
	}
	_, me := ee.do(t, http.MethodGet, "/api/v1/me", token, nil)
	if me["display_name"] != "Budi Santoso" {
		t.Fatalf("me: %v", me)
	}
	// 2.500.000 + 150.000 − 75.000 − 45.000 + 250.000 − 50.000 − 38.000 = Rp2.692.000
	if b := balanceOf(t, ee, token); b != "269200000" {
		t.Fatalf("balance %s, want 269200000", b)
	}
	// Ringkasan bulan ini: masuk 2.500.000 + 150.000 + 250.000, keluar 75.000 + 45.000 + 50.000 + 38.000.
	if _, sum := ee.do(t, http.MethodGet, "/api/v1/wallet/summary", token, nil); sum["in_amount"] != "290000000" ||
		sum["out_amount"] != "20800000" || sum["transactions"] != float64(7) {
		t.Fatalf("wallet summary: %v", sum)
	}
	_, h := ee.do(t, http.MethodGet, "/api/v1/wallet/transactions", token, nil)
	items := h["data"].([]any)
	if len(items) != 7 { // saldo awal + 6 cerita
		t.Fatalf("history: %d items, want 7", len(items))
	}
	latest := items[0].(map[string]any)
	if latest["type"] != "PAYMENT" || latest["counterparty"] != "Kopi Senja" || latest["note"] != "Croissant + Americano" {
		t.Fatalf("latest item: %v", latest)
	}
	found := false
	for _, it := range items {
		m := it.(map[string]any)
		if m["counterparty"] == "Siti Rahma" && m["note"] == "Patungan makan siang" && m["direction"] == "IN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("transfer from Siti not labelled: %v", items)
	}

	_, sb := ee.do(t, http.MethodGet, "/api/v1/demo/sandbox", token, nil)
	contacts, _ := sb["contacts"].([]any)
	bill, _ := sb["pending_bill"].(map[string]any)
	if len(contacts) != 3 || bill["status"] != "REQUIRES_CONFIRMATION" || bill["amount"] != "5800000" || bill["merchant_name"] != "Kopi Senja" {
		t.Fatalf("sandbox info: %v", sb)
	}
	// Kontak bisa langsung dipakai di form transfer biasa (endpoint asli, bukan jalur demo).
	siti := contacts[0].(map[string]any)["email"].(string)
	if r, b := ee.transfer(t, token, newKey(), transferBody(siti, "1000000")); r.StatusCode != http.StatusCreated {
		t.Fatalf("transfer to contact: %d %v", r.StatusCode, b)
	}
	// Tagihan menunggu dibayar lewat endpoint confirm asli.
	if r, b := ee.confirm(t, token, bill["id"].(string), newKey()); r.StatusCode != http.StatusOK {
		t.Fatalf("pay pending bill: %d %v", r.StatusCode, b)
	}
	// User biasa (bukan sandbox) tidak punya info sandbox.
	rina, _ := login(t, ee, "rina@example.com")
	if r, _ := ee.do(t, http.MethodGet, "/api/v1/demo/sandbox", rina, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("non-sandbox user: want 404, got %d", r.StatusCode)
	}
}

// Dua pengunjung = dua dunia: email dan kontak berbeda, riwayat tidak bercampur.
func TestDemoSandbox_VisitorsAreIsolated(t *testing.T) {
	e := newEnv(t)
	srv := demoServer(t, e, nil)
	ee := e
	ee.srv = srv
	_, a, _ := startDemo(t, e, srv, "203.0.113.11")
	_, b, _ := startDemo(t, e, srv, "203.0.113.12")
	_, meA := ee.do(t, http.MethodGet, "/api/v1/me", a, nil)
	_, meB := ee.do(t, http.MethodGet, "/api/v1/me", b, nil)
	if meA["email"] == meB["email"] {
		t.Fatalf("two visitors share one account: %v", meA["email"])
	}
	_, sa := ee.do(t, http.MethodGet, "/api/v1/demo/sandbox", a, nil)
	_, sbx := ee.do(t, http.MethodGet, "/api/v1/demo/sandbox", b, nil)
	ca := sa["contacts"].([]any)[0].(map[string]any)["email"]
	cb := sbx["contacts"].([]any)[0].(map[string]any)["email"]
	if ca == cb {
		t.Fatalf("visitors share contacts: %v", ca)
	}
	// B tidak bisa melihat tagihan A setelah A membayarnya (capability URL → hanya payer, BOLA).
	billA := sa["pending_bill"].(map[string]any)["id"].(string)
	if r, _ := ee.confirm(t, a, billA, newKey()); r.StatusCode != http.StatusOK {
		t.Fatalf("A pays own bill: %d", r.StatusCode)
	}
	if r, _ := ee.do(t, http.MethodGet, "/api/v1/payment-intents/"+billA, b, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("B reads A's paid bill: want 404, got %d", r.StatusCode)
	}
}

// Kedaluwarsa: job menonaktifkan semua user sandbox dan mencabut refresh token → pengunjung keluar.
func TestDemoSandbox_ExpiryDisablesUsersAndRevokesSession(t *testing.T) {
	e := newEnv(t)
	srv := demoServer(t, e, func(d *app.Deps) { d.DemoSandboxTTL = 2 * time.Hour })
	ee := e
	ee.srv = srv
	_, token, refresh := startDemo(t, e, srv, "203.0.113.13")

	if n, err := app.ExpireDemoSandboxes(context.Background(), e.deps, 100); err != nil || n != 0 {
		t.Fatalf("before TTL: expired %d (%v), want 0", n, err)
	}
	e.clk.Advance(2*time.Hour + time.Minute)
	if n, err := app.ExpireDemoSandboxes(context.Background(), e.deps, 100); err != nil || n != 1 {
		t.Fatalf("after TTL: expired %d (%v), want 1", n, err)
	}
	if n, _ := app.ExpireDemoSandboxes(context.Background(), e.deps, 100); n != 0 {
		t.Fatalf("second run: expired %d, want 0 (idempotent)", n)
	}
	var disabled int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM users u JOIN demo_sandboxes s ON u.id = ANY(s.member_user_ids)
		WHERE u.status = 'DISABLED'`).Scan(&disabled); err != nil || disabled != 4 {
		t.Fatalf("disabled users %d (%v), want 4 (Budi + 3 contacts)", disabled, err)
	}
	r, _ := ee.doH(t, http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{
		"X-Requested-With": "fetch", "Cookie": refresh.Name + "=" + refresh.Value}, nil)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after expiry: want 401, got %d", r.StatusCode)
	}
	if r, _ := ee.do(t, http.MethodGet, "/api/v1/demo/sandbox", token, nil); r.StatusCode == http.StatusOK {
		t.Fatal("expired sandbox info must not be served")
	}
}

// Penyalahgunaan: rate limit per IP (429) dan kuota harian global (503).
func TestDemoSandbox_RateLimitAndDailyQuota(t *testing.T) {
	e := newEnv(t)
	tiers := ratelimit.DefaultTiers()
	srv := demoServer(t, e, func(d *app.Deps) { d.Tiers = &tiers }) // tier asli: 3/menit per IP
	for i := range 3 {
		if s, _, _ := startDemo(t, e, srv, "198.51.100.7"); s != http.StatusOK {
			t.Fatalf("session %d: %d", i+1, s)
		}
	}
	if s, _, _ := startDemo(t, e, srv, "198.51.100.7"); s != http.StatusTooManyRequests {
		t.Fatalf("4th session from one IP in a minute: want 429, got %d", s)
	}
	if s, _, _ := startDemo(t, e, srv, "198.51.100.8"); s != http.StatusOK {
		t.Fatalf("other IP: want 200, got %d", s)
	}

	full := demoServer(t, e, func(d *app.Deps) { d.DemoSandboxDailyMax = 4 }) // 4 sandbox sudah dibuat di atas
	if s, _, _ := startDemo(t, e, full, "198.51.100.9"); s != http.StatusServiceUnavailable {
		t.Fatalf("daily quota reached: want 503, got %d", s)
	}
}

// Uji ketahanan (versi tombol Demo B): 100 transfer Rp10.000 bersamaan dari saldo Rp100.000 → tepat 10
// berhasil, 90 INSUFFICIENT_BALANCE, saldo akhir 0. Invariant ledger dicek di akhir test (newEnv).
func TestDemoSandbox_StressTransfersExactlyTenSucceed(t *testing.T) {
	e := newEnv(t)
	srv := demoServer(t, e, nil)
	ee := e
	ee.srv = srv
	_, token, _ := startDemo(t, e, srv, "203.0.113.30")
	r, b := ee.do(t, http.MethodPost, "/api/v1/demo/stress/transfers", token, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("stress: %d %v", r.StatusCode, b)
	}
	if b["succeeded"] != float64(10) || b["rejected"] != float64(90) || b["end_balance"] != "0" || b["consistent"] != true {
		t.Fatalf("stress result: succeeded=%v rejected=%v end=%v consistent=%v", b["succeeded"], b["rejected"], b["end_balance"], b["consistent"])
	}
	results := b["results"].([]any)
	if len(results) != 100 {
		t.Fatalf("results: %d", len(results))
	}
	for _, it := range results {
		m := it.(map[string]any)
		if m["ok"] == false && m["code"] != "INSUFFICIENT_BALANCE" {
			t.Fatalf("rejected for the wrong reason: %v", m)
		}
	}
	// Siti menerima tepat Rp100.000 (10 × Rp10.000) di atas saldo awalnya; Budi tidak tersentuh.
	if bal := balanceOf(t, ee, token); bal != "269200000" {
		t.Fatalf("visitor balance changed: %s", bal)
	}
	// Periksa buku besar setelah 100 transfer: saldo tersimpan = Σ entry, semua journal seimbang.
	_, ig := ee.do(t, http.MethodGet, "/api/v1/demo/wallet/integrity", token, nil)
	if ig["ok"] != true || ig["stored_balance"] != ig["computed_balance"] || ig["unbalanced_journals"] != float64(0) || ig["journals"] != float64(7) {
		t.Fatalf("wallet integrity: %v", ig)
	}
	// Bukan pengunjung sandbox → 404.
	rina, _ := login(t, ee, "rina@example.com")
	if r, _ := ee.do(t, http.MethodGet, "/api/v1/demo/wallet/integrity", rina, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("non-sandbox integrity: want 404, got %d", r.StatusCode)
	}
	if r, _ := ee.do(t, http.MethodPost, "/api/v1/demo/stress/transfers", rina, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("non-sandbox user: want 404, got %d", r.StatusCode)
	}
}

// Regresi: GET /demo (dipanggil setiap halaman login dibuka) tidak boleh memakan kuota login per IP.
// Versi pertama memakai tier auth-ip: suite E2E (satu IP) mulai mendapat 429 saat login.
func TestDemoSandbox_DiscoveryDoesNotConsumeLoginQuota(t *testing.T) {
	e := newEnv(t)
	tiers := ratelimit.DefaultTiers() // auth-ip asli: 30/menit per IP
	srv := demoServer(t, e, func(d *app.Deps) { d.Tiers = &tiers })
	ee := e
	ee.srv = srv
	registerAs(t, ee, "nat@example.com", "Nat")
	ip := map[string]string{"X-Forwarded-For": "198.51.100.77"}
	for range 35 {
		if r, _ := ee.doH(t, http.MethodGet, "/api/v1/demo", "", ip, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("GET /demo: %d", r.StatusCode)
		}
	}
	r, _ := ee.doH(t, http.MethodPost, "/api/v1/auth/login", "", ip, map[string]string{"email": "nat@example.com", "password": "correct horse battery"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("login after 35 discovery calls from the same IP: want 200, got %d", r.StatusCode)
	}
}
