package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Purpose string

const (
	PurposeModel        Purpose = "model"
	PurposeRuntime      Purpose = "runtime"
	PurposeSteeringRead Purpose = "steering:read"
	PurposeSteeringAck  Purpose = "steering:ack"
)

type Claims struct {
	RunID     string    `json:"run_id"`
	Stage     int       `json:"stage"`
	Fence     int64     `json:"fence"`
	Purpose   Purpose   `json:"purpose"`
	IssuedAt  time.Time `json:"iat"`
	ExpiresAt time.Time `json:"exp"`
}

type Signer struct {
	secret []byte
}

func NewSigner(secret []byte) (*Signer, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("security: token signing secret must be >= 16 bytes")
	}
	return &Signer{secret: append([]byte(nil), secret...)}, nil
}

func (s *Signer) Mint(c Claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("security: marshal claims: %w", err)
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *Signer) Verify(token, runID string, stage int, fence int64, purpose Purpose, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return fmt.Errorf("security: malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("security: bad payload encoding")
	}
	wantSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("security: bad signature encoding")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	if !hmac.Equal(wantSig, mac.Sum(nil)) {
		return fmt.Errorf("security: invalid signature")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return fmt.Errorf("security: unreadable claims")
	}
	if c.Purpose != purpose {
		return fmt.Errorf("security: token purpose %q does not match %q", c.Purpose, purpose)
	}
	if c.RunID != runID || c.Stage != stage || c.Fence != fence {
		return fmt.Errorf("security: token identity mismatch (run/stage/fence)")
	}
	if !now.Before(c.ExpiresAt) {
		return fmt.Errorf("security: token expired")
	}
	if now.Before(c.IssuedAt) {
		return fmt.Errorf("security: token not yet valid")
	}
	return nil
}

func TokenTTL(p Purpose, configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	switch p {
	case PurposeSteeringRead, PurposeSteeringAck:
		return 10 * time.Minute
	default:
		return 35 * time.Minute
	}
}
