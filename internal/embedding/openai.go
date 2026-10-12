package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAICompatible calls the OpenAI /v1/embeddings endpoint, shared by
// DashScope (compatible-mode) and OpenAI.
type OpenAICompatible struct {
	name   string
	base   string
	apiKey string
	model  string
	dim    int
}

func NewOpenAICompatible(name, base, apiKey, model string, dim int) *OpenAICompatible {
	if dim <= 0 {
		dim = 1024
	}
	return &OpenAICompatible{name: name, base: strings.TrimRight(base, "/"), apiKey: apiKey, model: model, dim: dim}
}

func (o *OpenAICompatible) Name() string { return o.name }
func (o *OpenAICompatible) Dim() int     { return o.dim }

type embRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}
type embResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (o *OpenAICompatible) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if o.apiKey == "" {
		return nil, fmt.Errorf("embedding[%s]: missing API key", o.name)
	}
	body, _ := json.Marshal(embRequest{Model: o.model, Input: texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding[%s]: upstream %d: %s", o.name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var r embResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	out := make([][]float32, len(r.Data))
	for i, d := range r.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
