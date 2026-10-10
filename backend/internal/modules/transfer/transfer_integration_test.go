package transfer_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pgregory.net/rapid"

	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/transfer"
	"ledgerpay/internal/platform/apperr"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/db/dbtest"
	"ledgerpay/internal/platform/idgen"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// fakeUsers menggantikan identity untuk test level service (transfer hanya mengenal interface Users).
type fakeUsers struct{ byEmail map[string]transfer.Recipient }

func (f fakeUsers) RecipientByEmail(_ context.Context, _ db.DBTX, email string) (transfer.Recipient, error) {
	r, ok := f.byEmail[email]
	if !ok {
		return transfer.Recipient{}, apperr.ErrNotFound
	}
	return r, nil
}

func (f fakeUsers) DisplayName(_ context.Context, _ db.DBTX, id uuid.UUID) (string, error) {
	for _, r := range f.byEmail {
		if r.ID == id {
			return r.DisplayName, nil
		}
	}
	return "", apperr.ErrNotFound
}

type fixture struct {
	pool   *pgxpool.Pool
	ledger *ledger.Service
	users  fakeUsers
}

func newFixture(t *testing.T, opts ...dbtest.Option) *fixture {
	t.Helper()
	dbtest.SkipIfShort(t)
	tdb := dbtest.New(t, opts...)
	return &fixture{pool: tdb.App, ledger: ledger.NewService(idgen.V7{}), users: fakeUsers{byEmail: map[string]transfer.Recipient{}}}
}

// newUser membuat user + wallet (seperti register) dan top-up awal.
func (f *fixture) newUser(t testing.TB, email string, topup int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.Must(uuid.NewV7())
	err := db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, 'x', $2)`, id, email); err != nil {
			return err
		}
		return f.ledger.CreateUserWallet(ctx, tx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	f.users.byEmail[email] = transfer.Recipient{ID: id, DisplayName: email}
	if topup > 0 {
		f.topup(t, id, topup)
	}
	return id
}

func (f *fixture) topup(t testing.TB, userID uuid.UUID, amount int64) {
	t.Helper()
	ctx := context.Background()
	w, err := f.ledger.WalletOf(ctx, f.pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := f.ledger.SystemAccountID(ctx, f.pool, "PROVIDER_CLEARING")
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		return f.ledger.PostJournal(ctx, tx, ledger.Journal{
			ID: uuid.Must(uuid.NewV7()), Purpose: ledger.PurposeTopupCredit, SourceType: "topup", SourceID: uuid.Must(uuid.NewV7()),
			Currency: "IDR", CorrelationID: "seed", Actor: ledger.Actor{Type: "SYSTEM"},
			Entries: []ledger.Entry{{AccountID: prov, Direction: ledger.Debit, Amount: amount}, {AccountID: w.AccountID, Direction: ledger.Credit, Amount: amount}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) balance(t testing.TB, userID uuid.UUID) int64 {
	t.Helper()
	w, err := f.ledger.WalletOf(context.Background(), f.pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	return w.Balance
}

// Pembanding naif (HANYA di test code): read → check → write tanpa lock. Jeda 1 ms mewakili kerja
// nyata di antara baca dan tulis (validasi, query lain). Inilah race (a) di ADR-L03.
func naiveTransfer(ctx context.Context, pool *pgxpool.Pool, from, to uuid.UUID, amount int64) (bool, error) {
	var bal int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM accounts WHERE user_id = $1`, from).Scan(&bal); err != nil {
		return false, err
	}
	if bal < amount {
		return false, nil
	}
	time.Sleep(time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance = $2 WHERE user_id = $1`, from, bal-amount); err != nil {
		return false, err
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance = balance + $2 WHERE user_id = $1`, to, amount); err != nil {
		return false, err
	}
	return true, nil
}

// "Test the test": skenario yang sama dengan TestTransfer_ConcurrentDoubleSpend_100x, tetapi
// dengan implementasi naif, HARUS menghasilkan lebih dari 10 sukses (uang tercipta). Kalau tidak,
// test concurrency kita tidak cukup tajam untuk menangkap race.
func TestTransfer_NaiveImplementation_Overdraws(t *testing.T) {
	f := newFixture(t, dbtest.MaxConns(20))
	rina := f.newUser(t, "rina@example.com", 100_000)
	andi := f.newUser(t, "andi@example.com", 0)

	var ok atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if sent, err := naiveTransfer(context.Background(), f.pool, rina, andi, 10_000); err == nil && sent {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if ok.Load() <= 10 {
		t.Fatalf("naive implementation should overdraw under concurrency, but only %d succeeded", ok.Load())
	}
	t.Logf("naive: %d transfers of 10.000 'succeeded' from a 100.000 balance; receiver got %d", ok.Load(), f.balance(t, andi))
}

// Property (model-based): untuk urutan transfer acak antar 3 user (termasuk ke diri sendiri dan
// ke email tak dikenal), hasil setiap transfer sama dengan model sederhana di memori, total uang
// tetap, tidak ada saldo negatif, dan invariant checker bersih.
func TestProperty_RandomTransfersConserveTotal(t *testing.T) {
	f := newFixture(t)
	svc := transfer.NewService(f.ledger, f.users, idgen.V7{})
	ctx := context.Background()
	var round atomic.Int64

	rapid.Check(t, func(rt *rapid.T) {
		r := round.Add(1)
		emails := make([]string, 3)
		ids := make([]uuid.UUID, 3)
		model := make([]int64, 3)
		var total int64
		for i := range 3 {
			emails[i] = fmt.Sprintf("u%d-%d@example.com", i, r)
			model[i] = rapid.Int64Range(0, 100_000).Draw(rt, fmt.Sprintf("topup%d", i))
			ids[i] = f.newUser(t, emails[i], model[i])
			total += model[i]
		}

		for range rapid.IntRange(1, 10).Draw(rt, "ops") {
			from := rapid.IntRange(0, 2).Draw(rt, "from")
			to := rapid.IntRange(0, 3).Draw(rt, "to") // 3 = email tak terdaftar
			amt := rapid.Int64Range(1, 80_000).Draw(rt, "amount")
			email := "ghost@example.com"
			if to < 3 {
				email = emails[to]
			}

			var err error
			txErr := db.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
				_, err = svc.Create(ctx, tx, transfer.Command{SenderID: ids[from], RecipientEmail: email, Amount: amt, Currency: "IDR", CorrelationID: "prop"})
				return err
			})

			// Model: apa yang SEHARUSNYA terjadi.
			switch {
			case to == 3:
				expectErr(rt, txErr, apperr.ErrInvalidRecipient)
			case to == from:
				expectErr(rt, txErr, apperr.ErrSelfTransfer)
			case model[from] < amt:
				expectErr(rt, txErr, apperr.ErrInsufficientBalance)
			default:
				if txErr != nil {
					rt.Fatalf("transfer %d→%d of %d should succeed (balance %d): %v", from, to, amt, model[from], txErr)
				}
				model[from] -= amt
				model[to] += amt
			}
		}

		var sum int64
		for i := range 3 {
			got := f.balance(t, ids[i])
			if got != model[i] {
				rt.Fatalf("user %d balance = %d, model says %d", i, got, model[i])
			}
			if got < 0 {
				rt.Fatalf("negative balance for user %d: %d", i, got)
			}
			sum += got
		}
		if sum != total {
			rt.Fatalf("money not conserved: sum = %d, total topped up = %d", sum, total)
		}
		rep, err := ledger.Verify(ctx, f.pool)
		if err != nil {
			rt.Fatal(err)
		}
		if !rep.OK() {
			rt.Fatalf("invariant violated: %s", rep)
		}
	})
}

func expectErr(rt *rapid.T, got, want error) {
	if !errors.Is(got, want) {
		rt.Fatalf("want %v, got %v", want, got)
	}
}
