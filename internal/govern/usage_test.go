package govern

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestUsageLedger_JoinAndMask pins the /api/usage contract: rows join
// provisioned keys with live counters, unlimited keys omit remaining/pct,
// and no SHA-256 hash ever leaves the process.
func TestUsageLedger_JoinAndMask(t *testing.T) {
	hash := HashKey("sk-plain-never-shown")
	keys, err := LoadKeys(t.Context(), "team-a:"+hash+":tenant-a:5:10:1000", "")
	if err != nil {
		t.Fatal(err)
	}
	// a second, unlimited key
	hash2 := HashKey("sk-free")
	if err := keys.add(KeyRecord{ID: "free", KeySHA256: hash2, Tenant: "t2", Budget: -1}); err != nil {
		t.Fatal(err)
	}
	budget := NewMemBudget()
	_ = budget.Charge(t.Context(), "team-a", 700)

	led := NewUsageLedger(keys, budget)
	rec := httptest.NewRecorder()
	led.ServeHTTP(rec, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Entries []UsageEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %v", len(out.Entries), out.Entries)
	}
	byID := map[string]UsageEntry{}
	for _, e := range out.Entries {
		byID[e.KeyID] = e
	}
	a := byID["team-a"]
	if a.Used != 700 || a.Remaining == nil || *a.Remaining != 300 {
		t.Fatalf("team-a entry wrong: %+v", a)
	}
	if a.PctUsed == nil || *a.PctUsed < 69.9 || *a.PctUsed > 70.1 {
		t.Fatalf("team-a pct_used = %v, want ≈70", a.PctUsed)
	}
	f := byID["free"]
	if f.Budget != -1 || f.Remaining != nil || f.PctUsed != nil {
		t.Fatalf("unlimited key must omit remaining/pct: %+v", f)
	}
	// masking: the plain key and its hash must not appear anywhere
	body := rec.Body.String()
	if contains(body, "sk-plain-never-shown") || contains(body, hash) {
		t.Fatal("ledger leaked key material")
	}
	// sorted by usage desc
	if out.Entries[0].KeyID != "team-a" {
		t.Fatalf("entries must sort by usage desc, got %s first", out.Entries[0].KeyID)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
