package tools

import "testing"

func TestApplyApprovalDefaults(t *testing.T) {
	r := NewRegistry()
	BuiltinInto(r)
	r.ApplyApprovalDefaults([]string{"calculator"})
	e, ok := r.Get("calculator")
	if !ok {
		t.Fatal("calculator missing")
	}
	if !e.Tool.RequireApproval {
		t.Fatal("calculator should require approval after defaults")
	}
}
