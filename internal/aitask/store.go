package aitask

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Task statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusNeedsUser = "needs_user"
	StatusComplete  = "complete"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	// StatusReserved is a caller-named id held while its submit is still being made: not terminal, not a task a
	// person sees (List skips it), and never startable.
	StatusReserved = "reserved"
)

// ReservationTTL is how long a reservation may stay unfinished before Store.ReapReservations fails it (its submit
// is taken to have died).
const ReservationTTL = 5 * time.Minute

// Terminal reports whether a task in status s is over for good.
func Terminal(s string) bool {
	return s == StatusComplete || s == StatusFailed || s == StatusCancelled
}

// Task is Contract A's task JSON.
type Task struct {
	ID             string          `json:"taskId"`
	Kind           string          `json:"kind"`
	ConversationID string          `json:"conversationId"`
	Status         string          `json:"status"`
	Goal           string          `json:"goal"`
	CreatedAt      string          `json:"createdAt"`
	UpdatedAt      string          `json:"updatedAt"`
	ApplicationID  string          `json:"applicationId,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`

	// Not in the JSON the callers read.
	Profile    string `json:"-"`
	Access     string `json:"-"`
	Runtime    string `json:"-"`
	Unattended bool   `json:"-"`
	Prompt     string `json:"-"`
	// Owner is the private attempt token of the caller that made the task (see SubmitInput.Owner).
	Owner string `json:"-"`
	// What `start` checks against the task as it was at submit.
	Cwd         string `json:"-"`
	ContextHash string `json:"-"`
	CapsHash    string `json:"-"`
	// BudgetUSD is the spend cap; 0 is no spend, never unlimited.
	BudgetUSD float64 `json:"-"`
}

// Store is the ai_tasks table of one profile.
type Store struct {
	DB      *sql.DB
	Profile string
	Now     func() time.Time
}

func (s *Store) now() string {
	t := time.Now
	if s.Now != nil {
		t = s.Now
	}
	return t().UTC().Format(time.RFC3339Nano)
}

const taskColumns = `id, profile_id, kind, conversation_id, status, goal, application_id, access, runtime,
	unattended, prompt, budget_usd, cwd, context_hash, caps_hash, result_json, error, owner, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanTask(r scanner) (Task, error) {
	var t Task
	var unattended int
	var result string
	err := r.Scan(&t.ID, &t.Profile, &t.Kind, &t.ConversationID, &t.Status, &t.Goal, &t.ApplicationID, &t.Access,
		&t.Runtime, &unattended, &t.Prompt, &t.BudgetUSD, &t.Cwd, &t.ContextHash, &t.CapsHash, &result, &t.Error, &t.Owner, &t.CreatedAt, &t.UpdatedAt)
	t.Unattended = unattended != 0
	if result != "" {
		t.Result = json.RawMessage(result)
	}
	return t, err
}

// NewID is a fresh task id.
// ValidID says whether id has the shape NewID makes.
func ValidID(id string) bool {
	h, ok := strings.CutPrefix(id, "at-")
	if !ok || len(h) != 16 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && h == strings.ToLower(h)
}

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "at-" + hex.EncodeToString(b)
}

// Insert stores t for the store's profile.
func (s *Store) Insert(t Task) (Task, error) { return s.insert(t) }

func (s *Store) insert(t Task) (Task, error) {
	now := s.now()
	t.Profile, t.CreatedAt, t.UpdatedAt = s.Profile, now, now
	un := 0
	if t.Unattended {
		un = 1
	}
	_, err := s.DB.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id,
		access, runtime, unattended, prompt, budget_usd, cwd, context_hash, caps_hash, error, owner, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Profile, t.Kind, t.ConversationID, t.Status, t.Goal, t.ApplicationID, t.Access, t.Runtime, un,
		t.Prompt, t.BudgetUSD, t.Cwd, t.ContextHash, t.CapsHash, t.Error, t.Owner, now, now)
	return t, err
}

// Reserve takes the caller-named id t.ID before anything else is made for it: the table key is the gate, so of two
// submits that name one id only one gets past here. The row is StatusReserved (never startable, not listed, not a
// finished task to anything that judges tasks) until Fill queues it; Abandon fails it with the reason. A taken id
// is refused with CodeInvalidInput. A reservation older than ReservationTTL is failed first (its submit died).
func (s *Store) Reserve(t Task) error {
	if err := s.ReapReservations(); err != nil {
		return err
	}
	t.Status = StatusReserved
	if _, err := s.insert(t); err != nil {
		if taken, terr := s.IDTaken(t.ID); terr == nil && taken {
			return errf(CodeInvalidInput, "--task-id %s is already a task", t.ID)
		}
		return err
	}
	return nil
}

// ReapReservations fails the profile's reservations that have been open longer than ReservationTTL.
func (s *Store) ReapReservations() error {
	rows, err := s.DB.Query(`SELECT id, updated_at FROM ai_tasks WHERE profile_id = ? AND status = ?`, s.Profile, StatusReserved)
	if err != nil {
		return err
	}
	var stale []string
	cutoff := time.Now().Add(-ReservationTTL)
	if s.Now != nil {
		cutoff = s.Now().Add(-ReservationTTL)
	}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return err
		}
		if when, perr := time.Parse(time.RFC3339Nano, at); perr != nil || when.Before(cutoff) {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := s.move(id, []string{StatusReserved}, StatusFailed, map[string]any{"error": "the submit never finished"}); err != nil {
			return err
		}
	}
	return nil
}

// Fill completes the row Reserve made: the whole task, queued. It holds only while the row is still reserved.
func (s *Store) Fill(t Task) (Task, error) {
	un := 0
	if t.Unattended {
		un = 1
	}
	t.Profile, t.UpdatedAt = s.Profile, s.now()
	res, err := s.DB.Exec(`UPDATE ai_tasks SET kind = ?, conversation_id = ?, status = ?, goal = ?, application_id = ?,
		access = ?, runtime = ?, unattended = ?, prompt = ?, budget_usd = ?, cwd = ?, context_hash = ?, caps_hash = ?,
		error = '', updated_at = ? WHERE id = ? AND profile_id = ? AND status = ?`,
		t.Kind, t.ConversationID, StatusQueued, t.Goal, t.ApplicationID, t.Access, t.Runtime, un, t.Prompt, t.BudgetUSD,
		t.Cwd, t.ContextHash, t.CapsHash, t.UpdatedAt, t.ID, s.Profile, StatusReserved)
	if err != nil {
		return t, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return t, errf(CodeTaskClosed, "task %s was reserved but the reservation is gone", t.ID)
	}
	return s.Get(t.ID)
}

// Abandon fails the row Reserve made, with the reason the submit did not finish.
func (s *Store) Abandon(id, reason string) {
	_, _ = s.move(id, []string{StatusReserved}, StatusFailed, map[string]any{"error": clip(reason)})
}

// CancelQueuedOwned cancels task id only while it is still queued and was made by owner for application: one
// statement, so a task that started in the meantime is not touched. It says whether it cancelled.
func (s *Store) CancelQueuedOwned(id, owner, applicationID string) (bool, error) {
	if owner == "" {
		return false, nil
	}
	res, err := s.DB.Exec(`UPDATE ai_tasks SET status = ?, updated_at = ? WHERE id = ? AND profile_id = ? AND status = ?
		AND owner = ? AND application_id = ?`, StatusCancelled, s.now(), id, s.Profile, StatusQueued, owner, applicationID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// IDTaken says whether any profile has a task with this id (the id is the table's key).
func (s *Store) IDTaken(id string) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM ai_tasks WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// Get returns the profile's task id.
func (s *Store) Get(id string) (Task, error) {
	t, err := scanTask(s.DB.QueryRow(`SELECT `+taskColumns+` FROM ai_tasks WHERE id = ? AND profile_id = ?`, id, s.Profile))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, errf(CodeNotFound, "no AI task %s", id)
	}
	return t, err
}

// List returns the profile's tasks, newest first, filtered by kind and
// application when those are set.
func (s *Store) List(kind, applicationID string) ([]Task, error) {
	q := `SELECT ` + taskColumns + ` FROM ai_tasks WHERE profile_id = ? AND status != '` + StatusReserved + `'`
	args := []any{s.Profile}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if applicationID != "" {
		q += ` AND application_id = ?`
		args = append(args, applicationID)
	}
	rows, err := s.DB.Query(q+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ForApplication is the task shown on an application's page: its open task
// when it has one, else the latest. A nil task means none.
func (s *Store) ForApplication(applicationID string) (*Task, error) {
	ts, err := s.List("", applicationID)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	for i := range ts {
		if !Terminal(ts[i].Status) {
			return &ts[i], nil
		}
	}
	return &ts[0], nil
}

// move changes a task's status in one statement that holds only while the
// task is in one of from, so two callers never both win. extra are more
// columns to set ("error = ?" ...) with their values.
func (s *Store) move(id string, from []string, to string, extra map[string]any) (bool, error) {
	set, args := []string{"status = ?", "updated_at = ?"}, []any{to, s.now()}
	for _, col := range []string{"conversation_id", "error", "result_json", "token_hash"} {
		if v, ok := extra[col]; ok {
			set, args = append(set, col+" = ?"), append(args, v)
		}
	}
	args = append(args, id, s.Profile)
	in := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.DB.Exec(`UPDATE ai_tasks SET `+strings.Join(set, ", ")+
		` WHERE id = ? AND profile_id = ? AND status IN (`+in+`)`, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// closedError is the refusal of a change to a task that is no longer in the
// state the change needs.
func closedError(t Task, what string) *Error {
	return &Error{Code: CodeTaskClosed, Message: "cannot " + what + ": task " + t.ID + " is " + t.Status,
		Fields: map[string]any{"status": t.Status}}
}

// NewToken is a fresh task token: the secret the task's turn runs with.
func NewToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashToken is what is stored of a token: bound to the profile, task and
// conversation it was minted for, so a hash copied to another task, or a
// token used on another, matches nothing.
func HashToken(profile, taskID, conversationID, token string) string {
	h := sha256.Sum256([]byte("aitask-token\x00" + profile + "\x00" + taskID + "\x00" + conversationID + "\x00" + token))
	return hex.EncodeToString(h[:])
}

// liveStatuses are the statuses a task can still be given a token and a holder in.
const liveStatuses = `('queued','running','needs_user')`

// SetToken stores the hash of the token the task's next turn runs with. A task that is over
// (cancelled by a deletion, finished, failed) takes none: a write that arrives late must not
// bring it back.
func (s *Store) SetToken(id, token string) error {
	t, err := s.Get(id)
	if err != nil {
		return err
	}
	res, err := s.DB.Exec(`UPDATE ai_tasks SET token_hash = ? WHERE id = ? AND profile_id = ? AND status IN `+liveStatuses,
		HashToken(t.Profile, t.ID, t.ConversationID, token), id, s.Profile)
	return closedUnlessChanged(res, err, id)
}

// SetHolder records the process that runs the task's turn (the supervisor):
// only that process and its descendants hold the token afterwards. A task that is over takes none.
func (s *Store) SetHolder(id string, pid int) error {
	res, err := s.DB.Exec(`UPDATE ai_tasks SET holder_pid = ? WHERE id = ? AND profile_id = ? AND status IN `+liveStatuses, pid, id, s.Profile)
	return closedUnlessChanged(res, err, id)
}

// closedUnlessChanged is the error of a write to a task that is no longer live: it changed no row.
func closedUnlessChanged(res sql.Result, err error, id string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("aitask: task %s is not live: nothing was written", id)
	}
	return nil
}

// CheckToken says whether token is the one minted for task id (this profile,
// this conversation), in constant time, and whether the calling process is the
// one that runs the task's turn or inside it (ProcessWithin) once that process
// is recorded. A task with no token (never started) accepts none.
func (s *Store) CheckToken(id, token string) bool {
	return s.checkToken(id, token, true)
}

// CheckTokenRemote is CheckToken for a caller that is not a process of this
// machine's tree: the daemon API, where the request arrives from a process the
// task runs in but that the daemon cannot place in the task's process tree.
// The hash is checked as for CheckToken; the process-tree check is not made,
// and the API route requires an agent token bound to this task in its place.
func (s *Store) CheckTokenRemote(id, token string) bool {
	return s.checkToken(id, token, false)
}

func (s *Store) checkToken(id, token string, local bool) bool {
	var stored, conv, status string
	var pid int
	if err := s.DB.QueryRow(`SELECT token_hash, conversation_id, holder_pid, status FROM ai_tasks WHERE id = ? AND profile_id = ?`,
		id, s.Profile).Scan(&stored, &conv, &pid, &status); err != nil {
		return false
	}
	if stored == "" || token == "" || status == StatusCancelled {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(HashToken(s.Profile, id, conv, token))) != 1 {
		return false
	}
	return !local || pid == 0 || ProcessWithin(pid)
}

// OpenForConversation returns the profile's task on conversation convID that
// is still open (running or waiting for the user), newest first; ok is false
// when there is none. A queued task is not open: `start` begins it, and a
// finished one is never reopened.
func (s *Store) OpenForConversation(convID string) (Task, bool, error) {
	t, err := scanTask(s.DB.QueryRow(`SELECT `+taskColumns+` FROM ai_tasks WHERE profile_id = ? AND conversation_id = ?
		AND status IN ('running','needs_user') ORDER BY created_at DESC, id DESC LIMIT 1`, s.Profile, convID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	return t, err == nil, err
}

// ForConversation returns the profile's newest task on conversation convID in
// any status, for deciding what the conversation is (an application-apply one
// stays confined after its task is over); ok is false when there is none.
func (s *Store) ForConversation(convID string) (Task, bool, error) {
	t, err := scanTask(s.DB.QueryRow(`SELECT `+taskColumns+` FROM ai_tasks WHERE profile_id = ? AND conversation_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, s.Profile, convID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	return t, err == nil, err
}

// Rebind hands an open task to a new turn: it mints the token that turn runs
// with (the old one stops working), records pid as the process that holds it,
// and puts the task back to running, in one conditional update. It does
// nothing, and reports false, for a task that is queued or over.
func (s *Store) Rebind(id, token string, pid int) (bool, error) {
	t, err := s.Get(id)
	if err != nil {
		return false, err
	}
	res, err := s.DB.Exec(`UPDATE ai_tasks SET status = 'running', token_hash = ?, holder_pid = ?, updated_at = ?
		WHERE id = ? AND profile_id = ? AND status IN ('running','needs_user')`,
		HashToken(t.Profile, t.ID, t.ConversationID, token), pid, s.now(), id, s.Profile)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// EndTurn is what the process that ran a task's turn reports when it is over:
// a task still running was not completed, so it needs the user (with the
// turn's error, if it ended in one). A task that is already over stays.
func (s *Store) EndTurn(id, turnErr string) error {
	extra := map[string]any{}
	if turnErr != "" {
		extra["error"] = clip(turnErr)
	}
	_, err := s.move(id, []string{StatusRunning}, StatusNeedsUser, extra)
	return err
}

// SetPrompt replaces a task's first-turn prompt (start points it at the
// verified context snapshot).
func (s *Store) SetPrompt(id, prompt string) error {
	_, err := s.DB.Exec(`UPDATE ai_tasks SET prompt = ? WHERE id = ? AND profile_id = ?`, prompt, id, s.Profile)
	return err
}
