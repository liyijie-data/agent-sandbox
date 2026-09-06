package recovery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"agent-platform/internal/executor"
	"agent-platform/internal/storage"
)

type objectStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	GetBytes(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
	Verify(context.Context, string, string, int64) error
	Exists(context.Context, string) (bool, error)
}

type ObjectStoreAdapter struct {
	objects  objectStore
	maxBytes int64
}

var _ executor.RecoveryStore = (*ObjectStoreAdapter)(nil)

func NewObjectStoreAdapter(objects *storage.ObjectStore, maxBytes int64) (*ObjectStoreAdapter, error) {
	if objects == nil {
		return nil, fmt.Errorf("recovery store: object store is required")
	}
	return NewAdapter(objects, maxBytes)
}

func NewAdapter(objects objectStore, maxBytes int64) (*ObjectStoreAdapter, error) {
	if objects == nil {
		return nil, fmt.Errorf("recovery store: object store is required")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("recovery store: max bytes must be positive")
	}
	return &ObjectStoreAdapter{objects: objects, maxBytes: maxBytes}, nil
}

func (s *ObjectStoreAdapter) MaxBytes() int64 { return s.maxBytes }

func (s *ObjectStoreAdapter) Put(ctx context.Context, r io.Reader, size int64, wantSHA string) (string, error) {
	if size <= 0 || size > s.maxBytes {
		return "", fmt.Errorf("recovery store: package size is invalid")
	}
	wantSHA = strings.ToLower(strings.TrimSpace(wantSHA))
	if len(wantSHA) != sha256.Size*2 {
		return "", fmt.Errorf("recovery store: package hash is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(r, s.maxBytes+1))
	if err != nil || int64(len(data)) != size {
		return "", fmt.Errorf("recovery store: package could not be read")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != wantSHA {
		return "", fmt.Errorf("recovery store: package integrity check failed")
	}
	identity, err := manifestIdentity(data)
	if err != nil {
		return "", err
	}
	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", fmt.Errorf("recovery store: package reference unavailable")
	}

	runDigest := sha256.Sum256([]byte(identity.runID))
	key := "recovery/v1/" + hex.EncodeToString(runDigest[:]) + "/" +
		strconv.Itoa(identity.stage) + "/" + strconv.FormatInt(identity.fence, 10) + "/" +
		wantSHA + "/" + hex.EncodeToString(nonce[:])
	if err := s.objects.Put(ctx, key, bytes.NewReader(data), size, "application/gzip"); err != nil {
		return "", fmt.Errorf("recovery store: package write failed")
	}
	if err := s.objects.Verify(ctx, key, wantSHA, size); err != nil {
		_ = s.objects.Delete(ctx, key)
		return "", fmt.Errorf("recovery store: package verification failed")
	}
	return key, nil
}

func (s *ObjectStoreAdapter) Get(ctx context.Context, objectRef string) ([]byte, error) {
	if !validRef(objectRef) {
		return nil, fmt.Errorf("recovery store: package reference is invalid")
	}
	data, err := s.objects.GetBytes(ctx, objectRef)
	if err != nil || int64(len(data)) > s.maxBytes {
		return nil, fmt.Errorf("recovery store: package unavailable")
	}
	return data, nil
}

func (s *ObjectStoreAdapter) Exists(ctx context.Context, objectRef string) (bool, error) {
	if !validRef(objectRef) {
		return false, nil
	}
	ok, err := s.objects.Exists(ctx, objectRef)
	if err != nil {
		return false, fmt.Errorf("recovery store: package availability check failed")
	}
	return ok, nil
}

func (s *ObjectStoreAdapter) Delete(ctx context.Context, objectRef string) error {
	if !validRef(objectRef) {
		return fmt.Errorf("recovery store: package reference is invalid")
	}
	if err := s.objects.Delete(ctx, objectRef); err != nil {
		return fmt.Errorf("recovery store: package deletion failed")
	}
	return nil
}

func validRef(ref string) bool {
	if !strings.HasPrefix(ref, "recovery/v1/") || strings.ContainsAny(ref, "\r\n") || len(ref) >= 512 {
		return false
	}
	parts := strings.Split(ref, "/")
	return len(parts) == 7 && len(parts[2]) == sha256.Size*2 && len(parts[5]) == sha256.Size*2 &&
		len(parts[6]) == 32 && !strings.Contains(ref, "..")
}

type packageIdentity struct {
	runID string
	stage int
	fence int64
}

func manifestIdentity(raw []byte) (packageIdentity, error) {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return packageIdentity{}, fmt.Errorf("recovery store: package format is invalid")
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return packageIdentity{}, fmt.Errorf("recovery store: package format is invalid")
		}
		if h.Name != "manifest.json" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return packageIdentity{}, fmt.Errorf("recovery store: package manifest is invalid")
		}
		var m struct {
			RunID string `json:"run_id"`
			Stage int    `json:"stage"`
			Fence int64  `json:"fence"`
		}
		if json.Unmarshal(b, &m) != nil || m.RunID == "" || m.Stage < 1 || m.Fence < 0 {
			return packageIdentity{}, fmt.Errorf("recovery store: package identity is invalid")
		}
		return packageIdentity{runID: m.RunID, stage: m.Stage, fence: m.Fence}, nil
	}
	return packageIdentity{}, fmt.Errorf("recovery store: package manifest is missing")
}
