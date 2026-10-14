package workflow

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ExecutionTimeoutSetting is the Workflow.Settings key (n8n's name) bounding
// how long a run may be active, in seconds: a number or a numeric string.
// Unset, zero, negative or unparsable means no timeout. Only active running
// time counts: time spent WAITING (a Human-in-Loop approval, a durable
// wait) does not, and each resumed segment gets what earlier segments left.
const ExecutionTimeoutSetting = "executionTimeout"

// executionTimeout reads the workflow's executionTimeout setting; 0 = none.
func executionTimeout(wf *Workflow) time.Duration {
	if wf == nil {
		return 0
	}
	var secs float64
	switch v := wf.Settings[ExecutionTimeoutSetting].(type) {
	case float64:
		secs = v
	case int:
		secs = float64(v)
	case int64:
		secs = float64(v)
	case string:
		secs, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// executionTimeoutError ends a run whose executionTimeout ran out. It is a
// cancel (the run ends CANCELLED) and a timeout (error class timeout).
type executionTimeoutError struct{ limit time.Duration }

func (e executionTimeoutError) Error() string {
	return "execution timeout after " + strconv.FormatFloat(e.limit.Seconds(), 'f', -1, 64) + "s"
}

func (executionTimeoutError) Unwrap() []error {
	return []error{ErrExecutionCancelled, ErrExecutionTimeout}
}

// withExecutionTimeout bounds ctx by what is left of the workflow's
// executionTimeout after the active time earlier segments used. The returned
// cancel must be called; with no timeout ctx is returned as is.
func (r *runner) withExecutionTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	r.segmentStart = time.Now()
	limit := executionTimeout(r.wf)
	if limit <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeoutCause(ctx, limit-r.activeBefore, executionTimeoutError{limit: limit})
}

// activeTime is the run's active time so far, over every segment.
func (r *runner) activeTime() time.Duration {
	return r.activeBefore + time.Since(r.segmentStart)
}

// timeoutErr turns the error a timed-out run stopped with into the timeout
// itself; any other outcome (success, a pause) is returned unchanged.
func (r *runner) timeoutErr(err error) error {
	var te executionTimeoutError
	if err == nil || errors.Is(err, ErrExecutionPaused) || !errors.As(context.Cause(r.ctx), &te) {
		return err
	}
	return te
}
