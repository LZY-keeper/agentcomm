package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agentcomm/internal/types"
)

// OpenAICompatible speaks the OpenAI /v1/chat/completions (streaming) wire
// format, which DashScope (Qwen compatible-mode), DeepSeek, Ollama and others
// all implement — giving us a provider-agnostic streaming LLM client.
type OpenAICompatible struct {
	name   string
	base   string
	apiKey string
	model  string
}

func NewOpenAICompatible(name, base, apiKey, model string) *OpenAICompatible {
	return &OpenAICompatible{name: name, base: strings.TrimRight(base, "/"), apiKey: apiKey, model: model}
}

func (c *OpenAICompatible) Name() string { return c.name }

// --- request shape ---

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
}
type oaiToolCall struct {
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Function oaiToolFunction `json:"function"`
}
type oaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type oaiTool struct {
	Type     string      `json:"type"` // "function"
	Function oaiFuncSpec `json:"function"`
}
type oaiFuncSpec struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  types.ToolSchema `json:"parameters"`
}
type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []oaiTool    `json:"tools,omitempty"`
	Stream   bool         `json:"stream"`
}

// --- response (streaming) shape ---

type oaiChunk struct {
	Choices []oaiChoice `json:"choices"`
}
type oaiChoice struct {
	Index        int      `json:"index"`
	Delta        oaiDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}
type oaiDelta struct {
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []oaiDeltaTool `json:"tool_calls,omitempty"`
}
type oaiDeltaTool struct {
	Index    int             `json:"index"`
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Function oaiToolFunction `json:"function"`
}

func (c *OpenAICompatible) Chat(ctx context.Context, req types.ChatRequest) (<-chan types.Token, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("llm[%s]: missing API key (set QWEN_API_KEY / OPENAI_API_KEY or use LLM_PROVIDER=mock)", c.name)
	}
	body, err := json.Marshal(c.buildReq(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("llm[%s]: upstream %d: %s", c.name, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	out := make(chan types.Token, 32)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		c.pump(ctx, resp.Body, out)
	}()
	return out, nil
}

func (c *OpenAICompatible) buildReq(req types.ChatRequest) oaiRequest {
	msgs := make([]oaiMessage, 0, len(req.History)+2)
	if req.System != "" {
		msgs = append(msgs, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.History {
		msgs = append(msgs, oaiMessage{Role: string(m.Role), Content: m.Content})
	}
	msgs = append(msgs, oaiMessage{Role: "user", Content: req.Prompt})

	r := oaiRequest{Model: c.model, Messages: msgs, Stream: true}
	for _, t := range req.Tools {
		r.Tools = append(r.Tools, oaiTool{Type: "function", Function: oaiFuncSpec{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema,
		}})
	}
	return r
}

// accumulated tool call being streamed (arguments arrive in fragments).
type accTool struct {
	id   string
	name string
	args strings.Builder
}

func (c *OpenAICompatible) pump(ctx context.Context, body io.Reader, out chan<- types.Token) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // large tool args possible

	acc := map[int]*accTool{}
	emitFinish := func(reason string) {
		switch reason {
		case "tool_calls":
			calls := make([]types.ToolCall, 0, len(acc))
			for _, a := range acc {
				args := decodeArgs(a.args.String())
				calls = append(calls, types.ToolCall{ToolName: a.name, Args: args})
			}
			out <- types.Token{ToolCalls: calls, Finish: "tool_calls"}
		default:
			out <- types.Token{Finish: "stop"}
		}
	}

	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			return
		}
		var chunk oaiChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				select {
				case out <- types.Token{Text: ch.Delta.Content}:
				case <-ctx.Done():
					return
				}
			}
			for _, dt := range ch.Delta.ToolCalls {
				a, ok := acc[dt.Index]
				if !ok {
					a = &accTool{}
					acc[dt.Index] = a
				}
				if dt.ID != "" {
					a.id = dt.ID
				}
				if dt.Function.Name != "" {
					a.name = dt.Function.Name
				}
				if dt.Function.Arguments != "" {
					a.args.WriteString(dt.Function.Arguments)
				}
			}
			if ch.FinishReason != nil {
				emitFinish(*ch.FinishReason)
			}
		}
	}
}

// decodeArgs parses accumulated tool-call argument JSON into an any; on failure
// returns a map with the raw string so the call still surfaces.
func decodeArgs(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return map[string]any{"_raw": raw}
}
