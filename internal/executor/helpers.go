package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
)

func newReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func entryCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	cmd := strings.Join(argv, " ")
	if !strings.Contains(cmd, "--config") {
		cmd += " --config " + RunConfigPath
	}
	return cmd
}

func (e *Executor) resolveRuntimeEntry(ctx context.Context, runID string) string {
	entry, _, err := e.runs.LoadFrozenImageFacts(ctx, runID)
	if err != nil || len(entry) == 0 {
		return e.runtimeEntry
	}
	if cmd := entryCommand(entry); cmd != "" {
		return cmd
	}
	return e.runtimeEntry
}

func newInputID(runID string, stageNo int) string {
	sum := sha256.Sum256([]byte("input\x00" + runID + "\x00" + itoa(stageNo)))
	return "input_" + hex.EncodeToString(sum[:8])
}

func applyAccessRefresh(cfg *contracts.RuntimeConfig, ar *contracts.AccessRefreshTargets) {
	if ar == nil {
		return
	}
	if len(ar.Files) > 0 {
		byID := map[string]contracts.ResourceAccess{}
		for _, f := range ar.Files {
			byID[f.ID] = f
		}
		for i := range cfg.Files {
			if r, ok := byID[cfg.Files[i].ID]; ok {
				cfg.Files[i].DownloadURL = r.DownloadURL
				cfg.Files[i].ExpiresAt = r.ExpiresAt
			}
		}
	}
	if len(ar.Skills) > 0 {
		byID := map[string]contracts.ResourceAccess{}
		for _, sk := range ar.Skills {
			byID[sk.ID] = sk
		}
		for i := range cfg.Skills {
			if r, ok := byID[cfg.Skills[i].ID]; ok {
				cfg.Skills[i].DownloadURL = r.DownloadURL
				cfg.Skills[i].ExpiresAt = r.ExpiresAt
			}
		}
	}
	if ar.ResultBundle != nil && cfg.ResultBundle != nil {
		cfg.ResultBundle.UploadURL = ar.ResultBundle.UploadURL
		cfg.ResultBundle.ExpiresAt = ar.ResultBundle.ExpiresAt
	}
}

func (e *Executor) defaultProvision(ctx context.Context, in ProvisionIn) (*contracts.RuntimeConfig, error) {
	cfg := &contracts.RuntimeConfig{
		RunID:       in.RunID,
		Stage:       in.StageNo,
		Fence:       in.Fence,
		ExecutionID: in.ExecutionID,
		Limits:      contracts.RuntimeLimits{},
	}

	configJSON, err := e.runs.LoadFrozenConfig(ctx, in.RunID)
	if err != nil {
		return nil, fmt.Errorf("executor: load frozen config: %w", err)
	}
	if len(configJSON) > 0 {
		var frozen contracts.CreateRunRequest
		if err := json.Unmarshal(configJSON, &frozen); err != nil {
			return nil, fmt.Errorf("executor: decode frozen config: %w", err)
		}
		cfg.Messages = frozen.Messages
		cfg.Model = contracts.RuntimeModelSpec{
			Name:                frozen.Model.Name,
			BaseURL:             frozen.Model.BaseURL,
			ReasoningEffort:     frozen.Model.ReasoningEffort,
			ContextWindowTokens: frozen.Model.ContextWindowTokens,
			MaxOutputTokens:     frozen.Model.MaxOutputTokens,
			Parameters:          cloneModelParameters(frozen.Model.Parameters),
		}
		cfg.Files = frozen.Files
		cfg.Skills = frozen.Skills
		cfg.Tools = frozen.Tools
		cfg.ResultBundle = frozen.ResultBundle
	}

	if ar, arerr := e.runs.LoadAccessRefresh(ctx, in.RunID); arerr != nil {
		return nil, fmt.Errorf("executor: load access refresh: %w", arerr)
	} else if ar != nil {
		applyAccessRefresh(cfg, ar)
	}

	if in.Resume != nil {
		var answer any
		if err := json.Unmarshal(in.Resume.Answer, &answer); err != nil {
			return nil, fmt.Errorf("executor: resume answer is not JSON: %w", err)
		}
		cfg.Resume = &contracts.ResumeRef{
			CheckpointPath: InputCheckpoint,
			InputID:        in.Resume.InputID,
			Answer:         answer,
		}
	}
	return cfg, nil
}

func cloneModelParameters(in contracts.ModelParameters) contracts.ModelParameters {
	if in == nil {
		return nil
	}
	b, _ := json.Marshal(in)
	var out contracts.ModelParameters
	_ = json.Unmarshal(b, &out)
	return out
}
