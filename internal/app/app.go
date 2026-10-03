// Package app merakit dependency dan router. Ini satu-satunya tempat yang boleh mengenal
// semua modul sekaligus (composition root); modul tidak saling meng-import (ADR-001).
package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/db/migrations"
	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/merchant"
	"ledgerpay/internal/modules/payment"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/modules/wallet"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/ratelimit"
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
}

// APIRouter adalah router API publik, di-mount di /api/v1.
func APIRouter(d Deps) http.Handler {
	tiers := ratelimit.DefaultTiers()
	if d.Tiers != nil {
		tiers = *d.Tiers
	}
	limiter := ratelimit.New(d.Clock)
	users := usersAdapter{identity.NewDirectory()}

	ledgerSvc := ledger.NewService(d.IDs)
	idem := idempotency.NewService(d.Pool, d.IDs, d.Clock)
	idSvc := identity.NewService(d.Pool, ledgerSvc, d.Tokens, d.Clock, d.IDs, d.Identity)
	idH := identity.NewHandler(idSvc, identity.HandlerConfig{
		CookieSecure: d.CookieSecure, AllowedOrigins: d.AllowedOrigins, Limiter: limiter, Tiers: tiers,
	})
	walletH := wallet.NewHandler(d.Pool, ledgerSvc, d.Clock)
	transferH := transfer.NewHandler(d.Pool, transfer.NewService(ledgerSvc, users, d.IDs), idem)
	merchantH := merchant.NewHandler(merchant.NewService(d.Pool, ledgerSvc, d.IDs, d.Clock), idem)
	paymentH := payment.NewHandler(d.Pool, payment.NewService(ledgerSvc, users, d.IDs), idem, d.PublicBaseURL)

	r := chi.NewRouter()
	r.Use(httpx.RequestID(d.Logger), httpx.Recover, httpx.SecurityHeaders, httpx.ClientIP(d.TrustedProxies),
		httpx.AccessLog, httpx.BodyLimit(httpx.MaxBodyBytes))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpx.WriteError(w, r, apperr.ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { httpx.WriteError(w, r, apperr.ErrNotFound) })

	byUser := func(r *http.Request) string { p, _ := auth.PrincipalFrom(r.Context()); return p.UserID.String() }
	byMerchant := func(r *http.Request) string { m, _ := auth.MerchantFrom(r.Context()); return m.MerchantID.String() }

	r.Route("/api/v1", func(v1 chi.Router) {
		authed := v1.With(auth.RequireUser(d.Tokens))
		read := authed.With(limiter.Middleware(tiers.Read, byUser))        // GET user: 300/menit
		money := authed.With(limiter.Middleware(tiers.MoneyWrite, byUser)) // pindah uang: 30/menit
		ops := authed.With(auth.RequireRole("OPS"))                        // /ops/*: role OPS
		merchantAPI := v1.With(merchantH.RequireMerchant, limiter.Middleware(tiers.MerchantAPI, byMerchant))

		idH.Routes(v1, read)
		walletH.Routes(read)
		transferH.Routes(read, money)
		paymentH.UserRoutes(read, money)
		paymentH.MerchantRoutes(merchantAPI)
		merchantH.OpsRoutes(ops)
	})
	return r
}

// OpsRouter: /healthz dan /readyz di port internal (tidak diekspos Caddy; Step 8 `01` §2).
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
		httpx.WriteJSON(w, status, map[string]any{"status": state, "checks": checks, "schema_version": v, "min_schema_version": migrations.MinSchemaVersion})
	})
	return r
}
