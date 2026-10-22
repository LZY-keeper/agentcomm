// Package router implements pillar 1: the semantic + token-budget-aware
// federated MCP tool router engine. It selects, from a federation of MCP
// servers' tools, the value-optimal subset that fits the LLM's token budget —
// attacking the "tool sprawl → token explosion" problem at the gateway layer.
//
// Pipeline: Refresh (federate + index) → Select (hybrid retrieval → feedback →
// dedup → token-budget knapsack packing → accounting).
package router

import (
	"context"
	"sync"

	"github.com/LZY-keeper/agentcomm/internal/rag"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

// Params tunes the router engine.
type Params struct {
	TopK         int     // max tools to return (after packing)
	TokenBudget  int     // max tokens reserved for tool schemas
	CandidateCap int     // max pre-pack candidates fed to the knapsack
	SimThreshold float64 // minimum fused score to keep (0 = off)
	DenseWeight  float64
	SparseWeight float64
	CapWeight    float64
}

// Selection is the outcome of Select — the chosen tools plus full vs packed
// token accounting (the headline "tokens saved" metric).
type Selection struct {
	Query         string
	Candidates    int
	Selected      []types.ScoredTool
	FullTokens    int
	PackedTokens  int
	TokenSaved    int
	TokenSavedPct float64
	Hybrid        bool
}

// Router is the federated tool router engine. It is safe for concurrent use.
type Router struct {
	source    ToolSource
	retriever *rag.Retriever
	hybrid    *Hybrid
	packer    *Packer
	feedback  Feedback
	params    Params
	pins      *FingerprintStore

	mu    sync.RWMutex
	ready bool
}

// SetPinStore enables anti-rug-pull definition checking on Refresh (v3).
func (r *Router) SetPinStore(f *FingerprintStore) { r.pins = f }

func New(source ToolSource, retriever *rag.Retriever, p Params, fb Feedback) *Router {
	if p.TopK <= 0 {
		p.TopK = 5
	}
	if p.TokenBudget <= 0 {
		p.TokenBudget = 1500
	}
	if p.CandidateCap <= 0 {
		p.CandidateCap = 50
	}
	if fb == nil {
		fb = NoopFeedback{}
	}
	return &Router{
		source: source, retriever: retriever, packer: NewPacker(),
		feedback: fb, params: p,
		hybrid: NewHybrid(retriever, p.DenseWeight, p.SparseWeight, p.CapWeight),
	}
}

// Refresh pulls tools from the federated source and rebuilds the dense index
// and BM25 corpus. Call after upstream tool sets change (or periodically).
func (r *Router) Refresh(ctx context.Context) error {
	tools, err := r.source.Tools(ctx)
	if err != nil {
		return err
	}
	if r.pins != nil {
		tools = r.pins.Check(tools)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retriever.Reset()
	if err := r.retriever.IndexTools(ctx, tools); err != nil {
		return err
	}
	r.hybrid.Reindex()
	r.ready = true
	return nil
}

func (r *Router) ensureReady(ctx context.Context) error {
	r.mu.RLock()
	ready := r.ready
	r.mu.RUnlock()
	if ready {
		return nil
	}
	return r.Refresh(ctx)
}

// Select runs the full pipeline and returns the value-optimal tool subset for
// the query. budgetOverride<=0 uses the configured TokenBudget.
func (r *Router) Select(ctx context.Context, query string, budgetOverride int) (*Selection, error) {
	if err := r.ensureReady(ctx); err != nil {
		return nil, err
	}
	scored, err := r.hybrid.Score(ctx, query)
	if err != nil {
		return nil, err
	}
	// threshold filter
	if r.params.SimThreshold > 0 {
		filtered := scored[:0]
		for _, s := range scored {
			if s.Score >= r.params.SimThreshold {
				filtered = append(filtered, s)
			}
		}
		scored = filtered
	}
	// learned feedback + dedup
	scored = r.feedback.Adjust(scored)
	scored = Dedup(scored)

	// bound candidates to keep the knapsack well-conditioned
	if len(scored) > r.params.CandidateCap {
		scored = scored[:r.params.CandidateCap]
	}

	allTools := r.retriever.AllTools()
	full := SumTokens(allTools)

	budget := budgetOverride
	if budget <= 0 {
		budget = r.params.TokenBudget
	}
	packed := r.packer.Pack(scored, budget)
	// honor TopK count cap
	if len(packed) > r.params.TopK {
		packed = packed[:r.params.TopK]
	}
	packedTokens := sumScoredTokens(packed)

	saved := full - packedTokens
	var pct float64
	if full > 0 {
		pct = float64(saved) / float64(full) * 100
	}
	return &Selection{
		Query: query, Candidates: len(allTools), Selected: packed,
		FullTokens: full, PackedTokens: packedTokens,
		TokenSaved: saved, TokenSavedPct: pct, Hybrid: true,
	}, nil
}

func sumScoredTokens(s []types.ScoredTool) int {
	var sum int
	for i := range s {
		sum += EstimateTokens(s[i].Tool)
	}
	return sum
}
