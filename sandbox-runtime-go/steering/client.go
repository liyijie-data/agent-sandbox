package steering

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/engine"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL, ExecutionID, Token string
	HTTP                        *http.Client
	Inject                      func(context.Context, contracts.Message) ([]engine.Message, error)
}

func (c *Client) BeforeModel(ctx context.Context, s *engine.State) error {
	if c == nil {
		return nil
	}
	cl := c.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}
	if c.HTTP == nil {
		cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	b, _ := json.Marshal(map[string]any{"execution_id": c.ExecutionID, "after_seq": s.Cursor})
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+"/steers/pull", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	r, e := cl.Do(req)
	if e != nil {
		return fmt.Errorf("steering_delivery_failed: %w", e)
	}
	defer r.Body.Close()
	if r.StatusCode == 409 {
		return fmt.Errorf("steering_cursor_conflict")
	}
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("steering_delivery_failed")
	}
	var p contracts.SteerPullResponse
	if e = json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&p); e != nil {
		return fmt.Errorf("steering_delivery_failed: %w", e)
	}
	expect := s.Cursor + 1
	if len(p.Items) == 0 && p.ThroughSeq != s.Cursor {
		return fmt.Errorf("steering_cursor_conflict")
	}
	if len(p.Items) > 0 && (p.BatchID == nil || p.ThroughSeq != p.Items[len(p.Items)-1].Seq) {
		return fmt.Errorf("steering_cursor_conflict")
	}
	for _, it := range p.Items {
		if it.Seq != expect {
			return fmt.Errorf("steering_cursor_conflict")
		}
		expect++
	}
	if c.Inject != nil {
		for _, it := range p.Items {
			content := it.Message.Content
			if content != nil {
				if _, e := c.Inject(ctx, it.Message); e != nil {
					return e
				}
			}
		}
	}
	expect = s.Cursor + 1
	for _, it := range p.Items {
		if it.Seq != expect {
			return fmt.Errorf("steering_cursor_conflict")
		}
		expect++
		content := ""
		if it.Message.Content != nil {
			content = *it.Message.Content
		}
		s.Messages = append(s.Messages, engine.Message{Role: it.Message.Role, Content: content})
	}
	if p.BatchID == nil {
		return nil
	}
	for i := 0; i < 3; i++ {
		a, _ := json.Marshal(contracts.SteerAckRequest{ExecutionID: c.ExecutionID, BatchID: *p.BatchID, IncorporatedThroughSeq: p.ThroughSeq})
		q, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+"/steers/ack", bytes.NewReader(a))
		q.Header.Set("Content-Type", "application/json")
		if c.Token != "" {
			q.Header.Set("Authorization", "Bearer "+c.Token)
		}
		x, e := cl.Do(q)
		if e == nil {
			var ar contracts.SteerAckResponse
			_ = json.NewDecoder(io.LimitReader(x.Body, 4<<20)).Decode(&ar)
			x.Body.Close()
			if x.StatusCode == 409 {
				return fmt.Errorf("steering_cursor_conflict")
			}
			if x.StatusCode/100 == 2 && ar.IncorporatedThroughSeq == p.ThroughSeq {
				s.Cursor = p.ThroughSeq
				return nil
			}
		}
		if ctx.Err() != nil {
			return fmt.Errorf("steering_delivery_failed: %w", ctx.Err())
		}
		t := time.NewTimer(time.Duration(i+1) * 50 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("steering_delivery_failed: %w", ctx.Err())
		case <-t.C:
		}
	}
	return fmt.Errorf("steering_delivery_failed")
}
