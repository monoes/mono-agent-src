package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
)

// urlLookup is `monomind doc lookup --json` (UrlLookup in lookup.ts).
type urlLookup struct {
	Saved      bool     `json:"saved"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	Site       string   `json:"site"`
	CapturedAt string   `json:"capturedAt"`
	Versions   int      `json:"versions"`
	Note       string   `json:"note"`
	Tags       []string `json:"tags"`
	Collection string   `json:"collection"`
	Source     string   `json:"source"`
	FilePath   string   `json:"filePath"`
	Envelope   string   `json:"envelope"`
	Highlights int      `json:"highlights"`
}

// lookupViaCommand asks `doc lookup`. Any failure — including the command
// not existing on an older monomind, which surfaces as help text where JSON
// was expected — is an error the caller falls back from.
func lookupViaCommand(ctx context.Context, runner Runner, target, scope string) (*SavedDocument, error) {
	flags := []string{"--json", "--highlights"}
	if scope != "" {
		flags = append(flags, "--scope", scope)
	}
	out, err := runDoc(ctx, runner, "lookup", flags, target)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	var got urlLookup
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		return nil, fmt.Errorf("doc lookup did not return JSON: %s", firstLine(trimmed))
	}
	doc := &SavedDocument{
		Saved:      got.Saved,
		URL:        target,
		Title:      got.Title,
		Site:       got.Site,
		CapturedAt: got.CapturedAt,
		Versions:   got.Versions,
		Note:       got.Note,
		Tags:       got.Tags,
		Collection: got.Collection,
		Source:     got.Source,
		Path:       got.FilePath,
		Envelope:   got.Envelope,
		Highlights: got.Highlights,
	}
	if doc.Saved && doc.Versions < 1 {
		doc.Versions = 1
	}
	if doc.Envelope == "" {
		doc.Envelope = envelopeDir(doc.Path)
	}
	return doc, nil
}

// lookupViaLibrary is the compatibility path: `doc list --json` over both
// stores, matched exactly on the identity URL.
//
// The global brain is asked FIRST because that is where extension captures
// land (`doc ingest` routes paths outside the project there, and the inbox
// is under ~/.monomind). The project store is only consulted when the
// global one has nothing, so the common case stays at one exec. A capture
// scope (a profile's store) is the only store asked: the shared brain's
// pages are not that profile's.
func lookupViaLibrary(ctx context.Context, runner Runner, target, scope string) (*SavedDocument, error) {
	stores := [][]string{
		{"--global", "--json"},
		{"--json"},
	}
	if scope != "" {
		stores = [][]string{{"--scope", scope, "--json"}}
	}
	var firstErr error
	for _, flags := range stores {
		out, err := runDoc(ctx, runner, "list", flags)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rows, err := decodeLibraryRows(out)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if doc := matchLibraryRows(rows, target); doc != nil {
			return doc, nil
		}
		firstErr = nil // this store answered; it simply has no such page
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return &SavedDocument{Saved: false, URL: target}, nil
}

// matchLibraryRows picks the newest version of the row whose identity URL
// is exactly the target, or nil. Exactly: a row for
// `…/post/comments` must not answer a question about `…/post`.
func matchLibraryRows(rows []libraryRow, target string) *SavedDocument {
	var best *libraryRow
	for i := range rows {
		if !rows[i].Captured || identityURL(rows[i].URL) != target {
			continue
		}
		if best == nil || rows[i].Version > best.Version {
			best = &rows[i]
		}
	}
	if best == nil {
		return nil
	}
	versions := best.Version
	if versions < 1 {
		versions = 1 // a record predating version tracking is still one save
	}
	return &SavedDocument{
		Saved:      true,
		URL:        target,
		Title:      best.Title,
		Site:       best.Site,
		CapturedAt: best.CapturedAt,
		Versions:   versions,
		// NOTE deliberately absent: `doc list`'s row shape (libraryEntry
		// in library.ts) does not carry one. It arrives as soon as the
		// monomind side grows `doc lookup`.
		Tags:       best.Tags,
		Collection: best.Collection,
		Source:     best.Source,
		Path:       best.FilePath,
		Envelope:   envelopeDir(best.FilePath),
	}
}

// isUnavailable reports whether err is the "monomind is not installed"
// signal, which no fallback can get past.
func isUnavailable(err error) bool {
	var re *RequestError
	return asRequestError(err, &re) && re.Code == CodeUnavailable
}

// decodeLibraryRows tolerates the CLI printing either the bare array or an
// object wrapping one — the shape has moved before and a badge is not worth
// breaking over it.
func decodeLibraryRows(out []byte) ([]libraryRow, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	var rows []libraryRow
	if err := json.Unmarshal([]byte(trimmed), &rows); err == nil {
		return rows, nil
	}
	var wrapped struct {
		Documents []libraryRow `json:"documents"`
		Entries   []libraryRow `json:"entries"`
		Results   []libraryRow `json:"results"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err != nil {
		return nil, fmt.Errorf("doc list did not return JSON: %s", firstLine(trimmed))
	}
	for _, set := range [][]libraryRow{wrapped.Documents, wrapped.Entries, wrapped.Results} {
		if len(set) > 0 {
			return set, nil
		}
	}
	return nil, nil
}

// identityURL is the Go half of captureIdentityUrl: drop the fragment,
// keep everything else. Query strings and trailing slashes genuinely
// distinguish pages on real sites, so nothing else is rewritten.
func identityURL(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// isCapturableURL reports whether a URL names a page the capture pipeline
// could ever have saved.
func isCapturableURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// envelopeDir is the capture directory a `…/readable.md` lives in — the
// thing "open the archived copy" actually opens. Empty when the indexed
// file is not inside an envelope-shaped path.
func envelopeDir(filePath string) string {
	if filePath == "" {
		return ""
	}
	sep := strings.LastIndexAny(filePath, `/\`)
	if sep <= 0 {
		return ""
	}
	return filePath[:sep]
}

// captureScopeOf is the monomind scope of the profile a request names
// (`profile`, the one the side panel is "Saving into"): that profile's own
// capture store, so a question asked while saving into work is answered
// from work's captures only. No profile keeps the shared brain, exactly as
// before profiles existed. An id profiledir would not accept is refused
// rather than dropped: answering from the shared brain instead would show
// pages from outside the profile the person asked about.
func captureScopeOf(req *Request) (string, error) {
	profile := strings.TrimSpace(req.String("profile"))
	if profile == "" {
		return "", nil
	}
	scope := monomind.CaptureScope(profile)
	if scope == "" {
		return "", fmt.Errorf("unusable profile id %q", firstLine(profile))
	}
	return scope, nil
}
