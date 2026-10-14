package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// executeWithRetry executes a node, retrying on failure according to the
// supplied RetryPolicy.  Panics from the executor are caught and returned
// as errors so that DB persistence in the caller is not bypassed.
//
// Errors a retry cannot fix (isNonRetryable) are returned after one attempt.
// Other errors are retried per policy, consulting RetryClassifier (when
// installed) before each retry: it may stop retrying or override the delay.
//
// An opt-in node_timeout (seconds) bounds each attempt; an attempt it stops
// fails with an error matching ErrNodeTimeout. A node that returns a
// *PartialItemsError has only its failed, retryable items run again; see
// PartialItemsError for what is returned when some still fail.
func executeWithRetry(
	ctx context.Context,
	executor NodeExecutor,
	input NodeInput,
	config map[string]interface{},
	policy RetryPolicy,
	observers ...func([]NodeOutput),
) (outputs []NodeOutput, err error) {
	maxRetries := policy.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 10 {
		maxRetries = 10
	}
	timeout := extractNodeTimeout(config)
	items := &itemRetry{orig: input}

	var lastErr error
	var override time.Duration // classifier-chosen delay for the next retry
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := computeDelay(attempt, policy)
			if override > 0 {
				delay = override
			}
			// Waiting out the back-off is not work: yield the concurrency
			// slot so other executions run meanwhile.
			if YieldSlotWhile(ctx, func() error { return retrySleep(ctx, delay) }) != nil || ctx.Err() != nil {
				return nil, ErrExecutionCancelled
			}
		}

		outputs, err = runAttempt(ctx, executor, items.input(), config, timeout)
		var partial *PartialItemsError
		if err != nil && errors.As(err, &partial) && outputs == nil {
			outputs = partial.Succeeded
		}
		for _, observer := range observers {
			observer(outputs)
		}
		// Items on the "error" output are a failed attempt too, as in n8n,
		// where a node retries before it routes to its error output: retry
		// while attempts are left, then route the last attempt's items.
		routed := false
		if err == nil {
			msg, failed := errorOutputFailure(outputs)
			if !failed || attempt == maxRetries {
				return items.succeed(outputs)
			}
			err, routed = errors.New(msg), true
		}
		lastErr = err

		// Do not retry if the context is done.
		select {
		case <-ctx.Done():
			return nil, ErrExecutionCancelled
		default:
		}

		switch {
		case partial != nil && items.absorb(outputs, partial):
		case items.partial:
			// The retried items failed as a whole.
			if errors.Is(err, ErrNodePaused) {
				return nil, err
			}
			items.failAll(err)
		case isNonRetryable(err):
			// A pause, invalid config, cancellation or Permanent error cannot
			// be fixed by running the node again: return it as-is (the caller
			// relies on errors.Is(err, ErrNodePaused) to suspend the run).
			return nil, err
		}
		if items.partial && len(items.remaining) == 0 {
			return items.result()
		}
		if attempt == maxRetries {
			break // no retry left: never consult the classifier for nothing
		}
		retry, delay := classifyRetry(ctx, executor.Type(), err, attempt+1, config)
		if !retry {
			if routed {
				return items.succeed(outputs)
			}
			break
		}
		override = delay
	}
	if items.partial {
		return items.result()
	}
	return nil, lastErr
}

// errorOutputFailure reports whether an attempt routed items to the "error"
// output, with the message the node's failure would carry.
func errorOutputFailure(outputs []NodeOutput) (string, bool) {
	for _, out := range outputs {
		if out.Handle == "error" && len(out.Items) > 0 {
			msg := errorHandleMessage(out.Items)
			if msg == "" {
				msg = fmt.Sprintf("routed %d item(s) to the 'error' output", len(out.Items))
			}
			return msg, true
		}
	}
	return "", false
}

// runAttempt runs one attempt of the node, bounded by timeout when > 0.
func runAttempt(ctx context.Context, executor NodeExecutor, input NodeInput, config map[string]interface{}, timeout time.Duration) (out []NodeOutput, execErr error) {
	actx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	defer func() {
		if r := recover(); r != nil {
			execErr = fmt.Errorf("node executor panicked: %v\n%s", r, debug.Stack())
		}
	}()
	if gout, handled, gerr := RunExecGuard(actx, executor.Type(), input, config); handled {
		out, execErr = gout, gerr
	} else {
		out, execErr = executor.Execute(actx, input, config)
	}
	if execErr != nil && timeout > 0 && ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		execErr = &nodeTimeoutError{after: timeout, err: execErr}
	}
	return out, execErr
}

// itemRetry tracks which input items still need a retry once a node has
// reported a *PartialItemsError. Until then (partial == false) every attempt
// runs the whole input, exactly as before.
type itemRetry struct {
	orig      NodeInput
	partial   bool
	remaining []int         // original indexes the next attempt runs
	pending   map[int]error // last error of each remaining item
	failed    map[int]error // items that will not be retried, by original index
	merged    []NodeOutput  // successful outputs of all attempts so far
}

// input is the node input for the next attempt: the remaining items, each
// with the idempotency key of its original position.
func (r *itemRetry) input() NodeInput {
	if !r.partial {
		return r.orig
	}
	in := r.orig
	in.Items = make([]Item, len(r.remaining))
	in.ItemKeys = make([]string, len(r.remaining))
	for i, oi := range r.remaining {
		in.Items[i] = r.orig.Items[oi]
		in.ItemKeys[i] = r.orig.ItemKey(oi)
	}
	return in
}

// absorb records an attempt's per-item outcome. It reports false, changing
// nothing, when pe names no item of that attempt's input (the error is then
// handled like any other).
func (r *itemRetry) absorb(outputs []NodeOutput, pe *PartialItemsError) bool {
	n := len(r.orig.Items)
	if r.partial {
		n = len(r.remaining)
	}
	orig := func(i int) int {
		if r.partial {
			return r.remaining[i]
		}
		return i
	}
	pending := map[int]error{}
	final := map[int]error{}
	for k, i := range pe.FailedIndexes {
		if i < 0 || i >= n {
			continue
		}
		if e := pe.itemErr(k); isNonRetryable(e) {
			final[orig(i)] = e
		} else {
			pending[orig(i)] = e
		}
	}
	if len(pending)+len(final) == 0 {
		return false
	}
	if r.failed == nil {
		r.failed = map[int]error{}
	}
	for i, e := range final {
		r.failed[i] = e
	}
	r.partial = true
	r.merged = mergeOutputs(r.merged, outputs)
	r.setPending(pending)
	return true
}

// failAll applies an attempt-wide error to every item that attempt ran.
func (r *itemRetry) failAll(err error) {
	pending := map[int]error{}
	for _, i := range r.remaining {
		if isNonRetryable(err) {
			r.failed[i] = err
		} else {
			pending[i] = err
		}
	}
	r.setPending(pending)
}

func (r *itemRetry) setPending(pending map[int]error) {
	r.pending = pending
	r.remaining = r.remaining[:0:0]
	for i := range pending {
		r.remaining = append(r.remaining, i)
	}
	sort.Ints(r.remaining)
}

// succeed finishes after an attempt in which every item it ran worked.
func (r *itemRetry) succeed(outputs []NodeOutput) ([]NodeOutput, error) {
	if !r.partial {
		return outputs, nil
	}
	r.merged = mergeOutputs(r.merged, outputs)
	r.setPending(nil)
	if len(r.failed) == 0 {
		return r.merged, nil
	}
	return r.result()
}

// result returns the merged successes and a PartialItemsError naming every
// item that still failed, by its index in the original input.
func (r *itemRetry) result() ([]NodeOutput, error) {
	all := make(map[int]error, len(r.failed)+len(r.pending))
	for i, e := range r.failed {
		all[i] = e
	}
	for i, e := range r.pending {
		all[i] = e
	}
	pe := &PartialItemsError{Succeeded: r.merged}
	for i := range all {
		pe.FailedIndexes = append(pe.FailedIndexes, i)
	}
	sort.Ints(pe.FailedIndexes)
	for _, i := range pe.FailedIndexes {
		pe.Errs = append(pe.Errs, all[i])
	}
	return r.merged, pe
}

// mergeOutputs appends add's items to base, per handle, keeping the order in
// which handles first appear.
func mergeOutputs(base, add []NodeOutput) []NodeOutput {
	for _, o := range add {
		found := false
		for j := range base {
			if base[j].Handle == o.Handle {
				base[j].Items = append(base[j].Items, o.Items...)
				found = true
				break
			}
		}
		if !found {
			base = append(base, NodeOutput{Handle: o.Handle, Items: append([]Item(nil), o.Items...)})
		}
	}
	return base
}

// computeDelay returns the sleep duration before the given retry attempt
// (attempt is 1-based: first retry is attempt=1).
func computeDelay(attempt int, p RetryPolicy) time.Duration {
	const maxDelaySeconds = 3600.0

	var seconds float64
	switch strings.ToLower(p.BackoffType) {
	case "exponential":
		// p.InitialDelay * 2^(attempt-1)
		seconds = p.InitialDelay * math.Pow(2, float64(attempt-1))
	default: // "fixed" or anything else
		seconds = p.InitialDelay
	}

	if seconds > maxDelaySeconds {
		seconds = maxDelaySeconds
	}
	if seconds < 0 {
		seconds = 0
	}

	return time.Duration(seconds * float64(time.Second))
}

// extractRetryPolicy reads RetryPolicy fields from the resolved config map.
// Defaults: MaxRetries=0, BackoffType="fixed", InitialDelay=1.
func extractRetryPolicy(config map[string]interface{}) RetryPolicy {
	p := RetryPolicy{
		MaxRetries:   0,
		BackoffType:  "fixed",
		InitialDelay: 1,
	}

	rpRaw, ok := config["retry_policy"]
	if !ok {
		return p
	}

	// Try to round-trip through JSON to get a clean RetryPolicy struct.
	b, err := json.Marshal(rpRaw)
	if err != nil {
		return p
	}
	var parsed RetryPolicy
	if err := json.Unmarshal(b, &parsed); err != nil {
		return p
	}
	if parsed.MaxRetries > 0 {
		p.MaxRetries = parsed.MaxRetries
	}
	if parsed.BackoffType != "" {
		p.BackoffType = parsed.BackoffType
	}
	if parsed.InitialDelay > 0 {
		p.InitialDelay = parsed.InitialDelay
	}
	return p
}

// extractNodeTimeout reads the opt-in per-attempt node_timeout (seconds) from
// the resolved config. Unset, zero, negative or unparsable means no timeout.
func extractNodeTimeout(config map[string]interface{}) time.Duration {
	var seconds float64
	switch v := config["node_timeout"].(type) {
	case float64:
		seconds = v
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	case json.Number:
		seconds, _ = v.Float64()
	case string:
		seconds, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	// Past ~292 years a Duration overflows: that is no timeout either.
	if seconds <= 0 || math.IsNaN(seconds) || seconds > math.MaxInt64/float64(time.Second) {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}
