package remote

import (
	"agent-platform/internal/contracts"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type StdioOptions struct{ SandboxRoot, WorkingDir string }
type boundedBuffer struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.max - len(b.b)
	if n > len(p) {
		n = len(p)
	}
	if n > 0 {
		b.b = append(b.b, p[:n]...)
	}
	return len(p), nil
}

type stdio struct {
	mu       sync.Mutex
	idv      string
	cmd      *exec.Cmd
	in       io.WriteCloser
	pending  map[int]chan map[string]any
	next     int
	tools    map[string]map[string]any
	allowed  map[string]bool
	done     chan struct{}
	waitDone chan struct{}
	stderr   *boundedBuffer
}

func DiscoverWithOptions(ctx context.Context, specs []contracts.ToolSpec, opt StdioOptions) (*Registry, []Unavailable, error) {
	return discoverOpts(ctx, specs, opt)
}
func discoverOpts(ctx context.Context, specs []contracts.ToolSpec, opt StdioOptions) (*Registry, []Unavailable, error) {
	r := &Registry{owners: map[string]adapter{}}
	seen := map[string]bool{}
	var u []Unavailable
	for _, s := range specs {
		if seen[s.ID] {
			return nil, nil, fmt.Errorf("duplicate remote tool id %q", s.ID)
		}
		seen[s.ID] = true
		var a adapter
		var e error
		if s.Type == contracts.ToolTypeMCP && s.Transport == contracts.ToolTransportStdio {
			a, e = newStdio(ctx, s, opt)
		} else if s.Type == contracts.ToolTypeMCP {
			a, e = newMCP(ctx, s)
		} else if s.Type == contracts.ToolTypeOpenAPI {
			a, e = newOpenAPI(ctx, s)
		} else {
			e = fmt.Errorf("unsupported remote tool type")
		}
		if e != nil {
			u = append(u, Unavailable{s.ID, e.Error()})
			continue
		}
		d, e := a.definitions()
		if e != nil {
			_ = a.close()
			u = append(u, Unavailable{s.ID, e.Error()})
			continue
		}
		r.adapters = append(r.adapters, a)
		r.defs = append(r.defs, d...)
		for _, x := range d {
			r.owners[x.Function.Name] = a
		}
	}
	return r, u, nil
}
func newStdio(ctx context.Context, s contracts.ToolSpec, opt StdioOptions) (adapter, error) {
	if s.Command == "" {
		return nil, fmt.Errorf("stdio command required")
	}
	dir := opt.WorkingDir
	if dir == "" {
		dir = opt.SandboxRoot
	}
	c := exec.CommandContext(ctx, s.Command, s.Args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Dir = dir
	c.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	for k, v := range s.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	in, e := c.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, e := c.StdoutPipe()
	if e != nil {
		return nil, e
	}
	bb := &boundedBuffer{max: 64 << 10}
	c.Stderr = bb
	if e = c.Start(); e != nil {
		return nil, e
	}
	a := &stdio{idv: s.ID, cmd: c, in: in, stderr: bb, pending: map[int]chan map[string]any{}, tools: map[string]map[string]any{}, allowed: map[string]bool{}, done: make(chan struct{}), waitDone: make(chan struct{})}
	for _, x := range s.AllowedActions {
		a.allowed[x] = true
	}
	go a.read(out)
	go func() { _ = c.Wait(); close(a.waitDone) }()
	if _, e := a.rpc(ctx, "initialize", map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agent-runtime-go", "version": "1"}}); e != nil {
		_ = a.close()
		return nil, e
	}
	_, _ = a.rpc(ctx, "notifications/initialized", nil)
	r, e := a.rpc(ctx, "tools/list", map[string]any{})
	if e != nil {
		_ = a.close()
		return nil, e
	}
	list, ok := r["tools"].([]any)
	if !ok {
		_ = a.close()
		return nil, fmt.Errorf("invalid tools list")
	}
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			_ = a.close()
			return nil, fmt.Errorf("invalid tool entry")
		}
		n, _ := m["name"].(string)
		a.tools[n] = m
	}
	for cursor, pages := r["nextCursor"], 0; cursor != nil && pages < 16; pages++ {
		r, e = a.rpc(ctx, "tools/list", map[string]any{"cursor": cursor})
		if e != nil {
			break
		}
		list, ok := r["tools"].([]any)
		if !ok {
			_ = a.close()
			return nil, fmt.Errorf("invalid tools list")
		}
		for _, x := range list {
			m, ok := x.(map[string]any)
			if !ok {
				_ = a.close()
				return nil, fmt.Errorf("invalid tool entry")
			}
			n, _ := m["name"].(string)
			a.tools[n] = m
		}
		cursor = r["nextCursor"]
	}
	return a, nil
}
func (a *stdio) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
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
	close(a.done)
}
func (a *stdio) rpc(ctx context.Context, method string, params any) (map[string]any, error) {
	if method == "notifications/initialized" {
		p := map[string]any{"jsonrpc": "2.0", "method": method}
		b, _ := json.Marshal(p)
		a.mu.Lock()
		_, e := a.in.Write(append(b, '\n'))
		a.mu.Unlock()
		return map[string]any{}, e
	}
	a.mu.Lock()
	a.next++
	id := a.next
	ch := make(chan map[string]any, 1)
	a.pending[id] = ch
	defer func() { a.mu.Lock(); delete(a.pending, id); a.mu.Unlock() }()
	a.mu.Unlock()
	p := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		p["params"] = params
	}
	b, _ := json.Marshal(p)
	if len(b) > maxRequest {
		return nil, fmt.Errorf("request exceeds bound")
	}
	a.mu.Lock()
	_, e := a.in.Write(append(b, '\n'))
	a.mu.Unlock()
	if e != nil {
		return nil, e
	}
	select {
	case m := <-ch:
		if er := m["error"]; er != nil {
			return nil, fmt.Errorf("MCP error: %v", er)
		}
		result, ok := m["result"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid result")
		}
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.done:
		return nil, fmt.Errorf("stdio process exited")
	}
}
func (a *stdio) id() string { return a.idv }
func (a *stdio) close() error {
	_ = a.in.Close()
	if a.cmd.Process != nil {
		_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-a.waitDone:
		case <-time.After(2 * time.Second):
		}
		_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
	}
	<-a.waitDone
	return nil
}
func (a *stdio) definitions() ([]Definition, error) {
	out := []Definition{}
	for n := range a.allowed {
		m, ok := a.tools[n]
		if !ok {
			return nil, fmt.Errorf("allowed tool not offered")
		}
		sc, _ := m["inputSchema"].(map[string]any)
		if sc == nil {
			sc = map[string]any{"type": "object"}
		}
		out = append(out, Definition{Type: "function", Function: FunctionDefinition{Name: toolName(a.idv, n), Description: fmt.Sprint(m["description"]), Parameters: sc}})
	}
	return out, nil
}
func (a *stdio) call(ctx context.Context, name, args string) (map[string]any, error) {
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
		return nil, fmt.Errorf("invalid arguments")
	}
	if e := validate(a.tools[n]["inputSchema"], a.tools[n]["inputSchema"], v, "arguments"); e != nil {
		return nil, e
	}
	r, e := a.rpc(ctx, "tools/call", map[string]any{"name": n, "arguments": v})
	if e != nil {
		return nil, e
	}
	return map[string]any{"ok": true, "result": r}, nil
}

var _ = strings.TrimSpace
var _ = os.ErrNotExist
