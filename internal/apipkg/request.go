package apipkg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"sort"
	"strings"
)

// Body types.
const (
	BodyJSON      = "json"
	BodyForm      = "form"
	BodyMultipart = "multipart"
	BodyRaw       = "raw"
	BodyNone      = "none"
	// BodyNDJSON: the body renders to a list, one JSON line per element
	// (an element that is a list gives one line per its elements), with a
	// final newline; Content-Type application/x-ndjson (Elasticsearch _bulk).
	BodyNDJSON = "ndjson"
)

// effectiveBodyType is body_type, defaulting to json when a body is set, raw
// for a binary upload without a body, else none.
func (o *Operation) effectiveBodyType() string {
	if o.BodyType != "" {
		return o.BodyType
	}
	if o.Body != nil {
		return BodyJSON
	}
	if o.Binary != nil && o.Binary.UploadField != "" {
		return BodyRaw
	}
	return BodyNone
}

// pageParams are the pagination params one page request adds.
type pageParams struct {
	values  map[string]any
	inBody  bool
	inPath  bool              // values go to the path template as page.<param>
	headers map[string]string // header_cursor pagination
	url     string            // absolute next-page URL (link_header, next_url)
}

// built is a request ready to send, with the body bytes SigV4 needs.
type built struct {
	req  *http.Request
	body []byte
}

// buildRequest builds op's request for one item.
func (r *opRun) buildRequest(ctx context.Context, pp pageParams) (*built, error) {
	if pp.inPath {
		r.scope.Page = pp.values
	}
	sc := r.scope
	rawURL := pp.url
	if rawURL == "" {
		base := r.op.BaseURL
		if base == "" {
			base = r.pkg.BaseURL
		}
		b, err := renderText(base, sc)
		if err != nil {
			return nil, r.errf("base_url: %w", err)
		}
		path, err := renderPath(r.op.Path, sc)
		if err != nil {
			return nil, r.errf("path: %w", err)
		}
		if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
			rawURL = path
		} else {
			rawURL = strings.TrimRight(b, "/") + path
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, r.errf("url %q: %w", rawURL, err)
	}
	if pp.url == "" && r.op.Query != nil {
		qv, err := renderObject(r.op.Query, sc)
		if err != nil {
			return nil, r.errf("query: %w", err)
		}
		q := u.Query()
		r.addValues(q, qv)
		u.RawQuery = q.Encode()
	}
	if len(pp.values) > 0 && !pp.inBody && !pp.inPath {
		q := u.Query()
		for k, v := range pp.values {
			q.Set(k, stringify(v))
		}
		u.RawQuery = q.Encode()
	}

	body, contentType, err := r.buildBody(pp)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	method := strings.ToUpper(r.op.Method)
	if hasTemplate(r.op.Method) {
		m, err := renderText(r.op.Method, r.scope)
		if err != nil {
			return nil, r.errf("method: %w", err)
		}
		if method = strings.ToUpper(strings.TrimSpace(m)); !validMethods[method] {
			return nil, r.errf("method %q is not GET, POST, PUT, PATCH, DELETE, HEAD or a WebDAV method", m)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, r.errf("request: %w", err)
	}
	for _, hs := range []map[string]string{r.pkg.Headers, r.op.Headers} {
		if err := applyHeaders(req.Header, hs, sc); err != nil {
			return nil, r.errf("%w", err)
		}
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	if req.Header.Get("Accept") == "" && !r.downloads() {
		req.Header.Set("Accept", "application/json")
	}
	for k, v := range pp.headers {
		req.Header.Set(k, v)
	}
	if err := injectAuth(req, r.credDef, sc, body); err != nil {
		return nil, r.errf("%w", err)
	}
	return &built{req: req, body: body}, nil
}

// buildBody renders the body for the op's body type.
func (r *opRun) buildBody(pp pageParams) ([]byte, string, error) {
	sc := r.scope
	bt := r.bodyType()
	var obj map[string]any
	if bt == BodyJSON || bt == BodyForm || bt == BodyMultipart {
		v, err := renderValue(r.op.Body, sc)
		if err != nil {
			return nil, "", r.errf("body: %w", err)
		}
		if m, ok := v.(map[string]any); ok {
			obj = m
		} else if v != nil && bt != BodyJSON {
			return nil, "", r.errf("body: a %s body must be an object", bt)
		}
		if c := r.credDef; c != nil && len(c.Inject.Body) > 0 {
			if obj == nil {
				obj = map[string]any{}
			}
			for k, tv := range c.Inject.Body {
				v, err := renderString(tv, sc) // typed: a single expression keeps bools/numbers
				if err != nil {
					return nil, "", r.errf("credential %s: inject.body.%s: %w", c.Platform, k, err)
				}
				if !isEmpty(v) {
					obj[k] = v
				}
			}
		}
		if pp.inBody && len(pp.values) > 0 {
			if obj == nil {
				obj = map[string]any{}
			}
			for k, val := range pp.values {
				setPath(obj, k, val) // "variables.after" nests (GraphQL)
			}
		}
		if bt == BodyJSON {
			var out any = obj
			if obj == nil {
				out = v
			}
			if out == nil {
				out = map[string]any{}
			}
			b, err := json.Marshal(out)
			if err != nil {
				return nil, "", r.errf("body: %w", err)
			}
			return b, "application/json", nil
		}
	}
	switch bt {
	case BodyNone:
		return nil, "", nil
	case BodyNDJSON:
		v, err := renderValue(r.op.Body, sc)
		if err != nil {
			return nil, "", r.errf("body: %w", err)
		}
		b, err := ndjsonLines(v)
		if err != nil {
			return nil, "", r.errf("body: %w", err)
		}
		return b, "application/x-ndjson", nil
	case BodyForm:
		q := url.Values{}
		r.addValues(q, obj)
		return []byte(q.Encode()), "application/x-www-form-urlencoded", nil
	case BodyMultipart:
		return r.multipartBody(obj)
	case BodyRaw:
		if r.uploads() {
			data, err := r.uploadData()
			if err != nil {
				return nil, "", err
			}
			ct, _ := renderText(r.op.Binary.MimeType, sc)
			if ct == "" {
				ct = r.uploadMime()
			}
			return data, ct, nil
		}
		v, err := renderValue(r.op.Body, sc)
		if err != nil {
			return nil, "", r.errf("body: %w", err)
		}
		if v == nil {
			return nil, "", nil
		}
		return []byte(stringify(v)), "text/plain; charset=utf-8", nil
	}
	return nil, "", r.errf("unknown body_type %q", bt)
}

func (r *opRun) multipartBody(obj map[string]any) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := []any{obj[k]}
		if arr, ok := obj[k].([]any); ok {
			vals = arr
		}
		for _, v := range vals {
			if err := w.WriteField(k, stringify(v)); err != nil {
				return nil, "", r.errf("body: %w", err)
			}
		}
	}
	if b := r.op.Binary; r.uploads() && b.Properties != "" {
		for i, prop := range r.uploadProperties() {
			one := *r
			one.prop = prop
			one.scope.El = map[string]any{"property": prop, "index": i}
			if err := one.writeFilePart(w, b); err != nil {
				return nil, "", err
			}
		}
	} else if b := r.op.Binary; r.uploads() {
		if err := r.writeFilePart(w, b); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", r.errf("body: %w", err)
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// uploadProperty is the input item's binary property to upload.
func (r *opRun) uploadProperty() string {
	if r.prop != "" {
		return r.prop
	}
	if t := r.op.Binary.Property; t != "" {
		if p, err := renderText(t, r.scope); err == nil && p != "" {
			return p
		}
	}
	prop := stringify(paramAt(r.params, r.op.Binary.UploadField))
	if prop == "" {
		prop = "data"
	}
	return prop
}

func (r *opRun) uploadName() string {
	prop := r.uploadProperty()
	if m, ok := r.item.JSON["binary"].(map[string]any); ok {
		if meta, ok := m[prop].(map[string]any); ok {
			if n, _ := meta["fileName"].(string); n != "" {
				return n
			}
		}
	}
	if n, _ := r.item.JSON["file_name"].(string); n != "" {
		return n
	}
	return prop
}

// uploadData finds the bytes to upload: the item's binary property, else a
// base64 string at item.json[prop] or item.json.binary[prop].data (n8n).
func (r *opRun) uploadData() ([]byte, error) {
	if t := r.op.Binary.UploadText; t != "" {
		txt, err := renderText(t, r.scope)
		if err != nil {
			return nil, r.errf("binary.upload_text: %w", err)
		}
		if txt != "" {
			return []byte(txt), nil
		}
	}
	prop := r.uploadProperty()
	if b, ok := r.item.Binary[prop]; ok {
		return b, nil
	}
	if s, ok := r.item.JSON[prop].(string); ok && s != "" {
		if b, err := base64.StdEncoding.DecodeString(s); err == nil {
			return b, nil
		}
	}
	if m, ok := r.item.JSON["binary"].(map[string]any); ok {
		if meta, ok := m[prop].(map[string]any); ok {
			if s, _ := meta["data"].(string); s != "" {
				if b, err := base64.StdEncoding.DecodeString(s); err == nil {
					return b, nil
				}
			}
		}
	}
	return nil, r.errf("field %q: the input item has no binary property %q to upload", r.op.Binary.UploadField, prop)
}

// renderPath renders a path template, URL-escaping each expression value.
func renderPath(s string, sc Scope) (string, error) {
	if !hasTemplate(s) {
		return s, nil
	}
	t, err := parseTemplate(s)
	if err != nil {
		return "", err
	}
	// A path that is one expression yielding an absolute URL (an upload
	// URL from a before step, hdr.Location) is used as is, unescaped.
	if t.single {
		if v, err := t.parts[0].expr.eval(sc); err == nil {
			if u := stringify(v); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				return u, nil
			}
		}
	}
	v, err := t.render(sc, url.PathEscape)
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// addValues adds rendered values to q: arrays repeat the key, objects are
// JSON, everything else is stringified.
func addValues(q url.Values, obj map[string]any) {
	for k, v := range obj {
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				q.Add(k, stringify(el))
			}
			continue
		}
		q.Set(k, stringify(v))
	}
}

// uploadMime is the upload's MIME type: the binary's own (n8n mimeType or
// mono's mime_type field), else guessed from the file name, else
// application/octet-stream.
func (r *opRun) uploadMime() string {
	if t := r.op.Binary.UploadText; t != "" {
		if txt, _ := renderText(t, r.scope); txt != "" {
			return "text/plain"
		}
	}
	prop := r.uploadProperty()
	if m, ok := r.item.JSON["binary"].(map[string]any); ok {
		if meta, ok := m[prop].(map[string]any); ok {
			if t, _ := meta["mimeType"].(string); t != "" {
				return t
			}
		}
	}
	if t, _ := r.item.JSON["mime_type"].(string); t != "" {
		return t
	}
	if t := mime.TypeByExtension(path.Ext(r.uploadName())); t != "" {
		return t
	}
	return "application/octet-stream"
}

// binMeta describes the upload for the bin template root.
func (r *opRun) binMeta() map[string]any {
	name := r.uploadName()
	m := map[string]any{
		"file_name": name, "mime_type": r.uploadMime(),
		"file_extension": strings.TrimPrefix(path.Ext(name), "."),
	}
	// base64 for JSON-body uploads (GitHub/GitLab file content); a missing
	// binary leaves it unset so the error comes from the op that needs it.
	if data, err := r.uploadData(); err == nil {
		m["base64"] = base64.StdEncoding.EncodeToString(data)
		m["file_size"] = len(data)
	}
	if r.prop == "" && r.op.Binary.Properties != "" {
		var files []any
		for _, prop := range r.uploadProperties() {
			one := *r
			one.prop = prop
			f := one.binMeta()
			f["property"] = prop
			files = append(files, f)
		}
		m["files"] = files
	}
	return m
}

// uploads reports whether this run uploads the item's binary.
func (r *opRun) uploads() bool {
	b := r.op.Binary
	if b == nil || b.UploadField == "" {
		return false
	}
	if b.UploadWhen == "" {
		return true
	}
	w, err := renderString(b.UploadWhen, r.scope)
	return err == nil && truthy(w)
}

// bodyType is the op's body type for this run: without an upload, an op
// that only uploads sends no body and a multipart op sends JSON.
func (r *opRun) bodyType() string {
	bt := r.op.effectiveBodyType()
	if r.op.Binary == nil || r.op.Binary.UploadWhen == "" || r.uploads() {
		return bt
	}
	if fb := r.op.Binary.FallbackBodyType; fb != "" {
		return fb
	}
	if bt == BodyForm || bt == BodyJSON {
		return bt // an explicit json/form body stays that type
	}
	if r.op.Body != nil {
		return BodyJSON
	}
	return BodyNone
}

// addValues encodes obj into q in the op's query style: "brackets"
// flattens nested objects and lists PHP-style (a[b][0][c]=v, Magento),
// otherwise lists repeat the key and objects are sent as JSON.
func (r *opRun) addValues(q url.Values, obj map[string]any) {
	style := r.op.QueryStyle
	if style == "" {
		style = r.pkg.QueryStyle
	}
	if style != "brackets" {
		addValues(q, obj)
		return
	}
	for k, v := range obj {
		addBrackets(q, k, v)
	}
}

func addBrackets(q url.Values, key string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			addBrackets(q, key+"["+k+"]", x)
		}
	case []any:
		for i, x := range t {
			addBrackets(q, fmt.Sprintf("%s[%d]", key, i), x)
		}
	default:
		if !isEmpty(v) {
			q.Set(key, stringify(v))
		}
	}
}

// setPath sets obj[a][b]… = val for the dot path "a.b…", creating
// intermediate objects (a non-object in the way is replaced).
func setPath(obj map[string]any, path string, val any) {
	parts := strings.Split(path, ".")
	cur := obj
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = val
}

// uploadProperties renders binary.properties into property names.
func (r *opRun) uploadProperties() []string {
	v, err := renderString(r.op.Binary.Properties, r.scope)
	if err != nil {
		return nil
	}
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if s := strings.TrimSpace(stringify(x)); s != "" {
				out = append(out, s)
			}
		}
	default:
		for _, s := range strings.Split(stringify(v), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// writeFilePart writes the current upload property as one multipart part.
func (r *opRun) writeFilePart(w *multipart.Writer, b *Binary) error {
	data, err := r.uploadData()
	if err != nil {
		return err
	}
	field, _ := renderText(b.MultipartField, r.scope)
	if field == "" {
		field = "file"
	}
	name, _ := renderText(b.FileName, r.scope)
	if name == "" {
		name = r.uploadName()
	}
	ct, _ := renderText(b.MimeType, r.scope)
	if ct == "" {
		ct = r.uploadMime()
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, name))
	h.Set("Content-Type", ct)
	part, err := w.CreatePart(h)
	if err != nil {
		return r.errf("body: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return r.errf("body: %w", err)
	}
	return nil
}

// ndjsonLines encodes a list as newline-delimited JSON: one line per
// element, a nested list giving one line per its elements (a $map that
// emits an action line and a document line per item).
func ndjsonLines(v any) ([]byte, error) {
	list, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil, fmt.Errorf("an ndjson body must render to a list")
		}
		list = []any{v}
	}
	var buf bytes.Buffer
	for _, el := range list {
		lines := []any{el}
		if inner, ok := el.([]any); ok {
			lines = inner
		}
		for _, l := range lines {
			b, err := json.Marshal(l)
			if err != nil {
				return nil, err
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}
