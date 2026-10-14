package apipkg

import (
	"context"
	"fmt"
	"mime"
	"path"

	"github.com/monoes/mono-agent/internal/workflow"
)

// DownloadStep (output.download) fetches files for each output element
// and puts them on that element's item as binary data: Jira attachment
// content, NocoDB attachment fields, Notion file properties.
//
// When renders per element (el = the element); false skips it. Without
// Files the step is sent once with el = the element; with Files (a
// template or builder giving a list, rendered with el = the element) it is
// sent once per entry with el = the entry, up = the element and idx = its
// position. Property, FileName and MimeType are templates in the same
// scope; Property defaults to "data", MimeType to the response's
// Content-Type and FileName to its Content-Disposition name.
type DownloadStep struct {
	Step
	Files    any    `json:"files,omitempty"`
	Property string `json:"property,omitempty"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// elemBinaryKey holds an element's downloaded files while the output is
// shaped; toItems moves them onto the item.
const elemBinaryKey = "\x00binary"

// elemFiles are the files downloaded for one output element.
type elemFiles struct {
	data map[string][]byte
	meta map[string]any
}

// downloadFiles runs output.download for every element of list.
func (r *opRun) downloadFiles(ctx context.Context, list []any) error {
	d := r.op.Output.Download
	for i, el := range list {
		obj, ok := el.(map[string]any)
		if !ok {
			continue
		}
		sc := r.scope
		sc.El = el
		if d.When != "" {
			w, err := renderString(d.When, sc)
			if err != nil {
				return r.errf("output.download.when: %w", err)
			}
			if !truthy(w) {
				continue
			}
		}
		files := []any{el}
		scopes := []Scope{sc}
		if d.Files != nil {
			fv, err := renderValue(d.Files, sc)
			if err != nil {
				return r.errf("output.download.files: %w", err)
			}
			files, scopes = nil, nil
			for _, f := range flattenOnce(fv) {
				fsc := sc
				fsc.El, fsc.Up, fsc.Idx = f, el, len(files)
				files = append(files, f)
				scopes = append(scopes, fsc)
			}
		}
		for j := range files {
			if err := r.downloadOne(ctx, obj, scopes[j]); err != nil {
				return fmt.Errorf("output.download element %d file %d: %w", i, j, err)
			}
		}
	}
	return nil
}

// downloadOne sends the download request in sc and stores the file on obj.
func (r *opRun) downloadOne(ctx context.Context, obj map[string]any, sc Scope) error {
	d := r.op.Output.Download
	st := d.Step
	st.When, st.ForEach, st.Upload, st.download = "", "", false, true
	resp, err := r.sendStep(ctx, st, sc)
	if err != nil {
		return err
	}
	prop, err := renderText(d.Property, sc)
	if err != nil {
		return r.errf("output.download.property: %w", err)
	}
	if prop == "" {
		prop = "data"
	}
	name, err := renderText(d.FileName, sc)
	if err != nil {
		return r.errf("output.download.file_name: %w", err)
	}
	if name == "" {
		if _, params, err := mime.ParseMediaType(resp.header.Get("Content-Disposition")); err == nil {
			name = params["filename"]
		}
	}
	mt, err := renderText(d.MimeType, sc)
	if err != nil {
		return r.errf("output.download.mime_type: %w", err)
	}
	if mt == "" {
		mt = resp.header.Get("Content-Type")
	}
	if mt == "" && name != "" {
		mt = mime.TypeByExtension(path.Ext(name))
	}
	ef, _ := obj[elemBinaryKey].(*elemFiles)
	if ef == nil {
		ef = &elemFiles{data: map[string][]byte{}, meta: map[string]any{}}
		obj[elemBinaryKey] = ef
	}
	ef.data[prop] = resp.body
	meta := map[string]any{"fileSize": len(resp.body)}
	if name != "" {
		meta["fileName"] = name
	}
	if mt != "" {
		meta["mimeType"] = mt
	}
	ef.meta[prop] = meta
	return nil
}

// flattenOnce turns a list (whose list elements are spread one level, as a
// $map of $maps gives) into a flat list; nil → empty; a non-list → [v].
func flattenOnce(v any) []any {
	l, ok := v.([]any)
	if !ok {
		if isEmpty(v) {
			return nil
		}
		return []any{v}
	}
	var out []any
	for _, x := range l {
		if inner, ok := x.([]any); ok {
			out = append(out, inner...)
			continue
		}
		if !isEmpty(x) {
			out = append(out, x)
		}
	}
	return out
}

// carryFiles keeps an element's downloaded files on its reshaped form
// (output.to builds a new object).
func carryFiles(from, to any) {
	src, ok := from.(map[string]any)
	if !ok {
		return
	}
	ef, ok := src[elemBinaryKey].(*elemFiles)
	if !ok {
		return
	}
	if dst, ok := to.(map[string]any); ok {
		dst[elemBinaryKey] = ef
	}
}

// takeFiles moves an element's downloaded files onto its item: the bytes
// into item.Binary and n8n-style {fileName, mimeType, fileSize} under
// json.binary.<property>, which uploads in later nodes read.
func takeFiles(it *workflow.Item) {
	ef, ok := it.JSON[elemBinaryKey].(*elemFiles)
	if !ok {
		return
	}
	delete(it.JSON, elemBinaryKey)
	if it.Binary == nil {
		it.Binary = map[string][]byte{}
	}
	bm, _ := it.JSON["binary"].(map[string]any)
	if bm == nil {
		bm = map[string]any{}
	}
	for prop, data := range ef.data {
		it.Binary[prop] = data
		bm[prop] = ef.meta[prop]
	}
	it.JSON["binary"] = bm
}
