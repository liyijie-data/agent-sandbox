package events

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/engine"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

type Client struct {
	BaseURL, Token, ExecutionID string
	http                        *http.Client
	q                           chan engine.Event
	done                        chan struct{}
	exited                      chan struct{}
	once                        sync.Once
	mu                          sync.Mutex
	seq                         int64
	ctx                         context.Context
	cancel                      context.CancelFunc
	closed                      bool
	Sanitizer                   func(any) any
}

func New(base, token, id string) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{BaseURL: base, Token: token, ExecutionID: id, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, q: make(chan engine.Event, 256), done: make(chan struct{}), exited: make(chan struct{}), ctx: ctx, cancel: cancel}
	go c.run()
	return c
}
func (c *Client) run() {
	defer close(c.exited)
	for {
		select {
		case e, ok := <-c.q:
			if !ok {
				return
			}
			c.send(e)
		case <-c.done:
			return
		}
	}
}
func (c *Client) Emit(ctx context.Context, e engine.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return context.Canceled
	}
	select {
	case c.q <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func (c *Client) send(e engine.Event) {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()
	var p []byte
	if c.Sanitizer != nil {
		e.Data = c.Sanitizer(e.Data)
	}
	if e.Type == "agent.tool_call" || e.Type == "agent.tool_result" {
		p, _ = json.Marshal(toolPayload(e.Data))
	} else {
		p, _ = json.Marshal(e.Data)
	}
	body, _ := json.Marshal(contracts.RuntimeEventEntry{ExecutionID: c.ExecutionID, SourceSeq: seq, Type: contracts.EventType(e.Type), Payload: p})
	if _, v := contracts.DecodeRuntimeEventEntry(body); v != nil {
		return
	}
	if len(body) > 128<<10 {
		return
	}
	req, er := http.NewRequestWithContext(c.ctx, "POST", c.BaseURL+"/events", bytes.NewReader(body))
	if er != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	r, er := c.http.Do(req)
	if er == nil {
		r.Body.Close()
	}
}

func toolPayload(data any) contracts.AgentToolPayload {
	b, _ := json.Marshal(data)
	var x struct {
		ToolCallID       string          `json:"tool_call_id"`
		ToolName         string          `json:"tool_name"`
		Status           string          `json:"status"`
		ErrorType        string          `json:"error_type"`
		ToolID           string          `json:"tool_id"`
		Operation        string          `json:"operation"`
		ModelToolName    string          `json:"model_tool_name"`
		Arguments        json.RawMessage `json:"arguments"`
		Result           any             `json:"result"`
		DurationMs       *int64          `json:"duration_ms"`
		DetailsTruncated bool            `json:"details_truncated"`
	}
	_ = json.Unmarshal(b, &x)
	p := contracts.AgentToolPayload{ToolCallID: x.ToolCallID, ToolName: x.ToolName, Status: x.Status, ErrorType: x.ErrorType, ToolID: x.ToolID, Operation: x.Operation, ModelToolName: x.ModelToolName, DurationMs: x.DurationMs, DetailsTruncated: x.DetailsTruncated}
	if len(x.Arguments) <= 16<<10 {
		p.Arguments = x.Arguments
	} else {
		p.Arguments = previewJSON(x.Arguments)
		p.DetailsTruncated = true
	}
	if x.Result != nil {
		var r []byte
		if s, ok := x.Result.(string); ok {
			r, _ = json.Marshal(s)
		} else {
			r, _ = json.Marshal(x.Result)
		}
		if len(r) <= 16<<10 {
			p.Result = r
		} else {
			p.Result = previewJSON(r)
			p.DetailsTruncated = true
		}
	}
	return p
}

func previewJSON(raw []byte) json.RawMessage {
	const max = 16 << 10
	lo, hi := 0, len(raw)
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		candidate := mid
		for candidate > 0 && candidate < len(raw) && !utf8.RuneStart(raw[candidate]) {
			candidate--
		}
		b, _ := json.Marshal(map[string]any{"preview": string(raw[:candidate]), "truncated": true})
		if len(b) <= max {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && lo < len(raw) && !utf8.RuneStart(raw[lo]) {
		lo--
	}
	b, _ := json.Marshal(map[string]any{"preview": string(raw[:lo]), "truncated": true})
	if len(b) <= max {
		return b
	}
	return json.RawMessage(`{"preview":"","truncated":true}`)
}
func (c *Client) Close() {
	c.once.Do(func() {
		defer c.cancel()
		c.mu.Lock()
		c.closed = true
		close(c.q)
		c.mu.Unlock()
		select {
		case <-c.exited:
		case <-time.After(time.Second):
			c.cancel()
			<-c.exited
		}
	})
}

var _ engine.EventSink = (*Client)(nil)
