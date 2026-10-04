package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/ratelimit"
)

// RefreshCookieName dan path mengikuti ADR-009: cookie hanya dikirim browser ke /api/v1/auth/*.
const (
	RefreshCookieName = "lp_refresh"
	refreshCookiePath = "/api/v1/auth"
)

// HandlerConfig: pengaturan HTTP identity.
type HandlerConfig struct {
	CookieSecure   bool
	AllowedOrigins []string // untuk cek Origin di endpoint cookie (CSRF, C-08)
	Limiter        *ratelimit.Limiter
	Tiers          ratelimit.Tiers
}

type Handler struct {
	svc *Service
	cfg HandlerConfig
}

func NewHandler(svc *Service, cfg HandlerConfig) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

// Routes mendaftarkan rute publik (tanpa auth) dan rute yang butuh user.
// Semua /auth/* dibatasi tier auth-ip (per IP). Login/register/refresh juga tier auth-strict
// (per IP + email), yang dicek di handler karena butuh email dari body.
func (h *Handler) Routes(public, authed chi.Router) {
	public.Group(func(r chi.Router) {
		r.Use(h.cfg.Limiter.Middleware(h.cfg.Tiers.AuthIP, ratelimit.ByIP))
		r.Post("/auth/register", h.register)
		r.Post("/auth/login", h.login)
		r.Group(func(r chi.Router) { // endpoint yang diautentikasi lewat cookie → CSRF guard
			r.Use(httpx.CSRFGuard(h.cfg.AllowedOrigins))
			r.Post("/auth/refresh", h.refresh)
			r.Post("/auth/logout", h.logout)
		})
	})
	authed.Get("/me", h.me)
}

// strict menerapkan tier auth-strict untuk (IP, subjek). Email tak terdaftar ikut dibatasi,
// sehingga rate limit tidak bisa dipakai membedakan email yang ada dan tidak (C-03).
func (h *Handler) strict(w http.ResponseWriter, r *http.Request, subject string) bool {
	ok, wait := h.cfg.Limiter.Allow(h.cfg.Tiers.AuthStrict, httpx.ClientIPFrom(r).String()+"|"+strings.ToLower(subject))
	if !ok {
		ratelimit.Reject(w, r, wait)
	}
	return ok
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.strict(w, r, "register|"+in.Email) {
		return
	}
	u, err := h.svc.Register(r.Context(), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, u)
}

type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in LoginInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.strict(w, r, "login|"+in.Email) {
		return
	}
	res, err := h.svc.Login(r.Context(), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.setRefreshCookie(w, res.RefreshToken, res.RefreshMaxAge)
	writeAccess(w, res)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	// Key auth-strict untuk refresh = IP + SESI (hash cookie), bukan IP saja. Temuan E2E Sprint 5:
	// SPA memanggil refresh setiap kali dimuat ulang, jadi limit 5/menit per IP membuat semua user
	// di belakang satu NAT (kantor) ter-logout. Tanpa cookie tidak dihitung (langsung 401, tanpa
	// query DB); tier auth-ip 30/menit per IP tetap membatasi penyerang yang merotasi cookie palsu.
	raw := refreshCookie(r)
	if raw != "" {
		sum := sha256.Sum256([]byte(raw))
		if !h.strict(w, r, "refresh|"+hex.EncodeToString(sum[:8])) {
			return
		}
	}
	res, err := h.svc.Refresh(r.Context(), raw, meta(r))
	if err != nil {
		if apperr.As(err).Code == apperr.ErrRefreshReused.Code {
			h.setRefreshCookie(w, "", -1) // family sudah dicabut: hapus cookie di browser
		}
		httpx.WriteError(w, r, err)
		return
	}
	if res.RefreshToken != "" { // grace window: tidak ada token baru, cookie penerus sudah di browser
		h.setRefreshCookie(w, res.RefreshToken, res.RefreshMaxAge)
	}
	writeAccess(w, res)
}

// logout selalu 204 (cookie tidak valid pun "berhasil" logout) dan selalu menghapus cookie.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), refreshCookie(r), meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.setRefreshCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func refreshCookie(r *http.Request) string {
	c, err := r.Cookie(RefreshCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// setRefreshCookie: maxAge < 0 menghapus cookie.
func (h *Handler) setRefreshCookie(w http.ResponseWriter, value string, maxAge time.Duration) {
	c := &http.Cookie{ //nolint:gosec // G124: Secure=false hanya diizinkan di dev/test; dijaga config.ValidateAPI.
		Name:     RefreshCookieName,
		Value:    value,
		Path:     refreshCookiePath,
		HttpOnly: true,                    // tidak terbaca JavaScript (mitigasi XSS)
		Secure:   h.cfg.CookieSecure,      // hanya lewat HTTPS (false hanya di dev)
		SameSite: http.SameSiteStrictMode, // tidak dikirim dari situs lain (mitigasi CSRF)
		MaxAge:   int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func writeAccess(w http.ResponseWriter, res LoginResult) {
	httpx.WriteJSON(w, http.StatusOK, accessTokenResponse{
		AccessToken: res.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   int64(res.AccessExpiresIn.Seconds()),
	})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.ErrUnauthenticated)
		return
	}
	u, err := h.svc.Me(r.Context(), p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

func meta(r *http.Request) RequestMeta {
	m := RequestMeta{RequestID: httpx.RequestIDFrom(r.Context()), UserAgent: r.UserAgent()}
	if ip := httpx.ClientIPFrom(r); ip.IsValid() {
		m.IP = &ip
	}
	return m
}
