package check

import (
	"slices"
	"strings"
	"testing"
)

func valid() Check {
	return Check{Name: "API", Kind: KindHTTP, Target: "https://example.com/health", IntervalS: 60, TimeoutMS: 10000}
}

func fields(errs []FieldError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Field)
	}
	return out
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Check)
		want   []string
	}{
		{name: "valid http", mutate: func(*Check) {}},
		{name: "valid dns", mutate: func(c *Check) { c.Kind, c.Target = KindDNS, "donblack.dev" }},
		{name: "valid tls with port", mutate: func(c *Check) { c.Kind, c.Target = KindTLS, "example.com:8443" }},
		{name: "empty name", mutate: func(c *Check) { c.Name = "  " }, want: []string{"name"}},
		{name: "control characters in name", mutate: func(c *Check) { c.Name = "a\x07b" }, want: []string{"name"}},
		{name: "target too long", mutate: func(c *Check) { c.Target = "https://example.com/" + strings.Repeat("a", MaxTargetLen) }, want: []string{"target"}},
		{name: "unknown kind", mutate: func(c *Check) { c.Kind = "ftp" }, want: []string{"kind"}},
		{name: "http without scheme", mutate: func(c *Check) { c.Target = "example.com" }, want: []string{"target"}},
		{name: "http with credentials", mutate: func(c *Check) { c.Target = "https://u:p@example.com" }, want: []string{"target"}},
		{name: "dns with url", mutate: func(c *Check) { c.Kind, c.Target = KindDNS, "https://example.com" }, want: []string{"target"}},
		{name: "tls bad port", mutate: func(c *Check) { c.Kind, c.Target = KindTLS, "example.com:99999" }, want: []string{"target"}},
		{name: "interval too short", mutate: func(c *Check) { c.IntervalS = 10 }, want: []string{"intervalS"}},
		{name: "timeout too long", mutate: func(c *Check) { c.TimeoutMS = 60000 }, want: []string{"timeoutMs"}},
		{name: "timeout not shorter than interval", mutate: func(c *Check) { c.IntervalS, c.TimeoutMS = 30, 30000 }, want: []string{"timeoutMs"}},
		{name: "expected status out of range", mutate: func(c *Check) { c.ExpectedStatus = intPtr(42) }, want: []string{"expectedStatus"}},
		{name: "expected status on dns", mutate: func(c *Check) { c.Kind, c.Target, c.ExpectedStatus = KindDNS, "a.dev", intPtr(200) }, want: []string{"expectedStatus"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid()
			tt.mutate(&c)
			if got := fields(Validate(c)); len(got)+len(tt.want) > 0 && !slices.Equal(got, tt.want) {
				t.Errorf("fields = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusFrom(t *testing.T) {
	ok := Result{OK: true, TotalMS: 100}
	slow := Result{OK: true, TotalMS: SlowThresholdMS + 1}
	bad := Result{OK: false}

	tests := []struct {
		name   string
		recent []Result
		want   Status
	}{
		{"no data", nil, StatusUnknown},
		{"all ok", []Result{ok, ok, ok}, StatusOperational},
		{"one blip", []Result{ok, bad, ok}, StatusDegraded},
		{"latest slow", []Result{slow, ok}, StatusDegraded},
		{"all failed", []Result{bad, bad, bad}, StatusDown},
		{"only last three count", []Result{ok, ok, ok, bad, bad}, StatusOperational},
	}
	for _, tt := range tests {
		if got := StatusFrom(tt.recent); got != tt.want {
			t.Errorf("%s: StatusFrom = %s, want %s", tt.name, got, tt.want)
		}
	}

	if got := Overall([]Status{StatusOperational, StatusDegraded}); got != StatusDegraded {
		t.Errorf("Overall with a degraded service = %s", got)
	}
	if got := Overall([]Status{StatusDegraded, StatusDown}); got != StatusDown {
		t.Errorf("Overall with a down service = %s", got)
	}
}
