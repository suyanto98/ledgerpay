// Package config membaca konfigurasi dari environment variable dan gagal cepat (fail fast)
// kalau ada yang salah: lebih baik proses tidak mau start daripada berjalan dengan konfigurasi
// setengah benar. Katalog env var: docs/design/24-docker-compose.md §8.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"ledgerpay/internal/platform/crypto"
)

type Config struct {
	AppEnv   string `env:"APP_ENV" envDefault:"dev"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	Version  string `env:"APP_VERSION" envDefault:"dev"`

	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"` // API publik (lewat Caddy)
	OpsAddr  string `env:"OPS_ADDR" envDefault:":9090"`  // health/metrics; tidak diekspos Caddy

	DatabaseURL        string        `env:"DATABASE_URL"` // role ledgerpay_app
	MigrateDatabaseURL string        `env:"MIGRATE_DATABASE_URL"`
	DBMaxConns         int32         `env:"DB_MAX_CONNS" envDefault:"20"`
	DBStatementTimeout time.Duration `env:"DB_STATEMENT_TIMEOUT" envDefault:"5s"` // ADR-011
	DBLockTimeout      time.Duration `env:"DB_LOCK_TIMEOUT" envDefault:"2s"`
	DBIdleInTxTimeout  time.Duration `env:"DB_IDLE_IN_TX_TIMEOUT" envDefault:"10s"`

	// JWT_SIGNING_KEYS = {"kid":"base64-secret", ...}; semua kunci dipakai untuk verifikasi,
	// hanya JWT_ACTIVE_KID yang dipakai untuk menandatangani (rotasi tanpa logout massal).
	JWTSigningKeysJSON string        `env:"JWT_SIGNING_KEYS"`
	JWTActiveKID       string        `env:"JWT_ACTIVE_KID"`
	JWTIssuer          string        `env:"JWT_ISSUER" envDefault:"ledgerpay"`
	JWTAccessTTL       time.Duration `env:"JWT_ACCESS_TTL" envDefault:"15m"`

	RefreshTTL       time.Duration `env:"REFRESH_TTL" envDefault:"168h"`        // 7 hari, sliding
	RefreshFamilyTTL time.Duration `env:"REFRESH_FAMILY_TTL" envDefault:"720h"` // 30 hari, absolut
	CookieSecure     bool          `env:"COOKIE_SECURE" envDefault:"true"`

	// Origin SPA yang boleh memanggil endpoint cookie (/auth/refresh, /auth/logout; CSRF C-08).
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envSeparator:"," envDefault:"http://localhost:5173"`
	// CIDR proxy tepercaya (Caddy). Hanya dari alamat ini X-Forwarded-For dipercaya (C-07, T-SH-15).
	TrustedProxiesRaw []string `env:"TRUSTED_PROXIES" envSeparator:","`
	// Basis URL publik SPA, untuk checkout_url payment intent.
	PublicBaseURL string `env:"PUBLIC_BASE_URL" envDefault:"http://localhost:5173"`
	// Batas tier money-write per user per menit (default konvensi API: 30). Demo B lokal menaikkannya,
	// karena demo itu sengaja mengirim 100 transfer dari satu user dalam satu detik.
	RateLimitMoneyWritePerMin int `env:"RATE_LIMIT_MONEY_WRITE_PER_MIN" envDefault:"30"`
	// Hanya perf env (load test mengukur kapasitas mentah, docs/design/23-performance-plan.md §5 LP-9):
	// semua tier rate limit dimatikan dan pprof dipasang di port ops internal. Ditolak di demo/production.
	RateLimitDisabled bool `env:"RATE_LIMIT_DISABLED"`
	// Kill switch env (docs/design/26-deploy-migrations-backup.md §4.3): false = matikan jalur itu tanpa build ulang.
	PayoutSubmitEnabled    bool `env:"LP_PAYOUT_SUBMIT_ENABLED" envDefault:"true"`
	WebhookDeliveryEnabled bool `env:"LP_WEBHOOK_DELIVERY_ENABLED" envDefault:"true"`
	TopupEnabled           bool `env:"LP_TOPUP_ENABLED" envDefault:"true"`
	// Mode demo (O15): endpoint /api/v1/demo/* HANYA di binary build tag `demo` DAN kalau DEMO_MODE=true;
	// setiap request wajib header X-Demo-Token. DEMO_MODE=true tanpa token (≥ 24 karakter) = gagal start.
	DemoMode     bool   `env:"DEMO_MODE"`
	DemoToken    string `env:"DEMO_TOKEN"`
	PprofEnabled bool   `env:"PPROF_ENABLED"`

	// Payment provider (mock-provider di V1, ADR-L05).
	ProviderBaseURL        string   `env:"PROVIDER_BASE_URL"`                         // contoh http://mock-provider:9090
	ProviderAPISecret      string   `env:"PROVIDER_API_SECRET"`                       // Authorization: Bearer ke provider
	ProviderWebhookSecrets []string `env:"PROVIDER_WEBHOOK_SECRETS" envSeparator:","` // >1 saat rotasi
	ProviderCallbackURL    string   `env:"PROVIDER_CALLBACK_URL"`                     // URL webhook LedgerPay yang dipanggil provider

	// Enkripsi PII at-rest: {"v1":"base64-32B"}; versi aktif untuk enkripsi baru (rotasi: §6.3).
	PIIKeyringJSON   string `env:"PII_KEYRING"`
	PIIActiveVersion string `env:"PII_ACTIVE_KEY_VERSION"`
	// Webhook keluar: CIDR yang boleh dituju walaupun private. HANYA dev/test (contoh jaringan compose);
	// ditolak di environment lain (C-44). Di luar dev/test, URL webhook wajib https.
	WebhookEgressAllowCIDRs []string `env:"WEBHOOK_EGRESS_ALLOW_CIDRS" envSeparator:","`
	// Safe window kontrak bank (ADR-L06): NOT_FOUND baru boleh berarti "tidak pernah diterima" setelah ini.
	PayoutSafeWindow time.Duration `env:"PAYOUT_SAFE_WINDOW" envDefault:"5m"`
	// OpenTelemetry: contoh http://jaeger:4318/v1/traces. Kosong = tracing nonaktif.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	WorkerID           string        `env:"WORKER_ID"` // default: hostname
	WorkerPollInterval time.Duration `env:"WORKER_POLL_INTERVAL" envDefault:"500ms"`

	// Diisi dari JWTSigningKeysJSON / TrustedProxiesRaw / dst. setelah validasi.
	JWTKeys           map[string][]byte `env:"-"`
	TrustedProxies    []netip.Prefix    `env:"-"`
	Keyring           *crypto.Keyring   `env:"-"`
	EgressAllow       []netip.Prefix    `env:"-"`
	RequireHTTPSHooks bool              `env:"-"`
}

// Dev melaporkan apakah environment adalah dev/test (pagar untuk fitur yang tidak aman di produksi).
func (c *Config) Dev() bool { return c.AppEnv == "dev" || c.AppEnv == "test" }

// validateShared: keyring PII + kebijakan egress, dipakai api dan worker.
func (c *Config) validateShared() []error {
	var errs []error
	kr, err := crypto.ParseKeyring(c.PIIKeyringJSON, c.PIIActiveVersion)
	if err != nil {
		errs = append(errs, fmt.Errorf("PII_KEYRING / PII_ACTIVE_KEY_VERSION: %w", err))
	}
	c.Keyring = kr
	for _, raw := range c.WebhookEgressAllowCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			errs = append(errs, fmt.Errorf("WEBHOOK_EGRESS_ALLOW_CIDRS: %q bukan CIDR", raw))
			continue
		}
		c.EgressAllow = append(c.EgressAllow, p)
	}
	if len(c.EgressAllow) > 0 && !c.Dev() {
		errs = append(errs, errors.New("WEBHOOK_EGRESS_ALLOW_CIDRS hanya boleh di APP_ENV dev/test (SSRF, C-44)"))
	}
	c.RequireHTTPSHooks = !c.Dev()
	if c.DemoMode && len(c.DemoToken) < 24 {
		errs = append(errs, errors.New("DEMO_MODE=true membutuhkan DEMO_TOKEN minimal 24 karakter"))
	}
	if (c.RateLimitDisabled || c.PprofEnabled) && !c.Dev() && c.AppEnv != "perf" {
		errs = append(errs, errors.New("RATE_LIMIT_DISABLED / PPROF_ENABLED hanya boleh di APP_ENV dev/test/perf"))
	}
	return errs
}

// Load membaca env. Validasi per subcommand ada di ValidateAPI (dan nanti ValidateWorker).
func Load() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// ValidateAPI memastikan semua yang dibutuhkan subcommand `api` tersedia.
func (c *Config) ValidateAPI() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL wajib"))
	}
	keys, err := parseKeys(c.JWTSigningKeysJSON)
	if err != nil {
		errs = append(errs, err)
	}
	if _, ok := keys[c.JWTActiveKID]; !ok {
		errs = append(errs, fmt.Errorf("JWT_ACTIVE_KID %q tidak ada di JWT_SIGNING_KEYS", c.JWTActiveKID))
	}
	if c.AppEnv != "dev" && c.AppEnv != "test" && !c.CookieSecure {
		errs = append(errs, errors.New("COOKIE_SECURE=false hanya boleh di dev/test"))
	}
	for _, raw := range c.TrustedProxiesRaw {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %q bukan CIDR: %w", raw, err))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}
	if c.ProviderBaseURL == "" || c.ProviderAPISecret == "" || len(c.ProviderWebhookSecrets) == 0 || c.ProviderCallbackURL == "" {
		errs = append(errs, errors.New("PROVIDER_BASE_URL, PROVIDER_API_SECRET, PROVIDER_WEBHOOK_SECRETS, PROVIDER_CALLBACK_URL wajib"))
	}
	if c.RateLimitMoneyWritePerMin <= 0 {
		errs = append(errs, errors.New("RATE_LIMIT_MONEY_WRITE_PER_MIN harus > 0"))
	}
	errs = append(errs, c.validateShared()...)
	c.JWTKeys = keys
	return errors.Join(errs...)
}

// ValidateWorker memastikan semua yang dibutuhkan subcommand `worker` tersedia.
func (c *Config) ValidateWorker() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL wajib"))
	}
	if c.ProviderBaseURL == "" || c.ProviderAPISecret == "" {
		errs = append(errs, errors.New("PROVIDER_BASE_URL dan PROVIDER_API_SECRET wajib (verify-by-fetch, payout)"))
	}
	errs = append(errs, c.validateShared()...)
	return errors.Join(errs...)
}

func parseKeys(raw string) (map[string][]byte, error) {
	if raw == "" {
		return nil, errors.New("JWT_SIGNING_KEYS wajib")
	}
	var enc map[string]string
	if err := json.Unmarshal([]byte(raw), &enc); err != nil {
		return nil, fmt.Errorf("JWT_SIGNING_KEYS harus JSON {kid: base64}: %w", err)
	}
	keys := make(map[string][]byte, len(enc))
	for kid, b64 := range enc {
		k, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("JWT key %q bukan base64: %w", kid, err)
		}
		if len(k) < 32 { // HS256: kunci minimal 256 bit
			return nil, fmt.Errorf("JWT key %q terlalu pendek (%d byte, minimal 32)", kid, len(k))
		}
		keys[kid] = k
	}
	return keys, nil
}
