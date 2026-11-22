// Package peoplereview is the review queue for people staged for outreach:
// a workflow saves a lead with category "pending_approval" and a drafted
// introduction; a person approves (optionally sending it through a
// dispatch workflow) or rejects it. `monoagentcli people review` is the
// surface; the GUI's Human in Loop page calls that command.
package peoplereview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Category values the queue moves people between.
const (
	Pending  = "pending_approval"
	Approved = "approved"
	Rejected = "rejected"
)

// SendWorkflowName is the naming convention for the dispatch workflow when
// none is named explicitly: the profile's one workflow whose name contains
// it.
const SendWorkflowName = "Send Approved DMs"

// ErrNotFound reports a person or workflow that isn't in the profile.
var ErrNotFound = errors.New("not found")

// ErrNotPending reports an approval of a person who is not awaiting approval
// (never queued, already approved, or rejected).
var ErrNotPending = errors.New("not pending approval")

// ErrAmbiguous reports more than one workflow matching the send choice.
var ErrAmbiguous = errors.New("ambiguous")

// Person is one lead in the queue.
type Person struct {
	ID               string `json:"id"`
	Platform         string `json:"platform"`
	PlatformUsername string `json:"platform_username"`
	FullName         string `json:"full_name"`
	ImageURL         string `json:"image_url"`
	ProfileURL       string `json:"profile_url"`
	JobTitle         string `json:"job_title"`
	Category         string `json:"category"`
	Introduction     string `json:"introduction"`
	CreatedAt        string `json:"created_at"`
}

// Workflow names the dispatch workflow chosen for sending.
type Workflow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ListPending returns the profile's people awaiting review, newest first.
func ListPending(ctx context.Context, db *sql.DB, profileID string) ([]Person, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(platform,''), COALESCE(platform_username,''), COALESCE(full_name,''),
		       COALESCE(image_url,''), COALESCE(profile_url,''), COALESCE(job_title,''),
		       COALESCE(category,''), COALESCE(introduction,''), COALESCE(created_at,'')
		FROM people
		WHERE profile_id = ? AND category = ?
		ORDER BY created_at DESC`, profileID, Pending)
	if err != nil {
		return nil, fmt.Errorf("listing people to review: %w", err)
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Platform, &p.PlatformUsername, &p.FullName, &p.ImageURL,
			&p.ProfileURL, &p.JobTitle, &p.Category, &p.Introduction, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("reading people to review: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one person of the profile.
func Get(ctx context.Context, db *sql.DB, profileID, personID string) (Person, error) {
	var p Person
	err := db.QueryRowContext(ctx, `
		SELECT id, COALESCE(platform,''), COALESCE(platform_username,''), COALESCE(full_name,''),
		       COALESCE(image_url,''), COALESCE(profile_url,''), COALESCE(job_title,''),
		       COALESCE(category,''), COALESCE(introduction,''), COALESCE(created_at,'')
		FROM people WHERE id = ? AND profile_id = ?`, personID, profileID).
		Scan(&p.ID, &p.Platform, &p.PlatformUsername, &p.FullName, &p.ImageURL,
			&p.ProfileURL, &p.JobTitle, &p.Category, &p.Introduction, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("person %q: %w", personID, ErrNotFound)
	}
	return p, err
}

// Approve marks a person approved, replacing the introduction when intro is
// not empty, and returns the updated person.
func Approve(ctx context.Context, db *sql.DB, profileID, personID, intro string) (Person, error) {
	p, err := Get(ctx, db, profileID, personID)
	if err != nil {
		return p, err
	}
	if p.Category != Pending {
		return p, fmt.Errorf("person %q is %q: %w", personID, p.Category, ErrNotPending)
	}
	if strings.TrimSpace(intro) != "" {
		p.Introduction = intro
	}
	// Guarded on the pending state so a concurrent or repeated approval
	// matches no row; an empty intro keeps the stored text instead of writing
	// back what was read earlier.
	res, err := db.ExecContext(ctx,
		`UPDATE people SET introduction = COALESCE(NULLIF(?, ''), introduction), category = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND profile_id = ? AND category = ?`,
		strings.TrimSpace(intro), Approved, personID, profileID, Pending)
	if err != nil {
		return p, fmt.Errorf("approving %s: %w", personID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p, fmt.Errorf("person %q: %w", personID, ErrNotPending)
	}
	p.Category = Approved
	return p, nil
}

// Reject marks a person rejected so they leave the queue.
func Reject(ctx context.Context, db *sql.DB, profileID, personID string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE people SET category = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ?`,
		Rejected, personID, profileID)
	if err != nil {
		return fmt.Errorf("rejecting %s: %w", personID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("person %q: %w", personID, ErrNotFound)
	}
	return nil
}

// PickSendWorkflow chooses the dispatch workflow from the profile's
// workflows: want (an id, or part of a name) when given, else the one
// workflow whose name contains SendWorkflowName. None, or more than one, is
// an error that says how to choose. Workflows live in files and SQLite, so
// the caller lists them (the hybrid store) rather than this package
// querying one table.
func PickSendWorkflow(workflows []Workflow, want string) (Workflow, error) {
	want = strings.TrimSpace(want)
	if want != "" {
		for _, w := range workflows {
			if w.ID == want {
				return w, nil
			}
		}
		return matchOne(workflows, want, fmt.Sprintf("matches --send-workflow %q", want))
	}
	return matchOne(workflows, SendWorkflowName, fmt.Sprintf("is named like %q", SendWorkflowName))
}

func matchOne(workflows []Workflow, name, what string) (Workflow, error) {
	var found []Workflow
	for _, w := range workflows {
		if strings.Contains(strings.ToLower(w.Name), strings.ToLower(name)) {
			found = append(found, w)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Workflow{}, fmt.Errorf("no workflow in this profile %s — create one, or name it with --send-workflow <id|name>: %w", what, ErrNotFound)
	default:
		sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
		names := make([]string, len(found))
		for i, w := range found {
			names[i] = fmt.Sprintf("%s (%s)", w.Name, w.ID)
		}
		return Workflow{}, fmt.Errorf("%d workflows in this profile %s: %s — name one with --send-workflow <id>: %w",
			len(found), what, strings.Join(names, ", "), ErrAmbiguous)
	}
}

// ErrNotOperator is a write by someone other than the user (an agent, the
// chat, a workflow's AI run) that would approve a person for outreach.
// Approving lets the person's drafted DM go out, so only the user approves.
var ErrNotOperator = errors.New("only the user approves a person for outreach")

// ApprovalRefusal is ErrNotOperator for a write by who, saying what to do
// instead.
func ApprovalRefusal(who string) error {
	return fmt.Errorf("%w (category %q), not %s: save them as %q for the user's review; the user approves them "+
		"with `monoagentcli people review approve <id>` in their own terminal or on the Human in Loop page",
		ErrNotOperator, Approved, who, Pending)
}

// IsApproved reports whether a category value means approved, in any case
// and with any surrounding space (a dispatch workflow may match it loosely).
func IsApproved(category string) bool {
	return strings.EqualFold(strings.TrimSpace(category), Approved)
}

// Stored is a person's review state before a write: whether the row
// exists, its category and its introduction (the drafted DM).
type Stored struct {
	Found        bool
	Category     string
	Introduction string
}

// rowQueryer is a *sql.DB or a *sql.Tx.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// StoredPerson reads the review state of the person platform/username of
// profileID; Found is false when there is none.
func StoredPerson(ctx context.Context, q rowQueryer, profileID, platform, username string) (Stored, error) {
	var s Stored
	// The platform is compared without case: people import, workflow nodes and
	// the chat spell it differently, and the guard must find the approved row
	// whichever spelling this write uses. The username is compared exactly,
	// as the unique key does.
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(category,''), COALESCE(introduction,'') FROM people
		 WHERE platform_username = ? AND UPPER(platform) = UPPER(?) AND profile_id = ?
		 ORDER BY (platform = ?) DESC, updated_at DESC LIMIT 1`,
		username, platform, profileID, platform).Scan(&s.Category, &s.Introduction)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Stored{}, nil
	case err != nil:
		return Stored{}, fmt.Errorf("reading %s/%s: %w", platform, username, err)
	}
	s.Found = true
	return s, nil
}

// NotOperatorCategory is the category a write by someone other than the
// user may store for one person. stored is the row before the write;
// category and intro are what the row holds after it, and set says whether
// the write named the category itself rather than leaving the stored one.
//
// An approval is the user's, and it covers the text they approved. A write
// that names category approved is refused (ApprovalRefusal) unless the
// person already was approved with the same introduction. A write that
// leaves an approved person's category alone but changes the introduction
// puts them back in the queue (Pending), so the new text is reviewed before
// anything sends it. Anything else, queueing and rejecting included, is
// stored as written.
func NotOperatorCategory(stored Stored, category string, set bool, intro string, who string) (string, error) {
	approvedBefore := stored.Found && IsApproved(stored.Category)
	changed := stored.Found && intro != stored.Introduction
	if set && IsApproved(category) && (!approvedBefore || changed) {
		return "", ApprovalRefusal(who)
	}
	if approvedBefore && changed && !set {
		return Pending, nil
	}
	return category, nil
}

// SendInput is the trigger input a dispatch workflow receives for a person.
func SendInput(p Person) map[string]interface{} {
	return map[string]interface{}{
		"person_id":         p.ID,
		"platform":          p.Platform,
		"platform_username": p.PlatformUsername,
		"introduction":      p.Introduction,
	}
}
