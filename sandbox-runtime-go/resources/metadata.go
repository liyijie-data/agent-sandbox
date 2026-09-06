package resources

import (
	"agent-platform/internal/contracts"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const metadataEntryLimit = 1024
const noticeReserve = 160

func prepareReferencesWithRoot(root string, files, skills []contracts.ResourceRef) []contracts.Message {
	out := make([]contracts.Message, 0, len(files)+len(skills))
	used, omitted := 0, 0
	add := func(s string) {
		if len(s) > metadataEntryLimit || used+len(s)+noticeReserve > maxContextRefBytes || len(out) >= maxContextRefs {
			omitted++
			return
		}
		out = append(out, contracts.Message{Role: contracts.RoleSystem, Content: &s})
		used += len(s)
	}
	for _, r := range files {
		st, e := os.Stat(filepath.Join(root, r.Name))
		size, typ := r.SizeBytes, "file"
		if e == nil {
			size = st.Size()
			if !st.Mode().IsRegular() {
				typ = "non-regular"
			}
		}
		add(fmt.Sprintf("Untrusted resource metadata: name=%q path=%q size=%d sha256=%q type=%q; read on demand with agent_read_file.", r.Name, r.Name, size, r.SHA256, typ))
	}
	for _, r := range skills {
		add(fmt.Sprintf("Untrusted skill metadata: name=%q description=%q path=%q; load on demand with agent_load_skill.", r.Name, skillDescription(root, r.Name), ".skills/"+r.Name+"/SKILL.md"))
	}
	if omitted > 0 {
		s := fmt.Sprintf("Untrusted resource metadata omitted: count=%d; use agent_list_files or load entries on demand.", omitted)
		if used+len(s) <= maxContextRefBytes {
			out = append(out, contracts.Message{Role: contracts.RoleSystem, Content: &s})
		}
	}
	return out
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func skillDescription(root, name string) string {
	f, e := os.Open(filepath.Join(root, ".skills", name, "SKILL.md"))
	if e != nil {
		return "available on demand"
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 16<<10+1))
	if e != nil || len(b) > 16<<10 {
		return "available on demand"
	}
	text := string(b)
	if !strings.HasPrefix(text, "---\n") {
		return "available on demand"
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "available on demand"
	}
	var fm skillFrontmatter
	if yaml.Unmarshal([]byte(rest[:end]), &fm) != nil || fm.Description == "" {
		return "available on demand"
	}
	d := strings.TrimSpace(fm.Description)
	if len(d) > 512 {
		d = d[:512]
		for !utf8.ValidString(d) {
			d = d[:len(d)-1]
		}
	}
	return d
}
