package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

func httpCheck(target string) Check {
	return Check{ID: 1, Name: "test", Kind: KindHTTP, Target: target, IntervalS: 60, TimeoutMS: 2000}
}

func TestRunHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("fine"))
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	tests := []struct {
		name     string
		path     string
		expected *int
		timeout  int
		wantOK   bool
		wantCode int
		wantErr  string
	}{
		{name: "200 is up", path: "/ok", wantOK: true, wantCode: 200},
		{name: "500 is down", path: "/fail", wantCode: 500, wantErr: "got status 500"},
		{name: "redirect is followed", path: "/redirect", wantOK: true, wantCode: 200},
		{name: "expected status matches", path: "/teapot", expected: intPtr(418), wantOK: true, wantCode: 418},
		{name: "expected status differs", path: "/ok", expected: intPtr(204), wantCode: 200, wantErr: "want 204"},
		{name: "timeout", path: "/slow", timeout: 100, wantErr: "timed out after 100 ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := httpCheck(srv.URL + tt.path)
			c.ExpectedStatus = tt.expected
			if tt.timeout > 0 {
				c.TimeoutMS = tt.timeout
			}
			res := NewRunner(true).Run(context.Background(), c)

			if res.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (error %q)", res.OK, tt.wantOK, res.Error)
			}
			if tt.wantCode != 0 && (res.StatusCode == nil || *res.StatusCode != tt.wantCode) {
				t.Errorf("StatusCode = %v, want %d", res.StatusCode, tt.wantCode)
			}
			if !strings.Contains(res.Error, tt.wantErr) {
				t.Errorf("Error = %q, want it to contain %q", res.Error, tt.wantErr)
			}
			if res.CheckID != 1 || res.StartedAt.IsZero() {
				t.Errorf("result not linked to the check: %+v", res)
			}
		})
	}
}

func TestRunHTTPMeasuresPhases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	res := NewRunner(true).Run(context.Background(), httpCheck(srv.URL))
	if !res.OK {
		t.Fatalf("unexpected failure: %s", res.Error)
	}
	if res.ConnectMS == nil || res.TTFBMS == nil {
		t.Errorf("connect and TTFB should be measured: %+v", res)
	}
	if res.TLSMS != nil {
		t.Errorf("plain HTTP should not report a TLS phase")
	}
}

func trustingRunner(srv *httptest.Server) *Runner {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	r := NewRunner(true)
	r.TLSConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return r
}

func TestRunHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	t.Run("untrusted certificate fails", func(t *testing.T) {
		res := NewRunner(true).Run(context.Background(), httpCheck(srv.URL))
		if res.OK || !strings.Contains(res.Error, "certificate") {
			t.Fatalf("want a certificate error, got OK=%v %q", res.OK, res.Error)
		}
	})
	t.Run("trusted certificate passes and reports expiry", func(t *testing.T) {
		res := trustingRunner(srv).Run(context.Background(), httpCheck(srv.URL))
		if !res.OK {
			t.Fatalf("unexpected failure: %s", res.Error)
		}
		if res.TLSMS == nil || res.CertExpiresAt == nil {
			t.Errorf("TLS phase and certificate expiry should be set: %+v", res)
		}
	})
}

func TestRunTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	// httptest certificates are issued for "example.com" and 127.0.0.1.
	addr := strings.TrimPrefix(srv.URL, "https://")
	c := Check{ID: 2, Name: "tls", Kind: KindTLS, Target: addr, IntervalS: 60, TimeoutMS: 2000}

	t.Run("valid certificate", func(t *testing.T) {
		res := trustingRunner(srv).Run(context.Background(), c)
		if !res.OK || res.CertExpiresAt == nil {
			t.Fatalf("want OK with expiry, got %+v", res)
		}
	})
	t.Run("certificate about to expire", func(t *testing.T) {
		r := trustingRunner(srv)
		exp := srv.Certificate().NotAfter
		r.Now = func() time.Time { return exp.Add(-3 * 24 * time.Hour) }
		// Verification uses the real clock; only the expiry window uses r.Now.
		res := r.Run(context.Background(), c)
		if res.OK || !strings.Contains(res.Error, "expires in") {
			t.Fatalf("want an expiry warning, got OK=%v %q", res.OK, res.Error)
		}
	})
	t.Run("untrusted certificate", func(t *testing.T) {
		res := NewRunner(true).Run(context.Background(), c)
		if res.OK || !strings.Contains(res.Error, "certificate") {
			t.Fatalf("want a certificate error, got OK=%v %q", res.OK, res.Error)
		}
	})
}

func TestRunDNS(t *testing.T) {
	c := Check{ID: 3, Name: "dns", Kind: KindDNS, Target: "localhost", IntervalS: 60, TimeoutMS: 2000}
	res := NewRunner(true).Run(context.Background(), c)
	if !res.OK || res.DNSMS == nil {
		t.Fatalf("localhost should resolve: %+v", res)
	}

	c.Target = "does-not-exist.invalid"
	res = NewRunner(true).Run(context.Background(), c)
	if res.OK || res.Error == "" {
		t.Fatalf(".invalid must not resolve: %+v", res)
	}
}
