package triage

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

// Errors returned by Request.
var (
	ErrDisabled = errors.New("AI triage is not configured; set WK_ANTHROPIC_API_KEY")
	ErrLimit    = errors.New("the daily limit of investigations was reached")
	ErrBusy     = errors.New("too many investigations are queued; try again in a minute")
)

// Store is the persistence the service needs; *store.Store implements it.
type Store interface {
	Data
	GetCheck(ctx context.Context, id int64) (check.Check, error)
	GetIncident(ctx context.Context, id int64) (store.Incident, error)
	LatestTriage(ctx context.Context, incidentID int64) (store.Triage, error)
	StartTriage(ctx context.Context, incidentID int64, trigger, model string, at time.Time) (int64, error)
	FinishTriage(ctx context.Context, id int64, r store.TriageReport, at time.Time) error
	FailTriage(ctx context.Context, id int64, reason string, calls []store.ToolCall, inTok, outTok int, at time.Time) error
	CountTriagesSince(ctx context.Context, since time.Time) (int, error)
}

// Sender queues alerts; *notify.Dispatcher implements it.
type Sender interface {
	Enabled() bool
	Enqueue(m notify.Message) bool
}

// Observer records finished investigations; *metrics.Metrics implements it.
type Observer interface {
	ObserveTriage(result string, inputTokens, outputTokens int)
}

// Options tunes the service; zero values take the defaults noted below.
type Options struct {
	// Auto investigates every incident the first time it goes down.
	Auto bool
	// DailyLimit caps investigations started in any 24 hours (default 20).
	DailyLimit int
	// Workers is how many investigations run at once (default 2).
	Workers int
	// Timeout bounds one investigation (default 4 minutes).
	Timeout time.Duration
	// PublicURL, when set, adds a dashboard link to the diagnosis alert.
	PublicURL string
}

type job struct {
	triageID   int64
	incidentID int64
	trigger    string
}

// Service queues and runs investigations. A nil *Service is valid and disabled.
type Service struct {
	agent   *Agent
	store   Store
	prober  Prober
	sender  Sender
	metrics Observer
	log     *slog.Logger
	opts    Options
	now     func() time.Time
	queue   chan job
	mu      sync.Mutex // serializes the limit check with StartTriage
	done    chan struct{}
}

// NewService returns a Service. sender and metrics may be nil.
func NewService(agent *Agent, st Store, prober Prober, sender Sender, metrics Observer, log *slog.Logger, opts Options) *Service {
	if opts.DailyLimit <= 0 {
		opts.DailyLimit = 20
	}
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 4 * time.Minute
	}
	return &Service{agent: agent, store: st, prober: prober, sender: sender, metrics: metrics, log: log,
		opts: opts, now: time.Now, queue: make(chan job, 16), done: make(chan struct{})}
}

// Enabled reports whether investigations can run.
func (s *Service) Enabled() bool { return s != nil }

// Model is the model investigations use, or "" when disabled.
func (s *Service) Model() string {
	if s == nil {
		return ""
	}
	return s.agent.Model()
}

// Run processes the queue until ctx is cancelled and the running
// investigations have stopped.
func (s *Service) Run(ctx context.Context) {
	defer close(s.done)
	var wg sync.WaitGroup
	for range s.opts.Workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-s.queue:
					s.process(ctx, j)
				}
			}
		})
	}
	wg.Wait()
	// Whatever is still queued never ran; record why instead of leaving it running.
	for {
		select {
		case j := <-s.queue:
			s.failJob(j, &Failure{Reason: "Watchkeeper stopped before the investigation started"})
		default:
			return
		}
	}
}

// Done is closed when Run has returned.
func (s *Service) Done() <-chan struct{} { return s.done }

// Request starts an investigation of an incident and returns its triage id.
func (s *Service) Request(ctx context.Context, incidentID int64, trigger string) (int64, error) {
	if s == nil {
		return 0, ErrDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.store.CountTriagesSince(ctx, s.now().Add(-24*time.Hour))
	if err != nil {
		return 0, fmt.Errorf("count investigations: %w", err)
	}
	if n >= s.opts.DailyLimit {
		return 0, ErrLimit
	}
	id, err := s.store.StartTriage(ctx, incidentID, trigger, s.agent.Model(), s.now())
	if err != nil {
		return 0, err
	}
	select {
	case s.queue <- job{triageID: id, incidentID: incidentID, trigger: trigger}:
		return id, nil
	default:
		s.failJob(job{triageID: id, incidentID: incidentID}, &Failure{Reason: "the queue was full"})
		return 0, ErrBusy
	}
}

// IncidentDown is called when an incident reaches "down". With Auto set it
// investigates the incident once; later changes do not start another run.
func (s *Service) IncidentDown(ctx context.Context, incidentID int64) {
	if s == nil || !s.opts.Auto {
		return
	}
	if _, err := s.store.LatestTriage(ctx, incidentID); err == nil {
		return // already investigated
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Error("triage: latest", "incident_id", incidentID, "err", err)
		return
	}
	_, err := s.Request(ctx, incidentID, "auto")
	switch {
	case err == nil, errors.Is(err, store.ErrTriageRunning):
	case errors.Is(err, ErrLimit), errors.Is(err, ErrBusy):
		s.log.Warn("triage: skipped", "incident_id", incidentID, "reason", err.Error())
	default:
		s.log.Error("triage: request", "incident_id", incidentID, "err", err)
	}
}

func (s *Service) process(parent context.Context, j job) {
	ctx, cancel := context.WithTimeout(parent, s.opts.Timeout)
	defer cancel()
	start := s.now()
	inc, err := s.store.GetIncident(ctx, j.incidentID)
	if err != nil {
		s.failJob(j, &Failure{Reason: "could not read the incident"})
		return
	}
	c, err := s.store.GetCheck(ctx, inc.CheckID)
	if err != nil {
		s.failJob(j, &Failure{Reason: "could not read the check"})
		return
	}
	tools := &toolbox{check: c, data: s.store, prober: s.prober, now: s.now}
	report, err := s.agent.Investigate(ctx, c, inc, tools)
	if err != nil {
		var f *Failure
		if !errors.As(err, &f) {
			f = &Failure{Reason: err.Error()}
		}
		s.failJob(j, f)
		return
	}
	// The run's own context may have expired; saving gets a fresh budget.
	sctx, scancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer scancel()
	if err := s.store.FinishTriage(sctx, j.triageID, report, s.now()); err != nil {
		s.log.Error("triage: save", "triage_id", j.triageID, "err", err)
		return
	}
	s.observe("done", report.InputTokens, report.OutputTokens)
	s.log.Info("triage", "event", "done", "triage_id", j.triageID, "incident_id", j.incidentID, "check_id", c.ID,
		"confidence", report.Confidence, "tool_calls", len(report.ToolCalls),
		"input_tokens", report.InputTokens, "output_tokens", report.OutputTokens,
		"duration", s.now().Sub(start).Round(time.Millisecond).String())
	s.announce(sctx, j, c, report)
}

// announce sends the diagnosis after the "down" alert, so the owner gets the
// likely cause next to the alarm. Manual runs are read in the dashboard.
func (s *Service) announce(ctx context.Context, j job, c check.Check, r store.TriageReport) {
	if j.trigger != "auto" || s.sender == nil || !s.sender.Enabled() {
		return
	}
	inc, err := s.store.GetIncident(ctx, j.incidentID)
	if err != nil || !inc.Notified {
		return // nothing was announced, so the diagnosis stays quiet too
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 <b>%s</b>: likely cause (%s confidence)\n", esc(c.Name), r.Confidence)
	b.WriteString(esc(r.Summary))
	if len(r.NextSteps) > 0 {
		fmt.Fprintf(&b, "\nNext: %s", esc(r.NextSteps[0]))
	}
	if s.opts.PublicURL != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">Open dashboard</a>", html.EscapeString(s.opts.PublicURL))
	}
	s.sender.Enqueue(notify.Message{Text: b.String()})
}

func (s *Service) failJob(j job, f *Failure) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.FailTriage(ctx, j.triageID, truncate(f.Reason, 900), f.Calls, f.InputTokens, f.OutputTokens, s.now()); err != nil {
		s.log.Error("triage: save failure", "triage_id", j.triageID, "err", err)
	}
	s.observe("failed", f.InputTokens, f.OutputTokens)
	s.log.Warn("triage", "event", "failed", "triage_id", j.triageID, "incident_id", j.incidentID, "reason", f.Reason)
}

func (s *Service) observe(result string, in, out int) {
	if s.metrics != nil {
		s.metrics.ObserveTriage(result, in, out)
	}
}

// esc shortens and escapes a value for Telegram HTML.
func esc(s string) string {
	return html.EscapeString(truncate(s, 600))
}
