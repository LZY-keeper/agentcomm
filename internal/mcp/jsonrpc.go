// Package mcp implements the Model Context Protocol (2025-06-18) wire format
// directly in Go (stdlib only), so the gateway exposes and consumes MCP tools
// without an opaque SDK dependency. It implements the parts the project uses:
// initialize handshake, notifications/initialized, tools/list, tools/call over
// stdio and Streamable HTTP. (v1's hand-rolled MCP never sent initialize and
// had mismatched SSE endpoints — both fixed here.)
package mcp

import "encoding/json"

// JSONRPC 2.0 envelope.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

func newResponse(id json.RawMessage) Response {
	return Response{JSONRPC: "2.0", ID: id}
}

func errResp(id json.RawMessage, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

// notification is a Request with no ID.
func (r Request) IsNotification() bool {
	return len(r.ID) == 0
}

// --- MCP-specific shapes ---

type InitializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ClientInfo      ClientInfo      `json:"clientInfo"`
}
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      ClientInfo     `json:"serverInfo"`
}
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}
type ToolsListResult struct {
	Tools []ToolDef `json:"tools"`
}

type ToolCallParams struct {
	Name string         `json:"name"`
	Args map[string]any `json:"arguments,omitempty"`
}
type ToolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
type ToolCallResult struct {
	Content []ToolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// Protocol versions the gateway speaks (v3):
//   - 2025-06-18: baseline from v2 (initialize + Streamable HTTP).
//   - 2025-11-25: current default fallback per the official client guidance.
//   - 2026-07-28: newest revision — stateless core (no initialize/Session-Id
//     requirement). Our HTTP server is stateless per request, so it tolerates
//     sessionless clients; tools/* methods are unchanged for our subset.
const (
	ProtocolVersion       = "2025-06-18"
	ProtocolVersionLatest = "2025-11-25"
	ProtocolVersionNext   = "2026-07-28"
)

// supportedProtocols is the negotiation table server-side.
var supportedProtocols = map[string]bool{
	ProtocolVersion: true, ProtocolVersionLatest: true, ProtocolVersionNext: true,
}

// negotiateProtocol picks the protocol both sides support; unknown client
// versions get ProtocolVersionLatest (spec: respond with the server's latest
// when there is no overlap).
func negotiateProtocol(requested string) string {
	if supportedProtocols[requested] {
		return requested
	}
	return ProtocolVersionLatest
}
