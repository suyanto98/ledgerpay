// Package inbox: webhook masuk dari provider (ADR-L08, LI-09).
//
//	POST /webhooks/provider:  raw body → verifikasi HMAC → cek timestamp → INSERT inbox (dedupe) → 200
//	Worker (Processor):       claim (lease) → verify-by-fetch (di luar tx) → tx[fence → apply → final] → commit
//
// Handler sengaja TIDAK menjalankan efek bisnis: respons cepat memutus ketergantungan antara retry
// provider dan pemrosesan internal, dan raw body tersimpan sebagai bukti + bahan replay.
//
// Modul ini generik terhadap jenis event. Efek bisnis diserahkan ke Applier (topup untuk "charge.*"),
// yang disuntikkan composition root; inbox tidak meng-import modul topup (ADR-001).
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/inbox/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/observability"
	"ledgerpay/internal/platform/testhook"
	"ledgerpay/internal/platform/webhooksig"
)

// Provider tunggal V1.
const Provider = "mock"

// SignatureTolerance: |now − t| ≤ 5 menit (anti-replay; replay di dalam window ditangani dedupe).
const SignatureTolerance = 5 * time.Minute

// Applier menerapkan event satu keluarga ("charge"). Tipe sengaja primitif supaya modul pelaksana
// tidak perlu meng-import inbox.
type Applier interface {
	// VerifyEvent di LUAR transaksi (boleh memanggil jaringan). err → coba lagi; dead → DEAD.
	VerifyEvent(ctx context.Context, eventType string, payload []byte) (dead string, err error)
	// ApplyEvent di DALAM transaksi, setelah baris inbox di-fence.
	ApplyEvent(ctx context.Context, tx pgx.Tx, eventType, eventID string, payload []byte) (status, outcome, dead string, err error)
}

// ---------------------------------------------------------------- handler webhook

type Handler struct {
	pool    *pgxpool.Pool
	secrets [][]byte // boleh lebih dari satu: masa transisi rotasi secret
	clock   clock.Clock
	ids     idgen.Generator
}

func NewHandler(pool *pgxpool.Pool, secrets []string, c clock.Clock, ids idgen.Generator) *Handler {
	h := &Handler{pool: pool, clock: c, ids: ids}
	for _, s := range secrets {
		if s = strings.TrimSpace(s); s != "" {
			h.secrets = append(h.secrets, []byte(s))
		}
	}
	return h
}

// Routes: endpoint publik tanpa JWT/rate limit; autentikasinya adalah tanda tangan HMAC.
func (h *Handler) Routes(public chi.Router) {
	public.Post("/webhooks/provider", h.receive)
}

type envelope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		ChargeID string `json:"charge_id"`
	} `json:"data"`
}

func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	// 1. Raw body SEBELUM parse JSON: signature dihitung atas byte persis yang dikirim provider.
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			httpx.WriteError(w, r, apperr.ErrPayloadTooLarge)
			return
		}
		httpx.WriteError(w, r, apperr.ErrMalformedJSON)
		return
	}
	// 2–3. HMAC (constant-time) lalu timestamp.
	ts, err := webhooksig.Verify(r.Header.Get("Provider-Signature"), raw, h.secrets, h.clock.Now(), SignatureTolerance)
	switch {
	case errors.Is(err, webhooksig.ErrStale):
		httpx.WriteError(w, r, apperr.ErrStaleWebhook)
		return
	case err != nil:
		observability.Logger(r.Context()).Warn("webhook.signature_rejected", "reason", err.Error())
		httpx.WriteError(w, r, apperr.ErrInvalidSignature)
		return
	}
	var ev envelope
	if err := json.Unmarshal(raw, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		httpx.WriteError(w, r, apperr.ErrMalformedJSON)
		return
	}
	// 4. Simpan (dedupe pada provider_event_id). Duplikat tetap 200, supaya provider berhenti retry.
	_, err = store.New(h.pool).InsertEvent(r.Context(), store.InsertEventParams{
		ID: h.ids.New(), Provider: Provider, ProviderEventID: ev.ID, EventType: ev.Type, ResourceRef: ev.Data.ChargeID,
		Payload: raw, RawBody: raw, SignatureTs: ts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"received": true, "duplicate": true})
		return
	}
	if err != nil { // DB gagal → 5xx → provider akan retry
		httpx.WriteError(w, r, db.MapError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// ---------------------------------------------------------------- worker

// ProcessorConfig: nilai default sesuai docs/design/19-tx-concurrency.md §2.6.
type ProcessorConfig struct {
	WorkerID    string
	Lease       time.Duration // default 30 dtk
	Batch       int32         // default 10
	MaxAttempts int32         // default 8, lalu DEAD
}

type Processor struct {
	pool     *pgxpool.Pool
	appliers map[string]Applier // per keluarga event: "charge" → topup
	cfg      ProcessorConfig
}

func NewProcessor(pool *pgxpool.Pool, appliers map[string]Applier, cfg ProcessorConfig) *Processor {
	if cfg.Lease == 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.Batch == 0 {
		cfg.Batch = 10
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 8
	}
	return &Processor{pool: pool, appliers: appliers, cfg: cfg}
}

// Run memproses inbox sampai ctx selesai. Polling sederhana; LISTEN/NOTIFY bisa ditambah kalau
// latensi top-up perlu < interval (ADR-L08 trade-off).
func (p *Processor) Run(ctx context.Context, interval time.Duration) {
	log := observability.Logger(ctx)
	for {
		n, err := p.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error("inbox.run_failed", "err", err.Error())
		}
		if n > 0 {
			continue // masih ada antrean: langsung ambil batch berikutnya
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// RunOnce meng-claim satu batch dan memprosesnya. Mengembalikan jumlah event yang di-claim.
func (p *Processor) RunOnce(ctx context.Context) (int, error) {
	events, err := store.New(p.pool).ClaimEvents(ctx, store.ClaimEventsParams{
		Worker: &p.cfg.WorkerID, LeaseSecs: p.cfg.Lease.Seconds(), Batch: p.cfg.Batch,
	})
	if err != nil {
		return 0, db.MapError(err)
	}
	for _, ev := range events {
		p.process(ctx, ev)
	}
	return len(events), nil
}

func (p *Processor) process(ctx context.Context, ev store.ClaimEventsRow) {
	log := observability.Logger(ctx).With("event_id", ev.ID.String(), "event_type", ev.EventType)
	family, _, _ := strings.Cut(ev.EventType, ".")
	applier := p.appliers[family]
	if applier == nil { // jenis event tak dikenal: disimpan sebagai bukti, tidak diproses
		p.finish(ctx, ev, func(context.Context, pgx.Tx) (string, string, string, error) { return "IGNORED", "STALE", "", nil })
		return
	}
	// Verify-by-fetch di LUAR transaksi: panggilan jaringan tidak boleh menahan lock (ADR-011).
	if dead, err := applier.VerifyEvent(ctx, ev.EventType, ev.Payload); err != nil {
		p.fail(ctx, ev, err)
		return
	} else if dead != "" {
		log.Error("inbox.dead", "reason", dead)
		p.finish(ctx, ev, func(context.Context, pgx.Tx) (string, string, string, error) { return "DEAD", "", dead, nil })
		return
	}
	if err := testhook.Pause(ctx, "inbox.before_apply"); err != nil {
		p.fail(ctx, ev, err)
		return
	}
	p.finish(ctx, ev, func(ctx context.Context, tx pgx.Tx) (string, string, string, error) {
		return applier.ApplyEvent(ctx, tx, ev.EventType, providerEventID(ev.Payload), ev.Payload)
	})
}

// finish: satu transaksi [fence → apply → satu UPDATE final]. Fence gagal (lease sudah diambil worker
// lain) → rollback tanpa efek apa pun.
func (p *Processor) finish(ctx context.Context, ev store.ClaimEventsRow, apply func(context.Context, pgx.Tx) (status, outcome, dead string, err error)) {
	log := observability.Logger(ctx).With("event_id", ev.ID.String())
	lost := false
	err := db.WithTx(ctx, p.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.FenceEvent(ctx, store.FenceEventParams{ID: ev.ID, Worker: &p.cfg.WorkerID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				lost = true
			}
			return err
		}
		status, outcome, dead, err := apply(ctx, tx)
		if err != nil {
			return err
		}
		params := store.FinalizeEventParams{ID: ev.ID, Status: status}
		if dead != "" {
			params.Status, params.LastError = "DEAD", &dead
		} else {
			params.Outcome = &outcome
		}
		return q.FinalizeEvent(ctx, params)
	})
	switch {
	case lost:
		log.Warn("inbox.lease_lost", "detail", "another worker owns this event now; rolled back without effect")
	case err != nil:
		p.fail(ctx, ev, err)
	}
}

// fail: attempts++, backoff eksponensial, lease dilepas; setelah MaxAttempts → DEAD.
func (p *Processor) fail(ctx context.Context, ev store.ClaimEventsRow, cause error) {
	msg := cause.Error()
	observability.Logger(ctx).Warn("inbox.attempt_failed", "event_id", ev.ID.String(), "attempts", ev.Attempts+1, "err", msg)
	if err := store.New(p.pool).FailEvent(ctx, store.FailEventParams{LastError: &msg, MaxAttempts: p.cfg.MaxAttempts, ID: ev.ID, Worker: &p.cfg.WorkerID}); err != nil {
		observability.Logger(ctx).Error("inbox.fail_update_failed", "event_id", ev.ID.String(), "err", err.Error())
	}
}

func providerEventID(payload []byte) string {
	var e struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(payload, &e)
	return e.ID
}
