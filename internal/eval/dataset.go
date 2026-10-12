// Package eval 是 v3.1 的评测子系统：金标数据集（YAML）、确定性运行器
// （router 召回 / E2E 任务成功）、指标计算、版本化报告与回归门禁。
//
// 确定性保证（评测可复现的前提，任何改动不得破坏）：
//  1. 反馈一律 router.NoopFeedback{}（BetaFeedback 有状态，严禁用于评测）；
//  2. embedding 写死 mock（FNV-1a，同文本比特级同向量），不读环境变量；
//  3. 对 Selected 按 (Score desc, ToolID asc) 全序重排后再算排名类指标，
//     免疫 rag.Retriever.AllTools() 的 map 迭代序；
//  4. 门禁只比较 suites.*.metrics 子树；时间戳与延迟不入门禁指标；
//  5. E2E 每例全新 fixture（memory store、gate、breaker、LB），零端口零
//     redis 零网络；task ID 随机但从不进入断言。
package eval

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// RouterCase 是路由评测的一条金标：query → 期望被选中的工具集合。
type RouterCase struct {
	ID            string   `yaml:"id" json:"id"`
	Category      string   `yaml:"category" json:"category"`         // math|weather|code|file|web|mixed
	Difficulty    string   `yaml:"difficulty" json:"difficulty"`     // easy|hard|distractor
	Query         string   `yaml:"query" json:"query"`
	RelevantTools []string `yaml:"relevant_tools" json:"relevant_tools"` // "ServerID/Name"，≥1
	ExpectTop1    string   `yaml:"expect_top1,omitempty" json:"expect_top1,omitempty"`
	Notes         string   `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// E2ECase 是端到端任务评测的一条金标：query → 期望终态/答案/工具轨迹。
// Script 为空时走原生 mock provider（数学路径）；非空时走 Scripted provider。
type E2ECase struct {
	ID                  string        `yaml:"id" json:"id"`
	Category            string        `yaml:"category" json:"category"` // math|weather|code|file|hitl|general
	Query               string        `yaml:"query" json:"query"`
	ExpectState         string        `yaml:"expect_state" json:"expect_state"`
	ExpectIntent        string        `yaml:"expect_intent,omitempty" json:"expect_intent,omitempty"`
	ExpectAgent         string        `yaml:"expect_agent,omitempty" json:"expect_agent,omitempty"`
	ExpectAnswerContains []string     `yaml:"expect_answer_contains,omitempty" json:"expect_answer_contains,omitempty"`
	ExpectToolCalls     []string      `yaml:"expect_tool_calls,omitempty" json:"expect_tool_calls,omitempty"` // 有序工具名
	Script              *Script       `yaml:"script,omitempty" json:"script,omitempty"`
	Approval            *ApprovalSpec `yaml:"approval,omitempty" json:"approval,omitempty"`
}

// Script 由 llm.NewScripted 消费：先按序发起工具调用，工具结果回喂后用
// Final 模板产出终答（{result}=最后一个工具输出，{query}=原查询）。
type Script struct {
	Calls []ScriptCall `yaml:"calls" json:"calls"`
	Final string       `yaml:"final" json:"final"`
}

// ScriptCall 是脚本中的一步工具调用。
type ScriptCall struct {
	Tool string         `yaml:"tool" json:"tool"`
	Args map[string]any `yaml:"args" json:"args"`
}

// ApprovalSpec 驱动 HITL 用例：任务 park 后按 Verdict 裁决。
type ApprovalSpec struct {
	Verdict        string `yaml:"verdict" json:"verdict"`                 // approve|deny
	ExpectDecision string `yaml:"expect_decision" json:"expect_decision"` // approved|denied
}

// DatasetMeta 记录数据集自身元信息（进报告，供追溯）。
type DatasetMeta struct {
	Version        string `yaml:"version" json:"dataset_version"`
	Toolset        string `yaml:"toolset" json:"toolset"`
	CaseCount      int    `yaml:"-" json:"case_count"`
	Source         string `yaml:"-" json:"source"`
}

type routerDatasetFile struct {
	Version int `yaml:"version"`
	Suite   string `yaml:"suite"`
	Meta    struct {
		DatasetVersion string `yaml:"dataset_version"`
		Toolset        string `yaml:"toolset"`
	} `yaml:"meta"`
	Cases []RouterCase `yaml:"cases"`
}

type e2eDatasetFile struct {
	Version int `yaml:"version"`
	Suite   string `yaml:"suite"`
	Meta    struct {
		DatasetVersion string `yaml:"dataset_version"`
		Toolset        string `yaml:"toolset"`
	} `yaml:"meta"`
	Cases []E2ECase `yaml:"cases"`
}

var (
	routerDifficulties = map[string]bool{"easy": true, "hard": true, "distractor": true}
	e2eTerminalStates  = map[string]bool{
		"completed": true, "failed": true, "canceled": true, "rejected": true, "input-required": true,
	}
)

// LoadRouterDataset 读取并校验路由金标集。toolIDs 为合法工具 ID 集合
//（"ServerID/Name"），relevant_tools 与 expect_top1 必须落在其中。
func LoadRouterDataset(path string, toolIDs map[string]bool) ([]RouterCase, DatasetMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, DatasetMeta{}, fmt.Errorf("读取路由数据集: %w", err)
	}
	var f routerDatasetFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, DatasetMeta{}, fmt.Errorf("解析路由数据集 %s: %w", path, err)
	}
	if f.Version != 1 || f.Suite != "router" {
		return nil, DatasetMeta{}, fmt.Errorf("%s: 期望 version=1 suite=router，实际 version=%d suite=%q", path, f.Version, f.Suite)
	}
	if len(f.Cases) == 0 {
		return nil, DatasetMeta{}, fmt.Errorf("%s: cases 为空", path)
	}
	seen := map[string]bool{}
	for i, c := range f.Cases {
		if c.ID == "" {
			return nil, DatasetMeta{}, fmt.Errorf("%s: 第 %d 条缺 id", path, i+1)
		}
		if seen[c.ID] {
			return nil, DatasetMeta{}, fmt.Errorf("%s: id 重复 %q", path, c.ID)
		}
		seen[c.ID] = true
		if c.Query == "" {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s 缺 query", path, c.ID)
		}
		if len(c.RelevantTools) == 0 {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s relevant_tools 为空", path, c.ID)
		}
		if !routerDifficulties[c.Difficulty] {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s difficulty 非法 %q", path, c.ID, c.Difficulty)
		}
		for _, id := range c.RelevantTools {
			if !toolIDs[id] {
				return nil, DatasetMeta{}, fmt.Errorf("%s: %s relevant_tools 含未知工具 %q", path, c.ID, id)
			}
		}
		if c.ExpectTop1 != "" && !toolIDs[c.ExpectTop1] {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s expect_top1 含未知工具 %q", path, c.ID, c.ExpectTop1)
		}
	}
	meta := DatasetMeta{Version: f.Meta.DatasetVersion, Toolset: f.Meta.Toolset, CaseCount: len(f.Cases), Source: path}
	return f.Cases, meta, nil
}

// LoadE2EDataset 读取并校验端到端金标集。
func LoadE2EDataset(path string) ([]E2ECase, DatasetMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, DatasetMeta{}, fmt.Errorf("读取 E2E 数据集: %w", err)
	}
	var f e2eDatasetFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, DatasetMeta{}, fmt.Errorf("解析 E2E 数据集 %s: %w", path, err)
	}
	if f.Version != 1 || f.Suite != "e2e" {
		return nil, DatasetMeta{}, fmt.Errorf("%s: 期望 version=1 suite=e2e，实际 version=%d suite=%q", path, f.Version, f.Suite)
	}
	if len(f.Cases) == 0 {
		return nil, DatasetMeta{}, fmt.Errorf("%s: cases 为空", path)
	}
	seen := map[string]bool{}
	for i, c := range f.Cases {
		if c.ID == "" {
			return nil, DatasetMeta{}, fmt.Errorf("%s: 第 %d 条缺 id", path, i+1)
		}
		if seen[c.ID] {
			return nil, DatasetMeta{}, fmt.Errorf("%s: id 重复 %q", path, c.ID)
		}
		seen[c.ID] = true
		if c.Query == "" {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s 缺 query", path, c.ID)
		}
		if !e2eTerminalStates[c.ExpectState] {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s expect_state 非法 %q", path, c.ID, c.ExpectState)
		}
		if c.Script != nil && len(c.Script.Calls) == 0 {
			return nil, DatasetMeta{}, fmt.Errorf("%s: %s script.calls 为空", path, c.ID)
		}
		if c.Approval != nil {
			if c.Approval.Verdict != "approve" && c.Approval.Verdict != "deny" {
				return nil, DatasetMeta{}, fmt.Errorf("%s: %s approval.verdict 非法 %q", path, c.ID, c.Approval.Verdict)
			}
			if c.Script == nil {
				return nil, DatasetMeta{}, fmt.Errorf("%s: %s 带 approval 但无 script（HITL 依赖脚本化工具调用）", path, c.ID)
			}
		}
	}
	meta := DatasetMeta{Version: f.Meta.DatasetVersion, Toolset: f.Meta.Toolset, CaseCount: len(f.Cases), Source: path}
	return f.Cases, meta, nil
}

// SortCases 按 ID 排序，保证报告输出与迭代顺序无关。
func SortRouterCases(cs []RouterCase) { sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID }) }
func SortE2ECases(cs []E2ECase)       { sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID }) }
