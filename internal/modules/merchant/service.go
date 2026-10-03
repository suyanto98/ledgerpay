// Package merchant: merchant sebagai principal kedua (LF-09, ADR-009/010, C-42).
//
// API key berformat lp_<mode>_<prefix8>_<secret32>:
//   - prefix (8 hex) publik: untuk lookup di DB dan identifikasi di log;
//   - secret (32 karakter base64url = 192 bit) rahasia: yang disimpan hanya SHA-256-nya.
//
// SHA-256 (bukan argon2) cukup karena secret acak berentropi tinggi tidak bisa di-brute-force;
// hash lambat hanya menambah latensi di SETIAP request merchant (beda dengan password).
//
// Manajemen key HANYA lewat ops (tidak ada /merchant/api-keys): key yang bocor tidak bisa
// dipakai untuk menerbitkan key pengganti dan bertahan setelah di-revoke.
package merchant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/merchant/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
)

// Mode V1: hanya "test" (tidak ada uang sungguhan). "live" disiapkan di skema untuk masa depan.
const Mode = "test"

type Service struct {
	pool   *pgxpool.Pool
	ledger *ledger.Service
	ids    idgen.Generator
	clock  clock.Clock
}

func NewService(pool *pgxpool.Pool, l *ledger.Service, ids idgen.Generator, c clock.Clock) *Service {
	return &Service{pool: pool, ledger: l, ids: ids, clock: c}
}

// Create mendaftarkan merchant beserta wallet-nya dalam satu transaksi (hanya lewat CLI seed/ops).
func (s *Service) Create(ctx context.Context, name string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return uuid.Nil, apperr.Validation(apperr.FieldError{Field: "name", Code: "INVALID_LENGTH"})
	}
	id := s.ids.New()
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := store.New(tx).CreateMerchant(ctx, store.CreateMerchantParams{ID: id, Name: name}); err != nil {
			return err
		}
		return s.ledger.CreateMerchantWallet(ctx, tx, id)
	})
	return id, db.MapError(err)
}

// IssuedKey adalah key yang baru dibuat. APIKey (secret) hanya ada di sini, sekali.
type IssuedKey struct {
	ID        uuid.UUID
	Prefix    string
	Slot      int16
	APIKey    string
	CreatedAt time.Time
}

// IssueKey membuat key di slot kosong (1 atau 2). Dipanggil di dalam tx idempotency milik endpoint ops.
// Dua permintaan paralel yang memilih slot yang sama → salah satunya kena partial unique
// uq_merchant_api_keys_active_slot → 409 API_KEY_LIMIT_REACHED (jaring DB, bukan hanya cek aplikasi).
func (s *Service) IssueKey(ctx context.Context, tx pgx.Tx, merchantID, opsUserID uuid.UUID, label *string, expiresAt *time.Time, requestID string) (IssuedKey, error) {
	q := store.New(tx)
	if _, err := q.GetMerchant(ctx, merchantID); err != nil {
		return IssuedKey{}, db.MapError(err)
	}
	used, err := q.ActiveKeySlots(ctx, merchantID)
	if err != nil {
		return IssuedKey{}, db.MapError(err)
	}
	slot := freeSlot(used)
	if slot == 0 {
		return IssuedKey{}, apperr.ErrAPIKeyLimitReached
	}
	prefix, secret, err := newKeyMaterial()
	if err != nil {
		return IssuedKey{}, err
	}
	hash := sha256.Sum256([]byte(secret))
	id := s.ids.New()
	createdAt, err := q.InsertAPIKey(ctx, store.InsertAPIKeyParams{
		ID: id, MerchantID: merchantID, Mode: Mode, Prefix: prefix, KeyHash: hash[:], Slot: slot,
		Label: label, ExpiresAt: expiresAt, CreatedBy: opsUserID,
	})
	if err != nil {
		return IssuedKey{}, db.MapError(err)
	}
	if err := s.audit(ctx, q, opsUserID, "merchant.api_key_created", id, requestID, ""); err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{ID: id, Prefix: prefix, Slot: slot, APIKey: "lp_" + Mode + "_" + prefix + "_" + secret, CreatedAt: createdAt}, nil
}

// RevokedKey adalah hasil revoke.
type RevokedKey struct {
	ID        uuid.UUID
	Prefix    string
	RevokedAt time.Time
}

// RevokeKey mencabut key (idempoten: revoke ulang mengembalikan revoked_at yang sama). Alasan wajib
// dan tercatat di audit_log dalam transaksi yang sama (aturan ops §11).
func (s *Service) RevokeKey(ctx context.Context, keyID, opsUserID uuid.UUID, reason, requestID string) (RevokedKey, error) {
	if len(strings.TrimSpace(reason)) < 10 {
		return RevokedKey{}, apperr.Validation(apperr.FieldError{Field: "reason", Code: "TOO_SHORT", Message: "min 10 characters"})
	}
	now := s.clock.Now()
	var out RevokedKey
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.RevokeKey(ctx, store.RevokeKeyParams{Now: &now, ID: keyID})
		if err != nil {
			return err
		}
		out = RevokedKey{ID: row.ID, Prefix: row.Prefix, RevokedAt: *row.RevokedAt}
		return s.audit(ctx, q, opsUserID, "merchant.api_key_revoked", keyID, requestID, reason)
	})
	return out, db.MapError(err)
}

// Authenticate memverifikasi API key mentah dan mengembalikan principal merchant. Semua kegagalan
// (format salah, prefix tak dikenal, hash beda, revoked, kedaluwarsa, merchant nonaktif) adalah
// error yang sama: penyerang tidak tahu bagian mana yang salah.
func (s *Service) Authenticate(ctx context.Context, raw string) (auth.MerchantPrincipal, error) {
	parts := strings.SplitN(raw, "_", 4) // secret base64url boleh berisi "_", jadi maksimal 4 bagian
	if len(parts) != 4 || parts[0] != "lp" || parts[1] != Mode || len(parts[2]) != 8 || len(parts[3]) != 32 {
		return auth.MerchantPrincipal{}, apperr.ErrUnauthenticated
	}
	q := store.New(s.pool)
	k, err := q.GetActiveKeyByPrefix(ctx, parts[2])
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.MerchantPrincipal{}, apperr.ErrUnauthenticated
	}
	if err != nil {
		return auth.MerchantPrincipal{}, db.MapError(err)
	}
	sum := sha256.Sum256([]byte(parts[3]))
	if subtle.ConstantTimeCompare(sum[:], k.KeyHash) != 1 { // constant-time: tidak bocor lewat timing
		return auth.MerchantPrincipal{}, apperr.ErrUnauthenticated
	}
	now := s.clock.Now()
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return auth.MerchantPrincipal{}, apperr.ErrUnauthenticated
	}
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= time.Minute { // throttle di aplikasi + di SQL
		if err := q.TouchKeyLastUsed(ctx, store.TouchKeyLastUsedParams{Now: now, ID: k.ID}); err != nil {
			return auth.MerchantPrincipal{}, db.MapError(err)
		}
	}
	return auth.MerchantPrincipal{MerchantID: k.MerchantID, KeyID: k.ID}, nil
}

func (s *Service) audit(ctx context.Context, q *store.Queries, opsUserID uuid.UUID, action string, keyID uuid.UUID, requestID, reason string) error {
	target := "merchant_api_key"
	return q.InsertAudit(ctx, store.InsertAuditParams{
		ID: s.ids.New(), ActorType: "OPS", ActorID: &opsUserID, Action: action,
		TargetType: &target, TargetID: &keyID, RequestID: nonEmpty(requestID), Reason: nonEmpty(reason),
	})
}

func freeSlot(used []int16) int16 {
	for _, s := range []int16{1, 2} {
		taken := false
		for _, u := range used {
			if u == s {
				taken = true
			}
		}
		if !taken {
			return s
		}
	}
	return 0
}

// newKeyMaterial: prefix 4 byte (8 hex), secret 24 byte (32 karakter base64url, 192 bit).
func newKeyMaterial() (prefix, secret string, err error) {
	p := make([]byte, 4)
	sec := make([]byte, 24)
	if _, err = rand.Read(p); err != nil {
		return "", "", err
	}
	if _, err = rand.Read(sec); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(p), base64.RawURLEncoding.EncodeToString(sec), nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
