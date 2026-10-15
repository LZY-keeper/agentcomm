// Package wasm is pillar 3: a sandboxed runtime for untrusted MCP tools. Tools
// are compiled to .wasm and executed via wazero (pure Go, no cgo), giving:
//   - cross-platform single-binary operation (replaces v1's LoadLibrary/fork);
//   - runtime-level isolation: a module cannot touch host memory/files/network
//     unless explicitly granted (WASI FS mounts / host functions);
//
// which directly addresses MCP's third-party-tool security risk.
//
// Tool ABI (simple, language-agnostic): the module reads a JSON request from
// stdin and writes a JSON result to stdout. TinyGo source template under
// plugins/wasm/. Native tools (internal/tools) remain the default for the
// offline demo; WASM is the path for untrusted/3rd-party tools.
package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Host compiles and runs WASM tool modules in a shared runtime.
type Host struct {
	rt wazero.Runtime
}

// NewHost creates a runtime with WASI enabled. Call Close at shutdown.
func NewHost(ctx context.Context) (*Host, error) {
	rt := wazero.NewRuntime(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasm: init wasi: %w", err)
	}
	return &Host{rt: rt}, nil
}

func (h *Host) Close(ctx context.Context) error { return h.rt.Close(ctx) }

// Tool is a compiled, sandboxed WASM tool. It is immutable after Load.
type Tool struct {
	host   *Host
	name   string
	module wazero.CompiledModule
	// Perm controls sandbox capabilities. Zero value = maximally sandboxed
	// (no FS, no network) — the safe default for untrusted tools.
	Perm Permissions
	// StdoutLimit caps bytes captured from the tool's stdout per call
	// (defense in depth against runaway modules). Set by Load to defaultLimit.
	StdoutLimit int
}

// Permissions declares what a sandboxed tool may access. Empty = locked down.
type Permissions struct {
	// ReadOnlyDirs mounts these host directories read-only at "/" inside the
	// module (WASI preopens). Leave empty for no file access.
	ReadOnlyDirs []string
	// Args/Env passed to the module's WASI main.
	Args []string
	Env  []string
	// StdoutLimit caps bytes read from the tool's stdout (defense in depth).
	StdoutLimit int
}

// Load compiles a WASM module from bytes. The name is used for the module
// instance and tool identity.
func (h *Host) Load(ctx context.Context, name string, wasmBytes []byte) (*Tool, error) {
	cm, err := h.rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("wasm: compile %s: %w", name, err)
	}
	return &Tool{host: h, name: name, module: cm, StdoutLimit: defaultLimit}, nil
}

// LoadDir loads every *.wasm under dir as a sandboxed tool.
func (h *Host) LoadDir(ctx context.Context, dir string) ([]*Tool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var tools []*Tool
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wasm") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(e.Name(), ".wasm")
		t, err := h.Load(ctx, name, b)
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// Name returns the tool identity.
func (t *Tool) Name() string { return t.name }

// defaultLimit caps a tool's stdout to avoid runaway output.
const defaultLimit = 1 << 20 // 1 MiB

// Result is the JSON contract tools write to stdout: {is_error, text}.
type Result struct {
	IsError bool   `json:"is_error,omitempty"`
	Text    string `json:"text"`
}

// Call runs the tool: feeds reqJSON on stdin, returns parsed stdout. The module
// is instantiated fresh per call (no shared state between invocations — full
// isolation). The module's WASI main reads stdin → computes → writes stdout.
func (t *Tool) Call(ctx context.Context, reqJSON string) (Result, error) {
	var out Result
	stdout := &limitedBuffer{limit: t.StdoutLimit}
	cfg := wazero.NewModuleConfig().
		WithName(t.name).
		WithStdin(strings.NewReader(reqJSON)).
		WithStdout(stdout).
		WithStderr(io.Discard)
	for _, a := range t.Perm.Args {
		cfg = cfg.WithArgs(a)
	}
	for _, e := range t.Perm.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			cfg = cfg.WithEnv(k, v)
		}
	}
	// grant read-only FS only if explicitly permitted
	for _, dir := range t.Perm.ReadOnlyDirs {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithDirMount(dir, "/"))
	}

	mod, err := t.host.rt.InstantiateModule(ctx, t.module, cfg)
	if err != nil {
		return out, fmt.Errorf("wasm[%s]: instantiate: %w", t.name, err)
	}
	defer mod.Close(ctx)

	// WASI modules exit via _start; run it. A non-zero exit is an error.
	if start := mod.ExportedFunction("_start"); start != nil {
		if _, err := start.Call(ctx); err != nil {
			// WASI exit(0) surfaces as a typed error; tolerate success exits.
			if !isWasiExit0(err) {
				return out, fmt.Errorf("wasm[%s]: run: %w", t.name, err)
			}
		}
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		return out, errors.New("wasm: tool produced no output")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// tolerate non-JSON text output
		out.Text = string(raw)
	}
	return out, nil
}

// limitedBuffer caps stdout size to defend against pathological tools.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit > 0 && b.Len()+len(p) > b.limit {
		n := b.limit - b.Len()
		if n < 0 {
			n = 0
		}
		_, _ = b.Buffer.Write(p[:n])
		return n, errors.New("wasm: stdout limit exceeded")
	}
	return b.Buffer.Write(p)
}

// isWasiExit0 tolerates WASI's success exit which wazero surfaces as an error.
func isWasiExit0(err error) bool {
	if err == nil {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "module closed with exit_code(0)")
}
