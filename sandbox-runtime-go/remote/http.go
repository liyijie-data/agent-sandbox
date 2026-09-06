package remote

import (
	"agent-platform/internal/contracts"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxRequest  = 1 << 20
	maxResponse = 4 << 20
	maxSpec     = 4 << 20
	maxSchema   = 64 << 10
)

type unavailableError struct{ err error }

func (e unavailableError) Error() string { return e.err.Error() }
func (e unavailableError) Unwrap() error { return e.err }
func isUnavailable(err error) bool       { _, ok := err.(unavailableError); return ok }

var client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func request(ctx context.Context, method, target string, body []byte, headers http.Header, limit int64) (int, http.Header, []byte, error) {
	if int64(len(body)) > maxRequest {
		return 0, nil, nil, fmt.Errorf("request exceeds bound")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header = headers
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, unavailableError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("redirects are forbidden")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	if int64(len(data)) > limit {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("response exceeds bound")
	}
	return resp.StatusCode, resp.Header, data, nil
}
func download(ctx context.Context, ref *contracts.ResourceRef) ([]byte, error) {
	if ref == nil {
		return nil, fmt.Errorf("missing spec")
	}
	u, err := url.Parse(ref.DownloadURL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.User != nil {
		return nil, fmt.Errorf("invalid spec download URL")
	}
	status, _, b, err := request(ctx, http.MethodGet, ref.DownloadURL, nil, http.Header{"Accept": []string{"application/json"}}, maxSpec)
	if err != nil {
		return nil, unavailableError{err}
	}
	if status < 200 || status >= 300 {
		return nil, unavailableError{fmt.Errorf("spec download HTTP %d", status)}
	}
	if ref.SizeBytes > 0 && int64(len(b)) != ref.SizeBytes {
		return nil, fmt.Errorf("spec size mismatch")
	}
	sum := sha256.Sum256(b)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), ref.SHA256) {
		return nil, fmt.Errorf("spec sha256 mismatch")
	}
	return b, nil
}
