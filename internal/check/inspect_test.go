package check

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostAndTLSEndpoint(t *testing.T) {
	cases := []struct {
		c          Check
		host, addr string
		tls        bool
	}{
		{Check{Kind: KindHTTP, Target: "https://api.example.com/health"}, "api.example.com", "api.example.com:443", true},
		{Check{Kind: KindHTTP, Target: "https://api.example.com:8443/"}, "api.example.com", "api.example.com:8443", true},
		{Check{Kind: KindHTTP, Target: "http://plain.example.com/"}, "plain.example.com", "", false},
		{Check{Kind: KindTLS, Target: "mail.example.com:993"}, "mail.example.com", "mail.example.com:993", true},
		{Check{Kind: KindTLS, Target: "example.com"}, "example.com", "example.com:443", true},
		{Check{Kind: KindDNS, Target: "example.org"}, "example.org", "", false},
	}
	for _, tc := range cases {
		if got := Host(tc.c); got != tc.host {
			t.Errorf("Host(%s) = %q, want %q", tc.c.Target, got, tc.host)
		}
		_, addr, ok := TLSEndpoint(tc.c)
		if ok != tc.tls || addr != tc.addr {
			t.Errorf("TLSEndpoint(%s) = %q %v, want %q %v", tc.c.Target, addr, ok, tc.addr, tc.tls)
		}
	}
}

func TestInspectTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")

	info, err := trustingRunner(srv).InspectTLS(context.Background(), "127.0.0.1", addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.VerifyError != "" || info.DaysLeft <= 0 || info.ChainLength < 1 || info.Version == "" {
		t.Errorf("trusted certificate: %+v", info)
	}

	// Untrusted: still described, with the reason it fails.
	info, err = NewRunner(true).InspectTLS(context.Background(), "127.0.0.1", addr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.VerifyError, "unknown authority") || info.Subject == "" {
		t.Errorf("untrusted certificate: %+v", info)
	}

	// Wrong host name.
	info, _ = trustingRunner(srv).InspectTLS(context.Background(), "other.test", addr)
	if !strings.Contains(info.VerifyError, "host name") {
		t.Errorf("mismatched host: %+v", info)
	}

	// The private-address guard applies as in checks.
	if _, err := NewRunner(false).InspectTLS(context.Background(), "127.0.0.1", addr); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("loopback must be blocked: %v", err)
	}
}

func TestLookupDNS(t *testing.T) {
	r := NewRunner(true)
	got, err := r.LookupDNS(context.Background(), "localhost", "A")
	if err != nil || len(got) == 0 {
		t.Fatalf("localhost A: %v %v", got, err)
	}
	if _, err := r.LookupDNS(context.Background(), "does-not-exist.invalid", "A"); err == nil || !strings.HasPrefix(err.Error(), "dns:") {
		t.Errorf("invalid name: %v", err)
	}
	if _, err := r.LookupDNS(context.Background(), "localhost", "SOA"); err == nil {
		t.Error("unsupported record type must fail")
	}
}
