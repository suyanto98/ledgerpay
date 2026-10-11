// Package app merakit dependency dan router. Ini satu-satunya tempat yang boleh mengenal
// semua modul sekaligus (composition root); modul tidak saling meng-import (ADR-001).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/db/migrations"
	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/inbox"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/merchant"
	"ledgerpay/internal/modules/ops"
	"ledgerpay/internal/modules/payment"
	"ledgerpay/internal/modules/payout"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/modules/topup"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/modules/wallet"
	"ledgerpay/internal/modules/webhookout"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/crypto"
	"ledgerpay/internal/platform/egress"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/metrics"
	"ledgerpay/internal/platform/ratelimit"
	"ledgerpay/internal/platform/tracing"
)

// Deps adalah dependency yang dibuat oleh main (atau test).
type Deps struct {
	Logger       *slog.Logger
	Pool         *pgxpool.Pool
	Tokens       *auth.Tokens
	Clock        clock.Clock
	IDs          idgen.Generator
	Identity     identity.Config
	CookieSecure bool

	AllowedOrigins []string
	TrustedProxies []netip.Prefix
	PublicBaseURL  string
	Tiers          *ratelimit.Tiers // nil → ratelimit.DefaultTiers()

	Provider       topup.Provider
	CallbackURL    string   // URL webhook LedgerPay yang diberikan ke provider
	WebhookSecrets []string // secret HMAC webhook provider (lebih dari satu saat rotasi)

	Settlements      recon.Source // laporan settlement provider (rekonsiliasi)
	Bank             payout.Bank
	Keyring          *crypto.Keyring // PII at-rest (rekening payout, secret webhook merchant)
	Egress           egress.Policy   // webhook keluar ke merchant (anti-SSRF)
	PayoutSafeWindow time.Duration

	// Kill switch (docs/design/26-deploy-migrations-backup.md §4.3), dibalik supaya nilai nol = aktif.
	DisablePayoutSubmit    bool // lane money berhenti meng-claim payout.submit; POST /payouts tetap menahan dana
	DisableWebhookDelivery bool // lane webhook-out berhenti; event menumpuk aman di jobs
	DisableTopup           bool // POST /topups → 503; inbox tetap memproses webhook yang masuk

	DemoMode  bool   // hanya berpengaruh di binary build tag `demo` (O15)
	DemoToken string // header X-Demo-Token

	DemoSandboxTTL      time.Duration // umur sandbox pengunjung; 0 → 24 jam
	DemoSandboxDailyMax int           // sandbox baru per 24 jam; 0 → 2000
}

// ValidateAPI memastikan dependency wajib API terisi. Dipanggil main saat START: wiring yang lupa
// (temuan Demo C Sprint 6: Keyring tidak diteruskan ke API → panic di POST /payouts pertama) harus
// menggagalkan boot, bukan meledak di tengah request uang.
func (d Deps) ValidateAPI() error {
	var errs []error
	for name, missing := range map[string]bool{
		"Pool": d.Pool == nil, "Tokens": d.Tokens == nil, "Clock": d.Clock == nil, "IDs": d.IDs == nil,
		"Provider": d.Provider == nil, "Keyring": d.Keyring == nil, "CallbackURL": d.CallbackURL == "", "WebhookSecrets": len(d.WebhookSecrets) == 0,
		"Settlements": d.Settlements == nil,
	} {
		if missing {
			errs = append(errs, fmt.Errorf("app: dependency %s is required", name))
		}
	}
	return errors.Join(errs...)
}

// ValidateWorker: dependency wajib proses worker.
func (d Deps) ValidateWorker() error {
	var errs []error
	for name, missing := range map[string]bool{
		"Pool": d.Pool == nil, "Clock": d.Clock == nil, "IDs": d.IDs == nil, "Provider": d.Provider == nil,
		"Bank": d.Bank == nil, "Keyring": d.Keyring == nil, "Settlements": d.Settlements == nil,
	} {
		if missing {
			errs = append(errs, fmt.Errorf("app: dependency %s is required", name))
		}
	}
	return errors.Join(errs...)
}

// services dirakit di SATU tempat, dipakai API dan worker, supaya keduanya memakai wiring yang sama.
type services struct {
	ledger   *ledger.Service
	idem     *idempotency.Service
	users    usersAdapter
	merchant *merchant.Service
	webhooks *webhookout.Service
	payment  *payment.Service
	topup    *topup.Service
	payout   *payout.Service
	recon    *recon.Service
}

func build(d Deps) services {
	s := services{ledger: ledger.NewService(d.IDs), idem: idempotency.NewService(d.Pool, d.IDs, d.Clock), users: usersAdapter{identity.NewDirectory()}}
	s.merchant = merchant.NewService(d.Pool, s.ledger, d.IDs, d.Clock, d.Keyring)
	s.webhooks = webhookout.NewService(d.Pool, merchantTargets{s.merchant}, egress.NewClient(d.Egress), d.IDs, d.Clock)
	s.payment = payment.NewService(s.ledger, s.users, s.webhooks, d.IDs)
	s.topup = topup.NewService(s.ledger, d.Provider, d.IDs, topup.Config{CallbackURL: d.CallbackURL, ReturnBaseURL: strings.TrimRight(d.PublicBaseURL, "/")})
	s.payout = payout.NewService(d.Pool, s.ledger, d.Bank, d.Keyring, d.IDs, payout.Config{SafeWindow: d.PayoutSafeWindow})
	s.recon = recon.NewService(d.Pool, s.ledger, d.Settlements, d.IDs)
	return s
}

// InboxProcessor merakit worker inbox (dipakai `ledgerpay worker` dan test).
func InboxProcessor(d Deps, cfg inbox.ProcessorConfig) *inbox.Processor {
	return inbox.NewProcessor(d.Pool, map[string]inbox.Applier{"charge": build(d).topup}, cfg)
}

// APIRouter adalah router API publik, di-mount di /api/v1.
func APIRouter(d Deps) http.Handler {
	tiers := ratelimit.DefaultTiers()
	if d.Tiers != nil {
		tiers = *d.Tiers
	}
	limiter := ratelimit.New(d.Clock)
	sv := build(d)
	users, ledgerSvc, idem := sv.users, sv.ledger, sv.idem
	idSvc := identity.NewService(d.Pool, ledgerSvc, d.Tokens, d.Clock, d.IDs, d.Identity)
	idH := identity.NewHandler(idSvc, identity.HandlerConfig{
		CookieSecure: d.CookieSecure, AllowedOrigins: d.AllowedOrigins, Limiter: limiter, Tiers: tiers,
	})
	transferSvc := transfer.NewService(ledgerSvc, users, d.IDs)
	walletH := wallet.NewHandler(d.Pool, ledgerSvc, d.Clock, txLabels{transfers: transferSvc, payments: sv.payment, users: identity.NewDirectory()})
	transferH := transfer.NewHandler(d.Pool, transferSvc, idem)
	merchantH := merchant.NewHandler(sv.merchant, idem)
	paymentH := payment.NewHandler(d.Pool, sv.payment, idem, d.PublicBaseURL, d.Clock)
	topupH := topup.NewHandler(d.Pool, sv.topup, idem)
	payoutH := payout.NewHandler(d.Pool, sv.payout, idem)
	inboxH := inbox.NewHandler(d.Pool, d.WebhookSecrets, d.Clock, d.IDs)
	opsH := ops.NewHandler(d.Pool, idem, d.IDs, sv.payout, sv.webhooks, reconAdapter{})
	reconH := recon.NewHandler(d.Pool, sv.recon, d.Clock)

	r := chi.NewRouter()
	r.Use(httpx.RequestID(d.Logger), httpx.Recover, tracing.Middleware, httpx.Metrics, httpx.SecurityHeaders,
		httpx.ClientIP(d.TrustedProxies), httpx.AccessLog, httpx.BodyLimit(httpx.MaxBodyBytes))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpx.WriteError(w, r, apperr.ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { httpx.WriteError(w, r, apperr.ErrNotFound) })

	byUser := func(r *http.Request) string { p, _ := auth.PrincipalFrom(r.Context()); return p.UserID.String() }
	byMerchant := func(r *http.Request) string { m, _ := auth.MerchantFrom(r.Context()); return m.MerchantID.String() }

	r.Route("/api/v1", func(v1 chi.Router) {
		authed := v1.With(auth.RequireUser(d.Tokens))
		read := authed.With(limiter.Middleware(tiers.Read, byUser))        // GET user: 300/menit
		money := authed.With(limiter.Middleware(tiers.MoneyWrite, byUser)) // pindah uang: 30/menit
		opsAll := authed.With(auth.RequireRole("OPS"))                     // /ops/*: role OPS
		opsRead := opsAll.With(limiter.Middleware(tiers.Read, byUser))
		opsWrite := opsAll.With(limiter.Middleware(tiers.MoneyWrite, byUser)) // aksi ops bisa memindahkan uang
		merchantAPI := v1.With(merchantH.RequireMerchant, limiter.Middleware(tiers.MerchantAPI, byMerchant))

		idH.Routes(v1, read)
		walletH.Routes(read)
		transferH.Routes(read, money)
		paymentH.UserRoutes(read, money)
		paymentH.MerchantRoutes(merchantAPI)
		merchantH.OpsRoutes(opsAll)
		opsH.Routes(opsRead, opsWrite)
		reconH.Routes(opsRead, opsWrite)
		topupH.Routes(read, money.With(killSwitch(d.DisableTopup, "topup")))
		payoutH.Routes(read, money)
		inboxH.Routes(v1) // publik: autentikasi = tanda tangan HMAC provider
		// build tag `demo` + DEMO_MODE; no-op di build release (fault: + X-Demo-Token; sandbox: publik, rate limit per IP)
		registerDemoRoutes(demoWiring{public: v1, read: read, money: money, identity: idSvc, identityH: idH,
			transfers: transferSvc, sv: sv, limiter: limiter, tiers: tiers}, d)
	})
	return r
}

// demoWiring: apa yang dibutuhkan rute demo (demo_routes.go) dari router API. Di build release tidak dipakai.
type demoWiring struct {
	public, read, money chi.Router
	identity            *identity.Service
	identityH           *identity.Handler
	transfers           *transfer.Service
	sv                  services
	limiter             *ratelimit.Limiter
	tiers               ratelimit.Tiers
}

// OpsRouter: /healthz dan /readyz di port internal (tidak diekspos Caddy; `21-observability.md` §2).
func OpsRouter(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	// Liveness: proses hidup. Sengaja TIDAK memeriksa DB: kalau DB mati, me-restart API tidak menolong.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Readiness: boleh menerima trafik? DB terjangkau dan skema cukup baru untuk binary ini.
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		checks := map[string]string{}
		ready := true
		v, err := migrations.SchemaVersion(ctx, pool)
		switch {
		case err != nil:
			checks["database"] = "unreachable"
			ready = false
		case v < migrations.MinSchemaVersion:
			checks["database"] = "ok"
			checks["schema"] = "too old"
			ready = false
		default:
			checks["database"] = "ok"
			checks["schema"] = "ok"
		}
		status := http.StatusOK
		state := "ready"
		if !ready {
			status, state = http.StatusServiceUnavailable, "not_ready"
		}
		metrics.Ready.WithLabelValues("api").Set(boolFloat(ready))
		httpx.WriteJSON(w, status, map[string]any{"status": state, "checks": checks, "schema_version": v, "min_schema_version": migrations.MinSchemaVersion})
	})
	r.Handle("/metrics", metrics.Handler())
	return r
}

// killSwitch: fitur dimatikan lewat konfigurasi (tanpa build) → 503 SERVICE_UNAVAILABLE. Dipakai saat
// insiden (contoh: provider bermasalah) supaya tidak ada request baru yang masuk ke jalur itu.
func killSwitch(disabled bool, feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !disabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet { // baca status tetap jalan
				next.ServeHTTP(w, r)
				return
			}
			httpx.WriteError(w, r, apperr.ErrUnavailable.WithDetail(feature+" is temporarily disabled"))
		})
	}
}
