// Command server wires the layers together and runs the HTTP API.
// It contains no business logic: only configuration, wiring, and shutdown.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saumya712/ledgerlite/internal/config"
	"github.com/saumya712/ledgerlite/internal/handler"
	"github.com/saumya712/ledgerlite/internal/metrics"
	"github.com/saumya712/ledgerlite/internal/repository"
	"github.com/saumya712/ledgerlite/internal/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	// ctx is cancelled on Ctrl-C or SIGTERM (what Docker and Kubernetes send on stop).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := newPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close() // runs last, after the server has finished its requests

	// ---- wiring: repository -> service -> handler ----
	accountStore := repository.NewAccountStore(pool)
	externalID, err := accountStore.ExternalAccountID(ctx)
	if err != nil {
		return err
	}
	txm := repository.NewTxManager(pool)

	transferSvc := service.NewService(txm, externalID)
	accountSvc := service.NewAccountService(accountStore)

	m := metrics.New()
	m.RegisterPool(pool)
	h := handler.New(transferSvc, accountSvc, log).WithRecorder(m)

	// /metrics is served outside the handler package, and outside request
	// logging, so Prometheus scraping every few seconds does not flood the logs.
	// Production note: expose it on a separate, internal-only port.
	root := http.NewServeMux()
	root.Handle("GET /metrics", m.Handler())
	root.Handle("/", h.Routes())

	// Timeouts stop slow or stuck clients from holding connections forever.
	// Request contexts come from the server, NOT from ctx above, so a shutdown
	// signal lets in-flight transfers finish instead of cancelling them mid-way.
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received, draining requests")
	}

	stop() // a second Ctrl-C now kills the process immediately
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("server stopped cleanly")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func newPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	// The URL contains the password, so it is never logged.
	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	pcfg.MaxConns = int32(cfg.DBMaxConns)

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}
