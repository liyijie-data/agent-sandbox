package executor

import (
	"context"
	"fmt"

	"agent-platform/internal/service/runs"
)

func (e *Executor) resolveResume(ctx context.Context, runID string, stageNo int) (*ResumeRef, int64, error) {
	cursor, err := e.steers.ConfirmedCursor(ctx, runID)
	if err != nil {
		return nil, 0, err
	}
	if stageNo <= 1 {
		return nil, cursor, nil
	}

	m, err := e.runs.LatestResumeMaterial(ctx, runID)
	if err != nil {
		if err == runs.ErrNotFound {
			return nil, cursor, fmt.Errorf("executor: resume stage %d has no verified checkpoint", stageNo)
		}
		return nil, 0, err
	}
	return &ResumeRef{
		RunID:       m.Checkpoint.RunID,
		StageNo:     m.Checkpoint.StageNo,
		Fence:       m.Checkpoint.Fence,
		ObjectRef:   m.Checkpoint.ObjectRef,
		SHA256:      m.Checkpoint.SHA256,
		StateFormat: m.Checkpoint.StateFormat,
		InputID:     m.InputID,
		Answer:      m.Answer,
	}, cursor, nil
}
