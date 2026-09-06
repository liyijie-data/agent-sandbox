package runtime

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/trace"
	"net/url"
	"strings"
)

func runtimeTraceOptions(cfg *contracts.RuntimeConfig, versions map[string]string, enabled bool) trace.Options {
	o := trace.Options{RunID: cfg.RunID, ExecutionID: cfg.ExecutionID, Stage: cfg.Stage, Fence: cfg.Fence, PluginVersions: versions, Enabled: enabled}
	o.Secrets = append(o.Secrets, cfg.Model.Token, cfg.Runtime.Token)
	o.SignatureQueryKeys = append(o.SignatureQueryKeys, "signature", "sig", "token", "access_token", "auth", "authorization")
	for _, t := range cfg.Tools {
		for k, v := range t.Env {
			u := strings.ToUpper(k)
			if strings.Contains(u, "TOKEN") || strings.Contains(u, "KEY") || strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD") || strings.Contains(u, "CREDENTIAL") {
				o.Secrets = append(o.Secrets, v)
			}
		}
		addQuery := func(raw string, keys []string) {
			x, e := url.Parse(raw)
			if e != nil {
				return
			}
			q := x.Query()
			for _, k := range keys {
				o.SignatureQueryKeys = append(o.SignatureQueryKeys, k)
				for _, v := range q[k] {
					if v != "" {
						o.Secrets = append(o.Secrets, v)
					}
				}
			}
		}
		if t.Spec != nil {
			addQuery(t.Spec.DownloadURL, t.Spec.SignatureQueryKeys)
		}
	}
	for _, r := range append(append([]contracts.ResourceRef{}, cfg.Files...), cfg.Skills...) {
		x, e := url.Parse(r.DownloadURL)
		if e == nil {
			for _, k := range r.SignatureQueryKeys {
				o.SignatureQueryKeys = append(o.SignatureQueryKeys, k)
				for _, v := range x.Query()[k] {
					if v != "" {
						o.Secrets = append(o.Secrets, v)
					}
				}
			}
		}
	}
	if cfg.ResultBundle != nil {
		x, e := url.Parse(cfg.ResultBundle.UploadURL)
		if e == nil {
			for _, k := range cfg.ResultBundle.SignatureQueryKeys {
				o.SignatureQueryKeys = append(o.SignatureQueryKeys, k)
				for _, v := range x.Query()[k] {
					if v != "" {
						o.Secrets = append(o.Secrets, v)
					}
				}
			}
		}
	}
	return o
}
