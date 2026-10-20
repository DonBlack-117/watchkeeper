// Package scheduler runs every enabled check on its own interval.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

// Store is the part of the database the scheduler needs.
type Store interface {
	ListEnabledChecks(ctx context.Context) ([]check.Check, error)
	SaveResult(ctx context.Context, r check.Result) (check.Result, error)
}

// Runner executes a single check.
type Runner interface {
	Run(ctx context.Context, c check.Check) check.Result
}

// Options tunes the scheduler; zero values take the defaults.
type Options struct {
	MaxConcurrent  int           // checks running at the same time (default 10)
	ReloadInterval time.Duration // how often the check list is re-read (default 30s)
	// IntervalUnit converts IntervalS into a duration. Tests use time.Millisecond.
	IntervalUnit time.Duration
	// OnResult runs after each result is saved, in the check's goroutine:
	// metrics and incidents hook in here. It must not block for long.
	OnResult func(ctx context.Context, c check.Check, res check.Result)
}

// Scheduler keeps one goroutine per enabled check and reloads the list periodically,
// so checks created, changed or disabled through the API take effect without a restart.
type Scheduler struct {
	store  Store
	runner Runner
	log    *slog.Logger
	opts   Options
	sem    chan struct{}

	mu      sync.Mutex
	running map[int64]*job
	wg      sync.WaitGroup

	// Health signals read by the API.
	lastSync  atomic.Int64 // unix nanoseconds of the last successful reload
	saveFails atomic.Int64 // consecutive failed SaveResult calls
}

type job struct {
	check  check.Check
	cancel context.CancelFunc
}

// New builds a scheduler; call Run to start it.
func New(store Store, runner Runner, log *slog.Logger, opts Options) *Scheduler {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 10
	}
	if opts.ReloadInterval <= 0 {
		opts.ReloadInterval = 30 * time.Second
	}
	if opts.IntervalUnit <= 0 {
		opts.IntervalUnit = time.Second
	}
	return &Scheduler{
		store:   store,
		runner:  runner,
		log:     log,
		opts:    opts,
		sem:     make(chan struct{}, opts.MaxConcurrent),
		running: make(map[int64]*job),
	}
}

// Run blocks until ctx is cancelled, then waits for in-flight checks to finish.
func (s *Scheduler) Run(ctx context.Context) error {
	defer s.wg.Wait()
	defer s.stopAll()

	s.sync(ctx)
	ticker := time.NewTicker(s.opts.ReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sync(ctx)
		}
	}
}

// Running returns the ids of the checks that currently have a goroutine.
func (s *Scheduler) Running() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	return ids
}

func (s *Scheduler) sync(ctx context.Context) {
	checks, err := s.store.ListEnabledChecks(ctx)
	if err != nil {
		// Keep the current jobs running; a database hiccup should not stop monitoring.
		s.log.Error("reload checks", "err", err, "running", len(s.Running()),
			"last_success", time.Unix(0, s.lastSync.Load()).UTC())
		return
	}
	s.lastSync.Store(time.Now().UnixNano())

	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[int64]check.Check, len(checks))
	for _, c := range checks {
		wanted[c.ID] = c
	}
	for id, j := range s.running {
		if c, ok := wanted[id]; !ok || !sameSchedule(c, j.check) {
			j.cancel()
			delete(s.running, id)
		}
	}
	for id, c := range wanted {
		if _, ok := s.running[id]; ok {
			continue
		}
		jctx, cancel := context.WithCancel(ctx) //nolint:gosec // cancel is kept in s.running and called on reload or shutdown
		j := &job{check: c, cancel: cancel}
		s.running[id] = j
		s.wg.Add(1)
		go s.loop(jctx, j)
	}
}

func sameSchedule(a, b check.Check) bool {
	return a.Kind == b.Kind && a.Target == b.Target && a.IntervalS == b.IntervalS &&
		a.TimeoutMS == b.TimeoutMS && equalPtr(a.ExpectedStatus, b.ExpectedStatus) && a.Name == b.Name
}

func equalPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Scheduler) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.running {
		j.cancel()
		delete(s.running, id)
	}
}

// Healthy reports an error when checks are probably not being monitored: the
// check list has not been reloaded for three reload periods, or several results
// in a row could not be saved.
func (s *Scheduler) Healthy() error {
	if last := s.lastSync.Load(); last == 0 || time.Since(time.Unix(0, last)) > 3*s.opts.ReloadInterval {
		return errors.New("check list not reloaded recently")
	}
	if n := s.saveFails.Load(); n >= 3 {
		return fmt.Errorf("%d results in a row could not be saved", n)
	}
	return nil
}

// SaveFailures is the number of results in a row that could not be saved.
func (s *Scheduler) SaveFailures() int64 { return s.saveFails.Load() }

// forget removes j if it is still the registered job for its check, so the next
// reload starts a fresh goroutine.
func (s *Scheduler) forget(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[j.check.ID] == j {
		j.cancel()
		delete(s.running, j.check.ID)
	}
}

func (s *Scheduler) loop(ctx context.Context, j *job) {
	defer s.wg.Done()
	c := j.check
	defer func() {
		// A bug in a runner must not silently stop one check or crash the process.
		if v := recover(); v != nil {
			s.log.Error("check goroutine panicked; it restarts on the next reload",
				"check_id", c.ID, "name", c.Name, "panic", v, "stack", string(debug.Stack()))
			s.forget(j)
		}
	}()
	interval := time.Duration(c.IntervalS) * s.opts.IntervalUnit

	// Random initial delay so checks created together do not all fire at once.
	if !sleep(ctx, rand.N(interval)) { //nolint:gosec // jitter does not need a cryptographic source
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.runOnce(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, c check.Check) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	res := s.runner.Run(ctx, c)
	<-s.sem

	if ctx.Err() != nil {
		// Shutting down or the check changed mid-run; the cancelled result is not meaningful.
		s.log.Debug("result discarded after cancellation", "check_id", c.ID, "name", c.Name)
		return
	}
	// Saving uses its own deadline so a slow database cannot pile up goroutines.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	saved, err := s.store.SaveResult(saveCtx, res)
	if err != nil {
		fails := s.saveFails.Add(1)
		s.log.Error("save result", "check_id", c.ID, "name", c.Name, "ok", res.OK, "consecutive_failures", fails, "err", err)
		return
	}
	s.saveFails.Store(0)
	if s.opts.OnResult != nil {
		s.opts.OnResult(saveCtx, c, saved)
	}
	level := slog.LevelDebug
	if !res.OK {
		level = slog.LevelWarn
	}
	s.log.Log(ctx, level, "check run", "check_id", c.ID, "name", c.Name, "ok", res.OK, "total_ms", res.TotalMS, "error", res.Error)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
