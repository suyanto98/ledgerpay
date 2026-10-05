package app_test

import (
	"context"
	"testing"

	"ledgerpay/internal/app"
	"ledgerpay/internal/platform/db/dbtest"
)

// Seed perf memakai COPY dengan trigger dimatikan, jadi gate invariant WAJIB lolos lewat role aplikasi;
// saldo tidak boleh negatif (seed menghormati LI-02); seed ke DB yang sudah berisi ditolak.
func TestSeed_SmallProfilePassesGate(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	p := app.SeedProfile{Name: "test", Users: 300, Merchants: 5, Journals: 3_000}
	rep, err := app.Seed(ctx, d.SuperDSN(), d.App, p, 7)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Journals != 3_000+300+app.SeedHotUsers || rep.Entries != 2*rep.Journals {
		t.Fatalf("report: %+v", rep)
	}
	var negative, hotOK, users int
	_ = d.App.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE balance < 0`).Scan(&negative)
	_ = d.App.QueryRow(ctx, `SELECT count(*) FROM accounts a JOIN users u ON u.id = a.user_id
		WHERE u.email LIKE 'hot-%' AND a.balance = $1`, app.SeedHotCredit).Scan(&hotOK)
	_ = d.App.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users)
	if negative != 0 || hotOK != app.SeedHotUsers || users != 300+app.SeedHotUsers+1 {
		t.Fatalf("negative balances=%d, hot users with exact balance=%d, users=%d", negative, hotOK, users)
	}
	if _, err := app.Seed(ctx, d.SuperDSN(), d.App, p, 7); err == nil {
		t.Fatal("seeding a non-empty database must be refused")
	}
}
