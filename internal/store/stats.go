package store

import (
	"context"
	"time"
)

// HourLatency is the response time of one check during one UTC hour. The
// percentiles only count successful runs, so a timeout does not pose as latency.
type HourLatency struct {
	Hour  time.Time `json:"hour"`
	Total int       `json:"total"`
	OK    int       `json:"ok"`
	P50   *float64  `json:"p50"` // nil when the hour has no successful runs
	P95   *float64  `json:"p95"`
}

// HourlyLatency returns one entry per hour in [from, to), oldest first, including
// hours without runs. from and to are truncated to the hour.
func (s *Store) HourlyLatency(ctx context.Context, checkID int64, from, to time.Time) ([]HourLatency, error) {
	from, to = from.UTC().Truncate(time.Hour), to.UTC().Truncate(time.Hour)
	rows, err := s.pool.Query(ctx,
		`SELECT g.hour, coalesce(a.total, 0), coalesce(a.ok, 0), a.p50, a.p95
		 FROM generate_series($2::timestamptz, $3::timestamptz - interval '1 hour', interval '1 hour') AS g(hour)
		 LEFT JOIN (
		     SELECT date_trunc('hour', started_at) AS hour,
		            count(*) AS total,
		            count(*) FILTER (WHERE ok) AS ok,
		            percentile_cont(0.5) WITHIN GROUP (ORDER BY total_ms) FILTER (WHERE ok) AS p50,
		            percentile_cont(0.95) WITHIN GROUP (ORDER BY total_ms) FILTER (WHERE ok) AS p95
		     FROM check_results
		     WHERE check_id = $1 AND started_at >= $2 AND started_at < $3
		     GROUP BY 1
		 ) a ON a.hour = g.hour
		 ORDER BY g.hour`, checkID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourLatency{}
	for rows.Next() {
		var h HourLatency
		if err := rows.Scan(&h.Hour, &h.Total, &h.OK, &h.P50, &h.P95); err != nil {
			return nil, err
		}
		h.Hour = h.Hour.UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// CheckStats is the recent behaviour of one check.
type CheckStats struct {
	Uptime float64  // percentage of successful runs
	P95    *float64 // milliseconds, successful runs only; nil when none passed
}

// StatsSince returns the uptime and p95 latency of every check with runs since `since`.
func (s *Store) StatsSince(ctx context.Context, since time.Time) (map[int64]CheckStats, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT check_id,
		        count(*) FILTER (WHERE ok) * 100.0 / count(*),
		        percentile_cont(0.95) WITHIN GROUP (ORDER BY total_ms) FILTER (WHERE ok)
		 FROM check_results WHERE started_at >= $1 GROUP BY check_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]CheckStats{}
	for rows.Next() {
		var id int64
		var st CheckStats
		if err := rows.Scan(&id, &st.Uptime, &st.P95); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// Summary is the dashboard headline for enabled checks over a period.
type Summary struct {
	Total  int      `json:"total"`
	OK     int      `json:"ok"`
	Uptime *float64 `json:"uptime"` // nil without runs
	P95    *float64 `json:"p95Ms"`  // successful runs only
}

// SummarySince aggregates the runs of enabled checks since `since`.
func (s *Store) SummarySince(ctx context.Context, since time.Time) (Summary, error) {
	var sum Summary
	err := s.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE r.ok),
		        percentile_cont(0.95) WITHIN GROUP (ORDER BY r.total_ms) FILTER (WHERE r.ok)
		 FROM check_results r JOIN checks c ON c.id = r.check_id AND c.enabled
		 WHERE r.started_at >= $1`, since).Scan(&sum.Total, &sum.OK, &sum.P95)
	if err != nil {
		return sum, err
	}
	if sum.Total > 0 {
		u := float64(sum.OK) * 100 / float64(sum.Total)
		sum.Uptime = &u
	}
	return sum, nil
}

// DailyUptimeAll returns DailyUptime for every check in one query: the last
// `days` UTC days, oldest first, with nil percentages on days without runs.
// Checks without runs in the period are absent from the map.
func (s *Store) DailyUptimeAll(ctx context.Context, days int, now time.Time) (map[int64][]DayUptime, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(days - 1))
	rows, err := s.pool.Query(ctx,
		`SELECT check_id, to_char((started_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD'),
		        count(*), count(*) FILTER (WHERE ok)
		 FROM check_results WHERE started_at >= $1 GROUP BY 1, 2`, first)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct {
		id  int64
		day string
	}
	agg := map[key]DayUptime{}
	ids := map[int64]bool{}
	for rows.Next() {
		var k key
		var u DayUptime
		if err := rows.Scan(&k.id, &k.day, &u.Total, &u.OK); err != nil {
			return nil, err
		}
		agg[k], ids[k.id] = u, true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[int64][]DayUptime, len(ids))
	for id := range ids {
		series := make([]DayUptime, days)
		for i := range series {
			day := first.AddDate(0, 0, i).Format(time.DateOnly)
			u := agg[key{id, day}]
			u.Day = day
			if u.Total > 0 {
				p := float64(u.OK) * 100 / float64(u.Total)
				u.Percent = &p
			}
			series[i] = u
		}
		out[id] = series
	}
	return out, nil
}
