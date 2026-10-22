// federation.go wires MCP_SERVERS upstreams into the local tool registry.
// Each remote tool becomes a forwarding-handler entry (see internal/wasm's
// RegisterInto idiom), so approval / audit / truncation / fingerprint hooks
// apply unchanged; local registrations keep precedence on name conflicts
// (trust ordering: builtin > WASM > remote). An upstream that is down at
// boot is skipped with a warning and retried on the periodic refresh cycle —
// federation degrades per-upstream, never takes the gateway down.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentcomm/internal/config"
	"agentcomm/internal/logx"
	"agentcomm/internal/mcp"
	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

// federation tracks upstream MCP clients for retry-on-refresh.
type federation struct {
	mu      sync.Mutex
	pending []mcpPending // upstreams unreachable at boot, retried periodically
	reg     *tools.Registry
	count   int // successfully federated upstream count
}

type mcpPending struct {
	name string
	url  string
	cli  *mcp.Client
}

func wireMCPServers(ctx context.Context, cfg config.Config, reg *tools.Registry) *federation {
	f := &federation{reg: reg}
	for i, raw := range cfg.Router.MCPServers {
		name, url := mcpUpstreamName(i, raw)
		cli := mcp.NewClient(name, url)
		mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ts, err := cli.Tools(mctx)
		cancel()
		if err != nil {
			logx.Warn(ctx, "mcp upstream unreachable at boot (will retry on refresh)", "server", name, "url", url, "err", err)
			f.pending = append(f.pending, mcpPending{name: name, url: url, cli: cli})
			continue
		}
		f.register(ctx, name, cli, ts)
	}
	logx.Info(ctx, "mcp federation ready", "upstreams", f.count, "pending-retry", len(f.pending))
	return f
}

// register installs a remote tool set into the registry. Remote tools never
// overwrite an existing (local) entry — lookup order is trust order.
func (f *federation) register(ctx context.Context, name string, cli *mcp.Client, ts []types.Tool) {
	added := 0
	for _, t := range ts {
		if _, exists := f.reg.Get(t.Name); exists {
			logx.Warn(ctx, "mcp tool name conflict; keeping local", "server", name, "tool", t.Name)
			continue
		}
		t.ServerID = name
		cli, tname := cli, t.Name
		f.reg.Register(t.Name, t.Description, t.InputSchema,
			func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
				return cli.Call(ctx, tname, args)
			},
			tools.WithServerID(name), tools.WithOutputTypes(t.OutputTypes...))
		added++
	}
	f.count++
	logx.Info(ctx, "mcp upstream federated", "server", name, "tools", added)
}

// retryUnreached re-probes boot-time-unreachable upstreams (refresh cycle).
func (f *federation) retryUnreached(ctx context.Context) {
	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		return
	}
	pending := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, p := range pending {
		mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ts, err := p.cli.Tools(mctx)
		cancel()
		if err != nil {
			f.mu.Lock()
			f.pending = append(f.pending, p) // still down: back into the queue
			f.mu.Unlock()
			continue
		}
		f.register(ctx, p.name, p.cli, ts)
	}
}

// mcpUpstreamName derives a stable server id from an MCP_SERVERS entry:
// "name@http://..." names it explicitly; otherwise the URL host (or a
// positional fallback) is used.
func mcpUpstreamName(i int, raw string) (name, url string) {
	url = raw
	if at := strings.LastIndex(raw, "@"); at > 0 && strings.Contains(raw[at+1:], "://") {
		return raw[:at], raw[at+1:]
	}
	if host := hostOf(raw); host != "" {
		return host, raw
	}
	return fmt.Sprintf("mcp-%d", i+1), raw
}

func hostOf(raw string) string {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
