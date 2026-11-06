package doomsday

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/tasks"
)

// What a Stop leaves behind in the chat. The run's gate and processes are ended by the Stop order; the sessions that hold
// the chat open (a lead mission in draft, running or paused locks the chat's mode toggle) are not, so afterStop ends them
// once the stop is proven, and writes one message into the transcript of each chat it touched.
//
// Which sessions: the run's boss (the top-level lead stamped with the run's charter and generation), and every open lead
// session (kind mission, direction, research or task, or the planner's board session: coderhost.LeadSessionSQL) of the chats
// the boss belongs to. Nothing else: not another lead of the profile, not a plain board session, not a session already
// finished. A session is stopped, never closed (closing is the operator's verdict on the work, and Stop makes none).

// stopMessage is the transcript message: who, when, and the exact stop line (which says "best effort" when it was).
func stopMessage(verdict string, at time.Time) string {
	return "Open-ended run stopped by you at " + at.UTC().Format("2006-01-02 15:04:05") + " UTC: " + cleanLine(verdict)
}

// maxLineBytes caps any text taken from outside this file (the verdict, an error) before it is printed or stored.
const maxLineBytes = 400

// cleanLine makes s one printable line: every control character (newline, carriage return, escape, tab) becomes a space,
// runs of spaces collapse, and the result is cut at maxLineBytes on a rune boundary with an ellipsis. Text from an OS or a
// process (a best-effort limit, an error) therefore cannot add a line to the output the app parses or to the transcript.
func cleanLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) <= maxLineBytes {
		return out
	}
	cut := maxLineBytes
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + "…"
}

func cleanLines(in []string) []string {
	for i := range in {
		in[i] = cleanLine(in[i])
	}
	return in
}

// afterStop ends the sessions of the runs o proved stopped and writes the chat message. It is called only after the stop
// verb printed its verdict, never when the stop failed (o.Runs holds only fully stopped runs), and is idempotent: it
// looks at open sessions only and writes a message only for a chat in which it ended a session. Lines are printed after
// the verdict: "sessions ended: 4, 5", "transcript <conversation>: <message>" (the app reads the chat's own line) and a
// "... NOT ..." line for anything it could not do.
func (w Wiring) afterStop(ctx context.Context, o StopOutcome, verdict string, at time.Time) []string {
	if w.DB == nil || len(o.Runs) == 0 {
		return nil
	}
	ids, convs, err := w.openRunSessions(ctx, o.Runs, "")
	if err != nil {
		return []string{"sessions NOT ended: the run's sessions cannot be read: " + err.Error()}
	}
	lines, ended, endedConv := w.endSessions(ctx, ids)
	if len(ended) > 0 {
		lines = append([]string{"sessions ended: " + strings.Join(ended, ", ")}, lines...)
	}
	for _, c := range convs {
		if !endedConv[c] {
			continue
		}
		lines = append(lines, w.noteChat(ctx, c, stopMessage(verdict, at)))
	}
	return cleanLines(lines)
}

// noteChat writes msg into chat c's transcript and returns the output line for it.
func (w Wiring) noteChat(ctx context.Context, c, msg string) string {
	if w.ChatNote == nil {
		return "transcript NOT written (" + c + "): no chat store is wired"
	}
	if err := w.ChatNote(ctx, c, msg); err != nil {
		return "transcript NOT written (" + c + "): " + err.Error()
	}
	return "transcript " + c + ": " + msg
}

// endSessions stops each open session as the operator; a failure is a line, never a stop of the others.
func (w Wiring) endSessions(ctx context.Context, ids []runSession) (lines, ended []string, endedConv map[string]bool) {
	endedConv = map[string]bool{}
	ts := tasks.NewStore(w.DB)
	for _, s := range ids {
		if _, err := ts.StopSession(ctx, w.Profile, s.id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			lines = append(lines, fmt.Sprintf("session %d NOT ended: %v", s.id, err))
			continue
		}
		ended = append(ended, strconv.FormatInt(s.id, 10))
		endedConv[s.conv] = true
	}
	return lines, ended, endedConv
}

type runSession struct {
	id   int64
	conv string
}

// openRunSessions are the open sessions to end, in id order, and the chats they belong to, sorted.
func (w Wiring) openRunSessions(ctx context.Context, runs []RunRef, notAfter string) ([]runSession, []string, error) {
	seen := map[int64]bool{}
	var out []runSession
	chats := map[string]bool{}
	add := func(rows []runSession) {
		for _, r := range rows {
			if !seen[r.id] {
				seen[r.id] = true
				out = append(out, r)
			}
		}
	}
	for _, r := range runs {
		bosses, err := w.queryRunSessions(ctx, `SELECT id, conversation_id FROM task_sessions WHERE profile_id = ? AND doomsday_charter_id = ?
			AND doomsday_generation = ? AND COALESCE(parent_session_id, 0) = 0 AND archived_at = '' AND state IN ('draft','running','paused')`,
			w.Profile, r.CharterID, r.Generation)
		if err != nil {
			return nil, nil, err
		}
		add(bosses)
		// The boss's chat: stamped sessions of the run, open or not, name it (a boss already stopped by hand still has one).
		stamped, err := w.queryRunSessions(ctx, `SELECT id, conversation_id FROM task_sessions WHERE profile_id = ? AND doomsday_charter_id = ?
			AND doomsday_generation = ? AND COALESCE(parent_session_id, 0) = 0 AND archived_at = ''`, w.Profile, r.CharterID, r.Generation)
		if err != nil {
			return nil, nil, err
		}
		for _, b := range stamped {
			if b.conv == "" {
				continue
			}
			chats[b.conv] = true
			leads, err := w.queryRunSessions(ctx, `SELECT id, conversation_id FROM task_sessions WHERE profile_id = ? AND conversation_id = ?
				AND archived_at = '' AND state IN ('draft','running','paused') AND (? = '' OR created_at <= ?) AND `+coderhost.LeadSessionSQL, w.Profile, b.conv, notAfter, notAfter)
			if err != nil {
				return nil, nil, err
			}
			add(leads)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	convs := make([]string, 0, len(chats))
	for c := range chats {
		convs = append(convs, c)
	}
	sort.Strings(convs)
	return out, convs, nil
}

func (w Wiring) queryRunSessions(ctx context.Context, q string, args ...any) ([]runSession, error) {
	rows, err := w.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runSession
	for rows.Next() {
		var s runSession
		if err := rows.Scan(&s.id, &s.conv); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Repair of a stop whose transition did not finish. A stop that was proven but could not end every session (or could not
// write the message) leaves the run quiescent, so the next `doomsday stop` finds no open run. The step is then run again
// for the LAST run of the profile only: the highest generation, when it is quiescent by an operator stop (not by a finish,
// and never while any run is open: the caller only asks when none is). Its boss is the session stamped with its charter
// and generation; the lead sessions are those of the boss's chat that existed when that stop was recorded, so a mission
// the person started in the chat afterwards is not touched. Nothing is looked at in another run or profile.

// operatorStopCause is the cause the Stop order records for a run an operator stopped.
const operatorStopCause = "operator stop"

// RepairStatus is what a repair found.
type RepairStatus int

const (
	RepairNothing RepairStatus = iota // nothing to repair: the stop verb keeps its "no open run" failure
	RepairDone                        // something was ended or written, and nothing failed: exit 0
	RepairFailed                      // part of it failed: the lines say what, exit non-zero
)

// repairStop finishes what the last stop left. Lines are printed in place of the "no open run" failure.
func (w Wiring) repairStop(ctx context.Context, at time.Time) ([]string, RepairStatus) {
	if w.DB == nil || w.Store == nil {
		return nil, RepairNothing
	}
	next, err := w.Store.NextGeneration(ctx, w.Profile)
	if err != nil || next <= 1 {
		return nil, RepairNothing
	}
	run, err := w.Store.GetRun(ctx, w.Profile, next-1)
	if err != nil || run.State != store.StateQuiescent || run.Cause != operatorStopCause {
		return nil, RepairNothing
	}
	ref := RunRef{CharterID: run.CharterID, Generation: run.Generation}
	notAfter := time.Unix(run.IntentAt, 0).UTC().Format("2006-01-02T15:04:05Z")
	ids, convs, err := w.openRunSessions(ctx, []RunRef{ref}, notAfter)
	if err != nil {
		return []string{"no open run; the previous stop's sessions cannot be read: " + err.Error()}, RepairFailed
	}
	lines, ended, endedConv := w.endSessions(ctx, ids)
	failed := len(lines) > 0
	var notes []string
	for _, c := range convs {
		// Written only when the chat does not hold this stop's message yet (and, where that cannot be asked, only for a chat
		// in which a session was ended just now).
		if (w.ChatNoteSince != nil && w.chatHasNote(ctx, c, run.IntentAt)) || (w.ChatNoteSince == nil && !endedConv[c]) {
			continue
		}
		msg := stopMessage("stopped (this stop's exact line was not kept; its sessions were ended by a later stop)", time.Unix(run.IntentAt, 0))
		l := w.noteChat(ctx, c, msg)
		failed = failed || strings.HasPrefix(l, "transcript NOT")
		notes = append(notes, l)
	}
	if len(ended) == 0 && len(notes) == 0 && !failed {
		return nil, RepairNothing
	}
	head := "no open run; wrote the chat message the previous stop did not leave"
	if len(ended) > 0 {
		head = "no open run; ended sessions left open by the previous stop: " + strings.Join(ended, ", ")
	}
	out := cleanLines(append(append([]string{head}, lines...), notes...))
	if failed {
		return out, RepairFailed
	}
	return out, RepairDone
}

func (w Wiring) chatHasNote(ctx context.Context, c string, since int64) bool {
	ok, err := w.ChatNoteSince(ctx, c, since)
	return err != nil || ok // an unreadable answer writes nothing twice
}
