package merchant

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/observability"
)

type Handler struct {
	svc  *Service
	idem *idempotency.Service
}

func NewHandler(svc *Service, idem *idempotency.Service) *Handler {
	return &Handler{svc: svc, idem: idem}
}

// OpsRoutes: dipasang di grup yang sudah melewati RequireUser + RequireRole("OPS").
func (h *Handler) OpsRoutes(ops chi.Router) {
	ops.Post("/ops/merchants/{id}/api-keys", h.createKey)
	ops.Post("/ops/api-keys/{id}/revoke", h.revokeKey)
}

// RequireMerchant memverifikasi "Authorization: Bearer lp_test_…" untuk /merchant/*.
func (h *Handler) RequireMerchant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			httpx.WriteError(w, r, apperr.ErrUnauthenticated)
			return
		}
		m, err := h.svc.Authenticate(r.Context(), raw)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := auth.WithMerchant(r.Context(), m)
		ctx = observability.WithLogger(ctx, observability.Logger(ctx).With("merchant_id", m.MerchantID.String()))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type createKeyRequest struct {
	Label     *string    `json:"label"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type keyResponse struct {
	ID        uuid.UUID `json:"id"`
	Prefix    string    `json:"prefix"`
	Slot      int16     `json:"slot"`
	APIKey    *string   `json:"api_key"`        // hanya terisi pada respons PERTAMA
	Note      string    `json:"note,omitempty"` // dijelaskan di respons replay
	CreatedAt time.Time `json:"created_at"`
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	merchantID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	key, err := idempotency.KeyFrom(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	raw, err := idempotency.ReadBody(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in createKeyRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.Label != nil && len(*in.Label) > 100 {
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "label", Code: "TOO_LONG"}))
		return
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.idem.Execute(r, idempotency.Request{
		ClientType: idempotency.ClientUser, ClientID: p.UserID, Endpoint: "POST /v1/ops/merchants/{id}/api-keys", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (idempotency.Outcome, error) {
		k, err := h.svc.IssueKey(ctx, tx, merchantID, p.UserID, in.Label, in.ExpiresAt, httpx.RequestIDFrom(ctx))
		if err != nil {
			return idempotency.Outcome{}, err
		}
		first := keyResponse{ID: k.ID, Prefix: k.Prefix, Slot: k.Slot, APIKey: &k.APIKey, CreatedAt: k.CreatedAt}
		replay := first
		replay.APIKey = nil
		replay.Note = "api_key is shown only once; if it was lost, revoke this key and create a new one"
		return idempotency.Outcome{Status: http.StatusCreated, Body: first, Stored: replay, ResourceType: "merchant_api_key", ResourceID: &k.ID}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

type revokeRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	keyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	var in revokeRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	k, err := h.svc.RevokeKey(r.Context(), keyID, p.UserID, in.Reason, httpx.RequestIDFrom(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": k.ID, "prefix": k.Prefix, "revoked_at": k.RevokedAt.UTC()})
}
