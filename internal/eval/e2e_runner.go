package eval

// e2e_runner.go 在金标集上评测端到端任务链路：intent → 路由 → agent →
// ReAct →（可选 HITL 审批）→ 终态/答案/token 计量。
//
// 每条用例构建全新进程内 fixture（memory store、gate、breaker、LB），
// 零端口、零 redis、零网络。decision 捕获：Hub 的 streamFn 会丢弃
// RoutingDecision，因此用 hub.SetStream 包一层 wrapper 转发给评测器。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentcomm/internal/approval"
	"agentcomm/internal/embedding"
	"agentcomm/internal/llm"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/rag"
	"agentcomm/internal/router"
	"agentcomm/internal/store"
	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

// e2eGuardedTool 是评测专用的审批门工具（仅存在于 fixture，不进生产注册表）。
const e2eGuardedTool = "eval_guarded_echo"

// evalStubTools 是评测离线化的桩工具集：weather 真联网（open-meteo）、
// search/fetch/write 未进 builtin——E2E 确定性要求全部以桩替代（脚本驱动）。
// get_weather/read_file 仍用真实现的能力签名（Input/OutputTypes）以保持路由行为一致。
func registerEvalStubs(reg *tools.Registry) {
	stub := func(name, desc string, output string, result string) {
		reg.Register(name, desc, types.ToolSchema{Type: "object"},
			func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
				return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText, Text: result}}}, nil
			}, tools.WithOutputTypes(output))
	}
	stub("get_weather", "查询城市天气 weather forecast 温度（评测离线桩）", "weather", "晴，25°C")
	stub("search", "搜索 web search 检索（评测离线桩）", "url", "A2A 规范要点摘要（离线桩）")
	stub("fetch_url", "抓取网页 fetch_url html（评测离线桩）", "html", "Example Domain")
	stub("write_file", "写文件 write_file file（评测离线桩）", "file", "written ok")
	stub("read_file", "读取文件 read_file file（评测离线桩）", "file", "localhost key=value # agentcomm")
	stub("code_review", "代码审查 review code（评测离线桩）", "code", "审查完成：无阻塞问题，未发现明显 bug，代码 ok 已 checked")
	stub("format_code", "格式化代码 format_code code（评测离线桩）", "code", "formatted code ok")
	stub("run_tests", "运行测试 run_tests code（评测离线桩）", "code", "all passed")
}

// E2ERunner 端到端评测器。Judge 决定答案维度评分（默认 RuleJudge）。
type E2ERunner struct {
	judge Judge
}

// NewE2ERunner 构建 E2E 评测器。
func NewE2ERunner(j Judge) *E2ERunner { return &E2ERunner{judge: j} }

// Run 跑完整个 E2E 金标集。每例独立 fixture；单例出错记失败不中断整体。
func (r *E2ERunner) Run(ctx context.Context, cases []E2ECase) *SuiteResult {
	SortE2ECases(cases)
	suite := &SuiteResult{CaseCount: len(cases), Metrics: map[string]float64{}, ByCategory: map[string]map[string]float64{}}

	var success, stateOK, answerOK, traceOK, intentOK float64
	var inTok, outTok float64
	cats := map[string]*e2eCatStat{}

	for _, c := range cases {
		st := cats[c.Category]
		if st == nil {
			st = &e2eCatStat{}
			cats[c.Category] = st
		}
		res := r.runCase(ctx, c)
		if res.Pass {
			success++
			st.success++
		}
		if res.StateOK {
			stateOK++
		}
		if res.AnswerOK {
			answerOK++
		}
		if res.TraceOK {
			traceOK++
		}
		if res.IntentOK && res.IntentAsserted {
			intentOK++ // 分母只计已断言 intent 的用例
		}
		st.total++
		inTok += float64(res.InputTokens)
		outTok += float64(res.OutputTokens)

		detail := map[string]any{
			"id": c.ID, "category": c.Category, "query": c.Query,
			"pass": res.Pass, "state": res.FinalState, "want_state": c.ExpectState,
			"answer": res.Answer, "tool_calls": res.ToolCalls,
			"input_tokens": res.InputTokens, "output_tokens": res.OutputTokens,
		}
		if res.Intent != "" {
			detail["intent"] = res.Intent
		}
		if res.AgentID != "" {
			detail["agent"] = res.AgentID
		}
		suite.Cases = append(suite.Cases, detail)
		if !res.Pass {
			suite.Failures = append(suite.Failures, CaseFailure{ID: c.ID, Query: c.Query, Reasons: res.Reasons})
		}
	}

	n := float64(len(cases))
	suite.Metrics["success_rate"] = round4(success / n)
	suite.Metrics["state_correct_rate"] = round4(stateOK / n)
	suite.Metrics["answer_contains_rate"] = round4(answerOK / n)
	suite.Metrics["tool_trace_match_rate"] = round4(traceOK / n)
	if hasIntentAssertion(cases) {
		suite.Metrics["intent_accuracy"] = round4(intentOK / float64(countIntentAssertions(cases)))
	}
	suite.Metrics["avg_input_tokens"] = round4(inTok / n)
	suite.Metrics["avg_output_tokens"] = round4(outTok / n)

	for cat, st := range cats {
		suite.ByCategory[cat] = map[string]float64{
			"success_rate": round4(float64(st.success) / float64(st.total)),
			"case_count":   float64(st.total),
		}
	}
	_ = suite // by_category 仅 success_rate/case_count（E2E 无排名型指标）
	return suite
}

type e2eCatStat struct {
	total, success int
}

type e2eCaseResult struct {
	Pass                            bool
	StateOK, AnswerOK, TraceOK      bool
	IntentOK                        bool
	IntentAsserted                  bool
	FinalState, Answer, AgentID     string
	Intent                          string
	ToolCalls                       []string
	InputTokens, OutputTokens       int64
	Reasons                         []string
}

// runCase 构建 fixture 并跑一条用例（含 HITL approve/deny 驱动）。
func (r *E2ERunner) runCase(ctx context.Context, c E2ECase) e2eCaseResult {
	res := e2eCaseResult{ToolCalls: []string{}, Reasons: []string{}}

	// 1) provider：script ? Scripted : 原生 Mock
	var provider llm.Provider = llm.NewMock("mock-small")
	if c.Script != nil {
		provider = llm.NewScripted(c.Query, *c.Script)
	}

	// 2) 工具注册表：calculator 真 handler（算术结果可断言）+ 评测桩工具
	//（离线确定性；weather 真联网、search/fetch/write 未进 builtin）+ 审批门工具
	reg := tools.NewRegistry()
	tools.RegisterCalculator(reg)
	registerEvalStubs(reg)
	reg.Register(e2eGuardedTool, "需要人工审批的回声工具 (eval)", types.ToolSchema{Type: "object"},
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText, Text: "guarded-result"}}}, nil
		}, tools.WithRequireApproval(), tools.WithOutputTypes("text"))

	// 3) 编排器（与生产同构：路由+意图+LB+breaker，全确定性组件）
	emb := embedding.NewMock(1024)
	retr := rag.NewRetriever(emb, rag.NewBruteForceIndex(), nil, 5, 0.0)
	rt := router.New(reg, retr, router.Params{TopK: 5, TokenBudget: 1500, CandidateCap: 50,
		SimThreshold: 0.0, DenseWeight: 0.6, SparseWeight: 0.3, CapWeight: 0.1}, router.NoopFeedback{})
	if err := rt.Refresh(ctx); err != nil {
		res.Reasons = append(res.Reasons, "路由器索引失败: "+err.Error())
		return res
	}
	orch := orchestrator.New(orchestrator.Deps{Router: rt, Tools: reg, LLM: provider})
	orch.RegisterAgent(orchestrator.NewLLMAgent("math-1", "数学助手", []string{"math"}, provider, "你是数学助手。"))
	orch.RegisterAgent(orchestrator.NewLLMAgent("weather-1", "天气助手", []string{"weather"}, provider, "你是天气助手。"))
	orch.RegisterAgent(orchestrator.NewLLMAgent("code-1", "代码助手", []string{"code"}, provider, "你是代码助手。"))
	orch.RegisterAgent(orchestrator.NewLLMAgent("file-1", "文件助手", []string{"file"}, provider, "你是文件助手。"))

	// 4) Hub + decision 捕获缝
	st := store.NewMemoryTaskStore(time.Hour, 0, store.RealClock{})
	sess := store.NewMemorySessionStore(time.Hour, store.RealClock{})
	gate := approval.NewGate()
	hub := orchestrator.NewHub(orch, st, sess, gate, time.Hour, 20)
	defer hub.Shutdown()
	var decision *types.RoutingDecision
	hub.SetStream(func(ctx context.Context, query string, history []types.Message, sessionKey string) (<-chan types.Token, *types.RoutingDecision, error) {
		ch, dec, err := orch.Stream(ctx, query, history, sessionKey)
		if dec != nil {
			decision = dec
		}
		return ch, dec, err
	})
	orch.SetHooks(hub.ApprovalHooks())

	rec, ch, err := hub.Start(ctx, c.Query, nil, "eval-"+c.ID)
	if err != nil {
		res.Reasons = append(res.Reasons, "任务启动失败: "+err.Error())
		return res
	}

	// 5) HITL：轮询 park → Deliver（生产同构双路）
	if c.Approval != nil {
		if !r.driveApproval(ctx, hub, gate, st, rec.ID, *c.Approval) {
			res.Reasons = append(res.Reasons, "审批驱动失败（未 park 或裁决未送达）")
			return res
		}
	}

	// 6) 收流至终态
	final := r.awaitTerminal(ctx, st, rec.ID, ch, &res)
	res.FinalState = string(final.State)
	res.Answer = final.Answer
	res.InputTokens, res.OutputTokens = final.InputTokens, final.OutputTokens
	if decision != nil {
		res.Intent = decision.Intent
		res.AgentID = decision.Agent.ID
		for _, tc := range decision.ToolCalls {
			res.ToolCalls = append(res.ToolCalls, tc.ToolName)
		}
	}

	// 7) 逐维度判定
	if res.FinalState == c.ExpectState {
		res.StateOK = true
	} else {
		res.Reasons = append(res.Reasons, fmt.Sprintf("终态=%s 期望 %s", res.FinalState, c.ExpectState))
	}

	if len(c.ExpectAnswerContains) > 0 {
		score, err := r.judge.ScoreAnswer(ctx, c, res.Answer)
		if err != nil {
			res.Reasons = append(res.Reasons, "judge 出错: "+err.Error())
		} else {
			res.AnswerOK = score.Pass
			if !score.Pass {
				res.Reasons = append(res.Reasons, "答案: "+score.Rationale)
			}
		}
	} else {
		res.AnswerOK = true
	}

	if len(c.ExpectToolCalls) > 0 {
		if equalStrings(res.ToolCalls, c.ExpectToolCalls) {
			res.TraceOK = true
		} else {
			res.Reasons = append(res.Reasons, fmt.Sprintf("工具轨迹=%v 期望 %v", res.ToolCalls, c.ExpectToolCalls))
		}
	} else {
		res.TraceOK = true
	}

	if c.ExpectIntent != "" {
		res.IntentAsserted = true
		if res.Intent == c.ExpectIntent {
			res.IntentOK = true
		} else {
			res.Reasons = append(res.Reasons, fmt.Sprintf("意图=%s 期望 %s", res.Intent, c.ExpectIntent))
		}
	} else {
		res.IntentOK = true
	}

	if c.ExpectAgent != "" && res.AgentID != c.ExpectAgent {
		res.Reasons = append(res.Reasons, fmt.Sprintf("agent=%s 期望 %s", res.AgentID, c.ExpectAgent))
	}
	if c.Approval != nil && final.Approval != nil && final.Approval.Decision != c.Approval.ExpectDecision {
		res.Reasons = append(res.Reasons, fmt.Sprintf("裁决=%s 期望 %s", final.Approval.Decision, c.Approval.ExpectDecision))
	}

	res.Pass = res.StateOK && res.AnswerOK && res.TraceOK && res.IntentOK &&
		(c.ExpectAgent == "" || res.AgentID == c.ExpectAgent) &&
		(c.Approval == nil || (final.Approval != nil && final.Approval.Decision == c.Approval.ExpectDecision))
	return res
}

// driveApproval 等待 input-required 后按裁决送达（Deliver 快路 → Resume 慢路，
// 与生产 API 和 task_test.go 完全同构）。
func (r *E2ERunner) driveApproval(ctx context.Context, hub *orchestrator.Hub, gate *approval.Gate, st store.TaskStore, taskID string, spec ApprovalSpec) bool {
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, err := st.Get(ctx, taskID)
		if err == nil && rec.State == types.TaskInputRequired && rec.Approval != nil {
			break
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	d := approval.Decision{Approve: spec.Verdict == "approve", By: "eval-runner", Reason: "评测驱动"}
	delivered := false
	for i := 0; i < 50 && !delivered; i++ {
		delivered = gate.Deliver(taskID, d)
		if !delivered {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !delivered {
		return hub.Resume(ctx, taskID, d) == nil
	}
	return true
}

// awaitTerminal 轮询任务至终态（同时消费 token 流，防止 runner 阻塞）。
func (r *E2ERunner) awaitTerminal(ctx context.Context, st store.TaskStore, taskID string, ch <-chan types.Token, res *e2eCaseResult) store.TaskRecord {
	go func() {
		for range ch { // 丢弃流内容（answer 已持久化在 TaskRecord）
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec, err := st.Get(ctx, taskID)
		if err == nil && rec.Terminal() {
			return rec
		}
		if time.Now().After(deadline) {
			res.Reasons = append(res.Reasons, "任务未在 30s 内到达终态")
			return rec
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func hasIntentAssertion(cases []E2ECase) bool {
	for _, c := range cases {
		if c.ExpectIntent != "" {
			return true
		}
	}
	return false
}

func countIntentAssertions(cases []E2ECase) int {
	n := 0
	for _, c := range cases {
		if c.ExpectIntent != "" {
			n++
		}
	}
	return n
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}
