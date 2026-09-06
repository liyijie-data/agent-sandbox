package checkpoint

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const Format = "go-runtime/1"
const defaultMaxBytes int64 = 512 << 20
const maxMembers = 4096

type Options struct {
	RunID          string
	Stage          int
	Fence          int64
	PluginVersions map[string]string
	Cursor         int64
	MaxBytes       int64
	TraceRoot      string
}
type Entry struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}
type Manifest struct {
	ContractVersion    string            `json:"contract_version"`
	StateFormat        string            `json:"state_format"`
	RunID              string            `json:"run_id"`
	Stage              int               `json:"stage"`
	Fence              int64             `json:"fence"`
	PluginVersions     map[string]string `json:"plugin_versions,omitempty"`
	Cursor             int64             `json:"steering_cursor"`
	Entries            map[string]Entry  `json:"entries"`
	TotalSizeBytes     int64             `json:"total_size_bytes"`
	DiagnosticsMissing []string          `json:"diagnostics_missing,omitempty"`
}
type State struct {
	Manifest Manifest
	Data     []byte
}

func lim(o Options) int64 {
	if o.MaxBytes > 0 {
		return o.MaxBytes
	}
	return defaultMaxBytes
}
func ctxok(c context.Context) error {
	select {
	case <-c.Done():
		return c.Err()
	default:
		return nil
	}
}
func Save(c context.Context, ws, out, path string, o Options, data []byte) error {
	if e := ctxok(c); e != nil {
		return e
	}
	en, total, e := collect(c, ws, out, path)
	if e != nil {
		return e
	}
	en["agent/state.bin"] = Entry{hex.EncodeToString(hash(data)), int64(len(data))}
	total += int64(len(data))
	missing := []string{}
	if o.TraceRoot != "" {
		diag, _, de := collectTrace(c, o.TraceRoot, path)
		if de != nil {
			missing = append(missing, "diagnostics/.runtime-trace")
		} else {
			groups := map[string][]string{}
			for n := range diag {
				parts := strings.Split(n, "/")
				if len(parts) >= 3 {
					groups[strings.Join(parts[:3], "/")] = append(groups[strings.Join(parts[:3], "/")], n)
				}
			}
			stages := make([]string, 0, len(groups))
			for s := range groups {
				stages = append(stages, s)
			}
			sort.Strings(stages)
			for _, stage := range stages {
				trial := map[string]Entry{}
				trialTotal := total
				for _, n := range groups[stage] {
					trial[n] = diag[n]
					trialTotal += diag[n].SizeBytes
				}
				candidateEntries := map[string]Entry{}
				for n, v := range en {
					candidateEntries[n] = v
				}
				candidate := Manifest{ContractVersion: "agent-platform-runtime/v1", StateFormat: Format, RunID: o.RunID, Stage: o.Stage, Fence: o.Fence, PluginVersions: o.PluginVersions, Cursor: o.Cursor, Entries: candidateEntries, TotalSizeBytes: trialTotal}
				for n, v := range trial {
					candidate.Entries[n] = v
				}
				cb, _ := json.Marshal(candidate)
				if trialTotal+int64(len(cb)) <= lim(o) && len(candidate.Entries) <= maxMembers-2 {
					for n, v := range trial {
						en[n] = v
					}
					total = trialTotal
				} else {
					missing = append(missing, stage)
				}
			}
		}
	}
	m := Manifest{ContractVersion: "agent-platform-runtime/v1", StateFormat: Format, RunID: o.RunID, Stage: o.Stage, Fence: o.Fence, PluginVersions: o.PluginVersions, Cursor: o.Cursor, Entries: en, TotalSizeBytes: total, DiagnosticsMissing: missing}
	mb, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if int64(len(mb))+total > lim(o) {
		return fmt.Errorf("checkpoint budget")
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".checkpoint-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	g := gzip.NewWriter(f)
	t := tar.NewWriter(g)
	if e = write(c, t, "manifest.json", mb); e == nil {
		e = write(c, t, "agent/state.bin", data)
	}
	if e == nil {
		e = writeDir(c, t, ws, "workspace", path)
	}
	if e == nil && out != "" {
		e = writeDir(c, t, out, "output", path)
	}
	if e == nil && o.TraceRoot != "" {
		e = writeTrace(c, t, o.TraceRoot, path, en)
	}
	if e == nil {
		e = t.Close()
	}
	if e == nil {
		e = g.Close()
	}
	if e == nil {
		e = f.Sync()
	}
	if e == nil {
		e = f.Close()
	}
	if e != nil {
		_ = f.Close()
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	ok = true
	return nil
}
func hash(b []byte) []byte { x := sha256.Sum256(b); return x[:] }
func write(c context.Context, t *tar.Writer, n string, b []byte) error {
	if e := ctxok(c); e != nil {
		return e
	}
	if e := t.WriteHeader(&tar.Header{Name: n, Mode: 0600, Size: int64(len(b)), ModTime: time.Unix(0, 0)}); e != nil {
		return e
	}
	for len(b) > 0 {
		if e := ctxok(c); e != nil {
			return e
		}
		n, e := t.Write(b)
		if e != nil {
			return e
		}
		b = b[n:]
	}
	return nil
}
func collect(c context.Context, ws, out, cp string) (map[string]Entry, int64, error) {
	r := map[string]Entry{}
	var total int64
	for _, x := range []struct{ r, p string }{{ws, "workspace"}, {out, "output"}} {
		if x.r == "" {
			continue
		}
		e := filepath.Walk(x.r, func(p string, i os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			if e = ctxok(c); e != nil {
				return e
			}
			if i.IsDir() {
				return nil
			}
			if i.Mode()&os.ModeSymlink != 0 || !i.Mode().IsRegular() {
				return fmt.Errorf("checkpoint unsupported file")
			}
			rel, _ := filepath.Rel(x.r, p)
			n := x.p + "/" + filepath.ToSlash(rel)
			if skip(p, cp) {
				return nil
			}
			h, z, e := hashFile(c, p)
			if e != nil {
				return e
			}
			r[n] = Entry{hex.EncodeToString(h), z}
			total += z
			return nil
		})
		if e != nil {
			return nil, 0, e
		}
	}
	return r, total, nil
}
func collectTrace(c context.Context, root, cp string) (map[string]Entry, int64, error) {
	r := map[string]Entry{}
	var total int64
	e := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if e = ctxok(c); e != nil {
			return e
		}
		if i.IsDir() {
			return nil
		}
		if i.Mode()&os.ModeSymlink != 0 || !i.Mode().IsRegular() {
			return fmt.Errorf("checkpoint unsupported trace file")
		}
		if skip(p, cp) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		n := "diagnostics/.runtime-trace/" + filepath.ToSlash(rel)
		h, z, e := hashFile(c, p)
		if e != nil {
			return e
		}
		r[n] = Entry{hex.EncodeToString(h), z}
		total += z
		return nil
	})
	return r, total, e
}
func writeTrace(c context.Context, t *tar.Writer, root, cp string, entries map[string]Entry) error {
	return filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if e = ctxok(c); e != nil {
			return e
		}
		if i.IsDir() {
			return nil
		}
		if i.Mode()&os.ModeSymlink != 0 || !i.Mode().IsRegular() {
			return fmt.Errorf("checkpoint unsupported trace file")
		}
		if skip(p, cp) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		name := "diagnostics/.runtime-trace/" + filepath.ToSlash(rel)
		if _, ok := entries[name]; !ok {
			return nil
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		st, e := in.Stat()
		if e == nil {
			e = t.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: st.Size(), ModTime: time.Unix(0, 0)})
		}
		if e == nil {
			_, e = io.Copy(t, in)
		}
		ce := in.Close()
		if e == nil {
			e = ce
		}
		return e
	})
}
func skip(p, cp string) bool {
	b := filepath.Base(p)
	if b == "result.json" || strings.HasPrefix(b, ".checkpoint-") || strings.HasPrefix(b, ".restore-") {
		return true
	}
	a, e1 := filepath.Abs(p)
	d, e2 := filepath.Abs(cp)
	return e1 == nil && e2 == nil && a == d
}
func hashFile(c context.Context, p string) ([]byte, int64, error) {
	f, e := os.Open(p)
	if e != nil {
		return nil, 0, e
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var n int64
	for {
		if e = ctxok(c); e != nil {
			_ = f.Close()
			return nil, 0, e
		}
		k, r := f.Read(buf)
		if k > 0 {
			_, _ = h.Write(buf[:k])
			n += int64(k)
		}
		if r == io.EOF {
			break
		}
		if r != nil {
			_ = f.Close()
			return nil, 0, r
		}
	}
	e = f.Close()
	return h.Sum(nil), n, e
}
func writeDir(c context.Context, t *tar.Writer, root, prefix, cp string) error {
	return filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if e = ctxok(c); e != nil {
			return e
		}
		if i.IsDir() {
			return nil
		}
		if i.Mode()&os.ModeSymlink != 0 || !i.Mode().IsRegular() {
			return fmt.Errorf("checkpoint unsupported file")
		}
		if skip(p, cp) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		name := prefix + "/" + filepath.ToSlash(rel)
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		st, e := in.Stat()
		if e == nil {
			e = t.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: st.Size(), ModTime: time.Unix(0, 0)})
		}
		if e == nil {
			_, e = io.Copy(t, in)
		}
		ce := in.Close()
		if e == nil {
			e = ce
		}
		return e
	})
}
func RestoreTo(c context.Context, path, root string, o Options) (State, error) {
	m, state, sizes, e := scan(c, path, o)
	if e != nil {
		return State{}, e
	}
	if e = os.MkdirAll(root, 0755); e != nil {
		return State{}, e
	}
	st, e := os.MkdirTemp(root, ".restore-")
	if e != nil {
		return State{}, e
	}
	defer os.RemoveAll(st)
	if e = extract(c, path, st, o, m, state, sizes); e != nil {
		return State{}, e
	}
	for _, p := range []string{"workspace", "output"} {
		if e = os.MkdirAll(filepath.Join(st, p), 0755); e != nil {
			return State{}, e
		}
	}
	if e = swap(st, root); e != nil {
		return State{}, e
	}
	return State{Manifest: m, Data: state}, nil
}

func openArchive(path string) (*os.File, *gzip.Reader, *tar.Reader, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, nil, nil, e
	}
	g, e := gzip.NewReader(f)
	if e != nil {
		_ = f.Close()
		return nil, nil, nil, e
	}
	return f, g, tar.NewReader(g), nil
}
func scan(c context.Context, path string, o Options) (Manifest, []byte, map[string]int64, error) {
	var m Manifest
	sizes := map[string]int64{}
	var state []byte
	var total, manifestSize int64
	seen := map[string]bool{}
	count := 0
	gotM, gotS := false, false
	f, g, t, e := openArchive(path)
	if e != nil {
		return m, nil, nil, e
	}
	defer f.Close()
	defer g.Close()
	for {
		if e = ctxok(c); e != nil {
			return m, nil, nil, e
		}
		h, x := t.Next()
		if x == io.EOF {
			break
		}
		if x != nil {
			return m, nil, nil, x
		}
		count++
		if count > maxMembers || !safe(h.Name) || seen[h.Name] || h.Typeflag != tar.TypeReg {
			return m, nil, nil, fmt.Errorf("unsafe or duplicate checkpoint member")
		}
		seen[h.Name] = true
		if h.Name == "manifest.json" || h.Name == "agent/state.bin" {
			if h.Size > 4<<20 {
				return m, nil, nil, fmt.Errorf("control member too large")
			}
			b, x := readN(c, t, h.Size, 4<<20)
			if x != nil {
				return m, nil, nil, x
			}
			if h.Name == "manifest.json" {
				if x = json.Unmarshal(b, &m); x != nil {
					return m, nil, nil, x
				}
				gotM = true
				manifestSize = int64(len(b))
			} else {
				state = b
				gotS = true
			}
			total += int64(len(b))
			if total > lim(o) {
				return m, nil, nil, fmt.Errorf("checkpoint budget")
			}
			continue
		}
		if !strings.HasPrefix(h.Name, "workspace/") && !strings.HasPrefix(h.Name, "output/") && !strings.HasPrefix(h.Name, "diagnostics/.runtime-trace/") {
			return m, nil, nil, fmt.Errorf("member not allowed")
		}
		if h.Size < 0 || h.Size > lim(o) || total+h.Size > lim(o) {
			return m, nil, nil, fmt.Errorf("checkpoint budget")
		}
		sizes[h.Name] = h.Size
		total += h.Size
		if _, x = io.CopyN(io.Discard, t, h.Size); x != nil {
			return m, nil, nil, x
		}
	}
	if !gotM || !gotS {
		return m, nil, nil, fmt.Errorf("missing manifest or state")
	}
	if m.ContractVersion != "agent-platform-runtime/v1" || m.StateFormat != Format || m.RunID != o.RunID || m.Stage != o.Stage || m.Fence != o.Fence || m.Cursor != o.Cursor || !plugins(m.PluginVersions, o.PluginVersions) {
		return m, nil, nil, fmt.Errorf("checkpoint identity mismatch")
	}
	if m.TotalSizeBytes != total-manifestSize {
		return m, nil, nil, fmt.Errorf("checkpoint total mismatch")
	}
	if len(m.Entries) != len(sizes)+1 {
		return m, nil, nil, fmt.Errorf("manifest entries mismatch")
	}
	se, ok := m.Entries["agent/state.bin"]
	if !ok {
		return m, nil, nil, fmt.Errorf("manifest state missing")
	}
	if se.SizeBytes != int64(len(state)) || se.SHA256 != hex.EncodeToString(hash(state)) {
		return m, nil, nil, fmt.Errorf("state entry mismatch")
	}
	return m, state, sizes, nil
}
func extract(c context.Context, path, st string, o Options, m Manifest, state []byte, sizes map[string]int64) error {
	f, g, t, e := openArchive(path)
	if e != nil {
		return e
	}
	defer f.Close()
	defer g.Close()
	seen := map[string]bool{}
	var gotState bool
	for {
		if e = ctxok(c); e != nil {
			return e
		}
		h, x := t.Next()
		if x == io.EOF {
			break
		}
		if x != nil {
			return x
		}
		if !safe(h.Name) || seen[h.Name] || h.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsafe or duplicate checkpoint member")
		}
		seen[h.Name] = true
		if h.Name == "manifest.json" {
			if h.Size > 4<<20 {
				return fmt.Errorf("control member too large")
			}
			if _, x = io.CopyN(io.Discard, t, h.Size); x != nil {
				return x
			}
			continue
		}
		if h.Name == "agent/state.bin" {
			if h.Size != int64(len(state)) {
				return fmt.Errorf("state size mismatch")
			}
			if _, x = io.CopyN(io.Discard, t, h.Size); x != nil {
				return x
			}
			if e = os.MkdirAll(filepath.Join(st, "agent"), 0755); e != nil {
				return e
			}
			if e = os.WriteFile(filepath.Join(st, "agent/state.bin"), state, 0600); e != nil {
				return e
			}
			gotState = true
			continue
		}
		if !strings.HasPrefix(h.Name, "workspace/") && !strings.HasPrefix(h.Name, "output/") && !strings.HasPrefix(h.Name, "diagnostics/.runtime-trace/") {
			return fmt.Errorf("member not allowed")
		}
		expected, ok := m.Entries[h.Name]
		if !ok || sizes[h.Name] != h.Size {
			return fmt.Errorf("entry mismatch")
		}
		pfx, rel, _ := strings.Cut(h.Name, "/")
		if pfx == "diagnostics" {
			rel = strings.TrimPrefix(rel, ".runtime-trace/")
			pfx = ".runtime-trace"
		}
		dst := filepath.Join(st, pfx, rel)
		if e = os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
			return e
		}
		out, e := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		hashr := sha256.New()
		n, e := io.Copy(io.MultiWriter(out, hashr), io.LimitReader(t, h.Size))
		ce := out.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
		if n != expected.SizeBytes || hex.EncodeToString(hashr.Sum(nil)) != expected.SHA256 {
			return fmt.Errorf("entry mismatch")
		}
	}
	if !gotState {
		return fmt.Errorf("missing state")
	}
	for n := range m.Entries {
		if n == "agent/state.bin" {
			continue
		}
		if !seen[n] {
			return fmt.Errorf("manifest entries mismatch")
		}
	}
	return nil
}

var renameFn = os.Rename
var removeAllFn = os.RemoveAll

func swap(st, root string) error {
	bk, e := os.MkdirTemp(root, ".checkpoint-backup-")
	if e != nil {
		return e
	}
	moved := []string{}
	installed := []string{}
	for _, n := range []string{"workspace", "output", ".runtime-trace"} {
		old, new := filepath.Join(root, n), filepath.Join(st, n)
		if _, ne := os.Stat(new); ne != nil && n == ".runtime-trace" {
			continue
		}
		if _, e = os.Stat(old); e == nil {
			if e = renameFn(old, filepath.Join(bk, n)); e != nil && errors.Is(e, syscall.EXDEV) {
				if e = copyTree(old, filepath.Join(bk, n)); e == nil {
					moved = append(moved, n)
					e = removeAllFn(old)
				}
			}
			if e != nil {
				if re := rollback(root, bk, moved, installed); re != nil {
					return fmt.Errorf("%w (backup %s; rollback: %v)", e, bk, re)
				}
				_ = removeAllFn(bk)
				return e
			}
			if len(moved) == 0 || moved[len(moved)-1] != n {
				moved = append(moved, n)
			}
		}
		if e = renameFn(new, old); e != nil {
			if re := rollback(root, bk, moved, installed); re != nil {
				return fmt.Errorf("%w (backup %s; rollback: %v)", e, bk, re)
			}
			_ = removeAllFn(bk)
			return e
		}
		installed = append(installed, n)
	}
	if e = removeAllFn(bk); e != nil {
		return fmt.Errorf("backup cleanup failed (backup %s): %w", bk, e)
	}
	return nil
}
func rollback(root, bk string, moved, installed []string) error {
	var first error
	for i := len(installed) - 1; i >= 0; i-- {
		if e := removeAllFn(filepath.Join(root, installed[i])); e != nil && first == nil {
			first = e
		}
	}
	for i := len(moved) - 1; i >= 0; i-- {
		n := moved[i]
		if e := removeAllFn(filepath.Join(root, n)); e != nil {
			if first == nil {
				first = e
			}
			continue
		}
		if e := renameFn(filepath.Join(bk, n), filepath.Join(root, n)); e != nil && first == nil {
			first = e
		}
	}
	return first
}

func copyTree(src, dst string) error {
	var total int64
	var members int
	return filepath.Walk(src, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(src, p)
		q := filepath.Join(dst, rel)
		if info.IsDir() {
			members++
			if members > maxMembers {
				return fmt.Errorf("checkpoint too many members")
			}
			return os.MkdirAll(q, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in checkpoint tree")
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file in checkpoint tree")
		}
		members++
		if members > maxMembers || total+info.Size() > defaultMaxBytes {
			return fmt.Errorf("checkpoint copy budget")
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		defer in.Close()
		if e = os.MkdirAll(filepath.Dir(q), 0755); e != nil {
			return e
		}
		out, e := os.OpenFile(q, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if e != nil {
			return e
		}
		w := io.LimitReader(in, defaultMaxBytes-total+1)
		var n int64
		n, e = io.Copy(out, w)
		if e == nil && n > defaultMaxBytes-total {
			e = fmt.Errorf("checkpoint copy budget")
		}
		if e == nil {
			total += n
		}
		ce := out.Close()
		if e == nil {
			e = ce
		}
		return e
	})
}
func readN(c context.Context, r io.Reader, n, max int64) ([]byte, error) {
	if n < 0 || n > max {
		return nil, fmt.Errorf("checkpoint budget")
	}
	b := make([]byte, 0, n)
	buf := make([]byte, 1<<20)
	for int64(len(b)) < n {
		if e := ctxok(c); e != nil {
			return nil, e
		}
		want := n - int64(len(b))
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		k, e := io.ReadFull(r, buf[:want])
		b = append(b, buf[:k]...)
		if e != nil {
			return nil, e
		}
	}
	return b, nil
}
func safe(n string) bool {
	n = strings.ReplaceAll(n, "\\", "/")
	if n == "" || strings.HasPrefix(n, "/") {
		return false
	}
	for _, p := range strings.Split(n, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return n == "manifest.json" || n == "agent/state.bin" || strings.HasPrefix(n, "workspace/") || strings.HasPrefix(n, "output/") || strings.HasPrefix(n, "diagnostics/.runtime-trace/")
}
func plugins(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func Restore(path string, o Options) (State, error) {
	m, state, sizes, e := scan(context.Background(), path, o)
	if e != nil {
		return State{}, e
	}
	st, e := os.MkdirTemp("", ".checkpoint-verify-")
	if e != nil {
		return State{}, e
	}
	defer os.RemoveAll(st)
	if e = extract(context.Background(), path, st, o, m, state, sizes); e != nil {
		return State{}, e
	}
	return State{Manifest: m, Data: state}, nil
}
func Hash(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
