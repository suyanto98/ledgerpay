package identity

import (
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/httpx"
)

// RefreshCookieName dan path mengikuti ADR-009: cookie hanya dikirim browser ke /api/v1/auth/*.
const (
	RefreshCookieName = "lp_refresh"
	refreshCookiePath = "/api/v1/auth"
)

type Handler struct {
	svc          *Service
	cookieSecure bool
}

func NewHandler(svc *Service, cookieSecure bool) *Handler {
	return &Handler{svc: svc, cookieSecure: cookieSecure}
}

// Routes mendaftarkan rute publik (tanpa auth) dan rute yang butuh user.
func (h *Handler) Routes(public, authed chi.Router) {
	public.Post("/auth/register", h.register)
	public.Post("/auth/login", h.login)
	authed.Get("/me", h.me)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
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
	res, err := h.svc.Login(r.Context(), in, meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	//nolint:gosec // G124: Secure=false hanya diizinkan di dev/test; dijaga config.ValidateAPI.
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    res.RefreshToken,
		Path:     refreshCookiePath,
		Expires:  res.RefreshExpiresAt,
		MaxAge:   int(res.RefreshMaxAge.Seconds()),
		HttpOnly: true,                    // tidak terbaca JavaScript (mitigasi XSS)
		Secure:   h.cookieSecure,          // hanya lewat HTTPS (false hanya di dev)
		SameSite: http.SameSiteStrictMode, // tidak dikirim dari situs lain (mitigasi CSRF)
	})
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
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		a := ap.Addr()
		m.IP = &a
	}
	return m
}
