package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentcomm/internal/types"
)

// Client speaks the Streamable HTTP transport to an upstream MCP server,
// performing the mandatory initialize → notifications/initialized handshake
// before listing/calling tools. Implements router.ToolSource for federation.
type Client struct {
	name    string
	base    string
	httpCli *http.Client
	counter uint64
	once    sync.Once
	initErr error
}

func NewClient(name, baseURL string) *Client {
	return &Client{
		name: name, base: strings.TrimRight(baseURL, "/"),
		httpCli: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Name() string { return c.name }

// ensureInit performs the handshake once.
func (c *Client) ensureInit(ctx context.Context) error {
	c.once.Do(func() {
		if _, err := c.request(ctx, "initialize", map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      ClientInfo{Name: "agentcomm-router", Version: "1.0"},
		}); err != nil {
			c.initErr = err
			return
		}
		// notifications/initialized — no response expected.
		_, _ = c.request(ctx, "notifications/initialized", nil)
	})
	return c.initErr
}

// Tools lists tools from the upstream server (router.ToolSource).
func (c *Client) Tools(ctx context.Context) ([]types.Tool, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	raw, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var lr ToolsListResult
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, err
	}
	out := make([]types.Tool, 0, len(lr.Tools))
	for _, td := range lr.Tools {
		out = append(out, types.Tool{ServerID: c.name, Name: td.Name, Description: td.Description, InputSchema: mapToSchema(td.InputSchema)})
	}
	return out, nil
}

// Call invokes a tool on the upstream server.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (types.ToolResult, error) {
	if err := c.ensureInit(ctx); err != nil {
		return types.ToolResult{}, err
	}
	raw, err := c.request(ctx, "tools/call", ToolCallParams{Name: name, Args: args})
	if err != nil {
		return types.ToolResult{}, err
	}
	var r ToolCallResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return types.ToolResult{}, err
	}
	res := types.ToolResult{ToolName: name, IsError: r.IsError}
	for _, ct := range r.Content {
		res.Content = append(res.Content, types.ToolContent{Type: types.ContentText, Text: ct.Text})
	}
	return res, nil
}

// request sends a JSON-RPC request to POST /mcp and returns the result field.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("%s-%d", c.name, atomic.AddUint64(&c.counter, 1))
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/mcp", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mcp[%s] upstream %d: %s", c.name, resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	// Streamable HTTP may return SSE; extract the first data: payload.
	payload := extractPayload(string(rb))
	var rpc Response
	if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
		return nil, fmt.Errorf("mcp[%s] decode: %w", c.name, err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp[%s] %d %s", c.name, rpc.Error.Code, rpc.Error.Message)
	}
	return rawResult(rpc.Result), nil
}

func extractPayload(s string) string {
	// Streamable HTTP may reply with SSE (lines starting with "data:"), carrying
	// one or more JSON-RPC frames. The methods we use (initialize, tools/list,
	// tools/call) each yield a single result frame, so return the first non-empty
	// data payload. (Joining all frames would concatenate them into invalid JSON.)
	if !strings.Contains(s, "data:") {
		return s
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			if p := strings.TrimSpace(strings.TrimPrefix(line, "data:")); p != "" {
				return p
			}
		}
	}
	return s
}

func rawResult(r any) json.RawMessage {
	if r == nil {
		return nil
	}
	b, _ := json.Marshal(r)
	return b
}

func mapToSchema(m map[string]any) types.ToolSchema {
	b, _ := json.Marshal(m)
	var s types.ToolSchema
	_ = json.Unmarshal(b, &s)
	return s
}
