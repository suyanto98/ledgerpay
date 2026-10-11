package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/platform/ratelimit"
)

// ---------------------------------------------------------------- helpers

const password = "correct horse battery"

// loginCookie login dan mengembalikan nilai cookie refresh.
func loginCookie(t *testing.T, e env, email string) string {
	t.Helper()
	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": email, "password": password})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	return cookieValue(resp)
}

func cookieValue(r result) string {
	for _, c := range r.Cookies() {
		if c.Name == identity.RefreshCookieName {
			return c.Value
		}
	}
	return ""
}

// cookiePost memanggil endpoint cookie (refresh/logout) seperti SPA: cookie + X-Requested-With.
func (e env) cookiePost(t *testing.T, path, cookie string, headers map[string]string) (result, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
	}
	if headers == nil {
		headers = map[string]string{"X-Requested-With": "fetch"}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return result{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}, out
}

func (e env) loginFrom(t *testing.T, ip, email, pass string) (result, map[string]any) {
	t.Helper()
	return e.doH(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"X-Forwarded-For": ip},
		map[string]string{"email": email, "password": pass})
}

// ---------------------------------------------------------------- refresh token (C-05)

func TestRefresh_RotatesToken(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	c0 := loginCookie(t, e, "rina@example.com")

	resp, body := e.cookiePost(t, "/api/v1/auth/refresh", c0, nil)
	c1 := cookieValue(resp)
	if resp.StatusCode != http.StatusOK || c1 == "" || c1 == c0 {
		t.Fatalf("refresh must rotate the cookie: %d %v (new=%q)", resp.StatusCode, body, c1)
	}
	token, _ := body["access_token"].(string)
	if r, _ := e.do(t, http.MethodGet, "/api/v1/me", token, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("new access token must work, got %d", r.StatusCode)
	}
}

// refresh_reuse_revokes_family: token lama dipakai lagi di luar grace → seluruh sesi dicabut,
// termasuk token terbaru milik korban (pencuri dan korban sama-sama harus login ulang).
func TestRefresh_ReuseRevokesFamily(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	c0 := loginCookie(t, e, "rina@example.com")
	r1, _ := e.cookiePost(t, "/api/v1/auth/refresh", c0, nil)
	c1 := cookieValue(r1)

	e.clk.Advance(11 * time.Second) // lewat grace window 10 dtk
	r2, b2 := e.cookiePost(t, "/api/v1/auth/refresh", c0, nil)
	if r2.StatusCode != http.StatusUnauthorized || b2["code"] != "REFRESH_TOKEN_REUSED" {
		t.Fatalf("reuse: want 401 REFRESH_TOKEN_REUSED, got %d %v", r2.StatusCode, b2)
	}
	r3, b3 := e.cookiePost(t, "/api/v1/auth/refresh", c1, nil)
	if r3.StatusCode != http.StatusUnauthorized || b3["code"] != "UNAUTHENTICATED" {
		t.Fatalf("latest token of a revoked family must be dead, got %d %v", r3.StatusCode, b3)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'auth.refresh_reuse_detected'`).Scan(&n)
	if n != 1 {
		t.Fatalf("reuse must be audited once, got %d", n)
	}
}

// refresh_parallel_within_grace_ok: dua tab me-refresh token yang sama bersamaan → keduanya 200,
// hanya satu yang menerima cookie baru, dan sesi TIDAK dicabut.
func TestRefresh_ParallelWithinGrace(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	c0 := loginCookie(t, e, "rina@example.com")

	var mu sync.Mutex
	var results []result
	runConcurrently(2, func(int) int {
		r, _ := e.cookiePost(t, "/api/v1/auth/refresh", c0, nil)
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
		return r.StatusCode
	})
	var rotated string
	for _, r := range results {
		if r.StatusCode != http.StatusOK {
			t.Fatalf("both parallel refreshes must succeed, got %d", r.StatusCode)
		}
		if c := cookieValue(r); c != "" {
			if rotated != "" {
				t.Fatal("only one of the parallel refreshes may rotate the token")
			}
			rotated = c
		}
	}
	if r, _ := e.cookiePost(t, "/api/v1/auth/refresh", rotated, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("family must still be alive after parallel refresh, got %d", r.StatusCode)
	}
}

// Batas absolut family 30 hari: refresh rutin (sliding 7 hari) tidak bisa memperpanjang sesi selamanya.
func TestRefresh_FamilyAbsoluteExpiry(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	c := loginCookie(t, e, "rina@example.com")
	for day := 6; day <= 30; day += 6 {
		e.clk.Advance(6 * 24 * time.Hour)
		r, b := e.cookiePost(t, "/api/v1/auth/refresh", c, nil)
		if day < 30 {
			if r.StatusCode != http.StatusOK {
				t.Fatalf("day %d: refresh must work, got %d %v", day, r.StatusCode, b)
			}
			c = cookieValue(r)
			continue
		}
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("day %d: family past 30 days must be rejected, got %d", day, r.StatusCode)
		}
	}
}

func TestLogout_RevokesFamilyAndClearsCookie(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	c := loginCookie(t, e, "rina@example.com")
	r, _ := e.cookiePost(t, "/api/v1/auth/logout", c, nil)
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: want 204, got %d", r.StatusCode)
	}
	cleared := false
	for _, ck := range r.Cookies() {
		cleared = cleared || (ck.Name == identity.RefreshCookieName && ck.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("logout must clear the refresh cookie")
	}
	if r, _ := e.cookiePost(t, "/api/v1/auth/refresh", c, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout must fail, got %d", r.StatusCode)
	}
	if r, _ := e.cookiePost(t, "/api/v1/auth/logout", "garbage", nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout with an invalid cookie must still be 204, got %d", r.StatusCode)
	}
}

// csrf_refresh_without_header_rejected (C-08).
func TestCSRF_RefreshGuard(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	c := loginCookie(t, e, "rina@example.com")
	cases := map[string]map[string]string{
		"no X-Requested-With": {},
		"foreign Origin":      {"X-Requested-With": "fetch", "Origin": "https://evil.example"},
	}
	for name, h := range cases {
		if r, b := e.cookiePost(t, "/api/v1/auth/refresh", c, h); r.StatusCode != http.StatusForbidden || b["code"] != "FORBIDDEN" {
			t.Errorf("%s: want 403, got %d %v", name, r.StatusCode, b)
		}
	}
	r, _ := e.cookiePost(t, "/api/v1/auth/refresh", c, map[string]string{"X-Requested-With": "fetch", "Origin": "http://localhost:5173"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("allowed origin must pass, got %d", r.StatusCode)
	}
}

// ---------------------------------------------------------------- brute force (C-03)

// authn_bruteforce_lockout: 5 password salah dari 5 IP berbeda (botnet; rate limit per IP tidak
// menolong) → akun terkunci. Login ke-6 dengan password BENAR tetap ditolak, dan responsnya
// identik dengan "email tidak ada" (tidak ada oracle ACCOUNT_LOCKED / Retry-After, final review R2-5).
func TestAuthn_BruteforceLockout(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")
	for i := 1; i <= 5; i++ {
		if r, _ := e.loginFrom(t, fmt.Sprintf("203.0.113.%d", i), "rina@example.com", "wrong password!!"); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, r.StatusCode)
		}
	}
	rLocked, locked := e.loginFrom(t, "203.0.113.6", "rina@example.com", password)
	_, unknown := e.loginFrom(t, "203.0.113.7", "nobody@example.com", password)
	if rLocked.StatusCode != http.StatusUnauthorized || locked["code"] != "INVALID_CREDENTIALS" {
		t.Fatalf("locked account with correct password: want 401 INVALID_CREDENTIALS, got %d %v", rLocked.StatusCode, locked)
	}
	if rLocked.Header.Get("Retry-After") != "" {
		t.Fatal("locked response must not carry Retry-After (it would reveal that the email exists)")
	}
	delete(locked, "request_id")
	delete(unknown, "request_id")
	a, _ := json.Marshal(locked)
	b, _ := json.Marshal(unknown)
	if string(a) != string(b) {
		t.Fatalf("locked vs unknown email must be indistinguishable:\n locked:  %s\n unknown: %s", a, b)
	}

	e.clk.Advance(61 * time.Second) // lock pertama: 1 menit
	if r, b := e.loginFrom(t, "203.0.113.8", "rina@example.com", password); r.StatusCode != http.StatusOK {
		t.Fatalf("after lock expires the correct password must work, got %d %v", r.StatusCode, b)
	}
	var failed int
	var audits int
	ctx := context.Background()
	_ = e.pool.QueryRow(ctx, `SELECT failed_login_count FROM users WHERE email = 'rina@example.com'`).Scan(&failed)
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'auth.login_failed'`).Scan(&audits)
	if failed != 0 || audits != 6 {
		t.Fatalf("success must reset the counter (got %d) and all 6 failures must be audited (got %d)", failed, audits)
	}
}

// ---------------------------------------------------------------- rate limit (C-07)

func TestRateLimit_AuthStrictPerIPAndEmail(t *testing.T) {
	tiers := ratelimit.DefaultTiers()
	e := newEnvTiers(t, &tiers)
	register(t, e, "rina@example.com")
	for i := 1; i <= 5; i++ {
		e.loginFrom(t, "198.51.100.1", "rina@example.com", "wrong password!!")
	}
	r, b := e.loginFrom(t, "198.51.100.1", "rina@example.com", "wrong password!!")
	if r.StatusCode != http.StatusTooManyRequests || b["code"] != "RATE_LIMITED" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("6th attempt/min from the same IP+email: want 429 + Retry-After, got %d %v", r.StatusCode, b)
	}
	// Email tak terdaftar dibatasi sama persis (rate limit bukan oracle enumerasi).
	for i := 1; i <= 5; i++ {
		e.loginFrom(t, "198.51.100.2", "nobody@example.com", "wrong password!!")
	}
	if r, _ := e.loginFrom(t, "198.51.100.2", "nobody@example.com", "x"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unknown email must be limited the same way, got %d", r.StatusCode)
	}
}

func TestRateLimit_MoneyWritePerUser(t *testing.T) {
	tiers := ratelimit.DefaultTiers()
	e := newEnvTiers(t, &tiers)
	rina, _ := login(t, e, "rina@example.com")
	login(t, e, "andi@example.com")
	var last result
	for range 31 {
		last, _ = e.transfer(t, rina, newKey(), transferBody("andi@example.com", "1000"))
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("31st money-write in a minute: want 429, got %d", last.StatusCode)
	}
}

// ---------------------------------------------------------------- headers (C-10)

func TestHeaders_PresentOnEveryResponse(t *testing.T) {
	e := newEnv(t)
	token, _ := login(t, e, "rina@example.com")
	want := map[string]string{
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Cache-Control":           "no-store",
	}
	for name, call := range map[string]func() result{
		"200 /me":       func() result { r, _ := e.do(t, http.MethodGet, "/api/v1/me", token, nil); return r },
		"401 /me":       func() result { r, _ := e.do(t, http.MethodGet, "/api/v1/me", "", nil); return r },
		"404 unknown":   func() result { r, _ := e.do(t, http.MethodGet, "/api/v1/nope", "", nil); return r },
		"400 bad login": func() result { r, _ := e.do(t, http.MethodPost, "/api/v1/auth/login", "", "not an object"); return r },
	} {
		r := call()
		for h, v := range want {
			if got := r.Header.Get(h); got != v {
				t.Errorf("%s: header %s = %q, want %q", name, h, got, v)
			}
		}
	}
}

// Temuan E2E Sprint 5: refresh dibatasi per SESI, bukan per IP. Banyak user di belakang satu IP
// (NAT kantor) yang memuat ulang SPA tidak boleh saling menghabiskan kuota refresh.
func TestRateLimit_RefreshIsPerSessionNotPerIP(t *testing.T) {
	tiers := ratelimit.DefaultTiers()
	e := newEnvTiers(t, &tiers)
	for i := range 8 { // 8 user, satu IP (loopback), masing-masing refresh sekali
		email := fmt.Sprintf("u%d@example.com", i)
		register(t, e, email)
		c := loginCookie(t, e, email)
		if r, b := e.cookiePost(t, "/api/v1/auth/refresh", c, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("user %d behind the same IP: refresh must not be rate limited by others, got %d %v", i, r.StatusCode, b)
		}
	}
}

// Wiring yang lupa harus menggagalkan START, bukan panic di request uang pertama (temuan Demo C).
func TestDeps_ValidateRejectsMissingKeyring(t *testing.T) {
	e := newEnv(t)
	d := e.deps
	d.Keyring = nil
	if err := d.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "Keyring") {
		t.Fatalf("missing Keyring must be rejected at startup, got %v", err)
	}
	d.Bank = nil
	if err := d.ValidateWorker(); err == nil || !strings.Contains(err.Error(), "Bank") {
		t.Fatalf("missing Bank must be rejected at startup, got %v", err)
	}
}
