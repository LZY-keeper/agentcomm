package llm

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/types"
)

// Mock is an offline, deterministic provider. It needs no API key and is the
// default, so the system runs end-to-end for demos. It streams tokens to
// exercise the real streaming plumbing, and can synthesize a tool call when an
// arithmetic expression is detected and a matching tool is available.
type Mock struct {
	model string
}

func NewMock(model string) *Mock {
	if model == "" {
		model = "mock-small"
	}
	return &Mock{model: model}
}

func (m *Mock) Name() string { return "mock" }

var arithRe = regexp.MustCompile(`(?mi)[-+/*^]|(\bplus\b|\bminus\b|\btimes\b|\bdivided by\b)|(平方根|阶乘|开方|乘|除|加|减)|\d+\s*[*+\-/^]\s*\d+`)

func (m *Mock) Chat(ctx context.Context, req types.ChatRequest) (<-chan types.Token, error) {
	out := make(chan types.Token, 16)
	go func() {
		defer close(out)
		// If a tool result has been fed back (agentic loop continuation), produce
		// the final answer from it — keeps the offline demo coherent.
		if last := lastMessage(req.History); last != nil && last.Role == types.RoleTool {
			streamText(ctx, out, "根据工具结果，「"+strings.TrimSpace(req.Prompt)+"」的答案是 "+last.Content+"。")
			return
		}
		// Tool-call path: arithmetic + a calculator tool available.
		if calc := findCalculator(req.Tools); calc != nil && arithRe.MatchString(req.Prompt) {
			expr := extractExpr(req.Prompt)
			out <- types.Token{ToolCalls: []types.ToolCall{{ToolName: calc.Name, Args: map[string]any{"expression": expr}}}, Finish: "tool_calls"}
			return
		}
		// Plain chat path: stream a deterministic answer token-by-token.
		text := mockAnswer(req.Prompt, req.System)
		streamText(ctx, out, text)
	}()
	return out, nil
}

func lastMessage(h []types.Message) *types.Message {
	if len(h) == 0 {
		return nil
	}
	return &h[len(h)-1]
}

// streamText emits text in small rune-chunks at a cadence that simulates a
// real token stream (so the UI shows live typing). Respects context cancel.
func streamText(ctx context.Context, out chan<- types.Token, text string) {
	const chunkSize = 3 // runes per simulated token
	runes := []rune(text)
	for i := 0; i < len(runes); i += chunkSize {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
		j := i + chunkSize
		if j > len(runes) {
			j = len(runes)
		}
		out <- types.Token{Text: string(runes[i:j])}
	}
	out <- types.Token{Finish: "stop"}
}

func mockAnswer(prompt, system string) string {
	p := strings.TrimSpace(prompt)
	if p == "" {
		p = "(empty prompt)"
	}
	return "（离线 mock 模型）我收到了你的问题：" + p + "。在真实模式下会调用 LLM 流式回答；当前为免 key 演示模式，仅回显意图。"
}

func findCalculator(tools []types.Tool) *types.Tool {
	for i := range tools {
		n := strings.ToLower(tools[i].Name)
		if strings.Contains(n, "calc") {
			return &tools[i]
		}
	}
	return nil
}

// extractExpr pulls a likely arithmetic expression out of a prompt for the
// synthesized tool call (demo only; real LLM does this). Strips common
// Chinese/English leading and trailing phrases — the trailing strip was added
// after the E2E eval dataset caught 「计算 123 + 456 等于多少」 feeding the
// trailing 「等于多少」 straight into the calculator (parse error).
func extractExpr(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	for _, pre := range []string{"计算", "算一下", "帮我算", "what is", "compute", "calculate"} {
		s = strings.TrimPrefix(s, pre)
	}
	for _, post := range []string{"等于多少", "等于几", "结果是多少", "是多少", "等于", "= ?"} {
		s = strings.TrimSuffix(strings.TrimSpace(s), post)
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "?"))
}
