package rag

import (
	"context"
	"encoding/json"
	"fmt"

	"agentcomm/internal/embedding"
	"agentcomm/internal/types"
)

// Retriever is the dense-retrieval core: it indexes tools by an embedding of
// their textual fingerprint and retrieves the top-k most similar to a query.
// Pillar 1 (internal/router) composes this with BM25 + capability-graph +
// token-budget packing; keeping the dense core here keeps both packages small.
type Retriever struct {
	embed     embedding.Provider
	idx       VectorIndex
	cache     *Cache
	tools     map[string]types.Tool // id -> tool (id = serverID/name)
	topK      int
	threshold float64
}

// NewRetriever builds a dense retriever. cache may be nil (caching disabled).
func NewRetriever(embed embedding.Provider, idx VectorIndex, cache *Cache, topK int, threshold float64) *Retriever {
	if topK <= 0 {
		topK = 5
	}
	return &Retriever{embed: embed, idx: idx, cache: cache, tools: map[string]types.Tool{}, topK: topK, threshold: threshold}
}

// ToolID is the stable federated key for a tool.
func ToolID(t types.Tool) string { return t.ServerID + "/" + t.Name }

// IndexTools embeds each tool's fingerprint and adds it to the index.
func (r *Retriever) IndexTools(ctx context.Context, tools []types.Tool) error {
	if len(tools) == 0 {
		return nil
	}
	texts := make([]string, len(tools))
	for i, t := range tools {
		texts[i] = Fingerprint(t)
	}
	vecs, err := r.embedBatch(ctx, texts)
	if err != nil {
		return err
	}
	for i, t := range tools {
		t.Embedding = vecs[i]
		id := ToolID(t)
		r.tools[id] = t
		r.idx.Add(id, vecs[i])
	}
	return nil
}

// Retrieve returns the top-k tools whose dense similarity to the query meets
// the threshold. Scores fill ScoredTool.Dense and .Score.
func (r *Retriever) Retrieve(ctx context.Context, query string) ([]types.ScoredTool, error) {
	if r.idx.Size() == 0 {
		return nil, nil
	}
	qv, err := r.embedOne(ctx, query)
	if err != nil {
		return nil, err
	}
	hits := r.idx.Search(qv, r.topK)
	out := make([]types.ScoredTool, 0, len(hits))
	for _, h := range hits {
		if r.threshold > 0 && h.Score < r.threshold {
			continue
		}
		t, ok := r.tools[h.ID]
		if !ok {
			continue
		}
		out = append(out, types.ScoredTool{Tool: t, Dense: h.Score, Score: h.Score})
	}
	return out, nil
}

// Reset clears all indexed tools and vectors (used by the router on Refresh).
func (r *Retriever) Reset() {
	r.tools = map[string]types.Tool{}
	r.idx.Clear()
}

// AllTools returns every indexed tool (used by the router for full/baseline
// token accounting and BM25 corpus construction).
func (r *Retriever) AllTools() []types.Tool {
	out := make([]types.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Fingerprint is the text representation of a tool used for embedding and BM25.
func Fingerprint(t types.Tool) string {
	b, _ := json.Marshal(t.InputSchema)
	return fmt.Sprintf("%s %s %s inputs=%v outputs=%v", t.Name, t.Description, string(b), t.InputTypes, t.OutputTypes)
}

func (r *Retriever) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	// cache-aware: serve hits from cache, embed only misses in one batch.
	res := make([][]float32, len(texts))
	type pending struct {
		orig int
		text string
	}
	var miss []pending
	for i, t := range texts {
		if r.cache != nil {
			if v, ok := r.cache.Get(t); ok {
				res[i] = v
				continue
			}
		}
		miss = append(miss, pending{orig: i, text: t})
	}
	if len(miss) > 0 {
		only := make([]string, len(miss))
		for i, p := range miss {
			only[i] = p.text
		}
		vecs, err := r.embed.Embed(ctx, only)
		if err != nil {
			return nil, err
		}
		for i, p := range miss {
			res[p.orig] = vecs[i]
			if r.cache != nil {
				r.cache.Put(p.text, vecs[i])
			}
		}
	}
	return res, nil
}

func (r *Retriever) embedOne(ctx context.Context, text string) ([]float32, error) {
	if r.cache != nil {
		if v, ok := r.cache.Get(text); ok {
			return v, nil
		}
	}
	vecs, err := r.embed.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if r.cache != nil {
		r.cache.Put(text, vecs[0])
	}
	return vecs[0], nil
}
