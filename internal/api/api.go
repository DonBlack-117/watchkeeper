// Package api exposes the REST API, version 1.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
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
}

const (
	maxBodyBytes   = 64 << 10
	defaultLimit   = 100
	maxLimit       = 500
	maxUptimeDays  = 90
	statusLookback = 90 * 24 * time.Hour
)

// Options configures the server; zero values take the defaults noted below.
type Options struct {
	// Token protects every endpoint except /status and /health. Empty disables auth,
	// which main only allows on a loopback address.
	Token string
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
	store   Store
	log     *slog.Logger
	opts    Options
	now     func() time.Time
	status  *statusCache
	limiter *rateLimiter
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
	return &Server{
		store:   store,
		log:     log,
		opts:    opts,
		now:     time.Now,
		status:  &statusCache{ttl: opts.StatusTTL},
		limiter: newRateLimiter(opts.StatusRate, time.Minute),
	}
}

// Handler returns the router wrapped in panic recovery, logging, headers and auth.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/status", s.publicStatus)
	mux.HandleFunc("GET /api/v1/checks", s.listChecks)
	mux.HandleFunc("POST /api/v1/checks", s.createCheck)
	mux.HandleFunc("GET /api/v1/checks/{id}", s.getCheck)
	mux.HandleFunc("PATCH /api/v1/checks/{id}", s.patchCheck)
	mux.HandleFunc("DELETE /api/v1/checks/{id}", s.deleteCheck)
	mux.HandleFunc("GET /api/v1/checks/{id}/results", s.listResults)
	mux.HandleFunc("GET /api/v1/checks/{id}/uptime", s.uptime)
	return s.recoverer(s.logRequests(securityHeaders(s.authenticate(mux))))
}

// ---- auth and headers ----

func isPublic(r *http.Request) bool {
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	return read && (r.URL.Path == "/api/v1/status" || r.URL.Path == "/api/v1/health")
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.opts.Token == "" {
		return next
	}
	want := []byte("Bearer " + s.opts.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isPublic(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="watchkeeper"`)
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !isPublic(r) {
			h.Set("Cache-Control", "no-store")
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
	out := make([]checkView, 0, len(checks))
	for _, c := range checks {
		out = append(out, view(c, recent[c.ID]))
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
	ExpectedStatus *int        `json:"expectedStatus"`
	Enabled        *bool       `json:"enabled"`
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
	if in.ExpectedStatus != nil {
		c.ExpectedStatus = in.ExpectedStatus
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
		s.log.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
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
