package store

import (
	"context"
	"errors"
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
