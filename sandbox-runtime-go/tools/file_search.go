package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

func searchFile(b Builtin, path, query string, offset *int64) (string, error) {
	if query == "" || len(query) > 1024 {
		return "", fmt.Errorf("query is required and bounded")
	}
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
	start := int64(0)
	if offset != nil {
		start = *offset
	}
	if start < 0 {
		return "", fmt.Errorf("invalid offset")
	}
	st, e := f.Stat()
	if e != nil || start > st.Size() {
		return "", fmt.Errorf("invalid offset")
	}
	if _, e = f.Seek(start, io.SeekStart); e != nil {
		return "", e
	}
	matches := make([]map[string]any, 0, 40)
	const chunkSize = 64 * 1024
	carry := ""
	var scanned int64
	next := start
	for scanned < maxSearchBytes && len(matches) < 40 {
		buf := make([]byte, chunkSize)
		n, er := f.Read(buf)
		if n > 0 {
			part := carry + string(buf[:n])
			base := start + scanned - int64(len(carry))
			for pos := 0; len(matches) < 40; {
				i := strings.Index(part[pos:], query)
				if i < 0 {
					break
				}
				abs := base + int64(pos+i)
				lo, hi := pos+i-80, pos+i+len(query)+80
				if lo < 0 {
					lo = 0
				}
				if hi > len(part) {
					hi = len(part)
				}
				lo, hi = safeSnippet(part, lo, hi)
				matches = append(matches, map[string]any{"offset": abs, "snippet": part[lo:hi]})
				pos += i + len(query)
			}
			scanned += int64(n)
			keep := len(query) - 1
			if keep > len(part) {
				keep = len(part)
			}
			carry = part[len(part)-keep:]
			next = start + scanned
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return "", er
		}
	}
	truncated := next < st.Size()
	if len(matches) == 40 {
		truncated = true
		next = matches[len(matches)-1]["offset"].(int64) + int64(len(query))
	} else if truncated && len(carry) > 0 {
		next -= int64(len(carry))
	}
	result := map[string]any{"path": path, "query": query, "matches": matches, "next_offset": next, "eof": !truncated, "truncated": truncated}
	out, _ := json.Marshal(result)
	for len(out) > 16*1024 && len(matches) > 0 {
		dropped := matches[len(matches)-1]
		matches = matches[:len(matches)-1]
		result["matches"] = matches
		next = dropped["offset"].(int64)
		result["next_offset"] = next
		result["truncated"] = true
		result["eof"] = false
		out, _ = json.Marshal(result)
	}
	return string(out), nil
}

func safeSnippet(s string, lo, hi int) (int, int) {
	for lo < hi && lo < len(s) && !utf8.RuneStart(s[lo]) {
		lo++
	}
	for hi > lo && hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi--
	}
	for lo < hi && !utf8.ValidString(s[lo:hi]) {
		lo++
	}
	for hi > lo && !utf8.ValidString(s[lo:hi]) {
		hi--
	}
	return lo, hi
}
