package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ips, ok := f[host]; ok {
		return ips, nil
	}
	return []netip.Addr{netip.MustParseAddr(host)}, nil
}

// webhook_egress_blocks_private_ip (C-44).
func TestEgress_BlocksPrivateAndMetadata(t *testing.T) {
	res := fakeResolver{
		"metadata.evil.example": {netip.MustParseAddr("169.254.169.254")},
		"mixed.evil.example":    {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, // publik + internal
		"localhost":             {netip.MustParseAddr("127.0.0.1")},
	}
	c := NewClient(Policy{Resolver: res})
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://metadata.evil.example/",
		"http://mixed.evil.example/",
		"http://localhost:5432/",
		"http://10.1.2.3/",
		"http://192.168.1.1/",
		"http://100.64.0.1/",
		"http://[::1]/",
		"http://[fd00::1]/",
		"http://[::ffff:127.0.0.1]/", // IPv4-mapped IPv6 tetap loopback
	} {
		if _, err := c.Post(context.Background(), u, nil, strings.NewReader("{}")); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s must be blocked, got %v", u, err)
		}
	}
	for _, u := range []string{"ftp://example.com/", "file:///etc/passwd", "http://user:pass@example.com/"} {
		if _, err := c.Post(context.Background(), u, nil, nil); !errors.Is(err, ErrScheme) {
			t.Errorf("%s must be rejected by scheme rules, got %v", u, err)
		}
	}
	if _, err := NewClient(Policy{RequireHTTPS: true}).Post(context.Background(), "http://example.com/", nil, nil); !errors.Is(err, ErrScheme) {
		t.Error("live mode must require https")
	}
}

// Metadata tetap diblok walaupun allowlist dev mencakup seluruh link-local.
func TestEgress_MetadataBlockedEvenWhenAllowlisted(t *testing.T) {
	p := Policy{AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("127.0.0.0/8")}}
	if p.Allowed(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("cloud metadata must always be blocked")
	}
	if !p.Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("allowlisted dev range must pass")
	}
}

// webhook_egress_no_redirect: URL yang diizinkan me-redirect ke IP internal → tidak diikuti.
func TestEgress_NoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	c := NewClient(Policy{AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}})
	if _, err := c.Post(context.Background(), srv.URL, nil, strings.NewReader("{}")); !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect must not be followed, got %v", err)
	}
}
