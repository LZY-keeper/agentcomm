// Package notify delivers approval requests to the humans who must decide
// them. The default Webhook sink posts a signed JSON card to an outbound
// endpoint — 钉钉/企业微信/飞书 group bots and generic operator webhooks all
// speak one of the supported shapes — so an input-required task pings the
// on-call channel instead of dying silently until the approval timeout.
//
// Safety invariants (mirror the audit chain):
//   - Only the pre-masked ArgsSummary (and never raw call args) leaves the
//     process: a webhook endpoint must not become a secret exfiltration path.
//   - Notification is fire-and-forget from the runner's perspective: delivery
//     failures are logged and dropped, never queued, and can never block or
//     fail the approval flow itself.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/logx"
	"github.com/LZY-keeper/agentcomm/internal/store"
)

// Sink is the outbound delivery abstraction. The approval hook fires it
// asynchronously; failure semantics are the implementation's problem.
type Sink interface {
	NotifyApproval(ctx context.Context, req store.ApprovalRequest)
}

// Config bounds webhook delivery (env-driven, see config.NotifyConfig).
type Config struct {
	URL         string        // empty → disabled
	Format      string        // generic|dingtalk|wecom|feishu (card template)
	Timeout     time.Duration // per-attempt HTTP timeout
	Retries     int           // attempts beyond the first (exponential backoff)
	HMACSecret  string        // optional: X-AgentComm-Signature: sha256=<hex>
	DecisionURL string        // optional base URL shown in cards for the approve/deny API
}

// Webhook is the default Sink: an outbound HTTP POST of a JSON card.
type Webhook struct {
	cfg    Config
	client *http.Client
}

// NewWebhook builds a webhook sink. Returns nil when cfg.URL is empty, so
// wiring can be unconditional (disabled by default, friction-free offline).
func NewWebhook(cfg Config) *Webhook {
	if cfg.URL == "" {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Format == "" {
		cfg.Format = "generic"
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	return &Webhook{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

// Enabled reports whether a sink is configured (used for startup logging).
func (w *Webhook) Enabled() bool { return w != nil && w.cfg.URL != "" }

// NotifyApproval renders the card and delivers it with bounded retries in a
// detached goroutine. Never blocks the caller beyond spawn cost, never
// returns an error — the approval flow must not depend on delivery.
func (w *Webhook) NotifyApproval(_ context.Context, req store.ApprovalRequest) {
	if w == nil {
		return
	}
	body, err := json.Marshal(w.card(req))
	if err != nil {
		return
	}
	go func() {
		// Detached from the task context: a canceled task's approval notice
		// may still deserve delivery (the record persists). Bounded lifetime.
		ctx, cancel := context.WithTimeout(context.Background(), w.totalTimeout())
		defer cancel()
		var lastErr error
		for attempt := 0; attempt <= w.cfg.Retries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff(attempt)):
				}
			}
			if lastErr = w.post(ctx, body); lastErr == nil {
				logx.Info(ctx, "approval webhook delivered", "task", req.TaskID, "tool", req.Tool, "attempt", attempt+1)
				return
			}
		}
		// All attempts failed: log and drop (no queue — see package doc).
		logx.Warn(ctx, "approval webhook delivery failed (dropped)", "task", req.TaskID,
			"attempts", w.cfg.Retries+1, "err", lastErr)
	}()
}

// totalTimeout bounds the whole retry sequence: per-attempt timeout × attempts
// plus backoff headroom.
func (w *Webhook) totalTimeout() time.Duration {
	return w.cfg.Timeout*time.Duration(w.cfg.Retries+1) + 30*time.Second
}

// post performs one delivery attempt, attaching an HMAC signature header so
// the receiver can authenticate the caller (omitted without a secret).
func (w *Webhook) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AgentComm-Event", "approval.requested")
	if w.cfg.HMACSecret != "" {
		req.Header.Set("X-AgentComm-Signature", "sha256="+w.sign(body))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook endpoint %d", resp.StatusCode)
	}
	return nil
}

// sign returns hex HMAC-SHA256 of body under the configured secret.
func (w *Webhook) sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(w.cfg.HMACSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// backoff is exponential: 1s, 2s, 4s… capped at 10s.
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

// card renders the payload per configured format. All formats carry the same
// masked fields (task/tool/summary); the variants exist so 钉钉/企微/飞书 group
// bots render an actionable card without a translating proxy.
func (w *Webhook) card(req store.ApprovalRequest) any {
	base := map[string]any{
		"event":        "approval.requested",
		"task_id":      req.TaskID,
		"approval_id":  req.ID,
		"tool":         req.Tool,
		"server_id":    req.ServerID,
		"args_summary": req.ArgsSummary, // pre-masked by the approval hook
		"created_at":   req.CreatedAt.UTC().Format(time.RFC3339),
	}
	if w.cfg.DecisionURL != "" {
		base["decision_url"] = strings.TrimRight(w.cfg.DecisionURL, "/") +
			"/api/tasks/" + req.TaskID + "/approval"
	}
	switch w.cfg.Format {
	case "dingtalk":
		text := "### AgentComm 审批请求\n\n" +
			"- **任务**: " + req.TaskID + "\n" +
			"- **工具**: " + req.Tool + "\n" +
			"- **参数摘要**(已脱敏): `" + argsSummaryOrNone(req) + "`\n" +
			"- **时间**: " + req.CreatedAt.Format("2006-01-02 15:04:05") + "\n"
		if w.cfg.DecisionURL != "" {
			text += "\n裁决接口: `POST " + strings.TrimRight(w.cfg.DecisionURL, "/") +
				"/api/tasks/" + req.TaskID + "/approval`\n"
		}
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": "AgentComm 审批请求",
				"text":  text,
			},
			"at": map[string]any{"isAtAll": false},
		}
	case "wecom":
		content := "AgentComm 审批请求\n" +
			"工具: " + req.Tool + " 等待审批\n" +
			"参数(已脱敏): " + argsSummaryOrNone(req) + "\n" +
			"任务: " + req.TaskID
		if w.cfg.DecisionURL != "" {
			content += "\n裁决: POST " + strings.TrimRight(w.cfg.DecisionURL, "/") +
				"/api/tasks/" + req.TaskID + "/approval"
		}
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": content,
			},
		}
	case "feishu":
		text := "【AgentComm 审批请求】工具 " + req.Tool + " 等待审批；参数(已脱敏): " +
			argsSummaryOrNone(req) + "；任务 " + req.TaskID
		return map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		}
	default:
		return base
	}
}

func argsSummaryOrNone(req store.ApprovalRequest) string {
	if req.ArgsSummary == "" {
		return "(无参数)"
	}
	return req.ArgsSummary
}
