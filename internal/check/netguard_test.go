package check

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"syscall"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.10", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "224.0.0.1", "255.255.255.255"}
	allowed := []string{"1.1.1.1", "140.82.112.3", "2606:4700:4700::1111"}
	for _, s := range blocked {
		if !IsBlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range allowed {
		if IsBlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestTargetPolicyError(t *testing.T) {
	cases := []struct {
		c       Check
		blocked bool
	}{
		{Check{Kind: KindHTTP, Target: "http://localhost:8080/x"}, true},
		{Check{Kind: KindHTTP, Target: "http://[::1]/"}, true},
		{Check{Kind: KindTLS, Target: "10.0.0.1:443"}, true},
		{Check{Kind: KindDNS, Target: "api.localhost"}, true},
		{Check{Kind: KindHTTP, Target: "https://example.com"}, false},
	}
	for _, tc := range cases {
		if got := TargetPolicyError(tc.c, false) != ""; got != tc.blocked {
			t.Errorf("%s: blocked = %v, want %v", tc.c.Target, got, tc.blocked)
		}
		if TargetPolicyError(tc.c, true) != "" {
			t.Errorf("%s: AllowPrivate must accept everything", tc.c.Target)
		}
	}
}

func TestRunnerBlocksPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	res := NewRunner(false).Run(context.Background(), httpCheck(srv.URL))
	if res.OK || res.Error != ErrBlockedAddress.Error() {
		t.Fatalf("loopback must be blocked at dial time, got OK=%v %q", res.OK, res.Error)
	}
	tlsCheck := Check{Kind: KindTLS, Target: strings.TrimPrefix(srv.URL, "http://"), TimeoutMS: 2000}
	if res := NewRunner(false).Run(context.Background(), tlsCheck); res.Error != ErrBlockedAddress.Error() {
		t.Fatalf("tls dial must be blocked too, got %q", res.Error)
	}
}

func TestRedirectLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()
	res := NewRunner(true).Run(context.Background(), httpCheck(srv.URL+"/"))
	if res.OK || !strings.Contains(res.Error, "redirects") {
		t.Fatalf("want a redirect limit error, got OK=%v %q", res.OK, res.Error)
	}
}

func TestDescribeErrorHidesDetails(t *testing.T) {
	err := &wrapped{msg: "dial tcp 10.0.0.5:5432: connect: connection refused", err: syscall.ECONNREFUSED}
	if got := describeError(err); got != "connection refused" {
		t.Fatalf("describeError = %q", got)
	}
	if got := describeError(errors.New("something with 10.0.0.5")); strings.Contains(got, "10.0.0.5") {
		t.Fatalf("internal address leaked: %q", got)
	}
	if got := truncate(strings.Repeat("é", 600), 500); len([]rune(got)) != 501 {
		t.Fatalf("truncate kept %d runes", len([]rune(got)))
	}
}

type wrapped struct {
	msg string
	err error
}

func (w *wrapped) Error() string { return w.msg }
func (w *wrapped) Unwrap() error { return w.err }
