package contracts

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	MaxResourcesPerType = 32
	MaxAnswerChoices    = 32
	MaxReqIDLen         = 128
	MaxNetworkRules     = 32
	hashLen             = 64
	maxPort             = 65535

	MaxModelTokens          = 1 << 21
	MaxModelParametersBytes = 64 << 10
	MaxModelParametersDepth = 32
)

var reqIDRe = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,128}$`)
var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
var queryKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

type SandboxSpec struct {
	ImageID string `json:"image_id"`
}

type ModelAccess struct {
	APIKey    string    `json:"api_key"`
	ExpiresAt time.Time `json:"expires_at"`
}
type ModelParameters map[string]json.RawMessage

type ModelSpec struct {
	Name                string          `json:"name"`
	Provider            string          `json:"provider,omitempty"`
	BaseURL             string          `json:"base_url"`
	Access              ModelAccess     `json:"access"`
	ReasoningEffort     *string         `json:"reasoning_effort,omitempty"`
	ContextWindowTokens *int            `json:"context_window_tokens,omitempty"`
	MaxOutputTokens     *int            `json:"max_output_tokens,omitempty"`
	Parameters          ModelParameters `json:"parameters,omitempty"`
}

type ResourceRef struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	SHA256             string    `json:"sha256"`
	SizeBytes          int64     `json:"size_bytes"`
	DownloadURL        string    `json:"download_url"`
	SignatureQueryKeys []string  `json:"signature_query_keys,omitempty"`
	ExpiresAt          time.Time `json:"expires_at"`
}

const (
	ToolTypeOpenAPI             = "openapi"
	ToolTypeMCP                 = "mcp"
	ToolTransportStreamableHTTP = "streamable_http"
	ToolTransportSSE            = "sse"
	ToolTransportStdio          = "stdio"
)

type ToolSpec struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Target         string   `json:"target"`
	AllowedActions []string `json:"allowed_operations,omitempty"`

	BaseURL string       `json:"base_url,omitempty"`
	Spec    *ResourceRef `json:"spec,omitempty"`

	URL       string `json:"url,omitempty"`
	Transport string `json:"transport,omitempty"`

	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type ResultBundle struct {
	DestinationID      string    `json:"destination_id"`
	UploadURL          string    `json:"upload_url"`
	SignatureQueryKeys []string  `json:"signature_query_keys,omitempty"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type PortRule struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

type EgressRule struct {
	CIDR  string     `json:"cidr"`
	Ports []PortRule `json:"ports"`
}

type NetworkPolicy struct {
	Egress []EgressRule `json:"egress,omitempty"`
}

type CreateRunRequest struct {
	ReqID        string        `json:"req_id"`
	Sandbox      SandboxSpec   `json:"sandbox"`
	Messages     []Message     `json:"messages"`
	Model        ModelSpec     `json:"model"`
	Files        []ResourceRef `json:"files,omitempty"`
	Skills       []ResourceRef `json:"skills,omitempty"`
	Tools        []ToolSpec    `json:"tools,omitempty"`
	ResultBundle *ResultBundle `json:"result_bundle,omitempty"`
}

func DecodeCreateRunRequest(data []byte) (*CreateRunRequest, *validationError) {
	var req CreateRunRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "create_run decode failed: %v", err)
	}
	if err := ValidateCreateRun(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func ValidateCreateRun(req *CreateRunRequest) *validationError {
	if !reqIDRe.MatchString(req.ReqID) {
		return validationErrorf(ErrInvalidRequest, "req_id must be 1-128 opaque chars [A-Za-z0-9._:-]")
	}
	if req.Sandbox.ImageID == "" {
		return validationErrorf(ErrInvalidRequest, "sandbox.image_id is required")
	}
	if err := ValidateMessages(req.Messages); err != nil {
		return err
	}
	if err := validateModelSpec(&req.Model); err != nil {
		return err
	}
	if err := validateResources("files", req.Files); err != nil {
		return err
	}
	if err := validateResources("skills", req.Skills); err != nil {
		return err
	}
	if err := validateTools(req.Tools); err != nil {
		return err
	}
	if req.ResultBundle != nil {
		if req.ResultBundle.DestinationID == "" || req.ResultBundle.UploadURL == "" {
			return validationErrorf(ErrInvalidRequest, "result_bundle requires destination_id and upload_url")
		}

	}
	return nil
}

func validateModelSpec(m *ModelSpec) *validationError {
	if m.Name == "" {
		return validationErrorf(ErrInvalidRequest, "model.name is required")
	}
	if m.BaseURL == "" {
		return validationErrorf(ErrInvalidRequest, "model.base_url is required")
	}
	if m.Access.APIKey == "" {
		return validationErrorf(ErrInvalidRequest, "model.access.api_key is required")
	}
	if m.Access.ExpiresAt.IsZero() {
		return validationErrorf(ErrInvalidRequest, "model.access.expires_at is required")
	}
	if m.ContextWindowTokens != nil && (*m.ContextWindowTokens <= 0 || *m.ContextWindowTokens > MaxModelTokens) {
		return validationErrorf(ErrInvalidRequest, "model.context_window_tokens must be between 1 and %d", MaxModelTokens)
	}
	if m.MaxOutputTokens != nil && (*m.MaxOutputTokens <= 0 || *m.MaxOutputTokens > MaxModelTokens) {
		return validationErrorf(ErrInvalidRequest, "model.max_output_tokens must be between 1 and %d", MaxModelTokens)
	}
	if m.ContextWindowTokens != nil && m.MaxOutputTokens != nil && *m.ContextWindowTokens <= *m.MaxOutputTokens {
		return validationErrorf(ErrInvalidRequest, "model.context_window_tokens must exceed model.max_output_tokens")
	}
	if e := ValidateModelParameters(m.Parameters, m.ReasoningEffort, m.MaxOutputTokens, m.ContextWindowTokens); e != nil {
		return e
	}
	return nil
}

func ValidateModelParameters(p ModelParameters, top *string, output, window *int) *validationError {
	if p == nil {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil || len(b) > MaxModelParametersBytes {
		return validationErrorf(ErrInvalidRequest, "model.parameters must be valid JSON object no larger than %d bytes", MaxModelParametersBytes)
	}
	protected := map[string]bool{"model": true, "messages": true, "tools": true, "stream": true, "n": true, "base_url": true, "access": true, "token": true, "api_key": true, "authorization": true, "headers": true, "context_window_tokens": true, "max_output_tokens": true}
	for k := range p {
		if protected[k] {
			return validationErrorf(ErrInvalidRequest, "model.parameters.%s is runtime-owned", k)
		}
	}
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if v, ok := p[k]; ok {
			var n int
			if json.Unmarshal(v, &n) != nil || n <= 0 || n > MaxModelTokens {
				return validationErrorf(ErrInvalidRequest, "model.parameters.%s must be a positive integer", k)
			}
			if output != nil && n != *output {
				return validationErrorf(ErrInvalidRequest, "model.parameters.%s conflicts with model.max_output_tokens", k)
			}
			if window != nil && *window <= n {
				return validationErrorf(ErrInvalidRequest, "model.context_window_tokens must exceed model.parameters.%s", k)
			}
		}
	}
	if _, a := p["max_tokens"]; a {
		if _, b := p["max_completion_tokens"]; b {
			return validationErrorf(ErrInvalidRequest, "model.parameters cannot contain both max_tokens and max_completion_tokens")
		}
	}
	if v, ok := p["reasoning_effort"]; ok {
		if string(v) == "null" {
			if top != nil {
				return validationErrorf(ErrInvalidRequest, "reasoning_effort conflicts with model.parameters.reasoning_effort")
			}
		} else {
			var s string
			if json.Unmarshal(v, &s) != nil || s == "" {
				return validationErrorf(ErrInvalidRequest, "model.parameters.reasoning_effort must be a non-empty string or null")
			}
			if top != nil && *top != s {
				return validationErrorf(ErrInvalidRequest, "reasoning_effort conflicts with model.parameters.reasoning_effort")
			}
		}
	}
	if jsonDepthRaw(p, 1) > MaxModelParametersDepth {
		return validationErrorf(ErrInvalidRequest, "model.parameters nesting exceeds %d", MaxModelParametersDepth)
	}
	return nil
}
func jsonDepthRaw(p ModelParameters, d int) int {
	max := d
	for _, raw := range p {
		var v any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if dec.Decode(&v) == nil {
			if n := jsonDepth(v, d+1); n > max {
				max = n
			}
		}
	}
	return max
}
func jsonDepth(v any, d int) int {
	max := d
	switch x := v.(type) {
	case map[string]any:
		for _, c := range x {
			if n := jsonDepth(c, d+1); n > max {
				max = n
			}
		}
	case []any:
		for _, c := range x {
			if n := jsonDepth(c, d+1); n > max {
				max = n
			}
		}
	}
	return max
}

func validateResources(kind string, res []ResourceRef) *validationError {
	if len(res) > MaxResourcesPerType {
		return validationErrorf(ErrInvalidRequest, "too many %s (max %d)", kind, MaxResourcesPerType)
	}
	ids := map[string]bool{}
	for _, r := range res {
		if r.ID == "" {
			return validationErrorf(ErrInvalidRequest, "%s entry has empty id", kind)
		}
		if ids[r.ID] {
			return validationErrorf(ErrInvalidRequest, "duplicate %s id %q", kind, r.ID)
		}
		ids[r.ID] = true
		if err := validateResourceRef(fmt.Sprintf("%s[%s]", kind, r.ID), r); err != nil {
			return err
		}
	}
	return nil
}

func validateResourceRef(label string, r ResourceRef) *validationError {
	if !sha256HexRe.MatchString(r.SHA256) {
		return validationErrorf(ErrInvalidRequest, "%s: sha256 must be 64 hex chars", label)
	}
	if r.SizeBytes < 0 {
		return validationErrorf(ErrInvalidRequest, "%s: size_bytes must be non-negative", label)
	}
	if !isHTTPURL(r.DownloadURL) {
		return validationErrorf(ErrInvalidRequest, "%s: download_url must be an absolute http(s) URL", label)
	}
	if r.ExpiresAt.IsZero() {
		return validationErrorf(ErrInvalidRequest, "%s: expires_at is required", label)
	}
	if !isSafeRelativePath(r.Name) {
		return validationErrorf(ErrInvalidRequest, "%s: name must be a safe relative path (no absolute path, no '..', no empty segment, no backslash)", label)
	}
	for _, k := range r.SignatureQueryKeys {
		if !queryKeyRe.MatchString(k) {
			return validationErrorf(ErrInvalidRequest, "%s: signature_query_keys entry %q is not a safe query key name", label, k)
		}
	}
	return nil
}

func validateTools(tools []ToolSpec) *validationError {
	if len(tools) > MaxResourcesPerType {
		return validationErrorf(ErrInvalidRequest, "too many tools (max %d)", MaxResourcesPerType)
	}
	ids := map[string]bool{}
	for _, t := range tools {
		if t.ID == "" {
			return validationErrorf(ErrInvalidRequest, "tool has empty id")
		}
		if !reqIDRe.MatchString(t.ID) {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: id must be 1-128 opaque chars [A-Za-z0-9._:-]", t.ID)
		}
		if ids[t.ID] {
			return validationErrorf(ErrInvalidRequest, "duplicate tool id %q", t.ID)
		}
		ids[t.ID] = true
		if t.Target == "" {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: target is required", t.ID)
		}
		switch t.Type {
		case ToolTypeOpenAPI:
			if err := validateOpenAPITool(t); err != nil {
				return err
			}
		case ToolTypeMCP:
			if err := validateMCPTool(t); err != nil {
				return err
			}
		default:
			return validationErrorf(ErrInvalidRequest, "tool[%s]: unknown tool type %q (supported: %s, %s)", t.ID, t.Type, ToolTypeOpenAPI, ToolTypeMCP)
		}
	}
	return nil
}

func ValidateTools(tools []ToolSpec) error {
	if e := validateTools(tools); e != nil {
		return e
	}
	return nil
}

func validateOpenAPITool(t ToolSpec) *validationError {
	if t.Command != "" || len(t.Args) != 0 || len(t.Env) != 0 || t.URL != "" || t.Transport != "" {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: openapi has invalid local/mcp fields", t.ID)
	}
	if t.BaseURL == "" {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: openapi base_url is required", t.ID)
	}
	if !isHTTPURL(t.BaseURL) {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: openapi base_url must be an absolute http(s) URL", t.ID)
	}
	if t.Spec == nil {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: openapi spec is required", t.ID)
	}
	if err := validateResourceRef(fmt.Sprintf("tool[%s].spec", t.ID), *t.Spec); err != nil {
		return err
	}
	if len(t.AllowedActions) == 0 {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: openapi allowed_operations must be non-empty", t.ID)
	}
	for _, op := range t.AllowedActions {
		if op == "" || hasWhitespace(op) {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: openapi operationId %q must be non-empty without whitespace", t.ID, op)
		}
	}
	return nil
}

func validateMCPTool(t ToolSpec) *validationError {
	if t.BaseURL != "" || t.Spec != nil {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: mcp has invalid openapi fields", t.ID)
	}
	if t.Transport == ToolTransportStdio {
		if t.Command == "" || strings.TrimSpace(t.Command) != t.Command || strings.ContainsAny(t.Command, ";&|`$\n\r\t ") {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: stdio command must be a safe non-empty executable", t.ID)
		}
		if len(t.Args) > 64 {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: too many stdio args", t.ID)
		}
		for _, a := range t.Args {
			if len(a) > 4096 || strings.IndexByte(a, 0) >= 0 {
				return validationErrorf(ErrInvalidRequest, "tool[%s]: invalid stdio arg", t.ID)
			}
		}
		if len(t.Env) > 64 {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: too many stdio environment variables", t.ID)
		}
		for k, v := range t.Env {
			if k == "" || len(k) > 256 || strings.ContainsAny(k, "=\x00\r\n") || len(v) > 4096 || strings.IndexByte(v, 0) >= 0 {
				return validationErrorf(ErrInvalidRequest, "tool[%s]: invalid stdio environment", t.ID)
			}
		}
		if t.URL != "" {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: stdio url is forbidden", t.ID)
		}
	} else {
		if t.Command != "" || len(t.Args) != 0 || len(t.Env) != 0 {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: remote mcp has local fields", t.ID)
		}
		if t.URL == "" || !isHTTPURL(t.URL) {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: mcp url must be an absolute http(s) URL", t.ID)
		}
		if t.Transport != ToolTransportStreamableHTTP && t.Transport != ToolTransportSSE {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: unsupported mcp transport", t.ID)
		}
	}
	if t.Transport == ToolTransportStdio {
	} else if t.URL == "" {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: mcp url is required", t.ID)
	}
	if t.Transport != ToolTransportStdio && !isHTTPURL(t.URL) {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: mcp url must be an absolute http(s) URL", t.ID)
	}
	if len(t.AllowedActions) == 0 {
		return validationErrorf(ErrInvalidRequest, "tool[%s]: mcp allowed_tools must be non-empty", t.ID)
	}
	for _, name := range t.AllowedActions {
		if name == "" || hasWhitespace(name) {
			return validationErrorf(ErrInvalidRequest, "tool[%s]: mcp allowed tool name %q must be non-empty without whitespace", t.ID, name)
		}
	}
	return nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func isSafeRelativePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	if len(name) >= 2 && name[1] == ':' &&
		((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == ".." {
			return false
		}
	}
	return true
}

func hasWhitespace(s string) bool {
	return strings.IndexFunc(s, unicode.IsSpace) >= 0
}

func validateNetworkPolicy(p *NetworkPolicy) *validationError {
	if p == nil {
		return nil
	}
	if len(p.Egress) > MaxNetworkRules {
		return validationErrorf(ErrInvalidRequest, "too many egress rules (max %d)", MaxNetworkRules)
	}
	for i, r := range p.Egress {
		if _, _, err := net.ParseCIDR(r.CIDR); err != nil {
			return validationErrorf(ErrNetworkPolicyRejected, "egress[%d]: invalid CIDR %q", i, r.CIDR)
		}
		if len(r.Ports) == 0 {
			return validationErrorf(ErrNetworkPolicyRejected, "egress[%d]: ports must be non-empty (no implicit full egress)", i)
		}
		for _, pt := range r.Ports {
			switch pt.Protocol {
			case "TCP", "UDP":
			default:
				return validationErrorf(ErrNetworkPolicyRejected, "egress[%d]: supported protocols are TCP/UDP", i)
			}
			if pt.Port < 1 || pt.Port > maxPort {
				return validationErrorf(ErrNetworkPolicyRejected, "egress[%d]: port out of range 1-65535", i)
			}
		}
	}
	return nil
}
