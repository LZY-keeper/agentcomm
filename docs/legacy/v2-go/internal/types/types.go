// Package types defines shared domain types used across agentcomm.
//
// These are project-owned types aligned with (but not coupled to) the
// A2A / MCP protocol shapes. Protocol SDK types are mapped into these at
// adapter boundaries (internal/a2a, internal/mcp) so the rest of the codebase
// stays protocol-version-agnostic.
package types

import "context"

// AgentInfo describes a registered agent (A2A peer).
type AgentInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`           // A2A endpoint, e.g. http://host:5001
	Skills       []string `json:"skills"`        // capability tags used for routing/discovery
	Tags         []string `json:"tags"`          // free-form tags for registry index
	Description  string   `json:"description"`
	Version      string   `json:"version"`
	Healthy      bool     `json:"healthy"`
	// runtime stats used by cost/SLA-aware routing (see internal/orchestrator)
	LatencyP50MS float64  `json:"latency_p50_ms"`
	SuccessRate  float64  `json:"success_rate"`
	CostPerCall  float64  `json:"cost_per_call"`
}

// Role enumerates message authors in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAgent     Role = "agent"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message is one turn in a conversation history.
type Message struct {
	ID         string     `json:"id"`
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	TaskID     string     `json:"task_id,omitempty"`
	AgentID    string     `json:"agent_id,omitempty"`
	Timestamp  int64      `json:"timestamp"` // unix millis
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant tool-call requests
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool result back-reference
}

// TaskState follows A2A 2025 state names.
type TaskState string

const (
	TaskSubmitted      TaskState = "submitted"
	TaskWorking        TaskState = "working" // A2A 2025 (v1 used "running")
	TaskInputRequired  TaskState = "input-required"
	TaskCompleted      TaskState = "completed"
	TaskFailed         TaskState = "failed"
	TaskCanceled       TaskState = "canceled"
	TaskRejected       TaskState = "rejected"
)

// Task represents a long-running A2A task with checkpointable state.
type Task struct {
	ID      string    `json:"id"`
	State   TaskState `json:"state"`
	Context string    `json:"context_id"` // conversation / session id
	History []Message `json:"history"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// ToolSchema is the JSON-Schema-ish parameter description of an MCP tool.
type ToolSchema struct {
	Type       string                 `json:"type"`        // usually "object"
	Properties map[string]any         `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
}

// Tool is a federated MCP tool descriptor aggregated by the router engine.
type Tool struct {
	ServerID    string     `json:"server_id"`    // which MCP server hosts it
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema ToolSchema `json:"input_schema"`
	// Router-side metadata, computed at ingestion / selection time.
	Embedding   []float32  `json:"embedding,omitempty"`
	InputTypes  []string   `json:"input_types,omitempty"`  // capability-graph: accepted input kinds
	OutputTypes []string   `json:"output_types,omitempty"` // capability-graph: produced output kinds
	CostPerCall float64    `json:"cost_per_call,omitempty"`
	LatencyMS   float64    `json:"latency_ms,omitempty"`
	SuccessRate float64    `json:"success_rate,omitempty"`
}

// ToolCall is a request to invoke a tool with JSON arguments.
type ToolCall struct {
	ToolName string `json:"tool_name"`
	ServerID string `json:"server_id,omitempty"`
	Args     any    `json:"args"`
}

// ToolResult is the outcome of a tool invocation (MCP-shaped: isError + content).
type ToolResult struct {
	ToolName string         `json:"tool_name"`
	IsError  bool           `json:"is_error"`
	Content  []ToolContent  `json:"content"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type ToolContentType string

const (
	ContentText     ToolContentType = "text"
	ContentImage    ToolContentType = "image"
	ContentResource ToolContentType = "resource"
)

type ToolContent struct {
	Type ToolContentType `json:"type"`
	Text string          `json:"text,omitempty"`
}

// Token is one streamed LLM token. Streaming end-to-end is built on channels
// of Token (see internal/llm.Provider).
type Token struct {
	Text      string     `json:"text"`                // the piece of text (may be "")
	Role      Role       `json:"role,omitempty"`      // for leading role marker
	Finish    string     `json:"finish,omitempty"`    // non-empty on terminal token (e.g. "stop", "tool_calls")
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // present when the model requests tool calls
}

// ChatRequest is a streaming chat request to an LLM provider.
type ChatRequest struct {
	Context   context.Context `json:"-"`
	Model     string          `json:"model,omitempty"`
	System    string          `json:"system,omitempty"`
	History   []Message       `json:"history,omitempty"`
	Prompt    string          `json:"prompt"`
	Tools     []Tool          `json:"tools,omitempty"`     // function-calling tools exposed to the LLM
	MaxTokens int             `json:"max_tokens,omitempty"`
}

// RoutingDecision captures why the gateway routed a query the way it did.
// It is the data behind the Playground's decision panel and the OTel span
// attributes — making the "super agent = multi-agent + RAG-MCP" story visible.
type RoutingDecision struct {
	Query       string         `json:"query"`
	Intent      string         `json:"intent"`
	IntentScore float64        `json:"intent_score"`
	Agent       AgentInfo      `json:"agent"`
	// Tool selection (pillar 1) telemetry
	CandidateTools int         `json:"candidate_tools"`
	SelectedTools  []ScoredTool `json:"selected_tools"`
	FullTokenSize  int         `json:"full_token_size"`  // tokens if all tools injected
	PackedTokenSize int        `json:"packed_token_size"` // tokens after budget packing
	TokenSaved     int         `json:"token_saved"`
	TokenSavedPct  float64     `json:"token_saved_pct"`
	RetrievalHybrid bool        `json:"retrieval_hybrid"`
	ToolCalls      []ToolCall  `json:"tool_calls,omitempty"`
	LatencyMS      float64     `json:"latency_ms"`
	Cost           float64     `json:"cost"`
	TraceID        string      `json:"trace_id,omitempty"`
}

// ScoredTool is a tool with its (fused) retrieval score.
type ScoredTool struct {
	Tool      Tool    `json:"tool"`
	Score     float64 `json:"score"`      // fused hybrid score
	Dense     float64 `json:"dense"`      // semantic cosine
	Sparse    float64 `json:"sparse"`     // BM25
	Capability float64 `json:"capability"` // capability-graph compatibility
}
