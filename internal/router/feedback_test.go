package router

import (
	"testing"

	"agentcomm/internal/types"
)

// memoryCountStore is the in-test CountStore double.
type memoryCountStore struct {
	counts map[string][2]float64
	saved  int
}

func (m *memoryCountStore) SaveCounts(c map[string][2]float64) error {
	m.counts = c
	m.saved++
	return nil
}
func (m *memoryCountStore) LoadCounts() (map[string][2]float64, error) {
	return m.counts, nil
}

// TestBetaFeedback_FlushPersistsAndReload pins the restart-survival contract:
// records accumulate in memory, Flush writes them, and a fresh feedback
// instance loaded from the same store adjusts scores identically.
func TestBetaFeedback_FlushPersistsAndReload(t *testing.T) {
	cs := &memoryCountStore{}
	fb := NewBetaFeedback(0.5)
	if err := fb.SetCountStore(cs); err != nil {
		t.Fatal(err)
	}
	fb.Record("calculator", true)
	fb.Record("calculator", true)
	fb.Record("calculator", false)
	if err := fb.Flush(); err != nil {
		t.Fatal(err)
	}
	if cs.saved != 1 {
		t.Fatalf("expected exactly 1 save after one Flush, got %d", cs.saved)
	}
	if c := cs.counts["calculator"]; c[0] != 2 || c[1] != 1 {
		t.Fatalf("counts = %v, want [2 1]", c)
	}

	// restart: a fresh BetaFeedback loads and applies the learned counts
	fb2 := NewBetaFeedback(0.5)
	if err := fb2.SetCountStore(cs); err != nil {
		t.Fatal(err)
	}
	scored := []types.ScoredTool{{Tool: types.Tool{Name: "calculator"}, Score: 0.50}}
	adjusted := fb2.Adjust(scored)
	// learned mean = (2+1)/(3+2) = 0.6 → +0.5*0.1 = +0.05
	if adjusted[0].Score < 0.549 || adjusted[0].Score > 0.551 {
		t.Fatalf("reloaded feedback score = %v, want ≈0.55", adjusted[0].Score)
	}
}

// TestBetaFeedback_FlushOnlyWhenDirty: a clean Flush is a no-op — the
// periodic flusher never hammers the store without new observations.
func TestBetaFeedback_FlushOnlyWhenDirty(t *testing.T) {
	cs := &memoryCountStore{}
	fb := NewBetaFeedback(0.5)
	_ = fb.SetCountStore(cs)
	if err := fb.Flush(); err != nil {
		t.Fatal(err)
	}
	if cs.saved != 0 {
		t.Fatalf("clean flush must not save, got %d saves", cs.saved)
	}
	fb.Record("x", true)
	_ = fb.Flush()
	_ = fb.Flush() // second flush: not dirty anymore
	if cs.saved != 1 {
		t.Fatalf("dirty flush must save exactly once, got %d", cs.saved)
	}
}

// TestNoopFeedback_Identity is the eval determinism law in one line: the
// stateless feedback never reorders candidates.
func TestNoopFeedback_Identity(t *testing.T) {
	in := []types.ScoredTool{
		{Tool: types.Tool{Name: "b"}, Score: 0.9},
		{Tool: types.Tool{Name: "a"}, Score: 0.5},
	}
	out := (NoopFeedback{}).Adjust(in)
	if len(out) != 2 || out[0].Tool.Name != "b" || out[1].Tool.Name != "a" {
		t.Fatalf("NoopFeedback must be identity, got %v", out)
	}
}
