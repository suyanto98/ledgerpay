// Package egress: klien HTTP untuk URL yang ditentukan PIHAK LUAR (webhook_url merchant), dengan
// pertahanan SSRF (C-44). Tanpa ini, merchant bisa mendaftarkan http://169.254.169.254/… (metadata
// cloud berisi kredensial) atau http://postgres:5432 dan membuat server kita menyerangnya.
//
// Aturan:
//   - hostname di-resolve SEKALI; setiap IP hasil resolve diperiksa; koneksi dibuat ke IP yang SUDAH
//     divalidasi (bukan resolve ulang → menutup celah DNS rebinding);
//   - tolak loopback, private (RFC 1918), link-local (termasuk metadata), CGNAT, ULA, unspecified,
//     multicast; metadata cloud SELALU ditolak, bahkan kalau ada allowlist;
//   - redirect tidak diikuti (3xx = gagal), supaya URL publik tidak bisa memantul ke IP internal;
//   - HTTPS wajib di mode live; body respons dibaca maksimal 64 KB lalu dibuang.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"ledgerpay/internal/platform/metrics"
	"ledgerpay/internal/platform/tracing"
)

var (
	ErrBlockedAddress = errors.New("egress: destination address is not allowed")
	ErrScheme         = errors.New("egress: URL scheme not allowed")
	ErrRedirect       = errors.New("egress: redirects are not followed")
)

// Selalu diblok, termasuk saat ada allowlist dev: endpoint metadata cloud (AWS/GCP/Azure, IMDS IPv6).
var metadata = []netip.Prefix{netip.MustParsePrefix("169.254.169.254/32"), netip.MustParsePrefix("fd00:ec2::254/128")}

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

// Policy: AllowCIDRs HANYA untuk dev/test (contoh jaringan compose); config menolaknya di produksi.
type Policy struct {
	AllowCIDRs   []netip.Prefix
	RequireHTTPS bool
	Timeout      time.Duration // default 5 dtk (kontrak webhook keluar)
	MaxBody      int64         // default 64 KB
	Resolver     interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
}

// Allowed memeriksa satu IP terhadap kebijakan.
func (p Policy) Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, m := range metadata {
		if m.Contains(ip) {
			return false
		}
	}
	for _, a := range p.AllowCIDRs {
		if a.Contains(ip) {
			return true
		}
	}
	for _, b := range blocked {
		if b.Contains(ip) {
			return false
		}
	}
	return ip.IsValid()
}

// Client adalah klien HTTP dengan guard di atas.
type Client struct {
	policy Policy
	http   *http.Client
}

func NewClient(p Policy) *Client {
	if p.Timeout == 0 {
		p.Timeout = 5 * time.Second
	}
	if p.MaxBody == 0 {
		p.MaxBody = 64 << 10
	}
	if p.Resolver == nil {
		p.Resolver = net.DefaultResolver
	}
	dialer := &net.Dialer{Timeout: p.Timeout}
	tr := &http.Transport{
		Proxy: nil, // proxy dari env tidak dipakai: guard harus melihat tujuan sebenarnya
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := p.Resolver.LookupNetIP(ctx, "ip", host) // resolve SEKALI
			if err != nil {
				return nil, err
			}
			for _, ip := range ips { // SEMUA IP harus lolos (satu nama bisa resolve ke publik + internal)
				if !p.Allowed(ip) {
					return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip)
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%w: %s has no address", ErrBlockedAddress, host)
			}
			// Connect ke IP yang sudah divalidasi, bukan ke hostname (anti DNS rebinding).
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout: p.Timeout,
		MaxIdleConns:        16,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{policy: p, http: &http.Client{
		Transport:     metrics.OutboundTransport("merchant_webhook", tracing.Transport(tr)),
		Timeout:       p.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}}
}

// Response adalah hasil POST.
type Response struct {
	StatusCode int
	Duration   time.Duration
}

// Post mengirim body ke rawURL setelah validasi skema. Error koneksi/guard dikembalikan sebagai error.
func (c *Client) Post(ctx context.Context, rawURL string, headers map[string]string, body io.Reader) (Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return Response{}, fmt.Errorf("%w: %q", ErrScheme, rawURL)
	}
	if u.Scheme != "https" && (c.policy.RequireHTTPS || u.Scheme != "http") {
		return Response{}, fmt.Errorf("%w: %s", ErrScheme, u.Scheme)
	}
	if u.User != nil {
		return Response{}, fmt.Errorf("%w: credentials in URL", ErrScheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return Response{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now() //nolint:forbidigo // mengukur durasi jaringan, bukan waktu bisnis
	resp, err := c.http.Do(req)
	d := time.Since(start) //nolint:forbidigo // lihat di atas
	if err != nil {
		return Response{Duration: d}, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.policy.MaxBody))
	return Response{StatusCode: resp.StatusCode, Duration: d}, nil
}

// String untuk log.
func (r Response) String() string { return strconv.Itoa(r.StatusCode) }
