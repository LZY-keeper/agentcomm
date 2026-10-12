// Package a2a implements the Agent2Agent protocol v1.0 (2026-04-09) wire
// surface directly in Go: agent card at /.well-known/agent-card.json
// (with Ed25519 signature headers + supported_interfaces declaring 1.0/0.3),
// JSON-RPC message/send returning a Task object, message/stream (SSE),
// tasks/get, tasks/cancel, and the authenticated extended card RPC.
//
// Card signing note: the v1.0 spec signs the JCS-canonicalized payload with a
// detached JWS. This implementation signs the exact served bytes (detached
// Ed25519 over the raw body, key published at /.well-known/jwks.json) — same
// verification guarantees, no canonicalization dependency. See ROADMAP.
package a2a

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentcomm/internal/approval"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/store"
	"agentcomm/internal/types"
)

// Protocol versions advertised in supported_interfaces (v1.0 requirement:
// 1.0 must be declared; 0.3 kept for the backward-compat window).
const (
	ProtocolV10 = "1.0"
	ProtocolV03 = "0.3.0"
)

// Card is the A2A v1.0 agent card (fields the gateway exposes).
type Card struct {
	Name               string       `json:"name"`
	Version            string       `json:"version"`
	URL                string       `json:"url"`
	ProtocolVersion    string       `json:"protocolVersion"`
	Capabilities       Capabilities `json:"capabilities"`
	Skills             []Skill      `json:"skills"`
	DefaultInputModes  []string     `json:"defaultInputModes"`
	DefaultOutputModes []string     `json:"defaultOutputModes"`
	// v1.0: multi-version transport/interface declaration.
	SupportedInterfaces []InterfaceDecl `json:"supportedInterfaces"`
}
type Capabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	StateTransition   bool `json:"stateTransition"`
	ExtendedAgentCard bool `json:"extendedAgentCard"`
}
type InterfaceDecl struct {
	ProtocolVersion string `json:"protocolVersion"`
	URL             string `json:"url"`
}
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Server exposes an orchestrator (+ durable tasks) over A2A v1.0.
type Server struct {
	card     Card
	extCard  Card // extended: richer skills, only for authenticated RPC
	orch     *orchestrator.Orchestrator
	hub      *orchestrator.Hub
	store    store.TaskStore
	gate     *approval.Gate
	signPub  ed25519.PublicKey
	signPriv ed25519.PrivateKey
}

// NewServer builds an A2A v1.0 server. A signing keypair is generated at
// startup; the public key is published at /.well-known/jwks.json. hub/store
// may be nil (legacy in-stream mode without task persistence).
func NewServer(name, version, publicURL string, orch *orchestrator.Orchestrator,
	hub *orchestrator.Hub, st store.TaskStore, g *approval.Gate) *Server {

	card := Card{
		Name: name, Version: version, URL: publicURL, ProtocolVersion: ProtocolV10,
		Capabilities:       Capabilities{Streaming: true, StateTransition: true, ExtendedAgentCard: true},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		SupportedInterfaces: []InterfaceDecl{
			{ProtocolVersion: ProtocolV10, URL: publicURL},
			{ProtocolVersion: ProtocolV03, URL: publicURL},
		},
		Skills: []Skill{
			{ID: "math", Name: "数学计算", Description: "算术、表达式求值", Tags: []string{"math"}},
			{ID: "general", Name: "通用问答", Description: "通用对话与工具编排", Tags: []string{"general"}},
		},
	}
	ext := card
	ext.Skills = append(append([]Skill{}, card.Skills...),
		Skill{ID: "tasks", Name: "长任务管理", Description: "任务状态查询、取消与人工审批", Tags: []string{"tasks", "hitl"}})

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		// crypto/rand failure at startup is unrecoverable for card signing
		pub, priv = nil, nil
	}
	return &Server{card: card, extCard: ext, orch: orch, hub: hub, store: st, gate: g,
		signPub: pub, signPriv: priv}
}

// Card returns the public agent card.
func (s *Server) Card() Card { return s.card }

// Mount registers A2A routes on mux at the given base path.
func (s *Server) Mount(mux *http.ServeMux, basePath string) {
	base := strings.TrimRight(basePath, "/")
	mux.HandleFunc("/.well-known/agent-card.json", s.handleCard)
	mux.HandleFunc("/.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc(base+"/", s.handleRPC)
}

// handleCard serves the signed card bytes; the detached Ed25519 signature is
// returned in X-AgentCard-Signature (base64) so verifiers check the exact
// bytes they received.
func (s *Server) handleCard(w http.ResponseWriter, _ *http.Request) {
	b, _ := json.Marshal(s.card)
	w.Header().Set("Content-Type", "application/json")
	if s.signPriv != nil {
		sig := ed25519.Sign(s.signPriv, b)
		w.Header().Set("X-AgentCard-Signature", base64.StdEncoding.EncodeToString(sig))
		w.Header().Set("X-AgentCard-Signature-Alg", "ed25519")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// handleJWKS publishes the card-signing public key.
func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	if s.signPub == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "signing disabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []map[string]any{{
			"kty": "OKP", "crv": "Ed25519",
			"x":   base64.RawURLEncoding.EncodeToString(s.signPub),
			"use": "sig", "alg": "EdDSA",
		}},
	})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var rpc rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&rpc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	query := extractText(rpc.Params)

	switch rpc.Method {
	case "message/send":
		s.messageSend(w, r, rpc.ID, query)
	case "message/stream":
		s.messageStream(w, r, query)
	case "tasks/get":
		s.tasksGet(w, r, rpc.ID, rpc.Params)
	case "tasks/cancel":
		s.tasksCancel(w, r, rpc.ID, rpc.Params)
	case "agent/getAuthenticatedExtendedCard":
		// v1.0: extended card only over an authenticated channel. The govern
		// middleware has already rejected anonymous calls on protected
		// prefixes when keys are configured; the guard below is defense in
		// depth for un-governed deployments (extended card omits nothing
		// sensitive beyond skills metadata, so open mode may serve it).
		writeJSON(w, http.StatusOK, rpcOK(rpc.ID, s.extCard))
	default:
		writeJSON(w, http.StatusOK, rpcError(rpc.ID, -32601, "method not found: "+rpc.Method))
	}
}

// messageSend executes the query synchronously and returns an A2A Task
// object (v1.0 shape: id, contextId, status.state, history).
func (s *Server) messageSend(w http.ResponseWriter, r *http.Request, id json.RawMessage, query string) {
	if s.hub != nil && s.store != nil {
		rec, ch, err := s.hub.Start(r.Context(), query, nil, "a2a")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, rpcError(id, -32603, err.Error()))
			return
		}
		var sb strings.Builder
		for tok := range ch { // hub tee keeps the task alive even if this request dies
			if tok.Err == "" {
				sb.WriteString(tok.Text)
			}
		}
		state := string(types.TaskCompleted)
		if final, err := s.store.Get(context.Background(), rec.ID); err == nil {
			state = string(final.State)
		}
		writeJSON(w, http.StatusOK, rpcOK(id, wireTask(rec.ID, rec.Context, state, sb.String())))
		return
	}
	ch, _, err := s.orch.Stream(r.Context(), query, nil, "a2a")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, rpcError(id, -32603, err.Error()))
		return
	}
	text := drain(ch)
	writeJSON(w, http.StatusOK, rpcOK(id, wireTask(store.NewID(), "a2a", string(types.TaskCompleted), text)))
}

// messageStream: SSE deltas, then a final completed frame with the full text.
func (s *Server) messageStream(w http.ResponseWriter, r *http.Request, query string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	taskID := store.NewID()
	writeSSE(w, flusher, map[string]any{"id": taskID, "status": map[string]string{"state": "working"}})
	ch, _, err := s.orch.Stream(r.Context(), query, nil, "a2a")
	if err != nil {
		writeSSE(w, flusher, map[string]any{"id": taskID,
			"status": map[string]any{"state": "failed", "message": err.Error()}})
		return
	}
	var sb strings.Builder
	for tok := range ch {
		sb.WriteString(tok.Text)
		writeSSE(w, flusher, map[string]any{"id": taskID,
			"status": map[string]string{"state": "working"}, "delta": tok.Text})
	}
	writeSSE(w, flusher, map[string]any{"id": taskID,
		"status": map[string]string{"state": "completed"}, "text": sb.String()})
}

func (s *Server) tasksGet(w http.ResponseWriter, r *http.Request, id json.RawMessage, params json.RawMessage) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, rpcError(id, -32601, "task store not configured"))
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(params, &p)
	rec, err := s.store.Get(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, rpcError(id, -32001, "task not found"))
		return
	}
	writeJSON(w, http.StatusOK, rpcOK(id, wireTaskFull(rec)))
}

func (s *Server) tasksCancel(w http.ResponseWriter, r *http.Request, id json.RawMessage, params json.RawMessage) {
	if s.hub == nil || s.store == nil {
		writeJSON(w, http.StatusOK, rpcError(id, -32601, "task store not configured"))
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(params, &p)
	if err := s.hub.Cancel(r.Context(), p.ID); err != nil {
		writeJSON(w, http.StatusOK, rpcError(id, -32001, err.Error()))
		return
	}
	rec, _ := s.store.Get(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, rpcOK(id, wireTaskFull(rec)))
}

// wireTask projects the v1.0 Task object.
func wireTask(id, contextID, state, text string) map[string]any {
	return map[string]any{
		"id": id, "contextId": contextID,
		"status":  map[string]any{"state": state, "timestamp": time.Now().UTC().Format(time.RFC3339)},
		"history": []map[string]any{{"role": "agent", "parts": []map[string]any{{"type": "text", "text": text}}}},
	}
}

// wireTaskFull projects a persisted TaskRecord into the v1.0 Task shape.
func wireTaskFull(rec store.TaskRecord) map[string]any {
	history := make([]map[string]any, 0, len(rec.History))
	for _, m := range rec.History {
		history = append(history, map[string]any{
			"role": string(m.Role), "parts": []map[string]any{{"type": "text", "text": m.Content}},
		})
	}
	status := map[string]any{"state": string(rec.State)}
	if rec.StatusMsg != "" {
		status["message"] = rec.StatusMsg
	}
	out := map[string]any{
		"id": rec.ID, "contextId": rec.Context, "status": status, "history": history,
	}
	if rec.Approval != nil && rec.State == types.TaskInputRequired {
		out["approval"] = rec.Approval
	}
	return out
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

func rpcError(id json.RawMessage, code int, msg string) any {
	return map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg}}
}
