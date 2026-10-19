package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

type statusCheck struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	Kind          check.Kind   `json:"kind"`
	Status        check.Status `json:"status"`
	Uptime90      *float64     `json:"uptime90"`
	LastCheckedAt *time.Time   `json:"lastCheckedAt"`
}

// statusCache keeps the encoded public status for a short time, so a burst of
// visitors costs one set of queries instead of three per request.
type statusCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	body    []byte
	expires time.Time
}

// publicStatus is the summary for a status page: names and states only, never
// targets, errors or timings.
func (s *Server) publicStatus(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r), s.now()) {
		w.Header().Set("Retry-After", "60")
		tooManyRequests(w)
		return
	}

	c := s.status
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if c.body == nil || now.After(c.expires) {
		body, err := s.buildStatus(r.Context(), now)
		if err != nil {
			s.fail(w, r, "build status", err)
			return
		}
		c.body, c.expires = body, now.Add(c.ttl)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(c.ttl.Seconds())))
	_, _ = w.Write(c.body)
}

func (s *Server) buildStatus(ctx context.Context, now time.Time) ([]byte, error) {
	checks, err := s.store.ListChecks(ctx)
	if err != nil {
		return nil, err
	}
	recent, err := s.store.RecentResults(ctx, 3)
	if err != nil {
		return nil, err
	}
	uptime, err := s.store.UptimeSince(ctx, now.Add(-statusLookback))
	if err != nil {
		return nil, err
	}

	out := []statusCheck{}
	var statuses []check.Status
	for _, c := range checks {
		if !c.Enabled {
			continue
		}
		sc := statusCheck{ID: c.ID, Name: c.Name, Kind: c.Kind, Status: check.StatusFrom(recent[c.ID])}
		if u, ok := uptime[c.ID]; ok {
			sc.Uptime90 = &u
		}
		if rs := recent[c.ID]; len(rs) > 0 {
			sc.LastCheckedAt = &rs[0].StartedAt
		}
		statuses = append(statuses, sc.Status)
		out = append(out, sc)
	}
	return json.Marshal(map[string]any{"data": map[string]any{
		"overall":   check.Overall(statuses),
		"updatedAt": now.UTC(),
		"checks":    out,
	}})
}

// rateLimiter is a fixed-window counter per client IP. It is small on purpose:
// one process, one public endpoint. Behind a reverse proxy, RemoteAddr is the
// proxy, so phase 2 must read the client IP from a trusted header instead.
type rateLimiter struct {
	limit  int
	window time.Duration
	mu     sync.Mutex
	start  time.Time
	counts map[string]int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, counts: map[string]int{}}
}

func (l *rateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.start) >= l.window {
		l.start, l.counts = now, map[string]int{}
	}
	l.counts[ip]++
	return l.counts[ip] <= l.limit
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
