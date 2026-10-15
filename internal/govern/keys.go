package govern

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// KeyRecord is one virtual API key (2026 gateway-standard governance
// primitive): a tenant-scoped credential with its own rate limit, token
// budget, and optional MCP tool allow-list. Plaintext keys never leave the
// operator's config; only SHA-256 hashes are held in memory.
type KeyRecord struct {
	ID        string   `json:"id"`
	KeySHA256 string   `json:"key_sha256"` // hex; lowercased
	Tenant    string   `json:"tenant"`
	RPS       float64  `json:"rps,omitempty"`     // 0 → gateway default
	Burst     int      `json:"burst,omitempty"`   // 0 → gateway default
	Budget    int64    `json:"budget_tokens"`     // <0 = unlimited; 0 → default
	Tools     []string `json:"tools,omitempty"`   // empty = all tools allowed
	Servers   []string `json:"servers,omitempty"` // empty = all MCP servers allowed
}

// AllowsTool reports whether the key may see/call a tool on a server.
// Empty allow-lists mean "all" (fail-open inside a tenant; the tenant itself
// is the hard boundary, per-key tool filters are an extra restriction).
func (k KeyRecord) AllowsTool(server, tool string) bool {
	if len(k.Tools) == 0 && len(k.Servers) == 0 {
		return true
	}
	toolOK := len(k.Tools) == 0
	for _, t := range k.Tools {
		if t == tool {
			toolOK = true
			break
		}
	}
	srvOK := len(k.Servers) == 0
	for _, s := range k.Servers {
		if s == server || s == "builtin" && server == "" {
			srvOK = true
			break
		}
	}
	return toolOK && srvOK
}

// Keys is the in-memory key index (hash → record).
type Keys struct {
	byHash map[string]KeyRecord
}

// Count returns the number of loaded keys.
func (k *Keys) Count() int { return len(k.byHash) }

// Lookup finds a key record by plaintext; constant-time over the hash.
func (k *Keys) Lookup(plaintext string) (KeyRecord, bool) {
	h := HashKey(plaintext)
	rec, ok := k.byHash[h]
	if !ok {
		// burn comparable time against a dummy to flatten timing
		subtle.ConstantTimeCompare([]byte(h), []byte("0000000000000000000000000000000000000000000000000000000000000000"))
		return KeyRecord{}, false
	}
	return rec, true
}

// HashKey is the config/storage form of a plaintext key.
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// LoadKeys builds the key index from GOV_KEYS inline entries and/or a JSON
// file. Inline format (comma-separated):
//
//	id:sha256hex:tenant:rps:burst:budget[|tool1,tool2][|@server1,server2]
//
// rps/burst 0 → defaults; budget <0 unlimited. Returns an error listing all
// malformed entries so operators fail fast at deploy time.
func LoadKeys(ctx context.Context, inline, file string) (*Keys, error) {
	k := &Keys{byHash: map[string]KeyRecord{}}
	var errs []string
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			errs = append(errs, fmt.Sprintf("keys file: %v", err))
		} else {
			var recs []KeyRecord
			if err := json.Unmarshal(b, &recs); err != nil {
				errs = append(errs, fmt.Sprintf("keys file parse: %v", err))
			}
			for _, rec := range recs {
				if err := k.add(rec); err != nil {
					errs = append(errs, err.Error())
				}
			}
		}
	}
	for _, entry := range strings.Split(inline, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		rec, err := parseInlineKey(entry)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if err := k.add(rec); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("govern: key config errors: %s", strings.Join(errs, "; "))
	}
	return k, nil
}

func (k *Keys) add(rec KeyRecord) error {
	rec.KeySHA256 = strings.ToLower(rec.KeySHA256)
	if rec.ID == "" || len(rec.KeySHA256) != 64 {
		return fmt.Errorf("key %q: id empty or key_sha256 not 64-hex", rec.ID)
	}
	if rec.Tenant == "" {
		rec.Tenant = "default"
	}
	if _, dup := k.byHash[rec.KeySHA256]; dup {
		return fmt.Errorf("key %q: duplicate key hash", rec.ID)
	}
	k.byHash[rec.KeySHA256] = rec
	return nil
}

// parseInlineKey parses id:sha256:tenant:rps:burst:budget[|tools][|@servers].
func parseInlineKey(s string) (KeyRecord, error) {
	var rec KeyRecord
	parts := strings.Split(s, "|")
	main := strings.Split(parts[0], ":")
	if len(main) < 2 {
		return rec, fmt.Errorf("inline key %q: want id:sha256[:tenant:rps:burst:budget]", parts[0])
	}
	rec.ID = main[0]
	rec.KeySHA256 = main[1]
	if len(main) > 2 {
		rec.Tenant = main[2]
	}
	if len(main) > 3 {
		fmt.Sscanf(main[3], "%g", &rec.RPS)
	}
	if len(main) > 4 {
		fmt.Sscanf(main[4], "%d", &rec.Burst)
	}
	if len(main) > 5 {
		fmt.Sscanf(main[5], "%d", &rec.Budget)
	}
	if len(main) > 6 {
		return rec, fmt.Errorf("inline key %q: too many fields", rec.ID)
	}
	// optional numeric fields default to zero-values handled by callers
	for i, p := range parts[1:] {
		switch {
		case strings.HasPrefix(p, "@"):
			rec.Servers = append(rec.Servers, strings.Split(p[1:], ",")...)
		default:
			if i != 0 {
				return rec, fmt.Errorf("inline key %q: only one tool list allowed", rec.ID)
			}
			rec.Tools = append(rec.Tools, strings.Split(p, ",")...)
		}
	}
	return rec, nil
}
