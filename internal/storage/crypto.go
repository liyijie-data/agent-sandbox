package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"crypto/subtle"
)

type ContentCipher struct {
	keys    map[int][]byte
	active  int
	hmacKey []byte
}

func NewContentCipher(keys map[int][]byte, hmacKey []byte) (*ContentCipher, error) {
	if len(hmacKey) == 0 {
		return nil, fmt.Errorf("content cipher: hmac key must not be empty")
	}
	active := -1
	for v, k := range keys {
		if len(k) != 32 {
			return nil, fmt.Errorf("content cipher: key version %d must be 32 bytes, got %d", v, len(k))
		}
		if v < 0 {
			return nil, fmt.Errorf("content cipher: key version %d must be >= 0", v)
		}
		if v > active {
			active = v
		}
	}
	if active < 0 {
		return nil, fmt.Errorf("content cipher: no key versions configured")
	}
	return &ContentCipher{keys: keys, active: active, hmacKey: hmacKey}, nil
}

func (c *ContentCipher) ActiveVersion() int { return c.active }

func (c *ContentCipher) Encrypt(purpose string, plaintext []byte) ([]byte, error) {
	key, ok := c.keys[c.active]
	if !ok {
		return nil, fmt.Errorf("content cipher: active key %d missing", c.active)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ad := make([]byte, 4+len(purpose))
	binary.BigEndian.PutUint32(ad, uint32(len(purpose)))
	copy(ad[4:], purpose)
	return gcm.Seal(nonce, nonce, plaintext, ad), nil
}

func (c *ContentCipher) Decrypt(purpose string, ciphertext []byte, version int) ([]byte, error) {
	key, ok := c.keys[version]
	if !ok {
		return nil, fmt.Errorf("content cipher: key version %d not held", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if gcm.NonceSize() > len(ciphertext) {
		return nil, fmt.Errorf("content cipher: ciphertext too short")
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	ad := make([]byte, 4+len(purpose))
	binary.BigEndian.PutUint32(ad, uint32(len(purpose)))
	copy(ad[4:], purpose)
	return gcm.Open(nil, nonce, body, ad)
}

func (c *ContentCipher) Fingerprint(domain string, payload []byte) []byte {
	mac := hmac.New(sha256.New, c.hmacKey)
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(domain)))
	mac.Write(lenBuf[:])
	mac.Write([]byte(domain))
	mac.Write(payload)
	return mac.Sum(nil)
}

func ConstantTimeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

func SafeExpiry(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.Before(b) {
		return a
	}
	return b
}
