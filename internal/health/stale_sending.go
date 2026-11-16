package health

import (
	"context"
	"fmt"
	"time"
)

// CheckStaleSending is the row that shows messages left in 'sending' for over an hour (a crash or a failed
// status write). It is advice only: it never changes a message, because the send may still be going out.
const CheckStaleSending = "comms.stale_sending"

// StaleSendingInfo is what the hook read: how many messages have been 'sending' too long, and the oldest age.
type StaleSendingInfo struct {
	Count  int
	Oldest time.Duration
}

func staleSendingChecks() []Check {
	return []Check{{ID: CheckStaleSending, Group: GroupCore, Title: "Messages stuck sending",
		Features: []string{"outbound messages"}, Run: checkStaleSending}}
}

func checkStaleSending(ctx context.Context, env *Env) Result {
	if env.StaleSending == nil {
		return Result{Status: StatusSkip}
	}
	info, err := env.StaleSending(ctx)
	if err != nil {
		return Result{Status: StatusWarn, Summary: "cannot tell whether a message is stuck sending", Detail: err.Error()}
	}
	if info.Count == 0 {
		return Result{Status: StatusSkip} // nothing stuck: the row stays out of the report
	}
	return Result{Status: StatusWarn,
		Summary: fmt.Sprintf("%d message(s) have been 'sending' for over 1h (the oldest for %s)", info.Count, info.Oldest.Round(time.Minute)),
		Detail: "a message has been 'sending' for over 1h: check the platform, then mark it failed or sent by hand.\n" +
			"Nothing was changed: the send may still have gone out, so it is never retried or failed automatically."}
}
