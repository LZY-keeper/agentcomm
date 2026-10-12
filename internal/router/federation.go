package router

import (
	"context"

	"agentcomm/internal/types"
)

// ToolSource is a supply of tools for the router to federate. The concrete MCP
// implementation (internal/mcp) satisfies this in M4; offline tests and the
// playground use StaticSource.
type ToolSource interface {
	Tools(ctx context.Context) ([]types.Tool, error)
}

// StaticSource serves a fixed in-memory tool set.
type StaticSource struct {
	tools []types.Tool
}

func NewStaticSource(tools ...types.Tool) *StaticSource { return &StaticSource{tools: tools} }
func (s *StaticSource) Tools(_ context.Context) ([]types.Tool, error) { return s.tools, nil }

// MultiSource federates several upstream sources, tagging every tool with its
// originating server id so downstream dedup/selection can reason about origin.
type MultiSource struct {
	servers []namedSource
}
type namedSource struct {
	id     string
	source ToolSource
}

func NewMultiSource() *MultiSource { return &MultiSource{} }

// Add registers an upstream source under a server id.
func (m *MultiSource) Add(serverID string, src ToolSource) *MultiSource {
	m.servers = append(m.servers, namedSource{id: serverID, source: src})
	return m
}

func (m *MultiSource) Tools(ctx context.Context) ([]types.Tool, error) {
	var all []types.Tool
	for _, ns := range m.servers {
		ts, err := ns.source.Tools(ctx)
		if err != nil {
			return nil, err
		}
		for i := range ts {
			ts[i].ServerID = ns.id
			all = append(all, ts[i])
		}
	}
	return all, nil
}

// DemoTools is a representative federated tool set (3 servers, 12 tools) used
// by the playground and `make bench-router` to demonstrate token savings.
func DemoTools() []types.Tool {
	mk := func(server, name, desc string, out ...string) types.Tool {
		return types.Tool{
			ServerID: server, Name: name, Description: desc, OutputTypes: out,
			InputSchema: types.ToolSchema{Type: "object", Properties: map[string]any{"input": map[string]any{"type": "string"}}},
		}
	}
	return []types.Tool{
		mk("math", "calculator", "计算数学表达式 加减乘除 幂 开方 阶乘", "numeric"),
		mk("math", "add", "两个数相加 addition sum", "numeric"),
		mk("math", "sqrt", "平方根 square root", "numeric"),
		mk("net", "get_weather", "查询城市天气 weather forecast 温度", "weather"),
		mk("net", "get_news", "新闻 headline news 资讯", "text"),
		mk("dev", "code_review", "代码审查 review code bug", "code"),
		mk("dev", "format_code", "代码格式化 format", "code"),
		mk("dev", "run_tests", "运行测试 test", "code"),
		mk("fs", "read_file", "读取文件 file read", "file"),
		mk("fs", "write_file", "写入文件 file write", "file"),
		mk("web", "search", "搜索引擎 search query", "url"),
		mk("web", "fetch_url", "抓取网页 fetch html", "html"),
	}
}
