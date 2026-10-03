package app

import (
	"context"

	"github.com/google/uuid"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/transfer"
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
