// Package store persists checks and results in PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver used only by the migrator
	"github.com/pressly/goose/v3"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/migrations"
)

// ErrNotFound is returned when a check does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps a connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies the connection. Sessions run in UTC
// so day buckets never shift with daylight saving, and every statement has a
// timeout so a stuck query cannot hold a connection forever.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether the database answers; used by the health endpoint.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies the embedded migrations that have not run yet.
func Migrate(ctx context.Context, url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

const checkCols = `id, name, kind, target, interval_s, timeout_ms, expected_status, enabled, created_at, updated_at`

func scanCheck(row pgx.Row) (check.Check, error) {
	var c check.Check
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.Target, &c.IntervalS, &c.TimeoutMS, &c.ExpectedStatus, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	// pgx returns timestamps in the local zone; the API always speaks UTC.
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

func collectChecks(rows pgx.Rows) ([]check.Check, error) {
	defer rows.Close()
	out := []check.Check{}
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateCheck inserts c and returns it with its id and timestamps.
func (s *Store) CreateCheck(ctx context.Context, c check.Check) (check.Check, error) {
	return scanCheck(s.pool.QueryRow(ctx,
		`INSERT INTO checks (name, kind, target, interval_s, timeout_ms, expected_status, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+checkCols,
		c.Name, c.Kind, c.Target, c.IntervalS, c.TimeoutMS, c.ExpectedStatus, c.Enabled))
}

// GetCheck returns one check or ErrNotFound.
func (s *Store) GetCheck(ctx context.Context, id int64) (check.Check, error) {
	return scanCheck(s.pool.QueryRow(ctx, `SELECT `+checkCols+` FROM checks WHERE id = $1`, id))
}

// ListChecks returns every check ordered by id.
func (s *Store) ListChecks(ctx context.Context) ([]check.Check, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+checkCols+` FROM checks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return collectChecks(rows)
}

// ListEnabledChecks returns the checks the scheduler should run.
func (s *Store) ListEnabledChecks(ctx context.Context) ([]check.Check, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+checkCols+` FROM checks WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return collectChecks(rows)
}

// UpdateCheck replaces the editable fields of c.
func (s *Store) UpdateCheck(ctx context.Context, c check.Check) (check.Check, error) {
	return scanCheck(s.pool.QueryRow(ctx,
		`UPDATE checks SET name = $2, kind = $3, target = $4, interval_s = $5, timeout_ms = $6,
		        expected_status = $7, enabled = $8, updated_at = now()
		 WHERE id = $1 RETURNING `+checkCols,
		c.ID, c.Name, c.Kind, c.Target, c.IntervalS, c.TimeoutMS, c.ExpectedStatus, c.Enabled))
}

// DeleteCheck removes a check and, by cascade, its results.
func (s *Store) DeleteCheck(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM checks WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const resultCols = `id, check_id, started_at, ok, status_code, total_ms, dns_ms, connect_ms, tls_ms, ttfb_ms, cert_expires_at, coalesce(error, '')`

func scanResult(row pgx.Row) (check.Result, error) {
	var r check.Result
	err := row.Scan(&r.ID, &r.CheckID, &r.StartedAt, &r.OK, &r.StatusCode, &r.TotalMS, &r.DNSMS, &r.ConnectMS, &r.TLSMS, &r.TTFBMS, &r.CertExpiresAt, &r.Error)
	if err != nil {
		return r, err
	}
	r.StartedAt = r.StartedAt.UTC()
	if r.CertExpiresAt != nil {
		exp := r.CertExpiresAt.UTC()
		r.CertExpiresAt = &exp
	}
	return r, nil
}

// SaveResult stores one run and returns it with its id.
func (s *Store) SaveResult(ctx context.Context, r check.Result) (check.Result, error) {
	var errText *string
	if r.Error != "" {
		errText = &r.Error
	}
	return scanResult(s.pool.QueryRow(ctx,
		`INSERT INTO check_results (check_id, started_at, ok, status_code, total_ms, dns_ms, connect_ms, tls_ms, ttfb_ms, cert_expires_at, error)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+resultCols,
		r.CheckID, r.StartedAt, r.OK, r.StatusCode, r.TotalMS, r.DNSMS, r.ConnectMS, r.TLSMS, r.TTFBMS, r.CertExpiresAt, errText))
}

// ResultQuery filters the history of one check. Zero values mean "no filter".
type ResultQuery struct {
	CheckID  int64
	From, To time.Time
	BeforeID int64 // keyset cursor: only results with a smaller id
	Limit    int
}

// ListResults returns results newest first.
func (s *Store) ListResults(ctx context.Context, q ResultQuery) ([]check.Result, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+resultCols+` FROM check_results
		 WHERE check_id = $1
		   AND ($2::timestamptz IS NULL OR started_at >= $2)
		   AND ($3::timestamptz IS NULL OR started_at < $3)
		   AND ($4::bigint = 0 OR id < $4)
		 ORDER BY id DESC
		 LIMIT $5`,
		q.CheckID, nullTime(q.From), nullTime(q.To), q.BeforeID, q.Limit)
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

// RecentResults returns up to n latest results per check (enabled or not, so a
// disabled check still shows its last state), newest first.
func (s *Store) RecentResults(ctx context.Context, n int) (map[int64][]check.Result, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, r.check_id, r.started_at, r.ok, r.status_code, r.total_ms, r.dns_ms, r.connect_ms,
		        r.tls_ms, r.ttfb_ms, r.cert_expires_at, coalesce(r.error, '')
		 FROM checks c
		 CROSS JOIN LATERAL (
		     SELECT id, check_id, started_at, ok, status_code, total_ms, dns_ms, connect_ms,
		            tls_ms, ttfb_ms, cert_expires_at, error
		     FROM check_results
		     WHERE check_id = c.id
		     ORDER BY started_at DESC, id DESC
		     LIMIT $1
		 ) r
		 ORDER BY r.check_id, r.started_at DESC, r.id DESC`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]check.Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out[r.CheckID] = append(out[r.CheckID], r)
	}
	return out, rows.Err()
}

// DayUptime is the availability of one check on one UTC day.
type DayUptime struct {
	Day     string   `json:"day"` // YYYY-MM-DD
	Total   int      `json:"total"`
	OK      int      `json:"ok"`
	Percent *float64 `json:"percent"` // nil when there were no runs ("no data", not "down")
}

// DailyUptime returns one entry per day for the last `days` UTC days, oldest first,
// including days without runs. Results are aggregated once with an index range scan
// and then joined to the calendar.
func (s *Store) DailyUptime(ctx context.Context, checkID int64, days int, now time.Time) ([]DayUptime, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(days - 1))
	rows, err := s.pool.Query(ctx,
		`SELECT to_char(g.day, 'YYYY-MM-DD'), coalesce(a.total, 0), coalesce(a.ok, 0)
		 FROM generate_series($2::date, $3::date, interval '1 day') AS g(day)
		 LEFT JOIN (
		     SELECT (started_at AT TIME ZONE 'UTC')::date AS day,
		            count(*) AS total,
		            count(*) FILTER (WHERE ok) AS ok
		     FROM check_results
		     WHERE check_id = $1 AND started_at >= $4
		     GROUP BY 1
		 ) a ON a.day = g.day::date
		 ORDER BY g.day`, checkID, first.Format(time.DateOnly), today.Format(time.DateOnly), first)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DayUptime, 0, days)
	for rows.Next() {
		var u DayUptime
		if err := rows.Scan(&u.Day, &u.Total, &u.OK); err != nil {
			return nil, err
		}
		if u.Total > 0 {
			p := float64(u.OK) * 100 / float64(u.Total)
			u.Percent = &p
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UptimeSince returns the share of successful runs per check since `since`, as a percentage.
func (s *Store) UptimeSince(ctx context.Context, since time.Time) (map[int64]float64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT check_id, count(*) FILTER (WHERE ok) * 100.0 / count(*)
		 FROM check_results WHERE started_at >= $1 GROUP BY check_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		var pct float64
		if err := rows.Scan(&id, &pct); err != nil {
			return nil, err
		}
		out[id] = pct
	}
	return out, rows.Err()
}

// CountChecks returns how many checks exist; the API uses it to cap the total.
func (s *Store) CountChecks(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM checks`).Scan(&n)
	return n, err
}

// PruneResults deletes results older than `before` in batches, so a large backlog
// does not turn into one long transaction. It returns how many rows were deleted.
func (s *Store) PruneResults(ctx context.Context, before time.Time, batch int) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM check_results
			 WHERE id IN (SELECT id FROM check_results WHERE started_at < $1 LIMIT $2)`, before, batch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batch) {
			return total, nil
		}
	}
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
