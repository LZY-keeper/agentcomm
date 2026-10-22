package rag

import (
	"context"
	"testing"

	"github.com/LZY-keeper/agentcomm/internal/embedding"
	"github.com/LZY-keeper/agentcomm/internal/types"
)

func sampleTools() []types.Tool {
	return []types.Tool{
		{ServerID: "math", Name: "calculator", Description: "计算数学表达式，加减乘除幂开方", InputSchema: types.ToolSchema{Type: "object"}},
		{ServerID: "math", Name: "add", Description: "两个数相加 addition", InputSchema: types.ToolSchema{Type: "object"}},
		{ServerID: "net", Name: "get_weather", Description: "查询城市天气 weather forecast", InputSchema: types.ToolSchema{Type: "object"}},
		{ServerID: "dev", Name: "code_review", Description: "review code 代码审查", InputSchema: types.ToolSchema{Type: "object"}},
	}
}

// TestRetrieve_OfflineDense verifies the default (mock embedding) path
// retrieves the right tool offline — the M1 "RAG runs without an API key" demo.
func TestRetrieve_OfflineDense(t *testing.T) {
	emb := embedding.NewMock(1024)
	r := NewRetriever(emb, NewBruteForceIndex(), NewCache(100, 0), 3, 0.0)
	if err := r.IndexTools(context.Background(), sampleTools()); err != nil {
		t.Fatalf("index: %v", err)
	}

	cases := []struct {
		query   string
		wantSub string // expected substring in top tool name
	}{
		{"计算 123+456 的结果", "calc"},
		{"今天北京天气怎么样", "weather"},
		{"帮我审查这段代码", "review"},
	}
	for _, c := range cases {
		got, err := r.Retrieve(context.Background(), c.query)
		if err != nil {
			t.Fatalf("retrieve %q: %v", c.query, err)
		}
		if len(got) == 0 {
			t.Fatalf("retrieve %q: no results", c.query)
		}
		top := got[0].Tool.Name
		if !contains(top, c.wantSub) {
			t.Fatalf("retrieve %q: top=%s want *%s*", c.query, top, c.wantSub)
		}
		t.Logf("query=%-22q -> top=%-12s score=%.3f", c.query, top, got[0].Score)
	}
}

// TestRetriever_TopK verifies top-k limiting.
func TestRetriever_TopK(t *testing.T) {
	emb := embedding.NewMock(256)
	r := NewRetriever(emb, NewBruteForceIndex(), nil, 2, 0.0)
	_ = r.IndexTools(context.Background(), sampleTools())
	got, _ := r.Retrieve(context.Background(), "计算")
	if len(got) > 2 {
		t.Fatalf("topk=2 but got %d", len(got))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
