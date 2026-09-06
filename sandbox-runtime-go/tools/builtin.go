package tools

import (
	"agent-platform/sandbox-runtime-go/engine"
	"agent-platform/sandbox-runtime-go/fsutil"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxListEntries   = 500
	maxListDepth     = 4
	maxReadBytes     = 8 * 1024
	defaultPageBytes = 8 * 1024
	maxPageBytes     = 16 * 1024
	maxSearchBytes   = 10 * 1024 * 1024
	maxWriteBytes    = 1024 * 1024
	maxCommandBytes  = 16 * 1024
	maxScriptOutput  = 1024 * 1024
)

type Builtin struct {
	Root, OutputRoot      string
	WriteLimit, ReadLimit int64
	ScriptTimeout         time.Duration
}
type builtinTool struct {
	name, description, op string
	owner                 Builtin
}

func (b Builtin) Name() string               { return "agent_builtin" }
func (b Builtin) Description() string        { return "root-confined file and script tools" }
func (b Builtin) Definitions() []engine.Tool { return b.Tools() }
func (t builtinTool) Name() string           { return t.name }
func (t builtinTool) Description() string    { return t.description }
func (t builtinTool) Execute(ctx context.Context, a json.RawMessage) (string, error) {
	var v map[string]any
	if len(a) > 0 && json.Unmarshal(a, &v) != nil {
		return "", fmt.Errorf("invalid arguments")
	}
	if v == nil {
		v = map[string]any{}
	}
	v["operation"] = t.op
	b, _ := json.Marshal(v)
	return t.owner.Execute(ctx, b)
}
func (b Builtin) Tools() []engine.Tool {
	return []engine.Tool{builtinTool{"agent_list_files", "List workspace files", "agent_list_files", b}, builtinTool{"agent_read_file", "Read a UTF-8 text file", "agent_read_file", b}, builtinTool{"agent_write_file", "Atomically write a UTF-8 text file", "agent_write_file", b}, builtinTool{"agent_run_script", "Run a bounded shell command", "agent_run_script", b}, builtinTool{"agent_search_file", "Search a UTF-8 text file", "agent_search_file", b}, builtinTool{"agent_load_skill", "Load a skill instructions file on demand", "agent_load_skill", b}}
}
func (b Builtin) Execute(ctx context.Context, a json.RawMessage) (string, error) {
	var x struct {
		Operation, Path, Content, Command string
		Name, Query                       string
		Offset                            *int64 `json:"offset"`
		MaxBytes                          *int64 `json:"max_bytes"`
		TimeoutSeconds                    int    `json:"timeout_seconds"`
	}
	if json.Unmarshal(a, &x) != nil {
		return "", fmt.Errorf("invalid arguments")
	}
	switch x.Operation {
	case "agent_list_files":
		return listFiles(b, x.Path)
	case "agent_read_file":
		return readFile(b, x.Path, x.Offset, x.MaxBytes)
	case "agent_search_file":
		return searchFile(b, x.Path, x.Query, x.Offset)
	case "agent_load_skill":
		return loadSkill(b, x.Name)
	case "agent_write_file":
		return writeFile(b, x.Path, x.Content)
	case "agent_run_script":
		return runScript(ctx, b, x.Command, x.TimeoutSeconds)
	default:
		return "", fmt.Errorf("unknown builtin operation")
	}
}
func rootFor(b Builtin, path string) (*os.Root, string, error) {
	rel, e := fsutil.SafeRel(path)
	if e != nil {
		return nil, "", e
	}
	rp := b.Root
	if rel == "output" || strings.HasPrefix(rel, "output/") {
		rp = b.OutputRoot
		if rp == "" {
			return nil, "", fmt.Errorf("output root is not configured")
		}
		rel = strings.TrimPrefix(rel, "output")
		rel = strings.TrimPrefix(rel, "/")
	}
	r, e := os.OpenRoot(rp)
	if e != nil {
		return nil, "", e
	}
	if e = rejectSymlink(r, rel); e != nil {
		r.Close()
		return nil, "", e
	}
	return r, rel, nil
}
func rejectSymlink(r *os.Root, rel string) error {
	cur := "."
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		fi, e := r.Lstat(cur)
		if e == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path rejected")
		}
	}
	return nil
}
func listFiles(b Builtin, path string) (string, error) {
	r, rel, e := rootFor(b, path)
	if e != nil {
		return "", e
	}
	defer r.Close()
	if _, e = r.Stat(relOrDot(rel)); e != nil {
		return "", e
	}
	out := make([]string, 0)
	e = fs.WalkDir(r.FS(), relOrDot(rel), func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == relOrDot(rel) {
			return nil
		}
		trimmed := strings.TrimPrefix(filepath.ToSlash(p), filepath.ToSlash(relOrDot(rel))+"/")
		depth := strings.Count(trimmed, "/") + 1
		if depth > maxListDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(out) >= maxListEntries {
			return fmt.Errorf("list limit exceeded")
		}
		out = append(out, filepath.ToSlash(p))
		return nil
	})
	if e != nil {
		return "", e
	}
	v, _ := json.Marshal(out)
	return string(v), nil
}
func relOrDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}
func writeFile(b Builtin, path, content string) (string, error) {
	r, rel, e := rootFor(b, path)
	if e != nil {
		return "", e
	}
	defer r.Close()
	if rel == "" {
		return "", fmt.Errorf("path must name a file")
	}
	if protected(rel) {
		return "", fmt.Errorf("protocol zone is read-only")
	}
	d := []byte(content)
	if len(d) > maxWriteBytes {
		return "", fmt.Errorf("write limit exceeded")
	}
	if e = r.MkdirAll(filepath.Dir(rel), 0755); e != nil {
		return "", e
	}
	tmp := filepath.Join(filepath.Dir(rel), fmt.Sprintf(".write-%d", time.Now().UnixNano()))
	f, e := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	defer r.Remove(tmp)
	if _, e = f.Write(d); e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return "", e
	}
	return "", r.Rename(tmp, rel)
}
func protected(p string) bool {
	return p == "result.json" || p == "checkpoint" || p == "checkpoint.tar.gz" || p == ".skills" || p == ".skill-archives" || p == ".runtime-trace" || p == ".context" || strings.HasPrefix(p, ".skills/") || strings.HasPrefix(p, ".skill-archives/") || strings.HasPrefix(p, ".runtime-trace/") || strings.HasPrefix(p, ".context/")
}
func readBounded(r io.Reader, n int64) ([]byte, error) {
	d, e := io.ReadAll(io.LimitReader(r, n+1))
	if e != nil {
		return nil, e
	}
	if int64(len(d)) > n {
		return nil, fmt.Errorf("read limit exceeded")
	}
	return d, nil
}
