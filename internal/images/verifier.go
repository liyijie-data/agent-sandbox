package images

import "context"

type Verifier interface {
	Verify(ctx context.Context, ref string) error
}

type FailClosedVerifier struct{}

func (FailClosedVerifier) Verify(context.Context, string) error { return ErrSignatureInvalid }

type OperatorVerifier struct{}

func (OperatorVerifier) Verify(context.Context, string) error { return nil }

type RevokeCoordinator interface {
	CancelImageRuns(ctx context.Context, clientID, imageRegistrationID string) error
}
