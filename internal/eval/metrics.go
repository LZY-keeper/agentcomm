package eval

// metrics.go 实现纯函数评测指标（无外部依赖，bench-router 与两个 runner 共用）。
//
// 约定：
//   - selected 为系统给出的有序候选列表（已按名次排序，rank 从 1 开始）；
//   - relevant 为金标相关集合（无序）；
//   - 元素格式统一为 "ServerID/ToolName"（与 rag.ToolID 一致）。

// RecallAtK = |selected[:k] ∩ relevant| / |relevant|。relevant 为空返回 0
// （金标集校验已禁止空 relevant，此处仅防御）。
func RecallAtK(selected, relevant []string, k int) float64 {
	if len(relevant) == 0 || k <= 0 {
		return 0
	}
	return float64(intersection(selected[:min(k, len(selected))], relevant)) / float64(len(relevant))
}

// PrecisionAtK = |selected[:k] ∩ relevant| / min(k, len(selected))。
// 分母用实际选出的数量而非 k：本项目 0/1 背包在紧预算下可能选出少于 k 个工具，
// 用 k 会对这些查询不公平地压低精度。
func PrecisionAtK(selected, relevant []string, k int) float64 {
	denom := min(k, len(selected))
	if denom <= 0 {
		return 0
	}
	return float64(intersection(selected[:denom], relevant)) / float64(denom)
}

// MRR = 1 / (第一个命中的相关工具名次)。无命中返回 0。
func MRR(selected, relevant []string) float64 {
	relevantSet := toSet(relevant)
	for i, id := range selected {
		if _, hit := relevantSet[id]; hit {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// HitAt1 判断期望的 top1 是否命中（0 或 1）。want 为空返回 0，
// 调用方聚合时应跳过未设 expect_top1 的用例（不计入分母）。
func HitAt1(selected []string, want string) float64 {
	if want == "" || len(selected) == 0 {
		return 0
	}
	if selected[0] == want {
		return 1
	}
	return 0
}

// Mean 求算术均值；空切片返回 0。
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func intersection(a, b []string) int {
	bs := toSet(b)
	n := 0
	for _, id := range a {
		if _, hit := bs[id]; hit {
			n++
		}
	}
	return n
}

func toSet(xs []string) map[string]struct{} {
	s := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		s[x] = struct{}{}
	}
	return s
}
