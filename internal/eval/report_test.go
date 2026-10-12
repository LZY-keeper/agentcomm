package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sampleReport() *Report {
	return &Report{
		SchemaVersion: 1,
		Meta:          ReportMeta{GeneratedAt: "2026-10-12T00:00:00Z", Embedding: "mock-1024", LLM: "mock", Judge: "rule"},
		Suites: map[string]*SuiteResult{
			"router": {
				Metrics:   map[string]float64{"recall_at_5": 0.95, "mrr": 0.9},
				CaseCount: 2,
				Failures:  []CaseFailure{{ID: "r-b", Query: "q2", Reasons: []string{"miss"}}},
				Cases:     []map[string]any{{"id": "r-b"}, {"id": "r-a"}},
			},
			"e2e": {
				Metrics:   map[string]float64{"success_rate": 1.0},
				CaseCount: 1,
			},
		},
	}
}

// 元测试：同输入两次序列化字节一致（Normalize 消除 map/切片序影响）。
func TestReport_JSONByteStable(t *testing.T) {
	a, err := json.MarshalIndent(sampleReport(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(sampleReport(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("两次序列化字节不一致")
	}
	// Normalize 后 failures/cases 必须已排序
	r := sampleReport()
	r.Normalize()
	if r.Suites["router"].Failures[0].ID != "r-b" || fmt.Sprint(r.Suites["router"].Cases[0]["id"]) != "r-a" {
		t.Errorf("Normalize 排序错误")
	}
}

// 元测试：StableDigest 只依赖 metrics 子树——meta 变化不影响 digest。
func TestReport_StableDigestIgnoresMeta(t *testing.T) {
	r1 := sampleReport()
	r2 := sampleReport()
	r2.Meta.GeneratedAt = "2030-01-01T00:00:00Z"
	r2.Meta.Commit = "deadbeef"
	if r1.StableDigest() == "" {
		t.Fatal("digest 为空")
	}
	if r1.StableDigest() != r2.StableDigest() {
		t.Errorf("digest 随 meta 变化: %s vs %s", r1.StableDigest(), r2.StableDigest())
	}
	// metrics 变化必须反映到 digest
	r2.Suites["router"].Metrics["recall_at_5"] = 0.5
	if r1.StableDigest() == r2.StableDigest() {
		t.Errorf("metrics 变化未影响 digest")
	}
}

func TestReport_WriteAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "20260101-000000-abcd12", "")
	r := sampleReport()
	if err := r.WriteJSON(dir, true); err != nil {
		t.Fatal(err)
	}
	// latest 兄弟目录必须存在且内容一致
	latest := filepath.Join(filepath.Dir(dir), "latest", "report.json")
	lb, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("latest 缺失: %v", err)
	}
	rb, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(lb) != string(rb) {
		t.Errorf("latest 与原始报告不一致")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "latest", "report.md")); err != nil {
		t.Errorf("latest/report.md 缺失: %v", err)
	}
	got, err := LoadReport(latest)
	if err != nil {
		t.Fatal(err)
	}
	if got.StableDigest() != r.StableDigest() {
		t.Errorf("round-trip digest 不一致")
	}
}
