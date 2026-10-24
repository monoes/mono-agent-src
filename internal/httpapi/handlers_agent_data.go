package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/ai/chat"
	"github.com/monoes/mono-agent/internal/doctags"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/peopletags"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/taskview"
	"github.com/monoes/mono-agent/internal/vault"
)

// The profile's documents, people and task board, for an agent that has no CLI
// (agent mode, SECURITY.md). Every route is scoped to the profile the request was
// authenticated for (profileOf) and every query takes that profile's id: an id of
// another profile is "not found". The reads reuse what the CLI and the MCP tools
// read with (vault.ListDocumentsPage, chat's person tools, internal/tasks,
// internal/taskview); this file only bounds what goes in and what comes out.
//
// What a response never holds, whatever the row: a file path (a document's path and
// capture directory stay out; so does its index error, which may quote one), the query
// string of a captured page's URL, a credential column.

const (
	// dataListDefault and dataListMax are the page size of a list route. Every list
	// is cut at dataListMax, whatever the caller asks for.
	dataListDefault = 50
	dataListMax     = 200
	// dataOffsetMax is the furthest a list can be paged into.
	dataOffsetMax = 100000
	// maxTagNameRunes is the longest tag name the API takes (the store has no limit).
	maxTagNameRunes = 64
	// maxTagsPerProfile is how many tags the API lets a profile hold before it refuses
	// to make a new one: a write token cannot grow the tags table without end.
	maxTagsPerProfile = 500
	// maxQueryTags is how many tags a document list filters by.
	maxQueryTags = 5
	// maxTagBody is the biggest request body a tag route reads.
	maxTagBody = 1 << 10
	// maxSearchRunes is the longest people search term.
	maxSearchRunes = 100
	// personFieldRunes caps each text of a person; personBudgetRunes caps all of
	// them together, so that a row whose notes or extras were filled by a scraper
	// answers in a bounded body.
	personFieldRunes  = 4096
	personBudgetRunes = 64 * 1024
	// personListFieldRunes caps each text of a row of the people list.
	personListFieldRunes = 512
	// personListBudgetRunes caps a whole page of the people list.
	personListBudgetRunes = 128 * 1024
)

var (
	idRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	platformRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
)

// paramsOnly refuses a query string that holds a parameter the route does not take.
func paramsOnly(r *http.Request, allowed ...string) error {
	for k := range r.URL.Query() {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("%q is not a parameter of this route (it takes %s)", k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// intParam reads a non-negative whole number from the query: def when absent.
func intParam(r *http.Request, name string, def, max int) (int, error) {
	vals := r.URL.Query()[name]
	if len(vals) == 0 {
		return def, nil
	}
	if len(vals) > 1 {
		return 0, fmt.Errorf("%s is given more than once", name)
	}
	n, err := strconv.Atoi(vals[0])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a whole number, 0 or more", name)
	}
	return min(n, max), nil
}

// pageParams reads limit (default dataListDefault, cut at dataListMax; 0 is the default
// too, as in the MCP tools) and offset.
func pageParams(r *http.Request) (limit, offset int, err error) {
	if limit, err = intParam(r, "limit", dataListDefault, dataListMax); err != nil {
		return 0, 0, err
	}
	if limit == 0 {
		limit = dataListDefault
	}
	if offset, err = intParam(r, "offset", 0, dataOffsetMax); err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

func validID(id string) bool { return idRE.MatchString(id) }

// cleanText says whether s is valid UTF-8 without control characters (category Cc) or format
// characters (Cf: bidi overrides, zero-width characters, the byte-order mark), which let a
// name read as something it is not.
func cleanText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// validTagName is a tag name or id as the API takes it: 1 to maxTagNameRunes
// characters, no control characters.
func validTagName(name string) error {
	n := utf8.RuneCountInString(name)
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("the tag name is empty")
	case n > maxTagNameRunes:
		return fmt.Errorf("a tag name is at most %d characters", maxTagNameRunes)
	case !cleanText(name):
		return errors.New("a tag name holds no control or format characters (such as a bidi override or a zero-width space)")
	}
	return nil
}

// dataFail answers a failed read or write of the data routes. A refusal of the store
// (not found, invalid) is said; anything else is logged and answered without its text,
// which may quote a path or a statement.
func dataFail(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, peopletags.ErrNotFound), errors.Is(err, tasks.ErrNotFound), errors.Is(err, chat.ErrPersonNotFound):
		writeError(w, http.StatusNotFound, what+": not found")
	case errors.Is(err, peopletags.ErrInvalid), errors.Is(err, tasks.ErrInvalid), errors.Is(err, tasks.ErrLimit):
		writeError(w, http.StatusBadRequest, what+": "+err.Error())
	case errors.Is(err, tasks.ErrOperatorOnly):
		writeError(w, http.StatusForbidden, what+": only the operator can do that")
	case strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY"):
		// Another writer held the lock past the busy timeout: the caller can retry.
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, what+": the database is busy, retry")
	default:
		lg := logging.Zerolog()
		lg.Warn().Err(err).Str("route", what).Msg("httpapi: a data route failed")
		writeError(w, http.StatusInternalServerError, what+": failed")
	}
}

func badRequest(w http.ResponseWriter, err error) { writeError(w, http.StatusBadRequest, err.Error()) }

// ── documents ────────────────────────────────────────────────────────────

// agentDocument is a document as the API shows it: no path, no capture directory, no
// index error.
type agentDocument struct {
	ID            string        `json:"id"`
	Filename      string        `json:"filename_untrusted"`
	SizeBytes     int64         `json:"size_bytes"`
	Source        string        `json:"source"`
	ApplicationID string        `json:"application_id"`
	CreatedAt     string        `json:"created_at"`
	Indexed       bool          `json:"indexed"`
	IndexFailed   bool          `json:"index_failed"`
	Stale         bool          `json:"stale"`
	URL           string        `json:"url_untrusted"`
	Tags          []doctags.Tag `json:"tags"`
}

// pageURL is a captured page's address without the parts that carry secrets: no
// credentials, query or fragment. An address that does not parse is dropped.
func pageURL(raw string) string {
	if raw == "" || len(raw) > 2048 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

func asAgentDocument(d vault.DocumentEntry, tags []doctags.Tag) agentDocument {
	if tags == nil {
		tags = []doctags.Tag{}
	}
	return agentDocument{
		ID: d.ID, Filename: filepath.Base(d.Filename), SizeBytes: d.SizeBytes, Source: d.Source,
		ApplicationID: d.ApplicationID, CreatedAt: d.CreatedAt, Indexed: d.Indexed,
		IndexFailed: d.IndexError != "", Stale: d.Stale, URL: pageURL(d.URL), Tags: tags,
	}
}

// GET /profiles/{profile}/documents?limit=&offset=&tag=
func (s *Server) handleDocumentList(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r, "limit", "offset", "tag"); err != nil {
		badRequest(w, err)
		return
	}
	limit, offset, err := pageParams(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	filter := r.URL.Query()["tag"]
	if len(filter) > maxQueryTags {
		badRequest(w, fmt.Errorf("at most %d tag filters", maxQueryTags))
		return
	}
	for _, t := range filter {
		if err := validTagName(t); err != nil {
			badRequest(w, err)
			return
		}
	}
	ctx, profile := r.Context(), profileOf(r)
	db := s.rt.db.DB

	// One more than the page is read, to see whether the page cut anything.
	var docs []vault.DocumentEntry
	if len(filter) == 0 {
		docs, err = vault.ListDocumentsPage(ctx, db, profile, limit+1, offset)
	} else {
		docs, err = taggedDocuments(ctx, s, profile, filter, limit+1, offset)
	}
	if err != nil {
		dataFail(w, "list documents", err)
		return
	}
	truncated := len(docs) > limit
	if truncated {
		docs = docs[:limit]
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	var byDoc map[string][]doctags.Tag
	if len(ids) > 0 {
		if byDoc, err = doctags.ByDocument(ctx, db, profile, ids); err != nil {
			dataFail(w, "list documents", err)
			return
		}
	}
	out := make([]agentDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, asAgentDocument(d, byDoc[d.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": out, "limit": limit, "offset": offset, "truncated": truncated, "note": taskview.UntrustedNote})
}

// taggedDocuments are the documents that carry every tag, newest first, from offset
// on, at most n of them: the ids that match come from the store in one query and only
// the page's rows are read.
func taggedDocuments(ctx context.Context, s *Server, profile string, tags []string, n, offset int) ([]vault.DocumentEntry, error) {
	ids, err := doctags.Search(ctx, s.rt.db.DB, profile, tags) // oldest first
	if err != nil {
		return nil, err
	}
	slices.Reverse(ids)
	if offset >= len(ids) {
		return []vault.DocumentEntry{}, nil
	}
	ids = ids[offset:min(len(ids), offset+n)]
	out := make([]vault.DocumentEntry, 0, len(ids))
	for _, id := range ids {
		d, err := vault.GetDocument(ctx, s.rt.db.DB, profile, id)
		if err != nil {
			return nil, err
		}
		if d != nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

// loadDocument is the request's document of the request's profile, or nil after
// answering: 400 for an id that is no id, 404 for a document that is not this
// profile's.
func (s *Server) loadDocument(w http.ResponseWriter, r *http.Request) *vault.DocumentEntry {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusBadRequest, "the document id is not valid")
		return nil
	}
	d, err := vault.GetDocument(r.Context(), s.rt.db.DB, profileOf(r), id)
	if err != nil {
		dataFail(w, "get document", err)
		return nil
	}
	if d == nil {
		writeError(w, http.StatusNotFound, "get document: not found")
		return nil
	}
	return d
}

// GET /profiles/{profile}/documents/{id}
func (s *Server) handleDocumentGet(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	d := s.loadDocument(w, r)
	if d == nil {
		return
	}
	tags, err := doctags.List(r.Context(), s.rt.db.DB, profileOf(r), d.ID)
	if err != nil {
		dataFail(w, "get document", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		agentDocument
		Note string `json:"note"`
	}{asAgentDocument(*d, tags), taskview.UntrustedNote})
}

// GET /profiles/{profile}/documents/{id}/tags
func (s *Server) handleDocumentTagList(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	d := s.loadDocument(w, r)
	if d == nil {
		return
	}
	tags, err := doctags.List(r.Context(), s.rt.db.DB, profileOf(r), d.ID)
	if err != nil {
		dataFail(w, "list document tags", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags, "note": taskview.UntrustedNote})
}

// POST /profiles/{profile}/documents/{id}/tags   {"name": "contract"}
func (s *Server) handleDocumentTagAdd(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTagBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(&body)
	if err == nil {
		if _, more := dec.Token(); more != io.EOF {
			err = errors.New("one JSON object is expected")
		}
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is at most %d bytes", maxTagBody))
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, `the body is {"name": "<tag>"}`)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if err := validTagName(body.Name); err != nil {
		badRequest(w, err)
		return
	}
	d := s.loadDocument(w, r)
	if d == nil {
		return
	}
	ctx, profile, db := r.Context(), profileOf(r), s.rt.db.DB
	// A name the profile has no tag for makes one, not past maxTagsPerProfile (counted and
	// inserted in one transaction).
	t, err := doctags.AddLimited(ctx, db, profile, d.ID, body.Name, maxTagsPerProfile)
	if errors.Is(err, doctags.ErrTooManyTags) {
		writeError(w, http.StatusConflict, fmt.Sprintf("this profile has the most tags the API lets it make (%d): use an existing one", maxTagsPerProfile))
		return
	}
	if err != nil {
		dataFail(w, "tag document", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// DELETE /profiles/{profile}/documents/{id}/tags/{tag}   (the tag's name or id)
func (s *Server) handleDocumentTagRemove(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	tag := r.PathValue("tag")
	if err := validTagName(tag); err != nil {
		badRequest(w, err)
		return
	}
	d := s.loadDocument(w, r)
	if d == nil {
		return
	}
	if err := doctags.Remove(r.Context(), s.rt.db.DB, profileOf(r), d.ID, tag); err != nil {
		dataFail(w, "untag document", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": d.ID, "removed": tag})
}

// ── people ───────────────────────────────────────────────────────────────

// bounder cuts a decoded JSON value to a budget: each text to field runes, and all of
// them together to what is left, with a cost for every array element and object key so
// that a million small entries are cut too. Nesting deeper than maxDepth is dropped.
type bounder struct {
	field, left int
	cut         bool
}

const maxDepth = 6

func (b *bounder) value(v any, depth int) any {
	if depth > maxDepth {
		b.cut = true
		return nil
	}
	switch x := v.(type) {
	case string:
		n := min(b.field, max(b.left, 0))
		if utf8.RuneCountInString(x) <= n {
			b.left -= utf8.RuneCountInString(x)
			return x
		}
		b.cut = true
		b.left -= n
		return string([]rune(x)[:n])
	case []any:
		out := make([]any, 0, min(len(x), 64))
		for _, e := range x {
			if b.left <= 0 {
				b.cut = true
				break
			}
			b.left -= 16
			out = append(out, b.value(e, depth+1))
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, min(len(keys), 64))
		for _, k := range keys {
			if b.left <= 0 {
				b.cut = true
				break
			}
			b.left -= 16 + utf8.RuneCountInString(k)
			out[k] = b.value(x[k], depth+1)
		}
		return out
	}
	return v
}

// GET /profiles/{profile}/people?platform=&search=&limit=
// The fields of a person that someone else wrote or a platform showed: they come back under
// names ending in _untrusted, as a task's texts do, so that a model weighs them and does not
// follow instructions inside them. Ids, counts and the platform are plain.
var (
	personUntrusted = []string{"platform_username", "full_name", "image_url", "contact_details", "website", "introduction",
		"category", "job_title", "headline", "location", "about", "experience", "education", "profile_details", "follower_count"}
	personListUntrusted = []string{"platform_username", "full_name", "category", "job_title"}
)

func markUntrusted(m map[string]any, keys []string) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			delete(m, k)
			m[k+"_untrusted"] = v
		}
	}
}

func (s *Server) handlePeopleList(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r, "platform", "search", "limit"); err != nil {
		badRequest(w, err)
		return
	}
	limit, err := intParam(r, "limit", dataListDefault, dataListMax)
	if err != nil {
		badRequest(w, err)
		return
	}
	if limit == 0 {
		limit = dataListDefault
	}
	q := r.URL.Query()
	platform, search := q.Get("platform"), q.Get("search")
	if platform != "" && !platformRE.MatchString(platform) {
		writeError(w, http.StatusBadRequest, "the platform is not valid")
		return
	}
	if utf8.RuneCountInString(search) > maxSearchRunes || !cleanText(search) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("search is at most %d characters, without control characters", maxSearchRunes))
		return
	}
	// One more than the page is read, to see whether the page cut anything.
	people, err := chat.ListPeople(s.rt.db.DB, profileOf(r), platform, search, limit+1)
	if err != nil {
		dataFail(w, "list people", err)
		return
	}
	truncated := len(people) > limit
	if truncated {
		people = people[:limit]
	}
	// Bounded as decoded JSON, so that the cut is by field and by page.
	raw, _ := json.Marshal(people)
	var rows []any
	_ = json.Unmarshal(raw, &rows)
	b := &bounder{field: personListFieldRunes, left: personListBudgetRunes}
	out := b.value(rows, 0)
	if list, ok := out.([]any); ok {
		for _, row := range list {
			if m, ok := row.(map[string]any); ok {
				markUntrusted(m, personListUntrusted)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": out, "limit": limit, "truncated": truncated || b.cut, "note": taskview.UntrustedNote})
}

// GET /profiles/{profile}/people/{id}
func (s *Server) handlePersonGet(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusBadRequest, "the person id is not valid")
		return
	}
	person, err := chat.GetPerson(s.rt.db.DB, profileOf(r), id)
	if err != nil {
		dataFail(w, "get person", err)
		return
	}
	// Through JSON, so that the bounds see what the caller would.
	raw, _ := json.Marshal(person)
	_ = json.Unmarshal(raw, &person)
	b := &bounder{field: personFieldRunes, left: personBudgetRunes}
	out := b.value(person, 0).(map[string]any)
	if b.cut {
		out["truncated"] = true
	}
	markUntrusted(out, personUntrusted)
	out["note"] = taskview.UntrustedNote
	writeJSON(w, http.StatusOK, out)
}

// ── tasks ────────────────────────────────────────────────────────────────

// taskActor is who the API reads the board as: an agent, so that the board shows it
// what it shows an MCP agent (the Inbox only when the operator delegated it).
var taskActor = tasks.Actor{Kind: tasks.Agent, Name: "agent:httpapi"}

// taskBoard resolves the request's profile on the board.
func (s *Server) taskBoard(w http.ResponseWriter, r *http.Request) (*tasks.Store, tasks.Profile, bool) {
	store := tasks.NewStore(s.rt.db.DB)
	p, err := store.Profile(r.Context(), profileOf(r))
	if err != nil {
		dataFail(w, "task board", err)
		return nil, tasks.Profile{}, false
	}
	return store, p, true
}

// agentsSeeInbox says whether the operator let agents see the Inbox on the profile. It fails
// closed: a setting that cannot be read is "no".
func (s *Server) agentsSeeInbox(ctx context.Context, store *tasks.Store, profile string) bool {
	a, err := store.AgentAccess(ctx, profile)
	return err == nil && a.View
}

// GET /profiles/{profile}/tasks?status=ready,review&limit=
func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r, "status", "limit"); err != nil {
		badRequest(w, err)
		return
	}
	limit, err := intParam(r, "limit", taskview.ListDefault, taskview.ListMax)
	if err != nil {
		badRequest(w, err)
		return
	}
	if limit == 0 {
		limit = taskview.ListDefault
	}
	var statuses []tasks.Status
	if raw := r.URL.Query().Get("status"); raw != "" {
		names := strings.Split(raw, ",")
		if len(names) > 6 {
			writeError(w, http.StatusBadRequest, "at most six statuses")
			return
		}
		for _, n := range names {
			st, err := tasks.ParseStatus(n)
			if err != nil {
				writeError(w, http.StatusBadRequest, "status: inbox, ready, in_progress, review, done or archived, comma-separated")
				return
			}
			statuses = append(statuses, st)
		}
	}
	store, p, ok := s.taskBoard(w, r)
	if !ok {
		return
	}
	// The Inbox is the operator's: naming it in ?status= does not open it. Only a profile
	// whose operator let agents see it (AgentAccess.View, the switch the board itself uses)
	// lists it, whatever columns are named.
	view := s.agentsSeeInbox(r.Context(), store, p.ID)
	if len(statuses) > 0 && !view {
		statuses = slices.DeleteFunc(statuses, func(st tasks.Status) bool { return st == tasks.StatusInbox })
		if len(statuses) == 0 { // only the Inbox was asked for: an empty list, not the default one
			writeJSON(w, http.StatusOK, taskview.ListResult{Profile: p, Tasks: []taskview.View{}, Note: taskview.UntrustedNote})
			return
		}
	}
	ts, err := store.List(r.Context(), p.ID, tasks.Filter{Statuses: statuses, Limit: limit + 1}, taskActor)
	if err != nil {
		dataFail(w, "list tasks", err)
		return
	}
	truncated := len(ts) > limit
	if truncated {
		ts = ts[:limit]
	}
	writeJSON(w, http.StatusOK, taskview.ListResult{Profile: p, Tasks: taskview.ListViews(ts), Truncated: truncated, Note: taskview.UntrustedNote})
}

// GET /profiles/{profile}/tasks/{id}
func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		writeError(w, http.StatusBadRequest, "the task id is the task's number, such as 12")
		return
	}
	store, p, ok := s.taskBoard(w, r)
	if !ok {
		return
	}
	t, events, err := store.Get(r.Context(), p.ID, id)
	if err != nil {
		dataFail(w, "get task", err)
		return
	}
	if t.Status == tasks.StatusInbox && !s.agentsSeeInbox(r.Context(), store, p.ID) {
		dataFail(w, "get task", tasks.ErrNotFound) // as if there were no such task
		return
	}
	recent, omitted := taskview.RecentEvents(events)
	writeJSON(w, http.StatusOK, taskview.GetResult{Profile: p, Task: taskview.Of(t), Events: taskview.EventViews(recent),
		EventsOmitted: omitted, Note: taskview.UntrustedNote})
}

// GET /profiles/{profile}/tasks/next: the task a claim would take, without claiming it.
func (s *Server) handleTaskNext(w http.ResponseWriter, r *http.Request) {
	if err := paramsOnly(r); err != nil {
		badRequest(w, err)
		return
	}
	store, p, ok := s.taskBoard(w, r)
	if !ok {
		return
	}
	t, err := store.Next(r.Context(), p.ID, taskActor, false, 0)
	if err != nil {
		dataFail(w, "next task", err)
		return
	}
	if t != nil && t.Status == tasks.StatusInbox && !s.agentsSeeInbox(r.Context(), store, p.ID) {
		t = nil // never offered today (Next takes Ready work); kept so that a change there cannot open the Inbox
	}
	writeJSON(w, http.StatusOK, taskview.Result{Profile: p, Task: taskview.Ptr(t), Note: taskview.UntrustedNote})
}
