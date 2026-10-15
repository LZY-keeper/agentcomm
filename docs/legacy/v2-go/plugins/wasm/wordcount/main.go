// word_count — a sample sandboxed MCP tool compiled to WASM (TinyGo).
//
// ABI: reads a JSON request from stdin, writes a JSON {text} result to stdout.
//   {"manifest": true}                    -> returns this tool's self-description
//   {"text": "..."}                       -> counts words/chars, returns summary
//
// Build (requires TinyGo + WASI target):
//   tinygo build -o wordcount.wasm -target wasi ./main.go
// Then drop wordcount.wasm in the WASM plugin dir (WASM_DIR) and the gateway
// loads it under full sandbox (no FS/network unless granted in Permissions).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

type req struct {
	Manifest bool   `json:"manifest,omitempty"`
	Text     string `json:"text,omitempty"`
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var r req
	_ = json.Unmarshal(raw, &r)

	if r.Manifest {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"name":        "word_count",
			"description": "统计文本的词数与字符数 word count (sandboxed WASM)",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
				"required": []string{"text"},
			},
			"output_types": []string{"numeric", "text"},
		})
		return
	}

	words := len(strings.Fields(r.Text))
	chars := utf8.RuneCountInString(r.Text)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"text": fmt.Sprintf("词数 %d，字符数 %d", words, chars),
	})
}
