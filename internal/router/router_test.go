package router

import (
	"context"
	"testing"

	"github.com/LZY-keeper/agentcomm/internal/embedding"
	"github.com/LZY-keeper/agentcomm/internal/rag"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

func newTestRouter(t *testing.T, budget int) *Router {
	t.Helper()
	emb := embedding.NewMock(512)
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), rag.NewCache(200, 0), 8, 0.0)
	src := NewStaticSource(DemoTools()...)
	p := Params{TopK: 5, TokenBudget: budget, CandidateCap: 50, DenseWeight: 0.6, SparseWeight: 0.3, CapWeight: 0.1}
	r := New(src, retr, p, NewBetaFeedback(0.1))
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return r
}

// TestSelect_PacksAndSaves is the M2 headline test: the router picks the
// value-optimal subset within a token budget and the packed size is materially
// smaller than naive full injection.
func TestSelect_PacksAndSaves(t *testing.T) {
	r := newTestRouter(t, 400) // tight budget forces packing
	sel, err := r.Select(context.Background(), "计算 123 + 456 等于多少", 0)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(sel.Selected) == 0 {
		t.Fatal("no tools selected")
	}
	// arithmetic query must surface a math tool at the top.
	if sel.Selected[0].Tool.ServerID != "math" {
		t.Fatalf("top tool server=%s want math; selected=%+v", sel.Selected[0].Tool.ServerID, names(sel.Selected))
	}
	if sel.PackedTokens >= sel.FullTokens {
		t.Fatalf("packing did not save tokens: full=%d packed=%d", sel.FullTokens, sel.PackedTokens)
	}
	if sel.TokenSavedPct < 30 {
		t.Fatalf("expected >30%% tokens saved, got %.1f%%", sel.TokenSavedPct)
	}
	t.Logf("query=%q candidates=%d selected=%v", sel.Query, sel.Candidates, names(sel.Selected))
	t.Logf("tokens full=%d packed=%d saved=%d (%.1f%%)", sel.FullTokens, sel.PackedTokens, sel.TokenSaved, sel.TokenSavedPct)
}

// TestSelect_KnapsackRespectsBudget verifies the knapsack never exceeds budget.
func TestSelect_KnapsackRespectsBudget(t *testing.T) {
	for _, budget := range []int{100, 200, 400, 800, 2000} {
		r := newTestRouter(t, budget)
		sel, _ := r.Select(context.Background(), "计算", 0)
		if sel.PackedTokens > budget && len(sel.Selected) > 1 {
			t.Fatalf("budget=%d packed=%d (single-tool fallback allowed)", budget, sel.PackedTokens)
		}
		t.Logf("budget=%4d packed=%d selected=%d", budget, sel.PackedTokens, len(sel.Selected))
	}
}

// TestSelect_FeedbackRewards verifies recording success raises a tool's rank.
func TestSelect_FeedbackRewards(t *testing.T) {
	fb := NewBetaFeedback(0.5)
	emb := embedding.NewMock(256)
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), nil, 8, 0.0)
	src := NewStaticSource(DemoTools()...)
	r := New(src, retr, Params{TopK: 3, TokenBudget: 800, CapWeight: 0.1}, fb)
	_ = r.Refresh(context.Background())

	// reward code_review many times, then query code → it should rank higher.
	// Feedback is keyed by tool Name (see toolKey in feedback.go): selected sets
	// are name-deduped, and the orchestrator records outcomes by ToolCall.ToolName.
	for i := 0; i < 20; i++ {
		fb.Record("code_review", true)
	}
	sel, _ := r.Select(context.Background(), "代码", 0)
	if sel.Selected[0].Tool.Name != "code_review" {
		t.Fatalf("expected feedback to promote code_review, top=%s", sel.Selected[0].Tool.Name)
	}
	t.Logf("after feedback, top for '代码' = %s", sel.Selected[0].Tool.Name)
}

func names(s []types.ScoredTool) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Tool.ServerID + "/" + x.Tool.Name
	}
	return out
}
