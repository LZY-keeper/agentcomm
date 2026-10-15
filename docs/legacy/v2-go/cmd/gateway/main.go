// Command gateway is the super-agent entry point. It assembles the three
// pillars (router / orchestrator / sandboxed tools) plus the Web Playground and
// observability endpoints, and serves them offline by default (mock LLM +
// embedding, no API key required).
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"agentcomm/internal/config"
	"agentcomm/internal/embedding"
	"agentcomm/internal/a2a"
	"agentcomm/internal/llm"
	"agentcomm/internal/logx"
	"agentcomm/internal/obs"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/playground"
	"agentcomm/internal/rag"
	"agentcomm/internal/registry"
	"agentcomm/internal/router"
	"agentcomm/internal/tools"
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

	logx.Info(ctx, "agentcomm gateway starting", "version", Version, "config", cfg.String())

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
	toolReg := tools.Builtin()
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
	rt := router.New(toolReg, retriever, router.Params{
		TopK: cfg.Router.TopK, TokenBudget: cfg.Router.TokenBudget, CandidateCap: 50,
		DenseWeight: cfg.Router.DenseWeight, SparseWeight: cfg.Router.SparseWeight, CapWeight: cfg.Router.CapabilityWeight,
	}, fb)

	// --- LLM provider + pillar 2: orchestrator ---
	provider, err := llm.New(cfg.LLM)
	mustInit(ctx, err)
	strat := orchestrator.Strategy(cfg.Orchestrator.LBStrategy)
	orch := orchestrator.New(orchestrator.Deps{
		Router: rt, Tools: toolReg, LLM: provider,
		LB:      orchestrator.NewLoadBalancer(strat),
		Breaker: orchestrator.NewBreakerManager(cfg.Orchestrator.BreakerFailThreshold, cfg.Orchestrator.BreakerOpenSecs),
		Feedback: fb, Metrics: metrics,
	})
	orch.RegisterAgent(orchestrator.NewLLMAgent("math-1", "数学助手",
		[]string{"math"}, provider, "你是数学助手，能用 calculator 工具计算。"))

	// --- HTTP server: playground + A2A + metrics ---
	reg := registry.New(30 * time.Second)
	a2aSrv := a2a.NewServer("agentcomm-gateway", Version, "http://localhost"+cfg.Gateway.HTTPAddr+"/", orch)

	pg := playground.NewHandler(orch, metrics, Version, map[string]string{
		"llm": cfg.LLM.Provider, "embedding": cfg.Embedding.Provider,
	})
	mux := http.NewServeMux()
	pg.Mount(mux)
	a2aSrv.Mount(mux, "/a2a")
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + Version + `"}`))
	})
	mux.Handle("/metrics", metrics.HTTPHandler())

	// Optional gRPC service (only compiled in with -tags grpc; see grpc.go).
	maybeStartGRPC(ctx, cfg, orch, reg)

	srv := &http.Server{Addr: cfg.Gateway.HTTPAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logx.Info(ctx, "playground + gateway listening", "addr", cfg.Gateway.HTTPAddr,
			"endpoints", []string{"/", "/api/chat", "/api/meta", "/healthz", "/metrics"})
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.Error(ctx, "http server error", "err", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logx.Info(ctx, "shutdown signal received")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	logx.Info(ctx, "gateway stopped")
}

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
