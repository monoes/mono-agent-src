package task

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
	"github.com/monoes/mono-agent/internal/tasks"
)

// PursuitSchemaVersion is the version of the status and list JSON documents: keys are added, never renamed or removed,
// within one version.
const PursuitSchemaVersion = 1

// maxIDsPerState caps the ids listed per direction state; the counts stay exact.
const maxIDsPerState = 50

// Agent text (a direction title the judge proposed, a mission title) is cut to this many characters wherever it is shown.
const agentTextRunes = 80

var directionStates = []types.DirectionState{types.DirProposed, types.DirActive, types.DirStalled, types.DirFinished, types.DirPruned}

// pursuitStrategy is what the strategy engine recorded for a mission. Host-written facts only: counts, ids the host
// generated, enums and times. No agent-authored text is in it.
type pursuitStrategy struct {
	Epochs         epochSummary   `json:"epochs"`
	Directions     directionStats `json:"directions"`
	LastJudgeRound *judgeRound    `json:"last_judge_round,omitempty"`
}

type epochSummary struct {
	Count  int          `json:"count"`
	Latest *latestEpoch `json:"latest,omitempty"`
}

type latestEpoch struct {
	Seq       int64  `json:"seq"`
	Trigger   string `json:"trigger"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
}

type directionStats struct {
	Counts map[string]int      `json:"counts"`
	IDs    map[string][]string `json:"ids"`
}

type judgeRound struct {
	At      string `json:"at"`      // when the latest epoch started
	Outcome string `json:"outcome"` // applied, invalid, model-failed or input, as the loop recorded it
}

// goalCheckStub is the place of the goal check line. Nothing reads it yet: the goal checker (S7b) does not exist. When it
// does, this value is filled from the host-measured check record, never from anything an agent wrote.
type goalCheckStub struct {
	Available bool   `json:"available"`
	Note      string `json:"note"`
}

// agentText is the only part of the status document that carries words an agent or the judge wrote. A GUI card must not
// read the host-written fields from here, and must show these as text, marked as agent text.
type agentText struct {
	Directions []agentDirection `json:"directions"`
}

type agentDirection struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Title string `json:"title"` // control characters stripped, at most agentTextRunes characters
}

// cleanAgentText strips what could reach a terminal as a command (control characters, hidden and format characters such
// as bidi overrides), makes it one line and cuts it.
func cleanAgentText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > agentTextRunes {
		s = strings.TrimRightFunc(string([]rune(s)[:agentTextRunes-1]), unicode.IsSpace) + "…"
	}
	return s
}

// readStrategy reads the epochs and directions of a mission. A mission the engine never touched yields zero counts.
func readStrategy(ctx context.Context, db *sql.DB, session int64, pr missionpursuit.Progress) (pursuitStrategy, agentText, error) {
	out := pursuitStrategy{Directions: directionStats{Counts: map[string]int{}, IDs: map[string][]string{}}}
	var text agentText
	for _, st := range directionStates {
		out.Directions.Counts[string(st)] = 0
		out.Directions.IDs[string(st)] = []string{}
	}
	text.Directions = []agentDirection{}
	ss, prof := strategy.NewStore(db), strconv.FormatInt(session, 10)
	eps, err := ss.ListEpochs(ctx, prof, 1)
	if err != nil {
		return out, text, fmt.Errorf("reading the epochs: %w", err)
	}
	out.Epochs.Count = len(eps)
	if n := len(eps); n > 0 {
		e := eps[n-1]
		at := time.Unix(e.StartedAt, 0).UTC().Format(time.RFC3339)
		out.Epochs.Latest = &latestEpoch{Seq: e.Seq, Trigger: string(e.Trigger), State: string(e.State), StartedAt: at}
		if pr.LastRound != "" {
			out.LastJudgeRound = &judgeRound{At: at, Outcome: pr.LastRound}
		}
	}
	dirs, err := ss.ListDirections(ctx, prof, 1)
	if err != nil {
		return out, text, fmt.Errorf("reading the directions: %w", err)
	}
	for _, d := range dirs {
		st := string(d.State)
		out.Directions.Counts[st]++
		if len(out.Directions.IDs[st]) < maxIDsPerState {
			out.Directions.IDs[st] = append(out.Directions.IDs[st], d.ID)
			text.Directions = append(text.Directions, agentDirection{ID: d.ID, State: st, Title: cleanAgentText(d.Title)})
		}
	}
	return out, text, nil
}

// hostRuns says whether a live coder host runs the pursuit loop.
func hostRuns(h coderhost.PursuitHostInfo, found, alive bool) bool {
	return found && alive && h.Enabled
}

// writeStatus prints the status: the JSON document (schema_version, the progress block with host facts only, the strategy
// block, the goal-check stub, and the agent text kept apart) or the plain words.
func (d Deps) writeStatus(cmd *cobra.Command, p tasks.Profile, pu missionpursuit.Pursuit, pr missionpursuit.Progress, st pursuitStrategy, at agentText) error {
	host, found, alive := d.hostPursuit()
	runs := hostRuns(host, found, alive)
	state := missionpursuit.StateOf(pu.Paused(), pr.Waiting, runs)
	needs := missionpursuit.NeedsOf(pu.Paused(), pr.Waiting, pr.Seen, runs)
	next := missionpursuit.NextLine(pu.Paused(), pr.Waiting, pr.Seen, runs)
	gc := goalCheckStub{Note: "no goal checker exists yet: the goal is never checked automatically; you decide when the goal is met and close the mission"}
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{
			"schema_version": PursuitSchemaVersion, "profile": p, "pursuit": pu, "progress": pr,
			"host":  map[string]any{"published": found, "running": alive, "pursuit": host, "judge_call_cap_usd": coderhost.PursuitJudgeCallCapUSD},
			"state": state, "needs": needs, "next": next, "strategy": st, "goal_check": gc, "agent_text": at,
		})
	}
	if err := d.writeProgress(cmd, p, pu, pr); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "  state: %s; needs: %s\n", state, needs)
	writeStrategyLines(w, st, at)
	fmt.Fprintf(w, "  goal check: %s\n  next: %s\n", gc.Note, next)
	return nil
}

func writeStrategyLines(w io.Writer, st pursuitStrategy, at agentText) {
	if st.Epochs.Count == 0 {
		fmt.Fprintln(w, "  epochs: none yet")
	} else {
		l := st.Epochs.Latest
		fmt.Fprintf(w, "  epochs: %d; the latest, #%d, started at %s because of %s, now %s", st.Epochs.Count, l.Seq, l.StartedAt, l.Trigger, l.State)
		if st.LastJudgeRound != nil {
			fmt.Fprintf(w, "; the last judge round ended: %s", st.LastJudgeRound.Outcome)
		}
		fmt.Fprintln(w)
	}
	var parts []string
	for _, s := range directionStates {
		parts = append(parts, fmt.Sprintf("%d %s", st.Directions.Counts[string(s)], s))
	}
	fmt.Fprintf(w, "  directions: %s\n", strings.Join(parts, ", "))
	for _, d := range at.Directions {
		fmt.Fprintf(w, "    %s [%s] (agent text, not checked) %q\n", d.ID, d.State, d.Title)
	}
}

// pursuitRow is one line of `pursuit list`. Title is agent text (a mission is titled by whoever made it) and is cut and
// stripped; every other field is host-written.
type pursuitRow struct {
	Session int64  `json:"session_id"`
	State   string `json:"state"`
	Waiting string `json:"waiting,omitempty"`
	Needs   string `json:"needs"`
	Title   string `json:"title_agent_text"`
}

func (d Deps) newPursuitListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the missions in pursuit for this profile: state, why they wait and who has to act (read only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			p, err := tasks.NewStore(db.DB).Profile(cmd.Context(), d.ProfileID())
			if err != nil {
				return d.taskErr(err)
			}
			rows, err := db.DB.QueryContext(cmd.Context(), `SELECT s.id, s.title, p.paused_by, COALESCE(st.waiting, ''), st.session_id IS NOT NULL
				FROM mission_pursuit p JOIN task_sessions s ON s.id = p.session_id LEFT JOIN mission_pursuit_state st ON st.session_id = p.session_id
				WHERE p.enabled = 1 AND s.kind = 'mission' AND s.state = 'running' AND s.archived_at = '' AND s.profile_id = ? ORDER BY s.id`, p.ID)
			if err != nil {
				return fmt.Errorf("listing the pursuits: %w", err)
			}
			defer rows.Close()
			host, found, alive := d.hostPursuit()
			runs := hostRuns(host, found, alive)
			list := []pursuitRow{}
			for rows.Next() {
				var r pursuitRow
				var title, paused string
				var seen bool
				if err := rows.Scan(&r.Session, &title, &paused, &r.Waiting, &seen); err != nil {
					return err
				}
				r.Title = cleanAgentText(title)
				r.State = missionpursuit.StateOf(paused != "", r.Waiting, runs)
				r.Needs = missionpursuit.NeedsOf(paused != "", r.Waiting, seen, runs)
				list = append(list, r)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"schema_version": PursuitSchemaVersion, "profile": p, "pursuits": list})
			}
			w := cmd.OutOrStdout()
			printProfileLine(w, p)
			if len(list) == 0 {
				fmt.Fprintln(w, "no mission is in pursuit")
				return nil
			}
			for _, r := range list {
				fmt.Fprintf(w, "#%d  %-8s  needs %-4s", r.Session, r.State, r.Needs)
				if r.Waiting != "" {
					fmt.Fprintf(w, "  waiting: %s", r.Waiting)
				}
				fmt.Fprintf(w, "  (title, agent text: %q)\n", r.Title)
			}
			return nil
		},
	}
}
