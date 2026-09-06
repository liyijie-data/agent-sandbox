package transport

import (
	"agent-platform/internal/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform/sandbox-runtime-go/fsutil"
	"agent-platform/sandbox-runtime-go/plugin"
	"agent-platform/sandbox-runtime-go/process"
)

type Transport struct {
	Root, RunConfig string
	Manifest        contracts.Manifest
	Processes       *process.ProcessManager
	rootFS          *os.Root
}

func NewTransport(root, runConfig string, m contracts.Manifest) *Transport {
	r, _ := os.OpenRoot(root)
	return &Transport{Root: root, RunConfig: runConfig, Manifest: m, Processes: &process.ProcessManager{}, rootFS: r}
}
func (t *Transport) Close() error {
	if t.Processes != nil {
		t.Processes.Shutdown()
	}
	if t.rootFS != nil {
		return t.rootFS.Close()
	}
	return nil
}
func (t *Transport) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", t.root)
	mux.HandleFunc("/manifest", t.manifest)
	mux.HandleFunc("/execute", t.execute)
	mux.HandleFunc("/cancel", t.cancel)
	mux.HandleFunc("/upload", t.upload)
	mux.HandleFunc("/download/", t.download)
	mux.HandleFunc("/list/", t.list)
	mux.HandleFunc("/exists/", t.exists)
	return mux
}
func (t *Transport) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		t.json(w, 404, map[string]string{"status": "error", "error_code": "not_found"})
		return
	}
	t.json(w, 200, map[string]string{"status": "ok", "version": plugin.RuntimeVersion})
}
func (t *Transport) manifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		t.json(w, 405, nil)
		return
	}
	t.json(w, 200, t.Manifest)
}
func (t *Transport) execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		t.json(w, 405, nil)
		return
	}
	if !jsonContentType(r.Header.Get("Content-Type")) {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, plugin.MaxBodyBytes+1)
	var b struct {
		Command     string `json:"command"`
		ExecutionID string `json:"execution_id"`
	}
	if err := decodeJSON(r.Body, &b); err != nil || b.Command == "" {
		if err != nil && errors.Is(err, errBodyTooLarge) {
			t.json(w, 413, map[string]string{"status": "error", "error_code": "payload_too_large"})
			return
		}
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	if b.ExecutionID == "" {
		if raw, e := readBoundedFile(t.RunConfig, plugin.MaxBodyBytes); e == nil {
			if cfg, ve := strictRuntimeConfig(raw); ve == nil {
				b.ExecutionID = cfg.ExecutionID
			}
		}
	}
	if b.ExecutionID != "" {
		raw, e := readBoundedFile(t.RunConfig, plugin.MaxBodyBytes)
		if e != nil {
			t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
			return
		}
		cfg, ve := strictRuntimeConfig(raw)
		if ve != nil || cfg.ExecutionID != b.ExecutionID {
			t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
			return
		}
	}
	if b.ExecutionID == "" {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	argv, e := shellwords(b.Command)
	if e != nil {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	finalArgv := argv
	if sameArgv(argv, t.Manifest.Entrypoint) {
		finalArgv = append(append([]string{}, argv...), "--config", t.RunConfig)
	} else if len(argv) != len(t.Manifest.Entrypoint)+2 || !sameArgv(argv[:len(t.Manifest.Entrypoint)], t.Manifest.Entrypoint) || argv[len(t.Manifest.Entrypoint)] != "--config" || argv[len(t.Manifest.Entrypoint)+1] != t.RunConfig {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	result, e := t.Processes.Execute(b.ExecutionID, finalArgv, os.Environ())
	if e == process.ErrBusy {
		t.json(w, 409, map[string]string{"status": "error", "error_code": "runtime_state_conflict"})
		return
	}
	if e != nil {
		t.json(w, 500, map[string]string{"status": "error", "error_code": "runtime_error"})
		return
	}
	t.json(w, 200, result)
}
func strictRuntimeConfig(raw []byte) (*contracts.RuntimeConfig, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid json")
	}
	cfg, err := contracts.DecodeRuntimeConfig(raw)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func sameArgv(got, expected []string) bool {
	if len(got) != len(expected) {
		return false
	}
	for i := range got {
		if got[i] != expected[i] {
			return false
		}
	}
	return true
}
func (t *Transport) cancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		t.json(w, 405, nil)
		return
	}
	if !jsonContentType(r.Header.Get("Content-Type")) {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, plugin.MaxBodyBytes+1)
	var b struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := decodeJSON(r.Body, &b); err != nil || b.ExecutionID == "" {
		if errors.Is(err, errBodyTooLarge) {
			t.json(w, 413, map[string]string{"status": "error", "error_code": "payload_too_large"})
			return
		}
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	ok, _ := t.Processes.Cancel(b.ExecutionID)
	if ok {
		t.json(w, 202, map[string]string{"status": "accepted", "execution_id": b.ExecutionID})
	} else {
		t.json(w, 200, map[string]string{"status": "stopped", "execution_id": b.ExecutionID})
	}
}
func (t *Transport) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		t.json(w, 405, nil)
		return
	}
	if r.ContentLength > plugin.MaxBodyBytes {
		t.json(w, 413, map[string]string{"status": "error", "error_code": "payload_too_large"})
		return
	}
	med, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || med != "multipart/form-data" {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	lr := &countLimitReader{r: r.Body, n: plugin.MaxBodyBytes + 1}
	mr := multipart.NewReader(lr, params["boundary"])
	part, e := mr.NextPart()
	if e != nil || part.FormName() != "file" {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	name := filepath.Base(part.FileName())
	_, e = fsutil.SafePath(t.Root, name)
	if e != nil || name == "." || name == "" {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	if t.rootFS == nil {
		t.json(w, 500, map[string]string{"status": "error", "error_code": "runtime_error"})
		return
	}
	tmp := ".upload-" + fmt.Sprint(os.Getpid()) + "-" + fmt.Sprint(time.Now().UnixNano())
	f, e := t.rootFS.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	tooLarge := false
	if e == nil {
		var n int64
		n, e = io.Copy(f, io.LimitReader(part, plugin.MaxBodyBytes+1))
		for {
			p2, e2 := mr.NextPart()
			if e2 == io.EOF {
				break
			}
			if e2 != nil {
				e = e2
				break
			}
			_, _ = io.Copy(io.Discard, p2)
			_ = p2.Close()
		}
		tooLarge = e == nil && (n > plugin.MaxBodyBytes || lr.read > plugin.MaxBodyBytes)
		_ = f.Close()
	}
	if tooLarge {
		_ = t.rootFS.Remove(tmp)
		t.json(w, 413, map[string]string{"status": "error", "error_code": "payload_too_large"})
		return
	}
	if e != nil {
		_ = t.rootFS.Remove(tmp)
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	if e = t.rootFS.Rename(tmp, name); e != nil {
		_ = t.rootFS.Remove(tmp)
		t.json(w, 500, map[string]string{"status": "error", "error_code": "runtime_error"})
		return
	}
	t.json(w, 200, map[string]string{"status": "uploaded", "path": "/" + name})
}
func (t *Transport) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		t.json(w, 405, nil)
		return
	}
	_, e := fsutil.SafePath(t.Root, strings.TrimPrefix(r.URL.Path, "/download/"))
	if e != nil || !t.regular(strings.TrimPrefix(r.URL.Path, "/download/")) {
		t.json(w, 404, map[string]string{"status": "error", "error_code": "not_found"})
		return
	}
	f, e := t.rootFS.Open(strings.TrimPrefix(r.URL.Path, "/download/"))
	if e != nil {
		t.json(w, 404, map[string]string{"status": "error", "error_code": "not_found"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, f)
}
func (t *Transport) list(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		t.json(w, 405, nil)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/list/")
	p, e := fsutil.SafePath(t.Root, rel)
	if e != nil {
		t.json(w, 400, map[string]string{"status": "error", "error_code": "invalid_request"})
		return
	}
	var es []os.DirEntry
	if t.rootFS != nil {
		openRel := rel
		if openRel == "" {
			openRel = "."
		}
		f, oe := t.rootFS.Open(openRel)
		if oe != nil {
			t.json(w, 404, map[string]string{"status": "error", "error_code": "not_found"})
			return
		}
		defer f.Close()
		es, e = f.ReadDir(-1)
	} else {
		es, e = os.ReadDir(p)
	}
	if e != nil {
		t.json(w, 404, map[string]string{"status": "error", "error_code": "not_found"})
		return
	}
	names := make([]string, 0, len(es))
	for _, x := range es {
		names = append(names, x.Name())
	}
	sort.Strings(names)
	if len(names) > 4096 {
		names = names[:4096]
	}
	t.json(w, 200, map[string]any{"path": "/" + strings.Trim(rel, "/"), "entries": names})
}
func (t *Transport) exists(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		t.json(w, 405, nil)
		return
	}
	p, e := fsutil.SafePath(t.Root, strings.TrimPrefix(r.URL.Path, "/exists/"))
	t.json(w, 200, map[string]bool{"exists": e == nil && fileExists(p)})
}
func regular(p string) bool {
	s, e := os.Lstat(p)
	return e == nil && s.Mode().IsRegular() && (s.Mode()&os.ModeSymlink) == 0
}
func fileExists(p string) bool { _, e := os.Stat(p); return e == nil }
func (t *Transport) regular(rel string) bool {
	if t.rootFS == nil {
		return false
	}
	s, e := t.rootFS.Stat(rel)
	return e == nil && s.Mode().IsRegular()
}
func (t *Transport) json(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func decodeJSON(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errBodyTooLarge
		}
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing json")
	}
	return nil
}

var errBodyTooLarge = errors.New("request body too large")

func jsonContentType(value string) bool {
	med, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(med, "application/json")
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errBodyTooLarge
	}
	return b, nil
}

type countLimitReader struct {
	r       io.Reader
	n, read int64
}

func (r *countLimitReader) Read(p []byte) (int, error) {
	if r.read >= r.n {
		return 0, io.EOF
	}
	if int64(len(p)) > r.n-r.read {
		p = p[:r.n-r.read]
	}
	n, e := r.r.Read(p)
	r.read += int64(n)
	return n, e
}
func shellwords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	quote := rune(0)
	esc := false
	for _, c := range s {
		if esc {
			b.WriteRune(c)
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				b.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		} else if c == ' ' || c == '\t' || c == '\n' {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		} else {
			b.WriteRune(c)
		}
	}
	if esc || quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return out, nil
}
