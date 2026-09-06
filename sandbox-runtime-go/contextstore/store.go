package contextstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"
)

const (
	DefaultMaxObject = 1 << 20
	DefaultMaxTotal  = 8 << 20
)

type Ref struct {
	Path, SHA256 string
	SizeBytes    int
}

type Store struct {
	Root          string
	MaxObjectByte int
	MaxTotalByte  int
	mu            sync.Mutex
}

func (s *Store) Save(content string) (Ref, error) {
	if s == nil {
		return Ref{}, errors.New("nil context store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !utf8.ValidString(content) {
		return Ref{}, errors.New("context result is not valid UTF-8")
	}
	maxObject := s.MaxObjectByte
	if maxObject <= 0 {
		maxObject = DefaultMaxObject
	}
	if len(content) > maxObject {
		return Ref{}, fmt.Errorf("context result exceeds object limit")
	}
	maxTotal := s.MaxTotalByte
	if maxTotal <= 0 {
		maxTotal = DefaultMaxTotal
	}
	sum := sha256.Sum256([]byte(content))
	hexsum := hex.EncodeToString(sum[:])
	dir := filepath.Join(s.Root, ".context", "tool-results")
	if err := ensureDir(filepath.Join(s.Root, ".context")); err != nil {
		return Ref{}, err
	}
	if err := ensureDir(dir); err != nil {
		return Ref{}, err
	}
	path := filepath.Join(dir, hexsum+".txt")
	if b, err := readBounded(path, maxObject); err == nil {
		if string(b) != content {
			return Ref{}, errors.New("context hash collision")
		}
		return Ref{Path: filepath.ToSlash(filepath.Join(".context", "tool-results", hexsum+".txt")), SHA256: hexsum, SizeBytes: len(b)}, nil
	} else if !os.IsNotExist(err) {
		return Ref{}, err
	}
	var total int64
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Ref{}, err
	}
	for _, ent := range ents {
		if ent.IsDir() || ent.Type()&os.ModeSymlink != 0 || !ent.Type().IsRegular() {
			continue
		}
		info, e := ent.Info()
		if e != nil {
			return Ref{}, e
		}
		total += info.Size()
	}
	if total+int64(len(content)) > int64(maxTotal) {
		return Ref{}, errors.New("context store total limit exceeded")
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return Ref{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = io.WriteString(tmp, content)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Ref{}, err
	}
	if err = os.Rename(tmpName, path); err != nil {
		if b, readErr := readBounded(path, maxObject); readErr == nil && string(b) == content {
			return Ref{Path: filepath.ToSlash(filepath.Join(".context", "tool-results", hexsum+".txt")), SHA256: hexsum, SizeBytes: len(content)}, nil
		}
		return Ref{}, err
	}
	check, err := readBounded(path, maxObject)
	if err != nil || len(check) != len(content) || string(check) != content {
		if err == nil {
			err = errors.New("context hash verification failed")
		}
		return Ref{}, err
	}
	return Ref{Path: filepath.ToSlash(filepath.Join(".context", "tool-results", hexsum+".txt")), SHA256: hexsum, SizeBytes: len(content)}, nil
}

func ensureDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("context store directory is unsafe")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.Mkdir(path, 0700)
}

func readBounded(path string, max int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("context store target is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, errors.New("context store object exceeds bound")
	}
	return b, nil
}
