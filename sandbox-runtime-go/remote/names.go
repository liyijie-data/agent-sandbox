package remote

import (
	"crypto/sha256"
	"fmt"
)

const maxModelToolName = 64

func encode(v string) string {
	out := ""
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' {
			out += string(b)
		} else if b == '_' {
			out += "_u"
		} else {
			out += fmt.Sprintf("_x%02x", b)
		}
	}
	return out
}
func toolName(id, op string) string { return encode(id) + "__" + encode(op) }

func ModelToolName(name string) string {
	if len(name) <= maxModelToolName {
		return name
	}
	h := sha256.Sum256([]byte(name))
	return fmt.Sprintf("t_%x", h[:])[:maxModelToolName]
}

func decode(v string) (string, bool) {
	var out []byte
	for i := 0; i < len(v); {
		if v[i] != '_' {
			out = append(out, v[i])
			i++
			continue
		}
		if i+1 >= len(v) {
			return "", false
		}
		if v[i+1] == 'u' {
			out = append(out, '_')
			i += 2
			continue
		}
		if i+3 >= len(v) || v[i+1] != 'x' {
			return "", false
		}
		hex := func(c byte) (byte, bool) {
			switch {
			case c >= '0' && c <= '9':
				return c - '0', true
			case c >= 'a' && c <= 'f':
				return c - 'a' + 10, true
			case c >= 'A' && c <= 'F':
				return c - 'A' + 10, true
			}
			return 0, false
		}
		hi, ok1 := hex(v[i+2])
		lo, ok2 := hex(v[i+3])
		if !ok1 || !ok2 {
			return "", false
		}
		out = append(out, hi<<4|lo)
		i += 4
	}
	return string(out), true
}

func DecodeToolName(name string) (string, string, bool) {
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '_' && name[i+1] == '_' {
			id, ok1 := decode(name[:i])
			op, ok2 := decode(name[i+2:])
			return id, op, ok1 && ok2
		}
	}
	return "", "", false
}
