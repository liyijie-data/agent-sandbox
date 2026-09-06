package controlplane

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/contracts"
	"agent-platform/internal/service/networks"
)

const networkConfigBodyLimit = 1 << 20

func (s *Server) requireNetworks(w http.ResponseWriter) bool {
	if s.networks == nil {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return false
	}
	return true
}

func (s *Server) authenticateConfigBearer(r *http.Request) (string, bool) {
	cid, err := s.runs.AuthenticateClient(r.Context(), bearerToken(r))
	if err != nil {
		return "", false
	}
	return cid, true
}

func (s *Server) handleGetClientNetworkConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetworks(w) {
		return
	}
	clientID, ok := s.authenticateConfigBearer(r)
	if !ok {
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	view, err := s.networks.GetClientConfig(r.Context(), clientID)
	if err != nil {
		writeError(w, clientID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, networkConfigView(view))
}

func (s *Server) handlePutClientNetworkConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetworks(w) {
		return
	}
	clientID, ok := s.authenticateConfigBearer(r)
	if !ok {
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, networkConfigBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeNetworkConfigRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	res, err := s.networks.PutClientConfig(r.Context(), clientID, *req)
	if err != nil {
		writeError(w, req.ReqID, errOutcome(err))
		return
	}
	status := http.StatusCreated
	if res.Idempotent {
		status = http.StatusOK
	}
	writeJSON(w, status, networkPutResponse("client", res))
}

func (s *Server) handleGetImageNetworkConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetworks(w) {
		return
	}
	clientID, ok := s.authenticateConfigBearer(r)
	if !ok {
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	imageID := chi.URLParam(r, "imageID")
	view, err := s.networks.GetImageConfig(r.Context(), clientID, imageID)
	if err != nil {
		writeError(w, imageID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, networkConfigView(view))
}

func (s *Server) handlePutImageNetworkConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetworks(w) {
		return
	}
	clientID, ok := s.authenticateConfigBearer(r)
	if !ok {
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	imageID := chi.URLParam(r, "imageID")
	body, err := io.ReadAll(io.LimitReader(r.Body, networkConfigBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeNetworkConfigRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	res, err := s.networks.PutImageConfig(r.Context(), clientID, imageID, *req)
	if err != nil {
		writeError(w, req.ReqID, errOutcome(err))
		return
	}
	status := http.StatusCreated
	if res.Idempotent {
		status = http.StatusOK
	}
	writeJSON(w, status, networkPutResponse("image", res))
}

func networkConfigView(v *networks.ConfigView) *contracts.NetworkConfigResponse {
	return &contracts.NetworkConfigResponse{
		Scope:         v.Scope,
		Source:        v.Source,
		RevisionID:    v.RevisionID,
		Revision:      v.Revision,
		RolloutStatus: v.RolloutStatus,
		HostAliases:   v.Spec.HostAliases,
		NetworkPolicy: v.Spec.NetworkPolicy,
	}
}

func networkPutResponse(scope string, res *networks.PutResult) *contracts.NetworkConfigResponse {
	return &contracts.NetworkConfigResponse{
		Scope:         scope,
		Source:        scope,
		RevisionID:    res.RevisionID,
		Revision:      res.Revision,
		RolloutStatus: res.RolloutStatus,
	}
}
