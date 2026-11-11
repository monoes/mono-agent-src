package application

import (
	"context"
	"database/sql"
	"github.com/monoes/mono-agent/internal/storage"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/vault"
)

// maxPostingText bounds the captured posting text returned in `show --assets`.
const maxPostingText = 20000

// Assets is Contract B's "assets" object: everything around one application.
type Assets struct {
	Posting   AssetPosting        `json:"posting"`
	Documents []AssetDocument     `json:"documents"`
	Fill      applications.Fill   `json:"fill"`
	People    []AssetPerson       `json:"people"`
	Messages  []AssetMessage      `json:"messages"`
	Tasks     []AssetTask         `json:"tasks"`
	Task      *AssetAITask        `json:"task"`
	Links     []applications.Link `json:"links"`
}

type AssetPosting struct {
	URL          string `json:"url"`
	CapturedText string `json:"capturedText"`
	CapturedAt   string `json:"capturedAt"`
	Deadline     string `json:"deadline"`
}

type AssetDocument struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"sizeBytes"`
	CreatedAt string `json:"createdAt"`
	Attached  bool   `json:"attached"`
	Path      string `json:"path"`
}

type AssetPerson struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	Email string `json:"email"`
}

type AssetMessage struct {
	ID        string `json:"id"`
	Channel   string `json:"channel"`
	Direction string `json:"direction"`
	Subject   string `json:"subject"`
	Snippet   string `json:"snippet"`
	At        string `json:"at"`
}

type AssetTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// AssetAITask is the aitask running for the application (Contract A's task, trimmed).
type AssetAITask struct {
	TaskID         string `json:"taskId"`
	ConversationID string `json:"conversationId"`
	Status         string `json:"status"`
}

// TaskLookup finds the aitask bound to an application; nil result means none.
type TaskLookup func(ctx context.Context, db *sql.DB, profileID, applicationID string) (*AssetAITask, error)

// LookupAITask is the seam for the aitask table (core worker, migration 114).
// Nil until the integrator wires it, so `task` stays null.
var LookupAITask TaskLookup

// BuildAssets gathers the assets of one application. The application must exist.
func BuildAssets(ctx context.Context, db *sql.DB, profileID string, app *applications.Application) (Assets, error) {
	store := applications.NewStore(db)
	a := Assets{Documents: []AssetDocument{}, People: []AssetPerson{}, Messages: []AssetMessage{}, Tasks: []AssetTask{}}

	links, err := store.ListLinks(ctx, profileID, app.ID, "")
	if err != nil {
		return a, err
	}
	a.Links = links
	if a.Fill, err = store.GetFill(ctx, profileID, app.ID); err != nil {
		return a, err
	}

	refs := map[applications.LinkKind][]string{}
	for _, l := range links {
		refs[l.Kind] = append(refs[l.Kind], l.RefID)
	}

	if app.Job != nil {
		a.Posting.URL = app.Job.URL
	}
	if app.Tender != nil {
		a.Posting.URL = app.Tender.URL
		a.Posting.Deadline = app.Tender.SubmissionDeadline
	}

	docs, err := vault.ListDocuments(ctx, db, profileID)
	if err != nil {
		return a, err
	}
	linkedDocs := toSet(refs[applications.LinkDocument])
	captures := toSet(refs[applications.LinkCapture])
	for _, d := range docs {
		if captures[d.ID] && a.Posting.CapturedText == "" {
			a.Posting.CapturedText = readText(d.Path, maxPostingText)
			a.Posting.CapturedAt = d.CreatedAt
			if a.Posting.URL == "" {
				a.Posting.URL = d.URL
			}
		}
		if d.ApplicationID != app.ID && !linkedDocs[d.ID] {
			continue
		}
		a.Documents = append(a.Documents, AssetDocument{
			ID: d.ID, Filename: d.Filename, Kind: docKind(d.Filename), Source: docSource(d.Source),
			Format: docFormat(d.Filename), SizeBytes: d.SizeBytes, CreatedAt: d.CreatedAt,
			Attached: linkedDocs[d.ID], Path: d.Path,
		})
	}

	if a.People, err = queryPeople(ctx, db, profileID, refs[applications.LinkPerson]); err != nil {
		return a, err
	}
	if a.Messages, err = queryMessages(ctx, db, profileID, refs[applications.LinkMessage]); err != nil {
		return a, err
	}
	if a.Tasks, err = queryTasks(ctx, db, profileID, refs[applications.LinkTask]); err != nil {
		return a, err
	}
	if LookupAITask != nil {
		if a.Task, err = LookupAITask(ctx, db, profileID, app.ID); err != nil {
			return a, err
		}
	}
	return a, nil
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func docKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "cover"):
		return "cover_letter"
	case strings.Contains(n, "cv"), strings.Contains(n, "resume"), strings.Contains(n, "résumé"):
		return "cv"
	}
	return "other"
}

func docSource(vaultSource string) string {
	if vaultSource == "upload" {
		return "upload"
	}
	return "generated"
}

func docFormat(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return "html"
	case ".pdf":
		return "pdf"
	}
	return "other"
}

// readText returns up to max bytes of the file at path; "" when it cannot be read.
func readText(path string, max int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, max)
	n, _ := f.Read(buf)
	return strings.ToValidUTF8(string(buf[:n]), "")
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func queryIn(ctx context.Context, db *sql.DB, query string, profileID string, ids []string, scan func(*sql.Rows) error) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{profileID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, strings.Replace(query, "(?)", "("+placeholders(len(ids))+")", 1), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func queryPeople(ctx context.Context, db *sql.DB, profileID string, ids []string) ([]AssetPerson, error) {
	out := []AssetPerson{}
	err := queryIn(ctx, db,
		`SELECT id, COALESCE(NULLIF(full_name, ''), platform_username), COALESCE(job_title, ''), platform_username, COALESCE(contact_details, '')
		 FROM people WHERE profile_id = ? AND id IN (?) ORDER BY created_at, id`, profileID, ids,
		func(r *sql.Rows) error {
			var p AssetPerson
			var username, contact string
			if err := r.Scan(&p.ID, &p.Name, &p.Role, &username, &contact); err != nil {
				return err
			}
			switch {
			case strings.Contains(username, "@"):
				p.Email = username
			case strings.Contains(contact, "@") && !strings.ContainsAny(contact, "{[ "):
				p.Email = contact
			}
			out = append(out, p)
			return nil
		})
	return out, err
}

func queryMessages(ctx context.Context, db *sql.DB, profileID string, ids []string) ([]AssetMessage, error) {
	out := []AssetMessage{}
	storage.EnsureMessagesScrubbed(db, profileID)
	err := queryIn(ctx, db,
		`SELECT id, source, direction, COALESCE(subject, ''), COALESCE(body, ''), COALESCE(sent_at, created_at)
		 FROM person_messages WHERE profile_id = ? AND id IN (?) ORDER BY COALESCE(sent_at, created_at), id`, profileID, ids,
		func(r *sql.Rows) error {
			var m AssetMessage
			var dir, body string
			if err := r.Scan(&m.ID, &m.Channel, &dir, &m.Subject, &body, &m.At); err != nil {
				return err
			}
			m.Direction = "in"
			if dir == "outbound" {
				m.Direction = "out"
			}
			m.Snippet = snippet(body, 200)
			out = append(out, m)
			return nil
		})
	return out, err
}

func queryTasks(ctx context.Context, db *sql.DB, profileID string, ids []string) ([]AssetTask, error) {
	out := []AssetTask{}
	err := queryIn(ctx, db,
		`SELECT CAST(id AS TEXT), title, status FROM tasks WHERE profile_id = ? AND CAST(id AS TEXT) IN (?) ORDER BY id`, profileID, ids,
		func(r *sql.Rows) error {
			var t AssetTask
			if err := r.Scan(&t.ID, &t.Title, &t.Status); err != nil {
				return err
			}
			out = append(out, t)
			return nil
		})
	return out, err
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
