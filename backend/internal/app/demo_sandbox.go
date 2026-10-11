//go:build demo

package app

// Sandbox pengunjung demo publik ("Coba demo tanpa daftar"). Satu klik = satu dunia kecil milik pengunjung:
// Budi (yang login) dengan saldo dan riwayat, tiga kontak untuk ditransfer, satu merchant dengan tagihan
// yang menunggu dibayar. Pengunjung tidak melihat data pengunjung lain, dan tidak ada yang perlu di-reset.
//
// Semua data dibuat lewat service ASLI (ledger.PostJournal, transfer.Create, payment.Create/Confirm) dalam
// SATU transaksi: invariant ledger, saldo, dan tabel bisnis sama sahnya dengan data user sungguhan. Waktu
// transaksi tidak dipalsukan (ledger append-only, created_at = waktu posting), jadi riwayatnya "hari ini".

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payment"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/httpx"
	"ledgerpay/internal/platform/jobs"
	"ledgerpay/internal/platform/ratelimit"
)

const (
	demoFundingCode   = "DEMO_FUNDING"
	demoEmailDomain   = "demo.ledgerpay.test"
	demoKindExpire    = "demo.sandbox_expire"
	defaultSandboxTTL = 24 * time.Hour
	defaultDailyMax   = 2000
)

// demoActorID: actor OPS untuk journal saldo awal (ADJUSTMENT_MANUAL wajib actor OPS + alasan). Bukan user
// sungguhan (journals.actor_id tanpa FK); nilai tetap supaya semua saldo demo mudah ditelusuri di ops console.
var demoActorID = uuid.MustParse("00000000-0000-7000-8000-00000000de30")

// rp: rupiah → minor unit (sen, ADR-L04).
func rp(n int64) int64 { return n * 100 }

type demoPerson struct {
	key, name string
	funds     int64
}

// Pengunjung + kontak. Nama dan cerita sengaja biasa: yang dinilai pengunjung adalah aplikasinya.
var (
	demoVisitor  = demoPerson{"budi", "Budi Santoso", rp(2_500_000)}
	demoContacts = []demoPerson{{"siti", "Siti Rahma", rp(1_000_000)}, {"andi", "Andi Wijaya", rp(1_000_000)}, {"rina", "Rina Kusuma", rp(1_000_000)}}
)

const demoMerchantName = "Kopi Senja"

// demoStory: riwayat Budi, urut kejadian (yang terakhir tampil paling atas). from/to = key orang;
// "merchant" = bayar Kopi Senja.
var demoStory = []struct {
	from, to string
	amount   int64
	note     string
}{
	{"siti", "budi", rp(150_000), "Patungan makan siang"},
	{"budi", "andi", rp(75_000), "Bayar bensin kemarin"},
	{"budi", "merchant", rp(45_000), "2x Es Kopi Susu"},
	{"rina", "budi", rp(250_000), "Titip beli tiket konser"},
	{"budi", "siti", rp(50_000), "Pulsa"},
	{"budi", "merchant", rp(38_000), "Croissant + Americano"},
}

// Tagihan yang menunggu dibayar pengunjung (halaman checkout).
var demoPendingBill = struct {
	amount int64
	note   string
}{rp(58_000), "Paket Sarapan Berdua"}

type sandboxContact struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type sandboxer struct {
	d        Deps
	w        demoWiring
	ttl      time.Duration
	dailyMax int
}

func newSandboxer(w demoWiring, d Deps) *sandboxer {
	s := &sandboxer{d: d, w: w, ttl: d.DemoSandboxTTL, dailyMax: d.DemoSandboxDailyMax}
	if s.ttl <= 0 {
		s.ttl = defaultSandboxTTL
	}
	if s.dailyMax <= 0 {
		s.dailyMax = defaultDailyMax
	}
	return s
}

// registerSandboxRoutes: POST /demo/sessions (publik) dan GET /demo/sandbox (user sandbox).
func registerSandboxRoutes(w demoWiring, d Deps) {
	s := newSandboxer(w, d)
	// Tidak ada body dan tidak ada cookie yang dibaca, tetapi endpoint ini MENERBITKAN cookie sesi:
	// CSRF guard (Origin + X-Requested-With) mencegah situs lain diam-diam me-login-kan browser korban.
	w.public.With(httpx.CSRFGuard(d.AllowedOrigins), w.limiter.Middleware(w.tiers.DemoSession, ratelimit.ByIP)).
		Post("/demo/sessions", s.createSession)
	w.read.Get("/demo/sandbox", s.getSandbox)
	// Uji ketahanan (demo_stress.go): berat (100 transaksi), jadi tier demo-session per USER juga.
	byUser := func(r *http.Request) string {
		p, _ := auth.PrincipalFrom(r.Context())
		return "stress|" + p.UserID.String()
	}
	w.read.With(w.limiter.Middleware(w.tiers.DemoSession, byUser)).Post("/demo/stress/transfers", s.stressTransfers)
	// Penemuan fitur untuk SPA: tombol "Coba demo" hanya tampil kalau rute ini ada (build release → 404).
	w.public.With(w.limiter.Middleware(w.tiers.AuthIP, ratelimit.ByIP)).Get("/demo", func(rw http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(rw, http.StatusOK, map[string]any{"sandbox": true, "ttl_seconds": int64(s.ttl / time.Second)})
	})
}

// createSession membuat sandbox lalu me-login-kan pengunjung sebagai Budi (cookie refresh + access token,
// sama persis dengan POST /auth/login).
func (s *sandboxer) createSession(w http.ResponseWriter, r *http.Request) {
	meta := identity.RequestMeta{RequestID: httpx.RequestIDFrom(r.Context()), UserAgent: r.UserAgent()}
	if ip := httpx.ClientIPFrom(r); ip.IsValid() {
		meta.IP = &ip
	}
	email, password, err := s.create(r.Context(), meta)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := s.w.identity.Login(r.Context(), identity.LoginInput{Email: email, Password: password}, meta)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.d.Logger.Info("demo.sandbox_created", "request_id", meta.RequestID)
	s.w.identityH.WriteSession(w, res)
}

// create membangun sandbox dalam SATU transaksi dan mengembalikan kredensial Budi (password acak yang
// tidak pernah keluar dari server: dipakai sekali untuk Login di request yang sama).
func (s *sandboxer) create(ctx context.Context, meta identity.RequestMeta) (email, password string, err error) {
	now := s.d.Clock.Now()
	var recent int
	if err := s.d.Pool.QueryRow(ctx, `SELECT count(*) FROM demo_sandboxes WHERE created_at > $1`, now.Add(-24*time.Hour)).Scan(&recent); err != nil {
		return "", "", err
	}
	// Batas lunak: dua request paralel di batas bisa sama-sama lolos (selisih beberapa sandbox, tidak masalah).
	if recent >= s.dailyMax {
		return "", "", apperr.ErrUnavailable.WithDetail("demo sandbox quota for today is used up; try again tomorrow")
	}
	fundingID, err := s.w.sv.ledger.SystemAccountID(ctx, s.d.Pool, demoFundingCode)
	if err != nil {
		return "", "", fmt.Errorf("demo: system account %s missing (demo migrations not applied?): %w", demoFundingCode, err)
	}
	tag, err := randomHex(4) // budi@3fa9c2d1.demo.ledgerpay.test: email unik per sandbox, tidak bisa ditebak
	if err != nil {
		return "", "", err
	}
	emailOf := func(key string) string { return key + "@" + tag + "." + demoEmailDomain }
	password, err = randomSecret()
	if err != nil {
		return "", "", err
	}
	contactPassword, err := randomSecret() // tidak disimpan di mana pun: kontak tidak bisa login
	if err != nil {
		return "", "", err
	}
	// Argon2 di luar transaksi (± 100 ms per hash), lewat antrean hash yang sama dengan register/login.
	visitorHash, err := s.w.identity.HashPassword(ctx, password)
	if err != nil {
		return "", "", err
	}
	contactHash, err := s.w.identity.HashPassword(ctx, contactPassword)
	if err != nil {
		return "", "", err
	}

	err = db.WithTx(ctx, s.d.Pool, func(tx pgx.Tx) error {
		users := map[string]uuid.UUID{}
		var members []uuid.UUID
		var contacts []sandboxContact
		for _, p := range append([]demoPerson{demoVisitor}, demoContacts...) {
			hash := contactHash
			if p == demoVisitor {
				hash = visitorHash
			}
			u, err := s.w.identity.ProvisionInTx(ctx, tx, emailOf(p.key), p.name, hash, "demo_sandbox", meta)
			if err != nil {
				return err
			}
			users[p.key] = u.ID
			members = append(members, u.ID)
			if p != demoVisitor {
				contacts = append(contacts, sandboxContact{Name: p.name, Email: u.Email})
			}
			if err := s.fund(ctx, tx, fundingID, u.ID, p.funds, meta.RequestID); err != nil {
				return err
			}
		}
		merchantID, err := s.w.sv.merchant.CreateInTx(ctx, tx, demoMerchantName)
		if err != nil {
			return err
		}
		for i, step := range demoStory {
			note := step.note
			if step.to == "merchant" {
				in, err := s.bill(ctx, tx, merchantID, tag, i, step.amount, note, payment.DefaultTTLSeconds)
				if err != nil {
					return err
				}
				if _, err := s.w.sv.payment.Confirm(ctx, tx, users[step.from], in.ID, meta.RequestID); err != nil {
					return err
				}
				continue
			}
			if _, err := s.w.transfers.Create(ctx, tx, transfer.Command{
				SenderID: users[step.from], RecipientEmail: emailOf(step.to), Amount: step.amount,
				Currency: ledger.SupportedCurrency, Note: &note, CorrelationID: meta.RequestID,
			}); err != nil {
				return err
			}
		}
		// Tagihan terbuka hidup selama sandbox (dibatasi TTL maksimum intent, 24 jam).
		billTTL := int32(min(s.ttl, time.Duration(payment.MaxTTLSeconds)*time.Second) / time.Second) //nolint:gosec // ≤ 86400
		pending, err := s.bill(ctx, tx, merchantID, tag, len(demoStory), demoPendingBill.amount, demoPendingBill.note, max(billTTL, payment.MinTTLSeconds))
		if err != nil {
			return err
		}
		contactsJSON, err := json.Marshal(contacts)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO demo_sandboxes (id, user_id, member_user_ids, contacts, checkout_intent_id, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, s.d.IDs.New(), users[demoVisitor.key], members, contactsJSON, pending.ID, now, now.Add(s.ttl))
		return err
	})
	if err != nil {
		return "", "", db.MapError(err)
	}
	return emailOf(demoVisitor.key), password, nil
}

// fund: saldo awal dari akun sistem DEMO_FUNDING (bukan top-up palsu: itu akan merusak rekonsiliasi provider).
func (s *sandboxer) fund(ctx context.Context, tx pgx.Tx, fundingID, userID uuid.UUID, amount int64, correlationID string) error {
	w, err := s.w.sv.ledger.WalletOf(ctx, tx, userID)
	if err != nil {
		return err
	}
	return s.w.sv.ledger.PostJournal(ctx, tx, ledger.Journal{
		ID: s.d.IDs.New(), Purpose: ledger.PurposeAdjustmentManual, SourceType: "ops_action", SourceID: s.d.IDs.New(),
		Currency: ledger.SupportedCurrency, CorrelationID: correlationID, Actor: ledger.Actor{Type: "OPS", ID: &demoActorID},
		Description: "Saldo awal sandbox demo",
		Entries: []ledger.Entry{
			{AccountID: fundingID, Direction: ledger.Debit, Amount: amount},
			{AccountID: w.AccountID, Direction: ledger.Credit, Amount: amount},
		},
	})
}

// bill membuat payment intent Kopi Senja, persis seperti merchant memanggil POST /merchant/payment-intents.
func (s *sandboxer) bill(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID, tag string, n int, amount int64, note string, ttl int32) (payment.Intent, error) {
	return s.w.sv.payment.Create(ctx, tx, payment.CreateCommand{
		MerchantID: merchantID, MerchantOrderID: fmt.Sprintf("DEMO-%s-%02d", tag, n), Amount: amount,
		Currency: ledger.SupportedCurrency, Description: &note, TTLSeconds: ttl,
	})
}

type sandboxBill struct {
	ID           uuid.UUID `json:"id"`
	MerchantName string    `json:"merchant_name"`
	Amount       string    `json:"amount"`
	Currency     string    `json:"currency"`
	Description  *string   `json:"description"`
	Status       string    `json:"status"`
}

// getSandbox: info sandbox untuk SPA (banner sisa waktu, kontak transfer, tagihan menunggu). User yang
// bukan pengunjung sandbox (atau sandbox-nya sudah kedaluwarsa) → 404.
func (s *sandboxer) getSandbox(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var (
		contactsJSON []byte
		billID       *uuid.UUID
		expiresAt    time.Time
	)
	err := s.d.Pool.QueryRow(r.Context(), `SELECT contacts, checkout_intent_id, expires_at FROM demo_sandboxes
		WHERE user_id = $1 AND expired_at IS NULL`, p.UserID).Scan(&contactsJSON, &billID, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var contacts []sandboxContact
	if err := json.Unmarshal(contactsJSON, &contacts); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var bill *sandboxBill
	if billID != nil {
		in, err := s.w.sv.payment.ForUser(r.Context(), s.d.Pool, p.UserID, *billID)
		if err != nil && !errors.Is(err, apperr.ErrNotFound) {
			httpx.WriteError(w, r, err)
			return
		}
		if err == nil {
			bill = &sandboxBill{ID: in.ID, MerchantName: in.MerchantName, Amount: ledger.FormatAmount(in.Amount),
				Currency: in.Currency, Description: in.Description, Status: in.Status}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"expires_at": expiresAt, "contacts": contacts, "pending_bill": bill})
}

// demoMaintenanceKinds: job demo.sandbox_expire tiap 5 menit. Sandbox kedaluwarsa → semua user-nya
// DISABLED + refresh token dicabut (pengunjung keluar paling lambat saat access token habis, ≤ 15 menit).
// Ledger tidak disentuh: journal append-only, dan saldo user nonaktif tetap tercatat benar.
func demoMaintenanceKinds(d Deps) ([]jobs.Kind, map[string]time.Duration) {
	kind := jobs.Kind{Name: demoKindExpire, MaxAttempts: 3, Timeout: 2 * time.Minute,
		Handler: func(ctx context.Context, _ *jobs.Job) error {
			n, err := ExpireDemoSandboxes(ctx, d, 100)
			d.Logger.Info("demo.sandbox_expire_done", "expired", n)
			return err
		}}
	return []jobs.Kind{kind}, map[string]time.Duration{demoKindExpire: 5 * time.Minute}
}

// ExpireDemoSandboxes menonaktifkan sandbox yang lewat expires_at (paling banyak batch per panggilan).
// SKIP LOCKED: dua worker tidak memproses sandbox yang sama.
func ExpireDemoSandboxes(ctx context.Context, d Deps, batch int) (int, error) {
	now := d.Clock.Now()
	expired := 0
	err := db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		expired = 0
		rows, err := tx.Query(ctx, `SELECT id, member_user_ids FROM demo_sandboxes
			WHERE expired_at IS NULL AND expires_at <= $1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, batch)
		if err != nil {
			return err
		}
		var ids, members []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			var m []uuid.UUID
			if err := rows.Scan(&id, &m); err != nil {
				rows.Close()
				return err
			}
			ids, members = append(ids, id), append(members, m...)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		if _, err := identity.DisableUsers(ctx, tx, d.IDs, now, members, "demo sandbox expired"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE demo_sandboxes SET expired_at = $1 WHERE id = ANY($2)`, now, ids); err != nil {
			return err
		}
		expired = len(ids)
		return nil
	})
	return expired, err
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
