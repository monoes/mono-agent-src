package task

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The readiness items, in the order they are shown. Each is "ok" or "missing"; a missing one carries the exact fix.
const (
	ItemHostLoop      = "host_loop"
	ItemJudgePin      = "judge_pin"
	ItemGoal          = "goal"
	ItemChecker       = "checker"
	ItemMissionBudget = "mission_budget"
	ItemLeadCap       = "lead_cap"
	ItemChat          = "chat"
	ItemConsent       = "consent"
	ItemAwayLimits    = "away_limits"
)

const (
	itemOK      = "ok"
	itemMissing = "missing"
)

const hostFix = "start the coder host with MONOAGENT_PURSUIT_JUDGE=<runtime/model> set in its environment (a model other than the lead's), e.g. `MONOAGENT_PURSUIT_JUDGE=<runtime/model> monoagentcli coder host run`; if a host is running, `monoagentcli coder host stop` first, because the flag is read once at start"

// readinessItem is one line of the checklist. The JSON keys are stable within PursuitSchemaVersion.
type readinessItem struct {
	ID     string `json:"id"`
	Status string `json:"status"` // ok | missing
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"` // the exact command or setting, only when missing
}

// readinessPursuit is the pursuit block: only the host-written facts the existing status already reports.
type readinessPursuit struct {
	Started bool   `json:"started"`
	State   string `json:"state,omitempty"`
	Waiting string `json:"waiting,omitempty"`
	Why     string `json:"why,omitempty"`
	Needs   string `json:"needs,omitempty"`
	Next    string `json:"next,omitempty"`
}

// readinessDoc is the `pursuit readiness --json` document.
type readinessDoc struct {
	SchemaVersion int              `json:"schema_version"`
	Profile       tasks.Profile    `json:"profile"`
	SessionID     int64            `json:"session_id"`
	Ready         bool             `json:"ready"` // every item but consent is ok (consent is the operator's act)
	Blockers      []string         `json:"blockers"`
	Items         []readinessItem  `json:"items"`
	Pursuit       readinessPursuit `json:"pursuit"`
}

// readinessInput is everything buildReadiness decides from; the command fills it, a test injects it.
type readinessInput struct {
	Session   int64
	View      missionpursuit.Readiness
	Host      coderhost.PursuitHostInfo
	HostFound bool
	HostAlive bool
	Progress  missionpursuit.Progress
}

// buildReadiness turns the consent blockers (the one source of truth) and the host facts into the checklist. It decides nothing
// itself: an item is missing exactly when its blocker is in the consent block.
func buildReadiness(in readinessInput) readinessDoc {
	c := in.View.Consent
	blocked := func(code string) bool { return hasBlocker(c.Blockers, code) }
	doc := readinessDoc{SchemaVersion: PursuitSchemaVersion, SessionID: in.Session, Blockers: append([]string{}, c.Blockers...)}
	add := func(id string, missing bool, okText, missText, fix string) {
		it := readinessItem{ID: id, Status: itemOK, Detail: okText}
		if missing {
			it = readinessItem{ID: id, Status: itemMissing, Detail: missText, Fix: fix}
		}
		doc.Items = append(doc.Items, it)
	}
	hostOff := blocked(missionpursuit.BlockHostOff)
	invalid := blocked(missionpursuit.BlockHostInfoInvalid)
	fix := hostFix
	if invalid {
		fix = "restart the coder host (`monoagentcli coder host stop`, then start it) with MONOAGENT_PURSUIT_JUDGE=<runtime/model> and MONOAGENT_PURSUIT_WAKE_CAP_USD, if set, in range: what it published is not a value it would have accepted"
	}
	add(ItemHostLoop, hostOff || invalid, "a running coder host has the pursuit loop on", hostLoopWhy(in, invalid), fix)
	pinMissing := hostOff || blocked(missionpursuit.BlockJudgePin)
	pinText := "the judge model " + c.Facts.JudgePin + " differs from the lead's"
	pinMiss, pinFix := missionpursuit.BlockReasons[missionpursuit.BlockJudgePin], "set MONOAGENT_PURSUIT_JUDGE on the host to a model other than the lead's, then restart the host"
	if hostOff {
		pinMiss, pinFix = "not known until the host loop is on", "fix host_loop first"
	} else if c.Facts.PinWhy != "" {
		pinMiss = c.Facts.PinWhy
	}
	add(ItemJudgePin, pinMissing, pinText, pinMiss, pinFix)
	g := in.View.Goal
	goalMiss := "this mission has no goal yet"
	if in.View.GoalFound {
		goalMiss = missionpursuit.BlockReasons[missionpursuit.BlockNoRubric]
	}
	add(ItemGoal, blocked(missionpursuit.BlockNoRubric), fmt.Sprintf("goal %s with %d criteria (hash %.8s)", g.State, len(g.Criteria), g.Hash), goalMiss,
		fmt.Sprintf("ask the lead to propose one (its mission_goal_propose tool), or propose it yourself with at least one rubric criterion: `monoagentcli task session goal propose %d --criteria FILE`", in.Session))
	needsChecker := false
	for _, cr := range g.Criteria {
		needsChecker = needsChecker || cr.Kind != missionpursuit.KindRubric
	}
	checkerMiss := "the goal has command checks and no goal checker is set up on the host"
	if in.Host.VerifierReason != "" {
		checkerMiss += ": " + in.Host.VerifierReason
	}
	checkerOK := "the goal has no command checks, so no checker is needed"
	if needsChecker {
		checkerOK = "the host has a goal checker (" + in.Host.VerifierPin + ")"
	}
	add(ItemChecker, needsChecker && !c.Facts.CheckerSetUp, checkerOK, checkerMiss,
		"set MONOAGENT_PURSUIT_VERIFIER=<runtime/model> (a model other than the judge's and the lead's) on the host and restart it; or re-propose the goal with rubric criteria only")
	add(ItemMissionBudget, blocked(missionpursuit.BlockBudgetUnknown), "the mission has a known budget", missionpursuit.BlockReasons[missionpursuit.BlockBudgetUnknown],
		fmt.Sprintf("`monoagentcli task session budget %d --usd N`", in.Session))
	add(ItemLeadCap, blocked(missionpursuit.BlockLeadUncapped), "the lead's chat has a spend cap", missionpursuit.BlockReasons[missionpursuit.BlockLeadUncapped],
		fmt.Sprintf("`monoagentcli coder sessions settings set %s --budget-usd N`", in.View.Chat))
	add(ItemChat, blocked(missionpursuit.BlockNoChat), "the mission has a chat to wake", missionpursuit.BlockReasons[missionpursuit.BlockNoChat],
		"run the work from a mission chat (a mission session with no chat cannot be pursued)")
	started := in.View.Pursuit != nil
	add(ItemConsent, !started, fmt.Sprintf("the pursuit was begun by %s", locker(in.View.Pursuit)), "you have not begun it: the consent (a typed phrase in the app, or the digest of `pursuit preview`) is yours alone",
		fmt.Sprintf("once the items above are ok, run `monoagentcli task session pursuit preview %d`, read it, and begin with the line it prints (`pursuit begin`; an agent is refused)", in.Session))
	lim := in.View.Limits
	doc.Items = append(doc.Items, readinessItem{ID: ItemAwayLimits, Status: itemOK, Fix: "",
		Detail: fmt.Sprintf("while you are away it stops after %d parked question(s), %d%% of the budget spent or %d wake-up(s) of the lead (the host also caps %d wake-ups and %d judge calls an hour); fixed once begun, set at begin with --parked-max, --pct and --wakes-max",
			lim.ParkedMax, lim.Pct, lim.Wakes, missionpursuit.MaxHostWakesPerHour, missionpursuit.MaxHostJudgeCallsPerHour)})
	doc.Ready = true
	for _, it := range doc.Items {
		doc.Ready = doc.Ready && (it.Status == itemOK || it.ID == ItemConsent)
	}
	if started {
		runs := hostRuns(in.Host, in.HostFound, in.HostAlive)
		pu, pr := *in.View.Pursuit, in.Progress
		doc.Pursuit = readinessPursuit{Started: true, State: missionpursuit.StateOf(pu.Paused(), pr.Waiting, runs), Waiting: pr.Waiting, Why: pr.Why,
			Needs: missionpursuit.NeedsOf(pu.Paused(), pr.Waiting, pr.Seen, runs), Next: missionpursuit.NextLine(pu.Paused(), pr.Waiting, pr.Seen, runs)}
	}
	return doc
}

func locker(p *missionpursuit.Pursuit) string {
	if p == nil {
		return ""
	}
	return p.LockedBy
}

// hostLoopWhy says in words why the host does not run the loop, from the same published facts `pursuit status` reads.
func hostLoopWhy(in readinessInput, invalid bool) string {
	switch {
	case invalid:
		return "the host published a judge model or wake-up cap that is not a usable value"
	case !in.HostFound:
		return "no coder host has published its pursuit state (none is running, or it is older than this field)"
	case !in.HostAlive:
		return "the coder host is not running"
	case in.Host.Reason != "":
		return "the coder host runs with the pursuit loop OFF: " + in.Host.Reason
	}
	return "the coder host runs with the pursuit loop OFF (MONOAGENT_PURSUIT_JUDGE was not set when it started)"
}

func writeReadinessText(w io.Writer, p tasks.Profile, doc readinessDoc) {
	printProfileLine(w, p)
	verdict := "NOT ready"
	if doc.Ready {
		verdict = "ready"
	}
	fmt.Fprintf(w, "Pursuit readiness of mission session #%d: %s\n", doc.SessionID, verdict)
	for _, it := range doc.Items {
		fmt.Fprintf(w, "  [%s] %s: %s\n", it.Status, it.ID, it.Detail)
		if it.Fix != "" {
			fmt.Fprintf(w, "      fix: %s\n", it.Fix)
		}
	}
	if pu := doc.Pursuit; pu.Started {
		fmt.Fprintf(w, "  pursuit: %s; needs: %s\n  next: %s\n", pu.State, pu.Needs, pu.Next)
	}
}

func (d Deps) newPursuitReadinessCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "readiness ID",
		Short: "List what is missing for a mission's pursuit to run, each item with its exact fix (read only)",
		Long: `Answers "what is missing for this mission's pursuit to run?". It reads and writes nothing, calls no model and starts nothing; an agent may
run it. Each item is ok or missing, and a missing one carries the exact command or setting that fixes it: the host switch
(MONOAGENT_PURSUIT_JUDGE on a running host, read once at its start), the judge's model differing from the lead's, a goal with a rubric
criterion, the goal checker (only when the goal has command checks), the mission's budget, the lead's spend cap, the chat, and your own
consent. The blockers are the ones "pursuit preview" and "pursuit begin" refuse for, from the same function. Once the pursuit has begun
the document also carries its state, why it waits and who has to act, as "pursuit status" does.

--json gives {schema_version, profile, session_id, ready, blockers[], items[{id, status, detail, fix?}], pursuit{started, state?, waiting?, why?, needs?, next?}}.
"ready" is true when every item but consent is ok. Keys are added, never renamed or removed, within one schema_version.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				v, err := ps.Readiness(ctx, p.ID, session, missionpursuit.AwayLimits{}, d.liveFacts(p.ID, session, nil))
				if err != nil {
					return d.beginErr(err)
				}
				host, found, alive := d.hostPursuit()
				in := readinessInput{Session: session, View: v, Host: host, HostFound: found, HostAlive: alive}
				if v.Pursuit != nil {
					if in.Progress, err = ps.GetProgress(ctx, p.ID, session); err != nil {
						return d.taskErr(err)
					}
				}
				doc := buildReadiness(in)
				doc.Profile = p
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), doc)
				}
				writeReadinessText(cmd.OutOrStdout(), p, doc)
				return nil
			})
		},
	}
}

// noPursuitErr is the plain-words refusal of a pursuit read for a mission that has no pursuit yet.
func (d Deps) noPursuitErr(err error) error {
	if !errors.Is(err, missionpursuit.ErrNotStarted) {
		return d.taskErr(err)
	}
	return taskCLIError{error: d.NotFound("this mission has no pursuit yet; run `pursuit readiness` to see what is needed"), code: "not_found"}
}
