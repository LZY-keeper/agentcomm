package router

import (
	"strings"

	"agentcomm/internal/types"
)

// Dedup resolves conflicts when several federated servers expose equivalent
// tools. Two tools are treated as duplicates if their (lowercased) names match;
// among duplicates the one with the best fused score wins (a proxy for the
// highest-quality origin). Order is preserved on first occurrence.
func Dedup(scored []types.ScoredTool) []types.ScoredTool {
	if len(scored) <= 1 {
		return scored
	}
	best := map[string]int{} // key -> index of current best
	order := []string{}
	for i, s := range scored {
		key := strings.ToLower(s.Tool.Name)
		idx, ok := best[key]
		if !ok {
			best[key] = i
			order = append(order, key)
			continue
		}
		// prefer higher fused score, then higher SuccessRate
		if better(scored[i], scored[idx]) {
			best[key] = i
		}
	}
	out := make([]types.ScoredTool, 0, len(order))
	for _, k := range order {
		out = append(out, scored[best[k]])
	}
	return out
}

func better(a, b types.ScoredTool) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Tool.SuccessRate >= b.Tool.SuccessRate
}
