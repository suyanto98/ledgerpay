// Package payment: payment intent (LF-10). Merchant membuat intent lewat API key, user membayar
// dengan meng-confirm intent dari wallet-nya. Uang berpindah lewat satu journal PAYMENT
// (DR wallet user / CR wallet merchant, ADR-L01).
//
// Kebenaran confirm (docs/design/19-tx-concurrency.md §2.2):
//   - urutan lock: idempotency key → intent (FOR UPDATE) → wallet (ORDER BY id) → insert;
//   - guard status: dua tab yang meng-confirm bersamaan dengan key berbeda → yang kedua menunggu
//     lock intent, lalu melihat SUCCEEDED → 409 INVALID_STATE_TRANSITION (LI-13, satu pembayaran);
//   - expiry dibandingkan dengan now() = awal transaksi, sehingga race dengan sweeper deterministik.
package payment

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payment/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/testhook"
)

// Batas kontrak API §9.
const (
	MinAmount         int64 = 100
	MaxAmount         int64 = 10_000_000_000 // Rp100.000.000
	DefaultTTLSeconds int32 = 1800
	MinTTLSeconds     int32 = 300
	MaxTTLSeconds     int32 = 86400
)

// Status payment intent.
const (
	StatusRequiresConfirmation = "REQUIRES_CONFIRMATION"
	StatusSucceeded            = "SUCCEEDED"
	StatusExpired              = "EXPIRED"
)

// Users: apa yang dibutuhkan payment dari identity (nama payer di merchant view). Disuntikkan lewat adapter.
type Users interface {
	DisplayName(ctx context.Context, q db.DBTX, id uuid.UUID) (string, error)
}

type Service struct {
	ledger *ledger.Service
	users  Users
	ids    idgen.Generator
}

func NewService(l *ledger.Service, u Users, ids idgen.Generator) *Service {
	return &Service{ledger: l, users: u, ids: ids}
}

// Intent adalah payment intent lengkap; handler memilih field sesuai siapa yang melihat.
type Intent struct {
	ID              uuid.UUID
	MerchantID      uuid.UUID
	MerchantName    string
	MerchantOrderID string
	Amount          int64
	Currency        string
	RefundedAmount  int64
	Status          string // status efektif: REQUIRES_CONFIRMATION yang lewat expires_at ditampilkan EXPIRED
	Description     *string
	PayerUserID     *uuid.UUID
	PayerName       string
	ExpiresAt       time.Time
	ConfirmedAt     *time.Time
	CreatedAt       time.Time
}

// Paid: status yang berarti uang sudah berpindah.
func (i Intent) Paid() bool {
	return i.Status == StatusSucceeded || i.Status == "PARTIALLY_REFUNDED" || i.Status == "REFUNDED"
}

// CreateCommand adalah intent yang sudah lolos validasi format.
type CreateCommand struct {
	MerchantID      uuid.UUID
	MerchantOrderID string
	Amount          int64
	Currency        string
	Description     *string
	TTLSeconds      int32
}

// Create membuat intent di dalam tx idempotency merchant. merchant_order_id yang masih punya
// intent hidup → 409 DUPLICATE_MERCHANT_ORDER + ID intent yang ada, supaya merchant bisa melanjutkan.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, c CreateCommand) (Intent, error) {
	w, err := s.ledger.MerchantWalletOf(ctx, tx, c.MerchantID)
	if err != nil {
		return Intent{}, err
	}
	id := s.ids.New()
	// Savepoint kecil: kalau INSERT melanggar unique index, transaksi tidak "aborted" dan kita
	// masih bisa membaca intent yang ada untuk existing_resource_id.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return Intent{}, err
	}
	_, err = store.New(sp).InsertIntent(ctx, store.InsertIntentParams{
		ID: id, MerchantID: c.MerchantID, MerchantAccountID: w.AccountID, MerchantOrderID: c.MerchantOrderID,
		Amount: c.Amount, Currency: c.Currency, Description: c.Description, ExpiresInSeconds: c.TTLSeconds,
	})
	if isUniqueViolation(err, "uq_payment_intents_merchant_order") {
		_ = sp.Rollback(ctx)
		existing, ferr := store.New(tx).FindLiveIntentByOrder(ctx, store.FindLiveIntentByOrderParams{MerchantID: c.MerchantID, MerchantOrderID: c.MerchantOrderID})
		if ferr != nil {
			return Intent{}, apperr.ErrDuplicateMerchantOrder
		}
		return Intent{}, apperr.ErrDuplicateMerchantOrder.WithExistingResource(existing.String())
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return Intent{}, db.MapError(err)
	}
	if err := sp.Commit(ctx); err != nil {
		return Intent{}, err
	}
	return s.load(ctx, tx, id)
}

// ForMerchant: intent milik merchant itu; milik merchant lain → 404 (BOLA, ADR-010).
func (s *Service) ForMerchant(ctx context.Context, q db.DBTX, merchantID, id uuid.UUID) (Intent, error) {
	in, err := s.load(ctx, q, id)
	if err != nil {
		return Intent{}, err
	}
	if in.MerchantID != merchantID {
		return Intent{}, apperr.ErrNotFound
	}
	return in, nil
}

// ForUser: sebelum dibayar, ID intent adalah capability URL (user login mana pun yang memegang
// link boleh melihat dan membayar). Setelah dibayar, hanya payer; selain itu 404.
func (s *Service) ForUser(ctx context.Context, q db.DBTX, userID, id uuid.UUID) (Intent, error) {
	in, err := s.load(ctx, q, id)
	if err != nil {
		return Intent{}, err
	}
	if in.Paid() && (in.PayerUserID == nil || *in.PayerUserID != userID) {
		return Intent{}, apperr.ErrNotFound
	}
	return in, nil
}

// Confirm membayar intent dari wallet payer, di dalam tx idempotency milik payer.
func (s *Service) Confirm(ctx context.Context, tx pgx.Tx, payerID, intentID uuid.UUID, correlationID string) (Intent, error) {
	q := store.New(tx)
	pi, err := q.LockIntent(ctx, intentID) // [2] objek bisnis dikunci sebelum wallet
	if err != nil {
		return Intent{}, db.MapError(err)
	}
	if err := testhook.Pause(ctx, "payment.after_intent_lock"); err != nil {
		return Intent{}, err
	}
	switch {
	case pi.Status == StatusExpired:
		return Intent{}, apperr.ErrPaymentExpired
	case pi.Status != StatusRequiresConfirmation: // sudah dibayar (tab lain) atau dibatalkan
		return Intent{}, apperr.ErrInvalidState
	case pi.Expired: // lewat expires_at tetapi sweeper belum sempat menandai
		return Intent{}, apperr.ErrPaymentExpired
	}
	payer, err := s.ledger.WalletOf(ctx, tx, payerID)
	if err != nil {
		return Intent{}, err
	}
	journalID := s.ids.New()
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{ // [3]–[5] lock wallet, cek saldo, journal
		ID: journalID, Purpose: ledger.PurposePayment, SourceType: "payment_intent", SourceID: pi.ID,
		Currency: pi.Currency, CorrelationID: correlationID, Actor: ledger.Actor{Type: "USER", ID: &payerID},
		Entries: []ledger.Entry{
			{AccountID: payer.AccountID, Direction: ledger.Debit, Amount: pi.Amount},       // utang ke user berkurang
			{AccountID: pi.MerchantAccountID, Direction: ledger.Credit, Amount: pi.Amount}, // utang ke merchant bertambah
		},
	}); err != nil {
		return Intent{}, err
	}
	n, err := q.MarkIntentSucceeded(ctx, store.MarkIntentSucceededParams{
		PayerUserID: &payerID, PayerAccountID: &payer.AccountID, JournalID: &journalID, ID: pi.ID,
	})
	if err != nil {
		return Intent{}, db.MapError(err)
	}
	if n != 1 { // mustahil selama baris terkunci; kalau terjadi, batalkan semuanya
		return Intent{}, apperr.ErrInvalidState
	}
	// Sprint 6: INSERT webhook_deliveries + job webhook.deliver di tx yang sama (outbox).
	return s.load(ctx, tx, pi.ID)
}

// ExpireDue menandai intent kedaluwarsa (sweeper; dijadwalkan sebagai job di Sprint 6).
func ExpireDue(ctx context.Context, q db.DBTX, batch int32) ([]uuid.UUID, error) {
	ids, err := store.New(q).ExpireDueIntents(ctx, batch)
	return ids, db.MapError(err)
}

func (s *Service) load(ctx context.Context, q db.DBTX, id uuid.UUID) (Intent, error) {
	r, err := store.New(q).GetIntent(ctx, id)
	if err != nil {
		return Intent{}, db.MapError(err)
	}
	in := Intent{
		ID: r.ID, MerchantID: r.MerchantID, MerchantName: r.MerchantName, MerchantOrderID: r.MerchantOrderID,
		Amount: r.Amount, Currency: r.Currency, RefundedAmount: r.RefundedAmount, Status: r.Status,
		Description: r.Description, PayerUserID: r.PayerUserID, ExpiresAt: r.ExpiresAt, ConfirmedAt: r.ConfirmedAt, CreatedAt: r.CreatedAt,
	}
	if r.Overdue != nil && *r.Overdue {
		in.Status = StatusExpired
	}
	if r.PayerUserID != nil {
		if in.PayerName, err = s.users.DisplayName(ctx, q, *r.PayerUserID); err != nil {
			return Intent{}, err
		}
	}
	return in, nil
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
