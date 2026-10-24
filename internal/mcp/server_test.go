package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// api is a fake Watchkeeper API that records requests.
type api struct {
	mu   sync.Mutex
	reqs []string
}

func (a *api) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		body, _ := io.ReadAll(r.Body)
		a.reqs = append(a.reqs, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		a.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/summary":
			_, _ = w.Write([]byte(`{"data":{"openIncidents":1,"down":1}}`))
		case r.URL.Path == "/api/v1/incidents/404":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"title":"Not found","status":404,"detail":"incident not found"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/triage"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"triageId":7,"state":"running"}}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	})
}

// session sends lines to a server and returns the decoded responses.
func session(t *testing.T, token string, lines ...string) ([]map[string]any, *api) {
	t.Helper()
	fake := &api{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client, err := NewClient(srv.URL+"/", token)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Tools(client), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	dec := json.NewDecoder(&out)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		resps = append(resps, m)
	}
	return resps, fake
}

func call(id int, name, args string) string {
	return `{"jsonrpc":"2.0","id":` + string(rune('0'+id)) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func text(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestHandshakeAndList(t *testing.T) {
	resps, _ := session(t, "secret-token",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`not json`,
	)
	if len(resps) != 5 {
		t.Fatalf("%d responses, want 5 (the notification gets none): %v", len(resps), resps)
	}
	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["serverInfo"].(map[string]any)["name"] != "watchkeeper" {
		t.Errorf("initialize = %v", init)
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names = append(names, m["name"].(string))
		if m["inputSchema"].(map[string]any)["type"] != "object" {
			t.Errorf("%s: schema must be an object", m["name"])
		}
	}
	if strings.Join(names, ",") != "get_summary,list_checks,get_check_results,list_incidents,get_incident,investigate_incident,set_check_enabled" {
		t.Errorf("tools = %v", names)
	}
	if resps[3]["error"].(map[string]any)["code"] != float64(codeMethodNotFound) {
		t.Errorf("unknown method: %v", resps[3])
	}
	if resps[4]["error"].(map[string]any)["code"] != float64(codeParse) {
		t.Errorf("bad JSON: %v", resps[4])
	}
}

func TestUnknownProtocolVersionGetsLatest(t *testing.T) {
	resps, _ := session(t, "secret-token", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := resps[0]["result"].(map[string]any)["protocolVersion"]; v != protocolVersions[0] {
		t.Errorf("version = %v", v)
	}
}

func TestToolCalls(t *testing.T) {
	resps, fake := session(t, "secret-token",
		call(1, "get_summary", `{}`),
		call(2, "get_check_results", `{"check_id":3,"limit":500}`),
		call(3, "list_incidents", `{"open_only":true,"check_id":2}`),
		call(4, "investigate_incident", `{"incident_id":9}`),
		call(5, "set_check_enabled", `{"check_id":3,"enabled":false}`),
		call(6, "get_incident", `{"incident_id":404}`),
		call(7, "get_incident", `{"incident_id":1.5}`),
		call(8, "nope", `{}`),
	)
	if got, isErr := text(t, resps[0]); isErr || got != `{"openIncidents":1,"down":1}` {
		t.Errorf("summary = %s (error %v)", got, isErr)
	}
	if got, _ := text(t, resps[3]); !strings.Contains(got, `"triageId":7`) {
		t.Errorf("investigate = %s", got)
	}
	if got, isErr := text(t, resps[5]); !isErr || !strings.Contains(got, "404: incident not found") {
		t.Errorf("not found = %s (error %v)", got, isErr)
	}
	if got, isErr := text(t, resps[6]); !isErr || !strings.Contains(got, "positive integer") {
		t.Errorf("bad id = %s (error %v)", got, isErr)
	}
	if resps[7]["error"] == nil {
		t.Errorf("unknown tool is a protocol error: %v", resps[7])
	}
	want := []string{
		"GET /api/v1/summary ",
		"GET /api/v1/checks/3/results?limit=100 ",
		"GET /api/v1/incidents?checkId=2&limit=10&open=true ",
		"POST /api/v1/incidents/9/triage ",
		`PATCH /api/v1/checks/3 {"enabled":false}`,
		"GET /api/v1/incidents/404 ",
	}
	if strings.Join(fake.reqs, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(fake.reqs, "\n"), strings.Join(want, "\n"))
	}
}

func TestUnauthorized(t *testing.T) {
	resps, _ := session(t, "wrong", call(1, "list_checks", `{}`))
	if got, isErr := text(t, resps[0]); !isErr || !strings.Contains(got, "WK_API_TOKEN") {
		t.Errorf("got %s (error %v)", got, isErr)
	}
}

func TestNewClientRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://x", "localhost:8090", "http://"} {
		if _, err := NewClient(u, ""); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
