package artifacts

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/contracts"
)

func statusEvent(stage string, raw []byte) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		v = map[string]any{"incomplete": true, "reason": "invalid stage status"}
	}
	b, _ := json.Marshal(map[string]any{"identity": map[string]any{"stage": stage}, "type": "trace.status", "time": time.Now().UTC().Format(time.RFC3339Nano), "content": v})
	return b
}

func inlineLegacyBlob(line []byte, stageDir string) ([]byte, bool) {
	var obj map[string]any
	if json.Unmarshal(line, &obj) != nil {
		return line, false
	}
	if typ, _ := obj["type"].(string); typ != "model.request" && typ != "model.response" && typ != "tool.call" && typ != "tool.result" && typ != "trace.status" && typ != "agent.final" && typ != "agent.stage_result" && typ != "agent.error" && typ != "diagnostic.status" {
		return nil, true
	}
	content, ok := obj["content"].(map[string]any)
	if !ok || len(content) != 3 {
		return line, true
	}
	blob, bok := content["blob"].(string)
	sz, sok := content["size"].(float64)
	sum, hok := content["sha256"].(string)
	if !bok || !sok || !hok || blob == "" || sz < 0 || sum == "" {
		return line, true
	}
	if filepath.IsAbs(blob) || filepath.Clean(blob) != blob || strings.HasPrefix(blob, "../") || strings.Contains(blob, "/../") {
		return line, false
	}
	path := filepath.Join(stageDir, filepath.FromSlash(blob))
	cur := stageDir
	for _, part := range strings.Split(filepath.FromSlash(blob), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		ci, ce := os.Lstat(cur)
		if ce != nil || ci.Mode()&os.ModeSymlink != 0 {
			return line, false
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || int64(sz) != info.Size() {
		return line, false
	}
	if info.Size() > DefaultDiagnosticMaxBytes {
		return line, false
	}
	f, err := os.Open(path)
	if err != nil {
		return line, false
	}
	data, err := io.ReadAll(io.LimitReader(f, DefaultDiagnosticMaxBytes+1))
	_ = f.Close()
	if err != nil || int64(len(data)) != info.Size() {
		return line, false
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != sum {
		return line, false
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		value = string(data)
	}
	obj["content"] = value
	out, err := json.Marshal(obj)
	return out, err == nil
}

const DefaultMaxBytes int64 = 64 << 20
const DefaultDiagnosticMaxBytes int64 = 32 << 20
const MaxUncompressedBytes int64 = 100 << 20

type Error struct{ Code, Reason string }

func (e *Error) Error() string { return e.Code + ": " + e.Reason }

type File struct {
	SHA256    string
	SizeBytes int64
}
type Baseline struct {
	Workspace map[string]File `json:"workspace"`
}

func Snapshot(root string) (Baseline, error) {
	b := Baseline{Workspace: map[string]File{}}
	return b, walk(root, func(rel, path string, info os.FileInfo) error {
		d, n, e := hashFile(path)
		if e == nil {
			b.Workspace[rel] = File{d, n}
		}
		return e
	})
}

type Entry struct {
	Source, Name, Path, SHA256 string
	SizeBytes                  int64
}
type Bundle struct {
	Path, SHA256 string
	SizeBytes    int64
	HasBusiness  bool
}
type Bundler struct {
	MaxBytes           int64
	DiagnosticMaxBytes int64
	TraceRoot          string
	Destination        *contracts.ResultBundle
	Client             *http.Client
}

func (b Bundler) limit() int64 {
	if b.MaxBytes > 0 {
		return b.MaxBytes
	}
	return DefaultMaxBytes
}

func (b Bundler) Collect(workspace, output string, baseline Baseline) (Bundle, error) {
	var entries []Entry
	add := func(source, root, rel string, info os.FileInfo) error {
		path := filepath.Join(root, filepath.FromSlash(rel))
		d, n, e := hashFile(path)
		if e != nil {
			return e
		}
		if source == "workspace" && baseline.Workspace[rel].SHA256 == d && baseline.Workspace[rel].SizeBytes == n {
			return nil
		}
		entries = append(entries, Entry{source, rel, path, d, n})
		return nil
	}
	if output != "" {
		if e := walk(output, func(rel, path string, info os.FileInfo) error {
			if skipOutput(rel) {
				return nil
			}
			return add("output", output, rel, info)
		}); e != nil {
			return Bundle{}, e
		}
	}
	if workspace != "" {
		if e := walk(workspace, func(rel, path string, info os.FileInfo) error {
			if skipWorkspace(rel) {
				return nil
			}
			return add("workspace", workspace, rel, info)
		}); e != nil {
			return Bundle{}, e
		}
	}
	if b.TraceRoot != "" && b.Destination != nil {
		businessCount := len(entries)
		tracePath, e := addTrace(b.TraceRoot, b.DiagnosticMaxBytes)
		if e == nil && tracePath != "" {
			d, n, he := hashFile(tracePath)
			if he != nil {
				_ = os.Remove(tracePath)
				e = he
			} else {
				entries = append(entries, Entry{Source: "output", Name: ".runtime-trace/trace.jsonl", Path: tracePath, SHA256: d, SizeBytes: n})
			}
		}
		if e != nil {

			fallback, fe := os.CreateTemp("", "agent-trace-error-*.jsonl")
			if fe == nil {
				line := statusEvent("trace", []byte(`{"incomplete":true,"reason":"diagnostic merge failed"}`))
				_, we := fallback.Write(append(line, '\n'))
				ce := fallback.Close()
				if we != nil || ce != nil {
					_ = os.Remove(fallback.Name())
				} else {
					d, n, he := hashFile(fallback.Name())
					if he == nil {
						entries = append(entries[:businessCount], Entry{Source: "output", Name: ".runtime-trace/trace.jsonl", Path: fallback.Name(), SHA256: d, SizeBytes: n})
					} else {
						_ = os.Remove(fallback.Name())
					}
				}
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Source == entries[j].Source {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Source < entries[j].Source
	})
	if len(entries) == 0 {
		return Bundle{}, nil
	}
	result, e := b.build(entries)
	for _, x := range entries {
		if x.Name == ".runtime-trace/trace.jsonl" && x.Path != "" {
			_ = os.Remove(x.Path)
		}
	}
	return result, e
}

func addTrace(root string, limit int64) (string, error) {
	if limit <= 0 {
		limit = DefaultDiagnosticMaxBytes
	}
	ents, e := os.ReadDir(root)
	if os.IsNotExist(e) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	type stageEnt struct {
		name, path string
		n          int
	}
	var stages []stageEnt
	for _, stage := range ents {
		if !stage.IsDir() || !strings.HasPrefix(stage.Name(), "stage-") {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(stage.Name(), "stage-"))
		stages = append(stages, stageEnt{stage.Name(), filepath.Join(root, stage.Name()), n})
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].n < stages[j].n })
	f, e := os.CreateTemp("", "agent-trace-*.jsonl")
	if e != nil {
		return "", e
	}
	path := f.Name()
	cleanup := func(err error) (string, error) { _ = f.Close(); _ = os.Remove(path); return "", err }
	var used int64
	const reserve int64 = 1024
	incomplete := false
	hasStage := len(stages) > 0
	write := func(line []byte) error {
		if incomplete {
			return nil
		}
		if used+int64(len(line)) > limit-reserve {
			incomplete = true
			return nil
		}
		if _, err := f.Write(line); err != nil {
			return err
		}
		used += int64(len(line))
		return nil
	}
	for _, stage := range stages {
		tracePath := filepath.Join(stage.path, "trace.jsonl")
		if in, err := os.Open(tracePath); err == nil {
			s := bufio.NewScanner(in)
			s.Buffer(make([]byte, 64<<10), int(limit)+1)
			for s.Scan() {
				line, ok := inlineLegacyBlob(s.Bytes(), stage.path)
				if line == nil {
					continue
				}
				if !ok {
					incomplete = true
					continue
				}
				line = append(line, '\n')
				if err := write(line); err != nil {
					_ = in.Close()
					return cleanup(err)
				}
			}
			if err := s.Err(); err != nil {
				incomplete = true
			}
			_ = in.Close()
		} else {
			incomplete = true
		}
		if sf, err := os.Open(filepath.Join(stage.path, "status.json")); err == nil {
			status, re := io.ReadAll(io.LimitReader(sf, 64<<10+1))
			_ = sf.Close()
			if re != nil || len(status) > 64<<10 {
				incomplete = true
				continue
			}
			line := statusEvent(stage.name, status)
			if err := write(append(line, '\n')); err != nil {
				return cleanup(err)
			}
		} else if !os.IsNotExist(err) {
			incomplete = true
		}
	}
	if !hasStage {
		return cleanup(nil)
	}
	if incomplete {
		line := statusEvent("trace", []byte(`{"incomplete":true,"reason":"diagnostic budget exceeded or legacy content unavailable"}`))
		if used+int64(len(line))+1 <= limit {
			if _, err := f.Write(append(line, '\n')); err != nil {
				return cleanup(err)
			}
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (b Bundler) build(entries []Entry) (Bundle, error) {
	f, e := os.CreateTemp("", "agent-result-*.zip")
	if e != nil {
		return Bundle{}, e
	}
	path := f.Name()
	defer f.Close()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	h := sha256.New()
	mw := io.MultiWriter(f, h)
	zw := zip.NewWriter(mw)
	total := int64(0)
	manifest := struct {
		Artifacts []map[string]any `json:"artifacts"`
	}{[]map[string]any{}}
	var business int64
	for i := range entries {
		x := &entries[i]
		var data []byte
		if x.Path == "" {
			data = []byte(`{"missing":"diagnostics_exceeded"}`)
			sum := sha256.Sum256(data)
			x.SHA256 = hex.EncodeToString(sum[:])
			x.SizeBytes = int64(len(data))
		}
		total += x.SizeBytes
		if !strings.HasPrefix(x.Name, ".runtime-trace/") {
			business += x.SizeBytes
		}
		if business > b.limit() || total > MaxUncompressedBytes {
			_ = f.Close()
			return Bundle{}, &Error{"resource_limit_exceeded", "bundle exceeds size bound"}
		}
		w, e := zw.Create(filepath.ToSlash(filepath.Join(map[bool]string{true: "artifacts", false: "workspace"}[x.Source == "output"], x.Name)))
		if e != nil {
			return Bundle{}, e
		}
		if data != nil {
			_, e = w.Write(data)
		} else {
			in, oe := os.Open(x.Path)
			if oe != nil {
				return Bundle{}, oe
			}
			hh := sha256.New()
			n, ce := io.Copy(io.MultiWriter(w, hh), io.LimitReader(in, x.SizeBytes+1))
			if ce == nil && (n != x.SizeBytes || hex.EncodeToString(hh.Sum(nil)) != x.SHA256) {
				ce = fmt.Errorf("artifact changed during bundle")
			}
			e = ce
			_ = in.Close()
		}
		if e != nil {
			return Bundle{}, e
		}
		manifest.Artifacts = append(manifest.Artifacts, map[string]any{"source": x.Source, "name": x.Name, "sha256": x.SHA256, "size_bytes": x.SizeBytes})
	}
	mb, _ := json.Marshal(manifest)
	w, e := zw.Create("manifest.json")
	if e == nil {
		_, e = w.Write(mb)
	}
	if e == nil {
		e = zw.Close()
	}
	if e == nil {
		e = f.Close()
	}
	if e != nil {
		return Bundle{}, e
	}
	ok = true
	sum := hex.EncodeToString(h.Sum(nil))
	st, _ := os.Stat(path)
	hasBusiness := false
	for _, x := range entries {
		if x.Name != ".runtime-trace/missing.json" && !strings.HasPrefix(x.Name, ".runtime-trace/") {
			hasBusiness = true
		}
	}
	return Bundle{path, sum, st.Size(), hasBusiness}, nil
}

func (b Bundler) Deliver(bundle Bundle, dest *contracts.ResultBundle) (contracts.DeliveryOutcome, error) {
	return b.DeliverContext(context.Background(), bundle, dest)
}
func (b Bundler) DeliverContext(ctx context.Context, bundle Bundle, dest *contracts.ResultBundle) (contracts.DeliveryOutcome, error) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if bundle.Path == "" {
		if dest == nil {
			return contracts.DeliveryOutcome{Status: contracts.DeliveryNotRequested}, nil
		}
		return contracts.DeliveryOutcome{Status: contracts.DeliveryEmpty}, nil
	}
	if dest == nil || dest.UploadURL == "" {
		if !bundle.HasBusiness {
			_ = os.Remove(bundle.Path)
			return contracts.DeliveryOutcome{Status: contracts.DeliveryEmpty}, nil
		}
		return contracts.DeliveryOutcome{}, &Error{"artifact_destination_required", "artifacts produced but no destination declared"}
	}
	defer os.Remove(bundle.Path)
	f, e := os.Open(bundle.Path)
	if e != nil {
		return contracts.DeliveryOutcome{}, &Error{"result_bundle_upload_failed", "cannot open bundle"}
	}
	defer f.Close()
	req, e := http.NewRequest(http.MethodPut, dest.UploadURL, f)
	if e != nil {
		return contracts.DeliveryOutcome{}, &Error{"result_bundle_upload_failed", e.Error()}
	}
	req = req.WithContext(ctx)
	req.ContentLength = bundle.SizeBytes
	req.Header.Set("Content-Type", "application/zip")
	c := b.Client
	if c == nil {
		c = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, e := c.Do(req)
	if e != nil {
		return contracts.DeliveryOutcome{}, &Error{"result_bundle_upload_failed", "upload failed"}
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return contracts.DeliveryOutcome{}, &Error{"result_bundle_upload_failed", fmt.Sprintf("upload HTTP %d", resp.StatusCode)}
	}
	return contracts.DeliveryOutcome{Status: contracts.DeliveryUploaded, DestinationID: dest.DestinationID, SHA256: bundle.SHA256, SizeBytes: bundle.SizeBytes}, nil
}

func skipOutput(rel string) bool {
	return rel == "result.json" || rel == "checkpoint.tar.gz" || strings.HasPrefix(rel, ".skills") || strings.HasPrefix(rel, ".skill")
}
func skipWorkspace(rel string) bool {
	return rel == ".context" || strings.HasPrefix(rel, ".context/") || strings.HasPrefix(rel, ".skills") || strings.HasPrefix(rel, ".skill-archives")
}
func walk(root string, fn func(string, string, os.FileInfo) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		return fn(filepath.ToSlash(rel), path, info)
	})
}
func hashFile(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
