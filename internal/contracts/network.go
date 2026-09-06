package contracts

import (
	"encoding/json"
	"net"
	"strings"
)

const MaxHostAliases = 32

type HostAlias struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
}

type NetworkConfigSpec struct {
	HostAliases   []HostAlias   `json:"host_aliases"`
	NetworkPolicy NetworkPolicy `json:"network_policy"`
}

type NetworkConfigRequest struct {
	ReqID                  string        `json:"req_id"`
	ExpectedActiveRevision *string       `json:"expected_active_revision,omitempty"`
	HostAliases            []HostAlias   `json:"host_aliases,omitempty"`
	NetworkPolicy          NetworkPolicy `json:"network_policy"`
}

func DecodeNetworkConfigRequest(data []byte) (*NetworkConfigRequest, *validationError) {
	var req NetworkConfigRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "network config decode failed: %v", err)
	}
	if err := ValidateNetworkConfig(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func ValidateNetworkConfig(req *NetworkConfigRequest) *validationError {
	if !reqIDRe.MatchString(req.ReqID) {
		return validationErrorf(ErrInvalidRequest, "req_id must be 1-128 opaque chars [A-Za-z0-9._:-]")
	}
	if len(req.HostAliases) > MaxHostAliases {
		return validationErrorf(ErrInvalidRequest, "too many host_aliases (max %d)", MaxHostAliases)
	}
	seen := map[string]string{}
	for i, a := range req.HostAliases {
		if a.Hostname == "" || a.IP == "" {
			return validationErrorf(ErrInvalidRequest, "host_aliases[%d]: hostname and ip are required", i)
		}
		if net.ParseIP(strings.TrimSpace(a.IP)) == nil {
			return validationErrorf(ErrNetworkPolicyRejected, "host_aliases[%d]: invalid ip %q", i, a.IP)
		}
		if prev, dup := seen[a.Hostname]; dup && prev != a.IP {
			return validationErrorf(ErrNetworkPolicyRejected, "host_aliases[%d]: conflicting mapping for hostname %q", i, a.Hostname)
		}
		seen[a.Hostname] = a.IP
	}
	if err := validateNetworkPolicy(&req.NetworkPolicy); err != nil {
		return err
	}
	return nil
}

type NetworkConfigResponse struct {
	Scope         string        `json:"scope"`
	Source        string        `json:"source"`
	RevisionID    string        `json:"revision_id,omitempty"`
	Revision      int64         `json:"revision"`
	RolloutStatus string        `json:"rollout_status"`
	HostAliases   []HostAlias   `json:"host_aliases"`
	NetworkPolicy NetworkPolicy `json:"network_policy"`
}
