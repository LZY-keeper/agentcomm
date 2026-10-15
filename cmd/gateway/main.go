// Command gateway is the super-agent entry point. It assembles the three
// pillars (router / orchestrator / sandboxed tools) plus the v3 enterprise
// layer (durable tasks + HITL approvals, governance middleware, MCP HTTP
// endpoint, A2A v1.0), and serves them offline by default (mock LLM +
// embedding, no API key required).
package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentcomm/internal/a2a"
	"agentcomm/internal/approval"
	"agentcomm/internal/config"
	"agentcomm/internal/embedding"
	"agentcomm/internal/govern"
	"agentcomm/internal/llm"
	"agentcomm/internal/logx"
	"agentcomm/internal/mcp"
	"agentcomm/internal/obs"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/playground"
	"agentcomm/internal/rag"
	"agentcomm/internal/registry"
	"agentcomm/internal/router"
	"agentcomm/internal/store"
	"agentcomm/internal/taskapi"
	"agentcomm/internal/tools"
	"agentcomm/internal/types"
	"agentcomm/internal/wasm"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	flag.Parse()
	if v := os.Getenv("AGENTCOMM_VERSION"); v != "" {
		Version = v
	}

	cfg := config.Load()
	logx.Configure(cfg.LogLevel, cfg.LogFormat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logx.Info(ctx, "agentcomm gateway starting (v3)", "version", Version, "config", cfg.String())

	metrics := obs.NewMetrics()
	tracer, shutdownTracer, err := obs.InitTracer(ctx, "agentcomm-gateway", cfg.Obs.OTLPEndpoint, cfg.Obs.TraceStdout)
	if err != nil {
		logx.Warn(ctx, "tracer init failed (no-op)", "err", err)
		tracer = nil
		shutdownTracer = func(context.Context) error { return nil }
	}
	defer shutdownTracer(context.Background())
	_ = tracer

	// --- pillar 3 + tool backend: native tools (+ optional WASM sandbox) ---
	if root := os.Getenv("READ_ROOT"); root != "" {
		tools.WithReadRoot(root)
	}
	toolReg := tools.NewRegistry()
	registerBuiltinSafe(toolReg, cfg)
	if dir := os.Getenv("WASM_DIR"); dir != "" {
		if err := loadWasmTools(ctx, dir, toolReg); err != nil {
			logx.Warn(ctx, "WASM tools not loaded (continuing with native only)", "err", err)
		}
	}

	// --- pillar 1: federated tool router engine ---
	emb, err := embedding.New(cfg.Embedding)
	mustInit(ctx, err)
	cache := rag.NewCache(cfg.Router.CacheMaxEntries, time.Duration(cfg.Router.CacheTTLSeconds)*time.Second)
	if !cfg.Router.EnableCache {
		cache = nil
	}
	retriever := rag.NewRetriever(emb, rag.NewBruteForceIndex(), cache, cfg.Router.TopK, cfg.Router.SimThreshold)
	fb := router.NewBetaFeedback(0.1)
	pins := router.NewFingerprintStore(os.Getenv("TOOL_FINGERPRINTS_FILE"))
	rt := router.New(toolReg, retriever, router.Params{
		TopK: cfg.Router.TopK, TokenBudget: cfg.Router.TokenBudget, CandidateCap: 50,
		DenseWeight: cfg.Router.DenseWeight, SparseWeight: cfg.Router.SparseWeight, CapWeight: cfg.Router.CapabilityWeight,
	}, fb)
	rt.SetPinStore(pins) // v3: anti-rug-pull — alert (or block) on tool drift

	// --- stores (v3): memory default, redis when configured ---
	taskStore, sessionStore := newStores(ctx, cfg)
	defer taskStore.Close()
	defer sessionStore.Close()

	// --- LLM provider + pillar 2: orchestrator + task hub (HITL) ---
	provider, err := llm.New(cfg.LLM)
	mustInit(ctx, err)
	strat := orchestrator.Strategy(cfg.Orchestrator.LBStrategy)
	stats := orchestrator.NewAgentStats()
	orch := orchestrator.New(orchestrator.Deps{
		Router: rt, Tools: toolReg, LLM: provider,
		LB:       orchestrator.NewLoadBalancer(strat),
		Breaker:  orchestrator.NewBreakerManager(cfg.Orchestrator.BreakerFailThreshold, cfg.Orchestrator.BreakerOpenSecs),
		Feedback: fb, Metrics: metrics, Stats: stats,
	})
	orch.RegisterAgent(orchestrator.NewLLMAgent("math-1", "数学助手",
		[]string{"math"}, provider, "你是数学助手，能用 calculator 工具计算。"))

	gate := approval.NewGate()
	budget := govern.NewMemBudget()
	hub := orchestrator.NewHub(orch, taskStore, sessionStore, gate,
		time.Duration(cfg.Store.ApprovalTimeoutSecs)*time.Second, cfg.Store.SessionMaxTurns)
	hub.SetStream(orch.Stream)
	hub.SetUsageSink(budget) // per-key token accounting → budget enforcement
	orch.SetHooks(&orchestrator.LoopHooks{
		OnBeforeTool: hub.ApprovalHooks().OnBeforeTool,
		OnAfterTool:  makeResultLimiter(cfg.Tools.MaxResultBytes),
	})
	hub.RecoverPending(ctx) // re-arm parked approvals after a restart (redis)
	go func() {             // background refresh keeps fingerprints + routing fresh
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = rt.Refresh(ctx)
			}
		}
	}()

	// --- HTTP surface: playground + taskapi + A2A v1.0 + MCP + governance ---
	reg := registry.New(30 * time.Second)
	publicURL := publicURLOf(cfg)
	a2aSrv := a2a.NewServer("agentcomm-gateway", Version, publicURL, orch, hub, taskStore, gate)
	mcpSrv := mcp.NewServer("agentcomm-gateway", Version, toolReg)

	pg := playground.NewHandler(hub, orch, metrics, Version, map[string]string{
		"llm": cfg.LLM.Provider, "embedding": cfg.Embedding.Provider,
	})
	pg.SetEvalReportDir(cfg.Eval.ReportDir) // v3.1: playground 评测报告面板（只读）
	mux := http.NewServeMux()
	pg.Mount(mux)
	taskapi.NewHandler(hub, taskStore, gate).Mount(mux)
	a2aSrv.Mount(mux, "/a2a")
	mux.Handle("/mcp", mcpSrv) // MCP Streamable HTTP (builtin tools federation-out)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + Version + `","major":"3"}`))
	})
	mux.Handle("/metrics", metrics.HTTPHandler())

	// v3 governance chain: request id → auth → rate limit → budget. Disabled
	// unless keys are configured — the offline demo keeps zero friction.
	handler := govern.Chain(mux, govern.RequestID())
	if cfg.KeysConfigured() {
		keys, err := govern.LoadKeys(ctx, cfg.Govern.KeysInline, cfg.Govern.KeysFile)
		if err != nil {
			logx.Error(ctx, "fatal: govern keys", "err", err)
			os.Exit(1)
		}
		handler = govern.Chain(handler,
			govern.Auth(keys,
				govern.WithLoopbackAllowed(cfg.Govern.AllowLoopback),
				govern.WithExemptPrefixes(cfg.Govern.ExemptPrefixes...),
				govern.WithProtectedPrefixes(cfg.Govern.ProtectedPrefixes...)),
			govern.RateLimit(govern.NewTokenBucketLimiter(cfg.Govern.DefaultRPS, cfg.Govern.DefaultBurst, keys)),
			govern.Budget(budget, cfg.Govern.DefaultBudget, 0.9),
		)
		logx.Info(ctx, "governance enabled", "keys", keys.Count(),
			"protected", strings.Join(cfg.Govern.ProtectedPrefixes, ","))
	} else {
		logx.Info(ctx, "governance disabled (no keys configured; set GOV_KEYS or GOV_KEYS_FILE)")
	}

	// Optional gRPC service (only compiled in with -tags grpc; see grpc.go).
	maybeStartGRPC(ctx, cfg, orch, reg)

	srv := &http.Server{Addr: cfg.Gateway.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logx.Info(ctx, "playground + gateway listening", "addr", cfg.Gateway.HTTPAddr,
			"endpoints", []string{"/", "/api/chat", "/api/tasks", "/a2a/", "/mcp", "/healthz", "/metrics"})
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.Error(ctx, "http server error", "err", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logx.Info(ctx, "shutdown signal received")
	hub.Shutdown()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	logx.Info(ctx, "gateway stopped")
}

// makeResultLimiter truncates oversized tool results before they enter the
// LLM context (v3: unbounded results blow the window and the budget).
func makeResultLimiter(maxBytes int) func(ctx context.Context, call types.ToolCall,
	res types.ToolResult, err error, dur time.Duration) types.ToolResult {
	if maxBytes <= 0 {
		maxBytes = 16384
	}
	return func(ctx context.Context, call types.ToolCall, res types.ToolResult, err error, _ time.Duration) types.ToolResult {
		size := 0
		for _, c := range res.Content {
			size += len(c.Text)
		}
		if size <= maxBytes {
			return res
		}
		truncated := types.ToolResult{ToolName: res.ToolName, IsError: res.IsError,
			Meta: map[string]any{"truncated": true, "original_bytes": size}}
		left := maxBytes
		for _, c := range res.Content {
			if left <= 0 {
				break
			}
			t := c.Text
			if len(t) > left {
				t = t[:left] + "…[截断]"
			}
			left -= len(t)
			truncated.Content = append(truncated.Content, types.ToolContent{Type: c.Type, Text: t})
		}
		logx.Warn(ctx, "tool result truncated", "tool", call.ToolName, "bytes", size, "limit", maxBytes)
		return truncated
	}
}

// registerBuiltinSafe registers the native tools with configured limits and
// env-driven approval defaults (TOOLS_REQUIRE_APPROVAL).
func registerBuiltinSafe(reg *tools.Registry, cfg config.Config) {
	tools.MaxFileBytes = int64(cfg.Tools.MaxFileBytes)
	tools.BuiltinInto(reg)
	reg.ApplyApprovalDefaults(cfg.Tools.ApprovalDefaults)
}

// newStores picks the persistence backend: redis when REDIS_ADDR is set (or
// STORE_BACKEND=redis), else in-memory with TTL.
func newStores(ctx context.Context, cfg config.Config) (store.TaskStore, store.SessionStore) {
	backend := cfg.Store.Backend
	if backend == "" || backend == "auto" {
		if cfg.Redis.Addr != "" {
			backend = "redis"
		} else {
			backend = "memory"
		}
	}
	taskTTL := time.Duration(cfg.Store.TaskTTLSeconds) * time.Second
	sessTTL := time.Duration(cfg.Store.SessionTTLSeconds) * time.Second
	sweep := time.Duration(cfg.Store.SweepIntervalSecs) * time.Second
	if backend == "redis" {
		rs := store.NewRedisStores(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, taskTTL, nil)
		if err := rs.Ping(ctx); err != nil {
			logx.Warn(ctx, "redis unreachable; falling back to in-memory stores", "addr", cfg.Redis.Addr, "err", err)
		} else {
			logx.Info(ctx, "redis stores enabled", "addr", cfg.Redis.Addr)
			return rs, rs
		}
	}
	return store.NewMemoryTaskStore(taskTTL, sweep, nil), store.NewMemorySessionStore(sessTTL, nil)
}

// publicURLOf derives the externally advertised card URL.
func publicURLOf(cfg config.Config) string {
	if u := os.Getenv("PUBLIC_URL"); u != "" {
		return strings.TrimRight(u, "/") + "/"
	}
	_, port, err := net.SplitHostPort(cfg.Gateway.HTTPAddr)
	if err != nil || port == "" {
		port = "8080"
	}
	return "http://localhost:" + port + "/"
}

var _ = strconv.Itoa // placate imports until config helpers grow

// loadWasmTools loads every .wasm under dir into the registry via the sandbox.
func loadWasmTools(ctx context.Context, dir string, reg *tools.Registry) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	host, err := wasm.NewHost(ctx)
	if err != nil {
		return err
	}
	ts, err := host.LoadDir(ctx, abs)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if err := t.RegisterInto(reg); err != nil {
			logx.Warn(ctx, "wasm tool register failed", "tool", t.Name(), "err", err)
			continue
		}
		logx.Info(ctx, "loaded wasm tool", "tool", t.Name())
	}
	return nil
}

func mustInit(ctx context.Context, err error) {
	if err != nil {
		logx.Error(ctx, "fatal init", "err", err)
		os.Exit(1)
	}
}
