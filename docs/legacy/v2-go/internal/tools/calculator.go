package tools

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"agentcomm/internal/types"
)

// RegisterCalculator registers a safe arithmetic evaluator (recursive descent).
// It supports + - * /, ^ (power, right-assoc), unary -, parentheses, and sqrt().
// No eval/reflect — only numeric evaluation, so it is safe to expose as a tool.
func RegisterCalculator(r *Registry) {
	schema := types.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"expression": map[string]any{"type": "string", "description": "数学表达式，如 1+2*3 或 sqrt(16)"},
		},
		Required: []string{"expression"},
	}
	r.Register("calculator", "计算数学表达式 加减乘除 幂 括号 平方根 sqrt", schema,
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			expr := stringArg(args, "expression", "expr", "q", "input")
			if strings.TrimSpace(expr) == "" {
				// tolerate bare arithmetic passed without a key
				if s := joinBareArgs(args); s != "" {
					expr = s
				}
			}
			v, err := Eval(expr)
			if err != nil {
				return types.ToolResult{}, err
			}
			return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText, Text: formatNumber(v)}}}, nil
		},
		WithInputTypes("numeric", "text"), WithOutputTypes("numeric"))
}

// Eval parses and evaluates an arithmetic expression.
func Eval(expr string) (float64, error) {
	p := &parser{src: normalizeExpr(expr)}
	p.advance()
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if !p.done {
		return 0, fmt.Errorf("unexpected trailing token %q", p.tok)
	}
	return v, nil
}

func formatNumber(v float64) string {
	if math.Mod(v, 1) == 0 && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'f', 6, 64)
}

func normalizeExpr(s string) string {
	// allow × ÷ and Chinese operators
	s = strings.ReplaceAll(s, "×", "*")
	s = strings.ReplaceAll(s, "÷", "/")
	s = strings.ReplaceAll(s, "＾", "^") // fullwidth caret
	s = strings.ReplaceAll(s, "（", "(")
	s = strings.ReplaceAll(s, "）", ")")
	return s
}

// --- recursive descent parser ---

type parser struct {
	src  string
	i    int
	tok  string
	done bool
}

func (p *parser) advance() {
	for p.i < len(p.src) && unicode.IsSpace(rune(p.src[p.i])) {
		p.i++
	}
	if p.i >= len(p.src) {
		p.done = true
		p.tok = ""
		return
	}
	c := p.src[p.i]
	if isDigit(c) || c == '.' {
		start := p.i
		for p.i < len(p.src) && (isDigit(p.src[p.i]) || p.src[p.i] == '.') {
			p.i++
		}
		p.tok = p.src[start:p.i]
		return
	}
	if isAlpha(c) {
		start := p.i
		for p.i < len(p.src) && isAlpha(p.src[p.i]) {
			p.i++
		}
		p.tok = p.src[start:p.i]
		return
	}
	p.tok = string(c)
	p.i++
}

func (p *parser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for p.tok == "+" || p.tok == "-" {
		op := p.tok
		p.advance()
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == "+" {
			v += rhs
		} else {
			v -= rhs
		}
	}
	return v, nil
}

func (p *parser) parseTerm() (float64, error) {
	v, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for p.tok == "*" || p.tok == "/" {
		op := p.tok
		p.advance()
		rhs, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op == "*" {
			v *= rhs
		} else {
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= rhs
		}
	}
	return v, nil
}

func (p *parser) parseFactor() (float64, error) {
	base, err := p.parseBase()
	if err != nil {
		return 0, err
	}
	if p.tok == "^" {
		p.advance()
		exp, err := p.parseFactor() // right associative
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

func (p *parser) parseBase() (float64, error) {
	switch {
	case p.tok == "(":
		p.advance()
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.tok != ")" {
			return 0, fmt.Errorf("expected ')'")
		}
		p.advance()
		return v, nil
	case p.tok == "-":
		p.advance()
		v, err := p.parseBase()
		return -v, err
	case p.tok == "+":
		p.advance()
		return p.parseBase()
	case isAlphaTok(p.tok):
		fn := strings.ToLower(p.tok)
		p.advance()
		if p.tok != "(" {
			return 0, fmt.Errorf("expected '(' after %s", fn)
		}
		p.advance()
		arg, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.tok != ")" {
			return 0, fmt.Errorf("expected ')'")
		}
		p.advance()
		return applyFunc(fn, arg)
	default:
		f, err := strconv.ParseFloat(p.tok, 64)
		if err != nil {
			return 0, fmt.Errorf("unexpected token %q", p.tok)
		}
		p.advance()
		return f, nil
	}
}

func applyFunc(name string, x float64) (float64, error) {
	switch name {
	case "sqrt":
		if x < 0 {
			return 0, fmt.Errorf("sqrt of negative")
		}
		return math.Sqrt(x), nil
	case "abs":
		return math.Abs(x), nil
	case "sin":
		return math.Sin(x), nil
	case "cos":
		return math.Cos(x), nil
	case "ln":
		return math.Log(x), nil
	case "log":
		return math.Log10(x), nil
	default:
		return 0, fmt.Errorf("unknown function %s", name)
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}
func isAlphaTok(s string) bool {
	return s != "" && isAlpha(s[0])
}

// stringArg reads a string from args trying several keys.
func stringArg(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			}
		}
	}
	return ""
}

// joinBareArgs joins non-schema args when callers pass {"123+456": ...} or a list.
func joinBareArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	// best-effort: concatenate all string values
	var parts []string
	for _, v := range args {
		if s, ok := v.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "")
}
