package contracts

import (
	"encoding/json"
	"strings"
)

type RuntimeConfig struct {
	ContractVersion string `json:"contract_version"`
	RunID           string `json:"run_id"`
	Stage           int    `json:"stage"`
	Fence           int64  `json:"fence"`
	ExecutionID     string `json:"execution_id"`

	Messages []Message        `json:"messages"`
	Model    RuntimeModelSpec `json:"model"`
	Runtime  RuntimeEndpoint  `json:"runtime"`

	Resume *ResumeRef `json:"resume,omitempty"`

	Limits RuntimeLimits `json:"limits"`

	Steering *RuntimeSteeringConfig `json:"steering,omitempty"`

	Files        []ResourceRef `json:"files,omitempty"`
	Skills       []ResourceRef `json:"skills,omitempty"`
	Tools        []ToolSpec    `json:"tools,omitempty"`
	ResultBundle *ResultBundle `json:"result_bundle,omitempty"`
}

type RuntimeModelSpec struct {
	Name                string          `json:"name"`
	BaseURL             string          `json:"base_url"`
	Token               string          `json:"token"`
	ReasoningEffort     *string         `json:"reasoning_effort,omitempty"`
	ContextWindowTokens *int            `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     *int            `json:"max_output_tokens,omitempty"`
	Parameters          ModelParameters `json:"parameters,omitempty"`
}

type RuntimeEndpoint struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

type ResumeRef struct {
	CheckpointPath string `json:"checkpoint_path"`
	InputID        string `json:"input_id"`
	Answer         any    `json:"answer"`
}

type RuntimeLimits struct {
	RemainingExecutionSeconds int64 `json:"remaining_execution_seconds"`
	CheckpointMaxBytes        int64 `json:"checkpoint_max_bytes"`
}

type RuntimeSteeringConfig struct {
	AfterSeq int64 `json:"after_seq"`
}

func DecodeRuntimeConfig(data []byte) (*RuntimeConfig, *validationError) {
	var cfg RuntimeConfig
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json decode failed: %v", err)
	}
	if cfg.ContractVersion != RuntimeContractVersion {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "unsupported contract_version %q", cfg.ContractVersion)
	}
	if cfg.ExecutionID == "" || cfg.RunID == "" || cfg.Stage < 1 {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json missing run/stage/execution identity")
	}

	if cfg.Model.Name == "" || cfg.Model.BaseURL == "" || cfg.Model.Token == "" {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json.model requires name, base_url, token")
	}
	if cfg.Model.ContextWindowTokens != nil && (*cfg.Model.ContextWindowTokens <= 0 || *cfg.Model.ContextWindowTokens > MaxModelTokens) {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json.model.context_window_tokens out of range")
	}
	if cfg.Model.MaxOutputTokens != nil && (*cfg.Model.MaxOutputTokens <= 0 || *cfg.Model.MaxOutputTokens > MaxModelTokens) {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json.model.max_output_tokens out of range")
	}
	if cfg.Model.ContextWindowTokens != nil && cfg.Model.MaxOutputTokens != nil && *cfg.Model.ContextWindowTokens <= *cfg.Model.MaxOutputTokens {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json.model.context_window_tokens must exceed max_output_tokens")
	}
	if e := ValidateModelParameters(cfg.Model.Parameters, cfg.Model.ReasoningEffort, cfg.Model.MaxOutputTokens, cfg.Model.ContextWindowTokens); e != nil {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "%s", e.Error())
	}
	if cfg.Runtime.BaseURL == "" || cfg.Runtime.Token == "" {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json.runtime requires base_url, token")
	}
	if cfg.Limits.RemainingExecutionSeconds <= 0 {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "run.json.limits.remaining_execution_seconds must be > 0")
	}
	return &cfg, nil
}

type RuntimeResultStatus string

const (
	RuntimeOk            RuntimeResultStatus = "ok"
	RuntimeAwaitingInput RuntimeResultStatus = "awaiting_input"
	RuntimeError         RuntimeResultStatus = "error"
)

type RuntimeResult struct {
	Status RuntimeResultStatus `json:"status"`

	Summary     string             `json:"summary,omitempty"`
	Delivery    DeliveryOutcome    `json:"delivery,omitempty"`
	Diagnostics *DiagnosticOutcome `json:"diagnostics,omitempty"`

	Request    *InputRequest  `json:"request,omitempty"`
	Checkpoint *CheckpointRef `json:"checkpoint,omitempty"`

	Steering *RuntimeSteeringResult `json:"steering,omitempty"`

	ErrorCode    string               `json:"error_code,omitempty"`
	ErrorType    string               `json:"error_type,omitempty"`
	ErrorDetails *RuntimeErrorDetails `json:"error_details,omitempty"`
}

type DiagnosticOutcome struct {
	Status        string `json:"status"` // uploaded | unavailable | disabled
	Reason        string `json:"reason,omitempty"`
	DestinationID string `json:"destination_id,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	Incomplete    bool   `json:"incomplete,omitempty"`
}

type RuntimeErrorDetails struct {
	Phase          string `json:"phase,omitempty"`
	ReasonCode     string `json:"reason_code,omitempty"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	UserMessage    string `json:"user_message,omitempty"`
}

type InputRequest struct {
	Kind    string   `json:"kind"`
	Prompt  string   `json:"prompt"`
	Options []Choice `json:"options,omitempty"`
}

type CheckpointRef struct {
	Path      string `json:"path"`
	Format    string `json:"format"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type DeliveryOutcome struct {
	Status        DeliveryState `json:"status"`
	DestinationID string        `json:"destination_id,omitempty"`
	SHA256        string        `json:"sha256,omitempty"`
	SizeBytes     int64         `json:"size_bytes,omitempty"`
}

type RuntimeSteeringResult struct {
	IncorporatedThroughSeq int64 `json:"incorporated_through_seq"`
}

func (r *RuntimeResult) ExitCodeFor() int {
	if r.Status == RuntimeError {
		return 1
	}
	return 0
}

type Manifest struct {
	ContractVersion string   `json:"contract_version"`
	ImageVersion    string   `json:"image_version,omitempty"`
	Entrypoint      []string `json:"entrypoint"`
	Capabilities    []string `json:"capabilities,omitempty"`
	StateFormat     string   `json:"state_format,omitempty"`
}

func DecodeManifest(data []byte) (*Manifest, *validationError) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "manifest decode failed: %v", err)
	}
	if m.ContractVersion != RuntimeContractVersion {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "unsupported manifest contract_version %q", m.ContractVersion)
	}
	if len(m.Entrypoint) == 0 || m.Entrypoint[0] == "" {
		return nil, validationErrorf(ErrRuntimeProtocolInvalid, "manifest entrypoint must be a non-empty argv array")
	}
	for _, f := range m.Entrypoint {
		if strings.ContainsAny(f, ";&|`\\\n\r") {
			return nil, validationErrorf(ErrRuntimeProtocolInvalid, "entrypoint argv must not contain shell metacharacters")
		}
	}
	if err := ValidateManifestCapabilities(m.Capabilities); err != nil {
		return nil, err
	}
	return &m, nil
}

func ValidateManifestCapabilities(caps []string) *validationError {
	seen := map[string]bool{}
	for _, c := range caps {
		if !KnownCapabilities[c] {
			return validationErrorf(ErrRuntimeProtocolInvalid, "manifest declares unknown capability %q", c)
		}
		if seen[c] {
			return validationErrorf(ErrRuntimeProtocolInvalid, "manifest duplicates capability %q", c)
		}
		seen[c] = true
	}
	for _, core := range LegacyCoreCapabilities {
		if !seen[core] {
			return validationErrorf(ErrRuntimeProtocolInvalid, "manifest missing required core capability %q", core)
		}
	}
	return nil
}

func ValidateCompleteManifestCapabilities(caps []string) *validationError {
	if err := ValidateManifestCapabilities(caps); err != nil {
		return err
	}
	seen := make(map[string]bool, len(caps))
	for _, c := range caps {
		seen[c] = true
	}
	for _, core := range CoreCapabilities {
		if !seen[core] {
			return validationErrorf(ErrRuntimeProtocolInvalid, "manifest missing required capability %q", core)
		}
	}
	return nil
}
