package triage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/DonBlack-117/watchkeeper/internal/check"
	"github.com/DonBlack-117/watchkeeper/internal/store"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// fakeMessages replays canned responses and records every request.
type fakeMessages struct {
	mu        sync.Mutex
	responses []string // JSON bodies of BetaMessage
	err       error
	requests  []anthropic.BetaMessageNewParams
}

func (f *fakeMessages) New(_ context.Context, p anthropic.BetaMessageNewParams, _ ...option.RequestOption) (*anthropic.BetaMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, p)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) == 0 {
		return nil, errors.New("no more canned responses")
	}
	var m anthropic.BetaMessage
	if err := json.Unmarshal([]byte(f.responses[0]), &m); err != nil {
		return nil, err
	}
	f.responses = f.responses[1:]
	return &m, nil
}

func toolUse(stop string, blocks ...string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"` + stop +
		`","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50},"content":[` + strings.Join(blocks, ",") + `]}`
}

func useBlock(id, name, input string) string {
	return `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}`
}

func textBlock(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"text","text":` + string(b) + `}`
}

const finalJSON = `{"summary":"api.example.com no longer resolves.","probable_cause":"The A record was removed.","confidence":"high","evidence":["dns_lookup A: dns: no such host"," "],"next_steps":["Restore the A record"]}`

// fakeData and fakeProber stand in for the store and the runner.
type fakeData struct{}

func (fakeData) ListResults(_ context.Context, q store.ResultQuery) ([]check.Result, error) {
	return []check.Result{{CheckID: q.CheckID, OK: false, Error: "dns: no such host"}, {CheckID: q.CheckID, OK: true}}, nil
}
func (fakeData) ListIncidents(context.Context, store.IncidentQuery) ([]store.Incident, error) {
	return []store.Incident{{ID: 1, Status: check.StatusDown, Triage: &store.Triage{State: store.TriageDone, ProbableCause: "expired cert"}}}, nil
}
func (fakeData) HourlyLatency(context.Context, int64, time.Time, time.Time) ([]store.HourLatency, error) {
	return []store.HourLatency{{Total: 60, OK: 60}}, nil
}

type fakeProber struct {
	mu    sync.Mutex
	hosts []string
}

func (p *fakeProber) Run(_ context.Context, c check.Check) check.Result {
	return check.Result{CheckID: c.ID, Error: "dns: no such host", TotalMS: 12}
}
func (p *fakeProber) LookupDNS(_ context.Context, host, recordType string) ([]string, error) {
	p.mu.Lock()
	p.hosts = append(p.hosts, host)
	p.mu.Unlock()
	if recordType == "A" {
		return nil, errors.New("dns: no such host")
	}
	return []string{"ns1.example.net."}, nil
}
func (p *fakeProber) InspectTLS(context.Context, string, string) (check.TLSInfo, error) {
	return check.TLSInfo{DaysLeft: 40}, nil
}

func testCheck() check.Check {
	return check.Check{ID: 5, Name: "API", Kind: check.KindHTTP, Target: "https://api.example.com/health", IntervalS: 60, TimeoutMS: 5000, Enabled: true}
}

func testIncident() store.Incident {
	return store.Incident{ID: 9, CheckID: 5, CheckName: "API", Status: check.StatusDown, StartedAt: now.Add(-10 * time.Minute),
		Updates: []store.IncidentUpdate{{At: now.Add(-10 * time.Minute), Status: "down", Error: "dns: no such host"}}}
}

func testTools(p *fakeProber) *toolbox {
	return &toolbox{check: testCheck(), data: fakeData{}, prober: p, now: func() time.Time { return now }}
}

func TestInvestigate(t *testing.T) {
	msgs := &fakeMessages{responses: []string{
		// Two tools in one turn: both results must go back in one message.
		toolUse("tool_use", textBlock("Checking DNS."), useBlock("tu_1", "dns_lookup", `{"record_type":"A"}`),
			useBlock("tu_2", "recent_results", `{"limit":5}`)),
		toolUse("end_turn", textBlock(finalJSON)),
	}}
	prober := &fakeProber{}
	agent := NewAgent(msgs, "claude-opus-5-5", "high")
	report, err := agent.Investigate(context.Background(), testCheck(), testIncident(), testTools(prober))
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary != "api.example.com no longer resolves." || report.Confidence != "high" {
		t.Errorf("report = %+v", report)
	}
	if len(report.Evidence) != 1 {
		t.Errorf("blank evidence items are dropped: %q", report.Evidence)
	}
	if len(report.ToolCalls) != 2 || report.ToolCalls[0].Name != "dns_lookup" || report.ToolCalls[0].Input != `{"record_type":"A"}` ||
		!strings.Contains(report.ToolCalls[0].Outcome, "no such host") || report.ToolCalls[1].Outcome != "2 runs, 1 failed" {
		t.Errorf("tool calls = %+v", report.ToolCalls)
	}
	if report.InputTokens != 300 || report.OutputTokens != 40 {
		t.Errorf("tokens = %d in, %d out", report.InputTokens, report.OutputTokens)
	}
	if len(prober.hosts) != 1 || prober.hosts[0] != "api.example.com" {
		t.Errorf("dns lookups went to %v, want only the check's host", prober.hosts)
	}

	if len(msgs.requests) != 2 {
		t.Fatalf("%d requests, want 2", len(msgs.requests))
	}
	first, second := msgs.requests[0], msgs.requests[1]
	if first.Model != "claude-opus-5-5" || first.OutputConfig.Effort != "high" || len(first.Tools) != 6 {
		t.Errorf("first request: model %q, effort %q, %d tools", first.Model, first.OutputConfig.Effort, len(first.Tools))
	}
	if len(second.Messages) != 3 {
		t.Fatalf("second request has %d messages, want user, assistant, tool results", len(second.Messages))
	}
	body, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"fallbacks":"default"`, `"tool_use_id":"tu_1"`, `"tool_use_id":"tu_2"`, `"is_error":true`,
		`"strict":true`, `"additionalProperties":false`, `"json_schema"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("request body lacks %s", want)
		}
	}
	if results := second.Messages[2].Content; len(results) != 2 {
		t.Errorf("tool results in %d blocks, want 2 in one message", len(results))
	}
	if !strings.Contains(first.Messages[0].Content[0].OfText.Text, "Target: https://api.example.com/health") {
		t.Errorf("first message: %s", first.Messages[0].Content[0].OfText.Text)
	}
}

func TestInvestigateFailures(t *testing.T) {
	cases := []struct {
		name string
		msgs *fakeMessages
		want string
	}{
		{"refusal", &fakeMessages{responses: []string{toolUse("refusal")}}, "declined"},
		{"max tokens", &fakeMessages{responses: []string{toolUse("max_tokens", textBlock(`{"summ`))}}, "token limit"},
		{"bad json", &fakeMessages{responses: []string{toolUse("end_turn", textBlock("it is DNS"))}}, "not valid JSON"},
		{"empty summary", &fakeMessages{responses: []string{toolUse("end_turn", textBlock(`{"summary":" ","probable_cause":"","confidence":"low","evidence":[],"next_steps":[]}`))}}, "no summary"},
		{"api error", &fakeMessages{err: &anthropic.Error{StatusCode: http.StatusUnauthorized}}, "rejected the key"},
		{"rate limited", &fakeMessages{err: &anthropic.Error{StatusCode: http.StatusTooManyRequests}}, "rate limit"},
		{"network", &fakeMessages{err: errors.New("dial tcp: connection refused")}, "could not reach"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := NewAgent(tc.msgs, "claude-opus-5-5", "high")
			_, err := agent.Investigate(context.Background(), testCheck(), testIncident(), testTools(&fakeProber{}))
			var f *Failure
			if !errors.As(err, &f) || !strings.Contains(f.Reason, tc.want) {
				t.Errorf("err = %v, want a Failure mentioning %q", err, tc.want)
			}
		})
	}
}

func TestInvestigateStopsAfterMaxTurns(t *testing.T) {
	var responses []string
	for range maxTurns + 2 {
		responses = append(responses, toolUse("tool_use", useBlock("tu", "probe_now", `{}`)))
	}
	msgs := &fakeMessages{responses: responses}
	_, err := NewAgent(msgs, "m", "low").Investigate(context.Background(), testCheck(), testIncident(), testTools(&fakeProber{}))
	var f *Failure
	if !errors.As(err, &f) || !strings.Contains(f.Reason, "without a diagnosis") {
		t.Fatalf("err = %v", err)
	}
	if len(msgs.requests) != maxTurns || len(f.Calls) != maxTurns {
		t.Errorf("%d requests and %d calls, want %d", len(msgs.requests), len(f.Calls), maxTurns)
	}
	if f.InputTokens == 0 {
		t.Error("a failed run keeps its token counts")
	}
}

func TestToolbox(t *testing.T) {
	tb := testTools(&fakeProber{})
	ctx := context.Background()

	out, outcome, isErr := tb.run(ctx, "past_incidents", json.RawMessage(`{"limit":3}`))
	if isErr || !strings.Contains(out, `"probableCause":"expired cert"`) || outcome != "1 incidents" {
		t.Errorf("past_incidents: %s / %s / %v", out, outcome, isErr)
	}
	if _, outcome, isErr = tb.run(ctx, "latency_by_hour", json.RawMessage(`{"hours":500}`)); isErr || outcome != "1 hours" {
		t.Errorf("latency_by_hour: %s / %v", outcome, isErr)
	}
	if _, outcome, isErr = tb.run(ctx, "probe_now", nil); isErr || outcome != "failed: dns: no such host in 12 ms" {
		t.Errorf("probe_now: %s / %v", outcome, isErr)
	}
	if _, outcome, isErr = tb.run(ctx, "inspect_tls", json.RawMessage(`{}`)); isErr || outcome != "40 days left" {
		t.Errorf("inspect_tls: %s / %v", outcome, isErr)
	}
	if out, _, isErr = tb.run(ctx, "rm_rf", nil); !isErr || !strings.Contains(out, "unknown tool") {
		t.Errorf("unknown tool: %s / %v", out, isErr)
	}
	if _, _, isErr = tb.run(ctx, "recent_results", json.RawMessage(`{"limit":"x"}`)); !isErr {
		t.Error("invalid input is an error result")
	}

	dns := &toolbox{check: check.Check{Kind: check.KindDNS, Target: "example.org"}, data: fakeData{}, prober: &fakeProber{}, now: time.Now}
	if out, _, isErr := dns.run(ctx, "inspect_tls", nil); !isErr || !strings.Contains(out, "does not use TLS") {
		t.Errorf("inspect_tls on a dns check: %s / %v", out, isErr)
	}
}

func TestClamp(t *testing.T) {
	if clamp(0, 1, 50) != 1 || clamp(80, 1, 50) != 50 || clamp(7, 1, 50) != 7 {
		t.Error("clamp")
	}
}
