package contracts

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

type RegisterImageRequest struct {
	ImageID    string    `json:"image_id"`
	Repository string    `json:"repository"`
	Manifest   *Manifest `json:"manifest,omitempty"`
}

func DecodeRegisterImageRequest(data []byte) (*RegisterImageRequest, *validationError) {
	var req RegisterImageRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "register image decode failed: %v", err)
	}
	if req.ImageID == "" {
		return nil, validationErrorf(ErrInvalidRequest, "image_id is required")
	}
	req.Repository = strings.TrimSpace(req.Repository)
	if req.Repository == "" || strings.ContainsAny(req.Repository, "@ \t\r\n") {
		return nil, validationErrorf(ErrInvalidRequest, "repository is required and must contain a tag")
	}
	if req.Manifest != nil {
		manifestJSON, err := json.Marshal(req.Manifest)
		if err != nil {
			return nil, validationErrorf(ErrInvalidRequest, "manifest decode failed")
		}
		manifest, verr := DecodeImageManifest(manifestJSON)
		if verr != nil {
			return nil, verr
		}
		if verr := ValidateCompleteManifestCapabilities(manifest.Capabilities); verr != nil {
			return nil, validationErrorf(ErrInvalidRequest, "%s", verr.Reason)
		}
		req.Manifest = manifest
	}
	return &req, nil
}

type RegisterImageResponse struct {
	RegistrationID string `json:"registration_id"`
	ImageID        string `json:"image_id"`
	Status         string `json:"status"`
}

type EnableImageRequest struct {
	Digest        string `json:"digest"`
	ValidationRef string `json:"validation_ref"`
}

func DecodeEnableImageRequest(data []byte) (*EnableImageRequest, *validationError) {
	var req EnableImageRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "enable image decode failed: %v", err)
	}
	return &req, nil
}

type ImageRegistrationView struct {
	RegistrationID  string   `json:"registration_id"`
	ClientID        string   `json:"client_id"`
	ImageID         string   `json:"image_id"`
	Digest          string   `json:"digest"`
	Repository      string   `json:"repository"`
	ContractVersion string   `json:"contract_version"`
	Capabilities    []string `json:"capabilities"`
	Entrypoint      []string `json:"entrypoint"`
	StateFormat     string   `json:"state_format"`
	Status          string   `json:"status"`
	Phase           string   `json:"phase"`
	PassedItems     []string `json:"passed_items"`
	FailureCodes    []string `json:"failure_codes,omitempty"`
	ValidationRef   string   `json:"validation_ref,omitempty"`

	WarmPoolReplicas int `json:"warm_pool_replicas,omitempty"`
}

type QuotaRequest struct {
	ConcurrencyQuota int `json:"concurrency_quota"`
}

func DecodeQuotaRequest(data []byte) (*QuotaRequest, *validationError) {
	var req QuotaRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "quota decode failed: %v", err)
	}
	if req.ConcurrencyQuota < 0 {
		return nil, validationErrorf(ErrInvalidRequest, "concurrency_quota must be >= 0")
	}
	return &req, nil
}

type ImageWarmPoolRequest struct {
	WarmPoolReplicas int `json:"warm_pool_replicas"`
}

func DecodeImageWarmPoolRequest(data []byte) (*ImageWarmPoolRequest, *validationError) {
	var req ImageWarmPoolRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "image warm-pool decode failed: %v", err)
	}
	if req.WarmPoolReplicas < 0 || req.WarmPoolReplicas > 512 {
		return nil, validationErrorf(ErrInvalidRequest, "warm_pool_replicas must be 0..512")
	}
	return &req, nil
}

type PlatformWarmPoolRequest struct {
	Budget      *int `json:"budget,omitempty"`
	MaxPerImage *int `json:"max_per_image,omitempty"`
	DefaultPool *int `json:"default_pool,omitempty"`
}

func DecodePlatformWarmPoolRequest(data []byte) (*PlatformWarmPoolRequest, *validationError) {
	var req PlatformWarmPoolRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "platform warm-pool decode failed: %v", err)
	}
	for name, v := range map[string]*int{"budget": req.Budget, "max_per_image": req.MaxPerImage} {
		if v != nil && (*v <= 0 || *v > 512) {
			return nil, validationErrorf(ErrInvalidRequest, "%s must be 1..512 (omitting it restores the default)", name)
		}
	}
	if req.DefaultPool != nil && (*req.DefaultPool < 0 || *req.DefaultPool > 512) {
		return nil, validationErrorf(ErrInvalidRequest, "default_pool must be 0..512")
	}
	return &req, nil
}

type CreateClientRequest struct {
	Name string `json:"name"`
}

func DecodeCreateClientRequest(data []byte) (*CreateClientRequest, *validationError) {
	var req CreateClientRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "create client decode failed: %v", err)
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, validationErrorf(ErrInvalidRequest, "name is required")
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > 128 {
		return nil, validationErrorf(ErrInvalidRequest, "name must be 1..128 characters")
	}
	return &req, nil
}

type CreateClientResponse struct {
	ClientID   string `json:"client_id"`
	Name       string `json:"name"`
	APIKey     string `json:"api_key"`
	APIKeyHash string `json:"api_key_hash"`
}

type AdminClientView struct {
	ClientID         string    `json:"client_id"`
	Name             string    `json:"name"`
	ConcurrencyQuota int       `json:"concurrency_quota"`
	CreatedAt        time.Time `json:"created_at"`
}

type AdminClientsResponse struct {
	Clients []AdminClientView `json:"clients"`
}

type AdminAPIKeyView struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type AdminClientDetailResponse struct {
	ClientID         string            `json:"client_id"`
	Name             string            `json:"name"`
	ConcurrencyQuota int               `json:"concurrency_quota"`
	CreatedAt        time.Time         `json:"created_at"`
	APIKeys          []AdminAPIKeyView `json:"api_keys"`
}

type AdminImageRow struct {
	RegistrationID   string    `json:"registration_id"`
	ImageID          string    `json:"image_id"`
	Digest           string    `json:"digest"`
	Repository       string    `json:"repository"`
	Status           string    `json:"status"`
	WarmPoolReplicas int       `json:"warm_pool_replicas"`
	ValidationRef    string    `json:"validation_ref"`
	CreatedAt        time.Time `json:"created_at"`
}

type AdminClientImagesResponse struct {
	Images []AdminImageRow `json:"images"`
}

type AdminRunRow struct {
	RunID        string     `json:"run_id"`
	ReqID        string     `json:"req_id"`
	Status       string     `json:"status"`
	CurrentStage int        `json:"current_stage"`
	CreatedAt    time.Time  `json:"created_at"`
	TerminalAt   *time.Time `json:"terminal_at"`
}

type AdminClientRunsResponse struct {
	Runs []AdminRunRow `json:"runs"`
}

type AdminPlatformNetworkResponse struct {
	ActiveRevision    *int64     `json:"active_revision"`
	ActiveRevisionID  *string    `json:"active_revision_id"`
	DesiredRevisionID *string    `json:"desired_revision_id"`
	RolloutStatus     string     `json:"rollout_status"`
	ErrorSummary      string     `json:"error_summary"`
	UpdatedAt         *time.Time `json:"updated_at"`
}

type AdminPlatformWarmPoolResponse struct {
	Budget      int  `json:"budget"`
	MaxPerImage int  `json:"max_per_image"`
	DefaultPool int  `json:"default_pool"`
	Configured  bool `json:"configured"`
}
