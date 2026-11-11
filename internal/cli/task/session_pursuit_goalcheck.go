package task

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// goalCheckStub is the goal-check block of the status document. Keys are only added within a schema version: available and note
// are the first two. Everything here is host-measured (the host's published state and the host's own check rows): no agent text.
type goalCheckStub struct {
	Available bool   `json:"available"` // goal checks are set up on a running host
	Note      string `json:"note"`
	// Verifier is off (the default), on, not-set-up (asked for, with the reason in Refusal) or host-not-running.
	Verifier       string           `json:"verifier"`
	CheckerPin     string           `json:"checker_pin,omitempty"`
	Confinement    string           `json:"confinement,omitempty"`
	Refusal        string           `json:"refusal,omitempty"` // why checks refuse: the missing piece, in plain words
	ChecksWindow   int              `json:"checks_since_you_acted"`
	ChecksWindowOf int              `json:"checks_window_max"`
	ChecksSince    int              `json:"checks_since_last_verdict"`
	ChecksSinceOf  int              `json:"checks_lifetime_max"`
	HostChecksHour int              `json:"host_checks_last_hour"`
	HostChecksOf   int              `json:"host_checks_hour_max"`
	SpendUSD       float64          `json:"spend_since_you_acted_usd"`
	CheckCapUSD    float64          `json:"check_cap_usd"`
	Latest         *goalCheckLatest `json:"latest,omitempty"`
}

type goalCheckLatest struct {
	ID        int64  `json:"id"`
	State     string `json:"state"`
	Outcome   string `json:"outcome,omitempty"`
	Reason    string `json:"reason,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

// readGoalCheck builds the block from the host's published state and the mission's check rows.
func readGoalCheck(ctx context.Context, db *sql.DB, session int64, host coderhost.PursuitHostInfo, found, alive bool) goalCheckStub {
	g := goalCheckStub{ChecksWindowOf: missionpursuit.MaxChecksPerAwayWindow, ChecksSinceOf: missionpursuit.MaxChecksPerMissionLifetime,
		HostChecksOf: missionpursuit.MaxHostChecksPerHour, CheckCapUSD: float64(missionpursuit.CheckCapMicro) / 1e6}
	switch {
	case host.VerifierOn && found && alive:
		g.Verifier, g.Available, g.CheckerPin, g.Confinement = "on", true, host.VerifierPin, host.Confinement
		g.Note = "goal checks are ON: when the work is settled the host runs the machine criteria of the locked goal on a copy of the project, a checker model that sees only the host's measurements may block but never approve, and the criteria that are yours wait for your answer (task session goal check / goal answer); accepting stays yours"
	case host.VerifierOn:
		g.Verifier, g.CheckerPin, g.Confinement = "host-not-running", host.VerifierPin, host.Confinement
		g.Note = "goal checks were ON when the coder host last ran; no coder host is running, so nothing is checked"
	case host.VerifierPin != "":
		g.Verifier, g.CheckerPin, g.Refusal = "not-set-up", host.VerifierPin, host.VerifierReason
		g.Note = "goal checks were asked for and are not set up: " + host.VerifierReason
	default:
		g.Verifier = "off"
		g.Note = "goal checks are OFF (the default): the goal is never checked automatically; you decide when the goal is met and close the mission (set MONOAGENT_PURSUIT_VERIFIER, with the judge flag, and restart the coder host to turn them on)"
	}
	if g.Verifier == "on" && (strings.HasPrefix(host.Confinement, "none:") || strings.HasPrefix(host.Confinement, "BROKEN")) {
		g.Refusal = "project commands are not run here: " + host.Confinement
	}
	if cs, err := missionpursuit.ReadCheckStatus(ctx, db, session, time.Now()); err == nil {
		g.ChecksWindow, g.ChecksSince, g.HostChecksHour = cs.Window.Count, cs.Facts.SinceVerdict, cs.HostHour
		g.SpendUSD = float64(cs.Window.Micro) / 1e6
		if f := cs.Facts; f.HaveLatest {
			g.Latest = &goalCheckLatest{ID: f.LatestID, State: f.LatestState, Outcome: f.LatestOutcome, Reason: missionpursuit.Clean(f.LatestReason, 200)}
		}
	}
	return g
}

// writeGoalCheck prints the block as plain words.
func writeGoalCheck(w io.Writer, g goalCheckStub) {
	fmt.Fprintf(w, "  goal check: %s\n", g.Note)
	if g.Verifier == "off" {
		return
	}
	if g.CheckerPin != "" {
		fmt.Fprintf(w, "    checker %s; programs of command criteria: %s\n", g.CheckerPin, orNone(g.Confinement))
	}
	if g.Refusal != "" {
		fmt.Fprintf(w, "    checks refuse: %s\n", g.Refusal)
	}
	fmt.Fprintf(w, "    since you last acted: %d of %d check(s), $%.2f (a running check, one that was abandoned and one whose cost the runtime did not report count at $%.2f); since the last accept or reject: %d of %d; this host, last hour: %d of %d\n",
		g.ChecksWindow, g.ChecksWindowOf, g.SpendUSD, g.CheckCapUSD, g.ChecksSince, g.ChecksSinceOf, g.HostChecksHour, g.HostChecksOf)
	if l := g.Latest; l != nil {
		fmt.Fprintf(w, "    latest check #%d: %s", l.ID, l.State)
		if l.Outcome != "" {
			fmt.Fprintf(w, ", %s", l.Outcome)
		}
		if l.Reason != "" {
			fmt.Fprintf(w, " (%s)", l.Reason)
		}
		fmt.Fprintln(w)
	}
}
