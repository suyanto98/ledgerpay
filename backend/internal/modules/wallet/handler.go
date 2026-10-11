// Package wallet: endpoint baca wallet (LF-02). Tidak menyentuh tabel ledger secara langsung;
// semua data diambil lewat ledger.Service (modul pemilik tabel, ADR-001).
package wallet

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
)

// jakarta: rentang tanggal ?from/?to dibaca di zona Asia/Jakarta (kontrak API), disimpan UTC.
var jakarta = time.FixedZone("WIB", 7*3600)

type Handler struct {
	pool   *pgxpool.Pool
	ledger *ledger.Service
	clock  clock.Clock
	labels Labeler
}

// SourceRef menunjuk objek bisnis sumber satu transaksi (contoh transfer/<id>).
type SourceRef struct {
	Type string
	ID   uuid.UUID
}

// Label adalah keterangan transaksi yang bisa dibaca manusia: "Andi Wijaya · Bayar bensin".
type Label struct {
	Counterparty *string
	Note         *string
}

// Labeler memberi label dari modul pemilik objek sumber (transfer, payment), keyed by SourceRef.ID.
// Disuntikkan composition root: wallet tidak meng-import modul bisnis lain (ADR-001). Implementasi
// wajib menerapkan BOLA: objek yang bukan milik viewer tidak diberi label.
type Labeler interface {
	Labels(ctx context.Context, q db.DBTX, viewer uuid.UUID, refs []SourceRef) (map[uuid.UUID]Label, error)
}

func NewHandler(pool *pgxpool.Pool, l *ledger.Service, c clock.Clock, labels Labeler) *Handler {
	return &Handler{pool: pool, ledger: l, clock: c, labels: labels}
}

// labelsFor mengambil label untuk satu halaman riwayat (satu panggilan, bukan satu per baris).
func (h *Handler) labelsFor(ctx context.Context, viewer uuid.UUID, items []ledger.Movement) (map[uuid.UUID]Label, error) {
	if h.labels == nil || len(items) == 0 {
		return map[uuid.UUID]Label{}, nil
	}
	refs := make([]SourceRef, 0, len(items))
	for _, m := range items {
		refs = append(refs, SourceRef{Type: m.SourceType, ID: m.SourceID})
	}
	return h.labels.Labels(ctx, h.pool, viewer, refs)
}

// Routes: semua rute wallet butuh user. Wallet ditentukan dari token (user_id), tidak ada ID
// wallet di path → tidak ada permukaan BOLA untuk GET /wallet.
func (h *Handler) Routes(authed chi.Router) {
	authed.Get("/wallet", h.getWallet)
	authed.Get("/wallet/transactions", h.listTransactions)
	authed.Get("/wallet/transactions/{journalID}", h.getTransaction)
}

type walletResponse struct {
	AccountID uuid.UUID `json:"account_id"`
	Balance   string    `json:"balance"` // uang = string minor unit (ADR-L04)
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	AsOf      time.Time `json:"as_of"`
}

func (h *Handler) getWallet(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	wl, err := h.ledger.WalletOf(r.Context(), h.pool, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, walletResponse{AccountID: wl.AccountID, Balance: ledger.FormatAmount(wl.Balance),
		Currency: wl.Currency, Status: wl.Status, AsOf: h.clock.Now()})
}

type transaction struct {
	ID        uuid.UUID `json:"id"` // = journal id (satu transaksi = satu journal)
	Type      string    `json:"type"`
	Direction string    `json:"direction"` // IN / OUT dari sudut pandang wallet ini
	Amount    string    `json:"amount"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	JournalID uuid.UUID `json:"journal_id"`
	CreatedAt time.Time `json:"created_at"`
	// Pihak lawan (nama user / merchant) dan catatan; null kalau tidak ada (top-up, tarik dana).
	Counterparty *string `json:"counterparty"`
	Note         *string `json:"note"`
}

// Wallet = liability: entry KREDIT di wallet berarti uang MASUK, DEBIT berarti KELUAR.
func directionLabel(d ledger.Direction) string {
	if d == ledger.Credit {
		return "IN"
	}
	return "OUT"
}

func (h *Handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	f, err := parseFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	wl, err := h.ledger.WalletOf(r.Context(), h.pool, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.ledger.History(r.Context(), h.pool, wl.AccountID, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	labels, err := h.labelsFor(r.Context(), p.UserID, items)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]transaction, 0, len(items))
	for _, m := range items {
		lb := labels[m.SourceID]
		out = append(out, transaction{ID: m.JournalID, Type: m.Purpose.TransactionType(), Direction: directionLabel(m.Direction),
			Amount: ledger.FormatAmount(m.Amount), Currency: m.Currency, Status: "POSTED", JournalID: m.JournalID, CreatedAt: m.CreatedAt,
			Counterparty: lb.Counterparty, Note: lb.Note})
	}
	pg := httpx.Page{HasMore: next != nil}
	if next != nil {
		c := httpx.EncodeCursor(httpx.Cursor(*next))
		pg.NextCursor = &c
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out, "page": pg})
}

type entryResponse struct {
	Side         string `json:"side"`
	Amount       string `json:"amount"`
	AccountLabel string `json:"account_label"`
}

func (h *Handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	jid, err := uuid.Parse(chi.URLParam(r, "journalID"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound) // ID tidak valid diperlakukan sama dengan tidak ada
		return
	}
	wl, err := h.ledger.WalletOf(r.Context(), h.pool, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.ledger.JournalFor(r.Context(), h.pool, wl.AccountID, jid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	labels, err := h.labelsFor(r.Context(), p.UserID, []ledger.Movement{{SourceType: v.SourceType, SourceID: v.SourceID}})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	lb := labels[v.SourceID]
	entries := make([]entryResponse, 0, len(v.Entries))
	for _, e := range v.Entries {
		entries = append(entries, entryResponse{Side: string(e.Direction), Amount: ledger.FormatAmount(e.Amount), AccountLabel: e.Label})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": v.ID, "type": v.Purpose.TransactionType(), "direction": directionLabel(v.Mine.Direction),
		"amount": ledger.FormatAmount(v.Mine.Amount), "currency": v.Currency, "status": "POSTED",
		"journal_id": v.ID, "created_at": v.CreatedAt, "counterparty": lb.Counterparty, "note": lb.Note,
		"journal": map[string]any{"id": v.ID, "purpose": v.Purpose, "created_at": v.CreatedAt, "entries": entries},
	})
}

var allowedQuery = map[string]bool{"limit": true, "cursor": true, "type": true, "direction": true, "from": true, "to": true}

// parseFilter: filter tidak dikenal → 400 (kontrak API), batas rentang 366 hari.
func parseFilter(r *http.Request) (ledger.HistoryFilter, error) {
	q := r.URL.Query()
	var fe []apperr.FieldError
	for k := range q {
		if !allowedQuery[k] {
			fe = append(fe, apperr.FieldError{Field: k, Code: "UNKNOWN_PARAMETER"})
		}
	}
	f := ledger.HistoryFilter{Limit: 20}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			fe = append(fe, apperr.FieldError{Field: "limit", Code: "OUT_OF_RANGE", Message: "1–100"})
		} else {
			f.Limit = int32(n) //nolint:gosec // 1..100
		}
	}
	if s := q.Get("direction"); s != "" {
		switch s {
		case "IN":
			d := ledger.Credit
			f.Direction = &d
		case "OUT":
			d := ledger.Debit
			f.Direction = &d
		default:
			fe = append(fe, apperr.FieldError{Field: "direction", Code: "INVALID", Message: "IN or OUT"})
		}
	}
	for _, t := range q["type"] {
		ps := ledger.PurposesForType(t)
		if len(ps) == 0 {
			fe = append(fe, apperr.FieldError{Field: "type", Code: "INVALID"})
		}
		f.Purposes = append(f.Purposes, ps...)
	}
	from, okFrom := parseDate(q.Get("from"), &fe, "from")
	to, okTo := parseDate(q.Get("to"), &fe, "to")
	if okFrom {
		f.From = &from
	}
	if okTo {
		end := to.AddDate(0, 0, 1) // to inklusif → batas atas eksklusif hari berikutnya
		f.To = &end
	}
	if okFrom && okTo && (to.Before(from) || to.Sub(from) > 366*24*time.Hour) {
		fe = append(fe, apperr.FieldError{Field: "to", Code: "INVALID_RANGE", Message: "from ≤ to, at most 366 days"})
	}
	if len(fe) > 0 {
		return f, apperr.Validation(fe...)
	}
	if s := q.Get("cursor"); s != "" {
		c, err := httpx.DecodeCursor(s)
		if err != nil {
			return f, err
		}
		lc := ledger.Cursor(c)
		f.Cursor = &lc
	}
	return f, nil
}

func parseDate(s string, fe *[]apperr.FieldError, field string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", s, jakarta)
	if err != nil {
		*fe = append(*fe, apperr.FieldError{Field: field, Code: "INVALID_FORMAT", Message: "YYYY-MM-DD"})
		return time.Time{}, false
	}
	return t, true
}
