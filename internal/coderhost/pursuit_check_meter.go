package coderhost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// The meter of the goal checks (S7c1b). The check row is the ledger: Begin writes it at state running, and from that moment
// it counts at CheckCapMicro (missionpursuit.CheckSpendAfter), so a crash during the reviewer call is already counted; Finish
// replaces the cap with the believed cost. The meter below therefore writes nothing: Reserve re-reads the run state and the
// host-wide hour, and settle has nothing to do. The check that crosses the 20% share does so by at most its cap, like the last
// judge call (documented in the status text).

type checkSessionKey struct{}

// withCheckSession carries the mission session to the meter: the Checker is one per host, so the session cannot be in it.
func withCheckSession(ctx context.Context, session int64) context.Context {
	return context.WithValue(ctx, checkSessionKey{}, session)
}

// checkMeter implements verifier.Meter over the host.
type checkMeter struct{ h *Host }

var _ verifier.Meter = checkMeter{}

func (m checkMeter) Reserve(ctx context.Context, capUSD float64) (func(int64, bool), error) {
	session, ok := ctx.Value(checkSessionKey{}).(int64)
	if !ok {
		return nil, errors.New("the check names no mission, so its reviewer call is not paid for")
	}
	if usdMicro(capUSD) > missionpursuit.CheckCapMicro {
		return nil, fmt.Errorf("a reviewer call may cost $%.2f at most", float64(missionpursuit.CheckCapMicro)/1e6)
	}
	if !m.h.pursuitRunning(ctx, inputs.New(m.h.Store.db), session) {
		return nil, errors.New("the pursuit does not run any more")
	}
	// The running check is one of the rows counted, so it is within the limit at MaxHostChecksPerHour.
	n, err := missionpursuit.HostChecksSince(ctx, m.h.Store.db, m.h.Store.now().Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if n > missionpursuit.MaxHostChecksPerHour {
		return nil, fmt.Errorf("this host has run %d checks in the last hour, the most is %d", n, missionpursuit.MaxHostChecksPerHour)
	}
	return func(int64, bool) {}, nil
}
