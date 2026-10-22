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
	"strings"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/config"
	"github.com/LZY-keeper/agentcomm/internal/eval"
	"github.com/LZY-keeper/agentcomm/internal/llm"
	"github.com/LZY-keeper/agentcomm/internal/router"
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
		Suites: map[string]*eval.SuiteResult{},
	}

	if runSuites["router"] {
		// SimThreshold=0.0：mock 哈希 embedding 的余弦相似度未按真实语义校准，
		// 0.25 阈值会把大多数查询拦成空选择（单测同取 0.0，理由一致）。
		// 阈值拦截属真实 embedding（dashscope）的生产调参范畴，评测测的是排序质量。
		params := router.Params{TopK: 5, TokenBudget: 1500, CandidateCap: 50, SimThreshold: 0.0,
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

	// 写报告：时间戳目录 + latest/。基线重建模式（-out 指向 baseline 目录，
	// make eval-baseline 约定）直接写 <out>/report.json——门禁读固定路径。
	dir := filepath.Join(*outDir, time.Now().Format("20060102-150405")+"-"+shortCommit())
	writeLatest := *wantLatest
	if strings.HasSuffix(filepath.Clean(*outDir), "baseline") {
		dir = *outDir
		writeLatest = false // 基线不覆盖 latest（latest 永远反映最近一次普通运行）
	}
	must(report.WriteJSON(dir, writeLatest))
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
				if sr, ok := vals["success_rate"]; ok { // E2E 分类维度
					fmt.Printf("    %-12s success=%.4f n=%d\n", cat, sr, int(vals["case_count"]))
				} else { // router 分类维度
					fmt.Printf("    %-12s recall@5=%.4f mrr=%.4f n=%d\n", cat, vals["recall_at_5"], vals["mrr"], int(vals["case_count"]))
				}
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

// gitCommit 解析当前 HEAD 提交哈希（.git/HEAD → ref 文件；detached 时 HEAD
// 直接是哈希）。失败返回空（报告相应字段留空，不影响门禁）。
func gitCommit() string {
	head, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(head))
	if !strings.HasPrefix(s, "ref: ") {
		return s
	}
	refPath := filepath.Join(".git", strings.TrimPrefix(s, "ref: "))
	b, err := os.ReadFile(refPath)
	if err != nil {
		// packed-refs 兜底
		ref := strings.TrimPrefix(s, "ref: ")
		packed, err := os.ReadFile(filepath.Join(".git", "packed-refs"))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(packed), "\n") {
			if strings.HasSuffix(strings.TrimSpace(line), " "+ref) {
				return strings.Fields(line)[0]
			}
		}
		return ""
	}
	return strings.TrimSpace(string(b))
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
