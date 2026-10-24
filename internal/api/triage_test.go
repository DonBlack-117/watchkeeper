package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/store"
	"github.com/DonBlack-117/watchkeeper/internal/triage"
)

type fakeTriage struct {
	err      error
	requests []int64
}

func (f *fakeTriage) Enabled() bool { return true }
func (f *fakeTriage) Model() string { return "claude-opus-5-5" }
func (f *fakeTriage) Request(_ context.Context, id int64, trigger string) (int64, error) {
	if trigger != "manual" {
		panic("the API only starts manual investigations")
	}
	f.requests = append(f.requests, id)
	return 7, f.err
}

func triageStore() *memStore {
	m := newMem()
	m.incidents = []store.Incident{{ID: 3, CheckID: 1, CheckName: "api", Status: check.StatusDown,
		StartedAt: time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC), Updates: []store.IncidentUpdate{}}}
	return m
}

func TestStartTriage(t *testing.T) {
	m := triageStore()
	ft := &fakeTriage{}
	h := newServerWith(m, Options{Triage: ft})

	rec, body := do(t, h, http.MethodPost, "/api/v1/incidents/3/triage", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %v", rec.Code, body)
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/incidents/3/triage" {
		t.Errorf("Location = %q", got)
	}
	if data := body["data"].(map[string]any); data["triageId"] != float64(7) || data["state"] != "running" {
		t.Errorf("data = %v", data)
	}
	if len(ft.requests) != 1 || ft.requests[0] != 3 {
		t.Errorf("requests = %v", ft.requests)
	}

	if rec, _ := do(t, h, http.MethodPost, "/api/v1/incidents/99/triage", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown incident: status %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodPost, "/api/v1/incidents/abc/triage", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
}

func TestStartTriageErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{store.ErrTriageRunning, http.StatusConflict},
		{triage.ErrLimit, http.StatusTooManyRequests},
		{triage.ErrBusy, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		h := newServerWith(triageStore(), Options{Triage: &fakeTriage{err: tc.err}})
		rec, body := do(t, h, http.MethodPost, "/api/v1/incidents/3/triage", "")
		if rec.Code != tc.code {
			t.Errorf("%v: status %d, want %d (%v)", tc.err, rec.Code, tc.code, body)
		}
	}
}

func TestStartTriageDisabled(t *testing.T) {
	h := newServer(triageStore())
	rec, body := do(t, h, http.MethodPost, "/api/v1/incidents/3/triage", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	if body["detail"] != triage.ErrDisabled.Error() {
		t.Errorf("detail = %v", body["detail"])
	}
	_, sum := do(t, h, http.MethodGet, "/api/v1/summary", "")
	tr := sum["data"].(map[string]any)["triage"].(map[string]any)
	if tr["enabled"] != false || tr["model"] != nil {
		t.Errorf("summary triage = %v", tr)
	}
}

func TestStartTriageRateLimit(t *testing.T) {
	h := newServerWith(triageStore(), Options{Triage: &fakeTriage{}})
	for i := range 5 {
		if rec, _ := do(t, h, http.MethodPost, "/api/v1/incidents/3/triage", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	rec, _ := do(t, h, http.MethodPost, "/api/v1/incidents/3/triage", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestGetIncidentAndTriage(t *testing.T) {
	m := triageStore()
	h := newServerWith(m, Options{Triage: &fakeTriage{}})

	if rec, _ := do(t, h, http.MethodGet, "/api/v1/incidents/3/triage", ""); rec.Code != http.StatusNotFound {
		t.Errorf("no triage yet: status %d", rec.Code)
	}
	rec, body := do(t, h, http.MethodGet, "/api/v1/incidents/3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if _, ok := body["data"].(map[string]any)["triage"]; ok {
		t.Error("an incident without investigation has no triage field")
	}

	m.triages = map[int64]store.Triage{3: {ID: 7, IncidentID: 3, State: store.TriageDone, Summary: "DNS record removed",
		Confidence: "high", Evidence: []string{}, NextSteps: []string{}, ToolCalls: []store.ToolCall{}}}
	_, body = do(t, h, http.MethodGet, "/api/v1/incidents/3", "")
	tr := body["data"].(map[string]any)["triage"].(map[string]any)
	if tr["summary"] != "DNS record removed" || tr["confidence"] != "high" {
		t.Errorf("triage = %v", tr)
	}
	rec, body = do(t, h, http.MethodGet, "/api/v1/incidents/3/triage", "")
	if rec.Code != http.StatusOK || body["data"].(map[string]any)["id"] != float64(7) {
		t.Errorf("status %d, body %v", rec.Code, body)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/v1/incidents/42", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown incident: status %d", rec.Code)
	}

	_, sum := do(t, h, http.MethodGet, "/api/v1/summary", "")
	st := sum["data"].(map[string]any)["triage"].(map[string]any)
	if st["enabled"] != true || st["model"] != "claude-opus-5-5" {
		t.Errorf("summary triage = %v", st)
	}
}
