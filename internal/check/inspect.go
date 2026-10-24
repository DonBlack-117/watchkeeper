package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Host returns the host name a check talks to: the URL host for http, the
// target for dns, and the host part of host[:port] for tls.
func Host(c Check) string {
	switch c.Kind {
	case KindHTTP:
		if u, err := url.Parse(c.Target); err == nil {
			return u.Hostname()
		}
		return ""
	case KindTLS:
		h, _ := TLSAddress(c.Target)
		return h
	default:
		return c.Target
	}
}

// TLSEndpoint returns host and host:port of the TLS server behind a check, or
// ok=false when the check does not use TLS (dns, or http:// URLs).
func TLSEndpoint(c Check) (host, addr string, ok bool) {
	switch c.Kind {
	case KindTLS:
		host, addr = TLSAddress(c.Target)
		return host, addr, true
	case KindHTTP:
		u, err := url.Parse(c.Target)
		if err != nil || u.Scheme != "https" {
			return "", "", false
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		return u.Hostname(), net.JoinHostPort(u.Hostname(), port), true
	}
	return "", "", false
}

// DNSRecordTypes are the record types LookupDNS answers.
var DNSRecordTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT"}

// maxDNSAnswers caps the records returned per lookup.
const maxDNSAnswers = 20

// LookupDNS resolves one record type of host with the runner's resolver, like
// dig. Errors are short categories, as in check results.
func (r *Runner) LookupDNS(ctx context.Context, host, recordType string) ([]string, error) {
	var out []string
	var err error
	switch strings.ToUpper(recordType) {
	case "A", "AAAA":
		network := "ip4"
		if strings.EqualFold(recordType, "AAAA") {
			network = "ip6"
		}
		ips, lerr := r.Resolver.LookupIP(ctx, network, host)
		for _, ip := range ips {
			out = append(out, ip.String())
		}
		err = lerr
	case "CNAME":
		var cname string
		cname, err = r.Resolver.LookupCNAME(ctx, host)
		if cname != "" {
			out = append(out, cname)
		}
	case "MX":
		mxs, lerr := r.Resolver.LookupMX(ctx, host)
		for _, mx := range mxs {
			out = append(out, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
		}
		err = lerr
	case "NS":
		nss, lerr := r.Resolver.LookupNS(ctx, host)
		for _, ns := range nss {
			out = append(out, ns.Host)
		}
		err = lerr
	case "TXT":
		out, err = r.Resolver.LookupTXT(ctx, host)
		for i, t := range out {
			out[i] = truncate(t, 200)
		}
	default:
		return nil, fmt.Errorf("unsupported record type %q", recordType)
	}
	if err != nil {
		return nil, errors.New(describeError(err))
	}
	slices.Sort(out)
	if len(out) > maxDNSAnswers {
		out = out[:maxDNSAnswers]
	}
	return out, nil
}

// TLSInfo describes the certificate and connection a TLS server offers.
type TLSInfo struct {
	Version     string    `json:"version"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	DNSNames    []string  `json:"dnsNames"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	DaysLeft    int       `json:"daysLeft"`
	ChainLength int       `json:"chainLength"`
	// VerifyError is why the chain or host name failed verification; empty when valid.
	VerifyError string `json:"verifyError,omitempty"`
}

// InspectTLS connects to addr and reports the certificate, also when it does
// not verify, so an expired or mismatched certificate can be explained. The
// connection goes through the same address guard as the checks.
func (r *Runner) InspectTLS(ctx context.Context, host, addr string) (TLSInfo, error) {
	cfg := r.TLSConfig.Clone()
	cfg.ServerName = host
	// Verification runs below with the same roots, so a bad certificate is
	// reported instead of ending the handshake before it can be read.
	cfg.InsecureSkipVerify = true //nolint:gosec // the chain is verified by hand below; nothing is sent over this connection
	dialer := &tls.Dialer{NetDialer: guardedDialer(r.Resolver, r.AllowPrivate), Config: cfg}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return TLSInfo{}, errors.New(describeError(err))
	}
	defer func() { _ = conn.Close() }()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return TLSInfo{}, errors.New("tls: unexpected connection type")
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return TLSInfo{}, errors.New("tls: server sent no certificate")
	}
	leaf := state.PeerCertificates[0]
	info := TLSInfo{
		Version:     tls.VersionName(state.Version),
		Subject:     truncate(leaf.Subject.String(), 200),
		Issuer:      truncate(leaf.Issuer.String(), 200),
		DNSNames:    leaf.DNSNames,
		NotBefore:   leaf.NotBefore.UTC(),
		NotAfter:    leaf.NotAfter.UTC(),
		DaysLeft:    int(leaf.NotAfter.Sub(r.Now()).Hours() / 24),
		ChainLength: len(state.PeerCertificates),
	}
	if len(info.DNSNames) > maxDNSAnswers {
		info.DNSNames = info.DNSNames[:maxDNSAnswers]
	}
	opts := x509.VerifyOptions{DNSName: host, Roots: r.TLSConfig.RootCAs, Intermediates: x509.NewCertPool(), CurrentTime: r.Now()}
	for _, c := range state.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(opts); err != nil {
		info.VerifyError = describeError(err)
	}
	return info, nil
}
