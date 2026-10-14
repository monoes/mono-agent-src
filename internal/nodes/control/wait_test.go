package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestWait_ShortWaitStaysInMemory(t *testing.T) {
	ctx := workflow.WithNodeState(context.Background(), nil)
	in := workflow.NodeInput{Items: numItems(2)}
	start := time.Now()
	outs, err := (&WaitNode{}).Execute(ctx, in, map[string]interface{}{"duration": 1})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Error("returned before the wait elapsed")
	}
	if len(outs) != 1 || len(outs[0].Items) != 2 {
		t.Errorf("outputs = %+v, want the 2 input items", outs)
	}
}

// A long wait pauses durably: it records its target time and returns a
// PauseError instead of holding the run.
func TestWait_LongWaitPausesDurably(t *testing.T) {
	ctx := workflow.WithNodeState(context.Background(), nil)
	_, err := (&WaitNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{
		"resume": "timeInterval", "amount": 2, "unit": "days",
	})
	var pe *workflow.PauseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want PauseError", err)
	}
	if pe.ResumeAfter < 47*time.Hour || pe.ResumeAfter > 48*time.Hour {
		t.Errorf("ResumeAfter = %v, want ~48h", pe.ResumeAfter)
	}
	if workflow.NodeStateFromContext(ctx) == nil {
		t.Error("wait did not record its target time")
	}
}

// On resume the node finds its recorded target in the past and continues.
func TestWait_ResumeAfterTargetPasses(t *testing.T) {
	st, _ := json.Marshal(map[string]interface{}{"until": time.Now().Add(-time.Second)})
	ctx := workflow.WithNodeState(context.Background(), st)
	outs, err := (&WaitNode{}).Execute(ctx, workflow.NodeInput{Items: numItems(1)}, map[string]interface{}{"duration": 7200})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(outs) != 1 || len(outs[0].Items) != 1 {
		t.Errorf("outputs = %+v", outs)
	}
	if workflow.NodeStateFromContext(ctx) != nil {
		t.Error("state should be cleared once the wait completes")
	}
}

// An early resume (before the target) pauses again for the remaining time
// rather than restarting the full wait.
func TestWait_EarlyResumeKeepsTarget(t *testing.T) {
	until := time.Now().Add(3 * time.Hour)
	st, _ := json.Marshal(map[string]interface{}{"until": until})
	ctx := workflow.WithNodeState(context.Background(), st)
	_, err := (&WaitNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"duration": 7200})
	var pe *workflow.PauseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want PauseError", err)
	}
	if pe.ResumeAfter < 2*time.Hour+59*time.Minute {
		t.Errorf("ResumeAfter = %v, want ~3h (the recorded target)", pe.ResumeAfter)
	}
}

func TestWait_SpecificTime(t *testing.T) {
	ctx := workflow.WithNodeState(context.Background(), nil)
	target := time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339)
	_, err := (&WaitNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"resume": "specificTime", "dateTime": target})
	var pe *workflow.PauseError
	if !errors.As(err, &pe) || pe.ResumeAfter < 4*time.Hour {
		t.Fatalf("err = %v, want a ~5h PauseError", err)
	}
	// A time already in the past continues at once.
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := (&WaitNode{}).Execute(workflow.WithNodeState(context.Background(), nil), workflow.NodeInput{}, map[string]interface{}{"resume": "specificTime", "dateTime": past}); err != nil {
		t.Errorf("past dateTime: %v", err)
	}
}

func TestWait_ConfigErrors(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"missing":         {},
		"negative":        {"duration": -1},
		"bad unit":        {"resume": "timeInterval", "amount": 1, "unit": "weeks"},
		"bad dateTime":    {"resume": "specificTime", "dateTime": "soon"},
		"webhook resume":  {"resume": "webhook"},
		"non-number":      {"duration": "ten"},
		"negative amount": {"amount": -2, "unit": "seconds"},
	}
	for name, cfg := range cases {
		_, err := (&WaitNode{}).Execute(workflow.WithNodeState(context.Background(), nil), workflow.NodeInput{}, cfg)
		if !errors.Is(err, workflow.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// Without an engine-provided state slot the node cannot pause durably and
// falls back to waiting in memory (honouring cancellation).
func TestWait_NoStateSlotWaitsInMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := (&WaitNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"duration": 7200})
	if !errors.Is(err, workflow.ErrExecutionCancelled) {
		t.Errorf("err = %v, want ErrExecutionCancelled", err)
	}
}

// resume "duration" (the inspector's default) is the same as no resume.
func TestWait_ResumeDurationUsesDuration(t *testing.T) {
	now := time.Now()
	for _, cfg := range []map[string]interface{}{
		{"duration": 30},
		{"resume": "duration", "duration": 30},
		{"resume": "", "duration": 30},
	} {
		got, err := waitTarget(cfg, now)
		if err != nil || !got.Equal(now.Add(30*time.Second)) {
			t.Errorf("%v: target = %v, %v; want now+30s", cfg, got.Sub(now), err)
		}
	}
}
