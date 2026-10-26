// Package profilectx describes the active profile to an agent: its name,
// folder, connected accounts, and what its tasks, documents, people,
// workflows, orgs and vault hold, as one prompt block (Block) or as JSON
// (Build, `monoagentcli profile context --json`).
//
// It is a map, not a copy: counts and a few recent names, so an agent knows
// what exists and which command to ask for the rest. It never carries a
// secret: accounts are platform + handle, vault entries are names only, and
// no query here selects a cookie, token, ciphertext or note.
package profilectx

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// DefaultRecent is how many recent items each section lists.
const DefaultRecent = 5

// Profile identifies the profile.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Account is a connected social account: platform and handle only.
type Account struct {
	Platform string `json:"platform"`
	Handle   string `json:"handle"`
}

// Section is a count and a few recent item names (or titles).
type Section struct {
	Count  int      `json:"count"`
	Recent []string `json:"recent"`
	// ByStatus is filled for tasks.
	ByStatus map[string]int `json:"by_status,omitempty"`
}

// Extension is the browser extension bridge's state.
type Extension struct {
	Status   string `json:"status"` // connected | waiting | unpaired | offline
	Browsers int    `json:"browsers"`
}

// Context is everything Block renders.
type Context struct {
	Profile   Profile   `json:"profile"`
	Folder    string    `json:"folder"`
	Accounts  []Account `json:"accounts"`
	Tasks     Section   `json:"tasks"`
	Documents Section   `json:"documents"`
	People    Section   `json:"people"`
	Workflows Section   `json:"workflows"`
	Orgs      Section   `json:"orgs"`
	Vault     Section   `json:"vault"` // names only
	Extension Extension `json:"extension"`
}

// Options tunes Build. The zero value lists DefaultRecent items per section
// and probes the local extension bridge.
type Options struct {
	Recent int
	// Extension reports the bridge state; nil probes the default local
	// addresses (honoring MONOAGENT_EXTENSION_PORT).
	Extension func() Extension
}

// Build reads the profile's context from db. A section whose query fails is
// left empty; only an unknown profile is an error.
func Build(ctx context.Context, db *sql.DB, profileID string, opts Options) (*Context, error) {
	if db == nil {
		return nil, fmt.Errorf("profilectx: no database")
	}
	if opts.Recent <= 0 {
		opts.Recent = DefaultRecent
	}
	c := &Context{Profile: Profile{ID: profileID}, Folder: profiledir.Root(db, profileID)}
	if err := db.QueryRowContext(ctx, `SELECT name FROM profiles WHERE id = ?`, profileID).Scan(&c.Profile.Name); err != nil {
		return nil, fmt.Errorf("profilectx: profile %q: %w", profileID, err)
	}
	n := opts.Recent

	c.Accounts = accounts(ctx, db, profileID)
	c.Tasks = tasksSection(ctx, db, profileID, n)
	c.Documents = section(ctx, db, `SELECT COUNT(*) FROM vault_documents WHERE profile_id = ?`,
		`SELECT filename FROM vault_documents WHERE profile_id = ? ORDER BY created_at DESC, seq DESC LIMIT ?`, profileID, n)
	c.People = section(ctx, db, `SELECT COUNT(*) FROM people WHERE profile_id = ?`,
		`SELECT COALESCE(NULLIF(full_name, ''), platform_username) || ' (' || platform || ')' FROM people WHERE profile_id = ? ORDER BY updated_at DESC LIMIT ?`, profileID, n)
	c.Workflows = section(ctx, db, `SELECT COUNT(*) FROM workflows WHERE profile_id = ?`,
		`SELECT name FROM workflows WHERE profile_id = ? ORDER BY updated_at DESC LIMIT ?`, profileID, n)
	c.Vault = section(ctx, db, `SELECT COUNT(*) FROM vault_secrets WHERE profile_id = ?`,
		`SELECT name FROM vault_secrets WHERE profile_id = ? ORDER BY updated_at DESC LIMIT ?`, profileID, n)
	c.Orgs = orgsSection(c.Folder, n)

	probe := opts.Extension
	if probe == nil {
		probe = probeExtension
	}
	c.Extension = probe()
	return c, nil
}

// Block builds the profile's context and renders it for a system prompt.
// It is best-effort and never fails: an unknown profile or a broken
// database gives "", so callers can append the result unconditionally.
//
// This is the entry point for prompts, including workers' (dynorg).
func Block(ctx context.Context, db *sql.DB, profileID string) string {
	c, err := Build(ctx, db, profileID, Options{})
	if err != nil {
		return ""
	}
	return c.Block()
}

func section(ctx context.Context, db *sql.DB, countQ, listQ, profileID string, n int) Section {
	s := Section{Recent: []string{}}
	_ = db.QueryRowContext(ctx, countQ, profileID).Scan(&s.Count)
	rows, err := db.QueryContext(ctx, listQ, profileID, n)
	if err != nil {
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var v sql.NullString
		if rows.Scan(&v) == nil && v.String != "" {
			s.Recent = append(s.Recent, v.String)
		}
	}
	return s
}

func tasksSection(ctx context.Context, db *sql.DB, profileID string, n int) Section {
	s := Section{Recent: []string{}, ByStatus: map[string]int{}}
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM tasks WHERE profile_id = ? AND session_id IS NULL GROUP BY status`, profileID)
	if err != nil {
		return s
	}
	for rows.Next() {
		var st string
		var cnt int
		if rows.Scan(&st, &cnt) == nil {
			s.ByStatus[st] = cnt
			s.Count += cnt
		}
	}
	rows.Close()
	// Recent lists open work only; finished tasks are counted.
	open, err := db.QueryContext(ctx, `SELECT title, status FROM tasks
		WHERE profile_id = ? AND session_id IS NULL AND status IN ('inbox','ready','in_progress','review')
		ORDER BY updated_at DESC, id DESC LIMIT ?`, profileID, n)
	if err != nil {
		return s
	}
	defer open.Close()
	for open.Next() {
		var title, status string
		if open.Scan(&title, &status) == nil {
			s.Recent = append(s.Recent, title+" ["+status+"]")
		}
	}
	return s
}

func accounts(ctx context.Context, db *sql.DB, profileID string) []Account {
	out := []Account{}
	rows, err := db.QueryContext(ctx, `SELECT platform, username FROM crawler_sessions WHERE profile_id = ? ORDER BY platform, username`, profileID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a Account
		if rows.Scan(&a.Platform, &a.Handle) == nil {
			out = append(out, a)
		}
	}
	return out
}

// orgsSection lists the org definitions in <folder>/.monomind/orgs/. Dotfiles
// there (.mcp.json) are tool config, not orgs.
func orgsSection(folder string, n int) Section {
	s := Section{Recent: []string{}}
	entries, err := os.ReadDir(filepath.Join(folder, ".monomind", "orgs"))
	if err != nil {
		return s
	}
	type org struct {
		name string
		mod  time.Time
	}
	var orgs []org
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		o := org{name: strings.TrimSuffix(name, ".json")}
		if info, err := e.Info(); err == nil {
			o.mod = info.ModTime()
		}
		orgs = append(orgs, o)
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].mod.After(orgs[j].mod) })
	s.Count = len(orgs)
	for i := 0; i < len(orgs) && i < n; i++ {
		s.Recent = append(s.Recent, orgs[i].name)
	}
	return s
}

// probeExtension asks the local bridge addresses in parallel and reports
// the first that answers.
func probeExtension() Extension {
	addrs := []string{"http://127.0.0.1:" + extension.DefaultExtensionPort, "http://127.0.0.1:" + extension.FallbackExtensionPort}
	if p := strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)); p != "" {
		addrs = []string{"http://127.0.0.1:" + p}
	}
	found := make([]*Extension, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			if st, err := extension.FetchStatusTimeout(addr, 600*time.Millisecond); err == nil {
				found[i] = &Extension{Status: st.Status, Browsers: st.Browsers}
			}
		}(i, addr)
	}
	wg.Wait()
	for _, e := range found {
		if e != nil {
			return *e
		}
	}
	return Extension{Status: "offline"}
}

// Block renders c as a system-prompt section.
func (c *Context) Block() string {
	var b strings.Builder
	b.WriteString("## Profile\n\n")
	fmt.Fprintf(&b, "You work for the mono-agent profile %q (id %s). Its folder is %s. ", c.Profile.Name, c.Profile.ID, c.Folder)
	b.WriteString("Everything below belongs to this profile only; other profiles' data is not yours to read or change.\n\n")
	if len(c.Accounts) == 0 {
		b.WriteString("- Connected accounts: none\n")
	} else {
		parts := make([]string, len(c.Accounts))
		for i, a := range c.Accounts {
			parts[i] = a.Platform + " @" + strings.TrimPrefix(a.Handle, "@")
		}
		b.WriteString("- Connected accounts: " + strings.Join(parts, ", ") + "\n")
	}
	line(&b, "Tasks", c.Tasks, "`monoagentcli task`")
	line(&b, "Documents", c.Documents, "`monoagentcli profile documents`")
	line(&b, "People", c.People, "`monoagentcli people`")
	line(&b, "Workflows", c.Workflows, "`monoagentcli workflow list`")
	line(&b, "Orgs", c.Orgs, "`monoagentcli org list`")
	line(&b, "Vault items (names only in this summary; check `monoagentcli secret agent-access` for use/reveal permissions)", c.Vault, "`monoagentcli vault`")
	fmt.Fprintf(&b, "- Browser extension: %s", c.Extension.Status)
	if c.Extension.Browsers > 0 {
		fmt.Fprintf(&b, " (%d browser(s))", c.Extension.Browsers)
	}
	b.WriteString("\n")
	return b.String()
}

func line(b *strings.Builder, label string, s Section, cmd string) {
	fmt.Fprintf(b, "- %s: %d", label, s.Count)
	if len(s.ByStatus) > 0 {
		keys := make([]string, 0, len(s.ByStatus))
		for k := range s.ByStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%d %s", s.ByStatus[k], k)
		}
		b.WriteString(" (" + strings.Join(parts, ", ") + ")")
	}
	if len(s.Recent) > 0 {
		b.WriteString("; recent: " + strings.Join(s.Recent, "; "))
	}
	if s.Count > 0 {
		b.WriteString(" - ask " + cmd)
	}
	b.WriteString("\n")
}
