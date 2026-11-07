package coderhost

// The question policy of a mission in pursuit (T6). While the person is away the pursuit must never act on a guess: every
// question goes to the person and parks until they answer, whatever the preset or a per-class override says, for every
// class (the soft ones too: ambiguity, scope growth, a failed check). Nothing answers it for them and no default is
// taken when it waits, so the wait is bounded by AwayState, not by a timeout.

// PursuitClamp is the decision of a mission in pursuit, whatever the settings decided: the person answers, and an
// unanswered question parks. Decide applies it when the mission's pursuit is started (Settings.pursuing).
func PursuitClamp(Decision) Decision { return Decision{Answerer: WhoUser, Fallback: FallbackPark} }

// AwayState is what a pursuit counts while the person is away: the questions parked for them and the money spent since
// their last action. The pursuit stops opening new work when WaitingForYou. Pure data: the host feeds it from rows.
type AwayState struct {
	ParkedMax   int   // stop at this many parked questions (1..20)
	Pct         int   // or at this percent of the budget spent (1..100)
	BudgetMicro int64 // the budget the share is measured against, in micro-USD; 0 = unknown
	Parked      int   // questions parked since the person last acted
	SpentMicro  int64 // spent since the person last acted, in micro-USD
}

// Park counts one more question parked for the person.
func (a *AwayState) Park() { a.Parked++ }

// Spend adds money spent. A negative amount never lowers the count.
func (a *AwayState) Spend(micro int64) {
	if micro > 0 {
		a.SpentMicro += micro
	}
}

// Reset is any action of the person: the counters start again (the limits and the budget stay).
func (a *AwayState) Reset() { a.Parked, a.SpentMicro = 0, 0 }

// WaitingForYou says the pursuit must stop opening new work until the person acts: the parked questions reached the limit,
// or the spend reached its share of the budget. Limits that were never set (or are out of range) wait at once, and money
// spent against an unknown budget waits too: the check fails closed.
func (a AwayState) WaitingForYou() bool {
	if a.ParkedMax < 1 || a.ParkedMax > 20 || a.Pct < 1 || a.Pct > 100 {
		return true
	}
	if a.Parked >= a.ParkedMax {
		return true
	}
	spent := a.SpentMicro
	if spent <= 0 {
		return false
	}
	if a.BudgetMicro <= 0 {
		return true
	}
	if spent >= a.BudgetMicro {
		return true
	}
	// spent < budget: spent*100 can wrap only for a budget above 9e16 micro-USD, which is split first.
	if a.BudgetMicro > 1<<56 {
		return spent/(a.BudgetMicro/100) >= int64(a.Pct)
	}
	return spent*100 >= a.BudgetMicro*int64(a.Pct)
}
