package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"agent-platform/internal/executor"
	"agent-platform/internal/queue"
	"agent-platform/internal/service/runs"
)

const outboxBackoffBase = 500 * time.Millisecond

const DefaultGroup = "agent-workers"

type Options struct {
	Consumer     string
	Group        string
	Concurrency  int
	ScanInterval time.Duration
	ScanBatch    int

	ReclaimEvery time.Duration

	PendingMinIdle time.Duration

	Cleanup CleanupRunner

	Orphans OrphanScanner

	Rollouts RolloutRunner

	RolloutInterval time.Duration
}

type RolloutRunner interface {
	Scan(ctx context.Context) error
}

type CleanupRunner interface {
	Scan(ctx context.Context) error
}

type OrphanScanner interface {
	ScanOrphans(ctx context.Context) error
}

type StageRunner interface {
	ExecuteStage(ctx context.Context, clientID, runID string, stageNo int, owner string) (executor.ExecuteResult, error)
}

type Worker struct {
	q              *queue.Client
	runs           *runs.Service
	exec           StageRunner
	log            *slog.Logger
	group          string
	consumer       string
	concurrency    int
	scanInterval   time.Duration
	scanBatch      int
	reclaimEvery   time.Duration
	pendingMinIdle time.Duration
	cleanup        CleanupRunner
	orphans        OrphanScanner
	rollouts       RolloutRunner
	rolloutEvery   time.Duration
}

func New(q *queue.Client, runsSvc *runs.Service, exec StageRunner, log *slog.Logger, opts Options) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if opts.Group == "" {
		opts.Group = DefaultGroup
	}
	if opts.Consumer == "" {
		opts.Consumer = "worker"
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = 5 * time.Second
	}
	if opts.ScanBatch <= 0 || opts.ScanBatch > 100 {
		opts.ScanBatch = 100
	}
	if opts.ReclaimEvery <= 0 {
		opts.ReclaimEvery = 2 * time.Second
	}
	if opts.PendingMinIdle <= 0 {
		opts.PendingMinIdle = 60 * time.Second
	}
	if opts.RolloutInterval <= 0 {
		opts.RolloutInterval = 5 * time.Second
	}
	return &Worker{q: q, runs: runsSvc, exec: exec, log: log, group: opts.Group,
		consumer: opts.Consumer, concurrency: opts.Concurrency, scanInterval: opts.ScanInterval,
		scanBatch: opts.ScanBatch, reclaimEvery: opts.ReclaimEvery, pendingMinIdle: opts.PendingMinIdle,
		cleanup: opts.Cleanup, orphans: opts.Orphans, rollouts: opts.Rollouts, rolloutEvery: opts.RolloutInterval}
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.q.EnsureGroup(ctx, w.group); err != nil {
		return fmt.Errorf("worker: ensure group: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	errf := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.compensateLoop(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.reclaimLoop(ctx)
	}()
	if w.rollouts != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.rolloutLoop(ctx)
		}()
	}
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		consumer := w.consumer
		if w.concurrency > 1 {
			consumer = fmt.Sprintf("%s-%d", w.consumer, i)
		}
		go func(c string) {
			defer wg.Done()
			w.consumeLoop(ctx, c, errf)
		}(consumer)
	}
	wg.Wait()
	return firstErr
}

func (w *Worker) dispatchRun(ctx context.Context, runID string) {
	clientID, err := w.runs.RunClientID(ctx, runID)
	if err != nil {
		w.log.Warn("run reference for unknown run; skipping", "run", runID, "error", err)
		return
	}
	stageNo, ok, err := w.runs.NextSchedulableStage(ctx, runID)
	if err != nil {
		w.log.Error("resolve schedulable stage", "run", runID, "error", err)
		return
	}
	if !ok {
		w.log.Info("run not schedulable now", "run", runID)
		return
	}
	out, err := w.exec.ExecuteStage(ctx, clientID, runID, stageNo, w.consumer)
	if err != nil {
		w.log.Error("execute stage failed", "run", runID, "stage", stageNo, "error", err)
		return
	}
	w.log.Info("stage executed", "run", runID, "stage", stageNo, "outcome", out.String())
}

func (w *Worker) consumeLoop(ctx context.Context, consumer string, errf func(error)) {
	for {
		if ctx.Err() != nil {
			return
		}
		msg, ok, err := w.q.Next(ctx, w.group, consumer, time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			errf(err)
			return
		}
		if !ok {
			continue
		}
		w.dispatchRun(ctx, msg.RunID)
		if err := w.q.Ack(ctx, w.group, msg.ID); err != nil {

			w.log.Warn("ack failed; entry remains pending for reclaim", "msg", msg.ID, "error", err)
		}
	}
}

func (w *Worker) reclaimLoop(ctx context.Context) {
	t := time.NewTicker(w.reclaimEvery)
	defer t.Stop()
	w.reclaim(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.reclaim(ctx); err != nil {
				w.log.Warn("reclaim failed", "error", err)
			}
		}
	}
}

func (w *Worker) reclaim(ctx context.Context) error {
	msgs, err := w.q.Reclaim(ctx, w.group, w.consumer, w.pendingMinIdle, 100)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		w.dispatchRun(ctx, m.RunID)
		if err := w.q.Ack(ctx, w.group, m.ID); err != nil {
			w.log.Warn("reclaim ack failed", "msg", m.ID, "error", err)
		}
	}
	return nil
}

func (w *Worker) rolloutLoop(ctx context.Context) {
	t := time.NewTicker(w.rolloutEvery)
	defer t.Stop()
	w.scanRollouts(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.scanRollouts(ctx); err != nil {
				w.log.Error("rollout scan failed", "error", err)
			}
		}
	}
}

func (w *Worker) scanRollouts(ctx context.Context) error {
	if err := w.rollouts.Scan(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (w *Worker) compensateLoop(ctx context.Context) {
	t := time.NewTicker(w.scanInterval)
	defer t.Stop()
	w.compensate(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.compensate(ctx); err != nil {
				w.log.Error("compensation scan failed", "error", err)
			}
		}
	}
}

func (w *Worker) compensate(ctx context.Context) error {
	ready, err := w.runs.ClaimReadyOutbox(ctx, w.scanBatch)
	if err != nil {
		return fmt.Errorf("worker: claim ready outbox: %w", err)
	}
	for _, ev := range ready {
		if ev.RunID == nil {

			if err := w.runs.MarkOutboxPublished(ctx, ev.EventID, ev.Token); err != nil {
				w.log.Warn("mark run-less outbox published lost", "event", ev.EventID, "error", err)
			}
			continue
		}
		if err := w.q.Publish(ctx, *ev.RunID); err != nil {
			w.log.Error("outbox publish failed; releasing with backoff", "event", ev.EventID, "run", *ev.RunID, "error", err)
			if rerr := w.runs.ReleaseOutbox(ctx, ev.EventID, ev.Token, time.Now().Add(outboxBackoffBase)); rerr != nil {
				w.log.Error("release outbox failed", "event", ev.EventID, "error", rerr)
			}
			continue
		}
		if err := w.runs.MarkOutboxPublished(ctx, ev.EventID, ev.Token); err != nil {

			w.log.Warn("mark outbox published lost; releasing for re-publish", "event", ev.EventID, "error", err)
			if rerr := w.runs.ReleaseOutbox(ctx, ev.EventID, ev.Token, time.Now().Add(outboxBackoffBase)); rerr != nil {
				w.log.Error("release outbox after mark loss failed", "event", ev.EventID, "error", rerr)
			}
		}
	}
	ids, err := w.runs.SchedulableRuns(ctx, w.scanBatch)
	if err != nil {
		return fmt.Errorf("worker: schedulable scan: %w", err)
	}
	for _, runID := range ids {
		if err := w.q.Publish(ctx, runID); err != nil {
			w.log.Error("republish schedulable run failed", "run", runID, "error", err)
		}
	}

	stalled, err := w.runs.StalledRuns(ctx, w.scanBatch)
	if err != nil {
		return fmt.Errorf("worker: stalled scan: %w", err)
	}
	for _, runID := range stalled {
		applied, err := w.runs.FinalizeStalled(ctx, runID)
		if err != nil {
			w.log.Error("finalize stalled run failed", "run", runID, "error", err)
			continue
		}
		if applied {
			w.log.Info("finalized stalled run", "run", runID, "reason", "schedule_attempts_or_budget_exhausted")
		}
	}

	interrupted, requeued, err := w.runs.RebasePlatformRuns(ctx, w.scanBatch)
	if err != nil {
		return fmt.Errorf("worker: platform rebase scan: %w", err)
	}
	for _, runID := range interrupted {
		w.log.Info("platform baseline changed; interrupting run", "run", runID, "reason", "platform_network_policy_changed")
	}
	for _, runID := range requeued {
		w.log.Info("interrupted run requeued on new baseline", "run", runID)
	}

	expired, err := w.runs.ExpireDueRuns(ctx, w.scanBatch)
	if err != nil {
		return fmt.Errorf("worker: due scan: %w", err)
	}
	for _, runID := range expired {
		w.log.Info("expired due run", "run", runID, "reason", "total_lifecycle_deadline_passed")
	}

	if w.cleanup != nil {
		if err := w.cleanup.Scan(ctx); err != nil {
			w.log.Error("cleanup scan failed", "error", err)
		}
	}

	if w.orphans != nil {
		if err := w.orphans.ScanOrphans(ctx); err != nil {
			w.log.Error("orphan object scan failed", "error", err)
		}
	}
	return nil
}
