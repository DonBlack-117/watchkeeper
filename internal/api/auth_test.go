package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/store"
)

const origin = "http://example.com" // httptest.NewRequest uses Host example.com

func withUser(t *testing.T, m *memStore, name, password string) {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	m.users[name] = store.User{ID: int64(len(m.users) + 1), Username: name, PasswordHash: hash}
}

// send is do with headers and cookies; it returns the raw recorder.
func send(h http.Handler, method, path, body string, header map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieHTTP || c.Name == sessionCookieHTTPS {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", rec.Header())
	return nil
}

func TestLoginSessionAndLogout(t *testing.T) {
	m := newMem()
	withUser(t, m, "diego", "correct horse battery")
	h := newServerWith(m, Options{RequireLogin: true, SecureCookie: true})
	json := map[string]string{"Content-Type": "application/json", "Origin": origin}

	if rec := send(h, "GET", "/api/v1/checks", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", rec.Code)
	}
	if rec := send(h, "POST", "/api/v1/auth/login", `{"username":"diego","password":"nope"}`, json); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if rec := send(h, "POST", "/api/v1/auth/login", `{"username":"ghost","password":"correct horse battery"}`, json); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: %d", rec.Code)
	}
	noOrigin := map[string]string{"Content-Type": "application/json"}
	if rec := send(h, "POST", "/api/v1/auth/login", `{"username":"diego","password":"correct horse battery"}`, noOrigin); rec.Code != http.StatusForbidden {
		t.Fatalf("login without Origin: %d", rec.Code)
	}

	rec := send(h, "POST", "/api/v1/auth/login", `{"username":"diego","password":"correct horse battery"}`, json)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := sessionFrom(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie flags: %+v", c)
	}
	if len(m.sessions) != 1 {
		t.Fatal("session not stored")
	}
	for k := range m.sessions {
		if k == c.Value {
			t.Error("the raw token must not be stored")
		}
	}

	if rec := send(h, "GET", "/api/v1/checks", "", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("with session: %d", rec.Code)
	}
	if rec := send(h, "GET", "/api/v1/auth/me", "", nil, c); !strings.Contains(rec.Body.String(), `"username":"diego"`) {
		t.Errorf("me: %s", rec.Body)
	}
	if rec := send(h, "POST", "/api/v1/checks", validBody, noOrigin, c); rec.Code != http.StatusForbidden {
		t.Fatalf("cookie write without Origin: %d", rec.Code)
	}
	cross := map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}
	if rec := send(h, "POST", "/api/v1/checks", validBody, cross, c); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write: %d", rec.Code)
	}
	if rec := send(h, "POST", "/api/v1/checks", validBody, json, c); rec.Code != http.StatusCreated {
		t.Fatalf("same-origin write: %d", rec.Code)
	}

	rec = send(h, "POST", "/api/v1/auth/logout", "", json, c)
	if rec.Code != http.StatusNoContent || sessionFrom(t, rec).MaxAge >= 0 {
		t.Fatalf("logout: %d %v", rec.Code, rec.Header())
	}
	if rec := send(h, "GET", "/api/v1/checks", "", nil, c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", rec.Code)
	}
}

func TestSessionExpires(t *testing.T) {
	m := newMem()
	withUser(t, m, "diego", "correct horse battery")
	s := New(m, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{RequireLogin: true, SessionTTL: time.Hour})
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	h := s.Handler()
	rec := send(h, "POST", "/api/v1/auth/login", `{"username":"diego","password":"correct horse battery"}`,
		map[string]string{"Origin": origin})
	c := sessionFrom(t, rec)
	now = now.Add(2 * time.Hour)
	if rec := send(h, "GET", "/api/v1/checks", "", nil, c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session: %d", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newServerWith(newMem(), Options{RequireLogin: true})
	var last int
	for range 11 {
		last = send(h, "POST", "/api/v1/auth/login", `{"username":"x","password":"y"}`, map[string]string{"Origin": origin}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("11th login = %d, want 429", last)
	}
}

func TestMeWithoutAuth(t *testing.T) {
	rec := send(newServer(newMem()), "GET", "/api/v1/auth/me", "", nil)
	if !strings.Contains(rec.Body.String(), `"authRequired":false`) || !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatalf("me: %s", rec.Body)
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	s := New(newMem(), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")},
	})
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:5000", "1.1.1.1", "203.0.113.9"},                  // untrusted peer: header ignored
		{"172.18.0.2:5000", "198.51.100.7", "198.51.100.7"},             // Caddy in front
		{"172.18.0.2:5000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},    // spoofed left entry ignored
		{"172.18.0.2:5000", "198.51.100.7, 172.18.0.3", "198.51.100.7"}, // chained trusted proxies
		{"172.18.0.2:5000", "", "172.18.0.2"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/v1/status", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientIP(r); got != c.want {
			t.Errorf("remote %s xff %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestFrontendAndUnknownAPI(t *testing.T) {
	front := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<!doctype html>") })
	h := newServerWith(newMem(), Options{Token: strings.Repeat("a", 32), Frontend: front})

	rec := send(h, "GET", "/status", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("frontend must be public and carry the CSP: %d %v", rec.Code, rec.Header())
	}
	rec = send(h, "GET", "/api/v1/nope", "", map[string]string{"Authorization": "Bearer " + strings.Repeat("a", 32)})
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unknown API path: %d %v", rec.Code, rec.Header())
	}
}

func TestHostPrefixedCookieOverHTTPS(t *testing.T) {
	m := newMem()
	withUser(t, m, "diego", "correct horse battery")
	h := newServerWith(m, Options{RequireLogin: true, SecureCookie: true})
	rec := send(h, "POST", "/api/v1/auth/login", `{"username":"diego","password":"correct horse battery"}`,
		map[string]string{"Origin": origin})
	if c := sessionFrom(t, rec); c.Name != "__Host-wk_session" || c.Domain != "" {
		t.Fatalf("cookie: %+v", c)
	}
}

func TestPublicEndpointsSkipSessionLookup(t *testing.T) {
	m := newMem()
	h := newServerWith(m, Options{RequireLogin: true})
	junk := &http.Cookie{Name: sessionCookieHTTP, Value: "junk"}
	for range 5 {
		send(h, "GET", "/api/v1/status", "", nil, junk)
		send(h, "GET", "/api/v1/health", "", nil, junk)
	}
	if m.sessionLookups != 0 {
		t.Fatalf("public endpoints looked up the session %d times", m.sessionLookups)
	}
	send(h, "GET", "/api/v1/auth/me", "", nil, junk)
	if m.sessionLookups != 1 {
		t.Fatalf("/auth/me must look up the session, got %d", m.sessionLookups)
	}
}
