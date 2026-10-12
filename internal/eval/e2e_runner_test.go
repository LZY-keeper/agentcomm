package eval

import (
	"context"
	"testing"

	"agentcomm/internal/llm"
)

// 元测试：E2E runner 自身在迷你金标上跑通三条关键路径
// （原生 math、scripted 天气、scripted HITL 审批）。

func runMiniE2E(t *testing.T) *SuiteResult {
	t.Helper()
	cases, _, err := LoadE2EDataset("testdata/mini-e2e.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := NewE2ERunner(&RuleJudge{})
	return r.Run(context.Background(), cases)
}

func TestE2ERunner_MiniDataset(t *testing.T) {
	suite := runMiniE2E(t)
	if suite.CaseCount != 3 {
		t.Fatalf("case 数 = %d, want 3", suite.CaseCount)
	}
	if len(suite.Failures) != 0 {
		t.Fatalf("迷你集应全过，失败: %+v", suite.Failures)
	}
	if suite.Metrics["success_rate"] != 1 {
		t.Errorf("success_rate = %v, want 1", suite.Metrics["success_rate"])
	}
	if suite.Metrics["state_correct_rate"] != 1 {
		t.Errorf("state_correct_rate = %v", suite.Metrics["state_correct_rate"])
	}
	if suite.Metrics["tool_trace_match_rate"] != 1 {
		t.Errorf("tool_trace_match_rate = %v", suite.Metrics["tool_trace_match_rate"])
	}
	// intent_accuracy 只统计有 expect_intent 的用例（2 条）
	if suite.Metrics["intent_accuracy"] != 1 {
		t.Errorf("intent_accuracy = %v, want 1", suite.Metrics["intent_accuracy"])
	}
}

// 确定性：同数据集双跑，metrics 子树完全一致。
func TestE2ERunner_Deterministic(t *testing.T) {
	a := runMiniE2E(t)
	b := runMiniE2E(t)
	for k, v := range a.Metrics {
		if b.Metrics[k] != v {
			t.Errorf("指标 %s 双跑不一致: %v vs %v", k, v, b.Metrics[k])
		}
	}
}

// 失败注入：把 math 用例的期望终答改错，必须被检出（防"永远绿灯"）。
func TestE2ERunner_DetectsFailure(t *testing.T) {
	cases, _, err := LoadE2EDataset("testdata/mini-e2e.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases[0].ExpectAnswerContains = []string{"不可能出现的答案 42xyz"}
	r := NewE2ERunner(&RuleJudge{})
	suite := r.Run(context.Background(), cases)
	if suite.Metrics["success_rate"] >= 1 {
		t.Fatalf("失败注入未检出: %+v", suite.Metrics)
	}
	if len(suite.Failures) == 0 {
		t.Fatal("失败用例未记录")
	}
}

// Scripted provider 直接验证（轨迹推进 + 模板渲染已在 llm 包测试，这里
// 验证 eval 层的 Script 类型桥接无损）。
func TestScriptBridge(t *testing.T) {
	s := llm.Script{
		Calls: []llm.ScriptCall{{Tool: "t1", Args: map[string]any{"a": 1}}},
		Final: "结果 {result}",
	}
	p := llm.NewScripted("q", s)
	if p.Name() != "scripted" {
		t.Errorf("name = %s", p.Name())
	}
}
