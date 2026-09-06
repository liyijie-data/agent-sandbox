package images

import "errors"

var (
	ErrNotFound = errors.New("images: not found")

	ErrConflict = errors.New("images: registration conflict")

	ErrStateConflict = errors.New("images: registration state conflict")

	ErrVerificationRequired = errors.New("images: verification evidence required")

	ErrSignatureInvalid = errors.New("images: signature verification failed")

	ErrInvalidDigest = errors.New("images: invalid digest")
)
