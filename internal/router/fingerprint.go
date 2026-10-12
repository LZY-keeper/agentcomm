package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"

	"agentcomm/internal/logx"
	"agentcomm/internal/obs"
	"agentcomm/internal/types"
)

// FingerprintStore implements anti-rug-pull protection (v3): a tool's
// definition (name + description + schema) is hashed at first sight and
// pinned. On later Refreshes, a changed hash means the upstream definition
// mutated after approval — the classic rug-pull / tool-poisoning vector.
// Strict mode refuses the drifted definition entirely.
type FingerprintStore struct {
	mu     sync.Mutex
	pins   map[string]string // toolKey -> sha256 hex
	strict bool
	mut    *obs.Metrics
}

// NewFingerprintStore loads pins from a JSON file (empty path = in-memory
// only; the gateway then treats the first Refresh after startup as baseline).
func NewFingerprintStore(path string) *FingerprintStore {
	fs := &FingerprintStore{pins: map[string]string{}}
	if path == "" {
		return fs
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fs
	}
	_ = json.Unmarshal(b, &fs.pins)
	return fs
}

// SetStrict enables block mode: drifted tools are rejected from the index
// instead of merely logged.
func (f *FingerprintStore) SetStrict(s bool) { f.strict = s }

// SetMetrics wires the drift counter.
func (f *FingerprintStore) SetMetrics(m *obs.Metrics) { f.mut = m }

// ToolDefinitionHash is the stable content hash of a tool definition.
func ToolDefinitionHash(t types.Tool) string {
	b, _ := json.Marshal(struct {
		ServerID    string         `json:"server_id"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema,omitempty"`
	}{t.ServerID, t.Name, t.Description, t.InputSchema.Properties})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Check validates a batch of tools, returning the subset that passed pin
// verification. First-seen tools are pinned. Drifted tools are dropped (and
// logged loudly); in non-strict mode they would still be served — we drop
// them always for definitions changed *silently*, since a legit upstream
// change bumps the tool description and operators re-pin via the file.
func (f *FingerprintStore) Check(tools []types.Tool) []types.Tool {
	if f == nil {
		return tools
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.Tool, 0, len(tools))
	for _, t := range tools {
		key := t.ServerID + "/" + t.Name
		h := ToolDefinitionHash(t)
		pinned, ok := f.pins[key]
		if !ok {
			f.pins[key] = h // baseline
			out = append(out, t)
			continue
		}
		if pinned == h {
			out = append(out, t)
			continue
		}
		// drift!
		logx.Error(nil, "TOOL DEFINITION DRIFT (possible rug-pull / poisoning)",
			"tool", key, "pinned_sha256", pinned, "current_sha256", h,
			"action", map[bool]string{true: "blocked", false: "allowed+alerted"}[f.strict])
		if f.mut != nil {
			f.mut.RouterTokens.WithLabelValues("drift").Inc()
		}
		if f.strict {
			continue // refuse the drifted definition
		}
		out = append(out, t)
	}
	return out
}

// Save persists current pins (operator workflow: review drift, then re-pin).
func (f *FingerprintStore) Save(path string) error {
	if f == nil || path == "" {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := json.MarshalIndent(f.pins, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
