package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/llm"
	"github.com/LZY-keeper/agentcomm/internal/logx"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

// ToolInvoker executes a tool call and returns its result. It is supplied by
// the orchestrator (which owns the tools.Registry) so agents stay decoupled
// from the tool backend.
type ToolInvoker func(ctx context.Context, call types.ToolCall) (types.ToolResult, error)

// LoopHooks are optional observation/intervention points in the ReAct loop.
// nil members are skipped. The approval hook uses OnBeforeTool to park the
// loop on RequireApproval tools; the governance layer uses OnAfterTool to
// truncate oversized results before they enter the LLM context.
type LoopHooks struct {
	// OnBeforeTool runs before a tool executes. Returning ok=false refuses the
	// call (reason is fed back to the model as the tool result). An error
	// aborts the loop with a terminal message.
	OnBeforeTool func(ctx context.Context, call types.ToolCall) (ok bool, reason string, err error)
	// OnAfterTool sees the result before it is appended to history and may
	// return a modified (e.g. truncated) version.
	OnAfterTool func(ctx context.Context, call types.ToolCall, res types.ToolResult, err error, dur time.Duration) types.ToolResult
}

// ctxKey for loop hooks attached by Orchestrator.Stream.
type hooksCtxKey struct{}

// WithLoopHooks attaches hooks to ctx so agents created before hook wiring
// (e.g. LLMAgent) still run the governed loop.
func WithLoopHooks(ctx context.Context, h *LoopHooks) context.Context {
	return context.WithValue(ctx, hooksCtxKey{}, h)
}

// LoopHooksFromCtx extracts ctx-attached hooks (nil when absent).
func LoopHooksFromCtx(ctx context.Context) *LoopHooks {
	if h, ok := ctx.Value(hooksCtxKey{}).(*LoopHooks); ok {
		return h
	}
	return nil
}

// LoopOpt configures RunAgentLoop without breaking v2 callers (variadic).
type LoopOpt func(*loopCfg)

type loopCfg struct {
	maxIter   int
	hooks     *LoopHooks
	startHist []types.Message
	pending   *PendingResolution
}

// PendingResolution carries the verdict for the tool call the loop was
// parked on (checkpoint-resume path): it is applied before the first LLM call.
type PendingResolution struct {
	Call    types.ToolCall
	Approve bool
	Reason  string
}

// WithMaxIter bounds the tool-call iterations (default 5).
func WithMaxIter(n int) LoopOpt { return func(c *loopCfg) { c.maxIter = n } }

// WithHooks installs observation/intervention points.
func WithHooks(h *LoopHooks) LoopOpt { return func(c *loopCfg) { c.hooks = h } }

// WithStartHistory overrides req.History (checkpoint resume injects the
// persisted history so the resumed segment sees the full prior context).
func WithStartHistory(h []types.Message) LoopOpt {
	return func(c *loopCfg) { c.startHist = h }
}

// WithPending applies a prior approval verdict before the loop starts.
func WithPending(p PendingResolution) LoopOpt { return func(c *loopCfg) { c.pending = &p } }

// RunAgentLoop is the agentic (ReAct-style) loop: stream LLM tokens; when the
// model requests tool calls, execute them, append results to history, and
// re-prompt — until the model emits a terminal stop. Bounded by maxIter (5 by
// default). Hooks (v3) add a human-approval park point and result truncation.
func RunAgentLoop(ctx context.Context, provider llm.Provider, system string,
	req types.ChatRequest, tools []types.Tool, invoker ToolInvoker, out chan<- types.Token, opts ...LoopOpt) {

	cfg := loopCfg{maxIter: 5}
	for _, o := range opts {
		o(&cfg)
	}
	history := append([]types.Message(nil), req.History...)
	if cfg.startHist != nil {
		history = append([]types.Message(nil), cfg.startHist...)
	}

	// Resume path: resolve the parked call first, then continue the normal loop.
	if cfg.pending != nil {
		history = append(history, types.Message{Role: types.RoleAgent, Content: "",
			ToolCalls: []types.ToolCall{cfg.pending.Call}})
		if cfg.pending.Approve {
			res, err := invoker(ctx, cfg.pending.Call)
			if cfg.hooks != nil && cfg.hooks.OnAfterTool != nil {
				res = cfg.hooks.OnAfterTool(ctx, cfg.pending.Call, res, err, 0)
			}
			history = append(history, toolResultMsg(cfg.pending.Call, res, err))
		} else {
			history = append(history, deniedMsg(cfg.pending.Call, cfg.pending.Reason))
		}
		logx.Info(ctx, "task resumed from checkpoint", "pending_tool", cfg.pending.Call.ToolName,
			"approved", cfg.pending.Approve)
	}

	for iter := 0; iter < cfg.maxIter; iter++ {
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
			// v3: pre-execution hook — the human-approval gate parks here.
			if cfg.hooks != nil && cfg.hooks.OnBeforeTool != nil {
				ok, reason, herr := cfg.hooks.OnBeforeTool(ctx, c)
				if herr != nil {
					logx.Warn(ctx, "before-tool hook failed", "tool", c.ToolName, "err", herr)
					out <- types.Token{Text: "[工具前置检查失败] " + herr.Error(), Finish: "stop"}
					return
				}
				if !ok {
					history = append(history, deniedMsg(c, reason))
					continue
				}
			}
			start := time.Now()
			res, err := invoker(ctx, c)
			if cfg.hooks != nil && cfg.hooks.OnAfterTool != nil {
				res = cfg.hooks.OnAfterTool(ctx, c, res, err, time.Since(start))
			}
			history = append(history, toolResultMsg(c, res, err))
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

// toolResultMsg builds the tool-role history message for an executed call.
func toolResultMsg(call types.ToolCall, res types.ToolResult, err error) types.Message {
	return types.Message{Role: types.RoleTool, Content: resultText(res, err), ToolCallID: call.ToolName}
}

// deniedMsg feeds a rejection back to the model as the tool result so it can
// adjust course instead of looping.
func deniedMsg(call types.ToolCall, reason string) types.Message {
	if reason == "" {
		reason = "未获批准"
	}
	return types.Message{Role: types.RoleTool, ToolCallID: call.ToolName,
		Content: "工具调用被拒绝：" + reason}
}
