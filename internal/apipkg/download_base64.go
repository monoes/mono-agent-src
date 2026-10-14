package apipkg

import (
	"encoding/base64"
	"mime"
	"path"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// paramAt reads an op param by name; a dotted name walks into a
// collection param (n8n's "options.binaryPropertyOutput").
func paramAt(params map[string]any, name string) any {
	if v, ok := params[name]; ok || !strings.Contains(name, ".") {
		return v
	}
	return lookupPath(params, strings.Split(name, "."))
}

// downloadProperty is the output binary property of a download: the
// binary.property template, else the property_field param, else "data".
func (r *opRun) downloadProperty() string {
	prop := "data"
	if t := r.op.Binary.Property; t != "" {
		if s, err := renderText(t, r.scope); err == nil && s != "" {
			prop = s
		}
	}
	if f := r.op.Binary.PropertyField; f != "" {
		if s := stringify(paramAt(r.params, f)); s != "" {
			prop = s
		}
	}
	return prop
}

// downloadBase64Item decodes a base64 file from the JSON response
// (binary.download_base64) onto the item, keeping the JSON as the item.
// With binary.download_each the response holds a list of files (OpenAI
// image generations): each element at that path becomes one item, with
// el bound to it, and its key holding the base64 text is left out.
func (r *opRun) downloadBase64Item(resp *response) ([]workflow.Item, error) {
	b := r.op.Binary
	doc := decodeBody(resp.body)
	sc := r.scope
	sc.Res = doc
	if b.DownloadEach == "" {
		obj, _ := doc.(map[string]any)
		it, err := r.base64Item(sc, obj, "")
		if err != nil {
			return nil, err
		}
		return []workflow.Item{it}, nil
	}
	var list []any
	switch v := lookupPath(doc, strings.Split(b.DownloadEach, ".")).(type) {
	case []any:
		list = v
	case nil:
	default:
		list = []any{v}
	}
	items := make([]workflow.Item, 0, len(list))
	for _, el := range list {
		esc := sc
		esc.El = el
		obj, _ := el.(map[string]any)
		it, err := r.base64Item(esc, obj, "el")
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

// base64Item renders download_base64 in sc and returns the item: obj's
// keys (a copy when from is "el", without the key holding the base64
// text) plus the file's binary_property, file_name, mime_type and size.
func (r *opRun) base64Item(sc Scope, obj map[string]any, from string) (workflow.Item, error) {
	b := r.op.Binary
	enc, err := renderText(b.DownloadBase64, sc)
	if err != nil {
		return workflow.Item{}, r.errf("binary.download_base64: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(enc), ""))
	if err != nil {
		return workflow.Item{}, r.errf("binary.download_base64: not base64: %w", err)
	}
	out := obj
	if from == "el" || out == nil {
		out = map[string]any{}
		for k, v := range obj {
			if s, ok := v.(string); ok && s == enc {
				continue
			}
			out[k] = v
		}
	}
	prop := r.downloadProperty()
	name, _ := renderText(b.FileName, sc)
	mt, _ := renderText(b.MimeType, sc)
	if mt == "" {
		mt = mime.TypeByExtension(path.Ext(name))
	}
	out["binary_property"], out["file_name"], out["mime_type"], out["size"] = prop, name, mt, len(data)
	return workflow.Item{JSON: out, Binary: map[string][]byte{prop: data}}, nil
}
