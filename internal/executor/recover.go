package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
)

type RecoveryStore interface {
	Put(ctx context.Context, r io.Reader, size int64, wantSHA string) (objectRef string, err error)

	Get(ctx context.Context, objectRef string) ([]byte, error)

	Delete(ctx context.Context, objectRef string) error

	MaxBytes() int64

	Exists(ctx context.Context, objectRef string) (bool, error)
}

const MaxRecoveryBytes = 512 << 20

type DevMemoryRecoveryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func NewDevMemoryRecoveryStore() *DevMemoryRecoveryStore {
	return &DevMemoryRecoveryStore{objects: map[string][]byte{}}
}

func (m *DevMemoryRecoveryStore) Put(ctx context.Context, r io.Reader, size int64, wantSHA string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) != size {
		return "", fmt.Errorf("dev recovery store: size mismatch got %d want %d", len(data), size)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.ToLower(wantSHA) {
		return "", fmt.Errorf("dev recovery store: hash mismatch")
	}
	ref := fmt.Sprintf("dev://cp/%s", hex.EncodeToString(sum[:]))
	m.mu.Lock()
	m.objects[ref] = data
	m.mu.Unlock()
	return ref, nil
}

func (m *DevMemoryRecoveryStore) Get(ctx context.Context, objectRef string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.objects[objectRef]
	if !ok {
		return nil, fmt.Errorf("dev recovery store: %s not found", objectRef)
	}
	return d, nil
}

func (m *DevMemoryRecoveryStore) Delete(ctx context.Context, objectRef string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, objectRef)
	return nil
}

func (m *DevMemoryRecoveryStore) MaxBytes() int64 { return MaxRecoveryBytes }

func (m *DevMemoryRecoveryStore) Exists(ctx context.Context, objectRef string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[objectRef]
	return ok, nil
}

func (e *Executor) persistCheckpoint(ctx context.Context, pod Pod, wantPath, wantSHA string, wantSize int64, identity checkpointIdentity) (string, error) {
	raw, err := pod.Read(ctx, wantPath)
	if err != nil {
		return "", fmt.Errorf("executor: read checkpoint from pod: %w", err)
	}
	if int64(len(raw)) != wantSize {
		return "", fmt.Errorf("executor: checkpoint size mismatch got %d want %d", len(raw), wantSize)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != wantSHA {
		return "", fmt.Errorf("executor: checkpoint hash mismatch")
	}

	if err := validateCheckpointPackage(raw, identity, e.recovery.MaxBytes()); err != nil {
		return "", err
	}
	ref, err := e.recovery.Put(ctx, newReader(raw), int64(len(raw)), wantSHA)
	if err != nil {
		return "", fmt.Errorf("executor: persist checkpoint: %w", err)
	}
	return ref, nil
}

type checkpointIdentity struct {
	RunID       string
	Stage       int
	Fence       int64
	StateFormat string
}

func validateCheckpointPackage(raw []byte, identity checkpointIdentity, maxBytes int64) error {
	const (
		manifestName = "manifest.json"
		stateName    = "agent/state.bin"
	)
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("executor: checkpoint is not gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)

	var manifest map[string]any
	gotManifest := false
	gotState := false
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("executor: checkpoint tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return fmt.Errorf("executor: checkpoint contains unsupported member type %q", hdr.Name)
		}
		switch hdr.Name {
		case manifestName:
			if hdr.Typeflag != tar.TypeReg {
				return fmt.Errorf("executor: checkpoint manifest must be a regular file")
			}
			lim := io.LimitReader(tr, 1<<20)
			b, err := io.ReadAll(lim)
			if err != nil {
				return fmt.Errorf("executor: read checkpoint manifest: %w", err)
			}
			if err := json.Unmarshal(b, &manifest); err != nil {
				return fmt.Errorf("executor: checkpoint manifest is not valid JSON: %w", err)
			}
			gotManifest = true
		case stateName:
			gotState = true
		}

		if hdr.Typeflag == tar.TypeReg {
			if hdr.Size > maxBytes-total {
				return fmt.Errorf("executor: checkpoint exceeds unpack budget")
			}
			total += hdr.Size

		}
	}
	if !gotManifest || !gotState {
		return fmt.Errorf("executor: checkpoint package missing manifest.json or agent/state.bin")
	}
	if identity.StateFormat != "" {
		if sf, _ := manifest["state_format"].(string); sf == "" {
			return fmt.Errorf("executor: checkpoint manifest missing state_format")
		} else if sf != identity.StateFormat {
			return fmt.Errorf("executor: checkpoint state_format mismatch: %q != %q", sf, identity.StateFormat)
		}
	}
	if identity.RunID != "" {
		manifestRun, _ := manifest["run_id"].(string)
		manifestStage, okStage := manifest["stage"].(float64)
		manifestFence, okFence := manifest["fence"].(float64)
		if manifestRun != identity.RunID || !okStage || manifestStage != math.Trunc(manifestStage) ||
			manifestStage != float64(identity.Stage) || !okFence || manifestFence != math.Trunc(manifestFence) ||
			manifestFence != float64(identity.Fence) {
			return fmt.Errorf("executor: checkpoint manifest identity mismatch")
		}
	}
	return nil
}
