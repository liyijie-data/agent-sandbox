package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"agent-platform/internal/security"
)

const runtimeTokenPurpose = security.PurposeRuntime

type TokenMinter interface {
	Mint(c security.Claims) (string, error)
}

type signerMinter struct {
	signer *security.Signer
	ttl    time.Duration
	clock  interface{ Now() time.Time }
}

func (m *signerMinter) Mint(c security.Claims) (string, error) {
	now := m.clock.Now()
	if c.IssuedAt.IsZero() {
		c.IssuedAt = now
	}
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = now.Add(security.TokenTTL(c.Purpose, m.ttl))
	}
	return m.signer.Mint(c)
}

func newExecutionID(runID string, stageNo int, owner string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + itoa(stageNo) + "\x00" + owner))
	return "exec_" + hex.EncodeToString(sum[:8])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(b[i:])
	if neg {
		s = "-" + s
	}
	return s
}
