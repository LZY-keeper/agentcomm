// Command bench-router is the offline pillar-1 evaluation: it federates a
// representative tool set, runs the router over sample queries, and prints the
// token-savings table (full naive injection vs budget-packed). This is the
// quantified "tool-selection token 占用↓70%+" evidence for the resume.
//
// Runs offline (mock embedding) by default. Set EMBEDDING_PROVIDER=dashscope
// + DASHSCOPE_API_KEY to evaluate with real embeddings.
package main

import (
	"context"
	"fmt"
	"os"

	"agentcomm/internal/config"
	"agentcomm/internal/embedding"
	"agentcomm/internal/rag"
	"agentcomm/internal/router"
)

func main() {
	cfg := config.Load()
	emb, err := embedding.New(cfg.Embedding)
	must(err)
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), rag.NewCache(cfg.Router.CacheMaxEntries, 0), cfg.Router.TopK, cfg.Router.SimThreshold)
	src := router.NewStaticSource(router.DemoTools()...)
	p := router.Params{
		TopK: cfg.Router.TopK, TokenBudget: cfg.Router.TokenBudget, CandidateCap: 50,
		DenseWeight: cfg.Router.DenseWeight, SparseWeight: cfg.Router.SparseWeight, CapWeight: cfg.Router.CapabilityWeight,
	}
	r := router.New(src, retr, p, router.NewBetaFeedback(0.1))
	must(r.Refresh(context.Background()))

	queries := []string{
		"计算 123 + 456 等于多少",
		"2 的 10 次方",
		"今天北京天气怎么样",
		"帮我审查这段 Python 代码",
		"读取 /etc/hosts 文件内容",
		"搜索 Go 语言的最新特性",
	}
	fmt.Printf("embed provider=%s dim=%d  tools=%d  budget=%d topk=%d\n",
		emb.Name(), emb.Dim(), len(router.DemoTools()), cfg.Router.TokenBudget, cfg.Router.TopK)
	fmt.Printf("%-30s %8s %8s %8s %8s   %s\n", "query", "tools", "full", "packed", "saved%", "top tool")
	fmt.Println(repeat("-", 86))
	var sumPct float64
	for _, q := range queries {
		sel, err := r.Select(context.Background(), q, 0)
		must(err)
		top := "-"
		if len(sel.Selected) > 0 {
			top = sel.Selected[0].Tool.ServerID + "/" + sel.Selected[0].Tool.Name
		}
		sumPct += sel.TokenSavedPct
		fmt.Printf("%-30s %8d %8d %8d %7.1f%%   %s\n",
			trunc(q, 30), len(sel.Selected), sel.FullTokens, sel.PackedTokens, sel.TokenSavedPct, top)
	}
	fmt.Printf("\navg token saved: %.1f%%\n", sumPct/float64(len(queries)))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
