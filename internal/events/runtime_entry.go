package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"agent-platform/internal/contracts"
)

type RuntimeEventInput struct {
	ExecutionID string
	SourceSeq   int64
	Type        contracts.EventType
	Delta       string
	Progress    *Progress
	Tool        *contracts.AgentToolPayload
}

type RuntimeRecordResult struct {
	Event   contracts.Event
	Cursor  Cursor
	Deduped bool
}

func (s *Service) RecordRuntimeEvent(ctx context.Context, meta RunMeta, in RuntimeEventInput) (RuntimeRecordResult, error) {
	ev := contracts.Event{
		Type: in.Type, RunID: meta.RunID, Stage: meta.Stage, Fence: meta.Fence,
		SourceSeq: in.SourceSeq, Delta: in.Delta, Tool: in.Tool,
	}
	var progress *Progress
	if in.Type == EventRuntimeProgress {
		progress = in.Progress
	}
	if err := s.ValidateEvent(ev, progress); err != nil {
		return RuntimeRecordResult{}, err
	}
	if meta.RunID == "" || (meta.HardDeadline.IsZero() && meta.TerminalAt.IsZero()) {
		return RuntimeRecordResult{}, fmt.Errorf("events: %w: run has no retention deadline", ErrInvalidEvent)
	}

	expiresAt := meta.ExpiresAt(s.retention)
	payload, err := json.Marshal(envelope{Event: ev, Progress: progress})
	if err != nil {
		return RuntimeRecordResult{}, fmt.Errorf("events: encode: %w", err)
	}
	ct, err := s.cipher.Encrypt(PurposeEventStream, payload)
	if err != nil {
		return RuntimeRecordResult{}, fmt.Errorf("events: encrypt stream payload: %w", err)
	}

	hash := runtimeContentHash(in)
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return RuntimeRecordResult{}, fmt.Errorf("events: %w: run events already expired", ErrExpired)
	}
	claimed, err := s.cache.ProducerSeqClaim(ctx, meta.RunID, in.ExecutionID, in.SourceSeq, hash, ttl)
	if err != nil {
		return RuntimeRecordResult{}, mapCacheErr(err)
	}
	if !claimed {
		return RuntimeRecordResult{Event: ev, Deduped: true}, nil
	}
	seq, epoch, err := s.cache.Append(ctx, meta.RunID, string(ev.Type), s.cipher.ActiveVersion(), ct, expiresAt)
	if err != nil {
		_ = s.cache.ReleaseProducerSeq(ctx, meta.RunID, in.ExecutionID, in.SourceSeq)
		return RuntimeRecordResult{}, mapCacheErr(err)
	}
	ev.Seq = seq
	return RuntimeRecordResult{Event: ev, Cursor: NewCursor(meta.RunID, epoch, seq)}, nil
}

func runtimeContentHash(in RuntimeEventInput) string {
	h := sha256.New()
	h.Write([]byte(in.Type))
	h.Write([]byte{0})
	if in.Type == EventRuntimeProgress && in.Progress != nil {
		h.Write([]byte(in.Progress.Phase))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(in.Progress.Value, 10)))
	} else if in.Tool != nil {
		h.Write([]byte(in.Tool.ToolCallID))
		h.Write([]byte{0})
		h.Write([]byte(in.Tool.ToolName))
		h.Write([]byte{0})
		h.Write([]byte(in.Tool.Status))
		h.Write([]byte{0})
		h.Write([]byte(in.Tool.ErrorType))
		if in.Tool.ToolID != "" || in.Tool.Operation != "" || in.Tool.ModelToolName != "" || len(in.Tool.Arguments) > 0 || len(in.Tool.Result) > 0 || in.Tool.DurationMs != nil || in.Tool.DetailsTruncated {
			b, _ := json.Marshal(struct {
				ToolID           string          `json:"tool_id,omitempty"`
				Operation        string          `json:"operation,omitempty"`
				ModelToolName    string          `json:"model_tool_name,omitempty"`
				Arguments        json.RawMessage `json:"arguments,omitempty"`
				Result           json.RawMessage `json:"result,omitempty"`
				DurationMs       *int64          `json:"duration_ms,omitempty"`
				DetailsTruncated bool            `json:"details_truncated,omitempty"`
			}{in.Tool.ToolID, in.Tool.Operation, in.Tool.ModelToolName, in.Tool.Arguments, in.Tool.Result, in.Tool.DurationMs, in.Tool.DetailsTruncated})
			h.Write([]byte{0})
			h.Write(b)
		}
	} else {
		h.Write([]byte(in.Delta))
	}
	return hex.EncodeToString(h.Sum(nil))
}
