package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

const (
	maxIncidents = 200
	// The status page shows open incidents and those resolved in this window.
	publicIncidentWindow = 14 * 24 * time.Hour
	publicIncidentLimit  = 20
)

// route is the ServeMux pattern that matches r, so metrics group /checks/1 and
// /checks/2 under one label. Everything outside the API is the frontend.
func (s *Server) route(r *http.Request) string {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return "frontend"
	}
	if _, pattern := s.mux.Handler(r); pattern != "" {
		return pattern
	}
	return "unmatched"
}

// methodLabel keeps the metrics' method label to a fixed set: the server
// accepts any token as a method, and each new one would be a new series.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// listIncidents is the dashboard's incident history, with error details.
func (s *Server) listIncidents(w http.ResponseWriter, r *http.Request) {
	q := store.IncidentQuery{Limit: 50}
	params := r.URL.Query()
	var errs []check.FieldError
	if v := params.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxIncidents {
			q.Limit = n
		} else {
			errs = append(errs, check.FieldError{Field: "limit", Message: "must be between 1 and " + strconv.Itoa(maxIncidents)})
		}
	}
	if v := params.Get("checkId"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			q.CheckID = id
		} else {
			errs = append(errs, check.FieldError{Field: "checkId", Message: "must be a positive integer"})
		}
	}
	if v := params.Get("open"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, check.FieldError{Field: "open", Message: "must be true or false"})
		}
		q.OpenOnly = b
	}
	if len(errs) > 0 {
		invalid(w, errs)
		return
	}
	incidents, err := s.store.ListIncidents(r.Context(), q)
	if err != nil {
		s.fail(w, r, "list incidents", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": incidents})
}

// publicIncident is what the status page may show: names, states and times,
// never targets or error details.
type publicIncident struct {
	ID         int64          `json:"id"`
	CheckID    int64          `json:"checkId"`
	Name       string         `json:"name"`
	Status     check.Status   `json:"status"`
	StartedAt  time.Time      `json:"startedAt"`
	ResolvedAt *time.Time     `json:"resolvedAt"`
	Updates    []publicUpdate `json:"updates"`
}

type publicUpdate struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
}

func (s *Server) publicIncidents(ctx context.Context, now time.Time) ([]publicIncident, error) {
	incidents, err := s.store.ListIncidents(ctx, store.IncidentQuery{
		Since: now.Add(-publicIncidentWindow), Limit: publicIncidentLimit, EnabledOnly: true,
	})
	if err != nil {
		return nil, err
	}
	out := make([]publicIncident, 0, len(incidents))
	for _, inc := range incidents {
		p := publicIncident{ID: inc.ID, CheckID: inc.CheckID, Name: inc.CheckName, Status: inc.Status,
			StartedAt: inc.StartedAt, ResolvedAt: inc.ResolvedAt, Updates: make([]publicUpdate, 0, len(inc.Updates))}
		for _, u := range inc.Updates {
			p.Updates = append(p.Updates, publicUpdate{At: u.At, Status: u.Status})
		}
		out = append(out, p)
	}
	return out, nil
}

// testAlert sends a message to every configured channel and reports the result,
// so the owner can check the bot token and chat id from the dashboard.
func (s *Server) testAlert(w http.ResponseWriter, r *http.Request) {
	if s.opts.Alerts == nil || len(s.opts.Alerts.Channels()) == 0 {
		conflict(w, "no alert channel is configured; set WK_TELEGRAM_BOT_TOKEN and WK_TELEGRAM_CHAT_ID")
		return
	}
	if !s.testLimiter.allow("all", s.now()) {
		w.Header().Set("Retry-After", "60")
		tooManyRequests(w)
		return
	}
	who := "the API token"
	if p, ok := r.Context().Value(userKey).(principal); ok && p.Username != "" {
		who = p.Username
	}
	msg := notify.Message{Text: "🧪 Watchkeeper test alert, sent by " + escHTML(who) + ". Alerts reach this chat."}
	if err := s.opts.Alerts.SendNow(r.Context(), msg); err != nil {
		s.log.WarnContext(r.Context(), "test alert failed", "err", err)
		var perm *notify.Permanent
		detail := "the alert channel did not accept the message: " + err.Error()
		if errors.As(err, &perm) {
			detail += " (check the bot token and chat id)"
		}
		badGateway(w, detail)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
