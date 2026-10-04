package payout

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
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
)

// V1: semua bank diarahkan ke mock-provider (kontrak API §8).
var supportedBanks = map[string]bool{"MOCKBANK": true, "BCA": true, "BNI": true, "BRI": true, "MANDIRI": true}

var accountPattern = regexp.MustCompile(`^[0-9]{6,20}$`)

type Handler struct {
	pool *pgxpool.Pool
	svc  *Service
	idem *idempotency.Service
}

func NewHandler(pool *pgxpool.Pool, svc *Service, idem *idempotency.Service) *Handler {
	return &Handler{pool: pool, svc: svc, idem: idem}
}

func (h *Handler) Routes(read, moneyWrite chi.Router) {
	moneyWrite.Post("/payouts", h.create)
	read.Get("/payouts/{id}", h.get)
}

type createRequest struct {
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	Destination struct {
		BankCode      string `json:"bank_code"`
		AccountNumber string `json:"account_number"`
		HolderName    string `json:"holder_name"`
	} `json:"destination"`
}

func (in createRequest) validate(userID uuid.UUID, requestID string) (Command, error) {
	var fe []apperr.FieldError
	amount, err := ledger.ParseAmount(in.Amount)
	switch {
	case err != nil:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "INVALID_FORMAT"})
	case amount < MinAmount || amount > MaxAmount:
		fe = append(fe, apperr.FieldError{Field: "amount", Code: "OUT_OF_RANGE", Message: "1000000 – 2500000000"})
	}
	if in.Currency == "" {
		fe = append(fe, apperr.FieldError{Field: "currency", Code: "REQUIRED"})
	}
	if !supportedBanks[in.Destination.BankCode] {
		fe = append(fe, apperr.FieldError{Field: "destination.bank_code", Code: "UNSUPPORTED"})
	}
	if !accountPattern.MatchString(in.Destination.AccountNumber) {
		fe = append(fe, apperr.FieldError{Field: "destination.account_number", Code: "INVALID_FORMAT"})
	}
	holder := strings.TrimSpace(in.Destination.HolderName)
	if n := utf8.RuneCountInString(holder); n < 2 || n > 100 {
		fe = append(fe, apperr.FieldError{Field: "destination.holder_name", Code: "INVALID_LENGTH"})
	}
	if len(fe) > 0 {
		return Command{}, apperr.Validation(fe...)
	}
	if in.Currency != ledger.SupportedCurrency {
		return Command{}, apperr.ErrCurrencyNotSupported
	}
	return Command{UserID: userID, Amount: amount, Currency: in.Currency, BankCode: in.Destination.BankCode,
		AccountNumber: in.Destination.AccountNumber, HolderName: holder, CorrelationID: requestID}, nil
}

type destination struct {
	BankCode      string `json:"bank_code"`
	AccountMasked string `json:"account_number_masked"` // nomor lengkap tidak pernah dikirim balik
	HolderName    string `json:"holder_name"`
}

type payoutResponse struct {
	ID                  uuid.UUID   `json:"id"`
	Amount              string      `json:"amount"`
	Currency            string      `json:"currency"`
	Status              string      `json:"status"`
	DisplayStatus       string      `json:"display_status"` // PENDING/SUBMITTED/UNKNOWN → PROCESSING untuk user
	Destination         destination `json:"destination"`
	HoldJournalID       uuid.UUID   `json:"hold_journal_id"`
	NeedsReconciliation bool        `json:"needs_reconciliation,omitempty"`
	CreatedAt           time.Time   `json:"created_at"`
}

func view(p Payout) payoutResponse {
	display := "PROCESSING" // user tidak perlu tahu beda SUBMITTED vs UNKNOWN: dananya sedang diproses
	if p.Status == "SUCCEEDED" || p.Status == "FAILED" {
		display = p.Status
	}
	return payoutResponse{ID: p.ID, Amount: ledger.FormatAmount(p.Amount), Currency: p.Currency, Status: p.Status, DisplayStatus: display,
		Destination:   destination{BankCode: p.BankCode, AccountMasked: "******" + p.Last4, HolderName: p.HolderName},
		HoldJournalID: p.HoldJournalID, NeedsReconciliation: p.NeedsReconciliation, CreatedAt: p.CreatedAt.UTC()}
}

// create: 202 karena hasil akhir ditentukan worker. Disimpan untuk replay: 202 dan 422 domain.
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
		ClientType: idempotency.ClientUser, ClientID: p.UserID, Endpoint: "POST /v1/payouts", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (idempotency.Outcome, error) {
		po, err := h.svc.Create(ctx, tx, cmd)
		if err != nil {
			return idempotency.Outcome{}, err
		}
		return idempotency.Outcome{Status: http.StatusAccepted, Body: view(po), ResourceType: "payout", ResourceID: &po.ID}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	po, err := h.svc.ForUser(r.Context(), h.pool, p.UserID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view(po))
}
