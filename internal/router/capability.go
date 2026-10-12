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
func requiredTypes(query string) []string {
	q := strings.ToLower(query)
	var out []string
	switch {
	case containsAny(q, "计算", "算", "calculator", "加", "减", "乘", "除", "sqrt", "power", "+", "-", "*", "/"):
		out = append(out, intentTypes["arithmetic"]...)
	case containsAny(q, "天气", "weather", "温度", "气温"):
		out = append(out, intentTypes["weather"]...)
	case containsAny(q, "代码", "code", "review", "审查", "bug"):
		out = append(out, intentTypes["code"]...)
	case containsAny(q, "文件", "file", "读取", "read"):
		out = append(out, intentTypes["file"]...)
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
