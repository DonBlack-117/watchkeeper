// Command watchkeeper runs the checks and serves the REST API.
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

	"github.com/DonBlack-117/watchkeeper/internal/api"
	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/config"
	"github.com/DonBlack-117/watchkeeper/internal/scheduler"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

const (
	startupTimeout  = 30 * time.Second
	shutdownTimeout = 15 * time.Second
	pruneEvery      = time.Hour
	pruneBatch      = 5000
)

func main() {
	if err := run(); err != nil {
		slog.Error("watchkeeper stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	if cfg.DatabaseURL == config.DefaultDatabaseURL && !config.IsLoopback(cfg.Addr) {
		log.Warn("using the default local database credentials on a public address; set WK_DATABASE_URL")
	}
	if cfg.AllowPrivate {
		log.Warn("private targets allowed: checks can reach localhost and internal networks")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer st.Close()

	sched := scheduler.New(st, check.NewRunner(cfg.AllowPrivate), log, scheduler.Options{
		MaxConcurrent:  cfg.MaxConcurrent,
		ReloadInterval: cfg.ReloadInterval,
	})
	handler := api.New(st, log, api.Options{
		Token:        cfg.APIToken,
		AllowPrivate: cfg.AllowPrivate,
		MaxChecks:    cfg.MaxChecks,
		Health:       sched.Healthy,
	}).Handler()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	errc := make(chan error, 1)
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		_ = sched.Run(ctx) // returns when ctx is cancelled; problems surface through /health
	}()
	go prune(ctx, st, cfg.RetentionDays, log)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "auth", cfg.APIToken != "", "retention_days", cfg.RetentionDays)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down", "reason", context.Cause(ctx))
	case err = <-errc:
		log.Error("shutting down after server error", "err", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if serr := srv.Shutdown(shutdownCtx); serr != nil {
		err = errors.Join(err, fmt.Errorf("http shutdown: %w", serr))
	}
	select {
	case <-schedDone:
	case <-shutdownCtx.Done():
		err = errors.Join(err, errors.New("checks still running after the shutdown timeout"))
	}
	return err
}

func connect(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	log.Info("applying migrations")
	if err := store.Migrate(ctx, url); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	log.Info("database ready")
	return st, nil
}

// prune deletes results older than the retention period once an hour.
func prune(ctx context.Context, st *store.Store, days int, log *slog.Logger) {
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	for {
		cutoff := time.Now().AddDate(0, 0, -days)
		n, err := st.PruneResults(ctx, cutoff, pruneBatch)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("prune old results", "cutoff", cutoff, "deleted", n, "err", err)
		case n > 0:
			log.Info("pruned old results", "cutoff", cutoff, "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
