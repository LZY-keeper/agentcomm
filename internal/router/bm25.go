package router

import (
	"math"
	"strings"

	"github.com/LZY-keeper/agentcomm/internal/rag"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

// BM25 is a classic sparse ranker over tool fingerprints. It complements dense
// (semantic) retrieval: exact keyword matches (API names, identifiers) score
// strongly here even when embeddings are similar, improving precision.
type BM25 struct {
	ids   []string
	docs  [][]string // tokenized fingerprints
	dl    []int
	avgdl float64
	idf   map[string]float64
	k1, b float64
}

// NewBM25 builds the ranker from the federated tool set.
func NewBM25(tools []types.Tool) *BM25 {
	b := &BM25{idf: map[string]float64{}, k1: 1.5, b: 0.75}
	df := map[string]int{}
	var total int
	for _, t := range tools {
		toks := Tokens(strings.ToLower(rag.Fingerprint(t)))
		b.ids = append(b.ids, rag.ToolID(t))
		b.docs = append(b.docs, toks)
		b.dl = append(b.dl, len(toks))
		total += len(toks)
		seen := map[string]struct{}{}
		for _, tk := range toks {
			if _, ok := seen[tk]; ok {
				continue
			}
			seen[tk] = struct{}{}
			df[tk]++
		}
	}
	N := len(tools)
	if N > 0 {
		b.avgdl = float64(total) / float64(N)
	}
	for t, n := range df {
		b.idf[t] = math.Log(1 + (float64(N-n)+0.5)/(float64(n)+0.5))
	}
	return b
}

// ScoreAll returns BM25 scores for every indexed doc against the query.
func (b *BM25) ScoreAll(query string) map[string]float64 {
	q := Tokens(strings.ToLower(query))
	out := make(map[string]float64, len(b.ids))
	for i, doc := range b.docs {
		tf := map[string]int{}
		for _, t := range doc {
			tf[t]++
		}
		var score float64
		for _, term := range q {
			f := float64(tf[term])
			if f == 0 {
				continue
			}
			idf := b.idf[term]
			denom := f + b.k1*(1-b.b+b.b*float64(b.dl[i])/nonZero(b.avgdl))
			score += idf * (f * (b.k1 + 1)) / denom
		}
		out[b.ids[i]] = score
	}
	return out
}

func nonZero(x float64) float64 {
	if x == 0 {
		return 1
	}
	return x
}
