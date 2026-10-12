package a2a

import (
	"context"

	"agentcomm/internal/orchestrator"
	"agentcomm/internal/types"
)

// A2AAgent adapts a remote A2A peer to the orchestrator's local Agent
// interface, so routing treats in-process and remote agents uniformly. It
// streams the peer's message/stream deltas as tokens (real streaming).
//
// Lives here (not in orchestrator) to keep the dependency one-directional:
// a2a -> orchestrator.
type A2AAgent struct {
	info types.AgentInfo
	c    *Client
}

func NewA2AAgent(info types.AgentInfo, c *Client) *A2AAgent {
	info.Healthy = true
	return &A2AAgent{info: info, c: c}
}

func (a *A2AAgent) Info() types.AgentInfo { return a.info }

func (a *A2AAgent) Handle(ctx context.Context, req types.ChatRequest, _ []types.Tool, _ orchestrator.ToolInvoker) (<-chan types.Token, error) {
	out := make(chan types.Token, 32)
	go func() {
		defer close(out)
		_, err := a.c.StreamMessage(ctx, req.Prompt, func(delta string) {
			select {
			case out <- types.Token{Text: delta}:
			case <-ctx.Done():
			}
		})
		if err != nil {
			out <- types.Token{Text: "[远端 agent 错误] " + err.Error(), Finish: "stop"}
			return
		}
		out <- types.Token{Finish: "stop"}
	}()
	return out, nil
}
