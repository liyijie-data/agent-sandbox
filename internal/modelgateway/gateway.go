package modelgateway

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const ChatCompletionsPath = "/chat/completions"

func NormalizeUpstream(raw string, requireHTTPS bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("modelgateway: parse base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("modelgateway: unsupported scheme %q", u.Scheme)
	}
	if requireHTTPS && u.Scheme != "https" {
		return "", fmt.Errorf("modelgateway: https required but got %q", u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("modelgateway: base_url has no host")
	}
	if u.User != nil {
		return "", fmt.Errorf("modelgateway: base_url must not carry userinfo")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("modelgateway: base_url must not carry a fragment")
	}
	if strings.Contains(u.Host, " ") || strings.Contains(u.Host, ",") {
		return "", fmt.Errorf("modelgateway: ambiguous host")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "", fmt.Errorf("modelgateway: unspecified address not allowed")
	}
	port := u.Port()
	if port != "" {
		if n, err := net.LookupPort("tcp", port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("modelgateway: invalid port %q", port)
		}
	}
	out := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimSuffix(u.Path, "/")}
	return out.String(), nil
}

func ValidateChatCompletions(method, path, model, frozenModel string) error {
	if method != http.MethodPost {
		return fmt.Errorf("modelgateway: only POST is allowed, got %s", method)
	}
	if path != ChatCompletionsPath {
		return fmt.Errorf("modelgateway: path %q not allowed", path)
	}
	if model != frozenModel {
		return fmt.Errorf("modelgateway: model %q does not match frozen %q", model, frozenModel)
	}
	return nil
}

var DangerousHeaderNames = []string{
	"Host", "Transfer-Encoding", "Proxy-Authorization",
	"Proxy-Authenticate", "X-Forwarded-For", "X-Forwarded-Host",
	"X-Forwarded-Proto", "Upgrade",
}

func CheckHeaders(h http.Header) error {
	for _, name := range DangerousHeaderNames {
		if h.Get(name) != "" {
			return fmt.Errorf("modelgateway: header %q must not be forwarded", name)
		}
	}
	return nil
}

type RedirectPolicy struct {
	Disallow bool
}

func (p RedirectPolicy) FollowAllowed() bool { return !p.Disallow }
