package topup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// chargeID dari payload webhook tidak boleh bisa membelokkan request verify-by-fetch ke path lain.
func TestHTTPProvider_GetChargeEscapesID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, err := NewHTTPProvider(srv.URL, "s").GetCharge(context.Background(), "../../admin/reset")
	if !errors.Is(err, ErrChargeNotFound) {
		t.Fatalf("want ErrChargeNotFound, got %v", err)
	}
	if gotPath != "/v1/charges/..%2F..%2Fadmin%2Freset" {
		t.Fatalf("charge id must stay one escaped path segment, got %q", gotPath)
	}
}
