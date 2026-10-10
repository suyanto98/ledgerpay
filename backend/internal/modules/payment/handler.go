package payment

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/metrics"
)

var orderIDPattern = regexp.MustCompile(`^[A-Za-z0-9_\-.:/]{1,64}$`)

type Handler struct {
	pool          *pgxpool.Pool
	svc           *Service
	idem          *idempotency.Service
	publicBaseURL string // untuk checkout_url
	clock         clock.Clock
}

func NewHandler(pool *pgxpool.Pool, svc *Service, idem *idempotency.Service, publicBaseURL string, clk clock.Clock) *Handler {
	return &Handler{pool: pool, svc: svc, idem: idem, publicBaseURL: strings.TrimRight(publicBaseURL, "/"), clock: clk}
}

// MerchantRoutes: grup yang sudah melewati middleware API key.
func (h *Handler) MerchantRoutes(r chi.Router) {
	r.Post("/merchant/payment-intents", h.create)
	r.Get("/merchant/payment-intents/{id}", h.getForMerchant)
	r.Post("/merchant/payment-intents/{id}/refunds", h.refund)
	r.Get("/merchant/balance", h.balance)
}

// UserRoutes: checkout oleh user login. confirm diberi middleware money-write oleh pemanggil.
func (h *Handler) UserRoutes(read, moneyWrite chi.Router) {
	read.Get("/payment-intents/{id}", h.getForUser)
	moneyWrite.Post("/payment-intents/{id}/confirm", h.confirm)
}

type createRequest struct {
	MerchantOrderID  string  `json:"merchant_order_id"`
	Amount           string  `json:"amount"`
	Currency         string  `json:"currency"`
	Description      *string `json:"description"`
	ExpiresInSeconds *int32  `json:"expires_in_seconds"`
}

func (in createRequest) validate(merchantID uuid.UUID) (CreateCommand, error) {
	var fe []apperr.FieldError
	if !orderIDPattern.MatchString(in.MerchantOrderID) {
		fe = append(fe, apperr.FieldError{Field: "merchant_order_id", Code: "INVALID_FORMAT"})
	}
	amount, err := ledger.ParseAmount(in.Amount)
	switch {
	case err != nil:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "INVALID_FORMAT"})
	case amount < MinAmount || amount > MaxAmount:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "OUT_OF_RANGE", Message: "100 – 10000000000"})
	}
	if in.Currency == "" {
		fe = append(fe, apperr.FieldError{Field: "currency", Code: "REQUIRED"})
	}
	var desc *string
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if utf8.RuneCountInString(d) > 200 {
			fe = append(fe, apperr.FieldError{Field: "description", Code: "TOO_LONG"})
		}
		if d != "" {
			desc = &d
		}
	}
	ttl := DefaultTTLSeconds
	if in.ExpiresInSeconds != nil {
		ttl = *in.ExpiresInSeconds
		if ttl < MinTTLSeconds || ttl > MaxTTLSeconds {
			fe = append(fe, apperr.FieldError{Field: "expires_in_seconds", Code: "OUT_OF_RANGE", Message: "300 – 86400"})
		}
	}
	if len(fe) > 0 {
		return CreateCommand{}, apperr.Validation(fe...)
	}
	if in.Currency != ledger.SupportedCurrency {
		return CreateCommand{}, apperr.ErrCurrencyNotSupported
	}
	return CreateCommand{MerchantID: merchantID, MerchantOrderID: in.MerchantOrderID, Amount: amount,
		Currency: in.Currency, Description: desc, TTLSeconds: ttl}, nil
}

// ---------------------------------------------------------------- views (docs/design/13-api-endpoints.md §2.6)

type merchantRef struct {
	ID   *uuid.UUID `json:"id,omitempty"` // hanya di merchant view
	Name string     `json:"name"`
}

type party struct {
	DisplayName string `json:"display_name"`
}

type intentView struct {
	ID              uuid.UUID    `json:"id"`
	Merchant        merchantRef  `json:"merchant"`
	MerchantOrderID string       `json:"merchant_order_id"`
	Amount          string       `json:"amount"`
	Currency        string       `json:"currency"`
	Description     *string      `json:"description"`
	Status          string       `json:"status"`
	RefundedAmount  *string      `json:"refunded_amount,omitempty"`
	CheckoutURL     string       `json:"checkout_url,omitempty"`
	Payer           *party       `json:"payer,omitempty"`
	Refunds         []refundView `json:"refunds,omitempty"`
	ExpiresAt       time.Time    `json:"expires_at"`
	ConfirmedAt     *time.Time   `json:"confirmed_at,omitempty"`
	CreatedAt       *time.Time   `json:"created_at,omitempty"`
}

type refundView struct {
	ID              uuid.UUID `json:"id"`
	PaymentIntentID uuid.UUID `json:"payment_intent_id"`
	Amount          string    `json:"amount"`
	Currency        string    `json:"currency"`
	Reason          *string   `json:"reason"`
	JournalID       uuid.UUID `json:"journal_id"`
	CreatedAt       time.Time `json:"created_at"`
}

func toRefundView(r Refund) refundView {
	return refundView{ID: r.ID, PaymentIntentID: r.PaymentIntentID, Amount: ledger.FormatAmount(r.Amount), Currency: r.Currency,
		Reason: r.Reason, JournalID: r.JournalID, CreatedAt: r.CreatedAt.UTC()}
}

// withRefunds melampirkan daftar refund (merchant view dan payer view, §2.6).
func (h *Handler) withRefunds(ctx context.Context, v intentView, in Intent) (intentView, error) {
	if !in.Paid() {
		return v, nil
	}
	rs, err := h.svc.Refunds(ctx, h.pool, in.ID)
	if err != nil {
		return v, err
	}
	v.Refunds = make([]refundView, 0, len(rs))
	for _, r := range rs {
		v.Refunds = append(v.Refunds, toRefundView(r))
	}
	return v, nil
}

func baseView(in Intent) intentView {
	return intentView{ID: in.ID, Merchant: merchantRef{Name: in.MerchantName}, MerchantOrderID: in.MerchantOrderID,
		Amount: ledger.FormatAmount(in.Amount), Currency: in.Currency, Description: in.Description, Status: in.Status,
		ExpiresAt: in.ExpiresAt.UTC()}
}

func (h *Handler) merchantView(in Intent) intentView {
	v := baseView(in)
	v.Merchant.ID = &in.MerchantID
	refunded := ledger.FormatAmount(in.RefundedAmount)
	v.RefundedAmount, v.CheckoutURL, v.ConfirmedAt, v.CreatedAt = &refunded, h.publicBaseURL+"/checkout/"+in.ID.String(), in.ConfirmedAt, &in.CreatedAt
	if in.PayerUserID != nil {
		v.Payer = &party{DisplayName: in.PayerName} // nama saja, tanpa ID/email user
	}
	return v
}

// userView: payer melihat detail pembayaran; viewer sebelum bayar hanya data checkout.
func userView(in Intent) intentView {
	v := baseView(in)
	if in.Paid() {
		refunded := ledger.FormatAmount(in.RefundedAmount)
		v.RefundedAmount, v.ConfirmedAt, v.CreatedAt = &refunded, in.ConfirmedAt, &in.CreatedAt
	}
	return v
}

// ---------------------------------------------------------------- handlers

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MerchantFrom(r.Context())
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
	cmd, err := in.validate(m.MerchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.idem.Execute(r, idempotency.Request{
		ClientType: "MERCHANT", ClientID: m.MerchantID, Endpoint: "POST /v1/merchant/payment-intents", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (idempotency.Outcome, error) {
		pi, err := h.svc.Create(ctx, tx, cmd)
		if err != nil {
			return idempotency.Outcome{}, err
		}
		return idempotency.Outcome{Status: http.StatusCreated, Body: h.merchantView(pi), ResourceType: "payment_intent", ResourceID: &pi.ID}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

func (h *Handler) getForMerchant(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MerchantFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	pi, err := h.svc.ForMerchant(r.Context(), h.pool, m.MerchantID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.withRefunds(r.Context(), h.merchantView(pi), pi)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Handler) getForUser(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	pi, err := h.svc.ForUser(r.Context(), h.pool, p.UserID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.withRefunds(r.Context(), userView(pi), pi)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// confirm: body {} (amount TIDAK pernah dikirim client; diambil dari intent). 200 karena tidak
// membuat resource baru, hanya mengubah status intent.
func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
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
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}") // body kosong diperlakukan sama dengan {}
	} else {
		var empty struct{}
		if err := httpx.Decode(r, &empty); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.idem.Execute(r, idempotency.Request{
		ClientType: idempotency.ClientUser, ClientID: p.UserID, Endpoint: "POST /v1/payment-intents/{id}/confirm", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (idempotency.Outcome, error) {
		pi, err := h.svc.Confirm(ctx, tx, p.UserID, id, httpx.RequestIDFrom(ctx))
		if err != nil {
			outcome := "error"
			switch apperr.As(err).Code {
			case apperr.ErrInsufficientBalance.Code:
				outcome = "insufficient_balance"
			case apperr.ErrPaymentExpired.Code:
				outcome = "expired"
			case apperr.ErrInvalidState.Code:
				outcome = "double_confirm_rejected"
			}
			metrics.Payments.WithLabelValues(outcome).Inc()
			return idempotency.Outcome{}, err
		}
		metrics.Payments.WithLabelValues("succeeded").Inc()
		return idempotency.Outcome{Status: http.StatusOK, Body: userView(pi), ResourceType: "payment_intent", ResourceID: &pi.ID}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

type refundRequest struct {
	Amount string  `json:"amount"`
	Reason *string `json:"reason"`
}

func (in refundRequest) validate() (int64, *string, error) {
	var fe []apperr.FieldError
	amount, err := ledger.ParseAmount(in.Amount)
	switch {
	case err != nil:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "INVALID_FORMAT"})
	case amount < 1:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "OUT_OF_RANGE", Message: "min 1"})
	}
	var reason *string
	if in.Reason != nil {
		rs := strings.TrimSpace(*in.Reason)
		if utf8.RuneCountInString(rs) > 200 {
			fe = append(fe, apperr.FieldError{Field: "reason", Code: "TOO_LONG"})
		}
		if rs != "" {
			reason = &rs
		}
	}
	if len(fe) > 0 {
		return 0, nil, apperr.Validation(fe...)
	}
	return amount, reason, nil
}

type refundResponse struct {
	refundView
	PaymentIntent struct {
		ID             uuid.UUID `json:"id"`
		Status         string    `json:"status"`
		RefundedAmount string    `json:"refunded_amount"`
	} `json:"payment_intent"`
}

// refund: POST /merchant/payment-intents/{id}/refunds (LF-16). 201 dan 422 domain disimpan untuk replay.
func (h *Handler) refund(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MerchantFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
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
	var in refundRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	amount, reason, err := in.validate()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.idem.Execute(r, idempotency.Request{
		ClientType: "MERCHANT", ClientID: m.MerchantID, Endpoint: "POST /v1/merchant/payment-intents/{id}/refunds", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, idemID uuid.UUID) (idempotency.Outcome, error) {
		rf, pi, err := h.svc.RefundIntent(ctx, tx, RefundCommand{MerchantID: m.MerchantID, IntentID: id, Amount: amount,
			Reason: reason, IdempotencyID: idemID, CorrelationID: httpx.RequestIDFrom(ctx)})
		if err != nil {
			outcome := "error"
			switch apperr.As(err).Code {
			case apperr.ErrRefundExceedsPayment.Code:
				outcome = "exceeds_payment"
			case apperr.ErrInsufficientBalance.Code:
				outcome = "insufficient_balance"
			case apperr.ErrInvalidState.Code:
				outcome = "invalid_state"
			}
			metrics.Refunds.WithLabelValues(outcome).Inc()
			return idempotency.Outcome{}, err
		}
		metrics.Refunds.WithLabelValues("succeeded").Inc()
		out := refundResponse{refundView: toRefundView(rf)}
		out.PaymentIntent.ID, out.PaymentIntent.Status, out.PaymentIntent.RefundedAmount = pi.ID, pi.Status, ledger.FormatAmount(pi.RefundedAmount)
		return idempotency.Outcome{Status: http.StatusCreated, Body: out, ResourceType: "refund", ResourceID: &rf.ID}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

// balance: GET /merchant/balance. Saldo dibaca dari primary (strong, ADR-008).
func (h *Handler) balance(w http.ResponseWriter, r *http.Request) {
	m, _ := auth.MerchantFrom(r.Context())
	wl, err := h.svc.ledger.MerchantWalletOf(r.Context(), h.pool, m.MerchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account_id": wl.AccountID, "balance": ledger.FormatAmount(wl.Balance),
		"currency": wl.Currency, "as_of": h.clock.Now().UTC()})
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return uuid.Nil, false
	}
	return id, true
}
