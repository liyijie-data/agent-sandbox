package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/storage"
)

type ReadResult struct {
	Events []contracts.Event
	Cursor Cursor
}

func (s *Service) Read(ctx context.Context, runID string, cur Cursor, block time.Duration, maxEvents int) (ReadResult, error) {
	if cur.RunID() != "" && cur.RunID() != runID {
		return ReadResult{}, fmt.Errorf("events: %w: cursor belongs to run %s", ErrInvalidCursor, cur.RunID())
	}
	entries, err := s.cache.Read(ctx, runID, cur.Epoch(), cur.Seq(), int64(maxEvents), block)
	if err != nil {
		return ReadResult{}, mapCacheErr(err)
	}
	out := make([]contracts.Event, 0, len(entries))
	next := cur
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		env, err := s.decodeEntry(entry)
		if err != nil {
			return ReadResult{}, err
		}
		ev := env.Event
		if ev.ID != "" {
			if seen[ev.ID] {
				continue
			}
			seen[ev.ID] = true
		}
		ev.Seq = entry.Seq
		out = append(out, ev)
		next = NewCursor(runID, entry.Epoch, entry.Seq)
	}
	return ReadResult{Events: out, Cursor: next}, nil
}

func (s *Service) decodeEntry(entry storage.EventEntry) (envelope, error) {
	pt, err := s.cipher.Decrypt(PurposeEventStream, entry.Payload, entry.KeyVersion)
	if err != nil {
		return envelope{}, fmt.Errorf("events: decrypt stream entry %s: %w", entry.StreamID, err)
	}
	var env envelope
	if err := json.Unmarshal(pt, &env); err != nil {
		return envelope{}, fmt.Errorf("events: decode stream entry %s: %w", entry.StreamID, err)
	}
	return env, nil
}
