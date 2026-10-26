package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Store reads and writes boards. Every method takes the profile id.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over a migrated database.
func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// dbx is what *sql.DB and *sql.Conn both offer, so a helper runs in or out of
// a transaction.
type dbx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) stamp() string { return s.now().UTC().Format(timeFmt) }

// tx runs fn in a BEGIN IMMEDIATE transaction on a connection of its own: the
// write lock is taken first, so processes that read and then write never
// interleave (the pattern of vault.Register). An error from fn rolls back.
func (s *Store) tx(ctx context.Context, fn func(x dbx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("tasks: connection: %w", err)
	}
	defer conn.Close()
	if err := s.beginImmediate(ctx, conn); err != nil {
		return fmt.Errorf("tasks: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	// The work is done: a context that ends now must not turn a commit that goes through into an
	// error (the caller would be told that a task which was stored failed), so COMMIT ignores ctx.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return fmt.Errorf("tasks: commit: %w", err)
	}
	committed = true
	return nil
}

// Taking the write lock waits up to the connection's busy timeout (5 s). A writer
// that holds it longer than that fails the BEGIN with "database is locked"; the
// BEGIN is retried a few times with a short backoff before the error is returned.
// Only the BEGIN is retried: nothing has been written yet, so a retry cannot repeat
// a change. Variables so that tests can shorten them.
var (
	busyRetries = 3
	busyBackoff = 100 * time.Millisecond
)

// isBusy says whether err is SQLite's "database is locked" or "table is locked" (SQLITE_BUSY).
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") || strings.Contains(m, "database table is locked")
}

// beginImmediate opens the write transaction, retrying only a busy database.
func (s *Store) beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for attempt := 0; ; attempt++ {
		_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil || !isBusy(err) || attempt >= busyRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(busyBackoff << attempt):
		}
	}
}

// snapshot runs fn in one read transaction, so what it reads agrees.
func (s *Store) snapshot(ctx context.Context, fn func(x dbx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("tasks: connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	return fn(conn)
}

// Profile returns the profile or an ErrInvalid ("unknown profile").
func (s *Store) Profile(ctx context.Context, profileID string) (Profile, error) {
	return s.profileOf(ctx, s.db, profileID)
}

func (s *Store) profileOf(ctx context.Context, x dbx, profileID string) (Profile, error) {
	var p Profile
	err := x.QueryRowContext(ctx, `SELECT id, name FROM profiles WHERE id = ?`, profileID).Scan(&p.ID, &p.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, invalid("unknown profile %q", echo(profileID))
	}
	if err != nil {
		return Profile{}, fmt.Errorf("tasks: reading the profile: %w", err)
	}
	return p, nil
}

// event writes one row of a task's history.
func (s *Store) event(ctx context.Context, x dbx, taskID int64, at, actor, kind, from, to, note string) error {
	_, err := x.ExecContext(ctx,
		`INSERT INTO task_events (task_id, at, actor, kind, from_status, to_status, note) VALUES (?,?,?,?,?,?,?)`,
		taskID, at, actor, kind, from, to, note)
	if err != nil {
		return fmt.Errorf("tasks: writing an event: %w", err)
	}
	return nil
}

// bump moves the profile's board revision; every write transaction does it once.
func (s *Store) bump(ctx context.Context, x dbx, profileID string) error {
	_, err := x.ExecContext(ctx,
		`INSERT INTO task_board_rev (profile_id, rev) VALUES (?, 1) ON CONFLICT(profile_id) DO UPDATE SET rev = rev + 1`, profileID)
	if err != nil {
		return fmt.Errorf("tasks: bumping the revision: %w", err)
	}
	return nil
}

// atTop says where a card new to a column goes: newest first where the order
// is chronological, at the end of a queue otherwise.
func atTop(st Status) bool { return st == StatusInbox || st == StatusReview || st == StatusDone }

// errPositionRange is what an end placement answers when the card at that end of a
// column is so near the limit of an int64 that one more gap would wrap round and put
// the new card at the other end. A move changes a position by one gap, so only a row
// written by hand or imported gets there; it is a plain error, not an invalid request.
var errPositionRange = errors.New("tasks: positions in this column are out of range")

// gapAbove is the position one gap above pos, which is before it in the column, and
// gapBelow the position one gap below it. Neither wraps: they refuse.
func gapAbove(pos int64) (int64, error) {
	if pos < math.MinInt64+positionGap {
		return 0, errPositionRange
	}
	return pos - positionGap, nil
}

func gapBelow(pos int64) (int64, error) {
	if pos > math.MaxInt64-positionGap {
		return 0, errPositionRange
	}
	return pos + positionGap, nil
}

// edgePosition is a position above every other card of the column (top) or
// below every one (bottom), ignoring the card except. It is refused, with
// errPositionRange, where the end card leaves no room for a gap.
//
// The column is the one of the card's own board: the personal board's, or its session's
// (a new card, except 0, goes to the personal board; a session's new card is placed by
// edgePositionIn).
func (s *Store) edgePosition(ctx context.Context, x dbx, profileID string, status Status, top bool, except int64) (int64, error) {
	var lo, hi sql.NullInt64
	err := x.QueryRowContext(ctx,
		`SELECT MIN(position), MAX(position) FROM tasks WHERE profile_id = ? AND status = ? AND id <> ?
		   AND session_id IS (SELECT session_id FROM tasks WHERE id = ?)`,
		profileID, string(status), except, except).Scan(&lo, &hi)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading a column: %w", err)
	}
	switch {
	case !lo.Valid:
		return positionGap, nil
	case top:
		return gapAbove(lo.Int64)
	}
	return gapBelow(hi.Int64)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

const taskCols = `id, profile_id, title, notes, status, position, source_kind, source_url, source_title, source_app, claimed_by, claim_until, created_at, updated_at, session_id, kind, parent_id, acceptance, assignee, suggested_executor, blocked, verified, retries`

type rowScanner interface{ Scan(dest ...any) error }

// scanTask reads a row of taskCols. A claim is stale when its task is in progress
// and its lease has run out (spec 4.2); a claim left on a task in another column
// is shown, and is not stale.
func (s *Store) scanTask(row rowScanner) (Task, error) {
	var t Task
	var status, claimedBy, until, created, updated, kind string
	var session, parent sql.NullInt64
	var blocked, verified int
	if err := row.Scan(&t.ID, &t.ProfileID, &t.Title, &t.Notes, &status, &t.Position,
		&t.Source.Kind, &t.Source.URL, &t.Source.Title, &t.Source.App, &claimedBy, &until, &created, &updated,
		&session, &kind, &parent, &t.Acceptance, &t.Assignee, &t.SuggestedExecutor, &blocked, &verified, &t.Retries); err != nil {
		return Task{}, err
	}
	if session.Valid {
		t.SessionID, t.Kind, t.ParentID = session.Int64, kind, parent.Int64
		t.Blocked, t.Verified = blocked == 1, verified == 1
	}
	t.Status = Status(status)
	var err error
	if t.CreatedAt, err = storedTime("created_at", created); err != nil {
		return Task{}, err
	}
	if t.UpdatedAt, err = storedTime("updated_at", updated); err != nil {
		return Task{}, err
	}
	if claimedBy != "" {
		u, err := storedTime("claim_until", until)
		if err != nil {
			return Task{}, err
		}
		t.Claim = &Claim{By: claimedBy, Until: u, Stale: t.Status == StatusInProgress && !u.After(s.now())}
	}
	return t, nil
}

// getTx reads one task of the profile, with its last event.
func (s *Store) getTx(ctx context.Context, x dbx, profileID string, id int64) (Task, error) {
	t, err := s.scanTask(x.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: reading #%d: %w", id, err)
	}
	ts := []Task{t}
	if err := s.attachLast(ctx, x, ts); err != nil {
		return Task{}, err
	}
	if err := s.attachAuthors(ctx, x, ts); err != nil {
		return Task{}, err
	}
	return ts[0], nil
}

// attachLast fills LastEvent of every task with one query.
func (s *Store) attachLast(ctx context.Context, x dbx, ts []Task) error {
	if len(ts) == 0 {
		return nil
	}
	index := make(map[int64]int, len(ts))
	args := make([]any, 0, len(ts))
	for i, t := range ts {
		index[t.ID] = i
		args = append(args, t.ID)
	}
	rows, err := x.QueryContext(ctx,
		`SELECT task_id, actor, kind, at FROM task_events
		 WHERE id IN (SELECT MAX(id) FROM task_events WHERE task_id IN (`+placeholders(len(ts))+`) GROUP BY task_id)`, args...)
	if err != nil {
		return fmt.Errorf("tasks: reading last events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			le LastEvent
			at string
		)
		if err := rows.Scan(&id, &le.Actor, &le.Kind, &at); err != nil {
			return fmt.Errorf("tasks: reading last events: %w", err)
		}
		if le.At, err = storedTime("time of a last event", at); err != nil {
			return fmt.Errorf("tasks: reading last events: %w", err)
		}
		ts[index[id]].LastEvent = &le
	}
	return rows.Err()
}

// echoRunes bounds how much of what a caller sent an error message repeats.
const echoRunes = 64

// echo is s cut for an error message that repeats it: a huge id or source must
// not make a huge message.
func echo(s string) string { return cutRunes(s, echoRunes) }

// storedTime reads a time this store wrote. Text that is not one means a writer
// used another format: that is an error, not the year 1, which would pass for a
// task from long ago and, in a claim, for a lease long run out.
func storedTime(what, text string) (time.Time, error) {
	t, err := time.Parse(timeFmt, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("the stored %s is not a time: %w", what, err)
	}
	return t, nil
}

// reservedNames are the labels the store writes for someone other than an agent:
// the operator's, an unnamed agent's and the capture surfaces'.
var reservedNames = []string{"you", "agent", "capture", SourceChrome, SourceOS}

// CheckAgentName refuses a name an agent may not act under: one outside the
// alphabet and the length of a name (1 to MaxNameLen characters of letters,
// digits and . _ # @ : -), and one that equals a reserved label in any case
// (a label is read by eye, and an agent that took one would write events that
// read as the operator's or a capture's). It is the rule the store itself
// applies, exported for a surface that has to judge a name before it opens the
// database: the refusal is an error that matches ErrInvalid, in the store's
// words. It is for a name that is there: an empty name is refused like any
// other outside the shape, and whether an agent may go without a name at all
// is the caller's business (an add lets an unnamed agent through, as "agent";
// a claim refuses it).
func CheckAgentName(name string) error {
	if !nameRE.MatchString(name) {
		return invalid("an agent name is 1-%d characters of letters, digits and . _ # @ : -", MaxNameLen)
	}
	for _, r := range reservedNames {
		if strings.EqualFold(name, r) {
			return invalid("an agent may not be named %q: you, agent, capture, chrome and os are reserved", name)
		}
	}
	return nil
}

// sourceKindFor decides the stored source kind. The actor decides what is
// allowed: an agent's tasks are always agent tasks (it may not call itself a
// capture, which would skip the hourly limit), a capture files the source it is
// named for, the operator is cli or app.
func sourceKindFor(actor Actor, requested string) (string, error) {
	switch actor.Kind {
	case Agent:
		if requested == "" || requested == SourceCLI || requested == SourceAgent {
			return SourceAgent, nil
		}
		return "", invalid("an agent's tasks are agent tasks: source %q is for captures", echo(requested))
	case Capture:
		// The name is what the events say about who acted, so it is a surface and
		// not free text, and it decides the source: a capture asking for another
		// one is refused. A capture with no name must say which surface it is.
		if actor.Name != "" && actor.Name != SourceChrome && actor.Name != SourceOS {
			return "", invalid("a capture is named chrome or os, not %q", echo(actor.Name))
		}
		kind := actor.Name
		switch {
		case kind == "":
			kind = requested
		case requested != "" && requested != kind:
			return "", invalid("the capture %s asked for source %q: a capture files only its own source", kind, echo(requested))
		}
		if kind != SourceChrome && kind != SourceOS {
			return "", invalid("a capture comes from chrome or os, not %q", echo(kind))
		}
		return kind, nil
	case Human:
		switch requested {
		case "", SourceCLI:
			return SourceCLI, nil
		case SourceApp:
			return SourceApp, nil
		}
		return "", invalid("source %q is for captures and agents: the operator's tasks come from cli or app", echo(requested))
	}
	return "", invalid("unknown actor")
}

// openTasksSQL counts the tasks of a profile that are on the board. It names the five
// columns where it could leave the archive out (status <> 'archived'): the archive has
// no limit, and a range that leaves one value out is read entry by entry, archive
// included, by every add and by every card that comes back from it, while five
// equalities are five seeks in the index, bounded by the 2,000 open tasks.
const openTasksSQL = `SELECT COUNT(*) FROM tasks WHERE profile_id = ? AND session_id IS NULL AND status IN ('inbox', 'ready', 'in_progress', 'review', 'done')`

// checkAddLimits refuses a task that would pass the board's size. Agents add
// as many tasks as they need: there is no hourly allowance.
func (s *Store) checkAddLimits(ctx context.Context, x dbx, profileID string) error {
	var open int
	if err := x.QueryRowContext(ctx, openTasksSQL, profileID).Scan(&open); err != nil {
		return fmt.Errorf("tasks: counting tasks: %w", err)
	}
	if open >= MaxOpenTasks {
		return limit("this profile already has %d open tasks: archive some first", MaxOpenTasks)
	}
	return nil
}

// Add creates a task in the profile's Inbox (or in Ready, for the operator who
// asks). With a ClientID it is idempotent: a second Add with the same key
// returns the task that exists and created is false.
func (s *Store) Add(ctx context.Context, profileID string, in AddInput, actor Actor) (Task, bool, error) {
	// Who is asking decides before what is asked: this gate comes first, so that a
	// request for Ready from anyone but the operator is refused as that whatever the
	// text, the title or the source say.
	if in.Ready && actor.Kind != Human {
		return Task{}, false, operatorOnly("add a task straight to Ready")
	}
	if err := bound(actor, 0); err != nil {
		return Task{}, false, err
	}
	title, notes, err := deriveTitleNotes(in.Title, in.Notes, in.Text)
	if err != nil {
		return Task{}, false, err
	}
	status := StatusInbox
	if in.Ready {
		status = StatusReady
	}
	kind, err := sourceKindFor(actor, in.SourceKind)
	if err != nil {
		return Task{}, false, err
	}
	if actor.Kind == Agent && actor.Name != "" {
		if err := CheckAgentName(actor.Name); err != nil {
			return Task{}, false, err
		}
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID != "" && !clientIDRE.MatchString(clientID) {
		return Task{}, false, invalid("a client id is 1-%d characters of letters, digits, _ and -", MaxClientIDLen)
	}
	src := Source{
		Kind:  kind,
		URL:   cleanURL(in.SourceURL),
		Title: cutRunes(oneLine(in.SourceTitle), MaxSourceTitleRunes),
		App:   cutRunes(oneLine(in.SourceApp), MaxAppRunes),
	}

	var out Task
	var created bool
	err = s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if clientID != "" {
			var existing int64
			err := x.QueryRowContext(ctx, `SELECT id FROM tasks WHERE profile_id = ? AND client_id = ?`, profileID, clientID).Scan(&existing)
			if err == nil {
				out, err = s.getTx(ctx, x, profileID, existing)
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("tasks: looking up the client id: %w", err)
			}
		}
		if err := s.checkAddLimits(ctx, x, profileID); err != nil {
			return err
		}
		pos, err := s.edgePosition(ctx, x, profileID, status, atTop(status), 0)
		if err != nil {
			return err
		}
		var cid any
		if clientID != "" {
			cid = clientID
		}
		stamp := s.stamp()
		res, err := x.ExecContext(ctx,
			`INSERT INTO tasks (profile_id, title, notes, status, position, source_kind, source_url, source_title, source_app, client_id, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			profileID, title, notes, string(status), pos, src.Kind, src.URL, src.Title, src.App, cid, stamp, stamp)
		if err != nil {
			return fmt.Errorf("tasks: adding a task: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("tasks: adding a task: %w", err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "created", "", string(status), ""); err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		out, err = s.getTx(ctx, x, profileID, id)
		created = err == nil
		return err
	})
	if err != nil {
		return Task{}, false, err
	}
	return out, created, nil
}
