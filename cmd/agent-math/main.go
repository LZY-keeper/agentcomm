// Command agent-math is a standalone specialist agent (A2A server) that serves
// math queries and registers itself with the discovery registry — demonstrating
// the distributed multi-agent story (vs the gateway's in-process default).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/a2a"
	"github.com/LZY-keeper/agentcomm/internal/config"
	"github.com/LZY-keeper/agentcomm/internal/embedding"
	"github.com/LZY-keeper/agentcomm/internal/llm"
	"github.com/LZY-keeper/agentcomm/internal/logx"
	"github.com/LZY-keeper/agentcomm/internal/orchestrator"
	"github.com/LZY-keeper/agentcomm/internal/rag"
	"github.com/LZY-keeper/agentcomm/internal/router"
	"github.com/LZY-keeper/agentcomm/internal/tools"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

func main() {
	cfg := config.Load()
	logx.Configure(cfg.LogLevel, cfg.LogFormat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := getenv("AGENT_PORT", "5001")
	regURL := getenv("REGISTRY_URL", "http://localhost:8500")
	publicURL := "http://localhost:" + port + "/"

	reg := tools.Builtin()
	emb, err := embedding.New(cfg.Embedding)
	if err != nil {
		logx.Error(ctx, "embedding", "err", err)
		os.Exit(1)
	}
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), nil, 5, 0.0)
	rt := router.New(reg, retr, router.Params{TopK: 5, TokenBudget: 800, CapWeight: 0.1}, router.NewBetaFeedback(0.1))
	provider, err := llm.New(cfg.LLM)
	if err != nil {
		logx.Error(ctx, "llm", "err", err)
		os.Exit(1)
	}
	orch := orchestrator.New(orchestrator.Deps{Router: rt, Tools: reg, LLM: provider})
	orch.RegisterAgent(orchestrator.NewLLMAgent("math-1", "数学助手", []string{"math"}, provider, "你是数学助手，能用 calculator 工具计算。"))

	srv := a2a.NewServer("math-agent", "1.0", publicURL, orch, nil, nil, nil)
	mux := http.NewServeMux()
	srv.Mount(mux, "/")

	httpSrv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logx.Info(ctx, "math agent (A2A) listening", "addr", httpSrv.Addr, "card", publicURL+".well-known/agent-card.json")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logx.Error(ctx, "serve", "err", err)
		}
	}()

	// register with discovery (best-effort, with retry)
	go registerLoop(ctx, regURL, types.AgentInfo{
		ID: "math-1", Name: "数学助手", URL: publicURL, Skills: []string{"math"}, Tags: []string{"math"}, Version: "1.0",
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	shut, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	_ = httpSrv.Shutdown(shut)
}

func registerLoop(ctx context.Context, regURL string, info types.AgentInfo) {
	body, _ := json.Marshal(info)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, regURL+"/agents", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		// heartbeat
		hb, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/agents/heartbeat/%s", regURL, info.ID), nil)
		if hresp, err := http.DefaultClient.Do(hb); err == nil {
			hresp.Body.Close()
		}
		time.Sleep(10 * time.Second)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
