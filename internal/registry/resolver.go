package registry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const acceptManifest = "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json"

type Resolver struct {
	scheme   string
	host     string
	client   *http.Client
	username string
	password string
}

func New(endpoint, authFile string) *Resolver {
	endpoint = strings.TrimSpace(endpoint)
	scheme := "https"
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Scheme != "" {
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			scheme = parsed.Scheme
		}
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://"), "/")
	r := &Resolver{scheme: scheme, host: host, client: &http.Client{Timeout: 15 * time.Second}}
	if authFile != "" {
		r.username, r.password = credentials(authFile, r.host)
	}
	return r
}

func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	host, repository, tag, err := splitReference(ref)
	if err != nil || r.host == "" || host != r.host {
		return "", fmt.Errorf("registry: untrusted tagged reference")
	}
	u := r.scheme + "://" + host + "/v2/" + repository + "/manifests/" + tag
	resp, err := r.request(ctx, u, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		token, terr := r.bearerToken(ctx, resp.Header.Get("Www-Authenticate"), repository)
		if terr != nil {
			return "", terr
		}
		resp.Body.Close()
		resp, err = r.request(ctx, u, "Bearer "+token)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry: manifest response %d", resp.StatusCode)
	}
	if digest := resp.Header.Get("Docker-Content-Digest"); validDigest(digest) {
		return digest, nil
	}

	if resp.Request.Method == http.MethodHead {
		resp.Body.Close()
		resp, err = r.get(ctx, u, "")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("registry: manifest response %d", resp.StatusCode)
		}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(b) == 0 {
		return "", fmt.Errorf("registry: read manifest")
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func (r *Resolver) request(ctx context.Context, u, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", acceptManifest)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	} else if r.username != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	return r.client.Do(req)
}

func (r *Resolver) get(ctx context.Context, u, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", acceptManifest)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	} else if r.username != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	return r.client.Do(req)
}

func (r *Resolver) bearerToken(ctx context.Context, challenge, repository string) (string, error) {
	params := parseChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry: unsupported authentication")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", "repository:"+repository+":pull")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if r.username != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return "", fmt.Errorf("registry: token request failed")
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("registry: token missing")
}

func splitReference(ref string) (string, string, string, error) {
	if strings.ContainsAny(ref, "@ \t\r\n") {
		return "", "", "", fmt.Errorf("invalid reference")
	}
	parts := strings.Split(ref, "/")
	if len(parts) < 2 || (!strings.Contains(parts[0], ".") && !strings.Contains(parts[0], ":") && parts[0] != "localhost") {
		return "", "", "", fmt.Errorf("registry host required")
	}
	last := parts[len(parts)-1]
	i := strings.LastIndex(last, ":")
	if i <= 0 || i == len(last)-1 {
		return "", "", "", fmt.Errorf("tag required")
	}
	parts[len(parts)-1] = last[:i]
	return parts[0], strings.Join(parts[1:], "/"), last[i+1:], nil
}

func validDigest(v string) bool {
	if !strings.HasPrefix(v, "sha256:") || len(v) != len("sha256:")+64 {
		return false
	}
	for _, c := range v[len("sha256:"):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func credentials(path, host string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	type authEntry struct {
		Auth     string `json:"auth"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	var doc struct {
		Auths map[string]authEntry `json:"auths"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return "", ""
	}

	if len(doc.Auths) == 0 && json.Unmarshal(b, &doc.Auths) != nil {
		return "", ""
	}
	for key, auth := range doc.Auths {
		if strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(key, "/"), "https://"), "http://") != host {
			continue
		}
		if auth.Username != "" {
			return auth.Username, auth.Password
		}
		decoded, err := base64.StdEncoding.DecodeString(auth.Auth)
		if err == nil {
			pair := strings.SplitN(string(decoded), ":", 2)
			if len(pair) == 2 {
				return pair[0], pair[1]
			}
		}
	}
	return "", ""
}

func parseChallenge(v string) map[string]string {
	out := map[string]string{}
	if !strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return out
	}
	for _, part := range strings.Split(strings.TrimSpace(v[7:]), ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) == 2 {
			out[pair[0]] = strings.Trim(pair[1], `"`)
		}
	}
	return out
}
