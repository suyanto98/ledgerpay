package transfer

import (
	"context"
	"net/http"
	"net/mail"
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
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/metrics"
)

// endpoint adalah scope key idempotency: template rute, bukan path konkret.
const endpoint = "POST /v1/transfers"

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
	moneyWrite.Post("/transfers", h.create)
	read.Get("/transfers/{id}", h.get)
}

type createRequest struct {
	RecipientEmail string  `json:"recipient_email"`
	Amount         string  `json:"amount"` // string minor unit (ADR-L04)
	Currency       string  `json:"currency"`
	Note           *string `json:"note"`
}

// validate: kesalahan format → 400 (tidak disimpan di idempotency, karena request tidak pernah
// diterima secara bisnis). Currency selain IDR → 422 CURRENCY_NOT_SUPPORTED.
func (in createRequest) validate(sender uuid.UUID, requestID string) (Command, error) {
	var fe []apperr.FieldError
	if a, err := mail.ParseAddress(in.RecipientEmail); err != nil || a.Address != in.RecipientEmail || len(in.RecipientEmail) > 254 {
		fe = append(fe, apperr.FieldError{Field: "recipient_email", Code: "INVALID_FORMAT"})
	}
	amount, err := ledger.ParseAmount(in.Amount)
	switch {
	case err != nil:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "INVALID_FORMAT", Message: "positive integer string in minor units"})
	case amount < MinAmount || amount > MaxAmount:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "OUT_OF_RANGE", Message: "100 – 1000000000"})
	}
	if in.Currency == "" {
		fe = append(fe, apperr.FieldError{Field: "currency", Code: "REQUIRED"})
	}
	var note *string
	if in.Note != nil {
		n := strings.TrimSpace(*in.Note)
		if utf8.RuneCountInString(n) > 140 {
			fe = append(fe, apperr.FieldError{Field: "note", Code: "TOO_LONG", Message: "max 140 characters"})
		}
		if n != "" {
			note = &n
		}
	}
	if len(fe) > 0 {
		return Command{}, apperr.Validation(fe...)
	}
	if in.Currency != ledger.SupportedCurrency {
		return Command{}, apperr.ErrCurrencyNotSupported
	}
	return Command{SenderID: sender, RecipientEmail: in.RecipientEmail, Amount: amount, Currency: in.Currency,
		Note: note, CorrelationID: requestID}, nil
}

type party struct {
	DisplayName string `json:"display_name"`
}

type transferResponse struct {
	ID        uuid.UUID `json:"id"`
	Amount    string    `json:"amount"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	Direction string    `json:"direction"` // OUT bagi pengirim, IN bagi penerima
	Receiver  *party    `json:"receiver,omitempty"`
	Sender    *party    `json:"sender,omitempty"`
	Note      *string   `json:"note"`
	JournalID uuid.UUID `json:"journal_id"`
	CreatedAt time.Time `json:"created_at"`
}

// view: pengirim melihat "receiver", penerima melihat "sender". ID user pihak lain tidak dikirim.
func view(t Transfer, viewer uuid.UUID) transferResponse {
	r := transferResponse{ID: t.ID, Amount: ledger.FormatAmount(t.Amount), Currency: t.Currency, Status: t.Status,
		Note: t.Note, JournalID: t.JournalID, CreatedAt: t.CreatedAt.UTC()}
	if viewer == t.SenderUserID {
		r.Direction, r.Receiver = "OUT", &party{DisplayName: t.CounterpartName}
	} else {
		r.Direction, r.Sender = "IN", &party{DisplayName: t.CounterpartName}
	}
	return r
}

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
	cmd, err := in.validate(p.UserID, httpx.RequestIDFrom(r.Context()))
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
		ClientType: idempotency.ClientUser, ClientID: p.UserID, Endpoint: endpoint, Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, keyID uuid.UUID) (idempotency.Outcome, error) {
		cmd.IdempotencyKeyID = &keyID
		t, err := h.svc.Create(ctx, tx, cmd)
		if err != nil {
			metrics.Transfers.WithLabelValues(transferOutcome(err)).Inc()
			return idempotency.Outcome{}, err
		}
		metrics.Transfers.WithLabelValues("completed").Inc()
		return idempotency.Outcome{Status: http.StatusCreated, Body: view(t, p.UserID), ResourceType: "transfer", ResourceID: &t.ID}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

// transferOutcome: label terbatas dari error domain (bukan teks error).
func transferOutcome(err error) string {
	switch apperr.As(err).Code {
	case apperr.ErrInsufficientBalance.Code:
		return "insufficient_balance"
	case apperr.ErrInvalidRecipient.Code:
		return "invalid_recipient"
	case apperr.ErrSelfTransfer.Code:
		return "self_transfer"
	case apperr.ErrAccountFrozen.Code:
		return "frozen"
	}
	return "error"
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound) // ID rusak = tidak ada (sama dengan BOLA)
		return
	}
	t, err := h.svc.Get(r.Context(), h.pool, p.UserID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view(t, p.UserID))
}
