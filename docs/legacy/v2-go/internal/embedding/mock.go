package embedding

import (
	"context"
	"math"
	"unicode"
)

// Mock produces deterministic, content-aware vectors without any API key.
// It hashes tokenized text (latin words + CJK bigrams) into a fixed-size
// signed bag-of-features vector and L2-normalizes it, so texts that share
// meaningful terms land closer in cosine space — enough for the offline RAG
// retrieval demo to retrieve sensible tools (e.g. calculator for "计算").
type Mock struct {
	dim int
}

func NewMock(dim int) *Mock {
	if dim <= 0 {
		dim = 1024
	}
	return &Mock{dim: dim}
}

func (m *Mock) Name() string { return "mock" }
func (m *Mock) Dim() int     { return m.dim }

func (m *Mock) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = m.embedOne(t)
	}
	return out, nil
}

func (m *Mock) embedOne(text string) []float32 {
	v := make([]float32, m.dim)
	for _, tok := range tokenize(text) {
		h := fnv1a(tok)
		idx := int(h % uint64(m.dim))
		sign := float32(1)
		if (h>>63)&1 == 1 {
			sign = -1
		}
		v[idx] += sign // repeated terms increase magnitude
	}
	// L2 normalize
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum > 0 {
		norm := float32(1.0 / math.Sqrt(sum))
		for i := range v {
			v[i] *= norm
		}
	}
	return v
}

// tokenize returns latin [a-z0-9]+ tokens and CJK bigrams, lowercased.
func tokenize(s string) []string {
	runes := []rune(s)
	var out []string
	var word []rune
	flushWord := func() {
		if len(word) > 0 {
			out = append(out, toLower(string(word)))
			word = word[:0]
		}
	}
	var cjk []rune
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		if len(cjk) == 1 {
			out = append(out, string(cjk))
		} else {
			for i := 0; i+1 < len(cjk); i++ {
				out = append(out, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range runes {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			flushCJK()
			word = append(word, r)
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return out
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

func toLower(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}

func fnv1a(s string) uint64 {
	const offset = 1469598103934665603
	const prime = 1099511628211
	h := uint64(offset)
	for _, b := range []byte(s) {
		h ^= uint64(b)
		h *= prime
	}
	return h
}
