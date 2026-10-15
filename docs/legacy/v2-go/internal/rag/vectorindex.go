// Package rag provides the dense-retrieval building blocks of the MCP tool
// router: an in-memory vector index and an embedding cache. The hybrid
// retrieval + token-budget packing lives in internal/router (pillar 1) and
// composes these primitives.
package rag

import "math"

// VectorIndex is a similarity-search index over named vectors. The default
// BruteForceIndex is O(N·D) — correct for tens-to-hundreds of tools; the
// interface lets a future HNSW/IVF backend drop in without touching callers.
type VectorIndex interface {
	Add(id string, vec []float32)
	Search(query []float32, k int) []Hit
	Size() int
	Clear()
}

// Hit is an id/score pair returned by Search.
type Hit struct {
	ID    string
	Score float64
}

// BruteForceIndex stores vectors in slices and scores by cosine similarity.
type BruteForceIndex struct {
	ids  []string
	vecs [][]float32
}

func NewBruteForceIndex() *BruteForceIndex { return &BruteForceIndex{} }

func (b *BruteForceIndex) Add(id string, vec []float32) {
	b.ids = append(b.ids, id)
	b.vecs = append(b.vecs, vec)
}

func (b *BruteForceIndex) Size() int { return len(b.ids) }

func (b *BruteForceIndex) Clear() {
	b.ids = b.ids[:0]
	b.vecs = b.vecs[:0]
}

func (b *BruteForceIndex) Search(query []float32, k int) []Hit {
	if k <= 0 || len(b.ids) == 0 {
		return nil
	}
	hits := make([]Hit, 0, len(b.ids))
	for i, vec := range b.vecs {
		hits = append(hits, Hit{ID: b.ids[i], Score: cosine(query, vec)})
	}
	// partial selection of top-k by score desc
	topK(hits, k)
	if k > len(hits) {
		k = len(hits)
	}
	return hits[:k]
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// topK performs a partial sort so that the k highest-scoring hits are first,
// in descending order. O(N·k) — fine for the small N here.
func topK(hits []Hit, k int) {
	n := len(hits)
	if k > n {
		k = n
	}
	for i := 0; i < k; i++ {
		max := i
		for j := i + 1; j < n; j++ {
			if hits[j].Score > hits[max].Score {
				max = j
			}
		}
		hits[i], hits[max] = hits[max], hits[i]
	}
}
