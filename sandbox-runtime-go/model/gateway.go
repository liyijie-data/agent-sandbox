package model

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/api"
	"agent-platform/sandbox-runtime-go/engine"
	"agent-platform/sandbox-runtime-go/remote"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

const maxSSELine = 1 << 20

type GatewayModel struct {
	BaseURL, Token, Model string
	MaxOutputTokens       int
	Parameters            map[string]json.RawMessage
	Client                *http.Client
	Tools                 []api.ToolDefinition

	ToolDefinitions func() []api.ToolDefinition
	OnDelta         func(api.ModelDelta) error
	OnRequest       func(json.RawMessage) error
	OnResponse      func(json.RawMessage) error
}
type Error struct {
	Code    string
	Status  int
	Message string
}

type ModelRequestError struct{ Err error }

func (e *ModelRequestError) Error() string { return e.Err.Error() }
func (e *ModelRequestError) Unwrap() error { return e.Err }

func (m *GatewayModel) CurrentTools() []api.ToolDefinition {
	if m.ToolDefinitions != nil {
		return m.ToolDefinitions()
	}
	return append([]api.ToolDefinition(nil), m.Tools...)
}

func (e *Error) Error() string { return e.Code }
func ContextLengthExceeded(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == "context_length_exceeded" || e.Code == "context_window_exceeded")
}

func ErrorDetails(err error, phase string) *contracts.RuntimeErrorDetails {
	var requestErr *ModelRequestError
	if !errors.As(err, &requestErr) {
		return nil
	}
	d := &contracts.RuntimeErrorDetails{Phase: phase, ReasonCode: "unknown"}
	if phase == "" {
		d.Phase = "model"
	}
	var e *Error
	if errors.As(err, &e) {
		d.UpstreamStatus = e.Status
		switch {
		case e.Code == "context_length_exceeded" || e.Code == "context_window_exceeded":
			d.ReasonCode = "context_limit_exceeded"
		case e.Status == 401 || e.Status == 403:
			d.ReasonCode = "upstream_auth_failed"
		case e.Status == 429:
			d.ReasonCode = "upstream_rate_limited"
		case e.Status >= 500:
			d.ReasonCode = "upstream_unavailable"
		case e.Status == 400 && missingReasoning(e.Message):
			d.ReasonCode = "invalid_reasoning_request"
		case e.Status == 400:
			d.ReasonCode = "upstream_invalid_request"
		}
		return contracts.NormalizeRuntimeErrorDetails(d)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		d.ReasonCode = "model_timeout"
		return contracts.NormalizeRuntimeErrorDetails(d)
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			d.ReasonCode = "model_timeout"
		} else {
			d.ReasonCode = "model_transport_error"
		}
	}
	return contracts.NormalizeRuntimeErrorDetails(d)
}

func missingReasoning(message string) bool {
	s := strings.ToLower(message)
	return strings.Contains(s, "reasoning_content") && (strings.Contains(s, "missing") || strings.Contains(s, "required") || strings.Contains(s, "must be returned") || strings.Contains(s, "must be passed back") || strings.Contains(s, "缺失") || strings.Contains(s, "必需"))
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}
type wireToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}
type wireToolDef struct {
	Type     string             `json:"type"`
	Function api.ToolDefinition `json:"function"`
}

func (m *GatewayModel) Complete(ctx context.Context, r engine.ModelRequest) (engine.ModelResponse, error) {
	if e := contracts.ValidateModelParameters(m.Parameters, nil, nil, nil); e != nil {
		return engine.ModelResponse{}, &ModelRequestError{Err: e}
	}
	msgs := make([]wireMessage, 0, len(r.Messages))
	for _, x := range r.Messages {
		w := wireMessage{Role: x.Role, Content: x.Content, ToolCallID: x.ToolCallID}
		for _, tc := range x.ToolCalls {

			name := m.compatibleToolName(tc.Name)
			w.ToolCalls = append(w.ToolCalls, wireToolCall{ID: tc.ID, Type: "function", Function: struct {
				Name      string `json:"name,omitempty"`
				Arguments string `json:"arguments,omitempty"`
			}{name, string(tc.Arguments)}})
		}
		msgs = append(msgs, w)
	}
	p := map[string]any{"model": m.Model, "messages": msgs, "stream": true}
	for k, raw := range m.Parameters {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return engine.ModelResponse{}, fmt.Errorf("invalid model parameter %s: %w", k, err)
		}
		p[k] = v
	}

	if m.MaxOutputTokens > 0 {
		if _, ok := m.Parameters["max_tokens"]; !ok {
			if _, ok := m.Parameters["max_completion_tokens"]; !ok {
				p["max_tokens"] = m.MaxOutputTokens
			}
		}
	}
	tools := m.Tools
	if m.ToolDefinitions != nil {
		tools = m.ToolDefinitions()
	}
	if len(tools) > 0 {
		d := make([]wireToolDef, len(tools))
		for i, t := range tools {
			d[i] = wireToolDef{Type: "function", Function: t}
		}
		p["tools"] = d
	}
	if r.ReasoningEffort != "" {
		if _, supplied := m.Parameters["reasoning_effort"]; !supplied {
			p["reasoning_effort"] = r.ReasoningEffort
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return engine.ModelResponse{}, fmt.Errorf("marshal model request: %w", err)
	}
	if m.OnRequest != nil {
		_ = m.OnRequest(json.RawMessage(b))
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if e != nil {
		return engine.ModelResponse{}, &ModelRequestError{Err: e}
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := m.client().Do(req)
	if e != nil {
		if m.OnResponse != nil {
			b, _ := json.Marshal(map[string]any{"choices": []any{}, "incomplete": true, "error_code": "model_gateway_transport_error"})
			_ = m.OnResponse(b)
		}
		return engine.ModelResponse{}, &ModelRequestError{Err: e}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, api.MaxBodyBytes))
		var x struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &x)
		code := "model_gateway_error"
		if x.Error.Code == "context_length_exceeded" || x.Error.Code == "context_window_exceeded" {
			code = "context_length_exceeded"
		}
		if m.OnResponse != nil {

			failure := map[string]any{"choices": []any{}, "incomplete": true, "error_code": code}
			if len(raw) > 0 {
				if json.Valid(raw) {
					failure["error"] = json.RawMessage(raw)
				} else {
					failure["error"] = string(raw)
				}
			}
			b, _ := json.Marshal(failure)
			_ = m.OnResponse(b)
		}
		return engine.ModelResponse{}, &ModelRequestError{Err: &Error{code, resp.StatusCode, x.Error.Message}}
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		v, e := m.readJSON(resp.Body)
		if e == nil && m.OnResponse != nil {
			_ = m.OnResponse(v.Raw)
		}
		if e != nil {
			return v, &ModelRequestError{Err: e}
		}
		return v, nil
	}
	v, e := m.readSSE(resp.Body)
	if e != nil {
		return v, &ModelRequestError{Err: e}
	}
	return v, nil
}

func (m *GatewayModel) compatibleToolName(name string) string {
	alias := remote.ModelToolName(name)
	if alias == name {
		return name
	}
	for _, d := range m.CurrentTools() {
		if d.Name == alias {
			return alias
		}
	}
	return name
}
func (m *GatewayModel) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type cappedReader struct {
	r      io.Reader
	n, cap int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.n > c.cap {
		return 0, fmt.Errorf("model response too large")
	}
	if int64(len(p)) > c.cap-c.n+1 {
		p = p[:c.cap-c.n+1]
	}
	n, e := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.cap {
		return n, fmt.Errorf("model response too large")
	}
	return n, e
}
func (m *GatewayModel) readSSE(body io.Reader) (result engine.ModelResponse, resultErr error) {
	s := bufio.NewScanner(&cappedReader{r: body, cap: api.MaxBodyBytes})
	s.Buffer(make([]byte, 4096), maxSSELine)
	var text strings.Builder
	calls := map[int]*engine.ToolCall{}
	var reasoning strings.Builder
	done, finished := false, false
	finishReason := ""
	role := ""
	var responseID, responseModel string
	var usage json.RawMessage
	defer func() {
		if m.OnResponse == nil {
			return
		}
		message := map[string]any{}
		if role != "" {
			message["role"] = role
		}
		if text.Len() > 0 {
			message["content"] = text.String()
		}
		choice := map[string]any{"message": message}
		if finishReason != "" {
			choice["finish_reason"] = finishReason
		}
		payload := map[string]any{"choices": []any{choice}}
		if reasoning.Len() > 0 {
			message["reasoning_content"] = reasoning.String()
		}
		if len(calls) > 0 {
			toolCalls := make([]any, 0, len(calls))
			for i := 0; i < len(calls); i++ {
				if c := calls[i]; c != nil {
					toolCalls = append(toolCalls, map[string]any{"id": c.ID, "type": "function", "function": map[string]string{"name": c.Name, "arguments": string(c.Arguments)}})
				}
			}
			message["tool_calls"] = toolCalls
		}
		if responseID != "" {
			payload["id"] = responseID
		}
		if responseModel != "" {
			payload["model"] = responseModel
		}
		if len(usage) > 0 {
			payload["usage"] = json.RawMessage(usage)
		}
		if resultErr != nil {
			payload["incomplete"] = true
			payload["error_code"] = streamErrorCode(resultErr)
		}
		b, _ := json.Marshal(payload)
		_ = m.OnResponse(b)
	}()
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "data:") {
			d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if d == "[DONE]" {
				done = true
				break
			}
			var x struct {
				ID      string          `json:"id"`
				Model   string          `json:"model"`
				Usage   json.RawMessage `json:"usage"`
				Choices []struct {
					Delta struct {
						Role      string `json:"role"`
						Content   string `json:"content"`
						Reasoning string `json:"reasoning_content"`
						ToolCalls []struct {
							Index    int                              `json:"index"`
							ID       string                           `json:"id"`
							Function struct{ Name, Arguments string } `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					Finish *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(d), &x) != nil {
				return engine.ModelResponse{}, fmt.Errorf("invalid model stream event")
			}
			if x.ID != "" {
				responseID = x.ID
			}
			if x.Model != "" {
				responseModel = x.Model
			}
			if len(x.Usage) > 0 && string(x.Usage) != "null" {
				usage = append(usage[:0], x.Usage...)
			}
			if len(x.Choices) == 0 {
				continue
			}
			c := x.Choices[0]
			if c.Delta.Role != "" {
				role = c.Delta.Role
			}
			text.WriteString(c.Delta.Content)
			if m.OnDelta != nil && c.Delta.Content != "" {
				_ = m.OnDelta(api.ModelDelta{Kind: "content", Text: c.Delta.Content})
			}
			reasoning.WriteString(c.Delta.Reasoning)
			if m.OnDelta != nil && c.Delta.Reasoning != "" {
				_ = m.OnDelta(api.ModelDelta{Kind: "reasoning", Text: c.Delta.Reasoning})
			}
			for _, tc := range c.Delta.ToolCalls {
				if tc.Index < 0 || tc.Index > 128 {
					return engine.ModelResponse{}, fmt.Errorf("invalid tool call index")
				}
				v := calls[tc.Index]
				if v == nil {
					v = &engine.ToolCall{ID: tc.ID}
					calls[tc.Index] = v
				}
				if v.ID == "" {
					v.ID = tc.ID
				}
				v.Name += tc.Function.Name
				v.Arguments = append(v.Arguments, tc.Function.Arguments...)
			}
			if c.Finish != nil {
				finished = true
				finishReason = *c.Finish
			}
		}
	}
	if s.Err() != nil {
		return engine.ModelResponse{}, s.Err()
	}
	if !done || !finished {
		return engine.ModelResponse{}, fmt.Errorf("incomplete model stream")
	}
	msg := engine.Message{Role: "assistant", Content: text.String()}
	for i := 0; i < len(calls); i++ {
		if c := calls[i]; c != nil {
			var obj map[string]any
			if c.ID == "" || c.Name == "" || !json.Valid(c.Arguments) || json.Unmarshal(c.Arguments, &obj) != nil {
				return engine.ModelResponse{}, fmt.Errorf("invalid tool call")
			}
			msg.ToolCalls = append(msg.ToolCalls, *c)
		}
	}
	if finishReason == "" {
		return engine.ModelResponse{}, fmt.Errorf("missing finish reason")
	}
	if len(calls) > 0 && finishReason != "tool_calls" {
		return engine.ModelResponse{}, fmt.Errorf("invalid tool finish reason")
	}
	return engine.ModelResponse{Message: msg, FinishReason: finishReason}, nil
}

func streamErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "too large") {
		return "model_response_too_large"
	}
	if strings.Contains(err.Error(), "invalid model stream event") {
		return "invalid_model_stream"
	}
	return "incomplete_model_stream"
}

func (m *GatewayModel) readJSON(body io.Reader) (engine.ModelResponse, error) {
	raw, e := io.ReadAll(io.LimitReader(body, api.MaxBodyBytes+1))
	if e != nil {
		return engine.ModelResponse{}, e
	}
	if int64(len(raw)) > api.MaxBodyBytes {
		return engine.ModelResponse{}, fmt.Errorf("model response too large")
	}
	var x struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &x) != nil || len(x.Choices) == 0 || x.Choices[0].Finish == "" {
		return engine.ModelResponse{}, fmt.Errorf("invalid model response")
	}
	w := x.Choices[0].Message
	msg := engine.Message{Role: w.Role, Content: w.Content}
	for _, tc := range w.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, engine.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: []byte(tc.Function.Arguments)})
	}
	return engine.ModelResponse{Message: msg, FinishReason: x.Choices[0].Finish, Raw: raw}, nil
}
