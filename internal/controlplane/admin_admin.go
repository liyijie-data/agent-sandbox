package controlplane

import (
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/contracts"
)

func (s *Server) requireRuns(w http.ResponseWriter) bool {
	if s.runs == nil {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return false
	}
	return true
}

func adminPagination(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit, offset = 100, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
			return 0, 0, false
		}
		if n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func (s *Server) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	req, verr := contracts.DecodeCreateClientRequest(body)
	if verr != nil {
		writeError(w, "", errOutcome(verr))
		return
	}
	c, err := s.runs.CreateClientWithKey(r.Context(), req.Name)
	if err != nil {
		writeError(w, "", errOutcome(err))
		return
	}
	writeJSON(w, http.StatusCreated, contracts.CreateClientResponse{
		ClientID: c.ClientID, Name: c.Name, APIKey: c.APIKey, APIKeyHash: c.APIKeyHash,
	})
}

func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	limit, offset, ok := adminPagination(w, r)
	if !ok {
		return
	}
	rows, err := s.runs.ListClients(r.Context(), limit, offset)
	if err != nil {
		writeError(w, "", errOutcome(err))
		return
	}
	out := contracts.AdminClientsResponse{Clients: make([]contracts.AdminClientView, 0, len(rows))}
	for _, c := range rows {
		out.Clients = append(out.Clients, contracts.AdminClientView{
			ClientID: c.ClientID, Name: c.Name, ConcurrencyQuota: c.ConcurrencyQuota, CreatedAt: c.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	d, err := s.runs.GetClientDetail(r.Context(), clientID)
	if err != nil {
		writeError(w, clientID, errOutcome(err))
		return
	}
	out := contracts.AdminClientDetailResponse{
		ClientID: d.Client.ClientID, Name: d.Client.Name,
		ConcurrencyQuota: d.Client.ConcurrencyQuota, CreatedAt: d.Client.CreatedAt,
		APIKeys: make([]contracts.AdminAPIKeyView, 0, len(d.Keys)),
	}
	for _, k := range d.Keys {
		out.APIKeys = append(out.APIKeys, contracts.AdminAPIKeyView{
			ID: k.ID, Label: k.Label, Active: k.Active, CreatedAt: k.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListClientImages(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	rows, err := s.runs.ListClientImages(r.Context(), clientID)
	if err != nil {
		writeError(w, clientID, errOutcome(err))
		return
	}
	out := contracts.AdminClientImagesResponse{Images: make([]contracts.AdminImageRow, 0, len(rows))}
	for _, img := range rows {
		out.Images = append(out.Images, contracts.AdminImageRow{
			RegistrationID: img.RegistrationID, ImageID: img.ImageID, Digest: img.Digest, Repository: img.Repository,
			Status: img.Status, WarmPoolReplicas: img.WarmPoolReplicas,
			ValidationRef: img.ValidationRef, CreatedAt: img.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListClientRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	clientID := chi.URLParam(r, "clientID")
	limit, offset, ok := adminPagination(w, r)
	if !ok {
		return
	}
	rows, err := s.runs.ListClientRuns(r.Context(), clientID, limit, offset)
	if err != nil {
		writeError(w, clientID, errOutcome(err))
		return
	}
	out := contracts.AdminClientRunsResponse{Runs: make([]contracts.AdminRunRow, 0, len(rows))}
	for _, r := range rows {
		out.Runs = append(out.Runs, contracts.AdminRunRow{
			RunID: r.RunID, ReqID: r.ReqID, Status: r.Status,
			CurrentStage: r.CurrentStage, CreatedAt: r.CreatedAt, TerminalAt: r.TerminalAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPlatformNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	st, err := s.runs.PlatformNetworkStatus(r.Context())
	if err != nil {
		writeError(w, "", errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, contracts.AdminPlatformNetworkResponse{
		ActiveRevision: st.ActiveRevision, ActiveRevisionID: st.ActiveRevisionID,
		DesiredRevisionID: st.DesiredRevisionID, RolloutStatus: st.RolloutStatus,
		ErrorSummary: st.ErrorSummary, UpdatedAt: st.UpdatedAt,
	})
}

func (s *Server) handleGetPlatformWarmPool(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	st, err := s.runs.PlatformWarmPoolStatus(r.Context())
	if err != nil {
		writeError(w, "", errOutcome(err))
		return
	}
	writeJSON(w, http.StatusOK, contracts.AdminPlatformWarmPoolResponse{
		Budget: st.Budget, MaxPerImage: st.MaxPerImage, DefaultPool: st.DefaultPool, Configured: st.Configured,
	})
}
