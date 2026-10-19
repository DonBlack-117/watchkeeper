package check

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits shared by validation and the database constraints.
const (
	MinIntervalS = 30
	MaxIntervalS = 86400
	MinTimeoutMS = 1000
	MaxTimeoutMS = 30000
	MaxTargetLen = 2048
)

// FieldError describes one invalid field, using the JSON field name.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Validate returns every problem with c. An empty slice means the check is valid.
func Validate(c Check) []FieldError {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	if n := utf8.RuneCountInString(strings.TrimSpace(c.Name)); n == 0 || n > 100 {
		add("name", "must be between 1 and 100 characters")
	} else if strings.IndexFunc(c.Name, unicode.IsControl) >= 0 {
		add("name", "must not contain control characters")
	}
	if len(c.Target) > MaxTargetLen {
		add("target", "must be at most "+strconv.Itoa(MaxTargetLen)+" bytes")
		return errs
	}

	switch c.Kind {
	case KindHTTP:
		if msg := validateHTTPTarget(c.Target); msg != "" {
			add("target", msg)
		}
	case KindDNS:
		if !validHostname(c.Target) {
			add("target", "must be a hostname such as example.com")
		}
	case KindTLS:
		if msg := validateTLSTarget(c.Target); msg != "" {
			add("target", msg)
		}
	default:
		add("kind", "must be one of http, dns, tls")
	}

	if c.IntervalS < MinIntervalS || c.IntervalS > MaxIntervalS {
		add("intervalS", "must be between "+strconv.Itoa(MinIntervalS)+" and "+strconv.Itoa(MaxIntervalS))
	}
	if c.TimeoutMS < MinTimeoutMS || c.TimeoutMS > MaxTimeoutMS {
		add("timeoutMs", "must be between "+strconv.Itoa(MinTimeoutMS)+" and "+strconv.Itoa(MaxTimeoutMS))
	} else if c.IntervalS >= MinIntervalS && c.TimeoutMS >= c.IntervalS*1000 {
		add("timeoutMs", "must be shorter than the interval")
	}

	if c.ExpectedStatus != nil {
		if c.Kind != KindHTTP {
			add("expectedStatus", "only applies to http checks")
		} else if *c.ExpectedStatus < 100 || *c.ExpectedStatus > 599 {
			add("expectedStatus", "must be a valid HTTP status code")
		}
	}
	return errs
}

func validateHTTPTarget(target string) string {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "must be an absolute http or https URL"
	}
	if u.User != nil {
		return "must not contain credentials"
	}
	return ""
}

func validateTLSTarget(target string) string {
	host, port := target, "443"
	if h, p, err := net.SplitHostPort(target); err == nil {
		host, port = h, p
	}
	if !validHostname(host) {
		return "must be host or host:port, such as example.com:443"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "port must be between 1 and 65535"
	}
	return ""
}

func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !isLabelChar(r) {
				return false
			}
		}
	}
	return true
}

func isLabelChar(r rune) bool {
	return r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// TLSAddress returns the host:port a TLS check dials, defaulting the port to 443.
func TLSAddress(target string) (host, addr string) {
	if h, _, err := net.SplitHostPort(target); err == nil {
		return h, target
	}
	return target, net.JoinHostPort(target, "443")
}
