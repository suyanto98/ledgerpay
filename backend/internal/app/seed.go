package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/auth"
)

// SeedProfile: ukuran dataset load test (docs/design/23-performance-plan.md §4).
type SeedProfile struct {
	Name      string
	Users     int
	Merchants int
	Journals  int // riwayat (di luar saldo awal user aktif dan hot user)
}

// Profil seed. "perf" mengikuti amandemen Step 12 (1 juta journal); "smoke" untuk CI/uji cepat.
var SeedProfiles = map[string]SeedProfile{
	"smoke": {Name: "smoke", Users: 2_000, Merchants: 20, Journals: 20_000},
	"perf":  {Name: "perf", Users: 100_000, Merchants: 1_000, Journals: 1_000_000},
}

// Akun yang dipakai skrip k6 (perf/k6). Semua user seed memakai password yang sama.
const (
	SeedPassword     = "correct horse battery"
	SeedActiveUsers  = 1_000      // seed-0 … seed-999: saldo awal besar, dipakai LP-2/LP-4
	SeedActiveCredit = 50_000_000 // Rp500.000 per user aktif
	SeedHotUsers     = 5          // hot-0 … hot-4: saldo PERSIS 100 × Rp10.000 (LP-3, sekali pakai per user)
	SeedHotCredit    = 100 * 1_000_000
)

// SeedEmail adalah email user seed ke-i (dipakai k6).
func SeedEmail(i int) string { return fmt.Sprintf("seed-%d@seed.ledgerpay.test", i) }

// Seed mengisi database kosong dengan dataset sintetis lewat COPY (superDSN, session_replication_role
// = replica: trigger keseimbangan dan FK dilewati), lalu menjalankan GATE invariant lewat role
// aplikasi (appPool). Seed yang gagal gate tidak boleh dipakai untuk mengukur apa pun.
//
// Yang di-seed: user + wallet, merchant + wallet, riwayat journal (top-up, transfer, pembayaran)
// dengan distribusi Pareto. Tabel bisnis (transfers, payment_intents, …) TIDAK di-seed: skenario
// subset V1 hanya membutuhkan ledger yang besar (indeks journal_entries) dan saldo yang benar.
func Seed(ctx context.Context, superDSN string, appPool *pgxpool.Pool, p SeedProfile, rngSeed uint64) (SeedReport, error) {
	rep := SeedReport{Profile: p.Name}
	start := time.Now() //nolint:forbidigo // durasi teknis
	conn, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		return rep, err
	}
	defer func() { _ = conn.Close(ctx) }()
	var existing int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM ledgerpay.journals`).Scan(&existing); err != nil {
		return rep, err
	}
	if existing > 0 {
		return rep, errors.New("seed: database already has journals; seed only into an empty database")
	}
	hash, err := auth.HashPassword(SeedPassword, auth.DefaultArgon2) // satu hash untuk semua user (100k × argon2 = jam)
	if err != nil {
		return rep, err
	}
	rng := rand.New(rand.NewPCG(rngSeed, rngSeed^0x9e3779b97f4a7c15)) //nolint:gosec // G404: data sintetis deterministik

	g := newSeedGen(p, rng)
	if err := conn.QueryRow(ctx, `SELECT id FROM ledgerpay.accounts WHERE system_code = 'PROVIDER_CLEARING'`).Scan(&g.providerClearing); err != nil {
		return rep, err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return rep, fmt.Errorf("seed: needs a superuser connection (SEED_DATABASE_URL): %w", err)
	}
	if err := g.copyUsers(ctx, tx, hash); err != nil {
		return rep, err
	}
	if err := g.copyMerchants(ctx, tx); err != nil {
		return rep, err
	}
	// Journal dulu (saldo dihitung sambil jalan), akun wallet terakhir dengan saldo final.
	journals, entries, err := g.copyJournals(ctx, tx)
	if err != nil {
		return rep, err
	}
	if err := g.copyWallets(ctx, tx); err != nil {
		return rep, err
	}
	if err := tx.Commit(ctx); err != nil {
		return rep, err
	}
	rep.Journals, rep.Entries, rep.Users, rep.Merchants = journals, entries, p.Users+SeedHotUsers+1, p.Merchants
	rep.LoadSeconds = time.Since(start).Seconds() //nolint:forbidigo // durasi teknis

	for _, t := range []string{"users", "accounts", "merchants", "journals", "journal_entries"} {
		if _, err := conn.Exec(ctx, "VACUUM (ANALYZE) ledgerpay."+t); err != nil {
			return rep, err
		}
	}
	// Perf env: statistik query mulai bersih setelah seed (extension hanya tersedia kalau di-preload).
	if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_stat_statements`); err == nil {
		_, _ = conn.Exec(ctx, `SELECT pg_stat_statements_reset()`)
	}

	// GATE (§4): constraint dilewati saat COPY, jadi buktikan lewat role aplikasi bahwa ledger sehat.
	verify, err := ledger.Verify(ctx, appPool)
	if err != nil {
		return rep, err
	}
	if !verify.OK() {
		return rep, fmt.Errorf("seed gate FAILED, do not measure on this data: %s", verify)
	}
	var unbalanced int
	if err := appPool.QueryRow(ctx, `SELECT count(*) FROM (SELECT journal_id FROM journal_entries GROUP BY journal_id
		HAVING count(*) < 2) x`).Scan(&unbalanced); err != nil {
		return rep, fmt.Errorf("seed gate: %w", err)
	}
	if unbalanced != 0 {
		return rep, fmt.Errorf("seed gate FAILED: %d journals with fewer than 2 entries", unbalanced)
	}
	rep.TotalSeconds = time.Since(start).Seconds() //nolint:forbidigo // durasi teknis
	return rep, nil
}

// SeedReport adalah ringkasan seed (dicatat di laporan perf).
type SeedReport struct {
	Profile                             string
	Users, Merchants, Journals, Entries int
	LoadSeconds, TotalSeconds           float64
}

type seedGen struct {
	p                SeedProfile
	rng              *rand.Rand
	userIDs          []uuid.UUID // termasuk hot user (di akhir) dan ops (terakhir)
	userWallets      []uuid.UUID
	merchantIDs      []uuid.UUID
	merchantWallets  []uuid.UUID
	balances         map[uuid.UUID]int64 // saldo wallet (sistem tidak dicatat: tidak materialized)
	providerClearing uuid.UUID
	clockStart       time.Time
}

func newSeedGen(p SeedProfile, rng *rand.Rand) *seedGen {
	g := &seedGen{p: p, rng: rng, balances: map[uuid.UUID]int64{},
		clockStart: time.Now().UTC().AddDate(-1, 0, 0)} //nolint:forbidigo // riwayat sintetis 12 bulan ke belakang
	for range p.Users + SeedHotUsers + 1 {
		g.userIDs, g.userWallets = append(g.userIDs, uuid.Must(uuid.NewV7())), append(g.userWallets, uuid.Must(uuid.NewV7()))
	}
	for range p.Merchants {
		g.merchantIDs, g.merchantWallets = append(g.merchantIDs, uuid.Must(uuid.NewV7())), append(g.merchantWallets, uuid.Must(uuid.NewV7()))
	}
	return g
}

func (g *seedGen) copyUsers(ctx context.Context, tx pgx.Tx, hash string) error {
	n := len(g.userIDs)
	i := 0
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"ledgerpay", "users"}, []string{"id", "email", "password_hash", "display_name", "role"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i == n {
				return nil, nil
			}
			email, name, role := SeedEmail(i), fmt.Sprintf("Seed User %d", i), "USER"
			switch {
			case i == n-1:
				email, name, role = "seed-ops@seed.ledgerpay.test", "Seed Ops", "OPS"
			case i >= g.p.Users:
				email, name = fmt.Sprintf("hot-%d@seed.ledgerpay.test", i-g.p.Users), fmt.Sprintf("Hot User %d", i-g.p.Users)
			}
			row := []any{g.userIDs[i], email, hash, name, role}
			i++
			return row, nil
		}))
	return err
}

func (g *seedGen) copyMerchants(ctx context.Context, tx pgx.Tx) error {
	i := 0
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"ledgerpay", "merchants"}, []string{"id", "name"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i == len(g.merchantIDs) {
				return nil, nil
			}
			row := []any{g.merchantIDs[i], fmt.Sprintf("Seed Merchant %d", i)}
			i++
			return row, nil
		}))
	return err
}

// pareto: 80% aktivitas jatuh ke 20% indeks pertama.
func (g *seedGen) pareto(n int) int {
	hot := max(n/5, 1)
	if g.rng.IntN(10) < 8 {
		return g.rng.IntN(hot)
	}
	return hot + g.rng.IntN(max(n-hot, 1))
}

type seedJournal struct {
	purpose ledger.Purpose
	source  string
	debit   uuid.UUID
	credit  uuid.UUID
	amount  int64
	at      time.Time
}

// copyJournals menulis riwayat + saldo awal user aktif/hot. Saldo tidak pernah negatif: transfer dan
// pembayaran hanya dibuat kalau pengirim punya saldo (sama dengan aturan LI-02 aplikasi).
func (g *seedGen) copyJournals(ctx context.Context, tx pgx.Tx) (journals, entries int, err error) {
	var batch []seedJournal
	users := g.p.Users
	step := 365 * 24 * time.Hour / time.Duration(max(g.p.Journals, 1))
	at := g.clockStart
	for range g.p.Journals {
		at = at.Add(step)
		u := g.pareto(users)
		switch r := g.rng.IntN(10); {
		case r < 3 || g.balances[g.userWallets[u]] < 1_000_000: // top-up Rp10rb–Rp1jt
			amt := int64(1+g.rng.IntN(100)) * 1_000_000
			batch = append(batch, seedJournal{ledger.PurposeTopupCredit, "topup", g.providerClearing, g.userWallets[u], amt, at})
		case r < 9:
			to := g.pareto(users)
			if to == u {
				to = (u + 1) % users
			}
			amt := min(int64(1+g.rng.IntN(50))*100_000, g.balances[g.userWallets[u]])
			batch = append(batch, seedJournal{ledger.PurposeTransfer, "transfer", g.userWallets[u], g.userWallets[to], amt, at})
		default:
			m := g.pareto(len(g.merchantIDs)) // 10 merchant populer ≈ 80% pembayaran di profil perf
			amt := min(int64(1+g.rng.IntN(20))*100_000, g.balances[g.userWallets[u]])
			batch = append(batch, seedJournal{ledger.PurposePayment, "payment_intent", g.userWallets[u], g.merchantWallets[m], amt, at})
		}
		g.apply(batch[len(batch)-1])
	}
	now := g.clockStart.AddDate(1, 0, 0)
	for i := range min(SeedActiveUsers, users) { // saldo awal yang diketahui untuk k6 (LP-2, LP-4)
		j := seedJournal{ledger.PurposeTopupCredit, "topup", g.providerClearing, g.userWallets[i], SeedActiveCredit, now}
		batch = append(batch, j)
		g.apply(j)
	}
	for h := range SeedHotUsers { // LP-3: saldo PERSIS 100 × Rp10.000
		j := seedJournal{ledger.PurposeTopupCredit, "topup", g.providerClearing, g.userWallets[users+h], SeedHotCredit, now}
		batch = append(batch, j)
		g.apply(j)
	}

	type jrow struct {
		id uuid.UUID
		j  seedJournal
	}
	rows := make([]jrow, len(batch))
	for i, j := range batch {
		rows[i] = jrow{uuid.Must(uuid.NewV7()), j}
	}
	i := 0
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"ledgerpay", "journals"},
		[]string{"id", "purpose", "source_type", "source_id", "currency", "correlation_id", "actor_type", "created_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i == len(rows) {
				return nil, nil
			}
			r := rows[i]
			i++
			return []any{r.id, string(r.j.purpose), r.j.source, uuid.Must(uuid.NewV7()), ledger.SupportedCurrency, "seed", "SYSTEM", r.j.at}, nil
		})); err != nil {
		return 0, 0, fmt.Errorf("copy journals: %w", err)
	}
	i, side := 0, 0
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"ledgerpay", "journal_entries"},
		[]string{"id", "journal_id", "account_id", "direction", "amount", "currency", "created_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i == len(rows) {
				return nil, nil
			}
			r := rows[i]
			acct, dir := r.j.debit, "D"
			if side == 1 {
				acct, dir = r.j.credit, "C"
			}
			row := []any{uuid.Must(uuid.NewV7()), r.id, acct, dir, r.j.amount, ledger.SupportedCurrency, r.j.at}
			if side == 1 {
				i++
			}
			side ^= 1
			return row, nil
		})); err != nil {
		return 0, 0, fmt.Errorf("copy entries: %w", err)
	}
	return len(rows), 2 * len(rows), nil
}

// apply: wallet = liability → KREDIT menambah, DEBIT mengurangi. Akun sistem tidak materialized.
func (g *seedGen) apply(j seedJournal) {
	if j.debit != g.providerClearing {
		g.balances[j.debit] -= j.amount
	}
	g.balances[j.credit] += j.amount
}

func (g *seedGen) copyWallets(ctx context.Context, tx pgx.Tx) error {
	i := 0
	nu := len(g.userWallets)
	total := nu + len(g.merchantWallets)
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"ledgerpay", "accounts"},
		[]string{"id", "user_id", "merchant_id", "is_system", "type", "normal_side", "currency", "balance"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i == total {
				return nil, nil
			}
			var row []any
			if i < nu {
				row = []any{g.userWallets[i], g.userIDs[i], nil, false, "LIABILITY", "C", ledger.SupportedCurrency, g.balances[g.userWallets[i]]}
			} else {
				m := i - nu
				row = []any{g.merchantWallets[m], nil, g.merchantIDs[m], false, "LIABILITY", "C", ledger.SupportedCurrency, g.balances[g.merchantWallets[m]]}
			}
			i++
			return row, nil
		}))
	return err
}
