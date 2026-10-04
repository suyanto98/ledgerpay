// Package topup: isi saldo lewat payment provider (LF-07).
//
// Dua jalur yang sengaja dipisah:
//  1. POST /topups HANYA membuat sesi pembayaran (pola recovery point, ADR-L02 b). Endpoint ini
//     tidak pernah menambah saldo.
//  2. Saldo bertambah HANYA lewat webhook provider yang sudah diverifikasi (HMAC + verify-by-fetch),
//     diproses worker inbox dengan transisi monoton (ADR-L08).
package topup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/topup/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/observability"
)

// Batas kontrak API §6: Rp10.000 – Rp10.000.000.
const (
	MinAmount int64 = 1_000_000
	MaxAmount int64 = 1_000_000_000
)

// Status top-up dan recovery point.
const (
	StatusPending   = "PENDING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusExpired   = "EXPIRED"

	RecoveryCreated         = "CREATED"
	RecoveryProviderCreated = "PROVIDER_CREATED"
)

// Config: URL yang dikirim ke provider.
type Config struct {
	CallbackURL   string // webhook LedgerPay, contoh http://api:8080/api/v1/webhooks/provider
	ReturnBaseURL string // SPA: provider me-redirect user ke <ReturnBaseURL>/topups/<id>
}

type Service struct {
	ledger   *ledger.Service
	provider Provider
	ids      idgen.Generator
	cfg      Config
}

func NewService(l *ledger.Service, p Provider, ids idgen.Generator, cfg Config) *Service {
	return &Service{ledger: l, provider: p, ids: ids, cfg: cfg}
}

// Topup adalah top-up dari sudut pandang pemiliknya.
type Topup struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Amount        int64
	Currency      string
	Status        string
	RecoveryPoint string
	CheckoutURL   *string
	FailureReason *string
	ExpiresAt     time.Time
	SucceededAt   *time.Time
	CreatedAt     time.Time
}

// CreatePending adalah isi tx1: top-up PENDING dengan recovery_point CREATED. Wallet frozen → 422
// (disimpan idempotency seperti penolakan domain lain).
func (s *Service) CreatePending(ctx context.Context, tx pgx.Tx, userID uuid.UUID, amount int64, currency string) (uuid.UUID, error) {
	w, err := s.ledger.WalletOf(ctx, tx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	if w.Status != "ACTIVE" {
		return uuid.Nil, apperr.ErrAccountFrozen
	}
	id := s.ids.New()
	if _, err := store.New(tx).InsertTopup(ctx, store.InsertTopupParams{ID: id, UserID: userID, AccountID: w.AccountID, Amount: amount, Currency: currency}); err != nil {
		return uuid.Nil, db.MapError(err)
	}
	return id, nil
}

// ErrProviderPending: charge belum berhasil dibuat di provider (timeout/5xx). Top-up tetap PENDING
// di recovery point CREATED; retry client dengan key yang sama (atau job topup.recover) melanjutkan.
var ErrProviderPending = errors.New("topup: provider charge not created yet")

// Pool: pool koneksi (bisa membuka transaksi dan menjalankan query).
type Pool interface {
	db.Beginner
	db.DBTX
}

// EnsureCharge melanjutkan top-up dari recovery point-nya. Panggilan provider di LUAR transaksi;
// lalu tx2 mencatat provider_ref (recovery_point → PROVIDER_CREATED) DAN menyimpan respons
// idempotency (complete) secara atomik: "respons tersimpan" ⇔ "top-up sudah di recovery point akhir".
func (s *Service) EnsureCharge(ctx context.Context, pool Pool, id uuid.UUID, complete func(ctx context.Context, tx pgx.Tx, t Topup) error) (Topup, error) {
	t, err := s.Get(ctx, pool, id)
	if err != nil {
		return Topup{}, err
	}
	var ch *Charge
	if t.RecoveryPoint == RecoveryCreated {
		// Aman diulang: idempotency key ke provider = topup id, jadi provider mengembalikan charge
		// yang SAMA kalau panggilan sebelumnya sebenarnya sudah diproses (timeout_after_process).
		// Syaratnya, body harus IDENTIK di setiap retry: semua field diturunkan dari baris top-up
		// (termasuk correlation_id = topup id), bukan dari request saat ini. Request ID yang berubah
		// di setiap retry membuat provider menjawab 422 idempotency_mismatch (temuan Sprint 5).
		c, err := s.provider.CreateCharge(ctx, CreateChargeInput{
			TopupID: t.ID.String(), Amount: t.Amount, Currency: t.Currency, CallbackURL: s.cfg.CallbackURL,
			ReturnURL: s.cfg.ReturnBaseURL + "/topups/" + t.ID.String(), ExpiresAt: t.ExpiresAt, CorrelationID: t.ID.String(),
		})
		if err != nil {
			observability.Logger(ctx).Warn("topup.provider_create_failed", "topup_id", t.ID.String(), "err", err.Error())
			return t, ErrProviderPending
		}
		ch = &c
	}
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if ch != nil {
			if _, err := store.New(tx).MarkProviderCreated(ctx, store.MarkProviderCreatedParams{ID: t.ID, ProviderRef: &ch.ID, CheckoutUrl: &ch.CheckoutURL}); err != nil {
				return err
			}
		}
		var err error
		if t, err = s.Get(ctx, tx, id); err != nil {
			return err
		}
		return complete(ctx, tx, t)
	})
	return t, db.MapError(err)
}

// Get mengembalikan top-up (tanpa cek pemilik; pakai ForUser untuk endpoint).
func (s *Service) Get(ctx context.Context, q db.DBTX, id uuid.UUID) (Topup, error) {
	r, err := store.New(q).GetTopup(ctx, id)
	if err != nil {
		return Topup{}, db.MapError(err)
	}
	return Topup{ID: r.ID, UserID: r.UserID, Amount: r.Amount, Currency: r.Currency, Status: r.Status, RecoveryPoint: r.RecoveryPoint,
		CheckoutURL: r.CheckoutUrl, FailureReason: r.FailureReason, ExpiresAt: r.ExpiresAt, SucceededAt: r.SucceededAt, CreatedAt: r.CreatedAt}, nil
}

// ForUser: top-up milik orang lain → 404 (BOLA).
func (s *Service) ForUser(ctx context.Context, q db.DBTX, userID, id uuid.UUID) (Topup, error) {
	t, err := s.Get(ctx, q, id)
	if err != nil {
		return Topup{}, err
	}
	if t.UserID != userID {
		return Topup{}, apperr.ErrNotFound
	}
	return t, nil
}

// ---------------------------------------------------------------- event provider (dipakai worker inbox)

type chargeEvent struct {
	Data struct {
		ChargeID      string  `json:"charge_id"`
		Reference     string  `json:"reference"`
		Amount        string  `json:"amount"`
		Currency      string  `json:"currency"`
		FailureReason *string `json:"failure_reason"`
	} `json:"data"`
}

func parseEvent(payload []byte) (chargeEvent, int64, error) {
	var ev chargeEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return ev, 0, err
	}
	amount, err := strconv.ParseInt(ev.Data.Amount, 10, 64)
	return ev, amount, err
}

// VerifyEvent (di LUAR transaksi) — verify-by-fetch, amandemen ADR-L08: sebelum kredit, tanyakan
// langsung ke provider. Kalau secret webhook bocor, webhook palsu bertanda tangan sah tetap tidak
// bisa menciptakan saldo, karena provider tidak mengenal pembayaran itu.
//
// Mengembalikan err → coba lagi nanti (provider down / belum konsisten); dead != "" → DEAD + alert.
func (s *Service) VerifyEvent(ctx context.Context, eventType string, payload []byte) (dead string, err error) {
	if eventType != "charge.succeeded" {
		return "", nil // hanya event yang menambah saldo yang perlu diverifikasi
	}
	ev, amount, err := parseEvent(payload)
	if err != nil {
		return "malformed payload: " + err.Error(), nil
	}
	ch, err := s.provider.GetCharge(ctx, ev.Data.ChargeID)
	if errors.Is(err, ErrChargeNotFound) {
		return "verify-by-fetch: provider does not know charge " + ev.Data.ChargeID, nil
	}
	if err != nil {
		return "", err
	}
	if ch.Amount != amount || ch.Currency != ev.Data.Currency || ch.Reference != ev.Data.Reference {
		return fmt.Sprintf("verify-by-fetch: event (%d %s ref %s) differs from provider (%d %s ref %s)",
			amount, ev.Data.Currency, ev.Data.Reference, ch.Amount, ch.Currency, ch.Reference), nil
	}
	if ch.Status != StatusSucceeded {
		return "", fmt.Errorf("verify-by-fetch: provider says %s, not SUCCEEDED yet", ch.Status)
	}
	return "", nil
}

// ApplyEvent (di DALAM tx worker, setelah baris inbox di-fence) menerapkan transisi monoton:
//
//	status top-up │ charge.succeeded        │ charge.failed    │ charge.pending
//	PENDING       │ SUCCEEDED + journal     │ FAILED           │ STALE (no-op)
//	EXPIRED       │ SUCCEEDED + journal (*) │ FAILED           │ STALE
//	SUCCEEDED     │ DUPLICATE (no-op)       │ CONFLICT (tidak dibalik) │ STALE
//	FAILED        │ CONFLICT (tidak dibalik)│ DUPLICATE        │ STALE
//
// (*) EXPIRED adalah status lunak: provider sumber kebenaran apakah uang dibayar.
// Urutan kedatangan event tidak memengaruhi hasil akhir.
func (s *Service) ApplyEvent(ctx context.Context, tx pgx.Tx, eventType, eventID string, payload []byte) (status, outcome, dead string, err error) {
	if eventType == "charge.pending" {
		return "IGNORED", "STALE", "", nil
	}
	ev, amount, perr := parseEvent(payload)
	if perr != nil {
		return "", "", "malformed payload: " + perr.Error(), nil
	}
	q := store.New(tx)
	row, err := q.LockTopupByProviderRef(ctx, &ev.Data.ChargeID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Charge sudah dibuat di provider tetapi tx2 belum sempat mencatat provider_ref (crash di antara
		// keduanya): cari lewat reference = topup id, lalu catat provider_ref-nya sekarang.
		id, perr := uuid.Parse(ev.Data.Reference)
		if perr != nil {
			return "", "", "unknown reference " + ev.Data.Reference, nil
		}
		var r store.LockTopupByIDRow
		r, err = q.LockTopupByID(ctx, id)
		row = store.LockTopupByProviderRefRow(r)
		if err == nil && row.ProviderRef == nil {
			if _, err = q.MarkProviderCreated(ctx, store.MarkProviderCreatedParams{ID: row.ID, ProviderRef: &ev.Data.ChargeID}); err != nil {
				return "", "", "", db.MapError(err)
			}
		} else if err == nil {
			return "", "", "charge " + ev.Data.ChargeID + " does not match topup provider_ref", nil
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "unknown topup for charge " + ev.Data.ChargeID, nil
	}
	if err != nil {
		return "", "", "", db.MapError(err)
	}
	if row.Amount != amount || row.Currency != ev.Data.Currency {
		return "", "", fmt.Sprintf("amount/currency mismatch: event %d %s, topup %d %s", amount, ev.Data.Currency, row.Amount, row.Currency), nil
	}

	switch eventType {
	case "charge.succeeded":
		switch row.Status {
		case StatusSucceeded:
			return "IGNORED", "DUPLICATE", "", nil
		case StatusFailed:
			observability.Logger(ctx).Error("topup.webhook_conflict", "topup_id", row.ID.String(), "status", row.Status, "event", eventType)
			return "PROCESSED", "CONFLICT", "", nil // Sprint 9: + recon_items di tx yang sama
		}
		if err := s.credit(ctx, tx, row.ID, row.AccountID, row.Amount, row.Currency, eventID); err != nil {
			return "", "", "", err
		}
		if row.Status == StatusExpired {
			observability.Logger(ctx).Warn("topup.webhook_late", "topup_id", row.ID.String()) // metrik webhook_late_total (Sprint 7)
		}
		return "PROCESSED", "APPLIED", "", nil
	case "charge.failed":
		switch row.Status {
		case StatusFailed:
			return "IGNORED", "DUPLICATE", "", nil
		case StatusSucceeded:
			observability.Logger(ctx).Error("topup.webhook_conflict", "topup_id", row.ID.String(), "status", row.Status, "event", eventType)
			return "PROCESSED", "CONFLICT", "", nil
		}
		if _, err := q.MarkTopupFailed(ctx, store.MarkTopupFailedParams{ID: row.ID, FailureReason: ev.Data.FailureReason}); err != nil {
			return "", "", "", db.MapError(err)
		}
		return "PROCESSED", "APPLIED", "", nil
	}
	return "IGNORED", "STALE", "", nil
}

// credit: journal TOPUP_CREDIT (DR PROVIDER_CLEARING / CR wallet) + top-up SUCCEEDED, satu tx.
// Lapis anti-dobel: fencing inbox, guard status top-up, dan UNIQUE(source_type, source_id, purpose).
func (s *Service) credit(ctx context.Context, tx pgx.Tx, topupID, walletID uuid.UUID, amount int64, currency, eventID string) error {
	prov, err := s.ledger.SystemAccountID(ctx, tx, "PROVIDER_CLEARING")
	if err != nil {
		return err
	}
	journalID := s.ids.New()
	if err := s.ledger.PostJournal(ctx, tx, ledger.Journal{
		ID: journalID, Purpose: ledger.PurposeTopupCredit, SourceType: "topup", SourceID: topupID, Currency: currency,
		CorrelationID: eventID, Actor: ledger.Actor{Type: "SYSTEM"},
		Entries: []ledger.Entry{
			{AccountID: prov, Direction: ledger.Debit, Amount: amount},      // provider berutang ke kita
			{AccountID: walletID, Direction: ledger.Credit, Amount: amount}, // kita berutang ke user
		},
	}); err != nil {
		return err
	}
	n, err := store.New(tx).MarkTopupSucceeded(ctx, store.MarkTopupSucceededParams{ID: topupID, JournalID: &journalID})
	if err != nil {
		return db.MapError(err)
	}
	if n != 1 {
		return errors.New("topup: guard rejected SUCCEEDED transition") // rollback journal juga
	}
	return nil
}

// ---------------------------------------------------------------- sweeper (dijadwalkan job runner)

// ExpireSweep (job topup.expire_sweep): top-up PENDING yang lewat waktu. Provider ditanya DULU:
// kalau ternyata sudah dibayar (webhook hilang/terlambat), kredit diterapkan lewat jalur yang sama
// dengan inbox; hanya yang benar-benar belum dibayar yang menjadi EXPIRED.
func (s *Service) ExpireSweep(ctx context.Context, pool Pool, batch int32) (expired, credited int, err error) {
	rows, err := store.New(pool).ListExpiredPending(ctx, batch)
	if err != nil {
		return 0, 0, db.MapError(err)
	}
	for _, r := range rows {
		paid := false
		if r.ProviderRef != nil {
			ch, err := s.provider.GetCharge(ctx, *r.ProviderRef) // di luar tx
			if err != nil && !errors.Is(err, ErrChargeNotFound) {
				return expired, credited, err // provider down: sweep berikutnya mencoba lagi
			}
			paid = err == nil && ch.Status == StatusSucceeded
		}
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			t, err := q.LockTopupByID(ctx, r.ID)
			if err != nil {
				return err
			}
			if t.Status != StatusPending { // webhook sudah memproses di sela-sela: no-op
				return nil
			}
			if paid {
				credited++
				observability.Logger(ctx).Warn("topup.credited_by_sweeper", "topup_id", t.ID.String())
				return s.credit(ctx, tx, t.ID, t.AccountID, t.Amount, t.Currency, "sweeper:"+t.ID.String())
			}
			expired++
			_, err = q.MarkTopupExpired(ctx, t.ID)
			return err
		})
		if err != nil {
			return expired, credited, db.MapError(err)
		}
	}
	return expired, credited, nil
}

// Recover (job topup.recover): melanjutkan top-up yang macet di recovery point CREATED tanpa
// menunggu client. Respons idempotency sengaja TIDAK ditulis di sini: kalau client kelak retry
// dengan key yang sama, EnsureCharge melihat PROVIDER_CREATED, melewati provider, lalu menyimpan respons.
func (s *Service) Recover(ctx context.Context, pool Pool, batch int32) (int, error) {
	ids, err := store.New(pool).ListStuckCreated(ctx, batch)
	if err != nil {
		return 0, db.MapError(err)
	}
	done := 0
	for _, id := range ids {
		_, err := s.EnsureCharge(ctx, pool, id, func(context.Context, pgx.Tx, Topup) error { return nil })
		if errors.Is(err, ErrProviderPending) {
			continue // provider masih bermasalah: sweep berikutnya
		}
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
