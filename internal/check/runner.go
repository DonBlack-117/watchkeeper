package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// CertWarningDays fails TLS checks whose certificate expires sooner than this.
const CertWarningDays = 7

const (
	maxBody           = 1 << 20  // response bytes read before closing
	maxResponseHeader = 64 << 10 // response header bytes accepted
	maxRedirects      = 5
	maxErrorRunes     = 500 // longest error text stored per result
)

// Runner executes checks. The zero value is not usable; call NewRunner.
type Runner struct {
	// TLSConfig is cloned for every run; tests set RootCAs to trust httptest servers.
	TLSConfig *tls.Config
	Resolver  *net.Resolver
	Now       func() time.Time
	// AllowPrivate lets checks reach loopback and private networks. Keep it off
	// whenever people other than the owner can create checks.
	AllowPrivate bool
}

// NewRunner returns a Runner that uses the system roots and resolver.
func NewRunner(allowPrivate bool) *Runner {
	return &Runner{
		TLSConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
		Resolver:     net.DefaultResolver,
		Now:          time.Now,
		AllowPrivate: allowPrivate,
	}
}

// Run executes c once and always returns a result; failures are reported in it.
func (r *Runner) Run(ctx context.Context, c Check) Result {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout())
	defer cancel()

	start := r.Now()
	res := Result{CheckID: c.ID, StartedAt: start.UTC()}
	switch c.Kind {
	case KindHTTP:
		r.runHTTP(ctx, c, &res)
	case KindDNS:
		r.runDNS(ctx, c, &res)
	case KindTLS:
		r.runTLS(ctx, c, &res)
	default:
		res.Error = fmt.Sprintf("unknown check kind %q", c.Kind)
	}
	res.TotalMS = ms(r.Now().Sub(start))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && !res.OK {
		res.Error = fmt.Sprintf("timed out after %d ms", c.TimeoutMS)
	}
	res.Error = truncate(res.Error, maxErrorRunes)
	return res
}

func (r *Runner) runHTTP(ctx context.Context, c Check, res *Result) {
	// Trace callbacks run on the transport's goroutines, so the timings are
	// collected under a lock and copied into res once the request is done.
	var (
		mu                              sync.Mutex
		dnsStart, connStart, tlsStart   time.Time
		reqStart                        time.Time
		dnsMS, connectMS, tlsMS, ttfbMS *int
	)
	stamp := func(t *time.Time) { mu.Lock(); *t = r.Now(); mu.Unlock() }
	since := func(dst **int, from *time.Time) {
		mu.Lock()
		defer mu.Unlock()
		if !from.IsZero() {
			*dst = msPtr(r.Now().Sub(*from))
		}
	}
	trace := &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { stamp(&dnsStart) },
		DNSDone:      func(httptrace.DNSDoneInfo) { since(&dnsMS, &dnsStart) },
		ConnectStart: func(string, string) { stamp(&connStart) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				since(&connectMS, &connStart)
			}
		},
		TLSHandshakeStart: func() { stamp(&tlsStart) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				since(&tlsMS, &tlsStart)
			}
		},
		GotFirstResponseByte: func() { since(&ttfbMS, &reqStart) },
	}
	defer func() {
		mu.Lock()
		res.DNSMS, res.ConnectMS, res.TLSMS, res.TTFBMS = dnsMS, connectMS, tlsMS, ttfbMS
		mu.Unlock()
	}()

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, c.Target, nil)
	if err != nil {
		res.Error = "invalid request: " + err.Error()
		return
	}
	req.Header.Set("User-Agent", "Watchkeeper/0.1 (+https://github.com/DonBlack-117/watchkeeper)")

	// A fresh transport per run so every check measures DNS, connect and TLS again
	// instead of reusing a pooled connection. No proxy: it would bypass the dialer guard.
	transport := &http.Transport{
		TLSClientConfig:        r.TLSConfig.Clone(),
		DisableKeepAlives:      true,
		DialContext:            guardedDialer(r.Resolver, r.AllowPrivate).DialContext,
		MaxResponseHeaderBytes: maxResponseHeader,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: checkRedirect}

	stamp(&reqStart)
	resp, err := client.Do(req)
	if err != nil {
		res.Error = describeError(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody)); err != nil {
		res.Error = "reading body: " + describeError(err)
		return
	}

	code := resp.StatusCode
	res.StatusCode = &code
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		exp := resp.TLS.PeerCertificates[0].NotAfter.UTC()
		res.CertExpiresAt = &exp
	}

	if c.ExpectedStatus != nil {
		res.OK = code == *c.ExpectedStatus
		if !res.OK {
			res.Error = fmt.Sprintf("got status %d, want %d", code, *c.ExpectedStatus)
		}
		return
	}
	res.OK = code >= 200 && code < 400
	if !res.OK {
		res.Error = fmt.Sprintf("got status %d", code)
	}
}

var (
	errTooManyRedirects = fmt.Errorf("stopped after %d redirects", maxRedirects)
	errRedirectScheme   = errors.New("redirect to an unsupported scheme")
)

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errTooManyRedirects
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errRedirectScheme
	}
	return nil
}

func (r *Runner) runDNS(ctx context.Context, c Check, res *Result) {
	start := r.Now()
	addrs, err := r.Resolver.LookupHost(ctx, c.Target)
	res.DNSMS = msPtr(r.Now().Sub(start))
	if err != nil {
		res.Error = describeError(err)
		return
	}
	if len(addrs) == 0 {
		res.Error = "dns: no addresses returned"
		return
	}
	res.OK = true
}

func (r *Runner) runTLS(ctx context.Context, c Check, res *Result) {
	host, addr := TLSAddress(c.Target)
	cfg := r.TLSConfig.Clone()
	cfg.ServerName = host

	start := r.Now()
	dialer := &tls.Dialer{NetDialer: guardedDialer(r.Resolver, r.AllowPrivate), Config: cfg}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	res.TLSMS = msPtr(r.Now().Sub(start))
	if err != nil {
		res.Error = describeError(err)
		return
	}
	defer func() { _ = conn.Close() }()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		res.Error = "tls: unexpected connection type"
		return
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		res.Error = "tls: server sent no certificate"
		return
	}
	exp := certs[0].NotAfter.UTC()
	res.CertExpiresAt = &exp

	daysLeft := int(exp.Sub(r.Now()).Hours() / 24)
	if daysLeft < CertWarningDays {
		res.Error = fmt.Sprintf("certificate expires in %d days", daysLeft)
		return
	}
	res.OK = true
}

// describeError turns network errors into short categories. Raw Go errors carry
// resolved IPs and internal host names, which would let API users map the network.
func describeError(err error) string {
	var (
		unknown x509.UnknownAuthorityError
		invalid x509.CertificateInvalidError
		hostErr x509.HostnameError
		recHdr  tls.RecordHeaderError
		dnsErr  *net.DNSError
		netErr  net.Error
	)
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return ErrBlockedAddress.Error()
	case errors.Is(err, errTooManyRedirects), errors.Is(err, errRedirectScheme):
		return errors.Unwrap(err).Error()
	case errors.As(err, &unknown):
		return "tls: certificate signed by an unknown authority"
	case errors.As(err, &invalid):
		return "tls: invalid certificate (expired or not yet valid)"
	case errors.As(err, &hostErr):
		return "tls: certificate does not match the host name"
	case errors.As(err, &recHdr):
		return "tls: server did not answer with TLS"
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return "dns: no such host"
		}
		return "dns: lookup failed"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	default:
		return "request failed"
	}
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
