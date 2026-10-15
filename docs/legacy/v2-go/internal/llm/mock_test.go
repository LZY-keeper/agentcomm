package llm

import (
	"context"
	"testing"

	"agentcomm/internal/types"
)

func collect(t *testing.T, ch <-chan types.Token) []types.Token {
	t.Helper()
	var out []types.Token
	for tk := range ch {
		out = append(out, tk)
	}
	return out
}

// TestMock_PlainStream verifies the offline mock streams text and terminates.
func TestMock_PlainStream(t *testing.T) {
	m := NewMock("mock")
	ch, err := m.Chat(context.Background(), types.ChatRequest{Prompt: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	toks := collect(t, ch)
	if len(toks) < 2 {
		t.Fatalf("expected multiple tokens, got %d", len(toks))
	}
	last := toks[len(toks)-1]
	if last.Finish != "stop" {
		t.Fatalf("expected finish=stop, got %q", last.Finish)
	}
}

// TestMock_ToolCall verifies the mock synthesizes a calculator tool call for an
// arithmetic prompt — the offline path that exercises the agentic tool loop.
func TestMock_ToolCall(t *testing.T) {
	m := NewMock("mock")
	tools := []types.Tool{{ServerID: "math", Name: "calculator", Description: "计算"}}
	ch, err := m.Chat(context.Background(), types.ChatRequest{Prompt: "计算 1+2", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	toks := collect(t, ch)
	if len(toks) != 1 {
		t.Fatalf("expected single tool-call token, got %d", len(toks))
	}
	if toks[0].Finish != "tool_calls" || len(toks[0].ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", toks[0])
	}
	if toks[0].ToolCalls[0].ToolName != "calculator" {
		t.Fatalf("expected calculator, got %s", toks[0].ToolCalls[0].ToolName)
	}
	t.Logf("synthesized tool call: %+v", toks[0].ToolCalls[0])
}
