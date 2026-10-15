package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

// manifestReq asks a tool to describe itself.
type manifestReq struct {
	Manifest bool `json:"manifest"`
}

// manifestDoc is the JSON a tool returns for a manifest request.
type manifestDoc struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema types.ToolSchema `json:"input_schema"`
	InputTypes  []string         `json:"input_types,omitempty"`
	OutputTypes []string         `json:"output_types,omitempty"`
}

// Manifest asks a tool to self-describe (convention: call with {"manifest":true}).
func (t *Tool) Manifest(ctx context.Context) (types.Tool, error) {
	req, _ := json.Marshal(manifestReq{Manifest: true})
	res, err := t.Call(ctx, string(req))
	if err != nil {
		return types.Tool{}, err
	}
	var doc manifestDoc
	if err := json.Unmarshal([]byte(res.Text), &doc); err != nil {
		return types.Tool{}, fmt.Errorf("wasm[%s]: bad manifest: %w", t.name, err)
	}
	if doc.Name == "" {
		doc.Name = t.name
	}
	return types.Tool{
		ServerID: "wasm", Name: doc.Name, Description: doc.Description,
		InputSchema: doc.InputSchema, InputTypes: doc.InputTypes, OutputTypes: doc.OutputTypes,
	}, nil
}

// Source exposes a set of loaded WASM tools as a router.ToolSource (federation).
// Manifests are fetched lazily and cached.
type Source struct {
	host     *Host
	tools    []*Tool
	cache    []types.Tool
	mu       sync.Mutex
	resolved bool
}

func NewSource(host *Host, ts ...*Tool) *Source { return &Source{host: host, tools: ts} }

func (s *Source) Tools(ctx context.Context) ([]types.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolved {
		return s.cache, nil
	}
	out := make([]types.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		desc, err := t.Manifest(ctx)
		if err != nil {
			return nil, fmt.Errorf("wasm source: %w", err)
		}
		out = append(out, desc)
	}
	s.cache = out
	s.resolved = true
	return out, nil
}

// RegisterInto registers a WASM tool into a native tools.Registry so the MCP
// server / orchestrator can call it uniformly. The handler marshals args to
// JSON, runs the sandboxed module, and maps the result back.
func (t *Tool) RegisterInto(reg *tools.Registry) error {
	ctx := context.Background()
	desc, err := t.Manifest(ctx)
	if err != nil {
		return err
	}
	opts := []tools.Opt{}
	if len(desc.InputTypes) > 0 {
		opts = append(opts, tools.WithInputTypes(desc.InputTypes...))
	}
	if len(desc.OutputTypes) > 0 {
		opts = append(opts, tools.WithOutputTypes(desc.OutputTypes...))
	}
	reg.Register(desc.Name, desc.Description+" [wasm 沙箱]", desc.InputSchema,
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			b, err := json.Marshal(args)
			if err != nil {
				return types.ToolResult{}, err
			}
			res, err := t.Call(ctx, string(b))
			if err != nil {
				return types.ToolResult{IsError: true}, err
			}
			return types.ToolResult{
				IsError: res.IsError,
				Content: []types.ToolContent{{Type: types.ContentText, Text: res.Text}},
			}, nil
		}, opts...)
	return nil
}
