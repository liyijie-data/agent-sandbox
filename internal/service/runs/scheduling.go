package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"agent-platform/internal/contracts"
	"agent-platform/model"
)

const outboxClaimTTL = 5 * time.Minute

const platformRebaseAbortGrace = 10 * time.Second

func (s *Service) RebasePlatformRuns(ctx context.Context, limit int) (interrupted, requeued []string, err error) {
	head, err := s.store.DAOs().PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if head.ActiveRevisionID == nil {
		return nil, nil, nil
	}
	stale, err := s.store.DAOs().Runs.ListStalePlatformExecuting(ctx, *head.ActiveRevisionID, s.clock.Now(), limit)
	if err != nil {
		return nil, nil, fmt.Errorf("runs: stale platform scan: %w", err)
	}
	for _, runID := range stale {
		applied, err := s.interruptStalePlatformRun(ctx, runID, *head.ActiveRevisionID)
		if err != nil {
			return interrupted, requeued, err
		}
		if applied {
			interrupted = append(interrupted, runID)
		}
	}
	rebasing, err := s.store.DAOs().Runs.ListPlatformRebasing(ctx, s.clock.Now(), limit)
	if err != nil {
		return interrupted, nil, fmt.Errorf("runs: rebasing scan: %w", err)
	}
	for _, runID := range rebasing {
		applied, err := s.requeueRebasedRun(ctx, runID)
		if err != nil {
			return interrupted, requeued, err
		}
		if applied {
			requeued = append(requeued, runID)
		}
	}
	return interrupted, requeued, nil
}

func (s *Service) interruptStalePlatformRun(ctx context.Context, runID, activePlatformRevID string) (bool, error) {
	now := s.clock.Now()
	var applied bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return err
		}
		if contracts.RunStatus(r.Status) != contracts.RunStatusPreparing && contracts.RunStatus(r.Status) != contracts.RunStatusRunning {
			return errAbort
		}
		ok, err := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{StatusIn: []string{string(contracts.RunStatusPreparing), string(contracts.RunStatusRunning)}},
			model.RunPatch{Status: model.Optional[string]{Set: true, Value: string(contracts.RunStatusPlatformRebasing)}})
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}

		grace := now.Add(platformRebaseAbortGrace)
		if _, uerr := d.Stages.UpdateIf(ctx, runID, r.CurrentStage, model.RunStageCondition{},
			model.RunStagePatch{LeaseExpiresAt: &grace}); uerr != nil {
			return uerr
		}
		if err := s.enqueueOutbox(ctx, d, runID, r.CurrentStage, contracts.EventPlatformNetworkPolicyChanged, now, ""); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Service) requeueRebasedRun(ctx context.Context, runID string) (bool, error) {
	now := s.clock.Now()
	var applied bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return err
		}
		if contracts.RunStatus(r.Status) != contracts.RunStatusPlatformRebasing {
			return errAbort
		}
		if !r.TotalLifecycleDeadline.After(now) {
			return s.expireRebasedRun(ctx, d, r, now)
		}
		ok, err := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{Status: strPtr(string(contracts.RunStatusPlatformRebasing))},
			model.RunPatch{Status: model.Optional[string]{Set: true, Value: string(contracts.RunStatusQueued)}})
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}

		if ok, uerr := d.Stages.ResetAfterRebase(ctx, runID, r.CurrentStage); uerr != nil {
			return uerr
		} else if !ok {
			return errAbort
		}
		if err := s.enqueueOutbox(ctx, d, runID, r.CurrentStage, contracts.EventRunQueued, now, ""); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Service) expireRebasedRun(ctx context.Context, d model.DAOs, r *model.Run, now time.Time) error {
	t := now
	me := now.Add(s.policy.MetadataRetention)
	cleanup := string(contracts.CleanupPending)
	ok, err := d.Runs.UpdateIf(ctx, r.ID,
		model.RunCondition{Status: strPtr(string(contracts.RunStatusPlatformRebasing))},
		model.RunPatch{
			Status:            model.Optional[string]{Set: true, Value: string(contracts.RunStatusExpired)},
			TerminalAt:        model.Optional[*time.Time]{Set: true, Value: &t},
			MetadataExpiresAt: model.Optional[*time.Time]{Set: true, Value: &me},
			CleanupStatus:     model.Optional[string]{Set: true, Value: cleanup},
		})
	if err != nil {
		return err
	}
	if !ok {
		return errAbort
	}
	if err := d.Contents.ExpireByRun(ctx, r.ID, now.Add(s.policy.ContentRetention)); err != nil {
		return err
	}
	if err := s.enqueueCleanupTx(ctx, d, "run_contents", r.ID, now); err != nil {
		return err
	}
	return s.enqueueOutbox(ctx, d, r.ID, r.CurrentStage, contracts.EventRunTerminal, now, "")
}

var ErrOutboxClaimLost = errors.New("runs: outbox claim lost")

type ReadyOutboxEvent struct {
	EventID string
	RunID   *string
	Token   string
}

func (s *Service) ClaimReadyOutbox(ctx context.Context, limit int) ([]ReadyOutboxEvent, error) {
	now := s.clock.Now()
	events, err := s.store.DAOs().Outbox.ListReadyPending(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	var out []ReadyOutboxEvent
	for _, ev := range events {
		token := uuid.NewString()
		until := now.Add(outboxClaimTTL)
		ok, err := s.store.DAOs().Outbox.UpdateIf(ctx, ev.EventID,
			model.OutboxCondition{StatusIn: []string{"pending", "claimed"}},
			model.OutboxPatch{Status: strPtr("claimed"), ClaimToken: &token, ClaimedUntil: &until})
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		out = append(out, ReadyOutboxEvent{EventID: ev.EventID, RunID: ev.RunID, Token: token})
	}
	return out, nil
}

func (s *Service) MarkOutboxPublished(ctx context.Context, eventID, token string) error {
	ok, err := s.store.DAOs().Outbox.UpdateIf(ctx, eventID,
		model.OutboxCondition{Status: strPtr("claimed"), ClaimToken: &token},
		model.OutboxPatch{Status: strPtr("published")})
	if err != nil {
		return err
	}
	if !ok {
		return ErrOutboxClaimLost
	}
	return nil
}

func (s *Service) ReleaseOutbox(ctx context.Context, eventID, token string, retryAt time.Time) error {
	now := s.clock.Now()
	ok, err := s.store.DAOs().Outbox.UpdateIf(ctx, eventID,
		model.OutboxCondition{Status: strPtr("claimed"), ClaimToken: &token},
		model.OutboxPatch{Status: strPtr("pending"), RetryAt: &retryAt, ClaimedUntil: &now})
	if err != nil {
		return err
	}
	if !ok {
		return ErrOutboxClaimLost
	}
	return nil
}

func (s *Service) NextSchedulableStage(ctx context.Context, runID string) (int, bool, error) {
	now := s.clock.Now()
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return 0, false, ErrNotFound
	}
	st := contracts.RunStatus(r.Status)
	if st != contracts.RunStatusQueued && st != contracts.RunStatusPreparing {
		return 0, false, nil
	}
	if !r.TotalLifecycleDeadline.After(now) {
		return 0, false, nil
	}
	stage, err := s.store.DAOs().Stages.Get(ctx, runID, r.CurrentStage)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if stage.LaunchStarted {
		return 0, false, nil
	}
	if stage.Owner != "" && stage.LeaseExpiresAt != nil && stage.LeaseExpiresAt.After(now) {
		return 0, false, nil
	}
	if stage.Attempt >= s.policy.MaxScheduleAttempts || stage.BudgetRemainingSec <= 0 {
		return 0, false, nil
	}
	if r.ImageRegistrationID != nil {
		if unready, perr := s.profileUnready(ctx, r); perr != nil {
			return 0, false, perr
		} else if unready {
			return 0, false, nil
		}
	}

	if client, cerr := s.store.DAOs().Clients.Get(ctx, r.ClientID); cerr == nil && client.ConcurrencyQuota > 0 {
		if active, aerr := s.store.DAOs().Runs.CountActiveExecutions(ctx, r.ClientID, now); aerr == nil && active >= client.ConcurrencyQuota {
			return 0, false, nil
		}
	}
	return r.CurrentStage, true, nil
}

func (s *Service) SchedulableRuns(ctx context.Context, limit int) ([]string, error) {
	ids, err := s.store.DAOs().Runs.ListSchedulableRuns(ctx, s.clock.Now(), s.policy.MaxScheduleAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("runs: schedulable scan: %w", err)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		_, ok, err := s.NextSchedulableStage(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Service) StalledRuns(ctx context.Context, limit int) ([]string, error) {
	ids, err := s.store.DAOs().Runs.ListStalledRuns(ctx, s.policy.MaxScheduleAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("runs: stalled scan: %w", err)
	}
	return ids, nil
}

func (s *Service) FinalizeStalled(ctx context.Context, runID string) (bool, error) {
	now := s.clock.Now()
	var applied bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		t := now
		me := now.Add(s.policy.MetadataRetention)
		cleanup := string(contracts.CleanupPending)
		ok, err := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{StatusIn: []string{string(contracts.RunStatusQueued), string(contracts.RunStatusPreparing)}},
			model.RunPatch{
				Status:            model.Optional[string]{Set: true, Value: string(contracts.RunStatusFailed)},
				TerminalAt:        model.Optional[*time.Time]{Set: true, Value: &t},
				MetadataExpiresAt: model.Optional[*time.Time]{Set: true, Value: &me},
				CleanupStatus:     model.Optional[string]{Set: true, Value: cleanup},
			})
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}
		if err := d.Contents.ExpireByRun(ctx, runID, now.Add(s.policy.ContentRetention)); err != nil {
			return err
		}
		if err := s.enqueueCleanupTx(ctx, d, "run_contents", runID, now); err != nil {
			return err
		}
		if err := s.enqueueOutbox(ctx, d, runID, 0, contracts.EventRunTerminal, now, ""); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Service) ExpireDueRuns(ctx context.Context, limit int) ([]string, error) {
	ids, err := s.store.DAOs().Runs.ListDueRuns(ctx, s.clock.Now(), limit)
	if err != nil {
		return nil, fmt.Errorf("runs: due scan: %w", err)
	}
	var expired []string
	for _, runID := range ids {
		applied, err := s.ExpireRun(ctx, runID)
		if err != nil {
			return expired, err
		}
		if applied {
			expired = append(expired, runID)
		}
	}
	return expired, nil
}

func (s *Service) ExpireRun(ctx context.Context, runID string) (bool, error) {
	now := s.clock.Now()
	var applied bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		t := now
		me := now.Add(s.policy.MetadataRetention)
		cleanup := string(contracts.CleanupPending)
		ok, err := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{StatusIn: []string{string(contracts.RunStatusQueued), string(contracts.RunStatusPreparing), string(contracts.RunStatusRunning), string(contracts.RunStatusAwaitingInput)}},
			model.RunPatch{
				Status:            model.Optional[string]{Set: true, Value: string(contracts.RunStatusExpired)},
				TerminalAt:        model.Optional[*time.Time]{Set: true, Value: &t},
				MetadataExpiresAt: model.Optional[*time.Time]{Set: true, Value: &me},
				CleanupStatus:     model.Optional[string]{Set: true, Value: cleanup},
			})
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}
		if err := d.Contents.ExpireByRun(ctx, runID, now.Add(s.policy.ContentRetention)); err != nil {
			return err
		}
		if err := s.enqueueCleanupTx(ctx, d, "run_contents", runID, now); err != nil {
			return err
		}
		if err := s.enqueueOutbox(ctx, d, runID, 0, contracts.EventRunTerminal, now, ""); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applied, nil
}

func strPtr(s string) *string { return &s }
