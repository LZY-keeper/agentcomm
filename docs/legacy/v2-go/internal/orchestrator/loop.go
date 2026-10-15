package orchestrator

import (
	"context"
	"strings"

	"agentcomm/internal/llm"
	"agentcomm/internal/logx"
	"agentcomm/internal/types"
)

// ToolInvoker executes a tool call and returns its result. It is supplied by
// the orchestrator (which owns the tools.Registry) so agents stay decoupled
// from the tool backend.
type ToolInvoker func(ctx context.Context, call types.ToolCall) (types.ToolResult, error)

// RunAgentLoop is the agentic (ReAct-style) loop: stream LLM tokens; when the
// model requests tool calls, execute them, append results to history, and
// re-prompt — until the model emits a terminal stop. Bounded by maxIter.
//
// This is where "the super agent" actually uses tools: RAG-selected tools are
// injected (token-budget packed) and the model decides which to call.
func RunAgentLoop(ctx context.Context, provider llm.Provider, system string,
	req types.ChatRequest, tools []types.Tool, invoker ToolInvoker, out chan<- types.Token) {

	history := append([]types.Message(nil), req.History...)
	const maxIter = 5

	for iter := 0; iter < maxIter; iter++ {
		chatReq := types.ChatRequest{
			Model: req.Model, System: joinSystem(system, req.System),
			History: history, Prompt: req.Prompt, Tools: tools, MaxTokens: req.MaxTokens,
		}
		stream, err := provider.Chat(ctx, chatReq)
		if err != nil {
			out <- types.Token{Text: "[llm 错误] " + err.Error(), Finish: "stop"}
			return
		}
		var calls []types.ToolCall
		toolCalled := false
		for token := range stream {
			if len(token.ToolCalls) > 0 {
				calls = append(calls, token.ToolCalls...)
				toolCalled = true
				continue // swallow the tool-call marker; results stream next
			}
			select {
			case out <- token:
			case <-ctx.Done():
				return
			}
		}
		if !toolCalled {
			return // a terminal stop token already flowed
		}
		// execute requested tools, feed results back as history
		history = append(history, types.Message{Role: types.RoleAgent, Content: "", ToolCalls: calls})
		for _, c := range calls {
			res, err := invoker(ctx, c)
			history = append(history, types.Message{
				Role: types.RoleTool, Content: resultText(res, err), ToolCallID: c.ToolName,
			})
			logx.Debug(ctx, "tool call", "tool", c.ToolName, "err", err != nil)
		}
	}
	out <- types.Token{Text: "[已达最大工具调用轮次]", Finish: "stop"}
}

func joinSystem(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}

func resultText(res types.ToolResult, err error) string {
	if err != nil {
		return "工具调用失败：" + err.Error()
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if c.Text != "" {
			sb.WriteString(c.Text)
		}
	}
	if sb.Len() == 0 {
		return "(工具无文本输出)"
	}
	return sb.String()
}
