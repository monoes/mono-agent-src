package doomsday

import (
	"context"
	"errors"
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
		if isInvisibleFormat(r) {
			continue // zero-width and bidirectional marks can reorder or hide text on a terminal or in the transcript
		}
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

// isInvisibleFormat is the zero-width, directional-mark, bidirectional-override/isolate and byte-order characters.
func isInvisibleFormat(r rune) bool {
	return (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0xFEFF
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
func (w Wiring) afterStop(ctx context.Context, o StopOutcome, verdict string, at time.Time) AfterReport {
	if w.DB == nil || len(o.Runs) == 0 {
		return AfterReport{}
	}
	ids, convs, err := w.openRunSessions(ctx, o.Runs, "")
	if err != nil {
		return AfterReport{Lines: cleanLines([]string{"sessions NOT ended: the run's sessions cannot be read: " + err.Error()}), Failed: true}
	}
	lines, ended, endedConv := w.endSessions(ctx, ids)
	if len(ended) > 0 {
		lines = append([]string{"sessions ended: " + strings.Join(ended, ", ")}, lines...)
	}
	for _, c := range convs {
		if endedConv[c] == 0 {
			continue
		}
		msg := stopMessage(verdict, at)
		l, failed := w.noteChat(ctx, c, msg)
		if l != "" {
			lines = append(lines, l)
		}
		if failed {
			// The message is owed: record it before the failure is reported, so a later stop can write it.
			if run, intent, ok := w.runOfChat(ctx, o.Runs, c); !ok {
				lines = append(lines, "transcript NOT recorded as owed ("+c+"): its run cannot be read")
			} else if err := w.writePending(run, intent, c, owedChat{Code: owedCode(verdict), At: at.Unix()}); err != nil {
				lines = append(lines, "transcript NOT recorded as owed ("+c+"): "+err.Error())
			}
		}
	}
	return AfterReport{Lines: cleanLines(lines), Failed: failedLines(lines)}
}

// failedLines says whether any line reports something that was not done (the lines are this file's own).
func failedLines(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, " NOT ended") || strings.HasPrefix(l, "transcript NOT") {
			return true
		}
	}
	return false
}

// noteChat writes msg into chat c's transcript and returns the output line for it.
func (w Wiring) noteChat(ctx context.Context, c, msg string) (line string, failed bool) {
	if w.ChatNote == nil {
		return "", false // an install with no chat store (only a test wiring): there is no transcript to write
	}
	if err := w.ChatNote(ctx, c, msg); err != nil {
		return "transcript NOT written (" + c + "): " + err.Error(), true
	}
	return "transcript " + c + ": " + msg, false
}

// endSessions stops each open session as the operator; a failure is a line, never a stop of the others.
func (w Wiring) endSessions(ctx context.Context, ids []runSession) (lines, ended []string, endedConv map[string]int) {
	endedConv = map[string]int{}
	ts := tasks.NewStore(w.DB)
	for _, s := range ids {
		if _, err := ts.StopSession(ctx, w.Profile, s.id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			if errors.Is(err, tasks.ErrNotReady) {
				continue // already out of the open states (stopped, closed or archived between the read and now): nothing to do, nothing to report
			}
			lines = append(lines, fmt.Sprintf("session %d NOT ended: %v", s.id, err))
			continue
		}
		ended = append(ended, strconv.FormatInt(s.id, 10))
		endedConv[s.conv]++
	}
	return lines, ended, endedConv
}

// runOfChat is the run of runs whose stamped boss belongs to chat c, and the time its stop was recorded.
func (w Wiring) runOfChat(ctx context.Context, runs []RunRef, c string) (RunRef, int64, bool) {
	for _, r := range runs {
		rows, err := w.queryRunSessions(ctx, `SELECT id, conversation_id FROM task_sessions WHERE profile_id = ? AND doomsday_charter_id = ?
			AND doomsday_generation = ? AND COALESCE(parent_session_id, 0) = 0 AND archived_at = '' AND conversation_id = ?`, w.Profile, r.CharterID, r.Generation, c)
		if err != nil || len(rows) == 0 {
			continue
		}
		run, err := w.Store.GetRun(ctx, w.Profile, r.Generation)
		if err != nil {
			return RunRef{}, 0, false
		}
		return r, run.IntentAt, true
	}
	return RunRef{}, 0, false
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

// repairWindow bounds the repair: a run stopped longer ago than this is left as it is (its sessions are the operator's by now).
const repairWindow = 7 * 24 * time.Hour

// The first lines a repair prints. The app reads them (components/chat/stopOutcome.js builds its patterns from the same
// strings, in stopOutput.fixture.json, and TestTheRepairHeadsMatchTheFrontendFixture ties the two).
const (
	repairHeadEnded  = "no open run; ended sessions left open by the previous stop"
	repairHeadNote   = "no open run; wrote the chat message the previous stop could not write"
	repairHeadFailed = "no open run; the previous stop's sessions could not be ended"
	// repairHeadNoteFailed: nothing was ended, and the owed chat message is still not written.
	repairHeadNoteFailed = "no open run; the chat message the previous stop could not write is still not written"
)

// repairStop finishes what the last stop left. Lines are printed in place of the "no open run" failure. A chat message is
// written in two cases only: a session of that chat was ended just now (and the chat does not hold this stop's message yet),
// or the first stop recorded that this very message is owed (a pending record of this run, inside the window). No other chat
// is written to: a chat whose sessions were ended by hand never gets a message that claims a stop.
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
	stoppedAt := time.Unix(run.IntentAt, 0)
	ref := RunRef{CharterID: run.CharterID, Generation: run.Generation}
	if at.Sub(stoppedAt) > repairWindow {
		w.dropExpiredFlags(ctx, ref) // an owed flag past the window is never honoured: drop it
		// A hint only when something is left open; a bare stop with nothing to say stays the plain old failure.
		if ids, _, err := w.openRunSessions(ctx, []RunRef{ref}, ""); err == nil && len(ids) > 0 {
			return []string{"no open run; the last run was stopped more than 7 days ago, so its sessions are left as they are"}, RepairNothing
		}
		return nil, RepairNothing
	}
	notAfter := stoppedAt.UTC().Format("2006-01-02T15:04:05Z")
	ids, convs, err := w.openRunSessions(ctx, []RunRef{ref}, notAfter)
	if err != nil {
		return []string{repairHeadFailed + ": " + err.Error()}, RepairFailed
	}
	lines, ended, endedConv := w.endSessions(ctx, ids)
	failed := failedLines(lines)
	var notes []string
	wrote, noteFailed := 0, false
	for _, c := range convs {
		if owed, isOwed := w.readPending(ref, run.IntentAt, c); isOwed {
			// The first stop could not write this message. The text is rebuilt from the record's closed code and time, never read
			// from the file. The flag is claimed (cleared) in one transaction first, so only one repair writes it; if the chat
			// already holds a stop message (the first write committed and still reported an error), only the record is cleared.
			claimed, written, err := w.claimPending(ctx, ref, run.IntentAt, c)
			if err != nil {
				notes = append(notes, "transcript check failed ("+c+"): "+err.Error()+"; the owed message stays recorded")
				failed, noteFailed = true, true
				continue
			}
			if !claimed || written {
				continue
			}
			l, bad := w.noteChat(ctx, c, owed.message())
			if l != "" {
				notes = append(notes, l)
			}
			failed, noteFailed = failed || bad, noteFailed || bad
			if bad {
				if err := w.setPending(ref, run.IntentAt, c, owed, true); err != nil {
					notes = append(notes, "transcript NOT recorded as owed ("+c+"): "+err.Error())
				}
			} else {
				wrote++
			}
			continue
		}
		if endedConv[c] == 0 {
			continue
		}
		if w.ChatNoteSince != nil {
			has, err := w.ChatNoteSince(ctx, w.DB, c, run.IntentAt)
			if err != nil {
				notes = append(notes, "transcript check failed ("+c+"): "+err.Error()+"; no message written there")
				failed = true
				continue
			}
			if has {
				continue
			}
		}
		// Worded for what happened: the run was stopped earlier (not now), and these sessions were ended now. Never "stopped by you at <now>".
		l, bad := w.noteChat(ctx, c, laterMessage(at))
		if l != "" {
			notes = append(notes, l)
		}
		failed = failed || bad
		if bad {
			if err := w.writePending(ref, run.IntentAt, c, owedChat{Code: owedLater, At: at.Unix()}); err != nil {
				notes = append(notes, "transcript NOT recorded as owed ("+c+"): "+err.Error())
			}
		}
	}
	if len(ended) == 0 && wrote == 0 && !failed {
		return nil, RepairNothing
	}
	head := repairHeadEnded + ": " + strings.Join(ended, ", ")
	switch {
	case len(ended) == 0 && wrote > 0:
		head = repairHeadNote
	case len(ended) == 0 && noteFailed && len(lines) == 0:
		head = repairHeadNoteFailed
	case len(ended) == 0:
		head = repairHeadFailed
	}
	out := cleanLines(append(append([]string{head}, lines...), notes...))
	if failed {
		return out, RepairFailed
	}
	return out, RepairDone
}

// dropExpiredFlags clears the owed flags of the run's chats, and of every earlier run's, once the run is past the repair
// window: a flag of an earlier generation is never honoured (a repair looks at the last run only).
func (w Wiring) dropExpiredFlags(ctx context.Context, ref RunRef) {
	// Archived bosses count here: their chat is no longer one the repair writes to, and its flag must still go.
	rows, err := w.DB.QueryContext(ctx, `SELECT conversation_id, doomsday_charter_id, doomsday_generation FROM task_sessions WHERE profile_id = ?
		AND doomsday_charter_id != '' AND doomsday_generation <= ? AND COALESCE(parent_session_id, 0) = 0 AND conversation_id != ''`, w.Profile, ref.Generation)
	if err != nil {
		return
	}
	type owner struct {
		conv string
		run  RunRef
	}
	var owners []owner
	for rows.Next() {
		var o owner
		if rows.Scan(&o.conv, &o.run.CharterID, &o.run.Generation) == nil {
			owners = append(owners, o)
		}
	}
	rows.Close()
	for _, o := range owners {
		if st, err := w.coder().GetSettings(w.Profile, o.conv); err == nil && st.OwedStopNotice != nil && st.OwedStopNotice.Charter == o.run.CharterID && st.OwedStopNotice.Generation == o.run.Generation {
			_ = w.clearPending(o.run, st.OwedStopNotice.IntentAt, o.conv)
		}
	}
}
