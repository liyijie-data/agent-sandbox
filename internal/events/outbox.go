package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"agent-platform/internal/contracts"
	"agent-platform/model"
)

func (s *Service) materializeOutbox(ctx context.Context, ev contracts.Event, meta RunMeta, expiresAt time.Time) error {

	outEv := ev
	outEv.Seq = 0
	payload, err := json.Marshal(envelope{Event: outEv})
	if err != nil {
		return fmt.Errorf("events: outbox encode: %w", err)
	}
	ct, err := s.cipher.Encrypt(PurposeOutboxEvent, payload)
	if err != nil {
		return fmt.Errorf("events: outbox encrypt: %w", err)
	}

	contentID := uuid.NewString()
	runID := meta.RunID
	exp := expiresAt
	if err := s.contents.Create(ctx, &model.RunContent{
		ID:         contentID,
		RunID:      runID,
		StageNo:    meta.Stage,
		Kind:       "event",
		Ciphertext: ct,
		KeyVersion: s.cipher.ActiveVersion(),
		ExpiresAt:  &exp,
	}); err != nil {
		return fmt.Errorf("events: outbox content: %w", err)
	}
	if err := s.outbox.Create(ctx, &model.OutboxEvent{
		EventID:    ev.ID,
		RunID:      &runID,
		StageNo:    meta.Stage,
		Fence:      meta.Fence,
		Topic:      string(ev.Type),
		Status:     "pending",
		PayloadRef: &contentID,
		ExpiresAt:  &exp,
	}); err != nil {
		if errors.Is(err, model.ErrDuplicate) {
			return s.resolveOutboxDuplicate(ctx, ev, payload)
		}
		return fmt.Errorf("events: outbox create: %w", err)
	}
	return nil
}

func (s *Service) resolveOutboxDuplicate(ctx context.Context, ev contracts.Event, payload []byte) error {
	existing, err := s.outbox.Get(ctx, ev.ID)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("events: outbox lookup: %w", err)
	}
	if existing.PayloadRef == nil {
		return fmt.Errorf("events: %w: %s already recorded without a payload", ErrSourceSeqConflict, ev.Type)
	}
	content, err := s.contents.Get(ctx, *existing.PayloadRef)
	if err != nil {
		return fmt.Errorf("events: outbox payload lookup: %w", err)
	}
	pt, err := s.cipher.Decrypt(PurposeOutboxEvent, content.Ciphertext, content.KeyVersion)
	if err != nil {
		return fmt.Errorf("events: outbox payload decrypt: %w", err)
	}
	if bytes.Equal(pt, payload) {
		return nil
	}
	return fmt.Errorf("events: %w: %s source_seq %d", ErrSourceSeqConflict, ev.Type, ev.SourceSeq)
}
