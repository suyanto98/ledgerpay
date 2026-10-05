// Package payout: tarik saldo ke rekening bank (LF-11) dengan hasil yang bisa "tidak diketahui"
// (ADR-L06). Prinsip: tidak pernah menebak. Kalau bank mungkin sudah mengirim uang, payout menjadi
// UNKNOWN dan diselesaikan lewat status query, bukan reversal atau submit ulang buta.
//
//	POST /payouts (1 tx): hold journal (DR wallet / CR PAYOUT_IN_TRANSIT) + payouts PENDING + job submit
//	payout.submit:        Tx-a SUBMITTED (commit) → panggil bank (di luar tx) → Tx-b hasil + CompleteInTx
//	payout.status_check:  jadwal 30s, 2m, 10m, 1h, 6h → SUCCEEDED/FAILED, atau needs_reconciliation
package payout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payout/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/crypto"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/jobs"
	"ledgerpay/internal/platform/metrics"
	"ledgerpay/internal/platform/observability"
	"ledgerpay/internal/platform/testhook"
)

// Batas kontrak API §8.
const (
	MinAmount int64 = 1_000_000     // Rp10.000
	MaxAmount int64 = 2_500_000_000 // Rp25.000.000
)

// Job kinds.
const (
	KindSubmit      = "payout.submit"
	KindStatusCheck = "payout.status_check"
)

// StatusCheckSchedule: jeda SEBELUM check ke-n (ADR-L06). Setelah check ke-5 masih tak pasti →
// needs_reconciliation (antrean ops), bukan dead.
var StatusCheckSchedule = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}

// Config: SafeWindow = janji kontrak bank "payout yang diterima selalu terlihat di status query
// paling lambat X setelah diterima" (mock: 5 menit). Hanya setelah lewat window ini, NOT_FOUND
// boleh dijadikan alasan submit ulang (dengan key yang sama).
type Config struct {
	SafeWindow time.Duration
}

type Service struct {
	pool    *pgxpool.Pool
	ledger  *ledger.Service
	bank    Bank
	keyring *crypto.Keyring
	ids     idgen.Generator
	cfg     Config
}

func NewService(pool *pgxpool.Pool, l *ledger.Service, bank Bank, kr *crypto.Keyring, ids idgen.Generator, cfg Config) *Service {
	if cfg.SafeWindow == 0 {
		cfg.SafeWindow = 5 * time.Minute
	}
	return &Service{pool: pool, ledger: l, bank: bank, keyring: kr, ids: ids, cfg: cfg}
}

// Command adalah payout yang sudah lolos validasi format.
type Command struct {
	UserID        uuid.UUID
	Amount        int64
	Currency      string
	BankCode      string
	AccountNumber string
	HolderName    string
	CorrelationID string
}

// Payout dari sudut pandang pemilik.
type Payout struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	Amount              int64
	Currency            string
	Status              string
	BankCode            string
	Last4               string
	HolderName          string
	HoldJournalID       uuid.UUID
	NeedsReconciliation bool
	CreatedAt           time.Time
}

// Create: satu tx (di dalam idempotency.Execute). Dana ditahan seketika, sehingga tidak bisa dipakai
// transfer lain; bank dipanggil worker (outbox), tidak pernah di request ini (ADR-011).
func (s *Service) Create(ctx context.Context, tx pgx.Tx, c Command) (Payout, error) {
	w, err := s.ledger.WalletOf(ctx, tx, c.UserID)
	if err != nil {
		return Payout{}, err
	}
	inTransit, err := s.ledger.SystemAccountID(ctx, tx, "PAYOUT_IN_TRANSIT")
	if err != nil {
		return Payout{}, err
	}
	id, holdID := s.ids.New(), s.ids.New()
	user := c.UserID
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{
		ID: holdID, Purpose: ledger.PurposePayoutHold, SourceType: "payout", SourceID: id, Currency: c.Currency,
		CorrelationID: c.CorrelationID, Actor: ledger.Actor{Type: "USER", ID: &user},
		Entries: []ledger.Entry{
			{AccountID: w.AccountID, Direction: ledger.Debit, Amount: c.Amount}, // saldo user turun (ditahan)
			{AccountID: inTransit, Direction: ledger.Credit, Amount: c.Amount},  // utang "sedang dikirim"
		},
	}); err != nil {
		return Payout{}, err
	}
	// Nomor rekening = PII: dienkripsi, AAD = payout id (blob tidak bisa dipindah ke baris lain).
	enc, err := s.keyring.Encrypt([]byte(c.AccountNumber), []byte(id.String()))
	if err != nil {
		return Payout{}, err
	}
	last4 := c.AccountNumber[len(c.AccountNumber)-4:]
	createdAt, err := store.New(tx).InsertPayout(ctx, store.InsertPayoutParams{
		ID: id, UserID: c.UserID, AccountID: w.AccountID, Amount: c.Amount, Currency: c.Currency, BankCode: c.BankCode,
		AccountNumberEnc: enc, AccountNumberLast4: last4, HolderName: c.HolderName, HoldJournalID: holdID,
	})
	if err != nil {
		return Payout{}, db.MapError(err)
	}
	metrics.Payouts.WithLabelValues("requested").Inc()
	if _, err := jobs.Enqueue(ctx, tx, jobs.Spec{Kind: KindSubmit, Payload: jobPayload{PayoutID: id}, Dedupe: KindSubmit + ":" + id.String(),
		MaxAttempts: 5, CorrelationID: c.CorrelationID}); err != nil {
		return Payout{}, err
	}
	return Payout{ID: id, UserID: c.UserID, Amount: c.Amount, Currency: c.Currency, Status: "PENDING", BankCode: c.BankCode,
		Last4: last4, HolderName: c.HolderName, HoldJournalID: holdID, CreatedAt: createdAt}, nil
}

// ForUser: payout milik orang lain → 404.
func (s *Service) ForUser(ctx context.Context, q db.DBTX, userID, id uuid.UUID) (Payout, error) {
	r, err := store.New(q).GetPayout(ctx, id)
	if err != nil {
		return Payout{}, db.MapError(err)
	}
	if r.UserID != userID {
		return Payout{}, apperr.ErrNotFound
	}
	return Payout{ID: r.ID, UserID: r.UserID, Amount: r.Amount, Currency: r.Currency, Status: r.Status, BankCode: r.BankCode,
		Last4: r.AccountNumberLast4, HolderName: r.HolderName, HoldJournalID: r.HoldJournalID, NeedsReconciliation: r.NeedsReconciliation, CreatedAt: r.CreatedAt}, nil
}

// ---------------------------------------------------------------- job handlers

type jobPayload struct {
	PayoutID uuid.UUID `json:"payout_id"`
	CheckNo  int       `json:"check_no,omitempty"`
}

// Kinds mengembalikan definisi job payout untuk lane "money".
func (s *Service) Kinds() []jobs.Kind {
	return []jobs.Kind{
		{Name: KindSubmit, Handler: s.handleSubmit, MaxAttempts: 5, Timeout: 15 * time.Second,
			Backoff: jobs.Schedule([]time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}, 0.2)},
		{Name: KindStatusCheck, Handler: s.handleStatusCheck, MaxAttempts: 3, Timeout: 15 * time.Second,
			OnDead: func(ctx context.Context, tx pgx.Tx, j *jobs.Job) error { // infra gagal terus → antrean ops
				var p jobPayload
				if err := j.Decode(&p); err != nil {
					return nil
				}
				msg := "status check failed repeatedly (infrastructure)"
				return store.New(tx).FlagNeedsReconciliation(ctx, store.FlagNeedsReconciliationParams{ID: p.PayoutID, LastError: &msg})
			}},
	}
}

// handleSubmit: ADR-L06 + docs/design/19-tx-concurrency.md §2.5.
func (s *Service) handleSubmit(ctx context.Context, j *jobs.Job) error {
	var p jobPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	q := store.New(s.pool)
	// Tx-a: PENDING → SUBMITTED, COMMIT sebelum bank dipanggil.
	n, err := q.MarkSubmitted(ctx, p.PayoutID)
	if err != nil {
		return db.MapError(err)
	}
	if n == 0 {
		cur, err := q.GetPayout(ctx, p.PayoutID)
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.Permanent(fmt.Errorf("payout %s not found", p.PayoutID))
		}
		if err != nil {
			return db.MapError(err)
		}
		if cur.Status == "SUBMITTED" {
			// Re-delivery setelah crash: bank MUNGKIN sudah menerima. JANGAN submit ulang; tanyakan.
			observability.Logger(ctx).Warn("payout.redelivered_submitted", "payout_id", p.PayoutID.String())
			return s.markUnknown(ctx, j, p.PayoutID, "job re-delivered while SUBMITTED (crash after Tx-a)", false)
		}
		return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error { return jobs.CompleteInTx(ctx, tx, j) }) // sudah diproses: no-op
	}

	metrics.Payouts.WithLabelValues("submitted").Inc()
	cur, err := q.GetPayout(ctx, p.PayoutID)
	if err != nil {
		return db.MapError(err)
	}
	acct, err := s.keyring.Decrypt(cur.AccountNumberEnc, []byte(cur.ID.String()))
	if err != nil {
		return jobs.Permanent(err)
	}
	res := s.bank.Submit(ctx, SubmitInput{PayoutID: cur.ID.String(), Amount: cur.Amount, Currency: cur.Currency,
		BankCode: cur.BankCode, AccountNumber: string(acct), HolderName: cur.HolderName})
	// Titik hook: "worker mati setelah bank menerima, sebelum Tx-b" (TestPayoutSubmit_CrashAfterBankAccepted_NoDoubleSend).
	if err := testhook.Pause(ctx, "payout.after_bank_call"); err != nil {
		return err
	}
	log := observability.Logger(ctx).With("payout_id", cur.ID.String())
	switch res.Outcome {
	case OutcomeSucceeded:
		return s.settle(ctx, j, cur, "BANK_RESPONSE", []string{"SUBMITTED"}, nil)
	case OutcomeFailed:
		log.Info("payout.declined", "detail", res.Detail)
		return s.reverse(ctx, j, cur, "BANK_RESPONSE", []string{"SUBMITTED"}, nil)
	default: // Unknown / Processing: tanyakan lewat status query
		if res.Mismatch {
			metrics.PayoutMismatch.WithLabelValues().Inc()
			log.Error("payout.bank_response_mismatch", "detail", res.Detail) // indikasi bug / provider dikompromikan
		}
		return s.markUnknown(ctx, j, cur.ID, res.Detail, res.Mismatch)
	}
}

// markUnknown: SUBMITTED → UNKNOWN + status check #1 (30 dtk), satu tx dengan CompleteInTx.
func (s *Service) markUnknown(ctx context.Context, j *jobs.Job, id uuid.UUID, detail string, needsRecon bool) error {
	metrics.Payouts.WithLabelValues("unknown").Inc()
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := store.New(tx).MarkPayoutUnknown(ctx, store.MarkPayoutUnknownParams{ID: id, LastError: &detail, NeedsReconciliation: needsRecon}); err != nil {
			return err
		}
		if err := s.enqueueCheck(ctx, tx, id, 1, j.CorrelationID); err != nil {
			return err
		}
		return jobs.CompleteInTx(ctx, tx, j)
	})
}

func (s *Service) enqueueCheck(ctx context.Context, tx pgx.Tx, id uuid.UUID, n int, corr string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.Spec{Kind: KindStatusCheck, Payload: jobPayload{PayoutID: id, CheckNo: n},
		Dedupe: fmt.Sprintf("%s:%s:%d", KindStatusCheck, id, n), Delay: StatusCheckSchedule[n-1], MaxAttempts: 3, CorrelationID: corr})
	return err
}

// handleStatusCheck: UNKNOWN → jawaban bank.
func (s *Service) handleStatusCheck(ctx context.Context, j *jobs.Job) error {
	var p jobPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	cur, err := store.New(s.pool).GetPayout(ctx, p.PayoutID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("payout %s not found", p.PayoutID))
	}
	if err != nil {
		return db.MapError(err)
	}
	if cur.Status != "UNKNOWN" { // sudah diselesaikan (status check lain / ops / recon): no-op
		return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error { return jobs.CompleteInTx(ctx, tx, j) })
	}
	answer, err := s.bank.Status(ctx, cur.ID.String())
	if err != nil {
		metrics.PayoutStatusChecks.WithLabelValues("error").Inc()
		return err // infra: retry kind (3×), lalu OnDead → needs_reconciliation
	}
	checkResult := map[StatusAnswer]string{StatusSucceeded: "succeeded", StatusFailed: "failed", StatusNotFound: "not_found"}[answer]
	if checkResult == "" {
		checkResult = "still_unknown"
	}
	metrics.PayoutStatusChecks.WithLabelValues(checkResult).Inc()
	if answer == StatusNotFound && time.Duration(cur.AgeSinceSubmitSecs*float64(time.Second)) >= s.cfg.SafeWindow {
		// Bank menjamin: payout yang diterima selalu terlihat setelah safe window. NOT_FOUND sekarang
		// berarti bank TIDAK PERNAH menerimanya → aman submit ulang, dengan key yang SAMA (lapis kedua).
		acct, err := s.keyring.Decrypt(cur.AccountNumberEnc, []byte(cur.ID.String()))
		if err != nil {
			return jobs.Permanent(err)
		}
		observability.Logger(ctx).Warn("payout.resubmit_after_safe_window", "payout_id", cur.ID.String())
		if err := store.New(s.pool).RecordResubmit(ctx, cur.ID); err != nil {
			return db.MapError(err)
		}
		switch s.bank.Submit(ctx, SubmitInput{PayoutID: cur.ID.String(), Amount: cur.Amount, Currency: cur.Currency,
			BankCode: cur.BankCode, AccountNumber: string(acct), HolderName: cur.HolderName}).Outcome {
		case OutcomeSucceeded:
			answer = StatusSucceeded
		case OutcomeFailed:
			answer = StatusFailed
		default:
			answer = StatusProcessing
		}
	}
	status := string(answer)
	switch answer {
	case StatusSucceeded:
		return s.settle(ctx, j, cur, "STATUS_QUERY", []string{"UNKNOWN"}, &status)
	case StatusFailed:
		return s.reverse(ctx, j, cur, "STATUS_QUERY", []string{"UNKNOWN"}, &status)
	}
	// Belum pasti (PROCESSING, atau NOT_FOUND sebelum safe window): jadwalkan check berikutnya.
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		exhausted := p.CheckNo >= len(StatusCheckSchedule)
		if err := store.New(tx).RecordStatusCheck(ctx, store.RecordStatusCheckParams{ID: cur.ID, ProviderStatus: &status, NeedsReconciliation: exhausted}); err != nil {
			return err
		}
		if exhausted {
			metrics.Payouts.WithLabelValues("needs_reconciliation").Inc()
			observability.Logger(ctx).Error("payout.needs_reconciliation", "payout_id", cur.ID.String(), "checks", p.CheckNo)
		} else if err := s.enqueueCheck(ctx, tx, cur.ID, p.CheckNo+1, j.CorrelationID); err != nil {
			return err
		}
		return jobs.CompleteInTx(ctx, tx, j)
	})
}

// errStatusChanged: UPDATE status mengenai 0 baris SETELAH journal diposting. Tidak mungkin selama
// lockInStatus memegang lock baris; kalau terjadi (bug), seluruh tx rollback alih-alih menyisakan
// journal tanpa perubahan status.
var errStatusChanged = errors.New("payout: status changed under lock (journal rolled back)")

// resolution adalah siapa/apa yang menetapkan hasil akhir payout.
type resolution struct {
	source         string     // BANK_RESPONSE, STATUS_QUERY, OPS
	providerStatus *string    // jawaban bank terakhir (nil untuk OPS)
	by             *uuid.UUID // user OPS (wajib untuk source OPS, ck_payouts_ops_resolution)
	actor          ledger.Actor
	correlationID  string
}

// settle (job): settleTx + CompleteInTx dalam satu tx. Kalah balapan → selesai tanpa journal.
func (s *Service) settle(ctx context.Context, j *jobs.Job, cur store.GetPayoutRow, source string, from []string, providerStatus *string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		res := resolution{source: source, providerStatus: providerStatus, actor: ledger.Actor{Type: "SYSTEM"}, correlationID: j.CorrelationID}
		if _, err := s.settleTx(ctx, tx, cur, from, res); err != nil {
			return err
		}
		return jobs.CompleteInTx(ctx, tx, j)
	})
}

// reverse (job): reverseTx + CompleteInTx dalam satu tx.
func (s *Service) reverse(ctx context.Context, j *jobs.Job, cur store.GetPayoutRow, source string, from []string, providerStatus *string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		res := resolution{source: source, providerStatus: providerStatus, actor: ledger.Actor{Type: "SYSTEM"}, correlationID: j.CorrelationID}
		if _, err := s.reverseTx(ctx, tx, cur, from, res); err != nil {
			return err
		}
		return jobs.CompleteInTx(ctx, tx, j)
	})
}

// lockInStatus: guard + lock baris payout (posisi 2c) SEBELUM journal. Pihak yang kalah balapan
// (worker vs ops, race g) menunggu di sini, lalu mendapat false: tidak pernah sempat memposting.
func lockInStatus(ctx context.Context, q *store.Queries, id uuid.UUID, from []string) (bool, error) {
	if _, err := q.LockPayoutInStatus(ctx, store.LockPayoutInStatusParams{ID: id, Statuses: from}); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// Titik hook: pemenang memegang lock payout; pihak lain (ops / status check) harus menunggu.
	return true, testhook.Pause(ctx, "payout.resolve.after_lock")
}

// settleTx: journal PAYOUT_SETTLE (akun sistem saja) → SUCCEEDED. false = payout tidak lagi di
// status yang diharapkan (sudah diselesaikan pihak lain).
func (s *Service) settleTx(ctx context.Context, tx pgx.Tx, cur store.GetPayoutRow, from []string, res resolution) (bool, error) {
	q := store.New(tx)
	if ok, err := lockInStatus(ctx, q, cur.ID, from); !ok || err != nil {
		return false, err
	}
	inTransit, err := s.ledger.SystemAccountID(ctx, tx, "PAYOUT_IN_TRANSIT")
	if err != nil {
		return false, err
	}
	bank, err := s.ledger.SystemAccountID(ctx, tx, "BANK_CLEARING")
	if err != nil {
		return false, err
	}
	journalID := s.ids.New()
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{
		ID: journalID, Purpose: ledger.PurposePayoutSettle, SourceType: "payout", SourceID: cur.ID, Currency: cur.Currency,
		CorrelationID: res.correlationID, Actor: res.actor,
		Entries: []ledger.Entry{
			{AccountID: inTransit, Direction: ledger.Debit, Amount: cur.Amount}, // tidak lagi "sedang dikirim"
			{AccountID: bank, Direction: ledger.Credit, Amount: cur.Amount},     // uang keluar dari rekening penampung
		},
	}); err != nil {
		return false, err
	}
	n, err := q.MarkPayoutSucceeded(ctx, store.MarkPayoutSucceededParams{ID: cur.ID, SettleJournalID: &journalID,
		ResolutionSource: &res.source, ResolvedBy: res.by, ProviderStatus: res.providerStatus})
	if err != nil {
		return false, err
	}
	if n != 1 { // mustahil selama baris terkunci; kalau terjadi, journal di atas ikut rollback
		return false, errStatusChanged
	}
	metrics.Payouts.WithLabelValues("succeeded").Inc()
	return true, nil
}

// reverseTx: penolakan DEFINITIF → journal PAYOUT_REVERSAL (reversal_of = hold) mengembalikan dana ke wallet.
func (s *Service) reverseTx(ctx context.Context, tx pgx.Tx, cur store.GetPayoutRow, from []string, res resolution) (bool, error) {
	q := store.New(tx)
	if ok, err := lockInStatus(ctx, q, cur.ID, from); !ok || err != nil {
		return false, err
	}
	inTransit, err := s.ledger.SystemAccountID(ctx, tx, "PAYOUT_IN_TRANSIT")
	if err != nil {
		return false, err
	}
	journalID, hold := s.ids.New(), cur.HoldJournalID
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{ // lock wallet (posisi 3) setelah payout (2c) ✓
		ID: journalID, Purpose: ledger.PurposePayoutReversal, SourceType: "payout", SourceID: cur.ID, Currency: cur.Currency,
		ReversalOf: &hold, CorrelationID: res.correlationID, Actor: res.actor,
		Entries: []ledger.Entry{
			{AccountID: inTransit, Direction: ledger.Debit, Amount: cur.Amount},
			{AccountID: cur.AccountID, Direction: ledger.Credit, Amount: cur.Amount}, // dana kembali ke wallet
		},
	}); err != nil {
		return false, err
	}
	n, err := q.MarkPayoutFailed(ctx, store.MarkPayoutFailedParams{ID: cur.ID, ReversalJournalID: &journalID,
		ResolutionSource: &res.source, ResolvedBy: res.by, ProviderStatus: res.providerStatus})
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, errStatusChanged
	}
	metrics.Payouts.WithLabelValues("failed").Inc()
	return true, nil
}

// ResolveByOps menetapkan hasil payout secara manual (ADR-L06, LF-18), di tx pemanggil (yang juga
// menulis audit + bukti). Hanya dari UNKNOWN atau SUBMITTED; sudah final / masih PENDING → 409.
// Tidak ada "edit saldo": hasilnya journal PAYOUT_SETTLE atau PAYOUT_REVERSAL yang sama dengan
// jalur otomatis, sehingga ledger tetap satu-satunya sumber kebenaran.
func (s *Service) ResolveByOps(ctx context.Context, tx pgx.Tx, id uuid.UUID, succeeded bool, opsUserID uuid.UUID, correlationID string) error {
	cur, err := store.New(tx).GetPayout(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return db.MapError(err)
	}
	res := resolution{source: "OPS", by: &opsUserID, actor: ledger.Actor{Type: "OPS", ID: &opsUserID}, correlationID: correlationID}
	from := []string{"UNKNOWN", "SUBMITTED"}
	var applied bool
	if succeeded {
		applied, err = s.settleTx(ctx, tx, cur, from, res)
	} else {
		applied, err = s.reverseTx(ctx, tx, cur, from, res)
	}
	if err != nil {
		return db.MapError(err)
	}
	if !applied { // sudah diselesaikan (status query / ops lain) atau belum pernah dikirim ke bank
		return apperr.ErrInvalidState
	}
	metrics.Payouts.WithLabelValues("ops_resolved").Inc()
	return nil
}

// Stats mengisi gauge payout UNKNOWN (loop periodik worker; alert umur tertua > 1 jam).
func Stats(ctx context.Context, q db.DBTX) error {
	r, err := store.New(q).UnknownStats(ctx)
	if err != nil {
		return db.MapError(err)
	}
	metrics.PayoutUnknown.WithLabelValues().Set(float64(r.N))
	metrics.PayoutUnknownOldest.WithLabelValues().Set(r.OldestSecs)
	return nil
}
