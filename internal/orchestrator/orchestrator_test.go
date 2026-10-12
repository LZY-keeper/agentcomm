package orchestrator

import (
	"context"
	"strings"
	"testing"

	"agentcomm/internal/config"
	"agentcomm/internal/embedding"
	"agentcomm/internal/llm"
	"agentcomm/internal/rag"
	"agentcomm/internal/router"
	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

func newTestOrchestrator(t *testing.T) (*Orchestrator, *router.Router) {
	t.Helper()
	reg := tools.Builtin()
	emb := embedding.NewMock(256)
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), nil, 5, 0.0)
	rt := router.New(reg, retr, router.Params{TopK: 5, TokenBudget: 800, CapWeight: 0.1}, router.NewBetaFeedback(0.1))
	prov, err := llm.New(config.LLMConfig{Provider: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	o := New(Deps{Router: rt, Tools: reg, LLM: prov})
	o.RegisterAgent(NewLLMAgent("math-1", "数学助手", []string{"math"}, prov,
		"你是数学助手，能用 calculator 工具计算。"))
	return o, rt
}

func collectTokens(t *testing.T, ch <-chan types.Token) string {
	t.Helper()
	var sb strings.Builder
	for tk := range ch {
		sb.WriteString(tk.Text)
	}
	return sb.String()
}

// TestStream_MathAgenticLoop verifies the offline end-to-end demo core:
// math query → math agent → RAG-selects calculator → tool executes →
// streamed final answer contains the computed result.
func TestStream_MathAgenticLoop(t *testing.T) {
	o, _ := newTestOrchestrator(t)
	ch, dec, err := o.Stream(context.Background(), "计算 2+3", nil, "sess-1")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	answer := collectTokens(t, ch)

	if !strings.Contains(answer, "5") {
		t.Fatalf("answer missing tool result 5: %q", answer)
	}
	if dec.Agent.ID != "math-1" {
		t.Fatalf("routed to %s, want math-1", dec.Agent.ID)
	}
	if len(dec.ToolCalls) == 0 {
		t.Fatalf("expected a tool call in decision, got none")
	}
	if dec.TokenSavedPct <= 0 {
		t.Fatalf("expected token savings, got %.1f%%", dec.TokenSavedPct)
	}
	t.Logf("intent=%s agent=%s toolCalls=%d tokensFull=%d packed=%d saved=%.1f%%",
		dec.Intent, dec.Agent.ID, len(dec.ToolCalls), dec.FullTokenSize, dec.PackedTokenSize, dec.TokenSavedPct)
	t.Logf("answer: %q", answer)
}
