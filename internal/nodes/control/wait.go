package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// inMemoryWaitLimit is the longest wait held in memory. Longer waits pause
// the execution durably (WAITING) and free the queue slot; the engine's
// resume loop re-runs the node once the target time has passed, across
// restarts.
const inMemoryWaitLimit = 60 * time.Second

// WaitNode delays the items passing through it.
// Config fields (one of):
//
//	"duration" (number): seconds to wait, at least 1; "resume" unset or
//	    "duration"
//	"resume": "timeInterval" with "amount" (number ≥ 0) and "unit"
//	    (seconds|minutes|hours|days, default hours)
//	"resume": "specificTime" with "dateTime" (RFC 3339, or local
//	    "2006-01-02T15:04[:05]" / "2006-01-02 15:04[:05]" / "2006-01-02")
type WaitNode struct{}

func (n *WaitNode) Type() string { return "core.wait" }

// waitState records the wait's target so a resumed (re-run) node continues
// the same wait instead of starting a new one.
type waitState struct {
	Until time.Time `json:"until"`
}

func (n *WaitNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	var until time.Time
	if raw := workflow.NodeStateFromContext(ctx); raw != nil {
		var st waitState
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, fmt.Errorf("wait: decode state: %w", err)
		}
		until = st.Until
	} else {
		target, err := waitTarget(config, time.Now())
		if err != nil {
			return nil, err
		}
		until = target
	}

	remaining := time.Until(until)
	if remaining > inMemoryWaitLimit {
		// Pause durably. Without a state slot (outside an execution)
		// nothing would remember the target, so wait in memory instead.
		if err := workflow.SetNodeState(ctx, waitState{Until: until}); err == nil {
			return nil, workflow.PauseFor(remaining, "waiting until "+until.UTC().Format(time.RFC3339))
		}
	}
	if err := sleepFor(ctx, remaining); err != nil {
		return nil, err
	}
	_ = workflow.SetNodeState(ctx, nil)

	return []workflow.NodeOutput{
		{Handle: "main", Items: input.Items},
	}, nil
}

// sleepFor holds a short wait in memory, honouring cancellation, with the
// execution's concurrency slot released while it sleeps.
func sleepFor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	return workflow.YieldSlotWhile(ctx, func() error {
		select {
		case <-ctx.Done():
			return workflow.ErrExecutionCancelled
		case <-time.After(d):
			return nil
		}
	})
}

// waitTarget computes when the wait ends from the node's config.
func waitTarget(config map[string]interface{}, now time.Time) (time.Time, error) {
	resume, _ := config["resume"].(string)
	switch {
	case resume == "specificTime":
		s, _ := config["dateTime"].(string)
		t, ok := parseWaitDateTime(s)
		if !ok {
			return time.Time{}, fmt.Errorf("%w: wait node \"dateTime\" must be a date and time, got %q", workflow.ErrInvalidConfig, s)
		}
		return t, nil
	case resume == "timeInterval":
		amount, err := toFloat(config["amount"])
		if err != nil || amount < 0 {
			return time.Time{}, fmt.Errorf("%w: wait node \"amount\" must be a number of at least 0", workflow.ErrInvalidConfig)
		}
		unit, _ := config["unit"].(string)
		per, ok := map[string]time.Duration{
			"": time.Hour, "seconds": time.Second, "minutes": time.Minute, "hours": time.Hour, "days": 24 * time.Hour,
		}[unit]
		if !ok {
			return time.Time{}, fmt.Errorf("%w: wait node \"unit\" must be seconds, minutes, hours or days, got %q", workflow.ErrInvalidConfig, unit)
		}
		return now.Add(time.Duration(amount * float64(per))), nil
	case resume != "" && resume != "duration":
		return time.Time{}, fmt.Errorf("%w: wait node \"resume\" must be duration, timeInterval or specificTime, got %q", workflow.ErrInvalidConfig, resume)
	}

	rawDuration, ok := config["duration"]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: wait node requires \"duration\"", workflow.ErrInvalidConfig)
	}
	seconds, err := toFloat(rawDuration)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: wait node \"duration\" must be a number, got %T", workflow.ErrInvalidConfig, rawDuration)
	}
	if seconds < 1 {
		return time.Time{}, fmt.Errorf("%w: wait node \"duration\" must be at least 1 second", workflow.ErrInvalidConfig)
	}
	return now.Add(time.Duration(seconds * float64(time.Second))), nil
}

// parseWaitDateTime accepts RFC 3339 or a local date/time without zone.
func parseWaitDateTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
