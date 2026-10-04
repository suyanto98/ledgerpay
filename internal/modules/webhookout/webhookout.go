// Package webhookout: webhook keluar ke merchant (LF-15, docs/design/14-api-external-contracts.md §3).
//
//	Publish (di tx bisnis): webhook_deliveries + job webhook.deliver → event hanya ada kalau
//	                        pembayaran benar-benar commit, dan pembayaran tidak pernah tanpa event.
//	webhook.deliver:        POST lewat egress guard (SSRF), tanda tangan HMAC per merchant,
//	                        setiap percobaan tercatat; gagal → 1m, 5m, 30m, 2h, 6h, 24h → DEAD.
//
// Delivery at-least-once: merchant WAJIB dedupe dengan event id (stabil antar retry dan replay).
package webhookout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/webhookout/store"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/egress"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/jobs"
	"ledgerpay/internal/platform/observability"
	"ledgerpay/internal/platform/webhooksig"
)

const (
	KindDeliver = "webhook.deliver"
	APIVersion  = "2026-03-01"
)

// RetrySchedule: jeda setelah percobaan ke-n (kontrak §3.1): total 7 percobaan dalam ±33 jam.
var RetrySchedule = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour}

// ErrNoTarget: merchant belum mengonfigurasi webhook.
var ErrNoTarget = errors.New("webhookout: merchant has no webhook configured")

// Targets disediakan modul merchant (pemilik tabel merchants) lewat adapter: URL + secret terdekripsi.
type Targets interface {
	WebhookTarget(ctx context.Context, q db.DBTX, merchantID uuid.UUID) (url string, secret []byte, err error)
}

type Service struct {
	pool    *pgxpool.Pool
	targets Targets
	client  *egress.Client
	ids     idgen.Generator
	clock   clock.Clock
}

func NewService(pool *pgxpool.Pool, t Targets, c *egress.Client, ids idgen.Generator, clk clock.Clock) *Service {
	return &Service{pool: pool, targets: t, client: c, ids: ids, clock: clk}
}

// Publish menulis event untuk merchant DI tx bisnis pemanggil. Merchant tanpa webhook → tidak ada
// event (merchant tetap bisa polling GET /merchant/payment-intents/{id}).
func (s *Service) Publish(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, eventType string, data any) error {
	if _, _, err := s.targets.WebhookTarget(ctx, tx, merchantID); errors.Is(err, ErrNoTarget) {
		return nil
	} else if err != nil {
		return err
	}
	eventID, deliveryID := s.ids.New(), s.ids.New()
	payload, err := json.Marshal(map[string]any{
		"id": eventID, "type": eventType, "api_version": APIVersion, "created_at": s.clock.Now().UTC(), "data": data,
	})
	if err != nil {
		return err
	}
	if err := store.New(tx).InsertDelivery(ctx, store.InsertDeliveryParams{
		ID: deliveryID, MerchantID: merchantID, EventID: eventID, EventType: eventType, Payload: payload,
	}); err != nil {
		return db.MapError(err)
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.Spec{Kind: KindDeliver, Payload: map[string]any{"delivery_id": deliveryID},
		Dedupe: KindDeliver + ":" + deliveryID.String(), MaxAttempts: len(RetrySchedule) + 1})
	return err
}

// Kinds: lane "webhook-out" (terpisah dari lane uang: merchant yang lambat tidak menahan payout).
func (s *Service) Kinds() []jobs.Kind {
	return []jobs.Kind{{
		Name: KindDeliver, Handler: s.deliver, MaxAttempts: len(RetrySchedule) + 1, Timeout: 8 * time.Second,
		Backoff: jobs.Schedule(RetrySchedule, 0.1),
		OnDead: func(ctx context.Context, tx pgx.Tx, j *jobs.Job) error {
			var p struct {
				DeliveryID uuid.UUID `json:"delivery_id"`
			}
			if err := j.Decode(&p); err != nil {
				return nil
			}
			return store.New(tx).MarkDeliveryDead(ctx, p.DeliveryID) // replay manual oleh ops (Sprint 8)
		},
	}}
}

func (s *Service) deliver(ctx context.Context, j *jobs.Job) error {
	var p struct {
		DeliveryID uuid.UUID `json:"delivery_id"`
	}
	if err := j.Decode(&p); err != nil {
		return err
	}
	q := store.New(s.pool)
	d, err := q.GetDelivery(ctx, p.DeliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("delivery %s not found", p.DeliveryID))
	}
	if err != nil {
		return db.MapError(err)
	}
	if d.Status != "PENDING" { // sudah terkirim / dead: no-op
		return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error { return jobs.CompleteInTx(ctx, tx, j) })
	}
	url, secret, err := s.targets.WebhookTarget(ctx, s.pool, d.MerchantID)
	if errors.Is(err, ErrNoTarget) {
		return jobs.Permanent(err) // webhook dihapus setelah event dibuat
	}
	if err != nil {
		return err
	}
	attempt := d.Attempts + 1
	resp, sendErr := s.client.Post(ctx, url, map[string]string{
		"Content-Type":               "application/json",
		"User-Agent":                 "LedgerPay-Webhooks/1",
		"LedgerPay-Signature":        webhooksig.Sign(secret, s.clock.Now(), d.Payload),
		"LedgerPay-Event-Id":         d.EventID.String(),
		"LedgerPay-Delivery-Attempt": strconv.Itoa(int(attempt)),
	}, bytes.NewReader(d.Payload))

	ms := int32(resp.Duration.Milliseconds()) //nolint:gosec // G115: durasi < 8 dtk
	var code *int32
	if resp.StatusCode != 0 {
		c := int32(resp.StatusCode) //nolint:gosec // G115: status HTTP
		code = &c
	}
	log := observability.Logger(ctx).With("delivery_id", d.ID.String(), "event_id", d.EventID.String(), "attempt", attempt)
	if sendErr == nil && resp.StatusCode/100 == 2 {
		log.Info("webhook.delivered", "status", resp.StatusCode)
		return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
			qt := store.New(tx)
			if err := qt.InsertAttempt(ctx, store.InsertAttemptParams{ID: s.ids.New(), DeliveryID: d.ID, AttemptNo: attempt, StatusCode: code, DurationMs: ms}); err != nil {
				return err
			}
			if _, err := qt.MarkDelivered(ctx, store.MarkDeliveredParams{ID: d.ID, StatusCode: code}); err != nil {
				return err
			}
			return jobs.CompleteInTx(ctx, tx, j)
		})
	}
	reason := fmt.Sprintf("HTTP %d", resp.StatusCode) // 3xx/4xx/5xx = gagal (redirect tidak diikuti)
	if sendErr != nil {
		reason = sendErr.Error()
	}
	log.Warn("webhook.attempt_failed", "reason", reason)
	if err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qt := store.New(tx)
		if err := qt.InsertAttempt(ctx, store.InsertAttemptParams{ID: s.ids.New(), DeliveryID: d.ID, AttemptNo: attempt, StatusCode: code, DurationMs: ms, Error: &reason}); err != nil {
			return err
		}
		return qt.RecordFailedAttempt(ctx, store.RecordFailedAttemptParams{ID: d.ID, StatusCode: code, LastError: &reason})
	}); err != nil {
		return err
	}
	return fmt.Errorf("webhook delivery failed: %s", reason) // runner menjadwalkan retry sesuai RetrySchedule
}
