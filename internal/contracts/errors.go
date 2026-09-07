package contracts

import (
	"fmt"
	"regexp"
)

var diagnosticHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func NormalizeDiagnosticOutcome(in *DiagnosticOutcome) *DiagnosticOutcome {
	if in == nil {
		return nil
	}
	out := *in
	allowedReasons := map[string]bool{"": true, "destination_missing": true, "trace_collect_failed": true, "trace_upload_failed": true, "trace_incomplete": true, "trace_not_delivered": true, "diagnostic_receipt_invalid": true, "runtime_result_missing": true, "diagnostic_unavailable": true}
	if out.Status != "uploaded" && out.Status != "unavailable" && out.Status != "disabled" {
		out.Status = "unavailable"
	}
	if out.Status != "uploaded" {
		out.DestinationID, out.SHA256, out.SizeBytes = "", "", 0
	}
	if out.Status == "uploaded" && (!diagnosticHashRE.MatchString(out.SHA256) || out.SizeBytes <= 0 || out.SizeBytes > 100<<20) {
		out.Status, out.Reason, out.DestinationID, out.SHA256, out.SizeBytes = "unavailable", "diagnostic_receipt_invalid", "", "", 0
	}
	if out.Status == "unavailable" {
		if !allowedReasons[out.Reason] {
			out.Reason = "diagnostic_unavailable"
		}
	} else if !allowedReasons[out.Reason] {
		out.Reason = ""
	}
	if out.DestinationID != "" && !reqIDRe.MatchString(out.DestinationID) {
		out.DestinationID = ""
	}
	if out.Incomplete {
		if out.Reason != "diagnostic_receipt_invalid" {
			out.Reason = "trace_incomplete"
		}
	}
	return &out
}

func IsPublicRuntimeErrorCode(code string) bool {
	for _, c := range []ErrorCode{ErrRuntimeProtocolInvalid, ErrAgentExecutionFailed, ErrResumeFailed, ErrCheckpointPersistFailed, ErrCheckpointInvalid, ErrResultBundleUploadFailed, ErrArtifactDestinationRequired, ErrExecutionTimeout, ErrResourceLimitExceeded, ErrEventDeliveryFailed} {
		if code == string(c) {
			return true
		}
	}
	if code == string(ErrExecutionOutcomeUnknown) {
		return true
	}
	return code == "context_limit_exceeded"
}

func NormalizeRuntimeErrorDetails(in *RuntimeErrorDetails) *RuntimeErrorDetails {
	if in == nil {
		return nil
	}
	out := *in
	allowedPhases := map[string]bool{"model": true, "model_after_tool": true, "config": true, "checkpoint": true, "composition": true, "tool_catalog": true, "resources": true, "context": true, "artifact_delivery": true, "timeout": true, "runtime": true}
	if !allowedPhases[out.Phase] {
		out.Phase = "runtime"
	}
	allowed := map[string]bool{"unknown": true, "context_limit_exceeded": true, "upstream_auth_failed": true, "upstream_rate_limited": true, "upstream_unavailable": true, "invalid_reasoning_request": true, "upstream_invalid_request": true, "model_timeout": true, "model_transport_error": true, "config_invalid": true, "checkpoint_restore_failed": true, "checkpoint_persist_failed": true, "composition_failed": true, "tool_catalog_failed": true, "resources_failed": true, "context_failed": true, "artifact_delivery_failed": true, "execution_timeout": true}
	if !allowed[out.ReasonCode] {
		out.ReasonCode = "unknown"
	}
	if out.UpstreamStatus < 400 || out.UpstreamStatus > 599 {
		out.UpstreamStatus = 0
	}
	messages := map[string]string{
		"config_invalid":            "运行配置无效，请检查配置后重试。",
		"checkpoint_restore_failed": "任务恢复失败，请重新提交任务。",
		"checkpoint_persist_failed": "检查点保存失败，请稍后重试。",
		"composition_failed":        "运行环境初始化失败，请稍后重试。",
		"tool_catalog_failed":       "工具初始化失败，请稍后重试。",
		"resources_failed":          "运行资源准备失败，请稍后重试。",
		"context_failed":            "上下文准备失败，请稍后重试。",
		"artifact_delivery_failed":  "结果交付失败，请稍后重试。",
		"execution_timeout":         "任务执行超时，请稍后重试。",
		"context_limit_exceeded":    "上下文长度超限，请减少输入内容或新建会话后重试。",
		"upstream_auth_failed":      "模型服务认证失败，请联系管理员检查模型配置。",
		"upstream_rate_limited":     "模型服务当前繁忙，请稍后重试。",
		"upstream_unavailable":      "模型服务暂时不可用，请稍后重试。",
		"invalid_reasoning_request": "模型要求回传的思考内容缺失，请联系管理员检查模型适配。",
		"upstream_invalid_request":  "模型请求未被接受，请检查模型配置后重试。",
		"model_timeout":             "模型请求超时，请稍后重试。",
		"model_transport_error":     "无法连接模型服务，请稍后重试。",
		"unknown":                   "模型服务暂时无法完成请求，请稍后重试。",
	}
	out.UserMessage = messages[out.ReasonCode]
	if out.Phase == "model_after_tool" {
		out.UserMessage = "工具调用已结束，但模型继续请求失败：" + out.UserMessage
	}
	return &out
}

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
