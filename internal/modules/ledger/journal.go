package ledger

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Direction adalah sisi entry.
type Direction string

const (
	Debit  Direction = "D"
	Credit Direction = "C"
)

// Entry adalah satu baris journal. Amount selalu positif; arah dari Direction.
type Entry struct {
	AccountID uuid.UUID
	Direction Direction
	Amount    int64
}

// Actor adalah siapa yang memicu journal (LI-14).
type Actor struct {
	Type string // USER, MERCHANT, OPS, SYSTEM
	ID   *uuid.UUID
}

// Journal adalah satu kejadian keuangan yang seimbang (aggregate, ADR-L01).
type Journal struct {
	ID            uuid.UUID
	Purpose       Purpose
	SourceType    string
	SourceID      uuid.UUID
	Currency      string
	ReversalOf    *uuid.UUID
	CorrelationID string
	Actor         Actor
	Description   string
	Entries       []Entry
}

var (
	ErrUnbalanced      = errors.New("ledger: journal is not balanced (sum debit != sum credit)")
	ErrTooFewEntries   = errors.New("ledger: journal needs at least 2 entries")
	ErrUnknownPurpose  = errors.New("ledger: unknown journal purpose")
	ErrSourceMismatch  = errors.New("ledger: source_type does not match purpose")
	ErrReversalInvalid = errors.New("ledger: reversal not allowed for this purpose")
	ErrBadDirection    = errors.New("ledger: entry direction must be D or C")
	ErrMissingActor    = errors.New("ledger: invalid actor")
	ErrManualReason    = errors.New("ledger: ADJUSTMENT_MANUAL requires OPS actor and description")
)

// Validate memeriksa aturan struktural yang tidak butuh database. Aturan yang butuh data akun
// (jenis akun vs template, saldo cukup, akun aktif) diperiksa PostJournal.
// Semua aturan ini juga dijaga DB (CHECK, composite FK, deferred trigger) sebagai jaring terakhir.
func (j Journal) Validate() error {
	t, ok := templates[j.Purpose]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownPurpose, j.Purpose)
	}
	if j.SourceType != t.SourceType {
		return fmt.Errorf("%w: purpose %s needs %q, got %q", ErrSourceMismatch, j.Purpose, t.SourceType, j.SourceType)
	}
	if j.ReversalOf != nil && !t.Reversal {
		return ErrReversalInvalid
	}
	if j.Currency != SupportedCurrency {
		return ErrUnsupportedCurrency
	}
	if j.Actor.Type != "USER" && j.Actor.Type != "MERCHANT" && j.Actor.Type != "OPS" && j.Actor.Type != "SYSTEM" {
		return ErrMissingActor
	}
	if (j.Actor.Type == "SYSTEM") != (j.Actor.ID == nil) {
		return ErrMissingActor
	}
	if j.Purpose == PurposeAdjustmentManual && (j.Actor.Type != "OPS" || j.Description == "") {
		return ErrManualReason
	}
	if len(j.Entries) < 2 {
		return ErrTooFewEntries
	}
	var debit, credit int64
	var err error
	for _, e := range j.Entries {
		if e.Amount <= 0 {
			return ErrInvalidAmount
		}
		switch e.Direction {
		case Debit:
			debit, err = addChecked(debit, e.Amount)
		case Credit:
			credit, err = addChecked(credit, e.Amount)
		default:
			return ErrBadDirection
		}
		if err != nil {
			return err
		}
	}
	if debit != credit {
		return fmt.Errorf("%w: debit=%d credit=%d", ErrUnbalanced, debit, credit)
	}
	return nil
}
