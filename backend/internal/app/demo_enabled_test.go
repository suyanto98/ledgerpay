//go:build demo

package app_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ledgerpay/internal/app"
	"ledgerpay/internal/platform/faults"
)

// Build demo: tiga lapis guard + fault sekali pakai. Jalankan: go test -tags demo ./internal/app/ -run Demo
func TestDemo_FaultsGuardedAndFireOnce(t *testing.T) {
	e := newEnv(t)
	token, _ := login(t, e, "rina@example.com")
	post := func(srv *httptest.Server, demoToken string, body string) int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/demo/faults", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if demoToken != "" {
			req.Header.Set("X-Demo-Token", demoToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	off := httptest.NewServer(app.APIRouter(e.deps)) // lapis runtime: DEMO_MODE=false
	t.Cleanup(off.Close)
	if s := post(off, "whatever-token-0123456789", `{"point":"payout.after_bank_call"}`); s != http.StatusNotFound {
		t.Fatalf("DEMO_MODE=false: want 404, got %d", s)
	}
	d := e.deps
	d.DemoMode, d.DemoToken = true, "demo-token-0123456789abcdef"
	on := httptest.NewServer(app.APIRouter(d))
	t.Cleanup(on.Close)
	if s := post(on, "wrong-token-0123456789abcd", `{"point":"payout.after_bank_call"}`); s != http.StatusNotFound {
		t.Fatalf("wrong X-Demo-Token: want 404 (route hidden), got %d", s)
	}
	if s := post(on, "", `{"point":"payout.after_bank_call"}`); s != http.StatusNotFound {
		t.Fatalf("missing X-Demo-Token: want 404, got %d", s)
	}
	if s := post(on, d.DemoToken, `{"point":"ledger.anything"}`); s != http.StatusBadRequest {
		t.Fatalf("unknown point: want 400, got %d", s)
	}
	if s := post(on, d.DemoToken, `{"point":"payout.after_bank_call","times":1}`); s != http.StatusCreated {
		t.Fatalf("arm fault: want 201, got %d", s)
	}

	exits := 0
	t.Cleanup(faults.SetExitForTest(func(code int) {
		if code != 137 {
			t.Errorf("exit code %d, want 137 (like kill -9)", code)
		}
		exits++
	}))
	faults.Check(context.Background(), e.pool, "inbox.before_apply") // titik lain: tidak terpicu
	faults.Check(context.Background(), e.pool, "payout.after_bank_call")
	faults.Check(context.Background(), e.pool, "payout.after_bank_call") // sekali pakai
	if exits != 1 {
		t.Fatalf("fault fired %d times, want exactly 1", exits)
	}
}
