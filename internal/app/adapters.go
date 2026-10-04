package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/merchant"
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
