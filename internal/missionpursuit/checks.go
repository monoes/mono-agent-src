package missionpursuit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The states of a goal check, and the outcomes that are the person's.
const (
	CheckRunning   = "running"
	CheckDone      = "done"
	CheckAbandoned = "abandoned"

	OutcomePassedMachine = "passed-machine"
	OutcomeAccepted      = "accepted"
	OutcomeRejected      = "rejected"

	// HostVerifier is the writer of a check and its results.
	HostVerifier = "host-verifier"

	// ExcerptShown is the most an excerpt shows, in bytes.
	ExcerptShown = 1024
	// MaxNoteBytes is the longest note on a rubric answer.
	MaxNoteBytes = 500
)

var (
	// ErrNoCheck: the mission has no check, or not the one named.
	ErrNoCheck = fmt.Errorf("%w: this mission has no such goal check", tasks.ErrNotFound)
	// ErrDossierStale: the check is not the one the person read.
	ErrDossierStale = fmt.Errorf("%w: the check changed since you read it: read it again before you answer", tasks.ErrInvalid)
	// ErrSubjectChanged: the project is not the one the check ran against.
	ErrSubjectChanged = fmt.Errorf("%w: the project changed after the check; a new check will run", tasks.ErrInvalid)
)

// A CheckResult is what the host measured for one machine criterion. The excerpts are filled only for the operator.
type CheckResult struct {
	Criterion       string `json:"criterion"` // c1, c2, ...
	Hash            string `json:"hash"`
	Kind            string `json:"kind"`
	Result          string `json:"result"`
	ExitCode        int    `json:"exit_code"`
	StdoutDigest    string `json:"stdout_digest,omitempty"`
	StderrDigest    string `json:"stderr_digest,omitempty"`
	RunsSubjectCode bool   `json:"runs_subject_code,omitempty"`
	Net             string `json:"net,omitempty"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// A RubricLine is a rubric criterion in the person's own words, with the answer given so far.
type RubricLine struct {
	Criterion  string `json:"criterion"`
	Hash       string `json:"hash"`
	Spec       string `json:"spec"`
	Expected   string `json:"expected,omitempty"`
	Answer     string `json:"answer,omitempty"` // yes, no or "" (not answered yet)
	AnsweredBy string `json:"answered_by,omitempty"`
	Note       string `json:"note,omitempty"` // the person's words; operator view only
}

// A Check is one check of a mission's locked goal, as read.
type Check struct {
	ID               int64         `json:"id"`
	SessionID        int64         `json:"session_id"`
	GoalHash         string        `json:"goal_hash"`
	Round            int64         `json:"round"`
	State            string        `json:"state"`
	Outcome          string        `json:"outcome,omitempty"`
	Reason           string        `json:"reason,omitempty"`
	SubjectDigest    string        `json:"subject_digest,omitempty"`
	SubjectFiles     int           `json:"subject_files"`
	Containment      string        `json:"containment,omitempty"`
	Net              string        `json:"net,omitempty"`
	Unconfined       bool          `json:"unconfined"`
	ReviewerPin      string        `json:"reviewer_pin,omitempty"`
	ReviewerObserved string        `json:"reviewer_observed,omitempty"`
	ReviewerVerdict  string        `json:"reviewer_verdict,omitempty"`
	ReviewerReason   string        `json:"reviewer_reason,omitempty"`
	ResultsDigest    string        `json:"results_digest,omitempty"`
	CostMicro        int64         `json:"cost_micro"`
	CostKnown        bool          `json:"cost_known"`
	StartedAt        string        `json:"started_at"`
	EndedAt          string        `json:"ended_at,omitempty"`
	WrittenBy        string        `json:"written_by"`
	Dossier          string        `json:"dossier"`
	Results          []CheckResult `json:"results"`
	Rubric           []RubricLine  `json:"rubric"`
	// SubjectRoot is the folder the check ran against: set for the operator's answer step, never printed to an agent.
	SubjectRoot string `json:"-"`
}

// UnconfinedNotice names what a check that ran without confinement could do, for every place that shows such a check.
const UnconfinedNotice = "UNCONFINED: the project commands of this check ran as you, with NO confinement beyond a process group, no network and resource limits. " +
	"They could read or write any of your files, connect to local sockets (the session bus, ssh-agent, other services), read the environment of your other processes through /proc, " +
	"use your keyring, and leave processes behind that outlive the check (setsid)."

// ranUnconfined says whether a containment string means that programs ran without the confinement the library proves. An
// empty or unknown string counts as unconfined for a check in which a program ran: the notice is the safe reading.
func ranUnconfined(containment string, results []CheckResult) bool {
	if strings.HasPrefix(containment, "confined: ") || strings.HasPrefix(containment, "none (") {
		return false
	}
	if containment != "" {
		return true
	}
	for _, r := range results {
		if r.RunsSubjectCode || r.Kind == "command" || r.Kind == "output" {
			return true
		}
	}
	return false
}

// dossierHash is what the person read: the goal, the machine results, the reviewer's verdict and the project's digest, and
// which check it is. An answer names it back, so an answer is never for a check that changed since it was read.
func dossierHash(id int64, goalHash, resultsDigest, verdict, subjectDigest string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("goal-check-dossier\ncheck:%d\ngoal:%s\nresults:%s\nreviewer:%s\nsubject:%s\n", id, goalHash, resultsDigest, verdict, subjectDigest)))
	return hex.EncodeToString(sum[:])
}

// Clean makes text from a model, a program or a person safe to show: invalid UTF-8, control, format (bidirectional, zero
// width) and unassigned characters are replaced by a space, and the result is cut at max bytes on a character boundary.
func Clean(s string, max int) string {
	s = strings.ToValidUTF8(s, "?")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' || r == '­':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
	}
	return out
}

// after is a stamp strictly later than prev (the stamps have a second's resolution), so that a person's action always moves
// the date the pursuit loop compares against.
func (s *Store) after(prev string) string {
	now := s.now().UTC().Truncate(time.Second)
	if t, err := time.Parse(time.RFC3339, prev); err == nil && !now.After(t) {
		now = t.Add(time.Second)
	}
	return now.Format(time.RFC3339)
}

// LatestCheck is the latest check of the mission's goal, with its machine results and the rubric criteria in the person's
// words; tasks.ErrNotFound-wrapping ErrNoCheck when there is none. Excerpts and the person's notes are filled only when
// operator is set: an agent gets host facts (ids, enums, counts, digests) and the criteria text.
func (s *Store) LatestCheck(ctx context.Context, profileID string, session int64, operator bool) (Check, error) {
	return s.readCheck(ctx, profileID, session, 0, operator)
}

// GetCheck is the check with the id, of the mission.
func (s *Store) GetCheck(ctx context.Context, profileID string, session, id int64, operator bool) (Check, error) {
	return s.readCheck(ctx, profileID, session, id, operator)
}

func (s *Store) readCheck(ctx context.Context, profileID string, session, id int64, operator bool) (Check, error) {
	var out Check
	err := s.read(ctx, func(x tasks.Queryer) error {
		var owner int64
		if err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&owner); err != nil {
			return notFound(err, session)
		}
		c, err := readCheckRow(ctx, x, session, id)
		if err != nil {
			return err
		}
		out, err = fillCheck(ctx, x, c, operator)
		return err
	})
	return out, err
}

const checkColumns = `id, session_id, goal_hash, round, state, outcome, reason, subject_root, subject_digest, subject_files, containment, net,
  reviewer_pin, reviewer_observed, reviewer_verdict, reviewer_reason, results_digest, cost_micro, cost_known, started_at, ended_at, written_by`

func readCheckRow(ctx context.Context, x tasks.Queryer, session, id int64) (Check, error) {
	q := `SELECT ` + checkColumns + ` FROM mission_goal_checks WHERE session_id = ?`
	args := []any{session}
	if id != 0 {
		q += ` AND id = ?`
		args = append(args, id)
	}
	q += ` ORDER BY id DESC LIMIT 1`
	var c Check
	var known int
	err := x.QueryRowContext(ctx, q, args...).Scan(&c.ID, &c.SessionID, &c.GoalHash, &c.Round, &c.State, &c.Outcome, &c.Reason, &c.SubjectRoot, &c.SubjectDigest, &c.SubjectFiles,
		&c.Containment, &c.Net, &c.ReviewerPin, &c.ReviewerObserved, &c.ReviewerVerdict, &c.ReviewerReason, &c.ResultsDigest, &c.CostMicro, &known, &c.StartedAt, &c.EndedAt, &c.WrittenBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Check{}, ErrNoCheck
	}
	if err != nil {
		return Check{}, fmt.Errorf("missionpursuit: reading the check: %w", err)
	}
	c.CostKnown = known == 1
	return c, nil
}

// fillCheck adds the results, the rubric lines, the dossier and the notice to a check row. Everything a person or a model
// wrote is cleaned and cut again on the way out.
func fillCheck(ctx context.Context, x tasks.Queryer, c Check, operator bool) (Check, error) {
	c.ReviewerPin = Clean(c.ReviewerPin, 200)
	c.ReviewerObserved = Clean(c.ReviewerObserved, 200)
	c.ReviewerReason = Clean(c.ReviewerReason, 200)
	c.Containment = Clean(c.Containment, 200)
	c.Net = Clean(c.Net, 200)
	c.Reason = Clean(c.Reason, 200)
	c.Dossier = dossierHash(c.ID, c.GoalHash, c.ResultsDigest, c.ReviewerVerdict, c.SubjectDigest)
	c.Results = []CheckResult{}
	rows, err := x.QueryContext(ctx, `SELECT r.pos, r.criterion_hash, r.kind, r.result, r.exit_code, r.stdout_digest, r.stderr_digest, r.runs_subject_code, r.net, r.stdout_excerpt, r.stderr_excerpt, r.detail
	  FROM mission_goal_check_results r WHERE r.check_id = ? ORDER BY r.pos`, c.ID)
	if err != nil {
		return Check{}, fmt.Errorf("missionpursuit: reading the check results: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r CheckResult
		var pos, runs int
		var out, errx, detail string
		if err := rows.Scan(&pos, &r.Hash, &r.Kind, &r.Result, &r.ExitCode, &r.StdoutDigest, &r.StderrDigest, &runs, &r.Net, &out, &errx, &detail); err != nil {
			return Check{}, err
		}
		r.Criterion = fmt.Sprintf("c%d", pos)
		r.RunsSubjectCode = runs == 1
		r.Net = Clean(r.Net, 200)
		if operator {
			r.Stdout, r.Stderr, r.Detail = Clean(out, ExcerptShown), Clean(errx, ExcerptShown), Clean(detail, 400)
		}
		c.Results = append(c.Results, r)
	}
	if err := rows.Err(); err != nil {
		return Check{}, err
	}
	c.Unconfined = ranUnconfined(c.Containment, c.Results)
	c.Rubric = []RubricLine{}
	rrows, err := x.QueryContext(ctx, `SELECT g.pos, g.hash, g.spec, g.expected, COALESCE(a.answer, ''), COALESCE(a.answered_by, ''), COALESCE(a.note, '')
	  FROM mission_goal_criteria g LEFT JOIN mission_goal_answers a ON a.check_id = ? AND a.pos = g.pos
	  WHERE g.session_id = ? AND g.kind = 'rubric' ORDER BY g.pos`, c.ID, c.SessionID)
	if err != nil {
		return Check{}, fmt.Errorf("missionpursuit: reading the rubric: %w", err)
	}
	defer rrows.Close()
	for rrows.Next() {
		var l RubricLine
		var pos int
		if err := rrows.Scan(&pos, &l.Hash, &l.Spec, &l.Expected, &l.Answer, &l.AnsweredBy, &l.Note); err != nil {
			return Check{}, err
		}
		l.Criterion = fmt.Sprintf("c%d", pos)
		l.AnsweredBy = Clean(l.AnsweredBy, 80)
		if operator {
			l.Note = Clean(l.Note, MaxNoteBytes)
		} else {
			l.Note = ""
		}
		c.Rubric = append(c.Rubric, l)
	}
	return c, rrows.Err()
}
