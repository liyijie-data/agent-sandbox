package remote

import (
	"fmt"
	"reflect"
	"strings"
)

var supported = map[string]bool{"$ref": true, "type": true, "title": true, "description": true, "default": true, "enum": true, "const": true, "properties": true, "required": true, "additionalProperties": true, "items": true, "oneOf": true, "anyOf": true, "allOf": true, "minLength": true, "maxLength": true, "minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "minItems": true, "maxItems": true, "uniqueItems": true, "format": true}

func schema(root any, s any, depth int, nodes *int) (map[string]any, error) {
	if depth > 16 || *nodes > 256 {
		return nil, fmt.Errorf("schema exceeds complexity limit")
	}
	m, ok := s.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema must be object")
	}
	(*nodes)++
	for k := range m {
		if !supported[k] {
			return nil, fmt.Errorf("unsupported schema keyword %s", k)
		}
	}
	if ref, ok := m["$ref"].(string); ok {
		if len(ref) < 3 || ref[:2] != "#/" {
			return nil, fmt.Errorf("unsupported schema reference")
		}
		var x any = root
		for _, p := range strings.Split(ref[2:], "/") {
			p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
			mm, ok := x.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("schema reference not found")
			}
			x = mm[p]
		}
		return schema(root, x, depth+1, nodes)
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	if props, ok := m["properties"].(map[string]any); ok {
		np := map[string]any{}
		for k, v := range props {
			x, e := schema(root, v, depth+1, nodes)
			if e != nil {
				return nil, e
			}
			np[k] = x
		}
		out["properties"] = np
	}
	if it, ok := m["items"]; ok {
		x, e := schema(root, it, depth+1, nodes)
		if e != nil {
			return nil, e
		}
		out["items"] = x
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		if a, ok := m[k].([]any); ok {
			na := make([]any, len(a))
			for i, v := range a {
				x, e := schema(root, v, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				na[i] = x
			}
			out[k] = na
		}
	}
	return out, nil
}
func validate(root any, s any, v any, path string) error {
	m, err := schema(root, s, 0, new(int))
	if err != nil {
		return err
	}
	if e, ok := m["enum"].([]any); ok {
		found := false
		for _, x := range e {
			if reflect.DeepEqual(x, v) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s violates enum", path)
		}
	}
	if c, ok := m["const"]; ok && !reflect.DeepEqual(c, v) {
		return fmt.Errorf("%s violates const", path)
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		o, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be object", path)
		}
		req, _ := m["required"].([]any)
		for _, x := range req {
			n, _ := x.(string)
			if _, ok := o[n]; !ok {
				return fmt.Errorf("%s.%s is required", path, n)
			}
		}
		props, _ := m["properties"].(map[string]any)
		for n, x := range o {
			if p, ok := props[n]; ok {
				if err := validate(root, p, x, path+"."+n); err != nil {
					return err
				}
			} else if m["additionalProperties"] == false {
				return fmt.Errorf("%s.%s is unknown", path, n)
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be array", path)
		}
		if it, ok := m["items"]; ok {
			for i, x := range a {
				if err := validate(root, it, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s must string", path)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must boolean", path)
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("%s must integer", path)
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%s must number", path)
		}
	}
	return nil
}
