package apipkg

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultPageSize = 100
	defaultMaxPages = 1000
	defaultLimit    = 50
)

// limits reads n8n's returnAll / limit params for a paginated op. Without a
// returnAll field the op returns everything.
func (r *opRun) limits() (returnAll bool, limit int) {
	pg := r.op.Pagination
	raField := pg.ReturnAllField
	if raField == "" {
		raField = "returnAll"
	}
	lField := pg.LimitField
	if lField == "" {
		lField = "limit"
	}
	declared := false
	var limitDefault any
	for _, f := range r.op.Fields {
		switch f.Key {
		case raField:
			declared = true
		case lField:
			limitDefault = f.Default
		}
	}
	returnAll = !declared
	if b, err := toBool(r.params[raField]); err == nil && b != nil {
		returnAll = b.(bool)
	}
	if returnAll {
		return true, 0
	}
	limit = defaultLimit
	if n, err := toInt(limitDefault); err == nil && n != nil && n.(int64) > 0 {
		limit = int(n.(int64))
	}
	if n, err := toInt(r.params[lField]); err == nil && n != nil && n.(int64) > 0 {
		limit = int(n.(int64))
	}
	return false, limit
}

// paginate fetches pages until the source runs out, the limit is reached
// or max_pages stops it (truncated).
func (r *opRun) paginate(ctx context.Context) ([]any, bool, error) {
	pgv := *r.op.Pagination
	pg := &pgv
	for _, f := range []*string{&pg.CursorParam, &pg.OffsetParam, &pg.LimitParam, &pg.PageParam} {
		if hasTemplate(*f) {
			v, err := renderText(*f, r.scope)
			if err != nil {
				return nil, false, r.errf("pagination param: %w", err)
			}
			*f = v
		}
	}
	returnAll, limit := r.limits()
	pageSize := pg.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if !returnAll && limit < pageSize {
		pageSize = limit
	}
	maxPages := pg.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	resultsPath := pg.ResultsPath
	if resultsPath == "" && r.op.Output != nil {
		resultsPath = r.outputPath()
	}
	bt := r.bodyType()
	inBody := pg.paramInBody() && (bt == BodyJSON || bt == BodyForm || bt == BodyMultipart)
	inPath := pg.ParamIn == "path"

	var (
		all       []any
		cursor    string
		rawCursor any // the cursor as the API returned it (an object for DynamoDB)
		offset    int
		page      = 1
		next      string
	)
	if pg.StartPage != nil {
		page = *pg.StartPage
	}
	for n := 0; n < maxPages; n++ {
		pp := pageParams{values: map[string]any{}, inBody: inBody, inPath: inPath, url: next}
		if next == "" {
			if pg.LimitParam != "" {
				pp.values[pg.LimitParam] = pageSize
			}
			switch pg.Type {
			case PageCursor:
				if cursor != "" {
					if inBody {
						pp.values[pg.CursorParam] = rawCursor // objects go back unchanged
					} else {
						pp.values[pg.CursorParam] = cursor
					}
				}
			case PageHeaderCursor:
				if cursor != "" {
					pp.headers = map[string]string{pg.CursorParam: cursor}
				}
			case PageOffset:
				pp.values[pg.OffsetParam] = offset
			case PagePage:
				pp.values[pg.PageParam] = page
			}
		}
		resp, err := r.send(ctx, pp)
		if err != nil {
			return nil, false, err
		}
		doc := decodeBody(resp.body)
		if r.scope.Res == nil {
			r.scope.Res = doc // output shaping reads headers etc. from the first page
		}
		results := pageResults(doc, resultsPath)
		if pg.Values && len(results) == 1 {
			if m, ok := results[0].(map[string]any); ok {
				results = objectValues(m)
			}
		}
		all = append(all, results...)
		if r.onPage != nil && r.onPage(results) {
			return all, false, nil
		}
		if !returnAll && len(all) >= limit {
			return all[:limit], false, nil
		}

		more := false
		switch pg.Type {
		case PageCursor:
			raw := lookupPath(doc, splitPath(pg.CursorPath))
			c := cursorText(raw)
			rawCursor = raw
			more = c != "" && c != cursor && len(results) > 0
			if pg.HasMorePath != "" {
				if b, err := toBool(lookupPath(doc, splitPath(pg.HasMorePath))); err != nil || b == nil || !b.(bool) {
					more = false
				}
			}
			cursor = c
		case PageHeaderCursor:
			c := resp.header.Get(pg.CursorPath)
			more = c != "" && c != cursor && len(results) > 0
			cursor = c
		case PageOffset:
			more = len(results) > 0 && (pg.LimitParam == "" || len(results) >= pageSize)
			offset += len(results)
		case PagePage:
			more = len(results) > 0 && (pg.LimitParam == "" || len(results) >= pageSize)
			page++
		case PageLinkHeader:
			next = resolveNext(resp.url, linkNext(resp.header.Get("Link")))
			more = next != ""
		case PageNextURL:
			rel := stringify(lookupPath(doc, splitPath(pg.NextURLPath)))
			if pg.NextURLPrefix != "" && strings.HasPrefix(rel, "/") {
				prefix, err := renderText(pg.NextURLPrefix, r.scope)
				if err != nil {
					return nil, false, r.errf("pagination.next_url_prefix: %w", err)
				}
				rel = strings.TrimRight(prefix, "/") + rel
			}
			next = resolveNext(resp.url, rel)
			more = next != ""
		}
		if !more {
			return all, false, nil
		}
	}
	return all, true, nil
}

// paramInBody: page params go in the JSON body when "param_in" says so.
func (pg *Pagination) paramInBody() bool { return pg.ParamIn == "body" }

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

// pageResults is the array at path (a lone object counts as one result).
func pageResults(doc any, path string) []any {
	v := lookupPath(doc, splitPath(path))
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		return []any{t}
	}
}

// linkNext extracts the rel="next" URL of an RFC 8288 Link header.
func linkNext(h string) string {
	for _, part := range strings.Split(h, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		target := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		for _, p := range segs[1:] {
			p = strings.TrimSpace(p)
			if strings.EqualFold(p, `rel="next"`) || strings.EqualFold(p, "rel=next") {
				return target
			}
		}
	}
	return ""
}

// resolveNext resolves a possibly relative next URL against the current one.
func resolveNext(current, next string) string {
	if next == "" {
		return ""
	}
	base, err := url.Parse(current)
	if err != nil {
		return next
	}
	ref, err := url.Parse(next)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// objectValues lists an object's values: numeric keys in numeric order,
// other keys sorted.
func objectValues(m map[string]any) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, ea := strconv.ParseFloat(keys[i], 64)
		b, eb := strconv.ParseFloat(keys[j], 64)
		if ea == nil && eb == nil {
			return a < b
		}
		return keys[i] < keys[j]
	})
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

// cursorText is a cursor's identity for the stop/repeat checks: strings as
// is, objects and lists as their JSON.
func cursorText(v any) string {
	switch v.(type) {
	case map[string]any, []any:
		if isEmpty(v) {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	return stringify(v)
}
