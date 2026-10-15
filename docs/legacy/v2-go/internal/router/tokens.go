package router

import "unicode"

// Tokens splits text into lowercase retrieval terms: latin [a-z0-9]+ words and
// CJK bigrams (with lone CJK chars for single-char terms). Shared by BM25 and
// capability scoring so dense/sparse/capability see the same vocabulary.
func Tokens(s string) []string {
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
		switch len(cjk) {
		case 0:
		case 1:
			out = append(out, string(cjk))
		default:
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
	rs := []rune(s)
	for i, r := range rs {
		if r >= 'A' && r <= 'Z' {
			rs[i] = r + ('a' - 'A')
		}
	}
	return string(rs)
}
