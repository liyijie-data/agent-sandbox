package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
)

type RuntimeResult struct {
	Status     string         `json:"status"`
	Summary    string         `json:"summary,omitempty"`
	Artifacts  []ArtifactRef  `json:"artifacts,omitempty"`
	Checkpoint *CheckpointRef `json:"checkpoint,omitempty"`
	Error      string         `json:"error,omitempty"`

	ConversationSummary string `json:"conversation_summary,omitempty"`

	Request     *RunInputRequest `json:"request,omitempty"`
	ResumeState json.RawMessage  `json:"resume_state,omitempty"`
}

type RunInputRequest struct {
	Kind    string          `json:"kind"`
	Prompt  string          `json:"prompt"`
	Options json.RawMessage `json:"options,omitempty"`
	Context json.RawMessage `json:"context,omitempty"`
}

type ArtifactRef struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	UploadURL string `json:"upload_url,omitempty"`
	ObjectKey string `json:"object_key,omitempty"`
}

type CheckpointRef struct {
	SHA256    string `json:"sha256"`
	ObjectKey string `json:"object_key,omitempty"`
	UploadURL string `json:"upload_url,omitempty"`
}

func CollectResult(ctx context.Context, sb Handle) (*RuntimeResult, error) {
	data, err := sb.Read(ctx, "/app/output/result.json")
	if err != nil {
		return nil, fmt.Errorf("read result: %w", err)
	}
	var out RuntimeResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode result: %w", err)
	}
	return &out, nil
}
