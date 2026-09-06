package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-platform/internal/contracts"
	"agent-platform/internal/service/runs"
)

func (e *Executor) dispatchResult(ctx context.Context, runID string, stageNo int,
	fence int64, owner string, pod Pod, res *contracts.RuntimeResult) (ExecuteResult, error) {
	switch res.Status {
	case contracts.RuntimeOk:
		return e.finalizeSucceeded(ctx, runID, stageNo, fence, owner, res)
	case contracts.RuntimeAwaitingInput:
		return e.pauseToAwaiting(ctx, runID, stageNo, fence, owner, pod, res)
	case contracts.RuntimeError:
		return e.commitFailed(ctx, runID, stageNo, fence, owner, res)
	default:
		return e.executionOutcomeUnknown(ctx, runID, stageNo, fence, owner,
			fmt.Errorf("executor: unknown result status %q", res.Status))
	}
}

func (e *Executor) terminalClose(ctx context.Context, runID string, reason contracts.SteerReasonCode) {
	if err := e.steers.TerminalClose(ctx, runID, reason); err != nil {
		e.log.Error("steering terminal close failed; compensation scan will retry",
			"run", runID, "reason", reason, "error", err)
	}
}

func (e *Executor) finalizeSucceeded(ctx context.Context, runID string, stageNo int,
	fence int64, owner string, res *contracts.RuntimeResult) (ExecuteResult, error) {
	if !e.steeringConsistent(ctx, runID, res) {
		return e.commitFailed(ctx, runID, stageNo, fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: string(contracts.ErrRuntimeProtocolInvalid),
		})
	}
	ok, err := e.runs.FinishRun(ctx, runID, stageNo, fence, owner,
		executingStatuses(),
		runs.FinishState{Status: contracts.RunStatusSucceeded, ResultContentID: e.resultContentID(res), ResultContent: e.resultContent(res)})
	if err != nil {
		return OutcomeFailed, err
	}
	if !ok {
		return OutcomeStale, nil
	}
	e.terminalClose(ctx, runID, contracts.SteerReasonRunFinished)
	return OutcomeSucceeded, nil
}

func (e *Executor) pauseToAwaiting(ctx context.Context, runID string, stageNo int,
	fence int64, owner string, pod Pod, res *contracts.RuntimeResult) (ExecuteResult, error) {
	status, err := e.runs.StatusOf(ctx, runID)
	if err != nil {
		return OutcomeFailed, err
	}
	if !isExecuting(string(status)) {

		e.log.Info("run left executing before pause; refusing await", "run", runID, "status", status)
		return outcomeByStatus(string(status)), nil
	}
	if res.Request == nil || res.Request.Kind == "" {
		return e.commitFailed(ctx, runID, stageNo, fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: string(contracts.ErrRuntimeProtocolInvalid)})
	}
	if !e.steeringConsistent(ctx, runID, res) {
		return e.commitFailed(ctx, runID, stageNo, fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: string(contracts.ErrSteeringCheckpointMismatch)})
	}
	if res.Checkpoint == nil {
		return e.commitFailed(ctx, runID, stageNo, fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: string(contracts.ErrCheckpointInvalid)})
	}

	ref, err := e.persistCheckpoint(ctx, pod, res.Checkpoint.Path,
		res.Checkpoint.SHA256, res.Checkpoint.SizeBytes, checkpointIdentity{RunID: runID, Stage: stageNo, Fence: fence, StateFormat: res.Checkpoint.Format})
	if err != nil {
		e.log.Error("persist checkpoint failed", "run", runID, "stage", stageNo, "error", err)
		return e.commitFailed(ctx, runID, stageNo, fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: string(contracts.ErrCheckpointPersistFailed)})
	}

	deadline, err := e.runs.InputDeadlineOf(ctx, runID)
	if err != nil {
		return e.commitFailed(ctx, runID, stageNo, fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: string(contracts.ErrRuntimeProtocolInvalid)})
	}
	var optionsJSON []byte
	if len(res.Request.Options) > 0 {
		optionsJSON, _ = json.Marshal(res.Request.Options)
	}
	applied, err := e.runs.PauseToAwaiting(ctx, runs.PauseInput{
		RunID: runID, StageNo: stageNo, Fence: fence, Owner: owner,
		InputID: newInputID(runID, stageNo), Kind: res.Request.Kind, Prompt: []byte(res.Request.Prompt),
		Options: optionsJSON,
		Checkpoint: runs.Checkpoint{RunID: runID, StageNo: stageNo, Fence: fence, ObjectRef: ref, SHA256: res.Checkpoint.SHA256,
			SizeBytes: res.Checkpoint.SizeBytes, StateFormat: res.Checkpoint.Format},
		InputDeadline: deadline,
	})
	if err != nil {
		return OutcomeFailed, err
	}
	if !applied {

		return OutcomeStale, nil
	}

	return OutcomeAwaitingInput, nil
}

func (e *Executor) commitFailed(ctx context.Context, runID string, stageNo int,
	fence int64, owner string, res *contracts.RuntimeResult) (ExecuteResult, error) {
	code := res.ErrorCode
	safeCode := code
	if safeCode != "context_limit_exceeded" && safeCode != string(contracts.ErrAgentExecutionFailed) {
		safeCode = string(contracts.ErrAgentExecutionFailed)
	}
	e.log.Warn("run committed failed", "run", runID, "stage", stageNo, "error_code", code)
	applied, err := e.runs.FinishRun(ctx, runID, stageNo, fence, owner,
		executingStatuses(),
		runs.FinishState{Status: contracts.RunStatusFailed, ErrorCode: code,
			ResultContent: e.resultContent(&contracts.RuntimeResult{Status: contracts.RuntimeError, ErrorCode: safeCode})})
	if err != nil {
		return OutcomeFailed, err
	}
	if !applied {
		return OutcomeStale, nil
	}
	e.terminalClose(ctx, runID, contracts.SteerReasonRunFinished)
	return OutcomeFailed, nil
}

func (e *Executor) executionOutcomeUnknown(ctx context.Context, runID string, stageNo int,
	fence int64, owner string, cause error) (ExecuteResult, error) {
	e.log.Warn("execution outcome unknown", "run", runID, "stage", stageNo, "error", cause)
	return e.runOutcomeUnknown(ctx, runID, stageNo, fence, owner), nil
}

func (e *Executor) runOutcomeUnknown(ctx context.Context, runID string, stageNo int, fence int64, owner string) ExecuteResult {
	applied, err := e.runs.FinishRun(ctx, runID, stageNo, fence, owner,
		executingStatuses(),
		runs.FinishState{Status: contracts.RunStatusFailed,
			ErrorCode: string(contracts.ErrExecutionOutcomeUnknown)})
	if err != nil || !applied {
		return OutcomeStale
	}
	e.terminalClose(ctx, runID, contracts.SteerReasonOutcomeUnknown)
	return OutcomeUnknown
}

func (e *Executor) preLaunchFailure(ctx context.Context, runID string, stageNo int,
	fence int64, owner string, cause error) (ExecuteResult, error) {
	e.log.Warn("pre-launch failure; stage retained for retry", "run", runID,
		"stage", stageNo, "fence", fence, "error", cause)
	return OutcomeFailed, nil
}

func (e *Executor) steeringConsistent(ctx context.Context, runID string, res *contracts.RuntimeResult) bool {
	confirmed, err := e.steers.ConfirmedCursor(ctx, runID)
	if err != nil {
		return false
	}
	if res.Steering == nil {
		return confirmed == 0
	}
	if res.Steering.IncorporatedThroughSeq != confirmed {
		return false
	}
	unacked, err := e.steers.HasUnackedBatch(ctx, runID)
	if err != nil {
		return false
	}
	return !unacked
}

func (e *Executor) resultContentID(res *contracts.RuntimeResult) string {
	return res.Delivery.DestinationID
}

func (e *Executor) resultContent(res *contracts.RuntimeResult) []byte {
	if res == nil {
		return nil
	}
	data, err := json.Marshal(res)
	if err != nil {
		e.log.Warn("result content marshal failed; result not persisted", "err", err)
		return nil
	}
	return data
}

func outcomeByStatus(status string) ExecuteResult {
	switch contracts.RunStatus(status) {
	case contracts.RunStatusCancelRequested, contracts.RunStatusCancelled:
		return OutcomeCancelRequested
	case contracts.RunStatusExpired:
		return OutcomeExpired
	}
	return OutcomeStale
}

func executingStatuses() []contracts.RunStatus {
	return []contracts.RunStatus{contracts.RunStatusRunning}
}

func isExecuting(status string) bool {
	return status == string(contracts.RunStatusRunning)
}

func isPreLaunch(status string) bool {
	return status == string(contracts.RunStatusPreparing)
}
