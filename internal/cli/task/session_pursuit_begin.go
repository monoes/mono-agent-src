package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// checkerSetUp is the seam of the goal checker (S7b): until it exists no checker is set up, the consent block says
// {set_up:false}, and the digest covers it so that a checker appearing between preview and begin makes the begin stale. The
// command-check rules (a goal with command criteria needs the person-set unconfined switch) are added here when the checker
// and the switch exist and can be tested; nothing here runs a criterion.
func (d Deps) checkerSetUp() bool { return false }

// liveFacts reads the host-side facts of the consent block through the connection of the begin (or the preview): the host's
// published loop state, the budgets, the lead's pin and the cost table. shown receives the cost table the digest covers.
func (d Deps) liveFacts(profileID string, session int64, shown *coderhost.CostTable) missionpursuit.LiveFunc {
	return func(ctx context.Context, x tasks.Queryer, chat string, lim missionpursuit.AwayLimits) (missionpursuit.LiveFacts, error) {
		host, found, alive := d.hostPursuit()
		on := hostRuns(host, found, alive)
		// host.json is display-only: any process running as the user can write it. A value the host could not have accepted is
		// never shown as a fact; it blocks (host-info-invalid), and the table is then computed from the code defaults.
		invalid := on && !coderhost.ValidHostPursuitInfo(host)
		reported := on && !invalid
		var budget, spent float64
		if err := x.QueryRowContext(ctx, `SELECT budget_usd, spent_usd FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&budget, &spent); err != nil {
			return missionpursuit.LiveFacts{}, fmt.Errorf("reading the mission budget: %w", err)
		}
		leadCapped := false
		if chat != "" {
			var leadBudget float64
			if err := x.QueryRowContext(ctx, `SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, chat).Scan(&leadBudget); err == nil {
				leadCapped = missionpursuit.BudgetKnown(leadBudget, 0)
			}
		}
		wakeCap := coderhost.DefaultWakeCapUSD
		if reported {
			wakeCap = host.WakeCapUSD
		}
		table := coderhost.PursuitCostRows(coderhost.CostInput{WakeCapUSD: wakeCap, MissionBudgetUSD: budget, Pct: lim.Pct, Wakes: lim.Wakes, HostReported: reported})
		if shown != nil {
			*shown = table
		}
		f := missionpursuit.LiveFacts{Cost: table.Text, HostOn: on, HostInvalid: invalid, CheckerSetUp: d.checkerSetUp(),
			BudgetKnown: missionpursuit.BudgetKnown(budget, spent), LeadCapped: leadCapped}
		if reported {
			f.JudgePin, f.WakeCapUSD = host.Pin, host.WakeCapUSD
			if chat != "" {
				f.PinWhy = coderhost.LeadPinWhy(host.Pin, coderhost.LeadPinIn(ctx, x, profileID, chat))
			} else {
				f.PinWhy = "the lead's chat is not known"
			}
		}
		return f, nil
	}
}

func (d Deps) limitsFromFlags(cmd *cobra.Command, parked, pct, wakes int) (missionpursuit.AwayLimits, error) {
	if (cmd.Flags().Changed("parked-max") && parked == 0) || (cmd.Flags().Changed("pct") && pct == 0) || (cmd.Flags().Changed("wakes-max") && wakes == 0) {
		return missionpursuit.AwayLimits{}, d.InvalidInput("--parked-max, --pct and --wakes-max are 1 or more (leave them out for the defaults %d, %d and %d)",
			missionpursuit.DefaultParkedMax, missionpursuit.DefaultPct, missionpursuit.DefaultWakesMax)
	}
	return missionpursuit.AwayLimits{ParkedMax: parked, Pct: pct, Wakes: wakes}, nil
}

func addLimitFlags(cmd *cobra.Command, parked, pct, wakes *int) {
	cmd.Flags().IntVar(parked, "parked-max", 0, fmt.Sprintf("Stop after this many questions wait for you since you last acted (1 to %d; default %d)", missionpursuit.MaxParkedMax, missionpursuit.DefaultParkedMax))
	cmd.Flags().IntVar(pct, "pct", 0, fmt.Sprintf("Stop after this percent of the budget is spent since you last acted (1 to %d; default %d)", missionpursuit.MaxPct, missionpursuit.DefaultPct))
	cmd.Flags().IntVar(wakes, "wakes-max", 0, fmt.Sprintf("Stop after the pursuit woke the lead this many times since you last acted (1 to %d; default %d)", missionpursuit.MaxWakesMax, missionpursuit.DefaultWakesMax))
}

type previewCriterion struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Spec     string `json:"spec"`
	Expected string `json:"expected,omitempty"`
}

type previewBlocker struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (d Deps) newPursuitPreviewCmd() *cobra.Command {
	var parked, pct, wakes int
	cmd := &cobra.Command{
		Use:   "preview ID [--parked-max N] [--pct N] [--wakes-max N]",
		Short: "Show everything you consent to when a pursuit begins: the goal whole, the limits, the dollars, the host, what blocks it (read only)",
		Long: `Writes nothing and is not the person's alone: an agent may read it. It is the consent block that "pursuit begin" and the
app's start form are built from. It shows the goal (state, its 64-character hash and every criterion whole), the away limits, the
dollars table (what this can cost you, at worst), the checker (none is set up yet), the host (whether a running coder host has the
pursuit loop on, the judge's model and the wake-up cap), the blockers that would stop a begin (budget-unknown, lead-uncapped,
judge-pin, no-chat, host-off, host-info-invalid, no-rubric), the chat, the phrase the app asks you to type (as a template) and the consent digest:
a hash over those host-side facts, which "pursuit begin --consent" must still match when you begin.

The criteria are written by whoever proposed them, so they are agent text. The plain output writes any hidden character
(zero-width, direction, tag, control) as a \uXXXX escape. With --json the criteria are given as they are, and the verb refuses
(hidden_characters, criterion_too_long) a goal that has a hidden character or a criterion over ` + fmt.Sprint(missionpursuit.MaxDisplayRunes) + ` characters, because the app shows every criterion whole or not at all.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			lim, err := d.limitsFromFlags(cmd, parked, pct, wakes)
			if err != nil {
				return err
			}
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				var table coderhost.CostTable
				v, err := ps.Preview(ctx, p.ID, session, lim, d.liveFacts(p.ID, session, &table))
				if err != nil {
					return d.beginErr(err)
				}
				if d.JSONOutput() {
					if derr := missionpursuit.CheckDisplayable(v.Goal.Criteria); derr != nil {
						return d.beginErr(derr)
					}
				}
				return d.writePreview(cmd, p, session, v, table)
			})
		},
	}
	addLimitFlags(cmd, &parked, &pct, &wakes)
	return cmd
}

func (d Deps) writePreview(cmd *cobra.Command, p tasks.Profile, session int64, v missionpursuit.ConsentView, table coderhost.CostTable) error {
	f := v.Consent.Facts
	blockers := []previewBlocker{}
	for _, c := range v.Consent.Blockers {
		blockers = append(blockers, previewBlocker{Code: c, Reason: missionpursuit.BlockReasons[c]})
	}
	crit := make([]previewCriterion, 0, len(v.Goal.Criteria))
	for _, c := range v.Goal.Criteria {
		crit = append(crit, previewCriterion{ID: c.ID, Kind: c.Kind, Spec: c.Spec, Expected: c.Expected})
	}
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{
			"schema_version": PursuitSchemaVersion, "profile": p, "session_id": session, "chat_id": v.Chat,
			"goal":   map[string]any{"state": v.Goal.State, "hash": v.Goal.Hash, "criteria": crit},
			"limits": map[string]any{"parked_max": v.Limits.ParkedMax, "pct": v.Limits.Pct, "wakes_max": v.Limits.Wakes}, "rubric_present": !hasBlocker(v.Consent.Blockers, missionpursuit.BlockNoRubric),
			"cost":     table,
			"checker":  map[string]any{"set_up": f.CheckerSetUp},
			"host":     map[string]any{"loop_on": f.HostOn, "judge_pin": f.JudgePin, "wake_cap_usd": f.WakeCapUSD, "judge_call_cap_usd": coderhost.PursuitJudgeCallCapUSD},
			"blockers": blockers, "phrase_template": missionpursuit.PhraseTemplate, "consent_digest": v.Consent.Digest,
		})
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	fmt.Fprintf(w, "Beginning the pursuit of mission session #%d (chat %q) confirms the goal [%s] and locks it for good.\n  goal hash: %s\n", session, missionpursuit.EscapeHidden(v.Chat), v.Goal.State, v.Goal.Hash)
	for _, c := range v.Goal.Criteria {
		fmt.Fprintf(w, "  %s [%s] (written by whoever proposed it, not checked) %s\n", missionpursuit.EscapeHidden(c.ID), missionpursuit.EscapeHidden(c.Kind), strings.ReplaceAll(missionpursuit.EscapeHidden(c.Spec), "\n", "\n      "))
		if c.Expected != "" {
			fmt.Fprintf(w, "      expects: %s\n", missionpursuit.EscapeHidden(c.Expected))
		}
	}
	fmt.Fprintf(w, "  away limits: %d parked question(s), %d percent of the budget, %d wake-up(s) of the lead\n", v.Limits.ParkedMax, v.Limits.Pct, v.Limits.Wakes)
	fmt.Fprintln(w, "  goal checker: none is set up; the goal is never checked automatically and you decide when it is met")
	switch {
	case f.HostInvalid:
		fmt.Fprintln(w, "  host: the pursuit loop is reported ON, but its judge model or wake-up cap is not a usable value; the figures below use the code defaults")
	case f.HostOn:
		fmt.Fprintf(w, "  host (as reported by the host, which is not proof): the pursuit loop is ON; judge %q; a wake-up turn is capped at %s\n", f.JudgePin, coderhost.UsdText(f.WakeCapUSD))
	default:
		fmt.Fprintln(w, "  host: the pursuit loop is OFF (or no coder host is running)")
	}
	for _, line := range strings.Split(strings.TrimRight(table.Text, "\n"), "\n") {
		fmt.Fprintln(w, "  "+line)
	}
	if len(blockers) == 0 {
		fmt.Fprintln(w, "  nothing blocks it")
	}
	for _, b := range blockers {
		fmt.Fprintf(w, "  BLOCKED %s: %s\n", b.Code, b.Reason)
	}
	fmt.Fprintf(w, "  the app asks you to type: %s\n  consent digest: %s\n  begin with: pursuit begin %d --goal-hash %s --consent %s --chat %q\n",
		missionpursuit.PhraseTemplate, v.Consent.Digest, session, v.Goal.Hash, v.Consent.Digest, missionpursuit.EscapeHidden(v.Chat))
	return nil
}

func hasBlocker(bl []string, code string) bool {
	for _, b := range bl {
		if b == code {
			return true
		}
	}
	return false
}

// beginErr maps the errors of preview and begin to the codes the app reads.
func (d Deps) beginErr(err error) error {
	code := ""
	var extra map[string]any
	switch {
	case errors.Is(err, missionpursuit.ErrStale):
		code = "stale"
	case errors.Is(err, missionpursuit.ErrAlreadyStarted):
		code = "already_started"
	case errors.Is(err, missionpursuit.ErrConsentStale):
		code = "consent_stale"
	case errors.Is(err, missionpursuit.ErrChatMismatch):
		code = "chat_mismatch"
	case errors.Is(err, missionpursuit.ErrHiddenText):
		code = "hidden_characters"
	case errors.Is(err, missionpursuit.ErrCriterionTooLong):
		code = "criterion_too_long"
	default:
		if codes, ok := missionpursuit.IsBlocked(err); ok {
			code, extra = "blocked", map[string]any{"blockers": codes}
		}
	}
	if code == "" {
		return d.taskErr(err)
	}
	return taskCLIError{error: d.InvalidInput("%v", err), code: code, extra: extra}
}

func (d Deps) newPursuitBeginCmd() *cobra.Command {
	var parked, pct, wakes int
	var goalHash, consent, chat, guiPhrase string
	cmd := &cobra.Command{
		Use:   "begin ID --goal-hash HEX64 --consent HEX64 --chat CONV [--parked-max N] [--pct N] [--wakes-max N]",
		Short: "Confirm the goal and start the pursuit in one step, for exactly what pursuit preview showed (you only)",
		Long: `Confirms the proposed goal (or accepts one you confirmed already) and starts the pursuit in ONE transaction: no crash can
leave a confirmed goal with no pursuit. Only you may; an agent is refused. You must name what you read: --goal-hash is the
goal's full hash, --consent the consent digest and --chat the mission's chat, all from "pursuit preview" with the same limits.
It refuses, writing nothing, when the goal changed (stale), the host-side facts changed since the preview (consent_stale: the judge,
the host switch, the costs), the pursuit already started (already_started), the chat is not the mission's (chat_mismatch) or
something blocks it (blocked: budget-unknown, lead-uncapped, judge-pin, no-chat, host-off, host-info-invalid, no-rubric). The goal becomes final.
"goal confirm" and "pursuit start" still work as before. --json gives {profile, pursuit, goal, via}.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "begin a mission's pursuit")
			if err != nil {
				return err
			}
			lim, err := d.limitsFromFlags(cmd, parked, pct, wakes)
			if err != nil {
				return err
			}
			for name, v := range map[string]string{"--goal-hash": goalHash, "--consent": consent} {
				if !isHex64(v) {
					return d.InvalidInput("%s must be the 64 hexadecimal characters that pursuit preview showed", name)
				}
			}
			gui := cmd.Flags().Changed("gui-phrase")
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				if gui {
					// the phrase is derived again from what the person must have read (limits in range first); the goal hash is
					// held to the live goal below
					resolved, rerr := lim.Resolved()
					if rerr != nil {
						return d.taskErr(rerr)
					}
					want := missionpursuit.Phrase(session, goalHash, resolved)
					if strings.TrimSpace(guiPhrase) != want {
						return d.InvalidInput("the phrase does not match; nothing started")
					}
				}
				pu, g, err := ps.ConfirmAndStart(ctx, p.ID, session, missionpursuit.BeginRequest{
					GoalHash: goalHash, Consent: consent, Chat: chat, Limits: lim, GUI: gui, Live: d.liveFacts(p.ID, session, nil),
				}, actor)
				if err != nil {
					return d.beginErr(err)
				}
				via := "terminal"
				if gui {
					via = "gui"
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "pursuit": pu, "goal": g, "via": via})
				}
				return d.writeBegun(cmd.OutOrStdout(), p, pu, g, via)
			})
		},
	}
	addLimitFlags(cmd, &parked, &pct, &wakes)
	cmd.Flags().StringVar(&goalHash, "goal-hash", "", "The goal's full hash, as pursuit preview showed it (64 hexadecimal characters)")
	cmd.Flags().StringVar(&consent, "consent", "", "The consent digest pursuit preview showed (64 hexadecimal characters)")
	cmd.Flags().StringVar(&chat, "chat", "", "The mission's chat, as pursuit preview showed it")
	cmd.Flags().StringVar(&guiPhrase, "gui-phrase", "", "")
	_ = cmd.Flags().MarkHidden("gui-phrase")
	return cmd
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (d Deps) writeBegun(w io.Writer, p tasks.Profile, pu missionpursuit.Pursuit, g missionpursuit.Goal, via string) error {
	printProfileLine(w, p)
	fmt.Fprintf(w, "Began the pursuit of mission session #%d (via %s): the goal is %s, hash %s\n  started by %s; away limits: %d parked question(s), %d%% of the budget (metered spend) or %d wake-up(s) of the lead since you last acted\n",
		pu.SessionID, via, g.State, g.Hash, pu.LockedBy, pu.ParkedMax, pu.Pct, pu.WakesMax)
	return nil
}
