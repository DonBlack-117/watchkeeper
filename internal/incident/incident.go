// Package incident turns check results into incidents and alerts: it opens an
// incident when a check stops being operational, records changes, resolves it
// on recovery, and warns before certificates expire.
package incident

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/notify"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

// Store is the persistence the manager needs; *store.Store implements it.
type Store interface {
	RecentResultsFor(ctx context.Context, checkID int64, n int, since time.Time) ([]check.Result, error)
	ApplyStatus(ctx context.Context, checkID int64, status check.Status, at time.Time, errText string) (store.Transition, error)
	MarkNotified(ctx context.Context, incidentID int64) error
	RecordCertNotice(ctx context.Context, checkID int64, expires time.Time, thresholdDays int) (bool, error)
	ForgetCertNotice(ctx context.Context, checkID int64, expires time.Time, thresholdDays int) error
}

// Sender queues alerts; *notify.Dispatcher implements it.
type Sender interface {
	Enabled() bool
	Enqueue(m notify.Message) bool
}

// CertThresholds are the days before expiry when a warning is sent, largest first.
// TLS checks already fail 7 days before expiry, which opens an incident.
var CertThresholds = []int{30, 14}

// Options tunes the manager.
type Options struct {
	// AlertOnDegraded also alerts when a check is only degraded (slow or some
	// failed runs). By default only "down" alerts, to avoid noise.
	AlertOnDegraded bool
	// PublicURL, when set, adds a dashboard link to each alert.
	PublicURL string
	// OnDown, when set, is called when an incident opens as down or escalates
	// to down; main uses it to start the AI investigation.
	OnDown func(ctx context.Context, incidentID int64)
}

// Manager is called by the scheduler after each saved result.
type Manager struct {
	store  Store
	sender Sender
	log    *slog.Logger
	opts   Options
	now    func() time.Time
}

// New returns a Manager.
func New(st Store, sender Sender, log *slog.Logger, opts Options) *Manager {
	return &Manager{store: st, sender: sender, log: log, opts: opts, now: time.Now}
}

// Process updates the check's incident after res was saved and queues alerts.
// Errors are logged: incidents must never stop the checks.
func (m *Manager) Process(ctx context.Context, c check.Check, res check.Result) {
	recent, err := m.store.RecentResultsFor(ctx, c.ID, 3, c.UpdatedAt)
	if err != nil {
		m.log.Error("incident: recent results", "check_id", c.ID, "err", err)
		return
	}
	status := check.StatusFrom(recent)
	tr, err := m.store.ApplyStatus(ctx, c.ID, status, res.StartedAt, res.Error)
	if err != nil {
		m.log.Error("incident: apply status", "check_id", c.ID, "status", status, "err", err)
	} else {
		m.alert(ctx, c, res, tr)
	}
	m.certWarning(ctx, c, res)
}

func (m *Manager) alert(ctx context.Context, c check.Check, res check.Result, tr store.Transition) {
	inc := tr.Incident
	switch tr.Kind {
	case store.Opened, store.Changed, store.NoChange:
		if inc.ID == 0 {
			return // no open incident
		}
		if tr.Kind != store.NoChange {
			m.log.Warn("incident", "event", map[store.TransitionKind]string{store.Opened: "opened", store.Changed: "changed"}[tr.Kind],
				"incident_id", inc.ID, "check_id", c.ID, "name", c.Name, "status", inc.Status, "error", res.Error)
			if inc.Status == check.StatusDown && m.opts.OnDown != nil {
				defer m.opts.OnDown(ctx, inc.ID) // after the alert, so it is queued first
			}
		}
		// Checked on every run while the incident is open, so an alert that
		// could not be queued or recorded goes out on the next run.
		wants := inc.Status == check.StatusDown || m.opts.AlertOnDegraded
		if !wants || inc.Notified || !m.sender.Enabled() {
			return
		}
		if m.sender.Enqueue(notify.Message{Text: m.openedText(c, inc, res)}) {
			if err := m.store.MarkNotified(ctx, inc.ID); err != nil {
				m.log.Error("incident: mark notified", "incident_id", inc.ID, "err", err)
			}
		}
	case store.Resolved:
		m.log.Info("incident", "event", "resolved", "incident_id", inc.ID, "check_id", c.ID, "name", c.Name,
			"duration", inc.ResolvedAt.Sub(inc.StartedAt).Round(time.Second).String())
		// Only close what was announced, so a short blip never pings anyone.
		if inc.Notified && m.sender.Enabled() {
			m.sender.Enqueue(notify.Message{Text: m.resolvedText(c, inc)})
		}
	}
}

func (m *Manager) certWarning(ctx context.Context, c check.Check, res check.Result) {
	if res.CertExpiresAt == nil || !m.sender.Enabled() {
		return
	}
	left := res.CertExpiresAt.Sub(m.now())
	if left <= 0 {
		return // the check itself fails and opens an incident
	}
	days := int(left.Hours() / 24)
	threshold := 0
	for _, t := range CertThresholds {
		if days < t {
			threshold = t // the smallest threshold already crossed
		}
	}
	if threshold == 0 {
		return
	}
	fresh, err := m.store.RecordCertNotice(ctx, c.ID, *res.CertExpiresAt, threshold)
	if err != nil {
		m.log.Error("incident: cert notice", "check_id", c.ID, "err", err)
		return
	}
	if fresh && !m.sender.Enqueue(notify.Message{Text: m.certText(c, *res.CertExpiresAt, days)}) {
		if err := m.store.ForgetCertNotice(ctx, c.ID, *res.CertExpiresAt, threshold); err != nil {
			m.log.Error("incident: forget cert notice", "check_id", c.ID, "err", err)
		}
	}
}

// ---- messages (Telegram HTML; every dynamic value is escaped) ----

func (m *Manager) openedText(c check.Check, inc store.Incident, res check.Result) string {
	icon, verb := "🔴", "is down"
	if inc.Status == check.StatusDegraded {
		icon, verb = "🟠", "is degraded"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> %s\n", icon, esc(c.Name), verb)
	fmt.Fprintf(&b, "%s <code>%s</code>\n", strings.ToUpper(string(c.Kind)), esc(c.Target))
	if res.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", esc(res.Error))
	} else if res.StatusCode != nil {
		fmt.Fprintf(&b, "HTTP %d in %d ms\n", *res.StatusCode, res.TotalMS)
	}
	fmt.Fprintf(&b, "Since %s UTC", inc.StartedAt.UTC().Format("2006-01-02 15:04"))
	m.link(&b)
	return b.String()
}

func (m *Manager) resolvedText(c check.Check, inc store.Incident) string {
	var b strings.Builder
	d := inc.ResolvedAt.Sub(inc.StartedAt)
	fmt.Fprintf(&b, "✅ <b>%s</b> is operational again\n", esc(c.Name))
	fmt.Fprintf(&b, "Incident lasted %s", human(d))
	m.link(&b)
	return b.String()
}

func (m *Manager) certText(c check.Check, expires time.Time, days int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔐 The certificate of <b>%s</b> expires in %d days\n", esc(c.Name), days)
	fmt.Fprintf(&b, "<code>%s</code> · %s UTC", esc(c.Target), expires.UTC().Format("2006-01-02 15:04"))
	m.link(&b)
	return b.String()
}

func (m *Manager) link(b *strings.Builder) {
	if m.opts.PublicURL != "" {
		fmt.Fprintf(b, "\n<a href=\"%s\">Open dashboard</a>", esc(m.opts.PublicURL))
	}
}

// esc shortens and escapes a dynamic value. Telegram rejects messages over
// 4096 characters, and escaping can make a value five times longer.
func esc(s string) string {
	const maxRunes = 300
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes]) + "…"
	}
	return html.EscapeString(s)
}

// human prints a duration as "2 h 5 min" or "40 s".
func human(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d days %d h", int(d.Hours())/24, int(d.Hours())%24)
	}
}
