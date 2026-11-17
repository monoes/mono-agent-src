package health

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func pausedEnv(info SchedulesPausedInfo, err error) *Env {
	return &Env{SchedulesPaused: func(context.Context) (SchedulesPausedInfo, error) { return info, err }}
}

func TestSchedulesPausedRow(t *testing.T) {
	res := checkSchedulesPaused(context.Background(), pausedEnv(SchedulesPausedInfo{Workflows: 2, Paused: true, Reason: "not_logged_in"}, nil))
	if res.Status != StatusWarn {
		t.Fatalf("signed out with schedules: %+v; want a warning", res)
	}
	for _, want := range []string{"paused", "sign-in", "2"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("summary %q lacks %q", res.Summary, want)
		}
	}
	for _, want := range []string{"monoagentcli account login", "missed", "not made up"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("detail %q lacks %q", res.Detail, want)
		}
	}
	expired := checkSchedulesPaused(context.Background(), pausedEnv(SchedulesPausedInfo{Workflows: 1, Paused: true, Reason: "expired"}, nil))
	if !strings.Contains(expired.Summary, "expired") {
		t.Errorf("expired login not named: %q", expired.Summary)
	}

	// Absent: signed in, no schedule, no hook, or nothing readable.
	for name, env := range map[string]*Env{
		"signed in":    pausedEnv(SchedulesPausedInfo{Workflows: 2}, nil),
		"no schedules": pausedEnv(SchedulesPausedInfo{Paused: true, Reason: "not_logged_in"}, nil),
		"no hook":      {},
	} {
		if res := checkSchedulesPaused(context.Background(), env); res.Status != StatusSkip || res.Summary != "" {
			t.Errorf("%s: %+v; want a silent skip", name, res)
		}
	}
	if res := checkSchedulesPaused(context.Background(), pausedEnv(SchedulesPausedInfo{}, errors.New("db busy"))); res.Status != StatusWarn {
		t.Errorf("unreadable: %+v; want a warning", res)
	}
}

// The row is advice: registered, never required, never a failure.
func TestSchedulesPausedIsRegisteredAndNeverFails(t *testing.T) {
	for _, c := range Default().Checks() {
		if c.ID == CheckSchedulesPaused {
			if c.Required {
				t.Fatal("the row is required")
			}
			for _, info := range []SchedulesPausedInfo{{Workflows: 1, Paused: true, Reason: "refused"}, {Workflows: 1}, {}} {
				if res := c.Run(context.Background(), pausedEnv(info, nil)); res.Status == StatusFail {
					t.Fatalf("%+v failed: %+v", info, res)
				}
			}
			return
		}
	}
	t.Fatal("the row is not in the registry")
}
