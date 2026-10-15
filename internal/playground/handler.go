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
	"os"
	"path/filepath"
	"strings"

	"context"

	"agentcomm/internal/logx"
	"agentcomm/internal/obs"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/types"
)

//go:embed web/*
var webFS embed.FS

// Handler wires the playground HTTP routes. v3: chat runs through the Hub so
// every exchange is a durable task (state + approval visible in the panel).
type Handler struct {
	orch          *orchestrator.Orchestrator
	hub           *orchestrator.Hub
	metrics       *obs.Metrics
	version       string
	meta          map[string]string
	evalReportDir string // v3.1: serves eval/reports/latest/report.json (read-only)
}

func NewHandler(hub *orchestrator.Hub, orch *orchestrator.Orchestrator, metrics *obs.Metrics, version string, meta map[string]string) *Handler {
	return &Handler{orch: orch, hub: hub, metrics: metrics, version: version, meta: meta}
}

// SetEvalReportDir points the /api/eval/report reader at the reports root
// (wired from config.Eval.ReportDir; empty disables the endpoint).
func (h *Handler) SetEvalReportDir(dir string) { h.evalReportDir = dir }

// Mount registers UI + API routes on the given mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	// static UI (embedded)
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/chat", h.handleChat)
	mux.HandleFunc("/api/health", h.handleHealth)
	mux.HandleFunc("/api/meta", h.handleMeta)
	mux.HandleFunc("/api/eval/report", h.handleEvalReport)
}

// handleEvalReport serves the latest eval report (read-only; reports are
// produced by `make eval` / CI, never from a UI button — keeps the server
// stateless w.r.t. evaluation). 404 with a hint when no report exists yet.
func (h *Handler) handleEvalReport(w http.ResponseWriter, _ *http.Request) {
	if h.evalReportDir == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"available": false, "hint": "评测报告目录未配置（EVAL_REPORT_DIR）"})
		return
	}
	b, err := os.ReadFile(filepath.Join(h.evalReportDir, "latest", "report.json"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"available": false, "hint": "运行 make eval 生成评测报告"})
		return
	}
	var report any
	if err := json.Unmarshal(b, &report); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"available": false, "hint": "报告解析失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "report": report})
}

func (h *Handler) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":   h.version,
		"provider":  h.meta["llm"],
		"embedding": h.meta["embedding"],
	})
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": h.version})
}

type chatRequest struct {
	Query   string          `json:"query"`
	History []types.Message `json:"history"`
	Session string          `json:"session"`
}

// maxQueryBytes caps the chat request body (v3: unbounded bodies are a DoS
// and prompt-flooding vector). Overridable via GOV_MAX_QUERY_BYTES at wiring.
var maxQueryBytes int64 = 32 << 10

// SetMaxQueryBytes configures the request-body cap (called from cmd wiring).
func SetMaxQueryBytes(n int64) { maxQueryBytes = n }

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQueryBytes)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "empty query", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	// v3: durable task execution — client disconnects don't kill the task.
	if h.hub != nil {
		h.streamTask(w, ctx, req)
		return
	}
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

// streamTask runs the chat through the Hub and emits the decision event plus
// live tokens. Control tokens (input-required) become typed SSE events the
// UI renders as approval prompts.
func (h *Handler) streamTask(w http.ResponseWriter, ctx context.Context, req chatRequest) {
	rec, ch, err := h.hub.Start(ctx, req.Query, req.History, req.Session)
	if err != nil {
		logx.Error(ctx, "orchestrate(task)", "err", err, "query", req.Query)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Task-Id", rec.ID)
	flusher, _ := w.(http.Flusher)

	// decision re-derived for the panel (the hub consumed the first stream);
	// keep it lightweight: report the task identity and let tokens flow.
	writeSSE(w, flusher, "decision", map[string]any{
		"task_id": rec.ID, "state": string(rec.State),
	})
	for tok := range ch {
		switch {
		case tok.Finish == string(types.ControlInputRequired):
			writeSSE(w, flusher, "input_required", map[string]any{"task_id": rec.ID})
		case tok.Text == "" && tok.Finish == "" && len(tok.ToolCalls) == 0 && tok.Err == "":
			continue
		default:
			writeSSE(w, flusher, "token", map[string]any{"text": tok.Text, "finish": tok.Finish, "err": tok.Err})
		}
	}
	writeSSE(w, flusher, "done", map[string]any{"ok": true, "task_id": rec.ID})
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
