package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/contracts"
	"agent-platform/internal/events"
	"agent-platform/internal/security"
	"agent-platform/internal/service/runs"
)

var runtimeEventsAccepts = []security.Purpose{security.PurposeRuntime}

func (s *Server) handleRuntimeEvent(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if s.events == nil {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, contracts.RuntimeEventMaxBodyBytes+1))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	if len(body) > contracts.RuntimeEventMaxBodyBytes {
		writeError(w, "", httpErr(http.StatusRequestEntityTooLarge, contracts.ErrPayloadTooLarge))
		return
	}
	req, verr := contracts.DecodeRuntimeEventEntry(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	ident, err := s.runs.ExecutionIdentity(r.Context(), req.ExecutionID)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
			return
		}
		writeError(w, "", errOutcome(err))
		return
	}
	si := &stageIdentity{RunID: ident.RunID, StageNo: ident.StageNo, Fence: ident.Fence, Status: ident.Status}
	if oe := s.verifyRuntimeToken(bearerToken(r), si, runtimeEventsAccepts); oe != nil {
		writeError(w, si.RunID, oe)
		return
	}

	run, err := s.retentionFacts(r.Context(), ident.RunID)
	if err != nil {
		writeError(w, si.RunID, errOutcome(err))
		return
	}

	in := events.RuntimeEventInput{ExecutionID: req.ExecutionID, SourceSeq: req.SourceSeq, Type: req.Type}
	switch req.Type {
	case contracts.EventAgentToken:
		var p contracts.AgentTokenPayload
		_ = json.Unmarshal(req.Payload, &p)
		in.Delta = p.Delta
	case contracts.EventAgentReasoning:
		var p contracts.AgentReasoningPayload
		_ = json.Unmarshal(req.Payload, &p)
		in.Delta = p.Delta
	case contracts.EventRuntimeProgress:
		var p contracts.RuntimeProgressPayload
		_ = json.Unmarshal(req.Payload, &p)
		in.Progress = &events.Progress{Phase: p.Phase, Value: int64(p.Value)}
	case contracts.EventAgentToolCall, contracts.EventAgentToolResult:
		var p contracts.AgentToolPayload
		_ = json.Unmarshal(req.Payload, &p)
		in.Tool = &p
	}
	res, err := s.events.RecordRuntimeEvent(r.Context(), events.RunMeta{
		RunID: ident.RunID, Stage: ident.StageNo, Fence: ident.Fence,
		SourceSeq: req.SourceSeq, HardDeadline: run.TotalDeadline, TerminalAt: run.TerminalAt,
	}, in)
	if err != nil {
		writeError(w, si.RunID, runtimeEventErr(err))
		return
	}
	writeJSON(w, http.StatusAccepted, contracts.RuntimeEventEntryResponse{
		Accepted: true, Seq: res.Event.Seq, Deduped: res.Deduped,
	})
	s.sseProbe(ident.RunID, "runtime_event_accepted",
		"source_seq", req.SourceSeq, "transport_seq", res.Event.Seq,
		"event_type", req.Type, "deduped", res.Deduped,
		"elapsed_ms", time.Since(started).Milliseconds())
}

func (s *Server) retentionFacts(ctx context.Context, runID string) (*runs.RunRecord, error) {
	cid, err := s.runs.RunClientID(ctx, runID)
	if err != nil {
		return nil, err
	}
	return s.runs.GetRun(ctx, cid, runID)
}

func runtimeEventErr(err error) *outcomeError {
	switch {
	case errors.Is(err, events.ErrNotWhitelisted), errors.Is(err, events.ErrInvalidEvent):
		return httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest)
	case errors.Is(err, events.ErrTooLarge):
		return httpErr(http.StatusRequestEntityTooLarge, contracts.ErrPayloadTooLarge)
	case errors.Is(err, events.ErrCapacity):
		return httpErr(http.StatusTooManyRequests, contracts.ErrResourceLimitExceeded)
	case errors.Is(err, events.ErrSourceSeqConflict):
		return httpErr(http.StatusConflict, contracts.ErrReqIDConflict)
	case errors.Is(err, events.ErrGap):
		return httpErr(http.StatusGone, contracts.ErrEventGap)
	case errors.Is(err, events.ErrExpired):
		return httpErr(http.StatusGone, contracts.ErrEventsExpired)
	default:
		return httpErr(http.StatusServiceUnavailable, "storage_unavailable")
	}
}

func (s *Server) handleGetRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	cid, err := s.authenticateRunBearer(r.Context(), runID, bearerToken(r))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	if s.events == nil {
		writeError(w, runID, httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}
	run, err := s.runs.GetRun(r.Context(), cid, runID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}

	now := s.clock.Now()
	retention := run.TotalDeadline
	if run.TerminalAt != nil {
		retention = run.TerminalAt.Add(s.retention)
	}
	if !now.Before(retention) {
		writeError(w, runID, httpErr(http.StatusGone, contracts.ErrEventsExpired))
		return
	}

	cur := events.Cursor{}
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		cur, err = events.ParseCursor(raw)
		if err != nil || cur.RunID() != runID {

			writeError(w, runID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
			return
		}
	}

	if _, err := s.events.Read(r.Context(), runID, cur, 0, 1); err != nil {
		switch {
		case errors.Is(err, events.ErrGap):
			writeError(w, runID, httpErr(http.StatusGone, contracts.ErrEventGap))
		case errors.Is(err, events.ErrExpired):
			writeError(w, runID, httpErr(http.StatusGone, contracts.ErrEventsExpired))
		default:
			writeError(w, runID, httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		}
		return
	}

	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, runID, httpErr(http.StatusInternalServerError, "internal_error"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	setSSEHeaders(w.Header())
	w.Header().Set("X-Run-ID", runID)
	f.Flush()

	poll := s.ssePoll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	keepalive := time.NewTicker(s.sseKeepalive)
	defer keepalive.Stop()

	emittedInput := ""
	lastQueueProgress := ""
	lastStatus := ""
	for {

		res, err := s.events.Read(r.Context(), runID, cur, poll, 100)
		if err != nil {
			code := sseFailureCode(err)
			s.sseFatalCode(w, f, code, "Event stream unavailable")
			return
		}
		if len(res.Events) > 0 {
			epoch := res.Cursor.Epoch()
			for _, ev := range res.Events {
				if !s.sseDeliver(w, f, runID, events.NewCursor(runID, epoch, ev.Seq), ev) {
					return
				}
			}
			cur = res.Cursor
		}

		status, serr := s.runs.StatusOf(r.Context(), runID)
		if serr == nil {
			if status == contracts.RunStatusAwaitingInput {
				if pi, perr := s.runs.PendingInput(r.Context(), runID); perr == nil && pi != nil &&
					pi.InputID != "" && pi.InputID != emittedInput {
					s.sseInput(w, f, runID, pi)
					emittedInput = pi.InputID
				}
			} else if status.IsTerminal() {
				if status == contracts.RunStatusSucceeded {
					s.sseStop(w, f, runID)
				} else {
					code, msg := terminalSSE(status)
					var details *contracts.RuntimeErrorDetails
					if status == contracts.RunStatusFailed {
						if result, rerr := s.runs.LoadRunResult(r.Context(), runID); rerr == nil && result != nil {
							details = contracts.NormalizeRuntimeErrorDetails(result.ErrorDetails)
							if result.ErrorCode == "context_limit_exceeded" {
								code, msg = "context_limit_exceeded", "Context length exceeded after compaction"
							} else if details != nil && details.UserMessage != "" {
								msg = details.UserMessage
							}
						}
					}
					s.sseFatalDetails(w, f, runID, code, msg, details)
				}
				return
			} else if status == contracts.RunStatusQueued {
				if prog, perr := s.runs.QueueProgress(r.Context(), runID); perr == nil && prog != nil && prog.WaitReason != "" {
					if sig := queueProgressSig(prog); sig != lastQueueProgress {
						s.sseQueueProgress(w, f, runID, prog)
						lastQueueProgress = sig
					}
				}
			} else if status == contracts.RunStatusPreparing && lastStatus != string(status) {
				s.sseStatus(w, f, runID, status)
				lastStatus = string(status)
			} else if status == contracts.RunStatusRunning && lastStatus != string(status) {
				s.sseStatus(w, f, runID, status)
				lastStatus = string(status)
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			f.Flush()
		default:
		}
	}
}

func (s *Server) sseDeliver(w io.Writer, f http.Flusher, runID string, cursor events.Cursor, ev contracts.Event) bool {
	switch ev.Type {
	case contracts.EventAgentToken, contracts.EventTextDelta:
		if ev.Delta == "" {
			return true
		}
		chunk := s.chatChunk(runID, map[string]any{"content": ev.Delta}, nil)
		sseFrame(w, f, cursor.String(), "", mustMarshal(chunk))
	case contracts.EventAgentReasoning:
		if ev.Delta == "" {
			return true
		}
		chunk := s.chatChunk(runID, map[string]any{"reasoning_content": ev.Delta}, nil)
		sseFrame(w, f, cursor.String(), "", mustMarshal(chunk))
	case contracts.EventAgentToolCall, contracts.EventAgentToolResult:
		if ev.Tool == nil {
			return true
		}
		payload := map[string]any{"run_id": runID, "tool_call_id": ev.Tool.ToolCallID, "tool_name": ev.Tool.ToolName, "status": ev.Tool.Status, "error_type": ev.Tool.ErrorType}
		if ev.Tool.ToolID != "" {
			payload["tool_id"] = ev.Tool.ToolID
		}
		if ev.Tool.Operation != "" {
			payload["operation"] = ev.Tool.Operation
		}
		if ev.Tool.ModelToolName != "" {
			payload["model_tool_name"] = ev.Tool.ModelToolName
		}
		if len(ev.Tool.Arguments) > 0 {
			payload["arguments"] = ev.Tool.Arguments
		}
		if len(ev.Tool.Result) > 0 {
			payload["result"] = ev.Tool.Result
		}
		if ev.Tool.DurationMs != nil {
			payload["duration_ms"] = ev.Tool.DurationMs
		}
		if ev.Tool.DetailsTruncated {
			payload["details_truncated"] = true
		}
		sseFrame(w, f, cursor.String(), string(ev.Type), mustMarshal(payload))
	case contracts.EventTextDone, contracts.EventRunTerminal:

		return true
	default:

		return true
	}
	s.sseProbe(runID, "sse_frame_flushed", "transport_seq", ev.Seq, "event_type", ev.Type)
	return true
}

func setSSEHeaders(h http.Header) {
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
}

func (s *Server) sseProbe(runID, action string, attrs ...any) {
	if runID == "" || os.Getenv("SSE_PROBE_RUN_ID") != runID {
		return
	}
	fields := append([]any{"sse_probe", action, "run_id", runID}, attrs...)
	s.log.Info("sse latency probe", fields...)
}

func (s *Server) sseQueueProgress(w io.Writer, f http.Flusher, runID string, p *runs.QueueProgress) {
	body := map[string]any{
		"run_id":         runID,
		"queue_position": p.QueuePosition,
		"queue_length":   p.QueueLength,
	}
	if p.WaitReason != "" {
		body["wait_reason"] = p.WaitReason
	}
	sseFrame(w, f, "", string(contracts.EventRunQueueProgress), mustMarshal(body))
}

func (s *Server) sseStatus(w io.Writer, f http.Flusher, runID string, status contracts.RunStatus) {
	event := ""
	switch status {
	case contracts.RunStatusPreparing:
		event = "run.preparing"
	case contracts.RunStatusRunning:
		event = string(contracts.EventRunStarted)
	default:
		return
	}
	sseFrame(w, f, "", event, mustMarshal(map[string]any{"run_id": runID, "status": status}))
}

func queueProgressSig(p *runs.QueueProgress) string {
	return fmt.Sprintf("%d/%d/%s", p.QueuePosition, p.QueueLength, p.WaitReason)
}

func (s *Server) sseInput(w io.Writer, f http.Flusher, runID string, pi *runs.PendingInput) {
	payload := mustMarshal(map[string]any{
		"run_id": runID, "input_id": pi.InputID, "kind": pi.Kind,
		"prompt": pi.Prompt, "options": pi.Options, "expires_at": pi.ExpiresAt,
	})
	sseFrame(w, f, "", "agent.request_input", payload)
}

func (s *Server) sseStop(w io.Writer, f http.Flusher, runID string) {
	chunk := s.chatChunk(runID, map[string]any{}, "stop")
	sseFrame(w, f, "", "", mustMarshal(chunk))
	fmt.Fprint(w, "data: [DONE]\n\n")
	f.Flush()
}

func (s *Server) sseFatalCode(w io.Writer, f http.Flusher, code, message string) {
	s.sseFatalDetails(w, f, "", code, message, nil)
}

func (s *Server) sseFatalDetails(w io.Writer, f http.Flusher, runID, code, message string, details *contracts.RuntimeErrorDetails) {
	details = contracts.NormalizeRuntimeErrorDetails(details)
	errBody := map[string]any{"message": message, "type": "agent_platform_error", "code": code}
	if runID != "" {
		errBody["run_id"] = runID
	}
	if details != nil {
		errBody["reason_code"] = details.ReasonCode
		if details.Phase != "" {
			errBody["phase"] = details.Phase
		}
		if details.UpstreamStatus != 0 {
			errBody["upstream_status"] = details.UpstreamStatus
		}
	}
	body := mustMarshal(map[string]any{
		"error": errBody,
	})
	sseFrame(w, f, "", "", body)
	fmt.Fprint(w, "data: [DONE]\n\n")
	f.Flush()
}

func sseFailureCode(err error) string {
	switch {
	case errors.Is(err, events.ErrGap):
		return string(contracts.ErrEventGap)
	case errors.Is(err, events.ErrExpired):
		return string(contracts.ErrEventsExpired)
	default:
		return "event_stream_unavailable"
	}
}

func terminalSSE(status contracts.RunStatus) (code, message string) {
	switch status {
	case contracts.RunStatusCancelled:
		return "run_cancelled", "Run cancelled"
	case contracts.RunStatusExpired:
		return "run_expired", "Run expired"
	default:
		return "run_failed", "Run failed"
	}
}

func (s *Server) chatChunk(runID string, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      runID,
		"object":  "chat.completion.chunk",
		"created": s.clock.Now().Unix(),
		"model":   s.modelName,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

func sseFrame(w io.Writer, f http.Flusher, id, event string, data []byte) {
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	f.Flush()
}
