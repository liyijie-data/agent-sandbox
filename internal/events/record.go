package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"agent-platform/internal/contracts"
)

func (s *Service) Record(ctx context.Context, ev contracts.Event, meta RunMeta, progress *Progress) (contracts.Event, Cursor, error) {
	if meta.RunID == "" {
		return ev, Cursor{}, fmt.Errorf("events: %w: run_id is required", ErrInvalidEvent)
	}
	if meta.HardDeadline.IsZero() && meta.TerminalAt.IsZero() {
		return ev, Cursor{}, fmt.Errorf("events: %w: run has no retention deadline", ErrInvalidEvent)
	}
	if err := s.ValidateEvent(ev, progress); err != nil {
		return ev, Cursor{}, err
	}

	ev.RunID = meta.RunID
	if ev.Stage == 0 {
		ev.Stage = meta.Stage
	}
	if ev.Fence == 0 {
		ev.Fence = meta.Fence
	}
	if ev.SourceSeq == 0 {
		ev.SourceSeq = meta.SourceSeq
	}
	if ev.ID == "" {
		if ev.KeyEvent() {

			ev.ID = deterministicEventID(meta.RunID, meta.Stage, meta.SourceSeq, ev.Type)
		} else {
			ev.ID = uuid.NewString()
		}
	}
	expiresAt := meta.ExpiresAt(s.retention)

	if ev.KeyEvent() {
		if err := s.materializeOutbox(ctx, ev, meta, expiresAt); err != nil {
			return ev, Cursor{}, err
		}
	}

	payload, err := json.Marshal(envelope{Event: ev, Progress: progress})
	if err != nil {
		return ev, Cursor{}, fmt.Errorf("events: encode: %w", err)
	}
	ct, err := s.cipher.Encrypt(PurposeEventStream, payload)
	if err != nil {
		return ev, Cursor{}, fmt.Errorf("events: encrypt stream payload: %w", err)
	}
	seq, epoch, err := s.cache.Append(ctx, meta.RunID, string(ev.Type), s.cipher.ActiveVersion(), ct, expiresAt)
	if err != nil {
		return ev, Cursor{}, mapCacheErr(err)
	}
	ev.Seq = seq
	return ev, NewCursor(meta.RunID, epoch, seq), nil
}
