package coderhost

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// The money rules of switching the loop on (S5b): what one wake-up turn may spend, the helper money the 20% rule counts, and
// the independence of the judge's model.

// errWakeUncapped: a pursuit wake-up is never started without a spend cap, whatever else says it may run.
var errWakeUncapped = errors.New("a pursuit wake-up is never started without a spend cap, and this lead has none")

// errNoPursuitLoop: a pursuit- turn is the loop's own; on a host where the loop is not running nothing starts one.
var errNoPursuitLoop = errors.New("the pursuit loop is not running on this host, so its wake-up does not run")

// validWakeCap says whether x is a usable per-wake-up cap: a number from MinWakeCapUSD (below a cent a cap would round to a
// turn that is charged nothing) to MaxWakeCapUSD.
func validWakeCap(x float64) bool { return !math.IsNaN(x) && x >= MinWakeCapUSD && x <= MaxWakeCapUSD }

// pursuitWakeCap is the cap of a pursuit wake-up turn: the lower of what the lead has left (capUSD) and the host's X. It runs
// only for a turn whose id starts with PursuitTurnPrefix, on the cap TurnCap just read; the cap that results is the one
// recorded before the turn starts and the one the launcher passes to the runner. A lead with no cap, or a host where the loop
// is not ready, gets an error: the turn is not started.
func (h *Host) pursuitWakeCap(capUSD float64, capped bool, err error) (float64, bool, error) {
	switch {
	case err != nil:
		return capUSD, capped, err
	case !h.Pursuit.ready():
		return 0, false, errNoPursuitLoop
	case !capped:
		return 0, false, errWakeUncapped
	}
	return min(capUSD, h.Pursuit.WakeCapUSD), true, nil
}

// helperCarveUnspentMicro is the money the lead has handed to helpers (the chats below it, whatever their depth) that they
// have not spent: budget less spend, per helper, at least 0. It is committed: a helper may spend it all after the gate has
// looked. A helper that ended has given its carve back (its budget is its spend), so it counts nothing. The lead's own
// budget is not a carve.
func (s *Store) helperCarveUnspentMicro(m pursuitMission, tree []string) (int64, error) {
	var sum int64
	for _, conv := range tree {
		if conv == m.Conv {
			continue
		}
		budget, err := s.Budget(m.Profile, conv)
		if err != nil {
			return 0, fmt.Errorf("reading the budget of a helper of the mission: %w", err)
		}
		if !(budget > 0) {
			continue
		}
		spent, err := s.Spent(m.Profile, conv)
		if err != nil {
			return 0, fmt.Errorf("reading the turn spend of a helper of the mission: %w", err)
		}
		if left := usdMicro(budget) - usdMicro(spent); left > 0 {
			sum += left
		}
		if sum < 0 || sum > 1e12*1e6 {
			sum = 1e12 * 1e6
		}
	}
	return sum, nil
}

// pinKey is a pin without its version, trimmed and in lower case: "runtime/model@1", "Runtime/Model" and "runtime/model" are the
// same name for the independence rule. It compares NAMES: two names for one model (a provider alias, "opus" and
// "claude-opus-4-5") are not recognised, so the rule is not a guarantee of different weights.
func pinKey(p string) string {
	k, _, _ := strings.Cut(strings.TrimSpace(p), "@")
	return strings.ToLower(strings.TrimSpace(k))
}

// pursuitPinWhy says why the judge may not judge this mission, or "": its pin is empty, the deployment's rule refuses it, or it
// equals the pin of the lead's conversation, read now. A lead whose model is not known is not shown to differ, so it waits too.
func (h *Host) pursuitPinWhy(m pursuitMission) string {
	d := h.Pursuit
	if !d.ready() {
		return "the pursuit loop is not ready"
	}
	return pinWhy(d, d.Model.Pin(), m)
}

func pinWhy(d *PursuitDeps, pin string, m pursuitMission) string {
	if pinKey(pin) == "" || d.Refuse(pin) {
		return "the judge's model is not allowed to judge"
	}
	return LeadPinWhy(pin, d.LeadPin(m.Profile, m.Conv))
}

// LeadPinWhy is the independence rule of the judge, alone: why a judge with this pin may not judge a lead with that pin, or "".
// A lead whose model is not known is not shown to differ. The host and `pursuit preview` both ask this.
func LeadPinWhy(judgePin, leadPin string) string {
	switch lead := pinKey(leadPin); {
	case pinKey(judgePin) == "":
		return "the judge's model is not allowed to judge"
	case lead == "":
		return "the lead's model is not known, so the judge cannot be shown to differ from it"
	case lead == pinKey(judgePin):
		return "the judge's model is the lead's"
	}
	return ""
}

// pursuitRefuse is the independence rule of one mission's judge, evaluated at each call: the lead's pin is read from its
// conversation now, not when the host started.
func (h *Host) pursuitRefuse(m pursuitMission) func(pin string) bool {
	d := h.Pursuit
	return func(pin string) bool { return !d.ready() || pinWhy(d, pin, m) != "" }
}
