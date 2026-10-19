// Package check defines what a check is, how it is validated and how it runs.
package check

import (
	"time"
)

// Kind is the protocol a check probes.
type Kind string

// Supported check kinds.
const (
	KindHTTP Kind = "http"
	KindDNS  Kind = "dns"
	KindTLS  Kind = "tls"
)

// Check is a probe configured by the user.
type Check struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Kind           Kind      `json:"kind"`
	Target         string    `json:"target"`
	IntervalS      int       `json:"intervalS"`
	TimeoutMS      int       `json:"timeoutMs"`
	ExpectedStatus *int      `json:"expectedStatus,omitempty"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Interval returns the time between two runs of the check.
func (c Check) Interval() time.Duration { return time.Duration(c.IntervalS) * time.Second }

// Timeout returns the maximum duration of a single run.
func (c Check) Timeout() time.Duration { return time.Duration(c.TimeoutMS) * time.Millisecond }

// Result is the outcome of one run. Phase timings are nil when the phase did not happen
// (for example, no TLS handshake on plain HTTP).
type Result struct {
	ID            int64      `json:"id"`
	CheckID       int64      `json:"checkId"`
	StartedAt     time.Time  `json:"startedAt"`
	OK            bool       `json:"ok"`
	StatusCode    *int       `json:"statusCode,omitempty"`
	TotalMS       int        `json:"totalMs"`
	DNSMS         *int       `json:"dnsMs,omitempty"`
	ConnectMS     *int       `json:"connectMs,omitempty"`
	TLSMS         *int       `json:"tlsMs,omitempty"`
	TTFBMS        *int       `json:"ttfbMs,omitempty"`
	CertExpiresAt *time.Time `json:"certExpiresAt,omitempty"`
	Error         string     `json:"error,omitempty"`
}

func ms(d time.Duration) int { return int(d.Milliseconds()) }

func msPtr(d time.Duration) *int {
	v := ms(d)
	return &v
}
