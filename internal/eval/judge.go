package eval

// judge.go 定义答案评判器：RuleJudge（默认，确定性包含匹配）与 LLMJudge
//（适配器，-judge llm 时启用；其分数不与规则基线互比，禁入 CI 门禁）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/LZY-keeper/agentcomm/internal/llm"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

// Judge 评判 E2E 终答是否可接受。
type Judge interface {
	Name() string
	ScoreAnswer(ctx context.Context, c E2ECase, answer string) (JudgeScore, error)
}

// JudgeScore 是评判结果。
type JudgeScore struct {
	Pass      bool
	Score     float64
	Rationale string
}

// RuleJudge 按 expect_answer_contains 全命中打分（离线确定性）。
type RuleJudge struct{}

func (j *RuleJudge) Name() string { return "rule" }

func (j *RuleJudge) ScoreAnswer(_ context.Context, c E2ECase, answer string) (JudgeScore, error) {
	if len(c.ExpectAnswerContains) == 0 {
		return JudgeScore{Pass: true, Score: 1, Rationale: "无答案断言，跳过"}, nil
	}
	misses := []string{}
	for _, sub := range c.ExpectAnswerContains {
		if !strings.Contains(answer, sub) {
			misses = append(misses, sub)
		}
	}
	if len(misses) == 0 {
		return JudgeScore{Pass: true, Score: 1, Rationale: "全部关键词命中"}, nil
	}
	return JudgeScore{Pass: false, Score: 0,
		Rationale: fmt.Sprintf("答案缺少关键词 %v", misses)}, nil
}

// LLMJudge 用真实 LLM 评判终答质量（供接 Qwen/OpenAI 后研究用）。
// 提示词要求以 PASS/FAIL 开头，解析失败按 FAIL 处理（保守）。
type LLMJudge struct {
	Provider llm.Provider
}

func (j *LLMJudge) Name() string { return "llm" }

const judgePrompt = `你是评测裁判。给定用户查询与 agent 终答，判断终答是否正确回应了查询。
期望要点：%v
仅输出一行，以 PASS: 或 FAIL: 开头，冒号后给一句理由。`

func (j *LLMJudge) ScoreAnswer(ctx context.Context, c E2ECase, answer string) (JudgeScore, error) {
	req := types.ChatRequest{
		System: "你是严格的 agent 评测裁判。",
		Prompt: fmt.Sprintf(judgePrompt, c.ExpectAnswerContains) + "\n查询：" + c.Query + "\n终答：" + answer,
	}
	ch, err := j.Provider.Chat(ctx, req)
	if err != nil {
		return JudgeScore{}, err
	}
	var sb strings.Builder
	for tok := range ch {
		if tok.Err != "" {
			return JudgeScore{}, fmt.Errorf("judge 流错误: %s", tok.Err)
		}
		sb.WriteString(tok.Text)
	}
	out := strings.TrimSpace(sb.String())
	if strings.HasPrefix(strings.ToUpper(out), "PASS") {
		return JudgeScore{Pass: true, Score: 1, Rationale: out}, nil
	}
	return JudgeScore{Pass: false, Score: 0, Rationale: out}, nil
}
