package remote

import (
	"agent-platform/internal/contracts"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type operation struct {
	id, method, path, desc string
	params                 []parameter
	body                   map[string]any
	root                   map[string]any
}
type parameter struct {
	name, where string
	required    bool
	schema      map[string]any
}
type openapi struct {
	idv     string
	base    string
	allowed map[string]bool
	ops     map[string]operation
	ctx     context.Context
}

func newOpenAPI(ctx context.Context, s contracts.ToolSpec) (adapter, error) {
	if s.ID == "" || s.BaseURL == "" || s.Spec == nil || len(s.AllowedActions) == 0 {
		return nil, fmt.Errorf("invalid OpenAPI tool configuration")
	}
	u, e := url.Parse(s.BaseURL)
	if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("invalid OpenAPI base URL")
	}
	a := &openapi{idv: s.ID, base: strings.TrimRight(s.BaseURL, "/"), allowed: map[string]bool{}, ops: map[string]operation{}, ctx: ctx}
	for _, x := range s.AllowedActions {
		if x == "" || a.allowed[x] {
			return nil, fmt.Errorf("invalid allowed operation")
		}
		a.allowed[x] = true
	}
	raw, e := download(ctx, s.Spec)
	if e != nil {
		return nil, e
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, fmt.Errorf("spec is not valid JSON")
	}
	v, _ := doc["openapi"].(string)
	if !strings.HasPrefix(v, "3.") {
		return nil, fmt.Errorf("only OpenAPI 3.x is supported")
	}
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		return nil, fmt.Errorf("spec has no paths")
	}
	for p, rawitem := range paths {
		item, ok := rawitem.(map[string]any)
		if !ok || !strings.HasPrefix(p, "/") || strings.Contains(p, "://") || strings.Contains(p, "\\") || strings.Contains(p, "?") || strings.Contains(p, "#") {
			return nil, fmt.Errorf("invalid OpenAPI path")
		}
		for _, m := range []string{"get", "post", "put", "patch", "delete", "head", "options"} {
			opraw, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			id, _ := opraw["operationId"].(string)
			if id == "" || a.ops[id].id != "" {
				return nil, fmt.Errorf("invalid or duplicate operationId")
			}
			op := operation{id: id, method: m, path: p, desc: id, root: doc}
			if x, _ := opraw["summary"].(string); x != "" {
				op.desc = x
			}
			if ps, ok := opraw["parameters"].([]any); ok {
				for _, x := range ps {
					pm, e := resolve(doc, x, 0, map[string]bool{})
					if e != nil {
						return nil, e
					}
					name, _ := pm["name"].(string)
					where, _ := pm["in"].(string)
					if name == "" || where != "path" && where != "query" {
						return nil, fmt.Errorf("unsupported OpenAPI parameter")
					}
					sc, _ := pm["schema"].(map[string]any)
					if sc == nil {
						sc = map[string]any{"type": "string"}
					}
					if _, e := schema(doc, sc, 0, new(int)); e != nil {
						return nil, e
					}
					op.params = append(op.params, parameter{name, where, pm["required"] == true, sc})
				}
			}
			if rb, ok := opraw["requestBody"]; ok {
				rm, e := resolve(doc, rb, 0, map[string]bool{})
				if e != nil {
					return nil, e
				}
				content, _ := rm["content"].(map[string]any)
				app, ok := content["application/json"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("unsupported request body")
				}
				bs, _ := app["schema"].(map[string]any)
				if bs == nil {
					bs = map[string]any{"type": "object"}
				}
				if _, e := schema(doc, bs, 0, new(int)); e != nil {
					return nil, e
				}
				op.body = map[string]any{"required": rm["required"] == true, "schema": bs}
			}
			a.ops[id] = op
		}
	}
	for id := range a.allowed {
		if _, ok := a.ops[id]; !ok {
			return nil, fmt.Errorf("allowed operation not found")
		}
	}
	return a, nil
}
func resolve(root map[string]any, v any, depth int, seen map[string]bool) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid reference")
	}
	r, _ := m["$ref"].(string)
	if r == "" {
		return m, nil
	}
	if !strings.HasPrefix(r, "#/") || depth >= 16 || seen[r] {
		return nil, fmt.Errorf("invalid or cyclic reference")
	}
	x := any(root)
	for _, p := range strings.Split(r[2:], "/") {
		mm, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolved reference")
		}
		x = mm[strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")]
	}
	return resolve(root, x, depth+1, func() map[string]bool {
		n := map[string]bool{}
		for k, v := range seen {
			n[k] = v
		}
		n[r] = true
		return n
	}())
}
func (a *openapi) id() string   { return a.idv }
func (a *openapi) close() error { return nil }
func (a *openapi) definitions() ([]Definition, error) {
	out := []Definition{}
	for id := range a.allowed {
		op := a.ops[id]
		props := map[string]any{}
		req := []any{}
		for _, p := range op.params {
			expanded, err := schema(op.root, p.schema, 0, new(int))
			if err != nil {
				return nil, err
			}
			props[p.name] = expanded
			if p.required {
				req = append(req, p.name)
			}
		}
		if op.body != nil {
			expanded, err := schema(op.root, op.body["schema"], 0, new(int))
			if err != nil {
				return nil, err
			}
			props["body"] = expanded
			if op.body["required"] == true {
				req = append(req, "body")
			}
		}
		params := map[string]any{"type": "object", "properties": props}
		if len(req) > 0 {
			params["required"] = req
		}
		d := Definition{Type: "function", Function: FunctionDefinition{Name: toolName(a.idv, id), Description: op.desc, Parameters: params}}
		b, _ := json.Marshal(d)
		if len(b) > maxSchema {
			return nil, fmt.Errorf("tool schema exceeds bound")
		}
		out = append(out, d)
	}
	return out, nil
}
func (a *openapi) call(ctx context.Context, name, args string) (map[string]any, error) {
	var opid string
	for id := range a.allowed {
		if toolName(a.idv, id) == name {
			opid = id
		}
	}
	if opid == "" {
		return nil, fmt.Errorf("tool unauthorized")
	}
	op := a.ops[opid]
	var vals map[string]any
	if args == "" {
		vals = map[string]any{}
	} else if json.Unmarshal([]byte(args), &vals) != nil {
		return nil, fmt.Errorf("arguments are not valid JSON")
	}
	allowed := map[string]bool{"body": op.body != nil}
	for _, p := range op.params {
		allowed[p.name] = true
		if p.required {
			if _, ok := vals[p.name]; !ok {
				return nil, fmt.Errorf("missing required argument %s", p.name)
			}
		}
	}
	for k := range vals {
		if !allowed[k] {
			return nil, fmt.Errorf("unknown argument %s", k)
		}
	}
	path := op.path
	q := url.Values{}
	for _, p := range op.params {
		if v, ok := vals[p.name]; ok {
			if p.where == "path" {
				encoded := strings.ReplaceAll(url.PathEscape(fmt.Sprint(v)), "/", "%2F")
				path = strings.ReplaceAll(path, "{"+p.name+"}", encoded)
			} else {
				q.Set(p.name, fmt.Sprint(v))
			}
		}
	}
	target := a.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var body []byte
	if op.body != nil {
		if v, ok := vals["body"]; ok {
			body, _ = json.Marshal(v)
			if len(body) > maxRequest {
				return nil, fmt.Errorf("request body exceeds bound")
			}
		}
	}
	h := http.Header{"Accept": []string{"application/json"}}
	if len(body) > 0 {
		h.Set("Content-Type", "application/json")
	}
	for _, p := range op.params {
		if v, ok := vals[p.name]; ok {
			if e := validate(op.root, p.schema, v, "arguments."+p.name); e != nil {
				return nil, e
			}
		}
	}
	if op.body != nil {
		if v, ok := vals["body"]; ok {
			if e := validate(op.root, op.body["schema"], v, "arguments.body"); e != nil {
				return nil, e
			}
		}
	}
	status, hh, data, e := request(ctx, strings.ToUpper(op.method), target, body, h, maxResponse)
	if e != nil {
		return nil, e
	}
	if status < 200 || status >= 300 {
		return map[string]any{"ok": false, "error_type": "OpenApiHttpError", "status": status}, nil
	}
	ct := strings.ToLower(strings.Split(hh.Get("Content-Type"), ";")[0])
	if ct == "application/json" || len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		var v any
		if json.Unmarshal(data, &v) != nil {
			return nil, fmt.Errorf("response is not valid JSON")
		}
		return map[string]any{"ok": true, "status": status, "data": v}, nil
	}
	return map[string]any{"ok": true, "status": status, "text": string(data)}, nil
}
