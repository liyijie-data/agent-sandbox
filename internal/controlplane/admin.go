package controlplane

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/contracts"
	"agent-platform/internal/images"
)

const adminBodyLimit = 1 << 20

func (s *Server) requireImages(w http.ResponseWriter) bool {
	if s.imagesSvc == nil || s.imageResolver == nil {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return false
	}
	return true
}

func (s *Server) handleRegisterImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireImages(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeRegisterImageRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	digest, err := s.imageResolver.Resolve(r.Context(), req.Repository)
	if err != nil {
		writeError(w, req.Repository, httpErr(http.StatusUnprocessableEntity, contracts.ErrRegistryNotAllowed))
		return
	}
	manifest := req.Manifest
	if manifest == nil {
		manifest = contracts.StandardRuntimeManifest()
	}
	reg, created, err := s.imagesSvc.Register(r.Context(), images.RegisterInput{
		ClientID: clientID, ImageID: req.ImageID, Digest: digest, Repository: req.Repository, Manifest: manifest,
	})
	if err != nil {
		writeError(w, clientID, errOutcome(err))
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, contracts.RegisterImageResponse{
		RegistrationID: reg.ID, ImageID: reg.ImageID, Status: reg.Status,
	})
}

func (s *Server) handleGetRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.requireImages(w) {
		return
	}
	registrationID := chi.URLParam(r, "registrationID")
	reg, err := s.imagesSvc.GetRegistration(r.Context(), registrationID)
	if err != nil {
		writeError(w, registrationID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, adminRegistrationView(reg))
}

func adminRegistrationView(reg *images.Registration) contracts.ImageRegistrationView {
	v := contracts.ImageRegistrationView{
		RegistrationID: reg.ID, ClientID: reg.ClientID, ImageID: reg.ImageID,
		Digest: reg.Digest, Repository: reg.Repository, ContractVersion: reg.ContractVersion,
		Capabilities: reg.Capabilities, Entrypoint: reg.Entrypoint,
		StateFormat: reg.StateFormat, Status: reg.Status,
		ValidationRef: reg.ValidationRef, WarmPoolReplicas: reg.WarmPoolReplicas,
	}
	switch reg.Status {
	case images.StatusValidating:
		v.Phase = "verification_pending"
	case images.StatusEnabled:
		v.Phase = "verified"

		v.PassedItems = []string{"signature", "contract_test"}
	case images.StatusDisabled:
		v.Phase = "disabled"
	case images.StatusRevoked:
		v.Phase = "revoked"
	}
	return v
}

func (s *Server) handleEnableImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireImages(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	imageID := chi.URLParam(r, "imageID")
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeEnableImageRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	reg, err := s.imagesSvc.Enable(r.Context(), clientID, imageID, images.Evidence{
		Digest: req.Digest, ValidationRef: req.ValidationRef,
	})
	if err != nil {
		writeError(w, imageID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, contracts.RegisterImageResponse{
		RegistrationID: reg.ID, ImageID: reg.ImageID, Status: reg.Status,
	})
}

func (s *Server) handleDisableImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireImages(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	imageID := chi.URLParam(r, "imageID")
	reg, err := s.imagesSvc.Disable(r.Context(), clientID, imageID)
	if err != nil {
		writeError(w, imageID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, contracts.RegisterImageResponse{
		RegistrationID: reg.ID, ImageID: reg.ImageID, Status: reg.Status,
	})
}

func (s *Server) handleRevokeImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireImages(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	imageID := chi.URLParam(r, "imageID")
	reg, err := s.imagesSvc.Revoke(r.Context(), clientID, imageID)
	if err != nil {
		writeError(w, imageID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusAccepted, contracts.RegisterImageResponse{
		RegistrationID: reg.ID, ImageID: reg.ImageID, Status: reg.Status,
	})
}

func (s *Server) handlePutClientQuota(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "clientID")
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, clientID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeQuotaRequest(body)
	if verr != nil {
		writeError(w, clientID, errOutcome(verr))
		return
	}
	if err := s.runs.SetClientQuota(r.Context(), clientID, req.ConcurrencyQuota); err != nil {
		writeError(w, clientID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id": clientID, "concurrency_quota": req.ConcurrencyQuota,
	})
}

func (s *Server) handlePutImageWarmPool(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "clientID")
	imageID := chi.URLParam(r, "imageID")
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, imageID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeImageWarmPoolRequest(body)
	if verr != nil {
		writeError(w, imageID, errOutcome(verr))
		return
	}
	if err := s.runs.SetImageWarmPoolReplicas(r.Context(), clientID, imageID, req.WarmPoolReplicas); err != nil {
		writeError(w, imageID, errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id": clientID, "image_id": imageID, "warm_pool_replicas": req.WarmPoolReplicas,
	})
}

func (s *Server) handlePutPlatformWarmPool(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodePlatformWarmPoolRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	if err := s.runs.SetPlatformWarmPool(r.Context(), req.Budget, req.MaxPerImage, req.DefaultPool); err != nil {
		writeError(w, "", errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"budget": req.Budget, "max_per_image": req.MaxPerImage, "default_pool": 0,
	})
}
