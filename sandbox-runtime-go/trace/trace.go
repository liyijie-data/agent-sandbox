package trace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Options struct {
	RunID, ExecutionID string
	Stage              int
	Fence              int64
	PluginVersions     map[string]string
	Secrets            []string
	SignatureQueryKeys []string
	MaxBytes           int64
	Enabled            bool
}

type Status struct {
	Incomplete bool   `json:"incomplete"`
	Reason     string `json:"reason,omitempty"`
	Bytes      int64  `json:"bytes"`
}

type Recorder struct {
	mu             sync.Mutex
	root, dir      string
	opt            Options
	file           *os.File
	bytes          int64
	closed, failed bool
	status         Status
	round          int
	secret         []string
	keys           map[string]bool
}

func New(root string, opt Options) (*Recorder, error) {
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = 32 << 20
	}
	r := &Recorder{root: root, opt: opt, keys: map[string]bool{}}
	for _, k := range opt.SignatureQueryKeys {
		r.keys[strings.ToLower(k)] = true
	}
	for _, k := range []string{"signature", "sig", "token", "access_token", "auth", "authorization", "x-amz-signature", "x-amz-credential", "x-amz-security-token"} {
		r.keys[k] = true
	}
	for _, s := range opt.Secrets {
		if s != "" {
			r.secret = append(r.secret, s)
		}
	}
	if !opt.Enabled {
		return r, nil
	}
	r.dir = filepath.Join(root, ".runtime-trace", fmt.Sprintf("stage-%d", opt.Stage))
	if err := os.MkdirAll(r.dir, 0700); err != nil {
		r.failLocked("mkdir: " + err.Error())
		return r, nil
	}
	f, err := os.OpenFile(filepath.Join(r.dir, "trace.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		r.failLocked("open: " + err.Error())
		return r, nil
	}
	r.file = f
	_ = filepath.Walk(r.dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			r.bytes += info.Size()
		}
		return nil
	})
	return r, nil
}

func (r *Recorder) Record(typ string, content any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.failed || !r.opt.Enabled {
		return nil
	}
	purpose := ""
	purposeKey := "request"
	switch typ {
	case "context.summary.request":
		typ, purpose = "model.request", "context_compaction"
	case "context.summary.response":
		typ, purpose = "model.response", "context_compaction"
		purposeKey = "response"
	case "agent.tool_call", "agent.tool_result":

		return nil
	}
	switch typ {
	case "model.request", "model.response", "tool.call", "tool.result", "trace.status":
	default:
		return nil
	}
	if typ == "model.request" && purpose == "" {
		r.round++
	}
	if purpose != "" {
		content = map[string]any{"purpose": purpose, purposeKey: content}
	}
	b, err := json.Marshal(r.sanitize(content))
	if err != nil {
		r.failLocked("encode: " + err.Error())
		return nil
	}
	var payload any = json.RawMessage(b)
	line := map[string]any{"identity": map[string]any{"run_id": r.opt.RunID, "execution_id": r.opt.ExecutionID, "stage": r.opt.Stage, "fence": r.opt.Fence, "round": r.round, "plugin_versions": r.opt.PluginVersions}, "type": typ, "time": time.Now().UTC().Format(time.RFC3339Nano), "content": payload}
	lineb, err := json.Marshal(line)
	if err != nil {
		r.failLocked("encode: " + err.Error())
		return nil
	}
	lineb = append(lineb, '\n')
	if r.bytes+int64(len(lineb)) > r.opt.MaxBytes {
		r.failLocked("budget exceeded")
		return nil
	}
	if _, err = r.file.Write(lineb); err != nil {
		r.failLocked("write: " + err.Error())
		return nil
	}
	r.bytes += int64(len(lineb))
	r.status.Bytes = r.bytes
	return nil
}

func (r *Recorder) Sanitize(content any) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sanitize(content)
}

func (r *Recorder) Dir() string { r.mu.Lock(); defer r.mu.Unlock(); return r.dir }

func (r *Recorder) ReadOnlyDir() string { return r.Dir() }
func (r *Recorder) Status() Status      { r.mu.Lock(); defer r.mu.Unlock(); return r.status }
func (r *Recorder) Freeze()             { r.mu.Lock(); defer r.mu.Unlock(); r.closed = true; r.closeFileLocked() }
func (r *Recorder) Close() error        { r.Freeze(); return nil }

func (r *Recorder) closeFileLocked() {
	if r.file != nil {
		if err := r.file.Sync(); err != nil && !r.status.Incomplete {
			r.status = Status{Incomplete: true, Reason: "sync: " + err.Error(), Bytes: r.bytes}
		}
		if err := r.file.Close(); err != nil && !r.status.Incomplete {
			r.status = Status{Incomplete: true, Reason: "close: " + err.Error(), Bytes: r.bytes}
		}
		r.file = nil
	}
	if r.status.Incomplete {
		r.writeStatusLocked()
	}
}
func (r *Recorder) failLocked(reason string) {
	if r.failed {
		return
	}
	r.failed = true
	r.status = Status{Incomplete: true, Reason: reason, Bytes: r.bytes}
	r.closeFileLocked()
}
func (r *Recorder) writeStatusLocked() {
	if r.dir == "" {
		_, _ = fmt.Fprintf(os.Stderr, "runtime trace incomplete: %s\n", r.status.Reason)
		return
	}
	b, _ := json.Marshal(r.status)
	if err := os.WriteFile(filepath.Join(r.dir, "status.json"), b, 0600); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "runtime trace incomplete: %s; status: %v\n", r.status.Reason, err)
	}
}

var urlInText = regexp.MustCompile(`https?://[^\s"<>]+`)

func sensitiveField(k string) bool {
	switch strings.ToLower(k) {
	case "authorization", "token", "password", "passwd", "secret", "api_key", "apikey", "credential", "signature":
		return true
	default:
		return false
	}
}

func (r *Recorder) sanitize(v any) any {
	switch x := v.(type) {
	case json.RawMessage:
		var y any
		dec := json.NewDecoder(bytes.NewReader(x))
		dec.UseNumber()
		if dec.Decode(&y) == nil {
			return r.sanitize(y)
		}
		return r.sanitizeString(string(x))
	case []byte:
		var y any
		dec := json.NewDecoder(bytes.NewReader(x))
		dec.UseNumber()
		if dec.Decode(&y) == nil {
			return r.sanitize(y)
		}
		return r.sanitizeString(string(x))
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, v := range x {
			if sensitiveField(k) || r.keys[strings.ToLower(k)] {
				o[k] = "[REDACTED]"
			} else {
				o[k] = r.sanitize(v)
			}
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i := range x {
			o[i] = r.sanitize(x[i])
		}
		return o
	case string:
		return r.sanitizeString(x)
	case nil, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "[UNSERIALIZABLE]"
		}
		var y any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if dec.Decode(&y) == nil {
			return r.sanitize(y)
		}
		return v
	}
}
func (r *Recorder) sanitizeString(s string) string {
	for _, secret := range r.secret {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	redactURL := func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		changed := u.User != nil
		if changed {
			u.User = nil
		}
		if u.RawQuery == "" {
			if changed {
				return u.String()
			}
			return raw
		}
		q := u.Query()
		for k := range q {
			if r.keys[strings.ToLower(k)] {
				q.Set(k, "[REDACTED]")
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
			return u.String()
		}
		return raw
	}
	s = urlInText.ReplaceAllStringFunc(s, redactURL)
	if !urlInText.MatchString(s) {
		s = redactURL(s)
	}
	for k := range r.keys {
		re := regexp.MustCompile(`(?i)(` + regexp.QuoteMeta(k) + `)=([^&\s]+)`)
		s = re.ReplaceAllString(s, `$1=[REDACTED]`)
	}
	s = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`).ReplaceAllString(s, `$1`)
	return s
}
