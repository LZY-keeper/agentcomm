// Package playground serves the web UI for the super-agent gateway: a chat
// surface that streams real tokens over SSE plus a side panel visualizing the
// routing decision (intent, chosen agent, hybrid-retrieved tools, token-budget
// packing savings, tool calls, latency). This is the headline interview demo.
package playground

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"agentcomm/internal/logx"
	"agentcomm/internal/obs"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/types"
)

//go:embed web/*
var webFS embed.FS

// Handler wires the playground HTTP routes.
type Handler struct {
	orch    *orchestrator.Orchestrator
	metrics *obs.Metrics
	version string
	meta    map[string]string
}

func NewHandler(orch *orchestrator.Orchestrator, metrics *obs.Metrics, version string, meta map[string]string) *Handler {
	return &Handler{orch: orch, metrics: metrics, version: version, meta: meta}
}

// Mount registers UI + API routes on the given mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	// static UI (embedded)
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/chat", h.handleChat)
	mux.HandleFunc("/api/health", h.handleHealth)
	mux.HandleFunc("/api/meta", h.handleMeta)
}

func (h *Handler) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  h.version,
		"provider": h.meta["llm"],
		"embedding": h.meta["embedding"],
	})
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": h.version})
}

type chatRequest struct {
	Query   string           `json:"query"`
	History []types.Message  `json:"history"`
	Session string           `json:"session"`
}

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "empty query", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	ch, decision, err := h.orch.Stream(ctx, req.Query, req.History, req.Session)
	if err != nil {
		logx.Error(ctx, "orchestrate", "err", err, "query", req.Query)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// SSE stream: first the routing decision (so the UI explains routing before
	// tokens appear), then incremental tokens, then a done marker.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	writeSSE(w, flusher, "decision", decision)
	for tok := range ch {
		if tok.Text == "" && tok.Finish == "" && len(tok.ToolCalls) == 0 {
			continue
		}
		writeSSE(w, flusher, "token", map[string]any{"text": tok.Text, "finish": tok.Finish})
	}
	writeSSE(w, flusher, "done", map[string]any{"ok": true})
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
