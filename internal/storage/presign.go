package storage

import (
	"context"
	"net/url"
)

type Presigner interface {
	PresignPut(ctx context.Context, objectKey string) (*url.URL, error)
	PresignPutSandbox(ctx context.Context, objectKey string) (*url.URL, error)
	PresignGet(ctx context.Context, objectKey string) (*url.URL, error)

	PresignGetSandbox(ctx context.Context, objectKey string) (*url.URL, error)
}
type ObjectVerifier interface {
	Verify(ctx context.Context, objectKey, expectedSHA string, size int64) error
}

type ObjectDeleter interface {
	Delete(ctx context.Context, objectKey string) error
}
