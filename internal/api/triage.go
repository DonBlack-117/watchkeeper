package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/DonBlack-117/watchkeeper/internal/store"
	"github.com/DonBlack-117/watchkeeper/internal/triage"
)

// Triage starts AI investigations; *triage.Service implements it.
type Triage interface {
	Enabled() bool
	Model() string
	Request(ctx context.Context, incidentID int64, trigger string) (int64, error)
}

func (s *Server) triageEnabled() bool {
	return s.opts.Triage != nil && s.opts.Triage.Enabled()
}

// getIncident returns one incident with its timeline and latest investigation.
func (s *Server) getIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		notFound(w, "incident not found")
		return
	}
	inc, err := s.store.GetIncident(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "incident not found")
		return
	}
	if err != nil {
		s.fail(w, r, "get incident", err)
		return
	}
	switch t, err := s.store.LatestTriage(r.Context(), id); {
	case err == nil:
		inc.Triage = &t
	case !errors.Is(err, store.ErrNotFound):
		s.fail(w, r, "latest triage", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": inc})
}

// startTriage queues an AI investigation of an incident (202 Accepted). The
// result appears on the incident when it finishes, usually within a minute.
func (s *Server) startTriage(w http.ResponseWriter, r *http.Request) {
	if !s.triageEnabled() {
		conflict(w, triage.ErrDisabled.Error())
		return
	}
	id, ok := parseID(r)
	if !ok {
		notFound(w, "incident not found")
		return
	}
	if !s.triageLimiter.allow("all", s.now()) {
		w.Header().Set("Retry-After", "60")
		tooManyRequests(w)
		return
	}
	if _, err := s.store.GetIncident(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		notFound(w, "incident not found")
		return
	} else if err != nil {
		s.fail(w, r, "get incident", err)
		return
	}
	triageID, err := s.opts.Triage.Request(r.Context(), id, "manual")
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "incident not found")
	case errors.Is(err, store.ErrTriageRunning):
		conflict(w, err.Error())
	case errors.Is(err, triage.ErrLimit):
		w.Header().Set("Retry-After", "3600")
		writeProblem(w, Problem{Type: problemBase + "rate-limited", Title: "Too many requests",
			Status: http.StatusTooManyRequests, Detail: err.Error() + " (WK_TRIAGE_DAILY_LIMIT)"})
	case errors.Is(err, triage.ErrBusy):
		w.Header().Set("Retry-After", "60")
		writeProblem(w, Problem{Type: problemBase + "unavailable", Title: "Service unavailable",
			Status: http.StatusServiceUnavailable, Detail: err.Error()})
	case err != nil:
		s.fail(w, r, "start triage", err)
	default:
		w.Header().Set("Location", "/api/v1/incidents/"+strconv.FormatInt(id, 10)+"/triage")
		writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{
			"triageId": triageID, "incidentId": id, "state": store.TriageRunning}})
	}
}

// getTriage returns the newest investigation of an incident.
func (s *Server) getTriage(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		notFound(w, "incident not found")
		return
	}
	t, err := s.store.LatestTriage(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "this incident has not been investigated")
		return
	}
	if err != nil {
		s.fail(w, r, "latest triage", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": t})
}
