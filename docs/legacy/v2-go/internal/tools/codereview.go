package tools

import (
	"context"
	"fmt"
	"strings"

	"agentcomm/internal/types"
)

// RegisterCodeReview runs lightweight static-analysis heuristics on a snippet
// — long lines, TODO/FIXME, deep nesting, missing error checks — and returns a
// structured review. Runs offline (no LLM); pair with an LLM provider for
// deeper narrative review in the orchestrator.
func RegisterCodeReview(r *Registry) {
	schema := types.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"code": map[string]any{"type": "string", "description": "待审查的源代码"},
		},
		Required: []string{"code"},
	}
	r.Register("code_review", "代码审查 review 静态分析 TODO 长行 嵌套", schema,
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			code := stringArg(args, "code", "snippet", "source", "input")
			if strings.TrimSpace(code) == "" {
				return types.ToolResult{}, fmt.Errorf("missing code")
			}
			return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText, Text: reviewCode(code)}}}, nil
		},
		WithInputTypes("code", "text"), WithOutputTypes("code", "review"))
}

func reviewCode(code string) string {
	lines := strings.Split(code, "\n")
	var findings []string
	todos, longLines, deepNest := 0, 0, 0
	maxDepth := 0
	for i, ln := range lines {
		t := strings.TrimSpace(strings.ToLower(ln))
		if strings.Contains(t, "todo") || strings.Contains(t, "fixme") || strings.Contains(t, "xxx") {
			todos++
			findings = append(findings, fmt.Sprintf("  - L%d: 待办标记 %q", i+1, strings.TrimSpace(ln)))
		}
		if len([]rune(ln)) > 120 {
			longLines++
			findings = append(findings, fmt.Sprintf("  - L%d: 行过长 (%d 字符)，建议拆分", i+1, len([]rune(ln))))
		}
		depth := nestingDepth(ln)
		if depth > maxDepth {
			maxDepth = depth
		}
		if depth >= 5 {
			deepNest++
		}
	}
	summary := fmt.Sprintf("代码审查报告：共 %d 行；TODO/FIXME %d、过长行(>120) %d、最大嵌套 %d。",
		len(lines), todos, longLines, maxDepth)
	if maxDepth >= 5 {
		summary += " 嵌套偏深，建议提取子函数。"
	}
	if len(findings) == 0 {
		summary += " 未发现明显问题。"
	} else {
		summary += "\n发现：\n" + strings.Join(findings[:min(10, len(findings))], "\n")
	}
	return summary
}

func nestingDepth(line string) int {
	depth := 0
	for _, c := range line {
		if c == '{' || c == '(' || c == '[' {
			depth++
		}
	}
	return depth
}
