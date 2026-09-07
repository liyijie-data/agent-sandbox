package resources

import (
	"agent-platform/internal/contracts"
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxDownload        int64 = 256 << 20
	maxArchive         int64 = 64 << 20
	maxTotal           int64 = 512 << 20
	maxMember          int64 = 64 << 20
	maxEntries               = 4096
	maxSkillMD         int64 = 1 << 20
	maxContextRefBytes       = 8 << 10
	maxContextRefs           = 256
)

var limits = struct{ maxDownload, maxArchive, maxTotal, maxMember int64 }{maxDownload, maxArchive, maxTotal, maxMember}

type Error struct{ Code, Reason string }

func (e *Error) Error() string { return e.Code + ": " + e.Reason }
func bad(c, r string) error    { return &Error{c, r} }
func safe(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, x := range strings.Split(p, "/") {
		if x == "" || x == "." || x == ".." {
			return false
		}
	}
	return true
}
func safeExisting(root, rel string) bool {
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		if st, e := os.Lstat(cur); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}
func download(ctx context.Context, ref contracts.ResourceRef, dst string) error {
	if !strings.HasPrefix(ref.DownloadURL, "http://") && !strings.HasPrefix(ref.DownloadURL, "https://") {
		return bad("resource_invalid_entry", "download url")
	}
	c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, "GET", ref.DownloadURL, nil)
	if e != nil {
		return bad("resource_download_failed", e.Error())
	}
	resp, e := c.Do(req)
	if e != nil {
		return bad("resource_download_failed", "request failed")
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return bad("resource_download_failed", "request failed")
	}
	defer resp.Body.Close()
	f, e := os.CreateTemp(filepath.Dir(dst), ".part-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limits.maxDownload+1))
	_ = f.Close()
	if e != nil {
		return e
	}
	if n > limits.maxDownload {
		return bad("resource_download_too_large", "limit")
	}
	if ref.SizeBytes >= 0 && ref.SizeBytes != n {
		return bad("resource_size_mismatch", "size")
	}
	if ref.SHA256 != "" && hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
		return bad("resource_hash_mismatch", "sha256")
	}
	return os.Rename(tmp, dst)
}
func Prepare(ctx context.Context, root string, cfg *contracts.RuntimeConfig, resume bool) ([]contracts.Message, error) {
	os.MkdirAll(filepath.Join(root, "workspace"), 0755)
	ws := filepath.Join(root, "workspace")
	seenFiles := map[string]bool{}
	for _, r := range cfg.Files {
		if len(r.SHA256) != 64 {
			return nil, bad("resource_invalid_entry", "sha256")
		}
		if _, e := hex.DecodeString(r.SHA256); e != nil {
			return nil, bad("resource_invalid_entry", "sha256")
		}
		if !safe(r.Name) {
			return nil, bad("resource_unsafe_path", r.Name)
		}
		if seenFiles[r.Name] {
			return nil, bad("resource_duplicate_path", r.Name)
		}
		seenFiles[r.Name] = true
		if !safeExisting(ws, r.Name) {
			return nil, bad("resource_unsafe_path", r.Name)
		}
		p := filepath.Join(ws, r.Name)
		os.MkdirAll(filepath.Dir(p), 0755)
		if resume {
			if _, e := os.Stat(p); e == nil {
				continue
			}
			continue
		}
		if e := download(ctx, r, p); e != nil {
			return nil, e
		}
	}
	for _, r := range cfg.Skills {
		if e := skill(ctx, ws, r, resume); e != nil {
			return nil, e
		}
	}
	return prepareReferencesWithRoot(ws, cfg.Files, cfg.Skills), nil
}
func PrepareReferences(files, skills []contracts.ResourceRef) []contracts.Message {
	var out []contracts.Message
	for _, r := range files {
		s := fmt.Sprintf("Attached file: workspace/%s", r.Name)
		out = append(out, contracts.Message{Role: contracts.RoleSystem, Content: &s})
	}
	for _, r := range skills {
		s := fmt.Sprintf("Attached skill: workspace/.skills/%s", r.Name)
		out = append(out, contracts.Message{Role: contracts.RoleSystem, Content: &s})
	}
	return out
}
func skill(ctx context.Context, ws string, r contracts.ResourceRef, resume bool) error {
	if len(r.SHA256) != 64 {
		return bad("resource_invalid_entry", "sha256")
	}
	if !safe(r.Name) || strings.Contains(r.Name, "/") {
		return bad("resource_unsafe_path", r.Name)
	}
	ad := filepath.Join(ws, ".skill-archives")
	os.MkdirAll(ad, 0755)
	ap := filepath.Join(ad, r.Name+".bin")
	if !resume {
		if e := download(ctx, r, ap); e != nil {
			return e
		}
	} else if h, e := fileHash(ap); e != nil || h != r.SHA256 {
		return bad("skill_rebuild_failed", "archive hash")
	}
	if st, e := os.Stat(ap); e != nil || st.Size() > limits.maxArchive {
		return bad("skill_limit_exceeded", "archive")
	}
	dst := filepath.Join(ws, ".skills", r.Name)
	os.MkdirAll(filepath.Dir(dst), 0755)
	tmp, e := os.MkdirTemp(filepath.Dir(dst), ".skill-staging-")
	if e != nil {
		return e
	}
	os.MkdirAll(tmp, 0755)
	var extractErr error
	if strings.EqualFold(filepath.Ext(r.Name), ".md") {
		extractErr = extractMarkdown(ap, tmp)
	} else {
		extractErr = extract(ap, tmp)
	}
	if extractErr != nil {
		os.RemoveAll(tmp)
		return extractErr
	}
	old := dst + ".old"
	os.RemoveAll(old)
	if _, e := os.Stat(dst); e == nil {
		os.Rename(dst, old)
	}
	if e := os.Rename(tmp, dst); e != nil {
		return e
	}
	os.RemoveAll(old)
	return nil
}
func extractMarkdown(path, dst string) error {
	st, e := os.Stat(path)
	if e != nil {
		return e
	}
	if st.Size() > maxSkillMD {
		return bad("skill_limit_exceeded", "SKILL.md")
	}
	in, e := os.Open(path)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.Create(filepath.Join(dst, "SKILL.md"))
	if e != nil {
		return e
	}
	_, copyErr := io.Copy(out, io.LimitReader(in, maxSkillMD+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if _, e := os.Stat(filepath.Join(dst, "SKILL.md")); e != nil {
		return e
	}
	return nil
}
func fileHash(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func extract(path, dst string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	h := make([]byte, 4)
	if _, e = io.ReadFull(f, h); e != nil {
		return bad("skill_unsupported_format", "short archive")
	}
	if string(h[:2]) == "PK" {
		z, e := zip.OpenReader(path)
		if e != nil {
			return e
		}
		defer z.Close()
		if len(z.File) > maxEntries {
			return bad("skill_limit_exceeded", "entries")
		}
		var total int64
		seen := map[string]bool{}
		for _, x := range z.File {
			name := strings.TrimSuffix(x.Name, "/")
			if name != "" && !safe(name) {
				return bad("skill_archive_unsafe", x.Name)
			}
			if seen[name] {
				return bad("skill_archive_unsafe", "duplicate")
			}
			seen[name] = true
			if x.FileInfo().Mode()&os.ModeSymlink != 0 {
				return bad("skill_archive_unsafe", "link")
			}
			if x.FileInfo().IsDir() {
				continue
			}
			if x.UncompressedSize64 > uint64(limits.maxMember) {
				return bad("skill_limit_exceeded", "member")
			}
			total += int64(x.UncompressedSize64)
			if total > limits.maxTotal {
				return bad("skill_limit_exceeded", "total")
			}
			p := filepath.Join(dst, x.Name)
			os.MkdirAll(filepath.Dir(p), 0755)
			in, e := x.Open()
			if e != nil {
				return e
			}
			out, e := os.Create(p)
			if e != nil {
				in.Close()
				return e
			}
			_, e = io.Copy(out, io.LimitReader(in, limits.maxMember+1))
			in.Close()
			out.Close()
			if e != nil {
				return e
			}
		}
	} else if h[0] == 0x1f && h[1] == 0x8b {
		f.Seek(0, 0)
		g, e := gzip.NewReader(f)
		if e != nil {
			return e
		}
		tr := tar.NewReader(g)
		n := 0
		seen := map[string]bool{}
		var total int64
		for {
			m, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			n++
			name := strings.TrimSuffix(m.Name, "/")
			if n > maxEntries || (name != "" && !safe(name)) || seen[name] {
				return bad("skill_archive_unsafe", m.Name)
			}
			seen[name] = true
			if m.FileInfo().IsDir() {
				os.MkdirAll(filepath.Join(dst, name), 0755)
				continue
			}
			if !m.FileInfo().Mode().IsRegular() {
				return bad("skill_archive_unsafe", m.Name)
			}
			p := filepath.Join(dst, m.Name)
			os.MkdirAll(filepath.Dir(p), 0755)
			if m.Size > limits.maxMember {
				return bad("skill_limit_exceeded", "member")
			}
			total += m.Size
			if total > limits.maxTotal {
				return bad("skill_limit_exceeded", "total")
			}
			o, e := os.Create(p)
			if e != nil {
				return e
			}
			_, e = io.Copy(o, io.LimitReader(tr, limits.maxMember+1))
			o.Close()
			if e != nil {
				return e
			}
		}
	} else {
		return bad("skill_unsupported_format", "format")
	}
	if _, e := os.Stat(filepath.Join(dst, "SKILL.md")); e != nil {
		es, _ := os.ReadDir(dst)
		if len(es) != 1 || !es[0].IsDir() {
			return bad("skill_missing_manifest", "SKILL.md")
		}
		if _, e = os.Stat(filepath.Join(dst, es[0].Name(), "SKILL.md")); e != nil {
			return bad("skill_missing_manifest", "SKILL.md")
		}
		inner := filepath.Join(dst, es[0].Name())
		tmp := dst + ".flatten"
		os.MkdirAll(tmp, 0755)
		innerEntries, _ := os.ReadDir(inner)
		for _, ent := range innerEntries {
			os.Rename(filepath.Join(inner, ent.Name()), filepath.Join(tmp, ent.Name()))
		}
		os.RemoveAll(dst)
		os.Rename(tmp, dst)
	}
	if st, e := os.Stat(filepath.Join(dst, "SKILL.md")); e != nil || st.Size() > maxSkillMD {
		return bad("skill_limit_exceeded", "SKILL.md")
	}
	return nil
}
