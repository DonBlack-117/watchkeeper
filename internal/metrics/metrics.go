// Package metrics exposes Prometheus metrics for checks, the HTTP API, alerts
// and the Go runtime. Names start with watchkeeper_.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

// Metrics owns a private registry, so tests can create as many as they need.
type Metrics struct {
	reg           *prometheus.Registry
	runs          *prometheus.CounterVec
	phases        *prometheus.HistogramVec
	httpRequests  *prometheus.CounterVec
	httpDuration  *prometheus.HistogramVec
	notifications *prometheus.CounterVec
	triages       *prometheus.CounterVec
	triageTokens  *prometheus.CounterVec
	checks        *checkCollector
}

// Sources are read on every scrape; nil ones are skipped.
type Sources struct {
	// Running lists the checks the scheduler runs now; per-check series of
	// other checks are dropped, so deleted checks disappear from Prometheus.
	Running func() []int64
	// SaveFailures is the number of results in a row that could not be saved.
	SaveFailures func() int64
	// OpenIncidents counts open incidents.
	OpenIncidents func(ctx context.Context) (int, error)
}

// Latency buckets from 10 ms to 30 s, the longest check timeout.
var durationBuckets = []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30}

// New registers every metric.
func New(src Sources) *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "watchkeeper_check_runs_total",
			Help: "Check runs by kind and result (ok or failed).",
		}, []string{"kind", "result"}),
		phases: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "watchkeeper_check_phase_duration_seconds",
			Help:    "Duration of each phase of successful and failed runs (dns, connect, tls, ttfb, total).",
			Buckets: durationBuckets,
		}, []string{"kind", "phase"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "watchkeeper_http_requests_total",
			Help: "API and frontend requests by route pattern, method and status code.",
		}, []string{"route", "method", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "watchkeeper_http_request_duration_seconds",
			Help:    "Time to serve a request, by route pattern.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}, []string{"route"}),
		notifications: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "watchkeeper_notifications_total",
			Help: "Alert deliveries by channel and result (sent, error, dropped).",
		}, []string{"channel", "result"}),
		triages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "watchkeeper_triage_runs_total",
			Help: "AI incident investigations by result (done or failed).",
		}, []string{"result"}),
		triageTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "watchkeeper_triage_tokens_total",
			Help: "Tokens used by AI incident investigations, by direction (input or output).",
		}, []string{"direction"}),
		checks: &checkCollector{running: src.Running, state: map[int64]checkState{}},
	}
	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.runs, m.phases, m.httpRequests, m.httpDuration, m.notifications,
		m.triages, m.triageTokens, m.checks,
	)
	if src.Running != nil {
		m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "watchkeeper_scheduler_running_checks",
			Help: "Checks with a running goroutine.",
		}, func() float64 { return float64(len(src.Running())) }))
	}
	if src.SaveFailures != nil {
		m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "watchkeeper_scheduler_save_failures",
			Help: "Results in a row that could not be saved; 0 when healthy.",
		}, func() float64 { return float64(src.SaveFailures()) }))
	}
	if src.OpenIncidents != nil {
		m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "watchkeeper_incidents_open",
			Help: "Open incidents; -1 when the database could not be read.",
		}, func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			n, err := src.OpenIncidents(ctx)
			if err != nil {
				return -1
			}
			return float64(n)
		}))
	}
	return m
}

// Handler serves the registry in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// ObserveRun records one saved result.
func (m *Metrics) ObserveRun(c check.Check, r check.Result) {
	result := "ok"
	if !r.OK {
		result = "failed"
	}
	kind := string(c.Kind)
	m.runs.WithLabelValues(kind, result).Inc()
	m.phases.WithLabelValues(kind, "total").Observe(float64(r.TotalMS) / 1000)
	for phase, v := range map[string]*int{"dns": r.DNSMS, "connect": r.ConnectMS, "tls": r.TLSMS, "ttfb": r.TTFBMS} {
		if v != nil {
			m.phases.WithLabelValues(kind, phase).Observe(float64(*v) / 1000)
		}
	}
	m.checks.set(c, r)
}

// ObserveHTTP records one served request. route is the ServeMux pattern, so
// the label set stays small no matter which ids are requested.
func (m *Metrics) ObserveHTTP(route, method string, code int, d time.Duration) {
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(code)).Inc()
	m.httpDuration.WithLabelValues(route).Observe(d.Seconds())
}

// ObserveTriage records one finished AI investigation.
func (m *Metrics) ObserveTriage(result string, inputTokens, outputTokens int) {
	m.triages.WithLabelValues(result).Inc()
	m.triageTokens.WithLabelValues("input").Add(float64(inputTokens))
	m.triageTokens.WithLabelValues("output").Add(float64(outputTokens))
}

// Notification implements notify.Recorder.
func (m *Metrics) Notification(channel, result string) {
	m.notifications.WithLabelValues(channel, result).Inc()
}

// ---- per-check gauges, built on every scrape from the last result ----

type checkState struct {
	name, kind string
	up         bool
	seconds    float64
	lastRun    time.Time
	certExpiry *time.Time
}

type checkCollector struct {
	running func() []int64
	mu      sync.Mutex
	state   map[int64]checkState
}

var (
	upDesc = prometheus.NewDesc("watchkeeper_check_up",
		"1 when the last run of the check passed, 0 when it failed.", []string{"check_id", "name", "kind"}, nil)
	durationDesc = prometheus.NewDesc("watchkeeper_check_last_duration_seconds",
		"Total duration of the last run.", []string{"check_id", "name", "kind"}, nil)
	lastRunDesc = prometheus.NewDesc("watchkeeper_check_last_run_timestamp_seconds",
		"Unix time of the last run.", []string{"check_id", "name", "kind"}, nil)
	certDesc = prometheus.NewDesc("watchkeeper_check_cert_expiry_timestamp_seconds",
		"Unix time when the certificate seen by the last run expires.", []string{"check_id", "name", "kind"}, nil)
)

func (c *checkCollector) set(ch check.Check, r check.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state[ch.ID] = checkState{
		name: ch.Name, kind: string(ch.Kind), up: r.OK,
		seconds: float64(r.TotalMS) / 1000, lastRun: r.StartedAt, certExpiry: r.CertExpiresAt,
	}
}

func (c *checkCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- upDesc
	ch <- durationDesc
	ch <- lastRunDesc
	ch <- certDesc
}

// Collect reads the scheduler before taking its own lock and sends after
// releasing it, so a slow scrape never holds up a check reporting a result.
// A check removed between the two steps is dropped on the next scrape.
func (c *checkCollector) Collect(ch chan<- prometheus.Metric) {
	var alive map[int64]bool
	if c.running != nil {
		alive = map[int64]bool{}
		for _, id := range c.running() {
			alive[id] = true
		}
	}
	c.mu.Lock()
	snapshot := make(map[int64]checkState, len(c.state))
	for id, s := range c.state {
		if alive != nil && !alive[id] {
			delete(c.state, id)
			continue
		}
		snapshot[id] = s
	}
	c.mu.Unlock()

	for id, s := range snapshot {
		labels := []string{strconv.FormatInt(id, 10), s.name, s.kind}
		up := 0.0
		if s.up {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, up, labels...)
		ch <- prometheus.MustNewConstMetric(durationDesc, prometheus.GaugeValue, s.seconds, labels...)
		ch <- prometheus.MustNewConstMetric(lastRunDesc, prometheus.GaugeValue, float64(s.lastRun.Unix()), labels...)
		if s.certExpiry != nil {
			ch <- prometheus.MustNewConstMetric(certDesc, prometheus.GaugeValue, float64(s.certExpiry.Unix()), labels...)
		}
	}
}
