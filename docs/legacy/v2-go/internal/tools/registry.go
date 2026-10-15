// Package tools is the native Go tool backend: a registry of built-in MCP tools
// (calculator, weather, file reader, code review) that ship in-process so the
// system runs offline with no external MCP server or WASM toolchain. It also
// satisfies router.ToolSource, feeding the federated router.
//
// Untrusted/3rd-party tools are isolated in the WASM sandbox (internal/wasm);
// native tools here are trusted in-process handlers.
package tools

import (
	"context"
	"fmt"
	"sync"

	"agentcomm/internal/types"
)

// Handler executes a tool call and returns an MCP-shaped result.
type Handler func(ctx context.Context, args map[string]any) (types.ToolResult, error)

// Entry pairs a tool descriptor with its handler.
type Entry struct {
	Tool    types.Tool
	Handler Handler
}

// Registry is a concurrency-safe map of tool name -> entry. The empty serverID
// is treated as "builtin".
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

func NewRegistry() *Registry { return &Registry{entries: map[string]Entry{}} }

// Register adds a tool. OutputTypes/InputTypes describe its capability contract
// (used by the router's capability-graph signal).
func (r *Registry) Register(name, desc string, schema types.ToolSchema, h Handler, opts ...Opt) {
	t := types.Tool{ServerID: "builtin", Name: name, Description: desc, InputSchema: schema}
	for _, o := range opts {
		o(&t)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[name] = Entry{Tool: t, Handler: h}
}

// Opt configures a tool descriptor at registration.
type Opt func(*types.Tool)

func WithInputTypes(s ...string) Opt  { return func(t *types.Tool) { t.InputTypes = s } }
func WithOutputTypes(s ...string) Opt { return func(t *types.Tool) { t.OutputTypes = s } }

func (r *Registry) Get(name string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	return e, ok
}

// Call invokes a tool by name.
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (types.ToolResult, error) {
	e, ok := r.Get(name)
	if !ok {
		return types.ToolResult{ToolName: name, IsError: true, Content: []types.ToolContent{{Type: types.ContentText, Text: "tool not found"}}}, fmt.Errorf("tool not found: %s", name)
	}
	res, err := e.Handler(ctx, args)
	res.ToolName = name
	if err != nil {
		res.IsError = true
		res.Content = []types.ToolContent{{Type: types.ContentText, Text: err.Error()}}
	}
	return res, err
}

// Tools returns descriptors for all registered tools (router.ToolSource).
func (r *Registry) Tools(_ context.Context) ([]types.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.Tool, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Tool)
	}
	return out, nil
}

// Builtin registers the default offline-runnable tool set.
func Builtin() *Registry {
	r := NewRegistry()
	RegisterCalculator(r)
	RegisterWeather(r)
	RegisterReadFile(r)
	RegisterCodeReview(r)
	return r
}
