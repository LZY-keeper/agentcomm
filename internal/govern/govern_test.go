package govern

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testKeyPlain = "sk-test-1234567890abcdef"
	// sha256("sk-test-1234567890abcdef")
	testKeyHash   = "93faf5716142f90256b4c1c4bc2c1bc2ee0a2460d5a738d6938dda1ad4216afd"
	testKeyInline = "k1:" + testKeyHash + ":tenant-a:2:5:1000"
)

func testKeys(t *testing.T) *Keys {
	t.Helper()
	k, err := LoadKeys(context.Background(), testKeyInline, "")
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}
	return k
}

func protectedHandler(hits *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if _, ok := From(r.Context()); ok {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("authorized"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("anonymous"))
	})
}

func TestAuth_AcceptsValidKey(t *testing.T) {
	keys := testKeys(t)
	var hits int
	h := Chain(protectedHandler(&hits),
		Auth(keys, WithLoopbackAllowed(false)))
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil).
		WithContext(context.Background())
	req.RemoteAddr = "10.0.0.9:5555" // non-loopback: must present a key
	req.Header.Set("Authorization", "Bearer "+testKeyPlain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "authorized" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAuth_RejectsMissingAndBadKeys(t *testing.T) {
	keys := testKeys(t)
	var hits int
	h := Chain(protectedHandler(&hits), Auth(keys, WithLoopbackAllowed(false)))
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	req.RemoteAddr = "10.0.0.9:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: code=%d", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	req2.RemoteAddr = "10.0.0.9:5555"
	req2.Header.Set("X-API-Key", "sk-wrong")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: code=%d", rec2.Code)
	}
}

func TestAuth_LoopbackExempt(t *testing.T) {
	keys := testKeys(t)
	var hits int
	h := Chain(protectedHandler(&hits), Auth(keys, WithLoopbackAllowed(true)))
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	req.RemoteAddr = "127.0.0.1:4444" // loopback: exempt
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "anonymous" {
		t.Fatalf("loopback should pass without key: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestAuth_ExemptPrefixesBypass(t *testing.T) {
	keys := testKeys(t)
	var hits int
	h := Chain(protectedHandler(&hits), Auth(keys, WithLoopbackAllowed(false)))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "10.0.0.9:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("exempt prefix: code=%d", rec.Code)
	}
}

func TestAuth_PrefixBoundary(t *testing.T) {
	keys := testKeys(t)
	var hits int
	h := Chain(protectedHandler(&hits), Auth(keys, WithLoopbackAllowed(false)))
	// "/apiv2-evil" must NOT be treated as protected "/api" (still reachable)
	req := httptest.NewRequest(http.MethodGet, "/apiv2-evil", nil)
	req.RemoteAddr = "10.0.0.9:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-protected path must pass: %d", rec.Code)
	}
}

// fakeLimiter allows N requests then rejects with retry-after.
type fakeLimiter struct{ allow bool }

func (f fakeLimiter) Allow(string) (bool, time.Duration) { return f.allow, 3 * time.Second }

func TestRateLimit_429WithRetryAfter(t *testing.T) {
	var hits int
	h := Chain(protectedHandler(&hits), RateLimit(fakeLimiter{allow: false}))
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestBudget_402WhenExhausted(t *testing.T) {
	bs := NewMemBudget()
	_ = bs.Charge(context.Background(), "k1", 5000)
	h := Chain(protectedHandler(new(int)),
		Auth(testKeys(t), WithLoopbackAllowed(false)),
		Budget(bs, 1000, 0.9))
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	req.RemoteAddr = "10.0.0.9:5555"
	req.Header.Set("Authorization", "Bearer "+testKeyPlain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("code=%d want 402", rec.Code)
	}
}

func TestMaskSecrets(t *testing.T) {
	in := `{"path":"/x","password":"hunter2","api_key":"sk-abc","note":"keep"}`
	out := MaskSecrets(in)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "sk-abc") {
		t.Fatalf("secrets leaked: %s", out)
	}
	if !strings.Contains(out, "keep") {
		t.Fatalf("non-secret values must survive: %s", out)
	}
}

func TestParseInlineKey(t *testing.T) {
	rec, err := parseInlineKey("ops:"+testKeyHash+":ops-tenant:10:20:-1|read_file,get_weather|@builtin")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Tenant != "ops-tenant" || rec.Budget != -1 || len(rec.Tools) != 2 || len(rec.Servers) != 1 {
		t.Fatalf("parsed=%+v", rec)
	}
	if !rec.AllowsTool("builtin", "read_file") || rec.AllowsTool("builtin", "write_file") {
		t.Fatalf("tool filter broken: %+v", rec)
	}
}
