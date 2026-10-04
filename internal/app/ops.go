package app

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/merchant"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/crypto"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
)

// DemoCredit mengisi saldo wallet lewat journal TOPUP_CREDIT tanpa provider. HANYA untuk dev/demo
// (dipanggil `ledgerpay demo-credit`, yang menolak berjalan di luar APP_ENV=dev/test). Alur top-up
// yang sebenarnya (provider + webhook) datang di Sprint 5.
func DemoCredit(ctx context.Context, pool *pgxpool.Pool, ids idgen.Generator, email string, amount int64) error {
	user, err := identity.NewDirectory().ActiveUserByEmail(ctx, pool, email)
	if errors.Is(err, apperr.ErrNotFound) {
		return errors.New("user not found or inactive: " + email)
	}
	if err != nil {
		return err
	}
	svc := ledger.NewService(ids)
	w, err := svc.WalletOf(ctx, pool, user.ID)
	if err != nil {
		return err
	}
	prov, err := svc.SystemAccountID(ctx, pool, "PROVIDER_CLEARING")
	if err != nil {
		return err
	}
	return db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return svc.PostJournal(ctx, tx, ledger.Journal{
			ID: ids.New(), Purpose: ledger.PurposeTopupCredit, SourceType: "topup", SourceID: uuid.Must(uuid.NewV7()),
			Currency: ledger.SupportedCurrency, CorrelationID: "demo-credit", Actor: ledger.Actor{Type: "SYSTEM"},
			Description: "demo credit (dev only)",
			Entries: []ledger.Entry{
				{AccountID: prov, Direction: ledger.Debit, Amount: amount},
				{AccountID: w.AccountID, Direction: ledger.Credit, Amount: amount},
			},
		})
	})
}

// CreateMerchant mendaftarkan merchant + wallet-nya (onboarding lewat seed/ops, LF-09 AC1).
func CreateMerchant(ctx context.Context, pool *pgxpool.Pool, ids idgen.Generator, name string) (uuid.UUID, error) {
	return merchant.NewService(pool, ledger.NewService(ids), ids, clock.System{}, nil).Create(ctx, name)
}

// SetMerchantWebhook mendaftarkan URL webhook merchant dan mengembalikan secret baru (tampil SEKALI).
func SetMerchantWebhook(ctx context.Context, pool *pgxpool.Pool, kr *crypto.Keyring, merchantID uuid.UUID, url string, requireHTTPS bool) (string, error) {
	return merchant.NewService(pool, ledger.NewService(idgen.V7{}), idgen.V7{}, clock.System{}, kr).SetWebhook(ctx, merchantID, url, requireHTTPS)
}

// GrantOps memberi role OPS. Hanya lewat CLI (akses shell ke server = akses admin); tidak ada
// endpoint, sehingga bug atau token bocor di API tidak bisa dipakai untuk menaikkan hak.
func GrantOps(ctx context.Context, pool *pgxpool.Pool, email string) error {
	return identity.GrantOps(ctx, pool, email)
}
