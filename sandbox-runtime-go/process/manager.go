package process

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"agent-platform/sandbox-runtime-go/plugin"
)

type ExecutionResult struct {
	ExecutionID string `json:"execution_id"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exit_code"`
}
type processState struct {
	id         string
	cmd        *exec.Cmd
	done       chan struct{}
	cancelling bool
	pid        int
}
type ProcessManager struct {
	mu     sync.Mutex
	active *processState
}

func (m *ProcessManager) Execute(id string, argv []string, env []string) (ExecutionResult, error) {
	if len(argv) == 0 {
		return ExecutionResult{}, fmt.Errorf("empty command")
	}
	m.mu.Lock()
	if m.active != nil {
		m.mu.Unlock()
		return ExecutionResult{}, ErrBusy
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = safeEnv(env)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ro, wo, e := os.Pipe()
	if e != nil {
		m.mu.Unlock()
		return ExecutionResult{}, e
	}
	re, we, e := os.Pipe()
	if e != nil {
		ro.Close()
		wo.Close()
		m.mu.Unlock()
		return ExecutionResult{}, e
	}
	c.Stdout = wo
	c.Stderr = we
	st := &processState{id: id, cmd: c, done: make(chan struct{})}
	m.active = st
	if e = c.Start(); e != nil {
		ro.Close()
		wo.Close()
		re.Close()
		we.Close()
		m.active = nil
		m.mu.Unlock()
		return ExecutionResult{}, e
	}
	st.pid = c.Process.Pid
	m.mu.Unlock()
	var out, errOut cappedBuffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); drain(&out, ro) }()
	go func() { defer wg.Done(); drain(&errOut, re) }()
	waitCh := make(chan error, 1)
	go func() { waitCh <- c.Wait() }()
	waitErr := <-waitCh

	killGroup(st.pid, syscall.SIGTERM)
	if !waitForGroup(st.pid, plugin.CancelGraceSeconds*time.Second) {
		killGroup(st.pid, syscall.SIGKILL)
	}
	_ = wo.Close()
	_ = we.Close()
	wg.Wait()
	_ = ro.Close()
	_ = re.Close()
	m.mu.Lock()
	if m.active == st {
		m.active = nil
		close(st.done)
	}
	m.mu.Unlock()
	code := 0
	if waitErr != nil {
		if x, ok := waitErr.(*exec.ExitError); ok {
			code = x.ExitCode()
		} else {
			code = -1
		}
	}
	return ExecutionResult{ExecutionID: id, Stdout: out.String(), Stderr: errOut.String(), ExitCode: code}, nil
}

func (m *ProcessManager) Cancel(id string) (bool, error) {
	m.mu.Lock()
	st := m.active
	if st == nil || st.id != id {
		m.mu.Unlock()
		return false, nil
	}
	if st.cancelling {
		m.mu.Unlock()
		return true, nil
	}
	st.cancelling = true
	pid := st.pid
	m.mu.Unlock()
	killGroup(pid, syscall.SIGTERM)
	go func() {
		time.Sleep(plugin.CancelGraceSeconds * time.Second)
		m.mu.Lock()
		active := m.active == st
		m.mu.Unlock()
		if active {
			killGroup(pid, syscall.SIGKILL)
		}
	}()
	return true, nil
}
func (m *ProcessManager) Shutdown() {
	m.mu.Lock()
	st := m.active
	m.mu.Unlock()
	if st != nil {
		m.Cancel(st.id)
		<-st.done
	}
}
func killGroup(pid int, sig syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
}
func waitForGroup(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(-pid, 0) != nil
}

type cappedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	orig := len(p)
	left := int(plugin.MaxOutputBytes) - w.b.Len()
	if left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = w.b.Write(p)
	}
	return orig, nil
}
func (w *cappedBuffer) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }
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
func safeEnv(env []string) []string {
	out := []string{}
	for _, v := range env {
		key, _, ok := strings.Cut(v, "=")
		if ok && (key == "PATH" || key == "HOME" || key == "LANG" || key == "LC_ALL") {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	}
	return out
}

var ErrBusy = fmt.Errorf("runtime_state_conflict")
