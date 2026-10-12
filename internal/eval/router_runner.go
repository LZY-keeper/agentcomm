package eval

// router_runner.go 在金标集上评测支柱一（联邦工具路由）的选择质量：
// recall@k / precision@k / MRR / hit@1 / token 节省。
//
// 与生产的差异仅两处（均为确定性要求）：NoopFeedback（禁 bandit 状态）、
// 写死 mock embedding（不读环境变量）。参数与生产默认一致。

import (
	"context"
	"fmt"
	"sort"

	"agentcomm/internal/embedding"
	"agentcomm/internal/rag"
	"agentcomm/internal/router"
	"agentcomm/internal/types"
)

// RouterRunner 评测配置。零值不可用，用 NewRouterRunner。
type RouterRunner struct {
	tools  []types.Tool
	params router.Params
	router *router.Router
}

// NewRouterRunner 构建路由评测器：默认联邦 = router.DemoTools()（5 server /
// 12 工具），参数 = 生产默认。Refresh 完成索引构建。
func NewRouterRunner(ctx context.Context, params router.Params) (*RouterRunner, error) {
	emb := embedding.NewMock(1024) // 写死：确定性优先，不读 env
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), nil, params.TopK, params.SimThreshold)
	src := router.NewStaticSource(router.DemoTools()...)
	r := router.New(src, retr, params, router.NoopFeedback{}) // NoopFeedback：无状态
	if err := r.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("评测路由器索引构建: %w", err)
	}
	return &RouterRunner{tools: router.DemoTools(), params: params, router: r}, nil
}

// DemoToolIDs 返回金标校验用的合法工具 ID 集（"ServerID/Name"）。
func (r *RouterRunner) DemoToolIDs() map[string]bool {
	ids := map[string]bool{}
	for _, t := range r.tools {
		ids[t.ServerID+"/"+t.Name] = true
	}
	return ids
}

// RunRouter 跑完整个路由金标集，产出 SuiteResult（含 per-case 明细与分类聚合）。
func (r *RouterRunner) Run(ctx context.Context, cases []RouterCase) *SuiteResult {
	SortRouterCases(cases)
	suite := &SuiteResult{CaseCount: len(cases), Metrics: map[string]float64{}, ByCategory: map[string]map[string]float64{}}

	var recalls, precisions, mrrs []float64
	var hitSum float64
	var hitDenom int
	var savedPctSum float64
	catAgg := map[string]*catStat{}

	for _, c := range cases {
		sel, err := r.router.Select(ctx, c.Query, 0)
		if err != nil {
			suite.Failures = append(suite.Failures, CaseFailure{ID: c.ID, Query: c.Query, Reasons: []string{"select 出错: " + err.Error()}})
			recalls = append(recalls, 0)
			precisions = append(precisions, 0)
			mrrs = append(mrrs, 0)
			continue
		}
		selected := canonicalOrder(sel.Selected) // []string，已按 (Score,ToolID) 全序

		rec := RecallAtK(selected, c.RelevantTools, r.params.TopK)
		prec := PrecisionAtK(selected, c.RelevantTools, r.params.TopK)
		mrr := MRR(selected, c.RelevantTools)
		recalls = append(recalls, rec)
		precisions = append(precisions, prec)
		mrrs = append(mrrs, mrr)
		savedPctSum += sel.TokenSavedPct
		if c.ExpectTop1 != "" {
			hitSum += HitAt1(selected, c.ExpectTop1)
			hitDenom++
		}

		// per-case 明细
		caseDetail := map[string]any{
			"id": c.ID, "category": c.Category, "query": c.Query,
			"selected": selected, "recall_at_k": round4(rec), "mrr": round4(mrr),
			"full_tokens": sel.FullTokens, "packed_tokens": sel.PackedTokens, "saved_pct": round4(sel.TokenSavedPct),
		}
		if firstRel := firstRelevantRank(selected, c.RelevantTools); firstRel > 0 {
			caseDetail["first_relevant_rank"] = firstRel
		}
		suite.Cases = append(suite.Cases, caseDetail)

		st := catAgg[c.Category]
		if st == nil {
			st = &catStat{}
			catAgg[c.Category] = st
		}
		st.recalls = append(st.recalls, rec)
		st.mrrs = append(st.mrrs, mrr)

		// 用例级失败判定：recall@k < 1 视为该用例未全召回（进失败列表供报告）
		if rec < 1 {
			reasons := []string{fmt.Sprintf("recall@%d=%.2f（相关 %v，实选 %v）", r.params.TopK, rec, c.RelevantTools, selected)}
			if c.ExpectTop1 != "" && len(selected) > 0 && selected[0] != c.ExpectTop1 {
				reasons = append(reasons, fmt.Sprintf("top1=%s 期望 %s", selected[0], c.ExpectTop1))
			}
			suite.Failures = append(suite.Failures, CaseFailure{ID: c.ID, Query: c.Query, Reasons: reasons})
		}
	}

	suite.Metrics["recall_at_5"] = round4(Mean(recalls))
	suite.Metrics["precision_at_5"] = round4(Mean(precisions))
	suite.Metrics["mrr"] = round4(Mean(mrrs))
	if hitDenom > 0 {
		suite.Metrics["hit_at_1"] = round4(hitSum / float64(hitDenom))
	}
	if len(cases) > 0 {
		suite.Metrics["token_saved_pct_avg"] = round4(savedPctSum / float64(len(cases)))
	}

	for cat, st := range catAgg {
		suite.ByCategory[cat] = map[string]float64{
			"recall_at_5": round4(Mean(st.recalls)),
			"mrr":         round4(Mean(st.mrrs)),
			"case_count":  float64(len(st.recalls)),
		}
	}
	return suite
}

type catStat struct {
	recalls, mrrs []float64
}

// canonicalOrder 按 (Score desc, ToolID asc) 全序重排——rag.AllTools() 的 map
// 迭代序会让并列分的 stable sort 结果在进程间漂移，重排后排名类指标可复现。
func canonicalOrder(scored []types.ScoredTool) []string {
	cp := make([]types.ScoredTool, len(scored))
	copy(cp, scored)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].Score != cp[j].Score {
			return cp[i].Score > cp[j].Score
		}
		return cp[i].Tool.ServerID+"/"+cp[i].Tool.Name < cp[j].Tool.ServerID+"/"+cp[j].Tool.Name
	})
	return toolIDs(cp)
}

func toolIDs(scored []types.ScoredTool) []string {
	ids := make([]string, len(scored))
	for i, s := range scored {
		ids[i] = s.Tool.ServerID + "/" + s.Tool.Name
	}
	return ids
}

func firstRelevantRank(selected, relevant []string) int {
	rel := toSet(relevant)
	for i, id := range selected {
		if _, ok := rel[id]; ok {
			return i + 1
		}
	}
	return 0
}

func round4(x float64) float64 {
	return float64(int64(x*10000+0.5)) / 10000 // 正数四舍五入到 4 位，避免浮点尾差进报告
}
