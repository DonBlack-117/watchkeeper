package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

type fakeStore struct {
	mu      sync.Mutex
	checks  []check.Check
	listErr error
	saved   []check.Result
}

func (f *fakeStore) ListEnabledChecks(context.Context) ([]check.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.checks), f.listErr
}

func (f *fakeStore) SaveResult(_ context.Context, r check.Result) (check.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, r)
	return r, nil
}

func (f *fakeStore) set(checks []check.Check, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks, f.listErr = checks, err
}

func (f *fakeStore) countFor(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.saved {
		if r.CheckID == id {
			n++
		}
	}
	return n
}

type fakeRunner struct {
	running, peak atomic.Int32
	delay         time.Duration
}

func (f *fakeRunner) Run(_ context.Context, c check.Check) check.Result {
	n := f.running.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(f.delay)
	f.running.Add(-1)
	return check.Result{CheckID: c.ID, OK: true}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// IntervalS of 20 with a millisecond unit runs every 20 ms.
func ck(id int64) check.Check {
	return check.Check{ID: id, Name: "c", Kind: check.KindHTTP, IntervalS: 20, TimeoutMS: 1000, Enabled: true}
}

func start(t *testing.T, s *Scheduler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler did not stop")
		}
	}
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestRunsChecksRepeatedly(t *testing.T) {
	st := &fakeStore{checks: []check.Check{ck(1), ck(2)}}
	s := New(st, &fakeRunner{}, quietLog(), Options{IntervalUnit: time.Millisecond, ReloadInterval: time.Hour})
	stop := start(t, s)
	defer stop()

	eventually(t, func() bool { return st.countFor(1) >= 3 && st.countFor(2) >= 3 }, "both checks should run several times")
}

func TestReloadAddsAndRemovesChecks(t *testing.T) {
	st := &fakeStore{checks: []check.Check{ck(1)}}
	s := New(st, &fakeRunner{}, quietLog(), Options{IntervalUnit: time.Millisecond, ReloadInterval: 20 * time.Millisecond})
	stop := start(t, s)
	defer stop()

	eventually(t, func() bool { return slices.Equal(s.Running(), []int64{1}) }, "check 1 should start")
	st.set([]check.Check{ck(2)}, nil)
	eventually(t, func() bool { return slices.Equal(s.Running(), []int64{2}) }, "check 1 should stop and check 2 start")
}

func TestReloadErrorKeepsJobs(t *testing.T) {
	st := &fakeStore{checks: []check.Check{ck(1)}}
	s := New(st, &fakeRunner{}, quietLog(), Options{IntervalUnit: time.Millisecond, ReloadInterval: 10 * time.Millisecond})
	stop := start(t, s)
	defer stop()

	eventually(t, func() bool { return len(s.Running()) == 1 }, "check should start")
	st.set(nil, errors.New("db down"))
	time.Sleep(50 * time.Millisecond)
	if len(s.Running()) != 1 {
		t.Fatal("a failed reload must not stop running checks")
	}
}

func TestConcurrencyLimit(t *testing.T) {
	var checks []check.Check
	for i := range 8 {
		checks = append(checks, ck(int64(i+1)))
	}
	st := &fakeStore{checks: checks}
	r := &fakeRunner{delay: 15 * time.Millisecond}
	s := New(st, r, quietLog(), Options{MaxConcurrent: 2, IntervalUnit: time.Millisecond, ReloadInterval: time.Hour})
	stop := start(t, s)

	eventually(t, func() bool { return st.countFor(8) >= 2 }, "all checks should get turns")
	stop()
	if p := r.peak.Load(); p > 2 {
		t.Fatalf("peak concurrency = %d, want at most 2", p)
	}
}

type panicRunner struct{ calls atomic.Int32 }

func (p *panicRunner) Run(_ context.Context, c check.Check) check.Result {
	if p.calls.Add(1) == 1 {
		panic("boom")
	}
	return check.Result{CheckID: c.ID, OK: true}
}

func TestPanicRestartsOnReload(t *testing.T) {
	st := &fakeStore{checks: []check.Check{ck(1)}}
	s := New(st, &panicRunner{}, quietLog(), Options{IntervalUnit: time.Millisecond, ReloadInterval: 20 * time.Millisecond})
	stop := start(t, s)
	defer stop()

	eventually(t, func() bool { return st.countFor(1) >= 2 }, "the check should keep running after a panic")
}

func TestHealthy(t *testing.T) {
	st := &fakeStore{checks: []check.Check{ck(1)}}
	s := New(st, &fakeRunner{}, quietLog(), Options{IntervalUnit: time.Millisecond, ReloadInterval: 10 * time.Millisecond})
	if s.Healthy() == nil {
		t.Fatal("a scheduler that never reloaded is not healthy")
	}
	stop := start(t, s)
	defer stop()
	eventually(t, func() bool { return s.Healthy() == nil }, "should be healthy after a reload")

	st.set(nil, errors.New("db down"))
	eventually(t, func() bool { return s.Healthy() != nil }, "should become unhealthy when reloads keep failing")
}
