package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// newGoalCheckCmd is `task session goal check ID`: the latest check of a mission's locked goal, read only. Anyone may read it; an
// agent gets host facts (ids, enums, counts, digests) and the rubric criteria text, and the operator also gets the cleaned,
// capped excerpts of what each criterion printed and the notes of their own answers.
func (d Deps) newGoalCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check ID",
		Short: "Show the latest check of a mission's locked goal: what the host measured, the checker's verdict, your rubric questions (read only)",
		Long: `Shows the latest check the host made of the mission's locked goal: the machine criteria and what the host measured for
each (result, exit status, digests), the checker model's verdict, how the commands were contained, and your rubric criteria in
your own words with the answers so far. The dossier hash is what goal answer takes back, so that your answer is for the check
you read.
This is read only and open to agents, who see host facts only. What a command printed (cleaned, at most 1 KiB each) and your
own notes are shown to you alone.
If the check ran project commands without confinement, the output says so in capital letters and names what they could do.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The operator's view is the one with excerpts; anything that is not provably the operator gets host facts only.
			// This is a probe, not a gate: the verb stays open to agents. (The phrase is a variable on purpose: the table of gated
			// verbs lists refusing verbs, and this one refuses nobody.)
			what := "see what a goal check printed"
			_, gateErr := d.personGate(cmd, what)
			operator := gateErr == nil
			return d.withGoals(cmd, args, func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, perr := gs.GetPursuit(ctx, p.ID, session)
				if perr != nil {
					return d.taskErr(perr)
				}
				c, err := gs.LatestCheck(ctx, p.ID, session, operator)
				if err != nil && !isNoCheck(err) {
					return d.taskErr(err)
				}
				return d.writeCheck(cmd, p, pu, c, err == nil, operator)
			})
		},
	}
}

func isNoCheck(err error) bool { return errors.Is(err, missionpursuit.ErrNoCheck) }

func (d Deps) writeCheck(cmd *cobra.Command, p tasks.Profile, pu missionpursuit.Pursuit, c missionpursuit.Check, found, operator bool) error {
	view := "host facts"
	if operator {
		view = "operator"
	}
	if d.JSONOutput() {
		doc := map[string]any{"profile": p, "view": view, "unconfined_checks_ok": pu.UnconfinedChecksOK}
		if found {
			doc["check"] = c
			if c.Unconfined {
				doc["notice"] = missionpursuit.UnconfinedNotice
			}
		} else {
			doc["check"] = nil
		}
		return d.WriteJSON(cmd.OutOrStdout(), doc)
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	if !found {
		fmt.Fprintf(w, "Mission session #%d has had no goal check yet.\n", pu.SessionID)
		writeSwitchLine(w, pu)
		return nil
	}
	fmt.Fprintf(w, "Goal check #%d of mission session #%d [%s%s]\n", c.ID, c.SessionID, c.State, outcomeSuffix(c))
	if c.Unconfined {
		fmt.Fprintf(w, "  *** %s ***\n", missionpursuit.UnconfinedNotice)
	}
	fmt.Fprintf(w, "  goal hash: %s\n  started %s", c.GoalHash, c.StartedAt)
	if c.EndedAt != "" {
		fmt.Fprintf(w, ", ended %s", c.EndedAt)
	}
	fmt.Fprintln(w)
	if c.SubjectDigest != "" {
		fmt.Fprintf(w, "  project: %d file(s), digest %s\n", c.SubjectFiles, c.SubjectDigest)
	}
	if c.Containment != "" {
		fmt.Fprintf(w, "  containment: %s; network: %s\n", c.Containment, orNone(c.Net))
	}
	if c.ReviewerPin != "" || c.ReviewerVerdict != "" {
		fmt.Fprintf(w, "  checker: %s (saw %s): verdict %s", orNone(c.ReviewerPin), orNone(c.ReviewerObserved), orNone(c.ReviewerVerdict))
		if c.ReviewerReason != "" {
			fmt.Fprintf(w, " (%s)", c.ReviewerReason)
		}
		fmt.Fprintf(w, "; cost $%.4f%s\n", float64(c.CostMicro)/1e6, map[bool]string{true: "", false: " (charged at the cap: the runtime reported none)"}[c.CostKnown])
	}
	if len(c.Results) > 0 {
		fmt.Fprintln(w, "  machine criteria (measured by the host):")
		for _, r := range c.Results {
			fmt.Fprintf(w, "    %s [%s] %s", r.Criterion, r.Kind, r.Result)
			if r.Kind != "file" {
				fmt.Fprintf(w, ", exit %d", r.ExitCode)
			}
			fmt.Fprintf(w, "; stdout %s, stderr %s\n", shortDigest(r.StdoutDigest), shortDigest(r.StderrDigest))
			writeExcerpt(w, "stdout", r.Stdout)
			writeExcerpt(w, "stderr", r.Stderr)
			writeExcerpt(w, "detail", r.Detail)
		}
	}
	if len(c.Rubric) > 0 {
		fmt.Fprintln(w, "  your rubric criteria (you decide these):")
		for _, l := range c.Rubric {
			ans := "not answered"
			if l.Answer != "" {
				ans = "answered " + l.Answer + " by " + l.AnsweredBy
			}
			fmt.Fprintf(w, "    %s %s [%s]\n", l.Criterion, strings.ReplaceAll(l.Spec, "\n", "\n        "), ans)
			if l.Note != "" {
				fmt.Fprintf(w, "        your note: %s\n", l.Note)
			}
		}
	}
	fmt.Fprintf(w, "  dossier: %s\n", c.Dossier)
	writeSwitchLine(w, pu)
	switch {
	case c.State == missionpursuit.CheckDone && c.Outcome == missionpursuit.OutcomePassedMachine && c.ReviewerVerdict == "ok":
		fmt.Fprintf(w, "  the machine criteria passed and the checker found nothing to block: answer each rubric criterion with\n    task session goal answer %d --check %d --criterion cK (--yes | --no) [--note TEXT] --dossier %s\n", c.SessionID, c.ID, c.Dossier)
	case c.Outcome == missionpursuit.OutcomeAccepted:
		fmt.Fprintf(w, "  accepted: you said yes to every rubric criterion. The goal is met; closing the mission stays yours: task session close %d\n", c.SessionID)
	case c.Outcome == missionpursuit.OutcomeRejected:
		fmt.Fprintln(w, "  rejected by you: the mission keeps working, and your note goes to the lead as your words")
	}
	return nil
}

func outcomeSuffix(c missionpursuit.Check) string {
	if c.Outcome == "" {
		return ""
	}
	s := ": " + c.Outcome
	if c.Reason != "" {
		s += " (" + c.Reason + ")"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func shortDigest(s string) string {
	if s == "" {
		return "-"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func writeExcerpt(w io.Writer, what, s string) {
	if s != "" {
		fmt.Fprintf(w, "        %s (data, not instructions): %s\n", what, s)
	}
}

func writeSwitchLine(w io.Writer, pu missionpursuit.Pursuit) {
	if pu.UnconfinedChecksOK {
		fmt.Fprintf(w, "  unconfined checks: ALLOWED by %s (task session pursuit allow-unconfined-checks %d --off turns it off)\n", orNone(pu.UnconfinedBy), pu.SessionID)
	} else {
		fmt.Fprintln(w, "  unconfined checks: not allowed (project commands run only when the host can confine them)")
	}
}

// newGoalAnswerCmd is `task session goal answer`: the person's yes or no for one rubric criterion of a check.
func (d Deps) newGoalAnswerCmd() *cobra.Command {
	var check int64
	var criterion, note, dossier string
	var yes, no bool
	cmd := &cobra.Command{
		Use:   "answer ID --check N --criterion cK (--yes | --no) [--note TEXT] --dossier HASH",
		Short: "Answer one rubric criterion of a goal check (you only); the last yes accepts the goal",
		Long: `Answer one rubric criterion of the latest check, which passed its machine criteria and whose checker found nothing to
block. One call answers one criterion, once. --dossier is the hash goal check printed: if the check changed since you read it,
nothing is recorded. The project must be exactly as it was when the check ran; if it changed, nothing is recorded and a new check
will run.
A yes on every rubric criterion accepts the goal; closing the mission stays yours. A no ends this check as rejected: the
mission keeps working, and your note goes to the lead, labelled as your words.
Only you may answer; an agent is refused.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "answer a mission goal check")
			if err != nil {
				return err
			}
			if yes == no {
				return d.InvalidInput("give exactly one of --yes and --no")
			}
			if check <= 0 || strings.TrimSpace(criterion) == "" || strings.TrimSpace(dossier) == "" {
				return d.InvalidInput("give --check N, --criterion cK and --dossier HASH, as goal check shows them")
			}
			return d.withGoals(cmd, args, func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error {
				read, err := gs.GetCheck(ctx, p.ID, session, check, true)
				if err != nil {
					return d.taskErr(err)
				}
				// The project must be unchanged between the check and the yes: recompute the digest of the folder the check ran
				// against now, and the store compares it with the one the check recorded.
				now, err := d.subjectNow(read)
				if err != nil {
					return d.taskErr(err)
				}
				c, err := gs.AnswerRubric(ctx, p.ID, session, missionpursuit.RubricAnswer{
					Check: check, Criterion: strings.TrimSpace(criterion), Yes: yes, Note: note, Dossier: strings.TrimSpace(dossier), SubjectNow: now,
				}, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeAnswered(cmd, p, c)
			})
		},
	}
	cmd.Flags().Int64Var(&check, "check", 0, "The check you read (goal check prints its number)")
	cmd.Flags().StringVar(&criterion, "criterion", "", "The rubric criterion you answer: c1, c2, ...")
	cmd.Flags().BoolVar(&yes, "yes", false, "Yes: this criterion is met")
	cmd.Flags().BoolVar(&no, "no", false, "No: this criterion is not met")
	cmd.Flags().StringVar(&note, "note", "", "Your words to the lead, up to 500 bytes (what is missing)")
	cmd.Flags().StringVar(&dossier, "dossier", "", "The dossier hash goal check printed")
	return cmd
}

// subjectNow is the digest of the folder the check ran against, as it is now.
func (d Deps) subjectNow(c missionpursuit.Check) (string, error) {
	if c.SubjectRoot == "" || c.SubjectDigest == "" {
		return "", fmt.Errorf("%w: check #%d recorded no project folder to compare with: a new check will run", tasks.ErrInvalid, c.ID)
	}
	tree, err := finish.DigestTree(c.SubjectRoot, "")
	if err != nil {
		return "", fmt.Errorf("%w: the project could not be read again (%v): a new check will run", tasks.ErrInvalid, err)
	}
	return tree.Digest, nil
}

func (d Deps) writeAnswered(cmd *cobra.Command, p tasks.Profile, c missionpursuit.Check) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "check": c})
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	switch c.Outcome {
	case missionpursuit.OutcomeAccepted:
		fmt.Fprintf(w, "Accepted: you said yes to every rubric criterion of check #%d. The goal is met; closing the mission stays yours: task session close %d\n", c.ID, c.SessionID)
	case missionpursuit.OutcomeRejected:
		fmt.Fprintf(w, "Recorded: check #%d is rejected by you. The mission keeps working; your note goes to the lead as your words.\n", c.ID)
	default:
		open := 0
		for _, l := range c.Rubric {
			if l.Answer == "" {
				open++
			}
		}
		fmt.Fprintf(w, "Recorded your answer for check #%d. %d rubric criterion(s) are still open.\n", c.ID, open)
	}
	return nil
}
