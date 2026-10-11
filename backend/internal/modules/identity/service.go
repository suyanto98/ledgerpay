// Package identity: registrasi, login, dan profil user (LF-01, ADR-009).
// Sprint 1: register, login (menerbitkan access token + refresh token), GET /me.
// Rotasi refresh token + reuse detection, lockout, dan rate limit datang di Sprint 4.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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
	"ledgerpay/internal/platform/metrics"
	"ledgerpay/internal/platform/observability"
)

type Config struct {
	Argon2           auth.Argon2Params
	RefreshTTL       time.Duration
	RefreshFamilyTTL time.Duration
	// HashConcurrency: hash argon2 paralel maksimum (C-45). 0 → 4.
	HashConcurrency int
}

// Kebijakan sesi dan lockout (ADR-009, C-03, C-05).
const (
	refreshGrace     = 10 * time.Second // refresh paralel dari dua tab tidak dianggap pencurian
	lockoutThreshold = 5                // gagal ke-5 mulai mengunci
	lockoutBase      = time.Minute      // lalu berlipat: 1, 2, 4, 8 menit …
	lockoutMax       = 15 * time.Minute // … maksimal 15 menit
	hashMaxWait      = 2 * time.Second  // antre slot argon2 maksimal 2 dtk, lalu 503
)

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
	hasher    *auth.Hasher
	dummyHash string
}

func NewService(pool *pgxpool.Pool, wallets WalletCreator, tokens *auth.Tokens, c clock.Clock, ids idgen.Generator, cfg Config) *Service {
	n := cfg.HashConcurrency
	if n <= 0 {
		n = 4
	}
	return &Service{pool: pool, wallets: wallets, tokens: tokens, clock: c, ids: ids, cfg: cfg,
		hasher: auth.NewHasher(cfg.Argon2, n, hashMaxWait), dummyHash: auth.DummyHash(cfg.Argon2)}
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
	hash, err := s.hasher.Hash(ctx, in.Password) // di luar transaksi: argon2 ± 100 ms
	if err != nil {
		return User{}, err
	}
	var u User
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, err = s.createWithWallet(ctx, tx, in.Email, in.DisplayName, hash, "", meta)
		return err
	})
	if err != nil {
		return User{}, db.MapError(err)
	}
	return u, nil
}

// createWithWallet: user dan wallet lahir bersama atau tidak sama sekali (LF-01 AC1), di tx pemanggil.
func (s *Service) createWithWallet(ctx context.Context, tx pgx.Tx, email, displayName, hash, reason string, meta RequestMeta) (User, error) {
	q := store.New(tx)
	row, err := q.CreateUser(ctx, store.CreateUserParams{
		ID: s.ids.New(), Email: email, PasswordHash: hash, DisplayName: strings.TrimSpace(displayName),
	})
	if err != nil {
		return User{}, err
	}
	if err := s.wallets.CreateUserWallet(ctx, tx, row.ID); err != nil {
		return User{}, err
	}
	return User{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt},
		s.auditWithReason(ctx, q, row.ID, "user.registered", reason, meta)
}

// HashPassword meng-hash password dengan antrean argon2 yang sama dengan register/login (C-45).
// Dipanggil DI LUAR transaksi: argon2 ± 100 ms tidak boleh menahan koneksi DB.
func (s *Service) HashPassword(ctx context.Context, password string) (string, error) {
	return s.hasher.Hash(ctx, password)
}

// ProvisionInTx membuat user + wallet di tx pemanggil, dengan hash yang sudah dihitung (HashPassword).
// Dipakai sandbox demo yang membuat beberapa user dan riwayatnya dalam SATU transaksi. reason dicatat di audit.
func (s *Service) ProvisionInTx(ctx context.Context, tx pgx.Tx, email, displayName, hash, reason string, meta RequestMeta) (User, error) {
	if n := utf8.RuneCountInString(strings.TrimSpace(displayName)); !validEmail(email) || n < 1 || n > 80 {
		return User{}, apperr.ErrValidation.WithDetail("invalid email or display name")
	}
	u, err := s.createWithWallet(ctx, tx, email, displayName, hash, reason, meta)
	return u, db.MapError(err)
}

// DisableUsers menonaktifkan user dan mencabut semua refresh token-nya (actor SYSTEM, tercatat di audit).
// Fungsi paket (seperti GrantOps), karena dipanggil worker yang tidak punya Service/token. Access token
// yang sudah terbit tetap berlaku sampai kedaluwarsa (≤ 15 menit, ADR-009). Mengembalikan jumlah user
// yang baru dinonaktifkan; user yang sudah DISABLED dilewati (idempoten).
func DisableUsers(ctx context.Context, tx pgx.Tx, ids idgen.Generator, now time.Time, userIDs []uuid.UUID, reason string) (int, error) {
	q := store.New(tx)
	disabled, err := q.DisableUsers(ctx, userIDs)
	if err != nil {
		return 0, db.MapError(err)
	}
	if _, err := q.RevokeUsersTokens(ctx, store.RevokeUsersTokensParams{Now: &now, Ids: userIDs}); err != nil {
		return 0, db.MapError(err)
	}
	target := "user"
	for _, id := range disabled {
		if err := q.InsertAudit(ctx, store.InsertAuditParams{
			ID: ids.New(), ActorType: "SYSTEM", Action: "user.disabled", TargetType: &target, TargetID: &id, Reason: nonEmpty(reason),
		}); err != nil {
			return 0, db.MapError(err)
		}
	}
	return len(disabled), nil
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
// aktif, akun terkunci) mengembalikan error yang SAMA PERSIS, dan argon2 SELALU dijalankan
// (dummy hash untuk email tak dikenal, hash asli untuk akun terkunci) supaya baik isi respons
// maupun waktunya tidak membocorkan email mana yang terdaftar atau sedang terkunci (T-SH-01, C-03).
func (s *Service) Login(ctx context.Context, in LoginInput, meta RequestMeta) (LoginResult, error) {
	q := store.New(s.pool)
	user, err := q.GetUserByEmail(ctx, in.Email)
	if err != nil {
		if mapped := db.MapError(err); apperr.As(mapped).Code == apperr.ErrNotFound.Code {
			if _, err := s.hasher.Verify(ctx, in.Password, s.dummyHash); err != nil {
				return LoginResult{}, err
			}
			metrics.AuthnFailures.WithLabelValues("invalid_credentials").Inc()
			observability.Logger(ctx).Info("auth.login_failed", "reason", "unknown_email")
			return LoginResult{}, apperr.ErrInvalidCredentials
		}
		return LoginResult{}, db.MapError(err)
	}
	now := s.clock.Now()
	locked := user.LockedUntil != nil && user.LockedUntil.After(now)
	ok, err := s.hasher.Verify(ctx, in.Password, user.PasswordHash) // tetap dijalankan saat terkunci (timing)
	if err != nil {
		return LoginResult{}, err
	}
	if locked || !ok || user.Status != "ACTIVE" {
		reason := "bad_password"
		switch {
		case locked:
			reason = "locked" // percobaan selama terkunci tidak memperpanjang lock
		case user.Status != "ACTIVE":
			reason = "inactive"
		}
		metricReason := "invalid_credentials"
		if locked {
			metricReason = "locked"
		}
		metrics.AuthnFailures.WithLabelValues(metricReason).Inc()
		if err := s.recordFailure(ctx, user.ID, reason, !locked && !ok, now, meta); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, apperr.ErrInvalidCredentials
	}

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

// recordFailure mencatat login gagal (audit) dan, untuk password salah, menaikkan counter lockout.
func (s *Service) recordFailure(ctx context.Context, userID uuid.UUID, reason string, count bool, now time.Time, meta RequestMeta) error {
	return db.MapError(db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if count {
			row, err := q.RecordLoginFailure(ctx, store.RecordLoginFailureParams{
				Threshold: lockoutThreshold, Now: now, BaseSecs: lockoutBase.Seconds(), MaxSecs: lockoutMax.Seconds(), ID: userID,
			})
			if err != nil {
				return err
			}
			if row.LockedUntil != nil && row.LockedUntil.After(now) {
				reason = "locked_now"
				observability.Logger(ctx).Warn("auth.account_locked", "user_id", userID.String(),
					"failed_count", row.FailedLoginCount, "locked_until", row.LockedUntil)
			}
		}
		return s.auditWithReason(ctx, q, userID, "auth.login_failed", reason, meta)
	}))
}

// Refresh menukar refresh token dengan access token baru + refresh token baru (rotasi, C-05).
//
//   - token belum dipakai → tandai used, terbitkan penerus di family yang sama;
//   - token sudah dipakai ≤ 10 dtk lalu → refresh paralel dari tab lain: beri access token saja,
//     TANPA refresh token baru (browser sudah menerima penerus dari respons pertama di cookie jar
//     yang sama; server tidak bisa mengirim ulang penerus karena yang disimpan hanya hash-nya);
//   - token sudah dipakai > 10 dtk lalu → REUSE: kemungkinan dicuri. Seluruh family dicabut
//     (pencuri DAN korban harus login ulang), dicatat di audit, 401 REFRESH_TOKEN_REUSED.
func (s *Service) Refresh(ctx context.Context, raw string, meta RequestMeta) (LoginResult, error) {
	if raw == "" {
		return LoginResult{}, apperr.ErrUnauthenticated
	}
	sum := sha256.Sum256([]byte(raw))
	var (
		res    LoginResult
		reused bool
		userID uuid.UUID
		role   string
	)
	now := s.clock.Now()
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, reused = LoginResult{}, false
		q := store.New(tx)
		t, err := q.GetRefreshTokenForUpdate(ctx, sum[:])
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.ErrUnauthenticated
			}
			return err
		}
		userID, role = t.UserID, t.Role
		if t.RevokedAt != nil || t.UserStatus != "ACTIVE" {
			return apperr.ErrUnauthenticated
		}
		if t.UsedAt != nil {
			if now.Sub(*t.UsedAt) <= refreshGrace {
				return nil // grace: access token saja
			}
			reused = true
			if _, err := q.RevokeFamily(ctx, store.RevokeFamilyParams{Now: &now, Reason: ptr("REUSE_DETECTED"), FamilyID: t.FamilyID}); err != nil {
				return err
			}
			observability.Logger(ctx).Warn("auth.refresh_reuse_detected", "user_id", t.UserID.String(), "family_id", t.FamilyID.String())
			return s.auditWithReason(ctx, q, t.UserID, "auth.refresh_reuse_detected", "family revoked", meta)
		}
		if !now.Before(t.ExpiresAt) || !now.Before(t.FamilyExpiresAt) {
			return apperr.ErrUnauthenticated
		}
		rawNew, hashNew, err := newRefreshToken()
		if err != nil {
			return err
		}
		newID := s.ids.New()
		expires := now.Add(s.cfg.RefreshTTL) // sliding 7 hari …
		if expires.After(t.FamilyExpiresAt) {
			expires = t.FamilyExpiresAt // … tetapi tidak melewati batas absolut family (30 hari sejak login)
		}
		if err := q.MarkRefreshTokenUsed(ctx, store.MarkRefreshTokenUsedParams{Now: &now, ReplacedBy: &newID, ID: t.ID}); err != nil {
			return err
		}
		if err := q.CreateRotatedRefreshToken(ctx, store.CreateRotatedRefreshTokenParams{
			ID: newID, UserID: t.UserID, FamilyID: t.FamilyID, TokenHash: hashNew, ParentID: &t.ID,
			IssuedAt: now, ExpiresAt: expires, FamilyExpiresAt: t.FamilyExpiresAt,
			UserAgent: nonEmpty(truncate(meta.UserAgent, 512)), Ip: meta.IP,
		}); err != nil {
			return err
		}
		res = LoginResult{RefreshToken: rawNew, RefreshExpiresAt: expires, RefreshMaxAge: expires.Sub(now)}
		return nil
	})
	if err != nil {
		return LoginResult{}, db.MapError(err)
	}
	if reused {
		metrics.RefreshReuse.WithLabelValues().Inc()
		return LoginResult{}, apperr.ErrRefreshReused
	}
	var accessExp time.Time
	res.AccessToken, accessExp, err = s.tokens.Issue(userID, []string{role})
	res.AccessExpiresIn = accessExp.Sub(now)
	return res, err
}

// Logout mencabut seluruh family dari refresh token (semua turunan sesi login ini). Selalu
// "berhasil": token tak dikenal atau sudah dicabut tidak menghasilkan error (idempoten).
func (s *Service) Logout(ctx context.Context, raw string, meta RequestMeta) error {
	if raw == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(raw))
	now := s.clock.Now()
	return db.MapError(db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		fam, err := q.GetFamilyByTokenHash(ctx, sum[:])
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		n, err := q.RevokeFamily(ctx, store.RevokeFamilyParams{Now: &now, Reason: ptr("LOGOUT"), FamilyID: fam.FamilyID})
		if err != nil || n == 0 {
			return err
		}
		return s.auditWithReason(ctx, q, fam.UserID, "auth.logged_out", "", meta)
	}))
}

// GrantOps memberi role OPS ke user (hanya lewat CLI admin `ledgerpay grant-ops`; tidak ada endpoint).
func GrantOps(ctx context.Context, q db.DBTX, email string) error {
	n, err := store.New(q).GrantOpsRole(ctx, email)
	if err != nil {
		return db.MapError(err)
	}
	if n == 0 {
		return apperr.ErrNotFound
	}
	return nil
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
	return s.auditWithReason(ctx, q, userID, action, "", meta)
}

func (s *Service) auditWithReason(ctx context.Context, q *store.Queries, userID uuid.UUID, action, reason string, meta RequestMeta) error {
	target := "user"
	return q.InsertAudit(ctx, store.InsertAuditParams{
		ID: s.ids.New(), ActorType: "USER", ActorID: &userID, Action: action,
		TargetType: &target, TargetID: &userID, RequestID: nonEmpty(meta.RequestID),
		Ip: meta.IP, UserAgent: nonEmpty(truncate(meta.UserAgent, 512)), Reason: nonEmpty(reason),
	})
}

func ptr[T any](v T) *T { return &v }

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
