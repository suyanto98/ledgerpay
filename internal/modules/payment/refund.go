package payment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payment/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/testhook"
)

// Refund adalah pengembalian dana (sebagian/penuh) atas intent yang sudah dibayar (LF-16).
// Tidak punya state machine di V1: refund sinkron, baris ada = berhasil.
type Refund struct {
	ID              uuid.UUID
	PaymentIntentID uuid.UUID
	Amount          int64
	Currency        string
	Reason          *string
	JournalID       uuid.UUID
	CreatedAt       time.Time
}

// RefundCommand adalah refund yang sudah lolos validasi format.
type RefundCommand struct {
	MerchantID    uuid.UUID
	IntentID      uuid.UUID
	Amount        int64
	Reason        *string
	IdempotencyID uuid.UUID
	CorrelationID string
}

// RefundIntent mengembalikan dana ke payer, di dalam tx idempotency merchant.
//
// Urutan lock (docs/design/13-api-endpoints.md §9, LI-08): key → intent FOR UPDATE → wallet
// (ORDER BY id, di PostJournal). Dua refund paralel atas intent yang sama diserialkan oleh lock
// intent: yang kedua membaca refunded_amount SETELAH yang pertama commit, jadi Σ refund tidak bisa
// melebihi amount walaupun masing-masing "muat" kalau dilihat sendiri-sendiri.
func (s *Service) RefundIntent(ctx context.Context, tx pgx.Tx, c RefundCommand) (Refund, Intent, error) {
	q := store.New(tx)
	pi, err := q.LockPaidIntent(ctx, c.IntentID)
	if err != nil {
		return Refund{}, Intent{}, db.MapError(err)
	}
	if pi.MerchantID != c.MerchantID { // BOLA: intent merchant lain tidak "ada"
		return Refund{}, Intent{}, apperr.ErrNotFound
	}
	if err := testhook.Pause(ctx, "refund.after_intent_lock"); err != nil {
		return Refund{}, Intent{}, err
	}
	if pi.Status != StatusSucceeded && pi.Status != "PARTIALLY_REFUNDED" { // belum dibayar / sudah REFUNDED
		return Refund{}, Intent{}, apperr.ErrInvalidState
	}
	refundable := pi.Amount - pi.RefundedAmount
	if c.Amount > refundable {
		return Refund{}, Intent{}, apperr.ErrRefundExceedsPayment.WithRefundable(ledger.FormatAmount(refundable))
	}
	id, journalID := s.ids.New(), s.ids.New()
	merchant := c.MerchantID
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{ // lock wallet merchant + payer, cek saldo merchant
		ID: journalID, Purpose: ledger.PurposeRefund, SourceType: "refund", SourceID: id, Currency: pi.Currency,
		CorrelationID: c.CorrelationID, Actor: ledger.Actor{Type: "MERCHANT", ID: &merchant},
		Entries: []ledger.Entry{
			{AccountID: pi.MerchantAccountID, Direction: ledger.Debit, Amount: c.Amount}, // saldo merchant turun
			{AccountID: *pi.PayerAccountID, Direction: ledger.Credit, Amount: c.Amount},  // saldo payer naik
		},
	}); err != nil {
		return Refund{}, Intent{}, err
	}
	n, err := q.ApplyRefund(ctx, store.ApplyRefundParams{ID: pi.ID, Amount: c.Amount})
	if err != nil {
		return Refund{}, Intent{}, db.MapError(err)
	}
	if n != 1 { // mustahil selama baris terkunci dan guard di atas benar; batalkan semuanya
		return Refund{}, Intent{}, apperr.ErrInvalidState
	}
	idem := c.IdempotencyID
	createdAt, err := q.InsertRefund(ctx, store.InsertRefundParams{
		ID: id, PaymentIntentID: pi.ID, MerchantID: pi.MerchantID, Amount: c.Amount, Currency: pi.Currency,
		Reason: c.Reason, JournalID: journalID, IdempotencyKeyID: &idem,
	})
	if err != nil {
		return Refund{}, Intent{}, db.MapError(err)
	}
	in, err := s.load(ctx, tx, pi.ID)
	if err != nil {
		return Refund{}, Intent{}, err
	}
	rf := Refund{ID: id, PaymentIntentID: pi.ID, Amount: c.Amount, Currency: pi.Currency, Reason: c.Reason, JournalID: journalID, CreatedAt: createdAt}
	data := eventData(in)
	data["refund"] = map[string]any{"id": rf.ID, "amount": ledger.FormatAmount(rf.Amount), "currency": rf.Currency, "reason": rf.Reason}
	if err := s.events.Publish(ctx, tx, in.MerchantID, "payment.refunded", data); err != nil { // outbox, tx yang sama
		return Refund{}, Intent{}, err
	}
	return rf, in, nil
}

// Refunds mengembalikan daftar refund sebuah intent (urut waktu).
func (s *Service) Refunds(ctx context.Context, q db.DBTX, intentID uuid.UUID) ([]Refund, error) {
	rows, err := store.New(q).ListRefunds(ctx, intentID)
	if err != nil {
		return nil, db.MapError(err)
	}
	out := make([]Refund, 0, len(rows))
	for _, r := range rows {
		out = append(out, Refund{ID: r.ID, PaymentIntentID: r.PaymentIntentID, Amount: r.Amount, Currency: r.Currency,
			Reason: r.Reason, JournalID: r.JournalID, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// RefundDrift (dipanggil job ledger.verify): intent yang refunded_amount-nya ≠ Σ refunds (LI-08).
// Normalnya kosong; isi = bug (refund tercatat tanpa update intent, atau sebaliknya).
func RefundDrift(ctx context.Context, q db.DBTX) ([]uuid.UUID, error) {
	ids, err := store.New(q).RefundDrift(ctx)
	return ids, db.MapError(err)
}
