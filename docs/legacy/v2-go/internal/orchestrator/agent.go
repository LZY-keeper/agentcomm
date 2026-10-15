package orchestrator

import (
	"context"

	"agentcomm/internal/llm"
	"agentcomm/internal/types"
)

// Agent is a specialist the orchestrator can route to. The default LLMAgent
// runs the agentic tool loop; remote A2A agents adapt the same interface.
type Agent interface {
	Info() types.AgentInfo
	Handle(ctx context.Context, req types.ChatRequest, tools []types.Tool, invoker ToolInvoker) (<-chan types.Token, error)
}

// LLMAgent is an in-process agent backed by an LLM provider and a system prompt.
type LLMAgent struct {
	info     types.AgentInfo
	provider llm.Provider
	system   string
}

// NewLLMAgent builds an agent with the given skills (used for routing) and an
// optional system prompt that shapes its persona/expertise.
func NewLLMAgent(id, name string, skills []string, provider llm.Provider, system string) *LLMAgent {
	return &LLMAgent{
		info:     types.AgentInfo{ID: id, Name: name, Skills: skills, Tags: skills, Healthy: true, SuccessRate: 1.0},
		provider: provider,
		system:   system,
	}
}

func (a *LLMAgent) Info() types.AgentInfo { return a.info }

func (a *LLMAgent) Handle(ctx context.Context, req types.ChatRequest, tools []types.Tool, invoker ToolInvoker) (<-chan types.Token, error) {
	out := make(chan types.Token, 32)
	go func() {
		defer close(out)
		RunAgentLoop(ctx, a.provider, a.system, req, tools, invoker, out)
	}()
	return out, nil
}
