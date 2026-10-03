package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ledgerpay/internal/app"
	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db/dbtest"
	"ledgerpay/internal/platform/idgen"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type env struct {
	srv *httptest.Server
	ops *httptest.Server
}

func newEnv(t *testing.T) env {
	t.Helper()
	dbtest.SkipIfShort(t)
	tdb := dbtest.New(t)
	clk, ids := clock.NewFake(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)), idgen.V7{}
	tokens, err := auth.NewTokens(map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}, "k1", "ledgerpay", 15*time.Minute, clk, ids)
	if err != nil {
		t.Fatal(err)
	}
	h := app.APIRouter(app.Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Pool: tdb.App, Tokens: tokens, Clock: clk, IDs: ids,
		Identity: identity.Config{Argon2: auth.TestArgon2, RefreshTTL: 7 * 24 * time.Hour, RefreshFamilyTTL: 30 * 24 * time.Hour},
	})
	e := env{srv: httptest.NewServer(h), ops: httptest.NewServer(app.OpsRouter(tdb.App))}
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.ops.Close)
	return e
}

type result struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func (r result) Cookies() []*http.Cookie { return r.cookies }

func (e env) do(t *testing.T, method, path, token string, body any) (result, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

func register(t *testing.T, e env, email string) {
	t.Helper()
	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": email, "password": "correct horse battery", "display_name": "Rina"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d %v", resp.StatusCode, body)
	}
}

// DoD Sprint 1: register → login → GET /me lewat HTTP sungguhan.
func TestIdentity_RegisterLoginMe(t *testing.T) {
	e := newEnv(t)
	register(t, e, "rina@example.com")

	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "RINA@example.com", "password": "correct horse battery"}) // email case-insensitive
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" || body["token_type"] != "Bearer" || body["expires_in"] != float64(900) {
		t.Fatalf("unexpected login body: %v", body)
	}

	// Cookie refresh: HttpOnly, SameSite=Strict, Path=/api/v1/auth (ADR-009).
	var refresh *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == identity.RefreshCookieName {
			refresh = c
		}
	}
	if refresh == nil || !refresh.HttpOnly || refresh.SameSite != http.SameSiteStrictMode || refresh.Path != "/api/v1/auth" || refresh.Value == "" {
		t.Fatalf("refresh cookie missing or insecure: %+v", refresh)
	}

	resp, body = e.do(t, http.MethodGet, "/api/v1/me", token, nil)
	if resp.StatusCode != http.StatusOK || body["email"] != "rina@example.com" {
		t.Fatalf("me: %d %v", resp.StatusCode, body)
	}
	if _, leaked := body["password_hash"]; leaked {
		t.Fatal("password_hash must never be returned")
	}
}

func TestIdentity_DuplicateEmailConflict(t *testing.T) {
	e := newEnv(t)
	register(t, e, "andi@example.com")
	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": "Andi@Example.com", "password": "another long password", "display_name": "Andi 2"})
	if resp.StatusCode != http.StatusConflict || body["code"] != "EMAIL_ALREADY_REGISTERED" {
		t.Fatalf("want 409 EMAIL_ALREADY_REGISTERED, got %d %v", resp.StatusCode, body)
	}
}

// authn_generic_error: password salah dan email tak dikenal harus TIDAK bisa dibedakan dari
// respons (status, code, title, detail) — hanya request_id yang berbeda (T-SH-01).
func TestIdentity_LoginErrorsAreIndistinguishable(t *testing.T) {
	e := newEnv(t)
	register(t, e, "sari@example.com")
	r1, wrongPass := e.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "sari@example.com", "password": "wrong password!!"})
	r2, unknown := e.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "nobody@example.com", "password": "wrong password!!"})
	if r1.StatusCode != http.StatusUnauthorized || r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("both must be 401, got %d and %d", r1.StatusCode, r2.StatusCode)
	}
	delete(wrongPass, "request_id")
	delete(unknown, "request_id")
	a, _ := json.Marshal(wrongPass)
	b, _ := json.Marshal(unknown)
	if string(a) != string(b) {
		t.Fatalf("login errors differ:\n wrong password: %s\n unknown email:  %s", a, b)
	}
	if wrongPass["code"] != "INVALID_CREDENTIALS" {
		t.Fatalf("want INVALID_CREDENTIALS, got %v", wrongPass)
	}
}

func TestIdentity_MeRequiresAuth(t *testing.T) {
	e := newEnv(t)
	for name, tok := range map[string]string{"no token": "", "garbage token": "not.a.jwt"} {
		resp, body := e.do(t, http.MethodGet, "/api/v1/me", tok, nil)
		if resp.StatusCode != http.StatusUnauthorized || body["code"] != "UNAUTHENTICATED" {
			t.Errorf("%s: want 401 UNAUTHENTICATED, got %d %v", name, resp.StatusCode, body)
		}
	}
}

func TestIdentity_RegisterValidation(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": "not-an-email", "password": "short", "display_name": ""})
	if resp.StatusCode != http.StatusBadRequest || body["code"] != "VALIDATION_ERROR" {
		t.Fatalf("want 400 VALIDATION_ERROR, got %d %v", resp.StatusCode, body)
	}
	if errs, _ := body["errors"].([]any); len(errs) != 3 {
		t.Fatalf("want 3 field errors (email, password, display_name), got %v", body["errors"])
	}
}

// Mass assignment (C-13): field yang tidak ada di kontrak (misalnya "role") ditolak, sehingga
// user tidak bisa mendaftar sebagai OPS dengan menambah field.
func TestIdentity_RegisterRejectsUnknownFields(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": "eve@example.com", "password": "correct horse battery", "display_name": "Eve", "role": "OPS"})
	if resp.StatusCode != http.StatusBadRequest || body["code"] != "VALIDATION_ERROR" {
		t.Fatalf("unknown field must be rejected, got %d %v", resp.StatusCode, body)
	}
}

// DoD Sprint 1: setiap respons membawa X-Request-ID; ID dari client yang aman diteruskan,
// ID yang tidak aman (bisa dipakai log injection) diganti.
func TestHTTP_RequestID(t *testing.T) {
	e := newEnv(t)
	cases := map[string]struct{ sent, wantSame bool }{"generated": {false, false}, "client safe": {true, true}}
	for name, c := range cases {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/api/v1/me", nil)
		if c.sent {
			req.Header.Set("X-Request-ID", "abc-123")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		got := resp.Header.Get("X-Request-ID")
		if got == "" || (c.wantSame && got != "abc-123") {
			t.Errorf("%s: unexpected X-Request-ID %q", name, got)
		}
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/api/v1/me", nil)
	// Karakter yang lolos HTTP client tetapi tidak aman untuk log/HTML harus diganti server.
	req.Header.Set("X-Request-ID", "evil<script>injected")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got == "" || strings.Contains(got, "injected") {
		t.Errorf("unsafe request id must be replaced, got %q", got)
	}
}

func TestHTTP_UnsupportedMediaTypeAndBodyLimit(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/api/v1/auth/login", strings.NewReader("email=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("want 415, got %d", resp.StatusCode)
	}

	big := `{"email":"` + strings.Repeat("a", 70<<10) + `","password":"x"}`
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/api/v1/auth/login", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("want 413 for body > 64 KB, got %d", resp.StatusCode)
	}
}

func TestOps_HealthAndReadiness(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(e.ops.URL + path) //nolint:noctx // test
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: want 200, got %d", path, resp.StatusCode)
		}
	}
}
