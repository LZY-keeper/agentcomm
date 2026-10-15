package router

import (
	"sync"

	"agentcomm/internal/types"
)

// Feedback learns the empirical success rate of each selected tool and nudges
// future selection scores accordingly — a multi-armed-bandit-style feedback
// loop that improves routing over time. The default BetaFeedback keeps
// Beta(α,β) counts per tool id and blends the posterior mean into the fused
// score (deterministic; a Thompson-sampling variant is a drop-in alternative).
type Feedback interface {
	Adjust(scored []types.ScoredTool) []types.ScoredTool
	Record(toolID string, success bool)
}

// NoopFeedback is the identity feedback (used until a persistence store is wired).
type NoopFeedback struct{}

func (NoopFeedback) Adjust(s []types.ScoredTool) []types.ScoredTool { return s }
func (NoopFeedback) Record(string, bool)                            {}

// BetaFeedback blends learned per-tool success into fused scores.
type BetaFeedback struct {
	mu     sync.Mutex
	alpha  map[string]float64 // successes (prior 1)
	beta   map[string]float64 // failures (prior 1)
	weight float64            // how much learned mean influences score
}

func NewBetaFeedback(weight float64) *BetaFeedback {
	if weight <= 0 {
		weight = 0.15
	}
	return &BetaFeedback{alpha: map[string]float64{}, beta: map[string]float64{}, weight: weight}
}

func (b *BetaFeedback) Record(toolID string, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.alpha[toolID]++
	} else {
		b.beta[toolID]++
	}
}

// Adjust adds a small bonus proportional to each tool's learned success mean,
// then re-sorts. Tools with no history get a neutral 0.5 (mild exploration).
func (b *BetaFeedback) Adjust(scored []types.ScoredTool) []types.ScoredTool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range scored {
		id := toolKey(scored[i].Tool)
		a := b.alpha[id] + 1
		bb := b.beta[id] + 1
		mean := a / (a + bb)                       // Beta posterior mean
		scored[i].Score += b.weight * (mean - 0.5) // center at neutral
	}
	insertionSortDesc(scored)
	return scored
}

// toolKey is the feedback map key. The orchestrator records outcomes by tool
// name (it only has the ToolCall.ToolName at invocation time), so we key by
// Name here too — selected tool sets are name-deduped, so Name is unique within
// a selection. (A ServerID/Name composite would never match the recorded key.)
func toolKey(t types.Tool) string { return t.Name }

func insertionSortDesc(s []types.ScoredTool) {
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j].Score > s[j-1].Score {
			s[j], s[j-1] = s[j-1], s[j]
			j--
		}
	}
}
