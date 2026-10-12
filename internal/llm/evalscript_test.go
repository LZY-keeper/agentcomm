package llm

import (
	"context"
	"strings"
	"testing"

	"agentcomm/internal/types"
)

func collectScripted(t *testing.T, ch <-chan types.Token) (text string, calls []types.ToolCall) {
	t.Helper()
	var sb strings.Builder
	for tok := range ch {
		sb.WriteString(tok.Text)
		calls = append(calls, tok.ToolCalls...)
	}
	return sb.String(), calls
}

// 脚本推进：两步工具调用按序发出，回喂后终答模板正确渲染 {result}/{query}。
func TestScripted_TwoStepScript(t *testing.T) {
	script := Script{
		Calls: []ScriptCall{
			{Tool: "get_weather", Args: map[string]any{"city": "北京"}},
			{Tool: "search", Args: map[string]any{"q": "北京 天气"}},
		},
		Final: "查询「{query}」的结果：{result}",
	}
	p := NewScripted("今天北京天气怎么样", script)

	// 第 1 轮：无 history → 发第 1 步
	_, calls := collectScripted(t, mustChat(t, p, types.ChatRequest{Prompt: "今天北京天气怎么样"}))
	if len(calls) != 1 || calls[0].ToolName != "get_weather" {
		t.Fatalf("第 1 步 = %+v", calls)
	}
	// 第 2 轮：1 条 RoleTool → 发第 2 步
	h1 := []types.Message{{Role: types.RoleTool, Content: "晴 25 度", ToolCallID: "get_weather"}}
	_, calls = collectScripted(t, mustChat(t, p, types.ChatRequest{Prompt: "今天北京天气怎么样", History: h1}))
	if len(calls) != 1 || calls[0].ToolName != "search" {
		t.Fatalf("第 2 步 = %+v", calls)
	}
	// 第 3 轮：2 条 RoleTool → 终答，{result}=最后一条工具输出
	h2 := append(h1, types.Message{Role: types.RoleTool, Content: "搜索摘要", ToolCallID: "search"})
	text, calls := collectScripted(t, mustChat(t, p, types.ChatRequest{Prompt: "今天北京天气怎么样", History: h2}))
	if len(calls) != 0 {
		t.Fatalf("终答轮不应再发工具调用: %+v", calls)
	}
	if text != "查询「今天北京天气怎么样」的结果：搜索摘要" {
		t.Errorf("终答模板渲染 = %q", text)
	}
}

// 未登记查询走 Mock 兜底（回显路径）。
func TestScripted_UnregisteredFallsBackToMock(t *testing.T) {
	p := NewScripted("登记的查询", Script{Calls: []ScriptCall{{Tool: "x"}}, Final: "ok"})
	text, _ := collectScripted(t, mustChat(t, p, types.ChatRequest{Prompt: "另一个问题"}))
	if !strings.Contains(text, "离线 mock 模型") {
		t.Errorf("未登记查询应走 Mock 兜底, got %q", text)
	}
}

func mustChat(t *testing.T, p Provider, req types.ChatRequest) <-chan types.Token {
	t.Helper()
	ch, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}
