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
	"strings"
	"sync"

	"github.com/LZY-keeper/agentcomm/internal/types"
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

// WithServerID overrides the owning server id (federation: remote tools
// registered into the local registry keep their upstream identity for audit
// and fingerprint keying — "server/name" — instead of "builtin").
func WithServerID(id string) Opt { return func(t *types.Tool) { t.ServerID = id } }

// WithRequireApproval marks the tool as human-approval-gated (HITL): the
// orchestrator parks any execution in input-required until an operator
// approves or denies via the approval API.
func WithRequireApproval() Opt { return func(t *types.Tool) { t.RequireApproval = true } }

// ApplyApprovalDefaults flips RequireApproval on the named tools (env-driven
// policy: TOOLS_REQUIRE_APPROVAL=write_file,deploy,...).
func (r *Registry) ApplyApprovalDefaults(names []string) {
	if len(names) == 0 {
		return
	}
	want := map[string]struct{}{}
	for _, n := range names {
		want[strings.TrimSpace(n)] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, e := range r.entries {
		if _, ok := want[name]; ok {
			e.Tool.RequireApproval = true
			r.entries[name] = e
		}
	}
}

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
	BuiltinInto(r)
	return r
}

// BuiltinInto registers the default tool set into an existing registry
// (v3: lets cmd/ own the registry instance and apply policy afterwards).
func BuiltinInto(r *Registry) {
	RegisterCalculator(r)
	RegisterWeather(r)
	RegisterReadFile(r)
	RegisterCodeReview(r)
}
