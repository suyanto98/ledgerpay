// Package ops: API ops console (LF-18, docs/design/13-api-endpoints.md §11).
//
// Modul ini adalah READ MODEL lintas tabel (pencarian, journal, antrean payout, job dead, delivery
// dead) ditambah pencatat audit. Aksi TULIS tidak dikerjakan di sini: modul pemilik tabel yang
// menjalankannya (payout.ResolveByOps, webhookout.Replay, jobs.RetryDead) lewat interface, DI
// transaksi yang sama dengan baris audit_log. Aksi ada ⇔ audit ada.
//
// Aturan ops (§11): setiap aksi tulis wajib alasan (≥ 10 karakter), resolve payout wajib referensi
// bukti, dan tidak ada endpoint "edit saldo": uang hanya bergerak lewat journal biasa.
package ops

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/ops/store"
	"ledgerpay/internal/platform/idgen"
)

// Payouts: resolusi manual payout (modul payout).
type Payouts interface {
	ResolveByOps(ctx context.Context, tx pgx.Tx, id uuid.UUID, succeeded bool, opsUserID uuid.UUID, correlationID string) error
}

// Webhooks: replay delivery webhook keluar yang DEAD (modul webhookout).
type Webhooks interface {
	Replay(ctx context.Context, tx pgx.Tx, deliveryID uuid.UUID) (eventID uuid.UUID, err error)
}

// ReconItem adalah item rekonsiliasi yang terkunci untuk diselesaikan.
type ReconItem struct {
	ObjectType     string
	InternalID     *uuid.UUID
	Category       string
	ExternalStatus *string
}

// Recon: antrean mismatch rekonsiliasi (modul recon, lewat adapter di composition root).
type Recon interface {
	LockOpenItem(ctx context.Context, tx pgx.Tx, id uuid.UUID) (ReconItem, error)
	ResolveItem(ctx context.Context, tx pgx.Tx, id uuid.UUID, action, note string, by uuid.UUID) error
	ItemView(ctx context.Context, tx pgx.Tx, id uuid.UUID) (any, error)
}

// MinReasonLen: alasan aksi ops (§11).
const MinReasonLen = 10

// Actor adalah konteks pelaku aksi ops untuk audit.
type Actor struct {
	UserID    uuid.UUID
	RequestID string
	IP        netip.Addr
	UserAgent string
}

// auditEntry adalah satu baris audit_log aksi ops.
type auditEntry struct {
	Action     string
	TargetType string
	TargetID   uuid.UUID
	Reason     string
	Diff       map[string]any // bukti + hasil, contoh {"outcome": "SUCCEEDED", "evidence_ref": "…"}
}

func writeAudit(ctx context.Context, tx pgx.Tx, ids idgen.Generator, a Actor, e auditEntry) error {
	diff, err := json.Marshal(e.Diff)
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(e.Reason)
	return store.New(tx).InsertAudit(ctx, store.InsertAuditParams{
		ID: ids.New(), ActorID: &a.UserID, Action: e.Action, TargetType: &e.TargetType, TargetID: &e.TargetID,
		RequestID: nonEmpty(a.RequestID), CorrelationID: nonEmpty(a.RequestID), Ip: validIP(a.IP), UserAgent: nonEmpty(a.UserAgent),
		Diff: diff, Reason: &reason,
	})
}

func validIP(ip netip.Addr) *netip.Addr {
	if !ip.IsValid() {
		return nil
	}
	return &ip
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
