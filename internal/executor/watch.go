package executor

import (
	"context"
	"log/slog"
	"time"

	"agent-platform/internal/service/runs"
)

func leaseAndCancelWatch(ctx context.Context, runsSvc *runs.Service, log *slog.Logger,
	runID string, stageNo int, fence int64, owner string, lease time.Duration) (execCtx context.Context, cancel func(), done chan struct{}) {
	execCtx, cancelCtx := context.WithCancel(ctx)
	done = make(chan struct{})

	tick := lease / 3
	if tick < time.Second {
		tick = time.Second
	}
	poll := time.Second

	go func() {
		defer close(done)
		hb := time.NewTicker(tick)
		defer hb.Stop()
		pc := time.NewTicker(poll)
		defer pc.Stop()
		for {
			select {
			case <-execCtx.Done():
				return
			case <-hb.C:
				ok, err := runsSvc.HeartbeatStage(ctx, runID, stageNo, fence, owner)
				if err != nil || !ok {
					log.Warn("lease heartbeat failed; stopping execution", "run", runID,
						"stage", stageNo, "fence", fence, "err", err)
					cancelCtx()
					return
				}
			case <-pc.C:
				status, err := runsSvc.StatusOf(ctx, runID)
				if err == nil && !isExecuting(string(status)) {
					log.Info("run left executing; stopping execution", "run", runID, "status", status)
					cancelCtx()
					return
				}

			}
		}
	}()
	return execCtx, cancelCtx, done
}
