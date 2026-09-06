package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

func runScript(parent context.Context, b Builtin, command string, seconds int) (string, error) {
	if command == "" || len([]byte(command)) > maxCommandBytes {
		return "", fmt.Errorf("invalid command")
	}
	limit := b.ScriptTimeout
	if limit <= 0 {
		limit = 15 * time.Second
	}
	if seconds != 0 {
		if seconds < 1 || seconds > 60 {
			return "", fmt.Errorf("timeout_seconds must be 1..60")
		}
		requested := time.Duration(seconds) * time.Second
		if b.ScriptTimeout <= 0 || b.ScriptTimeout == 15*time.Second || requested < limit {
			limit = requested
		}
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = b.Root
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	outR, outW, e := os.Pipe()
	if e != nil {
		return "", e
	}
	errR, errW, e := os.Pipe()
	if e != nil {
		outR.Close()
		outW.Close()
		return "", e
	}
	cmd.Stdout = outW
	cmd.Stderr = errW
	if e = cmd.Start(); e != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return "", e
	}
	lim := maxScriptOutput
	if b.ReadLimit > 0 && b.ReadLimit < int64(lim) {
		lim = int(b.ReadLimit)
	}
	buf := &syncBuffer{limit: lim}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); drain(buf, outR) }()
	go func() { defer wg.Done(); drain(buf, errR) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
		killProcessGroup(cmd.Process.Pid)
	case <-ctx.Done():
		killProcessGroup(cmd.Process.Pid)
		waitErr = <-done
	}
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	_ = outR.Close()
	_ = errR.Close()
	if ctx.Err() != nil {
		return buf.String(), ctx.Err()
	}
	return buf.String(), waitErr
}

type syncBuffer struct {
	mu       sync.Mutex
	b        strings.Builder
	n, limit int
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig := len(p)
	if s.n < s.limit {
		keep := s.limit - s.n
		if len(p) > keep {
			p = p[:keep]
		}
		s.b.Write(p)
		s.n += len(p)
	}
	return orig, nil
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func drain(w io.Writer, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, e := r.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
		}
		if e != nil {
			return
		}
	}
}
func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
