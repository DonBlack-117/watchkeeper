package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

// Incident is a period during which a check was not operational.
type Incident struct {
	ID         int64            `json:"id"`
	CheckID    int64            `json:"checkId"`
	CheckName  string           `json:"checkName"`
	Status     check.Status     `json:"status"` // degraded or down while open; the last of them once resolved
	StartedAt  time.Time        `json:"startedAt"`
	ResolvedAt *time.Time       `json:"resolvedAt"`
	Notified   bool             `json:"-"`
	Updates    []IncidentUpdate `json:"updates"`
}

// IncidentUpdate is one step of the timeline. Status "resolved" closes it.
type IncidentUpdate struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// TransitionKind says what ApplyStatus changed.
type TransitionKind int

// What a new status did to the check's open incident.
const (
	NoChange TransitionKind = iota
	Opened
	Changed // degraded to down or back while still open
	Resolved
)

// Transition is the result of ApplyStatus. Incident is the check's incident
// after the change; with NoChange it is the open incident, if there is one
// (ID 0 otherwise), so the caller can retry an alert that did not go out.
type Transition struct {
	Kind     TransitionKind
	Incident Incident
}

// RecentResultsFor returns up to n latest results of one check started at or
// after `since`, newest first. Passing the check's updated_at keeps runs against
// an old target out of its current status.
func (s *Store) RecentResultsFor(ctx context.Context, checkID int64, n int, since time.Time) ([]check.Result, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+resultCols+` FROM check_results WHERE check_id = $1 AND started_at >= $3
		 ORDER BY started_at DESC, id DESC LIMIT $2`, checkID, n, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []check.Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApplyStatus moves the check's incident to match its current status: it opens
// one when the check stops being operational, records a change between degraded
// and down, and resolves it when the check is operational again. Unknown keeps
// things as they are. errText is the latest run's error category.
func (s *Store) ApplyStatus(ctx context.Context, checkID int64, status check.Status, at time.Time, errText string) (Transition, error) {
	tr, err := s.applyStatus(ctx, checkID, status, at, errText)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Another goroutine for the same check (right after a reload) opened it
		// first; retrying sees that incident and continues from it.
		return s.applyStatus(ctx, checkID, status, at, errText)
	}
	return tr, err
}

func (s *Store) applyStatus(ctx context.Context, checkID int64, status check.Status, at time.Time, errText string) (Transition, error) {
	if status == check.StatusUnknown {
		return Transition{}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transition{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var inc Incident
	err = tx.QueryRow(ctx,
		`SELECT i.id, i.check_id, c.name, i.status, i.started_at, i.notified
		 FROM incidents i JOIN checks c ON c.id = i.check_id
		 WHERE i.check_id = $1 AND i.resolved_at IS NULL
		 FOR UPDATE OF i`, checkID).
		Scan(&inc.ID, &inc.CheckID, &inc.CheckName, &inc.Status, &inc.StartedAt, &inc.Notified)
	open := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Transition{}, fmt.Errorf("find open incident: %w", err)
	}
	inc.StartedAt = inc.StartedAt.UTC()

	var tr Transition
	switch {
	case status == check.StatusOperational && open:
		// Runs of the same check can overlap right after a reload, so never
		// resolve before the incident started.
		if at.Before(inc.StartedAt) {
			at = inc.StartedAt
		}
		if _, err := tx.Exec(ctx, `UPDATE incidents SET resolved_at = $2 WHERE id = $1`, inc.ID, at); err != nil {
			return tr, err
		}
		resolved := at.UTC()
		inc.ResolvedAt = &resolved
		tr = Transition{Kind: Resolved, Incident: inc}
		if err := addUpdate(ctx, tx, inc.ID, at, "resolved", ""); err != nil {
			return tr, err
		}
	case status == check.StatusOperational:
		return Transition{}, nil
	case !open:
		err := tx.QueryRow(ctx,
			`INSERT INTO incidents (check_id, status, started_at) VALUES ($1, $2, $3)
			 RETURNING id, (SELECT name FROM checks WHERE id = $1)`, checkID, status, at).
			Scan(&inc.ID, &inc.CheckName)
		if err != nil {
			return tr, err
		}
		inc.CheckID, inc.Status, inc.StartedAt = checkID, status, at.UTC()
		tr = Transition{Kind: Opened, Incident: inc}
		if err := addUpdate(ctx, tx, inc.ID, at, string(status), errText); err != nil {
			return tr, err
		}
	case inc.Status != status:
		if at.Before(inc.StartedAt) {
			at = inc.StartedAt
		}
		if _, err := tx.Exec(ctx, `UPDATE incidents SET status = $2 WHERE id = $1`, inc.ID, status); err != nil {
			return tr, err
		}
		inc.Status = status
		tr = Transition{Kind: Changed, Incident: inc}
		if err := addUpdate(ctx, tx, inc.ID, at, string(status), errText); err != nil {
			return tr, err
		}
	default:
		return Transition{Kind: NoChange, Incident: inc}, nil
	}
	return tr, tx.Commit(ctx)
}

// ResolveOpen closes the check's open incident, if any, without waiting for a
// run: main uses it when a check is disabled or pointed at another target,
// since its old results no longer say anything. It returns whether one closed.
func (s *Store) ResolveOpen(ctx context.Context, checkID int64, at time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx,
		`UPDATE incidents SET resolved_at = GREATEST($2, started_at)
		 WHERE check_id = $1 AND resolved_at IS NULL RETURNING id`, checkID, at).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO incident_updates (incident_id, at, status)
		 SELECT id, resolved_at, 'resolved' FROM incidents WHERE id = $1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func addUpdate(ctx context.Context, tx pgx.Tx, incidentID int64, at time.Time, status, errText string) error {
	var e *string
	if errText != "" {
		e = &errText
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO incident_updates (incident_id, at, status, error) VALUES ($1, $2, $3, $4)`,
		incidentID, at, status, e)
	return err
}

// MarkNotified records that the "opened" alert of an incident went out.
func (s *Store) MarkNotified(ctx context.Context, incidentID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE incidents SET notified = TRUE WHERE id = $1`, incidentID)
	return err
}

// IncidentQuery filters ListIncidents. Zero values mean no filter.
type IncidentQuery struct {
	CheckID  int64
	Since    time.Time // incidents still open, or resolved after this time
	OpenOnly bool
	// EnabledOnly skips incidents of disabled checks, which the status page hides.
	EnabledOnly bool
	Limit       int
}

// ListIncidents returns incidents newest first, each with its timeline oldest first.
func (s *Store) ListIncidents(ctx context.Context, q IncidentQuery) ([]Incident, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.check_id, c.name, i.status, i.started_at, i.resolved_at, i.notified
		 FROM incidents i JOIN checks c ON c.id = i.check_id
		 WHERE ($1::bigint = 0 OR i.check_id = $1)
		   AND ($2::timestamptz IS NULL OR i.resolved_at IS NULL OR i.resolved_at >= $2)
		   AND (NOT $3 OR i.resolved_at IS NULL)
		   AND (NOT $5 OR c.enabled)
		 ORDER BY i.started_at DESC, i.id DESC
		 LIMIT $4`, q.CheckID, nullTime(q.Since), q.OpenOnly, q.Limit, q.EnabledOnly)
	if err != nil {
		return nil, err
	}
	out := []Incident{}
	index := map[int64]int{}
	ids := []int64{}
	for rows.Next() {
		var inc Incident
		if err := rows.Scan(&inc.ID, &inc.CheckID, &inc.CheckName, &inc.Status, &inc.StartedAt, &inc.ResolvedAt, &inc.Notified); err != nil {
			rows.Close()
			return nil, err
		}
		inc.StartedAt = inc.StartedAt.UTC()
		if inc.ResolvedAt != nil {
			t := inc.ResolvedAt.UTC()
			inc.ResolvedAt = &t
		}
		inc.Updates = []IncidentUpdate{}
		index[inc.ID] = len(out)
		ids = append(ids, inc.ID)
		out = append(out, inc)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}

	rows, err = s.pool.Query(ctx,
		`SELECT incident_id, at, status, coalesce(error, '') FROM incident_updates
		 WHERE incident_id = ANY($1) ORDER BY at, id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var u IncidentUpdate
		if err := rows.Scan(&id, &u.At, &u.Status, &u.Error); err != nil {
			return nil, err
		}
		u.At = u.At.UTC()
		i := index[id]
		out[i].Updates = append(out[i].Updates, u)
	}
	return out, rows.Err()
}

// CountOpenIncidents returns how many incidents are open.
func (s *Store) CountOpenIncidents(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE resolved_at IS NULL`).Scan(&n)
	return n, err
}

// ForgetCertNotice removes a notice whose alert could not be queued, so the
// next run tries again.
func (s *Store) ForgetCertNotice(ctx context.Context, checkID int64, expires time.Time, thresholdDays int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM cert_notices WHERE check_id = $1 AND expires_at = $2 AND threshold_days = $3`,
		checkID, expires, thresholdDays)
	return err
}

// RecordCertNotice stores that the warning for this certificate and threshold
// was sent. It returns false when it had already been recorded.
func (s *Store) RecordCertNotice(ctx context.Context, checkID int64, expires time.Time, thresholdDays int) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO cert_notices (check_id, expires_at, threshold_days) VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`, checkID, expires, thresholdDays)
	return tag.RowsAffected() == 1, err
}
