package recon

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/recon/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
)

type Handler struct {
	pool  *pgxpool.Pool
	svc   *Service
	clock clock.Clock
}

func NewHandler(pool *pgxpool.Pool, svc *Service, clk clock.Clock) *Handler {
	return &Handler{pool: pool, svc: svc, clock: clk}
}

// Routes: read/write sudah melewati RequireUser + RequireRole("OPS"). Resolusi item ada di modul ops
// (butuh payout + audit dalam satu tx).
func (h *Handler) Routes(read, write chi.Router) {
	read.Get("/ops/recon-runs", h.listRuns)
	read.Get("/ops/recon-items", h.listItems)
	write.Post("/ops/recon-runs", h.createRun)
}

type runView struct {
	ID                   uuid.UUID  `json:"id"`
	Provider             string     `json:"provider"`
	BusinessDate         string     `json:"business_date"`
	Source               string     `json:"source"`
	Status               string     `json:"status"`
	RowCount             int32      `json:"row_count"`
	MatchedCount         int32      `json:"matched_count"`
	MissingInternalCount int32      `json:"missing_internal_count"`
	MissingExternalCount int32      `json:"missing_external_count"`
	AmountMismatchCount  int32      `json:"amount_mismatch_count"`
	StatusMismatchCount  int32      `json:"status_mismatch_count"`
	Error                *string    `json:"error"`
	StartedAt            time.Time  `json:"started_at"`
	FinishedAt           *time.Time `json:"finished_at"`
}

func toRunView(r Run) runView {
	return runView{ID: r.ID, Provider: Provider, BusinessDate: r.BusinessDate.Format("2006-01-02"), Source: r.Source, Status: r.Status,
		RowCount: r.Rows, MatchedCount: r.Matched, MissingInternalCount: r.MissingInternal, MissingExternalCount: r.MissingExternal,
		AmountMismatchCount: r.AmountMismatch, StatusMismatchCount: r.StatusMismatch, Error: r.Error, StartedAt: r.StartedAt.UTC(), FinishedAt: r.FinishedAt}
}

type createRunRequest struct {
	BusinessDate string `json:"business_date"`
	Source       string `json:"source"`
}

// createRun: POST /ops/recon-runs. V1 SINKRON (file kecil): 201 run baru, 200 file yang sama sudah
// diproses (LI-12). Desain menyebut 202 + job; dicatat sebagai deviasi (upload multipart belum ada).
func (h *Handler) createRun(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var in createRunRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	day, err := time.ParseInLocation("2006-01-02", in.BusinessDate, WIB)
	today := h.clock.Now().In(WIB)
	switch {
	case err != nil:
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "business_date", Code: "INVALID_FORMAT", Message: "YYYY-MM-DD"}))
		return
	case day.After(time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, WIB)):
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "business_date", Code: "OUT_OF_RANGE", Message: "not in the future (Asia/Jakarta)"}))
		return
	case in.Source != "" && in.Source != "PULL":
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "source", Code: "INVALID_VALUE", Message: "PULL (UPLOAD not supported yet)"}))
		return
	}
	run, created, err := h.svc.Reconcile(r.Context(), day, &p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, toRunView(run))
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	var fe []apperr.FieldError
	p := store.ListRunsParams{}
	for field, dst := range map[string]**time.Time{"from": &p.FromDate, "to": &p.ToDate} {
		if v := qs.Get(field); v != "" {
			d, err := time.ParseInLocation("2006-01-02", v, WIB)
			if err != nil {
				fe = append(fe, apperr.FieldError{Field: field, Code: "INVALID_FORMAT"})
				continue
			}
			*dst = &d
		}
	}
	limit, cur, err := pageParams(qs, &fe)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(fe) > 0 {
		httpx.WriteError(w, r, apperr.Validation(fe...))
		return
	}
	p.Lim = limit + 1
	if cur != nil {
		p.CursorAt, p.CursorID = &cur.At, &cur.ID
	}
	rows, err := store.New(h.pool).ListRuns(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	out := make([]runView, 0, len(rows))
	for _, x := range rows {
		out = append(out, toRunView(Run{ID: x.ID, BusinessDate: x.BusinessDate, Source: x.Source, Status: x.Status, Rows: x.RowCount,
			Matched: x.MatchedCount, MissingInternal: x.MissingInternalCount, MissingExternal: x.MissingExternalCount,
			AmountMismatch: x.AmountMismatchCount, StatusMismatch: x.StatusMismatchCount, Error: x.Error, StartedAt: x.StartedAt, FinishedAt: x.FinishedAt}))
	}
	writePage(w, out, limit, func(v runView) httpx.Cursor { return httpx.Cursor{At: v.StartedAt, ID: v.ID} })
}

// ItemView adalah item recon untuk ops (juga dipakai respons resolve di modul ops lewat ItemJSON).
type ItemView struct {
	ID                  uuid.UUID  `json:"id"`
	ReconRunID          *uuid.UUID `json:"recon_run_id"`
	Source              string     `json:"source"`
	ObjectType          string     `json:"object_type"`
	InternalID          *uuid.UUID `json:"internal_id"`
	ExternalRef         *string    `json:"external_ref"`
	ProviderRef         *string    `json:"provider_ref"`
	Category            string     `json:"category"`
	InternalAmount      *string    `json:"internal_amount"`
	ExternalAmount      *string    `json:"external_amount"`
	InternalStatus      *string    `json:"internal_status"`
	ExternalStatus      *string    `json:"external_status"`
	Currency            string     `json:"currency"`
	Resolution          *string    `json:"resolution"`
	ResolutionAction    *string    `json:"resolution_action"`
	ResolutionNote      *string    `json:"resolution_note"`
	ResolvedBy          *uuid.UUID `json:"resolved_by"`
	ResolvedAt          *time.Time `json:"resolved_at"`
	SettlementJournalID *uuid.UUID `json:"settlement_journal_id"`
	CreatedAt           time.Time  `json:"created_at"`
}

func money(v *int64) *string {
	if v == nil {
		return nil
	}
	s := ledger.FormatAmount(*v)
	return &s
}

func toItemView(x store.GetItemRow) ItemView {
	return ItemView{ID: x.ID, ReconRunID: x.ReconRunID, Source: x.Source, ObjectType: x.ObjectType, InternalID: x.InternalID,
		ExternalRef: x.ExternalRef, ProviderRef: x.ProviderRef, Category: x.Category, InternalAmount: money(x.InternalAmount),
		ExternalAmount: money(x.ExternalAmount), InternalStatus: x.InternalStatus, ExternalStatus: x.ExternalStatus, Currency: x.Currency,
		Resolution: x.Resolution, ResolutionAction: x.ResolutionAction, ResolutionNote: x.ResolutionNote, ResolvedBy: x.ResolvedBy,
		ResolvedAt: x.ResolvedAt, SettlementJournalID: x.SettlementJournalID, CreatedAt: x.CreatedAt.UTC()}
}

// GetItemView mengembalikan view satu item (dipakai modul ops setelah resolusi, lewat adapter).
func GetItemView(ctx context.Context, q db.DBTX, id uuid.UUID) (ItemView, error) {
	x, err := store.New(q).GetItem(ctx, id)
	if err != nil {
		return ItemView{}, db.MapError(err)
	}
	return toItemView(x), nil
}

var categories = map[string]bool{Matched: true, MissingInternal: true, MissingExternal: true, AmountMismatch: true, StatusMismatch: true}

func (h *Handler) listItems(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	var fe []apperr.FieldError
	p := store.ListItemsParams{Categories: []string{}}
	if v := qs.Get("resolution"); v != "" {
		if v != "OPEN" && v != "RESOLVED" {
			fe = append(fe, apperr.FieldError{Field: "resolution", Code: "INVALID_VALUE"})
		}
		p.Resolution = &v
	}
	for _, c := range qs["category"] {
		for _, x := range strings.Split(c, ",") {
			if !categories[x] {
				fe = append(fe, apperr.FieldError{Field: "category", Code: "INVALID_VALUE"})
				continue
			}
			p.Categories = append(p.Categories, x)
		}
	}
	if v := qs.Get("recon_run_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fe = append(fe, apperr.FieldError{Field: "recon_run_id", Code: "INVALID_FORMAT"})
		}
		p.RunID = &id
	}
	if v := qs.Get("object_type"); v != "" {
		if v != Topup && v != Payout {
			fe = append(fe, apperr.FieldError{Field: "object_type", Code: "INVALID_VALUE"})
		}
		p.ObjectType = &v
	}
	limit, cur, err := pageParams(qs, &fe)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(fe) > 0 {
		httpx.WriteError(w, r, apperr.Validation(fe...))
		return
	}
	p.Lim = limit + 1
	if cur != nil {
		p.CursorAt, p.CursorID = &cur.At, &cur.ID
	}
	rows, err := store.New(h.pool).ListItems(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	out := make([]ItemView, 0, len(rows))
	for _, x := range rows {
		out = append(out, toItemView(store.GetItemRow(x)))
	}
	writePage(w, out, limit, func(v ItemView) httpx.Cursor { return httpx.Cursor{At: v.CreatedAt, ID: v.ID} })
}

func pageParams(qs url.Values, fe *[]apperr.FieldError) (int32, *httpx.Cursor, error) {
	limit := int32(50)
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			*fe = append(*fe, apperr.FieldError{Field: "limit", Code: "OUT_OF_RANGE", Message: "1–100"})
		} else {
			limit = int32(n) //nolint:gosec // G115: dibatasi di atas
		}
	}
	if v := qs.Get("cursor"); v != "" {
		c, err := httpx.DecodeCursor(v)
		if err != nil {
			return 0, nil, err
		}
		return limit, &c, nil
	}
	return limit, nil, nil
}

func writePage[T any](w http.ResponseWriter, items []T, limit int32, key func(T) httpx.Cursor) {
	pg := httpx.Page{}
	if int32(len(items)) > limit { //nolint:gosec // G115: len ≤ limit+1
		items = items[:limit]
		pg.HasMore = true
		c := httpx.EncodeCursor(key(items[len(items)-1]))
		pg.NextCursor = &c
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items, "page": pg})
}
