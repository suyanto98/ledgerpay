// Command ledgerpay adalah satu binary dengan beberapa subcommand (ADR-001):
//
//	ledgerpay api           # server API publik (:8080) + ops (:9090)
//	ledgerpay worker        # pemroses latar: inbox webhook (Sprint 5); job runner menyusul (Sprint 6)
//	ledgerpay migrate up    # jalankan migrasi (role ledgerpay_owner)
//	ledgerpay healthcheck   # dipakai HEALTHCHECK container (image distroless tidak punya curl)
//	ledgerpay ledger-verify # invariant checker ledger (exit 1 kalau ada pelanggaran)
//	ledgerpay demo-credit <email> <amount>   # isi saldo untuk demo (APP_ENV dev/test saja)
//	ledgerpay merchant-create <name>         # onboarding merchant + wallet (LF-09: lewat seed/ops)
//	ledgerpay grant-ops <email>              # beri role OPS (tidak ada endpoint untuk ini)
//	ledgerpay merchant-webhook <id> <url>    # daftarkan webhook merchant; secret tampil SEKALI
//
// `worker` dan `seed` ditambahkan saat pertama kali dibutuhkan (Sprint 2/6).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"ledgerpay/db/migrations"
	"ledgerpay/internal/app"
	"ledgerpay/internal/modules/identity"
	"ledgerpay/internal/modules/ledger"
	"ledgerpay/internal/modules/payout"
	"ledgerpay/internal/modules/topup"
	"ledgerpay/internal/platform/auth"
	"ledgerpay/internal/platform/clock"
	"ledgerpay/internal/platform/config"
	"ledgerpay/internal/platform/crypto"
	"ledgerpay/internal/platform/db"
	"ledgerpay/internal/platform/egress"
	"ledgerpay/internal/platform/idgen"
	"ledgerpay/internal/platform/observability"
	"ledgerpay/internal/platform/ratelimit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ledgerpay <api|worker|migrate up|healthcheck|ledger-verify|demo-credit|merchant-create|merchant-webhook|grant-ops>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "api":
		err = runAPI()
	case "worker":
		err = runWorker()
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "healthcheck":
		err = runHealthcheck()
	case "ledger-verify":
		err = runLedgerVerify()
	case "demo-credit":
		err = runDemoCredit(os.Args[2:])
	case "merchant-create":
		err = runAdmin(os.Args[2:], "usage: ledgerpay merchant-create <name>", func(ctx context.Context, pool *pgxpool.Pool, arg string) error {
			id, err := app.CreateMerchant(ctx, pool, idgen.V7{}, arg)
			if err == nil {
				fmt.Println(id)
			}
			return err
		})
	case "merchant-webhook":
		err = runMerchantWebhook(os.Args[2:])
	case "grant-ops":
		err = runAdmin(os.Args[2:], "usage: ledgerpay grant-ops <email>", func(ctx context.Context, pool *pgxpool.Pool, arg string) error {
			if err := app.GrantOps(ctx, pool, arg); err != nil {
				return err
			}
			fmt.Println("granted OPS to", arg, "(berlaku setelah login ulang: role ada di access token)")
			return nil
		})
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runAPI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateAPI(); err != nil { // fail fast: jangan start dengan config setengah benar
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, "api", cfg.AppEnv, cfg.Version)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, db.Options{
		MaxConns: cfg.DBMaxConns, StatementTimeout: cfg.DBStatementTimeout,
		LockTimeout: cfg.DBLockTimeout, IdleInTxTimeout: cfg.DBIdleInTxTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	clk, ids := clock.System{}, idgen.V7{}
	tokens, err := auth.NewTokens(cfg.JWTKeys, cfg.JWTActiveKID, cfg.JWTIssuer, cfg.JWTAccessTTL, clk, ids)
	if err != nil {
		return err
	}
	tiers := ratelimit.DefaultTiers()
	tiers.MoneyWrite.Rules = []ratelimit.Rule{{Events: cfg.RateLimitMoneyWritePerMin, Per: time.Minute}}
	deps := app.Deps{
		Logger: logger, Pool: pool, Tokens: tokens, Clock: clk, IDs: ids, CookieSecure: cfg.CookieSecure,
		Identity:       identity.Config{Argon2: auth.DefaultArgon2, RefreshTTL: cfg.RefreshTTL, RefreshFamilyTTL: cfg.RefreshFamilyTTL},
		AllowedOrigins: cfg.AllowedOrigins, TrustedProxies: cfg.TrustedProxies, PublicBaseURL: cfg.PublicBaseURL, Tiers: &tiers,
		Provider:    topup.NewHTTPProvider(cfg.ProviderBaseURL, cfg.ProviderAPISecret),
		CallbackURL: cfg.ProviderCallbackURL, WebhookSecrets: cfg.ProviderWebhookSecrets,
		Keyring: cfg.Keyring, Egress: egress.Policy{AllowCIDRs: cfg.EgressAllow, RequireHTTPS: cfg.RequireHTTPSHooks},
		PayoutSafeWindow: cfg.PayoutSafeWindow,
	}
	if err := deps.ValidateAPI(); err != nil {
		return err
	}

	// Timeout server menutup slowloris dan koneksi menggantung (C-29).
	api := &http.Server{Addr: cfg.HTTPAddr, Handler: app.APIRouter(deps),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ops := &http.Server{Addr: cfg.OpsAddr, Handler: app.OpsRouter(pool), ReadHeaderTimeout: 5 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- serve(api) }()
	go func() { errc <- serve(ops) }()
	logger.Info("api.started", "http_addr", cfg.HTTPAddr, "ops_addr", cfg.OpsAddr)

	select {
	case err = <-errc:
	case <-ctx.Done():
		logger.Info("api.shutting_down")
	}
	// Graceful shutdown: berhenti menerima koneksi baru, selesaikan request yang sedang berjalan.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = api.Shutdown(shutdownCtx)
	_ = ops.Shutdown(shutdownCtx)
	return err
}

func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runMigrate(args []string) error {
	if len(args) != 1 || args[0] != "up" {
		return errors.New("usage: ledgerpay migrate up")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.MigrateDatabaseURL == "" {
		return errors.New("MIGRATE_DATABASE_URL wajib (role ledgerpay_owner)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := migrations.MigrateUp(ctx, cfg.MigrateDatabaseURL); err != nil {
		return err
	}
	fmt.Println("migrations applied")
	return nil
}

func runHealthcheck() error {
	// URL konstan (loopback): healthcheck tidak boleh bisa diarahkan ke host lain lewat input.
	const readyURL = "http://127.0.0.1:9090/readyz"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: HTTP %d", resp.StatusCode)
	}
	return nil
}

// openPool membuka pool kecil untuk subcommand satu kali jalan (bukan server).
func openPool(ctx context.Context) (*config.Config, *pgxpool.Pool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if cfg.DatabaseURL == "" {
		return nil, nil, errors.New("DATABASE_URL wajib")
	}
	pool, err := db.NewPool(ctx, cfg.DatabaseURL, db.Options{MaxConns: 2, StatementTimeout: time.Minute, LockTimeout: cfg.DBLockTimeout})
	return &cfg, pool, err
}

func runLedgerVerify() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, pool, err := openPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := ledger.Verify(ctx, pool)
	if err != nil {
		return err
	}
	if !rep.OK() {
		return fmt.Errorf("ledger invariant violated: %s", rep)
	}
	fmt.Println("ledger OK: no balance drift, no unbalanced journals")
	return nil
}

func runDemoCredit(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: ledgerpay demo-credit <email> <amount-minor-units>")
	}
	amount, err := ledger.ParseAmount(args[1])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, pool, err := openPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	// Pagar: uang "dari udara" hanya boleh di environment dev/test.
	if cfg.AppEnv != "dev" && cfg.AppEnv != "test" {
		return fmt.Errorf("demo-credit refused: APP_ENV=%q (only dev/test)", cfg.AppEnv)
	}
	if err := app.DemoCredit(ctx, pool, idgen.V7{}, args[0], amount); err != nil {
		return err
	}
	fmt.Printf("credited %s minor units to %s\n", strconv.FormatInt(amount, 10), args[0])
	return nil
}

// runAdmin menjalankan perintah admin satu argumen dengan pool kecil.
func runAdmin(args []string, usage string, fn func(context.Context, *pgxpool.Pool, string) error) error {
	if len(args) != 1 {
		return errors.New(usage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool, err := openPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(ctx, pool, args[0])
}

// runWorker menjalankan pemroses latar. Worker lebih sabar menunggu lock (5 dtk, ADR-011) karena
// tidak ada user yang menunggu respons. Verify-by-fetch ke provider selalu di luar transaksi.
func runWorker() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateWorker(); err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel, "worker", cfg.AppEnv, cfg.Version)
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL, db.Options{
		MaxConns: 10, StatementTimeout: cfg.DBStatementTimeout, LockTimeout: 5 * time.Second, IdleInTxTimeout: cfg.DBIdleInTxTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()
	workerID := cfg.WorkerID
	if workerID == "" {
		workerID, _ = os.Hostname()
	}
	deps := app.Deps{Logger: logger, Pool: pool, Clock: clock.System{}, IDs: idgen.V7{},
		Provider: topup.NewHTTPProvider(cfg.ProviderBaseURL, cfg.ProviderAPISecret), CallbackURL: cfg.ProviderCallbackURL, PublicBaseURL: cfg.PublicBaseURL,
		Bank:    payout.NewHTTPBank(cfg.ProviderBaseURL, cfg.ProviderAPISecret, 10*time.Second),
		Keyring: cfg.Keyring, Egress: egress.Policy{AllowCIDRs: cfg.EgressAllow, RequireHTTPS: cfg.RequireHTTPSHooks},
		PayoutSafeWindow: cfg.PayoutSafeWindow}
	if err := deps.ValidateWorker(); err != nil {
		return err
	}
	// ID unik per START (hostname:pid:uuid): worker yang restart tidak pernah dianggap pemilik lease lamanya.
	id := fmt.Sprintf("%s:%d:%s", workerID, os.Getpid(), uuid.NewString()[:8])
	w := app.NewWorker(deps, app.WorkerConfig{ID: id})
	logger.Info("worker.started", "worker_id", id)
	w.Run(observability.WithLogger(ctx, logger), cfg.WorkerPollInterval)
	logger.Info("worker.stopped")
	return nil
}

func runMerchantWebhook(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: ledgerpay merchant-webhook <merchant-id> <url>")
	}
	id, err := uuid.Parse(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, pool, err := openPool(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	kr, err := crypto.ParseKeyring(cfg.PIIKeyringJSON, cfg.PIIActiveVersion)
	if err != nil {
		return err
	}
	secret, err := app.SetMerchantWebhook(ctx, pool, kr, id, args[1], !cfg.Dev())
	if err != nil {
		return err
	}
	fmt.Println(secret) // satu-satunya saat secret terlihat; yang tersimpan hanya ciphertext
	return nil
}
