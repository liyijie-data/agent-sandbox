package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/modelgateway"
	"agent-platform/internal/security"
)

const ModelGatewayPath = "/internal/model/v1"

const modelMaxBody = 2 << 20

type ModelGateway struct {
	UpstreamBase   string
	UpstreamKey    string
	FrozenModel    string
	MaxRequests    int64
	MaxConcurrency int
	Charger        modelgateway.RequestCharger
	Permit         modelgateway.ConcurrencyPermit
	Client         *http.Client
	log            *slog.Logger
}

func (s *Server) handleModelChatCompletions(w http.ResponseWriter, r *http.Request) {
	gw := s.gateway
	if gw == nil {
		writeError(w, "", httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, ModelGatewayPath)
	if r.Method != http.MethodPost || path != modelgateway.ChatCompletionsPath {
		writeError(w, "", httpErr(http.StatusNotFound, notFoundCode))
		return
	}
	s.log.Info("model gateway request", "method", r.Method, "path", r.URL.Path)
	body, err := io.ReadAll(io.LimitReader(r.Body, modelMaxBody+1))
	if err != nil {
		s.log.Warn("model gateway: read body failed", "err", err)
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}
	if len(body) > modelMaxBody {
		writeError(w, "", httpErr(http.StatusRequestEntityTooLarge, contracts.ErrPayloadTooLarge))
		return
	}
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Model == "" {
		s.log.Warn("model gateway: bad model body", "model", m.Model, "bodylen", len(body))
		writeError(w, "", httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}

	tok := bearerToken(r)
	if tok == "" {
		s.log.Warn("model gateway: missing token")
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	claims, err := parseStageClaims(tok)
	if err != nil {
		s.log.Warn("model gateway: unparseable token", "err", err)
		writeError(w, "", httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	if err := s.signer.Verify(tok, claims.RunID, claims.Stage, claims.Fence, security.PurposeModel, s.clock.Now()); err != nil {
		s.log.Warn("model gateway: token verify failed", "run", claims.RunID, "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	status, err := s.runs.StatusOf(r.Context(), claims.RunID)
	if err != nil {
		s.log.Warn("model gateway: status lookup failed", "run", claims.RunID, "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	if status != contracts.RunStatusQueued && status != contracts.RunStatusRunning {
		s.log.Warn("model gateway: run not executing", "run", claims.RunID, "status", status)
		writeError(w, claims.RunID, httpErr(http.StatusConflict, contracts.ErrRuntimeStateConflict))
		return
	}

	frozenModel := gw.FrozenModel
	upstreamRaw := gw.UpstreamBase
	upstreamKey := gw.UpstreamKey
	upstreamProvider := ""
	if frozenModel == "" {
		frozen, err := s.runs.FrozenModel(r.Context(), claims.RunID)
		if err != nil {
			s.log.Warn("model gateway: frozen model lookup failed", "run", claims.RunID, "err", err)
			writeError(w, claims.RunID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
			return
		}
		if !frozen.Access.ExpiresAt.After(s.clock.Now()) {
			s.log.Warn("model gateway: model access expired", "run", claims.RunID)
			writeError(w, claims.RunID, httpErr(http.StatusUnauthorized, "unauthorized"))
			return
		}
		frozenModel = frozen.Name
		upstreamRaw = frozen.BaseURL
		upstreamKey = frozen.Access.APIKey
		upstreamProvider = frozen.Provider
	}
	if err := modelgateway.ValidateChatCompletions(r.Method, path, m.Model, frozenModel); err != nil {
		s.log.Warn("model gateway: surface/model mismatch", "run", claims.RunID,
			"model", m.Model, "frozen", frozenModel, "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}

	upstream, err := modelgateway.NormalizeUpstream(upstreamRaw, true)
	if err != nil {
		s.log.Warn("model gateway: upstream target rejected", "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusForbidden, contracts.ErrUpstreamTargetRejected))
		return
	}
	if !allowedModelUpstream(gw.UpstreamBase, upstream) {
		s.log.Warn("model gateway: upstream target rejected", "run", claims.RunID)
		writeError(w, claims.RunID, httpErr(http.StatusForbidden, contracts.ErrUpstreamTargetRejected))
		return
	}

	if err := modelgateway.CheckHeaders(r.Header); err != nil {
		s.log.Warn("model gateway: dangerous headers", "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusBadRequest, contracts.ErrInvalidRequest))
		return
	}

	count, ok, err := gw.Charger.ChargeModelRequest(r.Context(), claims.RunID, gw.MaxRequests)
	if err != nil {
		s.log.Warn("model gateway: budget charge failed", "run", claims.RunID, "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}
	if !ok {

		s.log.Warn("model gateway: run missing at budget charge", "run", claims.RunID)
		writeError(w, claims.RunID, httpErr(http.StatusUnauthorized, "unauthorized"))
		return
	}
	if count > gw.MaxRequests {

		s.log.Warn("model gateway: request budget exceeded", "run", claims.RunID, "count", count)
		writeError(w, claims.RunID, httpErr(http.StatusTooManyRequests, contracts.ErrResourceLimitExceeded))
		return
	}
	permit, ok, err := gw.Permit.Acquire(r.Context(), claims.RunID, gw.MaxConcurrency)
	if err != nil {
		s.log.Warn("model gateway: quota storage failed", "err", err)
		writeError(w, claims.RunID, httpErr(http.StatusServiceUnavailable, "storage_unavailable"))
		return
	}
	if !ok {
		s.log.Warn("model gateway: concurrency exceeded", "run", claims.RunID)
		writeError(w, claims.RunID, httpErr(http.StatusTooManyRequests, contracts.ErrResourceLimitExceeded))
		return
	}
	defer gw.Permit.Release(context.Background(), claims.RunID, permit)

	client := gw.Client
	if client == nil {
		client = defaultGatewayClient()
	}

	const upstreamMaxRetries = 3
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream+modelgateway.ChatCompletionsPath, bytes.NewReader(body))
		if err != nil {
			writeError(w, claims.RunID, httpErr(http.StatusBadGateway, upstreamErrorCode))
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if ac := r.Header.Get("Accept"); ac != "" {
			req.Header.Set("Accept", ac)
		}
		req.Header.Set("User-Agent", "agent-platform/1")
		if upstreamKey != "" {
			req.Header.Set("Authorization", "Bearer "+upstreamKey)
		}
		if upstreamProvider == "opencode-go" {
			req.Header.Set("x-opencode-session", claims.RunID)
		}
		resp, err = client.Do(req)
		if err == nil {
			break
		}

		var opErr *net.OpError
		retryable := errors.As(err, &opErr) && attempt < upstreamMaxRetries && r.Context().Err() == nil

		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		if retryable {
			s.log.Warn("model gateway upstream transport failed; retrying", "run", claims.RunID,
				"stage", claims.Stage, "attempt", attempt+1, "err", msg)
			select {
			case <-r.Context().Done():
				retryable = false
			case <-time.After(200 * time.Millisecond):
			}
		} else {
			s.log.Warn("model gateway upstream request failed", "run", claims.RunID,
				"stage", claims.Stage, "err", msg)
		}
		if !retryable {
			writeError(w, claims.RunID, httpErr(http.StatusBadGateway, upstreamErrorCode))
			return
		}
	}
	defer resp.Body.Close()

	s.log.Info("model gateway proxied", "run", claims.RunID, "stage", claims.Stage,
		"upstream_status", resp.StatusCode, "content_length", resp.ContentLength)

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	_, _ = io.Copy(flushWriter{w: w, f: fl}, resp.Body)
}

func allowedModelUpstream(allowed, normalized string) bool {
	found := false
	matched := false
	for _, raw := range strings.Split(allowed, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		candidate, err := modelgateway.NormalizeUpstream(raw, true)
		if err != nil {
			return false
		}
		found = true
		if candidate == normalized {
			matched = true
		}
	}
	return found && matched
}

func NewModelGateway(upstreamBase, upstreamKey, frozenModel string, maxRequests int64, maxConcurrency int, charger modelgateway.RequestCharger, permit modelgateway.ConcurrencyPermit, log *slog.Logger) *ModelGateway {
	return &ModelGateway{
		UpstreamBase:   upstreamBase,
		UpstreamKey:    upstreamKey,
		FrozenModel:    frozenModel,
		MaxRequests:    maxRequests,
		MaxConcurrency: maxConcurrency,
		Charger:        charger,
		Permit:         permit,
		Client:         defaultGatewayClient(),
		log:            log,
	}
}

func defaultGatewayClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			if !(modelgateway.RedirectPolicy{Disallow: true}).FollowAllowed() {
				return errors.New("modelgateway: redirects are not followed")
			}
			return nil
		},
	}
}

func parseStageClaims(token string) (*security.Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("controlplane: malformed stage token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("controlplane: bad token payload: %w", err)
	}
	var c security.Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("controlplane: unreadable token claims: %w", err)
	}
	return &c, nil
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
