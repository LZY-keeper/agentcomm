package eval

// gate.go 实现评测回归门禁：floor 绝对下限 + 相对基线最大跌幅（max_drop_pp）。
// CI 中门禁失败 → cmd/eval 退出码 1。延迟/时间类指标不入门禁。

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Thresholds 是门禁阈值配置（eval/thresholds.yaml）。
type Thresholds struct {
	Floors    map[string]float64 `yaml:"floors"`      // 指标名 → 绝对下限
	MaxDropPP map[string]float64 `yaml:"max_drop_pp"` // 指标名 → 相对基线最大跌幅（百分点）；"default" 兜底
}

// DefaultDropPP 是未配置指标的最大跌幅（百分点）。
const DefaultDropPP = 2.0

// LoadThresholds 读取阈值文件。path 为空时返回空阈值（只跑不拦）。
func LoadThresholds(path string) (Thresholds, error) {
	if path == "" {
		return Thresholds{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Thresholds{}, fmt.Errorf("读取阈值: %w", err)
	}
	var t Thresholds
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return Thresholds{}, fmt.Errorf("解析阈值 %s: %w", path, err)
	}
	return t, nil
}

// GateResult 是门禁判定结果（中文可读，供 CI 日志与报告）。
type GateResult struct {
	Passed       bool
	Failures     []string
	Improvements []string
}

// CompareGate 判定当前报告是否通过：
//  1. floor：cur 的每个在 floors 中登记的指标 ≥ floor；
//  2. drift：若基线存在同指标，cur 相对 base 跌幅 ≤ max_drop_pp（默认 2.0）。
//
// 基线缺失某指标时记为失败并提示重建基线（防"删指标绕过门禁"）。
func CompareGate(cur, base *Report, th Thresholds) GateResult {
	g := GateResult{Passed: true}

	curMetrics := flattenMetrics(cur)
	baseMetrics := map[string]float64{}
	if base != nil {
		baseMetrics = flattenMetrics(base)
	}

	for name, floor := range th.Floors {
		curv, ok := curMetrics[name]
		if !ok {
			g.Failures = append(g.Failures, fmt.Sprintf("%s：当前报告缺失该指标（基线过期或 suite 未跑），请 make eval-baseline 重建", name))
			g.Passed = false
			continue
		}
		if curv < floor {
			g.Failures = append(g.Failures, fmt.Sprintf("%s = %.4f 低于 floor %.4f", name, curv, floor))
			g.Passed = false
		}
	}

	if base != nil {
		for name, curv := range curMetrics {
			basev, ok := baseMetrics[name]
			if !ok {
				continue // 新增指标无基线：floor 已兜底
			}
			limit := th.MaxDropPP[name]
			if limit == 0 {
				limit = th.MaxDropPP["default"]
			}
			if limit == 0 {
				limit = DefaultDropPP
			}
			dropPP := (basev - curv) * 100
			if dropPP > limit {
				g.Failures = append(g.Failures, fmt.Sprintf("%s = %.4f 较基线 %.4f 下跌 %.1fpp，超过允许 %.1fpp", name, curv, basev, dropPP, limit))
				g.Passed = false
			} else if (curv-basev)*100 > limit {
				g.Improvements = append(g.Improvements, fmt.Sprintf("%s = %.4f 较基线 %.4f 提升 %.1fpp", name, curv, basev, (curv-basev)*100))
			}
		}
	}
	return g
}

func flattenMetrics(r *Report) map[string]float64 {
	out := map[string]float64{}
	for suiteName, suite := range r.Suites {
		if suite == nil {
			continue
		}
		for m, v := range suite.Metrics {
			out[suiteName+"."+m] = v
		}
	}
	return out
}
