package controlplane

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"agent-platform/internal/console"
	"agent-platform/internal/events"
	"agent-platform/internal/images"
	"agent-platform/internal/lifecycle"
	"agent-platform/internal/modelgateway"
	"agent-platform/internal/registry"
	"agent-platform/internal/security"
	"agent-platform/internal/service/networks"
	"agent-platform/internal/service/runs"
	"agent-platform/internal/service/steers"
)

type Server struct {
	runs   *runs.Service
	steers *steers.Service
	signer *security.Signer
	clock  lifecycle.Clock
	log    *slog.Logger

	events *events.Service

	gateway *ModelGateway

	imagesSvc     *images.Service
	imageResolver *registry.Resolver

	networks *networks.Service

	adminToken string

	consoleSessions *console.Store

	modelName string

	retention time.Duration

	ssePoll      time.Duration
	sseKeepalive time.Duration
}

func New(runsSvc *runs.Service, steersSvc *steers.Service, signer *security.Signer, clock lifecycle.Clock, log *slog.Logger) *Server {
	if clock == nil {
		clock = lifecycle.SystemClock
	}
	if log == nil {
		log = slog.Default()
	}
	pol := lifecycle.DefaultPolicy()
	return &Server{
		runs: runsSvc, steers: steersSvc, signer: signer, clock: clock, log: log,
		modelName: "platform-gateway", retention: pol.ContentRetention,
		ssePoll: 250 * time.Millisecond, sseKeepalive: 15 * time.Second,
	}
}

func (s *Server) WithEvents(es *events.Service) *Server {
	s.events = es
	return s
}

func (s *Server) WithModelGateway(gw *ModelGateway) *Server {
	s.gateway = gw
	if gw != nil && gw.FrozenModel != "" {
		s.modelName = gw.FrozenModel
	}
	return s
}

func (s *Server) WithImages(svc *images.Service) *Server {
	s.imagesSvc = svc
	return s
}

func (s *Server) WithImageResolver(resolver *registry.Resolver) *Server {
	s.imageResolver = resolver
	return s
}

func (s *Server) WithNetworks(svc *networks.Service) *Server {
	s.networks = svc
	return s
}

func (s *Server) WithAdminToken(token string) *Server {
	s.adminToken = token
	return s
}

func (s *Server) requireAdminToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
			return
		}
		tok := bearerToken(r)
		if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.adminToken)) != 1 {
			writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

const InternalRuntimePath = "/internal/v1/runtime"

func (s *Server) Router() *chi.Mux {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, "", httpErr(http.StatusNotFound, notFoundCode))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
		path := req.URL.Path
		mountPath := strings.TrimSuffix(path, "/")
		if strings.HasSuffix(mountPath, "/api/v1/runs") || strings.HasSuffix(mountPath, "/steers") {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, "", httpErr(http.StatusMethodNotAllowed, methodNotAllowedCode))
			return
		}

		if !strings.HasSuffix(path, "/") {
			for _, method := range methods {
				if r.Match(chi.NewRouteContext(), method, path+"/") {
					path += "/"
					break
				}
			}
		}
		for _, method := range methods {
			if r.Match(chi.NewRouteContext(), method, path) {
				w.Header().Add("Allow", method)
			}
		}
		writeError(w, "", httpErr(http.StatusMethodNotAllowed, methodNotAllowedCode))
	})

	r.Route("/api/v1/runs", func(r chi.Router) {
		r.Post("/", s.handleCreateRun)
		r.Get("/{runID}", s.handleGetRun)
		r.Get("/{runID}/events", s.handleGetRunEvents)
		r.Post("/{runID}/cancel", s.handleCancelRun)
		r.Route("/{runID}/steers", func(r chi.Router) {
			r.Post("/", s.handleCreateSteer)
			r.Get("/{steerID}", s.handleGetSteer)
		})
		r.Route("/{runID}/inputs/{inputID}", func(r chi.Router) {
			r.Post("/answer", s.handleAnswer)
		})
	})

	r.Route(InternalRuntimePath, func(r chi.Router) {
		r.Post("/steers/pull", s.handleSteerPull)
		r.Post("/steers/ack", s.handleSteerAck)
		r.Post("/events", s.handleRuntimeEvent)
	})

	r.Route(ModelGatewayPath, func(r chi.Router) {
		r.Handle(modelgateway.ChatCompletionsPath, http.HandlerFunc(s.handleModelChatCompletions))
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, "", httpErr(http.StatusNotFound, notFoundCode))
		})
	})

	r.Route("/admin/v1", func(r chi.Router) {
		r.Use(s.requireAdminToken)
		s.routeAdminAPI(r)
	})

	r.Route("/api/v1/config", func(r chi.Router) {
		r.Get("/network", s.handleGetClientNetworkConfig)
		r.Put("/network", s.handlePutClientNetworkConfig)
	})
	r.Route("/api/v1/images/{imageID}/config", func(r chi.Router) {
		r.Get("/network", s.handleGetImageNetworkConfig)
		r.Put("/network", s.handlePutImageNetworkConfig)
	})
	return r
}
