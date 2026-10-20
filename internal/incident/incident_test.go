package incident

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

// fakeStore keeps one incident per check and applies the same rules as the
// real ApplyStatus, which store tests cover against PostgreSQL.
type fakeStore struct {
	mu      sync.Mutex
	results map[int64][]check.Result // newest first
	open    map[int64]*store.Incident
	nextID  int64
	notices map[string]bool
}

func newFake() *fakeStore {
	return &fakeStore{results: map[int64][]check.Result{}, open: map[int64]*store.Incident{}, notices: map[string]bool{}}
}

func (f *fakeStore) RecentResultsFor(_ context.Context, id int64, n int, _ time.Time) ([]check.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rs := f.results[id]
	return rs[:min(n, len(rs))], nil
}

func (f *fakeStore) ApplyStatus(_ context.Context, id int64, st check.Status, at time.Time, _ string) (store.Transition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inc := f.open[id]
	switch {
	case st == check.StatusUnknown, st == check.StatusOperational && inc == nil:
		return store.Transition{}, nil
	case st == check.StatusOperational:
		delete(f.open, id)
		inc.ResolvedAt = &at
		return store.Transition{Kind: store.Resolved, Incident: *inc}, nil
	case inc == nil:
		f.nextID++
		f.open[id] = &store.Incident{ID: f.nextID, CheckID: id, Status: st, StartedAt: at}
		return store.Transition{Kind: store.Opened, Incident: *f.open[id]}, nil
	case inc.Status != st:
		inc.Status = st
		return store.Transition{Kind: store.Changed, Incident: *inc}, nil
	}
	return store.Transition{Kind: store.NoChange, Incident: *inc}, nil
}

func (f *fakeStore) ForgetCertNotice(_ context.Context, id int64, exp time.Time, days int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.notices, exp.String()+"/"+string(rune('0'+id))+"/"+time.Duration(days).String())
	return nil
}

func (f *fakeStore) MarkNotified(_ context.Context, incidentID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, inc := range f.open {
		if inc.ID == incidentID {
			inc.Notified = true
		}
	}
	return nil
}

func (f *fakeStore) RecordCertNotice(_ context.Context, id int64, exp time.Time, days int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := exp.String() + "/" + string(rune('0'+id)) + "/" + time.Duration(days).String()
	if f.notices[k] {
		return false, nil
	}
	f.notices[k] = true
	return true, nil
}

type fakeSender struct {
	msgs []string
	full bool // simulate a full queue
}

func (s *fakeSender) Enabled() bool { return true }
func (s *fakeSender) Enqueue(m notify.Message) bool {
	if s.full {
		return false
	}
	s.msgs = append(s.msgs, m.Text)
	return true
}

var c1 = check.Check{ID: 1, Name: "API <prod>", Kind: check.KindHTTP, Target: "https://api.example.com/?a=1&b=2"}

// run records a result and processes it, like the scheduler does.
func run(m *Manager, f *fakeStore, ok bool, ms int, at time.Time) {
	r := check.Result{CheckID: c1.ID, OK: ok, TotalMS: ms, StartedAt: at}
	if !ok {
		r.Error = "timeout"
	}
	f.results[c1.ID] = append([]check.Result{r}, f.results[c1.ID]...)
	m.Process(context.Background(), c1, r)
}

func newManager(opts Options) (*Manager, *fakeStore, *fakeSender) {
	f, s := newFake(), &fakeSender{}
	return New(f, s, slog.New(slog.NewTextHandler(io.Discard, nil)), opts), f, s
}

func TestAlertsOnlyWhenDownAndThenOnRecovery(t *testing.T) {
	m, f, s := newManager(Options{PublicURL: "https://wk.example.com"})
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	run(m, f, true, 100, t0)
	run(m, f, false, 0, t0.Add(time.Minute)) // degraded: incident opens, no alert
	if len(s.msgs) != 0 || f.open[1] == nil || f.open[1].Status != check.StatusDegraded {
		t.Fatalf("degraded must open an incident silently: %v %+v", s.msgs, f.open[1])
	}
	run(m, f, false, 0, t0.Add(2*time.Minute))
	run(m, f, false, 0, t0.Add(3*time.Minute)) // three failures: down
	if len(s.msgs) != 1 || !strings.Contains(s.msgs[0], "is down") {
		t.Fatalf("down must alert once: %v", s.msgs)
	}
	if !strings.Contains(s.msgs[0], "API &lt;prod&gt;") || !strings.Contains(s.msgs[0], "a=1&amp;b=2") {
		t.Errorf("names and targets must be HTML-escaped: %s", s.msgs[0])
	}
	run(m, f, false, 0, t0.Add(4*time.Minute))
	if len(s.msgs) != 1 {
		t.Fatalf("a still-down check must not alert again: %v", s.msgs)
	}
	run(m, f, true, 100, t0.Add(5*time.Minute)) // degraded again: no message
	run(m, f, true, 100, t0.Add(6*time.Minute))
	run(m, f, true, 100, t0.Add(7*time.Minute)) // operational: resolved
	if len(s.msgs) != 2 || !strings.Contains(s.msgs[1], "operational again") || !strings.Contains(s.msgs[1], "6 min") {
		t.Fatalf("recovery must alert with the duration: %v", s.msgs)
	}
	if !strings.Contains(s.msgs[1], `href="https://wk.example.com"`) {
		t.Errorf("missing dashboard link: %s", s.msgs[1])
	}
}

func TestShortBlipNeverAlerts(t *testing.T) {
	m, f, s := newManager(Options{})
	t0 := time.Now()
	run(m, f, true, 100, t0)
	run(m, f, false, 0, t0.Add(time.Minute))
	for i := 2; i <= 4; i++ {
		run(m, f, true, 100, t0.Add(time.Duration(i)*time.Minute))
	}
	if len(s.msgs) != 0 || len(f.open) != 0 {
		t.Fatalf("a degraded blip must resolve without alerts: %v", s.msgs)
	}
}

func TestAlertOnDegraded(t *testing.T) {
	m, f, s := newManager(Options{AlertOnDegraded: true})
	t0 := time.Now()
	run(m, f, true, 100, t0)
	run(m, f, true, 3000, t0.Add(time.Minute)) // slow: degraded
	if len(s.msgs) != 1 || !strings.Contains(s.msgs[0], "is degraded") {
		t.Fatalf("degraded alert: %v", s.msgs)
	}
}

func TestCertWarningsOncePerThreshold(t *testing.T) {
	m, f, s := newManager(Options{})
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	exp := now.Add(20 * 24 * time.Hour)
	tls := check.Check{ID: 2, Name: "TLS", Kind: check.KindTLS, Target: "example.com:443"}
	res := check.Result{CheckID: 2, OK: true, TotalMS: 50, StartedAt: now, CertExpiresAt: &exp}
	f.results[2] = []check.Result{res}

	m.Process(context.Background(), tls, res)
	m.Process(context.Background(), tls, res)
	if len(s.msgs) != 1 || !strings.Contains(s.msgs[0], "expires in 20 days") {
		t.Fatalf("30-day warning once: %v", s.msgs)
	}
	now = now.Add(7 * 24 * time.Hour) // 13 days left: crosses the 14-day threshold
	m.Process(context.Background(), tls, res)
	if len(s.msgs) != 2 || !strings.Contains(s.msgs[1], "expires in 13 days") {
		t.Fatalf("14-day warning: %v", s.msgs)
	}
	renewed := exp.Add(90 * 24 * time.Hour)
	res.CertExpiresAt = &renewed
	m.Process(context.Background(), tls, res)
	if len(s.msgs) != 2 {
		t.Fatalf("a renewed certificate far from expiry must not warn: %v", s.msgs)
	}
}

func TestHuman(t *testing.T) {
	for d, want := range map[time.Duration]string{40 * time.Second: "40 s", 6 * time.Minute: "6 min", 125 * time.Minute: "2 h 5 min", 50 * time.Hour: "2 days 2 h"} {
		if got := human(d); got != want {
			t.Errorf("human(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLostAlertIsRetriedOnTheNextRun(t *testing.T) {
	m, f, s := newManager(Options{})
	t0 := time.Now()
	s.full = true
	for i := range 3 {
		run(m, f, false, 0, t0.Add(time.Duration(i)*time.Minute))
	}
	if len(s.msgs) != 0 || f.open[1].Notified {
		t.Fatal("nothing can be sent while the queue is full")
	}
	s.full = false
	run(m, f, false, 0, t0.Add(3*time.Minute))
	if len(s.msgs) != 1 || !strings.Contains(s.msgs[0], "is down") || !f.open[1].Notified {
		t.Fatalf("the down alert must go out on the next run: %v", s.msgs)
	}
	run(m, f, false, 0, t0.Add(4*time.Minute))
	if len(s.msgs) != 1 {
		t.Fatal("and only once")
	}
}

func TestCertWarningRetriedWhenNotQueued(t *testing.T) {
	m, f, s := newManager(Options{})
	now := time.Now()
	m.now = func() time.Time { return now }
	exp := now.Add(10 * 24 * time.Hour)
	tls := check.Check{ID: 2, Name: "TLS", Kind: check.KindTLS, Target: "example.com:443"}
	res := check.Result{CheckID: 2, OK: true, TotalMS: 50, StartedAt: now, CertExpiresAt: &exp}
	f.results[2] = []check.Result{res}
	s.full = true
	m.Process(context.Background(), tls, res)
	s.full = false
	m.Process(context.Background(), tls, res)
	if len(s.msgs) != 1 {
		t.Fatalf("a warning that could not be queued must be retried: %v", s.msgs)
	}
}

func TestLongValuesAreCut(t *testing.T) {
	m, _, _ := newManager(Options{})
	long := check.Check{ID: 3, Name: "n", Kind: check.KindHTTP, Target: "https://x/?" + strings.Repeat("&", 2000)}
	text := m.openedText(long, store.Incident{Status: check.StatusDown, StartedAt: time.Now()}, check.Result{Error: strings.Repeat("<", 500)})
	if len([]rune(text)) > 4000 {
		t.Fatalf("message has %d characters", len([]rune(text)))
	}
}
