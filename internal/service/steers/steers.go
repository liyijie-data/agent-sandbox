package steers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"agent-platform/internal/contracts"
	"agent-platform/internal/lifecycle"
	"agent-platform/internal/storage"
	"agent-platform/model"
)

const ContentSteer = "steer.message"

const steerIdempotencyPurpose = "steer_idempotency"

const batchCap = 100

var (
	ErrNotFound         = errors.New("steers: not found")
	ErrConflict         = errors.New("steers: steer conflict")
	ErrLimitExceeded    = errors.New("steers: steer limit exceeded")
	ErrCursorConflict   = errors.New("steers: steering cursor conflict")
	ErrRunStateConflict = errors.New("steers: run state conflict")
)

var errAbort = errors.New("steers: race lost")

var steerAcceptStatuses = []string{
	string(contracts.RunStatusQueued),
	string(contracts.RunStatusPreparing),
	string(contracts.RunStatusRunning),
	string(contracts.RunStatusAwaitingInput),
}

var steerExecStatuses = []string{
	string(contracts.RunStatusQueued),
	string(contracts.RunStatusRunning),
}

type Service struct {
	store  *model.Store
	cipher *storage.ContentCipher
	clock  lifecycle.Clock
}

func New(store *model.Store, cipher *storage.ContentCipher, clock lifecycle.Clock) *Service {
	if clock == nil {
		clock = lifecycle.SystemClock
	}
	return &Service{store: store, cipher: cipher, clock: clock}
}

type Receipt struct {
	RunID          string
	SteerID        string
	Seq            int64
	Status         contracts.SteerStatus
	ReasonCode     string
	AcceptedAt     time.Time
	IncorporatedAt *time.Time
	Stage          int
}

func receiptFrom(r *model.RunSteer) *Receipt {
	rc := ""
	if r.ReasonCode != nil {
		rc = *r.ReasonCode
	}
	stage := 0
	if r.IncorporatedStage != nil {
		stage = *r.IncorporatedStage
	}
	return &Receipt{
		RunID: r.RunID, SteerID: r.SteerID, Seq: r.Seq,
		Status: contracts.SteerStatus(r.Status), ReasonCode: rc,
		AcceptedAt: r.AcceptedAt, IncorporatedAt: r.IncorporatedAt, Stage: stage,
	}
}

func (s *Service) Create(ctx context.Context, clientID, runID, steerID string, messageJSON []byte) (*Receipt, error) {
	now := s.clock.Now()
	hmac := s.cipher.Fingerprint(steerIdempotencyPurpose, []byte(steerID+"\x00"+string(messageJSON)))

	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return nil, ErrNotFound
	}
	if r.ClientID != clientID {
		return nil, ErrNotFound
	}
	if !steerAcceptable(contracts.RunStatus(r.Status)) || !r.TotalLifecycleDeadline.After(now) {
		return nil, ErrRunStateConflict
	}
	if existing, e := s.store.DAOs().Steers.Get(ctx, clientID, runID, steerID); e == nil {
		return s.classifyExisting(hmac, existing)
	}

	var res *Receipt
	err = s.store.Transaction(ctx, func(d model.DAOs) error {
		seq, ok, err := d.Runs.TakeSteerSlot(ctx, runID, int64(len(messageJSON)),
			steerAcceptStatuses, contracts.SteerMaxPerRun, contracts.SteerContentTotalMaxBytes, now)
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}
		contentID, err := s.putContent(ctx, d, runID, ContentSteer, messageJSON)
		if err != nil {
			return err
		}
		if err := d.Steers.Create(ctx, &model.RunSteer{
			ClientID: clientID, RunID: runID, SteerID: steerID, Seq: seq,
			ContentID: &contentID, ContentHash: hmac, Status: string(contracts.SteerPending),
		}); err != nil {
			if model.IsDuplicate(err) {
				return errAbort
			}
			return err
		}
		if err := s.enqueueOutbox(ctx, d, runID, 0, contracts.EventSteerAccepted, now, contentID); err != nil {
			return err
		}
		res = &Receipt{RunID: runID, SteerID: steerID, Seq: seq, Status: contracts.SteerPending, AcceptedAt: now}
		return nil
	})
	if errors.Is(err, errAbort) {

		cur, ge := s.store.DAOs().Runs.Get(ctx, runID)
		if ge != nil {
			return nil, ErrNotFound
		}
		if cur.ClientID != clientID {
			return nil, ErrNotFound
		}
		if !steerAcceptable(contracts.RunStatus(cur.Status)) || !cur.TotalLifecycleDeadline.After(now) {
			return nil, ErrRunStateConflict
		}
		if existing, se := s.store.DAOs().Steers.Get(ctx, clientID, runID, steerID); se == nil {
			return s.classifyExisting(hmac, existing)
		}
		return nil, ErrLimitExceeded
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) classifyExisting(hmac []byte, existing *model.RunSteer) (*Receipt, error) {
	if existing.ContentHash == nil || !storage.ConstantTimeEqual(existing.ContentHash, hmac) {
		return nil, ErrConflict
	}
	return receiptFrom(existing), nil
}

func (s *Service) GetReceipt(ctx context.Context, clientID, runID, steerID string) (*Receipt, error) {
	r, err := s.store.DAOs().Steers.Get(ctx, clientID, runID, steerID)
	if err != nil {
		return nil, ErrNotFound
	}
	return receiptFrom(r), nil
}

func (s *Service) ConfirmedCursor(ctx context.Context, runID string) (int64, error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return r.IncorporatedThrough, nil
}

func (s *Service) HasUnackedBatch(ctx context.Context, runID string) (bool, error) {
	n, err := s.store.DAOs().Batches.CountByRunAndStatus(ctx, runID, "issued")
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

type Item struct {
	Seq     int64
	SteerID string
	Message []byte
}

type Pull struct {
	BatchID    string
	ThroughSeq int64
	Items      []Item
}

func (s *Service) Pull(ctx context.Context, runID, executionID string, stageNo int, fence int64, afterSeq int64) (*Pull, error) {
	now := s.clock.Now()
	var res *Pull
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return errAbort
		}
		if !steerExecuting(contracts.RunStatus(r.Status)) || !r.TotalLifecycleDeadline.After(now) || r.CurrentStage != stageNo {
			return errAbort
		}
		st, err := d.Stages.Get(ctx, runID, stageNo)
		if err != nil {
			return errAbort
		}
		if st.ExecutionID != executionID || st.Fence != fence || !st.LaunchStarted {
			return errAbort
		}

		if ok, err := d.Stages.UpdateIf(ctx, runID, stageNo,
			model.RunStageCondition{Fence: &fence, ExecutionID: &executionID, LaunchStarted: boolPtr(true)},
			model.RunStagePatch{UpdatedAt: &now}); err != nil {
			return err
		} else if !ok {
			return errAbort
		}

		pend, err := d.Steers.ListAfter(ctx, runID, afterSeq, string(contracts.SteerPending), batchCap)
		if err != nil {
			return err
		}
		if len(pend) == 0 {
			res = &Pull{ThroughSeq: afterSeq}
			return nil
		}

		batch, err := d.Batches.GetIssuedByExecution(ctx, runID, executionID, stageNo, fence, "issued")
		if err == nil {
			res, err = s.fillPull(ctx, d, batch, pend)
			return err
		}
		if !errors.Is(err, model.ErrNotFound) {
			return err
		}
		created := &model.SteerBatch{
			RunID: runID, BatchID: uuid.NewString(), ExecutionID: executionID,
			StageNo: stageNo, Fence: fence,
			StartSeq: pend[0].Seq, ThroughSeq: pend[len(pend)-1].Seq, Status: "issued",
		}
		if err := d.Batches.Create(ctx, created); err != nil {
			if model.IsDuplicate(err) {

				b2, e2 := d.Batches.GetIssuedByExecution(ctx, runID, executionID, stageNo, fence, "issued")
				if e2 != nil {
					return e2
				}
				res, err = s.fillPull(ctx, d, b2, pend)
				return err
			}
			return err
		}
		res, err = s.fillPull(ctx, d, created, pend)
		return err
	})
	if errors.Is(err, errAbort) {
		return nil, ErrRunStateConflict
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) fillPull(ctx context.Context, d model.DAOs, b *model.SteerBatch, pend []model.RunSteer) (*Pull, error) {
	items := make([]Item, 0, len(pend))
	for _, st := range pend {
		if st.Seq > b.ThroughSeq {
			break
		}
		if st.ContentID == nil {
			continue
		}
		ct, err := d.Contents.Get(ctx, *st.ContentID)
		if err != nil {
			return nil, err
		}
		dec, err := s.cipher.Decrypt(ContentSteer, ct.Ciphertext, ct.KeyVersion)
		if err != nil {
			return nil, fmt.Errorf("steers: decrypt pull item: %w", err)
		}
		items = append(items, Item{Seq: st.Seq, SteerID: st.SteerID, Message: dec})
	}
	return &Pull{BatchID: b.BatchID, ThroughSeq: b.ThroughSeq, Items: items}, nil
}

func (s *Service) Ack(ctx context.Context, runID, batchID, executionID string, throughSeq int64) (int64, error) {
	now := s.clock.Now()
	var cursor int64
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return errAbort
		}
		if !steerExecuting(contracts.RunStatus(r.Status)) || !r.TotalLifecycleDeadline.After(now) {
			return errAbort
		}
		curStage := r.CurrentStage
		st, err := d.Stages.Get(ctx, runID, curStage)
		if err != nil {
			return errAbort
		}
		b, err := d.Batches.Get(ctx, batchID)
		if err != nil {
			return errAbort
		}

		if b.RunID != runID || b.ExecutionID != executionID || b.StageNo != curStage || b.Fence != st.Fence {
			return errAbort
		}
		if b.Status == "acked" {
			cursor = b.ThroughSeq
			return nil
		}
		if b.ThroughSeq != throughSeq {
			return errAbort
		}

		if ok, err := d.Stages.UpdateIf(ctx, runID, curStage,
			model.RunStageCondition{Fence: &st.Fence, ExecutionID: &st.ExecutionID},
			model.RunStagePatch{UpdatedAt: &now}); err != nil {
			return err
		} else if !ok {
			return errAbort
		}

		if ok, err := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{StatusIn: steerExecStatuses, CurrentStage: &curStage},
			model.RunPatch{
				IncorporatedThrough: model.Optional[int64]{Set: true, Value: throughSeq},
				UpdatedAt:           model.Optional[time.Time]{Set: true, Value: now},
			}); err != nil {
			return err
		} else if !ok {
			return errAbort
		}

		if ok, err := d.Batches.UpdateIf(ctx, batchID,
			model.SteerBatchCondition{Status: strPtr("issued"), ExecutionID: &executionID, Fence: &st.Fence},
			model.SteerBatchPatch{AckedAt: &now, Status: strPtr("acked")}); err != nil {
			return err
		} else if !ok {
			return errAbort
		}
		if _, err := d.Steers.MarkIncorporatedUpTo(ctx, runID, throughSeq, curStage,
			string(contracts.SteerPending), string(contracts.SteerIncorporated), now); err != nil {
			return err
		}
		if err := s.enqueueOutbox(ctx, d, runID, 0, contracts.EventSteerIncorporated, now, ""); err != nil {
			return err
		}
		cursor = throughSeq
		return nil
	})
	if errors.Is(err, errAbort) {

		cur, re := s.store.DAOs().Runs.Get(ctx, runID)
		if re == nil && steerExecuting(contracts.RunStatus(cur.Status)) {
			st, se := s.store.DAOs().Stages.Get(ctx, runID, cur.CurrentStage)
			b, be := s.store.DAOs().Batches.Get(ctx, batchID)
			if se == nil && be == nil && b.RunID == runID && b.ExecutionID == executionID &&
				b.StageNo == cur.CurrentStage && b.Fence == st.Fence && b.Status == "acked" {
				return b.ThroughSeq, nil
			}
		}
		if re == nil && !steerExecuting(contracts.RunStatus(cur.Status)) {
			return 0, ErrRunStateConflict
		}
		return 0, ErrCursorConflict
	}
	if err != nil {
		return 0, err
	}
	return cursor, nil
}

func (s *Service) TerminalClose(ctx context.Context, runID string, reason contracts.SteerReasonCode) error {
	return s.store.Transaction(ctx, func(d model.DAOs) error {
		maxThrough, err := d.Batches.MaxIssuedThroughSeq(ctx, runID, "issued")
		if err != nil {
			return err
		}
		if maxThrough > 0 {
			if _, err := d.Steers.CloseUpTo(ctx, runID, string(contracts.SteerPending), maxThrough,
				string(contracts.SteerUnknown), string(contracts.SteerReasonOutcomeUnknown)); err != nil {
				return err
			}
		}
		_, err = d.Steers.CloseAll(ctx, runID, string(contracts.SteerPending),
			string(contracts.SteerNotApplied), string(reason))
		return err
	})
}

func steerAcceptable(st contracts.RunStatus) bool {
	switch st {
	case contracts.RunStatusQueued, contracts.RunStatusPreparing, contracts.RunStatusRunning, contracts.RunStatusAwaitingInput:
		return true
	}
	return false
}

func steerExecuting(st contracts.RunStatus) bool {
	switch st {
	case contracts.RunStatusQueued, contracts.RunStatusRunning:
		return true
	}
	return false
}

func (s *Service) putContent(ctx context.Context, d model.DAOs, runID string, purpose string, plaintext []byte) (string, error) {
	ct, err := s.cipher.Encrypt(purpose, plaintext)
	if err != nil {
		return "", fmt.Errorf("steers: encrypt %s: %w", purpose, err)
	}
	row := &model.RunContent{RunID: runID, StageNo: 0, Kind: purpose, Ciphertext: ct, KeyVersion: s.cipher.ActiveVersion()}
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

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
