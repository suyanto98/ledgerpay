// Command mock-provider menjalankan payment gateway tiruan (ADR-L05) sebagai service terpisah.
// Konfigurasi lewat env: MOCK_ADDR, MOCK_API_SECRET, MOCK_WEBHOOK_SECRET, MOCK_PUBLIC_URL, MOCK_TIME_SCALE.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"ledgerpay/internal/mockprovider"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "mock-provider")
	scale, _ := strconv.ParseFloat(env("MOCK_TIME_SCALE", "1"), 64)
	m := mockprovider.New(mockprovider.Config{
		APISecret: must("MOCK_API_SECRET"), WebhookSecret: must("MOCK_WEBHOOK_SECRET"),
		PublicURL: env("MOCK_PUBLIC_URL", "http://localhost:9091"), TimeScale: scale, Logger: logger,
	})
	srv := &http.Server{Addr: env("MOCK_ADDR", ":9090"), Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		m.Close()
	}()
	logger.Info("mock.started", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("mock.failed", "err", err.Error())
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing env", "key", k)
		os.Exit(2)
	}
	return v
}
