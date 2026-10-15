// Package a2a implements the Agent2Agent protocol (2025) wire format directly:
// agent card at /.well-known/agent-card.json and JSON-RPC message/send (sync)
// + message/stream (SSE) at the agent endpoint. This exposes the gateway's
// super-agent to other A2A peers (and lets the gateway call remote agents).
package a2a

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agentcomm/internal/orchestrator"
	"agentcomm/internal/types"
)

// Card is the A2A agent card (subset of the spec fields we expose).
type Card struct {
	Name            string         `json:"name"`
	Version         string         `json:"version"`
	URL             string         `json:"url"`
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    Capabilities   `json:"capabilities"`
	Skills          []Skill        `json:"skills"`
	DefaultInputModes  []string    `json:"defaultInputModes"`
	DefaultOutputModes []string    `json:"defaultOutputModes"`
}
type Capabilities struct {
	Streaming          bool `json:"streaming"`
	PushNotifications  bool `json:"pushNotifications"`
	StateTransition    bool `json:"stateTransition"`
}
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Server exposes an orchestrator over A2A at a base path.
type Server struct {
	card Card
	orch *orchestrator.Orchestrator
}

// NewServer builds an A2A server. publicURL is the externally reachable URL of
// the agent endpoint (for the card).
func NewServer(name, version, publicURL string, orch *orchestrator.Orchestrator) *Server {
	return &Server{
		card: Card{
			Name: name, Version: version, URL: publicURL, ProtocolVersion: "0.3.0",
			Capabilities:       Capabilities{Streaming: true, StateTransition: true},
			DefaultInputModes:  []string{"text"},
			DefaultOutputModes: []string{"text"},
			Skills: []Skill{
				{ID: "math", Name: "数学计算", Description: "算术、表达式求值", Tags: []string{"math"}},
				{ID: "general", Name: "通用问答", Description: "通用对话与工具编排", Tags: []string{"general"}},
			},
		},
		orch: orch,
	}
}

// Card returns the agent card.
func (s *Server) Card() Card { return s.card }

// Mount registers A2A routes on mux at the given base path (e.g. "/").
func (s *Server) Mount(mux *http.ServeMux, basePath string) {
	base := strings.TrimRight(basePath, "/")
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.card)
	})
	mux.HandleFunc(base+"/", s.handleRPC)
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	query := extractText(rpc.Params)

	switch rpc.Method {
	case "message/send":
		ch, _, err := s.orch.Stream(r.Context(), query, nil, "a2a")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, rpcError(err))
			return
		}
		text := drain(ch)
		writeJSON(w, http.StatusOK, rpcOK(rpc.ID, map[string]any{
			"state": "completed",
			"messages": []map[string]any{{
				"role":      "agent",
				"messageId": "resp-1",
				"parts":     []map[string]any{{"type": "text", "text": text}},
			}},
		}))
	case "message/stream":
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		writeSSE(w, flusher, map[string]any{"state": "working"})
		ch, _, err := s.orch.Stream(r.Context(), query, nil, "a2a")
		if err != nil {
			writeSSE(w, flusher, map[string]any{"state": "failed", "error": err.Error()})
			return
		}
		var sb strings.Builder
		for tok := range ch {
			sb.WriteString(tok.Text)
			writeSSE(w, flusher, map[string]any{"state": "working", "delta": tok.Text})
		}
		writeSSE(w, flusher, map[string]any{"state": "completed", "text": sb.String()})
	default:
		writeJSON(w, http.StatusOK, rpcError(fmt.Errorf("method not found: %s", rpc.Method)))
	}
}

func extractText(params json.RawMessage) string {
	var p struct {
		Message struct {
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	}
	_ = json.Unmarshal(params, &p)
	var sb strings.Builder
	for _, part := range p.Message.Parts {
		if part.Type == "text" || part.Type == "" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func drain(ch <-chan types.Token) string {
	var sb strings.Builder
	for t := range ch {
		sb.WriteString(t.Text)
	}
	return sb.String()
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func rpcOK(id json.RawMessage, result any) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}
func rpcError(err error) any {
	return map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32603, "message": err.Error()}}
}
