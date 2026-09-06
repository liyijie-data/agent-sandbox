package fsutil

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func SafePath(root, rel string) (string, error) {
	var err error
	rel, err = url.PathUnescape(rel)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.IndexByte(rel, 0) >= 0 || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path escapes sandbox root")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes sandbox root")
	}
	rr, _ := filepath.Abs(root)
	p, _ := filepath.Abs(filepath.Join(root, clean))
	if p != rr && !strings.HasPrefix(p, rr+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes sandbox root")
	}

	cur := rr
	for _, component := range strings.Split(clean, string(os.PathSeparator)) {
		if component == "." || component == "" {
			continue
		}
		cur = filepath.Join(cur, component)
		if fi, e := os.Lstat(cur); e == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path component")
		}
	}
	return p, nil
}

func SafeRel(rel string) (string, error) {
	decoded, err := url.PathUnescape(rel)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	if strings.IndexByte(decoded, 0) >= 0 || strings.Contains(decoded, "\\") || filepath.IsAbs(decoded) {
		return "", fmt.Errorf("path escapes sandbox root")
	}
	clean := filepath.ToSlash(filepath.Clean(decoded))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes sandbox root")
	}
	return clean, nil
}
