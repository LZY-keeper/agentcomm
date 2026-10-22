package llm

// evalscript.go 提供评测专用的 Scripted provider：按查询映射到一段脚本化
// ReAct 轨迹（按序发 N 次工具调用，全部结果回喂后用 Final 模板终答）。
//
// 与 Mock 的分工：Mock 只会调 calc 类工具（数学演示路径）；Scripted 把轨迹
// 写进金标数据集（YAML），使天气/代码/审批等非数学 E2E 用例同样确定性可复现。
// 它是评测基础设施，生产装配（cmd/gateway）不使用。
//
// 进度推进：RunAgentLoop 每轮回喂的 History 中 RoleTool 消息数 = 已完成的
// 脚本步数。据此无状态推进（不在 provider 内记数，天然免疫并发与重试）。

import (
	"context"
	"strings"

	"github.com/LZY-keeper/agentcomm/internal/types"
)

// Script 是一段脚本化 ReAct 轨迹：按序发起工具调用，结果回喂后用 Final
// 模板产出终答（{result}=最后工具输出，{query}=原查询）。
type Script struct {
	Calls []ScriptCall
	Final string
}

// ScriptCall 是脚本中的一步工具调用。
type ScriptCall struct {
	Tool string
	Args map[string]any
}

// Scripted 按「精确查询」分发脚本；未登记的查询走 Mock 兜底（登记表由
// 评测 runner 逐例构建，正常不会未命中）。
type Scripted struct {
	mock    *Mock
	scripts map[string]Script
}

// NewScripted 注册唯一的（查询 → 脚本）映射；评测 runner 为每条用例构建
// 新实例，天然无跨例状态。
func NewScripted(query string, script Script) *Scripted {
	return &Scripted{
		mock:    NewMock("mock-small"),
		scripts: map[string]Script{query: script},
	}
}

func (s *Scripted) Name() string { return "scripted" }

func (s *Scripted) Chat(ctx context.Context, req types.ChatRequest) (<-chan types.Token, error) {
	out := make(chan types.Token, 16)
	go func() {
		defer close(out)

		script, ok := s.scripts[strings.TrimSpace(req.Prompt)]
		if !ok {
			// 未登记查询：退回 Mock 行为。
			ch, err := s.mock.Chat(ctx, req)
			if err != nil {
				out <- types.Token{Err: err.Error(), Finish: "stop"}
				return
			}
			for tok := range ch {
				out <- tok
			}
			return
		}

		done := countToolResults(req.History)
		if done < len(script.Calls) {
			// 还有脚本步未发：发出第 done 步工具调用。
			c := script.Calls[done]
			out <- types.Token{
				ToolCalls: []types.ToolCall{{ToolName: c.Tool, Args: c.Args}},
				Finish:    "tool_calls",
			}
			return
		}
		// 全部完成：用最后一条工具结果渲染终答模板。
		last := lastMessage(req.History)
		result := ""
		if last != nil {
			result = last.Content
		}
		text := strings.ReplaceAll(script.Final, "{result}", result)
		text = strings.ReplaceAll(text, "{query}", strings.TrimSpace(req.Prompt))
		streamText(ctx, out, text)
	}()
	return out, nil
}

func countToolResults(h []types.Message) int {
	n := 0
	for _, m := range h {
		if m.Role == types.RoleTool {
			n++
		}
	}
	return n
}
