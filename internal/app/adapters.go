package app

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/merchant"
	"ledgerpay/internal/modules/ops"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/modules/webhookout"
	"ledgerpay/internal/platform/db"
)

// usersAdapter menghubungkan identity.Directory ke interface transfer.Users. Adapter tinggal di
// composition root supaya transfer dan identity tidak saling mengenal tipe masing-masing (ADR-001).
type usersAdapter struct{ dir identity.Directory }

func (a usersAdapter) RecipientByEmail(ctx context.Context, q db.DBTX, email string) (transfer.Recipient, error) {
	p, err := a.dir.ActiveUserByEmail(ctx, q, email)
	if err != nil {
		return transfer.Recipient{}, err
	}
	return transfer.Recipient{ID: p.ID, DisplayName: p.DisplayName}, nil
}

func (a usersAdapter) DisplayName(ctx context.Context, q db.DBTX, id uuid.UUID) (string, error) {
	return a.dir.DisplayName(ctx, q, id)
}

// merchantTargets menghubungkan merchant.Service ke webhookout.Targets dan menerjemahkan error,
// supaya kedua modul tidak saling meng-import (ADR-001).
type merchantTargets struct{ svc *merchant.Service }

func (m merchantTargets) WebhookTarget(ctx context.Context, q db.DBTX, merchantID uuid.UUID) (string, []byte, error) {
	url, secret, err := m.svc.WebhookTarget(ctx, q, merchantID)
	if errors.Is(err, merchant.ErrNoWebhook) {
		return "", nil, webhookout.ErrNoTarget
	}
	return url, secret, err
}

// reconAdapter menghubungkan modul recon ke ops.Recon (ADR-001: ops tidak meng-import recon).
type reconAdapter struct{}

func (reconAdapter) LockOpenItem(ctx context.Context, tx pgx.Tx, id uuid.UUID) (ops.ReconItem, error) {
	it, err := recon.LockOpenItem(ctx, tx, id)
	if err != nil {
		return ops.ReconItem{}, err
	}
	return ops.ReconItem{ObjectType: it.ObjectType, InternalID: it.InternalID, Category: it.Category, ExternalStatus: it.ExternalStatus}, nil
}

func (reconAdapter) ResolveItem(ctx context.Context, tx pgx.Tx, id uuid.UUID, action, note string, by uuid.UUID) error {
	return recon.ResolveItem(ctx, tx, id, action, note, by)
}

func (reconAdapter) ItemView(ctx context.Context, tx pgx.Tx, id uuid.UUID) (any, error) {
	return recon.GetItemView(ctx, tx, id)
}
