package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"agent-platform/internal/config"
)

type ObjectStore struct {
	client         *minio.Client
	presign        *minio.Client
	sandboxPresign *minio.Client
	bucket         string
	presignTTL     time.Duration
}

func New(cfg config.S3Config) (*ObjectStore, error) {
	creds := credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	opts := &minio.Options{
		Creds:  creds,
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}

	client, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}

	presign, err := buildPresignClient(cfg.Endpoint, cfg.PublicEndpoint, opts)
	if err != nil {
		return nil, err
	}

	sandboxEndpoint := strings.TrimSpace(cfg.SandboxEndpoint)
	if sandboxEndpoint == "" {
		sandboxEndpoint = strings.TrimSpace(cfg.PublicEndpoint)
	}
	if sandboxEndpoint == "" {
		sandboxEndpoint = cfg.Endpoint
	}
	sandboxPresign, err := buildPresignClient(cfg.Endpoint, sandboxEndpoint, opts)
	if err != nil {
		return nil, err
	}

	return &ObjectStore{
		client:         client,
		presign:        presign,
		sandboxPresign: sandboxPresign,
		bucket:         cfg.Bucket,
		presignTTL:     cfg.PresignTTL,
	}, nil
}

func buildPresignClient(wireEndpoint, urlEndpoint string, opts *minio.Options) (*minio.Client, error) {
	urlEndpoint = stripScheme(strings.TrimSpace(urlEndpoint))
	if urlEndpoint == "" || urlEndpoint == stripScheme(strings.TrimSpace(wireEndpoint)) {
		return minio.New(wireEndpoint, opts)
	}
	return minio.New(urlEndpoint, opts)
}

func stripScheme(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return endpoint
}

func (s *ObjectStore) Bucket() string { return s.bucket }

func (s *ObjectStore) Delete(ctx context.Context, objectKey string) error {
	return s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{})
}

func (s *ObjectStore) Put(ctx context.Context, objectKey string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, objectKey, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

func (s *ObjectStore) PutBytes(ctx context.Context, objectKey string, data []byte, contentType string) error {
	return s.Put(ctx, objectKey, bytes.NewReader(data), int64(len(data)), contentType)
}

func (s *ObjectStore) PresignPut(ctx context.Context, objectKey string) (*url.URL, error) {
	return s.presign.PresignedPutObject(ctx, s.bucket, objectKey, s.presignTTL)
}
func (s *ObjectStore) PresignPutSandbox(ctx context.Context, objectKey string) (*url.URL, error) {
	return s.sandboxPresign.PresignedPutObject(ctx, s.bucket, objectKey, s.presignTTL)
}

func (s *ObjectStore) PresignGet(ctx context.Context, objectKey string) (*url.URL, error) {
	return s.presign.PresignedGetObject(ctx, s.bucket, objectKey, s.presignTTL, nil)
}

func (s *ObjectStore) PresignGetWithTTL(ctx context.Context, objectKey string, ttl time.Duration) (*url.URL, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("presign ttl must be positive")
	}
	if s.presignTTL > 0 && ttl > s.presignTTL {
		ttl = s.presignTTL
	}
	return s.presign.PresignedGetObject(ctx, s.bucket, objectKey, ttl, nil)
}
func (s *ObjectStore) Stat(ctx context.Context, objectKey string) (minio.ObjectInfo, error) {
	return s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
}
func (s *ObjectStore) GetBytes(ctx context.Context, objectKey string) ([]byte, error) {
	o, e := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer o.Close()
	return io.ReadAll(o)
}

func (s *ObjectStore) Exists(ctx context.Context, objectKey string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.Code == "NotFound" || resp.StatusCode == 404 {
		return false, nil
	}
	return false, err
}

func (s *ObjectStore) ListKeys(ctx context.Context, prefix string, limit int) ([]string, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	var keys []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return keys, obj.Err
		}
		keys = append(keys, obj.Key)
		if len(keys) >= limit {
			break
		}
	}
	return keys, nil
}
func (s *ObjectStore) Verify(ctx context.Context, objectKey, expected string, size int64) error {
	o, e := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if e != nil {
		return e
	}
	defer o.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(o, size+1))
	if e != nil {
		return e
	}
	if n != size {
		return fmt.Errorf("object size mismatch")
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return fmt.Errorf("object hash mismatch")
	}
	return nil
}

func (s *ObjectStore) PresignGetSandbox(ctx context.Context, objectKey string) (*url.URL, error) {
	return s.sandboxPresign.PresignedGetObject(ctx, s.bucket, objectKey, s.presignTTL, nil)
}
