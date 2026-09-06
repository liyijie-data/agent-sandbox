package controlplane

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/console"
	"agent-platform/internal/contracts"
)

func (s *Server) routeAdminAPI(r chi.Router) {

	r.Post("/clients", s.handleCreateClient)
	r.Get("/clients", s.handleListClients)
	r.Get("/clients/{clientID}", s.handleGetClient)
	r.Get("/clients/{clientID}/images", s.handleListClientImages)
	r.Get("/clients/{clientID}/runs", s.handleListClientRuns)
	r.Get("/platform/network", s.handleGetPlatformNetwork)
	r.Get("/platform/warm-pool", s.handleGetPlatformWarmPool)
	r.Post("/clients/{clientID}/images", s.handleRegisterImage)
	r.Get("/registrations/{registrationID}", s.handleGetRegistration)
	r.Post("/clients/{clientID}/images/{imageID}/enable", s.handleEnableImage)
	r.Post("/clients/{clientID}/images/{imageID}/disable", s.handleDisableImage)
	r.Post("/clients/{clientID}/images/{imageID}/revoke", s.handleRevokeImage)

	r.Put("/clients/{clientID}/quota", s.handlePutClientQuota)
	r.Put("/clients/{clientID}/images/{imageID}/warm-pool", s.handlePutImageWarmPool)
	r.Put("/platform/warm-pool", s.handlePutPlatformWarmPool)
}

type consoleLoginBody struct {
	Token string `json:"token"`
}

func (s *Server) ConsoleRouter() *chi.Mux {
	if s.consoleSessions == nil {
		s.consoleSessions = console.NewStore()
	}
	r := chi.NewRouter()
	r.Handle("/console", http.StripPrefix("/console", console.Static()))
	r.Handle("/console/*", http.StripPrefix("/console", console.Static()))
	r.Post("/console/api/login", s.handleConsoleLogin)
	r.Post("/console/api/logout", s.handleConsoleLogout)
	r.Get("/console/api/me", s.handleConsoleMe)
	r.Route("/console/api/v1", func(r chi.Router) {
		r.Use(s.requireConsoleSession)
		s.routeAdminAPI(r)
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, "", httpErr(http.StatusNotFound, notFoundCode))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, "", httpErr(http.StatusMethodNotAllowed, "method_not_allowed"))
	})
	return r
}

func (s *Server) handleConsoleLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, adminBodyLimit))
	if err != nil {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	var req consoleLoginBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Token == "" {
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.adminToken)) != 1 {
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	session := s.consoleSessions.Create()
	http.SetCookie(w, &http.Cookie{
		Name:     console.SessionCookieName,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(console.SessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) handleConsoleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(console.SessionCookieName); err == nil {
		s.consoleSessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     console.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleConsoleMe(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(console.SessionCookieName)
	if err == nil && s.consoleSessions.Validate(c.Value) {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
		return
	}
	writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
}

func (s *Server) requireConsoleSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(console.SessionCookieName)
		if err != nil || !s.consoleSessions.Validate(c.Value) {
			writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
