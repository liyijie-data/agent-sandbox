package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func loadSkill(b Builtin, name string) (string, error) {
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("invalid skill name")
	}
	r, rel, e := rootFor(b, filepath.Join(".skills", name, "SKILL.md"))
	if e != nil {
		return "", e
	}
	defer r.Close()
	f, e := r.Open(rel)
	if e != nil {
		return "", e
	}
	defer f.Close()
	d, e := readBounded(f, 16*1024)
	if e != nil {
		return "", fmt.Errorf("skill_instructions_too_large: split references into smaller files")
	}
	if !utf8.Valid(d) {
		return "", fmt.Errorf("skill is not valid UTF-8 text")
	}
	return string(d), nil
}
