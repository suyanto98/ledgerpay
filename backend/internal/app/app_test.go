package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/app"
	"ledgerpay/internal/mockprovider"
	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payment"
	"ledgerpay/internal/modules/payout"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/modules/topup"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/crypto"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/db/dbtest"
	"ledgerpay/internal/platform/egress"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/ratelimit"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type env struct {
	srv     *httptest.Server
	ops     *httptest.Server
	pool    *pgxpool.Pool
	clk     *clock.Fake
	mock    *mockprovider.Server // payment provider tiruan, in-process
	mockSrv *httptest.Server
	deps    app.Deps
	broken  *bool // allowBrokenLedger: test ini sengaja merusak ledger (verifier diuji)
}

// allowBrokenLedger mematikan cek invariant otomatis di akhir test: HANYA untuk test yang sengaja
// menyuntikkan drift untuk membuktikan bahwa verifier mendeteksinya.
func (e env) allowBrokenLedger() { *e.broken = true }

const testWebhookSecret = "whsec_test"

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	kr, err := crypto.ParseKeyring(`{"v1":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`, "v1")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// testTiers: tier default, kecuali money-write yang dilonggarkan supaya test concurrency
// (100 transfer dari satu user) menguji lock, bukan rate limit. Test rate limit memakai newEnvTiers.
func testTiers() *ratelimit.Tiers {
	t := ratelimit.DefaultTiers()
	t.MoneyWrite.Rules = []ratelimit.Rule{{Events: 10_000, Per: time.Minute}}
	return &t
}

func newEnv(t *testing.T, opts ...dbtest.Option) env {
	t.Helper()
	return newEnvTiers(t, testTiers(), opts...)
}

func newEnvTiers(t *testing.T, tiers *ratelimit.Tiers, opts ...dbtest.Option) env {
	t.Helper()
	dbtest.SkipIfShort(t)
	tdb := dbtest.New(t, opts...)
	clk, ids := clock.NewFake(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)), idgen.V7{}
	tokens, err := auth.NewTokens(map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}, "k1", "ledgerpay", 15*time.Minute, clk, ids)
	if err != nil {
		t.Fatal(err)
	}
	// Server API dibuat dulu (handler diisi belakangan), karena URL-nya dibutuhkan sebagai
	// callback_url yang diberikan ke provider.
	var api http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
	mock := mockprovider.New(mockprovider.Config{
		APISecret: "mock_api_secret", WebhookSecret: testWebhookSecret, TimeScale: 0.001,
		HoldTimeout: 2 * time.Second, Now: clk.Now, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	mockSrv := httptest.NewServer(mock.Handler())
	deps := app.Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Pool: tdb.App, Tokens: tokens, Clock: clk, IDs: ids,
		Identity: identity.Config{Argon2: auth.TestArgon2, RefreshTTL: 7 * 24 * time.Hour, RefreshFamilyTTL: 30 * 24 * time.Hour},
		// Loopback dianggap proxy tepercaya: test bisa mensimulasikan banyak IP client lewat X-Forwarded-For.
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
		AllowedOrigins: []string{"http://localhost:5173"}, PublicBaseURL: "http://localhost:5173", Tiers: tiers,
		// Timeout klien provider 300 ms < HoldTimeout mock 2 dtk: skenario timeout_* benar-benar timeout.
		Provider:    topup.NewHTTPProviderTimeout(mockSrv.URL, "mock_api_secret", 300*time.Millisecond),
		CallbackURL: srv.URL + "/api/v1/webhooks/provider", WebhookSecrets: []string{testWebhookSecret},
		// Bank: timeout klien 300 ms < HoldTimeout mock (skenario 0408 = timeout_after_process benar-benar timeout).
		Bank:        payout.NewHTTPBank(mockSrv.URL, "mock_api_secret", 300*time.Millisecond),
		Settlements: recon.NewHTTPSource(mockSrv.URL, "mock_api_secret", 5*time.Second),
		Keyring:     testKeyring(t),
		// Sink webhook merchant di test berjalan di 127.0.0.1 → allowlist loopback (HANYA test; metadata tetap diblok).
		Egress:           egress.Policy{AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}},
		PayoutSafeWindow: time.Hour,
	}
	if err := deps.ValidateAPI(); err != nil { // env test harus lengkap seperti produksi
		t.Fatal(err)
	}
	if err := deps.ValidateWorker(); err != nil {
		t.Fatal(err)
	}
	api = app.APIRouter(deps)
	e := env{srv: srv, ops: httptest.NewServer(app.OpsRouter(tdb.App)), pool: tdb.App, clk: clk, mock: mock, mockSrv: mockSrv, deps: deps, broken: new(bool)}
	t.Cleanup(mockSrv.Close)
	t.Cleanup(mock.Close) // LIFO: webhook yang masih terjadwal dihentikan SEBELUM server API ditutup
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.ops.Close)
	// DoD C2: setiap test integrasi berakhir dengan invariant checker (LI-01, LI-03, LI-08), bukan hanya
	// test yang ingat memanggilnya. Didaftarkan terakhir → berjalan pertama, sebelum DB dihapus.
	t.Cleanup(func() {
		if *e.broken || t.Failed() {
			return
		}
		rep, err := ledger.Verify(context.Background(), e.pool)
		if err != nil {
			t.Errorf("invariant checker: %v", err)
			return
		}
		drift, err := payment.RefundDrift(context.Background(), e.pool)
		if err != nil || !rep.OK() || len(drift) > 0 {
			t.Errorf("ledger invariant violated at the end of the test: %s; refund drift %v (%v)", rep, drift, err)
		}
	})
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
	return e.doH(t, method, path, token, nil, body)
}

// doH sama dengan do, dengan header tambahan (contoh Idempotency-Key).
func (e env) doH(t *testing.T, method, path, token string, headers map[string]string, body any) (result, map[string]any) {
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

func register(t *testing.T, e env, email string) { registerAs(t, e, email, "Rina") }

func registerAs(t *testing.T, e env, email, name string) {
	t.Helper()
	resp, body := e.do(t, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": email, "password": "correct horse battery", "display_name": name})
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

// ---------------------------------------------------------------- wallet (Sprint 2)

// login mendaftarkan user lalu login, mengembalikan access token dan id user.
func login(t *testing.T, e env, email string) (token string, userID uuid.UUID) {
	t.Helper()
	return loginAs(t, e, email, "Rina")
}

func loginAs(t *testing.T, e env, email, name string) (token string, userID uuid.UUID) {
	t.Helper()
	registerAs(t, e, email, name)
	_, body := e.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": email, "password": "correct horse battery"})
	token, _ = body["access_token"].(string)
	_, me := e.do(t, http.MethodGet, "/api/v1/me", token, nil)
	return token, uuid.MustParse(me["id"].(string))
}

// seedTopup memposting top-up langsung lewat ledger.Service (endpoint top-up baru ada di Sprint 5).
func seedTopup(t *testing.T, e env, userID uuid.UUID, amount int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	svc := ledger.NewService(idgen.V7{})
	w, err := svc.WalletOf(ctx, e.pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := svc.SystemAccountID(ctx, e.pool, "PROVIDER_CLEARING")
	if err != nil {
		t.Fatal(err)
	}
	j := ledger.Journal{ID: uuid.Must(uuid.NewV7()), Purpose: ledger.PurposeTopupCredit, SourceType: "topup", SourceID: uuid.Must(uuid.NewV7()),
		Currency: "IDR", CorrelationID: "seed", Actor: ledger.Actor{Type: "SYSTEM"},
		Entries: []ledger.Entry{{AccountID: prov, Direction: ledger.Debit, Amount: amount}, {AccountID: w.AccountID, Direction: ledger.Credit, Amount: amount}}}
	if err := db.WithTx(ctx, e.pool, func(tx pgx.Tx) error { return svc.PostJournal(ctx, tx, j) }); err != nil {
		t.Fatal(err)
	}
	return j.ID
}

// LF-01 AC1: register membuat wallet IDR saldo 0 dalam transaksi yang sama.
func TestWallet_CreatedOnRegisterWithZeroBalance(t *testing.T) {
	e := newEnv(t)
	token, _ := login(t, e, "w0@example.com")
	resp, body := e.do(t, http.MethodGet, "/api/v1/wallet", token, nil)
	if resp.StatusCode != http.StatusOK || body["balance"] != "0" || body["currency"] != "IDR" || body["status"] != "ACTIVE" {
		t.Fatalf("unexpected wallet: %d %v", resp.StatusCode, body)
	}
}

func TestWallet_HistoryPaginationAndFilters(t *testing.T) {
	e := newEnv(t)
	token, uid := login(t, e, "hist@example.com")
	for _, amt := range []int64{1_000_00, 2_000_00, 3_000_00} {
		seedTopup(t, e, uid, amt)
	}
	_, body := e.do(t, http.MethodGet, "/api/v1/wallet", token, nil)
	if body["balance"] != "600000" {
		t.Fatalf("balance = %v, want 600000", body["balance"])
	}

	_, p1 := e.do(t, http.MethodGet, "/api/v1/wallet/transactions?limit=2", token, nil)
	data1, _ := p1["data"].([]any)
	page1, _ := p1["page"].(map[string]any)
	if len(data1) != 2 || page1["has_more"] != true {
		t.Fatalf("page 1: want 2 items + has_more, got %v", p1)
	}
	first := data1[0].(map[string]any)
	if first["amount"] != "300000" || first["direction"] != "IN" || first["type"] != "TOPUP" {
		t.Fatalf("newest first, credit to wallet = IN; got %v", first)
	}
	cursor := page1["next_cursor"].(string)
	_, p2 := e.do(t, http.MethodGet, "/api/v1/wallet/transactions?limit=2&cursor="+cursor, token, nil)
	data2, _ := p2["data"].([]any)
	if len(data2) != 1 || p2["page"].(map[string]any)["has_more"] != false || data2[0].(map[string]any)["amount"] != "100000" {
		t.Fatalf("page 2: want the oldest item only, got %v", p2)
	}

	_, out := e.do(t, http.MethodGet, "/api/v1/wallet/transactions?direction=OUT", token, nil)
	if d, _ := out["data"].([]any); len(d) != 0 {
		t.Fatalf("direction=OUT: want 0 items, got %v", out)
	}
	for name, q := range map[string]string{"unknown param": "?sort=amount", "bad limit": "?limit=500", "bad type": "?type=MINT"} {
		resp, b := e.do(t, http.MethodGet, "/api/v1/wallet/transactions"+q, token, nil)
		if resp.StatusCode != http.StatusBadRequest || b["code"] != "VALIDATION_ERROR" {
			t.Errorf("%s: want 400 VALIDATION_ERROR, got %d %v", name, resp.StatusCode, b)
		}
	}
	resp, b := e.do(t, http.MethodGet, "/api/v1/wallet/transactions?cursor=not-a-cursor", token, nil)
	if resp.StatusCode != http.StatusBadRequest || b["code"] != "INVALID_CURSOR" {
		t.Errorf("bad cursor: want 400 INVALID_CURSOR, got %d %v", resp.StatusCode, b)
	}
}

// bola_wallet: user B tidak boleh tahu transaksi milik user A ada (404, bukan 403).
func TestWallet_TransactionDetailBOLA(t *testing.T) {
	e := newEnv(t)
	tokenA, uidA := login(t, e, "a@example.com")
	tokenB, _ := login(t, e, "b@example.com")
	jid := seedTopup(t, e, uidA, 5_000_00)

	resp, body := e.do(t, http.MethodGet, "/api/v1/wallet/transactions/"+jid.String(), tokenA, nil)
	if resp.StatusCode != http.StatusOK || body["amount"] != "500000" || body["direction"] != "IN" {
		t.Fatalf("owner must see own transaction: %d %v", resp.StatusCode, body)
	}
	entries := body["journal"].(map[string]any)["entries"].([]any)
	labels := map[string]bool{}
	for _, en := range entries {
		labels[en.(map[string]any)["account_label"].(string)] = true
	}
	if !labels["Wallet Anda"] || !labels["Sistem"] {
		t.Fatalf("entries must be labeled generically (no foreign account ids): %v", entries)
	}

	for name, path := range map[string]string{
		"other user's journal": "/api/v1/wallet/transactions/" + jid.String(),
		"random uuid":          "/api/v1/wallet/transactions/" + uuid.NewString(),
		"not a uuid":           "/api/v1/wallet/transactions/123",
	} {
		resp, b := e.do(t, http.MethodGet, path, tokenB, nil)
		if resp.StatusCode != http.StatusNotFound || b["code"] != "NOT_FOUND" {
			t.Errorf("%s: want 404 NOT_FOUND, got %d %v", name, resp.StatusCode, b)
		}
	}
}

// Riwayat menampilkan pihak lawan + catatan dari modul pemilik objek (transfer: nama user, payment: merchant),
// dan BOLA tetap berlaku: label hanya untuk transaksi milik viewer.
func TestWallet_HistoryShowsCounterpartyAndNote(t *testing.T) {
	e := newEnv(t)
	_, _, _, rina := paidIntent(t, e, "30000") // rina membayar Kopi Budi
	andi, andiID := loginAs(t, e, "andi@example.com", "Andi Wijaya")
	seedTopup(t, e, andiID, 500_000)
	body := transferBody("rina@example.com", "120000")
	body["note"] = "Patungan makan siang"
	if r, b := e.transfer(t, andi, newKey(), body); r.StatusCode != http.StatusCreated {
		t.Fatalf("transfer: %d %v", r.StatusCode, b)
	}

	_, h := e.do(t, http.MethodGet, "/api/v1/wallet/transactions", rina, nil)
	byType := map[string]map[string]any{}
	for _, it := range h["data"].([]any) {
		m := it.(map[string]any)
		byType[m["type"].(string)] = m
	}
	if tr := byType["TRANSFER"]; tr["counterparty"] != "Andi Wijaya" || tr["note"] != "Patungan makan siang" || tr["direction"] != "IN" {
		t.Fatalf("transfer label: %v", tr)
	}
	if pay := byType["PAYMENT"]; pay["counterparty"] != "Kopi Budi" {
		t.Fatalf("payment label: %v", pay)
	}
	if top := byType["TOPUP"]; top["counterparty"] != nil || top["note"] != nil {
		t.Fatalf("top-up has no counterparty, want nulls: %v", top)
	}
	// Detail transaksi memakai label yang sama.
	_, d := e.do(t, http.MethodGet, "/api/v1/wallet/transactions/"+byType["TRANSFER"]["id"].(string), rina, nil)
	if d["counterparty"] != "Andi Wijaya" {
		t.Fatalf("detail label: %v", d)
	}
	// Jurnal pembayaran: wallet merchant diberi label merchant, bukan "pengguna lain".
	_, pd := e.do(t, http.MethodGet, "/api/v1/wallet/transactions/"+byType["PAYMENT"]["id"].(string), rina, nil)
	labels := map[string]bool{}
	for _, en := range pd["journal"].(map[string]any)["entries"].([]any) {
		labels[en.(map[string]any)["account_label"].(string)] = true
	}
	if !labels["Wallet Anda"] || !labels["Wallet merchant"] {
		t.Fatalf("payment journal labels: %v", labels)
	}
	// Dari sisi pengirim: pihak lawan = penerima.
	_, ha := e.do(t, http.MethodGet, "/api/v1/wallet/transactions?type=TRANSFER", andi, nil)
	if got := ha["data"].([]any)[0].(map[string]any)["counterparty"]; got != "Rina" {
		t.Fatalf("sender sees counterparty %v, want Rina", got)
	}
}
