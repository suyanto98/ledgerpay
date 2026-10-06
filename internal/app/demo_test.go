//go:build !demo

package app_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ledgerpay/internal/app"
)

// Lapis "build": binary tanpa tag `demo` tidak punya /api/v1/demo/*, walaupun DEMO_MODE=true dan
// token yang benar dikirim (env tidak bisa menghidupkan kode yang tidak ter-compile).
func TestDemo_RoutesAbsentInReleaseBuildEvenWithDemoMode(t *testing.T) {
	e := newEnv(t)
	d := e.deps
	d.DemoMode, d.DemoToken = true, "demo-token-0123456789abcdef"
	srv := httptest.NewServer(app.APIRouter(d))
	t.Cleanup(srv.Close)
	token, _ := login(t, e, "rina@example.com")
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/demo/faults", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Demo-Token", d.DemoToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("release build: /api/v1/demo/faults must be 404, got %d", resp.StatusCode)
	}
}
