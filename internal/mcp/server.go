package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

// Server exposes a tools.Registry over MCP (stdio + Streamable HTTP).
type Server struct {
	name    string
	version string
	tools   *tools.Registry
}

func NewServer(name, version string, reg *tools.Registry) *Server {
	return &Server{name: name, version: version, tools: reg}
}

// Handle dispatches a single JSON-RPC request. v3: the initialize handshake
// negotiates the protocol version (2025-06-18 / 2025-11-25 / 2026-07-28);
// all other methods are stateless and version-agnostic for our subset, which
// is exactly what the 2026-07-28 stateless-core revision requires.
func (s *Server) Handle(ctx context.Context, req Request) Response {
	resp := newResponse(req.ID)
	switch req.Method {
	case "initialize":
		var ip InitializeParams
		_ = json.Unmarshal(req.Params, &ip)
		resp.Result = InitializeResult{
			ProtocolVersion: negotiateProtocol(ip.ProtocolVersion),
			Capabilities: map[string]any{
				"tools":     map[string]any{"listChanged": true},
				"resources": map[string]any{},
				"prompts":   map[string]any{},
				"logging":   map[string]any{},
			},
			ServerInfo: ClientInfo{Name: s.name, Version: s.version},
		}
	case "notifications/initialized":
		// notification — no response (handled by caller for stdio; HTTP returns 202)
		resp.ID = nil
		resp.Result = nil
	case "tools/list":
		ts, err := s.tools.Tools(ctx)
		if err != nil {
			return errResp(req.ID, ErrInternal, err.Error())
		}
		out := make([]ToolDef, 0, len(ts))
		for _, t := range ts {
			out = append(out, ToolDef{Name: t.Name, Description: t.Description, InputSchema: schemaToMap(t.InputSchema)})
		}
		resp.Result = ToolsListResult{Tools: out}
	case "tools/call":
		var p ToolCallParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, ErrInvalidParams, err.Error())
		}
		res, err := s.tools.Call(ctx, p.Name, p.Args)
		tc := ToolCallResult{IsError: res.IsError}
		for _, c := range res.Content {
			tc.Content = append(tc.Content, ToolCallContent{Type: string(c.Type), Text: c.Text})
		}
		if err != nil && !res.IsError {
			return errResp(req.ID, ErrInternal, err.Error())
		}
		resp.Result = tc
	case "ping":
		resp.Result = map[string]any{}
	default:
		return errResp(req.ID, ErrMethodNotFound, "method not found: "+req.Method)
	}
	return resp
}

func schemaToMap(s types.ToolSchema) map[string]any {
	// Marshal/unmarshal through JSON to coerce the typed schema into a generic
	// map (MCP wants a raw JSON Schema object).
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{"type": "object"}
	}
	return m
}

// ServeStdio runs the stdio transport: newline-delimited JSON-RPC on r/w.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(errResp(nil, ErrParseError, err.Error()))
			continue
		}
		resp := s.Handle(ctx, req)
		if req.IsNotification() {
			continue // notifications get no response
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ServeHTTP implements the Streamable HTTP transport (POST /mcp), making *Server
// a standard http.Handler so it can be mounted directly (e.g. mux.Handle("/mcp",
// srv)). The server replies with application/json (non-streaming) per request —
// a valid mode per the 2025-06-18 spec.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResp(w, http.StatusBadRequest, errResp(nil, ErrParseError, err.Error()))
		return
	}
	resp := s.Handle(r.Context(), req)
	if req.IsNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSONResp(w, http.StatusOK, resp)
}

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
