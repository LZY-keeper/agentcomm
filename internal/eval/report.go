package eval

// report.go 定义版本化评测报告：稳定 JSON（同输入同字节）、Markdown 渲染、
// latest/ 目录复制（Windows 无 symlink）与 StableDigest（可复现性指纹）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const ReportSchemaVersion = 1

// Report 是一次评测运行的完整产物。
type Report struct {
	SchemaVersion int                     `json:"schema_version"`
	Meta          ReportMeta              `json:"meta"`
	Suites        map[string]*SuiteResult `json:"suites"` // key: "router" | "e2e"
}

// ReportMeta 记录运行环境。GeneratedAt 仅作展示，不参与门禁与 digest。
type ReportMeta struct {
	GeneratedAt string            `json:"generated_at"` // 展示用；门禁比较忽略
	Commit      string            `json:"commit,omitempty"`
	GoVersion   string            `json:"go_version,omitempty"`
	Embedding   string            `json:"embedding"`
	LLM         string            `json:"llm"`
	Judge       string            `json:"judge"`
	Params      map[string]any    `json:"params,omitempty"`
	Datasets    map[string]string `json:"datasets"` // suite → dataset 版本
}

// SuiteResult 是单个 suite（router/e2e）的聚合结果。
type SuiteResult struct {
	Metrics    map[string]float64            `json:"metrics"`     // 指标名（suite.name）→ 值
	ByCategory map[string]map[string]float64 `json:"by_category"` // category → 指标 → 值
	CaseCount  int                           `json:"case_count"`
	Failures   []CaseFailure                 `json:"failures,omitempty"`
	Cases      []map[string]any              `json:"cases,omitempty"` // per-case 明细（已排序）
}

// CaseFailure 记一条未过用例（供报告与 playground 展示）。
type CaseFailure struct {
	ID      string   `json:"id"`
	Query   string   `json:"query"`
	Reasons []string `json:"reasons"`
}

// Normalize 在序列化前做确定性排序（map key、切片），保证字节级稳定。
func (r *Report) Normalize() {
	for _, suite := range r.Suites {
		if suite == nil {
			continue
		}
		sort.Slice(suite.Failures, func(i, j int) bool { return suite.Failures[i].ID < suite.Failures[j].ID })
		sort.Slice(suite.Cases, func(i, j int) bool {
			return fmt.Sprint(suite.Cases[i]["id"]) < fmt.Sprint(suite.Cases[j]["id"])
		})
	}
}

// WriteJSON 把报告写入 dir/report.json（尾换行）；再复制一份到 dir 的
// 兄弟目录 latest/report.json（评测目录约定：<out>/<stamp>/ 与 <out>/latest/）。
func (r *Report) WriteJSON(dir string, alsoLatest bool) error {
	r.Normalize()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
		return err
	}
	if !alsoLatest {
		return nil
	}
	latest := filepath.Join(filepath.Dir(dir), "latest")
	if err := os.MkdirAll(latest, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(latest, "report.json"), b, 0o644); err != nil {
		return err
	}
	if md := r.RenderMarkdown(); md != "" {
		return os.WriteFile(filepath.Join(latest, "report.md"), []byte(md), 0o644)
	}
	return nil
}

// StableDigest 只哈希 suites.*.metrics 子树的规范化 JSON——同配置重跑必须同
// digest（CI 打印此值验证可复现性）。
func (r *Report) StableDigest() string {
	subtree := map[string]map[string]float64{}
	for name, suite := range r.Suites {
		if suite != nil {
			subtree[name] = suite.Metrics
		}
	}
	// map 序不影响：encoding/json 对 map key 排序输出
	b, err := json.Marshal(subtree)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// RenderMarkdown 渲染中文 Markdown 报告（指标表 + 失败用例摘录）。
func (r *Report) RenderMarkdown() string {
	var sb stringsBuilder
	sb.printf("# agentcomm 评测报告\n\n")
	sb.printf("- 生成时间：%s\n", r.Meta.GeneratedAt)
	if r.Meta.Commit != "" {
		sb.printf("- 提交：`%s`\n", r.Meta.Commit)
	}
	sb.printf("- 环境：embedding=%s llm=%s judge=%s digest=`%s`\n\n", r.Meta.Embedding, r.Meta.LLM, r.Meta.Judge, r.StableDigest())

	for _, suiteName := range sortedKeys(r.Suites) {
		suite := r.Suites[suiteName]
		if suite == nil {
			continue
		}
		sb.printf("## %s（%d 例）\n\n", suiteName, suite.CaseCount)
		sb.printf("| 指标 | 值 |\n|---|---|\n")
		for _, m := range sortedFloatKeys(suite.Metrics) {
			sb.printf("| %s | %.4f |\n", m, suite.Metrics[m])
		}
		sb.printf("\n")
		if len(suite.Failures) > 0 {
			sb.printf("### 失败用例（%d）\n\n", len(suite.Failures))
			for _, f := range suite.Failures {
				sb.printf("- **%s** `%s`：%s\n", f.ID, f.Query, joinStrings(f.Reasons, "；"))
			}
			sb.printf("\n")
		}
	}
	return sb.String()
}

// LoadReport 从磁盘读取报告（基线对比用）。
func LoadReport(path string) (*Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("解析报告 %s: %w", path, err)
	}
	return &r, nil
}

// ---- 小工具（避免引入 slices 包依赖面） ----

type stringsBuilder struct{ b []byte }

func (s *stringsBuilder) printf(format string, args ...any) {
	s.b = fmt.Appendf(s.b, format, args...)
}

func (s *stringsBuilder) String() string { return string(s.b) }

func sortedKeys(m map[string]*SuiteResult) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedFloatKeys(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func joinStrings(xs []string, sep string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += sep
		}
		out += x
	}
	return out
}
