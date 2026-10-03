// Package identity: registrasi, login, dan profil user (LF-01, ADR-009).
// Sprint 1: register, login (menerbitkan access token + refresh token), GET /me.
// Rotasi refresh token + reuse detection, lockout, dan rate limit datang di Sprint 4.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/mail"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/identity/store"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/idgen"
)

type Config struct {
	Argon2           auth.Argon2Params
	RefreshTTL       time.Duration
	RefreshFamilyTTL time.Duration
}

// WalletCreator dipanggil di dalam transaksi register. Identity tidak meng-import modul ledger;
// ia hanya mengenal interface ini, dan implementasinya disuntikkan oleh composition root (ADR-001).
type WalletCreator interface {
	CreateUserWallet(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error
}

type Service struct {
	pool      *pgxpool.Pool
	wallets   WalletCreator
	tokens    *auth.Tokens
	clock     clock.Clock
	ids       idgen.Generator
	cfg       Config
	dummyHash string
}

func NewService(pool *pgxpool.Pool, wallets WalletCreator, tokens *auth.Tokens, c clock.Clock, ids idgen.Generator, cfg Config) *Service {
	return &Service{pool: pool, wallets: wallets, tokens: tokens, clock: c, ids: ids, cfg: cfg, dummyHash: auth.DummyHash(cfg.Argon2)}
}

// User adalah representasi user yang aman dikirim ke client (tanpa password_hash).
type User struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

// RequestMeta adalah metadata request untuk audit dan daftar sesi.
type RequestMeta struct {
	RequestID string
	UserAgent string
	IP        *netip.Addr
}

type RegisterInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (in RegisterInput) validate() error {
	var fe []apperr.FieldError
	if !validEmail(in.Email) {
		fe = append(fe, apperr.FieldError{Field: "email", Code: "INVALID_FORMAT"})
	}
	if n := utf8.RuneCountInString(in.Password); n < 10 || n > 128 {
		fe = append(fe, apperr.FieldError{Field: "password", Code: "LENGTH", Message: "10–128 characters"})
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.DisplayName)); n < 1 || n > 80 {
		fe = append(fe, apperr.FieldError{Field: "display_name", Code: "LENGTH", Message: "1–80 characters"})
	}
	if len(fe) > 0 {
		return apperr.Validation(fe...)
	}
	return nil
}

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s, "@")
}

// Register membuat user baru beserta wallet IDR-nya dalam satu transaksi. Email duplikat dideteksi oleh unique index (bukan SELECT dulu):
// dua register paralel dengan email sama → tepat satu sukses, satu 409 (tanpa race).
func (s *Service) Register(ctx context.Context, in RegisterInput, meta RequestMeta) (User, error) {
	if err := in.validate(); err != nil {
		return User{}, err
	}
	hash, err := auth.HashPassword(in.Password, s.cfg.Argon2) // di luar transaksi: argon2 ± 100 ms
	if err != nil {
		return User{}, err
	}
	var u User
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.CreateUser(ctx, store.CreateUserParams{
			ID: s.ids.New(), Email: in.Email, PasswordHash: hash, DisplayName: strings.TrimSpace(in.DisplayName),
		})
		if err != nil {
			return err
		}
		u = User{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt}
		// User dan wallet lahir bersama atau tidak sama sekali (LF-01 AC1).
		if err := s.wallets.CreateUserWallet(ctx, tx, row.ID); err != nil {
			return err
		}
		return s.audit(ctx, q, row.ID, "user.registered", meta)
	})
	if err != nil {
		return User{}, db.MapError(err)
	}
	return u, nil
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResult berisi access token (dikirim di body) dan refresh token mentah (dikirim sebagai
// cookie HttpOnly oleh handler; tidak pernah disimpan mentah di DB).
type LoginResult struct {
	AccessToken      string
	AccessExpiresIn  time.Duration // dihitung dengan clock service (bukan jam sistem) → konsisten di test
	RefreshToken     string
	RefreshExpiresAt time.Time
	RefreshMaxAge    time.Duration
}

// Login memverifikasi kredensial. Semua kegagalan (email tidak ada, password salah, akun tidak
// aktif) mengembalikan error yang SAMA, dan email tak dikenal tetap menjalankan argon2 (dummy
// hash) supaya waktu respons tidak membocorkan email mana yang terdaftar (T-SH-01, C-03).
func (s *Service) Login(ctx context.Context, in LoginInput, meta RequestMeta) (LoginResult, error) {
	q := store.New(s.pool)
	user, err := q.GetUserByEmail(ctx, in.Email)
	if err != nil {
		if mapped := db.MapError(err); apperr.As(mapped).Code == apperr.ErrNotFound.Code {
			_, _ = auth.VerifyPassword(in.Password, s.dummyHash)
			return LoginResult{}, apperr.ErrInvalidCredentials
		}
		return LoginResult{}, db.MapError(err)
	}
	ok, err := auth.VerifyPassword(in.Password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, err
	}
	if !ok || user.Status != "ACTIVE" {
		return LoginResult{}, apperr.ErrInvalidCredentials
	}

	now := s.clock.Now()
	rawRefresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return LoginResult{}, err
	}
	res := LoginResult{RefreshToken: rawRefresh, RefreshExpiresAt: now.Add(s.cfg.RefreshTTL), RefreshMaxAge: s.cfg.RefreshTTL}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qt := store.New(tx)
		if err := qt.RecordLoginSuccess(ctx, store.RecordLoginSuccessParams{ID: user.ID, Now: &now}); err != nil {
			return err
		}
		if err := qt.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
			ID: s.ids.New(), UserID: user.ID, FamilyID: s.ids.New(), TokenHash: refreshHash,
			IssuedAt: now, ExpiresAt: res.RefreshExpiresAt, FamilyExpiresAt: now.Add(s.cfg.RefreshFamilyTTL),
			UserAgent: nonEmpty(meta.UserAgent), Ip: meta.IP,
		}); err != nil {
			return err
		}
		return s.audit(ctx, qt, user.ID, "auth.login_succeeded", meta)
	})
	if err != nil {
		return LoginResult{}, db.MapError(err)
	}
	var accessExp time.Time
	res.AccessToken, accessExp, err = s.tokens.Issue(user.ID, []string{user.Role})
	res.AccessExpiresIn = accessExp.Sub(now)
	return res, err
}

// Me mengembalikan profil user yang sedang login.
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (User, error) {
	row, err := store.New(s.pool).GetUserByID(ctx, userID)
	if err != nil {
		return User{}, db.MapError(err)
	}
	return User{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt}, nil
}

func (s *Service) audit(ctx context.Context, q *store.Queries, userID uuid.UUID, action string, meta RequestMeta) error {
	target := "user"
	return q.InsertAudit(ctx, store.InsertAuditParams{
		ID: s.ids.New(), ActorType: "USER", ActorID: &userID, Action: action,
		TargetType: &target, TargetID: &userID, RequestID: nonEmpty(meta.RequestID),
		Ip: meta.IP, UserAgent: nonEmpty(truncate(meta.UserAgent, 512)),
	})
}

// newRefreshToken: 256 bit acak (opaque). Yang disimpan hanya SHA-256-nya. Hash cepat sudah cukup
// karena token berentropi tinggi tidak bisa di-brute-force (beda dengan password; ADR-009).
func newRefreshToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
