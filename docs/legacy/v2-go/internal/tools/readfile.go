package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentcomm/internal/types"
)

// readRoot limits file reads to a sandbox root (default cwd). Set via the
// READ_ROOT env var or the WithReadRoot option at registration time.
var readRoot = "."

// WithReadRoot sets the sandbox root for the file reader (call before Builtin).
func WithReadRoot(root string) { readRoot = root }

// RegisterReadFile registers a sandboxed file reader (reads text files under
// the configured root only; rejects path traversal).
func RegisterReadFile(r *Registry) {
	schema := types.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "文件相对路径"},
		},
		Required: []string{"path"},
	}
	r.Register("read_file", "读取文本文件内容 file read 沙箱受限", schema,
		func(ctx context.Context, args map[string]any) (types.ToolResult, error) {
			rel := stringArg(args, "path", "file", "filename")
			if rel == "" {
				return types.ToolResult{}, fmt.Errorf("missing path")
			}
			abs, err := safeJoin(readRoot, rel)
			if err != nil {
				return types.ToolResult{}, err
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return types.ToolResult{}, err
			}
			return types.ToolResult{Content: []types.ToolContent{{Type: types.ContentText, Text: string(data)}}}, nil
		},
		WithInputTypes("path"), WithOutputTypes("file", "text"))
}

// safeJoin resolves rel under root and rejects escapes (../ traversal).
func safeJoin(root, rel string) (string, error) {
	cleaned := filepath.Clean("/" + rel) // absolute-ize then clean → no leading ..
	abs := filepath.Join(root, cleaned)
	rl, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	al, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(al+string(filepath.Separator), rl+string(filepath.Separator)) && al != rl {
		return "", fmt.Errorf("path escapes sandbox root")
	}
	return abs, nil
}
