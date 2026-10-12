package router

import (
	"strings"

	"agentcomm/internal/types"
)

// Capability scoring models a lightweight tool capability graph: each tool
// declares the input/output kinds it consumes/produces, and a query implies
// required output kinds (by intent). A tool whose OutputTypes intersect the
// required kinds gets a compatibility bonus — a signal orthogonal to semantic
// and keyword retrieval, so "weather" tools win on weather queries even when
// text similarity is noisy.
var intentTypes = map[string][]string{
	"arithmetic": {"numeric", "number"},
	"weather":    {"weather"},
	"code":       {"code", "review"},
	"file":       {"file", "text"},
	"web":        {"url", "html"},
}

// requiredTypes infers the output kinds a query needs, from keywords.
// 词典随评测迭代扩充（eval/datasets/router.v1.yaml 驱动）：同义词缺口会以
// recall@5 下跌的形式暴露在评测报告里。
func requiredTypes(query string) []string {
	q := strings.ToLower(query)
	var out []string
	switch {
	case containsAny(q, "计算", "算", "calculator", "加", "减", "乘", "除", "sqrt", "power", "+", "-", "*", "/",
		"开方", "平方根", "求和", "求积", "平均值", "平均", "余数", "次方", "divided", "multiplied", "plus", "minus", "equals", "calculate", "compute", "^"):
		out = append(out, intentTypes["arithmetic"]...)
	case containsAny(q, "天气", "weather", "温度", "气温", "摄氏", "华氏", "下雨", "降雨", "下雪", "降雪", "带伞", "雨伞",
		"紫外线", "雾霾", "气候", "forecast", "temperature", "rain", "snow", "climate", "meteorological"):
		out = append(out, intentTypes["weather"]...)
	case containsAny(q, "代码", "code", "review", "审查", "bug", "重构", "lint", "格式化", "缩进", "美化", "规范",
		"注入", "风格", "测试", "pytest", "单元测试", "gofmt", "format", "refactor", "test", "style", "snippet"):
		out = append(out, intentTypes["code"]...)
	case containsAny(q, "文件", "file", "读取", "read", "写入", "保存", "写出", "打开", "查看", "导出", "导入", "追加",
		"日志", "write", "save", "dump", "append", "export", "import", "open", "cat", "load", "store"):
		out = append(out, intentTypes["file"]...)
	case containsAny(q, "搜索", "检索", "查询", "查找", "搜一下", "search", "look up", "fetch", "crawl", "grab", "download", "网页", "链接"):
		out = append(out, intentTypes["web"]...)
	}
	return out
}

// capabilityScore returns 1.0 if the tool's declared I/O kinds satisfy a
// required kind, else 0.0 (callers treat 0 as "no signal", not a penalty).
func capabilityScore(query string, t types.Tool) float64 {
	req := requiredTypes(query)
	if len(req) == 0 {
		return 0
	}
	kinds := append(append([]string{}, t.InputTypes...), t.OutputTypes...)
	for _, r := range req {
		for _, k := range kinds {
			if r == strings.ToLower(k) {
				return 1.0
			}
		}
	}
	return 0
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
