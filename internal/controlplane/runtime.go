package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"agent-platform/internal/contracts"
	"agent-platform/internal/security"
	"agent-platform/internal/service/runs"
)

var (
	pullAccepts = []security.Purpose{security.PurposeSteeringRead, security.PurposeRuntime}
	ackAccepts  = []security.Purpose{security.PurposeSteeringAck, security.PurposeRuntime}
)

type stageIdentity struct {
	RunID   string
	StageNo int
	Fence   int64
	Status  string
}

func (s *Server) verifyRuntimeToken(token string, si *stageIdentity, accepts []security.Purpose) *outcomeError {
	if token == "" {
		return httpErr(http.StatusUnauthorized, "unauthorized")
	}
	now := s.clock.Now()
	matched := false
	for _, p := range accepts {
		if err := s.signer.Verify(token, si.RunID, si.StageNo, si.Fence, p, now); err == nil {
			matched = true
			break
		}
	}
	if !matched {
		return httpErr(http.StatusUnauthorized, "unauthorized")
	}

	if st := contracts.RunStatus(si.Status); st != contracts.RunStatusQueued && st != contracts.RunStatusRunning {
		return httpErr(http.StatusConflict, contracts.ErrRuntimeStateConflict)
	}
	return nil
}

func (s *Server) handleSteerPull(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrRuntimeStateConflict))
		return
	}
	req, verr := contracts.DecodeSteerPullRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(httpErr(http.StatusConflict, contracts.ErrSteeringCursorConflict)))
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
	if oe := s.verifyRuntimeToken(bearerToken(r), si, pullAccepts); oe != nil {
		writeError(w, si.RunID, oe)
		return
	}

	pull, err := s.steers.Pull(r.Context(), si.RunID, req.ExecutionID, si.StageNo, si.Fence, req.AfterSeq)
	if err != nil {

		oe := errOutcome(err)
		if oe.Status != http.StatusConflict {
			oe = httpErr(http.StatusServiceUnavailable, "storage_unavailable")
		}
		writeError(w, si.RunID, oe)
		return
	}
	resp := contracts.SteerPullResponse{ThroughSeq: pull.ThroughSeq}
	if pull.BatchID != "" {
		resp.BatchID = &pull.BatchID
	}
	for _, it := range pull.Items {
		var msg contracts.Message
		_ = json.Unmarshal(it.Message, &msg)
		resp.Items = append(resp.Items, contracts.SteerItem{SteerID: it.SteerID, Seq: it.Seq, Message: msg})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSteerAck(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrRuntimeStateConflict))
		return
	}
	req, verr := contracts.DecodeSteerAckRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(httpErr(http.StatusConflict, contracts.ErrSteeringCursorConflict)))
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
	if oe := s.verifyRuntimeToken(bearerToken(r), si, ackAccepts); oe != nil {
		writeError(w, si.RunID, oe)
		return
	}

	cursor, err := s.steers.Ack(r.Context(), si.RunID, req.BatchID, req.ExecutionID, req.IncorporatedThroughSeq)
	if err != nil {
		oe := errOutcome(err)
		if oe.Status != http.StatusConflict {
			oe = httpErr(http.StatusServiceUnavailable, "storage_unavailable")
		}
		writeError(w, si.RunID, oe)
		return
	}
	writeJSON(w, http.StatusOK, contracts.SteerAckResponse{IncorporatedThroughSeq: cursor})
}
