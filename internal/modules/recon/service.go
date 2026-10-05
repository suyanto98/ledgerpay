package recon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/recon/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/metrics"
	"ledgerpay/internal/platform/testhook"
	"ledgerpay/internal/platform/tracing"
)

// Provider adalah nama provider di recon_runs / topups.provider (V1: satu provider, mock).
const Provider = "mock"

// WIB: business_date dihitung di Asia/Jakarta (tanpa DST; FixedZone karena image tanpa tzdata).
var WIB = time.FixedZone("WIB", 7*3600)

// DayWindow: [awal hari, cutoff) untuk objek yang HARUS ada di file hari itu.
func DayWindow(day time.Time) (from, cutoff time.Time) {
	from = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, WIB)
	return from, from.Add(24*time.Hour - CutoffTolerance)
}

// Source mengambil file settlement provider untuk satu business_date (YYYY-MM-DD).
type Source interface {
	Settlement(ctx context.Context, businessDate string) ([]byte, error)
}

// HTTPSource: GET {base}/v1/settlements/{date}.csv (docs/design/14-api-external-contracts.md §1.6).
type HTTPSource struct {
	baseURL, secret string
	client          *http.Client
}

func NewHTTPSource(baseURL, secret string, timeout time.Duration) *HTTPSource {
	return &HTTPSource{baseURL: strings.TrimRight(baseURL, "/"), secret: secret,
		client: &http.Client{Timeout: timeout, Transport: metrics.OutboundTransport("provider", tracing.Transport(nil))}}
}

const maxFileBytes = 5 << 20 // 5 MB (kontrak POST /ops/recon-runs)

func (s *HTTPSource) Settlement(ctx context.Context, businessDate string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/v1/settlements/"+businessDate+".csv", nil) //nolint:gosec // G704: base URL dari konfigurasi server
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)
	resp, err := s.client.Do(req) //nolint:gosec // G704: base URL dari konfigurasi server, path dari tanggal tervalidasi
	if err != nil {
		return nil, fmt.Errorf("recon: fetch settlement: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("recon: fetch settlement: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFileBytes {
		return nil, errors.New("recon: settlement file larger than 5 MB")
	}
	return body, nil
}

type Service struct {
	pool   *pgxpool.Pool
	ledger *ledger.Service
	source Source
	ids    idgen.Generator
}

func NewService(pool *pgxpool.Pool, l *ledger.Service, src Source, ids idgen.Generator) *Service {
	return &Service{pool: pool, ledger: l, source: src, ids: ids}
}

// Run adalah ringkasan satu recon run.
type Run struct {
	ID              uuid.UUID
	BusinessDate    time.Time
	Source          string
	Status          string
	Rows            int32
	Matched         int32
	MissingInternal int32
	MissingExternal int32
	AmountMismatch  int32
	StatusMismatch  int32
	Error           *string
	StartedAt       time.Time
	FinishedAt      *time.Time
}

// Reconcile menjalankan recon untuk businessDate (PULL dari provider). created = false kalau file
// dengan hash yang sama untuk tanggal itu sudah pernah diproses (LI-12): run lama dikembalikan.
//
// Satu transaksi: lock advisory per tanggal → run → klasifikasi → journal PROVIDER_SETTLEMENT untuk
// top-up MATCHED (dedupe per top-up) → item → ringkasan. Gagal di tengah = tidak ada jejak setengah.
func (s *Service) Reconcile(ctx context.Context, businessDate time.Time, by *uuid.UUID) (Run, bool, error) {
	date := businessDate.In(WIB).Format("2006-01-02")
	raw, err := s.source.Settlement(ctx, date) // di LUAR transaksi: tidak ada network di dalam tx
	if err != nil {
		metrics.ReconRuns.WithLabelValues("fetch_failed").Inc()
		return Run{}, false, apperr.ErrUnavailable.WithCause(err)
	}
	day, _ := time.ParseInLocation("2006-01-02", date, WIB)
	sum := sha256.Sum256(raw)
	var runID uuid.UUID
	created := false
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockReconDate(ctx, "recon:"+Provider+":"+date); err != nil {
			return err
		}
		ext, perr := Parse(raw, date)
		if perr != nil { // file rusak: run FAILED tercatat (dengan hash), tidak ada item
			msg := perr.Error()
			id, err := q.InsertFailedRun(ctx, store.InsertFailedRunParams{ID: s.ids.New(), Provider: Provider, BusinessDate: day,
				FileHash: sum[:], Source: "PULL", CreatedBy: by, Error: &msg})
			return s.resolveExisting(ctx, q, day, sum[:], id, err, &runID, &created)
		}
		id, err := q.InsertRun(ctx, store.InsertRunParams{ID: s.ids.New(), Provider: Provider, BusinessDate: day,
			FileHash: sum[:], Source: "PULL", CreatedBy: by})
		if err := s.resolveExisting(ctx, q, day, sum[:], id, err, &runID, &created); err != nil || !created {
			return err
		}
		return s.match(ctx, tx, runID, day, ext)
	})
	if err != nil {
		metrics.ReconRuns.WithLabelValues("error").Inc()
		return Run{}, false, err
	}
	run, err := s.Get(ctx, s.pool, runID)
	if err == nil && created {
		metrics.ReconRuns.WithLabelValues(strings.ToLower(run.Status)).Inc()
	}
	return run, created, err
}

// resolveExisting: INSERT … ON CONFLICT DO NOTHING mengembalikan ErrNoRows kalau file sudah ada.
func (s *Service) resolveExisting(ctx context.Context, q *store.Queries, day time.Time, hash []byte, id uuid.UUID, err error, runID *uuid.UUID, created *bool) error {
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := q.GetRunByFile(ctx, store.GetRunByFileParams{Provider: Provider, BusinessDate: day, FileHash: hash})
		*runID = existing
		return gerr
	}
	if err != nil {
		return err
	}
	*runID, *created = id, true
	return nil
}

func (s *Service) match(ctx context.Context, tx pgx.Tx, runID uuid.UUID, day time.Time, ext []External) error {
	q := store.New(tx)
	from, cutoff := DayWindow(day)
	internal := map[Key]Internal{}
	var expected []Key
	refs := map[string][]uuid.UUID{}
	for _, e := range ext {
		if id, err := uuid.Parse(e.Reference); err == nil {
			refs[e.Type] = append(refs[e.Type], id)
		}
	}
	topupIDs, err := q.ExpectedTopups(ctx, store.ExpectedTopupsParams{Provider: Provider, CreatedFrom: from.Add(-48 * time.Hour), CutoffTs: cutoff, FromTs: from})
	if err != nil {
		return err
	}
	payoutIDs, err := q.ExpectedPayouts(ctx, store.ExpectedPayoutsParams{CreatedFrom: from.Add(-48 * time.Hour), CutoffTs: cutoff, FromTs: from})
	if err != nil {
		return err
	}
	for _, id := range topupIDs {
		expected = append(expected, Key{Topup, id})
	}
	for _, id := range payoutIDs {
		expected = append(expected, Key{Payout, id})
	}
	topups, err := q.TopupsByIDs(ctx, append(refs[Topup], topupIDs...))
	if err != nil {
		return err
	}
	for _, t := range topups {
		internal[Key{Topup, t.ID}] = Internal{Type: Topup, ID: t.ID, Amount: t.Amount, Status: t.Status, Currency: t.Currency}
	}
	payouts, err := q.PayoutsByIDs(ctx, append(refs[Payout], payoutIDs...))
	if err != nil {
		return err
	}
	for _, p := range payouts {
		internal[Key{Payout, p.ID}] = Internal{Type: Payout, ID: p.ID, Amount: p.Amount, Status: p.Status, Currency: p.Currency}
	}

	counts := map[string]int32{}
	for _, it := range Classify(ext, internal, expected) {
		counts[it.Category]++
		var journalID *uuid.UUID
		var resolution *string
		if it.Category == Matched {
			if it.Type == Topup && *it.ExternalStatus == "SUCCEEDED" {
				id, err := s.settle(ctx, tx, *it.InternalID, *it.ExternalAmount, it.Currency, runID)
				if err != nil {
					return err
				}
				journalID = &id
			}
		} else {
			open := "OPEN" // antrean investigasi ops
			resolution = &open
		}
		if err := q.InsertItem(ctx, store.InsertItemParams{ID: s.ids.New(), ReconRunID: &runID, ObjectType: it.Type,
			InternalID: it.InternalID, ExternalRef: it.ExternalRef, ProviderRef: it.ProviderRef, Category: it.Category,
			InternalAmount: it.InternalAmount, ExternalAmount: it.ExternalAmount, InternalStatus: it.InternalStatus,
			ExternalStatus: it.ExternalStatus, Currency: it.Currency, Resolution: resolution, SettlementJournalID: journalID}); err != nil {
			return err
		}
	}
	return q.CompleteRun(ctx, store.CompleteRunParams{ID: runID, RowCount: int32(len(ext)), //nolint:gosec // G115: file ≤ 5 MB
		MatchedCount: counts[Matched], MissingInternalCount: counts[MissingInternal], MissingExternalCount: counts[MissingExternal],
		AmountMismatchCount: counts[AmountMismatch], StatusMismatchCount: counts[StatusMismatch]})
}

// settle: uang top-up pindah dari provider ke rekening bank (DR BANK_CLEARING / CR PROVIDER_CLEARING).
// Source = ('provider_settlement', topup_id): recon ulang, bahkan dengan file ber-hash lain, merujuk
// journal yang sudah ada alih-alih memposting dua kali (Step 12 R1-5).
func (s *Service) settle(ctx context.Context, tx pgx.Tx, topupID uuid.UUID, amount int64, currency string, runID uuid.UUID) (uuid.UUID, error) {
	if id, ok, err := s.ledger.JournalBySource(ctx, tx, "provider_settlement", topupID, ledger.PurposeProviderSettlement); err != nil || ok {
		return id, err
	}
	// Titik hook: "sudah dicek belum ada, belum diposting". Run lain untuk tanggal yang sama harus
	// menunggu lock advisory di sini (TestRecon_ConcurrentDifferentFiles_OneSettlementJournal).
	if err := testhook.Pause(ctx, "recon.before_settlement_post"); err != nil {
		return uuid.Nil, err
	}
	bank, err := s.ledger.SystemAccountID(ctx, tx, "BANK_CLEARING")
	if err != nil {
		return uuid.Nil, err
	}
	provider, err := s.ledger.SystemAccountID(ctx, tx, "PROVIDER_CLEARING")
	if err != nil {
		return uuid.Nil, err
	}
	id := s.ids.New()
	return id, s.ledger.PostJournal(ctx, tx, ledger.Journal{
		ID: id, Purpose: ledger.PurposeProviderSettlement, SourceType: "provider_settlement", SourceID: topupID, Currency: currency,
		CorrelationID: "recon:" + runID.String(), Actor: ledger.Actor{Type: "SYSTEM"},
		Entries: []ledger.Entry{
			{AccountID: bank, Direction: ledger.Debit, Amount: amount},      // uang sudah di rekening bank
			{AccountID: provider, Direction: ledger.Credit, Amount: amount}, // piutang ke provider lunas
		},
	})
}

// Get mengembalikan satu run.
func (s *Service) Get(ctx context.Context, q db.DBTX, id uuid.UUID) (Run, error) {
	r, err := store.New(q).GetRun(ctx, id)
	if err != nil {
		return Run{}, db.MapError(err)
	}
	return Run{ID: r.ID, BusinessDate: r.BusinessDate, Source: r.Source, Status: r.Status, Rows: r.RowCount, Matched: r.MatchedCount,
		MissingInternal: r.MissingInternalCount, MissingExternal: r.MissingExternalCount, AmountMismatch: r.AmountMismatchCount,
		StatusMismatch: r.StatusMismatchCount, Error: r.Error, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}, nil
}

// Daily (job recon.daily, dicek tiap jam): setelah 02:30 WIB, recon D−1 kalau belum ada run COMPLETED.
// Sekaligus catch-up: worker yang mati saat jadwal tetap menjalankannya di jam berikutnya.
func (s *Service) Daily(ctx context.Context, now time.Time) (bool, error) {
	local := now.In(WIB)
	if local.Hour() < 2 || (local.Hour() == 2 && local.Minute() < 30) {
		return false, nil
	}
	day := time.Date(local.Year(), local.Month(), local.Day()-1, 0, 0, 0, 0, WIB)
	done, err := store.New(s.pool).HasCompletedRun(ctx, store.HasCompletedRunParams{Provider: Provider, BusinessDate: day})
	if err != nil || done {
		return false, db.MapError(err)
	}
	_, created, err := s.Reconcile(ctx, day, nil)
	return created, err
}

// LockOpenItem mengunci item recon untuk resolusi ops (di tx pemanggil).
func LockOpenItem(ctx context.Context, tx pgx.Tx, id uuid.UUID) (store.LockItemRow, error) {
	it, err := store.New(tx).LockItem(ctx, id)
	if err != nil {
		return store.LockItemRow{}, db.MapError(err)
	}
	if it.Resolution == nil || *it.Resolution != "OPEN" { // MATCHED (tanpa resolusi) atau sudah RESOLVED
		return store.LockItemRow{}, apperr.ErrInvalidState
	}
	return it, nil
}

// ResolveItem menandai item RESOLVED (di tx pemanggil, setelah LockOpenItem).
func ResolveItem(ctx context.Context, tx pgx.Tx, id uuid.UUID, action, note string, by uuid.UUID) error {
	n, err := store.New(tx).ResolveItem(ctx, store.ResolveItemParams{ID: id, Action: &action, Note: &note, ResolvedBy: &by})
	if err != nil {
		return db.MapError(err)
	}
	if n != 1 {
		return apperr.ErrInvalidState
	}
	return nil
}

// Stats: gauge item recon yang masih OPEN (loop periodik worker).
func Stats(ctx context.Context, q db.DBTX) error {
	n, err := store.New(q).OpenItemsCount(ctx)
	if err != nil {
		return db.MapError(err)
	}
	metrics.ReconOpenItems.WithLabelValues().Set(float64(n))
	return nil
}
