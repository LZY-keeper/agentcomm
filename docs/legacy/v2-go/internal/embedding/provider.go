// Package embedding abstracts text-embedding providers. As with llm, the
// default is an offline deterministic "mock" so RAG retrieval runs without an
// API key; DashScope / OpenAI-compatible providers are pluggable.
package embedding

import (
	"context"
	"fmt"

	"agentcomm/internal/config"
)

// Provider turns texts into vectors.
type Provider interface {
	Name() string
	Dim() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// New selects an embedding provider from configuration.
func New(cfg config.EmbeddingConfig) (Provider, error) {
	switch cfg.Provider {
	case "", "mock":
		return NewMock(cfg.Dimension), nil
	case "dashscope":
		base := "https://dashscope.aliyuncs.com/compatible-mode/v1"
		if cfg.Model == "" {
			cfg.Model = "text-embedding-v2"
		}
		return NewOpenAICompatible("dashscope", base, cfg.APIKey, cfg.Model, cfg.Dimension), nil
	case "openai":
		base := "https://api.openai.com/v1"
		if cfg.Model == "" {
			cfg.Model = "text-embedding-3-small"
		}
		return NewOpenAICompatible("openai", base, cfg.APIKey, cfg.Model, cfg.Dimension), nil
	default:
		return nil, fmt.Errorf("embedding: unknown provider %q", cfg.Provider)
	}
}
