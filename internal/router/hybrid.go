package router

import (
	"context"
	"sort"

	"agentcomm/internal/rag"
	"agentcomm/internal/types"
)

// Hybrid fuses three orthogonal retrieval signals into one ranking:
//   - dense (semantic, from rag.Retriever): "what does the query mean"
//   - sparse (BM25 keywords): exact identifier/term matches
//   - capability (I/O type graph): "can this tool produce what's needed"
//
// Each signal is min-max normalized to [0,1] then combined with configurable
// weights (default 0.6 / 0.3 / 0.1). Normalization keeps the fusion robust to
// differing score scales — the failure mode of raw weighted sums.
type Hybrid struct {
	retriever *rag.Retriever
	bm25      *BM25
	wDense    float64
	wSparse   float64
	wCap      float64
}

func NewHybrid(retriever *rag.Retriever, wDense, wSparse, wCap float64) *Hybrid {
	if wDense+wSparse+wCap == 0 {
		wDense, wSparse, wCap = 0.6, 0.3, 0.1
	}
	return &Hybrid{retriever: retriever, wDense: wDense, wSparse: wSparse, wCap: wCap}
}

// Reindex rebuilds the BM25 corpus from the retriever's tool set. Call after
// Refresh pulls new tools.
func (h *Hybrid) Reindex() {
	h.bm25 = NewBM25(h.retriever.AllTools())
}

// Score returns every tool with its (normalized) sub-scores and fused score,
// sorted by fused score descending.
func (h *Hybrid) Score(ctx context.Context, query string) ([]types.ScoredTool, error) {
	all := h.retriever.AllTools()
	if len(all) == 0 {
		return nil, nil
	}

	// dense (top-k semantic)
	denseByID := map[string]float64{}
	if dr, err := h.retriever.Retrieve(ctx, query); err == nil {
		for _, s := range dr {
			denseByID[rag.ToolID(s.Tool)] = s.Dense
		}
	}
	// sparse (all docs)
	sparseRaw := h.bm25.ScoreAll(query)

	out := make([]types.ScoredTool, 0, len(all))
	var denseVals, sparseVals []float64
	for _, t := range all {
		id := rag.ToolID(t)
		d := denseByID[id]
		sp := sparseRaw[id]
		denseVals = append(denseVals, d)
		sparseVals = append(sparseVals, sp)
		out = append(out, types.ScoredTool{Tool: t, Dense: d, Sparse: sp})
	}
	dMin, dMax := minMax(denseVals)
	sMin, sMax := minMax(sparseVals)

	for i := range out {
		dn := norm(out[i].Dense, dMin, dMax)
		sn := norm(out[i].Sparse, sMin, sMax)
		cap := capabilityScore(query, out[i].Tool)
		out[i].Dense = dn
		out[i].Sparse = sn
		out[i].Capability = cap
		out[i].Score = h.wDense*dn + h.wSparse*sn + h.wCap*cap
	}
	// (Score desc, ToolID asc) 全序：上游 AllTools() 遍历 map，并列分时
	// stable sort 的输入序不定 → 进程间排名/装箱漂移（评测双跑 digest 暴露）。
	// ToolID 决胜消除之。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return rag.ToolID(out[i].Tool) < rag.ToolID(out[j].Tool)
	})
	return out, nil
}

func minMax(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	mn, mx := v[0], v[0]
	for _, x := range v[1:] {
		if x < mn {
			mn = x
		}
		if x > mx {
			mx = x
		}
	}
	return mn, mx
}

func norm(x, mn, mx float64) float64 {
	if mx <= mn {
		return 0
	}
	return (x - mn) / (mx - mn)
}
