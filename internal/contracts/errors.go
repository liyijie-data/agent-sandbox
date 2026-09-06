package contracts

import "fmt"

type ErrorCode string

const (
	ErrInvalidRequest  = ErrorCode("invalid_request")
	ErrInvalidMessages = ErrorCode("invalid_messages")

	ErrReqIDConflict = ErrorCode("req_id_conflict")

	ErrSandboxImageUnavailable = ErrorCode("sandbox_image_unavailable")
	ErrCapabilityUnsupported   = ErrorCode("capability_unsupported")

	ErrNetworkPolicyRejected  = ErrorCode("network_policy_rejected")
	ErrUpstreamTargetRejected = ErrorCode("upstream_target_rejected")

	ErrNetworkRolloutBlocked = ErrorCode("network_rollout_blocked")

	ErrPlatformNetworkPolicyChanged = ErrorCode("platform_network_policy_changed")

	ErrConfigStateConflict = ErrorCode("config_state_conflict")

	ErrAnswerConflict   = ErrorCode("answer_conflict")
	ErrRunStateConflict = ErrorCode("run_state_conflict")

	ErrInputExpired  = ErrorCode("input_expired")
	ErrEventsExpired = ErrorCode("events_expired")
	ErrEventGap      = ErrorCode("event_gap")

	ErrInvalidAccessRefresh  = ErrorCode("invalid_access_refresh")
	ErrResourceAccessExpired = ErrorCode("resource_access_expired")

	ErrCheckpointPersistFailed = ErrorCode("checkpoint_persist_failed")
	ErrCheckpointInvalid       = ErrorCode("checkpoint_invalid")
	ErrResumeFailed            = ErrorCode("resume_failed")

	ErrExecutionOutcomeUnknown = ErrorCode("execution_outcome_unknown")

	ErrResultBundleUploadFailed    = ErrorCode("result_bundle_upload_failed")
	ErrArtifactDestinationRequired = ErrorCode("artifact_destination_required")

	ErrRuntimeProtocolInvalid = ErrorCode("runtime_protocol_invalid")
	ErrAgentExecutionFailed   = ErrorCode("agent_execution_failed")

	ErrRunCancelled     = ErrorCode("run_cancelled")
	ErrRunExpired       = ErrorCode("run_expired")
	ErrExecutionTimeout = ErrorCode("execution_timeout")

	ErrResourceLimitExceeded = ErrorCode("resource_limit_exceeded")
	ErrEventDeliveryFailed   = ErrorCode("event_delivery_failed")

	ErrPayloadTooLarge = ErrorCode("payload_too_large")
)

const (
	ErrInvalidSteer               = ErrorCode("invalid_steer")
	ErrSteerConflict              = ErrorCode("steer_conflict")
	ErrSteerLimitExceeded         = ErrorCode("steer_limit_exceeded")
	ErrSteeringCursorConflict     = ErrorCode("steering_cursor_conflict")
	ErrRuntimeStateConflict       = ErrorCode("runtime_state_conflict")
	ErrSteeringDeliveryFailed     = ErrorCode("steering_delivery_failed")
	ErrSteeringCheckpointMismatch = ErrorCode("steering_checkpoint_mismatch")
)

const (
	ErrInvalidDigest             = ErrorCode("invalid_digest")
	ErrRegistryNotAllowed        = ErrorCode("registry_not_allowed")
	ErrSignatureInvalid          = ErrorCode("signature_invalid")
	ErrManifestMismatch          = ErrorCode("manifest_mismatch")
	ErrContractTestFailed        = ErrorCode("contract_test_failed")
	ErrVerificationRequired      = ErrorCode("verification_required")
	ErrRegistrationConflict      = ErrorCode("registration_conflict")
	ErrRegistrationStateConflict = ErrorCode("registration_state_conflict")

	ErrWarmPoolBudgetConflict      = ErrorCode("warm_pool_budget_conflict")
	ErrWarmPoolMaxPerImageConflict = ErrorCode("warm_pool_max_per_image_conflict")
)

type validationError struct {
	Code   ErrorCode
	Reason string
}

func (e *validationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Reason)
}

func validationErrorf(code ErrorCode, format string, args ...any) *validationError {
	return &validationError{Code: code, Reason: fmt.Sprintf(format, args...)}
}
