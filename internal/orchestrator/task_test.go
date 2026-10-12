package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentcomm/internal/approval"
	"agentcomm/internal/llm"
	"agentcomm/internal/store"
	"agentcomm/internal/tools"
	"agentcomm/internal/types"
)

// scriptedProvider is a deterministic two-phase LLM for HITL tests:
// phase 0 → emits a tool call for guardedTool; phase ≥1 → final answer
// referencing the tool result fed back in history.
type scriptedProvider struct{ llm.Provider }

const guardedTool = "guarded_echo"

func (scriptedProvider) Name() string { return "scripted" }

func (p scriptedProvider) Chat(ctx context.Context, req types.ChatRequest) (<-chan types.Token, error) {
	out := make(chan types.Token, 8)
	go func() {
		defer close(out)
		for _, m := range req.History {
			if m.Role == types.RoleTool && strings.Contains(m.Content, "guarded-result") {
				out <- types.Token{Text: "完成：" + m.Content, Finish: "stop"}
				return
			}
			if m.Role == types.RoleTool && strings.Contains(m.Content, "拒绝") {
				out <- types.Token{Text: "工具被拒绝，就此打住。", Finish: "stop"}
				return
			}
		}
		out <- types.Token{ToolCalls: []types.ToolCall{{ToolName: guardedTool,
			Args: map[string]any{"input": "hi"}}}, Finish: "tool_calls"}
	}()
	return out, nil
}

// newGuardedRegistryInner builds a tools.Registry with one approval-gated tool
// (guarded_echo) that yields "guarded-result" when executed.
func newGuardedRegistryInner() *toolsRegistry {
	reg := tools.NewRegistry()
	reg.Register(guardedTool, "需要审批的回声工具 (test)", types.ToolSchema{Type: "object"},
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText,
				Text: "guarded-result"}}}, nil
		}, tools.WithRequireApproval(), tools.WithOutputTypes("text"))
	return &toolsRegistry{reg}
}

// toolsRegistry lets tests pass a *tools.Registry through Deps while keeping
// the package import graph tidy in test helpers.
type toolsRegistry struct{ *tools.Registry }

func newHITLFixture(t *testing.T) (*Hub, *approval.Gate, store.TaskStore) {
	t.Helper()
	reg := newGuardedRegistryInner()
	prov := scriptedProvider{}
	o := New(Deps{Tools: reg.Registry, LLM: prov})
	hub := newTestHub(t, o)
	return hub, hub.gate, hub.store
}

func newTestHub(t *testing.T, o *Orchestrator) *Hub {
	t.Helper()
	st := store.NewMemoryTaskStore(time.Hour, 0, store.RealClock{})
	sess := store.NewMemorySessionStore(time.Hour, store.RealClock{})
	g := approval.NewGate()
	h := NewHub(o, st, sess, g, time.Hour, 20)
	h.SetStream(func(ctx context.Context, query string, history []types.Message, sessionKey string) (<-chan types.Token, *types.RoutingDecision, error) {
		// bypass real routing: run the scripted provider directly through the loop
		out := make(chan types.Token, 32)
		go func() {
			defer close(out) // RunAgentLoop emits tokens but never closes the channel
			RunAgentLoop(ctx, o.llm, "test", types.ChatRequest{Prompt: query, History: history},
				nil, o.invokerWithHooks(nil, o.tools), out, WithHooks(&LoopHooks{OnBeforeTool: h.approvalHookFor(t)}))
		}()
		return out, &types.RoutingDecision{Agent: types.AgentInfo{ID: "scripted"}}, nil
	})
	return h
}

// approvalHookFor exposes the runner's hook for the stream shim above.
func (h *Hub) approvalHookFor(t *testing.T) func(ctx context.Context, call types.ToolCall) (bool, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return func(ctx context.Context, call types.ToolCall) (bool, string, error) {
		// delegate to the same logic TaskRunner uses via a synthetic runner
		return h.syntheticApprovalHook(ctx, call)
	}
}

// TestHITL_ApprovePath verifies the full in-process park → approve → resume
// flow (superseded by TestHITL_FullCycle which drives the real TaskRunner).
func TestHITL_ApprovePath(t *testing.T) {
	t.Skip("superseded by TestHITL_FullCycle")
}

// TestHITL_FullCycle drives the real Hub.Start → park → Deliver → complete.
func TestHITL_FullCycle(t *testing.T) {
	hub, _, st := newHITLFixture(t)
	defer st.Close()
	rec, ch, err := hub.Start(context.Background(), "触发守护工具", nil, "sess-h")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// consume stream in background; watch for input-required
	stateSeen := make(chan types.TaskState, 4)
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		for tok := range ch {
			if tok.Finish == string(types.ControlInputRequired) {
				stateSeen <- types.TaskInputRequired
			}
			sb.WriteString(tok.Text)
		}
		done <- sb.String()
	}()

	// wait for the park (approval ticket persisted)
	var recNow store.TaskRecord
	deadline := time.Now().Add(5 * time.Second)
	for {
		recNow, err = st.Get(context.Background(), rec.ID)
		if err == nil && recNow.State == types.TaskInputRequired && recNow.Approval != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task never parked; last state=%v err=%v", recNow.State, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if recNow.Approval.Tool != guardedTool {
		t.Fatalf("parked tool=%s want %s", recNow.Approval.Tool, guardedTool)
	}
	if recNow.Checkpoint == nil || recNow.Checkpoint.PendingCall.ToolName != guardedTool {
		t.Fatalf("checkpoint missing pending call: %+v", recNow.Checkpoint)
	}

	// approve — Deliver may race the park window (ticket persisted before the
	// waiter registers); retry briefly, then fall back to the resume path just
	// like the production API does.
	delivered := false
	for i := 0; i < 50 && !delivered; i++ {
		delivered = hub.gate.Deliver(rec.ID, approval.Decision{Approve: true, By: "tester"})
		if !delivered {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !delivered {
		if err := hub.Resume(context.Background(), rec.ID, approval.Decision{Approve: true, By: "tester"}); err != nil {
			t.Fatalf("deliver+resume failed: %v", err)
		}
	}
	answer := <-done
	if !strings.Contains(answer, "guarded-result") {
		t.Fatalf("answer missing tool result: %q", answer)
	}
	final, err := st.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != types.TaskCompleted {
		t.Fatalf("final state=%s want completed (msg=%s)", final.State, final.StatusMsg)
	}
	if final.OutputTokens <= 0 {
		t.Fatalf("output tokens not accounted: %+v", final)
	}
}

// TestHITL_DenyPath verifies a denial feeds the refusal back to the model and
// the task still completes.
func TestHITL_DenyPath(t *testing.T) {
	hub, _, st := newHITLFixture(t)
	defer st.Close()
	rec, ch, err := hub.Start(context.Background(), "触发守护工具", nil, "sess-d")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		for tok := range ch {
			sb.WriteString(tok.Text)
		}
		done <- sb.String()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := st.Get(context.Background(), rec.ID)
		if err == nil && r.State == types.TaskInputRequired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task never parked")
		}
		time.Sleep(20 * time.Millisecond)
	}
	delivered := false
	for i := 0; i < 50 && !delivered; i++ {
		delivered = hub.gate.Deliver(rec.ID, approval.Decision{Approve: false, By: "tester", Reason: "风险过高"})
		if !delivered {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !delivered {
		if err := hub.Resume(context.Background(), rec.ID, approval.Decision{Approve: false, By: "tester", Reason: "风险过高"}); err != nil {
			t.Fatalf("deliver+resume failed: %v", err)
		}
	}
	<-done
	final, _ := st.Get(context.Background(), rec.ID)
	if final.State != types.TaskCompleted {
		t.Fatalf("state=%s want completed", final.State)
	}
	if final.Approval != nil && final.Approval.Decision != "denied" {
		t.Fatalf("ticket decision=%+v", final.Approval)
	}
}

// TestHITL_ResumeAfterRestart simulates a gateway restart while a task is
// parked: a fresh Hub over the same store has no live waiter, so the operator
// verdict goes through Hub.Resume, which replays the checkpoint.
func TestHITL_ResumeAfterRestart(t *testing.T) {
	hub, _, st := newHITLFixture(t)
	rec, _, err := hub.Start(context.Background(), "触发守护工具", nil, "sess-r")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := st.Get(context.Background(), rec.ID)
		if err == nil && r.State == types.TaskInputRequired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task never parked")
		}
		time.Sleep(20 * time.Millisecond)
	}
	hub.Shutdown() // "restart": kill live runners

	// new Hub over the same store
	prov := scriptedProvider{}
	o2 := New(Deps{Tools: newGuardedRegistryInner().Registry, LLM: prov})
	hub2 := NewHub(o2, st, nil, approval.NewGate(), time.Hour, 20)
	hub2.SetStream(func(ctx context.Context, query string, history []types.Message, sessionKey string) (<-chan types.Token, *types.RoutingDecision, error) {
		out := make(chan types.Token, 32)
		go func() {
			defer close(out)
			RunAgentLoop(ctx, prov, "test", types.ChatRequest{Prompt: query, History: history},
				nil, o2.invokerWithHooks(nil, o2.tools), out)
		}()
		return out, &types.RoutingDecision{}, nil
	})

	if err := hub2.Resume(context.Background(), rec.ID, approval.Decision{Approve: true, By: "op"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		r, err := st.Get(context.Background(), rec.ID)
		if err == nil && r.State == types.TaskCompleted {
			if !strings.Contains(r.Answer, "guarded-result") {
				t.Fatalf("resumed answer=%q", r.Answer)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task not completed after resume; err=%v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAgentStats_Feedback verifies the EMA stats feed latency/success into the
// routing view.
func TestAgentStats_Feedback(t *testing.T) {
	s := NewAgentStats()
	info := types.AgentInfo{ID: "a1", Healthy: true, SuccessRate: 1.0}
	for i := 0; i < 10; i++ {
		s.Observe("a1", 50*time.Millisecond, i < 3) // 30% success
	}
	s.Apply(&info)
	if info.LatencyP50MS <= 0 {
		t.Fatalf("latency not recorded: %+v", info)
	}
	if info.SuccessRate > 0.5 {
		t.Fatalf("success rate should reflect failures: %v", info.SuccessRate)
	}
}

var _ = errors.New // keep import if unused paths evolve
