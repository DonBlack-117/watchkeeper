package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrTriageRunning is returned when the incident already has an investigation running.
var ErrTriageRunning = errors.New("an investigation of this incident is already running")

// Triage states.
const (
	TriageRunning = "running"
	TriageDone    = "done"
	TriageFailed  = "failed"
)

// Triage is one AI investigation of an incident.
type Triage struct {
	ID            int64      `json:"id"`
	IncidentID    int64      `json:"incidentId"`
	State         string     `json:"state"`
	Trigger       string     `json:"trigger"`
	Model         string     `json:"model"`
	Summary       string     `json:"summary,omitempty"`
	ProbableCause string     `json:"probableCause,omitempty"`
	Confidence    string     `json:"confidence,omitempty"`
	Evidence      []string   `json:"evidence"`
	NextSteps     []string   `json:"nextSteps"`
	ToolCalls     []ToolCall `json:"toolCalls"`
	InputTokens   int        `json:"inputTokens"`
	OutputTokens  int        `json:"outputTokens"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
}

// ToolCall is one tool the agent used, with a short outcome for the timeline.
type ToolCall struct {
	Name    string `json:"name"`
	Input   string `json:"input,omitempty"`
	Outcome string `json:"outcome"`
}

// TriageReport is what a finished investigation stores.
type TriageReport struct {
	Summary       string
	ProbableCause string
	Confidence    string
	Evidence      []string
	NextSteps     []string
	ToolCalls     []ToolCall
	InputTokens   int
	OutputTokens  int
}

const triageCols = `id, incident_id, state, trigger, model, coalesce(summary, ''), coalesce(probable_cause, ''),
	coalesce(confidence, ''), evidence, next_steps, tool_calls, input_tokens, output_tokens, coalesce(error, ''),
	started_at, finished_at`

func scanTriage(row pgx.Row) (Triage, error) {
	var t Triage
	var evidence, steps, calls []byte
	err := row.Scan(&t.ID, &t.IncidentID, &t.State, &t.Trigger, &t.Model, &t.Summary, &t.ProbableCause,
		&t.Confidence, &evidence, &steps, &calls, &t.InputTokens, &t.OutputTokens, &t.Error,
		&t.StartedAt, &t.FinishedAt)
	if err != nil {
		return t, err
	}
	t.Evidence, t.NextSteps, t.ToolCalls = []string{}, []string{}, []ToolCall{}
	if err := json.Unmarshal(evidence, &t.Evidence); err != nil {
		return t, fmt.Errorf("triage evidence: %w", err)
	}
	if err := json.Unmarshal(steps, &t.NextSteps); err != nil {
		return t, fmt.Errorf("triage next steps: %w", err)
	}
	if err := json.Unmarshal(calls, &t.ToolCalls); err != nil {
		return t, fmt.Errorf("triage tool calls: %w", err)
	}
	t.StartedAt = t.StartedAt.UTC()
	if t.FinishedAt != nil {
		f := t.FinishedAt.UTC()
		t.FinishedAt = &f
	}
	return t, nil
}

// GetIncident returns one incident with its timeline, or ErrNotFound.
func (s *Store) GetIncident(ctx context.Context, id int64) (Incident, error) {
	var inc Incident
	err := s.pool.QueryRow(ctx,
		`SELECT i.id, i.check_id, c.name, i.status, i.started_at, i.resolved_at, i.notified
		 FROM incidents i JOIN checks c ON c.id = i.check_id WHERE i.id = $1`, id).
		Scan(&inc.ID, &inc.CheckID, &inc.CheckName, &inc.Status, &inc.StartedAt, &inc.ResolvedAt, &inc.Notified)
	if errors.Is(err, pgx.ErrNoRows) {
		return inc, ErrNotFound
	}
	if err != nil {
		return inc, err
	}
	inc.StartedAt = inc.StartedAt.UTC()
	if inc.ResolvedAt != nil {
		t := inc.ResolvedAt.UTC()
		inc.ResolvedAt = &t
	}
	inc.Updates = []IncidentUpdate{}
	rows, err := s.pool.Query(ctx,
		`SELECT at, status, coalesce(error, '') FROM incident_updates WHERE incident_id = $1 ORDER BY at, id`, id)
	if err != nil {
		return inc, err
	}
	defer rows.Close()
	for rows.Next() {
		var u IncidentUpdate
		if err := rows.Scan(&u.At, &u.Status, &u.Error); err != nil {
			return inc, err
		}
		u.At = u.At.UTC()
		inc.Updates = append(inc.Updates, u)
	}
	return inc, rows.Err()
}

// StartTriage records a running investigation and returns its id. It returns
// ErrTriageRunning when one is already running for the incident.
func (s *Store) StartTriage(ctx context.Context, incidentID int64, trigger, model string, at time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO incident_triages (incident_id, state, trigger, model, started_at)
		 VALUES ($1, 'running', $2, $3, $4) RETURNING id`, incidentID, trigger, model, at).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return 0, ErrTriageRunning
		case "23503":
			return 0, ErrNotFound // the incident (or its check) was deleted
		}
	}
	return id, err
}

// FinishTriage stores the report of a finished investigation.
func (s *Store) FinishTriage(ctx context.Context, id int64, r TriageReport, at time.Time) error {
	evidence, err := json.Marshal(nonNil(r.Evidence))
	if err != nil {
		return err
	}
	steps, err := json.Marshal(nonNil(r.NextSteps))
	if err != nil {
		return err
	}
	calls, err := json.Marshal(nonNilCalls(r.ToolCalls))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE incident_triages SET state = 'done', summary = $2, probable_cause = $3, confidence = $4,
		   evidence = $5, next_steps = $6, tool_calls = $7, input_tokens = $8, output_tokens = $9, finished_at = $10
		 WHERE id = $1`,
		id, r.Summary, r.ProbableCause, nullString(r.Confidence), evidence, steps, calls,
		r.InputTokens, r.OutputTokens, at)
	return err
}

// FailTriage marks an investigation as failed with a short reason.
func (s *Store) FailTriage(ctx context.Context, id int64, reason string, calls []ToolCall, inTok, outTok int, at time.Time) error {
	b, err := json.Marshal(nonNilCalls(calls))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE incident_triages SET state = 'failed', error = $2, tool_calls = $3, input_tokens = $4,
		   output_tokens = $5, finished_at = $6 WHERE id = $1`, id, reason, b, inTok, outTok, at)
	return err
}

// FailStaleTriages marks investigations left running by a previous process as
// failed, so they do not block new ones. main calls it at startup.
func (s *Store) FailStaleTriages(ctx context.Context, at time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE incident_triages SET state = 'failed', error = 'interrupted by a restart', finished_at = $1
		 WHERE state = 'running'`, at)
	return tag.RowsAffected(), err
}

// CountTriagesSince counts investigations started at or after since, for the daily limit.
func (s *Store) CountTriagesSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM incident_triages WHERE started_at >= $1`, since).Scan(&n)
	return n, err
}

// LatestTriage returns the newest investigation of an incident, or ErrNotFound.
func (s *Store) LatestTriage(ctx context.Context, incidentID int64) (Triage, error) {
	t, err := scanTriage(s.pool.QueryRow(ctx,
		`SELECT `+triageCols+` FROM incident_triages WHERE incident_id = $1
		 ORDER BY started_at DESC, id DESC LIMIT 1`, incidentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// latestTriages returns the newest investigation of each incident in ids.
func (s *Store) latestTriages(ctx context.Context, ids []int64) (map[int64]Triage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (incident_id) `+triageCols+` FROM incident_triages
		 WHERE incident_id = ANY($1) ORDER BY incident_id, started_at DESC, id DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Triage{}
	for rows.Next() {
		t, err := scanTriage(rows)
		if err != nil {
			return nil, err
		}
		out[t.IncidentID] = t
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilCalls(c []ToolCall) []ToolCall {
	if c == nil {
		return []ToolCall{}
	}
	return c
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
