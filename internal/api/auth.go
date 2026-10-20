package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/DonBlack-117/watchkeeper/internal/store"
)

const (
	// Over HTTPS the cookie takes the __Host- prefix, which browsers only accept
	// with Secure, Path=/ and no Domain, so a subdomain cannot plant or shadow it.
	sessionCookieHTTPS = "__Host-wk_session"
	sessionCookieHTTP  = "wk_session"
	// bcrypt only reads the first 72 bytes; longer passwords are rejected instead of truncated.
	maxPasswordBytes = 72
	maxUsernameLen   = 64
)

// dummyHash is compared when the user does not exist, so a login takes the same
// time whether or not the name is valid.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("watchkeeper-timing-equalizer"), bcrypt.DefaultCost)

type ctxKey int

const userKey ctxKey = iota

// principal is who made the request: a dashboard user or the API token.
type principal struct {
	Username string
	Token    bool
}

func (s *Server) authEnabled() bool { return s.opts.Token != "" || s.opts.RequireLogin }

func (s *Server) cookieName() string {
	if s.opts.SecureCookie {
		return sessionCookieHTTPS
	}
	return sessionCookieHTTP
}

// isPublic lists what anyone may call: the status page, health, the login flow
// and the frontend files.
func isPublic(r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/") {
		return true
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch p {
	case "/api/v1/status", "/api/v1/health", "/api/v1/auth/me":
		return read
	case "/api/v1/auth/login", "/api/v1/auth/logout":
		return r.Method == http.MethodPost
	}
	return false
}

// authenticate accepts the bearer token or a session cookie. Requests that
// change state with a cookie must come from this origin, which together with
// SameSite=Strict blocks cross-site request forgery.
func (s *Server) authenticate(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.opts.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r) // frontend files: no session lookup per asset
			return
		}
		if s.opts.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) == 1 {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, principal{Token: true})))
			return
		}
		// Public endpoints other than /auth/me never need the user, so a cookie
		// sent to /status cannot turn each request into a database query.
		if isPublic(r) && r.URL.Path != "/api/v1/auth/me" {
			next.ServeHTTP(w, r)
			return
		}
		if u, ok := s.sessionUser(r); ok {
			if !safeMethod(r.Method) && !sameOrigin(r) {
				forbidden(w, "cross-origin request rejected")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, principal{Username: u.Username})))
			return
		}
		if s.authEnabled() && !isPublic(r) {
			if s.opts.Token != "" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="watchkeeper"`)
			}
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin checks the Origin header, which browsers send on every state-changing
// fetch, against the Host the request was sent to.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Server) sessionUser(r *http.Request) (store.User, bool) {
	c, err := r.Cookie(s.cookieName())
	if err != nil || c.Value == "" || len(c.Value) > 100 {
		return store.User{}, false
	}
	u, err := s.store.SessionUser(r.Context(), hashToken(c.Value), s.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.ErrorContext(r.Context(), "session lookup", "err", err)
		}
		return store.User{}, false
	}
	return u, true
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	if !s.loginLimiter.allow(s.clientIP(r), s.now()) {
		w.Header().Set("Retry-After", "60")
		tooManyRequests(w)
		return
	}
	var in loginInput
	if !decode(w, r, &in) {
		return
	}
	if in.Username == "" || len(in.Username) > maxUsernameLen || in.Password == "" || len(in.Password) > maxPasswordBytes {
		badCredentials(w)
		return
	}
	u, err := s.store.UserByName(r.Context(), in.Username)
	hash := []byte(u.PasswordHash)
	switch {
	case errors.Is(err, store.ErrNotFound):
		hash = dummyHash
	case err != nil:
		s.fail(w, r, "find user", err)
		return
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) != nil || u.ID == 0 {
		s.log.WarnContext(r.Context(), "failed login", "ip", s.clientIP(r))
		badCredentials(w)
		return
	}

	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // crypto/rand.Read never fails on supported platforms
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(s.opts.SessionTTL)
	if err := s.store.CreateSession(r.Context(), hashToken(token), u.ID, expires); err != nil {
		s.fail(w, r, "create session", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(),
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(s.opts.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.opts.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
	s.log.InfoContext(r.Context(), "login", "user", u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"username": u.Username}})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	if c, err := r.Cookie(s.cookieName()); err == nil && c.Value != "" && len(c.Value) <= 100 {
		if err := s.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			s.fail(w, r, "delete session", err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.opts.SecureCookie, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// me tells the frontend whether it must show the login form.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, ok := r.Context().Value(userKey).(principal)
	out := map[string]any{"authRequired": s.authEnabled(), "authenticated": ok || !s.authEnabled()}
	if ok && p.Username != "" {
		out["username"] = p.Username
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// PasswordMatches reports whether password is the one behind hash.
func PasswordMatches(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// HashPassword returns the bcrypt hash main stores for WK_ADMIN_PASSWORD.
func HashPassword(password string) (string, error) {
	if len(password) > maxPasswordBytes {
		return "", errors.New("password is longer than 72 bytes")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// sessionTTLDefault keeps a dashboard login for a working week.
const sessionTTLDefault = 7 * 24 * time.Hour
