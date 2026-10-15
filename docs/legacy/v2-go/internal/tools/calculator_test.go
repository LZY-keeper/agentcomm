package tools

import (
	"context"
	"testing"
)

func TestEval(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1+1", 2},
		{"2*3+4", 10},
		{"(1+2)*3", 9},
		{"2^10", 1024},
		{"sqrt(16)", 4},
		{"-3+5", 2},
		{"10/4", 2.5},
		{"2*(3+4)^2", 98},
		{"1.5+2.5", 4},
	}
	for _, c := range cases {
		got, err := Eval(c.expr)
		if err != nil {
			t.Fatalf("Eval(%q) error: %v", c.expr, err)
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	bad := []string{"1+", "", "1++", "1/0", "foo(2)", "(1+2", "1 2"}
	for _, b := range bad {
		if _, err := Eval(b); err == nil {
			t.Errorf("Eval(%q) expected error, got nil", b)
		}
	}
}

func TestRegistryCalculator(t *testing.T) {
	r := Builtin()
	res, err := r.Call(context.Background(), "calculator", map[string]any{"expression": "123+456"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	if res.Content[0].Text != "579" {
		t.Fatalf("got %q want 579", res.Content[0].Text)
	}
}
