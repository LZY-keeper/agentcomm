// Package taskapi exposes the durable task lifecycle over REST: create a task,
// poll its state, list pending approvals, approve/deny (in-process fast path
// or checkpoint-resume slow path), cancel, and replay the token stream via
// SSE. This is the operator/enterprise surface for the HITL pipeline.
package taskapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/approval"
	"github.com/LZY-keeper/agentcomm/internal/logx"
	"github.com/LZY-keeper/agentcomm/internal/orchestrator"
	"github.com/LZY-keeper/agentcomm/internal/store"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

// Handler mounts /api/tasks routes.
type Handler struct {
	hub   *orchestrator.Hub
	store store.TaskStore
	gate  *approval.Gate
}

func NewHandler(hub *orchestrator.Hub, st store.TaskStore, g *approval.Gate) *Handler {
	return &Handler{hub: hub, store: st, gate: g}
}

// Mount registers routes on the mux under /api/tasks.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/api/tasks", h.handleCollection)
	mux.HandleFunc("/api/tasks/", h.handleItem)
}

// handleCollection: POST create / GET list.
func (h *Handler) handleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.create(w, r)
	case http.MethodGet:
		h.list(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type createRequest struct {
	Query   string          `json:"query"`
	Session string          `json:"session"`
	History []types.Message `json:"history"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeErr(w, http.StatusBadRequest, "empty query")
		return
	}
	rec, ch, err := h.hub.Start(r.Context(), req.Query, req.History, req.Session)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// drain in background: the REST create returns immediately; state is
	// observable via GET / SSE. The drain uses a detached context — task
	// execution must not depend on this HTTP request's lifetime.
	go func() {
		drain, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		for {
			select {
			case <-drain.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
			}
		}
	}()
	writeJSON(w, http.StatusCreated, rec)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	recs, err := h.hub.List(r.Context(), session, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recs)
}

// handleItem dispatches /api/tasks/{id}[/approval|/cancel|/events].
func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if id == "" {
		writeErr(w, http.StatusNotFound, "missing task id")
		return
	}
	switch action {
	case "":
		h.get(w, r, id)
	case "approval":
		h.decide(w, r, id)
	case "cancel":
		h.cancel(w, r, id)
	case "events":
		h.events(w, r, id)
	default:
		writeErr(w, http.StatusNotFound, "unknown action")
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, id string) {
	rec, err := h.hub.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

type decideRequest struct {
	Decision string `json:"decision"` // approve | deny
	By       string `json:"by"`
	Reason   string `json:"reason"`
}

// decide is the HITL verdict entry: fast path delivers to the parked waiter;
// slow path resumes from the persisted checkpoint (works after restarts).
func (h *Handler) decide(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req decideRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	var d approval.Decision
	switch strings.ToLower(req.Decision) {
	case "approve", "approved":
		d = approval.Decision{Approve: true, By: byOrAnonymous(req.By), Reason: req.Reason}
	case "deny", "denied", "reject":
		d = approval.Decision{Approve: false, By: byOrAnonymous(req.By), Reason: req.Reason}
	default:
		writeErr(w, http.StatusBadRequest, `decision must be "approve" or "deny"`)
		return
	}
	rec, err := h.hub.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if rec.Approval == nil || rec.State != types.TaskInputRequired {
		writeErr(w, http.StatusConflict, "task has no pending approval")
		return
	}
	if h.gate.Deliver(id, d) { // fast path: waiter parked in this process
		writeJSON(w, http.StatusOK, map[string]any{"resumed": true, "mode": "in-process"})
		return
	}
	if err := h.hub.Resume(r.Context(), id, d); err != nil { // slow path: checkpoint
		logx.Warn(r.Context(), "approval resume failed", "task", id, "err", err)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resumed": true, "mode": "resume"})
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.hub.Cancel(r.Context(), id); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
}

// events replays the accumulated answer, then streams live state/answer
// updates until the task reaches a terminal state.
func (h *Handler) events(w http.ResponseWriter, r *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	last := 0
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
		rec, err := h.hub.Get(r.Context(), id)
		if err != nil {
			return
		}
		if len(rec.Answer) > last {
			writeSSE(w, flusher, "token", map[string]string{"text": rec.Answer[last:]})
			last = len(rec.Answer)
		}
		writeSSE(w, flusher, "state", map[string]any{"state": rec.State,
			"approval": rec.Approval})
		if rec.Terminal() {
			writeSSE(w, flusher, "done", map[string]any{"ok": true})
			return
		}
	}
	writeSSE(w, flusher, "done", map[string]any{"ok": false, "reason": "timeout"})
}

func byOrAnonymous(by string) string {
	if strings.TrimSpace(by) == "" {
		return "operator"
	}
	return by
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	flusher.Flush()
}
