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
			ctx := WithPrincipal(r.Context(), p)
			ctx = observability.WithLogger(ctx, observability.Logger(ctx).With("user_id", p.UserID.String()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
