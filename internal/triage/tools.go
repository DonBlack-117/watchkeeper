package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

// Data is the history the tools read; *store.Store implements it.
type Data interface {
	ListResults(ctx context.Context, q store.ResultQuery) ([]check.Result, error)
	ListIncidents(ctx context.Context, q store.IncidentQuery) ([]store.Incident, error)
	HourlyLatency(ctx context.Context, checkID int64, from, to time.Time) ([]store.HourLatency, error)
}

// Prober runs live probes; *check.Runner implements it.
type Prober interface {
	Run(ctx context.Context, c check.Check) check.Result
	LookupDNS(ctx context.Context, host, recordType string) ([]string, error)
	InspectTLS(ctx context.Context, host, addr string) (check.TLSInfo, error)
}

// toolTimeout bounds one live probe, on top of the check's own timeout.
const toolTimeout = 15 * time.Second

// toolbox runs the agent's tools for one incident. Every probe targets the
// incident's own check: the model picks what to look at, never where to
// connect, so text in a response or a DNS record cannot steer it elsewhere.
type toolbox struct {
	check  check.Check
	data   Data
	prober Prober
	now    func() time.Time
}

// definitions returns the tool schemas sent to the model, in a fixed order so
// the prompt cache prefix stays the same between runs.
func (t *toolbox) definitions() []anthropic.BetaToolUnionParam {
	tool := func(name, desc string, props map[string]any, required ...string) anthropic.BetaToolUnionParam {
		if required == nil {
			required = []string{}
		}
		return anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        name,
			Description: anthropic.String(desc),
			Strict:      anthropic.Bool(true),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties:  props,
				Required:    required,
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}}
	}
	return []anthropic.BetaToolUnionParam{
		tool("recent_results",
			"Latest stored runs of the check, newest first: ok, HTTP status, total time, per-phase timings (dns, connect, tls, ttfb in ms), certificate expiry and error category. Use it first to see when and how the check started failing.",
			map[string]any{"limit": map[string]any{"type": "integer", "description": "How many runs, 1 to 50."}}, "limit"),
		tool("latency_by_hour",
			"p50 and p95 response time of successful runs per hour, with total and successful run counts. Use it to tell a slow service from a failing one, or to find when latency started rising.",
			map[string]any{"hours": map[string]any{"type": "integer", "description": "How many hours back, 1 to 168."}}, "hours"),
		tool("past_incidents",
			"Earlier incidents of the same check with their timelines and, when they were investigated, the probable cause found then. Use it to spot recurring problems.",
			map[string]any{"limit": map[string]any{"type": "integer", "description": "How many incidents, 1 to 20."}}, "limit"),
		tool("dns_lookup",
			"Resolve the check's host name now, like dig. Returns the records or a short error category such as 'dns: no such host'.",
			map[string]any{"record_type": map[string]any{"type": "string", "enum": check.DNSRecordTypes}}, "record_type"),
		tool("probe_now",
			"Run the check once right now, like curl with timings, and return the result. It is not stored. Use it to see whether the problem is still happening.",
			map[string]any{}),
		tool("inspect_tls",
			"Connect to the check's TLS endpoint and describe the certificate: subject, issuer, names, validity dates, days left, chain length and why it fails verification, if it does. Only for https and tls checks.",
			map[string]any{}),
	}
}

// run executes one tool call and returns the text for the tool_result block,
// a short outcome for the stored timeline, and whether it failed.
func (t *toolbox) run(ctx context.Context, name string, raw json.RawMessage) (content, outcome string, isError bool) {
	var in struct {
		Limit      int    `json:"limit"`
		Hours      int    `json:"hours"`
		RecordType string `json:"record_type"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "invalid input: " + err.Error(), "invalid input", true
		}
	}
	out, outcome, err := t.dispatch(ctx, name, in.Limit, in.Hours, in.RecordType)
	if err != nil {
		return err.Error(), err.Error(), true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "could not encode the result", "encoding error", true
	}
	return string(b), outcome, false
}

func (t *toolbox) dispatch(ctx context.Context, name string, limit, hours int, recordType string) (any, string, error) {
	c := t.check
	switch name {
	case "recent_results":
		limit = clamp(limit, 1, 50)
		res, err := t.data.ListResults(ctx, store.ResultQuery{CheckID: c.ID, Limit: limit})
		if err != nil {
			return nil, "", errors.New("could not read the stored results")
		}
		failed := 0
		for _, r := range res {
			if !r.OK {
				failed++
			}
		}
		return res, fmt.Sprintf("%d runs, %d failed", len(res), failed), nil
	case "latency_by_hour":
		hours = clamp(hours, 1, 168)
		to := t.now().UTC().Truncate(time.Hour).Add(time.Hour)
		lat, err := t.data.HourlyLatency(ctx, c.ID, to.Add(-time.Duration(hours)*time.Hour), to)
		if err != nil {
			return nil, "", errors.New("could not read the latency history")
		}
		return lat, fmt.Sprintf("%d hours", len(lat)), nil
	case "past_incidents":
		limit = clamp(limit, 1, 20)
		incs, err := t.data.ListIncidents(ctx, store.IncidentQuery{CheckID: c.ID, Limit: limit})
		if err != nil {
			return nil, "", errors.New("could not read the incident history")
		}
		type past struct {
			ID            int64                  `json:"id"`
			Status        check.Status           `json:"status"`
			StartedAt     time.Time              `json:"startedAt"`
			ResolvedAt    *time.Time             `json:"resolvedAt"`
			Updates       []store.IncidentUpdate `json:"updates"`
			ProbableCause string                 `json:"probableCause,omitempty"`
		}
		out := make([]past, 0, len(incs))
		for _, inc := range incs {
			p := past{ID: inc.ID, Status: inc.Status, StartedAt: inc.StartedAt, ResolvedAt: inc.ResolvedAt, Updates: inc.Updates}
			if inc.Triage != nil && inc.Triage.State == store.TriageDone {
				p.ProbableCause = inc.Triage.ProbableCause
			}
			out = append(out, p)
		}
		return out, fmt.Sprintf("%d incidents", len(out)), nil
	case "dns_lookup":
		host := check.Host(c)
		if host == "" {
			return nil, "", errors.New("the check has no host name")
		}
		pctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		records, err := t.prober.LookupDNS(pctx, host, recordType)
		if err != nil {
			return nil, "", fmt.Errorf("%s %s: %w", recordType, host, err)
		}
		return map[string]any{"host": host, "type": recordType, "records": records},
			fmt.Sprintf("%s: %d records", recordType, len(records)), nil
	case "probe_now":
		pctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		res := t.prober.Run(pctx, c)
		outcome := "ok"
		if !res.OK {
			outcome = "failed: " + res.Error
		}
		return res, fmt.Sprintf("%s in %d ms", outcome, res.TotalMS), nil
	case "inspect_tls":
		host, addr, ok := check.TLSEndpoint(c)
		if !ok {
			return nil, "", errors.New("this check does not use TLS")
		}
		pctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		info, err := t.prober.InspectTLS(pctx, host, addr)
		if err != nil {
			return nil, "", err
		}
		outcome := fmt.Sprintf("%d days left", info.DaysLeft)
		if info.VerifyError != "" {
			outcome += ", " + info.VerifyError
		}
		return info, outcome, nil
	}
	return nil, "", fmt.Errorf("unknown tool %q", name)
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
