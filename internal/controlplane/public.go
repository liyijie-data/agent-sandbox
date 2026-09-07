package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/contracts"
	"agent-platform/internal/images"
	"agent-platform/internal/service/runs"
	"agent-platform/internal/service/steers"
)

func (s *Server) authenticateRunBearer(ctx context.Context, runID, bearer string) (string, error) {
	if bearer == "" {
		return "", httpErr(http.StatusUnauthorized, "unauthorized")
	}
	cid, err := s.runs.RunClientID(ctx, runID)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			return "", httpErr(http.StatusNotFound, notFoundCode)
		}
		return "", err
	}
	ok, err := s.runs.AuthenticateClientKey(ctx, cid, bearer)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", httpErr(http.StatusUnauthorized, "unauthorized")
	}
	return cid, nil
}

func bearerToken(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	cid, err := s.authenticateRunBearer(r.Context(), runID, bearerToken(r))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}

	cur, err := s.runs.StatusOf(r.Context(), runID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	status, err := s.runs.CancelRun(r.Context(), cid, runID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	code := http.StatusAccepted
	if cur.IsTerminal() {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"run_id": runID, "status": status})
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	rawKey := bearerToken(r)
	clientID, err := s.runs.AuthenticateClient(r.Context(), rawKey)
	if err != nil {
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, contracts.CreateRunMaxBytes+1))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	if int64(len(body)) > contracts.CreateRunMaxBytes {
		writeError(w, "", httpErr(http.StatusRequestEntityTooLarge, contracts.ErrPayloadTooLarge))
		return
	}
	req, verr := contracts.DecodeCreateRunRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}

	if s.imagesSvc == nil {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}
	reg, err := s.imagesSvc.GetEnabledRegistration(r.Context(), clientID, req.Sandbox.ImageID)
	if err != nil {
		if errors.Is(err, images.ErrNotFound) {
			writeError(w, "", httpErr(http.StatusUnprocessableEntity, contracts.ErrCapabilityUnsupported))
			return
		}
		writeError(w, "", errOutcome(err))
		return
	}
	if reg.Repository == "" {
		writeError(w, "", httpErr(http.StatusUnprocessableEntity, contracts.ErrSandboxImageUnavailable))
		return
	}

	if verr := contracts.ValidateCapabilityAdmission(req, reg.Capabilities); verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}

	var networkRevisionID *string
	if s.networks != nil {
		blocked, err := s.networks.BlockedForRun(r.Context(), clientID, req.Sandbox.ImageID)
		if err != nil {
			writeError(w, "", errOutcome(err))
			return
		}
		if blocked {
			writeError(w, "", httpErr(http.StatusConflict, contracts.ErrNetworkRolloutBlocked))
			return
		}

		networkRevisionID, err = s.networks.EffectiveRevisionFor(r.Context(), clientID, req.Sandbox.ImageID)
		if err != nil {
			writeError(w, "", errOutcome(err))
			return
		}
	}
	ready, err := s.runs.RuntimeProfileReady(r.Context(), reg.ID, networkRevisionID)
	if err != nil {
		writeError(w, "", errOutcome(err))
		return
	}
	if !ready {
		writeError(w, "", httpErr(http.StatusUnprocessableEntity, contracts.ErrSandboxImageUnavailable))
		return
	}

	configJSON := mustMarshal(req)
	fp := s.runs.FingerprintCreate(normalizeCreate(req))
	messagesJSON := mustMarshal(req.Messages)

	res, err := s.runs.CreateRun(r.Context(), runs.CreateRunInput{
		ClientID: clientID, ReqID: req.ReqID, Fingerprint: fp,
		Messages: messagesJSON, Config: configJSON,
		ImageRegistrationID: &reg.ID, NetworkRevisionID: networkRevisionID,
	})
	if err != nil {
		writeError(w, req.ReqID, errOutcome(err))
		return
	}

	status := http.StatusCreated
	if res.Idempotent {
		status = http.StatusOK
	}

	w.Header().Set("X-Run-ID", res.Run.ID)
	w.Header().Set("X-Trace-ID", res.Run.ID)
	writeJSON(w, status, &contracts.RunResponse{
		ID: res.Run.ID, ReqID: res.Run.ReqID, Status: contracts.RunStatus(res.Run.Status),
		Stage: res.Run.CurrentStage, ExpiresAt: res.Run.TotalDeadline,
		CleanupStatus: contracts.CleanupStatus(res.Run.CleanupStatus), ContentAvailable: true,
	})
}

func normalizeCreate(req *contracts.CreateRunRequest) []byte {
	type stableResource struct {
		ID, Name, SHA256 string
		SizeBytes        int64
	}
	type stableTool struct {
		ID, Type, Target   string
		AllowedActions     []string
		BaseURL, URL       string
		Transport          string
		SpecID, SpecSHA256 string
		SpecSizeBytes      int64
	}
	stable := struct {
		SandboxImageID string
		Messages       []contracts.Message
		ModelName      string
		ModelProvider  string
		ModelBaseURL   string
		Files          []stableResource
		Skills         []stableResource
		Tools          []stableTool
		ResultDest     string
	}{SandboxImageID: req.Sandbox.ImageID, Messages: req.Messages,
		ModelName: req.Model.Name, ModelProvider: req.Model.Provider, ModelBaseURL: req.Model.BaseURL}
	for _, f := range req.Files {
		stable.Files = append(stable.Files, stableResource{ID: f.ID, Name: f.Name, SHA256: f.SHA256, SizeBytes: f.SizeBytes})
	}
	for _, sk := range req.Skills {
		stable.Skills = append(stable.Skills, stableResource{ID: sk.ID, Name: sk.Name, SHA256: sk.SHA256, SizeBytes: sk.SizeBytes})
	}
	for _, t := range req.Tools {
		st := stableTool{ID: t.ID, Type: t.Type, Target: t.Target, AllowedActions: t.AllowedActions,
			BaseURL: t.BaseURL, URL: t.URL, Transport: t.Transport}
		if t.Spec != nil {
			st.SpecID, st.SpecSHA256, st.SpecSizeBytes = t.Spec.ID, t.Spec.SHA256, t.Spec.SizeBytes
		}
		stable.Tools = append(stable.Tools, st)
	}
	if req.ResultBundle != nil {
		stable.ResultDest = req.ResultBundle.DestinationID
	}
	return mustMarshal(stable)
}
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	cid, err := s.authenticateRunBearer(r.Context(), runID, bearerToken(r))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	run, err := s.runs.GetRun(r.Context(), cid, runID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	pi, err := s.runs.PendingInput(r.Context(), runID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	resp := &contracts.RunResponse{
		ID: run.ID, ReqID: run.ReqID, Status: run.Status, Stage: run.CurrentStage,
		ExpiresAt: run.TotalDeadline, CleanupStatus: contracts.CleanupStatus(run.CleanupStatus),
	}

	if avail, expAt, aerr := s.runs.ContentAvailability(r.Context(), runID); aerr == nil {
		resp.ContentAvailable = avail
		resp.ContentExpiresAt = expAt
	} else {
		resp.ContentAvailable = true
	}
	if pi != nil {
		resp.PendingInput = &contracts.PendingInput{
			ID: pi.InputID, Kind: pi.Kind, Prompt: pi.Prompt,
			Options: pi.Options, ExpiresAt: pi.ExpiresAt,
		}
		if !pi.Readable {
			resp.ContentAvailable = false
		}
	}

	if rr, err := s.runs.LoadRunResult(r.Context(), runID); err == nil && rr != nil {
		resp.Result = &contracts.ResultSummary{
			Status:        rr.Delivery.Status,
			DestinationID: rr.Delivery.DestinationID,
			SHA256:        rr.Delivery.SHA256,
			SizeBytes:     rr.Delivery.SizeBytes,
			Summary:       rr.Summary,
			ErrorDetails:  contracts.NormalizeRuntimeErrorDetails(rr.ErrorDetails),
			Diagnostics:   contracts.NormalizeDiagnosticOutcome(rr.Diagnostics),
		}
		if rr.Status == contracts.RuntimeError && contracts.IsPublicRuntimeErrorCode(rr.ErrorCode) {
			resp.Result.ErrorCode = rr.ErrorCode
			resp.Result.Summary = ""
			if resp.Result.Status == "" {
				resp.Result.Status = contracts.DeliveryNotRequested
			}
		}
	}

	if prog, perr := s.runs.QueueProgress(r.Context(), runID); perr == nil && prog != nil && prog.WaitReason != "" {
		pos, length := prog.QueuePosition, prog.QueueLength
		resp.QueuePosition = &pos
		resp.QueueLength = &length
		if prog.WaitReason != "" {
			reason := prog.WaitReason
			resp.WaitReason = &reason
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateSteer(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	clientID, err := s.authenticateRunBearer(r.Context(), runID, bearerToken(r))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, contracts.SteerRequestMaxBytes+1))
	if err != nil {
		writeError(w, runID, httpErr(http.StatusBadRequest, contracts.ErrInvalidSteer))
		return
	}
	if int64(len(body)) > contracts.SteerRequestMaxBytes {
		writeError(w, runID, httpErr(http.StatusRequestEntityTooLarge, contracts.ErrPayloadTooLarge))
		return
	}
	req, verr := contracts.DecodeSteerRequest(body)
	if verr != nil {
		writeError(w, runID, errOutcome(verr))
		return
	}

	_, existed := s.steers.GetReceipt(r.Context(), clientID, runID, req.SteerID)
	rec, err := s.steers.Create(r.Context(), clientID, runID, req.SteerID, mustMarshal(req.Message))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	status := http.StatusAccepted
	if existed == nil {
		status = http.StatusOK
	}
	writeJSON(w, status, publicReceipt(rec))
}

func (s *Server) handleGetSteer(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	steerID := chi.URLParam(r, "steerID")
	clientID, err := s.authenticateRunBearer(r.Context(), runID, bearerToken(r))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	rec, err := s.steers.GetReceipt(r.Context(), clientID, runID, steerID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, publicReceipt(rec))
}

func publicReceipt(rec *steers.Receipt) *contracts.SteerReceipt {
	out := &contracts.SteerReceipt{
		RunID: rec.RunID, SteerID: rec.SteerID, Seq: rec.Seq,
		Status: rec.Status, AcceptedAt: rec.AcceptedAt,
		IncorporatedAt: rec.IncorporatedAt, Stage: rec.Stage,
	}
	if rec.ReasonCode != "" {
		rc := contracts.SteerReasonCode(rec.ReasonCode)
		out.ReasonCode = &rc
	}
	return out
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	inputID := chi.URLParam(r, "inputID")
	clientID, err := s.authenticateRunBearer(r.Context(), runID, bearerToken(r))
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, runID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeAnswerRequest(body)
	if verr != nil {
		writeError(w, runID, errOutcome(verr))
		return
	}

	var answerAccess []byte
	if req.AccessRefresh != nil {
		frozenJSON, err := s.runs.LoadFrozenConfig(r.Context(), runID)
		if err != nil {
			writeError(w, runID, errOutcome(err))
			return
		}
		var frozen contracts.CreateRunRequest
		if len(frozenJSON) > 0 {
			if err := json.Unmarshal(frozenJSON, &frozen); err != nil {
				writeError(w, runID, httpErr(http.StatusUnprocessableEntity, contracts.ErrInvalidAccessRefresh))
				return
			}
		}
		if verr := contracts.ValidateAccessRefresh(&frozen, req.AccessRefresh); verr != nil {
			writeError(w, runID, errOutcome(verr))
			return
		}
		answerAccess = mustMarshal(req.AccessRefresh)
	}

	kind, opts := "", []string(nil)
	if meta, merr := s.runs.InputMeta(r.Context(), runID, inputID); merr == nil {
		kind, opts = meta.Kind, meta.Options
	}
	if !validateAnswerForKind(req.Answer, kind, opts) {
		writeError(w, runID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}

	answerBytes, _ := json.Marshal(req.Answer)
	ar, err := s.runs.AnswerInput(r.Context(), runID, inputID, answerBytes, answerAccess, clientID)
	if err != nil {
		writeError(w, runID, errOutcome(err))
		return
	}
	status := http.StatusAccepted
	if ar.Idempotent {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"applied": ar.Applied, "idempotent": ar.Idempotent, "stage": ar.NewStage})
}

func validateAnswerForKind(answer any, kind string, opts []string) bool {
	switch kind {
	case contracts.InputKindQuestion:
		v, ok := answer.(string)
		return ok && v != ""
	case contracts.InputKindChoice:
		v, ok := answer.(string)
		if !ok || v == "" {
			return false
		}

		for _, o := range opts {
			if o == v {
				return true
			}
		}
		return false
	case contracts.InputKindApproval:
		_, ok := answer.(bool)
		return ok
	case "":

		return true
	default:
		return answer != nil
	}
}
