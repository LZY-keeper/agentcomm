// Package store provides durable task/session persistence for the gateway's
// HITL (human-in-the-loop) pipeline: TaskStore keeps A2A-shaped task records
// with checkpoints and pending approvals; SessionStore keeps per-session
// conversation history for cross-request memory.
//
// Two backends: MemoryTaskStore (default, TTL + janitor) and RedisStores
// (enabled when REDIS_ADDR is set — survives restarts so parked approvals can
// be resumed). Callers depend only on the interfaces; cmd/ wires the backend.
package store

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/types"
)

// cryptoRead is a thin indirection over rand.Read (keeps NewID testable).
func cryptoRead(b []byte) (int, error) { return rand.Read(b) }

// ErrNotFound is returned by Get/Delete when the record does not exist (or
// has expired). ErrConflict is returned by Update when the caller's Rev does
// not match the stored revision (optimistic locking).
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: revision conflict")
)

// Clock abstracts time for TTL logic so tests can advance it deterministically.
type Clock interface{ Now() time.Time }

// RealClock is the production clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Checkpoint is the minimal snapshot needed to resume a paused ReAct loop:
// the full history at the park point plus the tool call awaiting a verdict.
type Checkpoint struct {
	History     []types.Message `json:"history"`      // includes the agent tool_calls message
	PendingCall types.ToolCall  `json:"pending_call"` // call parked for approval
	Iter        int             `json:"iter"`         // ReAct iteration at park time
}

// ApprovalRequest is one pending (or decided) human-approval ticket.
// Persisted so the UI can list it and auditors can replay it.
type ApprovalRequest struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	Tool        string    `json:"tool"`
	ServerID    string    `json:"server_id,omitempty"`
	ArgsSummary string    `json:"args_summary,omitempty"` // truncated JSON (bounded)
	CreatedAt   time.Time `json:"created_at"`
	Decision    string    `json:"decision,omitempty"` // "" | approved | denied | aborted
	DecidedBy   string    `json:"decided_by,omitempty"`
	DecidedAt   time.Time `json:"decided_at,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// TaskRecord is the gateway's durable task — A2A Task plus orchestration
// metadata. Wire responses project the embedded types.Task; the extra fields
// are gateway-private (checkpoints, accounting, approvals).
type TaskRecord struct {
	types.Task
	Rev          int64            `json:"rev"` // optimistic-lock revision, managed by the store
	SessionKey   string           `json:"session_key,omitempty"`
	StatusMsg    string           `json:"status_message,omitempty"`
	Checkpoint   *Checkpoint      `json:"checkpoint,omitempty"` // present while working/input-required
	Approval     *ApprovalRequest `json:"approval,omitempty"`   // pending/last approval ticket
	Answer       string           `json:"answer,omitempty"`     // accumulated streamed output
	InputTokens  int64            `json:"input_tokens,omitempty"`
	OutputTokens int64            `json:"output_tokens,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	ExpiresAt    time.Time        `json:"expires_at"`
}

// Terminal reports whether the task reached a final state.
func (t TaskRecord) Terminal() bool {
	switch t.State {
	case types.TaskCompleted, types.TaskFailed, types.TaskCanceled, types.TaskRejected:
		return true
	}
	return false
}

// SessionRecord is a rolling window of one session's conversation.
type SessionRecord struct {
	SessionKey string          `json:"session_key"`
	Messages   []types.Message `json:"messages"`
	UpdatedAt  time.Time       `json:"updated_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
}

// TaskStore persists task lifecycle records.
//
// Single-writer discipline: all mutations of a running task's record happen on
// the task's own runner goroutine; HTTP handlers only Create/Get/List/Delete
// and park decisions flow through the approval Gate back into the runner.
// Rev-based CAS guards the exceptional paths (cross-process resume).
type TaskStore interface {
	Create(ctx context.Context, t TaskRecord) error
	Get(ctx context.Context, id string) (TaskRecord, error) // ErrNotFound
	Update(ctx context.Context, t TaskRecord) error         // rev CAS; ErrConflict
	List(ctx context.Context, session string, limit int) ([]TaskRecord, error)
	Delete(ctx context.Context, id string) error
	Close() error
}

// SessionStore keeps per-session conversation history with TTL.
type SessionStore interface {
	Append(ctx context.Context, sessionKey string, msgs []types.Message) error
	Load(ctx context.Context, sessionKey string, maxTurns int) ([]types.Message, error)
	Close() error
}

// NewID returns a random identifier suitable for task/approval ids.
func NewID() string {
	var b [16]byte
	if _, err := cryptoRead(b[:]); err != nil {
		// crypto/rand failing is catastrophic but must not panic the gateway;
		// fall back to a timestamp-based id (still unique enough per process).
		return time.Now().Format("20060102150405.000000000")
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}
