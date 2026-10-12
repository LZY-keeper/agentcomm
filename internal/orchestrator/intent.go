package orchestrator

import (
	"strings"
)

// Classifier maps a query to a routing intent + a confidence score. The default
// is keyword-based (deterministic, offline). An LLM-backed classifier can be
// dropped in by implementing Classify with a provider call (intent_by_llm).
type Classifier struct {
	rules []rule
}

type rule struct {
	intent string
	keys   []string
	score  float64
}

func NewClassifier() *Classifier {
	// 关键词与 internal/router/capability.go 的能力词典同步迭代
	//（评测 eval/datasets/*.yaml 驱动：同义词缺口会以 intent_accuracy 下跌暴露）。
	return &Classifier{rules: []rule{
		{"math", str("计算", "算", "加", "减", "乘", "除", "平方根", "阶乘", "开方", "求和", "次方", "余数",
			"calculator", "sqrt", "power", "+", "-", "*", "^", "plus", "minus", "times", "divided", "calculate", "compute", "what is"), 0.9},
		{"weather", str("天气", "weather", "温度", "气温", "摄氏", "华氏", "下雨", "下雪", "带伞", "紫外线", "雾霾", "气候",
			"rain", "snow", "temperature", "forecast", "climate"), 0.9},
		{"code", str("代码", "code", "review", "审查", "bug", "重构", "lint", "格式化", "缩进", "美化", "测试", "pytest",
			"gofmt", "snippet", "format", "refactor", "test"), 0.85},
		{"file", str("文件", "file", "读取", "read", "写入", "保存", "打开", "查看", "导出", "追加",
			"write", "save", "open", "cat", "dump", "append", "export"), 0.8},
	}}
}

// Classify returns (intent, score). Unknown queries get intent "general", 0.
func (c *Classifier) Classify(query string) (string, float64) {
	q := strings.ToLower(query)
	var best string
	var bestScore float64
	for _, r := range c.rules {
		for _, k := range r.keys {
			if strings.Contains(q, k) {
				if r.score > bestScore {
					best, bestScore = r.intent, r.score
				}
			}
		}
	}
	if best == "" {
		return "general", 0
	}
	return best, bestScore
}

func str(s ...string) []string { return s }
