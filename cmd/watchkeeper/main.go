// Command watchkeeper runs the checks and serves the REST API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/DonBlack-117/watchkeeper/internal/api"
	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/config"
	"github.com/DonBlack-117/watchkeeper/internal/incident"
	"github.com/DonBlack-117/watchkeeper/internal/mcp"
	"github.com/DonBlack-117/watchkeeper/internal/metrics"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/scheduler"
	"github.com/DonBlack-117/watchkeeper/internal/store"
	"github.com/DonBlack-117/watchkeeper/internal/triage"
	"github.com/DonBlack-117/watchkeeper/internal/ui"
)

const (
	startupTimeout  = 30 * time.Second
	shutdownTimeout = 15 * time.Second
	pruneEvery      = time.Hour
	pruneBatch      = 5000
	alertDrain      = 10 * time.Second
	incidentTimeout = 10 * time.Second
)

func main() {
	// The distroless image has no shell or curl, so the container health check
	// runs the binary itself: watchkeeper healthcheck.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	// watchkeeper mcp: a Model Context Protocol server on stdio for AI assistants.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := serveMCP(); err != nil {
			fmt.Fprintln(os.Stderr, "watchkeeper mcp:", err)
			os.Exit(1)
		}
		return
	}
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
	if cfg.AdminUser != "" {
		if err := ensureAdmin(ctx, st, cfg.AdminUser, cfg.AdminPassword); err != nil {
			return err
		}
		log.Info("dashboard account ready", "user", cfg.AdminUser)
	}

	// The metrics read the scheduler, which reports to the metrics: build the
	// metrics first with closures over the scheduler variable.
	var sched *scheduler.Scheduler
	m := metrics.New(metrics.Sources{
		Running:       func() []int64 { return sched.Running() },
		SaveFailures:  func() int64 { return sched.SaveFailures() },
		OpenIncidents: st.CountOpenIncidents,
	})
	var notifiers []notify.Notifier
	if cfg.TelegramToken != "" {
		notifiers = append(notifiers, notify.NewTelegram(cfg.TelegramToken, cfg.TelegramChatID))
	}
	alerts := notify.NewDispatcher(log, m, notifiers...)
	runner := check.NewRunner(cfg.AllowPrivate)
	triager := newTriage(ctx, cfg, st, runner, alerts, m, log)
	incidentOpts := incident.Options{
		AlertOnDegraded: cfg.AlertOnDegraded,
		PublicURL:       cfg.PublicURL,
	}
	if triager != nil {
		incidentOpts.OnDown = triager.IncidentDown
	}
	incidents := incident.New(st, alerts, log, incidentOpts)
	sched = scheduler.New(st, runner, log, scheduler.Options{
		MaxConcurrent:  cfg.MaxConcurrent,
		ReloadInterval: cfg.ReloadInterval,
		OnResult: func(ctx context.Context, c check.Check, res check.Result) {
			m.ObserveRun(c, res)
			// Its own budget: incidents take a few queries after the save.
			ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), incidentTimeout)
			defer cancel()
			incidents.Process(ictx, c, res)
		},
	})
	apiOpts := api.Options{
		Token:          cfg.APIToken,
		AllowPrivate:   cfg.AllowPrivate,
		MaxChecks:      cfg.MaxChecks,
		Health:         sched.Healthy,
		RequireLogin:   cfg.AdminUser != "",
		SecureCookie:   cfg.SecureCookie,
		TrustedProxies: cfg.TrustedProxies,
		Frontend:       ui.Handler(),
		Metrics:        m,
		Alerts:         alerts,
	}
	if triager != nil {
		apiOpts.Triage = triager
	}
	handler := api.New(st, log, apiOpts).Handler()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	errc := make(chan error, 2) // API and metrics servers
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		_ = sched.Run(ctx) // returns when ctx is cancelled; problems surface through /health
	}()
	// Alerts stop after the checks, so the alerts of their last runs are queued.
	alertsCtx, stopAlerts := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAlerts()
	go alerts.Run(alertsCtx, alertDrain)
	// Investigations stop with the alerts, so a diagnosis that finishes during
	// shutdown can still be queued.
	if triager != nil {
		go triager.Run(alertsCtx)
	}
	go prune(ctx, st, cfg.RetentionDays, log)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "token", cfg.APIToken != "", "login", cfg.AdminUser != "",
			"retention_days", cfg.RetentionDays, "alerts", alerts.Channels(), "metrics", cfg.MetricsAddr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http server: %w", err)
		}
	}()
	// /metrics has its own listener so it is never reachable through the public
	// port; in production only Prometheus on the internal network can reach it.
	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", m.Handler())
		metricsSrv = &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		go func() {
			if err := metricsSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("metrics server: %w", err)
			}
		}()
	}

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
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(shutdownCtx)
	}
	select {
	case <-schedDone:
	case <-shutdownCtx.Done():
		err = errors.Join(err, errors.New("checks still running after the shutdown timeout"))
	}
	// Alerts queued by the last runs still go out, within alertDrain.
	stopAlerts()
	if triager != nil {
		select {
		case <-triager.Done():
		case <-shutdownCtx.Done():
		}
	}
	select {
	case <-alerts.Done():
	case <-shutdownCtx.Done():
	}
	return err
}

// newTriage returns the AI investigation service, or nil without an API key.
func newTriage(ctx context.Context, cfg config.Config, st *store.Store, runner *check.Runner,
	alerts *notify.Dispatcher, m *metrics.Metrics, log *slog.Logger) *triage.Service {
	if cfg.AnthropicKey == "" {
		log.Info("AI triage disabled: set WK_ANTHROPIC_API_KEY to enable it")
		return nil
	}
	if n, err := st.FailStaleTriages(ctx, time.Now()); err != nil {
		log.Error("triage: clear interrupted runs", "err", err)
	} else if n > 0 {
		log.Warn("triage: marked runs interrupted by a restart as failed", "count", n)
	}
	client := anthropic.NewClient(option.WithAPIKey(cfg.AnthropicKey), option.WithRequestTimeout(2*time.Minute))
	agent := triage.NewAgent(&client.Beta.Messages, cfg.TriageModel, cfg.TriageEffort)
	log.Info("AI triage enabled", "model", cfg.TriageModel, "effort", cfg.TriageEffort,
		"auto", cfg.TriageAuto, "daily_limit", cfg.TriageDailyLimit)
	return triage.NewService(agent, st, runner, alerts, m, log, triage.Options{
		Auto:       cfg.TriageAuto,
		DailyLimit: cfg.TriageDailyLimit,
		PublicURL:  cfg.PublicURL,
	})
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

// ensureAdmin creates the dashboard account or updates its password. An
// unchanged password is left alone: bcrypt salts differ on every hash, so
// rewriting it would sign everyone out on each restart.
func ensureAdmin(ctx context.Context, st *store.Store, user, password string) error {
	if u, err := st.UserByName(ctx, user); err == nil && api.PasswordMatches(u.PasswordHash, password) {
		return nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("admin account: %w", err)
	}
	hash, err := api.HashPassword(password)
	if err != nil {
		return fmt.Errorf("admin password: %w", err)
	}
	if _, err := st.EnsureUser(ctx, user, hash); err != nil {
		return fmt.Errorf("admin account: %w", err)
	}
	return nil
}

// serveMCP runs the MCP server against the API at WK_URL. stdout carries the
// protocol, so logs go to stderr.
func serveMCP() error {
	client, err := mcp.NewClient(env("WK_URL", "http://127.0.0.1:8090"), os.Getenv("WK_API_TOKEN"))
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcp.NewServer(mcp.Tools(client), version(), log).Serve(ctx, os.Stdin, os.Stdout)
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// healthcheck asks the running server for /api/v1/health on its own port.
func healthcheck() int {
	port := 8090
	if _, p, err := net.SplitHostPort(os.Getenv("WK_ADDR")); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			port = n
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // loopback only; the port is a validated integer
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // same loopback request as above
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "health:", resp.Status)
		return 1
	}
	return 0
}

// prune deletes results older than the retention period and expired sessions once an hour.
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
		if n, err := st.PruneSessions(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Error("prune expired sessions", "err", err)
		} else if n > 0 {
			log.Info("pruned expired sessions", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
