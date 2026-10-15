package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client resolves an A2A agent card and sends/stream messages to a peer.
type Client struct {
	base string
	hc   *http.Client
	card *Card
}

func NewClient(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 60 * time.Second}}
}

// ResolveCard fetches /.well-known/agent-card.json.
func (c *Client) ResolveCard(ctx context.Context) (*Card, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/.well-known/agent-card.json", nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("a2a: card %d", resp.StatusCode)
	}
	var card Card
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, err
	}
	c.card = &card
	return &card, nil
}

// SendMessage performs message/send and returns the final text.
func (c *Client) SendMessage(ctx context.Context, text string) (string, error) {
	body := rpc("message/send", map[string]any{
		"message": map[string]any{
			"role": "user", "messageId": "c1",
			"parts": []map[string]any{{"type": "text", "text": text}},
		},
	})
	raw, err := c.post(ctx, body)
	if err != nil {
		return "", err
	}
	var resp struct {
		Result struct {
			Messages []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"messages"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &resp)
	var sb strings.Builder
	for _, m := range resp.Result.Messages {
		for _, p := range m.Parts {
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), nil
}

// StreamMessage performs message/stream, invoking onDelta for each streamed
// chunk; returns the full text on completion.
func (c *Client) StreamMessage(ctx context.Context, text string, onDelta func(string)) (string, error) {
	body := rpc("message/stream", map[string]any{
		"message": map[string]any{
			"role": "user", "messageId": "c1",
			"parts": []map[string]any{{"type": "text", "text": text}},
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &ev); err != nil {
			continue
		}
		if d, ok := ev["delta"].(string); ok && onDelta != nil {
			onDelta(d)
		}
		if t, ok := ev["text"].(string); ok {
			full.WriteString(t)
		}
	}
	return full.String(), sc.Err()
}

func (c *Client) post(ctx context.Context, body []byte) (json.RawMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("a2a: upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rb, &rpc); err != nil {
		return nil, err
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("a2a: %s", rpc.Error.Message)
	}
	return rpc.Result, nil
}

func rpc(method string, params any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": method, "params": params})
	return b
}
