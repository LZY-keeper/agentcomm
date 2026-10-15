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

const ProtocolVersion = "2025-06-18"
