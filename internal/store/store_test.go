package store

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/DonBlack-117/watchkeeper/internal/check"
)

// newStore starts a throwaway PostgreSQL and applies the migrations.
// It is skipped when no Docker-compatible runtime is available.
func newStore(t *testing.T) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	pg, err := postgres.Run(ctx, "docker.io/library/postgres:16-alpine",
		postgres.WithDatabase("watchkeeper"),
		postgres.WithUsername("watchkeeper"),
		postgres.WithPassword("watchkeeper"),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, pg)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func sample(name string) check.Check {
	return check.Check{Name: name, Kind: check.KindHTTP, Target: "https://example.com", IntervalS: 60, TimeoutMS: 5000, Enabled: true}
}

func TestStore(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	a, err := s.CreateCheck(ctx, sample("a"))
	if err != nil || a.ID == 0 || a.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", a, err)
	}
	b, _ := s.CreateCheck(ctx, sample("b"))

	t.Run("crud", func(t *testing.T) {
		b.Enabled = false
		b.Target = "https://example.org"
		if _, err := s.UpdateCheck(ctx, b); err != nil {
			t.Fatal(err)
		}
		enabled, err := s.ListEnabledChecks(ctx)
		if err != nil || len(enabled) != 1 || enabled[0].ID != a.ID {
			t.Fatalf("enabled checks = %+v, %v", enabled, err)
		}
		all, _ := s.ListChecks(ctx)
		if len(all) != 2 {
			t.Fatalf("all checks = %d", len(all))
		}
		if _, err := s.GetCheck(ctx, 9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing check error = %v", err)
		}
		if _, err := s.UpdateCheck(ctx, check.Check{ID: 9999, Name: "x", Kind: check.KindDNS, Target: "x.dev", IntervalS: 60, TimeoutMS: 5000}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update of missing check error = %v", err)
		}
	})

	t.Run("results, cursor and uptime", func(t *testing.T) {
		now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
		code := 200
		// Two runs today (one failed), one yesterday, none two days ago.
		for _, r := range []check.Result{
			{CheckID: a.ID, StartedAt: now.Add(-25 * time.Hour), OK: true, StatusCode: &code, TotalMS: 120},
			{CheckID: a.ID, StartedAt: now.Add(-2 * time.Hour), OK: false, TotalMS: 3000, Error: "timeout"},
			{CheckID: a.ID, StartedAt: now.Add(-1 * time.Hour), OK: true, StatusCode: &code, TotalMS: 110},
		} {
			if _, err := s.SaveResult(ctx, r); err != nil {
				t.Fatal(err)
			}
		}

		page1, err := s.ListResults(ctx, ResultQuery{CheckID: a.ID, Limit: 2})
		if err != nil || len(page1) != 2 || page1[0].TotalMS != 110 {
			t.Fatalf("first page = %+v, %v", page1, err)
		}
		page2, _ := s.ListResults(ctx, ResultQuery{CheckID: a.ID, Limit: 2, BeforeID: page1[1].ID})
		if len(page2) != 1 || page2[0].TotalMS != 120 {
			t.Fatalf("second page = %+v", page2)
		}
		if page1[0].StartedAt.Location() != time.UTC {
			t.Errorf("timestamps must be UTC, got %v", page1[0].StartedAt.Location())
		}
		if page1[1].Error != "timeout" {
			t.Errorf("error text not stored: %+v", page1[1])
		}

		days, err := s.DailyUptime(ctx, a.ID, 3, now)
		if err != nil || len(days) != 3 {
			t.Fatalf("days = %+v, %v", days, err)
		}
		if days[0].Percent != nil || days[1].Total != 1 || days[2].Total != 2 || *days[2].Percent != 50 {
			t.Fatalf("uptime by day = %+v", days)
		}
		if days[2].Day != "2026-10-19" {
			t.Errorf("last day = %s", days[2].Day)
		}

		recent, _ := s.RecentResults(ctx, 2)
		if len(recent[a.ID]) != 2 || recent[a.ID][0].TotalMS != 110 {
			t.Fatalf("recent = %+v", recent[a.ID])
		}
		up, _ := s.UptimeSince(ctx, now.Add(-48*time.Hour))
		if got := up[a.ID]; got < 66 || got > 67 {
			t.Fatalf("uptime since = %v", got)
		}
	})

	t.Run("count and prune", func(t *testing.T) {
		if n, err := s.CountChecks(ctx); err != nil || n != 2 {
			t.Fatalf("count = %d, %v", n, err)
		}
		old := time.Now().AddDate(0, 0, -100)
		for range 3 {
			if _, err := s.SaveResult(ctx, check.Result{CheckID: a.ID, StartedAt: old, OK: true, TotalMS: 1}); err != nil {
				t.Fatal(err)
			}
		}
		deleted, err := s.PruneResults(ctx, time.Now().AddDate(0, 0, -90), 2)
		if err != nil || deleted != 3 {
			t.Fatalf("pruned %d, %v; want 3 in two batches", deleted, err)
		}
		if _, err := s.SaveResult(ctx, check.Result{CheckID: a.ID, StartedAt: time.Now(), OK: true, TotalMS: -1}); err == nil {
			t.Fatal("negative total_ms must violate the constraint")
		}
	})

	t.Run("delete cascades", func(t *testing.T) {
		if err := s.DeleteCheck(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteCheck(ctx, a.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete = %v", err)
		}
		res, _ := s.ListResults(ctx, ResultQuery{CheckID: a.ID, Limit: 10})
		if len(res) != 0 {
			t.Fatalf("results should be gone, got %d", len(res))
		}
	})
}

func TestStatsAndLatency(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	a, err := s.CreateCheck(ctx, sample("a"))
	if err != nil {
		t.Fatal(err)
	}
	off := sample("off")
	off.Enabled = false
	b, err := s.CreateCheck(ctx, off)
	if err != nil {
		t.Fatal(err)
	}
	save := func(id int64, at time.Time, ok bool, ms int) {
		t.Helper()
		if _, err := s.SaveResult(ctx, check.Result{CheckID: id, StartedAt: at, OK: ok, TotalMS: ms}); err != nil {
			t.Fatal(err)
		}
	}
	// Current hour: 100..1000 ms in steps of 100, plus a slow failure that must not count.
	for i := 1; i <= 10; i++ {
		save(a.ID, now.Add(-time.Duration(i)*time.Minute), true, i*100)
	}
	save(a.ID, now.Add(-15*time.Minute), false, 30000)
	save(a.ID, now.Add(-3*time.Hour), true, 50)
	save(a.ID, now.AddDate(0, 0, -2), false, 10)
	save(b.ID, now.Add(-time.Minute), false, 10)

	to := now.Truncate(time.Hour).Add(time.Hour)
	hours, err := s.HourlyLatency(ctx, a.ID, to.Add(-24*time.Hour), to)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 24 {
		t.Fatalf("hours = %d", len(hours))
	}
	last := hours[23]
	if !last.Hour.Equal(now.Truncate(time.Hour)) || last.Total != 11 || last.OK != 10 || math.Abs(*last.P50-550) > 0.01 || math.Abs(*last.P95-955) > 0.01 {
		t.Fatalf("current hour: %+v p50=%v p95=%v", last, *last.P50, *last.P95)
	}
	if hours[20].P50 == nil || *hours[20].P50 != 50 || hours[0].P50 != nil {
		t.Errorf("older hours: %+v / %+v", hours[20], hours[0])
	}

	stats, err := s.StatsSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st := stats[a.ID]; st.P95 == nil || st.Uptime < 91 || st.Uptime > 92 {
		t.Errorf("stats a: %+v", st) // 11 of 12 runs passed
	}

	sum, err := s.SummarySince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Total != 12 || sum.OK != 11 {
		t.Errorf("summary must skip the disabled check: %+v", sum)
	}

	daily, err := s.DailyUptimeAll(ctx, 90, now)
	if err != nil {
		t.Fatal(err)
	}
	da := daily[a.ID]
	if len(da) != 90 || da[89].Day != "2026-10-19" || da[89].Total != 12 || da[87].Percent == nil || *da[87].Percent != 0 || da[88].Percent != nil {
		t.Fatalf("daily a: %+v %+v %+v", da[87], da[88], da[89])
	}
}

func TestUsersAndSessions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	u, err := s.EnsureUser(ctx, "diego", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnsureUser(ctx, "diego", "hash-2")
	if err != nil || again.ID != u.ID || again.PasswordHash != "hash-2" {
		t.Fatalf("ensure twice: %+v %v", again, err)
	}
	if _, err := s.UserByName(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	live, expired := make([]byte, 32), make([]byte, 32)
	live[0], expired[0] = 1, 2
	if err := s.CreateSession(ctx, live, u.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, expired, u.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, live, now); err != nil || got.Username != "diego" {
		t.Fatalf("live session: %+v %v", got, err)
	}
	if _, err := s.SessionUser(ctx, expired, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if n, err := s.PruneSessions(ctx, now); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if _, err := s.EnsureUser(ctx, "diego", "hash-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, live, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a password change must end existing sessions: %v", err)
	}
	if err := s.DeleteSession(ctx, live); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, live, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session: %v", err)
	}
}
