package ops

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/ops/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idempotency"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/jobs"
)

const (
	defaultLimit = 50
	maxLimit     = 100
	searchLimit  = 50 // pencarian presisi, bukan daftar (§11)
)

type Handler struct {
	pool     *pgxpool.Pool
	idem     *idempotency.Service
	ids      idgen.Generator
	payouts  Payouts
	webhooks Webhooks
	recon    Recon
}

func NewHandler(pool *pgxpool.Pool, idem *idempotency.Service, ids idgen.Generator, p Payouts, w Webhooks, rc Recon) *Handler {
	return &Handler{pool: pool, idem: idem, ids: ids, payouts: p, webhooks: w, recon: rc}
}

// Routes: read dan write sudah melewati RequireUser + RequireRole("OPS") + rate limit masing-masing.
func (h *Handler) Routes(read, write chi.Router) {
	read.Get("/ops/transactions/search", h.search)
	read.Get("/ops/journals/{id}", h.journal)
	read.Get("/ops/payouts", h.listPayouts)
	read.Get("/ops/payouts/{id}", h.getPayout)
	read.Get("/ops/jobs", h.listJobs)
	read.Get("/ops/webhook-deliveries", h.listDeliveries)
	write.Post("/ops/payouts/{id}/resolve", h.resolvePayout)
	write.Post("/ops/jobs/{id}/retry", h.retryJob)
	write.Post("/ops/webhook-deliveries/{id}/replay", h.replayDelivery)
	write.Post("/ops/recon-items/{id}/resolve", h.resolveReconItem)
}

func actorFrom(r *http.Request) Actor {
	p, _ := auth.PrincipalFrom(r.Context())
	return Actor{UserID: p.UserID, RequestID: httpx.RequestIDFrom(r.Context()), IP: httpx.ClientIPFrom(r), UserAgent: r.UserAgent()}
}

// ---------------------------------------------------------------- search & journal

type userRef struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
}

type searchHit struct {
	ObjectType    string      `json:"object_type"`
	ID            uuid.UUID   `json:"id"`
	Status        string      `json:"status"`
	Amount        string      `json:"amount"`
	Currency      string      `json:"currency"`
	User          *userRef    `json:"user"`
	JournalIDs    []uuid.UUID `json:"journal_ids"`
	CorrelationID *string     `json:"correlation_id"`
	CreatedAt     time.Time   `json:"created_at"`
}

// search: q = UUID objek/journal, email user, atau provider_ref / merchant_order_id.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > 200 {
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "q", Code: "REQUIRED", Message: "1–200 characters"}))
		return
	}
	p := store.SearchObjectsParams{Lim: searchLimit}
	switch id, err := uuid.Parse(q); {
	case err == nil:
		p.ID = &id
	case strings.Contains(q, "@"):
		p.Email = &q
	default:
		p.Ref = &q
	}
	rows, err := store.New(h.pool).SearchObjects(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	out := make([]searchHit, 0, len(rows))
	for _, row := range rows {
		hit := searchHit{ObjectType: row.ObjectType, ID: row.ID, Status: row.Status, Amount: ledger.FormatAmount(row.Amount),
			Currency: row.Currency, JournalIDs: row.JournalIds, CorrelationID: row.CorrelationID, CreatedAt: row.CreatedAt.UTC()}
		if row.UserID != nil && row.UserEmail != nil {
			hit.User = &userRef{ID: *row.UserID, Email: *row.UserEmail}
		}
		out = append(out, hit)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

type entryView struct {
	AccountID uuid.UUID `json:"account_id"`
	Owner     string    `json:"owner"`
	Direction string    `json:"direction"`
	Amount    string    `json:"amount"`
	Currency  string    `json:"currency"`
}

func (h *Handler) journal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := store.New(h.pool)
	j, err := q.GetJournal(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	rows, err := q.JournalEntries(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	entries := make([]entryView, 0, len(rows))
	for _, e := range rows {
		entries = append(entries, entryView{AccountID: e.AccountID, Owner: e.Owner, Direction: e.Direction,
			Amount: ledger.FormatAmount(e.Amount), Currency: e.Currency})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": j.ID, "purpose": j.Purpose, "source_type": j.SourceType, "source_id": j.SourceID,
		"reversal_of": j.ReversalOf, "reversed_by": j.ReversedBy, "actor_type": j.ActorType, "actor_id": j.ActorID,
		"correlation_id": j.CorrelationID, "description": j.Description, "currency": j.Currency,
		"created_at": j.CreatedAt.UTC(), "entries": entries,
	})
}

// ---------------------------------------------------------------- payouts

type payoutView struct {
	ID                  uuid.UUID  `json:"id"`
	User                userRef    `json:"user"`
	Amount              string     `json:"amount"`
	Currency            string     `json:"currency"`
	Status              string     `json:"status"`
	Destination         any        `json:"destination"`
	HoldJournalID       uuid.UUID  `json:"hold_journal_id"`
	SettleJournalID     *uuid.UUID `json:"settle_journal_id"`
	ReversalJournalID   *uuid.UUID `json:"reversal_journal_id"`
	NeedsReconciliation bool       `json:"needs_reconciliation"`
	StatusCheckAttempts int32      `json:"status_check_attempts"`
	LastProviderStatus  *string    `json:"last_provider_status"`
	LastError           *string    `json:"last_error"`
	ResolutionSource    *string    `json:"resolution_source"`
	ResolvedBy          *uuid.UUID `json:"resolved_by"`
	SubmittedAt         *time.Time `json:"submitted_at"`
	ResolvedAt          *time.Time `json:"resolved_at"`
	CreatedAt           time.Time  `json:"created_at"`
}

func toPayoutView(o store.GetPayoutRow) payoutView {
	return payoutView{ID: o.ID, User: userRef{ID: o.UserID, Email: o.UserEmail}, Amount: ledger.FormatAmount(o.Amount), Currency: o.Currency,
		Status: o.Status, Destination: map[string]string{"bank_code": o.BankCode, "account_number_masked": "******" + o.AccountNumberLast4, "holder_name": o.HolderName},
		HoldJournalID: o.HoldJournalID, SettleJournalID: o.SettleJournalID, ReversalJournalID: o.ReversalJournalID,
		NeedsReconciliation: o.NeedsReconciliation, StatusCheckAttempts: o.StatusCheckAttempts, LastProviderStatus: o.LastProviderStatus,
		LastError: o.LastError, ResolutionSource: o.ResolutionSource, ResolvedBy: o.ResolvedBy, SubmittedAt: o.SubmittedAt,
		ResolvedAt: o.ResolvedAt, CreatedAt: o.CreatedAt.UTC()}
}

var payoutStatuses = map[string]bool{"PENDING": true, "SUBMITTED": true, "SUCCEEDED": true, "FAILED": true, "UNKNOWN": true}

// listPayouts: antrean ops (?status=UNKNOWN&needs_reconciliation=true&older_than_minutes=60). Paling lama di atas.
func (h *Handler) listPayouts(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	p := store.ListPayoutsParams{Statuses: []string{}}
	var fe []apperr.FieldError
	for _, s := range qs["status"] {
		for _, st := range strings.Split(s, ",") {
			if !payoutStatuses[st] {
				fe = append(fe, apperr.FieldError{Field: "status", Code: "INVALID_VALUE"})
				continue
			}
			p.Statuses = append(p.Statuses, st)
		}
	}
	if v := qs.Get("needs_reconciliation"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			fe = append(fe, apperr.FieldError{Field: "needs_reconciliation", Code: "INVALID_VALUE"})
		}
		p.NeedsReconciliation = &b
	}
	if v := qs.Get("older_than_minutes"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 60*24*90 {
			fe = append(fe, apperr.FieldError{Field: "older_than_minutes", Code: "OUT_OF_RANGE"})
		}
		p.OlderThanMinutes = int32(n) //nolint:gosec // G115: dibatasi di atas
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
	rows, err := store.New(h.pool).ListPayouts(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	out := make([]payoutView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPayoutView(store.GetPayoutRow(row)))
	}
	writePage(w, out, limit, func(v payoutView) httpx.Cursor { return httpx.Cursor{At: v.CreatedAt, ID: v.ID} })
}

func (h *Handler) getPayout(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := store.New(h.pool).GetPayout(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPayoutView(o))
}

type resolveRequest struct {
	Outcome     string `json:"outcome"`
	EvidenceRef string `json:"evidence_ref"`
	Reason      string `json:"reason"`
}

// resolvePayout: POST /ops/payouts/{id}/resolve (ADR-L06, LF-18). Satu tx: resolusi (guard status →
// journal SETTLE/REVERSAL) + audit berisi bukti. Kalah balapan dengan status check → 409.
func (h *Handler) resolvePayout(w http.ResponseWriter, r *http.Request) {
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
	var in resolveRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.Outcome != "SUCCEEDED" && in.Outcome != "FAILED" {
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "outcome", Code: "INVALID_VALUE", Message: "SUCCEEDED | FAILED"}))
		return
	}
	evidence, reason := strings.TrimSpace(in.EvidenceRef), strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(evidence) > 200 {
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "evidence_ref", Code: "TOO_LONG"}))
		return
	}
	if utf8.RuneCountInString(evidence) < 3 || utf8.RuneCountInString(reason) < MinReasonLen {
		// Bukan sekadar validasi format: resolusi manual tanpa bukti adalah celah insider (T-LP-16).
		httpx.WriteError(w, r, apperr.ErrResolutionEvidenceRequired.WithDetail("evidence_ref (3–200) and reason (≥ 10 characters) are required"))
		return
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a := actorFrom(r)
	resp, err := h.idem.Execute(r, idempotency.Request{
		ClientType: idempotency.ClientUser, ClientID: a.UserID, Endpoint: "POST /v1/ops/payouts/{id}/resolve", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (idempotency.Outcome, error) {
		if err := h.payouts.ResolveByOps(ctx, tx, id, in.Outcome == "SUCCEEDED", a.UserID, a.RequestID); err != nil {
			return idempotency.Outcome{}, err
		}
		if err := writeAudit(ctx, tx, h.ids, a, auditEntry{Action: "payout.manual_resolve", TargetType: "payout", TargetID: id, Reason: reason,
			Diff: map[string]any{"outcome": in.Outcome, "evidence_ref": evidence}}); err != nil {
			return idempotency.Outcome{}, err
		}
		o, err := store.New(tx).GetPayout(ctx, id)
		if err != nil {
			return idempotency.Outcome{}, db.MapError(err)
		}
		return idempotency.Outcome{Status: http.StatusOK, Body: toPayoutView(o), ResourceType: "payout", ResourceID: &id}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

// ---------------------------------------------------------------- jobs

type jobView struct {
	ID            uuid.UUID `json:"id"`
	Kind          string    `json:"kind,omitempty"`
	Status        string    `json:"status"`
	Attempts      int32     `json:"attempts"`
	MaxAttempts   int32     `json:"max_attempts,omitempty"`
	LastError     *string   `json:"last_error,omitempty"`
	RunAt         time.Time `json:"run_at"`
	CorrelationID *string   `json:"correlation_id,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitzero"`
}

var jobStatuses = map[string]bool{"queued": true, "running": true, "succeeded": true, "dead": true}

func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	var fe []apperr.FieldError
	status := qs.Get("status")
	if status == "" {
		status = "dead"
	}
	if !jobStatuses[status] {
		fe = append(fe, apperr.FieldError{Field: "status", Code: "INVALID_VALUE"})
	}
	var kind *string
	if k := qs.Get("kind"); k != "" {
		kind = &k
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
	var at *time.Time
	var after *uuid.UUID
	if cur != nil {
		at, after = &cur.At, &cur.ID
	}
	list, err := jobs.List(r.Context(), h.pool, status, kind, at, after, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]jobView, 0, len(list))
	for _, j := range list {
		out = append(out, jobView{ID: j.ID, Kind: j.Kind, Status: j.Status, Attempts: j.Attempts, MaxAttempts: j.MaxAttempts,
			LastError: j.LastError, RunAt: j.RunAt.UTC(), CorrelationID: j.CorrelationID, CreatedAt: j.CreatedAt.UTC()})
	}
	writePage(w, out, limit, func(v jobView) httpx.Cursor { return httpx.Cursor{At: v.CreatedAt, ID: v.ID} })
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func decodeReason(r *http.Request) (string, error) {
	var in reasonRequest
	if err := httpx.Decode(r, &in); err != nil {
		return "", err
	}
	reason := strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(reason) < MinReasonLen {
		return "", apperr.Validation(apperr.FieldError{Field: "reason", Code: "TOO_SHORT", Message: "min 10 characters"})
	}
	return reason, nil
}

// retryJob: dead → queued (natural idempotent lewat guard status) + audit, satu tx.
func (h *Handler) retryJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	reason, err := decodeReason(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a := actorFrom(r)
	var out jobs.Summary
	err = db.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = jobs.RetryDead(r.Context(), tx, id); err != nil {
			return err
		}
		return writeAudit(r.Context(), tx, h.ids, a, auditEntry{Action: "job.retry", TargetType: "job", TargetID: id, Reason: reason, Diff: map[string]any{}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, jobView{ID: out.ID, Status: out.Status, Attempts: out.Attempts, RunAt: out.RunAt.UTC()})
}

// ---------------------------------------------------------------- webhook deliveries

var deliveryStatuses = map[string]bool{"PENDING": true, "DELIVERED": true, "DEAD": true}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	var fe []apperr.FieldError
	status := qs.Get("status")
	if status == "" {
		status = "DEAD"
	}
	if !deliveryStatuses[status] {
		fe = append(fe, apperr.FieldError{Field: "status", Code: "INVALID_VALUE"})
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
	p := store.ListDeliveriesParams{Status: status, Lim: limit + 1}
	if cur != nil {
		p.CursorAt, p.CursorID = &cur.At, &cur.ID
	}
	rows, err := store.New(h.pool).ListDeliveries(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	type deliveryView struct {
		ID             uuid.UUID  `json:"id"`
		Merchant       any        `json:"merchant"`
		EventID        uuid.UUID  `json:"event_id"`
		EventType      string     `json:"event_type"`
		Status         string     `json:"status"`
		Attempts       int32      `json:"attempts"`
		LastStatusCode *int32     `json:"last_status_code"`
		LastError      *string    `json:"last_error"`
		LastAttemptAt  *time.Time `json:"last_attempt_at"`
		CreatedAt      time.Time  `json:"created_at"`
	}
	out := make([]deliveryView, 0, len(rows))
	for _, d := range rows {
		out = append(out, deliveryView{ID: d.ID, Merchant: map[string]any{"id": d.MerchantID, "name": d.MerchantName}, EventID: d.EventID,
			EventType: d.EventType, Status: d.Status, Attempts: d.Attempts, LastStatusCode: d.LastStatusCode, LastError: d.LastError,
			LastAttemptAt: d.LastAttemptAt, CreatedAt: d.CreatedAt.UTC()})
	}
	writePage(w, out, limit, func(v deliveryView) httpx.Cursor { return httpx.Cursor{At: v.CreatedAt, ID: v.ID} })
}

// replayDelivery: DEAD → PENDING + job baru (event_id tetap) + audit, satu tx. 202: pengiriman async.
func (h *Handler) replayDelivery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	reason, err := decodeReason(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a := actorFrom(r)
	var eventID uuid.UUID
	err = db.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		var err error
		if eventID, err = h.webhooks.Replay(r.Context(), tx, id); err != nil {
			return err
		}
		return writeAudit(r.Context(), tx, h.ids, a, auditEntry{Action: "webhook.replay", TargetType: "webhook_delivery", TargetID: id,
			Reason: reason, Diff: map[string]any{"event_id": eventID}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"id": id, "event_id": eventID, "status": "PENDING"})
}

// ---------------------------------------------------------------- recon items (LF-17, Demo D)

type reconResolveRequest struct {
	Action      string `json:"action"`
	EvidenceRef string `json:"evidence_ref"`
	Reason      string `json:"reason"`
}

var reconActions = map[string]bool{"APPLY_PAYOUT_SETTLE": true, "APPLY_PAYOUT_REVERSAL": true, "NO_ACTION_TIMING": true, "NO_ACTION_PROVIDER_ERROR": true}

// reconActionAllowed: aksi harus cocok dengan fakta item. APPLY_* menggerakkan uang, jadi hanya untuk
// payout yang bukti provider-nya menunjuk arah yang sama (SETTLE ⇐ provider SUCCEEDED; REVERSAL ⇐
// provider FAILED, atau payout yang tidak pernah sampai ke bank: MISSING_EXTERNAL).
func reconActionAllowed(action string, it ReconItem) bool {
	ext := ""
	if it.ExternalStatus != nil {
		ext = *it.ExternalStatus
	}
	switch action {
	case "APPLY_PAYOUT_SETTLE":
		return it.ObjectType == "payout" && it.Category == "STATUS_MISMATCH" && ext == "SUCCEEDED"
	case "APPLY_PAYOUT_REVERSAL":
		return it.ObjectType == "payout" && ((it.Category == "STATUS_MISMATCH" && ext == "FAILED") || it.Category == "MISSING_EXTERNAL")
	default: // NO_ACTION_*: mencatat keputusan ops tanpa efek uang
		return true
	}
}

// resolveReconItem: POST /ops/recon-items/{id}/resolve. Satu tx: lock item (OPEN) → aksi (payout lewat
// jalur ResolveByOps yang sama dengan resolve manual) → item RESOLVED → audit dengan bukti.
func (h *Handler) resolveReconItem(w http.ResponseWriter, r *http.Request) {
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
	var in reconResolveRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !reconActions[in.Action] {
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "action", Code: "INVALID_VALUE",
			Message: "APPLY_PAYOUT_SETTLE | APPLY_PAYOUT_REVERSAL | NO_ACTION_TIMING | NO_ACTION_PROVIDER_ERROR"}))
		return
	}
	evidence, reason := strings.TrimSpace(in.EvidenceRef), strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(evidence) > 200 {
		httpx.WriteError(w, r, apperr.Validation(apperr.FieldError{Field: "evidence_ref", Code: "TOO_LONG"}))
		return
	}
	if utf8.RuneCountInString(evidence) < 3 || utf8.RuneCountInString(reason) < MinReasonLen {
		httpx.WriteError(w, r, apperr.ErrResolutionEvidenceRequired.WithDetail("evidence_ref (3–200) and reason (≥ 10 characters) are required"))
		return
	}
	hash, err := idempotency.Hash(r.Method, r.URL.Path, raw)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a := actorFrom(r)
	resp, err := h.idem.Execute(r, idempotency.Request{
		ClientType: idempotency.ClientUser, ClientID: a.UserID, Endpoint: "POST /v1/ops/recon-items/{id}/resolve", Key: key, Hash: hash,
	}, func(ctx context.Context, tx pgx.Tx, _ uuid.UUID) (idempotency.Outcome, error) {
		it, err := h.recon.LockOpenItem(ctx, tx, id)
		if err != nil {
			return idempotency.Outcome{}, err
		}
		if !reconActionAllowed(in.Action, it) {
			return idempotency.Outcome{}, apperr.Validation(apperr.FieldError{Field: "action", Code: "NOT_ALLOWED_FOR_ITEM",
				Message: "action does not match the item's category, object type and provider status"})
		}
		diff := map[string]any{"action": in.Action, "evidence_ref": evidence, "category": it.Category}
		if strings.HasPrefix(in.Action, "APPLY_PAYOUT_") {
			succeeded := in.Action == "APPLY_PAYOUT_SETTLE"
			if err := h.payouts.ResolveByOps(ctx, tx, *it.InternalID, succeeded, a.UserID, a.RequestID); err != nil {
				return idempotency.Outcome{}, err
			}
			outcome := map[bool]string{true: "SUCCEEDED", false: "FAILED"}[succeeded]
			if err := writeAudit(ctx, tx, h.ids, a, auditEntry{Action: "payout.manual_resolve", TargetType: "payout", TargetID: *it.InternalID,
				Reason: reason, Diff: map[string]any{"outcome": outcome, "evidence_ref": evidence, "recon_item_id": id}}); err != nil {
				return idempotency.Outcome{}, err
			}
			diff["payout_id"] = *it.InternalID
		}
		if err := h.recon.ResolveItem(ctx, tx, id, in.Action, reason, a.UserID); err != nil {
			return idempotency.Outcome{}, err
		}
		if err := writeAudit(ctx, tx, h.ids, a, auditEntry{Action: "recon.item_resolve", TargetType: "recon_item", TargetID: id, Reason: reason, Diff: diff}); err != nil {
			return idempotency.Outcome{}, err
		}
		view, err := h.recon.ItemView(ctx, tx, id)
		if err != nil {
			return idempotency.Outcome{}, err
		}
		return idempotency.Outcome{Status: http.StatusOK, Body: view, ResourceType: "recon_item", ResourceID: &id}, nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	idempotency.Write(w, resp)
}

// ---------------------------------------------------------------- helpers

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// pageParams membaca limit (1–100, default 50) dan cursor. Cursor rusak → 400 INVALID_CURSOR.
func pageParams(qs url.Values, fe *[]apperr.FieldError) (int32, *httpx.Cursor, error) {
	limit := int32(defaultLimit)
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
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

// writePage: query mengambil limit+1 baris; baris ekstra hanya menandai has_more.
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
