// Package capturetask turns a capture envelope into a task on a board
// (GLU-03).
//
// The board is an existing one. mono-agent has no task system of its own —
// there is no monotask client in this repo, and the org runtime's issues
// live in monomind's store at <root>/.monomind/orgs/<org>-issues.json,
// which the mastermind-issues, mastermind-my-issues and
// mastermind-liveness skills read and the dashboard renders. So that is
// where a capture's task is written, in exactly that file's shape, rather
// than inventing a second place for tasks to hide.
//
// The file belongs to monomind. Every issue already in it is preserved
// byte for byte — the document is decoded as raw JSON, the new issue is
// appended, and the result is written through a temporary file and renamed,
// so a reader never sees a half-written board and a field this build has
// never heard of is never dropped.
//
// Preserving them needs more than careful encoding: appending is a
// read-modify-write of the whole document, and monomind's dashboard and
// the mastermind skills write it too. So the write is taken under an
// exclusive lock on the board, the board is read inside that lock, and the
// rename happens only if the file has not moved since. See lock.go.
package capturetask

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// issuesSuffix is monomind's own artifact suffix for an org's issue store
// (see orgdesign's orgArtifactSuffixes, which mirrors monomind's
// ORG_ARTIFACT_SUFFIXES).
const issuesSuffix = "-issues.json"

// Priorities are the values monomind's issue store accepts.
var Priorities = []string{"low", "medium", "high", "urgent"}

// DefaultPriority is what an unprioritised capture gets.
const DefaultPriority = "medium"

// statusTodo is the status a new issue starts in. monomind renamed "open"
// to "todo"; the skills migrate the old value on load.
const statusTodo = "todo"

// Options describes the task to create.
type Options struct {
	// Root is the project root holding .monomind/orgs.
	Root string
	// Org is the board — an org name under that root.
	Org string
	// EnvelopePath is the capture directory to attach.
	EnvelopePath string
	// Title defaults to the capture's title, then its URL.
	Title string
	// Description is added above the capture's own provenance block.
	Description string
	// Priority is one of Priorities; empty means DefaultPriority.
	Priority string
	// Assignee, Workspace and Parent map onto the store's assigneeId,
	// workspaceId and parentId.
	Assignee, Workspace, Parent string
	// Now supplies timestamps; nil means time.Now.
	Now func() time.Time
}

// Attachment is one file of the capture, in the shape
// mastermind-issue-detail's `attachments` action prints.
type Attachment struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	AddedAt   string `json:"added_at"`
}

// CaptureRef is the machine-readable link back to the envelope. It is an
// extra key on the issue: monomind's readers ignore what they do not know,
// and without it "which capture is this task about?" is answerable only by
// reading the description.
type CaptureRef struct {
	Path         string   `json:"path"`
	URL          string   `json:"url,omitempty"`
	CanonicalURL string   `json:"canonicalUrl,omitempty"`
	Title        string   `json:"title,omitempty"`
	CapturedAt   string   `json:"capturedAt,omitempty"`
	ContentHash  string   `json:"contentHash,omitempty"`
	Source       string   `json:"source,omitempty"`
	Collection   string   `json:"collection,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

// Issue is one record in <org>-issues.json. The field names and the null
// defaults match what monomind's skills write, so an issue created here is
// indistinguishable from one created there.
type Issue struct {
	ID                string       `json:"id"`
	Title             string       `json:"title"`
	Description       string       `json:"description"`
	Status            string       `json:"status"`
	Priority          string       `json:"priority"`
	AssigneeID        *string      `json:"assigneeId"`
	AssigneeAgentID   *string      `json:"assigneeAgentId"`
	AssigneeUserID    *string      `json:"assigneeUserId"`
	ParentID          *string      `json:"parentId"`
	ProjectID         *string      `json:"projectId"`
	WorkspaceID       *string      `json:"workspaceId"`
	BlockedByIssueIDs []string     `json:"blockedByIssueIds"`
	CreatedAt         string       `json:"createdAt"`
	UpdatedAt         string       `json:"updatedAt"`
	Attachments       []Attachment `json:"attachments,omitempty"`
	Capture           *CaptureRef  `json:"capture,omitempty"`
}

// Result is what Create did.
type Result struct {
	Org        string `json:"org"`
	IssuesFile string `json:"issuesFile"`
	Issue      Issue  `json:"issue"`
}

// IssuesPath is the issue store for org under root.
func IssuesPath(root, org string) (string, error) {
	if !orgdesign.ValidOrgName(org) {
		return "", fmt.Errorf("invalid org name %q", org)
	}
	return filepath.Join(orgdesign.OrgsDir(root), org+issuesSuffix), nil
}

// Boards lists the orgs under root that can hold a task: every org with a
// config file, plus any that already has an issue store.
func Boards(root string) ([]string, error) {
	names, err := orgdesign.ListOrgNames(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	entries, err := os.ReadDir(orgdesign.OrgsDir(root))
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if name, ok := strings.CutSuffix(e.Name(), issuesSuffix); ok && orgdesign.ValidOrgName(name) {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Create appends a task for one capture to an org's board.
func Create(opts Options) (*Result, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if strings.TrimSpace(opts.Org) == "" {
		return nil, errors.New("capturetask: no board — pass --board <org>")
	}
	path, err := IssuesPath(opts.Root, opts.Org)
	if err != nil {
		return nil, err
	}
	priority := strings.ToLower(strings.TrimSpace(opts.Priority))
	if priority == "" {
		priority = DefaultPriority
	}
	if !contains(Priorities, priority) {
		return nil, fmt.Errorf("capturetask: priority %q must be one of %s", priority, strings.Join(Priorities, ", "))
	}

	env, err := readEnvelope(opts.EnvelopePath)
	if err != nil {
		return nil, err
	}

	// Everything from here to the rename happens under the board's lock,
	// and the board is read inside it: an issue appended to a document this
	// process read before it waited for the lock would overwrite whatever
	// the lock holder wrote (see lock.go).
	release, err := lockBoard(path)
	if err != nil {
		return nil, err
	}
	defer release()

	var issue Issue
	for attempt := 0; ; attempt++ {
		doc, issues, stamp, err := loadBoard(path)
		if err != nil {
			return nil, err
		}
		if opts.Parent != "" && !hasIssue(issues, opts.Parent) {
			return nil, fmt.Errorf("capturetask: parent issue %q is not on board %q", opts.Parent, opts.Org)
		}

		ts := now().UTC().Format("2006-01-02T15:04:05Z")
		issue = Issue{
			ID:                nextID(issues, now()),
			Title:             clampTitle(firstNonEmpty(opts.Title, env.meta.Title, env.meta.DedupeURL(), filepath.Base(env.dir))),
			Description:       describe(opts.Description, env),
			Status:            statusTodo,
			Priority:          priority,
			AssigneeID:        optional(opts.Assignee),
			ParentID:          optional(opts.Parent),
			WorkspaceID:       optional(opts.Workspace),
			BlockedByIssueIDs: []string{},
			CreatedAt:         ts,
			UpdatedAt:         ts,
			Attachments:       env.attachments(ts),
			Capture:           env.ref(),
		}

		blob, err := json.Marshal(issue)
		if err != nil {
			return nil, fmt.Errorf("encode issue: %w", err)
		}
		issues = append(issues, blob)

		err = saveBoard(path, doc, issues, stamp)
		if err == nil {
			break
		}
		// A writer that did not take the lock moved the board underneath
		// us. Read it again and rebuild on top of what they wrote — and if
		// it never settles, say so rather than discard it.
		if errors.Is(err, errBoardChanged) && attempt < saveAttempts {
			continue
		}
		return nil, err
	}
	return &Result{Org: opts.Org, IssuesFile: path, Issue: issue}, nil
}

// saveAttempts bounds the rebuild-on-conflict loop. Each attempt is one
// read and one write of a file someone else is also writing; a board that
// loses this race four times running is contended by something that is not
// about to stop.
const saveAttempts = 3

// loadBoard reads the issue store as raw JSON. Every existing issue is kept
// as the bytes it was written as: this process knows nothing about the
// fields monomind's own tooling adds, and re-encoding them through a Go
// struct is how they would be lost.
//
// It also returns the version of the file it read, which saveBoard checks
// before publishing — the document it hands back is the whole board, so
// writing it on top of a newer one deletes the difference.
func loadBoard(path string) (map[string]json.RawMessage, []json.RawMessage, boardStamp, error) {
	stamp, err := stampOf(path)
	if err != nil {
		return nil, nil, boardStamp{}, err
	}
	blob, err := capture.ReadCapped(path, capture.MaxMetaFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil, boardStamp{}, nil
	}
	if err != nil {
		return nil, nil, boardStamp{}, fmt.Errorf("read board %s: %w", path, err)
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, nil, boardStamp{}, fmt.Errorf("read board %s: %w", path, err)
	}
	var issues []json.RawMessage
	if raw, ok := doc["issues"]; ok {
		if err := json.Unmarshal(raw, &issues); err != nil {
			return nil, nil, boardStamp{}, fmt.Errorf("read board %s: issues is not a list: %w", path, err)
		}
	}
	return doc, issues, stamp, nil
}

// saveBoard writes the board through a temporary file in the same
// directory and renames it, so a crash cannot leave a truncated board.
//
// The rename is only reached if the board still looks the way loadBoard
// left it. That check is a stat, not a content hash, and the window
// between it and the rename is not zero — it cannot be, without a lock the
// other writer also takes. It catches the writer that spent milliseconds
// on the same document, which is the one that exists.
func saveBoard(path string, doc map[string]json.RawMessage, issues []json.RawMessage, since boardStamp) error {
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	list, err := json.Marshal(issues)
	if err != nil {
		return fmt.Errorf("encode issues: %w", err)
	}
	doc["issues"] = list
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode board: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-issues-*")
	if err != nil {
		return fmt.Errorf("stage board: %w", err)
	}
	staged := tmp.Name()
	defer os.Remove(staged)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write board: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush board: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write board: %w", err)
	}
	if err := os.Chmod(staged, 0o600); err != nil {
		return fmt.Errorf("secure board: %w", err)
	}
	now, err := stampOf(path)
	if err != nil {
		return err
	}
	if !now.same(since) {
		return fmt.Errorf("%w: %s", errBoardChanged, path)
	}
	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("publish board %s: %w", path, err)
	}
	return nil
}

// nextID mints an id in monomind's own form, issue-<epoch-millis>-NNN,
// stepping the counter past any id the board already holds.
//
// The counter alone is not unique — a millisecond is long enough for two
// processes — so this is only safe because the caller holds the board's
// lock and read `issues` under it: the id is unique against the board as
// it is about to be written, not against the board as it was a moment
// ago. The tail past 999 is random rather than counted, so a board that
// somehow fills a millisecond still cannot hand out a duplicate.
func nextID(issues []json.RawMessage, now time.Time) string {
	taken := issueIDs(issues)
	millis := now.UTC().UnixMilli()
	for n := 1; n < 1000; n++ {
		id := fmt.Sprintf("issue-%d-%03d", millis, n)
		if !taken[id] {
			return id
		}
	}
	for {
		id := fmt.Sprintf("issue-%d-999-%s", millis, randomTail())
		if !taken[id] {
			return id
		}
	}
}

// randomTail is 8 hex digits of entropy for an id that ran out of counter.
// A clock that cannot be read and a random source that cannot be read are
// both survivable here: the result is checked against the board either way.
func randomTail() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// issueIDs collects every id on the board once, rather than re-decoding
// every issue for every candidate.
func issueIDs(issues []json.RawMessage) map[string]bool {
	out := make(map[string]bool, len(issues))
	for _, raw := range issues {
		var probe struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.ID != "" {
			out[probe.ID] = true
		}
	}
	return out
}

func hasIssue(issues []json.RawMessage, id string) bool {
	for _, raw := range issues {
		var probe struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.ID == id {
			return true
		}
	}
	return false
}

func optional(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v := strings.TrimSpace(s)
	return &v
}

// maxTitleRunes bounds the issue title. The title is usually a page's own
// <title>, which is to say it is chosen by whatever was captured, and it
// ends up in a document monomind's dashboard renders and other agents
// read. A title this long is already unreadable; one a thousand times
// longer is a board nobody can open.
const maxTitleRunes = 300

// clampTitle keeps the front of an over-long title rather than replacing
// it: the first line of a title is the part that identifies it.
func clampTitle(s string) string {
	r := []rune(s)
	if len(r) <= maxTitleRunes {
		return s
	}
	return strings.TrimSpace(string(r[:maxTitleRunes-1])) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return "Untitled capture"
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
