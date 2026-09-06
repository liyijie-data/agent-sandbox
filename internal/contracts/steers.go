package contracts

import (
	"encoding/json"
	"strings"
	"time"
)

type SteerStatus string

const (
	SteerPending      SteerStatus = "pending"
	SteerIncorporated SteerStatus = "incorporated"
	SteerNotApplied   SteerStatus = "not_applied"
	SteerUnknown      SteerStatus = "unknown"
)

func ValidSteerStatus(s string) bool {
	switch SteerStatus(s) {
	case SteerPending, SteerIncorporated, SteerNotApplied, SteerUnknown:
		return true
	}
	return false
}

type SteerReasonCode string

const (
	SteerReasonRunFinished    SteerReasonCode = "run_finished"
	SteerReasonRunCancelled   SteerReasonCode = "run_cancelled"
	SteerReasonRunExpired     SteerReasonCode = "run_expired"
	SteerReasonOutcomeUnknown SteerReasonCode = "execution_outcome_unknown"
)

func ValidSteerReasonCode(r string) bool {
	switch SteerReasonCode(r) {
	case SteerReasonRunFinished, SteerReasonRunCancelled, SteerReasonRunExpired,
		SteerReasonOutcomeUnknown:
		return true
	}
	return r == ""
}

type SteerRequest struct {
	SteerID string  `json:"steer_id"`
	Message Message `json:"message"`
}

type SteerReceipt struct {
	RunID          string           `json:"run_id"`
	SteerID        string           `json:"steer_id"`
	Seq            int64            `json:"seq"`
	Status         SteerStatus      `json:"status"`
	ReasonCode     *SteerReasonCode `json:"reason_code"`
	AcceptedAt     time.Time        `json:"accepted_at"`
	IncorporatedAt *time.Time       `json:"incorporated_at"`
	Stage          int              `json:"stage,omitempty"`
}

func DecodeSteerRequest(data []byte) (*SteerRequest, *validationError) {
	if len(data) > SteerRequestMaxBytes {
		return nil, validationErrorf(ErrPayloadTooLarge, "steer request exceeds %d bytes", SteerRequestMaxBytes)
	}
	var req SteerRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidSteer, "steer decode failed: %v", err)
	}
	if err := ValidateSteerRequest(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func ValidateSteerRequest(req *SteerRequest) *validationError {
	if len(req.SteerID) == 0 || len(req.SteerID) > SteerIDMaxLen ||
		!reqIDRe.MatchString(req.SteerID) {
		return validationErrorf(ErrInvalidSteer, "steer_id must be 1-%d chars [A-Za-z0-9._:-]", SteerIDMaxLen)
	}

	m := req.Message
	if m.Role != RoleUser {
		return validationErrorf(ErrInvalidSteer, "steer message role must be %q", RoleUser)
	}
	if m.Content == nil || strings.TrimSpace(*m.Content) == "" {
		return validationErrorf(ErrInvalidSteer, "steer message content must be a non-empty user text")
	}
	if len([]byte(*m.Content)) > SteerContentMaxBytes {
		return validationErrorf(ErrPayloadTooLarge, "steer content exceeds %d bytes", SteerContentMaxBytes)
	}
	if len(m.ToolCalls) > 0 || m.ToolCallID != nil {
		return validationErrorf(ErrInvalidSteer, "steer message must not carry tool_calls or tool_call_id")
	}
	return nil
}

func DecodeSteerPullRequest(data []byte) (*SteerPullRequest, *validationError) {
	var req SteerPullRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer pull decode failed: %v", err)
	}
	if req.ExecutionID == "" {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer pull requires execution_id")
	}
	if req.AfterSeq < 0 {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer pull after_seq must be >= 0")
	}
	return &req, nil
}

type SteerPullRequest struct {
	ExecutionID string `json:"execution_id"`
	AfterSeq    int64  `json:"after_seq"`
}

type SteerItem struct {
	SteerID string  `json:"steer_id"`
	Seq     int64   `json:"seq"`
	Message Message `json:"message"`
}

type SteerPullResponse struct {
	BatchID    *string     `json:"batch_id"`
	ThroughSeq int64       `json:"through_seq"`
	Items      []SteerItem `json:"items"`
}

func DecodeSteerAckRequest(data []byte) (*SteerAckRequest, *validationError) {
	var req SteerAckRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer ack decode failed: %v", err)
	}
	if req.ExecutionID == "" {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer ack requires execution_id")
	}
	if req.BatchID == "" {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer ack requires batch_id")
	}
	if req.IncorporatedThroughSeq < 1 {
		return nil, validationErrorf(ErrSteeringCursorConflict, "steer ack incorporated_through_seq must be >= 1")
	}
	return &req, nil
}

type SteerAckRequest struct {
	ExecutionID            string `json:"execution_id"`
	BatchID                string `json:"batch_id"`
	IncorporatedThroughSeq int64  `json:"incorporated_through_seq"`
}

type SteerAckResponse struct {
	IncorporatedThroughSeq int64 `json:"incorporated_through_seq"`
}
