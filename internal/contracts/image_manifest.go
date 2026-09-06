package contracts

import (
	"encoding/json"
	"errors"
	"regexp"
)

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidateImageDigest(digest string) *validationError {
	if !digestRe.MatchString(digest) {
		return validationErrorf(ErrInvalidDigest, "digest must be sha256:<64 hex chars>")
	}
	return nil
}

func DecodeImageManifest(data []byte) (*Manifest, *validationError) {
	m, err := DecodeManifest(data)
	if err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "%s", err.Reason)
	}
	return m, nil
}

func RegistrationManifestJSON(m *Manifest) (string, error) {
	if m == nil {
		return "", errors.New("contracts: nil manifest")
	}
	b, err := json.Marshal(struct {
		ContractVersion string   `json:"contract_version"`
		Capabilities    []string `json:"capabilities"`
		Entrypoint      []string `json:"entrypoint"`
		StateFormat     string   `json:"state_format"`
	}{
		ContractVersion: m.ContractVersion,
		Capabilities:    m.Capabilities,
		Entrypoint:      m.Entrypoint,
		StateFormat:     m.StateFormat,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func EqualRegistrationManifests(a, b *Manifest) bool {
	if a == nil || b == nil {
		return a == b
	}
	aj, errA := RegistrationManifestJSON(a)
	bj, errB := RegistrationManifestJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return aj == bj
}
