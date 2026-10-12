package eval

import "math"
import "testing"

// 手算用例验证指标数学的正确性（评测系统自身的元测试）。

func TestRecallAtK(t *testing.T) {
	selected := []string{"a", "b", "c", "d", "e"}
	relevant := []string{"b", "e", "z"} // 3 个相关，selected@5 命中 b、e
	if got := RecallAtK(selected, relevant, 5); !floatEq(got, 2.0/3.0) {
		t.Errorf("recall@5 = %v, want 2/3", got)
	}
	// k=1 只看第一个：未命中 z/b/e 之外——a 不相关 → 0
	if got := RecallAtK(selected, relevant, 1); got != 0 {
		t.Errorf("recall@1 = %v, want 0", got)
	}
	// k 超过 selected 长度：取全部
	if got := RecallAtK([]string{"b"}, relevant, 5); !floatEq(got, 1.0/3.0) {
		t.Errorf("recall 截断 = %v, want 1/3", got)
	}
	if got := RecallAtK(selected, nil, 5); got != 0 {
		t.Errorf("空 relevant = %v, want 0", got)
	}
}

func TestPrecisionAtK(t *testing.T) {
	selected := []string{"a", "b", "c", "d", "e"}
	relevant := []string{"b", "e"}
	if got := PrecisionAtK(selected, relevant, 5); !floatEq(got, 2.0/5.0) {
		t.Errorf("precision@5 = %v, want 0.4", got)
	}
	// 背包只选出 2 个：分母应为实际数 2 而非 k
	if got := PrecisionAtK([]string{"b", "x"}, relevant, 5); !floatEq(got, 1.0/2.0) {
		t.Errorf("precision（不足 k 个）= %v, want 0.5", got)
	}
	if got := PrecisionAtK(nil, relevant, 5); got != 0 {
		t.Errorf("空 selected = %v, want 0", got)
	}
}

func TestMRR(t *testing.T) {
	selected := []string{"a", "b", "c"}
	relevant := []string{"b"}
	if got := MRR(selected, relevant); !floatEq(got, 0.5) {
		t.Errorf("mrr = %v, want 0.5", got)
	}
	if got := MRR(selected, []string{"z"}); got != 0 {
		t.Errorf("mrr 无命中 = %v, want 0", got)
	}
	if got := MRR(selected, []string{"a"}); got != 1 {
		t.Errorf("mrr 首位命中 = %v, want 1", got)
	}
}

func TestHitAt1(t *testing.T) {
	if got := HitAt1([]string{"a", "b"}, "a"); got != 1 {
		t.Errorf("hit@1 命中 = %v, want 1", got)
	}
	if got := HitAt1([]string{"a", "b"}, "b"); got != 0 {
		t.Errorf("hit@1 未命中 = %v, want 0", got)
	}
	if got := HitAt1([]string{"a"}, ""); got != 0 {
		t.Errorf("hit@1 空 want = %v, want 0", got)
	}
	if got := HitAt1(nil, "a"); got != 0 {
		t.Errorf("hit@1 空 selected = %v, want 0", got)
	}
}

func TestMean(t *testing.T) {
	if got := Mean([]float64{1, 0, 0.5}); !floatEq(got, 0.5) {
		t.Errorf("mean = %v, want 0.5", got)
	}
	if got := Mean(nil); got != 0 {
		t.Errorf("mean 空 = %v, want 0", got)
	}
}

func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
