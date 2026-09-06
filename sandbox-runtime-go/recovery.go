package runtime

import (
	"agent-platform/sandbox-runtime-go/checkpoint"
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func checkpointHash(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}

func restoreCheckpoint(ctx context.Context, path, root string, cfgRun string, stage int, fence int64, versions map[string]string, cursor, maxBytes int64) (checkpoint.State, error) {
	f, e := os.Open(path)
	if e != nil {
		return checkpoint.State{}, e
	}
	defer f.Close()
	g, e := gzip.NewReader(f)
	if e != nil {
		return checkpoint.State{}, e
	}
	defer g.Close()
	t := tar.NewReader(io.LimitReader(g, 512<<20))
	var m checkpoint.Manifest
	for {
		h, e := t.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return checkpoint.State{}, e
		}
		if h.Name == "manifest.json" {
			b, e := io.ReadAll(io.LimitReader(t, 1<<20))
			if e != nil {
				return checkpoint.State{}, e
			}
			if json.Unmarshal(b, &m) != nil {
				return checkpoint.State{}, fmt.Errorf("invalid checkpoint manifest")
			}
			break
		}
	}
	if m.RunID != cfgRun || m.Stage != stage-1 || m.Fence <= 0 || m.Fence > fence {
		return checkpoint.State{}, fmt.Errorf("checkpoint identity mismatch")
	}
	o := checkpoint.Options{RunID: cfgRun, Stage: m.Stage, Fence: m.Fence, Cursor: cursor, PluginVersions: versions, MaxBytes: maxBytes}
	probe, e := checkpoint.Restore(path, o)
	if e != nil {
		return checkpoint.State{}, e
	}
	var saved struct {
		Engine struct {
			Cursor int64 `json:"cursor"`
		} `json:"engine"`
	}
	if json.Unmarshal(probe.Data, &saved) != nil || saved.Engine.Cursor != cursor {
		return checkpoint.State{}, fmt.Errorf("checkpoint cursor mismatch")
	}
	return checkpoint.RestoreTo(ctx, path, root, o)
}
