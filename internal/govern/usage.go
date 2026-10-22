// usage.go serves GET /api/usage — the read-only spend ledger (v3.2). The
// governance layer intercepts (429/402) but until now showed no numbers: a
// platform team doing budget review needs "who spent what" without scraping
// Prometheus counters. One endpoint, no new dependencies.
//
// Access model mirrors the audit chain: key id and tenant are operator-facing
// identifiers; SHA-256 hashes are never emitted. Anonymous traffic (governance
// disabled) gets an empty ledger rather than an error — the endpoint exists
// as soon as the gateway runs.
package govern

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
)

// UsageLedger joins provisioned keys with live counters. Both inputs are
// optional: keys==nil → anonymous deployments see counters (if any); when
// both are empty the ledger is empty.
type UsageLedger struct {
	mu         sync.Mutex
	keys       *Keys
	snapshotFn func() map[string]int64
}

// NewUsageLedger wires the ledger. budget may be nil when governance is
// disabled; SetKeys attaches provisioned key records once they load.
func NewUsageLedger(keys *Keys, budget *MemBudget) *UsageLedger {
	var fn func() map[string]int64
	if budget != nil {
		fn = budget.Snapshot
	}
	return &UsageLedger{keys: keys, snapshotFn: fn}
}

// SetKeys attaches the provisioned key index after LoadKeys succeeds.
func (l *UsageLedger) SetKeys(k *Keys) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = k
}

// UsageEntry is one key's row in the ledger. Budget <0 = unlimited; the
// remaining field is omitted for unlimited keys.
type UsageEntry struct {
	KeyID     string   `json:"key_id"`
	Tenant    string   `json:"tenant"`
	Used      int64    `json:"used_tokens"`
	Budget    int64    `json:"budget_tokens"`
	Remaining *int64   `json:"remaining_tokens,omitempty"` // nil = unlimited
	PctUsed   *float64 `json:"pct_used,omitempty"`         // omitted when unlimited
}

func (l *UsageLedger) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	l.mu.Lock()
	fn := l.snapshotFn
	keys := l.keys
	l.mu.Unlock()

	usedBy := map[string]int64{}
	if fn != nil {
		usedBy = fn()
	}
	entries := []UsageEntry{}
	if keys != nil {
		for _, rec := range keys.Records() {
			e := UsageEntry{
				KeyID:  rec.ID,
				Tenant: rec.Tenant,
				Used:   usedBy[rec.ID],
				Budget: rec.Budget,
			}
			if rec.Budget < 0 {
				e.Remaining = nil // unlimited: remaining/pct omitted
			} else {
				limit := rec.Budget
				rem := limit - e.Used
				if rem < 0 {
					rem = 0
				}
				e.Remaining = &rem
				if limit > 0 {
					p := float64(e.Used) / float64(limit) * 100
					e.PctUsed = &p
				}
			}
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Used > entries[j].Used })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}
