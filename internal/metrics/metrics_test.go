package metrics

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}

func TestMetrics(t *testing.T) {
	running := []int64{1, 2}
	m := New(Sources{
		Running:       func() []int64 { return running },
		SaveFailures:  func() int64 { return 0 },
		OpenIncidents: func(context.Context) (int, error) { return 3, nil },
	})
	dns, exp := 12, time.Unix(1_800_000_000, 0)
	m.ObserveRun(check.Check{ID: 1, Name: "api", Kind: check.KindHTTP}, check.Result{OK: true, TotalMS: 250, DNSMS: &dns, StartedAt: time.Unix(1_700_000_000, 0), CertExpiresAt: &exp})
	m.ObserveRun(check.Check{ID: 2, Name: "dns", Kind: check.KindDNS}, check.Result{OK: false, TotalMS: 5000})
	m.ObserveHTTP("GET /api/v1/checks/{id}", "GET", 200, 3*time.Millisecond)
	m.Notification("telegram", "sent")

	out := scrape(t, m)
	for _, want := range []string{
		`watchkeeper_check_up{check_id="1",kind="http",name="api"} 1`,
		`watchkeeper_check_up{check_id="2",kind="dns",name="dns"} 0`,
		`watchkeeper_check_last_duration_seconds{check_id="1",kind="http",name="api"} 0.25`,
		`watchkeeper_check_cert_expiry_timestamp_seconds{check_id="1",kind="http",name="api"} 1.8e+09`,
		`watchkeeper_check_runs_total{kind="dns",result="failed"} 1`,
		`watchkeeper_check_phase_duration_seconds_count{kind="http",phase="dns"} 1`,
		`watchkeeper_http_requests_total{code="200",method="GET",route="GET /api/v1/checks/{id}"} 1`,
		`watchkeeper_notifications_total{channel="telegram",result="sent"} 1`,
		`watchkeeper_incidents_open 3`,
		`watchkeeper_scheduler_running_checks 2`,
		`go_goroutines`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}

	running = []int64{1} // check 2 was deleted or disabled
	if out := scrape(t, m); strings.Contains(out, `check_id="2"`) {
		t.Error("series of a check that no longer runs must disappear")
	}
}
