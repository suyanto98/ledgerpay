package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/observability"
)

// Principal adalah identitas pemanggil yang sudah terverifikasi. Identitas SELALU diambil dari
// token yang diverifikasi server, tidak pernah dari body/query (cegah privilege escalation).
type Principal struct {
	UserID uuid.UUID
	Roles  []string
}

type principalKey struct{}

// PrincipalFrom mengambil principal; ok=false jika request tidak terautentikasi.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithPrincipal dipakai middleware (dan test) untuk menaruh principal di context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// HasRole: apakah principal memiliki role tertentu (contoh "OPS").
func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// RequireRole dipasang SETELAH RequireUser. User tanpa role → 403 (endpoint ops bukan rahasia,
// jadi 403 tidak membocorkan apa pun; ADR-010).
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, r, apperr.ErrUnauthenticated)
				return
			}
			if !p.HasRole(role) {
				httpx.WriteError(w, r, apperr.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MerchantPrincipal adalah identitas merchant dari API key (principal kedua, ADR-010).
type MerchantPrincipal struct {
	MerchantID uuid.UUID
	KeyID      uuid.UUID
}

type merchantKey struct{}

// WithMerchant menaruh merchant principal di context (dipakai middleware API key dan test).
func WithMerchant(ctx context.Context, m MerchantPrincipal) context.Context {
	return context.WithValue(ctx, merchantKey{}, m)
}

// MerchantFrom mengambil merchant principal; ok=false jika request bukan dari merchant.
func MerchantFrom(ctx context.Context) (MerchantPrincipal, bool) {
	m, ok := ctx.Value(merchantKey{}).(MerchantPrincipal)
	return m, ok
}

// RequireUser memverifikasi "Authorization: Bearer <jwt>" dan menaruh Principal di context.
func RequireUser(t *Tokens) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				httpx.WriteError(w, r, apperr.ErrUnauthenticated)
				return
			}
			claims, err := t.Verify(raw)
			switch {
			case errors.Is(err, ErrTokenExpired):
				httpx.WriteError(w, r, apperr.ErrTokenExpired)
				return
			case err != nil:
				observability.Logger(r.Context()).Info("auth.token_rejected", "reason", err.Error())
				httpx.WriteError(w, r, apperr.ErrUnauthenticated)
				return
			}
			p := Principal{UserID: uuid.MustParse(claims.Subject), Roles: claims.Roles}
			httpx.SetUserID(r.Context(), p.UserID.String()) // agar log akses memuat user_id
			ctx := WithPrincipal(r.Context(), p)
			ctx = observability.WithLogger(ctx, observability.Logger(ctx).With("user_id", p.UserID.String()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
