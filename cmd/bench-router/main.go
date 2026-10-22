// Command bench-router is the offline pillar-1 evaluation: it federates a
// representative tool set, runs the router over sample queries, and prints the
// token-savings table (full naive injection vs budget-packed). This is the
// quantified "tool-selection token 占用↓70%+" evidence for the resume.
//
// v3.1：在 token 节省之上补质量指标（recall@5 / MRR，内联 6 条小金标）——
// 只省 token 不保证选得对；完整评测（100+ 金标 / E2E / 回归门禁）用 `make eval`。
//
// Runs offline (mock embedding) by default. Set EMBEDDING_PROVIDER=dashscope
// + DASHSCOPE_API_KEY to evaluate with real embeddings.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LZY-keeper/agentcomm/internal/config"
	"github.com/LZY-keeper/agentcomm/internal/embedding"
	"github.com/LZY-keeper/agentcomm/internal/eval"
	"github.com/LZY-keeper/agentcomm/internal/rag"
	"github.com/LZY-keeper/agentcomm/internal/router"
)

type benchCase struct {
	query    string
	relevant []string // "ServerID/Name"
}

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

	cases := []benchCase{
		{"计算 123 + 456 等于多少", []string{"math/calculator"}},
		{"2 的 10 次方", []string{"math/calculator"}},
		{"今天北京天气怎么样", []string{"net/get_weather"}},
		{"帮我审查这段 Python 代码", []string{"dev/code_review"}},
		{"读取 /etc/hosts 文件内容", []string{"fs/read_file"}},
		{"搜索 Go 语言的最新特性", []string{"web/search"}},
	}
	fmt.Printf("embed provider=%s dim=%d  tools=%d  budget=%d topk=%d\n",
		emb.Name(), emb.Dim(), len(router.DemoTools()), cfg.Router.TokenBudget, cfg.Router.TopK)
	fmt.Printf("%-30s %8s %8s %8s %8s %8s %8s   %s\n", "query", "tools", "full", "packed", "saved%", "recall@5", "mrr", "top tool")
	fmt.Println(repeat("-", 112))
	var sumPct float64
	var recalls, mrrs []float64
	for _, c := range cases {
		sel, err := r.Select(context.Background(), c.query, 0)
		must(err)
		top := "-"
		if len(sel.Selected) > 0 {
			top = sel.Selected[0].Tool.ServerID + "/" + sel.Selected[0].Tool.Name
		}
		selected := make([]string, 0, len(sel.Selected))
		for _, s := range sel.Selected {
			selected = append(selected, s.Tool.ServerID+"/"+s.Tool.Name)
		}
		sumPct += sel.TokenSavedPct
		recalls = append(recalls, eval.RecallAtK(selected, c.relevant, cfg.Router.TopK))
		mrrs = append(mrrs, eval.MRR(selected, c.relevant))
		fmt.Printf("%-30s %8d %8d %8d %7.1f%% %8.2f %8.2f   %s\n",
			trunc(c.query, 30), len(sel.Selected), sel.FullTokens, sel.PackedTokens, sel.TokenSavedPct,
			recalls[len(recalls)-1], mrrs[len(mrrs)-1], top)
	}
	fmt.Printf("\navg token saved: %.1f%%   recall@5: %.2f   mrr: %.2f\n",
		sumPct/float64(len(cases)), eval.Mean(recalls), eval.Mean(mrrs))
	fmt.Println("完整评测（140 例金标 + E2E 成功率 + CI 回归门禁）: make eval")
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
