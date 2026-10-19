package check

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// ErrBlockedAddress is returned when a check would connect to a private,
// loopback or otherwise internal address while those are not allowed.
var ErrBlockedAddress = errors.New("blocked: target resolves to a private or internal address")

// Ranges that IsPrivate, IsLoopback and the link-local helpers do not cover.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2001:db8::/32"),  // documentation
	netip.MustParsePrefix("255.255.255.255/32"),
}

// IsBlockedIP reports whether ip is an address a public monitor must not reach:
// loopback, private, link-local (including cloud metadata), multicast,
// unspecified and reserved ranges. IPv4-mapped IPv6 addresses are unwrapped first.
func IsBlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, p := range extraBlocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// guardedDialer returns a dialer that refuses internal addresses at connect time.
// Checking the connected IP (not the hostname) also covers redirects and DNS rebinding.
func guardedDialer(resolver *net.Resolver, allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Resolver: resolver}
	if allowPrivate {
		return d
	}
	d.Control = func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBlockedAddress, err)
		}
		if IsBlockedIP(ap.Addr()) {
			return ErrBlockedAddress
		}
		return nil
	}
	return d
}

// TargetPolicyError explains why a target is refused by policy, or returns ""
// when it is allowed. It catches the obvious cases at validation time; the dialer
// enforces the policy for every connection.
func TargetPolicyError(c Check, allowPrivate bool) string {
	if allowPrivate {
		return ""
	}
	host := c.Target
	switch c.Kind {
	case KindHTTP:
		u, err := url.Parse(c.Target)
		if err != nil {
			return ""
		}
		host = u.Hostname()
	case KindTLS:
		host, _ = TLSAddress(c.Target)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "loopback targets are disabled (set WK_ALLOW_PRIVATE_TARGETS=true for local use)"
	}
	if ip, err := netip.ParseAddr(host); err == nil && IsBlockedIP(ip) {
		return "private and internal addresses are disabled (set WK_ALLOW_PRIVATE_TARGETS=true for local use)"
	}
	return ""
}
