package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentcomm/internal/approval"
	"agentcomm/internal/logx"
	"agentcomm/internal/store"
	"agentcomm/internal/types"
)

// ctxKey carries per-task identity through the pipeline (audit/trace use).
type ctxKey int

const taskIDKey ctxKey = iota

// WithTaskID stores the task id in ctx (the runner does this for its whole
// execution so hooks/invokers can attribute work).
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey, id)
}

// principalCtxKey stores the caller's budget principal (govern key id).
type principalCtxKey struct{}

// WithPrincipal attaches the budget principal to ctx (set by govern.Auth).
func WithPrincipal(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, id)
}

// keyIDFromCtx returns the budget principal ("" when anonymous).
func keyIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(principalCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// TaskIDFromCtx extracts the task id ("" when absent).
func TaskIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(taskIDKey).(string); ok {
		return v
	}
	return ""
}

// errPausePersisted is returned by the approval hook when the park could not
// complete in-process (timeout/cancel/no waiter). The runner treats it as a
// graceful end of segment: the task stays input-required and Hub.Resume (or a
// Deliver) continues it later.
var errPausePersisted = errors.New("orchestrator: approval parked; task persists input-required")

// Hub owns durable task execution. It turns one query into a TaskRecord with
// a lifecycle (submitted → working → input-required? → completed/failed/
// canceled), streams tokens through a tee (clients may disconnect without
// killing the task), parks for human approval on guarded tools, and resumes
// parked tasks from checkpoints — in-process or after a restart.
type Hub struct {
	orch     *Orchestrator
	store    store.TaskStore
	sessions store.SessionStore
	gate     *approval.Gate

	approvalTimeout time.Duration
	sessionTurns    int
	streamFn        streamFunc
	usage           UsageSink

	mu      sync.Mutex
	runners map[string]*TaskRunner
	wg      sync.WaitGroup // live runner goroutines; Shutdown waits on this
}

// UsageSink receives per-task token accounting keyed by principal (the
// govern layer implements it with its budget store; key id flows via ctx).
type UsageSink interface {
	ChargeTokens(ctx context.Context, principal string, input, output int64)
}

// streamFunc runs one orchestration for a task context, returning the token
// stream plus the routing decision (the Orchestrator.Stream signature).
type streamFunc func(ctx context.Context, query string, history []types.Message, sessionKey string) (<-chan types.Token, *types.RoutingDecision, error)

// NewHub wires a Hub. store/sessions may be nil (no persistence — Stream
// falls back to the plain orchestrator path); gate may be nil (no HITL).
func NewHub(o *Orchestrator, st store.TaskStore, sess store.SessionStore, g *approval.Gate,
	approvalTimeout time.Duration, sessionTurns int) *Hub {

	if approvalTimeout <= 0 {
		approvalTimeout = time.Hour
	}
	if sessionTurns <= 0 {
		sessionTurns = 20
	}
	return &Hub{
		orch: o, store: st, sessions: sess, gate: g,
		approvalTimeout: approvalTimeout, sessionTurns: sessionTurns,
		runners: map[string]*TaskRunner{},
	}
}

// SetStream overrides the stream function (used internally by cmd wiring to
// bind the orchestrator; exported for tests).
func (h *Hub) SetStream(fn streamFunc) { h.streamFn = fn }

// SetUsageSink wires the per-key budget accounting (govern layer).
func (h *Hub) SetUsageSink(u UsageSink) { h.usage = u }

// ApprovalHooks builds the loop hooks the orchestrator must apply so every
// ReAct loop parks on RequireApproval tools (v3 governance wiring).
func (h *Hub) ApprovalHooks() *LoopHooks {
	if h.gate == nil {
		return nil
	}
	return &LoopHooks{OnBeforeTool: h.contextualApprovalHook}
}

// contextualApprovalHook resolves the task id from ctx (set by the runner via
// WithTaskID) and delegates to the runner hook logic.
func (h *Hub) contextualApprovalHook(ctx context.Context, call types.ToolCall) (bool, string, error) {
	taskID := TaskIDFromCtx(ctx)
	logx.Debug(ctx, "approval hook fired", "task", taskID, "tool", call.ToolName)
	if taskID == "" {
		return true, "", nil // non-task stream (raw Stream API): no park
	}
	r := &TaskRunner{hub: h, id: taskID}
	return r.approvalHook(ctx, call)
}

// Start launches a durable task for query. It returns the initial record and
// the tee'd token stream for direct consumption. The task keeps running even
// if the returned channel is abandoned.
func (h *Hub) Start(ctx context.Context, query string, history []types.Message, sessionKey string) (*store.TaskRecord, <-chan types.Token, error) {
	if h.streamFn == nil {
		return nil, nil, errors.New("hub: stream not wired")
	}
	// Session memory: when the client sends no history, load recent turns.
	if len(history) == 0 && h.sessions != nil && sessionKey != "" {
		if past, err := h.sessions.Load(ctx, sessionKey, h.sessionTurns); err == nil {
			history = past
		}
	}
	now := time.Now()
	rec := store.TaskRecord{
		Task: types.Task{
			ID:      store.NewID(),
			State:   types.TaskSubmitted,
			Context: sessionKey,
			History: []types.Message{{ID: store.NewID(), Role: types.RoleUser, Content: query, Timestamp: now.UnixMilli()}},
		},
		SessionKey: sessionKey,
	}
	rec.Checkpoint = &store.Checkpoint{History: history}
	if err := h.store.Create(ctx, rec); err != nil {
		return nil, nil, fmt.Errorf("hub: create task: %w", err)
	}

	out := make(chan types.Token, 64)
	r := h.spawn(rec.ID, out, loopOptsFor(rec), ctx)
	return &rec, out, r
}

// spawn starts the runner goroutine for taskID and registers it. The task
// context is detached from any HTTP request and from the hub's callers: a
// client disconnect must never kill a durable task. Only explicit Cancel or
// process exit (which abandons goroutines) ends a runner early.
func (h *Hub) spawn(taskID string, out chan types.Token, opts []LoopOpt, inherit ...context.Context) error {
	base := context.Background()
	if len(inherit) > 0 && inherit[0] != nil {
		// carry governance values (budget principal) across the detach
		if p := keyIDFromCtx(inherit[0]); p != "" {
			base = WithPrincipal(base, p)
		}
	}
	taskCtx, cancel := context.WithCancel(context.WithoutCancel(base))
	taskCtx = WithTaskID(taskCtx, taskID)
	r := &TaskRunner{hub: h, id: taskID, cancel: cancel, opts: opts, out: out}
	h.mu.Lock()
	h.runners[taskID] = r
	h.wg.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.wg.Done()
		defer func() {
			h.mu.Lock()
			delete(h.runners, taskID)
			h.mu.Unlock()
		}()
		r.run(taskCtx)
	}()
	return nil
}

func loopOptsFor(rec store.TaskRecord) []LoopOpt { return nil }

// Resume continues a parked task from its checkpoint with a human verdict.
// Used when Deliver found no in-process waiter (post-restart) or when the
// park window lapsed. Errors if the task is live or has no checkpoint.
func (h *Hub) Resume(ctx context.Context, taskID string, d approval.Decision) error {
	rec, err := h.store.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if rec.Terminal() {
		return fmt.Errorf("task %s already terminal (%s)", taskID, rec.State)
	}
	h.mu.Lock()
	if _, live := h.runners[taskID]; live {
		h.mu.Unlock()
		return fmt.Errorf("task %s already running", taskID)
	}
	h.mu.Unlock()
	if rec.Checkpoint == nil {
		return fmt.Errorf("task %s has no checkpoint", taskID)
	}
	// record the verdict on the ticket before resuming
	if rec.Approval != nil && rec.Approval.Decision == "" {
		rec.Approval.Decision = verdictStr(d.Approve)
		rec.Approval.DecidedBy = d.By
		rec.Approval.DecidedAt = time.Now()
		rec.Approval.Reason = d.Reason
	}
	// snapshot the checkpoint BEFORE clearing it: its History is the loop
	// context at park time (the record's History only has the user turn).
	ckpt := rec.Checkpoint
	pending := pendingCallOf(rec)
	if ckpt != nil {
		pending = ckpt.PendingCall
	}
	rec.State = types.TaskWorking
	rec.Checkpoint = nil // consumed by the resume opts below
	if err := h.store.Update(ctx, rec); err != nil {
		return err
	}
	opts := []LoopOpt{
		WithPending(PendingResolution{Call: pending, Approve: d.Approve, Reason: d.Reason}),
	}
	if ckpt != nil {
		opts = append(opts, WithStartHistory(ckpt.History))
	}
	out := make(chan types.Token, 64)
	return h.spawn(taskID, out, opts)
}

// pendingCallOf recovers the parked call from the record's approval ticket.
func pendingCallOf(rec store.TaskRecord) types.ToolCall {
	if rec.Approval != nil {
		return types.ToolCall{ToolName: rec.Approval.Tool, ServerID: rec.Approval.ServerID,
			Args: parseLooseJSON(rec.Approval.ArgsSummary)}
	}
	return types.ToolCall{}
}

func parseLooseJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return map[string]any{}
}

func verdictStr(approve bool) string {
	if approve {
		return "approved"
	}
	return "denied"
}

// Cancel cancels a live task (or terminates a parked one in the store).
func (h *Hub) Cancel(ctx context.Context, taskID string) error {
	h.mu.Lock()
	r, live := h.runners[taskID]
	h.mu.Unlock()
	if live {
		h.gate.Abort(taskID)
		r.cancel()
		return nil
	}
	rec, err := h.store.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if rec.Terminal() {
		return fmt.Errorf("task %s already terminal", taskID)
	}
	rec.State = types.TaskCanceled
	rec.StatusMsg = "canceled by operator"
	rec.Checkpoint, rec.Approval = nil, nil
	return h.store.Update(ctx, rec)
}

// Get proxies the store (nil-safe for no-store deployments).
func (h *Hub) Get(ctx context.Context, id string) (store.TaskRecord, error) {
	if h.store == nil {
		return store.TaskRecord{}, store.ErrNotFound
	}
	return h.store.Get(ctx, id)
}

// List proxies the store.
func (h *Hub) List(ctx context.Context, session string, limit int) ([]store.TaskRecord, error) {
	if h.store == nil {
		return nil, nil
	}
	return h.store.List(ctx, session, limit)
}

// RecoverPending re-arms tasks left non-terminal by a restart. With the Redis
// store this restores parked approvals after deploys; with the memory store
// it finds nothing (state is gone). Tasks already approved-but-unexecuted are
// resumed with the recorded verdict; still-pending ones wait for the API.
func (h *Hub) RecoverPending(ctx context.Context) int {
	type pendingStore interface {
		PendingTasks(ctx context.Context, limit int) ([]store.TaskRecord, error)
	}
	ps, ok := h.store.(pendingStore)
	if !ok {
		return 0
	}
	recs, err := ps.PendingTasks(ctx, 100)
	if err != nil {
		logx.Warn(ctx, "recover pending scan failed", "err", err)
		return 0
	}
	n := 0
	for _, rec := range recs {
		if rec.State != types.TaskInputRequired || rec.Approval == nil {
			continue
		}
		if rec.Approval.Decision == "approved" || rec.Approval.Decision == "denied" {
			d := approval.Decision{Approve: rec.Approval.Decision == "approved",
				By: rec.Approval.DecidedBy, Reason: rec.Approval.Reason}
			if err := h.Resume(ctx, rec.ID, d); err != nil {
				logx.Warn(ctx, "resume after restart failed", "task", rec.ID, "err", err)
				continue
			}
			n++
		}
	}
	if n > 0 {
		logx.Info(ctx, "recovered parked tasks after restart", "count", n)
	}
	return n
}

// Shutdown aborts parked approvals and waits for live runners to persist
// their final state. Tasks parked in input-required keep that state (their
// checkpoints survive in the store for post-restart resume).
func (h *Hub) Shutdown() {
	h.mu.Lock()
	runners := make([]*TaskRunner, 0, len(h.runners))
	for _, r := range h.runners {
		runners = append(runners, r)
	}
	h.mu.Unlock()
	for _, r := range runners {
		h.gate.Abort(r.id)
	}
	h.wg.Wait()
}

// TaskRunner executes one durable task on its own goroutine. Single-writer:
// only this goroutine mutates the TaskRecord in the store; HTTP handlers read
// snapshots or signal via the approval Gate / Cancel.
type TaskRunner struct {
	hub    *Hub
	id     string
	cancel context.CancelFunc
	opts   []LoopOpt
	out    chan types.Token
}

// run consumes the orchestrator stream, tees it into out for the client, and
// persists state transitions. Client disconnects do not stop the task: the
// tee keeps draining until the stream ends.
func (r *TaskRunner) run(ctx context.Context) {
	defer close(r.out)
	h := r.hub
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rec, err := h.store.Get(ctx, r.id)
	if err != nil {
		logx.Error(ctx, "runner: task record missing", "task", r.id, "err", err)
		return
	}
	rec.State = types.TaskWorking
	rec.StatusMsg = ""
	_ = h.store.Update(ctx, rec)

	// Approval hook: parks on RequireApproval tools.
	hooks := &LoopHooks{OnBeforeTool: r.approvalHook}
	base := h.orch.deps().Tools
	invoker := h.orch.invokerWithHooks(hooks, base)

	streamCh, decision, err := h.streamFn(ctx, lastUserQuery(rec), checkpointHistory(h, rec), rec.SessionKey)
	if err != nil {
		r.finish(ctx, types.TaskFailed, "route: "+err.Error())
		return
	}
	_ = decision // decision is reported by the stream source (SSE layer re-derives)

	var answer strings.Builder
	inputTok, outputTok := int64(0), int64(0)
	for tok := range streamCh {
		if tok.Usage != nil {
			inputTok += tok.Usage.PromptTokens
			outputTok += tok.Usage.CompletionTokens
		} else {
			outputTok += int64(len(tok.Text) / 4)
		}
		if tok.Err != "" {
			answer.WriteString(tok.Err)
		} else {
			answer.WriteString(tok.Text)
		}
		select {
		case r.out <- tok:
		case <-ctx.Done():
			// consumer gone: keep draining so the task completes server-side
		}
	}
	state := types.TaskCompleted
	msg := ""
	// Latest persisted state decides: if the approval hook persisted
	// input-required (park without verdict, e.g. shutdown mid-park), the task
	// must stay input-required so a post-restart Resume can continue it —
	// not be flattened to canceled.
	if cur, err := h.store.Get(ctx, r.id); err == nil && cur.State == types.TaskInputRequired {
		return // parked: nothing to finalize; Resume/verdict owns the lifecycle
	}
	if ctx.Err() != nil {
		state = types.TaskCanceled
		msg = "canceled"
	} else if strings.Contains(answer.String(), "[llm 错误]") {
		state = types.TaskFailed
		msg = "llm error"
	}
	r.finishAccounted(ctx, state, msg, answer.String(), inputTok, outputTok)
	if h.usage != nil {
		principal := keyIDFromCtx(ctx)
		h.usage.ChargeTokens(ctx, principal, inputTok, outputTok)
	}
	_ = invoker // invoker already bound into streamFn via orchestrator
}

// checkpointHistory returns the history the stream should start from: the
// checkpoint's snapshot for fresh tasks (contains loaded session history).
func checkpointHistory(h *Hub, rec store.TaskRecord) []types.Message {
	if rec.Checkpoint != nil {
		return rec.Checkpoint.History
	}
	return nil
}

func lastUserQuery(rec store.TaskRecord) string {
	for i := len(rec.History) - 1; i >= 0; i-- {
		if rec.History[i].Role == types.RoleUser {
			return rec.History[i].Content
		}
	}
	return ""
}

// approvalHook is the OnBeforeTool implementation: guarded tools park the
// task in input-required with a persisted checkpoint, then wait on the gate.
func (r *TaskRunner) approvalHook(ctx context.Context, call types.ToolCall) (bool, string, error) {
	h := r.hub
	if h.gate == nil {
		return true, "", nil
	}
	tool, ok := h.orch.deps().Tools.Get(call.ToolName)
	if !ok || !tool.Tool.RequireApproval {
		return true, "", nil
	}

	rec, err := h.store.Get(ctx, r.id)
	if err != nil {
		return true, "", nil // no record → no park possible; run unguarded
	}
	// persist checkpoint + ticket, flip state
	rec.State = types.TaskInputRequired
	rec.Approval = &store.ApprovalRequest{
		ID:          store.NewID(),
		TaskID:      r.id,
		Tool:        call.ToolName,
		ServerID:    call.ServerID,
		ArgsSummary: summarizeArgs(call.Args, 256),
		CreatedAt:   time.Now(),
	}
	rec.Checkpoint = &store.Checkpoint{History: historyForCheckpoint(rec), PendingCall: call}
	if err := h.store.Update(ctx, rec); err != nil {
		logx.Error(ctx, "approval checkpoint write failed", "task", r.id, "err", err)
		return false, "审批状态写入失败", nil
	}
	// let connected clients see the transition before we block
	select {
	case r.out <- types.Token{Finish: string(types.ControlInputRequired)}:
	default:
	}

	logx.Info(ctx, "tool parked for approval", "task", r.id, "tool", call.ToolName)
	d, err := h.gate.Park(ctx, r.id, h.approvalTimeout)
	if err != nil {
		// timeout / abort / shutdown: keep input-required persisted; the runner
		// segment ends. A later Deliver finds no waiter → Hub.Resume path.
		r.finish(ctx, types.TaskInputRequired, "approval not received: "+err.Error())
		return false, "", errPausePersisted
	}
	// verdict arrived: persist it and continue
	rec, err = h.store.Get(ctx, r.id)
	if err != nil {
		return d.Approve, d.Reason, nil
	}
	if rec.Approval != nil {
		rec.Approval.Decision = verdictStr(d.Approve)
		rec.Approval.DecidedBy = d.By
		rec.Approval.DecidedAt = time.Now()
		rec.Approval.Reason = d.Reason
	}
	rec.State = types.TaskWorking
	rec.Checkpoint = nil
	if err := h.store.Update(ctx, rec); err != nil {
		logx.Warn(ctx, "approval verdict write failed", "task", r.id, "err", err)
	}
	if !d.Approve {
		return false, d.Reason, nil
	}
	return true, "", nil
}

// historyForCheckpoint snapshots the conversation history for the checkpoint.
// During a task run the loop's live history isn't in the record yet, so the
// checkpoint carries the record's own history (user turn) — Resume merges it
// with WithStartHistory + WithPending.
func historyForCheckpoint(rec store.TaskRecord) []types.Message {
	return append([]types.Message(nil), rec.History...)
}

// syntheticApprovalHook adapts the runner hook for direct-loop tests: it uses
// the task id from ctx instead of a live runner.
func (h *Hub) syntheticApprovalHook(ctx context.Context, call types.ToolCall) (bool, string, error) {
	taskID := TaskIDFromCtx(ctx)
	if taskID == "" {
		return true, "", nil
	}
	r := &TaskRunner{hub: h, id: taskID}
	return r.approvalHook(ctx, call)
}

// finish writes a terminal-ish state with default accounting.
func (r *TaskRunner) finish(ctx context.Context, state types.TaskState, msg string) {
	r.finishAccounted(ctx, state, msg, "", 0, 0)
}

func (r *TaskRunner) finishAccounted(ctx context.Context, state types.TaskState, msg, answer string, inTok, outTok int64) {
	rec, err := r.hub.store.Get(ctx, r.id)
	if err != nil {
		return
	}
	// don't clobber input-required persisted by a park that raced us
	if rec.State == types.TaskInputRequired && state == types.TaskCompleted {
		return
	}
	rec.State = state
	rec.StatusMsg = msg
	if answer != "" {
		rec.Answer = answer
	}
	rec.InputTokens += inTok
	rec.OutputTokens += outTok
	if state != types.TaskInputRequired {
		rec.Checkpoint = nil // only terminal/working states drop the checkpoint
	}
	if err := r.hub.store.Update(ctx, rec); err != nil {
		logx.Warn(ctx, "task finish write failed", "task", r.id, "err", err)
	}
	// session memory: remember the exchange
	if r.hub.sessions != nil && rec.SessionKey != "" && answer != "" && state == types.TaskCompleted {
		_ = r.hub.sessions.Append(ctx, rec.SessionKey, []types.Message{
			{ID: store.NewID(), Role: types.RoleUser, Content: lastUserQuery(rec), Timestamp: time.Now().UnixMilli()},
			{ID: store.NewID(), Role: types.RoleAgent, Content: answer, TaskID: r.id, Timestamp: time.Now().UnixMilli()},
		})
	}
}

// summarizeArgs renders tool args as bounded JSON for tickets/audit, with
// secret-looking keys masked.
func summarizeArgs(args any, max int) string {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	s := maskSecrets(string(b))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

var secretKeys = []string{"password", "token", "secret", "api_key", "apikey", "authorization"}

// maskSecrets replaces values of secret-looking keys with "***".
func maskSecrets(s string) string {
	low := strings.ToLower(s)
	for _, k := range secretKeys {
		idx := 0
		for {
			i := strings.Index(low[idx:], `"`+k+`"`)
			if i < 0 {
				break
			}
			start := idx + i + len(`"`+k+`"`)
			// find the value start after the colon
			ci := strings.Index(s[start:], ":")
			if ci < 0 {
				break
			}
			vs := start + ci + 1
			for vs < len(s) && (s[vs] == ' ' || s[vs] == '"') {
				vs++
			}
			ve := vs
			for ve < len(s) && s[ve] != '"' && s[ve] != ',' && s[ve] != '}' {
				ve++
			}
			if ve > vs {
				s = s[:vs] + "***" + s[ve:]
				low = strings.ToLower(s)
			}
			idx = vs + 3
		}
	}
	return s
}
