package triage

import (
	"context"
	"errors"
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

type memStore struct {
	fakeData
	mu       sync.Mutex
	incident store.Incident
	triages  map[int64]*store.Triage
	nextID   int64
	finished chan int64
}

func newStore() *memStore {
	return &memStore{incident: testIncident(), triages: map[int64]*store.Triage{}, finished: make(chan int64, 10)}
}

func (m *memStore) GetCheck(context.Context, int64) (check.Check, error) { return testCheck(), nil }

func (m *memStore) GetIncident(_ context.Context, id int64) (store.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.incident.ID {
		return store.Incident{}, store.ErrNotFound
	}
	return m.incident, nil
}

func (m *memStore) LatestTriage(_ context.Context, incidentID int64) (store.Triage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *store.Triage
	for _, t := range m.triages {
		if t.IncidentID == incidentID && (latest == nil || t.ID > latest.ID) {
			latest = t
		}
	}
	if latest == nil {
		return store.Triage{}, store.ErrNotFound
	}
	return *latest, nil
}

func (m *memStore) StartTriage(_ context.Context, incidentID int64, trigger, model string, at time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if incidentID != m.incident.ID {
		return 0, store.ErrNotFound
	}
	for _, t := range m.triages {
		if t.IncidentID == incidentID && t.State == store.TriageRunning {
			return 0, store.ErrTriageRunning
		}
	}
	m.nextID++
	m.triages[m.nextID] = &store.Triage{ID: m.nextID, IncidentID: incidentID, State: store.TriageRunning,
		Trigger: trigger, Model: model, StartedAt: at}
	return m.nextID, nil
}

func (m *memStore) FinishTriage(_ context.Context, id int64, r store.TriageReport, _ time.Time) error {
	m.mu.Lock()
	t := m.triages[id]
	t.State, t.Summary, t.Confidence, t.ToolCalls = store.TriageDone, r.Summary, r.Confidence, r.ToolCalls
	m.mu.Unlock()
	m.finished <- id
	return nil
}

func (m *memStore) FailTriage(_ context.Context, id int64, reason string, calls []store.ToolCall, _, _ int, _ time.Time) error {
	m.mu.Lock()
	t := m.triages[id]
	t.State, t.Error, t.ToolCalls = store.TriageFailed, reason, calls
	m.mu.Unlock()
	m.finished <- id
	return nil
}

func (m *memStore) CountTriagesSince(context.Context, time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.triages), nil
}

type fakeSender struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (s *fakeSender) Enabled() bool { return true }
func (s *fakeSender) Enqueue(m notify.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return true
}

type fakeObserver struct {
	mu      sync.Mutex
	results []string
}

func (o *fakeObserver) ObserveTriage(result string, _, _ int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, result)
}

func newService(st *memStore, msgs *fakeMessages, sender Sender, obs Observer, opts Options) *Service {
	agent := NewAgent(msgs, "claude-opus-5-5", "high")
	return NewService(agent, st, &fakeProber{}, sender, obs, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
}

func wait(t *testing.T, st *memStore) int64 {
	t.Helper()
	select {
	case id := <-st.finished:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("the investigation did not finish")
		return 0
	}
}

func TestServiceAutoTriage(t *testing.T) {
	st := newStore()
	st.incident.Notified = true
	msgs := &fakeMessages{responses: []string{toolUse("end_turn", textBlock(finalJSON))}}
	sender := &fakeSender{}
	obs := &fakeObserver{}
	svc := newService(st, msgs, sender, obs, Options{Auto: true, PublicURL: "https://wk.example.com"})
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)

	svc.IncidentDown(ctx, 9)
	id := wait(t, st)
	tr, _ := st.LatestTriage(ctx, 9)
	if tr.ID != id || tr.State != store.TriageDone || tr.Trigger != "auto" || tr.Model != "claude-opus-5-5" {
		t.Errorf("triage = %+v", tr)
	}
	// The alert is queued right after the result is saved.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		sender.mu.Lock()
		n := len(sender.msgs)
		sender.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
	}
	sender.mu.Lock()
	if len(sender.msgs) != 1 || !strings.Contains(sender.msgs[0].Text, "<b>API</b>: likely cause (high confidence)") ||
		!strings.Contains(sender.msgs[0].Text, "Next: Restore the A record") || !strings.Contains(sender.msgs[0].Text, "https://wk.example.com") {
		t.Errorf("alert = %+v", sender.msgs)
	}
	sender.mu.Unlock()

	// A second "down" for the same incident does not start another run.
	svc.IncidentDown(ctx, 9)
	if n, _ := st.CountTriagesSince(ctx, time.Time{}); n != 1 {
		t.Errorf("%d investigations, want 1", n)
	}
	cancel()
	<-svc.Done()
	if len(obs.results) != 1 || obs.results[0] != "done" {
		t.Errorf("observed %v", obs.results)
	}
}

func TestServiceManualIsQuiet(t *testing.T) {
	st := newStore()
	st.incident.Notified = true
	sender := &fakeSender{}
	svc := newService(st, &fakeMessages{responses: []string{toolUse("end_turn", textBlock(finalJSON))}}, sender, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	svc.IncidentDown(ctx, 9) // Auto is off
	if n, _ := st.CountTriagesSince(ctx, time.Time{}); n != 0 {
		t.Fatal("IncidentDown ran without Auto")
	}
	if _, err := svc.Request(ctx, 9, "manual"); err != nil {
		t.Fatal(err)
	}
	wait(t, st)
	if len(sender.msgs) != 0 {
		t.Errorf("manual runs send no alert, got %v", sender.msgs)
	}
}

func TestServiceLimitsAndFailures(t *testing.T) {
	st := newStore()
	svc := newService(st, &fakeMessages{responses: []string{toolUse("end_turn", textBlock("nope"))}}, nil, nil, Options{DailyLimit: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Not running yet: the first request queues, the second sees it running.
	if _, err := svc.Request(ctx, 9, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Request(ctx, 9, "manual"); !errors.Is(err, store.ErrTriageRunning) {
		t.Errorf("second request: %v", err)
	}
	if _, err := svc.Request(ctx, 404, "manual"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown incident: %v", err)
	}
	go svc.Run(ctx)
	id := wait(t, st)
	tr := st.triages[id]
	if tr.State != store.TriageFailed || !strings.Contains(tr.Error, "not valid JSON") {
		t.Errorf("triage = %+v", tr)
	}
	if _, err := svc.Request(ctx, 9, "manual"); err != nil {
		t.Fatalf("after a failure: %v", err)
	}
	wait(t, st)
	if _, err := svc.Request(ctx, 9, "manual"); !errors.Is(err, ErrLimit) {
		t.Errorf("third request in a day: %v, want ErrLimit", err)
	}
}

func TestServiceShutdownFailsQueued(t *testing.T) {
	st := newStore()
	svc := newService(st, &fakeMessages{}, nil, nil, Options{})
	if _, err := svc.Request(context.Background(), 9, "manual"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stopped before any worker picks the job
	svc.Run(ctx)
	id := wait(t, st)
	if tr := st.triages[id]; tr.State != store.TriageFailed || tr.Error == "" {
		t.Errorf("triage = %+v", tr)
	}
}

func TestNilService(t *testing.T) {
	var svc *Service
	if svc.Enabled() || svc.Model() != "" {
		t.Error("a nil service is disabled")
	}
	if _, err := svc.Request(context.Background(), 1, "manual"); !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v", err)
	}
	svc.IncidentDown(context.Background(), 1) // must not panic
}
