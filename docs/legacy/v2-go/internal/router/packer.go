package router

import (
	"agentcomm/internal/rag"
	"agentcomm/internal/types"
)

// EstimateTokens approximates how many prompt tokens a tool's schema costs when
// injected into the LLM context (name + description + JSON schema). ~4
// chars/token plus a fixed structural overhead. It is deliberately conservative
// and stable so the "tokens saved" metric is meaningful across runs.
func EstimateTokens(t types.Tool) int {
	fp := rag.Fingerprint(t)
	return len(fp)/4 + 12
}

// Packer solves a 0/1 knapsack: maximize total fused score (value) of injected
// tools subject to a token budget. This is the principled fix for tool-sprawl
// token explosion — neither naive top-k (ignores tool size) nor naive budget
// truncation (ignores value) is optimal; the knapsack is. O(N·budget).
type Packer struct{}

func NewPacker() *Packer { return &Packer{} }

// Pack returns the value-maximal subset whose total token cost <= budget. If
// even the best tool alone exceeds the budget, that single tool is returned so
// the agent always exposes its best capability (graceful degradation).
func (p *Packer) Pack(items []types.ScoredTool, budget int) []types.ScoredTool {
	if len(items) == 0 || budget <= 0 {
		return nil
	}
	n := len(items)
	costs := make([]int, n)
	for i, it := range items {
		c := EstimateTokens(it.Tool)
		if c < 1 {
			c = 1
		}
		costs[i] = c
	}
	dp := make([]float64, budget+1)
	keep := make([][]bool, n)
	for i := range keep {
		keep[i] = make([]bool, budget+1)
	}
	for i := 0; i < n; i++ {
		c := costs[i]
		for w := budget; w >= 0; w-- {
			if w >= c {
				val := items[i].Score
				if dp[w-c]+val > dp[w] {
					dp[w] = dp[w-c] + val
					keep[i][w] = true
				}
			}
		}
	}
	// reconstruct chosen items
	chosen := make([]int, 0, n)
	w := budget
	for i := n - 1; i >= 0; i-- {
		if w >= 0 && w <= budget && keep[i][w] {
			chosen = append(chosen, i)
			w -= costs[i]
		}
	}
	out := make([]types.ScoredTool, 0, len(chosen))
	for i := len(chosen) - 1; i >= 0; i-- {
		out = append(out, items[chosen[i]])
	}
	if len(out) == 0 {
		best := 0
		for i := range items {
			if items[i].Score > items[best].Score {
				best = i
			}
		}
		out = append(out, items[best])
	}
	return out
}

// SumTokens sums the token cost of a tool set (used for full vs packed accounting).
func SumTokens(tools []types.Tool) int {
	var sum int
	for i := range tools {
		sum += EstimateTokens(tools[i])
	}
	return sum
}
