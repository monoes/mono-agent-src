package doomsday

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
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
	return "Open-ended run stopped by you at " + at.UTC().Format("2006-01-02 15:04:05") + " UTC: " + verdict
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
	ids, convs, err := w.openRunSessions(ctx, o.Runs)
	if err != nil {
		return []string{"sessions NOT ended: the run's sessions cannot be read: " + err.Error()}
	}
	var lines []string
	var ended []string
	endedConv := map[string]bool{}
	ts := tasks.NewStore(w.DB)
	for _, s := range ids {
		if _, err := ts.StopSession(ctx, w.Profile, s.id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			lines = append(lines, fmt.Sprintf("session %d NOT ended: %v", s.id, err))
			continue
		}
		ended = append(ended, strconv.FormatInt(s.id, 10))
		endedConv[s.conv] = true
	}
	if len(ended) > 0 {
		lines = append([]string{"sessions ended: " + strings.Join(ended, ", ")}, lines...)
	}
	for _, c := range convs {
		if !endedConv[c] {
			continue
		}
		msg := stopMessage(verdict, at)
		switch {
		case w.ChatNote == nil:
			lines = append(lines, "transcript NOT written ("+c+"): no chat store is wired")
		default:
			if err := w.ChatNote(ctx, c, msg); err != nil {
				lines = append(lines, "transcript NOT written ("+c+"): "+err.Error())
			} else {
				lines = append(lines, "transcript "+c+": "+msg)
			}
		}
	}
	return lines
}

type runSession struct {
	id   int64
	conv string
}

// openRunSessions are the open sessions to end, in id order, and the chats they belong to, sorted.
func (w Wiring) openRunSessions(ctx context.Context, runs []RunRef) ([]runSession, []string, error) {
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
			AND doomsday_generation = ? AND COALESCE(parent_session_id, 0) = 0`, w.Profile, r.CharterID, r.Generation)
		if err != nil {
			return nil, nil, err
		}
		for _, b := range stamped {
			if b.conv == "" {
				continue
			}
			chats[b.conv] = true
			leads, err := w.queryRunSessions(ctx, `SELECT id, conversation_id FROM task_sessions WHERE profile_id = ? AND conversation_id = ?
				AND archived_at = '' AND state IN ('draft','running','paused') AND `+coderhost.LeadSessionSQL, w.Profile, b.conv)
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
