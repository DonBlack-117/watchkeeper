package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Tool is one MCP tool backed by the Watchkeeper API.
type Tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	ReadOnly    bool
	Run         func(ctx context.Context, args map[string]any) (string, error)
}

func (t Tool) describe() map[string]any {
	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": t.Description,
		"inputSchema": t.Schema,
		"annotations": map[string]any{
			"title":           t.Title,
			"readOnlyHint":    t.ReadOnly,
			"destructiveHint": false,
			"openWorldHint":   false,
		},
	}
}

// Client calls the Watchkeeper REST API.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient returns a client for the API at baseURL (for example
// https://status.example.com) that sends token as a bearer token.
func NewClient(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("WK_URL must be an http or https URL, got %q", baseURL)
	}
	return &Client{base: u, token: token, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// maxResponse caps an API response passed on to the model.
const maxResponse = 256 << 10

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (string, error) {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach Watchkeeper at %s", c.base.Redacted())
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return "", errors.New("reading the response failed")
	}
	if len(b) > maxResponse {
		return "", errors.New("the response is too large; ask for fewer items")
	}
	if resp.StatusCode >= 400 {
		var p struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(b, &p)
		msg := p.Detail
		if msg == "" {
			msg = p.Title
		}
		if resp.StatusCode == http.StatusUnauthorized {
			msg = "unauthorized: set WK_API_TOKEN to the server's API token"
		}
		return "", fmt.Errorf("watchkeeper answered %d: %s", resp.StatusCode, msg)
	}
	// Unwrap {"data": ...} so the model reads the payload directly.
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err == nil && len(env.Data) > 0 {
		return string(env.Data), nil
	}
	return string(b), nil
}

// Tools returns the tools served by `watchkeeper mcp`.
func Tools(c *Client) []Tool {
	obj := func(props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	id := func(desc string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "description": desc}
	}
	return []Tool{
		{
			Name: "get_summary", Title: "Monitoring summary", ReadOnly: true,
			Description: "Overview of the last 24 hours: how many checks are operational, degraded or down, open incidents, availability, p95 latency, alert channels and whether AI triage is enabled.",
			Schema:      obj(map[string]any{}),
			Run: func(ctx context.Context, _ map[string]any) (string, error) {
				return c.do(ctx, http.MethodGet, "/api/v1/summary", nil, nil)
			},
		},
		{
			Name: "list_checks", Title: "List checks", ReadOnly: true,
			Description: "All checks with their target, interval, current status (operational, degraded, down, unknown), last result and 24-hour availability and p95 latency.",
			Schema:      obj(map[string]any{}),
			Run: func(ctx context.Context, _ map[string]any) (string, error) {
				return c.do(ctx, http.MethodGet, "/api/v1/checks", nil, nil)
			},
		},
		{
			Name: "get_check_results", Title: "Check run history", ReadOnly: true,
			Description: "Latest runs of one check, newest first, with HTTP status, per-phase timings (dns, connect, tls, ttfb) and error category.",
			Schema: obj(map[string]any{
				"check_id": id("The check id from list_checks."),
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many runs (default 20)."},
			}, "check_id"),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				checkID, err := intArg(a, "check_id", 0, true)
				if err != nil {
					return "", err
				}
				limit, err := intArg(a, "limit", 20, false)
				if err != nil {
					return "", err
				}
				q := url.Values{"limit": {strconv.Itoa(min(max(limit, 1), 100))}}
				return c.do(ctx, http.MethodGet, "/api/v1/checks/"+strconv.Itoa(checkID)+"/results", q, nil)
			},
		},
		{
			Name: "list_incidents", Title: "List incidents", ReadOnly: true,
			Description: "Incidents newest first, each with its timeline and, when investigated, the AI diagnosis (summary, probable cause, confidence, evidence and next steps).",
			Schema: obj(map[string]any{
				"open_only": map[string]any{"type": "boolean", "description": "Only incidents still open."},
				"check_id":  id("Only incidents of this check."),
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "How many (default 10)."},
			}),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				q := url.Values{}
				limit, err := intArg(a, "limit", 10, false)
				if err != nil {
					return "", err
				}
				q.Set("limit", strconv.Itoa(min(max(limit, 1), 50)))
				if v, ok := a["open_only"].(bool); ok && v {
					q.Set("open", "true")
				}
				if checkID, err := intArg(a, "check_id", 0, false); err != nil {
					return "", err
				} else if checkID > 0 {
					q.Set("checkId", strconv.Itoa(checkID))
				}
				return c.do(ctx, http.MethodGet, "/api/v1/incidents", q, nil)
			},
		},
		{
			Name: "get_incident", Title: "Get incident", ReadOnly: true,
			Description: "One incident with its timeline and the newest AI investigation, including its state (running, done, failed) and the tools it used.",
			Schema:      obj(map[string]any{"incident_id": id("The incident id.")}, "incident_id"),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				incID, err := intArg(a, "incident_id", 0, true)
				if err != nil {
					return "", err
				}
				return c.do(ctx, http.MethodGet, "/api/v1/incidents/"+strconv.Itoa(incID), nil, nil)
			},
		},
		{
			Name: "investigate_incident", Title: "Investigate incident with Claude",
			Description: "Start an AI investigation of an incident on the Watchkeeper server. It probes DNS, HTTP and TLS of the check and reads its history, then stores a diagnosis. It uses Anthropic API tokens and is limited per day. It returns right away; call get_incident after a minute to read the result.",
			Schema:      obj(map[string]any{"incident_id": id("The incident id.")}, "incident_id"),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				incID, err := intArg(a, "incident_id", 0, true)
				if err != nil {
					return "", err
				}
				return c.do(ctx, http.MethodPost, "/api/v1/incidents/"+strconv.Itoa(incID)+"/triage", nil, nil)
			},
		},
		{
			Name: "set_check_enabled", Title: "Pause or resume a check",
			Description: "Pause (enabled=false) or resume (enabled=true) a check, for example during planned maintenance. Pausing resolves its open incident.",
			Schema: obj(map[string]any{
				"check_id": id("The check id from list_checks."),
				"enabled":  map[string]any{"type": "boolean"},
			}, "check_id", "enabled"),
			Run: func(ctx context.Context, a map[string]any) (string, error) {
				checkID, err := intArg(a, "check_id", 0, true)
				if err != nil {
					return "", err
				}
				enabled, ok := a["enabled"].(bool)
				if !ok {
					return "", errors.New("enabled must be true or false")
				}
				return c.do(ctx, http.MethodPatch, "/api/v1/checks/"+strconv.Itoa(checkID), nil, map[string]any{"enabled": enabled})
			},
		},
	}
}

// intArg reads a positive integer argument; JSON numbers arrive as float64.
func intArg(a map[string]any, name string, def int, required bool) (int, error) {
	v, ok := a[name]
	if !ok || v == nil {
		if required {
			return 0, fmt.Errorf("%s is required", name)
		}
		return def, nil
	}
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) || f < 1 || f > 1<<53 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return int(f), nil
}
