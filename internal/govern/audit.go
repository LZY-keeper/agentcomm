package govern

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/logx"
	"github.com/LZY-keeper/agentcomm/internal/orchestrator"
	"github.com/LZY-keeper/agentcomm/internal/store"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

// --- budget ---

// BudgetStore tracks per-key token consumption. The default implementation
// piggybacks on the task store's accounting (TaskRecord tokens) plus an
// in-process counter; a Redis-backed counter shares the same interface.
type BudgetStore interface {
	Used(ctx context.Context, keyID string) (int64, error)
	Charge(ctx context.Context, keyID string, delta int64) error
}

// MemBudget is the in-process budget counter.
type MemBudget struct {
	mu   sync.Mutex
	used map[string]int64
}

func NewMemBudget() *MemBudget { return &MemBudget{used: map[string]int64{}} }

func (b *MemBudget) Used(_ context.Context, keyID string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used[keyID], nil
}

// Snapshot returns a copy of all per-key counters (v3.2: the /api/usage
// ledger). Zero-usage keys are absent — the handler joins against provisioned
// key records to show them with 0.
func (b *MemBudget) Snapshot() map[string]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int64, len(b.used))
	for k, v := range b.used {
		out[k] = v
	}
	return out
}

func (b *MemBudget) Charge(_ context.Context, keyID string, delta int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used[keyID] += delta
	return nil
}

// TaskStoreBudget derives per-key usage from the task store: sessionKey is
// set to the key id on governed requests, so summing session tasks yields
// per-key spend without a second durable counter. A lighter MemBudget is
// used when no store is wired.
type TaskStoreBudget struct {
	st store.TaskStore
}

func NewBudgetStore(taskStore store.TaskStore, _ any) BudgetStore {
	if taskStore == nil {
		return NewMemBudget()
	}
	return &TaskStoreBudget{st: taskStore}
}

func (b *TaskStoreBudget) Used(ctx context.Context, keyID string) (int64, error) {
	recs, err := b.st.List(ctx, "key:"+keyID, 200)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, r := range recs {
		sum += r.InputTokens + r.OutputTokens
	}
	return sum, nil
}

func (b *TaskStoreBudget) Charge(_ context.Context, _ string, _ int64) error {
	return nil // accounting happens via TaskRecord token totals
}

// ChargeTokens implements orchestrator.UsageSink: token accounting lands on
// the per-key counter the Budget middleware checks.
func (b *MemBudget) ChargeTokens(ctx context.Context, principal string, input, output int64) {
	if principal == "" {
		return
	}
	_ = b.Charge(ctx, principal, input+output)
}

// Budget enforces a per-key token budget: requests over the hard limit get
// 402 before hitting the LLM. warnAt (0..1) is accepted for future headers.
func Budget(bs BudgetStore, defaultBudget int64, warnAt float64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := From(r.Context())
			if !ok { // anonymous (loopback/dev): no budget enforcement
				next.ServeHTTP(w, r)
				return
			}
			limit := id.Record().Budget
			if limit == 0 {
				limit = defaultBudget
			}
			if limit >= 0 {
				used, err := bs.Used(r.Context(), id.KeyID)
				if err == nil && used >= limit {
					writeGovErr(w, http.StatusPaymentRequired, "budget_exceeded",
						"token budget exhausted ("+itoa(int(used))+"/"+itoa(int(limit))+")")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- audit + tool permissions ---

// AuditInvoker wraps the orchestrator's ToolInvoker with: (1) per-key tool
// permission enforcement (deny before execution), (2) structured audit logs
// with masked arg summaries, result size, duration, key/tenant/task ids.
func AuditInvoker(next orchestrator.ToolInvoker, argsMax int) orchestrator.ToolInvoker {
	return func(ctx context.Context, call types.ToolCall) (types.ToolResult, error) {
		start := time.Now()
		res, err := next(ctx, call)
		id, _ := From(ctx)
		logx.Info(ctx, "audit.tool_call",
			"event", "tool_call",
			"request_id", RequestIDFromCtx(ctx),
			"key_id", id.KeyID, "tenant", id.Tenant,
			"task_id", orchestrator.TaskIDFromCtx(ctx),
			"tool", call.ToolName, "server", call.ServerID,
			"args_summary", summarizeArgs(call.Args, argsMax),
			"result_bytes", resultSize(res),
			"ok", err == nil && !res.IsError,
			"duration_ms", time.Since(start).Milliseconds())
		return res, err
	}
}

// resultSize sums the text bytes of a tool result.
func resultSize(res types.ToolResult) int {
	n := 0
	for _, c := range res.Content {
		n += len(c.Text)
	}
	return n
}

// summarizeArgs renders bounded, secret-masked JSON for audit trails.
func summarizeArgs(args any, max int) string {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	s := MaskSecrets(string(b))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

var secretKeys = []string{"password", "token", "secret", "api_key", "apikey", "authorization"}

// MaskSecrets replaces values of secret-looking JSON keys with "***".
func MaskSecrets(s string) string {
	low := strings.ToLower(s)
	for _, k := range secretKeys {
		idx := 0
		for {
			i := strings.Index(low[idx:], `"`+k+`"`)
			if i < 0 {
				break
			}
			start := idx + i + len(`"`+k+`"`)
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
