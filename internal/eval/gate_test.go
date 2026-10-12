package eval

import "testing"

func gateReport(routerRecall, e2eRate float64) *Report {
	return &Report{
		Suites: map[string]*SuiteResult{
			"router": {Metrics: map[string]float64{"recall_at_5": routerRecall}},
			"e2e":    {Metrics: map[string]float64{"success_rate": e2eRate}},
		},
	}
}

func TestCompareGate_Floor(t *testing.T) {
	th := Thresholds{Floors: map[string]float64{"router.recall_at_5": 0.90, "e2e.success_rate": 0.90}}
	g := CompareGate(gateReport(0.95, 0.91), nil, th)
	if !g.Passed {
		t.Errorf("应通过: %v", g.Failures)
	}
	g = CompareGate(gateReport(0.89, 0.91), nil, th)
	if g.Passed || len(g.Failures) != 1 {
		t.Errorf("floor 违反应失败: passed=%v failures=%v", g.Passed, g.Failures)
	}
}

func TestCompareGate_Drift(t *testing.T) {
	th := Thresholds{MaxDropPP: map[string]float64{"default": 2.0}}
	base := gateReport(0.95, 0.95)

	cur := gateReport(0.94, 0.95) // 跌 1pp：允许
	if g := CompareGate(cur, base, th); !g.Passed {
		t.Errorf("1pp 跌幅应允许: %v", g.Failures)
	}
	cur = gateReport(0.92, 0.95) // 跌 3pp：拦截
	if g := CompareGate(cur, base, th); g.Passed || len(g.Failures) != 1 {
		t.Errorf("3pp 跌幅应拦截: passed=%v failures=%v", g.Passed, g.Failures)
	}
	cur = gateReport(0.97, 0.95) // 升 2pp：改进项
	if g := CompareGate(cur, base, th); !g.Passed || len(g.Improvements) != 1 {
		t.Errorf("提升应记为改进: %+v", g)
	}
}

func TestCompareGate_MissingMetric(t *testing.T) {
	th := Thresholds{Floors: map[string]float64{"router.nonexistent": 0.5}}
	g := CompareGate(gateReport(1, 1), nil, th)
	if g.Passed {
		t.Errorf("登记了 floor 但报告缺失指标应失败")
	}
}

func TestLoadThresholds_EmptyPath(t *testing.T) {
	th, err := LoadThresholds("")
	if err != nil || th.Floors != nil {
		t.Errorf("空路径应返回空阈值: %+v %v", th, err)
	}
}
