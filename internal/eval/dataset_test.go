package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var demoToolIDs = map[string]bool{
	"math/calculator": true, "math/add": true, "math/sqrt": true,
	"net/get_weather": true, "net/get_news": true,
	"dev/code_review": true, "dev/format_code": true, "dev/run_tests": true,
	"fs/read_file": true, "fs/write_file": true,
	"web/search": true, "web/fetch_url": true,
}

func TestLoadRouterDataset_OK(t *testing.T) {
	cases, meta, err := LoadRouterDataset(filepath.Join("testdata", "mini-router.yaml"), demoToolIDs)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(cases) != 4 {
		t.Fatalf("case 数 = %d, want 4", len(cases))
	}
	if meta.CaseCount != 4 || meta.Toolset != "demo12" {
		t.Errorf("meta = %+v", meta)
	}
	if cases[0].ID != "r-math-001" || cases[0].ExpectTop1 != "math/calculator" {
		t.Errorf("首条 = %+v", cases[0])
	}
}

func TestLoadRouterDataset_Errors(t *testing.T) {
	dir := t.TempDir()
	base, err := os.ReadFile(filepath.Join("testdata", "mini-router.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for name, tt := range map[string]struct{ mutate func(s string) string; wantSub string }{
		"未知工具":   {func(s string) string { return strings.Replace(s, "math/calculator", "math/nope", 1) }, "未知工具"},
		"空相关集":   {func(s string) string { return strings.Replace(s, `relevant_tools: ["net/get_weather"]`, `relevant_tools: []`, 1) }, "relevant_tools 为空"},
		"难度枚举":   {func(s string) string { return strings.Replace(s, "difficulty: easy", "difficulty: extreme", 1) }, "difficulty 非法"},
		"suite错":  {func(s string) string { return strings.Replace(s, "suite: router", "suite: e2e", 1) }, "suite"},
	} {
		p := write(name+".yaml", tt.mutate(string(base)))
		if _, _, err := LoadRouterDataset(p, demoToolIDs); err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: err = %v, want 含 %q", name, err, tt.wantSub)
		}
	}
}

func TestLoadE2EDataset_OK(t *testing.T) {
	cases, _, err := LoadE2EDataset(filepath.Join("testdata", "mini-e2e.yaml"))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(cases) != 3 {
		t.Fatalf("case 数 = %d, want 3", len(cases))
	}
	hitl := cases[2]
	if hitl.Approval == nil || hitl.Approval.Verdict != "approve" {
		t.Errorf("hitl 用例 = %+v", hitl)
	}
	if hitl.Script == nil || len(hitl.Script.Calls) != 1 || hitl.Script.Calls[0].Tool != "eval_guarded_echo" {
		t.Errorf("hitl script = %+v", hitl.Script)
	}
}

func TestLoadE2EDataset_Errors(t *testing.T) {
	dir := t.TempDir()
	base, err := os.ReadFile(filepath.Join("testdata", "mini-e2e.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct{ mutate func(s string) string; wantSub string }{
		"状态枚举": {func(s string) string { return strings.Replace(s, "expect_state: completed", "expect_state: finished", 1) }, "expect_state 非法"},
		"裁决枚举": {func(s string) string { return strings.Replace(s, "verdict: approve", "verdict: maybe", 1) }, "verdict 非法"},
		"审批无脚本": {func(s string) string {
			return strings.Replace(s, "    approval: {verdict: approve, expect_decision: approved}", "", 1) +
				"\n  - id: e-hitl-noScript\n    category: hitl\n    query: q\n    expect_state: completed\n    approval: {verdict: deny, expect_decision: denied}\n"
		}, "无 script"},
		"空脚本": {func(s string) string { return strings.Replace(s, "calls: [{tool: get_weather, args: {city: 北京}}]", "calls: []", 1) }, "script.calls 为空"},
	} {
		p := filepath.Join(dir, name+".yaml")
		if err := os.WriteFile(p, []byte(tt.mutate(string(base))), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadE2EDataset(p); err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: err = %v, want 含 %q", name, err, tt.wantSub)
		}
	}
}
