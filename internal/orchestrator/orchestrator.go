// Package orchestrator implements pillar 2: cost/reliability-aware multi-agent
// orchestration. It classifies intent, discovers agents, routes with load
// balancing + circuit breaking, injects the token-budget-packed tool set, and
// streams the agent's response through the real agentic tool loop.
package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"agentcomm/internal/llm"
	"agentcomm/internal/logx"
	"agentcomm/internal/obs"
	"agentcomm/internal/router"
	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

// Deps bundles the orchestrator's collaborators (dependency injection).
type Deps struct {
	Router   *router.Router
	Tools    *tools.Registry
	LLM      llm.Provider // default/fallback provider
	LB       *LoadBalancer
	Breaker  *BreakerManager
	Intent   *Classifier
	Feedback router.Feedback
	Metrics  *obs.Metrics
	Stats    *AgentStats // live latency/success feedback into AgentInfo (v3)
}

// Orchestrator routes queries to specialist agents and streams their responses.
type Orchestrator struct {
	router   *router.Router
	tools    *tools.Registry
	llm      llm.Provider
	lb       *LoadBalancer
	breaker  *BreakerManager
	intent   *Classifier
	feedback router.Feedback
	metrics  *obs.Metrics
	stats    *AgentStats
	hooks    *LoopHooks // v3: applied to every RunAgentLoop (approval/truncation)

	mu     sync.RWMutex
	byID   map[string]Agent
	order  []string
}

// SetHooks installs loop hooks applied to every agent loop (v3: the hub
// wires the approval park hook + result truncation here).
func (o *Orchestrator) SetHooks(h *LoopHooks) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hooks = h
}

// deps exposes collaborators to the task layer (Hub/TaskRunner).
func (o *Orchestrator) deps() Deps {
	return Deps{Router: o.router, Tools: o.tools, LLM: o.llm, LB: o.lb, Breaker: o.breaker,
		Intent: o.intent, Feedback: o.feedback, Metrics: o.metrics, Stats: o.stats}
}

// invokerWithHooks returns the task-layer invoker: same feedback/breaker/stats
// accounting as the Stream path.
func (o *Orchestrator) invokerWithHooks(hooks *LoopHooks, reg *tools.Registry) ToolInvoker {
	return func(ctx context.Context, call types.ToolCall) (types.ToolResult, error) {
		agentID := TaskIDFromCtx(ctx)
		if agentID == "" {
			agentID = "task"
		}
		o.lb.ConnStart(agentID)
		defer o.lb.ConnDone(agentID)
		startCall := time.Now()
		res, err := o.tools.Call(ctx, call.ToolName, asMap(call.Args))
		ok := err == nil && !res.IsError
		o.feedback.Record(call.ToolName, ok)
		if o.metrics != nil {
			resLabel := "ok"
			if !ok {
				resLabel = "err"
			}
			o.metrics.ToolCalls.WithLabelValues(call.ToolName, "", resLabel).Inc()
		}
		_ = startCall
		return res, err
	}
}

// New constructs an orchestrator. A default "general" agent is auto-registered
// from the fallback LLM so unknown intents still get an answer.
func New(d Deps) *Orchestrator {
	if d.Intent == nil {
		d.Intent = NewClassifier()
	}
	if d.LB == nil {
		d.LB = NewLoadBalancer(LeastResponseTime)
	}
	if d.Breaker == nil {
		d.Breaker = NewBreakerManager(5, 30)
	}
	if d.Feedback == nil {
		d.Feedback = router.NoopFeedback{}
	}
	o := &Orchestrator{
		router: d.Router, tools: d.Tools, llm: d.LLM, lb: d.LB, breaker: d.Breaker,
		intent: d.Intent, feedback: d.Feedback, metrics: d.Metrics, stats: d.Stats, byID: map[string]Agent{},
	}
	if d.LLM != nil {
		o.RegisterAgent(NewLLMAgent("general", "通用助手", []string{"general"}, d.LLM,
			"你是一个企业 AI 网关的通用助手。无法确定意图时负责直接回答用户。"))
	}
	return o
}

// RegisterAgent adds a specialist agent.
func (o *Orchestrator) RegisterAgent(a Agent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.byID[a.Info().ID]; !ok {
		o.order = append(o.order, a.Info().ID)
	}
	o.byID[a.Info().ID] = a
}

func (o *Orchestrator) agents() []Agent {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]Agent, 0, len(o.order))
	for _, id := range o.order {
		if a, ok := o.byID[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

func (o *Orchestrator) bySkill(skill string) ([]Agent, []types.AgentInfo) {
	var matched []Agent
	all := o.agents()
	for _, a := range all {
		for _, s := range a.Info().Skills {
			if s == skill {
				matched = append(matched, a)
				break
			}
		}
	}
	pool := matched
	if len(pool) == 0 {
		pool = all
	}
	infos := make([]types.AgentInfo, len(pool))
	for i, a := range pool {
		info := a.Info()
		o.stats.Apply(&info) // v3: fold live latency/success into routing view
		infos[i] = info
	}
	return pool, infos
}

// Stream routes a query to the best agent and streams its response. The
// returned RoutingDecision carries the full explanation (intent, agent, hybrid
// retrieval + token-budget packing) for the playground and traces.
func (o *Orchestrator) Stream(ctx context.Context, query string, history []types.Message, sessionKey string) (<-chan types.Token, *types.RoutingDecision, error) {
	start := time.Now()
	intent, score := o.intent.Classify(query)
	_, infos := o.bySkill(intent)
	if len(infos) == 0 {
		return nil, nil, fmt.Errorf("no agents available")
	}
	chosenInfo := o.lb.Select(infos, sessionKey)
	pool, _ := o.bySkill(intent)
	agent := pickAgent(pool, chosenInfo.ID)
	if agent == nil {
		agent = o.agents()[0]
		chosenInfo = agent.Info()
	}

	// circuit breaker guard; on open, fall back to any healthy other agent.
	if !o.breaker.Allow(chosenInfo.ID) {
		logx.Warn(ctx, "breaker open, falling back", "agent", chosenInfo.ID)
		for _, a := range o.agents() {
			if a.Info().ID != chosenInfo.ID && o.breaker.Allow(a.Info().ID) {
				agent = a
				chosenInfo = a.Info()
				break
			}
		}
	}

	// token-budget-aware tool selection (pillar 1).
	sel, err := o.router.Select(ctx, query, 0)
	if err != nil {
		return nil, nil, err
	}
	toolSet := scoredToTools(sel.Selected)

	decision := &types.RoutingDecision{
		Query: query, Intent: intent, IntentScore: score, Agent: chosenInfo,
		CandidateTools: sel.Candidates, SelectedTools: sel.Selected,
		FullTokenSize: sel.FullTokens, PackedTokenSize: sel.PackedTokens,
		TokenSaved: sel.TokenSaved, TokenSavedPct: sel.TokenSavedPct,
		RetrievalHybrid: sel.Hybrid, LatencyMS: float64(time.Since(start).Microseconds()) / 1000.0,
	}

	if o.metrics != nil {
		o.metrics.RouteDecisions.WithLabelValues(chosenInfo.ID, intent).Inc()
		o.metrics.RouterTokens.WithLabelValues("full").Add(float64(sel.FullTokens))
		o.metrics.RouterTokens.WithLabelValues("packed").Add(float64(sel.PackedTokens))
		o.metrics.RouterPackedTools.WithLabelValues().Observe(float64(len(toolSet)))
	}

	// invoker: execute tool + record feedback + breaker + stats + metrics.
	invoker := func(ctx context.Context, call types.ToolCall) (types.ToolResult, error) {
		o.lb.ConnStart(chosenInfo.ID)
		defer o.lb.ConnDone(chosenInfo.ID)
		startCall := time.Now()
		res, err := o.tools.Call(ctx, call.ToolName, asMap(call.Args))
		ok := err == nil && !res.IsError
		o.feedback.Record(call.ToolName, ok)
		o.breaker.Record(chosenInfo.ID, ok)
		if o.stats != nil {
			o.stats.Observe(chosenInfo.ID, time.Since(startCall), ok)
		}
		if o.metrics != nil {
			resLabel := "ok"
			if !ok {
				resLabel = "err"
			}
			o.metrics.ToolCalls.WithLabelValues(call.ToolName, "", resLabel).Inc()
		}
		decision.ToolCalls = append(decision.ToolCalls, call)
		return res, err
	}

	req := types.ChatRequest{Model: "", History: history, Prompt: query, Tools: toolSet}
	if o.hooks != nil {
		ctx = WithLoopHooks(ctx, o.hooks)
	}
	ch, err := agent.Handle(ctx, req, toolSet, invoker)
	if err != nil {
		o.breaker.Record(chosenInfo.ID, false)
		return nil, decision, err
	}
	return ch, decision, nil
}

func pickAgent(pool []Agent, id string) Agent {
	for _, a := range pool {
		if a.Info().ID == id {
			return a
		}
	}
	return nil
}

func scoredToTools(s []types.ScoredTool) []types.Tool {
	out := make([]types.Tool, len(s))
	for i, x := range s {
		out[i] = x.Tool
	}
	return out
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{"input": v}
}
