package topup

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
)

type Handler struct {
	pool *pgxpool.Pool
	svc  *Service
	idem *idempotency.Service
}

func NewHandler(pool *pgxpool.Pool, svc *Service, idem *idempotency.Service) *Handler {
	return &Handler{pool: pool, svc: svc, idem: idem}
}

// Routes: POST dibatasi tier money-write, GET tier read (middleware dipasang pemanggil).
func (h *Handler) Routes(read, moneyWrite chi.Router) {
	moneyWrite.Post("/topups", h.create)
	read.Get("/topups/{id}", h.get)
}

type createRequest struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type topupResponse struct {
	ID            uuid.UUID  `json:"id"`
	Amount        string     `json:"amount"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status"`
	CheckoutURL   *string    `json:"checkout_url"` // null: charge belum dibuat di provider (retry dengan key yang sama)
	FailureReason *string    `json:"failure_reason,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	SucceededAt   *time.Time `json:"succeeded_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func view(t Topup) topupResponse {
	return topupResponse{ID: t.ID, Amount: ledger.FormatAmount(t.Amount), Currency: t.Currency, Status: t.Status,
		CheckoutURL: t.CheckoutURL, FailureReason: t.FailureReason, ExpiresAt: t.ExpiresAt.UTC(), SucceededAt: t.SucceededAt, CreatedAt: t.CreatedAt.UTC()}
}

func (in createRequest) validate() (int64, error) {
	var fe []apperr.FieldError
	amount, err := ledger.ParseAmount(in.Amount)
	switch {
	case err != nil:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "INVALID_FORMAT"})
	case amount < MinAmount || amount > MaxAmount:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "OUT_OF_RANGE", Message: "1000000 – 1000000000"})
	}
	if in.Currency == "" {
		fe = append(fe, apperr.FieldError{Field: "currency", Code: "REQUIRED"})
	}
	if len(fe) > 0 {
		return 0, apperr.Validation(fe...)
	}
	if in.Currency != ledger.SupportedCurrency {
		return 0, apperr.ErrCurrencyNotSupported
	}
	return amount, nil
}

// create: ADR-L02 pola (b). Endpoint ini TIDAK menambah saldo; ia hanya membuat sesi pembayaran.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
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
	var in createRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	amount, err := in.validate()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	// tx1: key (respons NULL) + top-up CREATED.
	res, err := h.idem.Reserve(r, idempotency.Request{
		ClientType: idempotency.ClientUser, ClientID: p.UserID, Endpoint: "POST /v1/topups", Key: key, Hash: hash,
	}, "topup", func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (uuid.UUID, error) {
		return h.svc.CreatePending(ctx, tx, p.UserID, amount, in.Currency)
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.Done != nil { // replay, atau penolakan 422 yang sudah disimpan
		idempotency.Write(w, *res.Done)
		return
	}

	// Panggil provider (di luar tx) lalu tx2: provider_ref + simpan respons.
	var resp idempotency.Response
	t, err := h.svc.EnsureCharge(r.Context(), h.pool, res.ResourceID, func(ctx context.Context, tx pgx.Tx, t Topup) error {
		var err error
		resp, err = idempotency.Complete(ctx, tx, res.KeyID, idempotency.Outcome{Status: http.StatusCreated, Body: view(t), ResourceType: "topup", ResourceID: &t.ID})
		return err
	})
	switch {
	case errors.Is(err, ErrProviderPending):
		// Kontrak §6: 201 dengan checkout_url null; respons TIDAK disimpan (key masih "berjalan"),
		// jadi retry dengan key yang sama melanjutkan dari recovery point CREATED.
		httpx.WriteJSON(w, http.StatusCreated, view(t))
	case err != nil:
		httpx.WriteError(w, r, err)
	default:
		idempotency.Write(w, resp)
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	t, err := h.svc.ForUser(r.Context(), h.pool, p.UserID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view(t))
}
