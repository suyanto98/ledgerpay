// Package transfer: transfer P2P antar wallet (LF-04). Uang hanya berpindah lewat
// ledger.PostJournal (ADR-L01); modul ini memiliki tabel transfers dan menerjemahkan niat user
// ("kirim Rp50.000 ke andi@example.com") menjadi satu journal TRANSFER yang seimbang.
//
// Concurrency (ADR-L03): PostJournal mengunci kedua wallet ORDER BY id FOR NO KEY UPDATE, lalu
// memeriksa saldo SETELAH lock. Dua transfer dari wallet yang sama mengantre, bukan saling menimpa.
package transfer

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/transfer/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
)

// Batas jumlah per transfer (kontrak API §5): Rp1 – Rp10.000.000, dalam minor unit (sen).
const (
	MinAmount int64 = 100
	MaxAmount int64 = 1_000_000_000
)

// Recipient adalah pihak lain dalam transfer.
type Recipient struct {
	ID          uuid.UUID
	DisplayName string
}

// Users adalah apa yang dibutuhkan transfer dari modul identity. Implementasinya disuntikkan
// composition root (adapter di internal/app), jadi transfer tidak meng-import identity (ADR-001).
type Users interface {
	// RecipientByEmail mengembalikan apperr.ErrNotFound untuk email tak terdaftar ATAU user nonaktif.
	RecipientByEmail(ctx context.Context, q db.DBTX, email string) (Recipient, error)
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

// Command adalah transfer yang sudah lolos validasi format (handler).
type Command struct {
	SenderID         uuid.UUID
	RecipientEmail   string
	Amount           int64
	Currency         string
	Note             *string
	CorrelationID    string
	IdempotencyKeyID *uuid.UUID
}

// Transfer adalah transfer yang sudah terjadi, dilihat dari sudut pandang satu pihak.
type Transfer struct {
	ID              uuid.UUID
	SenderUserID    uuid.UUID
	ReceiverUserID  uuid.UUID
	Amount          int64
	Currency        string
	Note            *string
	Status          string
	JournalID       uuid.UUID
	CreatedAt       time.Time
	CounterpartName string // nama pihak lawan (penerima bagi pengirim, pengirim bagi penerima)
}

// Create memindahkan uang di dalam tx pemanggil (tx milik idempotency.Execute). Urutan lock
// mengikuti docs/design/19-tx-concurrency.md §0: idempotency key → wallet (di PostJournal) →
// INSERT journal/entries/transfers.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, c Command) (Transfer, error) {
	// Resolve penerima tanpa lock. Tidak terdaftar / nonaktif / wallet frozen → satu error generik
	// yang sama, supaya endpoint ini tidak bisa dipakai menebak email mana yang terdaftar.
	rcpt, err := s.users.RecipientByEmail(ctx, tx, c.RecipientEmail)
	if errors.Is(err, apperr.ErrNotFound) {
		return Transfer{}, apperr.ErrInvalidRecipient
	}
	if err != nil {
		return Transfer{}, err
	}
	if rcpt.ID == c.SenderID {
		return Transfer{}, apperr.ErrSelfTransfer
	}
	from, err := s.ledger.WalletOf(ctx, tx, c.SenderID)
	if err != nil {
		return Transfer{}, err
	}
	to, err := s.ledger.WalletOf(ctx, tx, rcpt.ID)
	if errors.Is(err, apperr.ErrNotFound) || (err == nil && to.Status != "ACTIVE") {
		return Transfer{}, apperr.ErrInvalidRecipient
	}
	if err != nil {
		return Transfer{}, err
	}

	// PostJournal: lock kedua wallet berurutan, cek status + saldo SETELAH lock, tulis journal.
	transferID, journalID := s.ids.New(), s.ids.New()
	sender := c.SenderID
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{
		ID: journalID, Purpose: ledger.PurposeTransfer, SourceType: "transfer", SourceID: transferID,
		Currency: c.Currency, CorrelationID: c.CorrelationID, Actor: ledger.Actor{Type: "USER", ID: &sender},
		Entries: []ledger.Entry{
			{AccountID: from.AccountID, Direction: ledger.Debit, Amount: c.Amount}, // utang ke pengirim berkurang
			{AccountID: to.AccountID, Direction: ledger.Credit, Amount: c.Amount},  // utang ke penerima bertambah
		},
	}); err != nil {
		return Transfer{}, err
	}

	createdAt, err := store.New(tx).InsertTransfer(ctx, store.InsertTransferParams{
		ID: transferID, SenderUserID: c.SenderID, ReceiverUserID: rcpt.ID,
		SenderAccountID: from.AccountID, ReceiverAccountID: to.AccountID,
		Amount: c.Amount, Currency: c.Currency, Note: c.Note, JournalID: journalID, IdempotencyKeyID: c.IdempotencyKeyID,
	})
	if err != nil {
		return Transfer{}, db.MapError(err)
	}
	return Transfer{
		ID: transferID, SenderUserID: c.SenderID, ReceiverUserID: rcpt.ID, Amount: c.Amount, Currency: c.Currency,
		Note: c.Note, Status: "COMPLETED", JournalID: journalID, CreatedAt: createdAt, CounterpartName: rcpt.DisplayName,
	}, nil
}

// Get mengembalikan transfer bila viewer adalah pengirim atau penerimanya; selain itu 404 (BOLA).
func (s *Service) Get(ctx context.Context, q db.DBTX, viewer, id uuid.UUID) (Transfer, error) {
	row, err := store.New(q).GetTransferForParty(ctx, store.GetTransferForPartyParams{ID: id, UserID: viewer})
	if err != nil {
		return Transfer{}, db.MapError(err)
	}
	other := row.ReceiverUserID
	if viewer == row.ReceiverUserID {
		other = row.SenderUserID
	}
	name, err := s.users.DisplayName(ctx, q, other)
	if err != nil {
		return Transfer{}, err
	}
	return Transfer{
		ID: row.ID, SenderUserID: row.SenderUserID, ReceiverUserID: row.ReceiverUserID, Amount: row.Amount,
		Currency: row.Currency, Note: row.Note, Status: row.Status, JournalID: row.JournalID, CreatedAt: row.CreatedAt,
		CounterpartName: name,
	}, nil
}

// Counterpart adalah pihak lawan satu transfer dari sudut pandang viewer (untuk riwayat wallet).
type Counterpart struct {
	UserID uuid.UUID
	Note   *string
}

// Counterparts mengembalikan pihak lawan untuk banyak transfer sekaligus (satu query per halaman riwayat).
// Transfer yang bukan milik viewer tidak ikut (BOLA), sama dengan Get.
func (s *Service) Counterparts(ctx context.Context, q db.DBTX, viewer uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]Counterpart, error) {
	out := make(map[uuid.UUID]Counterpart, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := store.New(q).ListTransfersForParty(ctx, store.ListTransfersForPartyParams{Ids: ids, UserID: viewer})
	if err != nil {
		return nil, db.MapError(err)
	}
	for _, r := range rows {
		other := r.ReceiverUserID
		if viewer == r.ReceiverUserID {
			other = r.SenderUserID
		}
		out[r.ID] = Counterpart{UserID: other, Note: r.Note}
	}
	return out, nil
}
