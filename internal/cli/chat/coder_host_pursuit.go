package chat

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgemodel"
)

// The host's switch for the pursuit loop. Two host settings, both read once, when the host starts:
//
//	MONOAGENT_PURSUIT_JUDGE         the judge's pin, runtime/model[@version]. Unset or empty: the saved pin
//	                                (`coder host pursuit judge set`), else the loop is ON but idle ("judge-pin" missing:
//	                                nothing is judged or spent) and the check-ins go on as before.
//	MONOAGENT_PURSUIT_WAKE_CAP_USD  X, the most one wake-up turn of a lead may spend; unset or empty = 2; otherwise 0.01 to 10.
//
// There is no live switch: turning the loop off host-wide is `coder host pursuit off` (or MONOAGENT_PURSUIT_LOOP=off) and a restart. A value that does
// not parse leaves the loop OFF, with the reason shown in `coder host status` and the pursuit status.
const (
	envPursuitJudge   = "MONOAGENT_PURSUIT_JUDGE"
	envPursuitWakeCap = "MONOAGENT_PURSUIT_WAKE_CAP_USD"
	// MONOAGENT_PURSUIT_LOOP=off (or 0, false) is the operator's explicit OFF; the saved row `coder host pursuit off` is the other.
	envPursuitLoop = "MONOAGENT_PURSUIT_LOOP"
	// MONOAGENT_PURSUIT_VERIFIER is the goal checker's pin, runtime/model[@version], and turns the goal checks on. Unset or empty
	// (the default): they are OFF and nothing of them is built. It works only with the judge flag above, is read once when
	// the host starts, and has no live off (unset it and restart). See coderhost/pursuit_check.go.
	envPursuitVerifier = "MONOAGENT_PURSUIT_VERIFIER"
)

// verifierBuilder is coderhost.NewVerifierDeps; a variable so that a test can show it is not called while the flag is off.
var verifierBuilder = coderhost.NewVerifierDeps

// attachVerifier turns the goal checks on in the deps when the flag asks for them and the loop is on. With the flag unset it
// does nothing at all (nothing is built, the info stays as it was); with the flag set and no loop it says so and builds nothing;
// a checker that cannot be set up leaves the loop as it is, with the reason in the info.
func attachVerifier(ctx context.Context, getenv func(string) string, d *coderhost.PursuitDeps, info *coderhost.PursuitHostInfo, scratch string, forbidden coderhost.ForbiddenFolders) {
	pin := strings.TrimSpace(getenv(envPursuitVerifier))
	if pin == "" {
		return
	}
	info.VerifierPin = pin
	if d == nil {
		if info.OperatorOff {
			info.VerifierReason = envPursuitVerifier + " is set but the operator turned the pursuit loop off (run `coder host pursuit on` and restart the host): no goal check runs"
		} else {
			info.VerifierReason = envPursuitVerifier + " is set but the pursuit loop is not running (choose a judge with `coder host pursuit judge set RUNTIME/MODEL`, and restart the host): no goal check runs"
		}
		return
	}
	vd, vi := verifierBuilder(ctx, pin, scratch, forbidden)
	info.Confinement = vi.Confinement
	if vd == nil {
		info.VerifierReason = vi.Reason
		return
	}
	d.Verifier, info.VerifierOn = vd, true
}

// pursuitFromEnv builds the pursuit deps from the environment and the operator's saved settings, or nil with the info that says
// why the loop is not running. The loop is ON by default: with no judge pin it idles ("judge-pin" missing, nothing built, nothing
// spent) and never picks a model for the person. Order: an explicit operator OFF (env or saved row) beats everything; then the
// pin of MONOAGENT_PURSUIT_JUDGE; then the saved pin. db answers the lead's pin at each gate and each judge call.
func pursuitFromEnv(getenv func(string) string, db *sql.DB) (*coderhost.PursuitDeps, coderhost.PursuitHostInfo) {
	if v := getenv(envPursuitLoop); coderhost.PursuitLoopValueOff(v) {
		return nil, coderhost.PursuitHostInfo{OperatorOff: true, OffSource: "env", Reason: "turned off by the operator (" + envPursuitLoop + " is set and is not on/1/true); unset it (or set it to on) and restart the host to go back to the default"}
	}
	store := coderhost.NewStore(db)
	// a setting that cannot be read fails closed: the loop stays off rather than guess
	if off, err := store.PursuitOperatorOff(); err != nil {
		return nil, coderhost.PursuitHostInfo{OperatorOff: true, OffSource: "saved", Reason: fmt.Sprintf("the operator's pursuit switch could not be read (%v); the pursuit loop stays off", err)}
	} else if off {
		return nil, coderhost.PursuitHostInfo{OperatorOff: true, OffSource: "saved", Reason: "turned off by the operator (`coder host pursuit off`); `coder host pursuit on` and a host restart go back to the default"}
	}
	pin, source := strings.TrimSpace(getenv(envPursuitJudge)), "env"
	if pin == "" {
		saved, err := store.SavedPursuitJudge()
		if err != nil {
			return nil, coderhost.PursuitHostInfo{OperatorOff: true, OffSource: "saved", Reason: fmt.Sprintf("the saved judge could not be read (%v); the pursuit loop stays off", err)}
		}
		pin, source = saved, "saved"
	}
	if pin == "" {
		return nil, coderhost.PursuitHostInfo{Missing: coderhost.PursuitMissingJudgePin, Reason: "judge-pin missing: the pursuit loop is on by default but idle (nothing is judged, nothing is spent) until you choose a judge model that differs from your leads' (for $0 use an OpenRouter ':free' model through the vault's Openrouter key): run `monoagentcli coder host pursuit judge set RUNTIME/MODEL`, then restart the coder host"}
	}
	off := func(format string, a ...any) (*coderhost.PursuitDeps, coderhost.PursuitHostInfo) {
		return nil, coderhost.PursuitHostInfo{Pin: pin, PinSource: source, Reason: fmt.Sprintf(format, a...)}
	}
	wake := coderhost.DefaultWakeCapUSD
	// unset or empty is the default; anything else must be a number (a value of only spaces is not one)
	if raw := getenv(envPursuitWakeCap); raw != "" {
		x, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(x) || x < coderhost.MinWakeCapUSD || x > coderhost.MaxWakeCapUSD {
			return off("%s must be a number from %g to %g (it is %q); the pursuit loop stays off", envPursuitWakeCap, coderhost.MinWakeCapUSD, coderhost.MaxWakeCapUSD, raw)
		}
		wake = x
	}
	model, err := judgemodel.New(judgemodel.Config{Pin: pin})
	if err != nil {
		return off("the judge pin (%s): %v; the pursuit loop stays off", source, err)
	}
	d := &coderhost.PursuitDeps{
		Model:      model,
		Refuse:     func(pin string) bool { return pin == "" }, // the lead's pin is compared per mission, at each call (LeadPin)
		WakeCapUSD: wake,
		LeadPin:    func(profileID, conversationID string) string { return leadPin(db, profileID, conversationID) },
	}
	return d, coderhost.PursuitHostInfo{Enabled: true, Pin: pin, PinSource: source, WakeCapUSD: wake}
}

// leadPin is the runtime/model a conversation runs (coderhost.LeadPin: the host and `pursuit preview` read it alike).
func leadPin(db *sql.DB, profileID, conversationID string) string {
	return coderhost.LeadPin(db, profileID, conversationID)
}

// pursuitStatusLine is the one line `coder host status` prints for the loop.
func pursuitStatusLine(p coderhost.PursuitHostInfo) string {
	line := pursuitLoopLine(p)
	switch {
	case p.VerifierOn:
		line += "\ngoal checks ON  checker " + p.VerifierPin + "  project commands: " + p.Confinement + "  (at most $0.50 per check, 2 per away window, 6 per mission, 4 an hour on this host, one at a time)"
	case p.VerifierReason != "":
		line += "\ngoal checks OFF  " + p.VerifierReason
	case p.VerifierPin != "":
		line += "\ngoal checks OFF  " + p.VerifierPin + " was asked for and is not set up"
	}
	return line
}

func pursuitLoopLine(p coderhost.PursuitHostInfo) string {
	switch {
	case p.Enabled:
		return fmt.Sprintf("pursuit loop ON  judge %s  a wake-up turn is capped at $%.2f (and at what the lead has left)", p.Pin, p.WakeCapUSD)
	case p.Missing != "":
		return "pursuit loop ON (default) but idle  " + p.Reason
	case p.Reason != "":
		return "pursuit loop OFF  " + p.Reason
	}
	return "pursuit loop OFF  (no state published by this host)"
}
