package app

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/merchant"
	"ledgerpay/internal/modules/ops"
	"ledgerpay/internal/modules/payment"
	"ledgerpay/internal/modules/recon"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/modules/wallet"
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

// txLabels memberi label riwayat wallet dari modul pemilik objek sumber (ADR-001: wallet tidak
// meng-import transfer/payment/identity). Tiga query per halaman, berapa pun jumlah barisnya.
type txLabels struct {
	transfers *transfer.Service
	payments  *payment.Service
	users     identity.Directory
}

func (l txLabels) Labels(ctx context.Context, q db.DBTX, viewer uuid.UUID, refs []wallet.SourceRef) (map[uuid.UUID]wallet.Label, error) {
	var transferIDs, intentIDs []uuid.UUID
	for _, r := range refs {
		switch r.Type {
		case "transfer":
			transferIDs = append(transferIDs, r.ID)
		case "payment_intent":
			intentIDs = append(intentIDs, r.ID)
		}
	}
	out := map[uuid.UUID]wallet.Label{}
	cps, err := l.transfers.Counterparts(ctx, q, viewer, transferIDs)
	if err != nil {
		return nil, err
	}
	userIDs := make([]uuid.UUID, 0, len(cps))
	for _, c := range cps {
		userIDs = append(userIDs, c.UserID)
	}
	names, err := l.users.DisplayNames(ctx, q, userIDs)
	if err != nil {
		return nil, err
	}
	for id, c := range cps {
		lb := wallet.Label{Note: c.Note}
		if n, ok := names[c.UserID]; ok {
			lb.Counterparty = &n
		}
		out[id] = lb
	}
	paid, err := l.payments.PaidLabels(ctx, q, viewer, intentIDs)
	if err != nil {
		return nil, err
	}
	for id, p := range paid {
		out[id] = wallet.Label{Counterparty: &p.MerchantName, Note: p.Description}
	}
	return out, nil
}
