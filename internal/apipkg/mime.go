package apipkg

import (
	"fmt"
	"strings"
)

// wrapLines is the wrap:n filter: the text broken into lines of at most n
// characters joined by CRLF (base64 bodies in MIME messages, RFC 2045).
func wrapLines(v, arg any) (any, error) {
	n, err := toInt(arg)
	w, ok := n.(int64)
	if err != nil || !ok || w <= 0 {
		return nil, fmt.Errorf("wrap: the argument must be a positive line length, got %v", arg)
	}
	s := stringify(v)
	var b strings.Builder
	for len(s) > int(w) {
		b.WriteString(s[:w])
		b.WriteString("\r\n")
		s = s[w:]
	}
	b.WriteString(s)
	return b.String(), nil
}

// mimeParts is the mime_parts:'boundary' filter: a list of files (bin.files
// entries: file_name, mime_type, base64) → one base64 attachment part per
// file plus the closing boundary, for a multipart/mixed message whose
// header and first (text) part the template writes. An empty list is nil.
func mimeParts(v, arg any) (any, error) {
	boundary := stringify(arg)
	if boundary == "" {
		return nil, fmt.Errorf("mime_parts: needs the boundary as its argument")
	}
	files, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("mime_parts: value is not a list of files")
	}
	if len(files) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for i, f := range files {
		m, _ := f.(map[string]any)
		data := stringify(m["base64"])
		if data == "" {
			return nil, fmt.Errorf("mime_parts: attachment %d (%s): the item has no binary data there", i+1, stringify(m["property"]))
		}
		name := strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(stringify(m["file_name"]))
		if name == "" {
			name = stringify(m["property"])
		}
		ct := stringify(m["mime_type"])
		if ct == "" {
			ct = "application/octet-stream"
		}
		wrapped, _ := wrapLines(data, 76)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=\"%s\"\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n",
			boundary, ct, name, name, wrapped)
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String(), nil
}
