package remote

import (
	"agent-platform/internal/contracts"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const protocol = "2025-03-26"

type mcp struct {
	mu                sync.Mutex
	idv, url, session string
	allowed           map[string]bool
	tools             map[string]map[string]any
	next              int
	ctx               context.Context
	negotiated        string
	legacy            bool
	legacyCancel      context.CancelFunc
	legacyBody        io.ReadCloser
	pending           map[int]chan map[string]any
}

func newMCP(ctx context.Context, s contracts.ToolSpec) (adapter, error) {
	if s.Transport == contracts.ToolTransportStdio {
		return newStdio(ctx, s, StdioOptions{})
	}
	if s.ID == "" || s.URL == "" || (s.Transport != contracts.ToolTransportStreamableHTTP && s.Transport != "sse") || len(s.AllowedActions) == 0 {
		return nil, fmt.Errorf("invalid MCP tool configuration")
	}
	u, e := url.Parse(s.URL)
	if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("invalid MCP URL")
	}
	a := &mcp{idv: s.ID, url: s.URL, legacy: s.Transport == "sse", allowed: map[string]bool{}, tools: map[string]map[string]any{}, pending: map[int]chan map[string]any{}, ctx: ctx}
	for _, x := range s.AllowedActions {
		if x == "" || a.allowed[x] {
			return nil, fmt.Errorf("invalid allowed tool")
		}
		a.allowed[x] = true
	}
	if e := a.handshake(ctx); e != nil {
		return nil, e
	}
	return a, nil
}
func (a *mcp) id() string { return a.idv }
func (a *mcp) close() error {
	if a.legacyCancel != nil {
		a.legacyCancel()
		a.legacyCancel = nil
	}
	if a.legacyBody != nil {
		_ = a.legacyBody.Close()
		a.legacyBody = nil
	}
	a.mu.Lock()
	for _, ch := range a.pending {
		close(ch)
	}
	a.pending = map[int]chan map[string]any{}
	a.mu.Unlock()
	if a.session != "" {
		ctx, c := context.WithTimeout(context.Background(), 30e9)
		defer c()
		req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, a.url, nil)
		req.Header.Set("Mcp-Session-Id", a.session)
		_, e := client.Do(req)
		a.session = ""
		if e != nil {
			return nil
		}
		return e
	}
	return nil
}
func (a *mcp) rpc(ctx context.Context, method string, params any) (map[string]any, error) {
	if a.legacy {
		return a.legacyRPC(ctx, method, params)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	id := a.next
	p := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		p["params"] = params
	}
	body, _ := json.Marshal(p)
	h := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json, text/event-stream"}}
	if a.session != "" {
		h.Set("Mcp-Session-Id", a.session)
	}
	if a.negotiated != "" {
		h.Set("MCP-Protocol-Version", a.negotiated)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.url, strings.NewReader(string(body)))
	if e != nil {
		return nil, e
	}
	req.Header = h
	resp, e := client.Do(req)
	if e != nil {
		return nil, unavailableError{e}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("redirects are forbidden")
	}
	status, hh := resp.StatusCode, resp.Header
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("MCP HTTP %d", status)
	}
	if sid := hh.Get("Mcp-Session-Id"); sid != "" {
		a.session = sid
	}
	var out map[string]any
	if strings.HasPrefix(strings.ToLower(hh.Get("Content-Type")), "text/event-stream") {
		out, e = eSSEReader(resp.Body, id, maxResponse)
		if e != nil {
			return nil, e
		}
	} else {
		data, e := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
		if e != nil {
			return nil, e
		}
		if int64(len(data)) > maxResponse {
			return nil, fmt.Errorf("response exceeds bound")
		}
		if json.Unmarshal(data, &out) != nil {
			return nil, fmt.Errorf("invalid MCP JSON")
		}
	}
	if fmt.Sprint(out["id"]) != fmt.Sprint(id) {
		return nil, fmt.Errorf("MCP response id mismatch")
	}
	if er, ok := out["error"]; ok {
		return nil, fmt.Errorf("MCP JSON-RPC error: %v", er)
	}
	r, ok := out["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("MCP result missing")
	}
	return r, nil
}
func eSSEReader(r io.Reader, id int, limit int64) (map[string]any, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	var total int64
	for sc.Scan() {
		line := sc.Text()
		total += int64(len(line))
		if total > limit {
			return nil, fmt.Errorf("response exceeds bound")
		}
		if line == "" {
			if len(data) > 0 {
				var m map[string]any
				if json.Unmarshal([]byte(strings.Join(data, "\n")), &m) == nil && fmt.Sprint(m["id"]) == fmt.Sprint(id) {
					return m, nil
				}
			}
			data = nil
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return nil, fmt.Errorf("no matching JSON-RPC SSE event")
}
func eSSE(data []byte) (map[string]any, error) {
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "data:") {
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &m) == nil && m["id"] != nil {
				return m, nil
			}
		}
	}
	return nil, fmt.Errorf("no JSON-RPC SSE event")
}
func (a *mcp) handshake(ctx context.Context) error {
	if a.legacy {
		if err := a.negotiateSSE(ctx); err != nil {
			return unavailableError{err}
		}
	}
	r, e := a.rpc(ctx, "initialize", map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agent-runtime-go", "version": "1"}})
	if e != nil {
		return unavailableError{e}
	}
	if _, ok := r["protocolVersion"]; !ok {
		return unavailableError{fmt.Errorf("MCP initialize missing protocolVersion")}
	}
	if v, ok := r["protocolVersion"].(string); ok {
		a.negotiated = v
	}
	p := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	b, _ := json.Marshal(p)
	h := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json, text/event-stream"}, "Mcp-Session-Id": []string{a.session}}
	status, _, _, ne := request(ctx, http.MethodPost, a.url, b, h, 1024)
	if ne != nil || status >= 400 {
		return unavailableError{fmt.Errorf("MCP initialized notification failed")}
	}
	cursor := ""
	for page := 0; page < 16; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		r, e := a.rpc(ctx, "tools/list", params)
		if e != nil {
			return unavailableError{e}
		}
		list, _ := r["tools"].([]any)
		for _, v := range list {
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid MCP tool entry")
			}
			n, _ := m["name"].(string)
			if n == "" || len(a.tools) >= 512 {
				return fmt.Errorf("invalid or excessive MCP tools")
			}
			sc, _ := m["inputSchema"].(map[string]any)
			if sc == nil {
				sc = map[string]any{"type": "object"}
			}
			if _, e := schema(sc, sc, 0, new(int)); e != nil {
				return e
			}
			a.tools[n] = map[string]any{"description": fmt.Sprint(m["description"]), "schema": sc}
		}
		cursor, _ = r["nextCursor"].(string)
		if cursor == "" {
			break
		}
	}
	for n := range a.allowed {
		if _, ok := a.tools[n]; !ok {
			return unavailableError{fmt.Errorf("allowed MCP tool not offered: %s", n)}
		}
	}
	return nil
}

func (a *mcp) negotiateSSE(ctx context.Context) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if e != nil {
		return e
	}
	streamClient := *client
	streamClient.Timeout = 0
	resp, e := streamClient.Do(req)
	if e != nil {
		return e
	}
	defer func() {
		if a.legacyBody == nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MCP SSE HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	var event string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") && event == "endpoint" {
			ep := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			base, _ := url.Parse(a.url)
			u, e := url.Parse(ep)
			if e == nil {
				u = base.ResolveReference(u)
			}
			if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host != mustURLHost(a.url) || u.User != nil {
				return fmt.Errorf("invalid SSE endpoint")
			}
			a.url = u.String()
			cc, c := context.WithCancel(context.Background())
			a.legacyCancel = c
			a.legacyBody = resp.Body
			go a.readLegacyScanner(cc, sc)
			return nil
		}
	}
	return fmt.Errorf("SSE endpoint missing")
}
func (a *mcp) readLegacy(ctx context.Context, r io.Reader) {
	sc := bufio.NewScanner(r)
	a.readLegacyScanner(ctx, sc)
}
func (a *mcp) readLegacyScanner(ctx context.Context, sc *bufio.Scanner) {
	var data []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			var m map[string]any
			if len(data) > 0 && json.Unmarshal([]byte(strings.Join(data, "\n")), &m) == nil {
				if id, ok := m["id"].(float64); ok {
					a.mu.Lock()
					if ch := a.pending[int(id)]; ch != nil {
						delete(a.pending, int(id))
						ch <- m
						close(ch)
					}
					a.mu.Unlock()
				}
			}
			data = nil
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}
func (a *mcp) legacyRPC(ctx context.Context, method string, params any) (map[string]any, error) {
	a.mu.Lock()
	a.next++
	id := a.next
	ch := make(chan map[string]any, 1)
	a.pending[id] = ch
	neg := a.negotiated
	sid := a.session
	a.mu.Unlock()
	p := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		p["params"] = params
	}
	b, _ := json.Marshal(p)
	h := http.Header{"Content-Type": []string{"application/json"}}
	if neg != "" {
		h.Set("MCP-Protocol-Version", neg)
	}
	if sid != "" {
		h.Set("Mcp-Session-Id", sid)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.url, strings.NewReader(string(b)))
	if e != nil {
		return nil, e
	}
	req.Header = h
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	resp.Body.Close()
	if method == "notifications/initialized" {
		return map[string]any{}, nil
	}
	select {
	case m := <-ch:
		if er := m["error"]; er != nil {
			return nil, fmt.Errorf("MCP JSON-RPC error: %v", er)
		}
		return m["result"].(map[string]any), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func mustURLHost(s string) string { u, _ := url.Parse(s); return u.Host }
func (a *mcp) definitions() ([]Definition, error) {
	out := []Definition{}
	for n := range a.allowed {
		x := a.tools[n]
		desc, _ := x["description"].(string)
		sc, ok := x["schema"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid tool schema")
		}
		d := Definition{Type: "function", Function: FunctionDefinition{Name: toolName(a.idv, n), Description: desc, Parameters: sc}}
		b, _ := json.Marshal(d)
		if len(b) > maxSchema {
			return nil, fmt.Errorf("tool schema exceeds bound")
		}
		out = append(out, d)
	}
	return out, nil
}
func (a *mcp) call(ctx context.Context, name, args string) (map[string]any, error) {
	var n string
	for x := range a.allowed {
		if toolName(a.idv, x) == name {
			n = x
		}
	}
	if n == "" {
		return nil, fmt.Errorf("tool unauthorized")
	}
	var v map[string]any
	if args != "" && json.Unmarshal([]byte(args), &v) != nil {
		return nil, fmt.Errorf("arguments are not valid JSON")
	}
	if e := validate(a.tools[n]["schema"], a.tools[n]["schema"], v, "arguments"); e != nil {
		return nil, e
	}
	r, e := a.rpc(ctx, "tools/call", map[string]any{"name": n, "arguments": v})
	if e != nil {
		return nil, e
	}
	content, _ := r["content"].([]any)
	if len(content) > 64 {
		return nil, fmt.Errorf("MCP content exceeds bound")
	}
	texts := []string{}
	for _, x := range content {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid MCP content")
		}
		if m["type"] == "resource" || m["type"] == "resource_link" {
			return nil, fmt.Errorf("MCP resources are forbidden")
		}
		if t, ok := m["text"].(string); ok {
			texts = append(texts, t)
		}
	}
	if r["isError"] == true {
		return map[string]any{"ok": false, "error_type": "McpToolError", "error": strings.Join(texts, "\n")}, nil
	}
	return map[string]any{"ok": true, "result": map[string]any{"text": strings.Join(texts, "\n")}}, nil
}
