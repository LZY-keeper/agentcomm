// Command eval 是评测子系统 CLI：加载金标数据集 → 确定性运行（router 召回
// 与 E2E 任务成功）→ 指标/版本化报告/回归门禁。
//
// 默认 mock LLM + mock embedding 全离线、字节级可复现（StableDigest 验证）。
// -baseline 进入回归门禁模式：与基线报告比对 floor/跌幅，失败退出码 1（CI 用）。
//
// 用法：
//
//	go run ./cmd/eval -suite all                       # 本地评测，写 eval/reports/ 并复制 latest/
//	go run ./cmd/eval -suite all -baseline eval/reports/baseline/report.json   # 门禁模式
//	go run ./cmd/eval -suite all -out eval/reports/baseline                    # 重建基线
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"agentcomm/internal/config"
	"agentcomm/internal/eval"
	"agentcomm/internal/llm"
	"agentcomm/internal/router"
)

var Version = "dev"

func main() {
	suites := flag.String("suite", "all", "评测范围：router | e2e | all")
	datasetDir := flag.String("dataset", "eval/datasets", "金标数据集目录")
	outDir := flag.String("out", "eval/reports", "报告输出根目录（时间戳子目录 + latest/）")
	baseline := flag.String("baseline", "", "基线报告路径（设置即启用回归门禁，失败 exit 1）")
	thresholds := flag.String("thresholds", "eval/thresholds.yaml", "门禁阈值文件")
	format := flag.String("format", "table", "stdout 格式：table | markdown | json")
	judgeKind := flag.String("judge", "mock", "答案评判：mock（规则） | llm（接真实 LLM，禁门禁）")
	wantLatest := flag.Bool("latest", true, "复制报告到 {out}/latest/")
	flag.Parse()

	ctx := context.Background()
	cfg := config.Load() // 仅供 -judge llm 时取真实 provider；评测本体不读 env

	runSuites := map[string]bool{}
	switch *suites {
	case "all":
		runSuites["router"], runSuites["e2e"] = true, true
	case "router", "e2e":
		runSuites[*suites] = true
	default:
		fail("未知 -suite %q", *suites)
	}

	report := &eval.Report{
		SchemaVersion: eval.ReportSchemaVersion,
		Meta: eval.ReportMeta{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Commit:      gitCommit(),
			GoVersion:   runtime.Version(),
			Embedding:   "mock-1024",
			LLM:         "mock",
			Judge:       *judgeKind,
			Datasets:    map[string]string{},
		},
	}

	if runSuites["router"] {
		params := router.Params{TopK: 5, TokenBudget: 1500, CandidateCap: 50, SimThreshold: 0.25,
			DenseWeight: 0.6, SparseWeight: 0.3, CapWeight: 0.1}
		runner, err := eval.NewRouterRunner(ctx, params)
		must(err)
		path := filepath.Join(*datasetDir, "router.v1.yaml")
		cases, meta, err := eval.LoadRouterDataset(path, runner.DemoToolIDs())
		must(err)
		fmt.Printf("[router] 金标 %d 例（%s）\n", meta.CaseCount, path)
		report.Suites["router"] = runner.Run(ctx, cases)
		report.Meta.Datasets["router"] = meta.Version
		report.Meta.Params = map[string]any{
			"top_k": params.TopK, "token_budget": params.TokenBudget,
			"dense_w": params.DenseWeight, "sparse_w": params.SparseWeight, "cap_w": params.CapWeight,
		}
	}

	if runSuites["e2e"] {
		path := filepath.Join(*datasetDir, "e2e.v1.yaml")
		cases, meta, err := eval.LoadE2EDataset(path)
		must(err)
		var judge eval.Judge = &eval.RuleJudge{}
		if *judgeKind == "llm" {
			provider, err := llm.New(cfg.LLM)
			must(err)
			judge = &eval.LLMJudge{Provider: provider}
			report.Meta.LLM = cfg.LLM.Provider + "/" + cfg.LLM.Model
		}
		fmt.Printf("[e2e] 金标 %d 例（%s）judge=%s\n", meta.CaseCount, path, judge.Name())
		runner := eval.NewE2ERunner(judge)
		report.Suites["e2e"] = runner.Run(ctx, cases)
		report.Meta.Datasets["e2e"] = meta.Version
	}

	// 写报告：时间戳目录 + latest/（基线模式写固定目录名 baseline 不带时间戳）
	dir := filepath.Join(*outDir, time.Now().Format("20060102-150405")+"-"+shortCommit())
	if *baseline != "" {
		dir = filepath.Join(*outDir, "gate")
	}
	must(report.WriteJSON(dir, *wantLatest))
	if *format == "markdown" {
		fmt.Print(report.RenderMarkdown())
	} else {
		printTable(report)
	}
	fmt.Printf("digest=%s  report=%s\n", report.StableDigest(), filepath.Join(dir, "report.json"))

	// 门禁
	if *baseline != "" {
		if *judgeKind == "llm" {
			fail("-judge llm 与门禁互斥：LLM 评分不可与规则基线比较")
		}
		base, err := eval.LoadReport(*baseline)
		must(err)
		th, err := eval.LoadThresholds(*thresholds)
		must(err)
		gate := eval.CompareGate(report, base, th)
		fmt.Println("\n=== 评测回归门禁 ===")
		for _, f := range gate.Failures {
			fmt.Printf("✗ %s\n", f)
		}
		for _, i := range gate.Improvements {
			fmt.Printf("↑ %s\n", i)
		}
		if gate.Passed {
			fmt.Println("门禁通过")
			return
		}
		fail("门禁失败")
	}
}

func printTable(r *eval.Report) {
	for suite := range r.Suites {
		s := r.Suites[suite]
		if s == nil {
			continue
		}
		fmt.Printf("\n[%s] %d 例，失败 %d\n", suite, s.CaseCount, len(s.Failures))
		fmt.Printf("  %-24s %s\n", "指标", "值")
		for _, m := range sortedMetricNames(s.Metrics) {
			fmt.Printf("  %-24s %.4f\n", m, s.Metrics[m])
		}
		if len(s.ByCategory) > 0 {
			fmt.Printf("  分类聚合：\n")
			for _, cat := range sortedMetricNames(toFloatMap(s.ByCategory)) {
				vals := s.ByCategory[cat]
				fmt.Printf("    %-12s recall@5=%.4f mrr=%.4f n=%d\n", cat, vals["recall_at_5"], vals["mrr"], int(vals["case_count"]))
			}
		}
		for _, f := range s.Failures {
			fmt.Printf("  ✗ %s %q\n", f.ID, f.Query)
			for _, reason := range f.Reasons {
				fmt.Printf("      %s\n", reason)
			}
		}
	}
}

func toFloatMap(m map[string]map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k := range m {
		out[k] = 0
	}
	return out
}

func sortedMetricNames(m map[string]float64) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	for i := 0; i < len(names); i++ { // 小集合插入排序足够
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

func gitCommit() string {
	b, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return ""
	}
	return string(b)
}

func shortCommit() string {
	c := gitCommit()
	if len(c) > 8 {
		return c[:8]
	}
	if c == "" {
		return "nogit"
	}
	return c
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, "error:", fmt.Sprintf(format, args...))
	os.Exit(1)
}
