// Package applypark keeps filled applications parked: the agent fills an
// application's form, its browser tab stays open and bound to the
// application (application_tabs), and the person submits it. Nothing here
// ever sets an application to applied: that is `application send`, the
// person's (internal/applications.Store.SetStatus, ActorUser). A trusted
// submit seen in a parked tab only records a hint (submit_observed_at) that
// the app turns into "Looks submitted - confirm".
package applypark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/aitask"
	"net/url"
	"strings"
	"time"
)

// EnvTaskID names the aitask a browser command runs for (aitask.EnvTaskID).
const EnvTaskID = "MONOAGENT_AITASK_ID"

// State is where a tab is in its application's life.
type State string

const (
	StateParked State = "parked" // filled; the agent may not touch it
	StateActive State = "active" // one task is working in it (after a resume)
	StateClosed State = "closed" // the application is over
	StateLost   State = "lost"   // the tab is gone
)

// Tab is one application's bound tab.
type Tab struct {
	ApplicationID string `json:"applicationId"`
	TaskID        string `json:"taskId"`
	TabID         int    `json:"tabId"`
	Origin        string `json:"origin"`
	State         State  `json:"state"`
	UpdatedAt     string `json:"updatedAt"`
}

// LiveTab is a tab as `browser tabs` reports it.
type LiveTab struct {
	TabID      int    `json:"tabId"`
	GroupTitle string `json:"groupTitle"`
	URL        string `json:"url"`
}

// ErrInvalid is a tab binding that failed validation.
var ErrInvalid = errors.New("invalid tab binding")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// GroupPrefix starts the title of every agent tab group (browserctl.GroupTitle);
// the rest is the conversation's title, which the operator cannot know.
const GroupPrefix = "Coder · "

// maxTabID bounds a tab id: browsers use 32-bit ids.
const maxTabID = 1<<31 - 1

// ParseOrigin reduces rawURL (or an origin) to scheme://host[:port], lower
// case. Only http(s) with a host and no user-info qualifies.
func ParseOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", invalid("not a web address")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", invalid("not a web address")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalid("only http and https pages are bound")
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// Store is the tab bindings of one profile's applications.
type Store struct {
	DB      *sql.DB
	Profile string
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

const tabCols = `t.application_id, t.task_id, t.tab_id, t.origin, t.state, t.updated_at`

func scanTab(r interface{ Scan(...any) error }) (Tab, error) {
	var t Tab
	err := r.Scan(&t.ApplicationID, &t.TaskID, &t.TabID, &t.Origin, &t.State, &t.UpdatedAt)
	return t, err
}

// Get returns the application's binding, or nil.
func (s Store) Get(ctx context.Context, appID string) (*Tab, error) {
	t, err := scanTab(s.DB.QueryRowContext(ctx,
		`SELECT `+tabCols+` FROM application_tabs t JOIN applications a ON a.id = t.application_id
		 WHERE a.profile_id = ? AND t.application_id = ?`, s.Profile, appID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Holder returns the binding that holds tab id (parked or active), or nil. A
// Store with no Profile looks across profiles: a browser's tab ids do not
// belong to a profile, so the browser guard asks that way.
func (s Store) Holder(ctx context.Context, tabID int) (*Tab, error) {
	t, err := scanTab(s.DB.QueryRowContext(ctx,
		`SELECT `+tabCols+` FROM application_tabs t JOIN applications a ON a.id = t.application_id
		 WHERE (? = '' OR a.profile_id = ?) AND t.tab_id = ? AND t.state IN ('parked', 'active')`, s.Profile, s.Profile, tabID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Live lists the bindings whose tab is still thought open (parked or active).
func (s Store) Live(ctx context.Context) ([]Tab, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+tabCols+` FROM application_tabs t JOIN applications a ON a.id = t.application_id
		 WHERE a.profile_id = ? AND t.state IN ('parked', 'active') ORDER BY t.updated_at, t.application_id`, s.Profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tab
	for rows.Next() {
		t, err := scanTab(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Occupied counts the open tabs that still wait on the person: a live
// binding whose application is awaiting_submit or needs_you. A tab whose
// application was sent, rejected or cancelled no longer takes a slot.
func (s Store) Occupied(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM application_tabs t JOIN applications a ON a.id = t.application_id
		 WHERE a.profile_id = ? AND t.state IN ('parked', 'active') AND a.status IN ('awaiting_submit', 'needs_you')`,
		s.Profile).Scan(&n)
	return n, err
}

// BindRequest is a claim, made from an agent's result, that a task left tab
// TabID open on Origin for the application.
type BindRequest struct {
	ApplicationID string
	TaskID        string
	TabID         int
	Origin        string
	// Live is `browser tabs` as the operator sees it, read just now.
	Live []LiveTab
}

// Bind validates the claim against the browser's own tab list and records the
// tab as parked. A task may claim only a tab it opened itself (RecordOpen). Nothing from the agent is trusted: the tab must exist, sit in
// the agent's group, stand on the claimed origin, and belong to no other application. The claimed
// origin is the posting's origin or the redirect target the browser reports for
// that tab: the browser keeps no redirect chain, so the tab's own current origin
// is the record, and a claim that differs from it is refused.
func (s Store) Bind(ctx context.Context, r BindRequest) error {
	if r.TabID <= 0 || r.TabID > maxTabID {
		return invalid("tab id %d is out of range", r.TabID)
	}
	if strings.TrimSpace(r.TaskID) == "" {
		return invalid("no task")
	}
	origin, err := ParseOrigin(r.Origin)
	if err != nil {
		return err
	}
	var live *LiveTab
	for i := range r.Live {
		if r.Live[i].TabID == r.TabID {
			live = &r.Live[i]
		}
	}
	if live == nil {
		return invalid("tab %d is not open in the browser", r.TabID)
	}
	if !strings.HasPrefix(live.GroupTitle, GroupPrefix) {
		return invalid("tab %d was not opened by the agent (it is not in the agent's tab group)", r.TabID)
	}
	liveOrigin, err := ParseOrigin(live.URL)
	if err != nil {
		return invalid("tab %d is not on a web page", r.TabID)
	}
	if liveOrigin != origin {
		return invalid("tab %d is on %s, not %s", r.TabID, liveOrigin, origin)
	}
	if ok, err := s.opened(ctx, r.TaskID, r.TabID); err != nil {
		return err
	} else if !ok {
		return invalid("tab %d was not opened by task %s", r.TabID, r.TaskID)
	}
	if h, err := s.Holder(ctx, r.TabID); err != nil {
		return err
	} else if h != nil && h.ApplicationID != r.ApplicationID {
		return invalid("tab %d already belongs to another application", r.TabID)
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO application_tabs (application_id, task_id, tab_id, origin, state, updated_at)
		 SELECT id, ?, ?, ?, 'parked', ? FROM applications WHERE id = ? AND profile_id = ?
		 ON CONFLICT(application_id) DO UPDATE SET task_id = excluded.task_id, tab_id = excluded.tab_id,
		   origin = excluded.origin, state = 'parked', updated_at = excluded.updated_at`,
		r.TaskID, r.TabID, origin, now(), r.ApplicationID, s.Profile)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return invalid("application %s not found", r.ApplicationID)
	}
	return nil
}

// SetState moves the application's binding to st (and to task when given).
func (s Store) SetState(ctx context.Context, appID string, st State, task string) error {
	q := `UPDATE application_tabs SET state = ?, updated_at = ?`
	args := []any{string(st), now()}
	if task != "" {
		q += `, task_id = ?`
		args = append(args, task)
	}
	q += ` WHERE application_id = ? AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)`
	args = append(args, appID, s.Profile)
	_, err := s.DB.ExecContext(ctx, q, args...)
	return err
}

// Claim holds a parked tab for a resume under taskID, the id of the task it is about to create, and says whether
// it did: false means the tab no longer is the parked one under observed (the person, a sweep or another resume
// changed it since it was read), so nothing is written.
func (s Store) Claim(ctx context.Context, appID, observed, taskID string) (bool, error) {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE application_tabs SET state = ?, task_id = ?, updated_at = ?
		 WHERE application_id = ? AND state = ? AND COALESCE(task_id, '') = ?
		   AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)`,
		string(StateActive), taskID, now(), appID, string(StateParked), observed, s.Profile)
	return affected(res, err)
}

// Release gives a tab held under taskID back as parked (under observed again), and says whether it was still held
// under taskID; if not, whoever has it now keeps it.
func (s Store) Release(ctx context.Context, appID, taskID, observed string) (bool, error) {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE application_tabs SET state = ?, task_id = CASE WHEN ? = '' THEN task_id ELSE ? END, updated_at = ?
		 WHERE application_id = ? AND state = ? AND task_id = ?
		   AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)`,
		string(StateParked), observed, observed, now(), appID, string(StateActive), taskID, s.Profile)
	return affected(res, err)
}

// Holds says whether the tab is still the one taskID was started for: bound to it, and either active or left
// by the task itself (its fill is over and parked the tab). A tab a sweep parked before the task existed is not.
func (s Store) Holds(ctx context.Context, appID, taskID string) (bool, error) {
	t, err := s.Get(ctx, appID)
	if err != nil || t == nil || t.TaskID != taskID {
		return false, err
	}
	if t.State == StateActive {
		return true, nil
	}
	var over int
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_tasks WHERE id = ? AND profile_id = ? AND status IN (?, ?, ?)`,
		taskID, s.Profile, aitask.StatusComplete, aitask.StatusFailed, aitask.StatusCancelled).Scan(&over)
	return over > 0, err
}

func affected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DBTX is what *sql.DB and *sql.Tx share.
type DBTX interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}
