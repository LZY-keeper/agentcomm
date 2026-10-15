// Package llm abstracts large-language-model providers behind a streaming
// interface. Chat returns a channel of tokens, forcing end-to-end real
// streaming (no "slice + sleep" fake streaming) everywhere downstream.
//
// The default provider is "mock" — a deterministic, offline tokenizer that
// needs no API key, so the whole system runs out-of-the-box for demos.
package llm

import (
	"context"
	"fmt"

	"agentcomm/internal/config"
	"agentcomm/internal/types"
)

// Provider streams a chat completion as a channel of tokens. The channel
// closes when the response is complete; an error (if any) is delivered as the
// second return value before/as the channel drains.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req types.ChatRequest) (<-chan types.Token, error)
}

// New selects a provider from configuration.
func New(cfg config.LLMConfig) (Provider, error) {
	switch cfg.Provider {
	case "", "mock":
		return NewMock(cfg.Model), nil
	case "qwen":
		base := cfg.BaseURL
		if base == "" {
			base = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		return NewOpenAICompatible("qwen", base, cfg.APIKey, cfg.Model), nil
	case "openai", "deepseek":
		base := cfg.BaseURL
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		return NewOpenAICompatible(cfg.Provider, base, cfg.APIKey, cfg.Model), nil
	case "ollama":
		base := cfg.BaseURL
		if base == "" {
			base = "http://localhost:11434/v1"
		}
		return NewOpenAICompatible("ollama", base, cfg.APIKey, cfg.Model), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}
