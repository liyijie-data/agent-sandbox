package contracts

import (
	"encoding/json"
	"strings"
	"time"
)

type RunStatus string

const (
	RunStatusQueued          RunStatus = "queued"
	RunStatusPreparing       RunStatus = "preparing"
	RunStatusRunning         RunStatus = "running"
	RunStatusAwaitingInput   RunStatus = "awaiting_input"
	RunStatusCancelRequested RunStatus = "cancel_requested"

	RunStatusPlatformRebasing RunStatus = "platform_rebasing"
	RunStatusSucceeded        RunStatus = "succeeded"
	RunStatusFailed           RunStatus = "failed"
	RunStatusCancelled        RunStatus = "cancelled"
	RunStatusExpired          RunStatus = "expired"
)

func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCancelled, RunStatusExpired:
		return true
	}
	return false
}

const (
	InputKindQuestion = "question"
	InputKindChoice   = "choice"
	InputKindApproval = "approval"
)

var validInputKinds = map[string]bool{InputKindQuestion: true, InputKindChoice: true, InputKindApproval: true}

type PendingInput struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Prompt  string   `json:"prompt"`
	Options []Choice `json:"options,omitempty"`

	ExpiresAt time.Time `json:"expires_at"`
}

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type DeliveryState string

const (
	DeliveryNotRequested DeliveryState = "not_requested"
	DeliveryEmpty        DeliveryState = "empty"
	DeliveryUploaded     DeliveryState = "uploaded"
)

type ResultSummary struct {
	Status        DeliveryState `json:"status"`
	DestinationID string        `json:"destination_id,omitempty"`
	SHA256        string        `json:"sha256,omitempty"`
	SizeBytes     int64         `json:"size_bytes,omitempty"`

	Summary string `json:"summary,omitempty"`

	ErrorCode    string               `json:"error_code,omitempty"`
	ErrorDetails *RuntimeErrorDetails `json:"error_details,omitempty"`
	Diagnostics  *DiagnosticOutcome   `json:"diagnostics,omitempty"`
}

type CleanupStatus string

const (
	CleanupNotDue    CleanupStatus = "not_due"
	CleanupPending   CleanupStatus = "pending"
	CleanupRunning   CleanupStatus = "running"
	CleanupCompleted CleanupStatus = "completed"
	CleanupFailed    CleanupStatus = "failed"
)

type RunResponse struct {
	ID               string         `json:"id"`
	ReqID            string         `json:"req_id"`
	Status           RunStatus      `json:"status"`
	Stage            int            `json:"stage"`
	ExpiresAt        time.Time      `json:"expires_at"`
	PendingInput     *PendingInput  `json:"pending_input,omitempty"`
	Result           *ResultSummary `json:"result,omitempty"`
	ContentAvailable bool           `json:"content_available"`
	ContentExpiresAt *time.Time     `json:"content_expires_at,omitempty"`
	CleanupStatus    CleanupStatus  `json:"cleanup_status"`

	QueuePosition *int    `json:"queue_position,omitempty"`
	QueueLength   *int    `json:"queue_length,omitempty"`
	WaitReason    *string `json:"wait_reason,omitempty"`
}

type AccessRefreshTargets struct {
	Model        *ModelAccess        `json:"model,omitempty"`
	Files        []ResourceAccess    `json:"files,omitempty"`
	Skills       []ResourceAccess    `json:"skills,omitempty"`
	ResultBundle *ResultBundleAccess `json:"result_bundle,omitempty"`
}

type ResourceAccess struct {
	ID          string    `json:"id"`
	DownloadURL string    `json:"download_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type ResultBundleAccess struct {
	UploadURL string    `json:"upload_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AnswerRequest struct {
	Answer        any                   `json:"answer"`
	AccessRefresh *AccessRefreshTargets `json:"access_refresh,omitempty"`
}

func DecodeAnswerRequest(data []byte) (*AnswerRequest, *validationError) {
	var req AnswerRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "answer decode failed: %v", err)
	}
	return &req, nil
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error   ErrorBody `json:"error"`
	ReqID   string    `json:"req_id,omitempty"`
	TraceID string    `json:"trace_id,omitempty"`
}
