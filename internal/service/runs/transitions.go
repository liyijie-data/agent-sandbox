package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"agent-platform/internal/contracts"
	"agent-platform/internal/storage"
	"agent-platform/model"
)

var (
	errRace            = errors.New("runs: answer race")
	pendingInputState  = "pending"
	answeredInputState = "answered"
	awaitingInputState = string(contracts.RunStatusAwaitingInput)
)

type FinishState struct {
	Status          contracts.RunStatus
	ErrorCode       string
	ResultContentID string

	ResultContent []byte
}

func (s *Service) FinishRun(ctx context.Context, runID string, stageNo int, fence int64, owner string,
	allowedFrom []contracts.RunStatus, f FinishState) (bool, error) {
	now := s.clock.Now()
	var ok bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return errAbort
		}
		if !statusInAllowed(contracts.RunStatus(r.Status), allowedFrom) {
			return errAbort
		}
		leaseOK, err := d.Stages.Heartbeat(ctx, runID, stageNo, fence, owner, now, now.Add(s.policy.QueueBudget))
		if err != nil {
			return err
		}
		if !leaseOK {
			return errAbort
		}
		if err := s.chargeStageTime(ctx, d, runID, stageNo, fence, owner, now); err != nil {
			return err
		}

		terminal := f.Status.IsTerminal()
		var terminalAt *time.Time
		var metaExp *time.Time
		var cleanup string
		var resultContentID string
		if terminal {
			t := now
			terminalAt = &t
			me := now.Add(s.policy.MetadataRetention)
			metaExp = &me
			cleanup = string(contracts.CleanupPending)
			if err := d.Contents.ExpireByRun(ctx, runID, now.Add(s.policy.ContentRetention)); err != nil {
				return err
			}

			if len(f.ResultContent) > 0 {
				id, err := s.putContent(ctx, d, runID, stageNo, ContentResult, f.ResultContent, nil)
				if err != nil {
					return err
				}
				resultContentID = id
			}
			if err := s.enqueueCleanupTx(ctx, d, "run_contents", runID, now); err != nil {
				return err
			}
		} else {
			cleanup = string(contracts.CleanupNotDue)
		}

		from := make([]string, 0, len(allowedFrom))
		for _, a := range allowedFrom {
			from = append(from, string(a))
		}
		ok, err = d.Runs.UpdateIf(ctx, runID, model.RunCondition{StatusIn: from},
			model.RunPatch{
				Status:            model.Optional[string]{Set: true, Value: string(f.Status)},
				TerminalAt:        model.Optional[*time.Time]{Set: true, Value: terminalAt},
				MetadataExpiresAt: model.Optional[*time.Time]{Set: true, Value: metaExp},
				CleanupStatus:     model.Optional[string]{Set: true, Value: cleanup},
			})
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}

		ev := contracts.EventRunTerminal
		if f.Status == contracts.RunStatusAwaitingInput {
			ev = contracts.EventInputRequested
		}

		return s.enqueueOutbox(ctx, d, runID, stageNo, ev, now, resultContentID)
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ok, nil
}

type AnswerResult struct {
	Applied    bool
	Idempotent bool
	RunID      string
	NewStage   int
	NextFence  int64
}

func (s *Service) AnswerInput(ctx context.Context, runID, inputID string, answer, answerAccess []byte, clientID string) (*AnswerResult, error) {
	now := s.clock.Now()
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil || r.ClientID != clientID {
		return nil, ErrNotFound
	}
	inp, err := s.store.DAOs().Inputs.Get(ctx, runID, inputID)
	if err != nil {
		return nil, ErrNotFound
	}
	hmac := s.cipher.Fingerprint("answer", answer)

	if inp.State != "pending" {
		if inp.AnswerHMAC != nil && storage.ConstantTimeEqual(inp.AnswerHMAC, hmac) {
			return &AnswerResult{Idempotent: true, RunID: runID, NewStage: r.CurrentStage}, nil
		}
		return nil, ErrAnswerConflict
	}
	if contracts.RunStatus(r.Status) != contracts.RunStatusAwaitingInput {
		return nil, ErrRunStateConflict
	}
	if !now.Before(inp.InputDeadline) || !r.TotalLifecycleDeadline.After(now) {
		return nil, ErrInputExpired
	}

	var res *AnswerResult
	err = s.store.Transaction(ctx, func(d model.DAOs) error {
		answerID, err := s.putContent(ctx, d, runID, r.CurrentStage, ContentAnswer, answer, nil)
		if err != nil {
			return err
		}
		if answerAccess != nil {
			if _, err := s.putContent(ctx, d, runID, r.CurrentStage, ContentAccessRefresh, answerAccess, nil); err != nil {
				return err
			}
		}

		ok, err := d.Inputs.UpdateIf(ctx, runID, inputID, model.RunInputCondition{State: &pendingInputState},
			model.RunInputPatch{
				State:           &answeredInputState,
				AnswerHMAC:      &hmac,
				AnswerContentID: &answerID,
				AnsweredAt:      &now,
			})
		if err != nil {
			return err
		}
		if !ok {
			return errRace
		}

		newStage := r.CurrentStage + 1
		nextFence, err := d.Stages.NextFence(ctx, runID)
		if err != nil {
			return err
		}
		remaining := int64(s.policy.ExecutionBudget.Seconds()) - r.CumulativeExecSeconds
		if remaining < 0 {
			remaining = 0
		}
		if err := d.Stages.Create(ctx, &model.RunStage{
			RunID: runID, StageNo: newStage, Fence: nextFence, Phase: "queued",
			BudgetRemainingSec: remaining,
		}); err != nil {
			return err
		}
		if ok, err := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{Status: &awaitingInputState},
			model.RunPatch{
				Status:       model.Optional[string]{Set: true, Value: string(contracts.RunStatusQueued)},
				CurrentStage: model.Optional[int]{Set: true, Value: newStage},
			}); err != nil {
			return err
		} else if !ok {
			return errAbort
		}
		if err := s.enqueueOutbox(ctx, d, runID, newStage, contracts.EventInputAnswered, now, answerID); err != nil {
			return err
		}
		res = &AnswerResult{Applied: true, RunID: runID, NewStage: newStage, NextFence: nextFence}
		return nil
	})
	if errors.Is(err, errRace) {

		cur, ge := s.store.DAOs().Inputs.Get(ctx, runID, inputID)
		if ge != nil {
			return nil, ge
		}
		if cur.AnswerHMAC != nil && storage.ConstantTimeEqual(cur.AnswerHMAC, hmac) {
			return &AnswerResult{Idempotent: true, RunID: runID, NewStage: r.CurrentStage}, nil
		}
		return nil, ErrAnswerConflict
	}
	if errors.Is(err, errAbort) {
		return nil, ErrRunStateConflict
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) CancelRun(ctx context.Context, clientID, runID string) (contracts.RunStatus, error) {
	now := s.clock.Now()
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil || r.ClientID != clientID {
		return "", ErrNotFound
	}
	cur := contracts.RunStatus(r.Status)
	if cur.IsTerminal() {
		return cur, nil
	}
	next := contracts.RunStatusCancelled
	if cur == contracts.RunStatusRunning {
		next = contracts.RunStatusCancelRequested
	}

	var got contracts.RunStatus
	err = s.store.Transaction(ctx, func(d model.DAOs) error {
		terminal := next.IsTerminal()
		var tAt *time.Time
		var metaExp *time.Time
		var cleanup string
		if terminal {
			t := now
			tAt = &t
			me := now.Add(s.policy.MetadataRetention)
			metaExp = &me
			cleanup = string(contracts.CleanupPending)
			if err := d.Contents.ExpireByRun(ctx, runID, now.Add(s.policy.ContentRetention)); err != nil {
				return err
			}
			if err := s.enqueueCleanupTx(ctx, d, "run_contents", runID, now); err != nil {
				return err
			}
		} else {
			cleanup = string(contracts.CleanupNotDue)
		}
		ok, err := d.Runs.UpdateIf(ctx, runID, model.RunCondition{Status: &r.Status},
			model.RunPatch{
				Status:            model.Optional[string]{Set: true, Value: string(next)},
				TerminalAt:        model.Optional[*time.Time]{Set: true, Value: tAt},
				MetadataExpiresAt: model.Optional[*time.Time]{Set: true, Value: metaExp},
				CleanupStatus:     model.Optional[string]{Set: true, Value: cleanup},
			})
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}
		return s.enqueueOutbox(ctx, d, runID, 0, contracts.EventRunTerminal, now, "")
	})
	if errors.Is(err, errAbort) {

		cur2, ge := s.store.DAOs().Runs.Get(ctx, runID)
		if ge != nil {
			return "", ge
		}
		if contracts.RunStatus(cur2.Status).IsTerminal() {
			return contracts.RunStatus(cur2.Status), nil
		}
		return "", ErrRunStateConflict
	}
	if err != nil {
		return "", err
	}
	got = next
	return got, nil
}

func (s *Service) EnqueueCleanup(ctx context.Context, resourceKind, internalRef string, dueAt time.Time) error {
	return s.store.Transaction(ctx, func(d model.DAOs) error {
		return s.enqueueCleanupTx(ctx, d, resourceKind, internalRef, dueAt)
	})
}

func (s *Service) enqueueCleanupTx(ctx context.Context, d model.DAOs, resourceKind, internalRef string, dueAt time.Time) error {
	return d.CleanupJobs.Create(ctx, &model.CleanupJob{ResourceKind: resourceKind, InternalRef: internalRef, DueAt: dueAt, Status: "pending"})
}

func (s *Service) putContent(ctx context.Context, d model.DAOs, runID string, stageNo int, purpose string, plaintext []byte, expiresAt *time.Time) (string, error) {
	if len(plaintext) == 0 {
		return "", nil
	}
	ct, err := s.cipher.Encrypt(purpose, plaintext)
	if err != nil {
		return "", fmt.Errorf("runs: encrypt %s: %w", purpose, err)
	}
	row := &model.RunContent{RunID: runID, StageNo: stageNo, Kind: purpose, Ciphertext: ct, KeyVersion: s.cipher.ActiveVersion(), ExpiresAt: expiresAt}
	if err := d.Contents.Create(ctx, row); err != nil {
		return "", err
	}
	return row.ID, nil
}

func (s *Service) enqueueOutbox(ctx context.Context, d model.DAOs, runID string, stageNo int, topic contracts.EventType, at time.Time, payloadRef string) error {
	var ref *string
	if payloadRef != "" {
		ref = &payloadRef
	}
	ev := &model.OutboxEvent{EventID: uuid.NewString(), RunID: &runID, StageNo: stageNo, Topic: string(topic), PayloadRef: ref, RetryAt: &at}
	return d.Outbox.Create(ctx, ev)
}

func (s *Service) chargeStageTime(ctx context.Context, d model.DAOs, runID string, stageNo int, fence int64, owner string, now time.Time) error {
	st, err := d.Stages.Get(ctx, runID, stageNo)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil
		}
		return err
	}
	if st.Fence != fence || st.Owner != owner {
		return nil
	}
	if st.StartedAt != nil {
		secs := int64(now.Sub(*st.StartedAt).Seconds())
		if secs < 0 {
			secs = 0
		}
		if err := d.Runs.AddExecSeconds(ctx, runID, secs, now); err != nil {
			return err
		}
	}
	return nil
}

func statusInAllowed(status contracts.RunStatus, allowed []contracts.RunStatus) bool {
	for _, a := range allowed {
		if a == status {
			return true
		}
	}
	return false
}

func recordFrom(r *model.Run) *RunRecord {
	return &RunRecord{
		ID: r.ID, ClientID: r.ClientID, ReqID: r.ReqID,
		Status: contracts.RunStatus(r.Status), CurrentStage: r.CurrentStage,
		TotalDeadline: r.TotalLifecycleDeadline, CreatedAt: r.CreatedAt,
		TerminalAt: r.TerminalAt, CleanupStatus: r.CleanupStatus,
	}
}
