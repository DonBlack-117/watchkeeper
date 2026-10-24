// Package mcp is a Model Context Protocol server over stdio that lets an AI
// assistant (Claude Code, Claude Desktop) read Watchkeeper's checks and
// incidents and start investigations, through the REST API and its token.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
)

// protocolVersions are the MCP revisions this server speaks, newest first.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// maxMessage caps one JSON-RPC line read from the client.
const maxMessage = 1 << 20

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server answers MCP requests read from r and writes responses to w.
type Server struct {
	tools   []Tool
	byName  map[string]Tool
	version string
	log     *slog.Logger
	mu      sync.Mutex // one response line at a time
	out     io.Writer
}

// NewServer returns a server exposing tools. version is reported to clients.
func NewServer(tools []Tool, version string, log *slog.Logger) *Server {
	s := &Server{tools: tools, byName: map[string]Tool{}, version: version, log: log}
	for _, t := range tools {
		s.byName[t.Name] = t
	}
	return s
}

// Serve handles newline-delimited JSON-RPC messages until r ends or ctx is
// cancelled. Requests run one after another, in order.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.out = w
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		s.handle(ctx, line)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(response{ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error"}})
		return
	}
	notification := len(req.ID) == 0
	if req.JSONRPC != "2.0" || req.Method == "" {
		if !notification {
			s.write(response{ID: req.ID, Error: &rpcError{codeInvalidRequest, "invalid request"}})
		}
		return
	}
	result, rerr := s.dispatch(ctx, req)
	if notification {
		return // notifications/initialized and the like get no answer
	}
	s.write(response{ID: req.ID, Result: result, Error: rerr})
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "watchkeeper", "version": s.version},
			"instructions": "Watchkeeper monitors HTTP, DNS and TLS endpoints. Use get_summary for an overview, " +
				"list_incidents and get_incident to see what is failing, and investigate_incident to have Claude " +
				"diagnose an incident (it costs API tokens and finishes within a few minutes; read the result with get_incident).",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, t.describe())
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "invalid params"}
		}
		t, ok := s.byName[p.Name]
		if !ok {
			return nil, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
		}
		args := map[string]any{}
		if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				return toolError("arguments must be a JSON object"), nil
			}
		}
		text, err := t.Run(ctx, args)
		if err != nil {
			s.log.Debug("tool failed", "tool", p.Name, "err", err)
			return toolError(err.Error()), nil
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}, nil
	}
	return nil, &rpcError{codeMethodNotFound, "method not found: " + req.Method}
}

// toolError is a failed tool call: reported in the result so the model sees it.
func toolError(msg string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true}
}

func (s *Server) write(r response) {
	r.JSONRPC = "2.0"
	b, err := json.Marshal(r)
	if err != nil {
		b = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		s.log.Error("write response", "err", err)
	}
}
