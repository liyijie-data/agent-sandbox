package app

import (
	"agent-platform/sandbox-runtime-go/plugin"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func ValidateProfile(c plugin.Config) error {
	required := map[string]bool{}
	for _, p := range DefaultConfig().Plugins {
		required[p.Name] = true
	}
	seen := map[string]bool{}
	for _, p := range c.Plugins {
		if !required[p.Name] {
			return fmt.Errorf("unknown plugin %q", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate plugin %q", p.Name)
		}
		seen[p.Name] = true
		if len(bytes.TrimSpace(p.Config)) == 0 {
			continue
		}
		switch p.Name {
		case "context.standard":
			var x struct {
				ContextWindowTokens int `json:"context_window_tokens"`
				ReserveOutputTokens int `json:"reserve_output_tokens"`
			}
			if e := strict(p.Config, &x); e != nil {
				return e
			}
			if x.ContextWindowTokens <= 0 || x.ReserveOutputTokens <= 0 || x.ContextWindowTokens <= x.ReserveOutputTokens {
				return fmt.Errorf("invalid context profile")
			}
		case "trace.standard":
			var x struct {
				Enabled bool `json:"enabled"`
			}
			if e := strict(p.Config, &x); e != nil {
				return e
			}
		default:
			return fmt.Errorf("config not supported for %q", p.Name)
		}
	}
	for n := range required {
		if !seen[n] {
			return fmt.Errorf("missing plugin %q", n)
		}
	}
	return nil
}
func strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var x any
	if e := d.Decode(&x); e != io.EOF {
		return fmt.Errorf("trailing profile config")
	}
	return nil
}
