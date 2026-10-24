// Package api exposes the REST API, version 1.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

// Store is the persistence the API needs; *store.Store implements it.
type Store interface {
	Ping(ctx context.Context) error
	CreateCheck(ctx context.Context, c check.Check) (check.Check, error)
	GetCheck(ctx context.Context, id int64) (check.Check, error)
	ListChecks(ctx context.Context) ([]check.Check, error)
	CountChecks(ctx context.Context) (int, error)
	UpdateCheck(ctx context.Context, c check.Check) (check.Check, error)
	DeleteCheck(ctx context.Context, id int64) error
	ListResults(ctx context.Context, q store.ResultQuery) ([]check.Result, error)
	RecentResults(ctx context.Context, n int) (map[int64][]check.Result, error)
	DailyUptime(ctx context.Context, checkID int64, days int, now time.Time) ([]store.DayUptime, error)
	UptimeSince(ctx context.Context, since time.Time) (map[int64]float64, error)
	StatsSince(ctx context.Context, since time.Time) (map[int64]store.CheckStats, error)
	SummarySince(ctx context.Context, since time.Time) (store.Summary, error)
	DailyUptimeAll(ctx context.Context, days int, now time.Time) (map[int64][]store.DayUptime, error)
	HourlyLatency(ctx context.Context, checkID int64, from, to time.Time) ([]store.HourLatency, error)
	UserByName(ctx context.Context, username string) (store.User, error)
	CreateSession(ctx context.Context, tokenHash []byte, userID int64, expires time.Time) error
	SessionUser(ctx context.Context, tokenHash []byte, now time.Time) (store.User, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	ListIncidents(ctx context.Context, q store.IncidentQuery) ([]store.Incident, error)
	CountOpenIncidents(ctx context.Context) (int, error)
	ResolveOpen(ctx context.Context, checkID int64, at time.Time) (bool, error)
	GetIncident(ctx context.Context, id int64) (store.Incident, error)
	LatestTriage(ctx context.Context, incidentID int64) (store.Triage, error)
}

// HTTPObserver records served requests; *metrics.Metrics implements it.
type HTTPObserver interface {
	ObserveHTTP(route, method string, code int, d time.Duration)
}

// Alerts is the alert channel as seen by the API; *notify.Dispatcher implements it.
type Alerts interface {
	Channels() []string
	SendNow(ctx context.Context, m notify.Message) error
}

const (
	maxBodyBytes   = 64 << 10
	defaultLimit   = 100
	maxLimit       = 500
	maxUptimeDays  = 90
	statusLookback = 90 * 24 * time.Hour
	statsWindow    = 24 * time.Hour
	maxLatencyWin  = 7 * 24 * time.Hour
)

// Options configures the server; zero values take the defaults noted below.
type Options struct {
	// Token protects every endpoint except /status, /health and the login flow.
	// Empty disables auth unless RequireLogin is set; main only allows that on a
	// loopback address.
	Token string
	// RequireLogin asks for a dashboard session even without a token.
	RequireLogin bool
	// SecureCookie marks the session cookie Secure (HTTPS only).
	SecureCookie bool
	// SessionTTL is how long a dashboard login lasts (default 7 days).
	SessionTTL time.Duration
	// TrustedProxies are the reverse proxies whose X-Forwarded-For is believed
	// when picking the client IP for rate limits.
	TrustedProxies []netip.Prefix
	// Frontend serves every path outside /api/; nil serves nothing there.
	Frontend http.Handler
	// Metrics, when set, records every request.
	Metrics HTTPObserver
	// Alerts, when set, powers the test-alert endpoint and the summary.
	Alerts Alerts
	// Triage, when set, runs AI investigations of incidents.
	Triage Triage
	// AllowPrivate accepts loopback and private targets at validation time.
	AllowPrivate bool
	// MaxChecks caps how many checks can exist (default 200).
	MaxChecks int
	// Health reports scheduler problems for /health; nil means always healthy.
	Health func() error
	// StatusTTL is how long the public status is cached (default 15s).
	StatusTTL time.Duration
	// StatusRate is how many /status requests one client IP may make per minute (default 60).
	StatusRate int
}

// Server holds the handlers' dependencies.
type Server struct {
	store        Store
	log          *slog.Logger
	opts         Options
	now          func() time.Time
	status       *statusCache
	limiter      *rateLimiter
	loginLimiter *rateLimiter
	testLimiter  *rateLimiter
	// triageLimiter caps manual investigations, which cost API tokens.
	triageLimiter *rateLimiter
	mux           *http.ServeMux
}

// New returns a Server.
func New(store Store, log *slog.Logger, opts Options) *Server {
	if opts.MaxChecks <= 0 {
		opts.MaxChecks = 200
	}
	if opts.StatusTTL <= 0 {
		opts.StatusTTL = 15 * time.Second
	}
	if opts.StatusRate <= 0 {
		opts.StatusRate = 60
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = sessionTTLDefault
	}
	return &Server{
		store:         store,
		log:           log,
		opts:          opts,
		now:           time.Now,
		status:        &statusCache{ttl: opts.StatusTTL},
		limiter:       newRateLimiter(opts.StatusRate, time.Minute),
		loginLimiter:  newRateLimiter(10, time.Minute),
		testLimiter:   newRateLimiter(5, time.Minute),
		triageLimiter: newRateLimiter(5, time.Minute),
	}
}

// Handler returns the router wrapped in panic recovery, logging, headers and auth.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.mux = mux
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/status", s.publicStatus)
	mux.HandleFunc("GET /api/v1/checks", s.listChecks)
	mux.HandleFunc("POST /api/v1/checks", s.createCheck)
	mux.HandleFunc("GET /api/v1/checks/{id}", s.getCheck)
	mux.HandleFunc("PATCH /api/v1/checks/{id}", s.patchCheck)
	mux.HandleFunc("DELETE /api/v1/checks/{id}", s.deleteCheck)
	mux.HandleFunc("GET /api/v1/checks/{id}/results", s.listResults)
	mux.HandleFunc("GET /api/v1/checks/{id}/uptime", s.uptime)
	mux.HandleFunc("GET /api/v1/checks/{id}/latency", s.latency)
	mux.HandleFunc("GET /api/v1/summary", s.summary)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/me", s.me)
	mux.HandleFunc("GET /api/v1/incidents", s.listIncidents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", s.getIncident)
	mux.HandleFunc("GET /api/v1/incidents/{id}/triage", s.getTriage)
	mux.HandleFunc("POST /api/v1/incidents/{id}/triage", s.startTriage)
	mux.HandleFunc("POST /api/v1/alerts/test", s.testAlert)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { notFound(w, "no such endpoint") })
	if s.opts.Frontend != nil {
		mux.Handle("/", s.opts.Frontend)
	}
	return s.recoverer(s.logRequests(securityHeaders(s.authenticate(mux))))
}

// ---- headers ----

// frontendCSP allows only the app's own scripts, styles, fonts and API calls.
// React sets style properties through the DOM, which style-src does not block.
const frontendCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			if !isPublic(r) || strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
				h.Set("Cache-Control", "no-store")
			}
		} else {
			h.Set("Content-Security-Policy", frontendCSP)
		}
		next.ServeHTTP(w, r)
	})
}

// ---- health ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	body := map[string]string{"status": "ok", "database": "ok", "scheduler": "ok"}
	code := http.StatusOK
	if err := s.store.Ping(r.Context()); err != nil {
		s.log.ErrorContext(r.Context(), "health: database", "err", err)
		body["database"], body["status"], code = "unreachable", "down", http.StatusServiceUnavailable
	}
	if s.opts.Health != nil {
		if err := s.opts.Health(); err != nil {
			s.log.WarnContext(r.Context(), "health: scheduler", "err", err)
			body["scheduler"], body["status"], code = err.Error(), "down", http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, body)
}

// ---- checks ----

type checkView struct {
	check.Check
	Status     check.Status  `json:"status"`
	LastResult *check.Result `json:"lastResult,omitempty"`
	// Filled by the list endpoint only.
	Uptime24h *float64   `json:"uptime24h,omitempty"`
	P95MS24h  *float64   `json:"p95Ms24h,omitempty"`
	Uptime90  *float64   `json:"uptime90,omitempty"`
	Daily     []*float64 `json:"daily,omitempty"` // 90 daily percentages, oldest first; null = no runs
}

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	checks, err := s.store.ListChecks(r.Context())
	if err != nil {
		s.fail(w, r, "list checks", err)
		return
	}
	recent, err := s.store.RecentResults(r.Context(), 3)
	if err != nil {
		s.fail(w, r, "recent results", err)
		return
	}
	now := s.now()
	stats, err := s.store.StatsSince(r.Context(), now.Add(-statsWindow))
	if err != nil {
		s.fail(w, r, "check stats", err)
		return
	}
	uptime, err := s.store.UptimeSince(r.Context(), now.Add(-statusLookback))
	if err != nil {
		s.fail(w, r, "uptime", err)
		return
	}
	daily, err := s.store.DailyUptimeAll(r.Context(), maxUptimeDays, now)
	if err != nil {
		s.fail(w, r, "daily uptime", err)
		return
	}
	out := make([]checkView, 0, len(checks))
	for _, c := range checks {
		v := view(c, recent[c.ID])
		if st, ok := stats[c.ID]; ok {
			v.Uptime24h, v.P95MS24h = &st.Uptime, st.P95
		}
		if u, ok := uptime[c.ID]; ok {
			v.Uptime90 = &u
		}
		v.Daily = percents(daily[c.ID], maxUptimeDays)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func view(c check.Check, recent []check.Result) checkView {
	v := checkView{Check: c, Status: check.StatusFrom(recent)}
	if len(recent) > 0 {
		v.LastResult = &recent[0]
	}
	return v
}

type checkInput struct {
	Name           *string     `json:"name"`
	Kind           *check.Kind `json:"kind"`
	Target         *string     `json:"target"`
	IntervalS      *int        `json:"intervalS"`
	TimeoutMS      *int        `json:"timeoutMs"`
	ExpectedStatus optionalInt `json:"expectedStatus"`
	Enabled        *bool       `json:"enabled"`
}

// optionalInt tells a missing field (keep the value) from an explicit null
// (clear it), which a plain *int cannot.
type optionalInt struct {
	set bool
	v   *int
}

func (o *optionalInt) UnmarshalJSON(b []byte) error {
	o.set = true
	return json.Unmarshal(b, &o.v)
}

func (in checkInput) apply(c *check.Check) {
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Kind != nil {
		c.Kind = *in.Kind
	}
	if in.Target != nil {
		c.Target = strings.TrimSpace(*in.Target)
	}
	if in.IntervalS != nil {
		c.IntervalS = *in.IntervalS
	}
	if in.TimeoutMS != nil {
		c.TimeoutMS = *in.TimeoutMS
	}
	if in.ExpectedStatus.set {
		c.ExpectedStatus = in.ExpectedStatus.v
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
}

// validate runs the structural rules and the target policy.
func (s *Server) validate(c check.Check) []check.FieldError {
	errs := check.Validate(c)
	if len(errs) == 0 {
		if msg := check.TargetPolicyError(c, s.opts.AllowPrivate); msg != "" {
			errs = append(errs, check.FieldError{Field: "target", Message: msg})
		}
	}
	return errs
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		badRequest(w, "invalid JSON body")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		badRequest(w, "body must contain a single JSON object")
		return false
	}
	return true
}

func (s *Server) createCheck(w http.ResponseWriter, r *http.Request) {
	var in checkInput
	if !decode(w, r, &in) {
		return
	}
	c := check.Check{IntervalS: 60, TimeoutMS: 10000, Enabled: true}
	in.apply(&c)
	if errs := s.validate(c); len(errs) > 0 {
		invalid(w, errs)
		return
	}
	n, err := s.store.CountChecks(r.Context())
	if err != nil {
		s.fail(w, r, "count checks", err)
		return
	}
	if n >= s.opts.MaxChecks {
		conflict(w, "the maximum of "+strconv.Itoa(s.opts.MaxChecks)+" checks has been reached")
		return
	}
	created, err := s.store.CreateCheck(r.Context(), c)
	if err != nil {
		s.fail(w, r, "create check", err)
		return
	}
	w.Header().Set("Location", "/api/v1/checks/"+strconv.FormatInt(created.ID, 10))
	writeJSON(w, http.StatusCreated, map[string]any{"data": view(created, nil)})
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) loadCheck(w http.ResponseWriter, r *http.Request) (check.Check, bool) {
	id, ok := parseID(r)
	if !ok {
		notFound(w, "check not found")
		return check.Check{}, false
	}
	c, err := s.store.GetCheck(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "check not found")
		return c, false
	}
	if err != nil {
		s.fail(w, r, "get check", err)
		return c, false
	}
	return c, true
}

func (s *Server) getCheck(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	recent, err := s.store.RecentResults(r.Context(), 3)
	if err != nil {
		s.fail(w, r, "recent results", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": view(c, recent[c.ID])})
}

func (s *Server) patchCheck(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	var in checkInput
	if !decode(w, r, &in) {
		return
	}
	before := c
	in.apply(&c)
	if errs := s.validate(c); len(errs) > 0 {
		invalid(w, errs)
		return
	}
	updated, err := s.store.UpdateCheck(r.Context(), c)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "check not found")
		return
	}
	if err != nil {
		s.fail(w, r, "update check", err)
		return
	}
	// A paused check, or one pointed at something else, says nothing more
	// about its open incident: close it instead of leaving it open forever.
	if before.Enabled && !updated.Enabled || before.Kind != updated.Kind || before.Target != updated.Target {
		if _, err := s.store.ResolveOpen(r.Context(), updated.ID, s.now()); err != nil {
			s.log.ErrorContext(r.Context(), "resolve incident after edit", "check_id", updated.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": view(updated, nil)})
}

func (s *Server) deleteCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		notFound(w, "check not found")
		return
	}
	if err := s.store.DeleteCheck(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		notFound(w, "check not found")
		return
	} else if err != nil {
		s.fail(w, r, "delete check", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- results and uptime ----

func (s *Server) listResults(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	q := store.ResultQuery{CheckID: c.ID, Limit: defaultLimit}
	params := r.URL.Query()
	var errs []check.FieldError
	if v := params.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxLimit {
			q.Limit = n
		} else {
			errs = append(errs, check.FieldError{Field: "limit", Message: "must be between 1 and " + strconv.Itoa(maxLimit)})
		}
	}
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if v := params.Get(f.name); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				*f.dst = t
			} else {
				errs = append(errs, check.FieldError{Field: f.name, Message: "must be an RFC 3339 timestamp"})
			}
		}
	}
	if v := params.Get("cursor"); v != "" {
		if id, err := decodeCursor(v); err == nil {
			q.BeforeID = id
		} else {
			errs = append(errs, check.FieldError{Field: "cursor", Message: "is not valid"})
		}
	}
	if len(errs) > 0 {
		invalid(w, errs)
		return
	}

	results, err := s.store.ListResults(r.Context(), q)
	if err != nil {
		s.fail(w, r, "list results", err)
		return
	}
	meta := map[string]any{"limit": q.Limit}
	if len(results) == q.Limit {
		meta["nextCursor"] = encodeCursor(results[len(results)-1].ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": results, "meta": meta})
}

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeCursor(s string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(string(b), 10, 64)
	if err == nil && id <= 0 {
		err = errors.New("cursor out of range")
	}
	return id, err
}

func (s *Server) uptime(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	days := maxUptimeDays
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxUptimeDays {
			invalid(w, []check.FieldError{{Field: "days", Message: "must be between 1 and " + strconv.Itoa(maxUptimeDays)}})
			return
		}
		days = n
	}
	data, err := s.store.DailyUptime(r.Context(), c.ID, days, s.now())
	if err != nil {
		s.fail(w, r, "daily uptime", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// percents flattens a daily series to its percentages; a missing series becomes
// n days without data.
func percents(days []store.DayUptime, n int) []*float64 {
	out := make([]*float64, n)
	for i, d := range days {
		if i < n {
			out[i] = d.Percent
		}
	}
	return out
}

func (s *Server) latency(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	window := 24 * time.Hour
	if v := r.URL.Query().Get("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Hour || d > maxLatencyWin || d%time.Hour != 0 {
			invalid(w, []check.FieldError{{Field: "window", Message: "must be a whole number of hours between 1h and 168h"}})
			return
		}
		window = d
	}
	// The window ends after the current hour, so the latest runs are included.
	to := s.now().UTC().Truncate(time.Hour).Add(time.Hour)
	data, err := s.store.HourlyLatency(r.Context(), c.ID, to.Add(-window), to)
	if err != nil {
		s.fail(w, r, "hourly latency", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// summary is the KPI row of the dashboard: last 24 hours of enabled checks.
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	checks, err := s.store.ListChecks(r.Context())
	if err != nil {
		s.fail(w, r, "list checks", err)
		return
	}
	recent, err := s.store.RecentResults(r.Context(), 3)
	if err != nil {
		s.fail(w, r, "recent results", err)
		return
	}
	sum, err := s.store.SummarySince(r.Context(), s.now().Add(-statsWindow))
	if err != nil {
		s.fail(w, r, "summary", err)
		return
	}
	counts := map[check.Status]int{}
	enabled := 0
	for _, c := range checks {
		if c.Enabled {
			enabled++
			counts[check.StatusFrom(recent[c.ID])]++
		}
	}
	open, err := s.store.CountOpenIncidents(r.Context())
	if err != nil {
		s.fail(w, r, "count incidents", err)
		return
	}
	channels := []string{}
	if s.opts.Alerts != nil {
		channels = s.opts.Alerts.Channels()
	}
	triage := map[string]any{"enabled": s.triageEnabled()}
	if s.triageEnabled() {
		triage["model"] = s.opts.Triage.Model()
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"triage":        triage,
		"openIncidents": open,
		"alertChannels": channels,
		"checks":        len(checks),
		"enabled":       enabled,
		"operational":   counts[check.StatusOperational],
		"degraded":      counts[check.StatusDegraded],
		"down":          counts[check.StatusDown],
		"unknown":       counts[check.StatusUnknown],
		"uptime24h":     sum.Uptime,
		"p95Ms24h":      sum.P95,
		"runs24h":       sum.Total,
	}})
}

// ---- helpers and middleware ----

func (s *Server) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.log.ErrorContext(r.Context(), op, "method", r.Method, "path", r.URL.Path, "check_id", r.PathValue("id"), "err", err)
	internalError(w)
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status, r.written = code, true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)
		s.log.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", elapsed.Milliseconds())
		if s.opts.Metrics != nil {
			s.opts.Metrics.ObserveHTTP(s.route(r), methodLabel(r.Method), rec.status, elapsed)
		}
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.log.ErrorContext(r.Context(), "panic in handler", "method", r.Method, "path", r.URL.Path,
					"panic", v, "stack", string(debug.Stack()))
				if !rec.written {
					internalError(rec)
				}
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
