package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"pgregory.net/rapid"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/db/dbtest"
	"ledgerpay/internal/platform/idgen"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fixture struct {
	pool     *pgxpool.Pool
	svc      *ledger.Service
	provider uuid.UUID // akun sistem PROVIDER_CLEARING
}

func newFixture(t *testing.T) (fixture, dbtest.TestDB) {
	t.Helper()
	dbtest.SkipIfShort(t)
	tdb := dbtest.New(t)
	svc := ledger.NewService(idgen.V7{})
	prov, err := svc.SystemAccountID(context.Background(), tdb.App, "PROVIDER_CLEARING")
	if err != nil {
		t.Fatal(err)
	}
	return fixture{pool: tdb.App, svc: svc, provider: prov}, tdb
}

// newWallet membuat user + wallet (persis seperti register, tanpa lewat HTTP).
func (f fixture) newWallet(t *testing.T) (userID, accountID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	userID = uuid.Must(uuid.NewV7())
	err := db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'x', 'T')`,
			userID, userID.String()+"@example.com"); err != nil {
			return err
		}
		return f.svc.CreateUserWallet(ctx, tx, userID)
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.svc.WalletOf(ctx, f.pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	return userID, w.AccountID
}

func (f fixture) post(t *testing.T, j ledger.Journal) error {
	t.Helper()
	return db.WithTx(context.Background(), f.pool, func(tx pgx.Tx) error { return f.svc.PostJournal(context.Background(), tx, j) })
}

func topup(f fixture, wallet uuid.UUID, amount int64) ledger.Journal {
	return ledger.Journal{
		ID: uuid.Must(uuid.NewV7()), Purpose: ledger.PurposeTopupCredit, SourceType: "topup", SourceID: uuid.Must(uuid.NewV7()),
		Currency: "IDR", CorrelationID: "test", Actor: ledger.Actor{Type: "SYSTEM"},
		Entries: []ledger.Entry{{AccountID: f.provider, Direction: ledger.Debit, Amount: amount}, {AccountID: wallet, Direction: ledger.Credit, Amount: amount}},
	}
}

func transfer(from, to, actor uuid.UUID, amount int64) ledger.Journal {
	return ledger.Journal{
		ID: uuid.Must(uuid.NewV7()), Purpose: ledger.PurposeTransfer, SourceType: "transfer", SourceID: uuid.Must(uuid.NewV7()),
		Currency: "IDR", CorrelationID: "test", Actor: ledger.Actor{Type: "USER", ID: &actor},
		Entries: []ledger.Entry{{AccountID: from, Direction: ledger.Debit, Amount: amount}, {AccountID: to, Direction: ledger.Credit, Amount: amount}},
	}
}

func assertInvariants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rep, err := ledger.Verify(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("ledger invariants violated: %s", rep)
	}
}

func pgCode(err error) (string, string) {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code, e.ConstraintName
	}
	return "", ""
}

func balance(t *testing.T, f fixture, user uuid.UUID) int64 {
	t.Helper()
	w, err := f.svc.WalletOf(context.Background(), f.pool, user)
	if err != nil {
		t.Fatal(err)
	}
	return w.Balance
}

func TestPostJournal_TopupAndTransferMoveBalances(t *testing.T) {
	f, _ := newFixture(t)
	rina, rinaW := f.newWallet(t)
	andi, andiW := f.newWallet(t)

	if err := f.post(t, topup(f, rinaW, 100_000_00)); err != nil {
		t.Fatal(err)
	}
	if err := f.post(t, transfer(rinaW, andiW, rina, 30_000_00)); err != nil {
		t.Fatal(err)
	}
	if got := balance(t, f, rina); got != 70_000_00 {
		t.Fatalf("rina balance = %d, want 7000000", got)
	}
	if got := balance(t, f, andi); got != 30_000_00 {
		t.Fatalf("andi balance = %d, want 3000000", got)
	}
	assertInvariants(t, f.pool)
}

// LI-02: saldo tidak cukup → error ramah, dan TIDAK ada yang tertulis (transaksi di-rollback).
func TestPostJournal_InsufficientBalanceWritesNothing(t *testing.T) {
	f, _ := newFixture(t)
	rina, rinaW := f.newWallet(t)
	_, andiW := f.newWallet(t)
	_ = f.post(t, topup(f, rinaW, 10_000_00))

	err := f.post(t, transfer(rinaW, andiW, rina, 10_000_01))
	if !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Fatalf("want INSUFFICIENT_BALANCE, got %v", err)
	}
	if got := balance(t, f, rina); got != 10_000_00 {
		t.Fatalf("balance changed after rejected transfer: %d", got)
	}
	assertInvariants(t, f.pool)
}

// Template purpose ditegakkan dengan data akun: TRANSFER tidak boleh mendebit akun sistem
// (itu sama dengan mencetak uang dari clearing ke wallet tanpa top-up).
func TestPostJournal_TemplateViolationRejected(t *testing.T) {
	f, _ := newFixture(t)
	rina, rinaW := f.newWallet(t)
	j := transfer(f.provider, rinaW, rina, 1_000)
	if err := f.post(t, j); apperr.As(err).Code != "VALIDATION_ERROR" {
		t.Fatalf("TRANSFER debiting a system account must be rejected, got %v", err)
	}
}

// LI-01, jaring terakhir: journal tidak seimbang yang MELEWATI validasi aplikasi (INSERT mentah
// sebagai role app) tetap ditolak database saat COMMIT oleh deferred constraint trigger.
func TestJournal_UnbalancedRejectedAtCommit(t *testing.T) {
	f, _ := newFixture(t)
	_, w := f.newWallet(t)
	ctx := context.Background()
	jid := uuid.Must(uuid.NewV7())
	err := db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO journals (id, purpose, source_type, source_id, currency, correlation_id, actor_type)
			VALUES ($1, 'TOPUP_CREDIT', 'topup', $2, 'IDR', 'raw', 'SYSTEM')`, jid, uuid.New()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journal_entries (id, journal_id, account_id, direction, amount, currency) VALUES
			($1, $2, $3, 'D', 500, 'IDR'), ($4, $2, $5, 'C', 400, 'IDR')`, uuid.New(), jid, f.provider, uuid.New(), w); err != nil {
			return err
		}
		return nil // INSERT lolos; penolakan terjadi saat COMMIT
	})
	if code, con := pgCode(err); code != "23514" || con != "ck_journals_balanced" {
		t.Fatalf("want 23514/ck_journals_balanced at COMMIT, got %v", err)
	}
	if mapped := db.MapError(err); apperr.As(mapped).Code != "INTERNAL_ERROR" {
		t.Fatalf("unbalanced journal is a BUG and must map to INTERNAL_ERROR (alert), got %v", mapped)
	}
}

// LI-04: journal dan entries append-only. Lapis 1 (grant) untuk role app, lapis 2 (trigger) untuk owner.
func TestLedger_AppendOnly(t *testing.T) {
	f, tdb := newFixture(t)
	_, w := f.newWallet(t)
	j := topup(f, w, 5_000)
	if err := f.post(t, j); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stmts := []string{
		`UPDATE journals SET description = 'tamper' WHERE id = $1`,
		`DELETE FROM journals WHERE id = $1`,
		`UPDATE journal_entries SET amount = 1 WHERE journal_id = $1`,
		`DELETE FROM journal_entries WHERE journal_id = $1`,
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s, j.ID); func() bool { c, _ := pgCode(err); return c != "42501" }() {
			t.Errorf("app role: %s → want 42501 (no grant), got %v", s, err)
		}
	}
	owner := tdb.Owner(t)
	for _, s := range stmts {
		_, err := owner.Exec(ctx, s, j.ID)
		var e *pgconn.PgError
		if !errors.As(err, &e) || e.Code != "42501" || !strings.Contains(e.Message, "append-only") {
			t.Errorf("owner: %s → want trigger append-only, got %v", s, err)
		}
	}
}

// LI-04: satu journal maksimal di-reverse sekali (uq_journals_reversal_of → 409 ALREADY_REVERSED).
func TestLedger_DoubleReversalRejected(t *testing.T) {
	f, _ := newFixture(t)
	_, w := f.newWallet(t)
	ops := uuid.Must(uuid.NewV7())
	bank, err := f.svc.SystemAccountID(context.Background(), f.pool, "BANK_CLEARING")
	if err != nil {
		t.Fatal(err)
	}
	manual := func(rev *uuid.UUID, d, c uuid.UUID) ledger.Journal {
		return ledger.Journal{ID: uuid.Must(uuid.NewV7()), Purpose: ledger.PurposeAdjustmentManual, SourceType: "ops_action",
			SourceID: uuid.Must(uuid.NewV7()), Currency: "IDR", CorrelationID: "t", ReversalOf: rev,
			Actor: ledger.Actor{Type: "OPS", ID: &ops}, Description: "koreksi uji",
			Entries: []ledger.Entry{{AccountID: d, Direction: ledger.Debit, Amount: 700}, {AccountID: c, Direction: ledger.Credit, Amount: 700}}}
	}
	orig := manual(nil, bank, w)
	if err := f.post(t, orig); err != nil {
		t.Fatal(err)
	}
	if err := f.post(t, manual(&orig.ID, w, bank)); err != nil {
		t.Fatalf("first reversal must succeed: %v", err)
	}
	err = db.MapError(f.post(t, manual(&orig.ID, w, bank)))
	if !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("second reversal: want ALREADY_REVERSED, got %v", err)
	}
	assertInvariants(t, f.pool)
}

// LI-06: satu objek bisnis tidak bisa diposting dua kali untuk tujuan yang sama.
func TestLedger_SameSourcePostedOnce(t *testing.T) {
	f, _ := newFixture(t)
	_, w := f.newWallet(t)
	j1 := topup(f, w, 1_000)
	j2 := topup(f, w, 1_000)
	j2.SourceID = j1.SourceID // top-up yang sama "diproses" dua kali (misalnya webhook duplikat)
	if err := f.post(t, j1); err != nil {
		t.Fatal(err)
	}
	if code, con := pgCode(f.post(t, j2)); code != "23505" || con != "uq_journals_source" {
		t.Fatalf("want 23505/uq_journals_source, got code=%s constraint=%s", code, con)
	}
	assertInvariants(t, f.pool)
}

// Grant per kolom: aplikasi hanya boleh UPDATE balance/status/updated_at di accounts, dan tidak
// punya UPDATE/DELETE sama sekali di tabel append-only.
func TestCatalogue_AppendOnlyGrants(t *testing.T) {
	f, tdb := newFixture(t)
	ctx := context.Background()
	rows, err := tdb.Owner(t).Query(ctx, `
		SELECT table_name, privilege_type FROM information_schema.role_table_grants
		WHERE grantee = 'ledgerpay_app' AND table_schema = 'ledgerpay'
		  AND table_name IN ('journals', 'journal_entries', 'audit_log') AND privilege_type IN ('UPDATE', 'DELETE', 'TRUNCATE')`)
	if err != nil {
		t.Fatal(err)
	}
	bad, _ := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var tb, p string
		err := r.Scan(&tb, &p)
		return tb + ":" + p, err
	})
	if len(bad) > 0 {
		t.Fatalf("append-only tables must not grant UPDATE/DELETE to app: %v", bad)
	}
	_, w := f.newWallet(t)
	if _, err := f.pool.Exec(ctx, `UPDATE accounts SET type = 'ASSET' WHERE id = $1`, w); func() bool { c, _ := pgCode(err); return c != "42501" }() {
		t.Fatalf("app must not be able to UPDATE accounts.type, got %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE accounts SET status = 'ACTIVE' WHERE id = $1`, w); err != nil {
		t.Fatalf("app must be able to UPDATE accounts.status: %v", err)
	}
}

// Property test terhadap DB nyata: urutan acak top-up dan transfer antar tiga wallet.
// Setelah setiap urutan: invariant checker bersih, tidak ada saldo negatif, dan total saldo
// wallet = total top-up yang berhasil (uang tidak tercipta atau hilang).
func TestProperty_LedgerConservesMoney(t *testing.T) {
	f, _ := newFixture(t)
	users := make([]uuid.UUID, 3)
	wallets := make([]uuid.UUID, 3)
	for i := range users {
		users[i], wallets[i] = f.newWallet(t)
	}
	var totalTopup int64
	rapid.Check(t, func(rt *rapid.T) {
		for range rapid.IntRange(1, 8).Draw(rt, "ops") {
			if rapid.Bool().Draw(rt, "isTopup") {
				i := rapid.IntRange(0, 2).Draw(rt, "to")
				amt := rapid.Int64Range(1, 50_000_00).Draw(rt, "amount")
				if err := f.post(t, topup(f, wallets[i], amt)); err != nil {
					rt.Fatalf("topup failed: %v", err)
				}
				totalTopup += amt
				continue
			}
			from := rapid.IntRange(0, 2).Draw(rt, "from")
			to := (from + rapid.IntRange(1, 2).Draw(rt, "offset")) % 3
			amt := rapid.Int64Range(1, 80_000_00).Draw(rt, "amount")
			err := f.post(t, transfer(wallets[from], wallets[to], users[from], amt))
			if err != nil && !errors.Is(err, ledger.ErrInsufficientBalance) {
				rt.Fatalf("unexpected transfer error: %v", err)
			}
		}
		var sum int64
		for _, u := range users {
			b := balance(t, f, u)
			if b < 0 {
				rt.Fatalf("negative balance %d", b)
			}
			sum += b
		}
		if sum != totalTopup {
			rt.Fatalf("money not conserved: wallets=%d topups=%d", sum, totalTopup)
		}
		rep, err := ledger.Verify(context.Background(), f.pool)
		if err != nil || !rep.OK() {
			rt.Fatalf("invariants: %v %s", err, rep)
		}
	})
}

// ADR-L07 (regression Sprint 3): akun sistem TIDAK boleh dikunci. Dua top-up ke wallet berbeda
// sama-sama menyentuh PROVIDER_CLEARING; top-up kedua harus selesai walaupun transaksi top-up
// pertama masih terbuka. Sebelum perbaikan, LockAccounts ikut mengunci PROVIDER_CLEARING dengan
// FOR NO KEY UPDATE, sehingga semua top-up di seluruh sistem antre di satu baris itu.
func TestLedger_SystemAccountNotLocked(t *testing.T) {
	f, _ := newFixture(t)
	ctx := context.Background()
	_, walletA := f.newWallet(t)
	_, walletB := f.newWallet(t)

	first, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if err := f.svc.PostJournal(ctx, first, topup(f, walletA, 1000)); err != nil {
		t.Fatal(err)
	}
	// Transaksi pertama belum commit. Transaksi kedua dengan lock_timeout pendek:
	// kalau ia harus menunggu lock apa pun milik transaksi pertama, ia gagal dengan 55P03.
	err = db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '200ms'`); err != nil {
			return err
		}
		return f.svc.PostJournal(ctx, tx, topup(f, walletB, 2000))
	})
	if err != nil {
		t.Fatalf("second top-up blocked by the first (system account locked?): %v", err)
	}
}
