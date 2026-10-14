package apipkg

import (
	"context"
	"fmt"
	"strings"
)

// Walk (output.walk) follows a tree breadth-first and appends what it finds
// to the main result. Confluence "include descendants": the descendants
// endpoint only reaches a fixed depth, so records at that depth are
// expanded again until the tree is exhausted or Max records are kept.
//
// Start (a template or builder; res = the main response, or the list of
// results on a paginated op) gives the root id or ids.
// Request is sent once per frontier id (el = the id), paged with
// Pagination when set (results at ResultsPath). Per record (el = the
// record): ID gives its id (default el.id; empty or already seen records
// are skipped), Descend whether its id joins the next round's frontier,
// Keep whether it is collected (default: all). Max (a template) caps the
// kept records; a value of 0 or less walks nothing, empty means no cap.
// Fetch, when set, replaces the kept records by full objects loaded in
// chunks of ids.
type Walk struct {
	When        string      `json:"when,omitempty"`
	Start       any         `json:"start"`
	Request     Step        `json:"request"`
	Pagination  *Pagination `json:"pagination,omitempty"`
	ResultsPath string      `json:"results_path,omitempty"`
	ID          string      `json:"id,omitempty"`
	Descend     string      `json:"descend,omitempty"`
	Keep        string      `json:"keep,omitempty"`
	Max         string      `json:"max,omitempty"`
	Fetch       *WalkFetch  `json:"fetch,omitempty"`
}

// WalkFetch loads the walked ids in chunks: the request is sent once per
// chunk with el = the list of ids (`"id": "{{ el | csv }}"`), and the
// objects at ResultsPath are matched back by Key (a template, el = the
// object; default el.id). Output keeps the walk's order; ids the API did
// not return are dropped.
type WalkFetch struct {
	Step
	Chunk       int    `json:"chunk,omitempty"`
	ResultsPath string `json:"results_path,omitempty"`
	Key         string `json:"key,omitempty"`
}

const defaultWalkChunk = 100

// walk runs output.walk: doc is the decoded main response, v the result
// so far; it returns v followed by the walked records.
func (r *opRun) walk(ctx context.Context, doc, v any) (any, error) {
	w := r.op.Output.Walk
	sc := r.scope
	sc.Res, sc.Main = doc, doc
	if w.When != "" {
		on, err := renderString(w.When, sc)
		if err != nil {
			return nil, r.errf("output.walk.when: %w", err)
		}
		if !truthy(on) {
			return v, nil
		}
	}
	max := -1
	if w.Max != "" {
		mv, err := renderString(w.Max, sc)
		if err != nil {
			return nil, r.errf("output.walk.max: %w", err)
		}
		n, err := toInt(mv)
		if err != nil {
			return nil, r.errf("output.walk.max: %w", err)
		}
		if n != nil {
			max = int(n.(int64))
		}
	}
	sv, err := renderValue(w.Start, sc)
	if err != nil {
		return nil, r.errf("output.walk.start: %w", err)
	}
	var frontier []string
	seen := map[string]bool{}
	for _, x := range flattenOnce(sv) {
		if id := stringify(x); id != "" && !seen[id] {
			seen[id] = true
			frontier = append(frontier, id)
		}
	}
	var kept []any
	var keptIDs []string
	full := func() bool { return max >= 0 && len(kept) >= max }
	for len(frontier) > 0 && !full() {
		var next []string
		for _, node := range frontier {
			nsc := sc
			nsc.El = node
			var stepErr error
			take := func(recs []any) bool {
				for _, rec := range recs {
					esc := nsc
					esc.El = rec
					id, err := walkText(w.ID, "{{ el.id }}", esc)
					if err != nil {
						stepErr = r.errf("output.walk.id: %w", err)
						return true
					}
					if id == "" || seen[id] {
						continue
					}
					seen[id] = true
					down, err := walkFlag(w.Descend, false, esc)
					if err != nil {
						stepErr = r.errf("output.walk.descend: %w", err)
						return true
					}
					if down {
						next = append(next, id)
					}
					keep, err := walkFlag(w.Keep, true, esc)
					if err != nil {
						stepErr = r.errf("output.walk.keep: %w", err)
						return true
					}
					if keep {
						kept, keptIDs = append(kept, rec), append(keptIDs, id)
						if full() {
							return true
						}
					}
				}
				return false
			}
			if err := r.walkNode(ctx, w, nsc, take); err != nil {
				return nil, err
			}
			if stepErr != nil {
				return nil, stepErr
			}
			if full() {
				break
			}
		}
		frontier = next
	}
	if w.Fetch != nil {
		if kept, err = r.walkFetch(ctx, w.Fetch, sc, keptIDs); err != nil {
			return nil, err
		}
	}
	var out []any
	switch t := v.(type) {
	case []any:
		out = append(out, t...)
	case nil:
	default:
		out = []any{t}
	}
	return append(out, kept...), nil
}

// walkNode sends the walk request for one frontier id (all pages) and
// hands each page's records to take; take returning true stops paging.
func (r *opRun) walkNode(ctx context.Context, w *Walk, sc Scope, take func([]any) bool) error {
	run := r.stepRun(w.Request, sc)
	if w.Pagination != nil {
		pg := *w.Pagination
		if pg.ResultsPath == "" {
			pg.ResultsPath = w.ResultsPath
		}
		run.op.Pagination = &pg
		run.onPage = take
		if _, _, err := run.paginate(ctx); err != nil {
			return fmt.Errorf("output.walk %s: %w", sc.El, err)
		}
		return nil
	}
	resp, err := run.send(ctx, pageParams{})
	if err != nil {
		return fmt.Errorf("output.walk %s: %w", sc.El, err)
	}
	take(pageResults(decodeBody(resp.body), w.ResultsPath))
	return nil
}

// walkFetch loads ids in chunks and returns the objects in ids order.
func (r *opRun) walkFetch(ctx context.Context, f *WalkFetch, sc Scope, ids []string) ([]any, error) {
	chunk := f.Chunk
	if chunk <= 0 {
		chunk = defaultWalkChunk
	}
	byID := map[string]any{}
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		part := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			part = append(part, id)
		}
		csc := sc
		csc.El = part
		resp, err := r.stepRun(f.Step, csc).send(ctx, pageParams{})
		if err != nil {
			return nil, fmt.Errorf("output.walk.fetch ids %d-%d: %w", start, end-1, err)
		}
		for _, obj := range pageResults(decodeBody(resp.body), f.ResultsPath) {
			osc := sc
			osc.El = obj
			key, err := walkText(f.Key, "{{ el.id }}", osc)
			if err != nil {
				return nil, r.errf("output.walk.fetch.key: %w", err)
			}
			byID[key] = obj
		}
	}
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		if obj, ok := byID[id]; ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// stepRun is a copy of r that sends st (no pagination, before/also,
// binary or output of the op) with scope sc and fresh params, so the op's
// own returnAll/limit do not cap the step.
func (r *opRun) stepRun(st Step, sc Scope) *opRun {
	op := *r.op
	op.Method, op.Path, op.BaseURL = st.Method, st.Path, st.BaseURL
	op.Query, op.Body, op.BodyType, op.Headers = st.Query, st.Body, st.BodyType, st.Headers
	op.Pagination, op.Also, op.Before, op.Poll, op.Binary, op.Output, op.Fields = nil, nil, nil, nil, nil, nil, nil
	run := *r
	run.op, run.scope, run.params, run.onPage = &op, sc, map[string]any{}, nil
	if st.Auth != nil && !*st.Auth {
		run.credDef = nil
	}
	return &run
}

func walkText(tpl, def string, sc Scope) (string, error) {
	if tpl == "" {
		tpl = def
	}
	v, err := renderString(tpl, sc)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stringify(v)), nil
}

func walkFlag(tpl string, def bool, sc Scope) (bool, error) {
	if tpl == "" {
		return def, nil
	}
	v, err := renderString(tpl, sc)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

// check validates output.walk.
func (w *Walk) check(where string, fields, credKeys map[string]bool) []string {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, where+": "+fmt.Sprintf(format, a...)) }
	if isEmpty(w.Start) {
		add("start is required")
	}
	steps := []Step{w.Request}
	if w.Fetch != nil {
		steps = append(steps, w.Fetch.Step)
		if w.Fetch.Chunk < 0 {
			add("fetch.chunk must not be negative")
		}
	}
	for _, st := range steps {
		if (!validMethods[strings.ToUpper(st.Method)] && !hasTemplate(st.Method)) || st.Path == "" {
			add("request and fetch need a method (GET, …) and a path")
		}
		for _, v := range []any{st.BaseURL, st.Path, st.Query, st.Body} {
			probs = append(probs, checkTemplates(where, v, fields, credKeys)...)
		}
		probs = append(probs, checkHeaderTemplates(where, st.Headers, fields, credKeys)...)
	}
	if pg := w.Pagination; pg != nil && pg.Type != PageCursor && pg.Type != PageNextURL && pg.Type != PageLinkHeader &&
		pg.Type != PageOffset && pg.Type != PagePage && pg.Type != PageHeaderCursor {
		add("pagination type %q is unknown", pg.Type)
	}
	tpls := []any{w.When, w.Start, w.ID, w.Descend, w.Keep, w.Max}
	if w.Fetch != nil {
		tpls = append(tpls, w.Fetch.Key)
	}
	return append(probs, checkTemplates(where, tpls, fields, credKeys)...)
}
