package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/images"
	"agent-platform/internal/service/networks"
	"agent-platform/internal/service/runs"
	"agent-platform/internal/service/steers"
)

const notFoundCode = contracts.ErrorCode("not_found")

const methodNotAllowedCode = contracts.ErrorCode("method_not_allowed")

const upstreamErrorCode = contracts.ErrorCode("upstream_error")

var safeMessage = map[contracts.ErrorCode]string{
	contracts.ErrInvalidRequest:          "The request is malformed.",
	contracts.ErrInvalidMessages:         "The messages array is invalid.",
	contracts.ErrCapabilityUnsupported:   "The selected image does not support a requested capability.",
	contracts.ErrSandboxImageUnavailable: "The selected image is not available.",
	contracts.ErrPayloadTooLarge:         "The request exceeds the size limit.",
	contracts.ErrReqIDConflict:           "The request id was already used with different content.",
	notFoundCode:                         "The run or resource was not found.",
	methodNotAllowedCode:                 "The requested method is not allowed for this resource.",
	contracts.ErrAnswerConflict:          "The answer conflicts with a previously accepted answer.",
	contracts.ErrRunStateConflict:        "The run is not in a state that allows this action.",
	contracts.ErrInputExpired:            "The input has expired and can no longer be answered.",
	contracts.ErrInvalidSteer:            "The steer request is invalid.",
	contracts.ErrSteerConflict:           "The steer id was already used with different content.",
	contracts.ErrSteerLimitExceeded:      "The steer budget for this run is exhausted.",
	contracts.ErrSteeringCursorConflict:  "The steering cursor is inconsistent.",
	contracts.ErrRuntimeStateConflict:    "The runtime identity, status or fence does not match.",
	contracts.ErrExecutionOutcomeUnknown: "The execution outcome is unknown.",
	contracts.ErrEventsExpired:           "The event history has expired.",
	contracts.ErrEventGap:                "The event stream has a gap; reconnect from the beginning.",
	contracts.ErrResourceLimitExceeded:   "The resource limit for this run was exceeded.",
	contracts.ErrUpstreamTargetRejected:  "The upstream target was rejected.",
	upstreamErrorCode:                    "The model upstream is unavailable.",

	contracts.ErrInvalidDigest:             "The image digest is invalid.",
	contracts.ErrRegistryNotAllowed:        "The image registry is not allowed.",
	contracts.ErrSignatureInvalid:          "The image signature could not be verified.",
	contracts.ErrManifestMismatch:          "The image manifest does not match the registration.",
	contracts.ErrContractTestFailed:        "The image contract test failed.",
	contracts.ErrVerificationRequired:      "Verification evidence is required and must be bound to the image digest.",
	contracts.ErrRegistrationConflict:      "The image registration already exists with different data.",
	contracts.ErrRegistrationStateConflict: "The registration is not in a state that allows this action.",

	contracts.ErrWarmPoolBudgetConflict:      "The warm pool budget cannot satisfy the configured replicas.",
	contracts.ErrWarmPoolMaxPerImageConflict: "The warm pool max-per-image cannot satisfy the configured replicas.",

	contracts.ErrNetworkRolloutBlocked: "A network configuration rollout is still in progress; new runs are temporarily blocked.",
	contracts.ErrConfigStateConflict:   "The expected active configuration revision no longer matches.",
	contracts.ErrNetworkPolicyRejected: "The network configuration was rejected by the platform boundary.",

	contracts.ErrPlatformNetworkPolicyChanged: "The platform network baseline changed; the run is being requeued on the new baseline.",
}

var codeStatus = map[contracts.ErrorCode]int{
	contracts.ErrInvalidRequest:          http.StatusBadRequest,
	contracts.ErrInvalidMessages:         http.StatusBadRequest,
	contracts.ErrCapabilityUnsupported:   http.StatusUnprocessableEntity,
	contracts.ErrSandboxImageUnavailable: http.StatusUnprocessableEntity,
	contracts.ErrPayloadTooLarge:         http.StatusRequestEntityTooLarge,
	contracts.ErrReqIDConflict:           http.StatusConflict,
	notFoundCode:                         http.StatusNotFound,
	methodNotAllowedCode:                 http.StatusMethodNotAllowed,
	contracts.ErrAnswerConflict:          http.StatusConflict,
	contracts.ErrRunStateConflict:        http.StatusConflict,
	contracts.ErrInputExpired:            http.StatusGone,
	contracts.ErrInvalidSteer:            http.StatusBadRequest,
	contracts.ErrSteerConflict:           http.StatusConflict,
	contracts.ErrSteerLimitExceeded:      http.StatusTooManyRequests,
	contracts.ErrSteeringCursorConflict:  http.StatusConflict,
	contracts.ErrRuntimeStateConflict:    http.StatusConflict,
	contracts.ErrInvalidAccessRefresh:    http.StatusUnprocessableEntity,
	contracts.ErrExecutionOutcomeUnknown: http.StatusFailedDependency,
	contracts.ErrEventsExpired:           http.StatusGone,
	contracts.ErrEventGap:                http.StatusGone,
	contracts.ErrResourceLimitExceeded:   http.StatusTooManyRequests,
	contracts.ErrUpstreamTargetRejected:  http.StatusForbidden,

	contracts.ErrInvalidDigest:             http.StatusUnprocessableEntity,
	contracts.ErrRegistryNotAllowed:        http.StatusForbidden,
	contracts.ErrSignatureInvalid:          http.StatusForbidden,
	contracts.ErrManifestMismatch:          http.StatusConflict,
	contracts.ErrContractTestFailed:        http.StatusUnprocessableEntity,
	contracts.ErrVerificationRequired:      http.StatusConflict,
	contracts.ErrRegistrationConflict:      http.StatusConflict,
	contracts.ErrRegistrationStateConflict: http.StatusConflict,

	contracts.ErrWarmPoolBudgetConflict:      http.StatusConflict,
	contracts.ErrWarmPoolMaxPerImageConflict: http.StatusConflict,

	contracts.ErrNetworkRolloutBlocked: http.StatusConflict,
	contracts.ErrConfigStateConflict:   http.StatusConflict,
	contracts.ErrNetworkPolicyRejected: http.StatusUnprocessableEntity,

	contracts.ErrPlatformNetworkPolicyChanged: http.StatusConflict,
}

func knownCode(c contracts.ErrorCode) bool {
	_, ok := codeStatus[c]
	return ok
}

type outcomeError struct {
	Status int
	Code   contracts.ErrorCode
	msg    string
}

func (o *outcomeError) Error() string { return o.msg }

func errOutcome(err error) *outcomeError {
	if err == nil {
		return nil
	}
	var oe *outcomeError
	if errors.As(err, &oe) {
		return oe
	}

	switch {
	case errors.Is(err, runs.ErrNotFound), errors.Is(err, steers.ErrNotFound):
		return httpErr(http.StatusNotFound, notFoundCode)
	case errors.Is(err, runs.ErrRunStateConflict), errors.Is(err, steers.ErrRunStateConflict):
		return httpErr(http.StatusConflict, contracts.ErrRunStateConflict)
	case errors.Is(err, runs.ErrAnswerConflict):
		return httpErr(http.StatusConflict, contracts.ErrAnswerConflict)
	case errors.Is(err, runs.ErrInputExpired):
		return httpErr(http.StatusGone, contracts.ErrInputExpired)
	case errors.Is(err, runs.ErrReqIDConflict):
		return httpErr(http.StatusConflict, contracts.ErrReqIDConflict)
	case errors.Is(err, steers.ErrConflict):
		return httpErr(http.StatusConflict, contracts.ErrSteerConflict)
	case errors.Is(err, steers.ErrLimitExceeded):
		return httpErr(http.StatusTooManyRequests, contracts.ErrSteerLimitExceeded)
	case errors.Is(err, steers.ErrCursorConflict):
		return httpErr(http.StatusConflict, contracts.ErrSteeringCursorConflict)

	case errors.Is(err, images.ErrNotFound):
		return httpErr(http.StatusNotFound, notFoundCode)
	case errors.Is(err, images.ErrConflict):
		return httpErr(http.StatusConflict, contracts.ErrRegistrationConflict)
	case errors.Is(err, images.ErrStateConflict):
		return httpErr(http.StatusConflict, contracts.ErrRegistrationStateConflict)
	case errors.Is(err, images.ErrVerificationRequired):
		return httpErr(http.StatusConflict, contracts.ErrVerificationRequired)
	case errors.Is(err, images.ErrSignatureInvalid):
		return httpErr(http.StatusForbidden, contracts.ErrSignatureInvalid)
	case errors.Is(err, images.ErrInvalidDigest):
		return httpErr(http.StatusUnprocessableEntity, contracts.ErrInvalidDigest)

	case errors.Is(err, runs.ErrWarmPoolBudgetConflict):
		return httpErr(http.StatusConflict, contracts.ErrWarmPoolBudgetConflict)
	case errors.Is(err, runs.ErrWarmPoolMaxPerImageConflict):
		return httpErr(http.StatusConflict, contracts.ErrWarmPoolMaxPerImageConflict)

	case errors.Is(err, networks.ErrNotFound):
		return httpErr(http.StatusNotFound, notFoundCode)
	case errors.Is(err, networks.ErrReqIDConflict):
		return httpErr(http.StatusConflict, contracts.ErrReqIDConflict)
	case errors.Is(err, networks.ErrCASConflict):
		return httpErr(http.StatusConflict, contracts.ErrConfigStateConflict)
	case errors.Is(err, networks.ErrNetworkRejected):
		return httpErr(http.StatusUnprocessableEntity, contracts.ErrNetworkPolicyRejected)
	}

	if code := parseValidationCode(err); code != "" {
		return httpErr(statusFor(code), code)
	}
	return httpErr(http.StatusInternalServerError, "internal_error")
}

func parseValidationCode(err error) contracts.ErrorCode {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 {
		c := contracts.ErrorCode(msg[:i])
		if knownCode(c) {
			return c
		}
	}
	return ""
}

func statusFor(c contracts.ErrorCode) int {
	if st, ok := codeStatus[c]; ok {
		return st
	}
	return http.StatusInternalServerError
}

func httpErr(status int, code contracts.ErrorCode) *outcomeError {
	msg := safeMessage[code]
	if msg == "" {
		msg = "The request could not be completed."
	}
	return &outcomeError{Status: status, Code: code, msg: msg}
}

func writeError(w http.ResponseWriter, trace string, oe *outcomeError) {
	if oe.Status == 0 {
		oe = httpErr(http.StatusInternalServerError, "internal_error")
	}
	w.Header().Set("X-Trace-ID", trace)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(oe.Status)
	_ = json.NewEncoder(w).Encode(contracts.ErrorResponse{
		Error:   contracts.ErrorBody{Code: string(oe.Code), Message: oe.msg},
		TraceID: trace,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
