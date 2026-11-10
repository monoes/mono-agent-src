package task

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// PursuitSchemaVersion is the version of the status and list JSON documents: keys are added, never renamed or removed,
// within one version.
const PursuitSchemaVersion = 1

// Agent text (a direction title the judge proposed, a mission title) is cut to this many characters wherever it is shown.
const agentTextRunes = 80

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

// readStrategy reads the epochs and directions of a mission and keeps the direction titles (agent text) apart.
func readStrategy(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64, pr missionpursuit.Progress) (missionpursuit.StrategyView, agentText, error) {
	v, titles, err := ps.StrategyView(ctx, p.ID, session, pr)
	at := agentText{Directions: []agentDirection{}}
	for _, t := range titles {
		at.Directions = append(at.Directions, agentDirection{ID: t.ID, State: t.State, Title: cleanAgentText(t.Title)})
	}
	return v, at, err
}

// hostRuns says whether a live coder host runs the pursuit loop.
func hostRuns(h coderhost.PursuitHostInfo, found, alive bool) bool {
	return found && alive && h.Enabled
}

// writeStatus prints the status: the JSON document (schema_version, the progress block with host facts only, the strategy
// block, the goal-check stub, and the agent text kept apart) or the plain words.
func (d Deps) writeStatus(cmd *cobra.Command, p tasks.Profile, pu missionpursuit.Pursuit, pr missionpursuit.Progress, st missionpursuit.StrategyView, at agentText) error {
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

func writeStrategyLines(w io.Writer, st missionpursuit.StrategyView, at agentText) {
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
	for _, s := range missionpursuit.DirectionStates {
		parts = append(parts, fmt.Sprintf("%d %s", st.Directions.Counts[s], s))
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
