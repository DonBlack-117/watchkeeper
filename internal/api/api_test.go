package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

// memStore is an in-memory Store for handler tests.
type memStore struct {
	mu       sync.Mutex
	nextID   int64
	checks   map[int64]check.Check
	results  map[int64][]check.Result // newest first
	users    map[string]store.User
	sessions map[string]memSession
	pingErr  error

	sessionLookups int
	incidents      []store.Incident
	triages        map[int64]store.Triage // latest per incident
}

type memSession struct {
	userID  int64
	expires time.Time
}

func newMem() *memStore {
	return &memStore{checks: map[int64]check.Check{}, results: map[int64][]check.Result{},
		users: map[string]store.User{}, sessions: map[string]memSession{}}
}

func (m *memStore) Ping(context.Context) error { return m.pingErr }

func (m *memStore) CreateCheck(_ context.Context, c check.Check) (check.Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	c.ID, c.CreatedAt, c.UpdatedAt = m.nextID, time.Now(), time.Now()
	m.checks[c.ID] = c
	return c, nil
}

func (m *memStore) GetCheck(_ context.Context, id int64) (check.Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.checks[id]
	if !ok {
		return c, store.ErrNotFound
	}
	return c, nil
}

func (m *memStore) ListChecks(context.Context) ([]check.Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []check.Check{}
	for id := int64(1); id <= m.nextID; id++ {
		if c, ok := m.checks[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *memStore) CountChecks(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.checks), nil
}

func (m *memStore) UpdateCheck(_ context.Context, c check.Check) (check.Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.checks[c.ID]; !ok {
		return c, store.ErrNotFound
	}
	m.checks[c.ID] = c
	return c, nil
}

func (m *memStore) DeleteCheck(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.checks[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.checks, id)
	return nil
}

func (m *memStore) ListResults(_ context.Context, q store.ResultQuery) ([]check.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []check.Result{}
	for _, r := range m.results[q.CheckID] {
		if q.BeforeID != 0 && r.ID >= q.BeforeID {
			continue
		}
		if len(out) == q.Limit {
			break
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) RecentResults(_ context.Context, n int) (map[int64][]check.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64][]check.Result{}
	for id, rs := range m.results {
		out[id] = rs[:min(n, len(rs))]
	}
	return out, nil
}

func (m *memStore) DailyUptime(_ context.Context, _ int64, days int, now time.Time) ([]store.DayUptime, error) {
	out := make([]store.DayUptime, days)
	for i := range out {
		out[i].Day = now.AddDate(0, 0, i-days+1).Format(time.DateOnly)
	}
	return out, nil
}

func (m *memStore) UptimeSince(context.Context, time.Time) (map[int64]float64, error) {
	return map[int64]float64{1: 99.5}, nil
}

func (m *memStore) StatsSince(context.Context, time.Time) (map[int64]store.CheckStats, error) {
	p95 := 180.0
	return map[int64]store.CheckStats{1: {Uptime: 98, P95: &p95}}, nil
}

func (m *memStore) SummarySince(context.Context, time.Time) (store.Summary, error) {
	u, p95 := 99.0, 200.0
	return store.Summary{Total: 100, OK: 99, Uptime: &u, P95: &p95}, nil
}

func (m *memStore) DailyUptimeAll(_ context.Context, days int, now time.Time) (map[int64][]store.DayUptime, error) {
	series, _ := m.DailyUptime(context.Background(), 1, days, now)
	p := 100.0
	series[days-1].Percent = &p
	return map[int64][]store.DayUptime{1: series}, nil
}

func (m *memStore) HourlyLatency(_ context.Context, _ int64, from, to time.Time) ([]store.HourLatency, error) {
	out := []store.HourLatency{}
	for h := from; h.Before(to); h = h.Add(time.Hour) {
		out = append(out, store.HourLatency{Hour: h})
	}
	return out, nil
}

func (m *memStore) UserByName(_ context.Context, name string) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[name]; ok {
		return u, nil
	}
	return store.User{}, store.ErrNotFound
}

func (m *memStore) CreateSession(_ context.Context, hash []byte, userID int64, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(hash)] = memSession{userID, expires}
	return nil
}

func (m *memStore) SessionUser(_ context.Context, hash []byte, now time.Time) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionLookups++
	sess, ok := m.sessions[string(hash)]
	if !ok || !sess.expires.After(now) {
		return store.User{}, store.ErrNotFound
	}
	for _, u := range m.users {
		if u.ID == sess.userID {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (m *memStore) DeleteSession(_ context.Context, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(hash))
	return nil
}

func (m *memStore) ListIncidents(_ context.Context, q store.IncidentQuery) ([]store.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.Incident{}
	for _, inc := range m.incidents {
		if q.EnabledOnly && !m.checks[inc.CheckID].Enabled {
			continue
		}
		if (q.CheckID == 0 || inc.CheckID == q.CheckID) && (!q.OpenOnly || inc.ResolvedAt == nil) {
			out = append(out, inc)
		}
	}
	return out, nil
}

func (m *memStore) ResolveOpen(_ context.Context, checkID int64, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.incidents {
		if m.incidents[i].CheckID == checkID && m.incidents[i].ResolvedAt == nil {
			m.incidents[i].ResolvedAt = &at
			return true, nil
		}
	}
	return false, nil
}

func (m *memStore) CountOpenIncidents(context.Context) (int, error) {
	n := 0
	for _, inc := range m.incidents {
		if inc.ResolvedAt == nil {
			n++
		}
	}
	return n, nil
}

func (m *memStore) GetIncident(_ context.Context, id int64) (store.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inc := range m.incidents {
		if inc.ID == id {
			return inc, nil
		}
	}
	return store.Incident{}, store.ErrNotFound
}

func (m *memStore) LatestTriage(_ context.Context, incidentID int64) (store.Triage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.triages[incidentID]
	if !ok {
		return t, store.ErrNotFound
	}
	return t, nil
}

func newServer(m *memStore) http.Handler { return newServerWith(m, Options{}) }

func newServerWith(m *memStore, opts Options) http.Handler {
	s := New(m, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	s.now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response is not JSON: %s", rec.Body.String())
		}
	}
	return rec, out
}

const validBody = `{"name":"btc API","kind":"http","target":"https://api.btc-bot.dev/health"}`

func TestCreateAndGetCheck(t *testing.T) {
	h := newServer(newMem())

	rec, body := do(t, h, "POST", "/api/v1/checks", validBody)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/v1/checks/1" {
		t.Fatalf("create: %d %v", rec.Code, body)
	}
	data := body["data"].(map[string]any)
	if data["intervalS"].(float64) != 60 || data["timeoutMs"].(float64) != 10000 || data["enabled"] != true {
		t.Errorf("defaults not applied: %v", data)
	}
	if data["status"] != "unknown" {
		t.Errorf("a new check has no results yet: %v", data["status"])
	}

	rec, body = do(t, h, "GET", "/api/v1/checks/1", "")
	if rec.Code != http.StatusOK || body["data"].(map[string]any)["name"] != "btc API" {
		t.Fatalf("get: %d %v", rec.Code, body)
	}
}

func TestValidationProblem(t *testing.T) {
	h := newServer(newMem())
	rec, body := do(t, h, "POST", "/api/v1/checks", `{"name":"x","kind":"http","target":"nope","intervalS":5}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type = %s", ct)
	}
	errs := body["errors"].([]any)
	if len(errs) != 2 {
		t.Errorf("want target and intervalS errors, got %v", errs)
	}
}

func TestBadJSON(t *testing.T) {
	h := newServer(newMem())
	for _, b := range []string{`{`, `{"unknown":1}`, validBody + `{}`} {
		if rec, _ := do(t, h, "POST", "/api/v1/checks", b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", b, rec.Code)
		}
	}
}

func TestPatchAndDelete(t *testing.T) {
	h := newServer(newMem())
	do(t, h, "POST", "/api/v1/checks", validBody)

	rec, body := do(t, h, "PATCH", "/api/v1/checks/1", `{"enabled":false,"intervalS":120}`)
	data := body["data"].(map[string]any)
	if rec.Code != http.StatusOK || data["enabled"] != false || data["intervalS"].(float64) != 120 || data["name"] != "btc API" {
		t.Fatalf("patch: %d %v", rec.Code, body)
	}
	if rec, _ := do(t, h, "PATCH", "/api/v1/checks/1", `{"timeoutMs":999999}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid patch status = %d", rec.Code)
	}
	if rec, _ := do(t, h, "DELETE", "/api/v1/checks/1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d", rec.Code)
	}
	for _, p := range []string{"/api/v1/checks/1", "/api/v1/checks/abc", "/api/v1/checks/1/uptime"} {
		if rec, _ := do(t, h, "GET", p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d", p, rec.Code)
		}
	}
}

func TestResultsCursor(t *testing.T) {
	m := newMem()
	h := newServer(m)
	do(t, h, "POST", "/api/v1/checks", validBody)
	for id := int64(5); id >= 1; id-- {
		m.results[1] = append(m.results[1], check.Result{ID: id, CheckID: 1, OK: true})
	}

	_, body := do(t, h, "GET", "/api/v1/checks/1/results?limit=2", "")
	cursor, _ := body["meta"].(map[string]any)["nextCursor"].(string)
	if len(body["data"].([]any)) != 2 || cursor == "" {
		t.Fatalf("first page: %v", body)
	}
	_, body = do(t, h, "GET", "/api/v1/checks/1/results?limit=2&cursor="+cursor, "")
	first := body["data"].([]any)[0].(map[string]any)
	if first["id"].(float64) != 3 {
		t.Fatalf("second page should start at id 3: %v", body)
	}
	for _, q := range []string{"limit=0", "limit=9999", "cursor=not!base64", "from=yesterday"} {
		if rec, _ := do(t, h, "GET", "/api/v1/checks/1/results?"+q, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d", q, rec.Code)
		}
	}
}

func TestUptimeDays(t *testing.T) {
	h := newServer(newMem())
	do(t, h, "POST", "/api/v1/checks", validBody)
	_, body := do(t, h, "GET", "/api/v1/checks/1/uptime", "")
	days := body["data"].([]any)
	if len(days) != 90 || days[89].(map[string]any)["day"] != "2026-10-19" {
		t.Fatalf("default should be 90 days ending today: %d entries", len(days))
	}
	if rec, _ := do(t, h, "GET", "/api/v1/checks/1/uptime?days=91", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("days=91 status = %d", rec.Code)
	}
}

func TestStatus(t *testing.T) {
	m := newMem()
	h := newServer(m)
	do(t, h, "POST", "/api/v1/checks", validBody)
	do(t, h, "POST", "/api/v1/checks", `{"name":"dns","kind":"dns","target":"donblack.dev"}`)
	do(t, h, "POST", "/api/v1/checks", `{"name":"off","kind":"dns","target":"off.dev","enabled":false}`)
	m.results[1] = []check.Result{{ID: 2, CheckID: 1, OK: true, TotalMS: 100}, {ID: 1, CheckID: 1, OK: false}}

	_, body := do(t, h, "GET", "/api/v1/status", "")
	data := body["data"].(map[string]any)
	checks := data["checks"].([]any)
	if data["overall"] != "degraded" || len(checks) != 2 {
		t.Fatalf("status: %v", data)
	}
	first := checks[0].(map[string]any)
	if first["uptime90"].(float64) != 99.5 || first["status"] != "degraded" {
		t.Errorf("first check: %v", first)
	}
	if _, leaked := first["target"]; leaked {
		t.Error("the public status must not expose targets")
	}
}

func TestHealth(t *testing.T) {
	m := newMem()
	h := newServer(m)
	if rec, _ := do(t, h, "GET", "/api/v1/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("health = %d", rec.Code)
	}
	m.pingErr = errors.New("down")
	if rec, _ := do(t, h, "GET", "/api/v1/health", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health with db down = %d", rec.Code)
	}
}

func TestTokenAuth(t *testing.T) {
	token := strings.Repeat("a", 32)
	h := newServerWith(newMem(), Options{Token: token})

	if rec, _ := do(t, h, "GET", "/api/v1/checks", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d", rec.Code)
	}
	for _, p := range []string{"/api/v1/status", "/api/v1/health"} {
		head := httptest.NewRecorder()
		h.ServeHTTP(head, httptest.NewRequest("HEAD", p, nil))
		if head.Code != http.StatusOK {
			t.Errorf("HEAD %s must stay public, got %d", p, head.Code)
		}
		if rec, _ := do(t, h, "GET", p, ""); rec.Code != http.StatusOK {
			t.Errorf("%s must stay public, got %d", p, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/checks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("security headers missing: %v", rec.Header())
	}
}

func TestPrivateTargetsRejected(t *testing.T) {
	h := newServer(newMem())
	for _, target := range []string{"http://localhost:8080", "http://169.254.169.254/latest", "https://10.0.0.5"} {
		body := `{"name":"x","kind":"http","target":"` + target + `"}`
		if rec, _ := do(t, h, "POST", "/api/v1/checks", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", target, rec.Code)
		}
	}
	allowed := newServerWith(newMem(), Options{AllowPrivate: true})
	if rec, _ := do(t, allowed, "POST", "/api/v1/checks", `{"name":"x","kind":"http","target":"http://localhost:8080"}`); rec.Code != http.StatusCreated {
		t.Errorf("with AllowPrivate: status = %d", rec.Code)
	}
}

func TestMaxChecks(t *testing.T) {
	h := newServerWith(newMem(), Options{MaxChecks: 1})
	do(t, h, "POST", "/api/v1/checks", validBody)
	if rec, _ := do(t, h, "POST", "/api/v1/checks", validBody); rec.Code != http.StatusConflict {
		t.Fatalf("second check: status = %d, want 409", rec.Code)
	}
}

func TestStatusCacheAndRateLimit(t *testing.T) {
	m := newMem()
	h := newServerWith(m, Options{StatusRate: 3, StatusTTL: time.Hour})
	do(t, h, "POST", "/api/v1/checks", validBody)

	_, first := do(t, h, "GET", "/api/v1/status", "")
	do(t, h, "POST", "/api/v1/checks", `{"name":"new","kind":"dns","target":"donblack.dev"}`)
	rec, second := do(t, h, "GET", "/api/v1/status", "")
	if len(second["data"].(map[string]any)["checks"].([]any)) != len(first["data"].(map[string]any)["checks"].([]any)) {
		t.Error("status should be served from cache within the TTL")
	}
	if !strings.HasPrefix(rec.Header().Get("Cache-Control"), "public") {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	do(t, h, "GET", "/api/v1/status", "")
	if rec, _ := do(t, h, "GET", "/api/v1/status", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth request: status = %d, want 429", rec.Code)
	}
}

func TestHealthReportsScheduler(t *testing.T) {
	h := newServerWith(newMem(), Options{Health: func() error { return errors.New("check list not reloaded recently") }})
	rec, body := do(t, h, "GET", "/api/v1/health", "")
	if rec.Code != http.StatusServiceUnavailable || body["scheduler"] != "check list not reloaded recently" {
		t.Fatalf("health = %d %v", rec.Code, body)
	}
}

func TestLatencyWindow(t *testing.T) {
	h := newServer(newMem())
	do(t, h, "POST", "/api/v1/checks", validBody)
	_, body := do(t, h, "GET", "/api/v1/checks/1/latency", "")
	hours := body["data"].([]any)
	if len(hours) != 24 {
		t.Fatalf("default window: %d hours", len(hours))
	}
	if last := hours[23].(map[string]any)["hour"]; last != "2026-10-19T12:00:00Z" {
		t.Errorf("the window must end with the current hour, got %v", last)
	}
	for _, w := range []string{"30m", "90m", "200h", "x"} {
		if rec, _ := do(t, h, "GET", "/api/v1/checks/1/latency?window="+w, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("window=%s: status = %d", w, rec.Code)
		}
	}
}

func TestListAndSummaryStats(t *testing.T) {
	m := newMem()
	h := newServer(m)
	do(t, h, "POST", "/api/v1/checks", validBody)
	do(t, h, "POST", "/api/v1/checks", `{"name":"dns","kind":"dns","target":"donblack.dev"}`)
	m.results[1] = []check.Result{{ID: 1, CheckID: 1, OK: false}}

	_, body := do(t, h, "GET", "/api/v1/checks", "")
	first := body["data"].([]any)[0].(map[string]any)
	if first["uptime24h"].(float64) != 98 || first["p95Ms24h"].(float64) != 180 || len(first["daily"].([]any)) != 90 {
		t.Fatalf("list stats: %v", first)
	}
	second := body["data"].([]any)[1].(map[string]any)
	if _, ok := second["uptime24h"]; ok || len(second["daily"].([]any)) != 90 {
		t.Errorf("a check without runs has no stats but a full empty series: %v", second)
	}

	_, body = do(t, h, "GET", "/api/v1/summary", "")
	sum := body["data"].(map[string]any)
	if sum["checks"].(float64) != 2 || sum["down"].(float64) != 1 || sum["unknown"].(float64) != 1 || sum["uptime24h"].(float64) != 99 {
		t.Fatalf("summary: %v", sum)
	}

	_, body = do(t, h, "GET", "/api/v1/status", "")
	daily := body["data"].(map[string]any)["checks"].([]any)[0].(map[string]any)["daily"].([]any)
	if len(daily) != 90 || daily[89].(float64) != 100 {
		t.Errorf("status daily bars: %v", daily[85:])
	}
}

func TestPatchClearsExpectedStatus(t *testing.T) {
	h := newServer(newMem())
	do(t, h, "POST", "/api/v1/checks", `{"name":"a","kind":"http","target":"https://example.com","expectedStatus":204}`)

	_, body := do(t, h, "PATCH", "/api/v1/checks/1", `{"name":"renamed"}`)
	if body["data"].(map[string]any)["expectedStatus"].(float64) != 204 {
		t.Fatalf("a missing field must keep the value: %v", body)
	}
	rec, body := do(t, h, "PATCH", "/api/v1/checks/1", `{"kind":"dns","target":"example.com","expectedStatus":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("switch to dns with null expectedStatus: %d %v", rec.Code, body)
	}
	if _, ok := body["data"].(map[string]any)["expectedStatus"]; ok {
		t.Fatalf("null must clear expectedStatus: %v", body)
	}
}

func sampleIncidents() []store.Incident {
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Minute)
	return []store.Incident{
		{ID: 2, CheckID: 1, CheckName: "btc API", Status: check.StatusDown, StartedAt: t1,
			Updates: []store.IncidentUpdate{{At: t1, Status: "down", Error: "dial 10.0.0.5: timeout"}}},
		{ID: 1, CheckID: 1, CheckName: "btc API", Status: check.StatusDegraded, StartedAt: t0, ResolvedAt: &t1,
			Updates: []store.IncidentUpdate{{At: t0, Status: "degraded", Error: "timeout"}, {At: t1, Status: "resolved"}}},
	}
}

func TestIncidentsEndpointAndPublicStatus(t *testing.T) {
	m := newMem()
	h := newServer(m)
	do(t, h, "POST", "/api/v1/checks", validBody)
	m.incidents = sampleIncidents()

	_, body := do(t, h, "GET", "/api/v1/incidents?open=true", "")
	list := body["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["updates"].([]any)[0].(map[string]any)["error"] == nil {
		t.Fatalf("the dashboard sees open incidents with errors: %v", list)
	}
	for _, bad := range []string{"limit=0", "checkId=x", "open=maybe"} {
		if rec, _ := do(t, h, "GET", "/api/v1/incidents?"+bad, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d", bad, rec.Code)
		}
	}

	rec, body := do(t, h, "GET", "/api/v1/status", "")
	public := body["data"].(map[string]any)["incidents"].([]any)
	if len(public) != 2 || strings.Contains(rec.Body.String(), "10.0.0.5") || strings.Contains(rec.Body.String(), "timeout") {
		t.Fatalf("the status page must list incidents without errors: %s", rec.Body)
	}

	_, body = do(t, h, "GET", "/api/v1/summary", "")
	if body["data"].(map[string]any)["openIncidents"].(float64) != 1 {
		t.Errorf("summary: %v", body["data"])
	}
}

type fakeAlerts struct {
	channels []string
	err      error
	sent     []string
}

func (f *fakeAlerts) Channels() []string { return f.channels }
func (f *fakeAlerts) SendNow(_ context.Context, m notify.Message) error {
	f.sent = append(f.sent, m.Text)
	return f.err
}

func TestTestAlert(t *testing.T) {
	if rec, _ := do(t, newServer(newMem()), "POST", "/api/v1/alerts/test", ""); rec.Code != http.StatusConflict {
		t.Fatalf("without channels: %d", rec.Code)
	}
	a := &fakeAlerts{channels: []string{"telegram"}}
	h := newServerWith(newMem(), Options{Alerts: a})
	if rec, _ := do(t, h, "POST", "/api/v1/alerts/test", ""); rec.Code != http.StatusNoContent || len(a.sent) != 1 {
		t.Fatalf("test alert: %d %v", rec.Code, a.sent)
	}
	a.err = &notify.Permanent{Err: errors.New("telegram: 401 Unauthorized")}
	rec, body := do(t, h, "POST", "/api/v1/alerts/test", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(body["detail"].(string), "bot token") {
		t.Fatalf("failed test alert: %d %v", rec.Code, body)
	}
	_, body = do(t, h, "GET", "/api/v1/summary", "")
	if ch := body["data"].(map[string]any)["alertChannels"].([]any); len(ch) != 1 || ch[0] != "telegram" {
		t.Errorf("summary channels: %v", ch)
	}
	for range 5 {
		do(t, h, "POST", "/api/v1/alerts/test", "")
	}
	if rec, _ := do(t, h, "POST", "/api/v1/alerts/test", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("test alerts must be rate limited, got %d", rec.Code)
	}
}

type routes struct{ got []string }

func (r *routes) ObserveHTTP(route, method string, code int, _ time.Duration) {
	r.got = append(r.got, route+" "+method+" "+strconv.Itoa(code))
}

func TestHTTPMetricsUseRoutePatterns(t *testing.T) {
	obs := &routes{}
	front := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") })
	h := newServerWith(newMem(), Options{Metrics: obs, Frontend: front})
	do(t, h, "POST", "/api/v1/checks", validBody)
	do(t, h, "GET", "/api/v1/checks/1", "")
	do(t, h, "GET", "/api/v1/checks/999/uptime", "")
	do(t, h, "GET", "/status", "")
	want := []string{"POST /api/v1/checks POST 201", "GET /api/v1/checks/{id} GET 200", "GET /api/v1/checks/{id}/uptime GET 404", "frontend GET 200"}
	if strings.Join(obs.got, "|") != strings.Join(want, "|") {
		t.Fatalf("routes:\n got %v\nwant %v", obs.got, want)
	}
}

func TestDisablingACheckResolvesItsIncident(t *testing.T) {
	m := newMem()
	h := newServer(m)
	do(t, h, "POST", "/api/v1/checks", validBody)
	m.incidents = sampleIncidents()
	do(t, h, "PATCH", "/api/v1/checks/1", `{"name":"renamed"}`)
	if n, _ := m.CountOpenIncidents(context.Background()); n != 1 {
		t.Fatal("a rename must keep the incident open")
	}
	_, body := do(t, h, "GET", "/api/v1/status", "")
	if len(body["data"].(map[string]any)["incidents"].([]any)) != 2 {
		t.Fatal("incidents of enabled checks are public")
	}
	do(t, h, "PATCH", "/api/v1/checks/1", `{"enabled":false}`)
	if n, _ := m.CountOpenIncidents(context.Background()); n != 0 {
		t.Fatal("disabling a check must resolve its incident")
	}
	h = newServer(m) // fresh status cache
	_, body = do(t, h, "GET", "/api/v1/status", "")
	if len(body["data"].(map[string]any)["incidents"].([]any)) != 0 {
		t.Fatal("incidents of disabled checks must not be public")
	}
}

func TestMethodLabelIsBounded(t *testing.T) {
	obs := &routes{}
	h := newServerWith(newMem(), Options{Metrics: obs})
	for _, m := range []string{"FOO1", "FOO2", "PROPFIND"} {
		send(h, m, "/api/v1/checks", "", nil)
	}
	for _, got := range obs.got {
		if !strings.Contains(got, " OTHER ") {
			t.Fatalf("unknown methods must share one label: %v", obs.got)
		}
	}
}
