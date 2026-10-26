package dynorg

import (
	"path"
	"strings"
)

// The files a mission task touches are declared in its notes, in a "Files:" block:
//
//	Files:
//	- internal/foo/foo.go
//	- internal/foo/
//
// or on one line ("Files: a.go, b.go"). The block ends at the first blank line. A path
// that ends in "/" (or "/**", "/*") names a folder and everything under it. The scheduler
// reads it to keep two writers off the same files; a writing task that declares none is
// taken to touch everything, and runs alone among the writers.

// maxTaskFiles bounds what is read from one task's block.
const maxTaskFiles = 200

// TaskFiles returns the files a task declares in its notes, cleaned, without duplicates.
func TaskFiles(notes string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		p := cleanFilePath(raw)
		if p != "" && !seen[p] && len(out) < maxTaskFiles {
			seen[p] = true
			out = append(out, p)
		}
	}
	// A planned task carries its files in the closing [mission-meta] line.
	if m, ok := ParseTaskMeta(notes); ok {
		for _, f := range m.Files {
			add(f)
		}
		if len(out) > 0 {
			return out
		}
	}
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(notes, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !in {
			rest, ok := cutFold(trimmed, "files:")
			if !ok {
				continue
			}
			in = true
			for _, part := range strings.Split(rest, ",") {
				add(part)
			}
			continue
		}
		if trimmed == "" {
			in = false
			continue
		}
		for _, part := range strings.Split(strings.TrimLeft(trimmed, "-*+ \t"), ",") {
			add(part)
		}
	}
	return out
}

func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// cleanFilePath makes a declared path comparable: no quotes or backticks, slash separated,
// no leading "./" or "/", a folder ends in "/".
func cleanFilePath(raw string) string {
	p := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "`'\""))
	if p == "" {
		return ""
	}
	dir := strings.HasSuffix(p, "/") || strings.HasSuffix(p, "/**") || strings.HasSuffix(p, "/*")
	p = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(p, "/**"), "/*"), "/")
	p = path.Clean(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(strings.TrimPrefix(p, "/"), "./")
	if p == "." || p == "" {
		return "./" // the whole tree
	}
	if dir {
		p += "/"
	}
	return p
}

// filesOverlap reports whether two declared sets share a file: equal paths, or one a folder
// that holds the other. An empty set means everything, and overlaps any set.
func filesOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if pathsOverlap(x, y) {
				return true
			}
		}
	}
	return false
}

func pathsOverlap(x, y string) bool {
	if x == "./" || y == "./" || x == y {
		return true
	}
	if strings.HasSuffix(x, "/") && strings.HasPrefix(y, x) {
		return true
	}
	return strings.HasSuffix(y, "/") && strings.HasPrefix(x, y)
}
