package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

func readFile(b Builtin, path string, offset, maxBytes *int64) (string, error) {
	r, rel, e := rootFor(b, path)
	if e != nil {
		return "", e
	}
	defer r.Close()
	f, e := r.Open(rel)
	if e != nil {
		return "", e
	}
	defer f.Close()
	if offset == nil && maxBytes == nil {
		d, e := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
		if e != nil {
			return "", e
		}
		if int64(len(d)) <= maxReadBytes {
			if !utf8.Valid(d) {
				return "", fmt.Errorf("file is not valid UTF-8 text")
			}
			return string(d), nil
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			return "", e
		}
	}
	off := int64(0)
	if offset != nil {
		off = *offset
	}
	page := int64(defaultPageBytes)
	if maxBytes != nil {
		page = *maxBytes
	}
	if off < 0 || page <= 0 || page > maxPageBytes {
		return "", fmt.Errorf("invalid offset or max_bytes")
	}
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	if off > st.Size() {
		return "", fmt.Errorf("offset beyond EOF")
	}
	if _, e = f.Seek(off, io.SeekStart); e != nil {
		return "", e
	}
	d, e := io.ReadAll(io.LimitReader(f, page+1))
	if e == nil && int64(len(d)) > page {
		d = d[:page]
		cut := len(d)
		for i := 0; i < len(d); {
			if !utf8.FullRune(d[i:]) {
				cut = i
				break
			}
			rn, n := utf8.DecodeRune(d[i:])
			if rn == utf8.RuneError && n == 1 {
				return "", fmt.Errorf("file is not valid UTF-8 text")
			}
			i += n
		}
		d = d[:cut]
		if len(d) == 0 {
			return "", fmt.Errorf("max_bytes ends inside UTF-8 rune")
		}
	}
	if e != nil {
		return "", e
	}
	if !utf8.Valid(d) {
		return "", fmt.Errorf("file is not valid UTF-8 text")
	}
	if off > 0 && len(d) > 0 && !utf8.RuneStart(d[0]) {
		return "", fmt.Errorf("offset is not a UTF-8 boundary")
	}
	next := off + int64(len(d))
	v := map[string]any{"path": path, "content": string(d), "offset": off, "next_offset": next, "eof": next >= st.Size(), "truncated": next < st.Size()}
	out, _ := json.Marshal(v)
	for len(out) > 16*1024 && len(d) > 0 {
		cut := len(d) - len(d)/8 - 1
		if cut < 1 {
			cut = len(d) - 1
		}
		for cut > 0 && !utf8.RuneStart(d[cut]) {
			cut--
		}
		if cut <= 0 {
			break
		}
		d = d[:cut]
		next = off + int64(len(d))
		v["content"], v["next_offset"], v["eof"], v["truncated"] = string(d), next, next >= st.Size(), next < st.Size()
		out, _ = json.Marshal(v)
	}
	return string(out), nil
}
