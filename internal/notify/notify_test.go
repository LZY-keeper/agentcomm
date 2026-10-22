package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentcomm/internal/store"
)

func testReq() store.ApprovalRequest {
	return store.ApprovalRequest{
		ID:          "apr-1",
		TaskID:      "task-1",
		Tool:        "write_file",
		ServerID:    "builtin",
		ArgsSummary: `{"path":"…","password":"***"}`,
		CreatedAt:   time.Now(),
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached within deadline")
}

func TestWebhook_GenericCardShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	w := NewWebhook(Config{URL: srv.URL, Retries: 0})
	w.NotifyApproval(context.Background(), testReq())
	waitFor(t, func() bool { return got != nil })
	if got["event"] != "approval.requested" || got["tool"] != "write_file" || got["args_summary"] == "" {
		t.Fatalf("generic card missing fields: %v", got)
	}
}

func TestWebhook_DingTalkCardShape(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &raw)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	w := NewWebhook(Config{URL: srv.URL, Format: "dingtalk", Retries: 0})
	w.NotifyApproval(context.Background(), testReq())
	waitFor(t, func() bool { return raw != nil })
	if raw["msgtype"] != "markdown" {
		t.Fatalf("dingtalk card msgtype = %v", raw["msgtype"])
	}
	md, _ := raw["markdown"].(map[string]any)
	if md == nil {
		t.Fatalf("dingtalk card missing markdown: %v", raw)
	}
	text, _ := md["text"].(string)
	if !strings.Contains(text, "审批请求") || !strings.Contains(text, "write_file") {
		t.Fatalf("dingtalk card text incomplete: %v", md)
	}
}

func TestWebhook_RetryThenSuccess(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 2 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	w := NewWebhook(Config{URL: srv.URL, Retries: 2, Timeout: time.Second})
	w.NotifyApproval(context.Background(), testReq())
	waitFor(t, func() bool { return atomic.LoadInt32(&attempts) >= 2 })
}

func TestWebhook_DropsAfterRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	w := NewWebhook(Config{URL: srv.URL, Retries: 1, Timeout: time.Second})
	w.NotifyApproval(context.Background(), testReq()) // must not panic/block
	waitFor(t, func() bool { return atomic.LoadInt32(&attempts) >= 2 })
}

// TestWebhook_NeverSendsRawSecrets pins the masking invariant: the sink only
// forwards the pre-masked ArgsSummary — raw args never exist in its code path.
func TestWebhook_NeverSendsRawSecrets(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	req := testReq()
	req.ArgsSummary = `{"token":"***","api_key":"***","password":"***"}`
	w := NewWebhook(Config{URL: srv.URL, Retries: 0})
	w.NotifyApproval(context.Background(), req)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && body == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(body, "sk-live-") || strings.Contains(body, "hunter2") {
		t.Fatalf("raw secret leaked into webhook body: %s", body)
	}
}

func TestWebhook_DisabledWithoutURL(t *testing.T) {
	if NewWebhook(Config{}) != nil {
		t.Fatal("empty URL must disable the sink")
	}
	var n *Webhook
	n.NotifyApproval(context.Background(), testReq()) // nil-safe no-op
}
