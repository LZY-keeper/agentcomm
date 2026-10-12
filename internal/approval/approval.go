// Package approval implements the in-process mailbox that parks a running
// ReAct loop at a RequireApproval tool call and wakes it when a human verdict
// arrives over the approval API.
//
// The Gate is deliberately stateless beyond live waiters: persistence of the
// pending call/checkpoint lives in the TaskStore (written by the approval
// hook), so a verdict delivered after a restart (no live waiter) falls back
// to the checkpoint-resume path in orchestrator.Hub.Resume.
package approval

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Decision is a human verdict on one parked tool call.
type Decision struct {
	Approve bool
	By      string // who decided (key id / "operator")
	Reason  string
}

// Errors returned by Park; callers branch on them to pick the fallback path.
var (
	// ErrTimeout: no verdict within the park window — task stays input-required.
	ErrTimeout = errors.New("approval: wait timeout")
	// ErrAborted: the task was canceled while parked.
	ErrAborted = errors.New("approval: aborted")
)

type waiter struct {
	ch      chan Decision
	done    chan struct{}
	aborted bool // set by Abort so Park can distinguish cancel from deny
}

// Gate maps taskID → at most one parked waiter. Safe for concurrent use.
type Gate struct {
	mu      sync.Mutex
	waiters map[string]*waiter
}

func NewGate() *Gate { return &Gate{waiters: map[string]*waiter{}} }

// Park blocks the calling (runner) goroutine until Deliver/Abort for taskID,
// the park timeout elapses, or ctx is canceled. At most one waiter per task:
// a second Park for the same id returns ErrTimeout immediately (defensive —
// the runner lifecycle makes this unreachable in practice).
func (g *Gate) Park(ctx context.Context, taskID string, timeout time.Duration) (Decision, error) {
	w := &waiter{ch: make(chan Decision, 1), done: make(chan struct{})}
	g.mu.Lock()
	if _, dup := g.waiters[taskID]; dup {
		g.mu.Unlock()
		return Decision{}, ErrTimeout
	}
	g.waiters[taskID] = w
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if g.waiters[taskID] == w {
			delete(g.waiters, taskID)
		}
		g.mu.Unlock()
		close(w.done)
	}()

	if timeout <= 0 {
		timeout = time.Hour
	}
	tm := time.NewTimer(timeout)
	defer tm.Stop()
	select {
	case d := <-w.ch:
		if w.aborted {
			return Decision{}, ErrAborted
		}
		return d, nil
	case <-tm.C:
		return Decision{}, ErrTimeout
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
}

// Deliver hands a verdict to a live waiter. Returns false when no waiter is
// parked for taskID in this process (post-restart) — the caller should then
// take the Hub.Resume checkpoint path.
func (g *Gate) Deliver(taskID string, d Decision) bool {
	g.mu.Lock()
	w, ok := g.waiters[taskID]
	g.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case w.ch <- d:
		return true
	case <-w.done: // waiter raced away (timeout/cancel) — not delivered
		return false
	}
}

// Abort wakes a parked waiter because the task was canceled (not because a
// human denied it): Park returns ErrAborted so the hook ends the segment
// without recording a "denied" verdict.
func (g *Gate) Abort(taskID string) {
	g.mu.Lock()
	w, ok := g.waiters[taskID]
	if ok {
		w.aborted = true
	}
	g.mu.Unlock()
	if ok {
		select {
		case w.ch <- Decision{}: // wake; Park maps it to ErrAborted
		case <-w.done:
		}
	}
}

// Parked reports whether a waiter is currently parked for taskID.
func (g *Gate) Parked(taskID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.waiters[taskID]
	return ok
}
