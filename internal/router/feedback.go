package router

import (
	"sync"

	"github.com/LZY-keeper/agentcomm/internal/types"
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

// NoopFeedback is the identity feedback. Production wires BetaFeedback (with
// an optional CountStore for restart-surviving learning); eval and tests use
// Noop to stay stateless and deterministic — the eval no-state rule is law.
type NoopFeedback struct{}

func (NoopFeedback) Adjust(s []types.ScoredTool) []types.ScoredTool { return s }
func (NoopFeedback) Record(string, bool)                            {}

// BetaFeedback blends learned per-tool success into fused scores.
type BetaFeedback struct {
	mu     sync.Mutex
	alpha  map[string]float64 // successes (prior 1)
	beta   map[string]float64 // failures (prior 1)
	weight float64            // how much learned mean influences score
	store  CountStore         // optional persistence (v3.2: learned routing survives restarts)
	dirty  bool               // counts changed since last successful Flush
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
	b.dirty = true
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

// CountStore persists Beta(α,β) counters so learned routing survives a
// restart ("correct it once, it stays corrected"). Implementations must
// tolerate concurrent Save; errors are advisory — feedback loss degrades to
// re-learning, never to wrong routing.
type CountStore interface {
	SaveCounts(counts map[string][2]float64) error
	LoadCounts() (map[string][2]float64, error)
}

// SetCountStore attaches persistence and loads existing counts. Call before
// the router starts serving traffic.
func (b *BetaFeedback) SetCountStore(cs CountStore) error {
	if cs == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store = cs
	if counts, err := cs.LoadCounts(); err == nil && counts != nil {
		for id, c := range counts {
			b.alpha[id] = c[0]
			b.beta[id] = c[1]
		}
	} else if err != nil {
		return err
	}
	return nil
}

// Flush persists counters when dirty. Called periodically and at shutdown —
// never per-call: the hot path only sets a bool under the lock.
func (b *BetaFeedback) Flush() error {
	b.mu.Lock()
	if !b.dirty || b.store == nil {
		b.mu.Unlock()
		return nil
	}
	counts := make(map[string][2]float64, len(b.alpha))
	for id := range b.alpha {
		counts[id] = [2]float64{b.alpha[id], b.beta[id]}
	}
	b.mu.Unlock()
	if err := b.store.SaveCounts(counts); err != nil {
		return err
	}
	b.mu.Lock()
	b.dirty = false
	b.mu.Unlock()
	return nil
}
