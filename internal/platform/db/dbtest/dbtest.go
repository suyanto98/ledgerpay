// Package dbtest: Postgres sungguhan untuk test integration (pola Spike 1, Sprint 0).
//   - satu container per package test (Start di TestMain);
//   - template DB dimigrasi sekali sebagai ledgerpay_owner;
//   - setiap test mendapat DB hasil clone TEMPLATE (± 14 ms) dan terhubung sebagai
//     ledgerpay_app (BUKAN superuser), supaya grant dan append-only ikut teruji.
package dbtest

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"ledgerpay/db/migrations"
)

const (
	templateDB  = "lp_template"
	ownerRole   = "ledgerpay_owner"
	appRole     = "ledgerpay_app"
	rolePass    = "test"
	superUser   = "postgres"
	superPass   = "postgres"
	adminDBName = "admin"
)

var (
	adminCfg *pgxpool.Config
	admin    *pgxpool.Pool
	counter  atomic.Int64
	started  bool
)

// Bootstrap adalah SQL yang sama dengan skrip init container (deploy/postgres/init): role +
// schema. Dijalankan di setiap database baru (schema dibuat per database).
func bootstrapRoles() []string {
	return []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE", ownerRole, rolePass),
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS", appRole, rolePass),
		fmt.Sprintf("ALTER ROLE %s SET search_path = ledgerpay", ownerRole),
		fmt.Sprintf("ALTER ROLE %s SET search_path = ledgerpay", appRole),
	}
}

func bootstrapSchema() []string {
	return []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		fmt.Sprintf("CREATE SCHEMA ledgerpay AUTHORIZATION %s", ownerRole),
		fmt.Sprintf("GRANT USAGE ON SCHEMA ledgerpay TO %s", appRole),
	}
}

// Start menyalakan container dan menyiapkan template. Panggil dari TestMain; stop() di akhir.
func Start(ctx context.Context) (stop func(), err error) {
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase(adminDBName), postgres.WithUsername(superUser), postgres.WithPassword(superPass),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return nil, fmt.Errorf("dbtest: start container: %w", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}
	if adminCfg, err = pgxpool.ParseConfig(dsn); err != nil {
		return nil, err
	}
	if admin, err = pgxpool.NewWithConfig(ctx, adminCfg); err != nil {
		return nil, err
	}
	for _, s := range bootstrapRoles() {
		if _, err = admin.Exec(ctx, s); err != nil {
			return nil, fmt.Errorf("dbtest: %s: %w", s, err)
		}
	}
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return nil, err
	}
	if err = execIn(ctx, templateDB, superUser, superPass, bootstrapSchema()...); err != nil {
		return nil, err
	}
	// Migrasi sebagai owner (persis seperti container `ledgerpay-migrate`), lalu koneksi ditutup
	// (Postgres menolak clone template yang masih punya koneksi).
	if err = migrations.MigrateUp(ctx, DSN(templateDB, ownerRole, rolePass)); err != nil {
		return nil, fmt.Errorf("dbtest: migrate template: %w", err)
	}
	started = true
	return func() {
		admin.Close()
		_ = ctr.Terminate(context.Background())
	}, nil
}

// DSN membangun connection string ke database tertentu sebagai role tertentu.
func DSN(dbName, user, pass string) string {
	c := adminCfg.ConnConfig
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", user, pass, c.Host, c.Port, dbName)
}

func execIn(ctx context.Context, dbName, user, pass string, stmts ...string) error {
	p, err := pgxpool.New(ctx, DSN(dbName, user, pass))
	if err != nil {
		return err
	}
	defer p.Close()
	for _, s := range stmts {
		if _, err := p.Exec(ctx, s); err != nil {
			return fmt.Errorf("dbtest: %s: %w", s, err)
		}
	}
	return nil
}

// TestDB adalah database milik satu test.
type TestDB struct {
	Name string
	App  *pgxpool.Pool // role ledgerpay_app — yang dipakai kode aplikasi
}

// Option mengubah pengaturan pool aplikasi test.
type Option func(*pgxpool.Config)

// LockTimeout mengganti lock_timeout pool aplikasi (default sama dengan produksi: 2 detik).
// Test yang sengaja menunggu lock memakai nilai kecil supaya cepat.
func LockTimeout(d time.Duration) Option {
	return func(c *pgxpool.Config) {
		c.ConnConfig.RuntimeParams["lock_timeout"] = fmt.Sprint(d.Milliseconds())
	}
}

// MaxConns mengganti ukuran pool aplikasi.
func MaxConns(n int32) Option { return func(c *pgxpool.Config) { c.MaxConns = n } }

// New membuat DB baru dari template untuk satu test, dihapus otomatis setelah test selesai.
// Pool memakai lock_timeout 2 detik seperti API produksi (ADR-011), supaya perilaku menunggu
// lock di test sama dengan di produksi.
func New(t testing.TB, opts ...Option) TestDB {
	t.Helper()
	if !started {
		t.Fatal("dbtest: Start belum dipanggil di TestMain")
	}
	ctx := context.Background()
	name := fmt.Sprintf("t_%d_%d", time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatalf("dbtest: clone: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(DSN(name, appRole, rolePass))
	if err != nil {
		t.Fatalf("dbtest: parse dsn: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "2000"
	for _, o := range opts {
		o(cfg)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name))
	})
	return TestDB{Name: name, App: pool}
}

// NewEmpty membuat DB kosong (hanya role + schema, BELUM dimigrasi) dan mengembalikan DSN
// owner-nya. Dipakai TestMigrations_UpFromZero.
func NewEmpty(t testing.TB) (name, ownerDSN string) {
	t.Helper()
	ctx := context.Background()
	name = fmt.Sprintf("empty_%d_%d", time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("dbtest: create empty: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name)) })
	if err := execIn(ctx, name, superUser, superPass, bootstrapSchema()...); err != nil {
		t.Fatal(err)
	}
	return name, DSN(name, ownerRole, rolePass)
}

// Owner membuka pool sebagai ledgerpay_owner ke DB test (untuk test katalog/migrasi saja).
func (d TestDB) Owner(t testing.TB) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), DSN(d.Name, ownerRole, rolePass))
	if err != nil {
		t.Fatalf("dbtest: owner connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// Main adalah helper TestMain standar untuk package yang memakai DB.
// Test integration dilewati dengan `go test -short` (job CI "unit").
func Main(m *testing.M) {
	flag.Parse() // testing.Short() butuh flag sudah di-parse (TestMain berjalan sebelum m.Run)
	if testing.Short() {
		os.Exit(m.Run())
	}
	stop, err := Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	stop()
	os.Exit(code)
}

// SkipIfShort menandai test sebagai integration (dilewati di -short).
func SkipIfShort(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test (skipped with -short)")
	}
}
